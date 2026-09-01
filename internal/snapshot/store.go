package snapshot

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/pkg/models"
)

const schema = `
CREATE TABLE IF NOT EXISTS schema_version (
	version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS snapshots (
	id         INTEGER PRIMARY KEY AUTOINCREMENT,
	name       TEXT    NOT NULL DEFAULT '',
	data       TEXT    NOT NULL,
	source     TEXT    NOT NULL DEFAULT 'manual',
	hostname   TEXT    NOT NULL DEFAULT '',
	created_at TEXT    NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_snapshots_created ON snapshots(created_at);
CREATE INDEX IF NOT EXISTS idx_snapshots_name ON snapshots(name);
`

const currentSchemaVersion = 2

type Store struct {
	db *sql.DB
}

func Open() (*Store, error) {
	dataDir, err := config.DataDir()
	if err != nil {
		return nil, err
	}

	// Ensure data directory has restricted permissions
	os.MkdirAll(dataDir, 0700)
	os.Chmod(dataDir, 0700)

	dbPath := filepath.Join(dataDir, "thaw.db")
	return openPath(dbPath)
}

// sqliteHeader is the fixed 16-byte magic string at the start of every
// valid, non-empty SQLite database file.
const sqliteHeader = "SQLite format 3\x00"

// openPath opens a database at a specific path. Used by Open() and tests.
//
// Before touching an existing file, it runs a cheap corruption check (file
// header + a quick PRAGMA) so a corrupted database is never silently
// reinitialized as empty. If corruption is found, the file is preserved as
// a timestamped backup and a clear error is returned instead of proceeding.
func openPath(dbPath string) (*Store, error) {
	if err := checkExistingFile(dbPath); err != nil {
		return nil, err
	}

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("opening database: %w", err)
	}

	if _, err := db.Exec(schema); err != nil {
		db.Close()
		if backupErr := backupCorruptedFile(dbPath); backupErr != nil {
			return nil, fmt.Errorf("database appears corrupted (%v); additionally failed to back it up: %w", err, backupErr)
		}
		return nil, corruptionError(dbPath, err)
	}

	// Cheap sanity check on an existing, non-empty database: PRAGMA
	// quick_check validates page structure without the full (and
	// potentially expensive on a large DB) cross-index verification that
	// PRAGMA integrity_check performs. Only run it for pre-existing
	// databases — a freshly created one has nothing to check.
	if err := quickCheck(db); err != nil {
		db.Close()
		if backupErr := backupCorruptedFile(dbPath); backupErr != nil {
			return nil, fmt.Errorf("database appears corrupted (%v); additionally failed to back it up: %w", err, backupErr)
		}
		return nil, corruptionError(dbPath, err)
	}

	os.Chmod(dbPath, 0600)
	os.Chmod(dbPath+"-wal", 0600)
	os.Chmod(dbPath+"-shm", 0600)

	store := &Store{db: db}
	if err := store.migrateSchema(); err != nil {
		db.Close()
		return nil, fmt.Errorf("schema migration: %w", err)
	}

	return store, nil
}

// checkExistingFile does a cheap, pre-open check on a database file that
// already exists on disk: a zero-byte file or a file whose first bytes
// don't match the SQLite magic header is corrupted (or was truncated by a
// crash/disk-full event) and must not be silently treated as "new".
// A file that doesn't exist yet is fine — that's the normal first-run case.
func checkExistingFile(dbPath string) error {
	info, err := os.Stat(dbPath)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return nil // Can't stat it; let sql.Open surface the real error.
	}

	if info.Size() == 0 {
		if backupErr := backupCorruptedFile(dbPath); backupErr != nil {
			return fmt.Errorf("database file is empty (0 bytes), which would normally be silently reinitialized, destroying snapshot history; additionally failed to back it up: %w", backupErr)
		}
		return corruptionError(dbPath, fmt.Errorf("database file is empty (0 bytes)"))
	}

	if info.Size() < int64(len(sqliteHeader)) {
		if backupErr := backupCorruptedFile(dbPath); backupErr != nil {
			return fmt.Errorf("database file is too small to be a valid SQLite database; additionally failed to back it up: %w", backupErr)
		}
		return corruptionError(dbPath, fmt.Errorf("database file is too small (%d bytes) to be a valid SQLite database", info.Size()))
	}

	f, err := os.Open(dbPath)
	if err != nil {
		return nil // Let sql.Open surface the real error.
	}
	defer f.Close()

	header := make([]byte, len(sqliteHeader))
	if _, err := f.Read(header); err != nil {
		return nil
	}
	if string(header) != sqliteHeader {
		if backupErr := backupCorruptedFile(dbPath); backupErr != nil {
			return fmt.Errorf("database file does not have a valid SQLite header; additionally failed to back it up: %w", backupErr)
		}
		return corruptionError(dbPath, fmt.Errorf("database file does not have a valid SQLite header"))
	}

	return nil
}

// quickCheck runs PRAGMA quick_check, a cheaper alternative to
// PRAGMA integrity_check that validates page structure without the full
// cross-index verification. Used as a fast on-open sanity check; the full
// IntegrityCheck remains available via `thaw doctor` for deeper diagnosis.
func quickCheck(db *sql.DB) error {
	var result string
	if err := db.QueryRow("PRAGMA quick_check").Scan(&result); err != nil {
		return fmt.Errorf("quick_check failed: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("quick_check: %s", result)
	}
	return nil
}

// backupCorruptedFile copies a corrupted database file to a sibling
// ".corrupted-<timestamp>" file so no data is lost, before the caller
// refuses to open it.
func backupCorruptedFile(dbPath string) error {
	data, err := os.ReadFile(dbPath)
	if err != nil {
		return fmt.Errorf("reading corrupted file: %w", err)
	}
	backupPath := fmt.Sprintf("%s.corrupted-%s", dbPath, time.Now().Format("20060102-150405"))
	if err := os.WriteFile(backupPath, data, 0600); err != nil {
		return fmt.Errorf("writing backup: %w", err)
	}
	return nil
}

// corruptionError builds the user-facing error shown when a corrupted
// database is detected and a backup has been preserved.
func corruptionError(dbPath string, cause error) error {
	return fmt.Errorf(
		"database appears corrupted: %w\n\n"+
			"A backup of the corrupted file was saved alongside it (%s.corrupted-<timestamp>).\n"+
			"No snapshot history was destroyed. Run `thaw doctor` for next steps",
		cause, dbPath,
	)
}

func (s *Store) migrateSchema() error {
	var version int
	err := s.db.QueryRow("SELECT version FROM schema_version LIMIT 1").Scan(&version)
	if err != nil {
		// First run or table empty — initialize
		s.db.Exec("INSERT INTO schema_version (version) VALUES (?)", currentSchemaVersion)
		return nil
	}

	if version >= currentSchemaVersion {
		return nil
	}

	// Run migrations for each version step
	// v1 → v2: added name column (already in schema, this is for future use)
	// Future migrations go here as: if version < 3 { ... }

	_, err = s.db.Exec("UPDATE schema_version SET version = ?", currentSchemaVersion)
	return err
}

func (s *Store) Close() error { return s.db.Close() }

// Save persists a snapshot. If snap.Name is set, it's a named workspace.
// snapshotData is the serialized format stored in the data column.
// All snapshot metadata lives here — sessions, notes, clipboard, browser, hash chain.
type snapshotData struct {
	Sessions    []models.Session `json:"sessions"`
	Notes       []string         `json:"notes,omitempty"`
	Clipboard   string           `json:"clipboard,omitempty"`
	BrowserTabs []string         `json:"browser_tabs,omitempty"`
	PrevHash    string           `json:"prev_hash,omitempty"`
	Hash        string           `json:"hash,omitempty"`
	Intent      string           `json:"intent,omitempty"`
}

func (s *Store) Save(snap *models.Snapshot) error {
	sd := snapshotData{
		Sessions:    snap.Sessions,
		Notes:       snap.Notes,
		Clipboard:   snap.Clipboard,
		BrowserTabs: snap.BrowserTabs,
		PrevHash:    snap.PrevHash,
		Hash:        snap.Hash,
		Intent:      snap.Intent,
	}
	data, err := json.Marshal(sd)
	if err != nil {
		return fmt.Errorf("marshaling snapshot: %w", err)
	}
	result, err := s.db.Exec(
		"INSERT INTO snapshots (name, data, source, hostname, created_at) VALUES (?, ?, ?, ?, ?)",
		snap.Name, string(data), snap.Source, snap.Hostname, snap.CreatedAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("inserting snapshot: %w", err)
	}
	id, _ := result.LastInsertId()
	snap.ID = int(id)
	return nil
}

// Latest returns the most recent unnamed snapshot.
func (s *Store) Latest() (*models.Snapshot, error) {
	return s.getOne("SELECT id, name, data, source, hostname, created_at FROM snapshots WHERE name = '' ORDER BY id DESC LIMIT 1")
}

// Get returns a snapshot by ID.
func (s *Store) Get(id int) (*models.Snapshot, error) {
	return s.getOne("SELECT id, name, data, source, hostname, created_at FROM snapshots WHERE id = ?", id)
}

// GetNamed returns the most recent snapshot with the given workspace name.
func (s *Store) GetNamed(name string) (*models.Snapshot, error) {
	return s.getOne("SELECT id, name, data, source, hostname, created_at FROM snapshots WHERE name = ? ORDER BY id DESC LIMIT 1", name)
}

func (s *Store) getOne(query string, args ...any) (*models.Snapshot, error) {
	row := s.db.QueryRow(query, args...)
	var snap models.Snapshot
	var data, createdStr string
	err := row.Scan(&snap.ID, &snap.Name, &data, &snap.Source, &snap.Hostname, &createdStr)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("scanning snapshot: %w", err)
	}

	// Try wrapper format first (v3+), fall back to raw session array (v2 compat)
	var sd snapshotData
	if err := json.Unmarshal([]byte(data), &sd); err == nil && len(sd.Sessions) > 0 {
		snap.Sessions = sd.Sessions
		snap.Notes = sd.Notes
		snap.Clipboard = sd.Clipboard
		snap.BrowserTabs = sd.BrowserTabs
		snap.PrevHash = sd.PrevHash
		snap.Hash = sd.Hash
		snap.Intent = sd.Intent
	} else {
		// Backward compat: raw []Session from older versions
		json.Unmarshal([]byte(data), &snap.Sessions)
	}

	snap.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
	return &snap, nil
}

type SnapshotSummary struct {
	ID           int
	Name         string
	SessionCount int
	Source       string
	Hostname     string
	CreatedAt    time.Time
	Projects     []string // distinct project dir names across this snapshot's sessions, for display when Name is blank
	Intent       string   // workspace-level Intent (internal/intent), when the capture ran through intent detection
	Command      string   // first real (non-shell) Session.Command found, across sessions
	Branch       string   // git branch, when every session with a Git state agrees on one
}

func (s *Store) List(limit int) ([]SnapshotSummary, error) {
	return s.listQuery("SELECT id, name, data, source, hostname, created_at FROM snapshots ORDER BY id DESC LIMIT ?", limit)
}

// ListNamed returns all named workspaces (most recent version of each name).
func (s *Store) ListNamed() ([]SnapshotSummary, error) {
	return s.listQuery(`
		SELECT id, name, data, source, hostname, created_at FROM snapshots
		WHERE name != '' AND id IN (
			SELECT MAX(id) FROM snapshots WHERE name != '' GROUP BY name
		)
		ORDER BY name`, 100)
}

// projectNames returns distinct dir basenames from a session set's CWDs,
// in first-seen order — the "what was this actually a snapshot of" label
// for unnamed freezes, which otherwise all render as an indistinguishable
// "snapshot #N" (operator feedback: "the snapshot list says nothing, I
// can't identify what each was when they all have the same name").
func projectNames(sessions []models.Session) []string {
	var out []string
	seen := map[string]bool{}
	for _, sess := range sessions {
		if sess.CWD == "" {
			continue
		}
		name := filepath.Base(sess.CWD)
		if name == "" || name == "." || name == "/" || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, name)
	}
	return out
}

// genericWrapperCmds are process names that don't say anything about what's
// actually happening — the interpreter/runtime, not the tool. "node" is the
// common case here: `claude` itself runs as a node process, so the raw
// Session.Command is "node" while the real signal is a child process.
var genericWrapperCmds = map[string]bool{
	"node": true, "python3": true, "python": true, "bash": true, "zsh": true, "sh": true, "": true,
}

// sessionFingerprint pulls the "what was actually running" signal out of a
// session set: the first real (non-shell-idle) command, and a git branch if
// every session that has one agrees. Audit finding (2026-08-30, panel):
// SnapshotSummary was discarding Command/Git.Branch entirely, so every
// unnamed snapshot rendered identically — same CWD, no hint what was
// actually happening. If the top-level command is a generic
// interpreter/runtime wrapper, look one level down at its children — a
// "node" process with a "(claude)" child means the real answer is "claude
// session," not "node." Best-effort, first-match wins; doesn't try to be
// clever about multiple different commands across panes.
func sessionFingerprint(sessions []models.Session) (cmd, branch string) {
	for _, sess := range sessions {
		if cmd == "" {
			c := strings.Trim(sess.Command, "()") // ps wraps sleeping/backgrounded processes in parens
			if genericWrapperCmds[strings.ToLower(c)] {
				for _, child := range sess.Children {
					cc := strings.Trim(child.Command, "()")
					if cc != "" && !genericWrapperCmds[strings.ToLower(cc)] {
						c = cc
						break
					}
				}
			}
			if c != "" && !genericWrapperCmds[strings.ToLower(c)] {
				cmd = c
			}
		}
		if sess.Git != nil && sess.Git.Branch != "" {
			if branch == "" {
				branch = sess.Git.Branch
			} else if branch != sess.Git.Branch {
				branch = "" // disagreement across panes — don't show a misleading single branch
				break
			}
		}
	}
	return cmd, branch
}

func (s *Store) listQuery(query string, args ...any) ([]SnapshotSummary, error) {
	rows, err := s.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("listing snapshots: %w", err)
	}
	defer rows.Close()

	var summaries []SnapshotSummary
	for rows.Next() {
		var id int
		var name, data, source, hostname, createdStr string
		if err := rows.Scan(&id, &name, &data, &source, &hostname, &createdStr); err != nil {
			continue
		}
		// Try wrapper format first (v3+), fall back to raw array
		var count int
		var sessions []models.Session
		var sd snapshotData
		var workspaceIntent string
		if err := json.Unmarshal([]byte(data), &sd); err == nil && len(sd.Sessions) > 0 {
			sessions = sd.Sessions
			count = len(sessions)
			workspaceIntent = sd.Intent
		} else {
			json.Unmarshal([]byte(data), &sessions)
			count = len(sessions)
		}
		t, _ := time.Parse(time.RFC3339, createdStr)
		cmd, branch := sessionFingerprint(sessions)
		summaries = append(summaries, SnapshotSummary{
			ID: id, Name: name, SessionCount: count,
			Source: source, Hostname: hostname, CreatedAt: t,
			Projects: projectNames(sessions),
			Intent:   workspaceIntent,
			Command:  cmd,
			Branch:   branch,
		})
	}
	return summaries, nil
}

// DeleteNamed removes all snapshots with the given workspace name.
func (s *Store) DeleteNamed(name string) (int, error) {
	result, err := s.db.Exec("DELETE FROM snapshots WHERE name = ?", name)
	if err != nil {
		return 0, err
	}
	n, _ := result.RowsAffected()
	return int(n), nil
}

func (s *Store) Prune(olderThan time.Duration, keepMin int) (int, error) {
	cutoff := time.Now().Add(-olderThan).Format(time.RFC3339)
	var total int
	s.db.QueryRow("SELECT COUNT(*) FROM snapshots WHERE name = ''").Scan(&total)
	if total <= keepMin {
		return 0, nil
	}
	result, err := s.db.Exec(
		`DELETE FROM snapshots WHERE name = '' AND id NOT IN (
			SELECT id FROM snapshots WHERE name = '' ORDER BY id DESC LIMIT ?
		) AND created_at < ?`,
		keepMin, cutoff,
	)
	if err != nil {
		return 0, fmt.Errorf("pruning: %w", err)
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// DeleteRange removes all snapshots within a time range (for thaw forget).
func (s *Store) DeleteRange(from, to time.Time) (int, error) {
	result, err := s.db.Exec(
		"DELETE FROM snapshots WHERE created_at >= ? AND created_at <= ?",
		from.Format(time.RFC3339), to.Format(time.RFC3339),
	)
	if err != nil {
		return 0, fmt.Errorf("deleting range: %w", err)
	}
	affected, _ := result.RowsAffected()
	return int(affected), nil
}

// GetRange returns all snapshots within a time range.
func (s *Store) GetRange(from, to time.Time) ([]*models.Snapshot, error) {
	rows, err := s.db.Query(
		"SELECT id, name, data, source, hostname, created_at FROM snapshots WHERE created_at >= ? AND created_at <= ? ORDER BY created_at ASC",
		from.Format(time.RFC3339), to.Format(time.RFC3339),
	)
	if err != nil {
		return nil, fmt.Errorf("querying range: %w", err)
	}
	defer rows.Close()

	var snaps []*models.Snapshot
	for rows.Next() {
		var snap models.Snapshot
		var data, createdStr string
		if err := rows.Scan(&snap.ID, &snap.Name, &data, &snap.Source, &snap.Hostname, &createdStr); err != nil {
			continue
		}
		var sd snapshotData
		if err := json.Unmarshal([]byte(data), &sd); err == nil && len(sd.Sessions) > 0 {
			snap.Sessions = sd.Sessions
			snap.Notes = sd.Notes
			snap.Clipboard = sd.Clipboard
			snap.BrowserTabs = sd.BrowserTabs
			snap.PrevHash = sd.PrevHash
			snap.Hash = sd.Hash
			snap.Intent = sd.Intent
		} else {
			json.Unmarshal([]byte(data), &snap.Sessions)
		}
		snap.CreatedAt, _ = time.Parse(time.RFC3339, createdStr)
		snaps = append(snaps, &snap)
	}
	return snaps, nil
}

// GetLatestHash returns the hash of the most recent snapshot.
// IntegrityCheck runs PRAGMA integrity_check on the database.
func (s *Store) IntegrityCheck() error {
	var result string
	err := s.db.QueryRow("PRAGMA integrity_check").Scan(&result)
	if err != nil {
		return fmt.Errorf("integrity check failed: %w", err)
	}
	if result != "ok" {
		return fmt.Errorf("integrity check: %s", result)
	}
	return nil
}

func (s *Store) GetLatestHash() string {
	snap, err := s.getOne("SELECT id, name, data, source, hostname, created_at FROM snapshots ORDER BY id DESC LIMIT 1")
	if err != nil || snap == nil {
		return ""
	}
	if snap.Hash != "" {
		return snap.Hash
	}
	// Pre-audit snapshot — compute from raw data
	data, _ := json.Marshal(snapshotData{Sessions: snap.Sessions})
	return hashData(string(data))
}

func hashData(data string) string {
	h := sha256.Sum256([]byte(data))
	return hex.EncodeToString(h[:16])
}

// AddNote appends a note to the latest snapshot.
func (s *Store) AddNote(note string) error {
	latest, err := s.Latest()
	if err != nil || latest == nil {
		return fmt.Errorf("no snapshots yet — run `thaw freeze` first, then add your note")
	}

	latest.Notes = append(latest.Notes, note)

	// Re-serialize using the same wrapper format as Save()
	sd := snapshotData{
		Sessions:    latest.Sessions,
		Notes:       latest.Notes,
		Clipboard:   latest.Clipboard,
		BrowserTabs: latest.BrowserTabs,
		PrevHash:    latest.PrevHash,
		Hash:        latest.Hash,
		Intent:      latest.Intent,
	}
	data, err := json.Marshal(sd)
	if err != nil {
		return err
	}

	_, err = s.db.Exec("UPDATE snapshots SET data = ? WHERE id = ?", string(data), latest.ID)
	return err
}

// ExportSnapshot serializes a snapshot to portable JSON.
func (s *Store) ExportSnapshot(id int) ([]byte, error) {
	snap, err := s.Get(id)
	if err != nil || snap == nil {
		return nil, fmt.Errorf("snapshot #%d not found — run `thaw history` to list snapshot IDs", id)
	}
	return json.MarshalIndent(snap, "", "  ")
}

// ImportSnapshot deserializes, validates, and stores a snapshot from portable JSON.
// Commands are validated — dangerous patterns are flagged to prevent execution on restore.
func (s *Store) ImportSnapshot(data []byte) (*models.Snapshot, error) {
	var snap models.Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("invalid snapshot data: %w", err)
	}
	snap.ID = 0
	snap.Source = "imported"

	// Validate and sanitize imported sessions
	for i := range snap.Sessions {
		// Sanitize CWD — reject absolute paths that look like system dirs
		cwd := snap.Sessions[i].CWD
		if cwd == "/" || cwd == "/etc" || cwd == "/usr" || cwd == "/bin" {
			snap.Sessions[i].CWD = "~"
		}
		// Flag dangerous commands — they'll be blocked on restore but stored
		cmd := snap.Sessions[i].Command
		if containsDangerousPattern(cmd) {
			snap.Sessions[i].Intent = "(imported-blocked) " + snap.Sessions[i].Intent
		}
		// Strip control characters from all string fields
		snap.Sessions[i].Command = sanitizeStr(snap.Sessions[i].Command)
		snap.Sessions[i].Label = sanitizeStr(snap.Sessions[i].Label)
		snap.Sessions[i].Intent = sanitizeStr(snap.Sessions[i].Intent)
	}

	if err := s.Save(&snap); err != nil {
		return nil, err
	}
	return &snap, nil
}

func containsDangerousPattern(cmd string) bool {
	lower := strings.ToLower(cmd)
	dangerous := []string{"$(", "| sh", "|sh", "| bash", "|bash", "eval ", "exec ", "rm -rf /", "mkfs", "dd if="}
	for _, d := range dangerous {
		if strings.Contains(lower, d) {
			return true
		}
	}
	return false
}

func sanitizeStr(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] >= 32 || s[i] == '\t' {
			b = append(b, s[i])
		}
	}
	if len(b) > 1000 {
		b = b[:1000]
	}
	return string(b)
}

// InsertTombstone records an intentional deletion in the snapshot history.
// This preserves audit chain integrity when using `thaw forget`.
func (s *Store) InsertTombstone(from, to time.Time, count int) {
	tombstone := fmt.Sprintf(`{"tombstone":true,"from":"%s","to":"%s","deleted":%d}`,
		from.Format(time.RFC3339), to.Format(time.RFC3339), count)
	s.db.Exec(
		"INSERT INTO snapshots (name, data, source, hostname, created_at) VALUES (?, ?, ?, ?, ?)",
		"", tombstone, "tombstone", "", time.Now().Format(time.RFC3339),
	)
}

// MigratePaths rewrites CWDs in all snapshots, replacing oldHome with newHome.
// Used after a username change or machine migration.
func (s *Store) MigratePaths(oldHome, newHome string) (int, error) {
	rows, err := s.db.Query("SELECT id, data FROM snapshots")
	if err != nil {
		return 0, err
	}
	defer rows.Close()

	type update struct {
		id   int
		data string
	}
	var updates []update

	for rows.Next() {
		var id int
		var data string
		if err := rows.Scan(&id, &data); err != nil {
			continue
		}
		if !strings.Contains(data, oldHome) {
			continue
		}
		newData := strings.ReplaceAll(data, oldHome, newHome)
		updates = append(updates, update{id: id, data: newData})
	}

	for _, u := range updates {
		s.db.Exec("UPDATE snapshots SET data = ? WHERE id = ?", u.data, u.id)
	}

	return len(updates), nil
}

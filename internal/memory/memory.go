package memory

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ProjectMemory tracks per-directory session history across terminal restarts.
type ProjectMemory struct {
	db *sql.DB
}

// Entry is a remembered session for a project directory.
type Entry struct {
	Dir         string
	Branch      string
	LastCommand string
	LastSeen    time.Time
	SessionPID  int
	Notes       string
}

// Open opens or creates the project memory database.
func Open() (*ProjectMemory, error) {
	home, _ := os.UserHomeDir()
	dataDir := os.Getenv("XDG_DATA_HOME")
	if dataDir == "" {
		dataDir = filepath.Join(home, ".local", "share")
	}
	dir := filepath.Join(dataDir, "thaw")
	os.MkdirAll(dir, 0700)
	dbPath := filepath.Join(dir, "memory.db")

	db, err := sql.Open("sqlite", dbPath+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, err
	}

	db.Exec(`CREATE TABLE IF NOT EXISTS project_memory (
		dir TEXT PRIMARY KEY,
		branch TEXT DEFAULT '',
		last_command TEXT DEFAULT '',
		last_seen TEXT,
		session_pid INTEGER DEFAULT 0,
		notes TEXT DEFAULT ''
	)`)

	os.Chmod(dbPath, 0600)
	return &ProjectMemory{db: db}, nil
}

func (m *ProjectMemory) Close() { m.db.Close() }

// Remember stores the latest session info for a project directory.
func (m *ProjectMemory) Remember(dir string, branch string, lastCmd string, pid int) error {
	_, err := m.db.Exec(`INSERT INTO project_memory (dir, branch, last_command, last_seen, session_pid)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(dir) DO UPDATE SET
			branch = excluded.branch,
			last_command = excluded.last_command,
			last_seen = excluded.last_seen,
			session_pid = excluded.session_pid`,
		dir, branch, lastCmd, time.Now().Format(time.RFC3339), pid)
	return err
}

// Recall retrieves the last known state for a project directory.
func (m *ProjectMemory) Recall(dir string) (*Entry, error) {
	row := m.db.QueryRow(
		"SELECT dir, branch, last_command, last_seen, session_pid, notes FROM project_memory WHERE dir = ?", dir)
	var e Entry
	var lastSeen string
	err := row.Scan(&e.Dir, &e.Branch, &e.LastCommand, &lastSeen, &e.SessionPID, &e.Notes)
	if err != nil {
		return nil, err
	}
	e.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
	return &e, nil
}

// FormatContext returns a brief context line for displaying on cd into a project.
func FormatContext(e *Entry) string {
	if e == nil {
		return ""
	}
	ago := time.Since(e.LastSeen)
	agoStr := formatAgo(ago)

	var parts []string
	parts = append(parts, fmt.Sprintf("last session: %s ago", agoStr))
	if e.Branch != "" {
		parts = append(parts, fmt.Sprintf("branch: %s", e.Branch))
	}
	if e.LastCommand != "" {
		cmd := e.LastCommand
		if len(cmd) > 50 {
			cmd = cmd[:47] + "..."
		}
		parts = append(parts, fmt.Sprintf("last: %s", cmd))
	}
	return "thaw: " + strings.Join(parts, " | ")
}

// ListRecent returns the N most recently seen projects.
func (m *ProjectMemory) ListRecent(limit int) ([]Entry, error) {
	rows, err := m.db.Query(
		"SELECT dir, branch, last_command, last_seen, session_pid, notes FROM project_memory ORDER BY last_seen DESC LIMIT ?", limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var entries []Entry
	for rows.Next() {
		var e Entry
		var lastSeen string
		rows.Scan(&e.Dir, &e.Branch, &e.LastCommand, &lastSeen, &e.SessionPID, &e.Notes)
		e.LastSeen, _ = time.Parse(time.RFC3339, lastSeen)
		entries = append(entries, e)
	}
	return entries, nil
}

func formatAgo(d time.Duration) string {
	if d < time.Minute {
		return "just now"
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

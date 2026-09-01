package snapshot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joecattt/thaw/pkg/models"
)

func testStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	origDataDir := os.Getenv("THAW_DATA_DIR")
	os.Setenv("THAW_DATA_DIR", dir)
	t.Cleanup(func() { os.Setenv("THAW_DATA_DIR", origDataDir) })

	// Open directly with temp path
	dbPath := filepath.Join(dir, "test.db")
	store, err := openPath(dbPath)
	if err != nil {
		t.Fatalf("opening test store: %v", err)
	}
	t.Cleanup(func() { store.Close() })
	return store
}

func TestSaveLoadRoundTrip(t *testing.T) {
	store := testStore(t)

	snap := &models.Snapshot{
		Sessions: []models.Session{
			{PID: 1234, CWD: "/home/user/project", Command: "npm run dev", Shell: "zsh", Status: "running",
				Label: "Backend", Intent: "running dev server",
				Git: &models.GitState{Branch: "feature/auth", Commit: "abc123", Dirty: true, RepoRoot: "/home/user/project"},
				EnvDelta: models.EnvDelta{Set: map[string]string{"NODE_ENV": "development"}},
				History: []string{"git pull", "npm install", "npm run dev"},
			},
			{PID: 5678, CWD: "/home/user/project", Command: "zsh", Shell: "zsh", Status: "idle"},
		},
		CreatedAt: time.Now().Truncate(time.Second),
		Source:    "test",
		Hostname:  "testhost",
		Intent:    "working on auth feature",
	}

	if err := store.Save(snap); err != nil {
		t.Fatalf("Save: %v", err)
	}
	if snap.ID == 0 {
		t.Fatal("Save should assign ID")
	}

	loaded, err := store.Get(snap.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if loaded == nil {
		t.Fatal("Get returned nil")
	}

	// Verify all fields survived
	if len(loaded.Sessions) != 2 {
		t.Fatalf("expected 2 sessions, got %d", len(loaded.Sessions))
	}

	s := loaded.Sessions[0]
	if s.PID != 1234 {
		t.Errorf("PID: got %d, want 1234", s.PID)
	}
	if s.CWD != "/home/user/project" {
		t.Errorf("CWD: got %q", s.CWD)
	}
	if s.Command != "npm run dev" {
		t.Errorf("Command: got %q", s.Command)
	}
	if s.Intent != "running dev server" {
		t.Errorf("Intent: got %q", s.Intent)
	}
	if s.Git == nil {
		t.Fatal("Git state lost")
	}
	if s.Git.Branch != "feature/auth" {
		t.Errorf("Branch: got %q", s.Git.Branch)
	}
	if !s.Git.Dirty {
		t.Error("Dirty flag lost")
	}
	if s.EnvDelta.Set["NODE_ENV"] != "development" {
		t.Errorf("EnvDelta: got %v", s.EnvDelta.Set)
	}
	if len(s.History) != 3 {
		t.Errorf("History: got %d entries", len(s.History))
	}

	if loaded.Source != "test" {
		t.Errorf("Source: got %q", loaded.Source)
	}
	if loaded.Hostname != "testhost" {
		t.Errorf("Hostname: got %q", loaded.Hostname)
	}
	if loaded.Intent != "working on auth feature" {
		t.Errorf("Snapshot Intent: got %q", loaded.Intent)
	}
}

func TestAddNotePreservesData(t *testing.T) {
	store := testStore(t)

	snap := &models.Snapshot{
		Sessions:  []models.Session{{PID: 1, CWD: "/tmp", Command: "vim main.go", Shell: "zsh"}},
		CreatedAt: time.Now().Truncate(time.Second),
		Source:    "test",
	}
	store.Save(snap)

	// Add a note
	if err := store.AddNote("testing clock skew hypothesis"); err != nil {
		t.Fatalf("AddNote: %v", err)
	}

	// Reload and verify BOTH note AND sessions survived
	loaded, _ := store.Latest()
	if loaded == nil {
		t.Fatal("Latest returned nil after AddNote")
	}
	if len(loaded.Sessions) != 1 {
		t.Fatalf("Sessions lost after AddNote: got %d", len(loaded.Sessions))
	}
	if loaded.Sessions[0].Command != "vim main.go" {
		t.Errorf("Command corrupted after AddNote: got %q", loaded.Sessions[0].Command)
	}
	if len(loaded.Notes) != 1 || loaded.Notes[0] != "testing clock skew hypothesis" {
		t.Errorf("Notes: got %v", loaded.Notes)
	}

	// Add second note
	store.AddNote("confirmed: clock skew is 7 seconds")
	loaded, _ = store.Latest()
	if len(loaded.Notes) != 2 {
		t.Errorf("expected 2 notes, got %d", len(loaded.Notes))
	}
	if len(loaded.Sessions) != 1 {
		t.Fatalf("Sessions lost after second AddNote: got %d", len(loaded.Sessions))
	}
}

func TestClipboardAndBrowserTabsRoundTrip(t *testing.T) {
	store := testStore(t)

	snap := &models.Snapshot{
		Sessions:    []models.Session{{PID: 1, CWD: "/tmp", Command: "zsh", Shell: "zsh"}},
		CreatedAt:   time.Now().Truncate(time.Second),
		Source:      "test",
		Clipboard:   "192.168.1.100",
		BrowserTabs: []string{"https://docs.example.com/api", "https://github.com/pr/247"},
	}
	store.Save(snap)

	loaded, _ := store.Get(snap.ID)
	if loaded.Clipboard != "192.168.1.100" {
		t.Errorf("Clipboard lost: got %q", loaded.Clipboard)
	}
	if len(loaded.BrowserTabs) != 2 {
		t.Errorf("BrowserTabs lost: got %d", len(loaded.BrowserTabs))
	}
}

func TestNamedWorkspaceRoundTrip(t *testing.T) {
	store := testStore(t)

	snap := &models.Snapshot{
		Name:      "api-debug",
		Sessions:  []models.Session{{PID: 1, CWD: "/project/api", Command: "npm run dev", Shell: "zsh"}},
		CreatedAt: time.Now().Truncate(time.Second),
		Source:    "named",
	}
	store.Save(snap)

	// Retrieve by name
	loaded, _ := store.GetNamed("api-debug")
	if loaded == nil {
		t.Fatal("GetNamed returned nil")
	}
	if loaded.Name != "api-debug" {
		t.Errorf("Name: got %q", loaded.Name)
	}
	if loaded.Sessions[0].Command != "npm run dev" {
		t.Errorf("Command: got %q", loaded.Sessions[0].Command)
	}

	// Latest should not return named snapshots
	latest, _ := store.Latest()
	if latest != nil {
		t.Error("Latest should not return named snapshots")
	}
}

func TestImportExportRoundTrip(t *testing.T) {
	store := testStore(t)

	snap := &models.Snapshot{
		Name:      "export-test",
		Sessions:  []models.Session{{PID: 42, CWD: "/work", Command: "go test", Shell: "zsh", Intent: "testing"}},
		CreatedAt: time.Now().Truncate(time.Second),
		Source:    "test",
	}
	store.Save(snap)

	// Export
	data, err := store.ExportSnapshot(snap.ID)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// Import
	imported, err := store.ImportSnapshot(data)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if imported.Source != "imported" {
		t.Errorf("Import source: got %q", imported.Source)
	}
	if len(imported.Sessions) != 1 {
		t.Fatalf("Import sessions: got %d", len(imported.Sessions))
	}
	if imported.Sessions[0].Command != "go test" {
		t.Errorf("Import command: got %q", imported.Sessions[0].Command)
	}
}

// TestOpenPathRejectsEmptyFile is the core regression test for the silent
// data-loss bug: an empty (0-byte) database file — e.g. left behind by a
// crash mid-write or a disk-full event — must NOT be silently reinitialized
// as a fresh, empty database. It must be detected, backed up, and reported.
func TestOpenPathRejectsEmptyFile(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "thaw.db")

	if err := os.WriteFile(dbPath, []byte{}, 0600); err != nil {
		t.Fatalf("writing empty file: %v", err)
	}

	store, err := openPath(dbPath)
	if err == nil {
		store.Close()
		t.Fatal("openPath should have refused to open an empty (corrupted) database file")
	}
	if !strings.Contains(err.Error(), "corrupted") {
		t.Errorf("error should mention corruption, got: %v", err)
	}
	if !strings.Contains(err.Error(), "thaw doctor") {
		t.Errorf("error should point the user to `thaw doctor`, got: %v", err)
	}

	// The original (empty) file must still exist untouched — no silent
	// reinitialization.
	info, statErr := os.Stat(dbPath)
	if statErr != nil {
		t.Fatalf("original file should still exist: %v", statErr)
	}
	if info.Size() != 0 {
		t.Errorf("original file should remain untouched (0 bytes), got %d bytes", info.Size())
	}

	// A backup of the corrupted file must have been created alongside it.
	matches, _ := filepath.Glob(dbPath + ".corrupted-*")
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 backup file, got %d: %v", len(matches), matches)
	}
}

// TestOpenPathRejectsInvalidHeader covers a non-empty file whose contents
// aren't a valid SQLite database (garbage bytes, truncated file, etc).
func TestOpenPathRejectsInvalidHeader(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "thaw.db")

	garbage := []byte("this is definitely not a sqlite database, just noise 1234567890")
	if err := os.WriteFile(dbPath, garbage, 0600); err != nil {
		t.Fatalf("writing garbage file: %v", err)
	}

	store, err := openPath(dbPath)
	if err == nil {
		store.Close()
		t.Fatal("openPath should have refused to open a file with an invalid SQLite header")
	}
	if !strings.Contains(err.Error(), "corrupted") {
		t.Errorf("error should mention corruption, got: %v", err)
	}

	matches, _ := filepath.Glob(dbPath + ".corrupted-*")
	if len(matches) != 1 {
		t.Fatalf("expected exactly 1 backup file, got %d: %v", len(matches), matches)
	}
	backupData, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("reading backup: %v", err)
	}
	if string(backupData) != string(garbage) {
		t.Errorf("backup content should match original corrupted file exactly")
	}
}

// TestOpenPathAllowsFreshAndValidDatabase confirms the fix doesn't affect
// normal operation: creating a brand-new database and reopening a valid,
// existing one both continue to work.
func TestOpenPathAllowsFreshAndValidDatabase(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "thaw.db")

	store, err := openPath(dbPath)
	if err != nil {
		t.Fatalf("opening a brand-new database should succeed: %v", err)
	}
	snap := &models.Snapshot{CreatedAt: time.Now().Truncate(time.Second), Source: "test"}
	if err := store.Save(snap); err != nil {
		t.Fatalf("Save: %v", err)
	}
	store.Close()

	store2, err := openPath(dbPath)
	if err != nil {
		t.Fatalf("reopening a valid, existing database should succeed: %v", err)
	}
	defer store2.Close()

	latest, err := store2.Latest()
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if latest == nil {
		t.Fatal("snapshot saved before reopen should still be there")
	}
}

package daemon

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joecattt/thaw/pkg/models"
)

// withStateDir points XDG_STATE_HOME at a temp dir for the duration of a test,
// so commandLogPath/writeHeartbeat/HeartbeatAge never touch the real
// ~/.local/state — those are the only daemon.go paths that honor the env var
// override. pidFilePath/IsRunning/Stop go through config.DataDir(), which is
// hardcoded to ~/.local/share/thaw with no override, so this suite
// deliberately does not exercise those against a live path (see TODO.md).
func withStateDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	old, hadOld := os.LookupEnv("XDG_STATE_HOME")
	os.Setenv("XDG_STATE_HOME", dir)
	t.Cleanup(func() {
		if hadOld {
			os.Setenv("XDG_STATE_HOME", old)
		} else {
			os.Unsetenv("XDG_STATE_HOME")
		}
	})
	return dir
}

func TestSortStrings(t *testing.T) {
	s := []string{"banana", "apple", "cherry", "apple"}
	sortStrings(s)
	want := []string{"apple", "apple", "banana", "cherry"}
	for i := range want {
		if s[i] != want[i] {
			t.Errorf("sortStrings[%d] = %q, want %q (full: %v)", i, s[i], want[i], s)
		}
	}
}

func TestSortStrings_EmptyAndSingle(t *testing.T) {
	empty := []string{}
	sortStrings(empty) // must not panic
	single := []string{"only"}
	sortStrings(single)
	if single[0] != "only" {
		t.Errorf("single-element sort mutated the value: %v", single)
	}
}

func TestHashSnapshot_DeterministicRegardlessOfOrder(t *testing.T) {
	snapA := &models.Snapshot{Sessions: []models.Session{
		{CWD: "/a", Command: "vim"},
		{CWD: "/b", Command: "npm run dev"},
	}}
	snapB := &models.Snapshot{Sessions: []models.Session{
		{CWD: "/b", Command: "npm run dev"},
		{CWD: "/a", Command: "vim"},
	}}
	if hashSnapshot(snapA) != hashSnapshot(snapB) {
		t.Error("hashSnapshot should be order-independent (sorts before hashing), but two permutations differed")
	}
}

func TestHashSnapshot_ChangesWithContent(t *testing.T) {
	snap1 := &models.Snapshot{Sessions: []models.Session{{CWD: "/a", Command: "vim"}}}
	snap2 := &models.Snapshot{Sessions: []models.Session{{CWD: "/a", Command: "emacs"}}}
	if hashSnapshot(snap1) == hashSnapshot(snap2) {
		t.Error("expected different command to produce a different hash")
	}
}

func TestHashSnapshot_EmptySessionsIsStable(t *testing.T) {
	snap := &models.Snapshot{}
	h1 := hashSnapshot(snap)
	h2 := hashSnapshot(snap)
	if h1 != h2 {
		t.Errorf("hash of empty snapshot should be stable, got %q then %q", h1, h2)
	}
}

func TestCommandLogPath_RespectsXDGStateHome(t *testing.T) {
	dir := withStateDir(t)
	got := commandLogPath()
	want := filepath.Join(dir, "thaw", "commands.log")
	if got != want {
		t.Errorf("commandLogPath() = %q, want %q", got, want)
	}
}

func TestIsUserActive_NoLogFileMeansInactive(t *testing.T) {
	withStateDir(t)
	if isUserActive() {
		t.Error("expected isUserActive() to be false when no command log exists")
	}
}

func TestIsUserActive_RecentLogMeansActive(t *testing.T) {
	withStateDir(t)
	path := commandLogPath()
	os.MkdirAll(filepath.Dir(path), 0700)
	if err := os.WriteFile(path, []byte("ls\n"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	if !isUserActive() {
		t.Error("expected isUserActive() to be true right after writing the command log")
	}
}

func TestIsUserActive_StaleLogMeansInactive(t *testing.T) {
	withStateDir(t)
	path := commandLogPath()
	os.MkdirAll(filepath.Dir(path), 0700)
	if err := os.WriteFile(path, []byte("ls\n"), 0600); err != nil {
		t.Fatalf("setup: %v", err)
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("setup chtimes: %v", err)
	}
	if isUserActive() {
		t.Error("expected isUserActive() to be false for a log last touched 10 minutes ago")
	}
}

func TestHeartbeat_WriteThenAge(t *testing.T) {
	withStateDir(t)
	writeHeartbeat()
	age := HeartbeatAge()
	if age < 0 {
		t.Fatal("expected a non-negative heartbeat age right after writing one")
	}
	if age > 5*time.Second {
		t.Errorf("expected heartbeat age to be near-zero right after writing, got %s", age)
	}
}

func TestHeartbeatAge_NoHeartbeatReturnsNegativeOne(t *testing.T) {
	withStateDir(t)
	if age := HeartbeatAge(); age != -1 {
		t.Errorf("expected -1 when no heartbeat file exists, got %s", age)
	}
}

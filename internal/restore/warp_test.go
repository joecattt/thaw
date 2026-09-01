package restore

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/joecattt/thaw/pkg/models"
)

func TestParseLiveResumeIDs(t *testing.T) {
	psOut := ` 1234 claude --resume abc-123
 5678 /usr/local/bin/node something
 9999 claude --resume def-456 --verbose
 1111 claude --continue
 2222 grep claude --resume
`
	got := ParseLiveResumeIDs(psOut)
	if len(got) != 2 || !got["abc-123"] || !got["def-456"] {
		t.Errorf("ParseLiveResumeIDs = %v, want {abc-123, def-456}", got)
	}
}

func TestAssignWarpTabs(t *testing.T) {
	sessions := []models.Session{
		{CWD: "/p/alpha", Command: "claude"},
		{CWD: "/p/alpha", Command: "claude"}, // sibling pane, same project
		{CWD: "/p/beta", Command: "claude"},
		{CWD: "/p/beta", Command: "claude"}, // pool exhausted — skipped
	}
	ids := map[string][]string{
		"/p/alpha": {"a-live", "a-new", "a-old"}, // a-live is held by a running claude
		"/p/beta":  {"b-only"},
	}
	idsFor := func(cwd string) []string { return ids[cwd] }
	locked := map[string]bool{"a-live": true}

	tabs, skippedLocked, notes := AssignWarpTabs(sessions, idsFor, locked, "/home")
	want := []WarpTab{
		{Dir: "/p/alpha", Command: "claude --resume a-new"},
		{Dir: "/p/alpha", Command: "claude --resume a-old"},
		{Dir: "/p/beta", Command: "claude --resume b-only"},
	}
	if len(tabs) != len(want) {
		t.Fatalf("got %d tabs, want %d: %v", len(tabs), len(want), tabs)
	}
	for i := range want {
		if tabs[i] != want[i] {
			t.Errorf("tab %d = %v, want %v", i, tabs[i], want[i])
		}
	}
	if skippedLocked != 1 {
		t.Errorf("skippedLocked = %d, want 1", skippedLocked)
	}
	if len(notes) != 1 { // the fourth session found beta's pool empty
		t.Errorf("notes = %v, want one exhausted-pool note", notes)
	}
}

func TestAssignWarpTabsEmptyCWDUsesHome(t *testing.T) {
	var asked string
	idsFor := func(cwd string) []string { asked = cwd; return []string{"x"} }
	tabs, _, _ := AssignWarpTabs([]models.Session{{Command: "claude"}}, idsFor, nil, "/home/u")
	if asked != "/home/u" || len(tabs) != 1 || tabs[0].Dir != "/home/u" {
		t.Errorf("empty cwd should fall back to home: asked=%q tabs=%v", asked, tabs)
	}
}

func TestGenerateWarpLaunchConfigGolden(t *testing.T) {
	tabs := []WarpTab{
		{Dir: "/Users/x/dev/foo", Command: "claude --resume abc-123"},
		{Dir: `/Users/x/we"ird`, Command: "claude --resume def-456"},
	}
	got := GenerateWarpLaunchConfig(tabs, 42, "2026-08-30 03:00:00")
	golden := filepath.Join("testdata", "warp_launch.golden.toml")
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("reading golden file: %v", err)
	}
	if got != string(want) {
		t.Errorf("launch config differs from golden:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

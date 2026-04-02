package diff

import (
	"testing"

	"github.com/joecattt/thaw/pkg/models"
)

func TestCompare_NoChanges(t *testing.T) {
	prev := &models.Snapshot{Sessions: []models.Session{
		{CWD: "/project/api", Command: "npm run dev", Label: "Backend"},
	}}
	curr := &models.Snapshot{Sessions: []models.Session{
		{CWD: "/project/api", Command: "npm run dev", Label: "Backend"},
	}}

	result := Compare(prev, curr)
	if len(result.Added) != 0 || len(result.Removed) != 0 || len(result.Changed) != 0 {
		t.Error("expected no changes")
	}
	if result.Unchanged != 1 {
		t.Errorf("expected 1 unchanged, got %d", result.Unchanged)
	}
}

func TestCompare_AddedSession(t *testing.T) {
	prev := &models.Snapshot{Sessions: []models.Session{
		{CWD: "/project/api", Command: "npm run dev"},
	}}
	curr := &models.Snapshot{Sessions: []models.Session{
		{CWD: "/project/api", Command: "npm run dev"},
		{CWD: "/project/web", Command: "next dev"},
	}}

	result := Compare(prev, curr)
	if len(result.Added) != 1 {
		t.Errorf("expected 1 added, got %d", len(result.Added))
	}
	if result.Added[0].CWD != "/project/web" {
		t.Errorf("expected added session at /project/web")
	}
}

func TestCompare_RemovedSession(t *testing.T) {
	prev := &models.Snapshot{Sessions: []models.Session{
		{CWD: "/project/api", Command: "npm run dev"},
		{CWD: "/project/web", Command: "next dev"},
	}}
	curr := &models.Snapshot{Sessions: []models.Session{
		{CWD: "/project/api", Command: "npm run dev"},
	}}

	result := Compare(prev, curr)
	if len(result.Removed) != 1 {
		t.Errorf("expected 1 removed, got %d", len(result.Removed))
	}
}

func TestCompare_ChangedCommand(t *testing.T) {
	prev := &models.Snapshot{Sessions: []models.Session{
		{CWD: "/project/api", Command: "npm run dev"},
	}}
	curr := &models.Snapshot{Sessions: []models.Session{
		{CWD: "/project/api", Command: "npm test"},
	}}

	result := Compare(prev, curr)
	if len(result.Changed) != 1 {
		t.Errorf("expected 1 changed, got %d", len(result.Changed))
	}
	if result.Changed[0].Diffs[0] != "command: npm run dev → npm test" {
		t.Errorf("unexpected diff: %s", result.Changed[0].Diffs[0])
	}
}

func TestCompare_BranchChange(t *testing.T) {
	prev := &models.Snapshot{Sessions: []models.Session{
		{CWD: "/project", Command: "zsh", Shell: "zsh", Git: &models.GitState{Branch: "main"}},
	}}
	curr := &models.Snapshot{Sessions: []models.Session{
		{CWD: "/project", Command: "zsh", Shell: "zsh", Git: &models.GitState{Branch: "feature/auth"}},
	}}

	result := Compare(prev, curr)
	if len(result.Changed) != 1 {
		t.Fatalf("expected 1 changed, got %d", len(result.Changed))
	}

	found := false
	for _, d := range result.Changed[0].Diffs {
		if d == "branch: main → feature/auth" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected branch change in diffs: %v", result.Changed[0].Diffs)
	}
}

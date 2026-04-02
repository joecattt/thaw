package group

import (
	"testing"

	"github.com/joecattt/thaw/pkg/models"
)

func TestIsMonorepoPrefix(t *testing.T) {
	yes := []string{"apps", "packages", "services", "libs", "cmd", "internal"}
	for _, p := range yes {
		if !isMonorepoPrefix(p) {
			t.Errorf("expected %q to be a monorepo prefix", p)
		}
	}

	no := []string{"src", "dist", "build", "node_modules", "random"}
	for _, p := range no {
		if isMonorepoPrefix(p) {
			t.Errorf("expected %q to NOT be a monorepo prefix", p)
		}
	}
}

func TestSplitMonorepo(t *testing.T) {
	sessions := []models.Session{
		{CWD: "/repo/apps/api/src", Git: &models.GitState{RepoRoot: "/repo"}},
		{CWD: "/repo/apps/api/tests", Git: &models.GitState{RepoRoot: "/repo"}},
		{CWD: "/repo/apps/web/components", Git: &models.GitState{RepoRoot: "/repo"}},
		{CWD: "/repo/packages/shared", Git: &models.GitState{RepoRoot: "/repo"}},
	}

	groups := splitMonorepo("/repo", []int{0, 1, 2, 3}, sessions)

	if _, ok := groups["apps/api"]; !ok {
		t.Error("expected apps/api group")
	}
	if _, ok := groups["apps/web"]; !ok {
		t.Error("expected apps/web group")
	}
	if _, ok := groups["packages/shared"]; !ok {
		t.Error("expected packages/shared group")
	}

	if len(groups["apps/api"]) != 2 {
		t.Errorf("expected 2 sessions in apps/api, got %d", len(groups["apps/api"]))
	}
}

func TestSplitMonorepo_NonMonorepo(t *testing.T) {
	sessions := []models.Session{
		{CWD: "/repo/src/main.go", Git: &models.GitState{RepoRoot: "/repo"}},
		{CWD: "/repo/src/handler.go", Git: &models.GitState{RepoRoot: "/repo"}},
	}

	groups := splitMonorepo("/repo", []int{0, 1}, sessions)

	// "src" is not a monorepo prefix, so both should be under "src"
	if len(groups) != 1 {
		t.Errorf("expected 1 group for non-monorepo, got %d: %v", len(groups), groups)
	}
}

func TestAssign_GroupsByRepo(t *testing.T) {
	sessions := []models.Session{
		{CWD: "/project/api", TTY: "ttys001", Git: &models.GitState{RepoRoot: "/project"}},
		{CWD: "/project/web", TTY: "ttys002", Git: &models.GitState{RepoRoot: "/project"}},
		{CWD: "/other/tool", TTY: "ttys003"},
	}

	result := Assign(sessions)

	// First two should be grouped
	if result[0].GroupID == "" {
		t.Error("expected first session to be grouped")
	}
	if result[0].GroupID != result[1].GroupID {
		t.Error("expected first two sessions in same group")
	}
	// Third should be ungrouped
	if result[2].GroupID != "" {
		t.Error("expected third session to be ungrouped")
	}
}

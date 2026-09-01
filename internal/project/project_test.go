package project

import (
	"testing"

	"github.com/joecattt/thaw/pkg/models"
)

func TestGroup_BucketsByGroupName(t *testing.T) {
	sessions := []models.Session{
		{CWD: "/repo/a", GroupName: "api", Command: "npm run dev"},
		{CWD: "/repo/a", GroupName: "api", Command: "vim"},
		{CWD: "/repo/b", GroupName: "web", Command: "npm start"},
	}

	groups := Group(sessions)

	if len(groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(groups))
	}
	// Sorted by session count descending — "api" has 2 sessions, "web" has 1.
	if groups[0].Name != "api" || len(groups[0].Sessions) != 2 {
		t.Errorf("expected first group to be api with 2 sessions, got %q with %d", groups[0].Name, len(groups[0].Sessions))
	}
	if groups[1].Name != "web" || len(groups[1].Sessions) != 1 {
		t.Errorf("expected second group to be web with 1 session, got %q with %d", groups[1].Name, len(groups[1].Sessions))
	}
}

func TestGroup_FallsBackToCWDBasename(t *testing.T) {
	sessions := []models.Session{
		{CWD: "/home/user/myproject"},
	}

	groups := Group(sessions)

	if len(groups) != 1 || groups[0].Name != "myproject" {
		t.Fatalf("expected group named myproject, got %+v", groups)
	}
}

func TestGroup_TracksBranchDirtyAndAliveIdle(t *testing.T) {
	sessions := []models.Session{
		{CWD: "/repo", GroupName: "api", Command: "vim", Git: &models.GitState{Branch: "main", Dirty: true}},
		{CWD: "/repo", GroupName: "api", Command: "", Shell: ""}, // idle: Command == Shell == ""
	}

	groups := Group(sessions)

	if len(groups) != 1 {
		t.Fatalf("expected 1 group, got %d", len(groups))
	}
	g := groups[0]
	if g.Branch != "main*" {
		t.Errorf("expected dirty branch marker, got %q", g.Branch)
	}
	if g.Alive != 1 || g.Idle != 1 {
		t.Errorf("expected 1 alive and 1 idle, got alive=%d idle=%d", g.Alive, g.Idle)
	}
}

func TestGroup_DedupesDirs(t *testing.T) {
	sessions := []models.Session{
		{CWD: "/repo", GroupName: "api"},
		{CWD: "/repo", GroupName: "api"},
		{CWD: "/repo/sub", GroupName: "api"},
	}

	groups := Group(sessions)

	if len(groups) != 1 || len(groups[0].Dirs) != 2 {
		t.Fatalf("expected 2 unique dirs, got %+v", groups)
	}
}

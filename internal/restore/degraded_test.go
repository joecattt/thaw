package restore

import (
	"strings"
	"testing"
	"time"

	"github.com/joecattt/thaw/pkg/models"
)

func fixtureSnapshot() *models.Snapshot {
	return &models.Snapshot{
		ID:        42,
		CreatedAt: time.Date(2026, 7, 3, 16, 47, 0, 0, time.Local),
		Source:    "shutdown",
		Intent:    "fixing the restore race condition",
		Sessions: []models.Session{
			{
				PID:     100,
				CWD:     "/home/dev/api-project",
				Label:   "api",
				History: []string{"go test ./...", "git status"},
			},
			{
				PID:     200,
				CWD:     "/home/dev/popupplaza",
				Label:   "web",
				Focused: true,
				Git:     &models.GitState{Branch: "feature/auth", Dirty: true, RepoRoot: "/home/dev/popupplaza"},
				History: []string{"npm run dev"},
			},
		},
	}
}

func TestFormatSnapTime(t *testing.T) {
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.Local)
	tests := []struct {
		when   time.Time
		expect string
	}{
		{time.Date(2026, 7, 4, 16, 47, 0, 0, time.Local), "Today 4:47pm"},
		{time.Date(2026, 7, 3, 16, 47, 0, 0, time.Local), "Yesterday 4:47pm"},
		{time.Date(2026, 6, 29, 9, 5, 0, 0, time.Local), "Monday 9:05am"},
		{time.Date(2026, 6, 12, 8, 30, 0, 0, time.Local), "Jun 12 8:30am"},
	}

	for _, tt := range tests {
		got := FormatSnapTime(tt.when, now)
		if got != tt.expect {
			t.Errorf("FormatSnapTime(%s) = %q, want %q", tt.when, got, tt.expect)
		}
	}
}

func TestPrimarySession(t *testing.T) {
	snap := fixtureSnapshot()
	p := PrimarySession(snap)
	if p == nil || p.PID != 200 {
		t.Fatalf("expected focused session (PID 200), got %+v", p)
	}

	snap.Sessions[1].Focused = false
	p = PrimarySession(snap)
	if p == nil || p.PID != 100 {
		t.Fatalf("expected first session (PID 100) without focus, got %+v", p)
	}

	if PrimarySession(nil) != nil {
		t.Error("expected nil for nil snapshot")
	}
	if PrimarySession(&models.Snapshot{}) != nil {
		t.Error("expected nil for empty snapshot")
	}
}

func TestSummarize(t *testing.T) {
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.Local)
	got := Summarize(fixtureSnapshot(), now)

	for _, want := range []string{
		"Where you were: Yesterday 4:47pm",
		"popupplaza",
		"on branch feature/auth",
		"(uncommitted changes)",
		"2 session(s)",
		"Working on: fixing the restore race condition",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("Summarize() = %q, missing %q", got, want)
		}
	}

	if Summarize(nil, now) != "" {
		t.Error("expected empty summary for nil snapshot")
	}
	if Summarize(&models.Snapshot{}, now) != "" {
		t.Error("expected empty summary for empty snapshot")
	}
}

func TestSummarizeNamedClean(t *testing.T) {
	now := time.Date(2026, 7, 4, 10, 0, 0, 0, time.Local)
	snap := fixtureSnapshot()
	snap.Name = "sprint-12"
	snap.Intent = ""
	snap.Sessions[1].Git.Dirty = false
	snap.Sessions[1].Intent = "wiring auth middleware"

	got := Summarize(snap, now)
	if !strings.Contains(got, "sprint-12") {
		t.Errorf("expected workspace name in summary, got %q", got)
	}
	if strings.Contains(got, "uncommitted") {
		t.Errorf("clean branch should not mention uncommitted changes: %q", got)
	}
	if !strings.Contains(got, "Working on: wiring auth middleware") {
		t.Errorf("expected session intent fallback, got %q", got)
	}
}

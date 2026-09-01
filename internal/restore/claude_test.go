package restore

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/joecattt/thaw/pkg/models"
)

func TestClaudeProjectSlug(t *testing.T) {
	cases := map[string]string{
		"/Users/alice":           "-Users-alice",
		"/Users/alice/dev/thaw":  "-Users-alice-dev-thaw",
		"/Users/alice/dev/a.b_c": "-Users-alice-dev-a-b-c",
	}
	for in, want := range cases {
		if got := claudeProjectSlug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsClaudePane(t *testing.T) {
	r := NewClaudeResumer()
	if !r.IsClaudePane(models.Session{Command: "claude"}) {
		t.Error("bare claude not detected")
	}
	if !r.IsClaudePane(models.Session{Command: "claude --continue"}) {
		t.Error("claude with args not detected")
	}
	if !r.IsClaudePane(models.Session{Command: "zsh", Children: []models.Process{{Command: "/usr/local/bin/claude"}}}) {
		t.Error("claude child not detected")
	}
	if r.IsClaudePane(models.Session{Command: "vim claude-notes.md"}) {
		t.Error("false positive on claude-ish filename")
	}
}

func TestResumeCommandNewestFirstAndDistinct(t *testing.T) {
	tmp := t.TempDir()
	cwd := "/fake/project"
	projDir := filepath.Join(tmp, claudeProjectSlug(cwd))
	if err := os.MkdirAll(projDir, 0755); err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(projDir, "older-session.jsonl")
	newer := filepath.Join(projDir, "newer-session.jsonl")
	os.WriteFile(old, []byte("{}"), 0644)
	os.WriteFile(newer, []byte("{}"), 0644)
	past := time.Now().Add(-time.Hour)
	os.Chtimes(old, past, past)

	r := &ClaudeResumer{projectsDir: tmp, byDir: map[string][]string{}}
	sess := models.Session{CWD: cwd, Command: "claude"}

	if got := r.ResumeCommand(sess); got != "claude --resume newer-session" {
		t.Errorf("first pane got %q, want newest", got)
	}
	if got := r.ResumeCommand(sess); got != "claude --resume older-session" {
		t.Errorf("second pane got %q, want older (distinct)", got)
	}
	if got := r.ResumeCommand(sess); got != "claude --continue" {
		t.Errorf("exhausted pool got %q, want --continue fallback", got)
	}
}

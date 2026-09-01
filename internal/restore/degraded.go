package restore

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/joecattt/thaw/pkg/models"
)

// PrimarySession returns the most relevant session in a snapshot —
// the focused pane if one was recorded, otherwise the first session.
func PrimarySession(snap *models.Snapshot) *models.Session {
	if snap == nil || len(snap.Sessions) == 0 {
		return nil
	}
	for i := range snap.Sessions {
		if snap.Sessions[i].Focused {
			return &snap.Sessions[i]
		}
	}
	return &snap.Sessions[0]
}

// FormatSnapTime renders a snapshot timestamp as relative plain English:
// "Today 4:47pm", "Yesterday 4:47pm", "Monday 4:47pm" (within a week),
// or "Jun 12 4:47pm" for anything older.
func FormatSnapTime(t, now time.Time) string {
	t = t.In(now.Location())
	clock := t.Format("3:04pm")
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	day := time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, t.Location())
	switch {
	case day.Equal(today):
		return "Today " + clock
	case day.Equal(today.AddDate(0, 0, -1)):
		return "Yesterday " + clock
	case day.After(today.AddDate(0, 0, -6)):
		return t.Format("Monday") + " " + clock
	default:
		return t.Format("Jan 2") + " " + clock
	}
}

// Summarize builds a one-paragraph plain-English "where was I" summary of a
// snapshot from data already captured — used by the bare-`thaw` greeting and
// by degraded (no-tmux) restore.
func Summarize(snap *models.Snapshot, now time.Time) string {
	if snap == nil || len(snap.Sessions) == 0 {
		return ""
	}
	primary := PrimarySession(snap)

	name := snap.Name
	if name == "" {
		name = primary.GroupName
	}
	if name == "" {
		name = filepath.Base(primary.CWD)
	}

	var b strings.Builder
	fmt.Fprintf(&b, "Where you were: %s — %s", FormatSnapTime(snap.CreatedAt, now), name)
	if primary.Git != nil && primary.Git.Branch != "" {
		b.WriteString(" on branch " + primary.Git.Branch)
		if primary.Git.Dirty {
			b.WriteString(" (uncommitted changes)")
		}
	}
	fmt.Fprintf(&b, ", %d session(s)", len(snap.Sessions))

	intent := snap.Intent
	if intent == "" {
		intent = primary.Intent
	}
	if intent != "" {
		b.WriteString(". Working on: " + intent)
	}
	b.WriteString(".")
	return b.String()
}

// DegradedRestore is the no-tmux fallback: it prints the where-was-I summary,
// per-session context (cwd, branch, recent commands), and writes a small
// resume script. It never starts processes.
func DegradedRestore(snap *models.Snapshot, opts models.RestoreOptions) error {
	if snap == nil || len(snap.Sessions) == 0 {
		return fmt.Errorf("snapshot has no sessions to restore — run `thaw freeze` while terminals are open, then try again")
	}

	fmt.Println("Degraded restore (no tmux) — showing context only, no processes will be started.")
	fmt.Println()
	fmt.Println(Summarize(snap, time.Now()))
	fmt.Println()

	resumer := NewClaudeResumer()
	for i, s := range snap.Sessions {
		label := s.Label
		if label == "" {
			label = fmt.Sprintf("session %d", i+1)
		}
		fmt.Printf("[%d] %s\n", i+1, label)
		fmt.Printf("    cd %s\n", s.CWD)
		if s.Git != nil && s.Git.Branch != "" {
			branch := s.Git.Branch
			if s.Git.Dirty {
				branch += "*"
			}
			fmt.Printf("    branch: %s\n", branch)
		}
		if len(s.History) > 0 {
			limit := 5
			if opts.HistoryLines > 0 && opts.HistoryLines < limit {
				limit = opts.HistoryLines
			}
			start := 0
			if len(s.History) > limit {
				start = len(s.History) - limit
			}
			fmt.Println("    recent commands:")
			for _, h := range s.History[start:] {
				fmt.Printf("      $ %s\n", sanitizeForDisplay(h))
			}
		}
		if !s.IsIdle() {
			if resumer.IsClaudePane(s) {
				fmt.Printf("    resume conversation: %s\n", resumer.ResumeCommand(s))
			} else {
				fmt.Printf("    was running: %s\n", sanitizeForDisplay(s.Command))
			}
		}
		fmt.Println()
	}

	path, err := writeResumeScript(snap)
	if err != nil {
		fmt.Printf("Could not write resume script: %v\n", err)
		return nil
	}
	fmt.Println("Ready to paste, or source the resume script:")
	fmt.Printf("  source %s\n", path)
	return nil
}

// writeResumeScript writes ~/.local/share/thaw/resume.sh — a small script
// that cd's to the primary pane's cwd and prints the context note.
func writeResumeScript(snap *models.Snapshot) (string, error) {
	primary := PrimarySession(snap)
	dir, err := thawDataDir()
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "# thaw resume — snapshot #%d, captured %s\n",
		snap.ID, snap.CreatedAt.Format("2006-01-02 15:04"))
	fmt.Fprintf(&b, "cd %s\n", esc(primary.CWD))
	fmt.Fprintf(&b, "echo %s\n", esc(Summarize(snap, time.Now())))
	if primary.Git != nil && primary.Git.Branch != "" {
		fmt.Fprintf(&b, "echo %s\n", esc("branch was: "+primary.Git.Branch))
	}
	if !primary.IsIdle() {
		fmt.Fprintf(&b, "echo %s\n", esc("run: "+sanitizeForDisplay(primary.Command)))
	}

	path := filepath.Join(dir, "resume.sh")
	if err := os.WriteFile(path, []byte(b.String()), 0700); err != nil {
		return "", err
	}
	return path, nil
}

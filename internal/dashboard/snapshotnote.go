package dashboard

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/scrub"
	"github.com/joecattt/thaw/pkg/models"
)

// Per-snapshot AI summaries: the heuristic label (sessionFingerprint's
// Command/Intent) plateaus at generic values like "development" — real, but
// not specific. This adds an optional per-snapshot summary via a
// user-configured external command (THAW_SUMMARIZE_CMD), cached to disk so
// it's generated once and reused, not recomputed on every dashboard render.
//
// Cache format: JSONL, one {"id":123,"summary":"..."} per line, append-only
// (same shape as the rest of thaw's local stores — never rewritten in
// place, just appended and last-write-wins on read).

type snapshotNoteEntry struct {
	ID      int    `json:"id"`
	Summary string `json:"summary"`
}

func snapshotNotesPath() (string, error) {
	dir, err := dataDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "snapshot-summaries.jsonl"), nil
}

func dataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "thaw"), nil
}

// loadSnapshotNotes reads the whole cache into memory — small, append-only
// file, this is cheap even at a few thousand lines.
func loadSnapshotNotes() map[int]string {
	out := map[int]string{}
	path, err := snapshotNotesPath()
	if err != nil {
		return out
	}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1<<20)
	for sc.Scan() {
		var e snapshotNoteEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil && e.Summary != "" {
			out[e.ID] = e.Summary // last write wins — later lines overwrite earlier ones for the same id
		}
	}
	return out
}

func appendSnapshotNote(id int, summary string) {
	path, err := snapshotNotesPath()
	if err != nil {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	line, _ := json.Marshal(snapshotNoteEntry{ID: id, Summary: summary})
	f.Write(append(line, '\n'))
}

// summarizeSnapshot asks the user's configured summarizer command
// (THAW_SUMMARIZE_CMD — see summarizeViaCmd) for one concrete sentence about
// what a snapshot's sessions were actually doing, from CWDs/commands/branches
// only (never session History or Output — that's raw terminal content,
// out of scope for a metadata summary) — scrubbed defensively regardless,
// same scrub.Text() call thaw session-note uses. Best-effort: returns ""
// when no summarizer is configured or on any failure.
func summarizeSnapshot(sessions []models.Session) string {
	var b strings.Builder
	seen := map[string]bool{}
	for _, s := range sessions {
		var line []string
		if s.CWD != "" {
			line = append(line, "dir="+filepath.Base(s.CWD))
		}
		if s.Command != "" {
			line = append(line, "cmd="+s.Command)
		}
		if s.Git != nil && s.Git.Branch != "" {
			line = append(line, "branch="+s.Git.Branch)
		}
		if len(line) == 0 {
			continue
		}
		key := strings.Join(line, " ")
		if seen[key] {
			continue
		}
		seen[key] = true
		b.WriteString("- " + key + "\n")
	}
	if b.Len() == 0 {
		return ""
	}

	prompt := "Terminal sessions captured in one snapshot:\n" + scrub.Text(b.String()) +
		"\nWrite ONE short, specific phrase (under 12 words) naming what this snapshot was actually " +
		"doing — real project/activity, not a restatement of the raw fields above. Return ONLY that phrase, no preamble, no quotes."

	return summarizeViaCmd(prompt)
}

// summarizeViaCmd runs the user's configured summarizer (THAW_SUMMARIZE_CMD,
// a shell command that reads a prompt on stdin and prints a summary on
// stdout — e.g. any local or remote LLM CLI). Empty env var means the
// feature is entirely off: no exec, no external call, "" back. Hard
// deadline regardless — a dashboard render can't be allowed to hang
// indefinitely on a slow summarizer; better to skip a summary than block
// the whole page.
func summarizeViaCmd(prompt string) string {
	cmdStr := os.Getenv("THAW_SUMMARIZE_CMD")
	if cmdStr == "" {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	cmd.Stdin = strings.NewReader(prompt)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/scrub"
)

// sessionNoteCmd records project session boundaries and, optionally, an AI
// write-up of what happened in a session.
//
// Design note: a "start" write-up has nothing real to summarize yet — no
// activity has happened this session. Generating one would mean paying for
// an API call to say nothing. So "start" surfaces the last real "end" note
// for this project (free — just a log lookup) instead of firing a new one;
// "end" is the one that actually calls out to the user's configured
// summarizer command (THAW_SUMMARIZE_CMD — empty means the write-up feature
// is entirely off) to write one real sentence about what happened, using
// actual git activity since the session started. Cost-bounded: skips
// silently if the session was too short (<3min) or nothing changed (no
// commits, nothing dirty) — most cd's are not project sessions worth a
// write-up.
func sessionNoteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "session-note <start|end> [dir]",
		Short:  "Record a project session boundary; on end, an AI summary of what happened",
		Hidden: true,
		Args:   cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 1 {
				dir = args[1]
			}
			absDir, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			switch args[0] {
			case "start":
				return recordSessionStart(absDir)
			case "end":
				return recordSessionEnd(absDir)
			default:
				return fmt.Errorf("event must be start or end")
			}
		},
	}
	return cmd
}

type sessionTrack struct {
	StartTime int64  `json:"start_time"`
	StartHead string `json:"start_head"`
}

type sessionNote struct {
	TS      string `json:"ts"`
	Project string `json:"project"`
	Event   string `json:"event"` // start|end
	Note    string `json:"note"`
}

func sessionStateDir() string {
	d := filepath.Join(os.Getenv("HOME"), ".local", "state", "thaw")
	os.MkdirAll(d, 0755)
	return d
}

func sessionTrackPath() string {
	return filepath.Join(sessionStateDir(), "session-tracking.json")
}

func sessionNotesPath() string {
	d := filepath.Join(os.Getenv("HOME"), ".local", "share", "thaw")
	os.MkdirAll(d, 0755)
	return filepath.Join(d, "session-notes.jsonl")
}

func loadSessionTracking() map[string]sessionTrack {
	m := map[string]sessionTrack{}
	data, err := os.ReadFile(sessionTrackPath())
	if err != nil {
		return m
	}
	json.Unmarshal(data, &m)
	return m
}

func saveSessionTracking(m map[string]sessionTrack) {
	data, err := json.Marshal(m)
	if err != nil {
		return
	}
	os.WriteFile(sessionTrackPath(), data, 0644)
}

func gitHead(dir string) string {
	out, err := exec.Command("git", "-C", dir, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// recordSessionStart stamps this project's session-start marker (time +
// HEAD, so "end" can compute exactly what happened) and prints the last
// real end-note for this project, if one exists — free, no API call, and
// this IS "picking up where you left off."
func recordSessionStart(dir string) error {
	m := loadSessionTracking()
	m[dir] = sessionTrack{StartTime: time.Now().Unix(), StartHead: gitHead(dir)}
	saveSessionTracking(m)

	note, ts := lastEndNote(dir)
	if note != "" {
		fmt.Printf("thaw: last time in %s (%s) — %s\n", filepath.Base(dir), ago(ts), note)
	}
	return nil
}

// recordSessionEnd is the one that costs something: if the session was
// real (long enough, something actually changed), asks the configured
// summarizer for one real sentence about what happened, from actual git
// activity — not a generic stat line.
func recordSessionEnd(dir string) error {
	m := loadSessionTracking()
	track, ok := m[dir]
	if !ok {
		return nil // no matching start — nothing to close out
	}
	delete(m, dir)
	saveSessionTracking(m)

	// No summarizer configured = the write-up feature is off entirely.
	// Cheap-exit here, before any git exec, so the default shell hook
	// costs nothing beyond the tracking-file update above.
	if os.Getenv("THAW_SUMMARIZE_CMD") == "" {
		return nil
	}

	elapsed := time.Now().Unix() - track.StartTime
	if elapsed < 180 {
		return nil // too short to be a real session — cost control
	}

	head := gitHead(dir)
	var commitLog, diffStat string
	if track.StartHead != "" && head != "" && track.StartHead != head {
		out, _ := exec.Command("git", "-C", dir, "log", "--oneline", track.StartHead+".."+head).Output()
		commitLog = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "-C", dir, "diff", "--stat").Output(); err == nil {
		diffStat = strings.TrimSpace(string(out))
	}
	if out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output(); err == nil && strings.TrimSpace(string(out)) != "" && diffStat == "" {
		diffStat = strings.TrimSpace(string(out)) // untracked-only changes: diff --stat misses these
	}
	if commitLog == "" && diffStat == "" {
		return nil // nothing actually happened — not worth a note or a summarizer call
	}

	// Everything sent to the external summarizer is scrubbed first — every
	// other place this codebase touches command history or output routes
	// through internal/scrub (capture.go, history.go), and this call site
	// is no exception. scrub.Text catches known secret SHAPES (AWS keys,
	// JWTs, private key blocks, key=value secrets) — it does NOT know a
	// filename or commit message is itself sensitive, so this is a real
	// mitigation, not a complete guarantee.
	note := writeUpViaCmd(filepath.Base(dir), scrub.Text(commitLog), scrub.Text(diffStat))
	if note == "" {
		return nil // summarizer unreachable or empty answer — silent skip, fail closed
	}
	appendSessionNote(sessionNote{
		TS: time.Now().UTC().Format(time.RFC3339), Project: dir, Event: "end", Note: note,
	})
	return nil
}

// writeUpViaCmd runs the user's configured summarizer (THAW_SUMMARIZE_CMD, a
// shell command that reads a prompt on stdin and prints a summary on stdout
// — e.g. any local or remote LLM CLI). The caller has already checked the
// env var is set; empty output or any error just means no note.
func writeUpViaCmd(projectName, commitLog, diffStat string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Project: %s\n\n", projectName)
	if commitLog != "" {
		fmt.Fprintf(&b, "Commits made this session:\n%s\n\n", commitLog)
	}
	if diffStat != "" {
		fmt.Fprintf(&b, "Currently uncommitted:\n%s\n\n", diffStat)
	}
	b.WriteString("Write ONE concise, specific sentence (under 25 words) summarizing what actually " +
		"got done — name the real thing that changed, not a generic restatement of the stats above. " +
		"Return ONLY that sentence, no preamble, no quotes.")

	cmd := exec.Command("sh", "-c", os.Getenv("THAW_SUMMARIZE_CMD"))
	cmd.Stdin = strings.NewReader(b.String())
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func appendSessionNote(n sessionNote) {
	f, err := os.OpenFile(sessionNotesPath(), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return
	}
	defer f.Close()
	data, err := json.Marshal(n)
	if err != nil {
		return
	}
	f.Write(data)
	f.Write([]byte("\n"))
}

// lastEndNote returns the most recent "end" note for this project dir and
// its timestamp, or "" if none exists yet.
func lastEndNote(dir string) (string, time.Time) {
	f, err := os.Open(sessionNotesPath())
	if err != nil {
		return "", time.Time{}
	}
	defer f.Close()
	var note string
	var ts time.Time
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		var n sessionNote
		if json.Unmarshal(sc.Bytes(), &n) != nil || n.Event != "end" || n.Project != dir {
			continue
		}
		if t, err := time.Parse(time.RFC3339, n.TS); err == nil {
			note, ts = n.Note, t
		}
	}
	return note, ts
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

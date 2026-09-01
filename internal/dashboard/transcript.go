package dashboard

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// The process-tree data (sessionFingerprint) has a real ceiling for a
// common workflow: one long-lived terminal, so every capture shows the same
// top-level command (the shell's own tab title) no matter what was actually
// being worked on.
//
// This pulls a stronger signal instead: Claude Code's own transcript files
// (~/.claude/projects/<path-with-slashes-as-dashes>/<session-uuid>.jsonl)
// already contain exactly what was asked in that terminal. Quoting the
// actual most-recent user message near a snapshot's timestamp is primary-
// source evidence, not a reconstruction — no AI backend needed, and it's
// the literal thing that answers "what was this about."

// claudeProjectDir maps an absolute path to Claude Code's own transcript
// directory naming convention (verified against ~/.claude/projects/ on
// disk: "/" becomes "-", no other encoding).
func claudeProjectDir(root string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "projects", strings.ReplaceAll(root, "/", "-")), nil
}

// RecentUserMessage finds the transcript file closest in time to `at`
// (matched by file mtime — the file that was being actively written to
// right around when the snapshot was taken) and returns the last real
// user-authored message in it, truncated for display. Best-effort: returns
// "" on anything missing or unreadable, never an error the caller has to
// handle.
func RecentUserMessage(root string, at time.Time) string {
	dir, err := claudeProjectDir(root)
	if err != nil {
		return ""
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	type cand struct {
		path string
		diff time.Duration
	}
	var cands []cand
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		d := info.ModTime().Sub(at)
		if d < 0 {
			d = -d
		}
		// Only consider transcripts whose last write was within 6h of the
		// snapshot — otherwise the "closest" file could be a session from
		// a completely different day that just happens to be nearest.
		if d > 6*time.Hour {
			continue
		}
		cands = append(cands, cand{e.Name(), d})
	}
	if len(cands) == 0 {
		return ""
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].diff < cands[j].diff })

	return lastUserText(filepath.Join(dir, cands[0].path))
}

// lastUserText reads a transcript JSONL file and returns the last real
// user-authored text message in it. Reads only the tail (last 400KB) of
// the file — these transcripts run to multi-MB, and the most recent
// message is always near the end.
func lastUserText(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return ""
	}
	const tailBytes = 400 * 1024
	if info.Size() > tailBytes {
		if _, err := f.Seek(-tailBytes, io.SeekEnd); err != nil {
			return ""
		}
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 4<<20)

	type msg struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"message"`
	}
	var last string
	for sc.Scan() {
		var m msg
		if json.Unmarshal(sc.Bytes(), &m) != nil {
			continue
		}
		if m.Type != "user" || m.Message.Role != "user" {
			continue
		}
		text := extractText(m.Message.Content)
		text = strings.TrimSpace(text)
		if text == "" || looksAutomated(text) {
			continue // harness/automation-injected content, not something the human actually typed
		}
		last = text
	}
	if last == "" {
		return ""
	}
	last = strings.Join(strings.Fields(last), " ")
	if len(last) > 140 {
		last = last[:140] + "…"
	}
	return last
}

// automatedPrefixes catches the recognizable shapes of non-human turns that
// still land with role=="user" in a transcript: harness system-reminders,
// slash-command scaffolding, and cron/unattended-agent prompts — all real
// text, just not something typed by a person, so not a useful "what was
// this about" answer.
var automatedPrefixes = []string{
	"<system-reminder", "<command-", "<local-command-caveat",
	"You are running as an unattended",
	"Base directory for this skill",
}

func looksAutomated(text string) bool {
	for _, p := range automatedPrefixes {
		if strings.HasPrefix(text, p) {
			return true
		}
	}
	return false
}

// extractText pulls plain text out of a Claude Code transcript message's
// content field, which is either a bare string or an array of typed
// content blocks ({"type":"text","text":"..."}, tool_result, etc.) — this
// only wants the human-typed text, skips everything else (tool calls,
// tool results, images).
func extractText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		for _, b := range blocks {
			if b.Type == "text" && b.Text != "" {
				return b.Text
			}
		}
	}
	return ""
}

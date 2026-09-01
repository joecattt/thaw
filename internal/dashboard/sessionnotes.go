package dashboard

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// sessionNote mirrors cmd/thaw's sessionNote struct — duplicated rather than
// imported since that one lives in package main (cmd/thaw), not importable
// from here. Same file format (~/.local/share/thaw/session-notes.jsonl),
// read-only from this side.
type sessionNote struct {
	TS      string `json:"ts"`
	Project string `json:"project"`
	Event   string `json:"event"`
	Note    string `json:"note"`
}

// LastProjectNote returns the most recent AI-written session-end note for
// a project root (see `thaw session-note`), or "" if none exists yet —
// either the feature hasn't fired for this project, or K3 was down every
// time it tried (fails closed, no note gets written on failure).
func LastProjectNote(dir string) (string, time.Time) {
	path := filepath.Join(os.Getenv("HOME"), ".local", "share", "thaw", "session-notes.jsonl")
	f, err := os.Open(path)
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

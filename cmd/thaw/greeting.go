package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/restore"
	"github.com/joecattt/thaw/internal/upstream"
	"github.com/joecattt/thaw/pkg/models"
)

// isTTY reports whether the file is attached to a terminal.
func isTTY(f *os.File) bool {
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// greeting builds the "where was I" paragraph for bare `thaw`, appending
// upstream deltas when they arrive within a short budget.
func greeting(snap *models.Snapshot) string {
	text := restore.Summarize(snap, time.Now())
	if delta := upstreamDelta(snap); delta != "" {
		text += " " + delta
	}
	return text
}

// upstreamDelta checks the primary session's repo for upstream changes.
// It gives up silently after a short budget so the greeting never blocks.
func upstreamDelta(snap *models.Snapshot) string {
	primary := restore.PrimarySession(snap)
	if primary == nil || primary.Git == nil || primary.Git.RepoRoot == "" {
		return ""
	}

	ch := make(chan string, 1)
	go func() {
		report, err := upstream.Check(primary.Git.RepoRoot, snap.CreatedAt)
		if err != nil || !report.HasChanges() {
			ch <- ""
			return
		}
		var parts []string
		if report.BehindBy > 0 {
			parts = append(parts, fmt.Sprintf("%d new upstream commit(s) — pull needed", report.BehindBy))
		}
		if report.CIStatus == "failure" {
			parts = append(parts, "CI failed on "+report.Branch)
		}
		if report.ForcePushed {
			parts = append(parts, "upstream was force-pushed")
		}
		if len(parts) == 0 {
			ch <- ""
			return
		}
		ch <- "Upstream: " + strings.Join(parts, "; ") + "."
	}()

	select {
	case delta := <-ch:
		return delta
	case <-time.After(1500 * time.Millisecond):
		return ""
	}
}

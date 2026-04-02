package diff

import (
	"fmt"
	"strings"

	"github.com/joecattt/thaw/pkg/models"
)

// Result holds the comparison between two snapshots.
type Result struct {
	Added     []models.Session // sessions in current but not in previous
	Removed   []models.Session // sessions in previous but not in current
	Changed   []Change         // sessions that exist in both but differ
	Unchanged int              // count of unchanged sessions
}

// Change describes how a session changed between snapshots.
type Change struct {
	Before models.Session
	After  models.Session
	Diffs  []string // human-readable list of what changed
}

// Compare finds differences between a previous snapshot and current live state.
// Matches sessions by CWD (directory is the most stable identifier across restarts).
func Compare(previous, current *models.Snapshot) Result {
	var result Result

	// Index previous sessions by CWD
	prevByCWD := make(map[string]models.Session)
	prevUsed := make(map[string]bool)
	for _, s := range previous.Sessions {
		prevByCWD[s.CWD] = s
	}

	// Walk current sessions
	for _, curr := range current.Sessions {
		prev, found := prevByCWD[curr.CWD]
		if !found {
			result.Added = append(result.Added, curr)
			continue
		}
		prevUsed[curr.CWD] = true

		// Check for changes
		diffs := sessionDiffs(prev, curr)
		if len(diffs) > 0 {
			result.Changed = append(result.Changed, Change{
				Before: prev,
				After:  curr,
				Diffs:  diffs,
			})
		} else {
			result.Unchanged++
		}
	}

	// Find removed sessions (in previous but not current)
	for _, prev := range previous.Sessions {
		if !prevUsed[prev.CWD] {
			result.Removed = append(result.Removed, prev)
		}
	}

	return result
}

// sessionDiffs returns a list of human-readable differences between two sessions.
func sessionDiffs(prev, curr models.Session) []string {
	var diffs []string

	if prev.Command != curr.Command {
		diffs = append(diffs, fmt.Sprintf("command: %s → %s", prev.Command, curr.Command))
	}

	if prev.Status != curr.Status {
		diffs = append(diffs, fmt.Sprintf("status: %s → %s", prev.Status, curr.Status))
	}

	// Git branch change
	prevBranch := ""
	currBranch := ""
	if prev.Git != nil {
		prevBranch = prev.Git.Branch
	}
	if curr.Git != nil {
		currBranch = curr.Git.Branch
	}
	if prevBranch != currBranch && (prevBranch != "" || currBranch != "") {
		diffs = append(diffs, fmt.Sprintf("branch: %s → %s", prevBranch, currBranch))
	}

	// Git dirty state change
	prevDirty := prev.Git != nil && prev.Git.Dirty
	currDirty := curr.Git != nil && curr.Git.Dirty
	if prevDirty != currDirty {
		if currDirty {
			diffs = append(diffs, "uncommitted changes appeared")
		} else {
			diffs = append(diffs, "changes committed")
		}
	}

	// Env var changes
	prevEnvCount := len(prev.EnvDelta.Set)
	currEnvCount := len(curr.EnvDelta.Set)
	if prevEnvCount != currEnvCount {
		diffs = append(diffs, fmt.Sprintf("env vars: %d → %d", prevEnvCount, currEnvCount))
	}

	// Label change
	if prev.Label != curr.Label {
		diffs = append(diffs, fmt.Sprintf("label: %s → %s", prev.Label, curr.Label))
	}

	return diffs
}

// FormatResult returns a human-readable diff report.
func FormatResult(r Result, prevTime, currTime string) string {
	var b strings.Builder

	if len(r.Added) == 0 && len(r.Removed) == 0 && len(r.Changed) == 0 {
		b.WriteString("No changes since last snapshot.\n")
		return b.String()
	}

	b.WriteString(fmt.Sprintf("Comparing: %s → %s\n\n", prevTime, currTime))

	if len(r.Added) > 0 {
		b.WriteString(fmt.Sprintf("+ %d new session(s):\n", len(r.Added)))
		for _, s := range r.Added {
			intent := s.Intent
			if intent == "" {
				intent = s.Command
			}
			b.WriteString(fmt.Sprintf("  + %s — %s\n", s.CWD, intent))
		}
		b.WriteString("\n")
	}

	if len(r.Removed) > 0 {
		b.WriteString(fmt.Sprintf("- %d closed session(s):\n", len(r.Removed)))
		for _, s := range r.Removed {
			intent := s.Intent
			if intent == "" {
				intent = s.Command
			}
			b.WriteString(fmt.Sprintf("  - %s — %s\n", s.CWD, intent))
		}
		b.WriteString("\n")
	}

	if len(r.Changed) > 0 {
		b.WriteString(fmt.Sprintf("~ %d changed session(s):\n", len(r.Changed)))
		for _, c := range r.Changed {
			b.WriteString(fmt.Sprintf("  ~ %s\n", c.After.CWD))
			for _, d := range c.Diffs {
				b.WriteString(fmt.Sprintf("    %s\n", d))
			}
		}
		b.WriteString("\n")
	}

	b.WriteString(fmt.Sprintf("%d unchanged, %d added, %d removed, %d changed\n",
		r.Unchanged, len(r.Added), len(r.Removed), len(r.Changed)))

	return b.String()
}

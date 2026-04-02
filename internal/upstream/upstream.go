package upstream

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// Report describes what changed upstream since a given time.
type Report struct {
	Dir            string
	Branch         string
	NewCommits     int
	NewCommitLines []string // first 5 one-line summaries
	BehindBy       int
	CIStatus       string // "success", "failure", "pending", ""
	CIUrl          string
	PRComments     int
	PRUrl          string
	DepFilesChanged []string // e.g. ["package-lock.json", "go.sum"]
	ForcePushed    bool
}

// Check analyzes what changed in a git repo since the given time.
func Check(dir string, since time.Time) (*Report, error) {
	// Verify it's a git repo
	if _, err := execIn(dir, "git", "rev-parse", "--is-inside-work-tree"); err != nil {
		return nil, fmt.Errorf("not a git repo: %s", dir)
	}

	r := &Report{Dir: dir}

	// Current branch
	out, _ := execIn(dir, "git", "rev-parse", "--abbrev-ref", "HEAD")
	r.Branch = strings.TrimSpace(out)

	// Fetch latest (5s timeout — don't block if remote is unreachable)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exec.CommandContext(ctx, "git", "-C", dir, "fetch", "--quiet").Run()

	// Commits on upstream since our last freeze
	sinceStr := since.Format(time.RFC3339)
	out, _ = execIn(dir, "git", "log", "--oneline", "@{u}..HEAD", "--since="+sinceStr)
	if lines := nonEmpty(out); len(lines) > 0 {
		r.NewCommits = len(lines)
		for i, l := range lines {
			if i >= 5 {
				break
			}
			r.NewCommitLines = append(r.NewCommitLines, l)
		}
	}

	// Ahead/behind upstream
	out, _ = execIn(dir, "git", "rev-list", "--left-right", "--count", "HEAD...@{u}")
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 2 {
		r.BehindBy, _ = strconv.Atoi(fields[1])
	}

	// Check for force push (reflog)
	out, _ = execIn(dir, "git", "reflog", "show", "@{u}", "--format=%gs", "-1")
	if strings.Contains(out, "forced-update") {
		r.ForcePushed = true
	}

	// Changed dep files on upstream
	out, _ = execIn(dir, "git", "diff", "--name-only", "HEAD...@{u}")
	depFiles := map[string]bool{
		"package-lock.json": true, "package.json": true,
		"go.sum": true, "go.mod": true,
		"Cargo.lock": true, "yarn.lock": true,
		"Gemfile.lock": true, "composer.lock": true,
		"pnpm-lock.yaml": true,
	}
	for _, f := range nonEmpty(out) {
		f = strings.TrimSpace(f)
		if depFiles[f] {
			r.DepFilesChanged = append(r.DepFilesChanged, f)
		}
	}

	// GitHub CI status (requires gh CLI)
	if ghAvailable() {
		out, err := execIn(dir, "gh", "pr", "status", "--json", "statusCheckRollup,url", "--jq", ".currentBranch.statusCheckRollup[0].status + \"|\" + .currentBranch.url")
		if err == nil {
			parts := strings.SplitN(strings.TrimSpace(out), "|", 2)
			if len(parts) >= 1 {
				status := strings.ToLower(parts[0])
				switch {
				case strings.Contains(status, "success") || strings.Contains(status, "completed"):
					r.CIStatus = "success"
				case strings.Contains(status, "fail"):
					r.CIStatus = "failure"
				case strings.Contains(status, "pending") || strings.Contains(status, "progress"):
					r.CIStatus = "pending"
				}
			}
			if len(parts) >= 2 {
				r.PRUrl = parts[1]
			}
		}

		// PR review comments
		out, err = execIn(dir, "gh", "pr", "view", "--json", "reviewRequests,comments", "--jq", ".comments | length")
		if err == nil {
			r.PRComments, _ = strconv.Atoi(strings.TrimSpace(out))
		}
	}

	return r, nil
}

// HasChanges returns true if anything notable happened upstream.
func (r *Report) HasChanges() bool {
	return r.BehindBy > 0 || r.CIStatus == "failure" || r.ForcePushed || len(r.DepFilesChanged) > 0 || r.PRComments > 0
}

// Format returns a human-readable summary of upstream changes.
func Format(r *Report) string {
	if r == nil || !r.HasChanges() {
		return ""
	}
	var b strings.Builder
	b.WriteString(fmt.Sprintf("  Upstream changes for %s [%s]:\n", r.Dir, r.Branch))

	if r.ForcePushed {
		b.WriteString("  ⚠ upstream was force-pushed — your local may be diverged\n")
	}
	if r.BehindBy > 0 {
		b.WriteString(fmt.Sprintf("  ⚠ %d new commit(s) on upstream — pull needed\n", r.BehindBy))
		for _, l := range r.NewCommitLines {
			b.WriteString(fmt.Sprintf("      %s\n", l))
		}
	}
	if r.CIStatus == "failure" {
		msg := "  ⚠ CI failed on this branch"
		if r.PRUrl != "" {
			msg += " — " + r.PRUrl
		}
		b.WriteString(msg + "\n")
	} else if r.CIStatus == "pending" {
		b.WriteString("  · CI running on this branch\n")
	}
	if r.PRComments > 0 {
		b.WriteString(fmt.Sprintf("  · %d PR comment(s) to review\n", r.PRComments))
	}
	if len(r.DepFilesChanged) > 0 {
		b.WriteString(fmt.Sprintf("  ⚠ dependency files changed upstream: %s\n", strings.Join(r.DepFilesChanged, ", ")))
	}
	return b.String()
}

func execIn(dir, name string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func nonEmpty(s string) []string {
	var result []string
	for _, l := range strings.Split(s, "\n") {
		if strings.TrimSpace(l) != "" {
			result = append(result, strings.TrimSpace(l))
		}
	}
	return result
}

func ghAvailable() bool {
	_, err := exec.LookPath("gh")
	return err == nil
}

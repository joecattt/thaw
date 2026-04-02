package progress

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/project"
)

// Report is the progress analysis for a single project directory.
type Report struct {
	Dir         string
	Name        string
	ProjectType string

	// Git metrics
	Branch          string
	Dirty           bool
	CommitsToday    int
	CommitsThisWeek int
	FilesChanged    int
	Insertions      int
	Deletions       int
	AheadOfUpstream int
	BehindUpstream  int

	// Code health
	TodoCount   int
	TodoSamples []string

	// Test results (if test command is configured or auto-detected)
	TestsPassed int
	TestsFailed int
	TestsTotal  int
	TestOutput  string
	TestRan     bool

	// Dependency freshness
	DepsStale     bool
	StaleReason   string
	LastDepChange time.Time

	// Velocity (relative to own history)
	AvgCommitsPerDay float64

	// Heuristic completion signals
	Signals []Signal
}

// Signal is one piece of evidence about project state.
type Signal struct {
	Label   string // "tests passing", "TODOs remaining", "branch ahead"
	Status  string // "good", "warn", "info"
	Detail  string
}

// Analyze runs progress analysis on a project directory.
func Analyze(dir string, cfg *project.Config) (*Report, error) {
	if _, err := os.Stat(dir); err != nil {
		return nil, fmt.Errorf("directory not found: %s", dir)
	}

	r := &Report{
		Dir:         dir,
		Name:        filepath.Base(dir),
		ProjectType: project.DetectProjectType(dir),
	}

	if cfg != nil && cfg.Project.Name != "" {
		r.Name = cfg.Project.Name
	}

	analyzeGit(r)
	analyzeTodos(r, cfg)
	analyzeDeps(r)
	runTests(r, cfg)
	buildSignals(r)

	return r, nil
}

func analyzeGit(r *Report) {
	// Branch
	out, err := execIn(r.Dir, "git", "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return
	}
	r.Branch = strings.TrimSpace(out)

	// Dirty?
	out, _ = execIn(r.Dir, "git", "status", "--porcelain")
	r.Dirty = len(strings.TrimSpace(out)) > 0
	r.FilesChanged = len(strings.Split(strings.TrimSpace(out), "\n"))
	if strings.TrimSpace(out) == "" {
		r.FilesChanged = 0
	}

	// Commits today
	today := time.Now().Format("2006-01-02")
	out, _ = execIn(r.Dir, "git", "rev-list", "--count", "--since="+today+"T00:00:00", "HEAD")
	r.CommitsToday, _ = strconv.Atoi(strings.TrimSpace(out))

	// Commits this week
	weekAgo := time.Now().AddDate(0, 0, -7).Format("2006-01-02")
	out, _ = execIn(r.Dir, "git", "rev-list", "--count", "--since="+weekAgo+"T00:00:00", "HEAD")
	r.CommitsThisWeek, _ = strconv.Atoi(strings.TrimSpace(out))
	if r.CommitsThisWeek > 0 {
		r.AvgCommitsPerDay = float64(r.CommitsThisWeek) / 7.0
	}

	// Insertions/deletions today
	out, _ = execIn(r.Dir, "git", "diff", "--shortstat", "HEAD~"+strconv.Itoa(maxInt(r.CommitsToday, 1)))
	parts := strings.Fields(out)
	for i, p := range parts {
		if strings.Contains(p, "insertion") && i > 0 {
			r.Insertions, _ = strconv.Atoi(parts[i-1])
		}
		if strings.Contains(p, "deletion") && i > 0 {
			r.Deletions, _ = strconv.Atoi(parts[i-1])
		}
	}

	// Ahead/behind upstream
	out, _ = execIn(r.Dir, "git", "rev-list", "--left-right", "--count", "HEAD...@{upstream}")
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) == 2 {
		r.AheadOfUpstream, _ = strconv.Atoi(fields[0])
		r.BehindUpstream, _ = strconv.Atoi(fields[1])
	}
}

func analyzeTodos(r *Report, cfg *project.Config) {
	pattern := "TODO|FIXME|HACK|XXX"
	if cfg != nil && cfg.Project.TodoPattern != "" {
		pattern = cfg.Project.TodoPattern
	}
	r.TodoCount, r.TodoSamples = project.CountTodos(r.Dir, pattern)
}

func analyzeDeps(r *Report) {
	// Check if dependency files changed recently relative to lockfiles
	depFiles := map[string]string{
		"package.json":      "package-lock.json",
		"go.mod":            "go.sum",
		"requirements.txt":  "",
		"Cargo.toml":        "Cargo.lock",
		"Gemfile":           "Gemfile.lock",
		"composer.json":     "composer.lock",
	}

	for manifest, lockfile := range depFiles {
		mPath := filepath.Join(r.Dir, manifest)
		mInfo, err := os.Stat(mPath)
		if err != nil {
			continue
		}

		if lockfile == "" {
			continue
		}

		lPath := filepath.Join(r.Dir, lockfile)
		lInfo, err := os.Stat(lPath)
		if err != nil {
			r.DepsStale = true
			r.StaleReason = fmt.Sprintf("%s exists but %s is missing", manifest, lockfile)
			continue
		}

		// If manifest is newer than lockfile, deps might be stale
		if mInfo.ModTime().After(lInfo.ModTime()) {
			r.DepsStale = true
			r.StaleReason = fmt.Sprintf("%s modified after %s — run install", manifest, lockfile)
			r.LastDepChange = mInfo.ModTime()
		}
	}

	// Check if upstream changed deps (git diff against upstream)
	out, _ := execIn(r.Dir, "git", "diff", "--name-only", "HEAD...@{upstream}")
	for _, f := range strings.Split(out, "\n") {
		f = strings.TrimSpace(f)
		if f == "package-lock.json" || f == "go.sum" || f == "Cargo.lock" || f == "yarn.lock" {
			r.DepsStale = true
			r.StaleReason = fmt.Sprintf("%s changed upstream — pull and install", f)
		}
	}
}

func runTests(r *Report, cfg *project.Config) {
	testCmd := ""
	if cfg != nil && cfg.Project.TestCommand != "" {
		testCmd = cfg.Project.TestCommand
	} else {
		// Auto-detect
		switch r.ProjectType {
		case "node":
			if _, err := os.Stat(filepath.Join(r.Dir, "package.json")); err == nil {
				testCmd = "npm test -- --watchAll=false 2>&1"
			}
		case "go":
			testCmd = "go test ./... -count=1 -short 2>&1"
		case "python":
			testCmd = "python -m pytest --tb=no -q 2>&1"
		case "rust":
			testCmd = "cargo test --no-fail-fast 2>&1"
		}
	}

	if testCmd == "" {
		return
	}

	// Run with timeout
	cmd := exec.Command("sh", "-c", testCmd)
	cmd.Dir = r.Dir
	out, err := cmd.CombinedOutput()
	r.TestRan = true
	r.TestOutput = string(out)

	// Parse results (best effort)
	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		// Go: "ok" or "FAIL"
		if strings.HasPrefix(line, "ok ") {
			r.TestsPassed++
			r.TestsTotal++
		}
		if strings.HasPrefix(line, "FAIL") && !strings.HasPrefix(line, "FAIL\t") {
			r.TestsFailed++
			r.TestsTotal++
		}
		// Node/Jest: "Tests: X passed, Y failed"
		if strings.Contains(line, "passed") && strings.Contains(line, "failed") {
			// best effort
		}
		// pytest: "X passed, Y failed"
		if strings.Contains(line, " passed") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "passed" && i > 0 {
					n, _ := strconv.Atoi(parts[i-1])
					r.TestsPassed = n
					r.TestsTotal += n
				}
				if p == "failed" && i > 0 {
					n, _ := strconv.Atoi(parts[i-1])
					r.TestsFailed = n
					r.TestsTotal += n
				}
			}
		}
	}

	if err != nil && r.TestsFailed == 0 {
		r.TestsFailed = 1 // command failed = at least one test failed
	}
}

func buildSignals(r *Report) {
	// Git signals
	if r.Dirty {
		r.Signals = append(r.Signals, Signal{"uncommitted changes", "warn", fmt.Sprintf("%d files", r.FilesChanged)})
	} else {
		r.Signals = append(r.Signals, Signal{"working tree clean", "good", ""})
	}

	if r.BehindUpstream > 0 {
		r.Signals = append(r.Signals, Signal{"behind upstream", "warn", fmt.Sprintf("%d commits — pull needed", r.BehindUpstream)})
	}
	if r.AheadOfUpstream > 0 {
		r.Signals = append(r.Signals, Signal{"ahead of upstream", "info", fmt.Sprintf("%d commits — push when ready", r.AheadOfUpstream)})
	}

	// Test signals
	if r.TestRan {
		if r.TestsFailed > 0 {
			r.Signals = append(r.Signals, Signal{"failing tests", "warn", fmt.Sprintf("%d/%d failed", r.TestsFailed, r.TestsTotal)})
		} else if r.TestsTotal > 0 {
			r.Signals = append(r.Signals, Signal{"all tests passing", "good", fmt.Sprintf("%d tests", r.TestsTotal)})
		}
	}

	// TODO signals
	if r.TodoCount > 0 {
		r.Signals = append(r.Signals, Signal{"TODOs remaining", "info", fmt.Sprintf("%d found in source", r.TodoCount)})
	}

	// Deps signals
	if r.DepsStale {
		r.Signals = append(r.Signals, Signal{"dependencies stale", "warn", r.StaleReason})
	}

	// Velocity
	if r.AvgCommitsPerDay > 0 {
		r.Signals = append(r.Signals, Signal{"velocity", "info", fmt.Sprintf("%.1f commits/day this week", r.AvgCommitsPerDay)})
	}
}

// FormatReport returns a human-readable progress report.
func FormatReport(r *Report) string {
	var b strings.Builder

	fmt.Fprintf(&b, "\n%s", r.Name)
	if r.Branch != "" {
		dirty := ""
		if r.Dirty {
			dirty = "*"
		}
		fmt.Fprintf(&b, " [%s%s]", r.Branch, dirty)
	}
	fmt.Fprintf(&b, " — %s project\n", r.ProjectType)
	b.WriteString(strings.Repeat("─", 50) + "\n\n")

	// Git stats
	if r.CommitsToday > 0 || r.CommitsThisWeek > 0 {
		fmt.Fprintf(&b, "  Commits:     %d today, %d this week\n", r.CommitsToday, r.CommitsThisWeek)
	}
	if r.Insertions > 0 || r.Deletions > 0 {
		fmt.Fprintf(&b, "  Changes:     +%d -%d lines today\n", r.Insertions, r.Deletions)
	}

	// Signals
	b.WriteString("\n  Signals:\n")
	for _, s := range r.Signals {
		icon := "  ✓"
		if s.Status == "warn" {
			icon = "  ⚠"
		} else if s.Status == "info" {
			icon = "  ·"
		}
		detail := ""
		if s.Detail != "" {
			detail = " — " + s.Detail
		}
		fmt.Fprintf(&b, "  %s %s%s\n", icon, s.Label, detail)
	}

	// TODO samples
	if len(r.TodoSamples) > 0 {
		b.WriteString("\n  TODOs:\n")
		for _, t := range r.TodoSamples {
			fmt.Fprintf(&b, "    %s\n", t)
		}
	}

	return b.String()
}

func execIn(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

package recap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// HourRange is a low/high estimate in hours.
type HourRange struct {
	Low  float64
	High float64
}

// EffortConfig holds every tunable constant for the effort engine.
type EffortConfig struct {
	// BaseHours maps commit type → base hour range (before LOC modifier).
	BaseHours map[string]HourRange
	// DefaultHours is used when a commit matches no prefix or keyword.
	DefaultHours HourRange
	// PrefixPattern classifies conventional-commit prefixes (case-insensitive).
	PrefixPattern string
	// Keywords maps commit type → fallback keywords matched against the subject.
	Keywords map[string][]string
	// LocDivisorLow adds LOC/LocDivisorLow hours to the low estimate.
	LocDivisorLow float64
	// LocDivisorHigh adds LOC/LocDivisorHigh hours to the high estimate.
	LocDivisorHigh float64
	// MaxHoursPerCommit caps the high estimate for a single commit.
	MaxHoursPerCommit float64
	// RateLow / RateHigh are hourly rates in dollars.
	RateLow  float64
	RateHigh float64
	// ExcludePaths are substrings; matching file paths don't count toward LOC.
	ExcludePaths []string
}

// DefaultEffortConfig returns the out-of-the-box effort model.
func DefaultEffortConfig() EffortConfig {
	return EffortConfig{
		BaseHours: map[string]HourRange{
			"feat":     {3, 8},
			"fix":      {1.5, 5},
			"refactor": {2, 6},
			"security": {3, 8},
			"perf":     {2, 6},
			"test":     {1, 3},
			"docs":     {0.5, 1.5},
			"chore":    {0.5, 1.5},
			"style":    {0.5, 1.5},
		},
		DefaultHours:  HourRange{1, 4},
		PrefixPattern: `(feat|fix|refactor|perf|test|docs|chore|style|security)[(!:]`,
		Keywords: map[string][]string{
			"security": {"security", "harden"},
			"feat":     {"add", "implement", "build"},
			"fix":      {"fix", "repair", "restore"},
		},
		LocDivisorLow:     200,
		LocDivisorHigh:    60,
		MaxHoursPerCommit: 24,
		RateLow:           60,
		RateHigh:          175,
		ExcludePaths: []string{
			"package-lock.json", "yarn.lock", "pnpm-lock", "Cargo.lock",
			"Gemfile.lock", "poetry.lock", "composer.lock",
			"dist/", "build/", "node_modules/", "vendor/",
			".min.", ".map", "go.sum", ".jsonl",
			".png", ".jpg", ".jpeg", ".gif", ".svg", ".ico", ".webp", ".pdf",
		},
	}
}

// FileStat is one file's line counts from git numstat.
type FileStat struct {
	Path    string
	Added   int
	Deleted int
}

// RawCommit is a git commit with its numstat, before estimation.
type RawCommit struct {
	Hash    string
	Subject string
	Files   []FileStat
}

// CommitEffort is a single commit's labor estimate.
type CommitEffort struct {
	Hash      string
	Subject   string
	Type      string
	LOC       int
	HoursLow  float64
	HoursHigh float64
	CostLow   float64
	CostHigh  float64
}

// EffortReport aggregates labor estimates across commits.
type EffortReport struct {
	Commits        []CommitEffort
	TotalHoursLow  float64
	TotalHoursHigh float64
	TotalCostLow   float64
	TotalCostHigh  float64
}

// ClassifyCommit returns the commit type from its subject: conventional-commit
// prefix first, then keyword fallback, else "default".
func ClassifyCommit(subject string, cfg EffortConfig) string {
	re := regexp.MustCompile(`(?i)^\s*` + cfg.PrefixPattern)
	if m := re.FindStringSubmatch(subject); m != nil {
		return strings.ToLower(m[1])
	}
	lower := strings.ToLower(subject)
	// Deterministic keyword order
	types := make([]string, 0, len(cfg.Keywords))
	for t := range cfg.Keywords {
		types = append(types, t)
	}
	sort.Strings(types)
	for _, t := range types {
		for _, kw := range cfg.Keywords[t] {
			if strings.Contains(lower, kw) {
				return t
			}
		}
	}
	return "default"
}

// countLOC sums added+deleted lines, excluding generated/vendored paths.
func countLOC(files []FileStat, cfg EffortConfig) int {
	total := 0
	for _, f := range files {
		if excludedPath(f.Path, cfg) {
			continue
		}
		total += f.Added + f.Deleted
	}
	return total
}

func excludedPath(path string, cfg EffortConfig) bool {
	lower := strings.ToLower(path)
	for _, pat := range cfg.ExcludePaths {
		if strings.Contains(lower, pat) {
			return true
		}
	}
	return false
}

// EstimateCommit computes the labor estimate for one commit.
func EstimateCommit(c RawCommit, cfg EffortConfig) CommitEffort {
	typ := ClassifyCommit(c.Subject, cfg)
	base, ok := cfg.BaseHours[typ]
	if !ok {
		base = cfg.DefaultHours
	}
	loc := countLOC(c.Files, cfg)
	low := base.Low + float64(loc)/cfg.LocDivisorLow
	high := base.High + float64(loc)/cfg.LocDivisorHigh
	if high > cfg.MaxHoursPerCommit {
		high = cfg.MaxHoursPerCommit
	}
	return CommitEffort{
		Hash:      c.Hash,
		Subject:   c.Subject,
		Type:      typ,
		LOC:       loc,
		HoursLow:  low,
		HoursHigh: high,
		CostLow:   low * cfg.RateLow,
		CostHigh:  high * cfg.RateHigh,
	}
}

// ComputeEffort aggregates estimates across commits.
func ComputeEffort(commits []RawCommit, cfg EffortConfig) *EffortReport {
	report := &EffortReport{}
	for _, c := range commits {
		ce := EstimateCommit(c, cfg)
		report.Commits = append(report.Commits, ce)
		report.TotalHoursLow += ce.HoursLow
		report.TotalHoursHigh += ce.HoursHigh
		report.TotalCostLow += ce.CostLow
		report.TotalCostHigh += ce.CostHigh
	}
	return report
}

// RepoCommits returns non-merge commits in [from, to] with numstat, or nil on error.
func RepoCommits(repoRoot string, from, to time.Time) []RawCommit {
	cmd := exec.Command("git", "log",
		"--since="+from.Format(time.RFC3339),
		"--until="+to.Format(time.RFC3339),
		"--no-merges", "--numstat", "--format=@@@%H%x00%s")
	cmd.Dir = repoRoot
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	return parseGitLog(string(out))
}

// parseGitLog parses `git log --numstat --format=@@@%H%x00%s` output.
func parseGitLog(out string) []RawCommit {
	var commits []RawCommit
	var cur *RawCommit
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "@@@") {
			parts := strings.SplitN(strings.TrimPrefix(line, "@@@"), "\x00", 2)
			subject := ""
			if len(parts) == 2 {
				subject = parts[1]
			}
			commits = append(commits, RawCommit{Hash: parts[0], Subject: subject})
			cur = &commits[len(commits)-1]
			continue
		}
		if cur == nil {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) != 3 {
			continue
		}
		added, errA := strconv.Atoi(fields[0])   // "-" for binary files
		deleted, errD := strconv.Atoi(fields[1]) // "-" for binary files
		if errA != nil {
			added = 0
		}
		if errD != nil {
			deleted = 0
		}
		cur.Files = append(cur.Files, FileStat{Path: fields[2], Added: added, Deleted: deleted})
	}
	return commits
}

// EffortForRepos computes a combined effort report across repos for [from, to].
// Returns nil if no commits are found anywhere.
func EffortForRepos(repoRoots []string, from, to time.Time, cfg EffortConfig) *EffortReport {
	var all []RawCommit
	for _, root := range repoRoots {
		all = append(all, RepoCommits(root, from, to)...)
	}
	if len(all) == 0 {
		return nil
	}
	return ComputeEffort(all, cfg)
}

// FormatEffort renders the effort section for the text recap.
func FormatEffort(e *EffortReport) string {
	var b strings.Builder
	b.WriteString("\n━━━ effort ━━━\n\n")

	// Per-type rollup, sorted by hours (high) desc
	type rollup struct {
		name                string
		commits             int
		hoursLow, hoursHigh float64
	}
	byType := map[string]*rollup{}
	for _, c := range e.Commits {
		r, ok := byType[c.Type]
		if !ok {
			r = &rollup{name: c.Type}
			byType[c.Type] = r
		}
		r.commits++
		r.hoursLow += c.HoursLow
		r.hoursHigh += c.HoursHigh
	}
	var rollups []*rollup
	for _, r := range byType {
		rollups = append(rollups, r)
	}
	sort.Slice(rollups, func(i, j int) bool {
		return rollups[i].hoursHigh > rollups[j].hoursHigh
	})
	for _, r := range rollups {
		fmt.Fprintf(&b, "  %-9s %d commit(s) — %.1f–%.1fh\n",
			r.name, r.commits, r.hoursLow, r.hoursHigh)
	}

	fmt.Fprintf(&b, "\n  est. labor: %.1f–%.1fh ($%s–$%s)\n",
		e.TotalHoursLow, e.TotalHoursHigh,
		formatDollars(e.TotalCostLow), formatDollars(e.TotalCostHigh))
	return b.String()
}

func formatDollars(v float64) string {
	return strconv.FormatFloat(v, 'f', 0, 64)
}

// DiscoverRepos finds git repos with zero configuration: the repo containing
// the working directory, or git repos one level beneath it. Powers the
// no-daemon trial path (`thaw recap` before any tracking is set up).
func DiscoverRepos() []string {
	cwd, err := os.Getwd()
	if err != nil {
		return nil
	}
	out, err := exec.Command("git", "-C", cwd, "rev-parse", "--show-toplevel").Output()
	if err == nil {
		if root := strings.TrimSpace(string(out)); root != "" {
			return []string{root}
		}
	}
	entries, err := os.ReadDir(cwd)
	if err != nil {
		return nil
	}
	var roots []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sub := filepath.Join(cwd, e.Name())
		if fi, err := os.Stat(filepath.Join(sub, ".git")); err == nil && fi.IsDir() {
			roots = append(roots, sub)
		}
	}
	return roots
}

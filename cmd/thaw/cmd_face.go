package main

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/dashboard"
	"github.com/joecattt/thaw/internal/export"
	"github.com/joecattt/thaw/internal/snapshot"
)

// faceCmd is the "watch face" — glanceable complications, not a report.
// Every number here traces to data thaw already owns (or a live system
// read); nothing invented.
func faceCmd() *cobra.Command {
	var watch bool
	cmd := &cobra.Command{
		Use:   "face",
		Short: "Glanceable live stats — a watch face, not a report",
		Long: `Compact, complication-style readout: current session length, streak,
commits today, dirty files, AI spend today, median context-recovery time,
snapshot freshness, live CPU/memory/disk, and (if THAW_DEADLINES_FILE points
at a deadlines file) the single nearest item.

  thaw face            one snapshot
  thaw face --watch    refreshes every 2s until Ctrl-C`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if watch {
				return watchFace()
			}
			printFace()
			return nil
		},
	}
	cmd.Flags().BoolVar(&watch, "watch", false, "refresh every 2s until Ctrl-C")
	return cmd
}

func watchFace() error {
	for {
		fmt.Print("\033[H\033[2J")
		printFace()
		time.Sleep(2 * time.Second)
	}
}

func printFace() {
	home, _ := os.UserHomeDir()

	// Pull recent snapshots for the git-progress complications (commits
	// today, dirty count) — same Collect() the dashboard uses, just a short
	// window since we only need currently-known projects, not history.
	var commitsToday, dirtyFiles int
	var roots []string
	if store, err := snapshot.Open(); err == nil {
		defer store.Close()
		snaps, err := store.GetRange(time.Now().AddDate(0, 0, -3), time.Now())
		if err == nil {
			rows, _ := dashboard.Collect(export.Flatten(snaps))
			for _, rw := range rows {
				roots = append(roots, rw.Root)
				if rw.Report == nil {
					continue
				}
				commitsToday += rw.Report.CommitsToday
				if rw.Report.Dirty {
					dirtyFiles += rw.Report.FilesChanged
				}
			}
		}
	}

	activeStr := currentSessionLength(filepath.Join(home, ".local", "state", "thaw", "commands.log"))
	streak := ledgerStreak(filepath.Join(home, ".local", "share", "thaw", "ledger.jsonl"))
	spendToday := spendSinceMidnight(roots)
	tcr := tcrMedian(
		filepath.Join(home, ".local", "share", "thaw", "tcr.jsonl"),
		filepath.Join(home, ".local", "state", "thaw", "commands.log"))
	freezeAge := lastFreezeAge()

	fmt.Printf("  ⏱ %-14s 🔥 %-11s ⚙ %d commit(s) today\n",
		activeStr, fmt.Sprintf("%d day streak", streak), commitsToday)
	fmt.Printf("  📝 %-14s 💰 %-11s ↩ %s median resume\n",
		fmt.Sprintf("%d file(s) dirty", dirtyFiles), fmt.Sprintf("$%.2f today", spendToday), tcr)
	if freezeAge != "" {
		fmt.Printf("  ❄ last freeze: %s\n", freezeAge)
	}
	if path := dashboard.DeadlinesFile(); path != "" {
		if line := nextDeadlineLine(path); line != "" {
			fmt.Printf("  ⚠ %s\n", line)
		}
	}
	printSystemSnapshot()
}

// currentSessionLength walks commands.log backward from the end, summing
// consecutive gaps <=30min (same idle threshold the zsh hook already uses
// for its own gap detection) until a bigger gap breaks the run. That sum is
// how long you've been continuously active right now.
func currentSessionLength(path string) string {
	lines := tailLines(path, 500)
	if len(lines) == 0 {
		return "no session data"
	}
	var epochs []int64
	for _, ln := range lines {
		parts := strings.SplitN(ln, "|", 4)
		if len(parts) < 4 {
			continue
		}
		if e, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
			epochs = append(epochs, e)
		}
	}
	if len(epochs) == 0 {
		return "no session data"
	}
	const idleGap = int64(1800) // 30min, matches _thaw_preexec's own idle-gap check
	start := epochs[len(epochs)-1]
	for i := len(epochs) - 1; i > 0; i-- {
		if epochs[i]-epochs[i-1] > idleGap {
			break
		}
		start = epochs[i-1]
	}
	d := time.Since(time.Unix(start, 0))
	if d < 0 {
		return "just started"
	}
	h, m := int(d.Hours()), int(d.Minutes())%60
	if h > 0 {
		return fmt.Sprintf("%dh%dm active", h, m)
	}
	return fmt.Sprintf("%dm active", m)
}

// ledgerStreak counts consecutive calendar days (today backward) present in
// the permanent ledger. A gap of a day with no ledger row ends the streak.
func ledgerStreak(path string) int {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	days := map[string]bool{}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	dateRe := regexp.MustCompile(`"d":\s*"(\d{4}-\d{2}-\d{2})"`)
	for sc.Scan() {
		if m := dateRe.FindStringSubmatch(sc.Text()); m != nil {
			days[m[1]] = true
		}
	}
	streak := 0
	d := time.Now()
	for {
		key := d.Format("2006-01-02")
		if !days[key] {
			// today not yet banked (ledger runs nightly) shouldn't break
			// a real streak — only count it as a break once it's actually
			// missing for a day that's fully over.
			if key == time.Now().Format("2006-01-02") {
				d = d.AddDate(0, 0, -1)
				continue
			}
			break
		}
		streak++
		d = d.AddDate(0, 0, -1)
	}
	return streak
}

func spendSinceMidnight(roots []string) float64 {
	if len(roots) == 0 {
		return 0
	}
	midnight := time.Now().Truncate(24 * time.Hour)
	_, byProject := dashboard.AISpendHistory(roots, midnight)
	var total float64
	for _, c := range byProject {
		total += c
	}
	return total
}

// tcrMedian mirrors thaw-check's tcr_report(): pair each resume-screen view
// (tcr.jsonl) with the first command in that project afterward (commands.log,
// within 2h), and report the median gap. Same algorithm, reimplemented here
// so `thaw face` doesn't have to shell out to a Python sibling tool.
func tcrMedian(tcrPath, cmdPath string) string {
	tf, err := os.Open(tcrPath)
	if err != nil {
		return "no data"
	}
	defer tf.Close()
	type event struct {
		ts   int64
		proj string
	}
	var events []event
	sc := bufio.NewScanner(tf)
	tsRe := regexp.MustCompile(`"ts":\s*(\d+)`)
	pRe := regexp.MustCompile(`"p":\s*"([^"]*)"`)
	for sc.Scan() {
		tm := tsRe.FindStringSubmatch(sc.Text())
		pm := pRe.FindStringSubmatch(sc.Text())
		if tm == nil || pm == nil {
			continue
		}
		ts, _ := strconv.ParseInt(tm[1], 10, 64)
		events = append(events, event{ts, pm[1]})
	}
	if len(events) == 0 {
		return "no data"
	}

	home, _ := os.UserHomeDir()
	cf, err := os.Open(cmdPath)
	if err != nil {
		return "no data"
	}
	defer cf.Close()
	type cmdRow struct {
		ts  int64
		cwd string
	}
	var cmds []cmdRow
	sc2 := bufio.NewScanner(cf)
	sc2.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc2.Scan() {
		parts := strings.SplitN(sc2.Text(), "|", 4)
		if len(parts) < 4 {
			continue
		}
		if ts, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
			cmds = append(cmds, cmdRow{ts, parts[2]})
		}
	}

	var recoveries []int64
	for _, ev := range events {
		projDir := filepath.Join(home, ev.proj)
		for _, c := range cmds {
			if c.ts > ev.ts && c.ts-ev.ts < 7200 && strings.HasPrefix(c.cwd, projDir) {
				recoveries = append(recoveries, c.ts-ev.ts)
				break
			}
		}
	}
	if len(recoveries) == 0 {
		return "no data"
	}
	sort.Slice(recoveries, func(i, j int) bool { return recoveries[i] < recoveries[j] })
	med := recoveries[len(recoveries)/2]
	return fmt.Sprintf("%dm%ds", med/60, med%60)
}

func lastFreezeAge() string {
	store, err := snapshot.Open()
	if err != nil {
		return ""
	}
	defer store.Close()
	snap, err := store.Latest()
	if err != nil || snap == nil {
		return ""
	}
	d := time.Since(snap.CreatedAt)
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}

// nextDeadlineLine reads the first bullet in the deadlines file, display-only
// — no date math, no caching, always a live read. thaw has no way to verify
// the file's accuracy — say so, don't hide it.
var deadlineLineRe = regexp.MustCompile(`^- (\d{4}-\d{2}-\d{2})\s+(.+?)\s*(?:\*\*|\[doc|\[dl|$)`)

func nextDeadlineLine(path string) string {
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "- ") {
			continue
		}
		m := deadlineLineRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		return fmt.Sprintf("next: %s %s (unverified)", m[1], strings.TrimSpace(m[2]))
	}
	return ""
}

func tailLines(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		lines = append(lines, sc.Text())
		if len(lines) > n {
			lines = lines[1:]
		}
	}
	return lines
}

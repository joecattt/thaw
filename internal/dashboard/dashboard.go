package dashboard

import (
	"encoding/json"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/buildinfo"
	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/export"
	"github.com/joecattt/thaw/internal/progress"
	"github.com/joecattt/thaw/internal/project"
	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/pkg/models"
)

// ProjectProgress is real, git-derived progress for one project since it
// was last seen — commits, lines changed, dirty state. No time estimates:
// panel review (2026-08-29) killed the old snapshot-count-times-0.25h guess
// because it measured how often you glanced at a project, not what got done.
type ProjectProgress struct {
	Name     string
	Root     string
	LastSeen time.Time
	Report   *progress.Report // nil if not a git repo or analysis failed
}

// Collect groups records by git repo root (not raw CWD — a project touched
// from two subdirectories is one project, not two) and runs real progress
// analysis on each.
func Collect(records []export.Record) (rows []ProjectProgress, activeDays int) {
	type projState struct {
		Name     string
		Root     string
		LastSeen time.Time
	}
	byRoot := map[string]*projState{}
	var order []string
	dailyMap := make(map[string]bool)
	for _, r := range records {
		dailyMap[r.Timestamp.Format("2006-01-02")] = true
		if r.CWD == "" || r.CWD == "~" {
			// "~" is capture's placeholder for "couldn't read this
			// process's cwd" (internal/capture), not a real path —
			// resolving it against the wrong base directory silently
			// misattributes progress to whatever repo the caller happens
			// to be standing in. Drop it instead of guessing.
			continue
		}
		root := project.FindRepoRoot(r.CWD)
		if root == "" {
			root = r.CWD
		}
		p, ok := byRoot[root]
		if !ok {
			name := r.GroupName
			if name == "" {
				name = filepath.Base(root)
			}
			// 2026-08-29: if $HOME itself has a .git (dotfiles repo, common),
			// FindRepoRoot walks any non-git subdirectory under it all the
			// way up to $HOME — every unrelated ad-hoc session gets dumped
			// into one bucket named after the username, looking like a real
			// project. Confirmed in the wild: 1.4GB of unrelated Claude
			// transcripts under one such bucket vs 6MB for an actual repo.
			// Label it what it is — matches thaw-ledger's own "~ (home
			// ops)" convention — instead of implying it's one coherent thing.
			if home, err := os.UserHomeDir(); err == nil && root == home {
				name = "~ (home ops)"
			}
			p = &projState{Name: name, Root: root, LastSeen: r.Timestamp}
			byRoot[root] = p
			order = append(order, root)
		}
		if r.Timestamp.After(p.LastSeen) {
			p.LastSeen = r.Timestamp
		}
	}

	home, _ := os.UserHomeDir()
	for _, root := range order {
		var rep *progress.Report
		var err error
		if home != "" && root == home {
			// progress.Analyze includes a raw filepath.Walk TODO-scan
			// (internal/project.CountTodos) that does NOT respect
			// .gitignore the way git itself does — on $HOME that's every
			// file under the home directory, 6 levels deep. Measured: 77s.
			// Same root cause as AF-215 (gitignore blindness on ~). Full
			// analysis is wrong for this pseudo-project anyway — it isn't
			// really "a project," just a container. git-status only.
			rep, err = fastHomeStatus(root)
		} else {
			rep, err = progress.Analyze(root, nil)
		}
		if err != nil {
			rep = nil
		}
		rows = append(rows, ProjectProgress{
			Name: byRoot[root].Name, Root: byRoot[root].Root, LastSeen: byRoot[root].LastSeen, Report: rep,
		})
	}
	sort.Slice(rows, func(i, j int) bool {
		return rows[i].LastSeen.After(rows[j].LastSeen)
	})
	return rows, len(dailyMap)
}

// dirtyFileNames names, not just counts — panel feedback (2026-08-29): "35
// files uncommitted" is a real number but too vague to act on. progress.Report
// only stores the count (analyzeGit discards the porcelain output after
// counting lines), so this is a second, cheap `git status --porcelain`
// call just for display, limited to the busiest dirty project, not every one.
func dirtyFileNames(dir string, limit int) []string {
	out, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, line := range strings.Split(strings.TrimRight(string(out), "\n"), "\n") {
		if len(line) < 4 {
			continue
		}
		names = append(names, strings.TrimSpace(line[3:]))
		if len(names) >= limit {
			break
		}
	}
	return names
}

// noCommitsDetail replaces the old flat "no commits this week" with
// whatever real, already-computed specifics exist for this project — panel
// feedback (2026-08-29): a blank status line is vague even when the fields
// to be specific are sitting right there unused.
func noCommitsDetail(r *progress.Report) string {
	var parts []string
	if r.Dirty {
		parts = append(parts, fmt.Sprintf("%d file(s) uncommitted", r.FilesChanged))
	}
	if r.AheadOfUpstream > 0 {
		parts = append(parts, fmt.Sprintf("%d commit(s) ahead of origin, not pushed", r.AheadOfUpstream))
	}
	if r.TodoCount > 0 {
		parts = append(parts, fmt.Sprintf("%d TODO/FIXME in source", r.TodoCount))
	}
	if len(parts) == 0 {
		return fmt.Sprintf("no commits this week, branch %s clean — untouched", r.Branch)
	}
	return "no commits this week — " + strings.Join(parts, ", ")
}

// fastHomeStatus is progress.Analyze's git-status-only subset — branch and
// dirty/file-count, no TODO scan, no dependency check, no test run. See the
// call site in Collect() for why $HOME needs this instead of the real thing.
func fastHomeStatus(dir string) (*progress.Report, error) {
	branch, _ := exec.Command("git", "-C", dir, "rev-parse", "--abbrev-ref", "HEAD").Output()
	statusOut, err := exec.Command("git", "-C", dir, "status", "--porcelain").Output()
	if err != nil {
		return nil, err
	}
	filesChanged := 0
	if s := strings.TrimSpace(string(statusOut)); s != "" {
		filesChanged = len(strings.Split(s, "\n"))
	}
	return &progress.Report{
		Dir:          dir,
		Branch:       strings.TrimSpace(string(branch)),
		Dirty:        filesChanged > 0,
		FilesChanged: filesChanged,
	}, nil
}

// GenerateText renders the progress report as plain terminal text — the
// default output. Few words, per project, real data.
func GenerateText(records []export.Record, rangeDays int) string {
	rows, activeDays := Collect(records)

	var b strings.Builder
	fmt.Fprintf(&b, "PROGRESS SINCE LAST SESSION — last %d days, %d project(s), %d active day(s)\n\n",
		rangeDays, len(rows), activeDays)
	for _, rw := range rows {
		fmt.Fprintf(&b, "  %-24s %s\n", rw.Name, agoStr(rw.LastSeen))
		switch {
		case rw.Report == nil:
			b.WriteString("    not a git repo — no progress signal\n")
		case rw.Report.CommitsThisWeek == 0:
			fmt.Fprintf(&b, "    %s\n", noCommitsDetail(rw.Report))
		default:
			fmt.Fprintf(&b, "    %d commit(s) this week, +%d/-%d lines, branch %s",
				rw.Report.CommitsThisWeek, rw.Report.Insertions, rw.Report.Deletions, rw.Report.Branch)
			if rw.Report.Dirty {
				fmt.Fprintf(&b, "  [%d file(s) uncommitted]", rw.Report.FilesChanged)
			}
			b.WriteString("\n")
		}
	}

	since := time.Now().AddDate(0, 0, -rangeDays)

	// TIME WORKED — from thaw-ledger's permanent, honest-time store, not the
	// pruned snapshot store the git-progress section above is bounded by.
	// This is real data: presence-derived, idle gaps excluded, never pruned —
	// unlike the old snapshot-count guess this dashboard used to publish.
	if byDay, _, err := LedgerHistory(since); err == nil && len(byDay) > 0 {
		b.WriteString("\nTIME WORKED — permanent ledger, honest presence-derived hours\n")
		b.WriteString("  (summed across every project active that day — a day with 3 parallel\n" +
			"   terminals in 3 projects can read >24h; this is combined attention-time,\n" +
			"   not single-threaded wall-clock)\n")
		days := sortedKeys(byDay)
		shown := days
		const maxBars = 21 // full range still totals below; cap bar rows so a 90-day pull stays scannable
		if len(shown) > maxBars {
			shown = shown[len(shown)-maxBars:]
		}
		var maxS int64
		for _, s := range byDay {
			if s > maxS {
				maxS = s
			}
		}
		var totalS int64
		for _, s := range byDay {
			totalS += s
		}
		for _, d := range shown {
			s := byDay[d]
			fmt.Fprintf(&b, "  %s  %-20s %4.1fh\n", d, asciiBar(s, maxS, 20), float64(s)/3600)
		}
		fmt.Fprintf(&b, "  total: %.1fh across %d day(s) with activity, avg %.1fh/active-day\n",
			float64(totalS)/3600, len(byDay), float64(totalS)/3600/float64(len(byDay)))
	}

	// AI SPEND — real token-priced Claude Code transcript cost, per project.
	if len(rows) > 0 {
		roots := make([]string, len(rows))
		for i, rw := range rows {
			roots[i] = rw.Root
		}
		_, byProject := AISpendHistory(roots, since)
		if len(byProject) > 0 {
			b.WriteString("\nAI SPEND — Claude Code, list-price estimate (you're on flat-rate — this is 'what it would've cost')\n")
			var total float64
			type pc struct {
				name string
				cost float64
			}
			var pcs []pc
			for _, rw := range rows {
				if c, ok := byProject[rw.Root]; ok && c > 0 {
					pcs = append(pcs, pc{rw.Name, c})
					total += c
				}
			}
			sort.Slice(pcs, func(i, j int) bool { return pcs[i].cost > pcs[j].cost })
			maxC := 0.0
			for _, p := range pcs {
				if p.cost > maxC {
					maxC = p.cost
				}
			}
			for _, p := range pcs {
				fmt.Fprintf(&b, "  %-24s %-20s $%.2f\n", p.name, asciiBarF(p.cost, maxC, 20), p.cost)
			}
			fmt.Fprintf(&b, "  total: $%.2f across %d day(s)\n", total, rangeDays)
		}
	}

	return b.String()
}

func sortedKeys(m map[string]int64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func asciiBar(v, max int64, width int) string {
	if max <= 0 {
		return strings.Repeat("░", width)
	}
	n := int(float64(v) / float64(max) * float64(width))
	if n < 1 && v > 0 {
		n = 1
	}
	if n > width {
		n = width
	}
	return strings.Repeat("█", n) + strings.Repeat("░", width-n)
}

func asciiBarF(v, max float64, width int) string {
	if max <= 0 {
		return strings.Repeat("░", width)
	}
	n := int(v / max * float64(width))
	if n < 1 && v > 0 {
		n = 1
	}
	if n > width {
		n = width
	}
	return strings.Repeat("█", n) + strings.Repeat("░", width-n)
}

// Generate creates the HTML version, for --open.
// Generate creates the HTML dashboard. extras controls the news + own-data
// rails: audit finding (2026-08-29, opinion panel, 2/3 change-it) was that
// fetching external news inside a terminal-workspace-memory tool is scope
// creep — real disagreement, but the operator explicitly asked for the
// feature (then images, then configurable sources) earlier the same
// session, so it isn't being deleted. Split instead: extras=false (what
// `thaw dashboard` uses by default) is the pure workspace report the
// audit wanted; extras=true (`--extras`) opts into news/own-data.
func Generate(records []export.Record, rangeDays int, extras, summarize bool) string {
	rows, activeDays := Collect(records)
	since := time.Now().AddDate(0, 0, -rangeDays)

	totalCommits, totalIns, totalDel := 0, 0, 0
	for _, rw := range rows {
		if rw.Report != nil {
			totalCommits += rw.Report.CommitsThisWeek
			totalIns += rw.Report.Insertions
			totalDel += rw.Report.Deletions
		}
	}

	// PALETTE + SIGNATURE (2026-08-29, design panel — the prior dark-navy/
	// teal/monospace version was confirmed a generic AI-dashboard default,
	// disconnected from thaw's own ice/frost/thaw-cycle identity even though
	// frost_template.html sits right next to it). Deep-melt/glacier-ice/
	// meltwater/thaw-amber are named, restrained tokens — amber is scarce on
	// purpose, it only ever means "something's unresolved." Fraunces for
	// display (this is a page you read, not code you edit), Inter for prose,
	// IBM Plex Mono reserved for the actual stat digits only.
	type dirtyEnt struct {
		name    string
		files   int
		samples []string
	}
	var dirty []dirtyEnt
	oldestDirty := time.Time{}
	for _, rw := range rows {
		if rw.Report != nil && rw.Report.Dirty {
			dirty = append(dirty, dirtyEnt{rw.Name, rw.Report.FilesChanged, dirtyFileNames(rw.Root, 3)})
			if oldestDirty.IsZero() || rw.LastSeen.Before(oldestDirty) {
				oldestDirty = rw.LastSeen
			}
		}
	}
	sort.Slice(dirty, func(i, j int) bool { return dirty[i].files > dirty[j].files })
	totalDirtyFiles := 0
	for _, d := range dirty {
		totalDirtyFiles += d.files
	}

	// Hoisted once, shared by both rails and the retro-zone chart — was
	// previously computed twice (writeRetroZone had its own copy) and only
	// half-used (LedgerHistory's byProject return was discarded entirely).
	longSince := time.Now().AddDate(0, 0, -90)
	timeByDay, timeByProject, _ := LedgerHistory(longSince)
	roots := make([]string, len(rows))
	for i, rw := range rows {
		roots[i] = rw.Root
	}
	spendByDay, spendByProject := AISpendHistory(roots, longSince)

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>thaw dashboard</title>
<style>
@import url('https://fonts.googleapis.com/css2?family=Fraunces:opsz,wght@9..144,400;9..144,600;9..144,700&family=Inter:wght@400;500;600&family=IBM+Plex+Mono:wght@400;500;600&display=swap');
:root{--deep-melt:#0b1f24;--glacier-ice:#a8d8dc;--meltwater:#5fc9b3;--frost-white:#eef6f5;--thaw-amber:#f0a679;--crack-glow:#d4f0e8;--urgent-red:#e5645f}
*{margin:0;padding:0;box-sizing:border-box}
body{background:var(--deep-melt);color:var(--glacier-ice);font-family:'Inter',sans-serif;padding:44px 32px;position:relative}
#ice-tex{position:fixed;inset:0;width:100%;height:100%;z-index:0;pointer-events:none;opacity:0.5}
.layout{position:relative;z-index:1}
.layout{display:grid;grid-template-columns:300px minmax(0,720px) 300px;gap:32px;max-width:1436px;margin:0 auto;align-items:start}
.center{min-width:0}
.rail{display:flex;flex-direction:column;gap:14px;position:sticky;top:44px}
.rail-title{font-family:'Inter',sans-serif;font-size:13px;font-weight:700;letter-spacing:2px;text-transform:uppercase;color:var(--meltwater);margin-bottom:2px}
.rail-card{background:rgba(74,155,142,0.06);border:1px solid rgba(74,155,142,0.12);border-radius:6px;font-size:15px;line-height:1.45;transition:background 0.15s;overflow:hidden}
.rail-card:hover{background:rgba(74,155,142,0.14)}
.rail-card a{color:var(--frost-white);text-decoration:none}
.rail-card a:hover{text-decoration:underline}
.rail-card img{width:100%;height:150px;object-fit:cover;display:block;background:rgba(74,155,142,0.1)}
.rail-card .card-body{padding:14px 16px}
.rail-card .src{display:block;font-family:'IBM Plex Mono',monospace;font-size:11.5px;letter-spacing:0.5px;color:var(--meltwater);margin-top:7px;opacity:0.9}
.rail-card.own{color:var(--glacier-ice);font-family:'IBM Plex Mono',monospace;font-size:14px;padding:14px 16px}
.tp-row{display:flex;justify-content:space-between;align-items:baseline;margin-bottom:6px}
.tp-row .tp-h{color:var(--meltwater);font-weight:600}
.tp-row .amt.high{color:var(--glacier-ice)}
.rail-empty{font-size:14px;color:rgba(168,216,220,0.55);font-style:italic;padding:4px 2px}
.section.deadlines{background:rgba(232,147,90,0.05);border:1px solid rgba(232,147,90,0.18);border-radius:8px;padding:16px 18px;margin-bottom:24px}
.dl-row{display:flex;gap:14px;padding:6px 0;border-bottom:1px solid rgba(232,147,90,0.08);font-size:14.5px}
.dl-row:last-of-type{border-bottom:none}
.dl-date{font-family:'IBM Plex Mono',monospace;color:var(--thaw-amber);white-space:nowrap;font-weight:600}
.dl-text{color:var(--frost-white)}
.dl-row.urgent .dl-date,.dl-row.urgent .dl-text{color:var(--urgent-red)}
.dl-freshness{font-family:'IBM Plex Mono',monospace;font-size:12px;color:rgba(168,216,220,0.5);margin-bottom:10px}
.news-rotator{display:flex;flex-direction:column;gap:10px}
.nr-card{background:rgba(74,155,142,0.08);border:1px solid rgba(74,155,142,0.18);border-radius:6px;overflow:hidden;display:flex;gap:0}
.nr-img{width:110px;min-width:110px;height:110px;object-fit:cover;background:rgba(74,155,142,0.1)}
.nr-body{padding:12px 14px;flex:1;display:flex;flex-direction:column;justify-content:center;min-height:0;min-width:0}
.nr-link{color:var(--frost-white);text-decoration:none;font-size:14.5px;line-height:1.35;display:-webkit-box;-webkit-line-clamp:4;-webkit-box-orient:vertical;overflow:hidden}
.nr-link:hover{text-decoration:underline}
.nr-src{display:block;font-family:'IBM Plex Mono',monospace;font-size:11.5px;letter-spacing:0.5px;color:var(--meltwater);margin-top:6px;font-weight:600}
.news-rotator .nr-count{font-family:'IBM Plex Mono',monospace;font-size:12px;color:var(--glacier-ice);opacity:0.65}
.marquee{position:fixed;left:0;right:0;bottom:0;height:44px;background:rgba(11,31,36,0.94);border-top:1px solid rgba(232,147,90,0.3);overflow:hidden;z-index:15;backdrop-filter:blur(4px)}
.marquee-track{display:flex;width:max-content;height:44px;align-items:center;animation:marquee-scroll linear infinite;animation-duration:calc(var(--marquee-len, 40) * 1s)}
.marquee-track span{white-space:nowrap;padding-right:60px;font-family:'IBM Plex Mono',monospace;font-size:14.5px;letter-spacing:0.3px;color:var(--thaw-amber);font-weight:600}
@keyframes marquee-scroll{from{transform:translateX(0)}to{transform:translateX(-50%)}}
body{padding-bottom:64px}
@media (max-width:1200px){.layout{grid-template-columns:1fr}.rail{position:static;flex-direction:row;flex-wrap:wrap}.rail-card{flex:1;min-width:260px}}
.eyebrow{font-family:'Inter',sans-serif;font-size:13px;letter-spacing:2px;text-transform:uppercase;color:var(--meltwater);margin-bottom:28px}
.hero{margin-bottom:8px}
.hero-fact{font-family:'Fraunces',serif;font-weight:600;font-size:48px;line-height:1.15;color:var(--frost-white);letter-spacing:-0.5px}
.hero-fact.cracked{text-shadow:0 0 26px rgba(232,147,90,0.28)}
.hero-fact.sealed{text-shadow:0 0 20px rgba(168,216,220,0.22)}
.hero-fact.urgent{color:var(--urgent-red);text-shadow:0 0 30px rgba(229,84,79,0.4);animation:urgentPulse 1.8s ease-in-out infinite}
@keyframes urgentPulse{0%,100%{opacity:1}50%{opacity:0.72}}
.hero-detail{font-family:'IBM Plex Mono',monospace;font-size:17px;color:var(--glacier-ice);margin-top:14px}
.ice-band{position:relative;height:40px;margin:10px 0 32px}
.ice-band svg{width:100%;height:100%;display:block}
.crack-path{fill:none;stroke:var(--crack-glow);stroke-width:2;stroke-dasharray:900;stroke-dashoffset:900;animation:crackDraw 1.3s ease-out forwards}
@keyframes crackDraw{to{stroke-dashoffset:0}}
.seal-path{fill:none;stroke:var(--meltwater);stroke-width:1.5;opacity:0.4}
.amber-glow{position:absolute;left:30%;top:0;width:280px;height:100%;background:radial-gradient(ellipse at center,rgba(232,147,90,0.55),transparent 75%);filter:blur(4px);pointer-events:none}
.urgent-glow{position:absolute;left:30%;top:0;width:280px;height:100%;background:radial-gradient(ellipse at center,rgba(229,84,79,0.6),transparent 75%);filter:blur(4px);pointer-events:none;animation:urgentPulse 1.8s ease-in-out infinite}
.drip{position:absolute;left:calc(30% + 6px);top:14px;width:4px;height:4px;border-radius:50%;background:var(--thaw-amber);animation:drip 2.6s ease-in infinite;box-shadow:0 0 6px rgba(232,147,90,0.8)}
@keyframes drip{0%{opacity:0;transform:translateY(-4px)}12%{opacity:1}85%{opacity:1}100%{opacity:0;transform:translateY(28px)}}
.stat-strip{display:flex;border-top:1px solid rgba(74,155,142,0.18);border-bottom:1px solid rgba(74,155,142,0.18);padding:16px 0;margin-bottom:36px}
.stat-strip .stat{flex:1;text-align:center;border-right:1px solid rgba(74,155,142,0.14)}
.stat-strip .stat:last-child{border-right:none}
.stat-strip .n{font-family:'IBM Plex Mono',monospace;font-weight:700;font-size:32px;color:var(--frost-white)}
.stat-strip .l{font-size:12px;letter-spacing:1.5px;text-transform:uppercase;color:var(--meltwater);margin-top:6px}
.section{margin-bottom:40px}
.sec-title{font-family:'Fraunces',serif;font-weight:600;font-size:19px;letter-spacing:0.3px;color:var(--glacier-ice);margin-bottom:18px}
.proj-row{padding:16px 18px;background:rgba(74,155,142,0.06);border:1px solid rgba(74,155,142,0.1);border-radius:8px;margin-bottom:8px;transition:background 0.15s;cursor:pointer}
.proj-row:hover{background:rgba(74,155,142,0.12)}
.proj-row.copied{background:rgba(232,147,90,0.18);border-color:rgba(232,147,90,0.4)}
.proj-row .head{display:flex;align-items:baseline;gap:12px}
.proj-row .name{font-weight:600;color:var(--frost-white);font-size:18px}
.proj-row.stale .name{color:rgba(168,216,220,0.4);font-weight:500}
.proj-row .ago{color:rgba(168,216,220,0.55);font-size:14px}
.proj-row .dirty{color:var(--thaw-amber);font-size:14px}
.proj-row .body{margin-top:7px;font-size:14px;color:var(--glacier-ice);font-family:'IBM Plex Mono',monospace}
.proj-row .ins{color:#7fc9ba}
.proj-row .del{color:var(--thaw-amber)}
.proj-row .quiet{color:rgba(168,216,220,0.6);font-style:italic;font-family:'Inter',sans-serif}
.proj-row .note{margin-top:8px;padding-top:8px;border-top:1px solid rgba(74,155,142,0.12);color:var(--frost-white);font-family:'Inter',sans-serif;font-size:15px;line-height:1.5}
.proj-row .note-ago{color:rgba(168,216,220,0.4);font-size:12px;font-family:'IBM Plex Mono',monospace}
.session-row{display:flex;align-items:center;gap:14px;padding:14px 18px;background:rgba(74,155,142,0.06);border:1px solid rgba(74,155,142,0.1);border-radius:8px;margin-bottom:6px;cursor:pointer;transition:background 0.15s}
.session-row:hover{background:rgba(74,155,142,0.16)}
.session-row .name{font-weight:600;color:var(--frost-white);font-size:15px;font-family:'IBM Plex Mono',monospace;min-width:130px}
.session-row .meta{color:var(--meltwater);font-size:13px;flex:1}
.session-row .copy-hint{color:var(--glacier-ice);font-size:12px;font-family:'IBM Plex Mono',monospace;opacity:0.7}
.session-row:hover .copy-hint{opacity:1}
.retro{margin-top:8px}
.retro .sec-title{font-size:19px;letter-spacing:0.3px;color:var(--glacier-ice);margin-bottom:6px;font-family:'Fraunces',serif;font-weight:600;text-transform:none}
.retro .rz-range-label{font-family:'IBM Plex Mono',monospace;font-size:13px;color:var(--meltwater);margin-bottom:14px}
.retro .section{margin-bottom:16px}
.controls{display:flex;align-items:center;gap:8px;margin-bottom:16px;font-size:14px}
.controls select{background:rgba(74,155,142,0.12);color:var(--glacier-ice);border:1px solid rgba(74,155,142,0.25);border-radius:4px;padding:6px 10px;font-family:'Inter',sans-serif;font-size:14px;cursor:pointer}
.controls button{background:rgba(74,155,142,0.12);color:var(--meltwater);border:1px solid rgba(74,155,142,0.25);border-radius:4px;padding:6px 11px;font-family:'Inter',sans-serif;font-size:14px;cursor:pointer;transition:background 0.15s}
.controls button:hover{background:rgba(74,155,142,0.25);color:var(--frost-white)}
.controls .spacer{flex:1}
.bar-chart{display:flex;align-items:flex-end;gap:3px;height:180px;margin-bottom:8px}
.bar-chart .bar{background:var(--meltwater);border-radius:2px 2px 0 0;min-width:7px;flex:1;transition:height 0.3s}
.bar-chart .bar:hover{background:var(--glacier-ice)}
.bar-chart .bar.spend{background:var(--thaw-amber)}
.bar-chart .bar.spend:hover{background:#f0ab7c}
.bar-labels{display:flex;gap:3px;font-size:12px;color:rgba(168,216,220,0.6);margin-bottom:12px;font-family:'IBM Plex Mono',monospace}
.bar-labels span{flex:1;text-align:center;writing-mode:vertical-rl}
.spend-row{display:flex;align-items:center;gap:12px;padding:10px 16px;background:rgba(74,155,142,0.06);border-radius:8px;margin-bottom:5px;font-size:14px;transition:background 0.15s}
.spend-row:hover{background:rgba(74,155,142,0.12)}
.spend-row .name{min-width:160px;color:var(--frost-white);font-family:'Inter',sans-serif}
.spend-row .amt{color:var(--meltwater);min-width:70px;font-family:'IBM Plex Mono',monospace}
.spend-row .amt.high{color:var(--glacier-ice);font-weight:600}
.spend-row .bar-h,.bar-h{height:6px;background:rgba(74,155,142,0.1);border-radius:3px;overflow:hidden}
.spend-row .bar-h{flex:1}
.bar-fill{height:100%;background:var(--meltwater);border-radius:3px}
.sub{font-size:14px;color:rgba(168,216,220,0.45);font-family:'IBM Plex Mono',monospace}
.footer{margin-top:40px;font-size:12px;color:rgba(168,216,220,0.25);letter-spacing:3px;text-transform:uppercase;font-family:'Inter',sans-serif}
.ticker{position:relative;height:104px;margin-bottom:32px;padding:18px 24px 18px 28px;background:rgba(74,155,142,0.07);border:1px solid rgba(74,155,142,0.2);border-radius:8px;overflow:hidden}
.ticker::after{content:"";position:absolute;left:0;top:0;bottom:0;width:3px;background:var(--meltwater);border-radius:8px 0 0 8px}
.ticker span{position:absolute;left:24px;right:24px;top:50%;transform:translateY(-50%);font-size:20px;line-height:1.35;font-weight:500;color:var(--glacier-ice);opacity:0;transition:opacity 0.6s ease;white-space:normal;font-family:'Inter',sans-serif}
.ticker span.on{opacity:1}
.ticker span.reflect{color:var(--crack-glow);font-style:italic;font-weight:400}
::-webkit-scrollbar{width:10px}
::-webkit-scrollbar-track{background:var(--deep-melt)}
::-webkit-scrollbar-thumb{background:rgba(74,155,142,0.2);border-radius:5px}
</style></head><body>
<svg width="0" height="0" style="position:absolute"><defs>
<filter id="ice-fine" x="0%" y="0%" width="100%" height="100%">
<feTurbulence type="fractalNoise" baseFrequency="0.03" numOctaves="5" seed="14" result="n"/>
<feColorMatrix type="saturate" values="0" in="n" result="g"/>
<feComponentTransfer in="g"><feFuncR type="linear" slope="0.18" intercept="0.55"/>
<feFuncG type="linear" slope="0.15" intercept="0.68"/><feFuncB type="linear" slope="0.12" intercept="0.8"/>
<feFuncA type="linear" slope="0.25" intercept="0"/></feComponentTransfer>
</filter>
</defs></svg>
<!-- real ice texture, not a color-named variable pretending to be one — same
     feTurbulence technique frost_template.html uses, ported and toned down
     for a page you read rather than a full-screen animated scene. Operator
     feedback (2026-08-29): the prior version only renamed CSS tokens
     "ice/frost/melt" without actually looking like ice. -->
<svg id="ice-tex"><rect width="100%" height="100%" filter="url(#ice-fine)"/></svg>
`)
	// Both rails render unconditionally now (operator feedback 2026-08-29:
	// "the left and right side of the screen should be taken up") — they're
	// thaw's own data (time-by-project, spend, ahead-of-upstream/TODOs),
	// which was never the scope objection; only --extras-gated news is
	// still opt-in, inside the left rail.
	b.WriteString(`<div class="layout">`)

	// News, fetched once — general news goes in the left rail (next to
	// Time-by-project), as a rotator rather than a growing list.
	var news []NewsItem
	if extras {
		newsCfg, _ := config.Load()
		news = FetchNews(newsCfg.News.Sources, 12)
	}

	// LEFT RAIL — always on now (operator feedback 2026-08-29: "missing all
	// the previous terminals and how much time spent in each... the left
	// and right side of the screen should be taken up"). Time-by-project is
	// real, permanent ledger data (LedgerHistory's byProject return existed
	// since the retro-zone chart was built but was never actually
	// displayed anywhere — pure oversight, not a design choice). News stays
	// --extras-gated below it — that part of the scope decision doesn't
	// change just because the rail itself is back.
	b.WriteString(`<div class="rail">`)
	type tp struct {
		name string
		secs int64
	}
	var tps []tp
	for _, rw := range rows {
		if s, ok := timeByProject[rw.Name]; ok && s > 0 {
			tps = append(tps, tp{rw.Name, s})
		}
	}
	sort.Slice(tps, func(i, j int) bool { return tps[i].secs > tps[j].secs })
	if len(tps) > 0 {
		var maxS int64
		for _, t := range tps {
			if t.secs > maxS {
				maxS = t.secs
			}
		}
		b.WriteString(`<div class="rail-title">Time by project — 90d</div>`)
		for _, t := range tps {
			pct := 0
			if maxS > 0 {
				pct = int(float64(t.secs) / float64(maxS) * 100)
			}
			fmt.Fprintf(&b, `<div class="rail-card own"><div class="tp-row"><span>%s</span><span class="tp-h">%.1fh</span></div>`+
				`<div class="bar-h"><div class="bar-fill" style="width:%d%%"></div></div></div>`,
				html.EscapeString(t.name), float64(t.secs)/3600, pct)
		}
	} else {
		b.WriteString(`<div class="rail-title">Time by project</div><div class="rail-empty">no ledger history yet</div>`)
	}
	if len(news) > 0 {
		b.WriteString(`<div id="sec-news">`)
		b.WriteString(`<div class="rail-title" style="margin-top:20px">News</div>`)
		writeNewsRotator(&b, "nr", news)
		b.WriteString(`</div>`) // #sec-news
	}
	b.WriteString(`</div>`)

	b.WriteString(`<div class="center">`)

	fmt.Fprintf(&b, `<div class="eyebrow">thaw &middot; report generated %s</div>`, time.Now().Format("Jan 2, 2006 3:04 PM"))

	// Deadlines fetched here, once, so the hero decision below and the
	// "Upcoming deadlines" section can share the same read — no duplicate
	// file scan, and it means the hero can react to what's actually urgent.
	// Opt-in: THAW_DEADLINES_FILE unset means no deadlines feature at all.
	var deadlines []DeadlineItem
	deadlinesPath := DeadlinesFile()
	if deadlinesPath != "" {
		deadlines = NextDeadlines(deadlinesPath, 6)
	}
	var urgentDeadline *DeadlineItem
	for i := range deadlines {
		if deadlines[i].Urgent {
			urgentDeadline = &deadlines[i]
			break
		}
	}

	// HERO + SIGNATURE ICE BAND — the page's one bold move. Audit finding
	// (2026-08-30, panel, hero-priority lens): direct operator complaint —
	// "why is [uncommitted files] at the top? the most important and
	// relevant info is nowhere to be found." An urgent deadline (the
	// file's OWN "**OVERDUE-SOON**" marker — not a date thaw computed
	// itself) now outranks git status for the hero slot; dirty/clean state
	// is still the fallback when nothing's marked urgent.
	b.WriteString(`<div class="hero">`)
	switch {
	case urgentDeadline != nil:
		fmt.Fprintf(&b, `<div class="hero-fact urgent">DUE %s — %s</div>`,
			html.EscapeString(urgentDeadline.Date), html.EscapeString(heroDeadlineText(urgentDeadline.Text)))
		b.WriteString(`<div class="hero-detail">from the deadlines file (unverified) — see Upcoming deadlines below</div>`)
		b.WriteString(`<div class="ice-band"><svg viewBox="0 0 800 40" preserveAspectRatio="none">` +
			`<path class="crack-path" d="M0,20 L90,13 L145,29 L215,8 L285,25 L365,14 L435,27 L525,10 L605,23 L685,15 L800,19"/>` +
			`</svg><div class="urgent-glow"></div></div>`)
	case len(dirty) > 0:
		fmt.Fprintf(&b, `<div class="hero-fact cracked">%d file%s uncommitted across %d project%s, oldest %s</div>`,
			totalDirtyFiles, plural(totalDirtyFiles), len(dirty), plural(len(dirty)), agoStr(oldestDirty))
		if top := dirty[0]; len(top.samples) > 0 {
			extra := ""
			if top.files > len(top.samples) {
				extra = fmt.Sprintf(", +%d more", top.files-len(top.samples))
			}
			fmt.Fprintf(&b, `<div class="hero-detail">%s: %s%s</div>`,
				html.EscapeString(top.name), html.EscapeString(strings.Join(top.samples, ", ")), html.EscapeString(extra))
		}
		b.WriteString(`<div class="ice-band"><svg viewBox="0 0 800 40" preserveAspectRatio="none">` +
			`<path class="crack-path" d="M0,20 L90,13 L145,29 L215,8 L285,25 L365,14 L435,27 L525,10 L605,23 L685,15 L800,19"/>` +
			`</svg><div class="amber-glow"></div><div class="drip"></div></div>`)
	case len(rows) > 0:
		fmt.Fprintf(&b, `<div class="hero-fact sealed">all clean — %d project%s, nothing outstanding</div>`, len(rows), plural(len(rows)))
		b.WriteString(`<div class="ice-band"><svg viewBox="0 0 800 40" preserveAspectRatio="none">` +
			`<path class="seal-path" d="M0,20 L800,20"/></svg></div>`)
	default:
		b.WriteString(`<div class="hero-fact sealed">no tracked projects in this window</div><div class="ice-band"></div>`)
	}
	b.WriteString(`</div>`)

	// DEADLINES — placed right after the hero, above git-progress detail:
	// "what's about to bite you" outranks "what you didn't commit."
	// Read-only, live, same unverified-accuracy caveat carried everywhere
	// else this file appears. Freshness line: thaw doesn't verify deadlines
	// (that's the job of whatever tool writes the file), but it CAN
	// honestly say how old the file it's reading is.
	{
		if len(deadlines) > 0 {
			b.WriteString(`<div class="section deadlines"><div class="sec-title">Upcoming deadlines</div>`)
			if mtime, ok := DeadlinesFreshness(deadlinesPath); ok {
				fmt.Fprintf(&b, `<div class="dl-freshness">deadlines file last updated %s</div>`, html.EscapeString(agoStr(mtime)))
			}
			for _, d := range deadlines {
				cls := "dl-row"
				if d.Urgent {
					cls += " urgent"
				}
				fmt.Fprintf(&b, `<div class="%s"><span class="dl-date">%s</span><span class="dl-text">%s</span></div>`,
					cls, html.EscapeString(d.Date), html.EscapeString(d.Text))
			}
			b.WriteString(`<div class="sub" style="margin-top:6px;margin-bottom:0">from the deadlines file (unverified) — check the source before relying on any single date</div></div>`)
		}
	}

	// RETROSPECTIVE ZONE — Time worked + AI spend. Operator ask (2026-08-30):
	// "the time spend and ai spend could be improved... it should move up
	// not be at the bottom of the screen" — moved from the very bottom of
	// the page to right after Deadlines, ahead of the git-progress detail.
	// Interactive: range dropdown (7/14/30/90d, with prev/next steppers)
	// and a Time/Spend metric toggle that now also auto-rotates on its own
	// (same "rotate like news" ask) in addition to being clickable —
	// driven entirely client-side off embedded daily data, no backend to
	// hit since this is a static generated file. Your last choice is
	// remembered (localStorage); this page's own rangeDays is only the
	// fallback for a first-ever open.
	writeRetroZone(&b, timeByDay, spendByDay, rangeDays)

	// PROGRESS SINCE LAST SESSION
	b.WriteString(`<div class="section"><div class="sec-title">Progress since last session</div>`)
	for _, rw := range rows {
		name := html.EscapeString(rw.Name)
		cls := "proj-row"
		if time.Since(rw.LastSeen) > 7*24*time.Hour {
			cls += " stale"
		}
		launchCmd := claudeLaunchCmd(rw.Root, "whats next in this project")
		fmt.Fprintf(&b, `<div class="%s" data-cmd="%s" title="click to copy: %s" onclick="thawCopy(this)"><div class="head">`,
			cls, html.EscapeString(launchCmd), html.EscapeString(launchCmd))
		fmt.Fprintf(&b, `<span class="name">%s</span><span class="ago">%s</span>`,
			name, html.EscapeString(agoStr(rw.LastSeen)))
		if rw.Report != nil && rw.Report.Dirty {
			fmt.Fprintf(&b, `<span class="dirty">%d file(s) uncommitted</span>`, rw.Report.FilesChanged)
		}
		b.WriteString(`</div><div class="body">`)
		switch {
		case rw.Report == nil:
			b.WriteString(`<span class="quiet">not a git repo — no progress signal</span>`)
		case rw.Report.CommitsThisWeek == 0:
			b.WriteString(`<span class="quiet">` + html.EscapeString(noCommitsDetail(rw.Report)) + `</span>`)
		default:
			fmt.Fprintf(&b, `%d commit(s) this week &middot; <span class="ins">+%d</span> <span class="del">-%d</span> lines &middot; branch %s`,
				rw.Report.CommitsThisWeek, rw.Report.Insertions, rw.Report.Deletions,
				html.EscapeString(rw.Report.Branch))
		}
		// AI-written session write-up (`thaw session-note`), if one exists
		// for this project — real specifics ("fixed the TOCTOU race in the
		// budget check"), not a stat restatement. Silent when absent: the
		// feature is opt-in and fails closed when the summarizer is
		// unreachable, so most projects won't have one.
		if note, ts := LastProjectNote(rw.Root); note != "" {
			fmt.Fprintf(&b, `<div class="note">&#10078; %s <span class="note-ago">(%s)</span></div>`,
				html.EscapeString(note), html.EscapeString(agoStr(ts)))
		}
		b.WriteString(`</div></div>`)
	}
	b.WriteString(`</div>`)

	// PAST SESSIONS — the actual point of thaw, per direct operator
	// feedback: every previous frozen terminal state, restorable. This is
	// real per-freeze data (snapshot.Store.List), not the aggregated
	// ledger the rest of the page uses. A static HTML file can't itself
	// spawn a tmux session on your machine, so "click" means what it
	// honestly can: copy the exact `thaw recall <id>` command to run
	// yourself. Bounded by thaw's own retention (keep_days/keep_max in
	// config.toml, 7 days by default) — older sessions are genuinely gone,
	// not hidden; the empty-state message says so instead of pretending.
	writePastSessions(&b, summarize)

	// Quiet stat strip — demoted per design panel (this used to be the
	// loudest thing on the page; it's FYI, not the point).
	b.WriteString(`<div class="stat-strip">`)
	writeStatBox(&b, fmt.Sprintf("%d", len(rows)), "Projects")
	writeStatBox(&b, fmt.Sprintf("%d", totalCommits), "Commits/wk")
	writeStatBox(&b, fmt.Sprintf("+%d/-%d", totalIns, totalDel), "Lines/wk")
	writeStatBox(&b, fmt.Sprintf("%d", activeDays), "Active days")
	b.WriteString(`</div>`)

	// Ticker trimmed to non-duplicate facts only (panel review: "leads this
	// week"/"top AI spend" restated the stat row and the retro zone below at
	// slow motion — cut). Only "clean project" and "busiest day" survive,
	// since neither appears anywhere else on the page. Reflections (data-
	// grounded questions, not facts) ride the same rotation, visually
	// distinguished.
	items := buildHighlights(rows, since)
	items = append(items, buildReflections(rows, since)...)
	if len(items) > 0 {
		writeTicker(&b, items)
	}

	fmt.Fprintf(&b, `<div class="footer">thaw %s — %d records analyzed, real git+ledger+transcript data (no time estimates)</div>`, html.EscapeString(buildinfo.Version), len(records))
	b.WriteString(`</div>`) // .center

	// RIGHT RAIL — always on now, same reasoning as the left rail. Real
	// progress.Report fields (ahead-of-upstream, TODO counts, this-week
	// leaderboard) plus the AI-spend-by-project list, relocated here from
	// the center column (was stacked below the interactive chart — moving
	// it into the rail cuts vertical scroll, per operator feedback).
	b.WriteString(`<div class="rail">`)
	b.WriteString(`<div id="sec-yourdata">`)
	own := buildOwnDataRail(rows)
	if len(own) > 0 {
		b.WriteString(`<div class="rail-title">Your data</div>`)
		for _, it := range own {
			fmt.Fprintf(&b, `<div class="rail-card own">%s</div>`, html.EscapeString(it.Text))
		}
	} else {
		b.WriteString(`<div class="rail-title">Your data</div><div class="rail-empty">nothing extra to surface right now</div>`)
	}
	b.WriteString(`</div>`) // #sec-yourdata
	type pc struct {
		name string
		cost float64
	}
	var pcs []pc
	var totalSpend float64
	for _, rw := range rows {
		if c, ok := spendByProject[rw.Root]; ok && c > 0 {
			pcs = append(pcs, pc{rw.Name, c})
			totalSpend += c
		}
	}
	if len(pcs) > 0 {
		sort.Slice(pcs, func(i, j int) bool { return pcs[i].cost > pcs[j].cost })
		maxC := pcs[0].cost
		avg := totalSpend / float64(len(pcs))
		b.WriteString(`<div id="sec-aispend">`)
		b.WriteString(`<div class="rail-title" style="margin-top:20px">AI spend — 90d</div>`)
		for _, p := range pcs {
			pct := 0
			if maxC > 0 {
				pct = int(p.cost / maxC * 100)
			}
			amtCls := "amt"
			if p.cost > avg {
				amtCls = "amt high"
			}
			fmt.Fprintf(&b, `<div class="rail-card own"><div class="tp-row"><span>%s</span><span class="%s">$%.2f</span></div>`+
				`<div class="bar-h"><div class="bar-fill" style="width:%d%%"></div></div></div>`,
				html.EscapeString(p.name), amtCls, p.cost, pct)
		}
		b.WriteString(`</div>`) // #sec-aispend
	}
	b.WriteString(`</div>`)

	b.WriteString(`</div>`) // .layout
	writeMarqueeTicker(&b, rows)
	writeReportSettingsPanel(&b)
	b.WriteString(`</body></html>`)

	return b.String()
}

// writeMarqueeTicker is a fixed bottom bar, scrolling right-to-left like a
// news-station crawl. Content is real: the deadlines file (same unverified
// caveat as everywhere else it appears; skipped entirely when
// THAW_DEADLINES_FILE is unset) plus the same own-data facts the right rail
// already computes (TODO/FIXME counts, leaderboard) — no new data source,
// just a second, more attention-grabbing presentation of facts this page
// already has.
func writeMarqueeTicker(b *strings.Builder, rows []ProjectProgress) {
	var parts []string
	if path := DeadlinesFile(); path != "" {
		for _, d := range NextDeadlines(path, 8) {
			parts = append(parts, fmt.Sprintf("⚠ %s — %s", d.Date, d.Text))
		}
	}
	for _, it := range buildOwnDataRail(rows) {
		parts = append(parts, "☐ "+it.Text)
	}
	if len(parts) == 0 {
		return
	}
	line := strings.Join(parts, "    •    ")
	// Scroll speed scaled to content length (~12 chars/sec) instead of a
	// fixed duration — a short deadline list shouldn't crawl as slowly as
	// a long one, and a long one shouldn't whip past unreadably.
	durationSecs := len(line) / 12
	if durationSecs < 20 {
		durationSecs = 20
	}
	fmt.Fprintf(b, `<div class="marquee"><div class="marquee-track" style="--marquee-len:%d">`+
		`<span>%s</span><span aria-hidden="true">%s</span>`+
		`</div></div>`, durationSecs, html.EscapeString(line), html.EscapeString(line))
}

// writeNewsRotator renders a fixed-height card group that auto-rotates
// through a list of news items — no growing list, no scroll, real
// click-through (news items have a real URL, unlike the copy-to-clipboard
// action pattern elsewhere on this page). id is a short unique prefix (e.g.
// "nr") so multiple rotators could coexist on one page with independent DOM
// ids/timers. Position/count is a plain "N / total" text counter — scales
// to any item count. Shows SLOTS (default 3) cards at once instead of one;
// the whole group advances together every 7s. Items with a real image sort
// first — most RSS feeds (BBC/TechCrunch/etc) ship one per article; cards
// without one render text-forward instead of with a broken image slot.
const newsRotatorSlots = 3

func writeNewsRotator(b *strings.Builder, id string, items []NewsItem) {
	type jsonItem struct {
		Title string `json:"t"`
		Src   string `json:"s"`
		URL   string `json:"u,omitempty"`
		Img   string `json:"i,omitempty"`
	}
	var jitems []jsonItem
	for _, n := range items {
		jitems = append(jitems, jsonItem{Title: n.Title, Src: n.Source, URL: httpURLOnly(n.URL), Img: httpURLOnly(n.Image)})
	}
	if len(jitems) == 0 {
		return
	}
	// Images-first, stable otherwise — so the first-shown group leads with
	// whichever items actually have a real thumbnail.
	sort.SliceStable(jitems, func(i, j int) bool { return jitems[i].Img != "" && jitems[j].Img == "" })

	itemsJSON, _ := json.Marshal(jitems)
	b.WriteString(`<div id="` + id + `-rotator" class="news-rotator">`)
	for s := 0; s < newsRotatorSlots; s++ {
		fmt.Fprintf(b, `<div class="nr-card" id="%s-card%d"><img class="nr-img" alt="" style="display:none">`+
			`<div class="nr-body"><a class="nr-link" target="_blank" rel="noopener"></a><span class="nr-src"></span></div></div>`, id, s)
	}
	fmt.Fprintf(b, `<div id="%s-count" class="nr-count"></div></div>`, id)
	fmt.Fprintf(b, `<script>(function(){
  var ITEMS=`)
	b.Write(itemsJSON)
	fmt.Fprintf(b, `;
  var SLOTS=%d, i=0, countEl=document.getElementById('%s-count');
  var cards=[];
  for(var s=0;s<SLOTS;s++){
    var c=document.getElementById('%s-card'+s);
    cards.push({el:c, img:c.querySelector('.nr-img'), link:c.querySelector('.nr-link'), src:c.querySelector('.nr-src')});
  }
  function show(idx){
    for(var s=0;s<SLOTS;s++){
      var it=ITEMS[(idx+s)%%ITEMS.length];
      var c=cards[s];
      if(!it){ c.el.style.display='none'; continue; }
      c.el.style.display='';
      if(it.i){ c.img.src=it.i; c.img.style.display=''; } else { c.img.style.display='none'; }
      c.link.textContent=it.t;
      if(it.u) c.link.href=it.u; else c.link.removeAttribute('href');
      c.src.textContent=it.s;
    }
    countEl.textContent=Math.min(SLOTS,ITEMS.length)+' shown of '+ITEMS.length;
  }
  show(0);
  if(ITEMS.length>SLOTS) setInterval(function(){ i=(i+SLOTS)%%ITEMS.length; show(i); }, 7000);
})();</script>`, newsRotatorSlots, id, id)
}

// writeReportSettingsPanel adds the same gear-icon/localStorage settings
// pattern the kiosk mode uses, scoped to what the report page actually has:
// section-level show/hide (News, Past sessions, This week in review, AI
// spend). Operator ask (2026-08-29): "there should be an easier way for
// people to update what they want to see from their dashboard... everything
// we just did should be easier to do in settings." Browser-only, same
// scope decision as kiosk's panel — no config.toml writes, no new process.
func writeReportSettingsPanel(b *strings.Builder) {
	b.WriteString(`
<button id="rgear" title="Settings" style="position:fixed;top:20px;right:20px;width:38px;height:38px;border-radius:50%;background:rgba(74,155,142,0.12);color:#a8d8dc;border:1px solid rgba(168,216,220,0.2);font-size:17px;cursor:pointer;z-index:30">&#9881;</button>
<div id="rpanel" style="position:fixed;top:0;right:0;bottom:0;width:320px;max-width:88vw;background:#0e2a30;border-left:1px solid rgba(168,216,220,0.15);transform:translateX(100%);transition:transform 0.3s ease;z-index:40;overflow-y:auto;padding:60px 22px 22px;font-family:'Inter',sans-serif;color:#eef6f5">
<button id="rclosepanel" style="position:absolute;top:18px;right:18px;background:none;border:none;color:#a8d8dc;font-size:20px;cursor:pointer">&times;</button>
<div style="font-family:'Fraunces',serif;font-size:20px;margin-bottom:16px">Settings</div>
<div style="font-size:12px;letter-spacing:1.5px;text-transform:uppercase;color:#4a9b8e;margin:14px 0 8px;font-weight:600">Show on this page</div>
<div id="rcatlist"></div>
<div style="font-size:12px;color:rgba(238,246,245,0.5);margin-top:22px;line-height:1.5">Saved to this browser only — doesn't touch config.toml or other machines. News sources themselves: <code>thaw config set news.sources hn,bbc,gizmodo</code>.</div>
</div>
<script>
(function(){
  var SECTIONS=[{k:'sec-news',l:'News'},{k:'sec-pastsessions',l:'Past sessions'},{k:'sec-retro',l:'This week, in review'},{k:'sec-yourdata',l:'Your data'},{k:'sec-aispend',l:'AI spend'}];
  var KEY='thawReportPrefs';
  function load(){ var d={hidden:[]}; try{var r=localStorage.getItem(KEY); if(r) return Object.assign(d,JSON.parse(r));}catch(e){} return d; }
  function save(p){ try{localStorage.setItem(KEY,JSON.stringify(p));}catch(e){} }
  var prefs=load();
  function apply(){
    SECTIONS.forEach(function(s){
      var el=document.getElementById(s.k);
      if(el) el.style.display = prefs.hidden.indexOf(s.k)>-1 ? 'none' : '';
    });
  }
  var list=document.getElementById('rcatlist');
  SECTIONS.forEach(function(s){
    if(!document.getElementById(s.k)) return; // section not present on this render (e.g. no news data) — nothing to toggle
    var lbl=document.createElement('label');
    lbl.style.cssText='display:flex;align-items:center;gap:10px;margin-bottom:12px;font-size:15px;cursor:pointer;color:#eef6f5';
    var cb=document.createElement('input'); cb.type='checkbox'; cb.style.cssText='width:16px;height:16px;accent-color:#4a9b8e';
    cb.checked = prefs.hidden.indexOf(s.k)===-1;
    cb.addEventListener('change', function(){
      var idx=prefs.hidden.indexOf(s.k);
      if(cb.checked && idx>-1) prefs.hidden.splice(idx,1);
      if(!cb.checked && idx===-1) prefs.hidden.push(s.k);
      save(prefs); apply();
    });
    lbl.appendChild(cb); lbl.appendChild(document.createTextNode(s.l));
    list.appendChild(lbl);
  });
  document.getElementById('rgear').addEventListener('click', function(){ document.getElementById('rpanel').style.transform='translateX(0)'; });
  document.getElementById('rclosepanel').addEventListener('click', function(){ document.getElementById('rpanel').style.transform='translateX(100%)'; });
  apply();
})();
</script>`)
}

// writeRetroZone embeds up to 90 days of daily ledger + AI-spend data as
// JSON and renders a client-side-interactive chart over it: a metric toggle
// (Time/Spend), a range dropdown with prev/next arrow steppers, and
// localStorage persistence of your last choice. This is the one section of
// the page that can honestly be "different time frames, no reload" — the
// git-progress cards above need a live `git log`, which a static file can't
// re-run, so that section stays fixed to the --range this file was built with.
// writePastSessions lists real, individual freeze snapshots — not the
// aggregated ledger the rest of the dashboard reads — each restorable via
// a copied `thaw recall <id>` command. This is genuinely bounded by thaw's
// own retention (default 7 days / 100 snapshots), which is stated plainly
// rather than silently showing an incomplete list with no explanation.
// heroDeadlineTrimRe strips the "⚠ UNVERIFIED — (0d) — " status prefix a
// deadline line carries in the detailed list — useful there, but it turns
// the giant hero headline into an unreadable wall of text. The hero keeps
// just the actual case/action; the full line (including the caveat) still
// shows in the Upcoming deadlines section right below it.
var heroDeadlineTrimRe = regexp.MustCompile(`^(?:⚠\s*)?(?:UNVERIFIED\s*)?(?:—\s*)?(?:\(\d+d?\)\s*)?(?:—\s*)?`)

func heroDeadlineText(text string) string {
	trimmed := heroDeadlineTrimRe.ReplaceAllString(text, "")
	trimmed = strings.TrimSpace(trimmed)
	if trimmed == "" {
		return text
	}
	return trimmed
}

// genericIntentRe matches internal/intent's own bare fallback ("3 sessions")
// when it found nothing more specific to say — worth showing only as a last
// resort, since Session.Command usually beats it (see sessionFingerprint).
var genericIntentRe = regexp.MustCompile(`^\d+ sessions?$`)

// aiSummaryBudget caps how many NEW summarizer calls one dashboard render
// will make to backfill missing AI summaries (writePastSessions,
// summarize=true). Each call is seconds; doing this unbounded would make
// `thaw dashboard --open --summarize` visibly hang. The cache is cumulative
// across renders, so repeated runs eventually cover the whole list.
const aiSummaryBudget = 3

func writePastSessions(b *strings.Builder, summarize bool) {
	store, err := snapshot.Open()
	if err != nil {
		return // best-effort — a broken snapshot store shouldn't break the whole dashboard
	}
	defer store.Close()
	// Pull more raw rows than we'll show — most of the recent history is
	// the daemon re-freezing the same idle terminal, which collapses down
	// to far fewer distinct rows below. 80 raw is enough headroom to still
	// surface a real handful of distinct moments.
	summaries, err := store.List(80)
	if err != nil || len(summaries) == 0 {
		b.WriteString(`<div class="section"><div class="sec-title">Past sessions</div>` +
			`<div class="rail-empty">no snapshots in the retention window yet — run "thaw freeze" to start one</div></div>`)
		return
	}

	// Dedup: the auto-daemon ("scheduled") freezes an idle terminal every
	// few minutes, so a run of consecutive snapshots is routinely the exact
	// same CWD/session-count over and over — 20 rows that all say
	// "myproject, 1 session" and nothing else: not a labeling bug, a real
	// duplicate run. Collapse adjacent identical signatures into one row spanning
	// the time range, so what's left is genuinely distinct moments.
	type dispRow struct {
		s        snapshot.SnapshotSummary
		newest   time.Time
		oldest   time.Time
		runCount int
	}
	var disp []dispRow
	sig := func(s snapshot.SnapshotSummary) string {
		return s.Name + "|" + strings.Join(s.Projects, ",") + "|" + fmt.Sprintf("%d", s.SessionCount) + "|" + s.Intent + "|" + s.Command + "|" + s.Branch
	}
	for _, s := range summaries {
		if s.Source == "scheduled" && len(disp) > 0 {
			last := &disp[len(disp)-1]
			if last.s.Source == "scheduled" && sig(last.s) == sig(s) {
				last.oldest = s.CreatedAt
				last.runCount++
				continue
			}
		}
		disp = append(disp, dispRow{s: s, newest: s.CreatedAt, oldest: s.CreatedAt, runCount: 1})
	}
	if len(disp) > 20 {
		disp = disp[:20]
	}

	// AI summaries — cached, so this is cheap on every render except the
	// bounded backfill below. summarize controls whether we spend NEW
	// summarizer calls this render; the cache always gets read regardless,
	// so summaries from a previous --summarize run keep showing up on plain
	// renders too.
	aiNotes := loadSnapshotNotes()
	backfillBudget := 0
	if summarize {
		backfillBudget = aiSummaryBudget
	}

	b.WriteString(`<div id="sec-pastsessions"><div class="section"><div class="sec-title">Past sessions — click to copy the restore command</div>`)
	for _, d := range disp {
		s := d.s
		// Process-tree data (Intent/Command) has a real ceiling for a
		// long-lived-terminal workflow — the top-level command is often
		// just the tab's own title, never what was actually being worked
		// on. The real fix: quote the actual most-recent thing typed in
		// that terminal, straight from Claude Code's own transcript file
		// (RecentUserMessage — the primary source, not a paraphrase).
		// AI summary (aiNotes) is the fallback when no transcript exists
		// nearby; heuristics are the last resort.
		label := s.Name
		var snap *models.Snapshot
		if label == "" {
			if got, err := store.Get(s.ID); err == nil {
				snap = got
				for _, sess := range got.Sessions {
					if sess.CWD == "" {
						continue
					}
					if msg := RecentUserMessage(sess.CWD, s.CreatedAt); msg != "" {
						label = msg
						break
					}
				}
			}
		}
		if label == "" {
			label = aiNotes[s.ID]
		}
		if label == "" && backfillBudget > 0 {
			backfillBudget--
			if snap == nil {
				snap, _ = store.Get(s.ID)
			}
			if snap != nil {
				if summary := summarizeSnapshot(snap.Sessions); summary != "" {
					appendSnapshotNote(s.ID, summary)
					aiNotes[s.ID] = summary
					label = summary
				}
			}
		}
		if label == "" {
			switch {
			case s.Intent != "" && !genericIntentRe.MatchString(s.Intent):
				label = s.Intent
			case s.Command != "":
				label = s.Command
			case s.Intent != "":
				label = s.Intent // generic "N sessions" fallback — still better than nothing
			case len(s.Projects) > 0:
				label = strings.Join(s.Projects, " + ")
			default:
				label = fmt.Sprintf("snapshot #%d", s.ID)
			}
		}
		if s.Branch != "" && s.Branch != "main" && s.Branch != "master" {
			label += " [" + s.Branch + "]"
		}
		restoreArg := s.Name
		if restoreArg == "" {
			restoreArg = fmt.Sprintf("%d", s.ID)
		}
		meta := fmt.Sprintf("%d session(s) &middot; %s &middot; %s", s.SessionCount, html.EscapeString(s.Source), html.EscapeString(agoStr(d.newest)))
		if d.runCount > 1 {
			meta = fmt.Sprintf("%d session(s) &middot; %s &middot; auto-captured %d&times; from %s to %s (unchanged the whole time)",
				s.SessionCount, html.EscapeString(s.Source), d.runCount, html.EscapeString(agoStr(d.oldest)), html.EscapeString(agoStr(d.newest)))
		}
		fmt.Fprintf(b, `<div class="session-row" data-cmd="thaw recall %s" title="click to copy: thaw recall %s" onclick="thawCopy(this)">`+
			`<span class="name">%s</span><span class="meta">%s</span>`+
			`<span class="copy-hint">copy&nbsp;&#8594;</span></div>`,
			html.EscapeString(restoreArg), html.EscapeString(restoreArg),
			html.EscapeString(label), meta)
	}
	b.WriteString(`<div class="sub" style="margin-top:8px;margin-bottom:0">` +
		fmt.Sprintf("%d distinct (from the last %d raw snapshots — bounded by thaw's own retention, keep_days/keep_max in config.toml)", len(disp), len(summaries)) +
		`</div></div></div>`) // .section, #sec-pastsessions
	b.WriteString(`<script>
function thawCopy(el){
  var cmd=el.getAttribute('data-cmd');
  navigator.clipboard.writeText(cmd).then(function(){
    var hint=el.querySelector('.copy-hint');
    if(hint){
      var old=hint.textContent;
      hint.textContent='copied!';
      setTimeout(function(){hint.textContent=old},1500);
    } else {
      el.classList.add('copied');
      setTimeout(function(){el.classList.remove('copied')},1500);
    }
  });
}
</script>`)
}

func writeRetroZone(b *strings.Builder, timeByDay map[string]int64, spendByDay map[string]float64, rangeDays int) {
	if len(timeByDay) == 0 && len(spendByDay) == 0 {
		return
	}

	b.WriteString(`<div id="sec-retro"><div class="section retro"><div class="sec-title">Time &amp; AI spend</div>` +
		`<div class="rz-range-label" id="rz-range-label"></div>`)
	b.WriteString(`<div class="controls">` +
		`<button id="rz-prev" title="shorter range">&larr;</button>` +
		`<select id="rz-range"><option value="7">7d</option><option value="14">14d</option><option value="30">30d</option><option value="90">90d</option></select>` +
		`<button id="rz-next" title="longer range">&rarr;</button>` +
		`<div class="spacer"></div>` +
		`<select id="rz-metric"><option value="time">Time worked</option><option value="spend">AI spend</option></select>` +
		`</div>`)
	b.WriteString(`<div id="rz-chart" class="bar-chart"></div><div id="rz-labels" class="bar-labels"></div>` +
		`<div id="rz-summary" class="sub" style="margin-bottom:0"></div></div></div>`) // .section.retro, #sec-retro

	// Embed the raw daily series + wire up the client-side renderer.
	fmt.Fprintf(b, `<script>
var RZ_TIME=%s;
var RZ_SPEND=%s;
var RZ_DEFAULT_RANGE=%d;
(function(){
  var sel=document.getElementById('rz-range'), metric=document.getElementById('rz-metric');
  var savedRange=localStorage.getItem('thaw-dash-range'), savedMetric=localStorage.getItem('thaw-dash-metric');
  var presets=[7,14,30,90];
  var range=savedRange?parseInt(savedRange,10):RZ_DEFAULT_RANGE;
  if(presets.indexOf(range)===-1)range=presets.reduce(function(p,c){return Math.abs(c-range)<Math.abs(p-range)?c:p});
  sel.value=String(range);
  metric.value=savedMetric||'time';

  function fmtDate(d){var p=d.split('-');var months=['Jan','Feb','Mar','Apr','May','Jun','Jul','Aug','Sep','Oct','Nov','Dec'];return months[parseInt(p[1],10)-1]+' '+parseInt(p[2],10)}
  function render(){
    var m=metric.value, data=(m==='time')?RZ_TIME:RZ_SPEND;
    var days=Object.keys(data).sort().slice(-range);
    var chart=document.getElementById('rz-chart'), labels=document.getElementById('rz-labels'), summary=document.getElementById('rz-summary'), rangeLabel=document.getElementById('rz-range-label');
    chart.innerHTML='';labels.innerHTML='';
    if(days.length===0){summary.textContent='no data in this window';rangeLabel.textContent='';return}
    rangeLabel.textContent=fmtDate(days[0])+' → '+fmtDate(days[days.length-1])+' · '+days.length+' of '+range+' day(s) had activity · showing '+(m==='time'?'time worked':'AI spend');
    var max=0,total=0;
    days.forEach(function(d){var v=data[d]||0;if(v>max)max=v;total+=v});
    var bars=[];
    days.forEach(function(d){
      var v=data[d]||0,pct=max>0?Math.round(v/max*100):0;if(pct<2&&v>0)pct=2;
      var bar=document.createElement('div');bar.className='bar'+(m==='spend'?' spend':'');
      bar.style.height='0%%'; // grow-in on next frame, not a static plop-in
      bar.title=d+': '+(m==='time'?(v/3600).toFixed(1)+'h':'$'+v.toFixed(2));
      chart.appendChild(bar);bars.push([bar,pct]);
      var lbl=document.createElement('span');lbl.textContent=d.slice(5);labels.appendChild(lbl);
    });
    requestAnimationFrame(function(){requestAnimationFrame(function(){
      bars.forEach(function(pair){pair[0].style.height=pair[1]+'%%'});
    })});
    if(m==='time'){
      summary.textContent='total '+(total/3600).toFixed(1)+'h across '+days.length+' day(s) with activity, avg '+(total/3600/days.length).toFixed(1)+'h/active-day';
    }else{
      summary.textContent='total $'+total.toFixed(2)+' across '+range+'d — flat-rate, "what it would’ve cost"';
    }
  }
  var autoRotate=!savedMetric; // only auto-rotate if the visitor hasn't picked a metric before
  function stopAuto(){ autoRotate=false; }
  sel.addEventListener('change',function(){range=parseInt(sel.value,10);localStorage.setItem('thaw-dash-range',range);render()});
  metric.addEventListener('change',function(){stopAuto();localStorage.setItem('thaw-dash-metric',metric.value);render()});
  document.getElementById('rz-prev').addEventListener('click',function(){
    var i=presets.indexOf(range);if(i>0){range=presets[i-1];sel.value=String(range);localStorage.setItem('thaw-dash-range',range);render()}
  });
  document.getElementById('rz-next').addEventListener('click',function(){
    var i=presets.indexOf(range);if(i<presets.length-1){range=presets[i+1];sel.value=String(range);localStorage.setItem('thaw-dash-range',range);render()}
  });
  render();
  // Operator ask (2026-08-30): "any other types of charts...you can
  // implement to rotate like news" — auto-cycles Time worked / AI spend
  // every 8s until the visitor manually picks one, same "respect a real
  // choice once made" pattern as the rest of this page's localStorage use.
  if(RZ_TIME && RZ_SPEND) setInterval(function(){
    if(!autoRotate) return;
    metric.value = metric.value==='time' ? 'spend' : 'time';
    render();
  }, 8000);
})();
</script>`, jsonDayMap(timeByDay), jsonDayMapF(spendByDay), rangeDays)
}

// tickerItem is one rotating ticker line — a stated fact, or a reflect
// (question) styled differently so a reader can tell which is which.
type tickerItem struct {
	Text    string
	Reflect bool
}

// buildHighlights picks a handful of real, already-computed facts for the
// rotating ticker. Trimmed 2026-08-29 (3-agent panel review): "leads this
// week" and "top AI spend" duplicated the stat row and the retro zone
// verbatim, just slower — only facts that appear NOWHERE else on the page
// survive here. Every entry must trace to a real number computed elsewhere;
// no invented "goal reached" framing on top of it.
func buildHighlights(rows []ProjectProgress, since time.Time) []tickerItem {
	var hl []tickerItem

	for _, rw := range rows {
		if rw.Report != nil && !rw.Report.Dirty && rw.Report.CommitsThisWeek > 0 {
			hl = append(hl, tickerItem{Text: fmt.Sprintf("%s is clean — %d commit(s) this week, nothing uncommitted", rw.Name, rw.Report.CommitsThisWeek)})
		}
	}

	// Busiest day, from the permanent ledger.
	if byDay, _, err := LedgerHistory(since); err == nil && len(byDay) > 0 {
		var bestDay string
		var bestS int64
		for d, s := range byDay {
			if s > bestS {
				bestDay, bestS = d, s
			}
		}
		if bestDay != "" {
			hl = append(hl, tickerItem{Text: fmt.Sprintf("busiest day: %s at %.1fh combined attention-time", bestDay, float64(bestS)/3600)})
		}
	}

	return hl
}

// buildReflections asks short, data-grounded questions — not psychology
// claims. 2026-08-29: explicitly declined to cite "studies show" or
// attribute meaning that isn't in the data — that's the same shape of
// confident-fiction the EstHours guess and the Backend/Python ledger
// mislabeling were, just dressed as insight instead of a stat. Every
// question here names the real number it's asking about; if the data
// doesn't clear a real threshold, the question is skipped rather than
// forced.
func buildReflections(rows []ProjectProgress, since time.Time) []tickerItem {
	var out []tickerItem

	// Busiest day vs. the window's own average — only asks if it's a real outlier.
	if byDay, _, err := LedgerHistory(since); err == nil && len(byDay) > 1 {
		var bestDay string
		var bestS, totalS int64
		for d, s := range byDay {
			totalS += s
			if s > bestS {
				bestDay, bestS = d, s
			}
		}
		avg := float64(totalS) / float64(len(byDay))
		if avg > 0 && float64(bestS)/avg >= 1.8 {
			out = append(out, tickerItem{Reflect: true, Text: fmt.Sprintf(
				"%s ran %.1fx your average day this window (%.1fh vs %.1fh avg) — a sprint, or is that just how the days go now?",
				bestDay, float64(bestS)/avg, float64(bestS)/3600, avg/3600)})
		}
	}

	// Dirty work sitting untouched for days — a real, named number.
	for _, rw := range rows {
		if rw.Report == nil || !rw.Report.Dirty {
			continue
		}
		idle := time.Since(rw.LastSeen)
		if idle >= 3*24*time.Hour {
			out = append(out, tickerItem{Reflect: true, Text: fmt.Sprintf(
				"%s has had %d uncommitted file(s) sitting for %s — worth finishing, or safe to let go?",
				rw.Name, rw.Report.FilesChanged, agoStr(rw.LastSeen))})
		}
	}

	return out
}

// buildOwnDataRail surfaces real progress.Report fields that exist but were
// never shown anywhere on the page — ahead-of-upstream, TODO counts, a
// this-week commit leaderboard — for the right rail's "more of your own
// data" ask. Every line is a real field, not a repeat of the main ticker.
func buildOwnDataRail(rows []ProjectProgress) []tickerItem {
	var out []tickerItem
	for _, rw := range rows {
		if rw.Report == nil {
			continue
		}
		if rw.Report.AheadOfUpstream > 0 {
			out = append(out, tickerItem{Text: fmt.Sprintf("%s: %d commit(s) ahead of origin — not pushed yet", rw.Name, rw.Report.AheadOfUpstream)})
		}
		if rw.Report.TodoCount > 0 {
			out = append(out, tickerItem{Text: fmt.Sprintf("%s: %d TODO/FIXME marker(s) in source", rw.Name, rw.Report.TodoCount)})
		}
	}
	type lc struct {
		name    string
		commits int
	}
	var top *lc
	for _, rw := range rows {
		if rw.Report == nil || rw.Report.CommitsThisWeek == 0 {
			continue
		}
		if top == nil || rw.Report.CommitsThisWeek > top.commits {
			top = &lc{rw.Name, rw.Report.CommitsThisWeek}
		}
	}
	if top != nil {
		out = append(out, tickerItem{Text: fmt.Sprintf("%s leads this week: %d commit(s)", top.name, top.commits)})
	}
	return out
}

func jsonDayMap(m map[string]int64) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func jsonDayMapF(m map[string]float64) string {
	b, err := json.Marshal(m)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// writeTicker emits a self-contained (no external JS) rotating highlight
// strip: one line visible at a time, fading to the next every 4s. Pure
// CSS-class toggling via a tiny inline script — same self-contained-file
// discipline as the rest of this page.
func writeTicker(b *strings.Builder, items []tickerItem) {
	b.WriteString(`<div class="ticker" id="thaw-ticker">`)
	for i, it := range items {
		cls := ""
		if i == 0 {
			cls = "on"
		}
		if it.Reflect {
			cls = strings.TrimSpace(cls + " reflect")
		}
		fmt.Fprintf(b, `<span class="%s">%s</span>`, cls, html.EscapeString(it.Text))
	}
	b.WriteString(`</div><script>(function(){
var spans=document.querySelectorAll('#thaw-ticker span');
if(spans.length<2)return;
var i=0;
// Visual-panel finding (2026-08-29): the old version removed 'on' from the
// outgoing span and added it to the incoming span in the SAME tick, so both
// opacity transitions ran concurrently — for ~0.6s two lines of text were
// genuinely both partially visible, overlapping. Sequential now: fade the
// current one out completely, THEN start fading the next one in.
setInterval(function(){
  spans[i].classList.remove('on');
  var next=(i+1)%spans.length;
  setTimeout(function(){spans[next].classList.add('on')},650);
  i=next;
},8000);
})();</script>`)
}

func writeStatBox(b *strings.Builder, num, label string) {
	fmt.Fprintf(b, `<div class="stat"><div class="n">%s</div><div class="l">%s</div></div>`, num, label)
}

// httpURLOnly returns u unchanged if it's a plain http(s) URL, else "".
// RSS content (news headlines/images) is untrusted external input — this
// is the gate that stops a compromised feed from handing back a
// javascript: URI in an href, or forcing the browser to fetch an
// attacker-chosen/internal URL via <img src>, when the dashboard opens.
func httpURLOnly(u string) string {
	u = strings.TrimSpace(u)
	if strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") {
		return u
	}
	return ""
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// agoStr renders a coarse "how long ago" string — matches the resume
// screen's honesty level, no false precision.
func agoStr(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/24))
	}
}

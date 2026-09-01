package dashboard

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/export"
	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/pkg/models"
)

// claudeLaunchCmd builds the shell-safe "click to copy" command for a
// project slide. Operator ask (2026-08-29): "I should be able to click the
// advice as well, aka if I click a project, it defaults to open claude in
// terminal and ask the prompt ready to go." A static HTML page can't
// actually spawn a terminal, so this is the same honest copy-to-clipboard
// pattern as Past sessions — clicking copies the exact command to paste.
// rw.Root is already an absolute path from project.FindRepoRoot, so single
// quotes are enough; prompt text must avoid apostrophes to stay safe inside
// the single-quoted shell literal.
func claudeLaunchCmd(root, prompt string) string {
	cmd := fmt.Sprintf("cd '%s' && claude", root)
	if prompt != "" {
		cmd += fmt.Sprintf(" '%s'", prompt)
	}
	return cmd
}

// kioskSlide is one full-screen rotation — a single giant fact, nothing
// else on screen with it. Action, when set, is a shell command the slide
// can be clicked to copy — same honest pattern as Past sessions in the
// report (a static page can't spawn a terminal, so "click" means "copy
// the exact command to run yourself"). Category is what the settings panel
// filters on — one slide, one category, so "hide news" or "hide deadlines"
// is a straight match, not guesswork.
type kioskSlide struct {
	Headline string `json:"h"`
	Meta     string `json:"m"`
	Action   string `json:"a,omitempty"`
	Category string `json:"c"`
}

// kioskCategories is the fixed, ordered list the settings panel builds its
// checkboxes from. Adding a new slide category means adding it here too —
// otherwise it can't be hidden/shown from the panel.
var kioskCategories = []struct{ Key, Label string }{
	{"hero", "Uncommitted work"},
	{"project", "Per-project activity"},
	{"deadline", "Deadlines"},
	{"time", "Time by project"},
	{"spend", "AI spend"},
	{"freeze", "Last freeze"},
	{"highlight", "Highlights"},
	{"reflection", "Reflections"},
	{"news", "News"},
}

// GenerateKiosk is a different product mode from Generate, not a variant of
// it: full-viewport, one giant fact at a time, auto-rotating, zero scroll.
// Direct operator ask (2026-08-29): "the giant titles, rotating... take up
// full screen, I don't want to scroll... maximize all screen space." The
// dense report (Generate) stays for when you actually want to read
// details — this is the ambient/glance mode, same underlying real data,
// completely different presentation. extras controls whether news
// headlines join the rotation, same scope line as the report mode.
func GenerateKiosk(records []export.Record, rangeDays int, extras, summarize bool) string {
	rows, _ := Collect(records)
	since := time.Now().AddDate(0, 0, -rangeDays)

	var slides []kioskSlide

	// Operator feedback (2026-08-29): "the headlines are boring and hard to
	// understand" — the first version reduced every fact to a bare
	// fragment ("paymaster: 16 commits this week") with a throwaway
	// one-word meta line. This version writes each headline as an actual
	// sentence and puts a SECOND real fact in the meta line instead of a
	// filler tag, and adds several slide categories that were previously
	// dropped entirely (dirty-but-no-commits projects, time-by-project,
	// AI spend, the most recent freeze) — same "more detailed" ask.

	// Hero — leads the rotation, now with the actual filenames as detail
	// (same data the report's hero-detail line already computes).
	var dirtyFiles, dirtyProjects int
	var busiestDirty *ProjectProgress
	for i, rw := range rows {
		if rw.Report != nil && rw.Report.Dirty {
			dirtyFiles += rw.Report.FilesChanged
			dirtyProjects++
			if busiestDirty == nil || rw.Report.FilesChanged > busiestDirty.Report.FilesChanged {
				busiestDirty = &rows[i]
			}
		}
	}
	if dirtyProjects > 0 {
		meta := fmt.Sprintf("across %d project%s — nothing lost, just not committed yet", dirtyProjects, plural(dirtyProjects))
		if busiestDirty != nil {
			if samples := dirtyFileNames(busiestDirty.Root, 3); len(samples) > 0 {
				meta = busiestDirty.Name + ": " + strings.Join(samples, ", ")
			}
		}
		slides = append(slides, kioskSlide{
			Headline: fmt.Sprintf("%d file%s waiting to be committed", dirtyFiles, plural(dirtyFiles)),
			Meta:     meta,
			Category: "hero",
		})
	} else if len(rows) > 0 {
		slides = append(slides, kioskSlide{
			Headline: fmt.Sprintf("everything's committed across %d project%s", len(rows), plural(len(rows))),
			Meta:     "clean slate — nothing outstanding right now",
			Category: "hero",
		})
	}

	// Deadlines — read-only, live, same unverified-accuracy caveat the
	// deadlines file carries everywhere else — deadline math itself is a
	// hard line this codebase never touches, this only ever displays what
	// the file already says. Opt-in via THAW_DEADLINES_FILE (empty = no
	// deadline slides). Placed right after the hero — "what's about to
	// bite you" outranks per-project git trivia.
	if path := DeadlinesFile(); path != "" {
		for _, d := range NextDeadlines(path, 5) {
			headline := d.Text
			if d.Urgent {
				headline = "⚠ " + headline // the file's own OVERDUE-SOON marker, not a date thaw computed
			}
			slides = append(slides, kioskSlide{
				Headline: headline,
				Meta:     d.Date + " — deadlines file (unverified)",
				Category: "deadline",
			})
		}
	}

	// Per-project — every tracked project gets a real sentence, not just
	// the ones with commits. A dirty-no-commits project used to vanish
	// entirely from the rotation; now it says exactly what's true.
	for _, rw := range rows {
		if rw.Report == nil {
			continue
		}
		switch {
		case rw.Report.CommitsThisWeek > 0:
			headline := fmt.Sprintf("%s shipped %d commit%s this week", rw.Name, rw.Report.CommitsThisWeek, plural(rw.Report.CommitsThisWeek))
			meta := fmt.Sprintf("+%d/-%d lines on branch %s", rw.Report.Insertions, rw.Report.Deletions, rw.Report.Branch)
			if rw.Report.Dirty {
				meta += fmt.Sprintf(" · %d file(s) still uncommitted", rw.Report.FilesChanged)
			}
			slides = append(slides, kioskSlide{Headline: headline, Meta: meta, Action: claudeLaunchCmd(rw.Root, "whats next in this project"), Category: "project"})
		case rw.Report.Dirty:
			slides = append(slides, kioskSlide{
				Headline: fmt.Sprintf("%s has %d file%s sitting uncommitted", rw.Name, rw.Report.FilesChanged, plural(rw.Report.FilesChanged)),
				Meta:     fmt.Sprintf("last touched %s, no commits this week", agoStr(rw.LastSeen)),
				Action:   claudeLaunchCmd(rw.Root, "help me review and finish whats uncommitted here"),
				Category: "project",
			})
		}
		if rw.Report.AheadOfUpstream > 0 {
			slides = append(slides, kioskSlide{
				Headline: fmt.Sprintf("%s is %d commit%s ahead of origin", rw.Name, rw.Report.AheadOfUpstream, plural(rw.Report.AheadOfUpstream)),
				Meta:     "not pushed yet",
				Action:   claudeLaunchCmd(rw.Root, "help me review these commits before I push"),
				Category: "project",
			})
		}
		if note, ts := LastProjectNote(rw.Root); note != "" {
			slides = append(slides, kioskSlide{Headline: note, Meta: "what happened last time in " + rw.Name + " · " + agoStr(ts), Action: claudeLaunchCmd(rw.Root, ""), Category: "project"})
		}
	}

	// Time by project — real, permanent ledger data, never shown in the
	// first kiosk version at all.
	longSince := time.Now().AddDate(0, 0, -90)
	timeByDay, timeByProject, _ := LedgerHistory(longSince)
	type tpEnt struct {
		name string
		secs int64
	}
	var tps []tpEnt
	for _, rw := range rows {
		if s, ok := timeByProject[rw.Name]; ok && s > 0 {
			tps = append(tps, tpEnt{rw.Name, s})
		}
	}
	sort.Slice(tps, func(i, j int) bool { return tps[i].secs > tps[j].secs })
	for _, t := range tps {
		slides = append(slides, kioskSlide{
			Headline: fmt.Sprintf("%.0fh spent in %s", float64(t.secs)/3600, t.name),
			Meta:     "combined attention-time, last 90 days",
			Category: "time",
		})
	}
	if len(timeByDay) > 1 {
		var bestDay string
		var bestS, totalS int64
		for d, s := range timeByDay {
			totalS += s
			if s > bestS {
				bestDay, bestS = d, s
			}
		}
		if t, err := time.Parse("2006-01-02", bestDay); err == nil {
			slides = append(slides, kioskSlide{
				Headline: fmt.Sprintf("%s was your busiest day — %.0fh", t.Format("Jan 2"), float64(bestS)/3600),
				Meta:     fmt.Sprintf("%.0fh total across the last %d days", float64(totalS)/3600, rangeDays),
				Category: "time",
			})
		}
	}

	// AI spend per project — real, transcript-derived.
	roots := make([]string, len(rows))
	for i, rw := range rows {
		roots[i] = rw.Root
	}
	_, spendByProject := AISpendHistory(roots, longSince)
	for _, rw := range rows {
		if c, ok := spendByProject[rw.Root]; ok && c > 0 {
			slides = append(slides, kioskSlide{
				Headline: fmt.Sprintf("$%.2f in AI spend on %s", c, rw.Name),
				Meta:     "list-price estimate, last 90 days — you're on flat-rate",
				Category: "spend",
			})
		}
	}

	// Most recent freeze — the actual "what was I just doing" fact.
	if store, err := snapshot.Open(); err == nil {
		if summaries, err := store.List(1); err == nil && len(summaries) > 0 {
			s := summaries[0]
			what := s.Name
			var snap *models.Snapshot
			if what == "" {
				if got, err := store.Get(s.ID); err == nil {
					snap = got
					for _, sess := range got.Sessions {
						if sess.CWD == "" {
							continue
						}
						if msg := RecentUserMessage(sess.CWD, s.CreatedAt); msg != "" {
							what = msg
							break
						}
					}
				}
			}
			if what == "" {
				what = loadSnapshotNotes()[s.ID]
			}
			if what == "" && summarize {
				if snap == nil {
					snap, _ = store.Get(s.ID)
				}
				if snap != nil {
					if summary := summarizeSnapshot(snap.Sessions); summary != "" {
						appendSnapshotNote(s.ID, summary)
						what = summary
					}
				}
			}
			if what == "" {
				switch {
				case s.Intent != "" && !genericIntentRe.MatchString(s.Intent):
					what = s.Intent
				case s.Command != "":
					what = s.Command
				case s.Intent != "":
					what = s.Intent
				case len(s.Projects) > 0:
					what = strings.Join(s.Projects, " + ")
				default:
					what = fmt.Sprintf("%d terminal%s", s.SessionCount, plural(s.SessionCount))
				}
			}
			slides = append(slides, kioskSlide{
				Headline: fmt.Sprintf("last frozen %s — %s", agoStr(s.CreatedAt), what),
				Meta:     fmt.Sprintf("thaw recall %d to bring it back", s.ID),
				Action:   fmt.Sprintf("thaw recall %d", s.ID),
				Category: "freeze",
			})
		}
		store.Close()
	}

	// Same facts/reflections the report's ticker uses — real data, no
	// duplication of logic. Skip "busiest day:" here specifically — the
	// slide built above already covers that fact with clearer phrasing
	// (human date, no "combined attention-time" jargon); this would just
	// be a worse-worded duplicate.
	for _, it := range buildHighlights(rows, since) {
		if strings.HasPrefix(it.Text, "busiest day:") {
			continue
		}
		slides = append(slides, kioskSlide{Headline: it.Text, Meta: "from your own activity", Category: "highlight"})
	}
	for _, it := range buildReflections(rows, since) {
		slides = append(slides, kioskSlide{Headline: it.Text, Meta: "a question worth sitting with", Category: "reflection"})
	}

	// News, same --extras gate as the report mode. Deduped (FetchNews),
	// and pulled deeper per source now that duplicates aren't inflating the
	// count — operator feedback: "too many repeating stories."
	if extras {
		newsCfg, _ := config.Load()
		for _, n := range FetchNews(newsCfg.News.Sources, 12) {
			slides = append(slides, kioskSlide{Headline: n.Title, Meta: n.Source, Action: httpURLOnly(n.URL), Category: "news"})
		}
	}

	if len(slides) == 0 {
		slides = append(slides, kioskSlide{Headline: "nothing to show yet — run thaw freeze in a project", Meta: "thaw", Category: "hero"})
	}

	slidesJSON, _ := json.Marshal(slides)
	catsJSON, _ := json.Marshal(kioskCategories)

	var b strings.Builder
	b.WriteString(`<!DOCTYPE html><html><head><meta charset="utf-8"><title>thaw kiosk</title>
<style>
@import url('https://fonts.googleapis.com/css2?family=Fraunces:opsz,wght@9..144,600;9..144,700&family=Inter:wght@500;600&display=swap');
*{margin:0;padding:0;box-sizing:border-box}
html,body{width:100%;height:100%;overflow:hidden;background:#0b1f24}
body{display:flex;align-items:center;justify-content:center;font-family:'Inter',sans-serif}
#slide{max-width:92vw;text-align:center;opacity:0;transition:opacity 0.8s ease;cursor:default}
#slide.on{opacity:1}
#slide.clickable{cursor:pointer}
#headline{font-family:'Fraunces',serif;font-weight:700;font-size:min(9vw,110px);line-height:1.08;color:#eef6f5;letter-spacing:-1px;text-shadow:0 0 40px rgba(232,147,90,0.18)}
#meta{margin-top:28px;font-family:'Inter',sans-serif;font-weight:600;font-size:min(2.2vw,26px);letter-spacing:2px;text-transform:uppercase;color:#4a9b8e}
#copyhint{margin-top:16px;font-family:'Inter',sans-serif;font-weight:600;font-size:16px;letter-spacing:1px;color:#e8935a;opacity:0;transition:opacity 0.3s ease}
#copyhint.on{opacity:1}
#dots{position:fixed;bottom:32px;left:0;right:0;display:flex;justify-content:center;gap:8px}
.dot{width:8px;height:8px;border-radius:50%;background:rgba(74,155,142,0.25)}
.dot.on{background:#4a9b8e}
#gear{position:fixed;top:24px;right:24px;width:40px;height:40px;border-radius:50%;background:rgba(74,155,142,0.12);color:#a8d8dc;border:1px solid rgba(168,216,220,0.2);font-size:18px;cursor:pointer;z-index:10}
#gear:hover{background:rgba(74,155,142,0.22)}
#panel{position:fixed;top:0;right:0;bottom:0;width:340px;max-width:88vw;background:#0e2a30;border-left:1px solid rgba(168,216,220,0.15);transform:translateX(100%);transition:transform 0.3s ease;z-index:20;overflow-y:auto;padding:64px 24px 24px;font-family:'Inter',sans-serif;color:#eef6f5}
#panel.on{transform:translateX(0)}
#panel h2{font-family:'Fraunces',serif;font-size:22px;margin-bottom:20px;color:#eef6f5}
#panel label{display:flex;align-items:center;gap:10px;margin-bottom:12px;font-size:15px;cursor:pointer;color:#eef6f5}
#panel input[type=checkbox]{width:16px;height:16px;accent-color:#4a9b8e}
#panel .sect{font-size:12px;letter-spacing:1.5px;text-transform:uppercase;color:#4a9b8e;margin:20px 0 10px;font-weight:600}
#panel input[type=range]{width:100%;accent-color:#4a9b8e}
#panel .speedval{font-size:13px;color:#a8d8dc;margin-top:4px}
#panel .note{font-size:12px;color:rgba(238,246,245,0.5);margin-top:24px;line-height:1.5}
#closepanel{position:absolute;top:20px;right:20px;background:none;border:none;color:#a8d8dc;font-size:22px;cursor:pointer}
</style></head><body>
<button id="gear" title="Settings">&#9881;</button>
<div id="panel">
<button id="closepanel">&times;</button>
<h2>Settings</h2>
<div class="sect">Rotation speed</div>
<input type="range" id="speed" min="2" max="20" step="1">
<div class="speedval" id="speedval"></div>
<div class="sect">Show on rotation</div>
<div id="catlist"></div>
<div class="note">Saved to this browser only — matches what you asked for last (deadlines, per-project clicks, news). Doesn't touch config.toml or other machines.</div>
</div>
<div id="slide"><div id="headline"></div><div id="meta"></div><div id="copyhint">click to copy: run it in a terminal</div></div>
<div id="dots"></div>
<script>
var ALL_SLIDES=`)
	b.Write(slidesJSON)
	b.WriteString(`;
var CATS=`)
	b.Write(catsJSON)
	b.WriteString(`;
var PREFS_KEY='thawKioskPrefs';
function loadPrefs(){
  var d={speed:5,hidden:[]};
  try{var raw=localStorage.getItem(PREFS_KEY); if(raw) return Object.assign(d, JSON.parse(raw));}catch(e){}
  return d;
}
function savePrefs(p){ try{localStorage.setItem(PREFS_KEY, JSON.stringify(p));}catch(e){} }
var prefs=loadPrefs();
var SLIDES, i, timer;
var slideEl=document.getElementById('slide'), hEl=document.getElementById('headline'), mEl=document.getElementById('meta'), dotsEl=document.getElementById('dots'), hintEl=document.getElementById('copyhint');
var gearEl=document.getElementById('gear'), panelEl=document.getElementById('panel'), catlistEl=document.getElementById('catlist'), speedEl=document.getElementById('speed'), speedvalEl=document.getElementById('speedval');

function applyFilter(){
  SLIDES = ALL_SLIDES.filter(function(s){ return prefs.hidden.indexOf(s.c) === -1; });
  if(SLIDES.length===0) SLIDES = ALL_SLIDES;
  i=0;
  dotsEl.innerHTML='';
  SLIDES.forEach(function(){var d=document.createElement('div');d.className='dot';dotsEl.appendChild(d)});
  show(0);
}
function show(idx){
  var s=SLIDES[idx];
  hEl.textContent=s.h;
  mEl.textContent=s.m;
  var dots=dotsEl.querySelectorAll('.dot');
  dots.forEach(function(d,j){d.classList.toggle('on',j===idx)});
  slideEl.classList.add('on');
  slideEl.classList.toggle('clickable', !!s.a);
  hintEl.classList.remove('on');
}
function restartTimer(){
  if(timer) clearInterval(timer);
  if(SLIDES.length>1){
    timer=setInterval(function(){
      slideEl.classList.remove('on');
      setTimeout(function(){i=(i+1)%SLIDES.length;show(i)},500);
    }, prefs.speed*1000);
  }
}
slideEl.addEventListener('click', function(){
  var s=SLIDES[i];
  if(!s || !s.a) return;
  if(navigator.clipboard) navigator.clipboard.writeText(s.a);
  hintEl.textContent='copied — paste it in a terminal';
  hintEl.classList.add('on');
  setTimeout(function(){hintEl.classList.remove('on')},1500);
});

CATS.forEach(function(c){
  var lbl=document.createElement('label');
  var cb=document.createElement('input'); cb.type='checkbox'; cb.checked = prefs.hidden.indexOf(c.Key)===-1;
  cb.addEventListener('change', function(){
    var idx=prefs.hidden.indexOf(c.Key);
    if(cb.checked && idx>-1) prefs.hidden.splice(idx,1);
    if(!cb.checked && idx===-1) prefs.hidden.push(c.Key);
    savePrefs(prefs);
    applyFilter();
    restartTimer();
  });
  lbl.appendChild(cb);
  lbl.appendChild(document.createTextNode(c.Label));
  catlistEl.appendChild(lbl);
});
speedEl.value=prefs.speed;
speedvalEl.textContent=prefs.speed+'s per slide';
speedEl.addEventListener('input', function(){
  prefs.speed=parseInt(speedEl.value,10);
  speedvalEl.textContent=prefs.speed+'s per slide';
  savePrefs(prefs);
  restartTimer();
});
gearEl.addEventListener('click', function(){ panelEl.classList.add('on'); });
document.getElementById('closepanel').addEventListener('click', function(){ panelEl.classList.remove('on'); });

applyFilter();
restartTimer();
</script>
</body></html>`)
	return b.String()
}

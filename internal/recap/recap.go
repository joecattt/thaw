package recap

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/diff"
	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/pkg/models"
)

// DayRecap represents a single day's work summary.
type DayRecap struct {
	Date        string
	Transitions []Transition
	Projects    map[string]*ProjectBlock
	TotalTime   time.Duration
	FirstSeen   time.Time
	LastSeen    time.Time
}

// Transition represents a context switch or activity change.
type Transition struct {
	Time      time.Time
	Action    string // started | switched | returned | finished
	Project   string
	Branch    string
	Detail    string
	Sessions  int
}

// ProjectBlock tracks aggregate time and activity per project.
type ProjectBlock struct {
	Name       string
	TotalTime  time.Duration
	Branches   []string
	Commands   []string
	Intents    []string
	FirstSeen  time.Time
	LastSeen   time.Time
}

// Generate builds a recap for the given date range from snapshot history.
func Generate(store *snapshot.Store, from, to time.Time) (*DayRecap, error) {
	// Load all snapshots in range
	summaries, err := store.List(500) // get a large batch
	if err != nil {
		return nil, err
	}

	// Filter to date range and load full snapshots
	var snaps []*models.Snapshot
	for _, s := range summaries {
		if s.CreatedAt.Before(from) || s.CreatedAt.After(to) {
			continue
		}
		snap, err := store.Get(s.ID)
		if err != nil || snap == nil {
			continue
		}
		snaps = append(snaps, snap)
	}

	// Sort chronologically (oldest first)
	sort.Slice(snaps, func(i, j int) bool {
		return snaps[i].CreatedAt.Before(snaps[j].CreatedAt)
	})

	if len(snaps) == 0 {
		return nil, fmt.Errorf("no snapshots found for %s", from.Format("2006-01-02"))
	}

	recap := &DayRecap{
		Date:     from.Format("2006-01-02"),
		Projects: make(map[string]*ProjectBlock),
	}

	// Walk consecutive snapshot pairs and detect transitions
	var lastProject string
	for i, snap := range snaps {
		if i == 0 {
			recap.FirstSeen = snap.CreatedAt
		}
		recap.LastSeen = snap.CreatedAt

		// Determine primary project from sessions
		project := primaryProject(snap)
		branch := primaryBranch(snap)

		// Track project time
		pb, ok := recap.Projects[project]
		if !ok {
			pb = &ProjectBlock{Name: project, FirstSeen: snap.CreatedAt}
			recap.Projects[project] = pb
		}
		pb.LastSeen = snap.CreatedAt
		if branch != "" && !containsStr(pb.Branches, branch) {
			pb.Branches = append(pb.Branches, branch)
		}

		// Collect intents
		if snap.Intent != "" && !containsStr(pb.Intents, snap.Intent) {
			pb.Intents = append(pb.Intents, snap.Intent)
		}
		for _, s := range snap.Sessions {
			if s.Intent != "" && !containsStr(pb.Intents, s.Intent) {
				pb.Intents = append(pb.Intents, s.Intent)
			}
		}

		// Detect transitions
		if i == 0 {
			recap.Transitions = append(recap.Transitions, Transition{
				Time:    snap.CreatedAt,
				Action:  "started",
				Project: project,
				Branch:  branch,
				Sessions: len(snap.Sessions),
			})
			lastProject = project
			continue
		}

		// Diff with previous snapshot
		result := diff.Compare(snaps[i-1], snap)
		hasChanges := len(result.Added) > 0 || len(result.Removed) > 0 || len(result.Changed) > 0

		if project != lastProject {
			action := "switched"
			if wasSeenBefore(project, recap.Transitions) {
				action = "returned"
			}
			detail := ""
			if hasChanges {
				detail = formatChangesSummary(result)
			}
			recap.Transitions = append(recap.Transitions, Transition{
				Time:     snap.CreatedAt,
				Action:   action,
				Project:  project,
				Branch:   branch,
				Detail:   detail,
				Sessions: len(snap.Sessions),
			})
			lastProject = project
		} else if hasChanges && isSignificantChange(result) {
			recap.Transitions = append(recap.Transitions, Transition{
				Time:    snap.CreatedAt,
				Action:  "updated",
				Project: project,
				Branch:  branch,
				Detail:  formatChangesSummary(result),
				Sessions: len(snap.Sessions),
			})
		}
	}

	// Calculate project durations
	for _, pb := range recap.Projects {
		if !pb.FirstSeen.IsZero() && !pb.LastSeen.IsZero() {
			pb.TotalTime = pb.LastSeen.Sub(pb.FirstSeen)
		}
	}
	recap.TotalTime = recap.LastSeen.Sub(recap.FirstSeen)

	return recap, nil
}

// FormatText renders the recap as a readable terminal summary.
func FormatText(r *DayRecap) string {
	var b strings.Builder

	b.WriteString(fmt.Sprintf("━━━ %s ━━━\n\n", r.Date))

	if len(r.Transitions) == 0 {
		b.WriteString("No activity recorded.\n")
		return b.String()
	}

	for _, t := range r.Transitions {
		timeStr := t.Time.Format("3:04 PM")
		icon := actionIcon(t.Action)

		b.WriteString(fmt.Sprintf("%s %s  %s %s", timeStr, icon, t.Action, t.Project))
		if t.Branch != "" {
			b.WriteString(fmt.Sprintf(" (%s)", t.Branch))
		}
		b.WriteString("\n")

		if t.Detail != "" {
			b.WriteString(fmt.Sprintf("         %s\n", t.Detail))
		}
		if t.Sessions > 0 && t.Action == "started" {
			b.WriteString(fmt.Sprintf("         %d session(s)\n", t.Sessions))
		}
	}

	// Project summary
	b.WriteString("\n━━━ projects ━━━\n\n")
	for _, pb := range sortedProjects(r.Projects) {
		duration := formatDuration(pb.TotalTime)
		b.WriteString(fmt.Sprintf("  %s — %s\n", pb.Name, duration))
		if len(pb.Branches) > 0 {
			b.WriteString(fmt.Sprintf("    branches: %s\n", strings.Join(pb.Branches, ", ")))
		}
		if len(pb.Intents) > 0 {
			for _, intent := range pb.Intents[:min(3, len(pb.Intents))] {
				b.WriteString(fmt.Sprintf("    • %s\n", intent))
			}
		}
	}

	b.WriteString(fmt.Sprintf("\n%s → %s (%s)\n",
		r.FirstSeen.Format("3:04 PM"),
		r.LastSeen.Format("3:04 PM"),
		formatDuration(r.TotalTime)))

	return b.String()
}

// FormatVoiceBrief generates a 15-second spoken summary.
func FormatVoiceBrief(r *DayRecap) string {
	projects := sortedProjects(r.Projects)
	if len(projects) == 0 {
		return "No work activity recorded today."
	}

	var parts []string
	parts = append(parts, fmt.Sprintf("You worked on %d project%s today.",
		len(projects), plural(len(projects))))

	// Most time spent
	if len(projects) > 0 {
		top := projects[0]
		parts = append(parts, fmt.Sprintf("Most time in %s, about %s.",
			top.Name, formatDuration(top.TotalTime)))
		if len(top.Intents) > 0 {
			parts = append(parts, top.Intents[0]+".")
		}
	}

	// Context switches
	switches := 0
	for _, t := range r.Transitions {
		if t.Action == "switched" || t.Action == "returned" {
			switches++
		}
	}
	if switches > 0 {
		parts = append(parts, fmt.Sprintf("%d context switch%s.", switches, plural(switches)))
	}

	return strings.Join(parts, " ")
}

// FormatVoiceFull generates a 1-minute spoken summary.
func FormatVoiceFull(r *DayRecap) string {
	var parts []string
	parts = append(parts, fmt.Sprintf("Here's your work recap for %s.", r.Date))
	parts = append(parts, "")

	for _, t := range r.Transitions {
		timeStr := t.Time.Format("3:04 PM")
		switch t.Action {
		case "started":
			parts = append(parts, fmt.Sprintf("At %s, you started working on %s.", timeStr, t.Project))
		case "switched":
			parts = append(parts, fmt.Sprintf("At %s, you switched to %s.", timeStr, t.Project))
		case "returned":
			parts = append(parts, fmt.Sprintf("At %s, you came back to %s.", timeStr, t.Project))
		case "updated":
			if t.Detail != "" {
				parts = append(parts, fmt.Sprintf("At %s, %s.", timeStr, t.Detail))
			}
		}
		if t.Branch != "" && (t.Action == "started" || t.Action == "switched") {
			parts = append(parts, fmt.Sprintf("On branch %s.", t.Branch))
		}
	}

	parts = append(parts, "")
	parts = append(parts, fmt.Sprintf("Total working time: %s.", formatDuration(r.TotalTime)))

	return strings.Join(parts, " ")
}

// VoiceConfig holds TTS backend configuration.
type VoiceConfig struct {
	Backend     string // say | cortana | elevenlabs | piper | espeak
	CortanaPath string // path to cortana project (for Coqui XTTS)
	ElevenPath  string // path to elevenlabs-voice project
	KokoroMode  bool   // use kokoro backend in elevenlabs-voice
}

// DefaultVoiceConfig returns platform-appropriate defaults.
func DefaultVoiceConfig() VoiceConfig {
	return VoiceConfig{Backend: "auto"}
}

// SpeakWithConfig sends text to the configured TTS backend.
func SpeakWithConfig(text string, cfg VoiceConfig) error {
	backend := cfg.Backend
	if backend == "auto" || backend == "" {
		backend = detectBestBackend(cfg)
	}

	switch backend {
	case "cortana":
		if cfg.CortanaPath == "" {
			return fmt.Errorf("cortana_path not set in config")
		}
		// Coqui XTTS v2 voice cloning from cortana.wav reference
		tmpWav := filepath.Join(os.TempDir(), "thaw-voice.wav")
		script := fmt.Sprintf(`
import os, sys
os.environ["COQUI_TOS_AGREED"] = "1"
from TTS.api import TTS
tts = TTS("tts_models/multilingual/multi-dataset/xtts_v2", gpu=False)
tts.tts_to_file(text=%q, speaker_wav=%q, file_path=%q, language="en")
`, text, cfg.CortanaPath, tmpWav)
		pythonBin := "python3"
		home, _ := os.UserHomeDir()
		venvPython := filepath.Join(home, "tts-env2", "bin", "python3")
		if _, err := os.Stat(venvPython); err == nil {
			pythonBin = venvPython
		}
		cmd := exec.Command(pythonBin, "-c", script)
		cmd.Env = append(os.Environ(), "COQUI_TOS_AGREED=1")
		if err := cmd.Run(); err != nil {
			return err
		}
		return exec.Command("afplay", tmpWav).Run()

	case "elevenlabs":
		if cfg.ElevenPath == "" {
			return fmt.Errorf("elevenlabs_path not set in config")
		}
		backendArg := "elevenlabs"
		if cfg.KokoroMode {
			backendArg = "kokoro"
		}
		script := fmt.Sprintf(
			"const {speak} = require('%s/src/voice.js'); speak(%q, {backend: '%s'})",
			cfg.ElevenPath, text, backendArg)
		return exec.Command("node", "-e", script).Run()

	case "piper":
		cmd := exec.Command("piper", "--model", "en_US-lessac-medium", "--output-raw")
		cmd.Stdin = strings.NewReader(text)
		aplay := exec.Command("aplay", "-r", "22050", "-f", "S16_LE", "-t", "raw", "-")
		aplay.Stdin, _ = cmd.StdoutPipe()
		aplay.Start()
		cmd.Run()
		return aplay.Wait()

	case "espeak":
		if _, err := exec.LookPath("espeak-ng"); err == nil {
			return exec.Command("espeak-ng", "-s", "160", text).Run()
		}
		return exec.Command("espeak", "-s", "160", text).Run()

	default: // "say" or fallback
		if runtime.GOOS == "darwin" {
			return exec.Command("say", "-r", "180", text).Run()
		}
		return fmt.Errorf("no TTS backend available — set tts_backend in config")
	}
}

// Speak sends text to the best available TTS (backward compat).
func Speak(text string) error {
	return SpeakWithConfig(text, DefaultVoiceConfig())
}

func detectBestBackend(cfg VoiceConfig) string {
	// Cortana reference WAV available? Use XTTS voice cloning — best quality
	if cfg.CortanaPath != "" {
		if _, err := os.Stat(cfg.CortanaPath); err == nil {
			return "cortana"
		}
	}
	// ElevenLabs project available?
	if cfg.ElevenPath != "" {
		if _, err := os.Stat(filepath.Join(cfg.ElevenPath, "src", "voice.js")); err == nil {
			return "elevenlabs"
		}
	}
	// macOS say
	if runtime.GOOS == "darwin" {
		return "say"
	}
	// Linux: piper > espeak
	if _, err := exec.LookPath("piper"); err == nil {
		return "piper"
	}
	return "espeak"
}

// GenerateHTML creates a visual timeline as an HTML file and returns its path.
func GenerateHTML(r *DayRecap) (string, error) {
	html := buildTimelineHTML(r)

	dir, _ := os.UserHomeDir()
	outDir := filepath.Join(dir, ".local", "share", "thaw")
	os.MkdirAll(outDir, 0700)
	path := filepath.Join(outDir, "recap.html")

	if err := os.WriteFile(path, []byte(html), 0600); err != nil {
		return "", err
	}
	return path, nil
}

// OpenInBrowser opens a URL in the default browser.
func OpenInBrowser(url string) error {
	switch runtime.GOOS {
	case "darwin":
		return exec.Command("open", url).Run()
	case "linux":
		return exec.Command("xdg-open", url).Run()
	default:
		return fmt.Errorf("cannot open browser on %s", runtime.GOOS)
	}
}

func buildTimelineHTML(r *DayRecap) string {
	// Build timeline entries
	var timelineRows strings.Builder
	colors := map[string]string{
		"started": "#56d07a", "switched": "#5aabef",
		"returned": "#b89de8", "updated": "#607888", "finished": "#607888",
	}
	for i, t := range r.Transitions {
		c := colors[t.Action]
		if c == "" {
			c = "#607888"
		}
		detail := t.Detail
		if detail == "" && r.Projects[t.Project] != nil && len(r.Projects[t.Project].Intents) > 0 {
			detail = r.Projects[t.Project].Intents[0]
		}
		if t.Branch != "" && detail != "" {
			detail = t.Branch + " — " + detail
		} else if t.Branch != "" {
			detail = t.Branch
		}
		timelineRows.WriteString(fmt.Sprintf(`
		<div class="tl-row" style="animation-delay:%.2fs">
			<div class="tl-time">%s</div>
			<div class="tl-dot-wrap"><div class="tl-dot" style="background:%s;box-shadow:0 0 8px %s50"></div><div class="tl-line" style="background:linear-gradient(%s30,transparent)"></div></div>
			<div class="tl-body"><div class="tl-title">%s %s</div><div class="tl-sub">%s</div></div>
		</div>`, 0.1+float64(i)*0.08, t.Time.Format("3:04"), c, c, c, t.Action, t.Project, detail))
	}

	// Build project rows
	var projectRows strings.Builder
	projectColors := []string{"#56d07a", "#5aabef", "#b89de8", "#e8a855", "#ef6b6b"}
	for i, pb := range sortedProjects(r.Projects) {
		c := projectColors[i%len(projectColors)]
		branches := strings.Join(pb.Branches, ", ")
		intentText := ""
		if len(pb.Intents) > 0 {
			var short []string
			for _, in := range pb.Intents {
				if len(in) > 50 {
					in = in[:47] + "..."
				}
				short = append(short, in)
				if len(short) >= 2 {
					break
				}
			}
			intentText = strings.Join(short, ", ")
		}
		projectRows.WriteString(fmt.Sprintf(`
		<div class="glass proj-row" style="animation-delay:%.2fs">
			<div class="proj-left">
				<div class="proj-icon" style="background:%s0d;border-color:%s20;color:%s">%s</div>
				<div>
					<div class="proj-name">%s</div>
					<div class="proj-meta">
						<span class="proj-branch" style="color:%s;background:%s0a">%s</span>
						<span class="proj-detail">%s</span>
					</div>
				</div>
			</div>
			<div class="proj-dur">%s</div>
		</div>`, 0.06+float64(i)*0.1, c, c, c, string(pb.Name[0]),
			pb.Name, c, c, branches, intentText, formatDuration(pb.TotalTime)))
	}

	// Context switches count
	switches := 0
	for _, t := range r.Transitions {
		if t.Action == "switched" || t.Action == "returned" {
			switches++
		}
	}

	return fmt.Sprintf(`<!DOCTYPE html>
<html><head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1">
<title>thaw — %s</title>
<link href="https://fonts.googleapis.com/css2?family=Instrument+Serif:ital@0;1&family=DM+Sans:opsz,wght@9..40,300;9..40,400;9..40,500&family=Geist+Mono:wght@300;400&display=swap" rel="stylesheet">
<style>
*{margin:0;padding:0;box-sizing:border-box}
:root{--d:'Instrument Serif',Georgia,serif;--b:'DM Sans',sans-serif;--m:'Geist Mono',monospace}
body{background:#040608;color:#d0dae4;min-height:100vh;overflow-x:hidden}
@keyframes fadeUp{from{opacity:0;transform:translateY(16px)}to{opacity:1;transform:translateY(0)}}
@keyframes fadeIn{from{opacity:0}to{opacity:1}}
@keyframes revealLine{from{width:0}to{width:100%%}}
@keyframes drift{0%%{transform:translate(0,0)}100%%{transform:translate(var(--dx),var(--dy))}}
@keyframes pulse{0%%,100%%{transform:scale(1);opacity:.15}50%%{transform:scale(1.8);opacity:0}}
.wrap{max-width:540px;margin:0 auto;padding:56px 24px 48px;position:relative;z-index:1}
.particles{position:fixed;inset:0;pointer-events:none;z-index:0}
.p{position:absolute;background:#c8dff5;border-radius:50%%}
.aurora{position:fixed;pointer-events:none;z-index:0}
.a1{top:-20%%;left:-15%%;width:70%%;height:60%%;background:radial-gradient(ellipse at 30%% 40%%,rgba(40,70,120,.12) 0%%,transparent 65%%)}
.a2{bottom:-15%%;right:-10%%;width:50%%;height:45%%;background:radial-gradient(ellipse at 70%% 70%%,rgba(90,60,130,.05) 0%%,transparent 55%%)}
.glass{background:rgba(140,180,220,.03);backdrop-filter:blur(24px) saturate(1.4);-webkit-backdrop-filter:blur(24px) saturate(1.4);border-radius:20px;border:1px solid rgba(180,210,240,.06);position:relative;overflow:hidden;transition:all .4s cubic-bezier(.16,1,.3,1)}
.glass:hover{background:rgba(180,210,240,.055);border-color:rgba(180,210,240,.12)}
.glass::before{content:'';position:absolute;top:0;left:10%%;right:10%%;height:1px;background:linear-gradient(90deg,transparent,rgba(180,215,250,.08),transparent);transition:all .4s}
.glass:hover::before{background:linear-gradient(90deg,transparent,rgba(180,215,250,.18),transparent)}
.mark{filter:drop-shadow(0 0 12px rgba(140,200,255,.15))}
.greeting{font-family:var(--d);font-size:42px;font-weight:400;color:#e2ecf5;letter-spacing:-1px;line-height:1;animation:fadeUp .9s cubic-bezier(.16,1,.3,1) both}
.date{font-family:var(--m);font-size:10.5px;letter-spacing:2.5px;color:rgba(140,195,235,.18);text-transform:uppercase;margin-top:14px;animation:fadeIn .6s ease .5s both}
.metrics{display:grid;grid-template-columns:1fr 1fr 1fr;gap:10px;margin-bottom:48px}
.metric{padding:18px 20px;animation:fadeUp .6s cubic-bezier(.16,1,.3,1) both}
.metric-label{font-family:var(--m);font-size:9px;letter-spacing:2px;color:rgba(140,195,235,.2);text-transform:uppercase;margin-bottom:8px}
.metric-val{font-family:var(--d);font-size:32px;font-weight:400;color:#e2ecf5;line-height:1}
.divider{height:1px;margin-bottom:36px;overflow:hidden;animation:fadeIn .3s ease both}
.divider-inner{height:1px;background:linear-gradient(90deg,transparent,rgba(140,195,235,.1),transparent);animation:revealLine .8s ease-out forwards}
.section-label{font-family:var(--m);font-size:9px;letter-spacing:3px;color:rgba(140,195,235,.12);text-transform:uppercase;margin-bottom:16px;animation:fadeIn .5s ease both}
.tl-card{padding:8px 0;margin-bottom:44px}
.tl-row{display:grid;grid-template-columns:48px 18px 1fr;padding:14px 20px;border-bottom:1px solid rgba(140,195,235,.04);animation:fadeUp .45s cubic-bezier(.16,1,.3,1) both}
.tl-row:last-child{border-bottom:none}
.tl-time{font-family:var(--m);font-size:11px;color:rgba(140,195,235,.2);padding-top:1px}
.tl-dot-wrap{display:flex;flex-direction:column;align-items:center;padding-top:5px}
.tl-dot{width:8px;height:8px;border-radius:50%%;transition:box-shadow .3s}
.tl-row:hover .tl-dot{box-shadow:0 0 14px currentColor !important}
.tl-line{width:1px;flex:1;margin-top:4px;min-height:16px}
.tl-body{padding-left:10px}
.tl-title{font-family:var(--b);font-size:13.5px;font-weight:500;color:#d5e0ea}
.tl-sub{font-family:var(--b);font-size:12px;color:rgba(150,170,190,.4);margin-top:3px;line-height:1.45}
.proj-row{display:flex;justify-content:space-between;align-items:center;padding:16px 20px;cursor:pointer;animation:fadeUp .5s cubic-bezier(.16,1,.3,1) both;margin-bottom:10px}
.proj-left{display:flex;align-items:center;gap:12px}
.proj-icon{width:34px;height:34px;border-radius:10px;border:1px solid;display:flex;align-items:center;justify-content:center;font-family:var(--d);font-size:16px}
.proj-name{font-family:var(--b);font-size:14px;font-weight:500;color:#d5e0ea}
.proj-meta{display:flex;gap:8px;align-items:center;margin-top:3px}
.proj-branch{font-family:var(--m);font-size:10px;padding:2px 8px;border-radius:4px}
.proj-detail{font-family:var(--b);font-size:11px;color:rgba(150,170,190,.35)}
.proj-dur{font-family:var(--m);font-size:11px;color:rgba(140,195,235,.18)}
.callout{padding-left:22px;border-left:2px solid rgba(184,157,232,.12);margin-bottom:48px;animation:fadeUp .7s cubic-bezier(.16,1,.3,1) both}
.callout-text{font-family:var(--d);font-size:18px;font-weight:400;font-style:italic;color:rgba(184,157,232,.4);line-height:1.6}
.actions{display:flex;gap:10px;flex-wrap:wrap;animation:fadeUp .5s ease .3s both}
.btn{display:inline-flex;align-items:center;gap:8px;border-radius:14px;padding:12px 24px;font-family:var(--b);font-size:13px;font-weight:500;cursor:pointer;transition:all .25s;border:1px solid;backdrop-filter:blur(8px);text-decoration:none}
.btn:active{transform:scale(.96)}
.btn-primary{background:rgba(100,170,230,.1);color:#8cc5ef;border-color:rgba(100,170,230,.12)}
.btn-primary:hover{background:rgba(110,185,240,.18);border-color:rgba(110,185,240,.25)}
.btn-ghost{background:rgba(255,255,255,.02);color:rgba(150,170,190,.45);border-color:rgba(255,255,255,.04)}
.btn-ghost:hover{background:rgba(255,255,255,.05);border-color:rgba(255,255,255,.1)}
.version{text-align:center;margin-top:64px;font-family:var(--m);font-size:9px;letter-spacing:5px;color:rgba(140,195,235,.05);text-transform:uppercase}
</style>
</head><body>
<div class="particles">%s</div>
<div class="aurora a1"></div><div class="aurora a2"></div>
<div class="wrap">
<div style="margin-bottom:56px;animation:fadeUp .9s cubic-bezier(.16,1,.3,1) both">
<svg class="mark" width="36" height="36" viewBox="-85 -85 170 170" style="margin-bottom:20px"><defs><linearGradient id="ig" x1="0" y1="-1" x2="1" y2="1"><stop offset="0%%" stop-color="#b8ddf7"/><stop offset="50%%" stop-color="#5a9dd5"/><stop offset="100%%" stop-color="#2d6ba8"/></linearGradient></defs><polygon fill="url(#ig)" points="0,-28 24,-14 24,14 0,28 -24,14 -24,-14"/><polygon fill="#1e4f80" points="0,-10 8.7,-5 8.7,5 0,10 -8.7,5 -8.7,-5" opacity=".8"/>%s</svg>
<div class="greeting">good morning</div>
<div class="date">%s — last seen %s</div>
</div>
<div class="metrics">%s</div>
<div class="divider" style="animation-delay:.8s"><div class="divider-inner"></div></div>
<div class="section-label" style="animation-delay:.9s">yesterday</div>
<div class="glass tl-card">%s</div>
<div class="section-label" style="animation-delay:1.3s">projects</div>
%s
<div class="callout" style="animation-delay:1.6s"><div class="callout-text">%d context switch%s — estimated ramp-up overhead</div></div>
<div class="actions" style="animation-delay:1.7s">
<a class="btn btn-primary" onclick="alert('Run: thaw')">&#9670; restore workspace</a>
<a class="btn btn-ghost" onclick="alert('Run: thaw recap --voice')">&#9671; voice recap</a>
</div>
<div class="version">thaw v3.0.1</div>
</div>
</body></html>`,
		r.Date,
		buildParticlesHTML(40),
		buildCrystalArmsHTML(),
		r.Date,
		r.LastSeen.Format("3:04 PM"),
		buildMetricsHTML(len(r.Projects), len(r.Transitions), r.TotalTime),
		timelineRows.String(),
		projectRows.String(),
		switches, plural(switches))
}

func buildParticlesHTML(n int) string {
	var b strings.Builder
	for i := 0; i < n; i++ {
		x := float64(i*37%100) + float64(i%7)*3
		y := float64(i*53%100) + float64(i%5)*4
		s := 0.8 + float64(i%5)*0.4
		o := 0.015 + float64(i%8)*0.005
		dx := float64(i%11)*3 - 15
		dy := float64(i%7)*2 - 7
		dur := 20 + float64(i%13)*3
		b.WriteString(fmt.Sprintf(
			`<div class="p" style="left:%.0f%%;top:%.0f%%;width:%.1fpx;height:%.1fpx;opacity:%.3f;--dx:%.0fpx;--dy:%.0fpx;animation:drift %.0fs ease-in-out %.0fs infinite alternate"></div>`,
			x, y, s, s, o, dx, dy, dur, float64(i%8)*2))
	}
	return b.String()
}

func buildCrystalArmsHTML() string {
	var b strings.Builder
	for i, r := range []int{0, 60, 120, 180, 240, 300} {
		fill := "#6bb3e0"
		h := 32
		y := -58
		if i == 2 {
			fill = "rgba(140,200,245,.25)"
			h = 20
			y = -46
		}
		b.WriteString(fmt.Sprintf(
			`<g transform="rotate(%d)"><rect fill="%s" x="-2.5" y="%d" width="5" height="%d" rx="1.5"/></g>`,
			r, fill, y, h))
	}
	return b.String()
}

func buildMetricsHTML(projects, sessions int, total time.Duration) string {
	metrics := []struct{ label, value string }{
		{"projects", fmt.Sprintf("%d", projects)},
		{"sessions", fmt.Sprintf("%d", sessions)},
		{"deep work", formatDuration(total)},
	}
	var b strings.Builder
	for i, m := range metrics {
		b.WriteString(fmt.Sprintf(
			`<div class="glass metric" style="animation-delay:%.2fs"><div class="metric-label">%s</div><div class="metric-val">%s</div></div>`,
			0.3+float64(i)*0.08, m.label, m.value))
	}
	return b.String()
}

// --- helpers ---

func primaryProject(snap *models.Snapshot) string {
	for _, s := range snap.Sessions {
		if s.Git != nil && s.Git.RepoRoot != "" {
			return filepath.Base(s.Git.RepoRoot)
		}
	}
	for _, s := range snap.Sessions {
		if s.GroupName != "" {
			return s.GroupName
		}
	}
	if len(snap.Sessions) > 0 {
		return filepath.Base(snap.Sessions[0].CWD)
	}
	return "unknown"
}

func primaryBranch(snap *models.Snapshot) string {
	for _, s := range snap.Sessions {
		if s.Git != nil && s.Git.Branch != "" {
			return s.Git.Branch
		}
	}
	return ""
}

func wasSeenBefore(project string, transitions []Transition) bool {
	for _, t := range transitions {
		if t.Project == project {
			return true
		}
	}
	return false
}

func isSignificantChange(r diff.Result) bool {
	return len(r.Added) > 0 || len(r.Removed) > 0 || len(r.Changed) >= 2
}

func formatChangesSummary(r diff.Result) string {
	var parts []string
	if len(r.Added) > 0 {
		parts = append(parts, fmt.Sprintf("+%d session(s)", len(r.Added)))
	}
	if len(r.Removed) > 0 {
		parts = append(parts, fmt.Sprintf("-%d session(s)", len(r.Removed)))
	}
	if len(r.Changed) > 0 {
		parts = append(parts, fmt.Sprintf("~%d changed", len(r.Changed)))
	}
	return strings.Join(parts, ", ")
}

func actionIcon(action string) string {
	switch action {
	case "started":
		return "▶"
	case "switched":
		return "↻"
	case "returned":
		return "↩"
	case "updated":
		return "△"
	case "finished":
		return "■"
	default:
		return "·"
	}
}

func actionColor(action string) string {
	switch action {
	case "started":
		return "#3fb950"
	case "switched":
		return "#58a6ff"
	case "returned":
		return "#d2a8ff"
	case "updated":
		return "#8b949e"
	default:
		return "#484f58"
	}
}

func sortedProjects(m map[string]*ProjectBlock) []*ProjectBlock {
	var list []*ProjectBlock
	for _, pb := range m {
		list = append(list, pb)
	}
	sort.Slice(list, func(i, j int) bool {
		return list[i].TotalTime > list[j].TotalTime
	})
	return list
}

func formatDuration(d time.Duration) string {
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	if h > 0 {
		return fmt.Sprintf("%dh %dm", h, m)
	}
	if m > 0 {
		return fmt.Sprintf("%dm", m)
	}
	return "<1m"
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

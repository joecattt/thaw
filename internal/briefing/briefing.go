package briefing

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/pkg/models"
)

// ProjectData holds rendered project info for the briefing.
type ProjectData struct {
	Name           string
	Branch         string
	Dirty          bool
	TimeSpent      string
	LastActive     string
	LastActiveAgo  string
	FilesChanged   int
	TestStatus     string // "47/47 passing" or "1 test failing"
	Status         string // RUNNING | PAUSED | BLOCKED
	Priority       string // high | low
	PriorityLabel  string // Blocked | Shipped | Active
	Description    string // prose "where you left off"
	Processes      []ProcessInfo
	ResumeCommands []string
	AccentClass    string // "up" | "dn" | "blocked"
}

// ProcessInfo represents a running/stopped process.
type ProcessInfo struct {
	Name    string
	Running bool
}

// BriefingData holds all data for the briefing template.
type BriefingData struct {
	Version     string
	Date        string
	DeepWork    string
	Sessions    int
	TestSummary string
	DepStatus   string
	DepDetail   string
	Projects    []ProjectData
	TimeBars    []TimeBar
}

// TimeBar is a proportional bar segment.
type TimeBar struct {
	Name    string
	Flex    int
	Color   string
	Time    string
}

// Generate creates a briefing HTML file from snapshot data and config.
func Generate(snap *models.Snapshot, cfg config.Config) (string, error) {
	if snap == nil || len(snap.Sessions) == 0 {
		return "", fmt.Errorf("no snapshot data")
	}

	data := buildBriefingData(snap, cfg)
	htmlContent := renderHTML(data, cfg.Briefing)

	// Generate Cortana voice audio if configured
	voiceB64 := generateVoiceAudio(data, cfg)
	if voiceB64 != "" {
		htmlContent = strings.Replace(htmlContent,
			`<script id="cortana-audio" type="text/plain"></script>`,
			`<script id="cortana-audio" type="text/plain">`+voiceB64+`</script>`,
			1)
	}

	tmpDir := os.TempDir()
	path := filepath.Join(tmpDir, "thaw-briefing.html")
	if err := os.WriteFile(path, []byte(htmlContent), 0644); err != nil {
		return "", err
	}
	return path, nil
}

// Open opens the briefing in the default browser.
func Open(path string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", path)
	case "linux":
		cmd = exec.Command("xdg-open", path)
	default:
		return fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
	return cmd.Start()
}

func buildBriefingData(snap *models.Snapshot, cfg config.Config) BriefingData {
	// Group sessions by project directory
	type projGroup struct {
		dir      string
		sessions []models.Session
	}
	groups := make(map[string]*projGroup)
	var order []string

	for _, s := range snap.Sessions {
		dir := s.CWD
		if _, ok := groups[dir]; !ok {
			groups[dir] = &projGroup{dir: dir}
			order = append(order, dir)
		}
		groups[dir].sessions = append(groups[dir].sessions, s)
	}

	var projects []ProjectData
	var bars []TimeBar
	totalMinutes := 0
	colors := []string{"var(--emerald)", "var(--amber)", "#8b5cf6", "#ec4899", "#06b6d4"}

	for i, dir := range order {
		g := groups[dir]
		name := filepath.Base(dir)
		if name == "." || name == "/" {
			name = dir
		}

		p := ProjectData{
			Name: name,
		}

		// Git info from first session with git data
		for _, s := range g.sessions {
			if s.Git != nil {
				p.Branch = s.Git.Branch
				p.Dirty = s.Git.Dirty
				break
			}
		}

		// Estimate time (sessions * 15 min)
		mins := len(g.sessions) * 15
		totalMinutes += mins
		if mins >= 60 {
			p.TimeSpent = fmt.Sprintf("%dh %dm", mins/60, mins%60)
		} else {
			p.TimeSpent = fmt.Sprintf("%dm", mins)
		}
		p.LastActive = snap.CreatedAt.Format("3:04 PM")
		p.LastActiveAgo = formatAgo(snap.CreatedAt)

		// Count files (sessions as proxy)
		p.FilesChanged = len(g.sessions)

		// Determine status from session data
		hasRunning := false
		hasFailing := false
		for _, s := range g.sessions {
			if s.Status == "active" || s.Command != "" {
				hasRunning = true
			}
			if strings.Contains(strings.ToLower(s.Intent), "fail") || strings.Contains(strings.ToLower(s.Intent), "bug") {
				hasFailing = true
			}
		}

		if hasFailing {
			p.Status = "BLOCKED"
			p.Priority = "high"
			p.PriorityLabel = "Blocked"
			p.AccentClass = "blocked"
		} else if hasRunning {
			p.Status = "RUNNING"
			p.Priority = "low"
			p.PriorityLabel = "Active"
			p.AccentClass = "up"
		} else {
			p.Status = "PAUSED"
			p.Priority = "low"
			p.PriorityLabel = "Paused"
			p.AccentClass = "dn"
		}

		// Build description from session data
		var descParts []string
		for _, s := range g.sessions {
			if s.Intent != "" {
				descParts = append(descParts, s.Intent)
			}
		}
		if len(descParts) > 0 {
			p.Description = strings.Join(descParts, ". ") + "."
		} else {
			p.Description = fmt.Sprintf("Working in %s across %d sessions.", name, len(g.sessions))
		}

		// Resume commands from last session history
		for _, s := range g.sessions {
			if len(s.History) > 0 {
				last := s.History[len(s.History)-1]
				if !strings.HasPrefix(last, "cd ") && !strings.HasPrefix(last, "ls") {
					p.ResumeCommands = append(p.ResumeCommands, last)
				}
			}
		}
		if len(p.ResumeCommands) > 3 {
			p.ResumeCommands = p.ResumeCommands[:3]
		}

		// Processes
		for _, s := range g.sessions {
			if s.Command != "" && s.Status == "active" {
				p.Processes = append(p.Processes, ProcessInfo{Name: s.Command, Running: true})
			}
		}

		projects = append(projects, p)
		color := colors[i%len(colors)]
		bars = append(bars, TimeBar{Name: name, Flex: mins, Color: color, Time: p.TimeSpent})
	}

	// Sort: blocked first
	if cfg.Briefing.PriorityOrder == "blocked" {
		sorted := make([]ProjectData, 0, len(projects))
		for _, p := range projects {
			if p.Priority == "high" {
				sorted = append(sorted, p)
			}
		}
		for _, p := range projects {
			if p.Priority != "high" {
				sorted = append(sorted, p)
			}
		}
		projects = sorted
	}

	deepWork := fmt.Sprintf("%d:%02d", totalMinutes/60, totalMinutes%60)

	return BriefingData{
		Version:     "v1.0.0",
		Date:        snap.CreatedAt.Format("Jan 2, 2006"),
		DeepWork:    deepWork,
		Sessions:    len(snap.Sessions),
		TestSummary: fmt.Sprintf("%d sessions", len(snap.Sessions)),
		DepStatus:   "OK",
		Projects:    projects,
		TimeBars:    bars,
	}
}

func formatAgo(t time.Time) string {
	d := time.Since(t)
	if d < time.Hour {
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh ago", int(d.Hours()))
}

func renderHTML(data BriefingData, bcfg config.BriefingConfig) string {
	var b strings.Builder

	// Build project blocks
	var projectHTML strings.Builder
	for i, p := range data.Projects {
		pid := fmt.Sprintf("p%d", i+1)
		branchTag := ""
		if p.Branch != "" {
			dirty := ""
			if p.Dirty {
				dirty = " *"
			}
			cls := "tag-d"
			if p.AccentClass == "blocked" {
				cls = "tag-r"
			} else if p.AccentClass == "up" {
				cls = "tag-g"
			} else {
				cls = "tag-a"
			}
			branchTag = fmt.Sprintf(`<span class="tag %s">%s%s</span>`, cls, html.EscapeString(p.Branch), dirty)
		}

		priorityHTML := ""
		if p.PriorityLabel != "" {
			pcls := "low"
			if p.Priority == "high" {
				pcls = "high"
			}
			priorityHTML = fmt.Sprintf(`<span class="priority %s"><span class="priority-dot"></span>%s</span>`, pcls, p.PriorityLabel)
		}

		procsHTML := ""
		if bcfg.ShowProcesses && len(p.Processes) > 0 {
			procsHTML = `<div class="proc-row">`
			for _, pr := range p.Processes {
				cls := "off"
				if pr.Running {
					cls = "on"
				}
				procsHTML += fmt.Sprintf(`<div class="proc"><span class="proc-dot %s"></span><span class="proc-name %s">%s</span></div>`, cls, cls, html.EscapeString(pr.Name))
			}
			procsHTML += `</div>`
		}

		cmdsHTML := ""
		if bcfg.ShowResumeCommands && len(p.ResumeCommands) > 0 {
			cmdsHTML = `<div class="resume-lbl">To resume</div>`
			for _, c := range p.ResumeCommands {
				cmdsHTML += fmt.Sprintf(`<div class="cmd"><span class="d">$</span><span class="c">%s</span></div>`, html.EscapeString(c))
			}
		}

		indent := ""
		if i > 0 {
			indent = ` style="margin-left:16px"`
		}

		projectHTML.WriteString(fmt.Sprintf(`
        <div class="proj %s" id="%s"%s>
          <div class="proj-body">
            <div class="proj-top">
              <div>
                <div style="display:flex;align-items:center;gap:12px">
                  <div class="proj-name">%s</div>
                  %s
                </div>
                <div class="proj-last">Last active %s — %s</div>
              </div>
              <div class="proj-time">%s</div>
            </div>
            <div class="proj-meta">
              %s
              <span class="tag tag-d">%d sessions</span>
              <span class="tag tag-d">%s</span>
            </div>
            <div class="lo-lbl">Where you left off</div>
            <div class="lo-txt">%s</div>
            %s
            %s
          </div>
        </div>`,
			p.AccentClass, pid, indent,
			html.EscapeString(p.Name), priorityHTML,
			p.LastActive, p.LastActiveAgo,
			p.TimeSpent,
			branchTag, p.FilesChanged, p.Status,
			p.Description,
			procsHTML, cmdsHTML))
	}

	// Time bars
	var barsHTML strings.Builder
	for _, tb := range data.TimeBars {
		barsHTML.WriteString(fmt.Sprintf(`<div style="flex:%d;background:%s;border-radius:5px"></div>`, tb.Flex, tb.Color))
	}
	var legendHTML strings.Builder
	for _, tb := range data.TimeBars {
		legendHTML.WriteString(fmt.Sprintf(`<div class="legend-item"><div class="legend-dot" style="background:%s"></div>%s<span class="legend-dim">%s</span></div>`, tb.Color, html.EscapeString(tb.Name), tb.Time))
	}

	// Assemble — note: this is a simplified version of the frost briefing
	// The full frost briefing with Three.js/GSAP should be loaded as a template file
	// This generates a clean, functional briefing that works without external dependencies
	b.WriteString(fmt.Sprintf(`<!DOCTYPE html><html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1"><title>thaw briefing</title>
<style>
@import url('https://fonts.googleapis.com/css2?family=Anybody:wdth,wght@75..150,400;75..150,700;75..150,900&family=Fira+Code:wght@400;500;600&family=DM+Sans:opsz,wght@9..40,400;9..40,500;9..40,600;9..40,700&display=swap');
:root{--base:#0a2435;--ice-white:#fff;--ice-pale:#f0f8fc;--ice-mid:#8ab8ce;--ice-deep:#3a7a95;--ice-glow:rgba(180,230,255,0.18);--emerald:#34d399;--amber:#f59e0b;--red-alert:#f87171;--display:'Anybody',sans-serif;--mono:'Fira Code',monospace;--prose:'DM Sans',sans-serif}
*{margin:0;padding:0;box-sizing:border-box}body{background:var(--base);color:var(--ice-pale);min-height:100vh;display:flex;align-items:center;justify-content:center;padding:40px 20px}
.terminal{width:min(900px,94vw);font-family:var(--prose)}
.frame{background:rgba(18,60,85,0.15);border:1px solid rgba(160,220,245,0.04);border-radius:8px;padding:clamp(20px,3vh,36px) clamp(24px,3vw,40px);box-shadow:0 25px 60px rgba(0,0,0,0.3)}
.term-header{display:flex;justify-content:space-between;align-items:center;padding-bottom:14px;margin-bottom:18px;border-bottom:1px solid rgba(140,200,230,0.04)}
.term-title{font-family:var(--display);font-weight:900;font-stretch:125%%;font-size:18px;letter-spacing:4px;text-transform:uppercase;color:var(--ice-white)}
.term-date{font-family:var(--prose);font-size:12px;color:var(--ice-mid)}
.stats{display:flex;gap:0;margin-bottom:18px}.stat{flex:1;padding:14px 16px}
.stat-lbl{font-family:var(--prose);font-size:11px;font-weight:700;letter-spacing:1.5px;text-transform:uppercase;color:var(--ice-pale);opacity:0.7;margin-bottom:5px}
.stat-val{font-family:var(--display);font-weight:900;font-stretch:120%%;color:var(--ice-white);line-height:1}
.stat:nth-child(1) .stat-val{font-size:36px}.stat:nth-child(2) .stat-val{font-size:28px}
.stat:nth-child(3) .stat-val{font-size:24px;color:var(--emerald)}.stat:nth-child(4) .stat-val{font-size:20px;color:var(--amber)}
.time-row{display:flex;gap:2px;height:5px;border-radius:5px;overflow:hidden;margin-bottom:8px;background:rgba(140,200,230,0.03)}
.time-legend{display:flex;gap:20px;margin-bottom:16px}
.legend-item{display:flex;align-items:center;gap:6px;font-family:var(--prose);font-size:12px;font-weight:500;color:var(--ice-pale)}
.legend-dot{width:7px;height:7px;border-radius:2px}.legend-dim{color:var(--ice-mid);margin-left:3px}
.fracture{height:1px;background:rgba(160,220,245,0.06);margin:14px 0}
.proj{margin-bottom:6px;border-radius:4px;animation:fadeIn 0.6s ease both}
.proj:nth-child(2){animation-delay:0.15s}
@keyframes fadeIn{from{opacity:0;transform:translateY(10px)}to{opacity:1;transform:none}}
.proj-body{padding:clamp(14px,2vh,22px) clamp(16px,2vw,26px);border-radius:4px;border-left:7px solid transparent;transition:background 0.3s}
.proj-body:hover{background:rgba(140,200,230,0.03)}
.proj.up .proj-body{border-left-color:var(--emerald);background:rgba(52,211,153,0.012)}
.proj.blocked .proj-body{border-left-color:var(--red-alert);background:rgba(248,113,113,0.008)}
.proj.dn .proj-body{border-left-color:var(--amber);opacity:0.75}
.proj-top{display:flex;justify-content:space-between;align-items:flex-start;margin-bottom:8px}
.proj-name{font-family:var(--display);font-weight:900;font-stretch:115%%;font-size:20px;color:var(--ice-white)}
.proj-time{font-family:var(--display);font-weight:700;font-size:20px;color:var(--ice-mid)}
.proj-last{font-family:var(--prose);font-size:11px;color:var(--ice-pale);opacity:0.55;margin-top:3px}
.proj-meta{display:flex;gap:8px;align-items:center;margin-bottom:12px;flex-wrap:wrap}
.tag{font-family:var(--mono);font-size:10px;font-weight:500;padding:5px 14px;border-radius:3px}
.tag-g{color:var(--emerald);border:1px solid rgba(52,211,153,0.15);background:rgba(52,211,153,0.05)}
.tag-a{color:var(--amber);border:1px solid rgba(245,158,11,0.12);background:rgba(245,158,11,0.04)}
.tag-r{color:var(--red-alert);border:1px solid rgba(248,113,113,0.12);background:rgba(248,113,113,0.04)}
.tag-d{color:var(--ice-mid);border:1px solid rgba(140,185,205,0.1)}
.priority{font-family:var(--prose);font-size:10px;font-weight:700;letter-spacing:1px;text-transform:uppercase;padding:3px 10px;border-radius:3px;display:inline-flex;align-items:center;gap:5px}
.priority.high{color:var(--red-alert);background:rgba(248,113,113,0.06);border:1px solid rgba(248,113,113,0.12)}
.priority.low{color:var(--emerald);background:rgba(52,211,153,0.04);border:1px solid rgba(52,211,153,0.1)}
.priority-dot{width:5px;height:5px;border-radius:50%%}
.priority.high .priority-dot{background:var(--red-alert)}.priority.low .priority-dot{background:var(--emerald)}
.lo-lbl{font-family:var(--prose);font-size:11px;font-weight:700;letter-spacing:2px;text-transform:uppercase;color:var(--ice-mid);margin-bottom:10px}
.lo-txt{font-family:var(--prose);font-size:14px;line-height:1.75;color:var(--ice-pale)}
.lo-txt strong{color:var(--ice-white);font-weight:700}
.proc-row{display:flex;gap:16px;margin-top:10px;flex-wrap:wrap}
.proc{display:flex;align-items:center;gap:6px;font-family:var(--mono);font-size:11px}
.proc-dot{width:6px;height:6px;border-radius:50%%}
.proc-dot.on{background:var(--emerald);box-shadow:0 0 6px rgba(52,211,153,0.35)}.proc-dot.off{background:var(--ice-deep);opacity:0.4}
.proc-name.on{color:var(--ice-pale)}.proc-name.off{color:var(--ice-deep);opacity:0.5}
.resume-lbl{font-family:var(--prose);font-size:10px;font-weight:700;letter-spacing:1.5px;text-transform:uppercase;color:var(--ice-mid);margin-top:14px;margin-bottom:6px}
.cmd{font-family:var(--mono);font-size:11px;padding:4px 10px;display:flex;gap:6px;background:rgba(0,15,25,0.18);border-radius:3px;margin-bottom:2px}
.cmd .d{color:rgba(52,211,153,0.4)}.cmd .c{color:var(--ice-pale);opacity:0.65}
.actions{display:flex;gap:12px;margin-top:20px}
.act{font-family:var(--display);font-weight:700;font-stretch:110%%;font-size:11px;letter-spacing:2px;text-transform:uppercase;
  padding:14px 28px;background:transparent;color:var(--ice-mid);border:none;cursor:pointer;transition:all 0.3s;border-radius:4px}
.act:hover{background:rgba(140,200,230,0.05);color:var(--ice-white)}
.act.primary{color:var(--emerald)}.act.primary:hover{background:rgba(52,211,153,0.04)}
.footer{margin-top:24px;font-family:var(--prose);font-size:10px;color:var(--ice-deep);letter-spacing:2px;text-transform:uppercase;text-align:center}
</style></head><body>
<div class="terminal"><div class="frame">
<div class="term-header">
  <div class="term-title">Thaw %s</div>
  <div class="term-date">%s — %s deep work across %d sessions</div>
</div>
<div class="stats">
  <div class="stat"><div class="stat-lbl">Deep work</div><div class="stat-val">%s</div></div>
  <div class="stat"><div class="stat-lbl">Sessions</div><div class="stat-val">%d</div></div>
  <div class="stat"><div class="stat-lbl">Projects</div><div class="stat-val">%d</div></div>
</div>
<div class="time-row">%s</div>
<div class="time-legend">%s</div>
<div class="fracture"></div>
%s
<div class="fracture"></div>
<div class="actions">
  <button class="act primary" onclick="alert('thaw → restoring...')">← Restore</button>
  <button class="act" onclick="document.body.style.opacity=0;setTimeout(()=>window.close(),500)">Dismiss</button>
</div>
<div class="footer">thaw %s — generated %s</div>
</div></div>
<script id="cortana-audio" type="text/plain"></script>
</body></html>`,
		data.Version, data.Date, data.DeepWork, data.Sessions,
		data.DeepWork, data.Sessions, len(data.Projects),
		barsHTML.String(), legendHTML.String(),
		projectHTML.String(),
		data.Version, time.Now().Format("3:04 PM")))

	return b.String()
}

// generateVoiceAudio creates a voice recap using Cortana TTS and returns base64.
func generateVoiceAudio(data BriefingData, cfg config.Config) string {
	cortanaPath := cfg.Voice.CortanaPath
	if cortanaPath == "" {
		return ""
	}

	// Check if cortana reference WAV exists
	if _, err := os.Stat(cortanaPath); os.IsNotExist(err) {
		return ""
	}

	// Build recap text
	text := buildRecapText(data)
	if text == "" {
		return ""
	}

	// Generate audio via Coqui XTTS with voice cloning from cortana.wav
	tmpWav := filepath.Join(os.TempDir(), "thaw-voice.wav")
	script := fmt.Sprintf(`
import os, sys
os.environ["COQUI_TOS_AGREED"] = "1"
try:
    from TTS.api import TTS
    tts = TTS("tts_models/multilingual/multi-dataset/xtts_v1.1", progress_bar=False)
    tts.tts_to_file(
        text=%q,
        speaker_wav=%q,
        file_path=%q,
        language="en"
    )
except Exception as e:
    print(f"TTS error: {e}", file=sys.stderr)
    sys.exit(1)
`, text, cortanaPath, tmpWav)

	// Prefer the tts-env venv Python, fall back to system python3
	pythonBin := "python3"
	home, _ := os.UserHomeDir()
	venvPython := filepath.Join(home, "tts-env", "bin", "python3")
	if _, err := os.Stat(venvPython); err == nil {
		pythonBin = venvPython
	}

	fmt.Fprintf(os.Stderr, "thaw: generating Cortana voice audio (this may take a minute)...\n")
	cmd := exec.Command(pythonBin, "-c", script)
	cmd.Env = append(os.Environ(), "COQUI_TOS_AGREED=1")
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "thaw: voice generation failed: %v\n", err)
		if stderr.Len() > 0 {
			fmt.Fprintf(os.Stderr, "thaw: TTS stderr: %s\n", stderr.String())
		}
		return ""
	}

	// Read and base64-encode the audio
	audioData, err := os.ReadFile(tmpWav)
	if err != nil {
		return ""
	}
	defer os.Remove(tmpWav)

	return base64.StdEncoding.EncodeToString(audioData)
}

// buildRecapText generates a spoken recap from briefing data.
func buildRecapText(data BriefingData) string {
	var parts []string

	parts = append(parts, fmt.Sprintf("Good morning. Here's your thaw briefing for %s.", data.Date))
	parts = append(parts, fmt.Sprintf("You logged %s of deep work across %d sessions.", data.DeepWork, data.Sessions))

	for _, p := range data.Projects {
		status := ""
		if p.PriorityLabel != "" {
			status = ", status " + p.PriorityLabel
		}
		parts = append(parts, fmt.Sprintf("%s%s, %s.", p.Name, status, p.TimeSpent))
		if p.Description != "" {
			parts = append(parts, p.Description)
		}
	}

	return strings.Join(parts, " ")
}

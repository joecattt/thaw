package briefing

import (
	"bytes"
	_ "embed"
	"encoding/base64"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/pkg/models"
)

//go:embed frost_template.html
var frostTemplate string

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

		projectHTML.WriteString(fmt.Sprintf(`
        <div class="proj %s" id="%s">
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
			p.AccentClass, pid,
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

	// Inject data into frost template via marker replacement
	r := strings.NewReplacer(
		"<!-- THAW:VERSION -->", html.EscapeString(data.Version),
		"<!-- THAW:DATE -->", html.EscapeString(data.Date),
		"<!-- THAW:DEEPWORK -->", html.EscapeString(data.DeepWork),
		"<!-- THAW:SESSIONS -->", strconv.Itoa(data.Sessions),
		"<!-- THAW:TESTS -->", html.EscapeString(data.TestSummary),
		"<!-- THAW:DEPSTATUS -->", html.EscapeString(data.DepStatus),
		"<!-- THAW:DEPDETAIL -->", html.EscapeString(data.DepDetail),
		"<!-- THAW:TIMEBARS -->", barsHTML.String(),
		"<!-- THAW:TIMELEGEND -->", legendHTML.String(),
		"<!-- THAW:PROJECTS -->", projectHTML.String(),
	)

	return r.Replace(frostTemplate)
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

	// Generate audio via Coqui XTTS v2 with voice cloning from cortana.wav
	tmpWav := filepath.Join(os.TempDir(), "thaw-voice.wav")
	script := fmt.Sprintf(`
import os, sys
os.environ["COQUI_TOS_AGREED"] = "1"
try:
    from TTS.api import TTS
    tts = TTS("tts_models/multilingual/multi-dataset/xtts_v2", gpu=False)
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

	// Prefer the tts-env2 venv Python, fall back to system python3
	pythonBin := "python3"
	home, _ := os.UserHomeDir()
	venvPython := filepath.Join(home, "tts-env2", "bin", "python3")
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

// buildRecapText generates an opinionated spoken briefing — not a screen reader.
func buildRecapText(data BriefingData) string {
	var parts []string

	// Time-aware greeting
	hour := time.Now().Hour()
	greeting := "Good morning."
	if hour >= 12 && hour < 17 {
		greeting = "Good afternoon."
	} else if hour >= 17 {
		greeting = "Good evening."
	}

	// Headline framing based on intensity
	intensity := ""
	switch {
	case data.Sessions <= 2:
		intensity = fmt.Sprintf("Light day. %s of work across %d sessions.", data.DeepWork, data.Sessions)
	case data.Sessions >= 8:
		intensity = fmt.Sprintf("Heavy day. %s of deep work across %d sessions.", data.DeepWork, data.Sessions)
	default:
		intensity = fmt.Sprintf("%s of deep work across %d sessions.", data.DeepWork, data.Sessions)
	}
	parts = append(parts, greeting+" "+intensity)

	// Separate blocked, active, and clean projects
	var blocked, active, clean []ProjectData
	for _, p := range data.Projects {
		switch p.PriorityLabel {
		case "Blocked":
			blocked = append(blocked, p)
		default:
			// Has running processes or recent activity = active
			hasRunning := false
			for _, proc := range p.Processes {
				if proc.Running {
					hasRunning = true
					break
				}
			}
			if hasRunning || p.Description != "" || p.FilesChanged > 0 {
				active = append(active, p)
			} else {
				clean = append(clean, p)
			}
		}
	}

	// Blocked projects lead — they need attention
	if len(blocked) > 0 {
		if len(blocked) == 1 {
			parts = append(parts, "One thing needs attention.")
		} else {
			parts = append(parts, fmt.Sprintf("%d things need attention.", len(blocked)))
		}
		for _, p := range blocked {
			parts = append(parts, fmt.Sprintf("%s is blocked.", p.Name))
			if p.Description != "" {
				parts = append(parts, p.Description)
			}
			if len(p.ResumeCommands) > 0 {
				parts = append(parts, fmt.Sprintf("Pick up with: %s.", p.ResumeCommands[0]))
			}
		}
	}

	// Active projects get context
	for _, p := range active {
		line := fmt.Sprintf("%s, %s.", p.Name, p.TimeSpent)
		if p.Description != "" {
			line += " " + p.Description
		}
		// Mention running processes naturally
		var running []string
		for _, proc := range p.Processes {
			if proc.Running {
				running = append(running, proc.Name)
			}
		}
		if len(running) > 0 {
			line += " " + strings.Join(running, " and ") + " still running."
		}
		parts = append(parts, line)
	}

	// Clean projects get compressed
	if len(clean) > 0 {
		var names []string
		for _, p := range clean {
			names = append(names, p.Name)
		}
		if len(names) == 1 {
			parts = append(parts, fmt.Sprintf("%s is clean. Nothing to do there.", names[0]))
		} else {
			parts = append(parts, fmt.Sprintf("%s are clean.", strings.Join(names, " and ")))
		}
	}

	// Closing directive — point at the blocker first, or first active project
	if len(blocked) > 0 {
		parts = append(parts, fmt.Sprintf("Start with %s.", blocked[0].Name))
	} else if len(active) > 0 {
		parts = append(parts, fmt.Sprintf("Start with %s.", active[0].Name))
	} else {
		parts = append(parts, "Everything is clean.")
	}

	return strings.Join(parts, " ")
}

package briefing

import (
	"bytes"
	"context"
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

	"github.com/joecattt/thaw/internal/buildinfo"
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
	Convos         []ClaudeConvo
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
	Name  string
	Flex  int
	Color string
	Time  string
}

// Generate creates a briefing HTML file from snapshot data and config.
func Generate(snap *models.Snapshot, cfg config.Config) (string, error) {
	if snap == nil || len(snap.Sessions) == 0 {
		return "", fmt.Errorf("no snapshot data")
	}

	data := buildBriefingData(snap, cfg)
	htmlContent := renderHTML(data, cfg.Briefing)

	// Voice: if the user wired a synthesizer (voice.synth_cmd), narrate the
	// briefing through it and embed the audio — the page's Voice button then
	// plays a real voice instead of the browser speech-synthesis fallback.
	if audio := synthNarration(data, cfg); audio != "" {
		htmlContent = strings.Replace(htmlContent,
			`<script id="cortana-audio" type="text/plain"></script>`,
			`<script id="cortana-audio" type="text/plain">`+audio+`</script>`, 1)
	}

	// The briefing contains conversation titles and resume commands — keep
	// it out of the world-readable shared temp dir.
	tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("thaw-%d", os.Getuid()))
	if err := os.MkdirAll(tmpDir, 0700); err != nil {
		return "", err
	}
	path := filepath.Join(tmpDir, "thaw-briefing.html")
	if err := os.WriteFile(path, []byte(htmlContent), 0600); err != nil {
		return "", err
	}
	return path, nil
}

// narrationText composes the short spoken script for the briefing —
// a few sentences, because local TTS latency scales with length.
func narrationText(data BriefingData) string {
	var b strings.Builder
	hour := time.Now().Hour()
	switch {
	case hour < 12:
		b.WriteString("Good morning. ")
	case hour < 17:
		b.WriteString("Good afternoon. ")
	default:
		b.WriteString("Good evening. ")
	}
	// Spoken numbers get rounded — "seventeen forty" and "$955.33" read
	// aloud sound like a machine reciting a ledger, and these are estimates.
	fmt.Fprintf(&b, "%s of deep work this week across %d sessions. ", spokenHours(data.DeepWork), data.Sessions)
	if d := spokenDollars(data.TestSummary); d != "" {
		fmt.Fprintf(&b, "Claude spend, about %s. ", d)
	}
	if len(data.Projects) > 0 {
		p := data.Projects[0]
		if p.Description != "" {
			fmt.Fprintf(&b, "Where you left off: %s.", strings.TrimRight(p.Description, "."))
		}
	}
	return b.String()
}

// spokenHours turns "17:40" into "about 18 hours" for speech.
func spokenHours(hhmm string) string {
	parts := strings.SplitN(hhmm, ":", 2)
	if len(parts) != 2 {
		return hhmm
	}
	h, err1 := strconv.Atoi(parts[0])
	m, err2 := strconv.Atoi(parts[1])
	if err1 != nil || err2 != nil {
		return hhmm
	}
	if m >= 30 {
		h++
	}
	switch h {
	case 0:
		return "under an hour"
	case 1:
		return "about an hour"
	}
	return fmt.Sprintf("about %d hours", h)
}

// spokenDollars turns "$955.33 est" into "955 dollars" for speech.
func spokenDollars(summary string) string {
	s := strings.TrimSuffix(strings.TrimSpace(summary), " est")
	s = strings.TrimPrefix(s, "$")
	if i := strings.IndexByte(s, '.'); i >= 0 {
		s = s[:i]
	}
	if _, err := strconv.Atoi(s); err != nil {
		return ""
	}
	return s + " dollars"
}

// synthNarration runs voice.synth_cmd with the narration on stdin and returns
// the audio as base64, or "" (with a stderr warning) on any failure — the
// briefing must never fail to generate because a voice model hiccuped.
func synthNarration(data BriefingData, cfg config.Config) string {
	cmdStr := strings.TrimSpace(cfg.Voice.SynthCmd)
	if cmdStr == "" {
		return ""
	}
	// Generous: a local model's cold path (load + render) can take minutes.
	// Generation runs in the background, so patience is cheaper than silence.
	ctx, cancel := context.WithTimeout(context.Background(), 16*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, "sh", "-c", cmdStr)
	cmd.Stdin = strings.NewReader(narrationText(data))
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: voice.synth_cmd failed (%v): %s\n", err, strings.TrimSpace(errb.String()))
		return ""
	}
	if out.Len() < 1000 { // no real audio is this small
		fmt.Fprintf(os.Stderr, "warning: voice.synth_cmd produced %d bytes — expected WAV/MP3 on stdout\n", out.Len())
		return ""
	}
	return base64.StdEncoding.EncodeToString(out.Bytes())
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

		// Claude Code conversations for this project (last 7 days) — real
		// titles, durations, and estimated cost from transcript usage.
		p.Convos = loadClaudeConvos(dir, claudeWindow, 6)

		// Git info from first session with git data
		for _, s := range g.sessions {
			if s.Git != nil {
				p.Branch = s.Git.Branch
				p.Dirty = s.Git.Dirty
				break
			}
		}

		// Time: measured Claude conversation wall-clock when transcripts
		// exist; otherwise fall back to the old sessions*15min estimate.
		mins := 0
		for _, c := range p.Convos {
			mins += int(c.Duration.Minutes())
		}
		if mins == 0 {
			mins = len(g.sessions) * 15
		}
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
		if len(p.Convos) > 0 {
			p.Description = p.Convos[0].Title
		} else if len(descParts) > 0 {
			p.Description = strings.Join(descParts, ". ") + "."
		} else {
			p.Description = fmt.Sprintf("Working in %s across %d sessions.", name, len(g.sessions))
		}

		// Resume commands: prefer resuming the latest Claude conversation;
		// fall back to last shell-history commands.
		if len(p.Convos) > 0 {
			p.ResumeCommands = []string{"claude --resume " + p.Convos[0].ID}
		} else {
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

	totalCost := 0.0
	totalConvos := 0
	for _, p := range projects {
		totalConvos += len(p.Convos)
		for _, c := range p.Convos {
			totalCost += c.CostUSD
		}
	}
	costSummary := "—"
	if totalConvos > 0 {
		costSummary = fmt.Sprintf("$%.2f est", totalCost)
	}

	return BriefingData{
		Version:     "v" + buildinfo.Version,
		Date:        snap.CreatedAt.Format("Jan 2, 2006"),
		DeepWork:    deepWork,
		Sessions:    len(snap.Sessions),
		TestSummary: costSummary,
		DepStatus:   fmt.Sprintf("%d", totalConvos),
		DepDetail:   "last 7 days",
		Projects:    projects,
		TimeBars:    bars,
	}
}

// claudeWindow bounds how far back the briefing looks for Claude
// conversations, and matches the "7d" labels in the template.
const claudeWindow = 7 * 24 * time.Hour

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
			var cls string
			switch p.AccentClass {
			case "blocked":
				cls = "tag-r"
			case "up":
				cls = "tag-g"
			default:
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

		convosHTML := ""
		if len(p.Convos) > 0 {
			var cb strings.Builder
			cb.WriteString(`<div class="resume-lbl">Conversations · last 7 days</div>`)
			for _, c := range p.Convos {
				fmt.Fprintf(&cb,
					`<div class="convo"><div class="convo-top"><span class="convo-title">%s</span><span class="convo-meta">%s · %s · %s</span></div><div class="cmd"><span class="d">$</span><span class="c">claude --resume %s</span></div></div>`,
					html.EscapeString(c.Title), c.Ago(), c.DurationStr(), c.CostStr(), html.EscapeString(c.ID))
			}
			convosHTML = cb.String()
		}

		cmdsHTML := ""
		if bcfg.ShowResumeCommands && len(p.ResumeCommands) > 0 && len(p.Convos) == 0 {
			cmdsHTML = `<div class="resume-lbl">To resume</div>`
			for _, c := range p.ResumeCommands {
				cmdsHTML += fmt.Sprintf(`<div class="cmd"><span class="d">$</span><span class="c">%s</span></div>`, html.EscapeString(c))
			}
		}

		fmt.Fprintf(&projectHTML, `
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
            %s
          </div>
        </div>`,
			p.AccentClass, pid,
			html.EscapeString(p.Name), priorityHTML,
			p.LastActive, p.LastActiveAgo,
			p.TimeSpent,
			branchTag, p.FilesChanged, p.Status,
			html.EscapeString(p.Description),
			procsHTML, convosHTML, cmdsHTML)
	}

	// Time bars
	var barsHTML strings.Builder
	for _, tb := range data.TimeBars {
		fmt.Fprintf(&barsHTML, `<div style="flex:%d;background:%s;border-radius:5px"></div>`, tb.Flex, tb.Color)
	}
	var legendHTML strings.Builder
	for _, tb := range data.TimeBars {
		fmt.Fprintf(&legendHTML, `<div class="legend-item"><div class="legend-dot" style="background:%s"></div>%s<span class="legend-dim">%s</span></div>`, tb.Color, html.EscapeString(tb.Name), tb.Time)
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

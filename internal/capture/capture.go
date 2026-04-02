package capture

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/joecattt/thaw/internal/browser"
	"github.com/joecattt/thaw/internal/clipboard"
	gitpkg "github.com/joecattt/thaw/internal/git"
	"github.com/joecattt/thaw/internal/group"
	"github.com/joecattt/thaw/internal/history"
	"github.com/joecattt/thaw/internal/intent"
	"github.com/joecattt/thaw/internal/integrations/direnv"
	"github.com/joecattt/thaw/internal/integrations/zoxide"
	"github.com/joecattt/thaw/internal/ordering"
	"github.com/joecattt/thaw/internal/output"
	"github.com/joecattt/thaw/internal/process"
	"github.com/joecattt/thaw/pkg/models"
)

type Engine struct {
	discovery    process.Discovery
	labeler      *Labeler
	baselineEnv  map[string]string
	historyN     int
	outputN      int
	captureEnv   bool
	captureGit   bool
	captureAI    bool
	intentCfg    intent.Config
	envBlocklist []string
	excludePaths []string
}

func New(disc process.Discovery, labels map[string]string) *Engine {
	return &Engine{
		discovery:   disc,
		labeler:     NewLabeler(labels),
		baselineEnv: captureBaselineEnv(),
		historyN:    20,
		outputN:     30,
		captureEnv:  true,
		captureGit:  true,
	}
}

func (e *Engine) SetHistoryLines(n int)           { e.historyN = n }
func (e *Engine) SetOutputLines(n int)            { e.outputN = n }
func (e *Engine) SetCaptureEnv(v bool)            { e.captureEnv = v }
func (e *Engine) SetCaptureGit(v bool)            { e.captureGit = v }
func (e *Engine) SetCaptureAI(v bool)             { e.captureAI = v }
func (e *Engine) SetIntentConfig(c intent.Config) { e.intentCfg = c }
func (e *Engine) SetEnvBlocklist(bl []string)      { e.envBlocklist = bl }
func (e *Engine) SetExcludePaths(paths []string)   { e.excludePaths = paths }

func (e *Engine) isExcludedPath(cwd string) bool {
	for _, p := range e.excludePaths {
		if strings.HasPrefix(cwd, p) {
			return true
		}
	}
	return false
}

const subsystemTimeout = 2 * time.Second

func (e *Engine) Capture(source string) (*models.Snapshot, error) {
	shells, err := e.discovery.ListShells()
	if err != nil {
		return nil, err
	}

	now := time.Now()
	tmuxOutput := output.CaptureAllPanes(e.outputN)
	activeTTY := output.ActivePaneTTY()

	sessions := make([]models.Session, len(shells))
	var wg sync.WaitGroup

	for i, shell := range shells {
		wg.Add(1)
		go func(idx int, sh process.ShellInfo) {
			defer wg.Done()
			defer func() { recover() }()
			sess := e.captureSession(sh, now, tmuxOutput)
			if activeTTY != "" && (sh.TTY == activeTTY || "/dev/"+sh.TTY == activeTTY) {
				sess.Focused = true
			}
			sessions[idx] = sess
		}(i, shell)
	}
	wg.Wait()

	var valid []models.Session
	for _, s := range sessions {
		if s.PID <= 0 {
			continue
		}
		if e.isExcludedPath(s.CWD) {
			continue
		}
		valid = append(valid, s)
	}

	valid = group.Assign(valid)
	valid = ordering.Assign(valid)

	hostname, _ := os.Hostname()
	snap := &models.Snapshot{
		Sessions:    valid,
		CreatedAt:   now,
		Source:      source,
		Hostname:    hostname,
		Clipboard:   clipboard.Capture(),
		BrowserTabs: browser.CaptureTabs(),
	}

	intent.Summarize(snap, intent.Config{Provider: intent.ProviderRules})

	if e.captureAI && e.intentCfg.Provider != intent.ProviderRules {
		done := make(chan struct{})
		go func() {
			defer close(done)
			intent.Summarize(snap, e.intentCfg)
		}()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}

	return snap, nil
}

func (e *Engine) captureSession(shell process.ShellInfo, now time.Time, tmuxOutput map[string][]string) models.Session {
	sess := models.Session{
		PID: shell.PID, TTY: shell.TTY, Shell: shell.Shell, CapturedAt: now,
	}

	cwd, err := e.discovery.CWD(shell.PID)
	if err != nil {
		cwd = "~"
	}
	sess.CWD = cwd

	children, _ := e.discovery.Children(shell.PID)
	sess.Children = children
	sess.Command = process.ForegroundCommand(children, shell.Shell)
	sess.Status = "idle"
	if len(children) > 0 {
		sess.Status = "running"
	}

	if zoxide.Available() {
		sess.Label = zoxide.LabelForPath(cwd)
	}
	if sess.Label == "" {
		sess.Label = e.labeler.Match(sess.Command)
	}
	if sess.Label == "" {
		sess.Label = labelFromPath(cwd)
	}

	sess.HasDirenv = direnv.HasEnvrc(cwd)

	// Expensive subsystems — parallel with timeout
	type subResult struct {
		envDelta    models.EnvDelta
		git         *models.GitState
		history     []string
		projectType string
	}
	ch := make(chan subResult, 1)
	go func() {
		var r subResult
		if e.captureEnv && !sess.HasDirenv {
			penv, err := e.discovery.Environ(shell.PID)
			if err == nil && len(penv) > 0 {
				r.envDelta = process.EnvDiff(penv, e.baselineEnv, e.envBlocklist)
			}
		}
		if e.captureGit {
			r.git = gitpkg.State(cwd)
		}
		r.projectType = zoxide.DetectProjectType(cwd)
		r.history = history.ForSession(shell.PID, shell.Shell, cwd, e.historyN)
		ch <- r
	}()

	select {
	case r := <-ch:
		if !r.envDelta.IsEmpty() {
			sess.EnvDelta = r.envDelta
		}
		sess.Git = r.git
		sess.ProjectType = r.projectType
		sess.History = r.history
	case <-time.After(subsystemTimeout):
	}

	if lines, ok := tmuxOutput["/dev/"+shell.TTY]; ok {
		sess.Output = filterOutputLines(lines)
	} else if lines, ok := tmuxOutput[shell.TTY]; ok {
		sess.Output = filterOutputLines(lines)
	}

	return sess
}

// filterOutputLines removes lines that look like they contain credentials.
func filterOutputLines(lines []string) []string {
	var filtered []string
	for _, line := range lines {
		if outputLineContainsSecret(line) {
			filtered = append(filtered, "[redacted output line]")
		} else {
			filtered = append(filtered, line)
		}
	}
	return filtered
}

func outputLineContainsSecret(line string) bool {
	lower := strings.ToLower(line)
	// Key=value patterns with secret-looking keys
	secretPatterns := []string{
		"password:", "password=", "passwd:", "passwd=",
		"secret:", "secret=", "token:", "token=",
		"api_key:", "api_key=", "apikey:", "apikey=",
		"private_key", "-----begin rsa", "-----begin openssh",
		"-----begin private", "-----begin ec private",
		"bearer eyj", // JWT tokens
	}
	for _, p := range secretPatterns {
		if strings.Contains(lower, p) {
			return true
		}
	}
	// Long base64-like strings (> 40 chars of mostly alphanumeric)
	if len(line) > 60 {
		fields := strings.Fields(line)
		for _, f := range fields {
			if len(f) > 40 {
				alnum := 0
				for _, c := range f {
					if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '+' || c == '/' || c == '=' {
						alnum++
					}
				}
				if float64(alnum)/float64(len(f)) > 0.9 {
					return true
				}
			}
		}
	}
	return false
}

func captureBaselineEnv() map[string]string {
	env := make(map[string]string)
	for _, e := range os.Environ() {
		for i := 0; i < len(e); i++ {
			if e[i] == '=' {
				env[e[:i]] = e[i+1:]
				break
			}
		}
	}
	return env
}

func labelFromPath(path string) string { return filepath.Base(path) }

type Labeler struct{ patterns map[string]string }

func NewLabeler(patterns map[string]string) *Labeler { return &Labeler{patterns: patterns} }

func (l *Labeler) Match(command string) string {
	for pattern, label := range l.patterns {
		for _, p := range splitPipes(pattern) {
			if containsWord(command, p) {
				return label
			}
		}
	}
	return ""
}

func splitPipes(s string) []string {
	var parts []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '|' {
			parts = append(parts, s[start:i])
			start = i + 1
		}
	}
	return append(parts, s[start:])
}

func containsWord(haystack, needle string) bool {
	return len(haystack) >= len(needle) &&
		(haystack == needle ||
			(len(haystack) > len(needle) &&
				(haystack[:len(needle)] == needle ||
					haystack[len(haystack)-len(needle):] == needle)))
}

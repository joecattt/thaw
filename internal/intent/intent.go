package intent

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/joecattt/thaw/pkg/models"
)

// Provider defines which AI backend to use.
type Provider string

const (
	ProviderClaude  Provider = "claude"
	ProviderOllama  Provider = "ollama"
	ProviderRules   Provider = "rules" // no AI, rule-based fallback
)

// Config for the intent engine.
type Config struct {
	Provider   Provider
	APIKey     string // for Claude
	OllamaURL  string // default http://localhost:11434
	OllamaModel string // default llama3.2
}

// DefaultConfig returns a config that auto-detects the best available provider.
func DefaultConfig() Config {
	cfg := Config{
		Provider:    ProviderRules,
		OllamaURL:   "http://localhost:11434",
		OllamaModel: "llama3.2",
	}

	// Check for Claude API key
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		cfg.Provider = ProviderClaude
		cfg.APIKey = key
	} else if isOllamaRunning(cfg.OllamaURL) {
		cfg.Provider = ProviderOllama
	}

	return cfg
}

// Summarize generates a one-line intent for each session and an overall workspace summary.
func Summarize(snap *models.Snapshot, cfg Config) {
	switch cfg.Provider {
	case ProviderClaude:
		summarizeClaude(snap, cfg)
	case ProviderOllama:
		summarizeOllama(snap, cfg)
	default:
		summarizeRules(snap)
	}
}

// summarizeRules generates intent from pattern matching — no AI required.
func summarizeRules(snap *models.Snapshot) {
	for i := range snap.Sessions {
		snap.Sessions[i].Intent = ruleBasedIntent(snap.Sessions[i])
	}
	snap.Intent = workspaceIntent(snap.Sessions)
}

func ruleBasedIntent(s models.Session) string {
	cmd := strings.ToLower(s.Command)
	cwd := s.CWD
	branch := ""
	if s.Git != nil {
		branch = s.Git.Branch
	}

	// Pattern matching for common dev activities
	switch {
	case strings.Contains(cmd, "npm run dev") || strings.Contains(cmd, "yarn dev") || strings.Contains(cmd, "next dev"):
		return "running dev server"
	case strings.Contains(cmd, "npm run build") || strings.Contains(cmd, "yarn build"):
		return "building project"
	case strings.Contains(cmd, "npm test") || strings.Contains(cmd, "jest") || strings.Contains(cmd, "pytest") || strings.Contains(cmd, "go test"):
		return "running tests"
	case strings.Contains(cmd, "npm run start") || strings.Contains(cmd, "node server") || strings.Contains(cmd, "node index"):
		return "running server"
	case strings.Contains(cmd, "docker compose up") || strings.Contains(cmd, "docker-compose up"):
		return "running containers"
	case strings.Contains(cmd, "tail -f") || strings.Contains(cmd, "tail -F"):
		return "watching logs"
	case strings.Contains(cmd, "ssh "):
		host := extractHost(cmd)
		return "connected to " + host
	case strings.Contains(cmd, "vim ") || strings.Contains(cmd, "nvim ") || strings.Contains(cmd, "emacs "):
		return "editing files"
	case strings.Contains(cmd, "psql") || strings.Contains(cmd, "mysql") || strings.Contains(cmd, "redis-cli"):
		return "database session"
	case strings.Contains(cmd, "kubectl") || strings.Contains(cmd, "helm"):
		return "managing k8s"
	case strings.Contains(cmd, "git log") || strings.Contains(cmd, "git diff") || strings.Contains(cmd, "git show"):
		return "reviewing changes"
	case strings.Contains(cmd, "go run"):
		return "running go program"
	case strings.Contains(cmd, "cargo run"):
		return "running rust program"
	case strings.Contains(cmd, "python ") || strings.Contains(cmd, "python3 "):
		return "running python script"
	case s.IsIdle() && branch != "" && strings.Contains(branch, "fix"):
		return "working on bugfix (" + branch + ")"
	case s.IsIdle() && branch != "" && strings.Contains(branch, "feature"):
		return "developing feature (" + branch + ")"
	case s.IsIdle() && branch != "":
		return "on " + branch
	case s.IsIdle():
		return "idle in " + lastPathComponent(cwd)
	default:
		return "running " + firstWord(cmd)
	}
}

func workspaceIntent(sessions []models.Session) string {
	if len(sessions) == 0 {
		return ""
	}

	activities := make(map[string]int)
	var project string
	for _, s := range sessions {
		if s.Git != nil && s.Git.RepoRoot != "" && project == "" {
			project = lastPathComponent(s.Git.RepoRoot)
		}
		act := categorize(s)
		activities[act]++
	}

	var parts []string
	if project != "" {
		parts = append(parts, project)
	}
	for act := range activities {
		if act != "" {
			parts = append(parts, act)
		}
	}
	if len(parts) == 0 {
		return fmt.Sprintf("%d sessions", len(sessions))
	}
	return strings.Join(parts, " — ")
}

func categorize(s models.Session) string {
	cmd := strings.ToLower(s.Command)
	switch {
	case strings.Contains(cmd, "dev") || strings.Contains(cmd, "serve"):
		return "development"
	case strings.Contains(cmd, "test") || strings.Contains(cmd, "spec"):
		return "testing"
	case strings.Contains(cmd, "log") || strings.Contains(cmd, "tail"):
		return "monitoring"
	case strings.Contains(cmd, "ssh"):
		return "remote"
	case strings.Contains(cmd, "docker") || strings.Contains(cmd, "kubectl"):
		return "infrastructure"
	default:
		return ""
	}
}

// summarizeClaude calls the Anthropic API.
func summarizeClaude(snap *models.Snapshot, cfg Config) {
	prompt := buildPrompt(snap)

	body := map[string]interface{}{
		"model":      "claude-sonnet-4-20250514",
		"max_tokens": 500,
		"messages": []map[string]string{
			{"role": "user", "content": prompt},
		},
	}

	jsonBody, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", bytes.NewReader(jsonBody))
	if err != nil {
		summarizeRules(snap)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-api-key", cfg.APIKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		summarizeRules(snap)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		summarizeRules(snap)
		return
	}

	parseAIResponse(snap, string(respBody))
}

// summarizeOllama calls a local Ollama instance.
func summarizeOllama(snap *models.Snapshot, cfg Config) {
	prompt := buildPrompt(snap)

	body := map[string]interface{}{
		"model":  cfg.OllamaModel,
		"prompt": prompt,
		"stream": false,
	}

	jsonBody, _ := json.Marshal(body)
	req, err := http.NewRequest("POST", cfg.OllamaURL+"/api/generate", bytes.NewReader(jsonBody))
	if err != nil {
		summarizeRules(snap)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		summarizeRules(snap)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		summarizeRules(snap)
		return
	}

	// Ollama response: {"response": "..."}
	var ollamaResp struct {
		Response string `json:"response"`
	}
	if json.Unmarshal(respBody, &ollamaResp) == nil && ollamaResp.Response != "" {
		parseIntentText(snap, ollamaResp.Response)
	} else {
		summarizeRules(snap)
	}
}

func buildPrompt(snap *models.Snapshot) string {
	var b strings.Builder
	b.WriteString("Analyze these terminal sessions and provide a one-line intent summary for each, plus an overall workspace summary.\n\n")
	b.WriteString("Sessions:\n")

	for i, s := range snap.Sessions {
		// Redact full paths to just the last 2 components — prevents leaking internal infra paths
		cwd := redactPath(s.CWD)
		cmd := redactCommand(s.Command)

		b.WriteString(fmt.Sprintf("%d. CWD=%s CMD=%s", i+1, cwd, cmd))
		if s.Git != nil {
			b.WriteString(fmt.Sprintf(" BRANCH=%s", s.Git.Branch))
			if s.Git.Dirty {
				b.WriteString(" DIRTY")
			}
		}
		if len(s.History) > 0 {
			recent := s.History
			if len(recent) > 3 {
				recent = recent[len(recent)-3:]
			}
			var redacted []string
			for _, h := range recent {
				redacted = append(redacted, redactCommand(h))
			}
			b.WriteString(fmt.Sprintf(" RECENT=[%s]", strings.Join(redacted, "; ")))
		}
		b.WriteString("\n")
	}

	b.WriteString("\nRespond in ONLY this JSON format, no other text:\n")
	b.WriteString(`{"workspace":"one line overall summary","sessions":["session 1 intent","session 2 intent"]}`)
	b.WriteString("\n\nKeep each intent under 10 words. Be specific about what the developer was doing, not just what command was running.")

	return b.String()
}

// redactPath strips full paths to last 2 components.
// /home/joecat/projects/secret-client/api → .../secret-client/api
func redactPath(path string) string {
	parts := strings.Split(path, "/")
	if len(parts) <= 3 {
		return path
	}
	return ".../" + strings.Join(parts[len(parts)-2:], "/")
}

// redactCommand removes IP addresses, URLs, hostnames, and credential-looking arguments.
func redactCommand(cmd string) string {
	parts := strings.Fields(cmd)
	var redacted []string
	for _, p := range parts {
		switch {
		case strings.Contains(p, "://"):
			// Redact URLs (may contain credentials in query params)
			redacted = append(redacted, "[url]")
		case isIPAddress(p):
			redacted = append(redacted, "[ip]")
		case strings.Contains(p, "@") && strings.Contains(p, "."):
			// user@host patterns
			at := strings.LastIndex(p, "@")
			redacted = append(redacted, p[:at+1]+"[host]")
		case looksLikeCredential(p):
			redacted = append(redacted, "[redacted]")
		default:
			redacted = append(redacted, p)
		}
	}
	return strings.Join(redacted, " ")
}

func isIPAddress(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		p = strings.Split(p, ":")[0] // strip port
		n, err := fmt.Sscanf(p, "%d", new(int))
		if err != nil || n != 1 {
			return false
		}
	}
	return true
}

func looksLikeCredential(s string) bool {
	// Long hex/base64 strings are likely tokens
	if len(s) > 30 {
		alphanum := 0
		for _, c := range s {
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
				alphanum++
			}
		}
		if float64(alphanum)/float64(len(s)) > 0.8 {
			return true
		}
	}
	// Patterns: sk-, pk-, key-, token:, bearer
	lower := strings.ToLower(s)
	prefixes := []string{"sk-", "pk-", "sk_", "pk_", "key-", "token:", "bearer"}
	for _, p := range prefixes {
		if strings.HasPrefix(lower, p) {
			return true
		}
	}
	return false
}

func parseAIResponse(snap *models.Snapshot, body string) {
	// Extract text from Claude response
	var claudeResp struct {
		Content []struct {
			Text string `json:"text"`
		} `json:"content"`
	}
	if json.Unmarshal([]byte(body), &claudeResp) != nil || len(claudeResp.Content) == 0 {
		summarizeRules(snap)
		return
	}

	parseIntentText(snap, claudeResp.Content[0].Text)
}

func parseIntentText(snap *models.Snapshot, text string) {
	// Try to parse as JSON
	var result struct {
		Workspace string   `json:"workspace"`
		Sessions  []string `json:"sessions"`
	}

	// Find JSON in the response (may have surrounding text)
	start := strings.Index(text, "{")
	end := strings.LastIndex(text, "}")
	if start >= 0 && end > start {
		text = text[start : end+1]
	}

	if json.Unmarshal([]byte(text), &result) != nil {
		summarizeRules(snap)
		return
	}

	snap.Intent = result.Workspace
	for i := range snap.Sessions {
		if i < len(result.Sessions) {
			snap.Sessions[i].Intent = result.Sessions[i]
		}
	}
}

func isOllamaRunning(url string) bool {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == 200
}

func extractHost(cmd string) string {
	parts := strings.Fields(cmd)
	for i, p := range parts {
		if p == "ssh" && i+1 < len(parts) {
			for j := i + 1; j < len(parts); j++ {
				if !strings.HasPrefix(parts[j], "-") {
					host := parts[j]
					if idx := strings.LastIndex(host, "@"); idx >= 0 {
						host = host[idx+1:]
					}
					return host
				}
			}
		}
	}
	return "remote"
}

func lastPathComponent(p string) string {
	for i := len(p) - 1; i >= 0; i-- {
		if p[i] == '/' && i < len(p)-1 {
			return p[i+1:]
		}
	}
	return p
}

func firstWord(s string) string {
	for i, c := range s {
		if c == ' ' {
			return s[:i]
		}
	}
	return s
}

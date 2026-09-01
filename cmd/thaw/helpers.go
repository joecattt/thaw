package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joecattt/thaw/internal/capture"
	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/intent"
	"github.com/joecattt/thaw/internal/llm"
	"github.com/joecattt/thaw/internal/process"
)

func newEngine(cfg config.Config) *capture.Engine {
	disc := process.NewDiscovery()
	eng := capture.New(disc, cfg.Labels)
	eng.SetHistoryLines(cfg.Capture.HistoryLines)
	eng.SetOutputLines(cfg.Capture.OutputLines)
	eng.SetCaptureEnv(cfg.Capture.CaptureEnv)
	eng.SetCaptureGit(cfg.Capture.CaptureGit)
	eng.SetCaptureAI(cfg.Capture.CaptureAI)
	eng.SetCaptureClipboard(cfg.Capture.Clipboard)
	eng.SetCaptureBrowserTabs(cfg.Capture.BrowserTabs)
	eng.SetEnvBlocklist(cfg.Capture.EnvBlocklist)
	eng.SetExcludePaths(cfg.Capture.ExcludePaths)

	intentCfg := intent.DefaultConfig()
	switch cfg.Capture.AIProvider {
	case "claude":
		intentCfg.Provider = intent.ProviderClaude
	case "ollama":
		intentCfg.Provider = intent.ProviderOllama
	case "rules":
		intentCfg.Provider = intent.ProviderRules
	}
	if cfg.Capture.OllamaModel != "" {
		intentCfg.OllamaModel = cfg.Capture.OllamaModel
	}
	eng.SetIntentConfig(intentCfg)

	return eng
}

// newLLMClient creates an LLM client from the AI config section.

func newLLMClient(cfg config.Config) *llm.Client {
	return llm.New(llm.Config{
		Provider:  llm.Provider(cfg.AI.Provider),
		APIKeyEnv: cfg.AI.APIKeyEnv,
		Model:     cfg.AI.Model,
		Endpoint:  cfg.AI.Endpoint,
	})
}

func sanitize(s string) string {
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, " ", "-")
	if len(s) > 30 {
		s = s[:30]
	}
	return s
}

func parseFlexTime(s string) (time.Time, error) {
	now := time.Now()
	// Try HH:MM (today)
	if t, err := time.Parse("15:04", s); err == nil {
		return time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, now.Location()), nil
	}
	// Try full RFC3339
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	// Try YYYY-MM-DDTHH:MM
	if t, err := time.Parse("2006-01-02T15:04", s); err == nil {
		return t, nil
	}
	// Try YYYY-MM-DD HH:MM
	if t, err := time.Parse("2006-01-02 15:04", s); err == nil {
		return t, nil
	}
	return time.Time{}, fmt.Errorf("unrecognized format: %s (use HH:MM or YYYY-MM-DDTHH:MM)", s)
}

func scrubCommandLog(from, to time.Time) int {
	home, _ := os.UserHomeDir()
	logPath := filepath.Join(home, ".local", "state", "thaw", "commands.log")

	data, err := os.ReadFile(logPath)
	if err != nil {
		return 0
	}

	var kept []string
	scrubbed := 0
	for _, line := range strings.Split(string(data), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "|", 2)
		if len(parts) < 2 {
			kept = append(kept, line)
			continue
		}
		ts, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			kept = append(kept, line)
			continue
		}
		t := time.Unix(ts, 0)
		if t.After(from) && t.Before(to) {
			scrubbed++
			continue
		}
		kept = append(kept, line)
	}

	os.WriteFile(logPath, []byte(strings.Join(kept, "\n")+"\n"), 0600)
	return scrubbed
}

func removeThawHooks(rcFile string) bool {
	data, err := os.ReadFile(rcFile)
	if err != nil {
		return false
	}
	content := string(data)
	if !strings.Contains(content, "thaw") {
		return false
	}

	// Remove lines containing thaw references
	var kept []string
	inThawBlock := false
	for _, line := range strings.Split(content, "\n") {
		if strings.Contains(line, "# thaw") || strings.Contains(line, "_thaw_") || strings.Contains(line, "thaw shell-init") || strings.Contains(line, "thaw freeze") || strings.Contains(line, "thaw daemon") {
			inThawBlock = true
			continue
		}
		if inThawBlock && (strings.HasPrefix(line, "  ") || strings.HasPrefix(line, "\t") || line == "") {
			continue
		}
		inThawBlock = false
		kept = append(kept, line)
	}

	return os.WriteFile(rcFile, []byte(strings.Join(kept, "\n")), 0644) == nil
}

func shellRCPath(shell string) string {
	home, _ := os.UserHomeDir()
	switch shell {
	case "zsh":
		return filepath.Join(home, ".zshrc")
	case "bash":
		return filepath.Join(home, ".bashrc")
	case "fish":
		return filepath.Join(home, ".config", "fish", "config.fish")
	default:
		return ""
	}
}

func humanSize(bytes int64) string {
	if bytes < 1024 {
		return fmt.Sprintf("%d B", bytes)
	}
	if bytes < 1024*1024 {
		return fmt.Sprintf("%.1f KB", float64(bytes)/1024)
	}
	return fmt.Sprintf("%.1f MB", float64(bytes)/(1024*1024))
}

func formatDurationShort(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	m := int(d.Minutes())
	if m < 60 {
		return fmt.Sprintf("%dm", m)
	}
	return fmt.Sprintf("%dh %dm", m/60, m%60)
}

func availableDiskMB(path string) int64 {
	// Use syscall.Statfs on Unix
	var stat syscall.Statfs_t
	if err := syscall.Statfs(path, &stat); err != nil {
		return -1
	}
	return int64(stat.Bavail) * int64(stat.Bsize) / (1024 * 1024)
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func truncateLeft(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return "..." + s[len(s)-max+3:]
}

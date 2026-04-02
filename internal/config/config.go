package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	General   GeneralConfig     `toml:"general"`
	Safety    SafetyConfig      `toml:"safety"`
	Daemon    DaemonConfig      `toml:"daemon"`
	Capture   CaptureConfig     `toml:"capture"`
	Restore   RestoreConfig     `toml:"restore"`
	Voice     VoiceConfigTOML   `toml:"voice"`
	AI        AIConfig          `toml:"ai"`
	Briefing  BriefingConfig    `toml:"briefing"`
	Telemetry TelemetryConfig   `toml:"telemetry"`
	Labels    map[string]string `toml:"labels"`
}

type TelemetryConfig struct {
	Enabled     bool   `toml:"enabled"`      // opt-in anonymous analytics
	FirebaseURL string `toml:"firebase_url"` // Firebase Realtime DB URL
}

type VoiceConfigTOML struct {
	Backend       string `toml:"tts_backend"`
	CortanaPath   string `toml:"cortana_path"`
	ElevenPath    string `toml:"elevenlabs_path"`
	KokoroMode    bool   `toml:"kokoro_mode"`
	MorningBrief  bool   `toml:"morning_briefing"`
}

type AIConfig struct {
	Provider    string `toml:"provider"`      // claude | ollama | openai | none
	APIKeyEnv   string `toml:"api_key_env"`   // env var name for API key
	Model       string `toml:"model"`
	Endpoint    string `toml:"endpoint"`      // for ollama/custom
	GapAnalysis bool   `toml:"gap_analysis"`  // "what should I do next" in recap
}

type BriefingConfig struct {
	Theme              string `toml:"theme"`              // frost | minimal | terminal
	AutoOpen           bool   `toml:"auto_open"`
	PriorityOrder      string `toml:"priority_order"`     // blocked | recency | time_spent
	ShowProcesses      bool   `toml:"show_processes"`
	ShowResumeCommands bool   `toml:"show_resume_commands"`
	ShowUpstream       bool   `toml:"show_upstream"`
}

type GeneralConfig struct {
	RestoreTarget string `toml:"restore_target"`
	AutoSnapshot  bool   `toml:"auto_snapshot"`
	AutoRestore   bool   `toml:"auto_restore"`
}

type SafetyConfig struct {
	DefaultMode        string `toml:"default_mode"`
	ConfirmDestructive bool   `toml:"confirm_destructive"`
	SkipStale          bool   `toml:"skip_stale"`
}

type DaemonConfig struct {
	Enabled     bool `toml:"enabled"`
	IntervalMin int  `toml:"interval_min"`
	KeepMax     int  `toml:"keep_max"`
	KeepDays    int  `toml:"keep_days"`
}

type CaptureConfig struct {
	HistoryLines     int      `toml:"history_lines"`
	OutputLines      int      `toml:"output_lines"`
	CaptureEnv       bool     `toml:"capture_env"`
	CaptureGit       bool     `toml:"capture_git"`
	CaptureAI        bool     `toml:"capture_ai"`
	AIProvider       string   `toml:"ai_provider"`
	OllamaModel      string   `toml:"ollama_model"`
	EnvBlocklist     []string `toml:"env_blocklist"`
	ExcludePaths     []string `toml:"exclude_paths"`
	IdleThresholdMin int      `toml:"idle_threshold_min"` // minutes before idle gap triggers context
	Autostash        bool     `toml:"autostash"`          // git stash on context switch
	BrowserTabs      bool     `toml:"browser_tabs"`
	Clipboard        bool     `toml:"clipboard"`
}

type RestoreConfig struct {
	RestoreEnv    bool   `toml:"restore_env"`
	RestoreGit    bool   `toml:"restore_git"`
	ShowHistory   bool   `toml:"show_history"`
	ShowOutput    bool   `toml:"show_output"`
	ShowIntent    bool   `toml:"show_intent"`
	DefaultLayout string `toml:"default_layout"`
	MultiSession  bool   `toml:"multi_session"`
	MaxPanes      int    `toml:"max_panes"`
	TierDelaySec  int    `toml:"tier_delay_sec"`
}

func DefaultConfig() Config {
	return Config{
		General: GeneralConfig{
			RestoreTarget: "tmux",
			AutoSnapshot:  true,
			AutoRestore:   false,
		},
		Safety: SafetyConfig{
			DefaultMode:        "safe",
			ConfirmDestructive: true,
			SkipStale:          false,
		},
		Daemon: DaemonConfig{
			Enabled:     true,
			IntervalMin: 5,
			KeepMax:     100,
			KeepDays:    7,
		},
		Capture: CaptureConfig{
			HistoryLines:     20,
			OutputLines:      30,
			CaptureEnv:       true,
			CaptureGit:       true,
			CaptureAI:        false,
			AIProvider:       "auto",
			OllamaModel:      "llama3.2",
			EnvBlocklist:     []string{},
			IdleThresholdMin: 30,
			Autostash:        true,
			BrowserTabs:      false,
			Clipboard:        false,
		},
		Restore: RestoreConfig{
			RestoreEnv:    true,
			RestoreGit:    false,
			ShowHistory:   true,
			ShowOutput:    true,
			ShowIntent:    true,
			DefaultLayout: "tiled",
			MultiSession:  true,
			MaxPanes:      8,
			TierDelaySec:  2,
		},
		Labels: map[string]string{
			"node|npm|yarn|bun":       "Backend",
			"python|pip|pytest":       "Python",
			"ssh":                     "Remote",
			"tail|less|log":           "Logs",
			"psql|mysql|redis-cli":    "Database",
			"go run|go build|go test": "Go",
			"cargo|rustc":             "Rust",
			"vim|nvim|emacs":          "Editor",
			"docker|podman":           "Container",
			"kubectl|helm":            "K8s",
		},
		Voice: VoiceConfigTOML{
			Backend:      "auto",
			MorningBrief: false,
		},
		AI: AIConfig{
			Provider:    "none",
			APIKeyEnv:   "ANTHROPIC_API_KEY",
			Model:       "claude-sonnet-4-20250514",
			Endpoint:    "",
			GapAnalysis: true,
		},
		Briefing: BriefingConfig{
			Theme:              "frost",
			AutoOpen:           true,
			PriorityOrder:      "blocked",
			ShowProcesses:      true,
			ShowResumeCommands: true,
			ShowUpstream:       true,
		},
		Telemetry: TelemetryConfig{
			Enabled:     false,
			FirebaseURL: "",
		},
	}
}

func ConfigDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".config", "thaw"), nil
}

func DataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("cannot determine home directory: %w", err)
	}
	return filepath.Join(home, ".local", "share", "thaw"), nil
}

func ConfigPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

func Load() (Config, error) {
	cfg := DefaultConfig()
	path, err := ConfigPath()
	if err != nil {
		return cfg, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("reading config: %w", err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		fmt.Fprintf(os.Stderr, "thaw: config parse error (%s) — using defaults. Run: thaw config reset\n", err)
		return DefaultConfig(), nil
	}

	// Validate and warn about invalid values
	for _, w := range Validate(cfg) {
		fmt.Fprintf(os.Stderr, "thaw config warning: %s\n", w)
	}

	return cfg, nil
}

func Save(cfg Config) error {
	path, err := ConfigPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return fmt.Errorf("creating config dir: %w", err)
	}
	f, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("creating config file: %w", err)
	}
	defer f.Close()
	return toml.NewEncoder(f).Encode(cfg)
}

func EnsureDirectories() error {
	for _, fn := range []func() (string, error){ConfigDir, DataDir} {
		dir, err := fn()
		if err != nil {
			return err
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			return fmt.Errorf("creating directory %s: %w", dir, err)
		}
	}
	return nil
}

// Validate checks config values and returns warnings for invalid entries.
func Validate(cfg Config) []string {
	var warnings []string

	if cfg.Restore.MaxPanes < 1 || cfg.Restore.MaxPanes > 50 {
		warnings = append(warnings, fmt.Sprintf("max_panes=%d is unusual (expected 1-50)", cfg.Restore.MaxPanes))
	}
	if cfg.Restore.TierDelaySec < 0 || cfg.Restore.TierDelaySec > 60 {
		warnings = append(warnings, fmt.Sprintf("tier_delay_sec=%d is unusual (expected 0-60)", cfg.Restore.TierDelaySec))
	}
	if cfg.Daemon.IntervalMin < 1 || cfg.Daemon.IntervalMin > 60 {
		warnings = append(warnings, fmt.Sprintf("interval_min=%d is unusual (expected 1-60)", cfg.Daemon.IntervalMin))
	}

	validModes := map[string]bool{"safe": true, "run": true}
	if !validModes[cfg.Safety.DefaultMode] {
		warnings = append(warnings, fmt.Sprintf("default_mode=%q invalid (use 'safe' or 'run')", cfg.Safety.DefaultMode))
	}

	validProviders := map[string]bool{"auto": true, "claude": true, "ollama": true, "rules": true}
	if !validProviders[cfg.Capture.AIProvider] {
		warnings = append(warnings, fmt.Sprintf("ai_provider=%q not recognized (use auto, claude, ollama, or rules)", cfg.Capture.AIProvider))
	}

	validLayouts := map[string]bool{"tiled": true, "even-horizontal": true, "even-vertical": true, "main-horizontal": true, "main-vertical": true, "": true}
	if !validLayouts[cfg.Restore.DefaultLayout] {
		warnings = append(warnings, fmt.Sprintf("default_layout=%q not a valid tmux layout", cfg.Restore.DefaultLayout))
	}

	return warnings
}

// SetField sets a config field by dot-notation key (e.g. "ai.provider", "daemon.interval_min").
func SetField(cfg *Config, key, value string) error {
	switch key {
	// General
	case "general.restore_target":
		cfg.General.RestoreTarget = value
	case "general.auto_snapshot":
		cfg.General.AutoSnapshot = value == "true"
	// Safety
	case "safety.default_mode":
		cfg.Safety.DefaultMode = value
	case "safety.confirm_destructive":
		cfg.Safety.ConfirmDestructive = value == "true"
	// Daemon
	case "daemon.enabled":
		cfg.Daemon.Enabled = value == "true"
	case "daemon.interval_min":
		n := 0; fmt.Sscan(value, &n); cfg.Daemon.IntervalMin = n
	case "daemon.keep_max":
		n := 0; fmt.Sscan(value, &n); cfg.Daemon.KeepMax = n
	case "daemon.keep_days":
		n := 0; fmt.Sscan(value, &n); cfg.Daemon.KeepDays = n
	// Capture
	case "capture.history_lines":
		n := 0; fmt.Sscan(value, &n); cfg.Capture.HistoryLines = n
	case "capture.output_lines":
		n := 0; fmt.Sscan(value, &n); cfg.Capture.OutputLines = n
	case "capture.idle_threshold_min":
		n := 0; fmt.Sscan(value, &n); cfg.Capture.IdleThresholdMin = n
	case "capture.autostash":
		cfg.Capture.Autostash = value == "true"
	case "capture.browser_tabs":
		cfg.Capture.BrowserTabs = value == "true"
	case "capture.clipboard":
		cfg.Capture.Clipboard = value == "true"
	case "capture.capture_ai":
		cfg.Capture.CaptureAI = value == "true"
	case "capture.ai_provider":
		cfg.Capture.AIProvider = value
	// Restore
	case "restore.restore_env":
		cfg.Restore.RestoreEnv = value == "true"
	case "restore.show_history":
		cfg.Restore.ShowHistory = value == "true"
	case "restore.default_layout":
		cfg.Restore.DefaultLayout = value
	case "restore.multi_session":
		cfg.Restore.MultiSession = value == "true"
	case "restore.max_panes":
		n := 0; fmt.Sscan(value, &n); cfg.Restore.MaxPanes = n
	// Voice
	case "voice.tts_backend":
		cfg.Voice.Backend = value
	case "voice.morning_briefing":
		cfg.Voice.MorningBrief = value == "true"
	// AI
	case "ai.provider":
		cfg.AI.Provider = value
	case "ai.api_key_env":
		cfg.AI.APIKeyEnv = value
	case "ai.model":
		cfg.AI.Model = value
	case "ai.endpoint":
		cfg.AI.Endpoint = value
	case "ai.gap_analysis":
		cfg.AI.GapAnalysis = value == "true"
	// Briefing
	case "briefing.theme":
		cfg.Briefing.Theme = value
	case "briefing.auto_open":
		cfg.Briefing.AutoOpen = value == "true"
	case "briefing.priority_order":
		cfg.Briefing.PriorityOrder = value
	case "briefing.show_processes":
		cfg.Briefing.ShowProcesses = value == "true"
	case "briefing.show_resume_commands":
		cfg.Briefing.ShowResumeCommands = value == "true"
	case "briefing.show_upstream":
		cfg.Briefing.ShowUpstream = value == "true"
	// Telemetry
	case "telemetry.enabled":
		cfg.Telemetry.Enabled = value == "true"
	case "telemetry.firebase_url":
		cfg.Telemetry.FirebaseURL = value
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return nil
}

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/audit"
	"github.com/joecattt/thaw/internal/capture"
	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/context"
	"github.com/joecattt/thaw/internal/daemon"
	"github.com/joecattt/thaw/internal/dashboard"
	"github.com/joecattt/thaw/internal/deprot"
	"github.com/joecattt/thaw/internal/diff"
	"github.com/joecattt/thaw/internal/export"
	"github.com/joecattt/thaw/internal/history"
	"github.com/joecattt/thaw/internal/intent"
	"github.com/joecattt/thaw/internal/llm"
	"github.com/joecattt/thaw/internal/briefing"
	"github.com/joecattt/thaw/internal/memory"
	"github.com/joecattt/thaw/internal/process"
	"github.com/joecattt/thaw/internal/progress"
	"github.com/joecattt/thaw/internal/project"
	"github.com/joecattt/thaw/internal/recap"
	"github.com/joecattt/thaw/internal/recovery"
	"github.com/joecattt/thaw/internal/restore"
	"github.com/joecattt/thaw/internal/setup"
	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/internal/stale"
	"github.com/joecattt/thaw/internal/telemetry"
	"github.com/joecattt/thaw/internal/upstream"
	"github.com/joecattt/thaw/pkg/models"
)

var version = "3.3.0"

func main() {
	root := &cobra.Command{
		Use:   "thaw",
		Short: "Thaw — terminal workspace memory. Close your laptop, open it tomorrow, everything's back.",
		Long: `Thaw captures your terminal workspace and restores it exactly as it was.

  thaw              restore your last workspace
  thaw freeze       snapshot current state
  thaw save <name>  save a named workspace
  thaw recall <name> restore a named workspace
  thaw status       show live sessions
  thaw setup        one-command install`,
		Version: version,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			cfg, err := config.Load()
			if err == nil && cfg.Telemetry.Enabled && cfg.Telemetry.FirebaseURL != "" {
				telemetry.FirebaseURL = cfg.Telemetry.FirebaseURL
			}
			if cmd.Name() != "thaw" {
				telemetry.SendCommand(version, cmd.Name())
			}
		},
		// Default action with no subcommand: interactive restore
		RunE: func(cmd *cobra.Command, args []string) error {
			return doInteractiveRestore()
		},
	}

	// Core commands — daily use
	root.AddCommand(
		freezeCmd(),
		saveCmd(),
		recallCmd(),
		recapCmd(),
		diffCmd(),
		undoCmd(),
		doctorCmd(),
		statusCmd(),
		inspectCmd(),
		historyCmd(),
		progressCmd(),
		contextCmd(),
		exportDataCmd(),
		dashboardCmd(),
		initProjectCmd(),
		setupCmd(),
		daemonCmd(),
		configCmd(),
		shellInitCmd(),
		logCmdCmd(),
	)

	// Advanced commands under `thaw admin`
	admin := &cobra.Command{
		Use:   "admin",
		Short: "Advanced — note, forget, tag, audit, dump, import, prune",
	}
	admin.AddCommand(
		noteCmd(),
		forgetCmd(),
		tagCmd(),
		auditCmd(),
		dumpCmd(),
		importCmd(),
		pruneCmd(),
		migrateCmd(),
		uninstallCmd(),
	)
	root.AddCommand(admin)

	if err := root.Execute(); err != nil {
		os.Exit(1)
	}
}

// --- helpers ---

func newEngine(cfg config.Config) *capture.Engine {
	disc := process.NewDiscovery()
	eng := capture.New(disc, cfg.Labels)
	eng.SetHistoryLines(cfg.Capture.HistoryLines)
	eng.SetOutputLines(cfg.Capture.OutputLines)
	eng.SetCaptureEnv(cfg.Capture.CaptureEnv)
	eng.SetCaptureGit(cfg.Capture.CaptureGit)
	eng.SetCaptureAI(cfg.Capture.CaptureAI)
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

// initStore opens the snapshot store with standard setup.
func initStore() (*snapshot.Store, config.Config, error) {
	if err := config.EnsureDirectories(); err != nil {
		return nil, config.Config{}, err
	}
	cfg, err := config.Load()
	if err != nil {
		return nil, config.Config{}, err
	}
	store, err := snapshot.Open()
	if err != nil {
		return nil, config.Config{}, fmt.Errorf("opening store: %w", err)
	}
	return store, cfg, nil
}

// doRestore is the shared restore logic for both `thaw` (default) and `thaw recall`.
func doRestore(optsOverride models.RestoreOptions, nameOrID string, dryRun bool) error {
	if err := config.EnsureDirectories(); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	store, err := snapshot.Open()
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer store.Close()

	// Check for crash recovery — if last shutdown was unexpected,
	// reconstruct state from command log
	if nameOrID == "" {
		recovered, err := recovery.Check(store)
		if err == nil && recovered != nil {
			fmt.Printf("Recovered %d session(s) after unexpected shutdown\n", len(recovered.Sessions))
		}
	}

	var snap *models.Snapshot

	if nameOrID != "" {
		// Try as named workspace first
		snap, err = store.GetNamed(nameOrID)
		if err != nil {
			return err
		}
		// Try as numeric ID
		if snap == nil {
			if id, e := strconv.Atoi(nameOrID); e == nil {
				snap, err = store.Get(id)
				if err != nil {
					return err
				}
			}
		}
		if snap == nil {
			return fmt.Errorf("workspace %q not found — run `thaw save %s` to create it", nameOrID, nameOrID)
		}
	} else {
		snap, err = store.Latest()
		if err != nil {
			return err
		}
	}

	if snap == nil {
		fmt.Println("Nothing to thaw. Run `thaw freeze` first.")
		return nil
	}

	// Build options from config defaults + overrides
	opts := models.DefaultRestoreOptions()
	opts.RestoreEnv = cfg.Restore.RestoreEnv
	opts.ShowHistory = cfg.Restore.ShowHistory
	opts.ShowOutput = cfg.Restore.ShowOutput
	opts.ShowIntent = cfg.Restore.ShowIntent
	opts.MultiSession = cfg.Restore.MultiSession
	opts.MaxPanes = cfg.Restore.MaxPanes
	opts.TierDelaySec = cfg.Restore.TierDelaySec
	if cfg.Restore.DefaultLayout != "" {
		opts.Layout = cfg.Restore.DefaultLayout
	}
	opts.SkipStale = cfg.Safety.SkipStale
	opts.HistoryLines = cfg.Capture.HistoryLines

	// Apply overrides
	if optsOverride.Mode != 0 {
		opts.Mode = optsOverride.Mode
	}
	if optsOverride.SessionName != "" {
		opts.SessionName = optsOverride.SessionName
	}
	if optsOverride.Layout != "" {
		opts.Layout = optsOverride.Layout
	}

	// Auto-prune old snapshots
	store.Prune(time.Duration(cfg.Daemon.KeepDays)*24*time.Hour, cfg.Daemon.KeepMax)

	target := restore.NewTmux()
	if !target.Available() {
		return fmt.Errorf("tmux not found — install with: brew install tmux")
	}

	// Staleness report
	staleChecks := stale.CheckAll(snap)
	staleCount := 0
	for _, sc := range staleChecks {
		if sc.IsStale() {
			staleCount++
		}
	}

	// Dependency rot check — warn if project files changed since snapshot
	depRots := deprot.CheckAll(snap)
	if len(depRots) > 0 {
		warnings := deprot.FormatWarnings(depRots, snap.Sessions)
		fmt.Printf("⚠ Dependencies changed since snapshot:\n")
		for _, w := range warnings {
			fmt.Printf("  %s\n", w)
		}
		fmt.Println()
	}

	if dryRun {
		script, err := target.GenerateScript(snap, opts)
		if err != nil {
			return err
		}
		fmt.Println(script)
		return nil
	}

	label := "latest"
	if snap.Name != "" {
		label = snap.Name
	}
	modeLabel := "safe"
	if opts.Mode == models.RunMode {
		modeLabel = "run"
	}

	restored := len(snap.Sessions)
	if opts.SkipStale {
		restored -= staleCount
	}

	fmt.Printf("Thawing %q — %d sessions (%s mode)\n", label, restored, modeLabel)

	if staleCount > 0 && opts.SkipStale {
		fmt.Printf("  Skipped %d stale session(s)\n", staleCount)
	} else if staleCount > 0 {
		fmt.Printf("  ⚠ %d session(s) have stale context\n", staleCount)
	}

	// Check for auto-stashed work in project directories
	seen := map[string]bool{}
	for _, s := range snap.Sessions {
		if s.Git == nil || s.Git.RepoRoot == "" || seen[s.Git.RepoRoot] {
			continue
		}
		seen[s.Git.RepoRoot] = true
		out, err := exec.Command("git", "-C", s.Git.RepoRoot, "stash", "list").Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "thaw-auto-") {
				fmt.Printf("  📦 Auto-stashed work found in %s — run: git -C %s stash pop\n",
					filepath.Base(s.Git.RepoRoot), s.Git.RepoRoot)
				break
			}
		}
	}

	if err := target.Restore(snap, opts); err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}

	// Show attach instructions
	if opts.MultiSession {
		groups := snap.WorkstreamGroups()
		if len(groups) > 1 {
			fmt.Println("\nWorkstreams restored:")
			for name := range groups {
				fmt.Printf("  tmux attach -t %s\n", sanitize(name))
			}
			return nil
		}
	}
	fmt.Printf("\ntmux attach -t %s\n", opts.SessionName)
	return nil
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

// --- freeze (snapshot) ---

func freezeCmd() *cobra.Command {
	var source string
	cmd := &cobra.Command{
		Use:   "freeze",
		Short: "Capture current terminal state",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			snap, err := newEngine(cfg).Capture(source)
			if err != nil {
				return fmt.Errorf("capture failed: %w", err)
			}
			if len(snap.Sessions) == 0 {
				fmt.Println("No active sessions to freeze.")
				return nil
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.Save(snap); err != nil {
				return err
			}

			// Record project memory for cross-session continuity
			if mem, mErr := memory.Open(); mErr == nil {
				for _, s := range snap.Sessions {
					branch := ""
					if s.Git != nil {
						branch = s.Git.Branch
					}
					lastCmd := ""
					if len(s.History) > 0 {
						lastCmd = s.History[len(s.History)-1]
					}
					mem.Remember(s.CWD, branch, lastCmd, s.PID)
				}
				mem.Close()
			}

			// Auto-prune old snapshots
			store.Prune(time.Duration(cfg.Daemon.KeepDays)*24*time.Hour, cfg.Daemon.KeepMax)

			fmt.Printf("Frozen #%d — %d session(s)\n", snap.ID, len(snap.Sessions))
			if snap.Intent != "" {
				fmt.Printf("  Intent: %s\n", snap.Intent)
			}
			for _, s := range snap.Sessions {
				icon := "○"
				if s.Status == "running" {
					icon = "●"
				}
				extra := ""
				if s.Git != nil {
					extra += " [" + s.Git.Branch
					if s.Git.Dirty {
						extra += "*"
					}
					extra += "]"
				}
				if !s.EnvDelta.IsEmpty() {
					extra += fmt.Sprintf(" +%denv", len(s.EnvDelta.Set))
				}
				if s.Intent != "" {
					extra += " — " + s.Intent
				}
				fmt.Printf("  %s %s — %s%s\n", icon, s.Label, truncate(s.Command, 40), extra)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&source, "source", "manual", "Source tag: manual | shutdown | scheduled")
	return cmd
}

// --- save (named workspace) ---

func saveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "save <name>",
		Short: "Save current state as a named workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			snap, err := newEngine(cfg).Capture("named")
			if err != nil {
				return err
			}
			if len(snap.Sessions) == 0 {
				fmt.Println("No active sessions to save.")
				return nil
			}
			snap.Name = name
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()
			if err := store.Save(snap); err != nil {
				return err
			}
			fmt.Printf("Saved workspace %q — %d session(s)\n", name, len(snap.Sessions))
			return nil
		},
	}
}

// --- recall (named workspace restore) ---

func recallCmd() *cobra.Command {
	var (
		run    bool
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "recall <name>",
		Short: "Restore a named workspace",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := models.RestoreOptions{}
			if run {
				opts.Mode = models.RunMode
			}
			return doRestore(opts, args[0], dryRun)
		},
	}
	cmd.Flags().BoolVar(&run, "run", false, "Execute commands (default: safe mode)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print script without executing")
	// Tab completion for workspace names
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		store, err := snapshot.Open()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		defer store.Close()
		named, _ := store.ListNamed()
		var names []string
		for _, n := range named {
			if n.Name != "" {
				names = append(names, n.Name)
			}
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
	return cmd
}

// --- diff ---

func diffCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "diff",
		Short: "Show what changed since last snapshot",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}

			// Capture current live state
			current, err := newEngine(cfg).Capture("diff")
			if err != nil {
				return fmt.Errorf("capturing live state: %w", err)
			}
			if len(current.Sessions) == 0 {
				fmt.Println("No active sessions to compare.")
				return nil
			}

			// Load last snapshot
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			previous, err := store.Latest()
			if err != nil {
				return err
			}
			if previous == nil {
				fmt.Println("No previous snapshot to compare against. Run `thaw freeze` first.")
				return nil
			}

			// Compare
			result := diff.Compare(previous, current)
			prevTime := previous.CreatedAt.Format("2006-01-02 15:04")
			currTime := "now"
			fmt.Print(diff.FormatResult(result, prevTime, currTime))

			return nil
		},
	}
}

// --- recap ---

func recapCmd() *cobra.Command {
	var (
		voice   bool
		visual  bool
		brief   bool
		frost   bool
		full    bool
		metrics bool
	)

	cmd := &cobra.Command{
		Use:   "recap [today|yesterday|week]",
		Short: "AI-powered summary of your work — text, voice, or visual timeline",
		Long: `Generate a recap of your work activity from snapshot history.

  thaw recap              today's work summary
  thaw recap yesterday    yesterday's recap
  thaw recap week         weekly rollup
  thaw recap --voice      spoken summary (macOS say / Linux espeak)
  thaw recap --visual     open HTML timeline in browser
  thaw recap --brief      15-second flash briefing`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, _ := config.Load()
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			// Determine date range
			now := time.Now()
			var from, to time.Time
			period := "today"
			if len(args) > 0 {
				period = args[0]
			}

			switch period {
			case "today", "":
				from = time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
				to = now
			case "yesterday":
				y := now.AddDate(0, 0, -1)
				from = time.Date(y.Year(), y.Month(), y.Day(), 0, 0, 0, 0, y.Location())
				to = time.Date(y.Year(), y.Month(), y.Day(), 23, 59, 59, 0, y.Location())
			case "week":
				weekday := int(now.Weekday())
				if weekday == 0 {
					weekday = 7
				}
				monday := now.AddDate(0, 0, -(weekday - 1))
				from = time.Date(monday.Year(), monday.Month(), monday.Day(), 0, 0, 0, 0, monday.Location())
				to = now
			default:
				// Try parsing as date
				t, err := time.Parse("2006-01-02", period)
				if err != nil {
					return fmt.Errorf("unknown period: %s (use today, yesterday, week, or YYYY-MM-DD)", period)
				}
				from = t
				to = t.Add(24*time.Hour - time.Second)
			}

			r, err := recap.Generate(store, from, to)
			if err != nil {
				fmt.Println("No work activity found for that period.")
				return nil
			}

			// Frost briefing — full cinematic dashboard
			if frost {
				snap, err := store.Latest()
				if err != nil || snap == nil {
					return fmt.Errorf("no snapshot available for briefing")
				}
				path, err := briefing.Generate(snap, cfg)
				if err != nil {
					return err
				}
				fmt.Printf("Briefing: %s\n", path)
				return briefing.Open(path)
			}

			// Visual mode — open in browser
			if visual {
				path, err := recap.GenerateHTML(r)
				if err != nil {
					return err
				}
				fmt.Printf("Timeline saved to %s\n", path)
				return recap.OpenInBrowser("file://" + path)
			}

			// Voice mode
			if voice {
				var text string
				if brief {
					text = recap.FormatVoiceBrief(r)
				} else if full {
					text = recap.FormatVoiceFull(r)
				} else {
					// Default: brief first, then ask
					text = recap.FormatVoiceBrief(r)
					fmt.Println(recap.FormatText(r))
					fmt.Println("\nSpeaking brief summary...")
				}
				return recap.SpeakWithConfig(text, recap.VoiceConfig{
					Backend:     cfg.Voice.Backend,
					CortanaPath: cfg.Voice.CortanaPath,
					ElevenPath:  cfg.Voice.ElevenPath,
					KokoroMode:  cfg.Voice.KokoroMode,
				})
			}

			// Text mode (default)
			fmt.Print(recap.FormatText(r))

			// If brief requested, also print the voice version
			if brief {
				fmt.Println("\n" + recap.FormatVoiceBrief(r))
			}

			// Context switching metrics
			if metrics {
				m, err := context.Compute(store, from, to)
				if err == nil {
					fmt.Println()
					fmt.Print(context.FormatMetrics(m))
				}
			}

			// AI gap analysis — "what should I do next"
			if cfg.AI.GapAnalysis && cfg.AI.Provider != "none" {
				lc := newLLMClient(cfg)
				if lc.Available() {
					ctx := recap.FormatText(r)
					suggestion, err := lc.GapAnalysis(ctx)
					if err == nil && suggestion != "" {
						fmt.Println("\n  Next actions:")
						for _, line := range strings.Split(strings.TrimSpace(suggestion), "\n") {
							if line != "" {
								fmt.Printf("    → %s\n", strings.TrimSpace(line))
							}
						}
					}
				}
			}

			return nil
		},
	}

	cmd.Flags().BoolVar(&voice, "voice", false, "Speak the recap via TTS")
	cmd.Flags().BoolVar(&visual, "visual", false, "Open HTML timeline in browser")
	cmd.Flags().BoolVar(&frost, "briefing", false, "Open frost briefing dashboard")
	cmd.Flags().BoolVar(&brief, "brief", false, "15-second flash briefing")
	cmd.Flags().BoolVar(&full, "full", false, "Full detailed recap (with --voice)")
	cmd.Flags().BoolVar(&metrics, "metrics", false, "Show context-switching metrics")
	return cmd
}

// --- note ---

func noteCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "note <text>",
		Short: "Attach a note to the latest snapshot",
		Long:  "Record what you're thinking — shows up in recap, inspect, and restore context.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			note := strings.Join(args, " ")
			if err := store.AddNote(note); err != nil {
				return err
			}
			fmt.Printf("Note added: %s\n", note)
			return nil
		},
	}
}

// --- forget ---

func forgetCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "forget <from> <to>",
		Short: "Purge all snapshots and logs within a time range",
		Long: `Surgically remove all thaw data within a time window.

  thaw forget 14:00 15:30          today between 2pm-3:30pm
  thaw forget 2026-03-31T14:00 2026-03-31T15:30    explicit`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			from, err := parseFlexTime(args[0])
			if err != nil {
				return fmt.Errorf("invalid start time: %w", err)
			}
			to, err := parseFlexTime(args[1])
			if err != nil {
				return fmt.Errorf("invalid end time: %w", err)
			}

			if !yes {
				fmt.Printf("This will permanently delete all snapshots from %s to %s.\n",
					from.Format("2006-01-02 15:04"), to.Format("2006-01-02 15:04"))
				fmt.Print("Continue? [y/N]: ")
				var answer string
				fmt.Scanln(&answer)
				if answer != "y" && answer != "Y" {
					fmt.Println("Cancelled.")
					return nil
				}
			}

			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			deleted, err := audit.ForgetTimeRange(store, from, to)
			if err != nil {
				return err
			}

			// Also scrub command log
			scrubbed := scrubCommandLog(from, to)

			fmt.Printf("Forgotten: %d snapshot(s), %d log entries\n", deleted, scrubbed)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation")
	return cmd
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

// --- tag ---

func tagCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tag <name>",
		Short: "Tag the current terminal session for workspace filtering",
		Long:  "Mark this terminal as part of a named workstream. Use with `thaw recall --tag <name>`.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tag := args[0]
			// Write tag to a file keyed by PID of the parent shell
			home, _ := os.UserHomeDir()
			tagDir := filepath.Join(home, ".local", "state", "thaw", "tags")
			os.MkdirAll(tagDir, 0700)

			ppid := os.Getppid()
			tagFile := filepath.Join(tagDir, strconv.Itoa(ppid))

			// Read existing tags
			existing, _ := os.ReadFile(tagFile)
			tags := strings.TrimSpace(string(existing))
			if tags != "" && !strings.Contains(tags, tag) {
				tags += "," + tag
			} else if tags == "" {
				tags = tag
			}

			os.WriteFile(tagFile, []byte(tags), 0600)
			fmt.Printf("Tagged session %d as %q\n", ppid, tag)
			return nil
		},
	}
}

// --- audit ---

func auditCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "audit", Short: "Verify snapshot integrity"}

	cmd.AddCommand(&cobra.Command{
		Use:   "verify",
		Short: "Check hash chain integrity of all snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			result, err := audit.Verify(store)
			if err != nil {
				return err
			}

			fmt.Printf("Audit: %d total, %d verified, %d unsigned\n",
				result.Total, result.Verified, result.Unsigned)

			if result.IsIntact() {
				fmt.Println("Chain integrity: INTACT")
			} else {
				fmt.Printf("Chain integrity: BROKEN (%d broken links, %d tampered)\n",
					len(result.Broken), len(result.Tampered))
				for _, e := range result.Errors {
					fmt.Printf("  %s\n", e)
				}
			}
			return nil
		},
	})

	return cmd
}

// --- export ---

func dumpCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "dump <name-or-id>",
		Short: "Dump a snapshot as portable JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			// Try named first, then ID
			var snap *models.Snapshot
			snap, _ = store.GetNamed(args[0])
			if snap == nil {
				if id, err := strconv.Atoi(args[0]); err == nil {
					snap, _ = store.Get(id)
				}
			}
			if snap == nil {
				return fmt.Errorf("snapshot %q not found", args[0])
			}

			data, err := json.MarshalIndent(snap, "", "  ")
			if err != nil {
				return err
			}

			filename := args[0] + ".thaw.json"
			if err := os.WriteFile(filename, data, 0600); err != nil {
				return err
			}
			fmt.Printf("Exported to %s (%d sessions)\n", filename, len(snap.Sessions))
			return nil
		},
	}
}

// --- import ---

func importCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import <file.thaw.json>",
		Short: "Import a snapshot from portable JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}

			data, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("reading file: %w", err)
			}

			// Pre-validate: parse and check for dangerous commands before storing
			var preview models.Snapshot
			if err := json.Unmarshal(data, &preview); err != nil {
				return fmt.Errorf("invalid snapshot format: %w", err)
			}

			dangerousCount := 0
			for i, s := range preview.Sessions {
				if restore.IsDangerousCommand(s.Command) {
					fmt.Fprintf(os.Stderr, "  ⚠ session %d blocked: %s\n", i+1, s.Command)
					preview.Sessions[i].Command = "# BLOCKED: " + s.Command
					dangerousCount++
				}
			}
			if dangerousCount > 0 {
				fmt.Fprintf(os.Stderr, "Sanitized %d dangerous command(s) before import\n", dangerousCount)
				data, _ = json.Marshal(preview)
			}

			// Hostname check — warn if snapshot is from a different machine
			currentHost, _ := os.Hostname()
			if preview.Hostname != "" && preview.Hostname != currentHost {
				fmt.Printf("⚠ Snapshot is from host %q (current: %q)\n", preview.Hostname, currentHost)
				missing := 0
				for _, s := range preview.Sessions {
					if _, err := os.Stat(s.CWD); err != nil {
						missing++
					}
				}
				if missing > 0 {
					fmt.Printf("  %d of %d session CWDs don't exist on this machine\n", missing, len(preview.Sessions))
				}
			}

			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			snap, err := store.ImportSnapshot(data)
			if err != nil {
				return err
			}

			label := fmt.Sprintf("#%d", snap.ID)
			if snap.Name != "" {
				label = snap.Name + " (#" + strconv.Itoa(snap.ID) + ")"
			}
			fmt.Printf("Imported %s — %d sessions\n", label, len(snap.Sessions))
			return nil
		},
	}
}

// --- migrate ---

func migrateCmd() *cobra.Command {
	var oldHome, newHome string
	cmd := &cobra.Command{
		Use:   "migrate",
		Short: "Rewrite CWDs in all snapshots after a home directory change",
		Long: `Fix saved workspaces after moving to a new machine or renaming your user.

  thaw admin migrate --old-home /Users/oldname --new-home /Users/newname`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if oldHome == "" || newHome == "" {
				return fmt.Errorf("both --old-home and --new-home are required")
			}

			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			count, err := store.MigratePaths(oldHome, newHome)
			if err != nil {
				return err
			}
			fmt.Printf("Migrated %d snapshot(s): %s → %s\n", count, oldHome, newHome)
			return nil
		},
	}
	cmd.Flags().StringVar(&oldHome, "old-home", "", "Previous home directory path")
	cmd.Flags().StringVar(&newHome, "new-home", "", "New home directory path")
	return cmd
}

// --- uninstall ---

func uninstallCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove thaw shell hooks, daemon service, and data",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				fmt.Println("This will remove:")
				fmt.Println("  - Shell hooks from ~/.zshrc / ~/.bashrc")
				fmt.Println("  - Daemon service (launchd/systemd)")
				fmt.Println("  - All thaw data (~/.local/share/thaw, ~/.local/state/thaw)")
				fmt.Println("  - Configuration (~/.config/thaw)")
				fmt.Print("\nContinue? [y/N]: ")
				var answer string
				fmt.Scanln(&answer)
				if answer != "y" && answer != "Y" {
					fmt.Println("Cancelled.")
					return nil
				}
			}

			home, _ := os.UserHomeDir()
			var actions []string

			// Stop daemon
			if running, _ := daemon.IsRunning(); running {
				daemon.Stop()
				actions = append(actions, "Stopped daemon")
			}

			// Remove shell hooks from rc files
			for _, rc := range []string{
				filepath.Join(home, ".zshrc"),
				filepath.Join(home, ".bashrc"),
			} {
				if removed := removeThawHooks(rc); removed {
					actions = append(actions, "Removed hooks from "+filepath.Base(rc))
				}
			}

			// Remove systemd/launchd service
			systemdPath := filepath.Join(home, ".config", "systemd", "user", "thaw.service")
			if _, err := os.Stat(systemdPath); err == nil {
				os.Remove(systemdPath)
				actions = append(actions, "Removed systemd service")
			}
			launchdPath := filepath.Join(home, "Library", "LaunchAgents", "com.thaw.daemon.plist")
			if _, err := os.Stat(launchdPath); err == nil {
				exec.Command("launchctl", "unload", launchdPath).Run()
				os.Remove(launchdPath)
				actions = append(actions, "Removed launchd service")
			}

			// Remove data directories
			dirs := []string{
				filepath.Join(home, ".local", "share", "thaw"),
				filepath.Join(home, ".local", "state", "thaw"),
				filepath.Join(home, ".config", "thaw"),
			}
			for _, d := range dirs {
				if _, err := os.Stat(d); err == nil {
					os.RemoveAll(d)
					actions = append(actions, "Removed "+d)
				}
			}

			fmt.Println("thaw uninstalled:")
			for _, a := range actions {
				fmt.Printf("  ✓ %s\n", a)
			}
			if len(actions) == 0 {
				fmt.Println("  Nothing to remove — thaw wasn't fully installed.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation")
	return cmd
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

// --- undo ---

func undoCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "undo",
		Short: "Kill all tmux sessions created by the last restore",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				fmt.Println("This will kill all tmux sessions from the last restore.")
				fmt.Print("Continue? (use --yes to skip this prompt) [y/N]: ")
				var answer string
				fmt.Scanln(&answer)
				if answer != "y" && answer != "Y" && answer != "yes" {
					fmt.Println("Cancelled.")
					return nil
				}
			}
			killed, err := restore.Undo()
			if err != nil {
				return err
			}
			if killed == 0 {
				fmt.Println("No sessions to undo.")
			} else {
				fmt.Printf("Killed %d tmux session(s)\n", killed)
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation prompt")
	return cmd
}

// --- doctor ---

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check thaw installation and diagnose issues",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("thaw doctor — checking installation")
			fmt.Println()
			issues := 0

			// 1. tmux
			if _, err := exec.LookPath("tmux"); err != nil {
				fmt.Println("  ✗ tmux not found — install with: brew install tmux")
				issues++
			} else {
				out, _ := exec.Command("tmux", "-V").Output()
				fmt.Printf("  ✓ tmux installed (%s)\n", strings.TrimSpace(string(out)))
			}

			// 2. Config
			cfgPath, _ := config.ConfigPath()
			if _, err := os.Stat(cfgPath); err != nil {
				fmt.Printf("  ✗ config not found at %s — run: thaw setup\n", cfgPath)
				issues++
			} else {
				cfg, err := config.Load()
				if err != nil {
					fmt.Printf("  ✗ config parse error: %v\n", err)
					issues++
				} else {
					fmt.Printf("  ✓ config loaded from %s\n", cfgPath)
					warnings := config.Validate(cfg)
					for _, w := range warnings {
						fmt.Printf("    ⚠ %s\n", w)
					}
				}
			}

			// 3. Database
			dataDir, _ := config.DataDir()
			dbPath := filepath.Join(dataDir, "thaw.db")
			if _, err := os.Stat(dbPath); err != nil {
				fmt.Println("  ✗ database not found — run: thaw freeze")
				issues++
			} else {
				info, _ := os.Stat(dbPath)
				perm := info.Mode().Perm()
				fmt.Printf("  ✓ database exists (%s, permissions %04o)\n", humanSize(info.Size()), perm)
				if perm&0077 != 0 {
					fmt.Println("    ⚠ database is readable by other users — run: chmod 600 " + dbPath)
					issues++
				}

				store, err := snapshot.Open()
				if err == nil {
					defer store.Close()

					// Integrity check
					if intErr := store.IntegrityCheck(); intErr != nil {
						fmt.Printf("  ✗ database integrity check FAILED: %v\n", intErr)
						fmt.Println("    Run: thaw admin repair")
						issues++
					} else {
						fmt.Println("  ✓ database integrity check passed")
					}

					summaries, _ := store.List(1)
					if len(summaries) > 0 {
						fmt.Printf("  ✓ %d snapshot(s), latest: %s\n",
							summaries[0].ID, summaries[0].CreatedAt.Format("2006-01-02 15:04"))
					} else {
						fmt.Println("  ⚠ database exists but no snapshots — run: thaw freeze")
					}
				}
			}

			// 4. Shell hooks
			shell := os.Getenv("SHELL")
			if shell == "" {
				shell = "/bin/sh"
			}
			shellName := filepath.Base(shell)
			rcFile := shellRCPath(shellName)
			if rcFile != "" {
				data, err := os.ReadFile(rcFile)
				if err == nil && strings.Contains(string(data), "thaw") {
					fmt.Printf("  ✓ shell hooks installed in %s\n", rcFile)
				} else {
					fmt.Printf("  ✗ shell hooks not found in %s — run: thaw setup\n", rcFile)
					issues++
				}
			}

			// 5. Command log
			home, _ := os.UserHomeDir()
			logPath := filepath.Join(home, ".local", "state", "thaw", "commands.log")
			if info, err := os.Stat(logPath); err == nil {
				fmt.Printf("  ✓ command log exists (%s)\n", humanSize(info.Size()))
				perm := info.Mode().Perm()
				if perm&0077 != 0 {
					fmt.Println("    ⚠ command log readable by other users — run: chmod 600 " + logPath)
				}
			} else {
				fmt.Println("  ⚠ command log not found — shell hooks may not be active")
			}

			// 6. Daemon
			if running, pid := daemon.IsRunning(); running {
				fmt.Printf("  ✓ daemon running (PID %d)\n", pid)
				// Check heartbeat freshness
				age := daemon.HeartbeatAge()
				if age > 0 && age < 15*time.Minute {
					fmt.Printf("  ✓ daemon heartbeat %s ago\n", formatDurationShort(age))
				} else if age > 0 {
					fmt.Printf("  ⚠ daemon heartbeat stale (%s ago) — daemon may be hung\n", formatDurationShort(age))
				}
			} else {
				fmt.Println("  ⚠ daemon not running — run: thaw daemon start")
			}

			// 7. Disk space
			if avail := availableDiskMB(dataDir); avail >= 0 {
				if avail < 100 {
					fmt.Printf("  ⚠ low disk space: %d MB available — snapshots may fail\n", avail)
					issues++
				} else {
					fmt.Printf("  ✓ disk space: %d MB available\n", avail)
				}
			}

			// 8. Optional integrations
			fmt.Println("\nOptional integrations:")
			optionals := []struct{ name, bin string }{
				{"atuin", "atuin"}, {"direnv", "direnv"}, {"zoxide", "zoxide"},
			}
			for _, o := range optionals {
				if _, err := exec.LookPath(o.bin); err == nil {
					fmt.Printf("  ✓ %s found\n", o.name)
				} else {
					fmt.Printf("  · %s not installed (optional)\n", o.name)
				}
			}

			fmt.Println()
			if issues == 0 {
				fmt.Println("All checks passed. thaw is ready.")
			} else {
				fmt.Printf("%d issue(s) found. Run `thaw setup` to fix most of them.\n", issues)
			}
			return nil
		},
	}
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

// --- interactive restore ---

func doInteractiveRestore() error {
	if err := config.EnsureDirectories(); err != nil {
		return err
	}
	store, err := snapshot.Open()
	if err != nil {
		return err
	}
	defer store.Close()

	snap, err := store.Latest()
	if err != nil || snap == nil {
		fmt.Println("Nothing to thaw. Run `thaw freeze` first.")
		return nil
	}

	// Group sessions by project
	type projInfo struct {
		name     string
		branch   string
		sessions []models.Session
		alive    int
		idle     int
		dirs     []string
	}
	projects := make(map[string]*projInfo)
	for _, s := range snap.Sessions {
		key := s.GroupName
		if key == "" {
			key = filepath.Base(s.CWD)
		}
		p, ok := projects[key]
		if !ok {
			p = &projInfo{name: key}
			projects[key] = p
		}
		p.sessions = append(p.sessions, s)
		if s.Git != nil && s.Git.Branch != "" {
			p.branch = s.Git.Branch
			if s.Git.Dirty {
				p.branch += "*"
			}
		}
		if s.IsIdle() {
			p.idle++
		} else {
			p.alive++
		}
		// Track unique dirs
		found := false
		for _, d := range p.dirs {
			if d == s.CWD {
				found = true
				break
			}
		}
		if !found {
			p.dirs = append(p.dirs, s.CWD)
		}
	}

	// Sort projects by session count
	type projEntry struct {
		key string
		info *projInfo
	}
	var sorted []projEntry
	for k, v := range projects {
		sorted = append(sorted, projEntry{k, v})
	}
	for i := 0; i < len(sorted); i++ {
		for j := i + 1; j < len(sorted); j++ {
			if len(sorted[j].info.sessions) > len(sorted[i].info.sessions) {
				sorted[i], sorted[j] = sorted[j], sorted[i]
			}
		}
	}

	// Display
	ago := time.Since(snap.CreatedAt)
	agoStr := formatDurationShort(ago)
	fmt.Printf("\nLast snapshot: %s ago (%s)\n\n", agoStr, snap.CreatedAt.Format("Mon 3:04 PM"))

	fmt.Println("  Projects to restore:")
	for i, pe := range sorted {
		p := pe.info
		branch := ""
		if p.branch != "" {
			branch = " [" + p.branch + "]"
		}
		status := fmt.Sprintf("%d active", p.alive)
		if p.idle > 0 {
			status += fmt.Sprintf(", %d idle", p.idle)
		}

		// Check for dep staleness
		staleWarning := ""
		for _, s := range p.sessions {
			rots := deprot.Check(s)
			if len(rots) > 0 {
				staleWarning = " ⚠ deps may be stale"
				break
			}
		}

		fmt.Printf("  %d) %s%s — %d session(s) (%s)%s\n", i+1, p.name, branch, len(p.sessions), status, staleWarning)

		// Check upstream changes
		for _, dir := range p.dirs {
			report, err := upstream.Check(dir, snap.CreatedAt)
			if err == nil && report.HasChanges() {
				if report.BehindBy > 0 {
					fmt.Printf("       ↳ %d new upstream commit(s) — pull needed\n", report.BehindBy)
				}
				if report.CIStatus == "failure" {
					fmt.Printf("       ↳ CI failed on %s\n", report.Branch)
				}
				if report.ForcePushed {
					fmt.Printf("       ↳ upstream was force-pushed\n")
				}
				break
			}
		}
	}

	fmt.Printf("\n  a) Restore all\n")
	fmt.Printf("  q) Quit\n\n")
	fmt.Print("  Choice: ")

	var choice string
	fmt.Scanln(&choice)

	if choice == "q" || choice == "" {
		return nil
	}

	if choice == "a" {
		return doRestore(models.RestoreOptions{}, "", false)
	}

	// Parse selection (single number or comma-separated)
	var selected []int
	for _, part := range strings.Split(choice, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > len(sorted) {
			fmt.Printf("Invalid choice: %s\n", part)
			return nil
		}
		selected = append(selected, n-1)
	}

	// Filter snapshot to selected projects
	var filtered []models.Session
	for _, idx := range selected {
		filtered = append(filtered, sorted[idx].info.sessions...)
	}
	snap.Sessions = filtered

	// Save as temp and restore
	snap.Name = ""
	snap.Source = "interactive"
	store.Save(snap)
	return doRestore(models.RestoreOptions{}, strconv.Itoa(snap.ID), false)
}

// --- progress ---

func progressCmd() *cobra.Command {
	var runTests bool
	cmd := &cobra.Command{
		Use:   "progress [dir]",
		Short: "Analyze project progress — git velocity, tests, TODOs, dependency health",
		Long: `Show progress signals for a project directory.

  thaw progress              analyze current directory
  thaw progress ~/project    analyze specific directory
  thaw progress --test       also run tests`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			absDir, err := filepath.Abs(dir)
			if err != nil {
				return err
			}

			// Load project config if present
			pcfg, _ := project.Load(absDir)
			if !runTests && pcfg != nil {
				pcfg.Project.TestCommand = "" // skip tests unless --test
			}

			report, err := progress.Analyze(absDir, pcfg)
			if err != nil {
				return err
			}

			fmt.Print(progress.FormatReport(report))
			return nil
		},
	}
	cmd.Flags().BoolVar(&runTests, "test", false, "Run test suite as part of analysis")
	return cmd
}

// --- context ---

func contextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "context [dir]",
		Short: "Show what you were doing last time + what changed upstream",
		Long: `Display the last known session state for a project directory,
including upstream changes (new commits, CI status, dep changes).

  thaw context              current directory
  thaw context ~/project    specific directory`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			absDir, err := filepath.Abs(dir)
			if err != nil {
				return err
			}

			mem, err := memory.Open()
			if err != nil {
				return err
			}
			defer mem.Close()

			entry, err := mem.Recall(absDir)
			if err != nil {
				pcfg, _ := project.Load(absDir)
				if pcfg != nil {
					fmt.Printf("thaw: project %s (no previous sessions)\n", pcfg.Project.Name)
				}
				return nil
			}

			fmt.Println(memory.FormatContext(entry))

			// Check upstream changes since last session
			if entry != nil && !entry.LastSeen.IsZero() {
				report, err := upstream.Check(absDir, entry.LastSeen)
				if err == nil && report.HasChanges() {
					fmt.Print(upstream.Format(report))
				}
			}
			return nil
		},
	}
}

// --- export data ---

func exportDataCmd() *cobra.Command {
	var format string
	var rangeDays int
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export session data as CSV or JSON for analytics",
		Long: `Dump structured session data for time tracking, billing, or analysis.

  thaw export                     last 30 days as CSV
  thaw export --format=json       as JSON
  thaw export --range=7           last 7 days
  thaw export --range=90 > q3.csv pipe to file`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			to := time.Now()
			from := to.AddDate(0, 0, -rangeDays)
			snaps, err := store.GetRange(from, to)
			if err != nil {
				return err
			}
			if len(snaps) == 0 {
				fmt.Fprintf(os.Stderr, "No snapshots in the last %d days.\n", rangeDays)
				return nil
			}

			records := export.Flatten(snaps)
			switch format {
			case "json":
				return export.WriteJSON(os.Stdout, records)
			default:
				return export.WriteCSV(os.Stdout, records)
			}
		},
	}
	cmd.Flags().StringVar(&format, "format", "csv", "Output format: csv or json")
	cmd.Flags().IntVar(&rangeDays, "range", 30, "Number of days to export")
	return cmd
}

// --- dashboard ---

func dashboardCmd() *cobra.Command {
	var rangeDays int
	var open bool
	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Generate an HTML analytics dashboard",
		Long: `Create a visual report of your work patterns — time per project,
active hours, session counts. Opens in your browser.

  thaw dashboard                 last 30 days
  thaw dashboard --range=7       last 7 days
  thaw dashboard --no-open       write to stdout instead`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			to := time.Now()
			from := to.AddDate(0, 0, -rangeDays)
			snaps, err := store.GetRange(from, to)
			if err != nil {
				return err
			}
			if len(snaps) == 0 {
				fmt.Fprintf(os.Stderr, "No snapshots in the last %d days.\n", rangeDays)
				return nil
			}

			records := export.Flatten(snaps)
			html := dashboard.Generate(records, rangeDays)

			if !open {
				fmt.Print(html)
				return nil
			}

			// Write to temp file and open in browser
			tmpDir := os.TempDir()
			path := filepath.Join(tmpDir, "thaw-dashboard.html")
			if err := os.WriteFile(path, []byte(html), 0644); err != nil {
				return err
			}
			fmt.Printf("Dashboard: %s\n", path)
			exec.Command("open", path).Start()
			return nil
		},
	}
	cmd.Flags().IntVar(&rangeDays, "range", 30, "Number of days to include")
	cmd.Flags().BoolVar(&open, "open", true, "Open in browser (use --open=false for stdout)")
	return cmd
}

// --- init ---

func initProjectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Generate .thaw.toml for the current project",
		Long: `Detects the project type and creates a .thaw.toml with smart defaults.

  thaw init    creates .thaw.toml in the current directory`,
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, _ := os.Getwd()

			// Check if .thaw.toml already exists
			if _, err := os.Stat(filepath.Join(dir, ".thaw.toml")); err == nil {
				fmt.Println(".thaw.toml already exists in this directory.")
				return nil
			}

			ptype := project.DetectProjectType(dir)
			name := filepath.Base(dir)

			var restoreCmds []string
			var testCmd string
			var envVars string
			var healthCheck string
			var buildCmd string

			switch ptype {
			case "node":
				restoreCmds = []string{"npm run dev"}
				testCmd = "npm test"
				envVars = "{ NODE_ENV = \"development\" }"
				healthCheck = "curl -sf http://localhost:3000/api/health"
				buildCmd = "npm run build"
			case "go":
				restoreCmds = []string{"go run ."}
				testCmd = "go test ./... -count=1"
				buildCmd = "go build ./..."
			case "python":
				restoreCmds = []string{"python manage.py runserver"}
				testCmd = "python -m pytest"
			case "rust":
				restoreCmds = []string{"cargo run"}
				testCmd = "cargo test"
				buildCmd = "cargo build"
			case "ruby":
				restoreCmds = []string{"bundle exec rails server"}
				testCmd = "bundle exec rspec"
			case "docker":
				restoreCmds = []string{"docker compose up -d"}
			default:
				restoreCmds = []string{"# add your dev server command"}
				testCmd = "# add your test command"
			}

			// Build TOML content
			var b strings.Builder
			fmt.Fprintf(&b, "# thaw project config — %s project\n", ptype)
			fmt.Fprintf(&b, "# Docs: https://github.com/joecattt/thaw#project-config\n\n")
			fmt.Fprintf(&b, "[project]\n")
			fmt.Fprintf(&b, "name = %q\n", name)

			fmt.Fprintf(&b, "restore_commands = [")
			for i, c := range restoreCmds {
				if i > 0 {
					fmt.Fprintf(&b, ", ")
				}
				fmt.Fprintf(&b, "%q", c)
			}
			fmt.Fprintf(&b, "]\n")

			if envVars != "" {
				fmt.Fprintf(&b, "env = %s\n", envVars)
			}
			if testCmd != "" {
				fmt.Fprintf(&b, "test_command = %q\n", testCmd)
			}
			if healthCheck != "" {
				fmt.Fprintf(&b, "health_check = %q\n", healthCheck)
			}
			if buildCmd != "" {
				fmt.Fprintf(&b, "build_command = %q\n", buildCmd)
			}
			fmt.Fprintf(&b, "todo_pattern = \"TODO|FIXME|HACK|XXX\"\n")

			path := filepath.Join(dir, ".thaw.toml")
			if err := os.WriteFile(path, []byte(b.String()), 0644); err != nil {
				return fmt.Errorf("writing .thaw.toml: %w", err)
			}

			fmt.Printf("Created .thaw.toml (%s project)\n", ptype)
			fmt.Printf("  name: %s\n", name)
			fmt.Printf("  restore: %v\n", restoreCmds)
			if testCmd != "" {
				fmt.Printf("  test: %s\n", testCmd)
			}
			fmt.Printf("\nEdit %s to customize.\n", path)
			return nil
		},
	}
}

// --- status ---

func statusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show active terminal sessions",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			snap, err := newEngine(cfg).Capture("status")
			if err != nil {
				return err
			}
			if len(snap.Sessions) == 0 {
				fmt.Println("No active terminal sessions.")
				return nil
			}
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(snap.Sessions)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "PID\tSTATUS\tLABEL\tGROUP\tBRANCH\tCWD\tCOMMAND\n")
			for _, s := range snap.Sessions {
				branch := ""
				if s.Git != nil {
					branch = s.Git.Branch
					if s.Git.Dirty {
						branch += "*"
					}
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
					s.PID, s.Status, s.Label, s.GroupName,
					branch, truncateLeft(s.CWD, 25), truncate(s.Command, 30))
			}
			w.Flush()

			groups := snap.WorkstreamGroups()
			realGroups := 0
			for k := range groups {
				if k != "misc" && len(groups[k]) >= 2 {
					realGroups++
				}
			}
			fmt.Printf("\n%d session(s)", len(snap.Sessions))
			if realGroups > 0 {
				fmt.Printf(", %d workstream(s)", realGroups)
			}
			fmt.Println()
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

// --- inspect ---

func inspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect [id-or-name]",
		Short: "Deep-inspect a snapshot with staleness checks",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			var snap *models.Snapshot
			if len(args) > 0 {
				snap, err = store.GetNamed(args[0])
				if err != nil {
					return err
				}
				if snap == nil {
					if id, e := strconv.Atoi(args[0]); e == nil {
						snap, err = store.Get(id)
						if err != nil {
							return err
						}
					}
				}
			} else {
				snap, err = store.Latest()
				if err != nil {
					return err
				}
			}
			if snap == nil {
				fmt.Println("No snapshot found.")
				return nil
			}

			checks := stale.CheckAll(snap)
			label := fmt.Sprintf("#%d", snap.ID)
			if snap.Name != "" {
				label = fmt.Sprintf("%q (#%d)", snap.Name, snap.ID)
			}

			fmt.Printf("Snapshot %s\n", label)
			fmt.Printf("  Captured: %s  Source: %s  Host: %s\n\n",
				snap.CreatedAt.Format("2006-01-02 15:04:05"), snap.Source, snap.Hostname)

			for i, s := range snap.Sessions {
				sc := checks[s.PID]
				icon := "✓"
				if sc.IsStale() {
					icon = "✗"
				}
				fmt.Printf("[%d] %s %s — %s\n", i+1, icon, s.Label, s.Status)

				if s.Intent != "" {
					fmt.Printf("    Intent: %s\n", s.Intent)
				}

				fmt.Printf("    CWD:  %s", s.CWD)
				if !sc.CWDExists {
					fmt.Print("  ← MISSING")
				}
				fmt.Println()

				if !s.IsIdle() {
					fmt.Printf("    Cmd:  %s", s.Command)
					if !sc.BinaryExists {
						fmt.Print("  ← NOT FOUND")
					}
					fmt.Println()
				}

				if s.Git != nil {
					fmt.Printf("    Git:  %s @ %s", s.Git.Branch, s.Git.Commit)
					if s.Git.Dirty {
						fmt.Print(" (dirty)")
					}
					if !sc.GitBranchMatch {
						fmt.Print("  ← CHANGED")
					}
					fmt.Println()
				}

				if s.ProjectType != "" {
					fmt.Printf("    Project: %s\n", s.ProjectType)
				}

				if s.HasDirenv {
					fmt.Printf("    Direnv: .envrc detected (env managed by direnv)\n")
				} else if !s.EnvDelta.IsEmpty() {
					fmt.Printf("    Env:  %d var(s):", len(s.EnvDelta.Set))
					for k := range s.EnvDelta.Set {
						fmt.Printf(" %s", k)
					}
					fmt.Println()
				}

				if s.GroupName != "" {
					fmt.Printf("    Group: %s\n", s.GroupName)
				}

				if s.RestoreOrder > 0 {
					fmt.Printf("    Order: %d\n", s.RestoreOrder)
				}

				if len(s.Output) > 0 {
					fmt.Printf("    Output: %d lines captured\n", len(s.Output))
				}

				if len(s.History) > 0 {
					n := 5
					if len(s.History) < n {
						n = len(s.History)
					}
					fmt.Printf("    History (%d total):\n", len(s.History))
					for _, h := range s.History[len(s.History)-n:] {
						fmt.Printf("      $ %s\n", h)
					}
				}
				fmt.Println()
			}
			return nil
		},
	}
}

// --- history ---

func historyCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List snapshots and saved workspaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			// Named workspaces
			named, _ := store.ListNamed()
			if len(named) > 0 {
				fmt.Println("Saved workspaces:")
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				for _, s := range named {
					fmt.Fprintf(w, "  %s\t%d sessions\t%s\n",
						s.Name, s.SessionCount, s.CreatedAt.Format("2006-01-02 15:04"))
				}
				w.Flush()
				fmt.Println()
			}

			// Recent auto snapshots
			summaries, err := store.List(limit)
			if err != nil {
				return err
			}
			if len(summaries) == 0 && len(named) == 0 {
				fmt.Println("No snapshots yet. Run `thaw freeze` to capture your first.")
				return nil
			}

			if len(summaries) > 0 {
				fmt.Println("Recent snapshots:")
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintf(w, "  ID\tSESSIONS\tSOURCE\tCREATED\n")
				for _, s := range summaries {
					name := ""
					if s.Name != "" {
						name = " [" + s.Name + "]"
					}
					fmt.Fprintf(w, "  #%d%s\t%d\t%s\t%s\n",
						s.ID, name, s.SessionCount, s.Source,
						s.CreatedAt.Format("2006-01-02 15:04"))
				}
				w.Flush()
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "Number of snapshots to show")
	return cmd
}

// --- prune ---

func pruneCmd() *cobra.Command {
	var (
		keepDays int
		keepMin  int
	)
	cmd := &cobra.Command{
		Use:   "prune",
		Short: "Remove old auto-snapshots (named workspaces are preserved)",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()
			pruned, err := store.Prune(time.Duration(keepDays)*24*time.Hour, keepMin)
			if err != nil {
				return err
			}
			if pruned == 0 {
				fmt.Println("Nothing to prune.")
			} else {
				fmt.Printf("Pruned %d old snapshot(s)\n", pruned)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&keepDays, "days", 7, "Remove snapshots older than N days")
	cmd.Flags().IntVar(&keepMin, "keep", 10, "Always keep at least N snapshots")
	return cmd
}

// --- setup ---

func setupCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "setup",
		Short: "One-command install — configure shell hooks and defaults",
		RunE: func(cmd *cobra.Command, args []string) error {
			actions, err := setup.Run()
			if err != nil {
				return err
			}
			fmt.Println("thaw setup complete:")
			for _, a := range actions {
				fmt.Printf("  ✓ %s\n", a)
			}
			fmt.Println("\nRestart your shell or run: source ~/.zshrc")
			fmt.Println("Then just use your terminal normally. thaw handles the rest.")
			return nil
		},
	}
}

// --- config ---

func configCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Show or manage configuration",
	}
	cmd.AddCommand(&cobra.Command{
		Use: "show", Short: "Print current config",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(cfg)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "path", Short: "Print config file path",
		Run: func(cmd *cobra.Command, args []string) {
			p, _ := config.ConfigPath()
			fmt.Println(p)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config value",
		Long: `Set a configuration value. Keys use dot notation.

  thaw config set ai.provider claude
  thaw config set ai.model claude-sonnet-4-20250514
  thaw config set briefing.theme frost
  thaw config set briefing.priority_order blocked
  thaw config set capture.idle_threshold_min 30
  thaw config set daemon.interval_min 10
  thaw config set telemetry.enabled true
  thaw config set voice.tts_backend say`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			key, val := args[0], args[1]
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := config.SetField(&cfg, key, val); err != nil {
				return err
			}
			if err := config.Save(cfg); err != nil {
				return err
			}
			// Handle telemetry opt-in/out
			if key == "telemetry.enabled" {
				if val == "true" {
					telemetry.OptIn()
					if cfg.Telemetry.FirebaseURL != "" {
						telemetry.FirebaseURL = cfg.Telemetry.FirebaseURL
					}
					fmt.Println("Telemetry enabled (anonymous, opt-in)")
				} else {
					telemetry.OptOut()
					fmt.Println("Telemetry disabled")
				}
				return nil
			}
			fmt.Printf("Set %s = %s\n", key, val)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "reset", Short: "Reset config to defaults",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.DefaultConfig()
			if err := config.Save(cfg); err != nil {
				return err
			}
			fmt.Println("Config reset to defaults.")
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "validate", Short: "Check config for errors",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				fmt.Printf("Config error: %s\n", err)
				return nil
			}
			warnings := config.Validate(cfg)
			if len(warnings) == 0 {
				fmt.Println("Config is valid.")
			} else {
				for _, w := range warnings {
					fmt.Printf("  ⚠ %s\n", w)
				}
			}
			return nil
		},
	})
	return cmd
}

func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "daemon", Short: "Manage background snapshot daemon"}

	cmd.AddCommand(&cobra.Command{
		Use: "start", Short: "Start background snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			if running, pid := daemon.IsRunning(); running {
				fmt.Printf("Already running (PID %d)\n", pid)
				return nil
			}
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			interval := time.Duration(cfg.Daemon.IntervalMin) * time.Minute
			if interval < time.Minute {
				interval = 5 * time.Minute
			}
			fmt.Printf("Starting daemon (every %s)...\n", interval)
			return daemon.Run(newEngine(cfg), interval)
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "stop", Short: "Stop daemon",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := daemon.Stop(); err != nil {
				return err
			}
			fmt.Println("Stopped.")
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: "status", Short: "Check daemon status",
		Run: func(cmd *cobra.Command, args []string) {
			if running, pid := daemon.IsRunning(); running {
				fmt.Printf("Running (PID %d)\n", pid)
			} else {
				fmt.Println("Not running.")
			}
		},
	})
	return cmd
}

// --- hidden: log-cmd (shell hook calls this) ---

func logCmdCmd() *cobra.Command {
	return &cobra.Command{
		Use: "log-cmd", Hidden: true, Args: cobra.ExactArgs(3),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := strconv.Atoi(args[0])
			if err != nil {
				return err
			}
			return history.LogCommand(pid, args[1], args[2])
		},
	}
}

// --- shell-init ---

func shellInitCmd() *cobra.Command {
	return &cobra.Command{
		Use: "shell-init [zsh|bash]", Short: "Print shell hooks for eval",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "zsh":
				os.Stdout.WriteString(zshInit)
			case "bash":
				os.Stdout.WriteString(bashInit)
			default:
				return fmt.Errorf("unsupported: %s (use zsh or bash)", args[0])
			}
			return nil
		},
	}
}

const zshInit = `# thaw — terminal workspace memory
_thaw_log="${XDG_STATE_HOME:-$HOME/.local/state}/thaw"
[[ -d "$_thaw_log" ]] || mkdir -p "$_thaw_log" 2>/dev/null
zshexit() { command -v thaw &>/dev/null && thaw freeze --source=shutdown 2>/dev/null &! }
_thaw_preexec() {
  # Replace newlines with spaces (heredocs, continuations) then append
  local cmd="${1//$'\n'/ }"
  echo "$(date +%s)|$$|$PWD|$cmd" >> "$_thaw_log/commands.log" 2>/dev/null
  # Idle gap detection — if >30min since last command, show context
  if [[ -f "$_thaw_log/.last_cmd_time" ]]; then
    local last_t=$(cat "$_thaw_log/.last_cmd_time" 2>/dev/null)
    local now_t=$(date +%s)
    if [[ -n "$last_t" ]] && (( now_t - last_t > 1800 )); then
      local gap_min=$(( (now_t - last_t) / 60 ))
      echo "thaw: ${gap_min}m idle gap detected"
      if [[ -d "$PWD/.git" ]] && command -v thaw &>/dev/null; then
        thaw context "$PWD" 2>/dev/null
      fi
    fi
  fi
  echo "$(date +%s)" > "$_thaw_log/.last_cmd_time" 2>/dev/null
}
_thaw_chpwd() {
  echo "$(date +%s)|$$|$PWD|cd $PWD" >> "$_thaw_log/commands.log" 2>/dev/null
  # Show context when entering a tracked project dir
  if [[ -d "$PWD/.git" ]] && command -v thaw &>/dev/null; then
    thaw context "$PWD" 2>/dev/null
  fi
}
# Autostash: when leaving a dirty git repo, auto-stash uncommitted work
_thaw_autostash_dir=""
_thaw_pre_chpwd() {
  if [[ -n "$_thaw_autostash_dir" ]] && [[ "$_thaw_autostash_dir" != "$PWD" ]]; then
    if [[ -d "$_thaw_autostash_dir/.git" ]]; then
      local dirty=$(git -C "$_thaw_autostash_dir" status --porcelain 2>/dev/null)
      if [[ -n "$dirty" ]]; then
        git -C "$_thaw_autostash_dir" stash push -m "thaw-auto-$(date +%s)" -q 2>/dev/null
        echo "thaw: auto-stashed changes in $(basename $_thaw_autostash_dir)"
      fi
    fi
  fi
  _thaw_autostash_dir="$PWD"
}
# One-time heartbeat check per shell session
if [[ -f "$_thaw_log/daemon.heartbeat" ]]; then
  _thaw_hb_age=$(( $(date +%s) - $(cat "$_thaw_log/daemon.heartbeat" 2>/dev/null || echo 0) ))
  if (( _thaw_hb_age > 900 )); then
    echo "thaw: daemon may be stopped (last heartbeat $(( _thaw_hb_age / 60 ))m ago) — run: thaw daemon start"
  fi
  unset _thaw_hb_age
fi
autoload -U add-zsh-hook
add-zsh-hook preexec _thaw_preexec
add-zsh-hook chpwd _thaw_chpwd
add-zsh-hook chpwd _thaw_pre_chpwd
# Morning briefing — once per day, first terminal only (opt-in via config)
_thaw_brief="/tmp/.thaw-briefed-$(date +%Y%m%d)"
if [[ ! -f "$_thaw_brief" ]] && command -v thaw &>/dev/null; then
  if thaw config show 2>/dev/null | grep -q 'morning_briefing = true'; then
    touch "$_thaw_brief"
    thaw recap --briefing &>/dev/null &!
  fi
fi
unset _thaw_brief
`

const bashInit = `# thaw — terminal workspace memory
_thaw_log="${XDG_STATE_HOME:-$HOME/.local/state}/thaw"
[[ -d "$_thaw_log" ]] || mkdir -p "$_thaw_log" 2>/dev/null
trap 'command -v thaw &>/dev/null && thaw freeze --source=shutdown 2>/dev/null &' EXIT
_thaw_last_cmd=""
_thaw_prompt() {
  local cmd="$(HISTTIMEFORMAT= history 1 | sed 's/^[ ]*[0-9]*[ ]*//')"
  if [ -n "$cmd" ] && [ "$cmd" != "$_thaw_last_cmd" ]; then
    _thaw_last_cmd="$cmd"
    cmd="${cmd//$'\n'/ }"
    echo "$(date +%s)|$$|$PWD|$cmd" >> "$_thaw_log/commands.log" 2>/dev/null
  fi
}
# One-time heartbeat check
if [ -f "$_thaw_log/daemon.heartbeat" ]; then
  _thaw_hb_age=$(( $(date +%s) - $(cat "$_thaw_log/daemon.heartbeat" 2>/dev/null || echo 0) ))
  if [ "$_thaw_hb_age" -gt 900 ] 2>/dev/null; then
    echo "thaw: daemon may be stopped (last heartbeat $(( _thaw_hb_age / 60 ))m ago) — run: thaw daemon start"
  fi
  unset _thaw_hb_age
fi
PROMPT_COMMAND="_thaw_prompt;${PROMPT_COMMAND}"
`

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

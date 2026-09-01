package main

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/buildinfo"
	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/telemetry"
)

// version is the single build version, shared with recap/briefing via internal/buildinfo.
// Injected at release time via goreleaser ldflags (-X .../buildinfo.Version=...).
var version = buildinfo.Version

func main() {
	var (
		noGreeting bool
		noTmux     bool
	)
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
			return doInteractiveRestore(noGreeting, noTmux)
		},
	}
	root.Flags().BoolVar(&noGreeting, "no-greeting", false, "Skip the where-was-I summary")
	root.Flags().BoolVar(&noTmux, "no-tmux", false, "Degraded restore without tmux — print context and a resume script")

	// Command groups — shown in this order in --help
	root.AddGroup(
		&cobra.Group{ID: "everyday", Title: "Everyday:"},
		&cobra.Group{ID: "review", Title: "Review:"},
		&cobra.Group{ID: "setup", Title: "Setup:"},
		&cobra.Group{ID: "advanced", Title: "Advanced:"},
	)
	root.SetHelpCommandGroupID("advanced")
	root.SetCompletionCommandGroupID("advanced")

	grouped := func(id string, cmds ...*cobra.Command) {
		for _, c := range cmds {
			c.GroupID = id
			root.AddCommand(c)
		}
	}
	grouped("everyday", freezeCmd(), restoreCmd(), saveCmd(), recallCmd(), undoCmd(), statusCmd(), runCmd(), faceCmd(), spaceCmd(), junkCmd())
	grouped("review",
		recapCmd(), contextCmd(), inspectCmd(), historyCmd(), diffCmd(),
		progressCmd(), dashboardCmd(), exportDataCmd(), tuiCmd(), screensaverCmd(), ledgerCmd())
	grouped("setup",
		setupCmd(), initProjectCmd(), doctorCmd(), shellInitCmd(), daemonCmd(), configCmd(), allowCmd())
	root.AddCommand(logCmdCmd())      // hidden — shell hooks call this
	root.AddCommand(labelCmd())       // hidden — shell hooks call this
	root.AddCommand(sessionNoteCmd()) // hidden — shell hooks call this

	// Advanced commands under `thaw admin`
	admin := &cobra.Command{
		Use:     "admin",
		GroupID: "advanced",
		Short:   "Rarely-needed maintenance — notes, tags, purge, integrity checks",
	}
	admin.AddCommand(
		noteCmd(),
		forgetCmd(),
		purgeSecretsCmd(),
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

package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/daemon"
	"github.com/joecattt/thaw/internal/memory"
	"github.com/joecattt/thaw/internal/snapshot"
)

func freezeCmd() *cobra.Command {
	var source string
	cmd := &cobra.Command{
		Use:   "freeze",
		Short: "Save a snapshot of your terminal sessions right now",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			// N shell-exit hooks firing `thaw freeze &` at once stack expensive
			// process scans. Skip if one just completed; serialize the rest
			// behind the freeze lock (shared with the daemon's capture path).
			if daemon.FreezeDoneWithin(20 * time.Second) {
				fmt.Println("Skipped — a freeze completed seconds ago.")
				return nil
			}
			release, ok := daemon.AcquireFreezeLock()
			if !ok {
				fmt.Println("Skipped — another freeze is already in progress.")
				return nil
			}
			defer release()
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			snap, err := newEngine(cfg).Capture(source)
			if err != nil {
				return fmt.Errorf("capture failed: %w", err)
			}
			if len(snap.Sessions) == 0 {
				fmt.Println("No terminal sessions found to freeze — thaw may not see your shell. Run `thaw doctor` to check.")
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
			daemon.MarkFreezeDone()

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
			if _, err := store.Prune(time.Duration(cfg.Daemon.KeepDays)*24*time.Hour, cfg.Daemon.KeepMax); err != nil {
				fmt.Fprintf(os.Stderr, "warning: prune failed: %v\n", err)
			}

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

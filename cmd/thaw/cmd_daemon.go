package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/daemon"
)

func daemonCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "daemon", Short: "Manage automatic background snapshots"}

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
			return daemon.Run(newEngine(cfg), interval, cfg)
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

package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/diff"
	"github.com/joecattt/thaw/internal/snapshot"
)

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

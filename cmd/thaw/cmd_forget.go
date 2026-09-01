package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/audit"
	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/snapshot"
)

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

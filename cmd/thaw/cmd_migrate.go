package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/snapshot"
)

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

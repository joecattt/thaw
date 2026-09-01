package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/snapshot"
)

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

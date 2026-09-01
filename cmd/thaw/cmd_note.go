package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/snapshot"
)

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

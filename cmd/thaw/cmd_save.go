package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/snapshot"
)

func saveCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "save <name>",
		Short: "Save your current sessions as a named workspace",
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
				fmt.Println("No terminal sessions found to save — thaw may not see your shell. Run `thaw doctor` to check.")
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

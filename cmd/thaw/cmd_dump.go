package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/pkg/models"
)

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
				return fmt.Errorf("snapshot %q not found — run `thaw history` to list snapshot IDs and names", args[0])
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

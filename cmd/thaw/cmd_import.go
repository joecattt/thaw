package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/restore"
	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/pkg/models"
)

func importCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "import <file.thaw.json>",
		Short: "Import a snapshot from portable JSON",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}

			data, err := os.ReadFile(args[0])
			if err != nil {
				return fmt.Errorf("reading file: %w", err)
			}

			// Pre-validate: parse and check for dangerous commands before storing
			var preview models.Snapshot
			if err := json.Unmarshal(data, &preview); err != nil {
				return fmt.Errorf("invalid snapshot format: %w", err)
			}

			dangerousCount := 0
			for i, s := range preview.Sessions {
				if restore.IsDangerousCommand(s.Command) {
					fmt.Fprintf(os.Stderr, "  ⚠ session %d blocked: %s\n", i+1, s.Command)
					preview.Sessions[i].Command = "# BLOCKED: " + s.Command
					dangerousCount++
				}
			}
			if dangerousCount > 0 {
				fmt.Fprintf(os.Stderr, "Sanitized %d dangerous command(s) before import\n", dangerousCount)
				data, _ = json.Marshal(preview)
			}

			// Hostname check — warn if snapshot is from a different machine
			currentHost, _ := os.Hostname()
			if preview.Hostname != "" && preview.Hostname != currentHost {
				fmt.Printf("⚠ Snapshot is from host %q (current: %q)\n", preview.Hostname, currentHost)
				missing := 0
				for _, s := range preview.Sessions {
					if _, err := os.Stat(s.CWD); err != nil {
						missing++
					}
				}
				if missing > 0 {
					fmt.Printf("  %d of %d session CWDs don't exist on this machine\n", missing, len(preview.Sessions))
				}
			}

			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			snap, err := store.ImportSnapshot(data)
			if err != nil {
				return err
			}

			label := fmt.Sprintf("#%d", snap.ID)
			if snap.Name != "" {
				label = snap.Name + " (#" + strconv.Itoa(snap.ID) + ")"
			}
			fmt.Printf("Imported %s — %d sessions\n", label, len(snap.Sessions))
			return nil
		},
	}
}

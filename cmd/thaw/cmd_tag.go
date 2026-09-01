package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
)

func tagCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "tag <name>",
		Short: "Tag the current terminal session for workspace filtering",
		Long:  "Mark this terminal as part of a named workstream. Use with `thaw recall --tag <name>`.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			tag := args[0]
			// Write tag to a file keyed by PID of the parent shell
			home, _ := os.UserHomeDir()
			tagDir := filepath.Join(home, ".local", "state", "thaw", "tags")
			os.MkdirAll(tagDir, 0700)

			ppid := os.Getppid()
			tagFile := filepath.Join(tagDir, strconv.Itoa(ppid))

			// Read existing tags
			existing, _ := os.ReadFile(tagFile)
			tags := strings.TrimSpace(string(existing))
			if tags != "" && !strings.Contains(tags, tag) {
				tags += "," + tag
			} else if tags == "" {
				tags = tag
			}

			os.WriteFile(tagFile, []byte(tags), 0600)
			fmt.Printf("Tagged session %d as %q\n", ppid, tag)
			return nil
		},
	}
}

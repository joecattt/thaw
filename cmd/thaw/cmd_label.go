package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/integrations/zoxide"
	"github.com/joecattt/thaw/internal/project"
)

// labelCmd prints the same short project name capture.go computes for a
// session (zoxide alias, else git repo root basename, else dir basename).
// Hidden — the shell hooks call this to name the tmux window/tab live;
// it's not something you'd type by hand.
func labelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "label [dir]",
		Short:  "Print the project name for a directory (used by shell hooks to name terminal tabs)",
		Hidden: true,
		Args:   cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			absDir, err := filepath.Abs(dir)
			if err != nil {
				return err
			}
			fmt.Println(projectLabel(absDir))
			return nil
		},
	}
	return cmd
}

// projectLabel is the same fallback chain capture.go's captureSession uses
// for sess.Label, minus the command-pattern matcher (no live command here).
func projectLabel(dir string) string {
	if zoxide.Available() {
		if l := zoxide.LabelForPath(dir); l != "" {
			return l
		}
	}
	if root := project.FindRepoRoot(dir); root != "" {
		return filepath.Base(root)
	}
	return filepath.Base(dir)
}

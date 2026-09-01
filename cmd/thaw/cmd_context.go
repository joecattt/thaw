package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/memory"
	"github.com/joecattt/thaw/internal/project"
	"github.com/joecattt/thaw/internal/upstream"
)

func contextCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "context [dir]",
		Short: "Show where you left off in a project and what changed since",
		Long: `Display the last known session state for a project directory,
including upstream changes (new commits, CI status, dep changes).

  thaw context              current directory
  thaw context ~/project    specific directory`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}
			absDir, err := filepath.Abs(dir)
			if err != nil {
				return err
			}

			mem, err := memory.Open()
			if err != nil {
				return err
			}
			defer mem.Close()

			entry, err := mem.Recall(absDir)
			if err != nil {
				pcfg, _ := project.Load(absDir)
				if pcfg != nil {
					fmt.Printf("thaw: project %s (no previous sessions)\n", pcfg.Project.Name)
				}
				return nil
			}

			fmt.Println(memory.FormatContext(entry))

			// Check upstream changes since last session
			if entry != nil && !entry.LastSeen.IsZero() {
				report, err := upstream.Check(absDir, entry.LastSeen)
				if err == nil && report.HasChanges() {
					fmt.Print(upstream.Format(report))
				}
			}
			return nil
		},
	}
}

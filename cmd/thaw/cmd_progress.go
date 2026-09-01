package main

import (
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/progress"
	"github.com/joecattt/thaw/internal/project"
)

func progressCmd() *cobra.Command {
	var runTests bool
	cmd := &cobra.Command{
		Use:   "progress [dir]",
		Short: "Show project health — commit activity, tests, open TODOs",
		Long: `Show progress signals for a project directory.

  thaw progress              analyze current directory
  thaw progress ~/project    analyze specific directory
  thaw progress --test       also run tests`,
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

			// Load project config if present
			pcfg, _ := project.Load(absDir)
			if !runTests && pcfg != nil {
				pcfg.Project.TestCommand = "" // skip tests unless --test
			}

			report, err := progress.Analyze(absDir, pcfg)
			if err != nil {
				return err
			}

			fmt.Print(progress.FormatReport(report))
			return nil
		},
	}
	cmd.Flags().BoolVar(&runTests, "test", false, "Run test suite as part of analysis")
	return cmd
}

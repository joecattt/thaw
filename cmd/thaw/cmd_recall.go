package main

import (
	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/pkg/models"
)

func recallCmd() *cobra.Command {
	var (
		run    bool
		dryRun bool
		noTmux bool
	)
	cmd := &cobra.Command{
		Use:   "recall <name>",
		Short: "Bring back a saved workspace by name",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			opts := models.RestoreOptions{}
			if run {
				opts.Mode = models.RunMode
			}
			return doRestore(opts, args[0], dryRun, noTmux)
		},
	}
	cmd.Flags().BoolVar(&run, "run", false, "Execute commands (default: safe mode)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print script without executing")
	cmd.Flags().BoolVar(&noTmux, "no-tmux", false, "Degraded restore without tmux — print context and a resume script")
	// Tab completion for workspace names
	cmd.ValidArgsFunction = func(cmd *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
		if len(args) > 0 {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		store, err := snapshot.Open()
		if err != nil {
			return nil, cobra.ShellCompDirectiveNoFileComp
		}
		defer store.Close()
		named, _ := store.ListNamed()
		var names []string
		for _, n := range named {
			if n.Name != "" {
				names = append(names, n.Name)
			}
		}
		return names, cobra.ShellCompDirectiveNoFileComp
	}
	return cmd
}

package main

import (
	"encoding/json"
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
)

func statusCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show which terminal sessions are running right now",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			snap, err := newEngine(cfg).Capture("status")
			if err != nil {
				return err
			}
			if len(snap.Sessions) == 0 {
				fmt.Println("No active terminal sessions.")
				return nil
			}
			if jsonOut {
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(snap.Sessions)
			}

			w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintf(w, "PID\tSTATUS\tLABEL\tGROUP\tBRANCH\tCWD\tCOMMAND\n")
			for _, s := range snap.Sessions {
				branch := ""
				if s.Git != nil {
					branch = s.Git.Branch
					if s.Git.Dirty {
						branch += "*"
					}
				}
				fmt.Fprintf(w, "%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
					s.PID, s.Status, s.Label, s.GroupName,
					branch, truncateLeft(s.CWD, 25), truncate(s.Command, 30))
			}
			w.Flush()

			groups := snap.WorkstreamGroups()
			realGroups := 0
			for k := range groups {
				if k != "misc" && len(groups[k]) >= 2 {
					realGroups++
				}
			}
			fmt.Printf("\n%d session(s)", len(snap.Sessions))
			if realGroups > 0 {
				fmt.Printf(", %d workstream(s)", realGroups)
			}
			fmt.Println()
			return nil
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

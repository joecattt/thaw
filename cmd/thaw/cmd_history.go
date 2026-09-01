package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/snapshot"
)

func historyCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "history",
		Short: "List your snapshots and saved workspaces",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			// Named workspaces
			named, _ := store.ListNamed()
			if len(named) > 0 {
				fmt.Println("Saved workspaces:")
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				for _, s := range named {
					fmt.Fprintf(w, "  %s\t%d sessions\t%s\n",
						s.Name, s.SessionCount, s.CreatedAt.Format("2006-01-02 15:04"))
				}
				w.Flush()
				fmt.Println()
			}

			// Recent auto snapshots
			summaries, err := store.List(limit)
			if err != nil {
				return err
			}
			if len(summaries) == 0 && len(named) == 0 {
				fmt.Println("No snapshots yet. Run `thaw freeze` to capture your first.")
				return nil
			}

			if len(summaries) > 0 {
				fmt.Println("Recent snapshots:")
				w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintf(w, "  ID\tSESSIONS\tSOURCE\tCREATED\n")
				for _, s := range summaries {
					name := ""
					if s.Name != "" {
						name = " [" + s.Name + "]"
					}
					fmt.Fprintf(w, "  #%d%s\t%d\t%s\t%s\n",
						s.ID, name, s.SessionCount, s.Source,
						s.CreatedAt.Format("2006-01-02 15:04"))
				}
				w.Flush()
			}
			return nil
		},
	}
	cmd.Flags().IntVarP(&limit, "limit", "n", 20, "Number of snapshots to show")
	return cmd
}

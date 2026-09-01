package main

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/internal/stale"
	"github.com/joecattt/thaw/pkg/models"
)

func inspectCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "inspect [id-or-name]",
		Short: "Look inside a snapshot and flag anything gone stale",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			var snap *models.Snapshot
			if len(args) > 0 {
				snap, err = store.GetNamed(args[0])
				if err != nil {
					return err
				}
				if snap == nil {
					if id, e := strconv.Atoi(args[0]); e == nil {
						snap, err = store.Get(id)
						if err != nil {
							return err
						}
					}
				}
			} else {
				snap, err = store.Latest()
				if err != nil {
					return err
				}
			}
			if snap == nil {
				fmt.Println("No snapshot found — run `thaw freeze` first, or `thaw history` to list IDs.")
				return nil
			}

			checks := stale.CheckAll(snap)
			label := fmt.Sprintf("#%d", snap.ID)
			if snap.Name != "" {
				label = fmt.Sprintf("%q (#%d)", snap.Name, snap.ID)
			}

			fmt.Printf("Snapshot %s\n", label)
			fmt.Printf("  Captured: %s  Source: %s  Host: %s\n\n",
				snap.CreatedAt.Format("2006-01-02 15:04:05"), snap.Source, snap.Hostname)

			for i, s := range snap.Sessions {
				sc := checks[s.PID]
				icon := "✓"
				if sc.IsStale() {
					icon = "✗"
				}
				fmt.Printf("[%d] %s %s — %s\n", i+1, icon, s.Label, s.Status)

				if s.Intent != "" {
					fmt.Printf("    Intent: %s\n", s.Intent)
				}

				fmt.Printf("    CWD:  %s", s.CWD)
				if !sc.CWDExists {
					fmt.Print("  ← MISSING")
				}
				fmt.Println()

				if !s.IsIdle() {
					fmt.Printf("    Cmd:  %s", s.Command)
					if !sc.BinaryExists {
						fmt.Print("  ← NOT FOUND")
					}
					fmt.Println()
				}

				if s.Git != nil {
					fmt.Printf("    Git:  %s @ %s", s.Git.Branch, s.Git.Commit)
					if s.Git.Dirty {
						fmt.Print(" (dirty)")
					}
					if !sc.GitBranchMatch {
						fmt.Print("  ← CHANGED")
					}
					fmt.Println()
				}

				if s.ProjectType != "" {
					fmt.Printf("    Project: %s\n", s.ProjectType)
				}

				if s.HasDirenv {
					fmt.Printf("    Direnv: .envrc detected (env managed by direnv)\n")
				} else if !s.EnvDelta.IsEmpty() {
					fmt.Printf("    Env:  %d var(s):", len(s.EnvDelta.Set))
					for k := range s.EnvDelta.Set {
						fmt.Printf(" %s", k)
					}
					fmt.Println()
				}

				if s.GroupName != "" {
					fmt.Printf("    Group: %s\n", s.GroupName)
				}

				if s.RestoreOrder > 0 {
					fmt.Printf("    Order: %d\n", s.RestoreOrder)
				}

				if len(s.Output) > 0 {
					fmt.Printf("    Output: %d lines captured\n", len(s.Output))
				}

				if len(s.History) > 0 {
					n := 5
					if len(s.History) < n {
						n = len(s.History)
					}
					fmt.Printf("    History (%d total):\n", len(s.History))
					for _, h := range s.History[len(s.History)-n:] {
						fmt.Printf("      $ %s\n", h)
					}
				}
				fmt.Println()
			}
			return nil
		},
	}
}

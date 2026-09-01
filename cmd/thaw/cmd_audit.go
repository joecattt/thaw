package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/audit"
	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/snapshot"
)

func auditCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "audit", Short: "Verify snapshot integrity"}

	cmd.AddCommand(&cobra.Command{
		Use:   "verify",
		Short: "Check hash chain integrity of all snapshots",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			result, err := audit.Verify(store)
			if err != nil {
				return err
			}

			fmt.Printf("Audit: %d total, %d verified, %d unsigned\n",
				result.Total, result.Verified, result.Unsigned)

			if result.IsIntact() {
				fmt.Println("Chain integrity: INTACT")
			} else {
				fmt.Printf("Chain integrity: BROKEN (%d broken links, %d tampered)\n",
					len(result.Broken), len(result.Tampered))
				for _, e := range result.Errors {
					fmt.Printf("  %s\n", e)
				}
			}
			return nil
		},
	})

	return cmd
}

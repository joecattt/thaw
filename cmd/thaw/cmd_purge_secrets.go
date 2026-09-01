package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/history"
)

func purgeSecretsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "purge-secrets",
		Short: "Re-scrub the command log, redacting secrets older builds may have written raw",
		RunE: func(cmd *cobra.Command, args []string) error {
			n, err := history.PurgeSecrets()
			if err != nil {
				return err
			}
			fmt.Printf("thaw: re-scrubbed command log — %d line(s) redacted\n", n)
			return nil
		},
	}
}

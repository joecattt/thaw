package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/export"
	"github.com/joecattt/thaw/internal/snapshot"
)

func exportDataCmd() *cobra.Command {
	var format string
	var rangeDays int
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Export your session data as CSV or JSON",
		Long: `Dump structured session data for time tracking, billing, or analysis.

  thaw export                     last 30 days as CSV
  thaw export --format=json       as JSON
  thaw export --range=7           last 7 days
  thaw export --range=90 > q3.csv pipe to file`,
		RunE: func(cmd *cobra.Command, args []string) error {
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()

			to := time.Now()
			from := to.AddDate(0, 0, -rangeDays)
			snaps, err := store.GetRange(from, to)
			if err != nil {
				return err
			}
			if len(snaps) == 0 {
				fmt.Fprintf(os.Stderr, "No snapshots in the last %d days.\n", rangeDays)
				return nil
			}

			records := export.Flatten(snaps)
			switch format {
			case "json":
				return export.WriteJSON(os.Stdout, records)
			default:
				return export.WriteCSV(os.Stdout, records)
			}
		},
	}
	cmd.Flags().StringVar(&format, "format", "csv", "Output format: csv or json")
	cmd.Flags().IntVar(&rangeDays, "range", 30, "Number of days to export")
	return cmd
}

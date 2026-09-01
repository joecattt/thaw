package main

import (
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/ledger"
	"github.com/joecattt/thaw/internal/snapshot"
)

func ledgerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ledger",
		Short: "Permanent per-project time ledger — bank daily totals, verify the hash chain",
	}

	cmd.AddCommand(&cobra.Command{
		Use:   "bank",
		Short: "Bank per-project time from raw snapshots into the permanent ledger",
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()
			now := time.Now()
			snaps, err := store.GetRange(now.AddDate(0, 0, -60), now)
			if err != nil {
				return err
			}
			dataDir, err := config.DataDir()
			if err != nil {
				return err
			}
			home, _ := os.UserHomeDir()
			l := ledger.New(dataDir)
			res, err := l.Bank(snaps, home)
			if err != nil {
				return err
			}
			fmt.Printf("ledger: %d rows (%d updated)\n", res.Rows, res.Updated)
			fmt.Printf("chain: %d sealed days (+%d new)\n", res.Seals, res.NewSeals)
			return reportVerify(l)
		},
	})

	cmd.AddCommand(&cobra.Command{
		Use:   "verify",
		Short: "Verify the ledger's hash chain — sealed history must match, byte for byte",
		RunE: func(cmd *cobra.Command, args []string) error {
			dataDir, err := config.DataDir()
			if err != nil {
				return err
			}
			return reportVerify(ledger.New(dataDir))
		},
	})

	return cmd
}

func reportVerify(l *ledger.Ledger) error {
	problems, err := l.Verify()
	if err != nil {
		return err
	}
	for _, p := range problems {
		fmt.Println(p)
	}
	if len(problems) > 0 {
		return fmt.Errorf("ledger chain verification failed (%d problem(s))", len(problems))
	}
	sealed, err := l.SealedDays()
	if err != nil {
		return err
	}
	if sealed == 0 {
		fmt.Println("chain: empty (nothing sealed yet)")
	} else {
		fmt.Printf("chain OK: %d sealed days verified\n", sealed)
	}
	return nil
}

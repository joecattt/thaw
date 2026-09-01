package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/restore"
	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/pkg/models"
)

func restoreCmd() *cobra.Command {
	var (
		warp   bool
		best   bool
		snapID int
		dryRun bool
	)
	cmd := &cobra.Command{
		Use:   "restore",
		Short: "Restore a workspace — Warp tabs when inside Warp, tmux otherwise",
		Long: `Restore your workspace. Inside Warp on macOS (with no tmux server running),
reopens every lost Claude Code pane as a Warp tab, each pre-typed with
'claude --resume <id>'. Elsewhere, same as bare 'thaw' (tmux restore).

  --best   restore from the RICHEST snapshot in the last 24h — use after a
           crash: the newest snapshot may already reflect the post-crash
           world with your tabs gone`,
		RunE: func(cmd *cobra.Command, args []string) error {
			useWarp := warp || best || snapID > 0 || restore.WarpAvailable()
			if !useWarp {
				return doInteractiveRestore(false, false)
			}
			if err := config.EnsureDirectories(); err != nil {
				return err
			}
			store, err := snapshot.Open()
			if err != nil {
				return err
			}
			defer store.Close()
			snap, err := pickWarpSnapshot(store, best, snapID)
			if err != nil {
				return err
			}
			return restore.WarpRestore(snap, dryRun)
		},
	}
	cmd.Flags().BoolVar(&warp, "warp", false, "Force the Warp tab backend (auto-detected inside Warp)")
	cmd.Flags().BoolVar(&best, "best", false, "Use the richest snapshot of the last 24h, not the newest")
	cmd.Flags().IntVar(&snapID, "snap", 0, "Restore from a specific snapshot id (thaw history)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "Print what would open, touch nothing")
	return cmd
}

// pickWarpSnapshot chooses which snapshot to restore claude tabs from:
// an explicit id, the richest of the last 24h (--best), or the newest
// with any claude panes at all.
func pickWarpSnapshot(store *snapshot.Store, best bool, snapID int) (*models.Snapshot, error) {
	if snapID > 0 {
		snap, err := store.Get(snapID)
		if err != nil {
			return nil, err
		}
		if snap == nil {
			return nil, fmt.Errorf("snapshot %d not found", snapID)
		}
		return snap, nil
	}
	now := time.Now()
	snaps, err := store.GetRange(now.Add(-24*time.Hour), now)
	if err != nil {
		return nil, err
	}
	var pick *models.Snapshot
	bestN := 0
	for i := len(snaps) - 1; i >= 0; i-- { // newest first
		n := restore.CountClaudeSessions(snaps[i])
		if best {
			if n > bestN {
				pick, bestN = snaps[i], n
			}
		} else if n > 0 {
			return snaps[i], nil
		}
	}
	if pick == nil {
		return nil, fmt.Errorf("no snapshot with claude sessions in the last 24h — try --snap <id> (thaw history)")
	}
	return pick, nil
}

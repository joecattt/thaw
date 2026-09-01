package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/dashboard"
	"github.com/joecattt/thaw/internal/export"
	"github.com/joecattt/thaw/internal/snapshot"
)

// watchRefreshSecs is how often --watch regenerates the file and the page
// self-reloads. Fixed, not a flag — the browser-only settings panel is
// where per-viewer tuning lives now; this just needs to be "often enough
// to feel live" without hammering the snapshot db.
const watchRefreshSecs = 60

func dashboardCmd() *cobra.Command {
	var rangeDays int
	var open bool
	var extras bool
	var kiosk bool
	var watch bool
	var summarize bool
	cmd := &cobra.Command{
		Use:   "dashboard",
		Short: "Show real per-project progress since your last session",
		Long: `Per-project progress report: commits, lines changed, and dirty
state since you were last in each project — not a time estimate.
Prints a plain-text report by default; use --open for the HTML version
in your browser.

The HTML version (--open) always shows time-by-project, AI spend, and other
real thaw data in side rails — that's core to the product, not scope
creep. --extras adds ONE more thing to the left rail: news headlines from
external feeds ([news] sources in config.toml) — the one piece an audit
flagged as genuine scope creep for a workspace-memory tool, so it stays
opt-in specifically, not the rails themselves.

--kiosk is a different mode entirely, not a variant of the report: one
giant fact filling the screen, auto-rotating, zero scroll — for glancing
at from across a room, not reading. Same real data, different presentation.

--watch keeps the file current: regenerates it every 60s in the
background and the page reloads itself. The browser only opens once —
each regeneration overwrites the same file, it never spawns a new tab.
Runs in the foreground; Ctrl+C stops it.

  thaw dashboard                 last 30 days, prints to stdout
  thaw dashboard --range=7       last 7 days
  thaw dashboard --open          open the HTML version, with side rails
  thaw dashboard --open --extras also fetch news headlines into the left rail
  thaw dashboard --open --kiosk  full-screen giant-text rotating mode
  thaw dashboard --open --kiosk --watch  same, but keeps itself current
  thaw dashboard --open --summarize      backfill a few Past Sessions with real AI summaries (K3, cached)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			generate := func() (string, error) {
				store, err := snapshot.Open()
				if err != nil {
					return "", err
				}
				defer store.Close()

				to := time.Now()
				from := to.AddDate(0, 0, -rangeDays)
				snaps, err := store.GetRange(from, to)
				if err != nil {
					return "", err
				}
				if len(snaps) == 0 {
					return "", nil
				}
				records := export.Flatten(snaps)
				if !open {
					return dashboard.GenerateText(records, rangeDays), nil
				}
				if kiosk {
					return dashboard.GenerateKiosk(records, rangeDays, extras, summarize), nil
				}
				return dashboard.Generate(records, rangeDays, extras, summarize), nil
			}

			htmlOut, err := generate()
			if err != nil {
				return err
			}
			if htmlOut == "" {
				fmt.Fprintf(os.Stderr, "No snapshots in the last %d days.\n", rangeDays)
				return nil
			}

			if !open {
				fmt.Print(htmlOut)
				return nil
			}

			fileName := "thaw-dashboard.html"
			if kiosk {
				fileName = "thaw-kiosk.html"
			}
			// The report contains command history and Claude conversation
			// titles — keep it out of the world-readable shared temp dir.
			tmpDir := filepath.Join(os.TempDir(), fmt.Sprintf("thaw-%d", os.Getuid()))
			if err := os.MkdirAll(tmpDir, 0700); err != nil {
				return err
			}
			path := filepath.Join(tmpDir, fileName)
			if watch {
				htmlOut = injectSelfReload(htmlOut)
			}
			if err := os.WriteFile(path, []byte(htmlOut), 0600); err != nil {
				return err
			}
			fmt.Printf("Dashboard: %s\n", path)
			if err := exec.Command("open", path).Start(); err != nil {
				return fmt.Errorf("opening browser: %w", err)
			}
			if !watch {
				return nil
			}

			fmt.Printf("Watching — regenerating every %ds, Ctrl+C to stop.\n", watchRefreshSecs)
			for {
				time.Sleep(watchRefreshSecs * time.Second)
				htmlOut, err := generate()
				if err != nil {
					fmt.Fprintf(os.Stderr, "thaw dashboard --watch: %v\n", err)
					continue
				}
				if htmlOut == "" {
					continue
				}
				if err := os.WriteFile(path, []byte(injectSelfReload(htmlOut)), 0600); err != nil {
					fmt.Fprintf(os.Stderr, "thaw dashboard --watch: %v\n", err)
				}
			}
		},
	}
	cmd.Flags().IntVar(&rangeDays, "range", 30, "Number of days to include")
	cmd.Flags().BoolVar(&open, "open", false, "Open in browser instead of printing to stdout")
	cmd.Flags().BoolVar(&extras, "extras", false, "Also fetch news headlines into the left rail (HTML only, opt-in)")
	cmd.Flags().BoolVar(&kiosk, "kiosk", false, "Full-screen giant-text rotating mode instead of the report (HTML only)")
	cmd.Flags().BoolVar(&watch, "watch", false, "Keep regenerating in the background; the page reloads itself (HTML only)")
	cmd.Flags().BoolVar(&summarize, "summarize", false, "Backfill a few Past Sessions with real AI summaries via K3 (slow, cached — repeat runs cover more)")
	return cmd
}

// injectSelfReload appends a meta-refresh that reloads the page from disk
// on the fixed watchRefreshSecs cadence — the file on disk is what --watch
// keeps current, this just makes the open tab pick up each new version
// without the user hitting refresh by hand.
func injectSelfReload(htmlOut string) string {
	tag := fmt.Sprintf(`<meta http-equiv="refresh" content="%d">`, watchRefreshSecs+2)
	if i := indexHead(htmlOut); i >= 0 {
		return htmlOut[:i] + tag + htmlOut[i:]
	}
	return tag + htmlOut
}

func indexHead(s string) int {
	const marker = "<head>"
	for i := 0; i+len(marker) <= len(s); i++ {
		if s[i:i+len(marker)] == marker {
			return i + len(marker)
		}
	}
	return -1
}

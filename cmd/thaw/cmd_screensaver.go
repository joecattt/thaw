package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"time"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/dashboard"
	"github.com/joecattt/thaw/internal/export"
	"github.com/joecattt/thaw/internal/snapshot"
)

// hidIdleTimeRe pulls the HIDIdleTime value (nanoseconds since last
// keyboard/mouse input) out of `ioreg -c IOHIDSystem` — the same signal
// macOS's own screensaver/display-sleep uses.
var hidIdleTimeRe = regexp.MustCompile(`"HIDIdleTime"\s*=\s*(\d+)`)

// idleSeconds reads real macOS idle time via ioreg — no polling of our own
// mouse/keyboard hooks, just the same counter the OS already maintains.
func idleSeconds() (int, error) {
	out, err := exec.Command("ioreg", "-c", "IOHIDSystem").Output()
	if err != nil {
		return 0, err
	}
	m := hidIdleTimeRe.FindSubmatch(out)
	if m == nil {
		return 0, fmt.Errorf("HIDIdleTime not found in ioreg output")
	}
	ns, err := strconv.ParseInt(string(m[1]), 10, 64)
	if err != nil {
		return 0, err
	}
	return int(ns / 1e9), nil
}

func screensaverCmd() *cobra.Command {
	var idleThreshold int
	var rangeDays int
	cmd := &cobra.Command{
		Use:   "screensaver",
		Short: "Auto-launch the kiosk dashboard after the machine sits idle",
		Long: `Watches real macOS idle time (the same HIDIdleTime counter the
system's own screensaver/display-sleep uses) and opens the full-screen
kiosk dashboard once the machine has been idle past --idle seconds.
Regenerates the kiosk file fresh each time it triggers. Opens once per
idle period — moving the mouse resets the idle clock, but this command
doesn't try to close the browser tab when you come back; it just won't
re-open a new one until you've gone idle again.

Runs in the foreground — Ctrl+C stops it. This does NOT install itself as
a background/login item; if you want it running automatically, add it to
your own launchd or login-items setup.

  thaw screensaver                 default: 300s (5min) idle threshold
  thaw screensaver --idle=600      10 minutes idle before it opens`,
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Printf("Watching idle time — opens the kiosk dashboard after %ds idle. Ctrl+C to stop.\n", idleThreshold)
			triggered := false
			for {
				secs, err := idleSeconds()
				if err != nil {
					fmt.Fprintf(os.Stderr, "thaw screensaver: %v\n", err)
					time.Sleep(10 * time.Second)
					continue
				}
				switch {
				case secs >= idleThreshold && !triggered:
					triggered = true
					if err := launchKioskScreensaver(rangeDays); err != nil {
						fmt.Fprintf(os.Stderr, "thaw screensaver: %v\n", err)
					}
				case secs < idleThreshold:
					triggered = false // reset — next idle period can trigger again
				}
				time.Sleep(5 * time.Second)
			}
		},
	}
	cmd.Flags().IntVar(&idleThreshold, "idle", 300, "Seconds of real idle time before the kiosk opens")
	cmd.Flags().IntVar(&rangeDays, "range", 30, "Number of days to include")
	return cmd
}

func launchKioskScreensaver(rangeDays int) error {
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
		return nil
	}
	records := export.Flatten(snaps)
	htmlOut := dashboard.GenerateKiosk(records, rangeDays, true, false)
	path := filepath.Join(os.TempDir(), "thaw-kiosk.html")
	if err := os.WriteFile(path, []byte(htmlOut), 0644); err != nil {
		return err
	}
	return exec.Command("open", path).Start()
}

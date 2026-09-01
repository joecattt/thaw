package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/git"
	"github.com/joecattt/thaw/internal/restore"
	"github.com/joecattt/thaw/pkg/models"
)

// runTrustDemo stages a freeze → kill → restore cycle on a scratch tmux
// session so the user can watch thaw bring a workspace back. Falls back to
// the degraded-mode demo when tmux isn't installed.
func runTrustDemo() error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return runDegradedDemo()
	}

	name := fmt.Sprintf("thaw-demo-%d", time.Now().Unix())
	markerDir, err := os.MkdirTemp("", "thaw-demo-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(markerDir)
	marker := filepath.Join(markerDir, "THAW_DEMO_MARKER.txt")
	os.WriteFile(marker, []byte("if you can read this, thaw restored your workspace\n"), 0644)

	fmt.Printf("\n1/4 Creating scratch tmux session %q in %s\n", name, markerDir)
	if out, err := exec.Command("tmux", "new-session", "-d", "-s", name, "-c", markerDir).CombinedOutput(); err != nil {
		return fmt.Errorf("creating demo session: %v (%s)", err, strings.TrimSpace(string(out)))
	}
	// Always clean up the scratch session, whether or not the demo finishes
	defer exec.Command("tmux", "kill-session", "-t", name).Run()

	// Freeze — an in-memory snapshot of the scratch session's context.
	// Deliberately not saved to the store so the demo never becomes "latest".
	snap := demoSnapshot(name, markerDir)
	fmt.Println("2/4 Freezing it (cwd, history, context)")
	time.Sleep(1 * time.Second)

	fmt.Println("3/4 Killing the session — poof, it's gone")
	exec.Command("tmux", "kill-session", "-t", name).Run()
	time.Sleep(1 * time.Second)

	fmt.Println("4/4 Restoring from the snapshot...")
	opts := models.DefaultRestoreOptions()
	opts.SessionName = name
	opts.MultiSession = false
	if err := restore.NewTmux().Restore(snap, opts); err != nil {
		return fmt.Errorf("demo restore failed: %w", err)
	}
	if err := exec.Command("tmux", "has-session", "-t", name).Run(); err != nil {
		return fmt.Errorf("demo session did not come back")
	}

	fmt.Printf("\nSession %q is back — see for yourself: tmux attach -t %s\n", name, name)
	fmt.Println("That's it — thaw brought it back. It does this automatically for your real work.")
	fmt.Print("\nPress Enter to clean up the demo session...")
	var answer string
	fmt.Scanln(&answer)
	return nil
}

// runDegradedDemo shows the no-tmux experience: freeze the current directory
// state, then display the resume summary thaw would show after a restart.
func runDegradedDemo() error {
	fmt.Println("\ntmux isn't installed, so here's the degraded-mode demo instead:")
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}

	snap := demoSnapshot("thaw-demo", cwd)
	if g := git.State(cwd); g != nil {
		snap.Sessions[0].Git = g
	}

	fmt.Println("1/2 Froze the current directory state")
	fmt.Println("2/2 This is what `thaw` shows you after a restart:")
	fmt.Println()
	if err := restore.DegradedRestore(snap, models.DefaultRestoreOptions()); err != nil {
		return err
	}
	fmt.Println("That's it — thaw brought it back. It does this automatically for your real work.")
	return nil
}

// demoSnapshot builds an in-memory snapshot for the trust demo.
func demoSnapshot(label, dir string) *models.Snapshot {
	host, _ := os.Hostname()
	return &models.Snapshot{
		Sessions: []models.Session{{
			CWD:        dir,
			Shell:      os.Getenv("SHELL"),
			Label:      label,
			Status:     "idle",
			CapturedAt: time.Now(),
			History:    []string{"cat THAW_DEMO_MARKER.txt", "ls"},
			Intent:     "thaw setup demo — proving freeze/restore works",
		}},
		CreatedAt: time.Now(),
		Source:    "demo",
		Hostname:  host,
	}
}

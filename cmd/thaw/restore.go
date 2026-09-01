package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/deprot"
	"github.com/joecattt/thaw/internal/project"
	"github.com/joecattt/thaw/internal/recovery"
	"github.com/joecattt/thaw/internal/restore"
	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/internal/stale"
	"github.com/joecattt/thaw/internal/upstream"
	"github.com/joecattt/thaw/pkg/models"
)

// doRestore is the shared restore logic for both `thaw` (default) and `thaw recall`.
func doRestore(optsOverride models.RestoreOptions, nameOrID string, dryRun, noTmux bool) error {
	if err := config.EnsureDirectories(); err != nil {
		return err
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}

	store, err := snapshot.Open()
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer store.Close()

	// Check for crash recovery — if last shutdown was unexpected,
	// reconstruct state from command log
	if nameOrID == "" {
		recovered, err := recovery.Check(store)
		if err != nil {
			fmt.Fprintf(os.Stderr, "warning: recovery check failed: %v\n", err)
		} else if recovered != nil {
			fmt.Printf("Recovered %d session(s) after unexpected shutdown\n", len(recovered.Sessions))
		}
	}

	var snap *models.Snapshot

	if nameOrID != "" {
		// Try as named workspace first
		snap, err = store.GetNamed(nameOrID)
		if err != nil {
			return err
		}
		// Try as numeric ID
		if snap == nil {
			if id, e := strconv.Atoi(nameOrID); e == nil {
				snap, err = store.Get(id)
				if err != nil {
					return err
				}
			}
		}
		if snap == nil {
			return fmt.Errorf("workspace %q not found — run `thaw save %s` to create it", nameOrID, nameOrID)
		}
	} else {
		snap, err = store.Latest()
		if err != nil {
			return err
		}
	}

	if snap == nil {
		fmt.Println("Nothing to thaw. Run `thaw freeze` first.")
		return nil
	}

	// Build options from config defaults + overrides
	opts := models.DefaultRestoreOptions()
	opts.RestoreEnv = cfg.Restore.RestoreEnv
	opts.ShowHistory = cfg.Restore.ShowHistory
	opts.ShowOutput = cfg.Restore.ShowOutput
	opts.ShowIntent = cfg.Restore.ShowIntent
	opts.MultiSession = cfg.Restore.MultiSession
	opts.MaxPanes = cfg.Restore.MaxPanes
	opts.TierDelaySec = cfg.Restore.TierDelaySec
	if cfg.Restore.DefaultLayout != "" {
		opts.Layout = cfg.Restore.DefaultLayout
	}
	opts.SkipStale = cfg.Safety.SkipStale
	opts.HistoryLines = cfg.Capture.HistoryLines

	// Apply overrides
	if optsOverride.Mode != 0 {
		opts.Mode = optsOverride.Mode
	}
	if optsOverride.SessionName != "" {
		opts.SessionName = optsOverride.SessionName
	}
	if optsOverride.Layout != "" {
		opts.Layout = optsOverride.Layout
	}

	// Auto-prune old snapshots
	if _, err := store.Prune(time.Duration(cfg.Daemon.KeepDays)*24*time.Hour, cfg.Daemon.KeepMax); err != nil {
		fmt.Fprintf(os.Stderr, "warning: prune failed: %v\n", err)
	}

	target := restore.NewTmux()
	if noTmux || !target.Available() {
		if !noTmux {
			fmt.Println("tmux not found — falling back to degraded restore. Install with: brew install tmux")
			fmt.Println()
		}
		return restore.DegradedRestore(snap, opts)
	}

	// Staleness report
	staleChecks := stale.CheckAll(snap)
	staleCount := 0
	for _, sc := range staleChecks {
		if sc.IsStale() {
			staleCount++
		}
	}

	// Dependency rot check — warn if project files changed since snapshot
	depRots := deprot.CheckAll(snap)
	if len(depRots) > 0 {
		warnings := deprot.FormatWarnings(depRots, snap.Sessions)
		fmt.Printf("⚠ Dependencies changed since snapshot:\n")
		for _, w := range warnings {
			fmt.Printf("  %s\n", w)
		}
		fmt.Println()
	}

	if dryRun {
		script, err := target.GenerateScript(snap, opts)
		if err != nil {
			return err
		}
		fmt.Println(script)
		return nil
	}

	label := "latest"
	if snap.Name != "" {
		label = snap.Name
	}
	modeLabel := "safe"
	if opts.Mode == models.RunMode {
		modeLabel = "run"
	}

	restored := len(snap.Sessions)
	if opts.SkipStale {
		restored -= staleCount
	}

	playMelt()
	fmt.Printf("Thawing %q — %d sessions (%s mode)\n", label, restored, modeLabel)

	if n := restore.NewClaudeResumer().CountClaudePanes(snap); n > 0 {
		fmt.Printf("  ❄ %d claude conversation(s) set to resume — press Enter in each pane\n", n)
	}

	if staleCount > 0 && opts.SkipStale {
		fmt.Printf("  Skipped %d stale session(s)\n", staleCount)
	} else if staleCount > 0 {
		fmt.Printf("  ⚠ %d session(s) have stale context\n", staleCount)
	}

	// Check for auto-stashed work in project directories
	seen := map[string]bool{}
	for _, s := range snap.Sessions {
		if s.Git == nil || s.Git.RepoRoot == "" || seen[s.Git.RepoRoot] {
			continue
		}
		seen[s.Git.RepoRoot] = true
		out, err := exec.Command("git", "-C", s.Git.RepoRoot, "stash", "list").Output()
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "thaw-auto-") {
				fmt.Printf("  📦 Auto-stashed work found in %s — run: git -C %s stash pop\n",
					filepath.Base(s.Git.RepoRoot), s.Git.RepoRoot)
				break
			}
		}
	}

	if err := target.Restore(snap, opts); err != nil {
		return fmt.Errorf("restore failed: %w", err)
	}

	// Show attach instructions
	if opts.MultiSession {
		groups := snap.WorkstreamGroups()
		if len(groups) > 1 {
			fmt.Println("\nWorkstreams restored:")
			for name := range groups {
				fmt.Printf("  tmux attach -t %s\n", sanitize(name))
			}
			return nil
		}
	}
	fmt.Printf("\ntmux attach -t %s\n", opts.SessionName)
	return nil
}

func doInteractiveRestore(noGreeting, noTmux bool) error {
	// First run — no config yet means setup hasn't happened. Point there
	// instead of showing an empty picker.
	if cfgPath, err := config.ConfigPath(); err == nil {
		if _, statErr := os.Stat(cfgPath); os.IsNotExist(statErr) {
			fmt.Println("Welcome to thaw — terminal workspace memory.")
			fmt.Println()
			fmt.Println("Looks like this is your first run. Get started with:")
			fmt.Println("  thaw setup     install shell hooks and the background daemon")
			fmt.Println("  thaw freeze    take your first snapshot")
			fmt.Println()
			fmt.Println("After that, just run `thaw` to pick up where you left off.")
			return nil
		}
	}

	if err := config.EnsureDirectories(); err != nil {
		return err
	}
	store, err := snapshot.Open()
	if err != nil {
		return err
	}
	defer store.Close()

	snap, err := store.Latest()
	if err != nil {
		return fmt.Errorf("reading snapshot store: %w", err)
	}
	if snap == nil {
		fmt.Println("Nothing to thaw. Run `thaw freeze` first.")
		return nil
	}

	// Group sessions by project
	sorted := project.Group(snap.Sessions)

	// Display — where-was-I greeting first when interactive, plain header otherwise
	greeted := false
	if !noGreeting && isTTY(os.Stdin) && isTTY(os.Stdout) {
		fmt.Printf("\n%s\n", greeting(snap))
		if n := restore.NewClaudeResumer().CountClaudePanes(snap); n > 0 {
			fmt.Printf("❄ %d frozen claude conversation(s) — restore will queue them to resume\n", n)
		}
		fmt.Println("\nPress Enter to restore this, or choose:")
		fmt.Println()
		greeted = true
	} else {
		ago := time.Since(snap.CreatedAt)
		agoStr := formatDurationShort(ago)
		fmt.Printf("\nLast snapshot: %s ago (%s)\n\n", agoStr, snap.CreatedAt.Format("Mon 3:04 PM"))
	}

	fmt.Println("  Projects to restore:")
	for i, p := range sorted {
		branch := ""
		if p.Branch != "" {
			branch = " [" + p.Branch + "]"
		}
		status := fmt.Sprintf("%d active", p.Alive)
		if p.Idle > 0 {
			status += fmt.Sprintf(", %d idle", p.Idle)
		}

		// Check for dep staleness
		staleWarning := ""
		for _, s := range p.Sessions {
			rots := deprot.Check(s)
			if len(rots) > 0 {
				staleWarning = " ⚠ deps may be stale"
				break
			}
		}

		fmt.Printf("  %d) %s%s — %d session(s) (%s)%s\n", i+1, p.Name, branch, len(p.Sessions), status, staleWarning)

		// Check upstream changes
		for _, dir := range p.Dirs {
			report, err := upstream.Check(dir, snap.CreatedAt)
			if err == nil && report.HasChanges() {
				if report.BehindBy > 0 {
					fmt.Printf("       ↳ %d new upstream commit(s) — pull needed\n", report.BehindBy)
				}
				if report.CIStatus == "failure" {
					fmt.Printf("       ↳ CI failed on %s\n", report.Branch)
				}
				if report.ForcePushed {
					fmt.Printf("       ↳ upstream was force-pushed\n")
				}
				break
			}
		}
	}

	fmt.Printf("\n  a) Restore all\n")
	fmt.Printf("  q) Quit\n\n")
	fmt.Print("  Choice: ")

	var choice string
	fmt.Scanln(&choice)

	if choice == "q" {
		return nil
	}

	if choice == "" {
		// After the greeting, Enter restores the latest snapshot
		if greeted {
			return doRestore(models.RestoreOptions{}, "", false, noTmux)
		}
		return nil
	}

	if choice == "a" {
		return doRestore(models.RestoreOptions{}, "", false, noTmux)
	}

	// Parse selection (single number or comma-separated)
	var selected []int
	for _, part := range strings.Split(choice, ",") {
		n, err := strconv.Atoi(strings.TrimSpace(part))
		if err != nil || n < 1 || n > len(sorted) {
			fmt.Printf("Invalid choice: %s\n", part)
			return nil
		}
		selected = append(selected, n-1)
	}

	// Filter snapshot to selected projects
	var filtered []models.Session
	for _, idx := range selected {
		filtered = append(filtered, sorted[idx].Sessions...)
	}
	snap.Sessions = filtered

	// Save as temp and restore
	snap.Name = ""
	snap.Source = "interactive"
	store.Save(snap)
	return doRestore(models.RestoreOptions{}, strconv.Itoa(snap.ID), false, noTmux)
}

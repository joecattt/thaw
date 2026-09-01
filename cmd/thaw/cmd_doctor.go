package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/daemon"
	"github.com/joecattt/thaw/internal/snapshot"
)

func doctorCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "doctor",
		Short: "Check your install and diagnose problems",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("thaw doctor — checking installation")
			fmt.Println()
			issues := 0

			// 1. tmux — missing tmux is degraded mode, not a hard failure
			if _, err := exec.LookPath("tmux"); err != nil {
				fmt.Println("  ⚠ tmux missing → degraded mode available (context + resume script, no process restore)")
				fmt.Println("    install for full restore: brew install tmux")
			} else {
				out, _ := exec.Command("tmux", "-V").Output()
				fmt.Printf("  ✓ tmux installed (%s)\n", strings.TrimSpace(string(out)))
			}

			// 2. Config
			cfgPath, _ := config.ConfigPath()
			if _, err := os.Stat(cfgPath); err != nil {
				fmt.Printf("  ✗ config not found at %s — run: thaw setup\n", cfgPath)
				issues++
			} else {
				cfg, err := config.Load()
				if err != nil {
					fmt.Printf("  ✗ config parse error: %v\n", err)
					issues++
				} else {
					fmt.Printf("  ✓ config loaded from %s\n", cfgPath)
					warnings := config.Validate(cfg)
					for _, w := range warnings {
						fmt.Printf("    ⚠ %s\n", w)
					}
				}
			}

			// 3. Database
			dataDir, _ := config.DataDir()
			dbPath := filepath.Join(dataDir, "thaw.db")
			if _, err := os.Stat(dbPath); err != nil {
				fmt.Println("  ✗ database not found — run: thaw freeze")
				issues++
			} else {
				info, _ := os.Stat(dbPath)
				perm := info.Mode().Perm()
				fmt.Printf("  ✓ database exists (%s, permissions %04o)\n", humanSize(info.Size()), perm)
				if perm&0077 != 0 {
					fmt.Println("    ⚠ database is readable by other users — run: chmod 600 " + dbPath)
					issues++
				}

				store, err := snapshot.Open()
				if err != nil {
					fmt.Printf("  ✗ database open FAILED: %v\n", err)
					issues++
				} else {
					defer store.Close()

					// Integrity check
					if intErr := store.IntegrityCheck(); intErr != nil {
						fmt.Printf("  ✗ database integrity check FAILED: %v\n", intErr)
						fmt.Println("    Run: thaw admin repair")
						issues++
					} else {
						fmt.Println("  ✓ database integrity check passed")
					}

					summaries, _ := store.List(1)
					if len(summaries) > 0 {
						fmt.Printf("  ✓ %d snapshot(s), latest: %s\n",
							summaries[0].ID, summaries[0].CreatedAt.Format("2006-01-02 15:04"))
					} else {
						fmt.Println("  ⚠ database exists but no snapshots — run: thaw freeze")
					}
				}
			}

			// 4. Shell hooks
			shell := os.Getenv("SHELL")
			if shell == "" {
				shell = "/bin/sh"
			}
			shellName := filepath.Base(shell)
			rcFile := shellRCPath(shellName)
			if rcFile != "" {
				data, err := os.ReadFile(rcFile)
				if err == nil && strings.Contains(string(data), "thaw") {
					fmt.Printf("  ✓ shell hooks installed in %s\n", rcFile)
				} else {
					fmt.Printf("  ✗ shell hooks not found in %s — run: thaw setup\n", rcFile)
					issues++
				}
			}

			// 5. Command log
			home, _ := os.UserHomeDir()
			logPath := filepath.Join(home, ".local", "state", "thaw", "commands.log")
			if info, err := os.Stat(logPath); err == nil {
				fmt.Printf("  ✓ command log exists (%s)\n", humanSize(info.Size()))
				perm := info.Mode().Perm()
				if perm&0077 != 0 {
					fmt.Println("    ⚠ command log readable by other users — run: chmod 600 " + logPath)
				}
			} else {
				fmt.Println("  ⚠ command log not found — shell hooks may not be active")
			}

			// 6. Daemon
			if running, pid := daemon.IsRunning(); running {
				fmt.Printf("  ✓ daemon running (PID %d)\n", pid)
				// Check heartbeat freshness
				age := daemon.HeartbeatAge()
				if age > 0 && age < 15*time.Minute {
					fmt.Printf("  ✓ daemon heartbeat %s ago\n", formatDurationShort(age))
				} else if age > 0 {
					fmt.Printf("  ⚠ daemon heartbeat stale (%s ago) — daemon may be hung\n", formatDurationShort(age))
				}
			} else {
				fmt.Println("  ⚠ daemon not running — run: thaw daemon start")
			}

			// 7. Disk space
			if avail := availableDiskMB(dataDir); avail >= 0 {
				if avail < 100 {
					fmt.Printf("  ⚠ low disk space: %d MB available — snapshots may fail\n", avail)
					issues++
				} else {
					fmt.Printf("  ✓ disk space: %d MB available\n", avail)
				}
			}

			// 8. Optional integrations
			fmt.Println("\nOptional integrations:")
			optionals := []struct{ name, bin string }{
				{"atuin", "atuin"}, {"direnv", "direnv"}, {"zoxide", "zoxide"},
			}
			for _, o := range optionals {
				if _, err := exec.LookPath(o.bin); err == nil {
					fmt.Printf("  ✓ %s found\n", o.name)
				} else {
					fmt.Printf("  · %s not installed (optional)\n", o.name)
				}
			}

			fmt.Println()
			if issues == 0 {
				fmt.Println("All checks passed. thaw is ready.")
			} else {
				fmt.Printf("%d issue(s) found. Run `thaw setup` to fix most of them.\n", issues)
			}
			return nil
		},
	}
}

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/joecattt/thaw/internal/daemon"
)

func uninstallCmd() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove thaw shell hooks, daemon service, and data",
		RunE: func(cmd *cobra.Command, args []string) error {
			if !yes {
				fmt.Println("This will remove:")
				fmt.Println("  - Shell hooks from ~/.zshrc / ~/.bashrc")
				fmt.Println("  - Daemon service (launchd/systemd)")
				fmt.Println("  - All thaw data (~/.local/share/thaw, ~/.local/state/thaw)")
				fmt.Println("  - Configuration (~/.config/thaw)")
				fmt.Print("\nContinue? [y/N]: ")
				var answer string
				fmt.Scanln(&answer)
				if answer != "y" && answer != "Y" {
					fmt.Println("Cancelled.")
					return nil
				}
			}

			home, _ := os.UserHomeDir()
			var actions []string

			// Stop daemon
			if running, _ := daemon.IsRunning(); running {
				daemon.Stop()
				actions = append(actions, "Stopped daemon")
			}

			// Remove shell hooks from rc files
			for _, rc := range []string{
				filepath.Join(home, ".zshrc"),
				filepath.Join(home, ".bashrc"),
			} {
				if removed := removeThawHooks(rc); removed {
					actions = append(actions, "Removed hooks from "+filepath.Base(rc))
				}
			}

			// Remove systemd/launchd service
			systemdPath := filepath.Join(home, ".config", "systemd", "user", "thaw.service")
			if _, err := os.Stat(systemdPath); err == nil {
				os.Remove(systemdPath)
				actions = append(actions, "Removed systemd service")
			}
			launchdPath := filepath.Join(home, "Library", "LaunchAgents", "com.thaw.daemon.plist")
			if _, err := os.Stat(launchdPath); err == nil {
				exec.Command("launchctl", "unload", launchdPath).Run()
				os.Remove(launchdPath)
				actions = append(actions, "Removed launchd service")
			}

			// Remove data directories
			dirs := []string{
				filepath.Join(home, ".local", "share", "thaw"),
				filepath.Join(home, ".local", "state", "thaw"),
				filepath.Join(home, ".config", "thaw"),
			}
			for _, d := range dirs {
				if _, err := os.Stat(d); err == nil {
					os.RemoveAll(d)
					actions = append(actions, "Removed "+d)
				}
			}

			fmt.Println("thaw uninstalled:")
			for _, a := range actions {
				fmt.Printf("  ✓ %s\n", a)
			}
			if len(actions) == 0 {
				fmt.Println("  Nothing to remove — thaw wasn't fully installed.")
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "Skip confirmation")
	return cmd
}

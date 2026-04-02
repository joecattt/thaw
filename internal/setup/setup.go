package setup

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/joecattt/thaw/internal/config"
)

// Run performs a complete zero-config setup:
// 1. Create config directories
// 2. Write default config
// 3. Detect shell and inject hook into rc file
// 4. Install background daemon as system service
// 5. Report what was done
func Run() ([]string, error) {
	var actions []string

	// 1. Directories
	if err := config.EnsureDirectories(); err != nil {
		return nil, fmt.Errorf("creating directories: %w", err)
	}
	actions = append(actions, "Created config and data directories")

	// 2. Config file
	cfgPath, _ := config.ConfigPath()
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		cfg := config.DefaultConfig()
		if err := config.Save(cfg); err != nil {
			return nil, fmt.Errorf("writing config: %w", err)
		}
		actions = append(actions, fmt.Sprintf("Created config at %s", cfgPath))
	} else {
		actions = append(actions, "Config already exists, skipped")
	}

	// 3. Shell integration
	shell := detectShell()
	rcFile := rcFilePath(shell)
	if rcFile == "" {
		actions = append(actions, fmt.Sprintf("Could not detect rc file for %s — add manually: eval \"$(thaw shell-init %s)\"", shell, shell))
	} else {
		injected, err := injectShellHook(rcFile, shell)
		if err != nil {
			actions = append(actions, fmt.Sprintf("Failed to inject shell hook: %v", err))
		} else if injected {
			actions = append(actions, fmt.Sprintf("Added shell integration to %s", rcFile))
		} else {
			actions = append(actions, fmt.Sprintf("Shell integration already in %s, skipped", rcFile))
		}
	}

	// 4. Install system service (launchd on macOS, systemd on Linux)
	svcAction, err := installService()
	if err != nil {
		actions = append(actions, fmt.Sprintf("Service install skipped: %v", err))
	} else {
		actions = append(actions, svcAction)
	}

	return actions, nil
}

// detectShell returns the current shell name (zsh, bash, fish).
func detectShell() string {
	// Check SHELL env var
	shell := os.Getenv("SHELL")
	if shell != "" {
		parts := strings.Split(shell, "/")
		return parts[len(parts)-1]
	}

	// Check parent process
	ppid := os.Getppid()
	cmd := exec.Command("ps", "-p", fmt.Sprintf("%d", ppid), "-o", "comm=")
	out, err := cmd.Output()
	if err == nil {
		name := strings.TrimSpace(string(out))
		name = strings.TrimPrefix(name, "-") // login shell
		return name
	}

	return "zsh" // default assumption for macOS
}

// rcFilePath returns the path to the shell's rc file.
func rcFilePath(shell string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch shell {
	case "zsh":
		return filepath.Join(home, ".zshrc")
	case "bash":
		// Prefer .bashrc, fall back to .bash_profile
		rc := filepath.Join(home, ".bashrc")
		if _, err := os.Stat(rc); err == nil {
			return rc
		}
		return filepath.Join(home, ".bash_profile")
	case "fish":
		return filepath.Join(home, ".config", "fish", "config.fish")
	default:
		return ""
	}
}

// hookLine is what we inject into the rc file.
const hookMarker = "# thaw — terminal workspace memory"

func hookLine(shell string) string {
	return fmt.Sprintf("%s\neval \"$(thaw shell-init %s)\"\n", hookMarker, shell)
}

// injectShellHook adds the thaw hook to the rc file if not already present.
func injectShellHook(rcFile, shell string) (bool, error) {
	data, err := os.ReadFile(rcFile)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}

	content := string(data)

	// Already injected?
	if strings.Contains(content, hookMarker) || strings.Contains(content, "thaw shell-init") {
		return false, nil
	}

	// Append hook
	f, err := os.OpenFile(rcFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return false, err
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "\n%s", hookLine(shell))
	return err == nil, err
}

// Uninstall removes thaw hooks from the rc file.
func Uninstall() ([]string, error) {
	var actions []string
	shell := detectShell()
	rcFile := rcFilePath(shell)

	if rcFile != "" {
		removed, err := removeShellHook(rcFile)
		if err != nil {
			return nil, err
		}
		if removed {
			actions = append(actions, fmt.Sprintf("Removed shell hook from %s", rcFile))
		} else {
			actions = append(actions, "No shell hook found to remove")
		}
	}

	return actions, nil
}

func removeShellHook(rcFile string) (bool, error) {
	data, err := os.ReadFile(rcFile)
	if err != nil {
		return false, err
	}

	lines := strings.Split(string(data), "\n")
	var filtered []string
	removed := false
	for _, line := range lines {
		if strings.Contains(line, hookMarker) || strings.Contains(line, "thaw shell-init") {
			removed = true
			continue
		}
		filtered = append(filtered, line)
	}

	if !removed {
		return false, nil
	}

	return true, os.WriteFile(rcFile, []byte(strings.Join(filtered, "\n")), 0644)
}

// --- System service installation ---

func installService() (string, error) {
	thawBin, err := exec.LookPath("thaw")
	if err != nil {
		// Use the current binary path as fallback
		thawBin, err = os.Executable()
		if err != nil {
			return "", fmt.Errorf("cannot find thaw binary")
		}
	}

	switch runtime.GOOS {
	case "darwin":
		return installLaunchd(thawBin)
	case "linux":
		return installSystemd(thawBin)
	default:
		return "", fmt.Errorf("unsupported OS for service install: %s", runtime.GOOS)
	}
}

// installLaunchd creates and loads a macOS LaunchAgent.
func installLaunchd(thawBin string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	plistDir := filepath.Join(home, "Library", "LaunchAgents")
	plistPath := filepath.Join(plistDir, "com.thaw.daemon.plist")

	if _, err := os.Stat(plistPath); err == nil {
		return "LaunchAgent already installed, skipped", nil
	}

	if err := os.MkdirAll(plistDir, 0755); err != nil {
		return "", err
	}

	logDir, _ := config.DataDir()

	plist := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>com.thaw.daemon</string>
    <key>ProgramArguments</key>
    <array>
        <string>%s</string>
        <string>daemon</string>
        <string>start</string>
    </array>
    <key>RunAtLoad</key>
    <true/>
    <key>KeepAlive</key>
    <false/>
    <key>StandardOutPath</key>
    <string>%s/daemon.log</string>
    <key>StandardErrorPath</key>
    <string>%s/daemon.err</string>
</dict>
</plist>
`, thawBin, logDir, logDir)

	if err := os.WriteFile(plistPath, []byte(plist), 0644); err != nil {
		return "", err
	}

	// Load the agent
	exec.Command("launchctl", "load", plistPath).Run()

	return fmt.Sprintf("Installed LaunchAgent at %s", plistPath), nil
}

// installSystemd creates and enables a systemd user service.
func installSystemd(thawBin string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}

	unitDir := filepath.Join(home, ".config", "systemd", "user")
	unitPath := filepath.Join(unitDir, "thaw.service")

	if _, err := os.Stat(unitPath); err == nil {
		return "systemd service already installed, skipped", nil
	}

	if err := os.MkdirAll(unitDir, 0755); err != nil {
		return "", err
	}

	unit := fmt.Sprintf(`[Unit]
Description=Thaw - terminal workspace memory daemon
After=default.target

[Service]
Type=simple
ExecStart=%s daemon start
Restart=on-failure
RestartSec=10

[Install]
WantedBy=default.target
`, thawBin)

	if err := os.WriteFile(unitPath, []byte(unit), 0644); err != nil {
		return "", err
	}

	// Enable and start
	exec.Command("systemctl", "--user", "daemon-reload").Run()
	exec.Command("systemctl", "--user", "enable", "thaw.service").Run()
	exec.Command("systemctl", "--user", "start", "thaw.service").Run()

	return fmt.Sprintf("Installed systemd service at %s", unitPath), nil
}

// UninstallService removes the system service.
func UninstallService() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		home, _ := os.UserHomeDir()
		plistPath := filepath.Join(home, "Library", "LaunchAgents", "com.thaw.daemon.plist")
		exec.Command("launchctl", "unload", plistPath).Run()
		os.Remove(plistPath)
		return "Removed LaunchAgent", nil
	case "linux":
		exec.Command("systemctl", "--user", "stop", "thaw.service").Run()
		exec.Command("systemctl", "--user", "disable", "thaw.service").Run()
		home, _ := os.UserHomeDir()
		unitPath := filepath.Join(home, ".config", "systemd", "user", "thaw.service")
		os.Remove(unitPath)
		exec.Command("systemctl", "--user", "daemon-reload").Run()
		return "Removed systemd service", nil
	default:
		return "", fmt.Errorf("unsupported OS: %s", runtime.GOOS)
	}
}

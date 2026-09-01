package output

import (
	"fmt"
	"os/exec"
	"strings"
)

// CaptureTmuxPane captures the last N lines from a tmux pane.
// paneID is in the format "session:window.pane" or just "%N" for pane ID.
func CaptureTmuxPane(paneID string, lines int) []string {
	// tmux capture-pane captures pane content to a buffer
	// -p prints to stdout, -S specifies start line (negative = from bottom)
	cmd := exec.Command("tmux", "capture-pane", "-t", paneID, "-p", "-S", fmt.Sprintf("-%d", lines))
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	result := splitAndTrim(string(out))

	// Trim trailing empty lines
	for len(result) > 0 && result[len(result)-1] == "" {
		result = result[:len(result)-1]
	}

	return result
}

// CaptureAllPanes captures output from all panes in all tmux sessions.
// Returns a map of pane TTY → output lines.
func CaptureAllPanes(lines int) map[string][]string {
	if !isTmuxAvailable() {
		return nil
	}

	cmd := exec.Command("tmux", "list-panes", "-a", "-F", "#{pane_id}\t#{pane_tty}")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	result := make(map[string][]string)
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) != 2 {
			continue
		}
		paneID := parts[0]
		tty := parts[1]

		captured := CaptureTmuxPane(paneID, lines)
		if len(captured) > 0 {
			result[tty] = captured
		}
	}

	return result
}

// ActivePaneTTY returns the TTY of the currently focused tmux pane.
// Returns empty string if tmux isn't running or no pane is focused.
func ActivePaneTTY() string {
	if !isTmuxAvailable() {
		return ""
	}
	// list-panes with pane_active filter
	cmd := exec.Command("tmux", "list-panes", "-a", "-F", "#{pane_active}\t#{pane_tty}")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 && parts[0] == "1" {
			return parts[1]
		}
	}
	return ""
}

// CaptureForTTY captures output from a tmux pane matching the given TTY.
func CaptureForTTY(tty string, lines int) []string {
	if !isTmuxAvailable() {
		return nil
	}

	// Find pane ID for this TTY
	cmd := exec.Command("tmux", "list-panes", "-a", "-F", "#{pane_id}\t#{pane_tty}")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 && parts[1] == tty {
			return CaptureTmuxPane(parts[0], lines)
		}
	}

	return nil
}

// InsideTmux returns true if we're currently inside a tmux session.
func InsideTmux() bool {
	return exec.Command("tmux", "display-message", "-p", "").Run() == nil
}

func isTmuxAvailable() bool {
	_, err := exec.LookPath("tmux")
	if err != nil {
		return false
	}
	// Also check if there's a running tmux server
	return exec.Command("tmux", "list-sessions").Run() == nil
}

func splitAndTrim(s string) []string {
	lines := strings.Split(s, "\n")
	var result []string
	for _, l := range lines {
		result = append(result, strings.TrimRight(l, " \t"))
	}
	return result
}

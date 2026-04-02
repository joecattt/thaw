package clipboard

import (
	"os/exec"
	"runtime"
	"strings"
)

// Capture returns the current clipboard contents.
// Returns empty string if clipboard is empty or inaccessible.
// Truncates at 500 chars to prevent storing huge clipboard data.
func Capture() string {
	var cmd *exec.Cmd

	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbpaste")
	case "linux":
		// Try xclip, then xsel, then wl-paste (wayland)
		if _, err := exec.LookPath("xclip"); err == nil {
			cmd = exec.Command("xclip", "-selection", "clipboard", "-o")
		} else if _, err := exec.LookPath("xsel"); err == nil {
			cmd = exec.Command("xsel", "--clipboard", "--output")
		} else if _, err := exec.LookPath("wl-paste"); err == nil {
			cmd = exec.Command("wl-paste", "--no-newline")
		} else {
			return ""
		}
	default:
		return ""
	}

	out, err := cmd.Output()
	if err != nil {
		return ""
	}

	result := strings.TrimSpace(string(out))

	// Don't store if it looks like a credential
	if looksLikeSecret(result) {
		return ""
	}

	// Truncate
	if len(result) > 500 {
		result = result[:497] + "..."
	}

	return result
}

func looksLikeSecret(s string) bool {
	lower := strings.ToLower(s)
	// Long alphanumeric strings are likely tokens
	if len(s) > 40 {
		alpha := 0
		for _, c := range s {
			if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
				alpha++
			}
		}
		if float64(alpha)/float64(len(s)) > 0.9 {
			return true
		}
	}
	prefixes := []string{"sk-", "pk-", "ghp_", "gho_", "glpat-", "xoxb-", "xoxp-", "Bearer "}
	for _, p := range prefixes {
		if strings.HasPrefix(s, p) || strings.HasPrefix(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
}

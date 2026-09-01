package browser

import (
	"os/exec"
	"runtime"
	"strings"
)

// CaptureTabs returns URLs of currently open browser tabs.
// macOS: uses osascript to query Chrome and Safari.
// Linux: reads Chrome's session file (best effort).
func CaptureTabs() []string {
	switch runtime.GOOS {
	case "darwin":
		return captureMacOS()
	case "linux":
		return captureLinux()
	default:
		return nil
	}
}

func captureMacOS() []string {
	var tabs []string

	// Chrome
	chromeScript := `tell application "System Events" to if exists process "Google Chrome" then
		tell application "Google Chrome"
			set urls to {}
			repeat with w in windows
				repeat with t in tabs of w
					set end of urls to URL of t
				end repeat
			end repeat
			return urls
		end tell
	end if`
	out, err := exec.Command("osascript", "-e", chromeScript).Output()
	if err == nil {
		for _, url := range strings.Split(strings.TrimSpace(string(out)), ", ") {
			url = strings.TrimSpace(url)
			if url != "" && strings.HasPrefix(url, "http") {
				tabs = append(tabs, url)
			}
		}
	}

	// Safari
	safariScript := `tell application "System Events" to if exists process "Safari" then
		tell application "Safari"
			set urls to {}
			repeat with w in windows
				repeat with t in tabs of w
					set end of urls to URL of t
				end repeat
			end repeat
			return urls
		end tell
	end if`
	out, err = exec.Command("osascript", "-e", safariScript).Output()
	if err == nil {
		for _, url := range strings.Split(strings.TrimSpace(string(out)), ", ") {
			url = strings.TrimSpace(url)
			if url != "" && strings.HasPrefix(url, "http") {
				tabs = append(tabs, url)
			}
		}
	}

	return dedup(tabs)
}

func captureLinux() []string {
	// Try wmctrl to get browser window titles
	out, err := exec.Command("wmctrl", "-l").Output()
	if err != nil {
		return nil
	}

	var tabs []string
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Fields(line)
		if len(parts) >= 5 {
			title := strings.Join(parts[4:], " ")
			if strings.Contains(title, "Chrome") || strings.Contains(title, "Firefox") {
				tabs = append(tabs, title)
			}
		}
	}

	return tabs
}

func dedup(items []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, item := range items {
		if !seen[item] && !urlContainsCredential(item) {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

// urlContainsCredential checks if a URL contains session tokens, API keys, or credentials.
func urlContainsCredential(url string) bool {
	lower := strings.ToLower(url)
	// Query params that often contain credentials
	sensitiveParams := []string{
		"token=", "access_token=", "api_key=", "apikey=",
		"secret=", "password=", "passwd=", "auth=",
		"session_id=", "sessionid=", "jwt=", "bearer=",
		"code=", "refresh_token=", "client_secret=",
	}
	for _, p := range sensitiveParams {
		if strings.Contains(lower, p) {
			return true
		}
	}
	// Basic auth in URL: https://user:pass@host
	if strings.Contains(url, "://") && strings.Contains(url, "@") {
		schemeEnd := strings.Index(url, "://")
		rest := url[schemeEnd+3:]
		if strings.Contains(rest, ":") && strings.Contains(rest, "@") {
			at := strings.Index(rest, "@")
			colon := strings.Index(rest, ":")
			if colon < at {
				return true
			}
		}
	}
	return false
}

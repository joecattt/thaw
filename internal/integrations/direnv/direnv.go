package direnv

import (
	"os"
	"os/exec"
	"path/filepath"
)

// Available returns true if direnv is installed.
func Available() bool {
	_, err := exec.LookPath("direnv")
	return err == nil
}

// HasEnvrc returns true if the directory (or any parent up to root) has a .envrc file.
func HasEnvrc(dir string) bool {
	current := dir
	for current != "/" && current != "." && current != "" {
		if _, err := os.Stat(filepath.Join(current, ".envrc")); err == nil {
			return true
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return false
}

// EnvrcPath returns the path to the .envrc file for the directory, or empty string.
func EnvrcPath(dir string) string {
	current := dir
	for current != "/" && current != "." && current != "" {
		p := filepath.Join(current, ".envrc")
		if _, err := os.Stat(p); err == nil {
			return p
		}
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}
	return ""
}

// IsAllowed checks if the .envrc in the directory is already allowed by direnv.
// Returns true if allowed or if direnv is not available.
func IsAllowed(dir string) bool {
	if !Available() {
		return true
	}
	envrc := EnvrcPath(dir)
	if envrc == "" {
		return true
	}
	cmd := exec.Command("direnv", "status")
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		return false
	}
	// direnv status output contains "Found RC allowed true" when allowed
	return containsAllowed(string(out))
}

func containsAllowed(status string) bool {
	// Simple check — direnv status output varies by version
	for _, line := range splitLines(status) {
		if contains(line, "Found RC allowed") && contains(line, "true") {
			return true
		}
		if contains(line, "Allowed") && contains(line, "true") {
			return true
		}
	}
	return false
}

// AllowCommand returns the shell command to allow direnv for a directory.
func AllowCommand(dir string) string {
	return "direnv allow " + dir
}

// RestoreCommands returns the tmux send-keys commands needed to activate direnv.
func RestoreCommands(dir string) []string {
	if !HasEnvrc(dir) {
		return nil
	}
	// direnv hook handles loading automatically on cd, but we need to
	// ensure it's allowed and trigger an eval
	return []string{
		"eval \"$(direnv export " + shellName() + ")\" 2>/dev/null",
	}
}

func shellName() string {
	shell := os.Getenv("SHELL")
	if shell == "" {
		return "bash"
	}
	base := filepath.Base(shell)
	switch base {
	case "zsh", "bash", "fish":
		return base
	default:
		return "bash"
	}
}

func splitLines(s string) []string {
	var lines []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && searchString(s, substr)
}

func searchString(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

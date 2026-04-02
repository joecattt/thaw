package history

import (
	"bufio"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// ForSession extracts the most recent commands associated with a specific shell PID.
// Priority: 1) Atuin (best per-session data) 2) thaw's own log 3) global shell history
func ForSession(pid int, shell string, cwd string, limit int) []string {
	// Try thaw's own PID-specific session log first (most precise)
	if cmds := fromSessionLog(pid, limit); len(cmds) > 0 {
		return cmds
	}

	// Try atuin — but only for specific project directories, not home dir
	// Home directory returns global history which is useless per-session
	home, _ := os.UserHomeDir()
	if cwd != home && cwd != "" {
		if cmds := fromAtuin(cwd, limit); len(cmds) > 0 {
			return cmds
		}
	}

	// Fall back to shell's global history (not per-session)
	return fromGlobalHistory(shell, limit)
}

// fromAtuin reads history from atuin's database if available.
func fromAtuin(cwd string, limit int) []string {
	if !atuinAvailable() {
		return nil
	}
	return atuinHistory(cwd, limit)
}

// fromSessionLog reads from thaw's per-session command log.
// Format: TIMESTAMP|PID|CWD|COMMAND
func fromSessionLog(pid int, limit int) []string {
	logDir := stateDir()
	logPath := filepath.Join(logDir, "commands.log")

	f, err := os.Open(logPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	pidStr := strconv.Itoa(pid)
	var matches []string

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}
		if parts[1] == pidStr && parts[3] != "" {
			matches = append(matches, parts[3])
		}
	}

	// Return the last N
	if len(matches) > limit {
		matches = matches[len(matches)-limit:]
	}
	return matches
}

// fromGlobalHistory reads the tail of the shell's history file.
// This is imprecise — it captures recent commands across all sessions.
func fromGlobalHistory(shell string, limit int) []string {
	histPath := historyFilePath(shell)
	if histPath == "" {
		return nil
	}

	f, err := os.Open(histPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	// Read all lines (history files are usually not enormous)
	var lines []string
	scanner := bufio.NewScanner(f)
	buf := make([]byte, 0, 1024*1024) // 1MB buffer for large history files
	scanner.Buffer(buf, 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		// Skip zsh extended history timestamps (: TIMESTAMP:0;COMMAND)
		if strings.HasPrefix(line, ": ") && strings.Contains(line, ";") {
			idx := strings.Index(line, ";")
			line = line[idx+1:]
		}
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}

	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	return lines
}

// historyFilePath returns the path to the shell's history file.
func historyFilePath(shell string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}

	// Check HISTFILE env var first
	if hf := os.Getenv("HISTFILE"); hf != "" {
		return hf
	}

	switch shell {
	case "zsh":
		return filepath.Join(home, ".zsh_history")
	case "bash":
		return filepath.Join(home, ".bash_history")
	case "fish":
		return filepath.Join(home, ".local", "share", "fish", "fish_history")
	default:
		return filepath.Join(home, "."+shell+"_history")
	}
}

// stateDir returns the XDG state directory for thaw.
func stateDir() string {
	if d := os.Getenv("XDG_STATE_HOME"); d != "" {
		return filepath.Join(d, "thaw")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		// Never fall back to /tmp — it's world-readable
		return filepath.Join("/var/tmp", fmt.Sprintf("thaw-%d", os.Getuid()))
	}
	return filepath.Join(home, ".local", "state", "thaw")
}

// LogCommand writes a command entry to the per-session command log.
func LogCommand(pid int, cwd, command string) error {
	dir := stateDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("creating state dir: %w", err)
	}

	logPath := filepath.Join(dir, "commands.log")

	// Auto-rotate if log exceeds 5MB
	if info, err := os.Stat(logPath); err == nil && info.Size() > 5*1024*1024 {
		RotateLog(10000)
	}

	// Scrub credentials from command before logging
	command = scrubCommand(command)

	// Filter trivial commands that add noise
	if isTrivialCommand(command) {
		return nil
	}

	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("opening command log: %w", err)
	}
	defer f.Close()

	_, err = fmt.Fprintf(f, "%d|%d|%s|%s\n",
		unixNow(), pid, cwd, command)
	return err
}

// scrubCommand redacts credentials from commands before logging.
// Handles: flag arguments, Bearer tokens, export assignments, inline secrets.
func scrubCommand(cmd string) string {
	parts := strings.Fields(cmd)
	if len(parts) == 0 {
		return cmd
	}

	secretFlags := map[string]bool{
		"-p": true, "--password": true, "--passwd": true,
		"--token": true, "--secret": true, "--api-key": true,
		"--auth": true, "--credentials": true, "-P": true, "--pass": true,
	}

	var result []string
	skipNext := false
	for i, part := range parts {
		if skipNext {
			result = append(result, "[redacted]")
			skipNext = false
			continue
		}

		// --flag=value patterns
		for flag := range secretFlags {
			if strings.HasPrefix(part, flag+"=") {
				part = flag + "=[redacted]"
				break
			}
		}

		// Flag followed by value
		if secretFlags[part] {
			result = append(result, part)
			skipNext = true
			continue
		}

		// "Bearer <token>" or "Authorization: Bearer <token>" in -H args
		lower := strings.ToLower(part)
		if lower == "bearer" && i+1 < len(parts) {
			result = append(result, part)
			skipNext = true
			continue
		}

		// export KEY=value where KEY looks like a secret
		if (parts[0] == "export" || parts[0] == "set") && i == 1 && strings.Contains(part, "=") {
			eqIdx := strings.Index(part, "=")
			keyName := strings.ToUpper(part[:eqIdx])
			secretKeywords := []string{"SECRET", "TOKEN", "PASSWORD", "KEY", "CREDENTIAL", "AUTH"}
			for _, kw := range secretKeywords {
				if strings.Contains(keyName, kw) {
					part = part[:eqIdx+1] + "[redacted]"
					break
				}
			}
		}

		// Inline values that look like tokens (long alphanumeric strings > 30 chars)
		if len(part) > 30 && !strings.Contains(part, "/") && !strings.Contains(part, ".") {
			alnum := 0
			for _, c := range part {
				if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-' || c == '_' {
					alnum++
				}
			}
			if float64(alnum)/float64(len(part)) > 0.85 {
				result = append(result, "[redacted-token]")
				continue
			}
		}

		result = append(result, part)
	}

	return strings.Join(result, " ")
}

// isTrivialCommand returns true for commands that don't carry meaningful context.
func isTrivialCommand(cmd string) bool {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return true
	}
	trivial := []string{
		"ls", "ll", "la", "l", "pwd", "clear", "cls", "reset",
		"whoami", "date", "cal", "uptime", "w", "which", "where",
		"true", "false", ":", "history",
	}
	base := strings.Fields(cmd)[0]
	for _, t := range trivial {
		if base == t {
			return true
		}
	}
	return false
}

// RotateLog trims the command log to the last N lines.
func RotateLog(keepLines int) error {
	logPath := filepath.Join(stateDir(), "commands.log")

	data, err := os.ReadFile(logPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) <= keepLines {
		return nil
	}

	// Keep only the last N lines
	lines = lines[len(lines)-keepLines:]
	return os.WriteFile(logPath, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func unixNow() int64 {
	return time.Now().Unix()
}

func atuinAvailable() bool {
	_, err := osexec.LookPath("atuin")
	return err == nil
}

// atuinHistory reads from atuin CLI. Preferred over direct DB access since
// atuin handles its own schema versioning.
func atuinHistory(cwd string, limit int) []string {
	cmd := osexec.Command("atuin", "history", "list", "--cwd", cwd, "--cmd-only")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	var result []string
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			result = append(result, l)
		}
	}
	return result
}

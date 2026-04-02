package stale

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/git"
	"github.com/joecattt/thaw/pkg/models"
)

// Check validates whether a session's context is still valid for restore.
func Check(session models.Session) models.StaleCheck {
	sc := models.StaleCheck{
		CWDExists:      true,
		BinaryExists:   true,
		GitBranchMatch: true,
		Reachable:      true,
	}

	// 1. Does the working directory still exist?
	if _, err := os.Stat(session.CWD); os.IsNotExist(err) {
		sc.CWDExists = false
		sc.Reason = appendReason(sc.Reason, fmt.Sprintf("directory %s no longer exists", session.CWD))
	}

	// 2. Is the command's binary still available?
	if !session.IsIdle() {
		binary := extractBinary(session.Command)
		if binary != "" {
			if _, err := exec.LookPath(binary); err != nil {
				sc.BinaryExists = false
				sc.Reason = appendReason(sc.Reason, fmt.Sprintf("binary %q not found in PATH", binary))
			}
		}
	}

	// 3. Has the git branch changed?
	if session.Git != nil && session.Git.Branch != "" && sc.CWDExists {
		currentBranch := git.CurrentBranch(session.CWD)
		if currentBranch != "" && currentBranch != session.Git.Branch {
			sc.GitBranchMatch = false
			sc.Reason = appendReason(sc.Reason,
				fmt.Sprintf("branch changed: %s → %s", session.Git.Branch, currentBranch))
		}
	}

	// 4. For SSH sessions, is the target reachable?
	if isSSHCommand(session.Command) {
		host := extractSSHHost(session.Command)
		if host != "" {
			sc.Reachable = isHostReachable(host)
			if !sc.Reachable {
				sc.Reason = appendReason(sc.Reason, fmt.Sprintf("host %s unreachable", host))
			}
		}
	}

	return sc
}

// CheckAll runs staleness checks on all sessions in a snapshot.
// Returns a map of session PID → StaleCheck.
func CheckAll(snap *models.Snapshot) map[int]models.StaleCheck {
	results := make(map[int]models.StaleCheck)
	for _, s := range snap.Sessions {
		results[s.PID] = Check(s)
	}
	return results
}

// extractBinary pulls the first token from a command string as the binary name.
func extractBinary(command string) string {
	command = strings.TrimSpace(command)
	if command == "" {
		return ""
	}

	// Handle common prefixes: env, sudo, nohup, etc.
	prefixes := []string{"env", "sudo", "nohup", "nice", "time", "strace", "ltrace"}
	parts := strings.Fields(command)
	for len(parts) > 1 {
		found := false
		for _, p := range prefixes {
			if parts[0] == p {
				parts = parts[1:]
				found = true
				break
			}
		}
		// Skip env-style KEY=VALUE arguments
		if !found && strings.Contains(parts[0], "=") {
			parts = parts[1:]
			continue
		}
		if !found {
			break
		}
	}

	if len(parts) == 0 {
		return ""
	}
	return parts[0]
}

// isSSHCommand returns true if the command appears to be an SSH session.
func isSSHCommand(command string) bool {
	parts := strings.Fields(command)
	for _, p := range parts {
		if p == "ssh" || strings.HasSuffix(p, "/ssh") {
			return true
		}
	}
	return false
}

// extractSSHHost extracts the hostname from an SSH command.
func extractSSHHost(command string) string {
	parts := strings.Fields(command)
	for i, p := range parts {
		if (p == "ssh" || strings.HasSuffix(p, "/ssh")) && i+1 < len(parts) {
			// Walk forward past flags to find the host
			for j := i + 1; j < len(parts); j++ {
				arg := parts[j]
				if strings.HasPrefix(arg, "-") {
					// Some flags take an argument — skip it
					if len(arg) == 2 && strings.ContainsAny(string(arg[1]), "bcDEeFIiJLlmOopQRSWw") {
						j++ // skip the next arg (flag value)
					}
					continue
				}
				// This should be user@host or just host
				host := arg
				if idx := strings.LastIndex(host, "@"); idx >= 0 {
					host = host[idx+1:]
				}
				return host
			}
		}
	}
	return ""
}

// isHostReachable does a quick TCP dial to check if a host is up.
func isHostReachable(host string) bool {
	// Default SSH port
	if !strings.Contains(host, ":") {
		host = host + ":22"
	}
	conn, err := net.DialTimeout("tcp", host, 3*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

func appendReason(existing, addition string) string {
	if existing == "" {
		return addition
	}
	return existing + "; " + addition
}

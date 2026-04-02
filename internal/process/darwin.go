//go:build darwin

package process

import (
	"fmt"
	"os/exec"
	"strconv"
	"strings"

	"github.com/joecattt/thaw/pkg/models"
)

// DarwinDiscovery implements Discovery for macOS.
type DarwinDiscovery struct{}

// NewDiscovery returns the platform-appropriate Discovery implementation.
func NewDiscovery() Discovery {
	return &DarwinDiscovery{}
}

// ListShells finds all interactive shell sessions with a TTY.
func (d *DarwinDiscovery) ListShells() ([]ShellInfo, error) {
	out, err := runPS("-e", "-o", "pid,ppid,tty,comm")
	if err != nil {
		return nil, err
	}

	var shells []ShellInfo
	lines := strings.Split(strings.TrimSpace(out), "\n")

	for _, line := range lines[1:] {
		pid, _, tty, command, err := parsePSLine(line)
		if err != nil {
			continue
		}
		if tty == "??" || tty == "-" || tty == "" {
			continue
		}
		if !isShell(command) {
			continue
		}
		if IsSelfProcess(pid) {
			continue
		}
		shells = append(shells, ShellInfo{
			PID:     pid,
			TTY:     tty,
			Shell:   shellBasename(command),
			Command: command,
		})
	}

	return shells, nil
}

// Children returns child processes of a given PID.
func (d *DarwinDiscovery) Children(pid int) ([]models.Process, error) {
	out, err := runPS("-e", "-o", "pid,ppid,stat,comm")
	if err != nil {
		return nil, err
	}

	type psEntry struct {
		pid     int
		ppid    int
		status  string
		command string
	}

	var entries []psEntry
	lines := strings.Split(strings.TrimSpace(out), "\n")
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		p, _ := strconv.Atoi(fields[0])
		pp, _ := strconv.Atoi(fields[1])
		entries = append(entries, psEntry{
			pid:     p,
			ppid:    pp,
			status:  fields[2],
			command: strings.Join(fields[3:], " "),
		})
	}

	var children []models.Process
	queue := []int{pid}
	visited := map[int]bool{pid: true}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, e := range entries {
			if e.ppid == current && !visited[e.pid] {
				visited[e.pid] = true
				queue = append(queue, e.pid)

				status := "running"
				if strings.Contains(e.status, "T") {
					status = "stopped"
				} else if strings.Contains(e.status, "S") {
					status = "sleeping"
				}

				children = append(children, models.Process{
					PID:     e.pid,
					PPID:    e.ppid,
					Command: e.command,
					Status:  status,
				})
			}
		}
	}

	return children, nil
}

// CWD returns the current working directory of a process on macOS.
func (d *DarwinDiscovery) CWD(pid int) (string, error) {
	cmd := exec.Command("lsof", "-a", "-p", strconv.Itoa(pid), "-d", "cwd", "-Fn")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("lsof cwd for pid %d: %w", pid, err)
	}

	for _, line := range strings.Split(string(out), "\n") {
		if strings.HasPrefix(line, "n/") {
			return strings.TrimPrefix(line, "n"), nil
		}
	}

	return "", fmt.Errorf("cwd not found for pid %d", pid)
}

// Environ reads the environment of a process on macOS.
// macOS restricts this for processes owned by other users and under SIP.
// Uses `ps eww` which shows env vars appended to the command line.
// This is best-effort — SIP may block access to some processes.
func (d *DarwinDiscovery) Environ(pid int) (map[string]string, error) {
	cmd := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "command=", "eww")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("reading environ for pid %d: %w", pid, err)
	}

	env := make(map[string]string)
	// ps eww output: COMMAND arg1 arg2 ENV1=val1 ENV2=val2 ...
	// We find where env vars start by looking for KEY=VALUE patterns after the command
	parts := strings.Fields(strings.TrimSpace(string(out)))

	// Walk from the end backward — env vars are at the tail
	for _, part := range parts {
		idx := strings.IndexByte(part, '=')
		if idx <= 0 {
			continue
		}
		key := part[:idx]
		// Env var keys are uppercase alphanumeric + underscore
		isEnv := true
		for _, c := range key {
			if !((c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '_') {
				isEnv = false
				break
			}
		}
		if isEnv && len(key) > 1 {
			env[key] = part[idx+1:]
		}
	}

	return env, nil
}

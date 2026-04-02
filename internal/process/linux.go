//go:build linux

package process

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/joecattt/thaw/pkg/models"
)

// LinuxDiscovery implements Discovery for Linux using /proc.
type LinuxDiscovery struct{}

// NewDiscovery returns the platform-appropriate Discovery implementation.
func NewDiscovery() Discovery {
	return &LinuxDiscovery{}
}

// ListShells finds all interactive shell sessions with a TTY.
func (d *LinuxDiscovery) ListShells() ([]ShellInfo, error) {
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
		if tty == "?" || tty == "-" || tty == "" {
			continue
		}
		if !isShell(command) {
			continue
		}
		// Skip thaw's own shell — prevents capturing ourselves
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

// Children returns child processes of a given PID using /proc.
func (d *LinuxDiscovery) Children(pid int) ([]models.Process, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("reading /proc: %w", err)
	}

	type procInfo struct {
		pid     int
		ppid    int
		command string
		status  string
	}

	var procs []procInfo
	for _, entry := range entries {
		p, err := strconv.Atoi(entry.Name())
		if err != nil {
			continue
		}

		statusPath := filepath.Join("/proc", entry.Name(), "status")
		data, err := os.ReadFile(statusPath)
		if err != nil {
			continue
		}

		var ppid int
		var name, state string
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "Name:") {
				name = strings.TrimSpace(strings.TrimPrefix(line, "Name:"))
			}
			if strings.HasPrefix(line, "PPid:") {
				ppid, _ = strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(line, "PPid:")))
			}
			if strings.HasPrefix(line, "State:") {
				state = strings.TrimSpace(strings.TrimPrefix(line, "State:"))
			}
		}

		procs = append(procs, procInfo{pid: p, ppid: ppid, command: name, status: state})
	}

	var children []models.Process
	queue := []int{pid}
	visited := map[int]bool{pid: true}

	for len(queue) > 0 {
		current := queue[0]
		queue = queue[1:]

		for _, p := range procs {
			if p.ppid == current && !visited[p.pid] {
				visited[p.pid] = true
				queue = append(queue, p.pid)

				status := "running"
				if strings.HasPrefix(p.status, "T") {
					status = "stopped"
				} else if strings.HasPrefix(p.status, "S") {
					status = "sleeping"
				}

				children = append(children, models.Process{
					PID:     p.pid,
					PPID:    p.ppid,
					Command: p.command,
					Status:  status,
				})
			}
		}
	}

	return children, nil
}

// CWD returns the current working directory of a process on Linux.
func (d *LinuxDiscovery) CWD(pid int) (string, error) {
	link := filepath.Join("/proc", strconv.Itoa(pid), "cwd")
	target, err := os.Readlink(link)
	if err != nil {
		return "", fmt.Errorf("readlink %s: %w", link, err)
	}
	return target, nil
}

// Environ reads the environment of a process from /proc/PID/environ.
func (d *LinuxDiscovery) Environ(pid int) (map[string]string, error) {
	path := filepath.Join("/proc", strconv.Itoa(pid), "environ")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading environ for pid %d: %w", pid, err)
	}

	env := make(map[string]string)
	// /proc/PID/environ is null-byte delimited
	for _, entry := range strings.Split(string(data), "\x00") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		idx := strings.IndexByte(entry, '=')
		if idx < 0 {
			continue
		}
		env[entry[:idx]] = entry[idx+1:]
	}

	return env, nil
}

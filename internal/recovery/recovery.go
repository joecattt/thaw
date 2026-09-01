package recovery

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/pkg/models"
)

// Check detects if the last shutdown was unexpected (crash/power loss)
// and attempts to recover workspace state from the command log.
// Returns a recovered snapshot if successful, nil if no recovery needed.
func Check(store *snapshot.Store) (*models.Snapshot, error) {
	bootTime := getBootTime()
	if bootTime.IsZero() {
		return nil, nil
	}

	latest, err := store.Latest()
	if err != nil {
		return nil, err
	}

	// If latest snapshot exists and is AFTER boot time, no recovery needed
	if latest != nil && latest.CreatedAt.After(bootTime) {
		return nil, nil
	}

	// If latest snapshot is before boot time, we had an unclean shutdown.
	// Try to reconstruct from command log.
	logPath := commandLogPath()
	if _, err := os.Stat(logPath); err != nil {
		return nil, nil // no log, nothing to recover
	}

	snap, err := recoverFromLog(logPath, bootTime)
	if err != nil {
		return nil, fmt.Errorf("recovery failed: %w", err)
	}
	if snap == nil || len(snap.Sessions) == 0 {
		return nil, nil
	}

	// Save the recovered snapshot
	snap.Source = "recovered"
	if err := store.Save(snap); err != nil {
		return nil, fmt.Errorf("saving recovered snapshot: %w", err)
	}

	return snap, nil
}

// recoverFromLog reads the command log and reconstructs the last-known state
// for each shell PID that was active before the crash.
func recoverFromLog(logPath string, bootTime time.Time) (*models.Snapshot, error) {
	f, err := os.Open(logPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	// Track last-known state per PID
	type pidState struct {
		pid     int
		cwd     string
		command string
		lastSeen time.Time
	}
	states := make(map[int]*pidState)

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		// Format: TIMESTAMP|PID|CWD|COMMAND
		parts := strings.SplitN(line, "|", 4)
		if len(parts) < 4 {
			continue
		}

		ts, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			continue
		}
		entryTime := time.Unix(ts, 0)

		// Only consider entries from before the crash (before boot time)
		if !bootTime.IsZero() && entryTime.After(bootTime) {
			continue
		}

		pid, err := strconv.Atoi(parts[1])
		if err != nil {
			continue
		}

		cwd := parts[2]
		command := parts[3]

		states[pid] = &pidState{
			pid:      pid,
			cwd:      cwd,
			command:  command,
			lastSeen: entryTime,
		}
	}

	if len(states) == 0 {
		return nil, nil
	}

	// Filter to only PIDs active in the last hour before crash
	var cutoff time.Time
	if !bootTime.IsZero() {
		cutoff = bootTime.Add(-1 * time.Hour)
	} else {
		cutoff = time.Now().Add(-1 * time.Hour)
	}

	var sessions []models.Session
	for _, state := range states {
		if state.lastSeen.Before(cutoff) {
			continue // too old, probably already closed
		}
		sessions = append(sessions, models.Session{
			PID:        state.pid,
			CWD:        state.cwd,
			Command:    state.command,
			Shell:      "recovered",
			Label:      filepath.Base(state.cwd),
			Status:     "recovered",
			CapturedAt: state.lastSeen,
		})
	}

	if len(sessions) == 0 {
		return nil, nil
	}

	hostname, _ := os.Hostname()
	return &models.Snapshot{
		Sessions:  sessions,
		CreatedAt: time.Now(),
		Source:    "recovered",
		Hostname:  hostname,
		Intent:    "recovered after unexpected shutdown",
	}, nil
}

// getBootTime returns the system boot time.
func getBootTime() time.Time {
	// Linux: /proc/stat has btime
	data, err := os.ReadFile("/proc/stat")
	if err == nil {
		for _, line := range strings.Split(string(data), "\n") {
			if strings.HasPrefix(line, "btime ") {
				parts := strings.Fields(line)
				if len(parts) >= 2 {
					ts, err := strconv.ParseInt(parts[1], 10, 64)
					if err == nil {
						return time.Unix(ts, 0)
					}
				}
			}
		}
	}

	// Linux fallback: /proc/uptime
	data, err = os.ReadFile("/proc/uptime")
	if err == nil {
		parts := strings.Fields(string(data))
		if len(parts) >= 1 {
			uptime, err := strconv.ParseFloat(parts[0], 64)
			if err == nil {
				return time.Now().Add(-time.Duration(uptime * float64(time.Second)))
			}
		}
	}

	// macOS: sysctl kern.boottime → "{ sec = 1234567890, usec = 0 } ..."
	out, err := exec.Command("sysctl", "-n", "kern.boottime").Output()
	if err == nil {
		s := string(out)
		// Parse "{ sec = NNNN, usec = NNNN } ..."
		if idx := strings.Index(s, "sec = "); idx >= 0 {
			rest := s[idx+6:]
			if comma := strings.Index(rest, ","); comma > 0 {
				ts, err := strconv.ParseInt(strings.TrimSpace(rest[:comma]), 10, 64)
				if err == nil {
					return time.Unix(ts, 0)
				}
			}
		}
	}

	return time.Time{} // can't determine
}

func commandLogPath() string {
	home, _ := os.UserHomeDir()
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		stateDir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateDir, "thaw", "commands.log")
}

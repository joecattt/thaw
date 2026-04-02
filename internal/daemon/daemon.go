package daemon

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/joecattt/thaw/internal/capture"
	"github.com/joecattt/thaw/internal/config"
	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/pkg/models"
)

// Run starts the background snapshot daemon.
// Adapts snapshot frequency based on user activity:
// - Active (commands in last 5 min): snapshot every `interval`
// - Idle (no commands in 30+ min): snapshot every 6x interval
func Run(engine *capture.Engine, interval time.Duration) error {
	if err := config.EnsureDirectories(); err != nil {
		return err
	}

	if err := writePIDFile(); err != nil {
		return fmt.Errorf("writing pid file: %w", err)
	}
	defer removePIDFile()

	store, err := snapshot.Open()
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer store.Close()

	var lastHash string

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)

	activeInterval := interval
	idleInterval := interval * 6
	currentInterval := activeInterval

	ticker := time.NewTicker(currentInterval)
	defer ticker.Stop()

	log.Printf("thaw daemon started (active: %s, idle: %s)", activeInterval, idleInterval)

	lastHash = doSnapshot(engine, store, lastHash)

	for {
		select {
		case <-ticker.C:
			lastHash = doSnapshot(engine, store, lastHash)

			// Adaptive frequency: check if user is active
			newInterval := activeInterval
			if !isUserActive() {
				newInterval = idleInterval
			}
			if newInterval != currentInterval {
				currentInterval = newInterval
				ticker.Reset(currentInterval)
				if currentInterval == idleInterval {
					log.Printf("user idle, slowing to %s", idleInterval)
				} else {
					log.Printf("user active, speeding to %s", activeInterval)
				}
			}

		case sig := <-sigCh:
			log.Printf("received %s, taking final snapshot", sig)
			doSnapshot(engine, store, "")
			log.Println("daemon stopped")
			return nil
		}
	}
}

// isUserActive checks if the user has run any commands recently
// by reading the last modification time of the command log.
func isUserActive() bool {
	logPath := commandLogPath()
	info, err := os.Stat(logPath)
	if err != nil {
		return false // no log = no activity tracking = assume idle
	}
	// Active if log was modified in the last 5 minutes
	return time.Since(info.ModTime()) < 5*time.Minute
}

func commandLogPath() string {
	home, _ := os.UserHomeDir()
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		stateDir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(stateDir, "thaw", "commands.log")
}

// writeHeartbeat updates the daemon heartbeat file with the current timestamp.
func writeHeartbeat() {
	home, _ := os.UserHomeDir()
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		stateDir = filepath.Join(home, ".local", "state")
	}
	dir := filepath.Join(stateDir, "thaw")
	os.MkdirAll(dir, 0700)
	os.WriteFile(filepath.Join(dir, "daemon.heartbeat"), []byte(fmt.Sprintf("%d", time.Now().Unix())), 0600)
}

// HeartbeatAge returns how long since the daemon last reported. Returns -1 if no heartbeat.
func HeartbeatAge() time.Duration {
	home, _ := os.UserHomeDir()
	stateDir := os.Getenv("XDG_STATE_HOME")
	if stateDir == "" {
		stateDir = filepath.Join(home, ".local", "state")
	}
	info, err := os.Stat(filepath.Join(stateDir, "thaw", "daemon.heartbeat"))
	if err != nil {
		return -1
	}
	return time.Since(info.ModTime())
}

// doSnapshot captures state and saves if changed. Returns the new hash.
func doSnapshot(engine *capture.Engine, store *snapshot.Store, lastHash string) string {
	writeHeartbeat()
	snap, err := engine.Capture("scheduled")
	if err != nil {
		log.Printf("capture error: %v", err)
		return lastHash
	}

	if len(snap.Sessions) == 0 {
		return lastHash
	}

	// Compute a simple hash of the session state to detect changes
	hash := hashSnapshot(snap)
	if hash == lastHash {
		return lastHash // nothing changed
	}

	if err := store.Save(snap); err != nil {
		log.Printf("save error: %v", err)
		return lastHash
	}

	log.Printf("snapshot #%d saved (%d sessions)", snap.ID, len(snap.Sessions))

	// Prune old scheduled snapshots (keep last 100, older than 7 days)
	if pruned, err := store.Prune(7*24*time.Hour, 100); err == nil && pruned > 0 {
		log.Printf("pruned %d old snapshots", pruned)
	}

	return hash
}

// hashSnapshot creates a deterministic fingerprint for change detection.
// Uses sorted keys to avoid Go map iteration randomization false positives.
func hashSnapshot(snap *models.Snapshot) string {
	// Build a deterministic string from session state
	var parts []string
	for _, s := range snap.Sessions {
		parts = append(parts, s.CWD+"|"+s.Command)
	}
	// Sort for determinism
	sortStrings(parts)

	// Simple FNV-style hash
	var h uint64 = 14695981039346656037
	for _, p := range parts {
		for _, c := range p {
			h ^= uint64(c)
			h *= 1099511628211
		}
	}
	return strconv.FormatUint(h, 36)
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// PID file management

func pidFilePath() string {
	dataDir, err := config.DataDir()
	if err != nil {
		// Never fall back to /tmp — predictable path = security risk
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".thaw-daemon.pid")
	}
	return filepath.Join(dataDir, "daemon.pid")
}

func writePIDFile() error {
	path := pidFilePath()
	// Restricted permissions — owner only
	return os.WriteFile(path, []byte(strconv.Itoa(os.Getpid())), 0600)
}

func removePIDFile() {
	os.Remove(pidFilePath())
}

// IsRunning checks if the daemon is running AND is actually a thaw process.
func IsRunning() (bool, int) {
	path := pidFilePath()

	// Check for symlink attack — PID file must be a regular file
	info, err := os.Lstat(path)
	if err != nil {
		return false, 0
	}
	if info.Mode()&os.ModeSymlink != 0 {
		os.Remove(path) // remove symlink
		return false, 0
	}

	data, err := os.ReadFile(path)
	if err != nil {
		return false, 0
	}

	pidStr := strings.TrimSpace(string(data))
	pid, err := strconv.Atoi(pidStr)
	if err != nil || pid <= 0 {
		return false, 0
	}

	// Verify process exists
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false, 0
	}
	if err := proc.Signal(syscall.Signal(0)); err != nil {
		os.Remove(path)
		return false, 0
	}

	// Verify it's actually thaw — check /proc/PID/cmdline on Linux
	if isThawProcess(pid) {
		return true, pid
	}

	// If we can't verify, assume stale
	os.Remove(path)
	return false, 0
}

// isThawProcess checks if a PID belongs to a thaw daemon.
func isThawProcess(pid int) bool {
	// Try /proc on Linux
	cmdline, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err == nil {
		return strings.Contains(string(cmdline), "thaw")
	}
	// On macOS, use ps
	out, err := exec.Command("ps", "-p", strconv.Itoa(pid), "-o", "comm=").Output()
	if err == nil {
		return strings.Contains(strings.TrimSpace(string(out)), "thaw")
	}
	// Can't verify — allow it (safer than false negative)
	return true
}

// Stop sends SIGTERM to the running daemon.
func Stop() error {
	running, pid := IsRunning()
	if !running {
		return fmt.Errorf("daemon is not running")
	}

	proc, err := os.FindProcess(pid)
	if err != nil {
		return err
	}

	return proc.Signal(syscall.SIGTERM)
}

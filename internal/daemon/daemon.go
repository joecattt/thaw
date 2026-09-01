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
	"github.com/joecattt/thaw/internal/dashboard"
	"github.com/joecattt/thaw/internal/export"
	"github.com/joecattt/thaw/internal/ledger"
	"github.com/joecattt/thaw/internal/snapshot"
	"github.com/joecattt/thaw/pkg/models"
)

// Run starts the background snapshot daemon.
// Adapts snapshot frequency based on user activity:
// - Active (commands in last 5 min): snapshot every `interval`
// - Idle (no commands in 30+ min): snapshot every 6x interval
func Run(engine *capture.Engine, interval time.Duration, cfg config.Config) error {
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
			maybeBankLedger(cfg, store)

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

// maybeBankLedger runs the daily ledger banking duty: once per day, after the
// configured local time, bank per-project time into the permanent ledger and
// seal finalized days — BEFORE retention prunes the raw snapshots the numbers
// come from. Idempotent two ways: a marker file guards the daily trigger, and
// ledger.Bank itself never duplicates or shrinks a banked day.
func maybeBankLedger(cfg config.Config, store *snapshot.Store) {
	if !cfg.Ledger.Enabled {
		return
	}
	now := time.Now()
	if cfg.Ledger.BankAfter != "" && now.Format("15:04") < cfg.Ledger.BankAfter {
		return
	}
	dataDir, err := config.DataDir()
	if err != nil {
		return
	}
	marker := filepath.Join(dataDir, "ledger.banked")
	today := now.Format("2006-01-02")
	if data, err := os.ReadFile(marker); err == nil && strings.TrimSpace(string(data)) == today {
		return // already banked today
	}
	snaps, err := store.GetRange(now.AddDate(0, 0, -60), now)
	if err != nil || len(snaps) == 0 {
		return
	}
	home, _ := os.UserHomeDir()
	res, err := ledger.New(dataDir).Bank(snaps, home)
	if err != nil {
		log.Printf("ledger banking error: %v", err)
		return
	}
	log.Printf("ledger banked: %d rows (%d updated), %d sealed days (+%d new)",
		res.Rows, res.Updated, res.Seals, res.NewSeals)
	if problems, err := ledger.New(dataDir).Verify(); err == nil && len(problems) > 0 {
		for _, p := range problems {
			log.Printf("ledger verify: %s", p)
		}
	}
	os.WriteFile(marker, []byte(today), 0600)
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

// Freeze lock — a cross-process guard shared by `thaw freeze` and the daemon.
// Every capture forks top/ps/lsof and is expensive; N shell-exit hooks firing
// `thaw freeze &` at once used to stack those scans. A lock dir under the data
// dir serializes them, and a completion stamp lets callers skip entirely when
// a freeze just finished. (Ported from the operator's ~/bin/thaw wrapper,
// which grew these guards after a stale lock silently no-op'd every freeze
// for seven weeks.)

const freezeLockStaleAfter = 10 * time.Minute

func freezeLockDir() string {
	dataDir, err := config.DataDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".thaw-freeze.lock")
	}
	return filepath.Join(dataDir, ".freeze.lock")
}

func freezeStampPath() string {
	dataDir, err := config.DataDir()
	if err != nil {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, ".thaw-last-freeze")
	}
	return filepath.Join(dataDir, ".last_freeze")
}

// AcquireFreezeLock takes the freeze lock, recovering it if the holder died.
// Returns a release func and whether the lock was obtained. A lock older than
// freezeLockStaleAfter is a corpse from a killed freeze — remove and retake.
func AcquireFreezeLock() (release func(), ok bool) {
	lockDir := freezeLockDir()
	if err := os.Mkdir(lockDir, 0700); err == nil {
		return func() { os.Remove(lockDir) }, true
	}
	info, err := os.Stat(lockDir)
	if err != nil || time.Since(info.ModTime()) < freezeLockStaleAfter {
		return nil, false // genuinely held (or unreadable) — back off
	}
	os.Remove(lockDir)
	if err := os.Mkdir(lockDir, 0700); err != nil {
		return nil, false // lost the race to another recoverer
	}
	return func() { os.Remove(lockDir) }, true
}

// MarkFreezeDone records that a capture scan just completed.
func MarkFreezeDone() {
	os.WriteFile(freezeStampPath(), []byte(strconv.FormatInt(time.Now().Unix(), 10)), 0600)
}

// FreezeDoneWithin reports whether a freeze completed within the window —
// the dedup check that keeps rapid-fire shell-exit hooks from re-scanning.
func FreezeDoneWithin(window time.Duration) bool {
	data, err := os.ReadFile(freezeStampPath())
	if err != nil {
		return false
	}
	ts, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64)
	if err != nil {
		return false
	}
	return time.Since(time.Unix(ts, 0)) < window
}

// doSnapshot captures state and saves if changed. Returns the new hash.
func doSnapshot(engine *capture.Engine, store *snapshot.Store, lastHash string) string {
	writeHeartbeat()
	release, ok := AcquireFreezeLock()
	if !ok {
		return lastHash // a foreground `thaw freeze` is mid-scan — skip this tick
	}
	defer release()
	snap, err := engine.Capture("scheduled")
	if err != nil {
		log.Printf("capture error: %v", err)
		return lastHash
	}
	MarkFreezeDone()

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

	regenerateOpenDashboards(store)

	return hash
}

// regenerateOpenDashboards is the daemon-side half of "operate on its own"
// (autonomous-operation audit, 2026-08-30, biggest-leverage finding): the
// dashboard/kiosk HTML files sat static once opened, only refreshing if the
// operator remembered `--watch`. This keeps them current automatically —
// but ONLY the report/kiosk files that already exist (i.e., were opened at
// least once via `thaw dashboard --open`), never creates one unasked.
// Preserves whether --extras was used: reads the existing file first and
// checks for the news-section marker, rather than guessing — regenerating
// without extras would silently strip news out of a tab opened WITH it,
// and regenerating with extras would silently add an external fetch to a
// tab that was deliberately opened without it. Only runs when doSnapshot
// already detected real session-state change (the hash check above), so
// this is naturally rate-limited to genuine activity, not blind polling.
func regenerateOpenDashboards(store *snapshot.Store) {
	const rangeDays = 30
	to := time.Now()
	from := to.AddDate(0, 0, -rangeDays)
	snaps, err := store.GetRange(from, to)
	if err != nil || len(snaps) == 0 {
		return
	}
	records := export.Flatten(snaps)

	targets := []struct {
		file string
		gen  func(extras bool) string
	}{
		{"thaw-dashboard.html", func(extras bool) string { return dashboard.Generate(records, rangeDays, extras, false) }},
		{"thaw-kiosk.html", func(extras bool) string { return dashboard.GenerateKiosk(records, rangeDays, extras, false) }},
	}
	for _, t := range targets {
		path := filepath.Join(os.TempDir(), t.file)
		existing, err := os.ReadFile(path)
		if err != nil {
			continue // never opened this session — don't create it unasked
		}
		extras := strings.Contains(string(existing), `id="sec-news"`) || strings.Contains(string(existing), `"c":"news"`)
		if err := os.WriteFile(path, []byte(t.gen(extras)), 0644); err != nil {
			log.Printf("dashboard auto-regen (%s): %v", t.file, err)
		}
	}
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

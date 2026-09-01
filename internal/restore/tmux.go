package restore

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/joecattt/thaw/internal/ordering"
	"github.com/joecattt/thaw/internal/stale"
	"github.com/joecattt/thaw/internal/venv"
	"github.com/joecattt/thaw/pkg/models"
)

type Tmux struct{}

func NewTmux() *Tmux { return &Tmux{} }

func (t *Tmux) Name() string { return "tmux" }

func (t *Tmux) Available() bool {
	_, err := exec.LookPath("tmux")
	return err == nil
}

func (t *Tmux) Restore(snap *models.Snapshot, opts models.RestoreOptions) error {
	script, err := t.GenerateScript(snap, opts)
	if err != nil {
		return err
	}

	// Execute line by line with error recovery instead of monolithic script.
	// Track created sessions for undo support.
	var createdSessions []string
	var errors []string

	for _, line := range strings.Split(script, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		// Track session names for undo
		if strings.Contains(line, "new-session") && strings.Contains(line, "-s") {
			parts := strings.Fields(line)
			for i, p := range parts {
				if p == "-s" && i+1 < len(parts) {
					name := strings.Trim(parts[i+1], "'\"")
					createdSessions = append(createdSessions, name)
				}
			}
		}

		// Execute via sh -c to handle redirections (2>/dev/null)
		cmd := exec.Command("sh", "-c", line)
		out, err := cmd.CombinedOutput()
		if err != nil {
			errMsg := fmt.Sprintf("command failed: %s: %v", truncCmd(line, 80), err)
			if len(out) > 0 {
				errMsg += " (" + strings.TrimSpace(string(out)) + ")"
			}
			errors = append(errors, errMsg)

			// Non-fatal for most tmux commands — keep going
			// Only abort on session creation failure
			if strings.Contains(line, "new-session") {
				return fmt.Errorf("fatal: %s", errMsg)
			}
		}
	}

	// Write undo file for `thaw undo`
	writeUndoFile(createdSessions)

	if len(errors) > 0 {
		fmt.Fprintf(os.Stderr, "thaw: %d non-fatal error(s) during restore:\n", len(errors))
		for _, e := range errors {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
	}

	return nil
}

// writeUndoFile saves the list of tmux sessions created by the last restore.
func writeUndoFile(sessions []string) {
	if len(sessions) == 0 {
		return
	}
	dir, err := thawDataDir()
	if err != nil {
		return
	}
	data := strings.Join(sessions, "\n") + "\n"
	os.WriteFile(filepath.Join(dir, "last-restore.txt"), []byte(data), 0600)
}

// Undo kills all tmux sessions created by the last restore.
func Undo() (int, error) {
	dir, err := thawDataDir()
	if err != nil {
		return 0, err
	}
	data, err := os.ReadFile(filepath.Join(dir, "last-restore.txt"))
	if err != nil {
		return 0, fmt.Errorf("no restore to undo — last-restore.txt not found")
	}

	killed := 0
	for _, name := range strings.Split(strings.TrimSpace(string(data)), "\n") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		cmd := exec.Command("tmux", "kill-session", "-t", name)
		if cmd.Run() == nil {
			killed++
		}
	}

	os.Remove(filepath.Join(dir, "last-restore.txt"))
	return killed, nil
}

func truncCmd(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-3] + "..."
}

func thawDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".local", "share", "thaw")
	os.MkdirAll(dir, 0700)
	return dir, nil
}

func (t *Tmux) GenerateScript(snap *models.Snapshot, opts models.RestoreOptions) (string, error) {
	if len(snap.Sessions) == 0 {
		return "", fmt.Errorf("snapshot has no sessions to restore")
	}

	staleChecks := stale.CheckAll(snap)

	// Filter out stale sessions if requested
	var live []models.Session
	var skipped []models.Session
	for _, sess := range snap.Sessions {
		sc := staleChecks[sess.PID]
		if opts.SkipStale && sc.IsStale() {
			skipped = append(skipped, sess)
		} else {
			live = append(live, sess)
		}
	}
	if len(live) == 0 {
		return "", fmt.Errorf("all sessions are stale — nothing to restore")
	}

	// Sort by dependency order (infra → backend → frontend → tests → monitoring → idle)
	live = ordering.Sort(live)

	// Build a filtered snapshot for grouping
	filtered := &models.Snapshot{
		ID: snap.ID, Name: snap.Name, Sessions: live,
		CreatedAt: snap.CreatedAt, Source: snap.Source, Hostname: snap.Hostname,
	}

	var b strings.Builder
	resumer := NewClaudeResumer()
	b.WriteString("#!/bin/sh\n")
	fmt.Fprintf(&b, "# thaw — restoring %d sessions from snapshot #%d\n", len(live), snap.ID)
	if snap.Name != "" {
		fmt.Fprintf(&b, "# workspace: %s\n", snap.Name)
	}
	fmt.Fprintf(&b, "# captured: %s (%s)\n\n", snap.CreatedAt.Format("2006-01-02 15:04:05"), snap.Source)

	for _, s := range skipped {
		sc := staleChecks[s.PID]
		fmt.Fprintf(&b, "# SKIPPED: %s — %s\n", s.Label, sc.Reason)
	}
	if len(skipped) > 0 {
		b.WriteString("\n")
	}

	if opts.MultiSession {
		t.writeMultiSession(&b, filtered, opts, staleChecks, resumer)
	} else {
		t.writeSingleSession(&b, filtered, opts, staleChecks, resumer)
	}

	return b.String(), nil
}

// writeSessionGuard emits a single-line guard that renames an existing session
// out of the way instead of killing it. Must stay on one line: Restore()
// executes the script line by line, so multi-line if/fi blocks would fail.
// The trailing `|| true` keeps a missing session from reading as an error,
// and the date suffix sits outside the quotes so the shell expands it.
func writeSessionGuard(b *strings.Builder, name string) {
	fmt.Fprintf(b, "tmux has-session -t %s 2>/dev/null && tmux rename-session -t %s %s-prev-$(date +%%H%%M%%S) 2>/dev/null || true\n",
		esc(name), esc(name), esc(name))
}

// writeMultiSession creates a separate tmux session per workstream group.
func (t *Tmux) writeMultiSession(b *strings.Builder, snap *models.Snapshot, opts models.RestoreOptions, checks map[int]models.StaleCheck, resumer *ClaudeResumer) {
	groups := snap.WorkstreamGroups()

	// If only one group (or everything is misc), fall back to single session
	if len(groups) <= 1 {
		t.writeSingleSession(b, snap, opts, checks, resumer)
		return
	}

	var sessionNames []string

	for groupName, sessions := range groups {
		// Sanitize group name for tmux session name
		sessName := sanitizeTmuxName(groupName)
		sessionNames = append(sessionNames, sessName)

		fmt.Fprintf(b, "# --- group: %s (%d panes) ---\n", groupName, len(sessions))
		writeSessionGuard(b, sessName)

		first := sessions[0]
		cwd := safeCWD(first, checks[first.PID])
		fmt.Fprintf(b, "tmux new-session -d -s %s -c %s\n", esc(sessName), esc(cwd))
		fmt.Fprintf(b, "tmux rename-window -t %s %s\n", esc(sessName), esc(groupName))

		writePaneSetup(b, sessName, first, opts, checks[first.PID], resumer)

		maxPanes := opts.MaxPanes
		if maxPanes <= 0 {
			maxPanes = 8
		}
		paneCount := 1
		windowNum := 0
		lastTier := first.RestoreOrder

		for _, sess := range sessions[1:] {
			sc := checks[sess.PID]

			// Tier delay
			if opts.Mode == models.RunMode && opts.TierDelaySec > 0 {
				currentTier := sess.RestoreOrder / 10 * 10
				prevTier := lastTier / 10 * 10
				if currentTier > prevTier && prevTier < ordering.TierIdle {
					fmt.Fprintf(b, "sleep %d\n", opts.TierDelaySec)
				}
				lastTier = sess.RestoreOrder
			}

			// Max panes overflow
			if paneCount >= maxPanes {
				windowNum++
				paneCount = 0
				fmt.Fprintf(b, "tmux new-window -t %s -n %s -c %s\n",
					esc(sessName), esc(fmt.Sprintf("%s-%d", groupName, windowNum)), esc(safeCWD(sess, sc)))
			} else {
				fmt.Fprintf(b, "tmux split-window -t %s -c %s\n", esc(sessName), esc(safeCWD(sess, sc)))
			}
			paneCount++

			writePaneSetup(b, sessName, sess, opts, sc, resumer)
			fmt.Fprintf(b, "tmux select-layout -t %s %s\n", esc(sessName), opts.Layout)
		}

		fmt.Fprintf(b, "tmux select-layout -t %s %s\n", esc(sessName), opts.Layout)
		fmt.Fprintf(b, "tmux select-pane -t %s:0.0\n\n", esc(sessName))
	}

	// Summary comment
	fmt.Fprintf(b, "# Restored %d sessions across %d workstreams\n", len(snap.Sessions), len(groups))
	b.WriteString("# Attach with:\n")
	for _, name := range sessionNames {
		fmt.Fprintf(b, "#   tmux attach -t %s\n", name)
	}
}

// writeSingleSession creates one tmux session with all panes.
// Overflows to new windows when pane count exceeds MaxPanes.
// Inserts sleep between dependency tiers in --run mode.
func (t *Tmux) writeSingleSession(b *strings.Builder, snap *models.Snapshot, opts models.RestoreOptions, checks map[int]models.StaleCheck, resumer *ClaudeResumer) {
	name := opts.SessionName
	// Don't blindly kill existing sessions — rename them to preserve work in progress
	writeSessionGuard(b, name)

	first := snap.Sessions[0]
	cwd := safeCWD(first, checks[first.PID])
	fmt.Fprintf(b, "tmux new-session -d -s %s -c %s\n", esc(name), esc(cwd))

	label := first.Label
	if first.GroupName != "" {
		label = first.GroupName
	}
	if label == "" {
		label = "main"
	}
	fmt.Fprintf(b, "tmux rename-window -t %s %s\n\n", esc(name), esc(label))
	writePaneSetup(b, name, first, opts, checks[first.PID], resumer)

	maxPanes := opts.MaxPanes
	if maxPanes <= 0 {
		maxPanes = 8
	}
	paneCount := 1
	windowNum := 0
	lastTier := first.RestoreOrder

	for _, sess := range snap.Sessions[1:] {
		sc := checks[sess.PID]

		// Tier delay — insert sleep when transitioning between dependency tiers in --run mode
		if opts.Mode == models.RunMode && opts.TierDelaySec > 0 {
			currentTier := sess.RestoreOrder / 10 * 10 // round to tier
			prevTier := lastTier / 10 * 10
			if currentTier > prevTier && prevTier < ordering.TierIdle {
				fmt.Fprintf(b, "sleep %d\n", opts.TierDelaySec)
				fmt.Fprintf(b, "# tier transition: %s → %s\n",
					ordering.TierName(prevTier), ordering.TierName(currentTier))
			}
			lastTier = sess.RestoreOrder
		}

		// Max panes overflow — create new window
		if paneCount >= maxPanes {
			windowNum++
			paneCount = 0
			windowLabel := sess.Label
			if windowLabel == "" {
				windowLabel = fmt.Sprintf("overflow-%d", windowNum)
			}
			fmt.Fprintf(b, "tmux new-window -t %s -n %s -c %s\n",
				esc(name), esc(windowLabel), esc(safeCWD(sess, sc)))
		} else {
			fmt.Fprintf(b, "tmux split-window -t %s -c %s\n", esc(name), esc(safeCWD(sess, sc)))
		}
		paneCount++

		writePaneSetup(b, name, sess, opts, sc, resumer)
		fmt.Fprintf(b, "tmux select-layout -t %s %s\n", esc(name), opts.Layout)
	}

	fmt.Fprintf(b, "\ntmux select-layout -t %s %s\n", esc(name), opts.Layout)
	fmt.Fprintf(b, "tmux select-pane -t %s:0.0\n", esc(name))
}

func writePaneSetup(b *strings.Builder, target string, sess models.Session, opts models.RestoreOptions, sc models.StaleCheck, resumer *ClaudeResumer) {
	// Intent summary (AI or rule-based)
	if opts.ShowIntent && sess.Intent != "" {
		fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n",
			esc(target), esc("echo '▸ "+sess.Intent+"'"))
	}

	// Staleness warning
	if sc.Reason != "" {
		fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n",
			esc(target), esc("echo '⚠  "+sc.Reason+"'"))
	}

	// Git branch warning
	if sess.Git != nil && sess.Git.Branch != "" && !sc.GitBranchMatch {
		fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n",
			esc(target), esc(fmt.Sprintf("echo '⚠  branch was: %s'", sess.Git.Branch)))
	}

	// Terminal output replay — show last N lines of what was on screen
	if opts.ShowOutput && len(sess.Output) > 0 {
		fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n",
			esc(target), esc("echo '── last output ──'"))
		maxLines := 15
		start := 0
		if len(sess.Output) > maxLines {
			start = len(sess.Output) - maxLines
		}
		for _, line := range sess.Output[start:] {
			// Escape single quotes in output and truncate long lines
			safe := line
			if len(safe) > 120 {
				safe = safe[:117] + "..."
			}
			fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n",
				esc(target), esc("echo '"+escapeSingleQuotes(safe)+"'"))
		}
		fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n",
			esc(target), esc("echo '─────────────────'"))
	}

	// Direnv activation — trigger eval if .envrc exists
	if sess.HasDirenv && sc.CWDExists {
		fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n",
			esc(target), esc("eval \"$(direnv export bash 2>/dev/null)\""))
	}

	// Virtual environment activation — use activation commands instead of raw env injection
	activations := venv.Detect(sess.CWD, sess.EnvDelta.Set)
	venvSkipKeys := venv.EnvKeysToSkip(activations)
	for _, act := range activations {
		fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n",
			esc(target), esc(act.Command))
	}

	// Env var injection (skip keys handled by direnv or venv activation)
	if opts.RestoreEnv && !sess.HasDirenv && !sess.EnvDelta.IsEmpty() {
		for k, v := range sess.EnvDelta.Set {
			if venvSkipKeys[k] || k == "PATH" {
				continue // handled by venv activation
			}
			fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n",
				esc(target), esc(fmt.Sprintf("export %s=%s", k, shellQuote(v))))
		}
	}

	// Git checkout (opt-in)
	if opts.RestoreGit && sess.Git != nil && sess.Git.Branch != "" && !sc.GitBranchMatch && sc.CWDExists {
		fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n",
			esc(target), esc(fmt.Sprintf("git checkout %s 2>/dev/null", sess.Git.Branch)))
	}

	// Command history
	if opts.ShowHistory && len(sess.History) > 0 {
		limit := opts.HistoryLines
		if limit <= 0 {
			limit = 10
		}
		start := 0
		if len(sess.History) > limit {
			start = len(sess.History) - limit
		}
		fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n", esc(target), esc("echo '── recent ──'"))
		for _, cmd := range sess.History[start:] {
			safe := sanitizeForDisplay(cmd)
			fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n", esc(target), esc("echo '  "+safe+"'"))
		}
		fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n", esc(target), esc("echo '────────────'"))
	}

	// Claude Code pane — the conversation is on disk, so restore resumes it
	// instead of replaying a bare `claude` (which would start blank).
	if !sess.IsIdle() && resumer != nil && resumer.IsClaudePane(sess) {
		cmd := resumer.ResumeCommand(sess)
		if opts.Mode == models.RunMode && sc.CWDExists {
			fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n", esc(target), esc(cmd))
		} else {
			// Pre-type the command without Enter — one keystroke to thaw it.
			fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n",
				esc(target), esc("echo '❄ frozen claude conversation — press Enter to thaw'"))
			fmt.Fprintf(b, "tmux send-keys -t %s %s\n", esc(target), esc(cmd))
		}
		b.WriteString("\n")
		return
	}

	// Command
	if !sess.IsIdle() {
		if opts.Mode == models.RunMode && sc.BinaryExists {
			if IsDangerousCommand(sess.Command) {
				fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n",
					esc(target), esc("echo '⛔ BLOCKED — potentially dangerous: "+sanitizeForDisplay(sess.Command)+"'"))
			} else {
				fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n", esc(target), esc(sess.Command))
			}
		} else {
			fmt.Fprintf(b, "tmux send-keys -t %s %s C-m\n", esc(target), esc("# run: "+sanitizeForDisplay(sess.Command)))
		}
	}
	b.WriteString("\n")
}

// IsDangerousCommand detects commands that should never auto-execute.
// Precise patterns only — avoids blocking legitimate commands like `curl localhost:3000`.
func IsDangerousCommand(cmd string) bool {
	lower := strings.ToLower(cmd)

	// Command substitution embedded in the string
	if strings.Contains(lower, "$(") {
		return true
	}

	// Pipe to shell interpreter
	pipeToShell := []string{"| sh", "|sh", "| bash", "|bash", "| zsh", "|zsh"}
	for _, p := range pipeToShell {
		if strings.Contains(lower, p) {
			return true
		}
	}

	// Network fetch piped anywhere (the combination is dangerous, not curl alone)
	if (strings.Contains(lower, "curl ") || strings.Contains(lower, "wget ")) &&
		strings.Contains(lower, "|") {
		return true
	}

	// Direct eval/exec of dynamic content
	if strings.HasPrefix(lower, "eval ") || strings.HasPrefix(lower, "exec ") {
		return true
	}

	// Destructive filesystem operations
	destructive := []string{
		"rm -rf /", "rm -rf ~", "rm -rf /*",
		"mkfs", "dd if=",
		"> /dev/sd", "> /dev/nvme",
		"chmod 777 /", "chmod -R 777 /",
	}
	for _, d := range destructive {
		if strings.Contains(lower, d) {
			return true
		}
	}

	// Fork bomb patterns
	if strings.Contains(lower, ":(){ :|:") || strings.Contains(lower, ":(){:|:") {
		return true
	}

	return false
}

// sanitizeForDisplay strips control characters and limits length for safe display.
func sanitizeForDisplay(s string) string {
	var b strings.Builder
	for _, c := range s {
		if c >= 32 && c < 127 {
			b.WriteRune(c)
		}
	}
	result := b.String()
	if len(result) > 200 {
		result = result[:197] + "..."
	}
	return result
}

func escapeSingleQuotes(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}

func safeCWD(sess models.Session, sc models.StaleCheck) string {
	if !sc.CWDExists {
		return "$HOME"
	}
	return sess.CWD
}

func sanitizeTmuxName(s string) string {
	s = strings.ReplaceAll(s, ":", "-")
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, " ", "-")
	if len(s) > 30 {
		s = s[:30]
	}
	if s == "" {
		s = "thaw"
	}
	return s
}

func esc(s string) string {
	if s == "" {
		return "''"
	}
	s = strings.ReplaceAll(s, "'", "'\\''")
	return "'" + s + "'"
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') &&
			c != '_' && c != '/' && c != '.' && c != '-' && c != ':' {
			s = strings.ReplaceAll(s, "'", "'\\''")
			return "'" + s + "'"
		}
	}
	return s
}

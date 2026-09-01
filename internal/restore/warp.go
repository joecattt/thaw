package restore

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/joecattt/thaw/pkg/models"
)

// Warp backend — reopen every crashed/closed Claude Code pane as a Warp tab.
// Reads the snapshot's claude sessions, maps each to a distinct conversation
// (newest-first per cwd, same source data as ClaudeResumer but ranked by the
// last real message INSIDE the file — mtime lies: a failed `--resume` touches
// the file without adding content), writes a Warp launch configuration in the
// [[tabs]]/[[tabs.panes]] TOML schema, and opens warp://launch/thawed.
// Ported from the operator's ~/bin/thaw-tabs satellite script.

// WarpTab is one tab in the generated launch configuration.
type WarpTab struct {
	Dir     string
	Command string
}

// WarpLaunchName is the launch-config name Warp opens via warp://launch/<name>.
const WarpLaunchName = "thawed"

// WarpAvailable reports whether the Warp backend should be preferred without
// an explicit --warp: running inside Warp, on macOS, with no tmux server up.
func WarpAvailable() bool {
	if runtime.GOOS != "darwin" || os.Getenv("TERM_PROGRAM") != "WarpTerminal" {
		return false
	}
	// `tmux ls` exits non-zero when no server is running
	return exec.Command("tmux", "ls").Run() != nil
}

// IsClaudeSession reports whether the session was running Claude Code —
// foreground command or anywhere in the child tree.
func IsClaudeSession(s models.Session) bool {
	if isClaudeCmd(s.Command) {
		return true
	}
	for _, c := range s.Children {
		if isClaudeCmd(c.Command) {
			return true
		}
	}
	return false
}

// CountClaudeSessions returns how many of the snapshot's sessions were
// running Claude Code — the snapshot-picker's richness metric.
func CountClaudeSessions(snap *models.Snapshot) int {
	n := 0
	for _, s := range snap.Sessions {
		if IsClaudeSession(s) {
			n++
		}
	}
	return n
}

// ParseLiveResumeIDs extracts conversation ids currently held by running
// `claude --resume <id>` processes from `ps -axo pid=,args=` output. A
// conversation held by a live process resumes as a FRESH session (Claude Code
// refuses the in-use file) — handing that id to a new tab silently produces a
// blank session that LOOKS like restore did nothing, so callers skip these.
func ParseLiveResumeIDs(psOut string) map[string]bool {
	locked := map[string]bool{}
	for _, line := range strings.Split(psOut, "\n") {
		parts := strings.SplitN(strings.TrimSpace(line), " ", 2)
		if len(parts) < 2 {
			continue
		}
		args := parts[1]
		const marker = "claude --resume "
		if !strings.HasPrefix(args, marker) {
			continue
		}
		id := strings.Fields(args[len(marker):])
		if len(id) > 0 {
			locked[id[0]] = true
		}
	}
	return locked
}

// LiveResumeIDs runs ps and returns the live-locked conversation ids.
func LiveResumeIDs() map[string]bool {
	out, err := exec.Command("ps", "-axo", "pid=,args=").Output()
	if err != nil {
		return map[string]bool{}
	}
	return ParseLiveResumeIDs(string(out))
}

// AssignWarpTabs hands out distinct conversation ids per cwd, most recently
// active first, skipping ids already held by a live claude process. idsFor is
// injected so the pure assignment logic is testable without a filesystem.
func AssignWarpTabs(sessions []models.Session, idsFor func(cwd string) []string, locked map[string]bool, home string) (tabs []WarpTab, skippedLocked int, notes []string) {
	pool := map[string][]string{}
	for _, s := range sessions {
		cwd := s.CWD
		if cwd == "" {
			cwd = home
		}
		if _, ok := pool[cwd]; !ok {
			pool[cwd] = idsFor(cwd)
		}
		for len(pool[cwd]) > 0 && locked[pool[cwd][0]] {
			pool[cwd] = pool[cwd][1:]
			skippedLocked++
		}
		if len(pool[cwd]) == 0 {
			notes = append(notes, fmt.Sprintf("no more pre-snapshot conversations in %s — skipping a tab", cwd))
			continue
		}
		tabs = append(tabs, WarpTab{Dir: cwd, Command: "claude --resume " + pool[cwd][0]})
		pool[cwd] = pool[cwd][1:]
	}
	return tabs, skippedLocked, notes
}

// ConversationIDs returns conversation ids in cwd, most-recently-active
// first. With a non-zero cutoff (snapshot time), only conversations BORN
// before it — a conversation created after the snapshot is a fresh post-crash
// session, not one of the lost tabs. But a lost conversation may have content
// appended after the snapshot (failed resume attempts, accidental typing), so
// judge by birth, rank by last activity.
func ConversationIDs(projectsDir, cwd string, cutoff time.Time) []string {
	dir := filepath.Join(projectsDir, claudeProjectSlug(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type conv struct {
		id   string
		last time.Time
	}
	var convs []conv
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		info, err := e.Info()
		if err != nil {
			continue
		}
		if !cutoff.IsZero() && fileBirthTime(info).After(cutoff.Add(2*time.Minute)) {
			continue
		}
		last, ok := lastMessageTime(path)
		if !ok {
			continue
		}
		convs = append(convs, conv{id: strings.TrimSuffix(e.Name(), ".jsonl"), last: last})
	}
	sort.Slice(convs, func(i, j int) bool { return convs[i].last.After(convs[j].last) })
	ids := make([]string, len(convs))
	for i, c := range convs {
		ids[i] = c.id
	}
	return ids
}

// lastMessageTime finds the timestamp of the last real user/assistant message
// in a conversation file. Reads a tail window (retrying with a bigger one —
// a single huge tool-result record can bury 256K) instead of the whole file.
func lastMessageTime(path string) (time.Time, bool) {
	info, err := os.Stat(path)
	if err != nil {
		return time.Time{}, false
	}
	for _, window := range []int64{256 * 1024, 8 * 1024 * 1024} {
		f, err := os.Open(path)
		if err != nil {
			return time.Time{}, false
		}
		off := info.Size() - window
		if off < 0 {
			off = 0
		}
		f.Seek(off, 0)
		var best string
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
		for sc.Scan() {
			var d struct {
				Type      string `json:"type"`
				Timestamp string `json:"timestamp"`
			}
			if json.Unmarshal(sc.Bytes(), &d) != nil {
				continue
			}
			if (d.Type == "user" || d.Type == "assistant") && d.Timestamp != "" {
				best = d.Timestamp
			}
		}
		f.Close()
		if best != "" {
			t, err := time.Parse(time.RFC3339Nano, best)
			if err != nil {
				return time.Time{}, false
			}
			return t, true
		}
		if window >= info.Size() {
			break
		}
	}
	return time.Time{}, false
}

// GenerateWarpLaunchConfig renders the launch configuration in the TOML
// [[tabs]]/[[tabs.panes]] schema. (An earlier YAML windows:/layout:/exec:
// schema silently opened blank default tabs — Warp didn't recognize it, no
// error either. This one is the verified-working format.)
func GenerateWarpLaunchConfig(tabs []WarpTab, snapID int, created string) string {
	lines := []string{
		fmt.Sprintf("# generated by thaw from snapshot %d (%s) — do not hand-edit", snapID, created),
		fmt.Sprintf("name = %s", tomlStr(WarpLaunchName)),
	}
	for i, t := range tabs {
		lines = append(lines,
			"",
			"[[tabs]]",
			fmt.Sprintf("title = %s", tomlStr(fmt.Sprintf("thaw-%d", i+1))),
			"",
			"[[tabs.panes]]",
			fmt.Sprintf("id = %s", tomlStr(fmt.Sprintf("thaw-%d", i+1))),
			`type = "terminal"`,
			fmt.Sprintf("directory = %s", tomlStr(t.Dir)),
			fmt.Sprintf("commands = [%s]", tomlStr(t.Command)),
		)
	}
	return strings.Join(lines, "\n") + "\n"
}

func tomlStr(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}

// WarpRestore maps the snapshot's claude panes to conversations, writes the
// launch config, and opens Warp (macOS only). dryRun prints without touching
// anything.
func WarpRestore(snap *models.Snapshot, dryRun bool) error {
	var sessions []models.Session
	for _, s := range snap.Sessions {
		if IsClaudeSession(s) {
			sessions = append(sessions, s)
		}
	}
	if len(sessions) == 0 {
		return fmt.Errorf("snapshot #%d has no claude sessions", snap.ID)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	projectsDir := filepath.Join(home, ".claude", "projects")
	locked := LiveResumeIDs()
	idsFor := func(cwd string) []string {
		return ConversationIDs(projectsDir, cwd, snap.CreatedAt)
	}
	tabs, skippedLocked, notes := AssignWarpTabs(sessions, idsFor, locked, home)
	for _, n := range notes {
		fmt.Printf("  (%s)\n", n)
	}
	if skippedLocked > 0 {
		fmt.Printf("  (skipped %d conversation(s) already open in a live tab)\n", skippedLocked)
	}
	if len(tabs) == 0 {
		return fmt.Errorf("nothing to restore")
	}
	if len(locked) > 0 {
		fmt.Printf("\n  ⚠ %d conversation(s) currently held by a running claude process were\n", len(locked))
		fmt.Println("    excluded. If any of those are orphans from a crashed window, kill them")
		fmt.Println("    so they stop occupying that conversation on every future restore:")
		fmt.Println("    ps -axo pid,tty,lstart,args | grep -w claude")
	}
	created := snap.CreatedAt.Format("2006-01-02 15:04:05")
	fmt.Printf("snapshot %d (%s) — %d claude tab(s):\n", snap.ID, created, len(tabs))
	for i, t := range tabs {
		fmt.Printf("  %d) %s  →  %s\n", i+1, t.Dir, t.Command)
	}
	if dryRun {
		return nil
	}
	if runtime.GOOS != "darwin" {
		return fmt.Errorf("the Warp restore backend needs macOS (warp:// launch URLs)")
	}
	launchPath := filepath.Join(home, ".warp", "launch_configurations", WarpLaunchName+".toml")
	if err := os.MkdirAll(filepath.Dir(launchPath), 0700); err != nil {
		return err
	}
	if err := os.WriteFile(launchPath, []byte(GenerateWarpLaunchConfig(tabs, snap.ID, created)), 0600); err != nil {
		return err
	}
	if err := exec.Command("open", "warp://launch/"+WarpLaunchName).Run(); err != nil {
		return fmt.Errorf("opening warp://launch/%s: %w", WarpLaunchName, err)
	}
	fmt.Printf("\nopened %d tab(s) via warp://launch/%s\n", len(tabs), WarpLaunchName)
	return nil
}

package restore

import (
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/joecattt/thaw/pkg/models"
)

// ClaudeResumer maps a snapshot's Claude Code panes back to the conversations
// they were running. Claude Code persists every conversation as a .jsonl under
// ~/.claude/projects/<dir-slug>/, so a crashed pane's conversation is always
// recoverable — the restore command just has to say `--resume <id>` instead of
// starting a blank session. IDs are handed out per-directory, newest first, so
// several panes in the same project each get a distinct conversation back.
type ClaudeResumer struct {
	projectsDir string
	byDir       map[string][]string // cwd → remaining conversation IDs, newest first
}

func NewClaudeResumer() *ClaudeResumer {
	home, err := os.UserHomeDir()
	if err != nil {
		return &ClaudeResumer{byDir: map[string][]string{}}
	}
	return &ClaudeResumer{
		projectsDir: filepath.Join(home, ".claude", "projects"),
		byDir:       map[string][]string{},
	}
}

// claudeProjectSlug mirrors Claude Code's directory naming: every character
// that isn't alphanumeric becomes '-' (so "/Users/alice" → "-Users-alice").
func claudeProjectSlug(cwd string) string {
	var b strings.Builder
	for _, c := range cwd {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			b.WriteRune(c)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

// IsClaudePane reports whether the session was running Claude Code at capture
// time — either as the foreground command or anywhere in the child tree.
func (r *ClaudeResumer) IsClaudePane(s models.Session) bool {
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

func isClaudeCmd(cmd string) bool {
	fields := strings.Fields(cmd)
	if len(fields) == 0 {
		return false
	}
	return filepath.Base(fields[0]) == "claude"
}

// ResumeCommand returns the command that brings this pane's conversation back.
// Newest conversation in the pane's directory wins; each call consumes one ID
// so sibling panes resume distinct conversations. Falls back to `--continue`
// when no conversation files are found (still safe — worst case a fresh
// session, same as before).
func (r *ClaudeResumer) ResumeCommand(s models.Session) string {
	ids, ok := r.byDir[s.CWD]
	if !ok {
		ids = r.loadIDs(s.CWD)
	}
	if len(ids) == 0 {
		r.byDir[s.CWD] = ids
		return "claude --continue"
	}
	r.byDir[s.CWD] = ids[1:]
	return "claude --resume " + ids[0]
}

func (r *ClaudeResumer) loadIDs(cwd string) []string {
	if r.projectsDir == "" {
		return nil
	}
	dir := filepath.Join(r.projectsDir, claudeProjectSlug(cwd))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	type conv struct {
		id    string
		mtime int64
	}
	var convs []conv
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".jsonl") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		convs = append(convs, conv{
			id:    strings.TrimSuffix(e.Name(), ".jsonl"),
			mtime: info.ModTime().UnixNano(),
		})
	}
	sort.Slice(convs, func(i, j int) bool { return convs[i].mtime > convs[j].mtime })
	ids := make([]string, len(convs))
	for i, c := range convs {
		ids[i] = c.id
	}
	return ids
}

// CountClaudePanes returns how many sessions in the snapshot were running
// Claude Code — used by the greeting so the user knows what's recoverable.
func (r *ClaudeResumer) CountClaudePanes(snap *models.Snapshot) int {
	n := 0
	for _, s := range snap.Sessions {
		if r.IsClaudePane(s) {
			n++
		}
	}
	return n
}

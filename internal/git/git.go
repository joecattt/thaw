package git

import (
	"os"
	"os/exec"
	"strings"

	"github.com/joecattt/thaw/pkg/models"
)

// State captures the git repository state for a given directory.
// Returns nil if the directory is not inside a git repo.
func State(dir string) *models.GitState {
	// Check if dir is in a git repo
	root, err := gitCmd(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil
	}

	state := &models.GitState{
		RepoRoot: strings.TrimSpace(root),
	}

	// Current branch
	branch, err := gitCmd(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err == nil {
		state.Branch = strings.TrimSpace(branch)
	}

	// Short commit SHA
	commit, err := gitCmd(dir, "rev-parse", "--short", "HEAD")
	if err == nil {
		state.Commit = strings.TrimSpace(commit)
	}

	// Dirty state — any uncommitted changes
	status, err := gitCmd(dir, "status", "--porcelain")
	if err == nil {
		state.Dirty = strings.TrimSpace(status) != ""
	}

	// Upstream tracking branch
	upstream, err := gitCmd(dir, "rev-parse", "--abbrev-ref", "@{upstream}")
	if err == nil {
		state.Upstream = strings.TrimSpace(upstream)
	}

	return state
}

// CurrentBranch returns the current branch name for a directory, or empty string.
func CurrentBranch(dir string) string {
	branch, err := gitCmd(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(branch)
}

// IsRepo returns true if the directory is inside a git repository.
func IsRepo(dir string) bool {
	_, err := gitCmd(dir, "rev-parse", "--is-inside-work-tree")
	return err == nil
}

// RepoRoot returns the root directory of the git repo containing dir.
func RepoRoot(dir string) string {
	root, err := gitCmd(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(root)
}

// BranchChanged returns true if the current branch differs from the snapshotted one.
func BranchChanged(dir string, snapshotBranch string) bool {
	current := CurrentBranch(dir)
	if current == "" || snapshotBranch == "" {
		return false
	}
	return current != snapshotBranch
}

// gitCmd runs a git command in the given directory and returns stdout.
func gitCmd(dir string, args ...string) (string, error) {
	// Verify dir exists first
	if _, err := os.Stat(dir); err != nil {
		return "", err
	}

	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	// Suppress git's advice/hints
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
}

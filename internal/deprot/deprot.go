package deprot

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/joecattt/thaw/pkg/models"
)

// Check detects dependency file changes since a snapshot was taken.
// Compares current state of key project files against the snapshot's git commit.
func Check(session models.Session) []models.DepRot {
	if session.Git == nil || session.Git.Commit == "" {
		return nil
	}
	if _, err := os.Stat(session.CWD); err != nil {
		return nil
	}

	// Key dependency files to watch
	depFiles := []string{
		"package.json", "package-lock.json", "yarn.lock", "pnpm-lock.yaml",
		"go.mod", "go.sum",
		"Cargo.toml", "Cargo.lock",
		"requirements.txt", "pyproject.toml", "poetry.lock", "Pipfile.lock",
		"Gemfile", "Gemfile.lock",
		"docker-compose.yml", "docker-compose.yaml", "Dockerfile",
		".env", ".env.local",
		"Makefile", "CMakeLists.txt",
	}

	var issues []models.DepRot

	// Use git diff to check what changed since the snapshot commit
	out, err := exec.Command("git", "-C", session.CWD, "diff", "--name-status",
		session.Git.Commit, "HEAD").Output()
	if err != nil {
		// Can't diff — fall back to checking if files exist and are newer
		return checkByTimestamp(session, depFiles)
	}

	watched := make(map[string]bool)
	for _, f := range depFiles {
		watched[f] = true
	}

	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}
		status := parts[0]
		file := parts[1]

		// Check if it's a dependency file
		base := filepath.Base(file)
		if !watched[base] && !watched[file] {
			continue
		}

		var rot models.DepRot
		rot.File = file
		switch {
		case strings.HasPrefix(status, "M"):
			rot.Status = "modified"
		case strings.HasPrefix(status, "D"):
			rot.Status = "deleted"
		case strings.HasPrefix(status, "A"):
			rot.Status = "added"
		default:
			rot.Status = "changed"
		}
		issues = append(issues, rot)
	}

	return issues
}

// checkByTimestamp is a fallback when git diff isn't available.
func checkByTimestamp(session models.Session, depFiles []string) []models.DepRot {
	var issues []models.DepRot

	for _, f := range depFiles {
		path := filepath.Join(session.CWD, f)
		info, err := os.Stat(path)
		if err != nil {
			continue
		}
		if info.ModTime().After(session.CapturedAt) {
			issues = append(issues, models.DepRot{
				File:   f,
				Status: "modified",
			})
		}
	}

	return issues
}

// CheckAll runs dependency rot detection across all sessions.
func CheckAll(snap *models.Snapshot) map[int][]models.DepRot {
	result := make(map[int][]models.DepRot)
	for _, s := range snap.Sessions {
		rots := Check(s)
		if len(rots) > 0 {
			result[s.PID] = rots
		}
	}
	return result
}

// FormatWarnings returns human-readable warnings for dependency rot.
func FormatWarnings(rots map[int][]models.DepRot, sessions []models.Session) []string {
	if len(rots) == 0 {
		return nil
	}

	pidToLabel := make(map[int]string)
	for _, s := range sessions {
		pidToLabel[s.PID] = s.Label
	}

	var warnings []string
	for pid, issues := range rots {
		label := pidToLabel[pid]
		if label == "" {
			label = "session"
		}
		var files []string
		for _, r := range issues {
			files = append(files, r.File+" ("+r.Status+")")
		}
		warnings = append(warnings, label+": "+strings.Join(files, ", "))
	}
	return warnings
}

package zoxide

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Available returns true if zoxide is installed.
func Available() bool {
	_, err := exec.LookPath("zoxide")
	return err == nil
}

// Entry represents a zoxide directory entry.
type Entry struct {
	Score float64
	Path  string
}

// Query returns the best zoxide match for a search term.
func Query(term string) string {
	if !Available() {
		return ""
	}
	cmd := exec.Command("zoxide", "query", "--", term)
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// Score returns the zoxide frecency score for a path.
// Higher = more frequently/recently visited. 0 = unknown.
func Score(path string) float64 {
	entries := listAll()
	for _, e := range entries {
		if e.Path == path {
			return e.Score
		}
	}
	return 0
}

// LabelForPath returns a short meaningful name for a directory based on zoxide data.
// If the path is a top-visited directory, uses the basename.
// Otherwise returns empty string.
func LabelForPath(path string) string {
	score := Score(path)
	if score > 0 {
		return filepath.Base(path)
	}
	return ""
}

// SuggestReplacement finds the closest matching directory for a stale CWD.
// Useful when a directory has been moved or renamed.
func SuggestReplacement(stalePath string) string {
	if !Available() {
		return ""
	}
	// Try querying with the basename
	base := filepath.Base(stalePath)
	result := Query(base)
	if result != "" && result != stalePath {
		return result
	}

	// Try parent + base combo
	parent := filepath.Base(filepath.Dir(stalePath))
	result = Query(parent + " " + base)
	if result != "" && result != stalePath {
		return result
	}

	return ""
}

// TopPaths returns the N most-visited directories from zoxide.
func TopPaths(limit int) []Entry {
	entries := listAll()
	if len(entries) > limit {
		entries = entries[:limit]
	}
	return entries
}

// listAll reads all zoxide entries, sorted by score descending.
func listAll() []Entry {
	if !Available() {
		return nil
	}

	// zoxide query --list --score gives "SCORE PATH" per line
	cmd := exec.Command("zoxide", "query", "--list", "--score")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}

	var entries []Entry
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Format: "  123.45 /path/to/dir"
		parts := strings.SplitN(strings.TrimSpace(line), " ", 2)
		if len(parts) < 2 {
			continue
		}
		score, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
		if err != nil {
			continue
		}
		entries = append(entries, Entry{
			Score: score,
			Path:  strings.TrimSpace(parts[1]),
		})
	}

	sort.Slice(entries, func(i, j int) bool {
		return entries[i].Score > entries[j].Score
	})

	return entries
}

// DetectProjectType inspects a directory for project markers and returns a description.
// e.g. "node-18", "python-3.11-django", "go-1.22", "rust"
func DetectProjectType(dir string) string {
	if _, err := os.Stat(dir); err != nil {
		return ""
	}

	var parts []string

	// Node.js
	if fileExists(dir, "package.json") {
		parts = append(parts, "node")
		if v := readNodeVersion(dir); v != "" {
			parts[0] = "node-" + v
		}
		if fileExists(dir, "next.config.js") || fileExists(dir, "next.config.mjs") || fileExists(dir, "next.config.ts") {
			parts = append(parts, "next")
		} else if dirContains(dir, "node_modules", "express") {
			parts = append(parts, "express")
		} else if dirContains(dir, "node_modules", "react") {
			parts = append(parts, "react")
		}
	}

	// Python
	if fileExists(dir, "pyproject.toml") || fileExists(dir, "setup.py") || fileExists(dir, "requirements.txt") {
		parts = append(parts, "python")
		if fileExists(dir, "manage.py") {
			parts = append(parts, "django")
		} else if dirContains(dir, "", "flask") {
			parts = append(parts, "flask")
		}
	}

	// Go
	if fileExists(dir, "go.mod") {
		parts = append(parts, "go")
	}

	// Rust
	if fileExists(dir, "Cargo.toml") {
		parts = append(parts, "rust")
	}

	// Ruby
	if fileExists(dir, "Gemfile") {
		parts = append(parts, "ruby")
		if fileExists(dir, "config/routes.rb") {
			parts = append(parts, "rails")
		}
	}

	// Docker
	if fileExists(dir, "Dockerfile") || fileExists(dir, "docker-compose.yml") || fileExists(dir, "docker-compose.yaml") {
		parts = append(parts, "docker")
	}

	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, "-")
}

func fileExists(dir, name string) bool {
	_, err := os.Stat(filepath.Join(dir, name))
	return err == nil
}

func dirContains(dir, subdir, name string) bool {
	p := filepath.Join(dir, subdir, name)
	_, err := os.Stat(p)
	return err == nil
}

func readNodeVersion(dir string) string {
	// Check .nvmrc
	data, err := os.ReadFile(filepath.Join(dir, ".nvmrc"))
	if err == nil {
		v := strings.TrimSpace(string(data))
		v = strings.TrimPrefix(v, "v")
		return v
	}
	// Check .node-version
	data, err = os.ReadFile(filepath.Join(dir, ".node-version"))
	if err == nil {
		v := strings.TrimSpace(string(data))
		v = strings.TrimPrefix(v, "v")
		return v
	}
	return ""
}

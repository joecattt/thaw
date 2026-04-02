package project

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

// Config is a per-project .thaw.toml file found in a project root.
type Config struct {
	Project ProjectSection `toml:"project"`
}

type ProjectSection struct {
	Name            string            `toml:"name"`
	RestoreCommands []string          `toml:"restore_commands"`
	Env             map[string]string `toml:"env"`
	Layout          string            `toml:"layout"`
	HealthCheck     string            `toml:"health_check"`
	TestCommand     string            `toml:"test_command"`
	TodoPattern     string            `toml:"todo_pattern"`
	BuildCommand    string            `toml:"build_command"`
	LintCommand     string            `toml:"lint_command"`
}

// Load reads .thaw.toml from the given directory or any parent up to the repo root.
func Load(dir string) (*Config, error) {
	path := Find(dir)
	if path == "" {
		return nil, nil
	}
	var cfg Config
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	// Default todo pattern
	if cfg.Project.TodoPattern == "" {
		cfg.Project.TodoPattern = "TODO|FIXME|HACK|XXX"
	}
	return &cfg, nil
}

// Find walks up from dir looking for .thaw.toml.
func Find(dir string) string {
	for {
		candidate := filepath.Join(dir, ".thaw.toml")
		if _, err := os.Stat(candidate); err == nil {
			return candidate
		}
		// Also check if we're at a git root (stop here)
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			// Last chance — check this dir
			candidate := filepath.Join(dir, ".thaw.toml")
			if _, err := os.Stat(candidate); err == nil {
				return candidate
			}
			return ""
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// FindRepoRoot walks up from dir to find .git directory.
func FindRepoRoot(dir string) string {
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// DetectProjectType inspects directory contents to determine project type.
func DetectProjectType(dir string) string {
	// Ordered by specificity — language markers first, generic last
	checks := []struct{ file, ptype string }{
		{"package.json", "node"},
		{"go.mod", "go"},
		{"Cargo.toml", "rust"},
		{"pyproject.toml", "python"},
		{"requirements.txt", "python"},
		{"Gemfile", "ruby"},
		{"pom.xml", "java"},
		{"build.gradle", "java"},
		{"composer.json", "php"},
		{"mix.exs", "elixir"},
		{"docker-compose.yml", "docker"},
		{"docker-compose.yaml", "docker"},
		{"Makefile", "make"}, // generic — last
	}
	for _, c := range checks {
		if _, err := os.Stat(filepath.Join(dir, c.file)); err == nil {
			return c.ptype
		}
	}
	return "unknown"
}

// CountTodos scans source files for TODO/FIXME/HACK/XXX patterns.
func CountTodos(dir string, pattern string) (int, []string) {
	if pattern == "" {
		pattern = "TODO|FIXME|HACK|XXX"
	}
	parts := strings.Split(pattern, "|")
	count := 0
	var samples []string

	filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return nil
		}
		// Depth limit — don't recurse more than 6 levels deep
		rel, _ := filepath.Rel(dir, path)
		if strings.Count(rel, string(filepath.Separator)) > 6 {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip symlinks — prevents infinite loops
		if info.Mode()&os.ModeSymlink != 0 {
			if info.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip hidden dirs, node_modules, vendor, .git
		name := info.Name()
		if info.IsDir() {
			if name == ".git" || name == "node_modules" || name == "vendor" || name == "__pycache__" || name == ".next" || name == "dist" || name == "build" {
				return filepath.SkipDir
			}
			return nil
		}
		// Only scan source-like files
		ext := filepath.Ext(name)
		sourceExts := map[string]bool{
			".go": true, ".js": true, ".ts": true, ".jsx": true, ".tsx": true,
			".py": true, ".rb": true, ".rs": true, ".java": true, ".c": true,
			".cpp": true, ".h": true, ".css": true, ".scss": true, ".vue": true,
			".svelte": true, ".php": true, ".ex": true, ".exs": true,
		}
		if !sourceExts[ext] {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		lines := strings.Split(string(data), "\n")
		for _, line := range lines {
			for _, p := range parts {
				if strings.Contains(line, p) {
					count++
					if len(samples) < 5 {
						rel, _ := filepath.Rel(dir, path)
						trimmed := strings.TrimSpace(line)
						if len(trimmed) > 80 {
							trimmed = trimmed[:77] + "..."
						}
						samples = append(samples, rel+": "+trimmed)
					}
					break
				}
			}
		}
		return nil
	})
	return count, samples
}

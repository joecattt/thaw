package group

import (
	"os"
	"path/filepath"

	"github.com/joecattt/thaw/internal/git"
	"github.com/joecattt/thaw/pkg/models"
)

// Assign clusters sessions into workstream groups based on shared project roots.
// For monorepos, groups by first diverging subdirectory instead of repo root.
func Assign(sessions []models.Session) []models.Session {
	result := make([]models.Session, len(sessions))
	copy(result, sessions)

	// Phase 1: Group by git repo root, then split monorepo sub-projects
	repoGroups := make(map[string][]int) // repo root → session indices
	for i := range result {
		if result[i].Git != nil && result[i].Git.RepoRoot != "" {
			repoGroups[result[i].Git.RepoRoot] = append(repoGroups[result[i].Git.RepoRoot], i)
		}
	}

	assigned := make(map[int]bool)
	for root, indices := range repoGroups {
		if len(indices) < 2 {
			continue
		}

		// Check if this is a monorepo — do sessions diverge at the first level?
		subGroups := splitMonorepo(root, indices, result)

		if len(subGroups) > 1 {
			// Check if any sub-group has 2+ sessions
			hasMulti := false
			for _, subIndices := range subGroups {
				if len(subIndices) >= 2 {
					hasMulti = true
					break
				}
			}

			if hasMulti {
				// Monorepo with real clusters — group by sub-project
				for subName, subIndices := range subGroups {
					if len(subIndices) < 2 {
						continue
					}
					groupID := "mono:" + filepath.Base(root) + "/" + subName
					groupName := subName
					for _, idx := range subIndices {
						result[idx].GroupID = groupID
						result[idx].GroupName = groupName
						assigned[idx] = true
					}
				}
			} else {
				// All singletons — fall back to repo-level grouping
				groupID := "repo:" + filepath.Base(root)
				groupName := filepath.Base(root)
				for _, idx := range indices {
					result[idx].GroupID = groupID
					result[idx].GroupName = groupName
					assigned[idx] = true
				}
			}
		} else {
			// Standard repo — group by root
			groupID := "repo:" + filepath.Base(root)
			groupName := filepath.Base(root)
			for _, idx := range indices {
				result[idx].GroupID = groupID
				result[idx].GroupName = groupName
				assigned[idx] = true
			}
		}
	}

	// Phase 2: Group remaining by shared project root
	projectGroups := make(map[string][]int)
	for i := range result {
		if assigned[i] {
			continue
		}
		root := findProjectRoot(result[i].CWD)
		if root != "" {
			projectGroups[root] = append(projectGroups[root], i)
		}
	}

	for root, indices := range projectGroups {
		if len(indices) < 2 {
			continue
		}
		groupID := "proj:" + filepath.Base(root)
		groupName := filepath.Base(root)
		for _, idx := range indices {
			result[idx].GroupID = groupID
			result[idx].GroupName = groupName
			assigned[idx] = true
		}
	}

	return result
}

// splitMonorepo checks if sessions in a repo diverge at the first subdirectory level.
// Returns a map of sub-project name → session indices.
// If all sessions share the same subdir (or are at the root), returns a single group.
func splitMonorepo(repoRoot string, indices []int, sessions []models.Session) map[string][]int {
	subGroups := make(map[string][]int)

	for _, idx := range indices {
		cwd := sessions[idx].CWD
		// Get the relative path from repo root
		rel, err := filepath.Rel(repoRoot, cwd)
		if err != nil || rel == "." || rel == "" {
			subGroups["root"] = append(subGroups["root"], idx)
			continue
		}

		// First path component is the sub-project
		parts := splitPath(rel)
		if len(parts) == 0 {
			subGroups["root"] = append(subGroups["root"], idx)
			continue
		}

		// Use first 2 components for common monorepo structures like apps/api, packages/shared
		subName := parts[0]
		if len(parts) >= 2 && isMonorepoPrefix(parts[0]) {
			subName = parts[0] + "/" + parts[1]
		}

		subGroups[subName] = append(subGroups[subName], idx)
	}

	return subGroups
}

// isMonorepoPrefix returns true for common monorepo top-level directory names.
func isMonorepoPrefix(name string) bool {
	prefixes := []string{"apps", "packages", "services", "libs", "modules", "tools", "projects", "crates", "cmd", "internal"}
	for _, p := range prefixes {
		if name == p {
			return true
		}
	}
	return false
}

func splitPath(path string) []string {
	var parts []string
	for _, p := range filepath.SplitList(path) {
		parts = append(parts, p)
	}
	// filepath.SplitList uses os.PathListSeparator, need regular split
	parts = nil
	for _, p := range split(path, '/') {
		if p != "" && p != "." {
			parts = append(parts, p)
		}
	}
	return parts
}

func split(s string, sep byte) []string {
	var result []string
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep {
			if i > start {
				result = append(result, s[start:i])
			}
			start = i + 1
		}
	}
	if start < len(s) {
		result = append(result, s[start:])
	}
	return result
}

// AutoLabel generates a human-readable group name from sessions in a group.
func AutoLabel(sessions []models.Session) string {
	if len(sessions) == 0 {
		return ""
	}

	// If all share a git repo, use the repo name
	if sessions[0].Git != nil && sessions[0].Git.RepoRoot != "" {
		allSame := true
		root := sessions[0].Git.RepoRoot
		for _, s := range sessions[1:] {
			if s.Git == nil || s.Git.RepoRoot != root {
				allSame = false
				break
			}
		}
		if allSame {
			return filepath.Base(root)
		}
	}

	// Count labels and pick most common
	counts := make(map[string]int)
	for _, s := range sessions {
		if s.Label != "" {
			counts[s.Label]++
		}
	}
	bestLabel := ""
	bestCount := 0
	for label, count := range counts {
		if count > bestCount {
			bestLabel = label
			bestCount = count
		}
	}
	if bestLabel != "" {
		return bestLabel
	}

	return filepath.Base(sessions[0].CWD)
}

// findProjectRoot walks up from dir looking for a project marker file.
func findProjectRoot(dir string) string {
	root := git.RepoRoot(dir)
	if root != "" {
		return root
	}

	markers := []string{
		"go.mod", "package.json", "Cargo.toml", "pyproject.toml",
		"setup.py", "Makefile", "CMakeLists.txt", "pom.xml",
		"build.gradle", ".project", "Gemfile", "mix.exs",
	}

	current := dir
	for current != "/" && current != "." {
		for _, m := range markers {
			if _, err := os.Stat(filepath.Join(current, m)); err == nil {
				return current
			}
		}
		current = filepath.Dir(current)
	}

	return ""
}

package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/spf13/cobra"
)

// junkCmd is the classification half of "does it know what's junk" — a
// real candidate list, never an action. Two categories, both bounded to
// ~/Downloads and ~/Desktop (not the whole disk: hashing every file under
// $HOME is exactly the kind of naive full-tree walk that cost 82s
// elsewhere in this tool tonight):
//   - exact duplicates (SHA-256 match) — unambiguous, genuinely wasted space
//   - stale-and-large (>25MB, untouched 180+ days) — a candidate, not a
//     verdict; presented for review, nothing else
// Never deletes, moves, or offers a one-key action to do either. That's
// deliberate, not a missing flag — see `thaw space`'s doc comment for why.
func junkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "junk",
		Short: "Find likely-junk files (duplicates, stale+large) — a candidate list, never an action",
		Long: `Classifies files in ~/Downloads and ~/Desktop as likely junk:
exact duplicates (content hash match — unambiguous) and stale+large files
(25MB+, untouched 180+ days — a candidate, not a verdict). Prints what it
found and where. Does not delete, move, or trash anything, ever — review
the list and act yourself.

  thaw junk`,
		RunE: func(cmd *cobra.Command, args []string) error {
			printJunk()
			return nil
		},
	}
}

type fileInfo struct {
	path    string
	size    int64
	modTime time.Time
}

func scanFiles(dirs []string) []fileInfo {
	var files []fileInfo
	for _, dir := range dirs {
		filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info == nil || info.IsDir() {
				return nil
			}
			// Skip hidden files (.DS_Store etc.) — noise, not junk candidates.
			if filepath.Base(path)[0] == '.' {
				return nil
			}
			files = append(files, fileInfo{path, info.Size(), info.ModTime()})
			return nil
		})
	}
	return files
}

func printJunk() {
	home, _ := os.UserHomeDir()
	dirs := []string{filepath.Join(home, "Downloads"), filepath.Join(home, "Desktop")}
	files := scanFiles(dirs)

	fmt.Println("DUPLICATE FILES — exact content match (unambiguous as bytes; NOT unambiguous as")
	fmt.Println("intent — archive/mail-pull directories routinely duplicate records ON PURPOSE")
	fmt.Println("as snapshot redundancy, which is not junk. Read the paths before acting.)")
	dupGroups := findDuplicates(files)
	if len(dupGroups) == 0 {
		fmt.Println("  none found")
	} else {
		var dupWaste int64
		for _, g := range dupGroups {
			fmt.Printf("  %s each, %d copies:\n", humanBytes(g[0].size), len(g))
			for _, f := range g {
				fmt.Printf("    %s\n", f.path)
			}
			dupWaste += g[0].size * int64(len(g)-1) // all but one copy is waste
		}
		fmt.Printf("  wasted space if you keep one copy of each: %s\n", humanBytes(dupWaste))
	}

	fmt.Println("\nSTALE + LARGE — 25MB+, untouched 180+ days (candidates, not verdicts)")
	cutoff := time.Now().AddDate(0, 0, -180)
	var stale []fileInfo
	for _, f := range files {
		if f.size >= 25<<20 && f.modTime.Before(cutoff) {
			stale = append(stale, f)
		}
	}
	sort.Slice(stale, func(i, j int) bool { return stale[i].size > stale[j].size })
	if len(stale) == 0 {
		fmt.Println("  none found")
	} else {
		var staleTotal int64
		for _, f := range stale {
			fmt.Printf("  %8s  last touched %s  %s\n", humanBytes(f.size), f.modTime.Format("2006-01-02"), f.path)
			staleTotal += f.size
		}
		fmt.Printf("  total: %s\n", humanBytes(staleTotal))
	}

	fmt.Println("\n(candidate list only — nothing was deleted, moved, or trashed; review and act yourself)")
}

// findDuplicates groups files by content hash, keeping only groups with 2+
// members. Two-pass: group by size first (cheap), only hash files that
// share a size with at least one other file — skips hashing the vast
// majority of a normal Downloads folder.
func findDuplicates(files []fileInfo) [][]fileInfo {
	bySize := map[int64][]fileInfo{}
	for _, f := range files {
		if f.size < 1024 {
			continue // sub-1KB files collide on size/hash constantly and mean nothing as "duplicates"
		}
		bySize[f.size] = append(bySize[f.size], f)
	}
	byHash := map[string][]fileInfo{}
	for _, group := range bySize {
		if len(group) < 2 {
			continue
		}
		for _, f := range group {
			h, err := hashFile(f.path)
			if err != nil {
				continue
			}
			byHash[h] = append(byHash[h], f)
		}
	}
	var result [][]fileInfo
	for _, g := range byHash {
		if len(g) >= 2 {
			result = append(result, g)
		}
	}
	sort.Slice(result, func(i, j int) bool { return result[i][0].size > result[j][0].size })
	return result
}

func hashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func humanBytes(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f%ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

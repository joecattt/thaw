package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// spaceCmd is the read-only half of "what CleanMyMac does" — real sizes for
// the well-known bloat locations (caches, logs, trash, derived data,
// language-tool caches), sorted biggest-first. Deliberately does NOT delete
// anything, ever, regardless of flags: permanently deleting data is outside
// what this tool does on your behalf — the report tells you what's big and
// where, you decide what to clear.
func spaceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "space",
		Short: "What's taking up disk space — read-only, never deletes anything",
		Long: `Sizes of the well-known bloat locations (caches, logs, Trash, build
tool caches), sorted biggest-first, plus overall disk usage. This is a
report, not a cleaner — it never deletes or moves a single file. Where a
location is generally safe to clear by hand, the line says so; you decide.

  thaw space`,
		RunE: func(cmd *cobra.Command, args []string) error {
			printSpace()
			return nil
		},
	}
}

type spaceEntry struct {
	label string
	path  string
	note  string // shown when the location is generally safe to hand-clear; "" when it's not (e.g. Downloads)
}

func printSpace() {
	home, _ := os.UserHomeDir()
	fmt.Println(diskSnapshot("/System/Volumes/Data"))
	fmt.Println()

	entries := []spaceEntry{
		{"App & system caches", filepath.Join(home, "Library/Caches"), "generally safe — apps rebuild what they need"},
		{"Trash", filepath.Join(home, ".Trash"), "empty it in Finder when you're sure"},
		{"System logs (this account)", filepath.Join(home, "Library/Logs"), "generally safe, rotates on its own"},
		{"App containers", filepath.Join(home, "Library/Containers"), "app data — check before clearing, not pure cache"},
		{"Xcode DerivedData", filepath.Join(home, "Library/Developer/Xcode/DerivedData"), "safe — Xcode rebuilds it"},
		{"npm cache", filepath.Join(home, ".npm"), "safe — `npm cache clean --force` rebuilds on demand"},
		{"pip cache", filepath.Join(home, "Library/Caches/pip"), "safe — pip re-downloads on demand"},
		{"Homebrew cache", filepath.Join(home, "Library/Caches/Homebrew"), "safe — `brew cleanup` handles it"},
		{"Docker data", filepath.Join(home, "Library/Containers/com.docker.docker"), "images/volumes — check `docker system df` before touching"},
		{"Go build cache", filepath.Join(home, "Library/Caches/go-build"), "safe — rebuilt automatically"},
		{"Downloads", filepath.Join(home, "Downloads"), ""}, // real files, not cache — no "safe to clear" claim
	}

	type sized struct {
		spaceEntry
		bytes int64
		human string
	}
	var results []sized
	done := make(chan sized, len(entries))
	for _, e := range entries {
		go func(e spaceEntry) {
			b, h := dirSize(e.path)
			done <- sized{e, b, h}
		}(e)
	}
	for range entries {
		r := <-done
		if r.bytes > 0 {
			results = append(results, r)
		}
	}
	sort.Slice(results, func(i, j int) bool { return results[i].bytes > results[j].bytes })

	for _, r := range results {
		note := r.note
		if note == "" {
			note = "not a cache — real files"
		}
		fmt.Printf("  %-28s %8s   %s\n", r.label, r.human, note)
	}
	fmt.Println("\n  (report only — nothing here was deleted or moved; clear by hand if you want to)")
}

// dirSize shells to `du` rather than walking in Go — matches what Finder/
// CleanMyMac-style tools report, and du already skips permission-denied
// subtrees gracefully instead of erroring the whole scan. 15s timeout per
// path: a slow location just gets skipped, not a hung report.
func dirSize(path string) (int64, string) {
	if _, err := os.Stat(path); err != nil {
		return 0, ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "du", "-sh", path)
	out, err := cmd.Output()
	if err != nil {
		return 0, ""
	}
	fields := strings.Fields(string(out))
	if len(fields) == 0 {
		return 0, ""
	}
	human := fields[0]
	return humanToBytes(human), human
}

func humanToBytes(h string) int64 {
	if h == "" {
		return 0
	}
	unit := h[len(h)-1]
	var mult int64 = 1
	switch unit {
	case 'K':
		mult = 1 << 10
	case 'M':
		mult = 1 << 20
	case 'G':
		mult = 1 << 30
	case 'T':
		mult = 1 << 40
	default:
		return 0
	}
	var whole, frac int64
	numStr := h[:len(h)-1]
	if i := strings.Index(numStr, "."); i >= 0 {
		fmt.Sscanf(numStr[:i], "%d", &whole)
		fmt.Sscanf(numStr[i+1:], "%d", &frac)
		return whole*mult + frac*mult/10
	}
	fmt.Sscanf(numStr, "%d", &whole)
	return whole * mult
}

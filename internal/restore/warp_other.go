//go:build !darwin

package restore

import (
	"os"
	"time"
)

// fileBirthTime falls back to mtime where birth time isn't exposed. Best
// effort: mtime >= birth time, so a post-snapshot append can wrongly exclude
// a conversation here — acceptable, the Warp backend is macOS-gated anyway.
func fileBirthTime(fi os.FileInfo) time.Time {
	return fi.ModTime()
}

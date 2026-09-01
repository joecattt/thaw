//go:build darwin

package restore

import (
	"os"
	"syscall"
	"time"
)

// fileBirthTime returns when the file was created. On macOS the real birth
// time is available — needed because a lost conversation's mtime moves every
// time a failed `--resume` touches it.
func fileBirthTime(fi os.FileInfo) time.Time {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Birthtimespec.Sec, st.Birthtimespec.Nsec)
	}
	return fi.ModTime()
}

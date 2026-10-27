//go:build darwin

package cache

import (
	"os"
	"syscall"
	"time"
)

// atime is the last-use time the cache records. Ensure, Adopt and Touch set it
// explicitly, so LRU order does not depend on the volume's atime mount options.
func atime(info os.FileInfo) time.Time {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return time.Unix(st.Atimespec.Sec, st.Atimespec.Nsec)
	}
	return info.ModTime()
}

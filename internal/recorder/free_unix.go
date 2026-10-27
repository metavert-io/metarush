//go:build unix

package recorder

import "syscall"

// freeBytes is the free space where dir is (-1 when it can't be told).
func freeBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return -1
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

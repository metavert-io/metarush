//go:build darwin

package assistant

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// onDiskPath is a folder's path as the file system stores it (F_GETPATH):
// its own case and Unicode form, links followed — what getcwd() gives the
// Claude Code working in it, whatever spelling opened it. A folder named in
// decomposed form ("e" + U+0301) and entered by its composed spelling comes
// back decomposed, and Claude Code names its sessions folder from that.
func onDiskPath(dir string) string {
	f, err := os.Open(dir)
	if err != nil {
		return realPath(dir)
	}
	defer f.Close()
	buf := make([]byte, 1024) // MAXPATHLEN
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETPATH, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		return realPath(dir)
	}
	if i := strings.IndexByte(string(buf), 0); i > 0 {
		return string(buf[:i])
	}
	return realPath(dir)
}

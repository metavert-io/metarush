//go:build darwin

package abilities

import (
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// fdPath is the path the file system stores for an open file or folder
// (F_GETPATH): its own case and Unicode form, its links followed, whatever
// was typed to open it. "" when it won't say.
func fdPath(f *os.File) string {
	buf := make([]byte, 1024) // MAXPATHLEN
	if _, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), syscall.F_GETPATH, uintptr(unsafe.Pointer(&buf[0]))); errno != 0 {
		return ""
	}
	if i := strings.IndexByte(string(buf), 0); i > 0 {
		return string(buf[:i])
	}
	return ""
}

// foldCase: a Mac's volumes are usually case-insensitive, so paths are
// compared without case (stricter on the rare case-sensitive one).
const foldCase = true

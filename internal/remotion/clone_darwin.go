//go:build darwin

package remotion

import "golang.org/x/sys/unix"

// cloneOS makes an APFS clone of a file or a whole folder (no extra space
// until either side changes).
func cloneOS(src, dst string) error { return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW) }

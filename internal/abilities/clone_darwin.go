//go:build darwin

package abilities

import "golang.org/x/sys/unix"

// cloneFile makes an APFS copy-on-write clone (a set-up file copied into a
// run's room without using the space twice, D285).
func cloneFile(src, dst string) error { return unix.Clonefile(src, dst, unix.CLONE_NOFOLLOW) }

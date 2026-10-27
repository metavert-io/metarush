//go:build darwin

package cache

import "golang.org/x/sys/unix"

// cloneFile makes an APFS copy-on-write clone (no extra disk space).
func cloneFile(src, dst string) error { return unix.Clonefile(src, dst, 0) }

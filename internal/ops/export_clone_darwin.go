package ops

import "golang.org/x/sys/unix"

// cloneFile makes a copy-on-write clone (APFS): instant and no extra space;
// changing the clone never changes the original.
func cloneFile(src, dst string) error { return unix.Clonefile(src, dst, 0) }

//go:build unix

package cache

import (
	"errors"
	"syscall"
)

// ProcessAlive reports whether a process with this PID exists. A process owned by
// another user counts as alive (EPERM), so a lease is never broken by mistake.
func ProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}

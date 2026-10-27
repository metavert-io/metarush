//go:build darwin

package abilities

import "golang.org/x/sys/unix"

// darwinArgv reads a process's arguments from kern.procargs2 (a process of
// the same person; nil otherwise). The process can rewrite these, so they
// only add to the executable and working folder lsof gives.
func darwinArgv(pid int) []string {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil {
		return nil
	}
	return procArgv(b)
}

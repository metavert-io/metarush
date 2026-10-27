//go:build darwin

package cli

import "golang.org/x/sys/unix"

func underRosetta() bool {
	v, err := unix.SysctlUint32("sysctl.proc_translated")
	return err == nil && v == 1
}

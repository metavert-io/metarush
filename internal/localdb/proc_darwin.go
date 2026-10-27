package localdb

import (
	"bytes"
	"encoding/binary"

	"golang.org/x/sys/unix"
)

// procInfo is a running process's program and arguments, as the kernel
// recorded them when it started (kern.procargs2: argc, the program's path,
// then the arguments). ok is false for a process this user can't read.
func procInfo(pid int) (exe string, args []string, ok bool) {
	b, err := unix.SysctlRaw("kern.procargs2", pid)
	if err != nil || len(b) < 4 {
		return "", nil, false
	}
	argc := int(binary.LittleEndian.Uint32(b[:4]))
	b = b[4:]
	i := bytes.IndexByte(b, 0)
	if i < 0 {
		return "", nil, false
	}
	exe, b = string(b[:i]), b[i:]
	for len(b) > 0 && b[0] == 0 { // padding after the path
		b = b[1:]
	}
	for len(args) < argc && len(b) > 0 {
		j := bytes.IndexByte(b, 0)
		if j < 0 {
			args = append(args, string(b))
			break
		}
		args = append(args, string(b[:j]))
		b = b[j+1:]
	}
	return exe, args, true
}

//go:build darwin || linux

// Package procio keeps a process's protocol stream clean: servers that speak
// a protocol on standard output (the transcription model server, the MCP
// server) write it to a private copy, and anything else printed to standard
// output (C libraries, stray prints) goes to standard error instead.
package procio

import (
	"os"

	"golang.org/x/sys/unix"
)

// ProtocolStdout moves the protocol to a private copy of standard output and
// points file descriptor 1 at standard error, so nothing else can land in the
// middle of a message. os.Stdout itself then writes to standard error.
func ProtocolStdout() (*os.File, error) {
	fd, err := unix.Dup(1)
	if err != nil {
		return nil, err
	}
	if err := dupOnto(2, 1); err != nil {
		_ = unix.Close(fd)
		return nil, err
	}
	unix.CloseOnExec(fd)
	return os.NewFile(uintptr(fd), "protocol"), nil
}

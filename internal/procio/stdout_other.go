//go:build !darwin && !linux

package procio

import "os"

// ProtocolStdout returns standard output as is on other systems.
func ProtocolStdout() (*os.File, error) { return os.Stdout, nil }

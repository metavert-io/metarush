//go:build darwin

package worker

import (
	"golang.org/x/sys/unix"
)

func memoryGB() float64 {
	v, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return float64(v) / (1 << 30)
}

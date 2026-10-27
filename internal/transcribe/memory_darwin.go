package transcribe

import "golang.org/x/sys/unix"

// physicalMemory is the machine's RAM in bytes (0 when unknown).
func physicalMemory() int64 {
	v, err := unix.SysctlUint64("hw.memsize")
	if err != nil {
		return 0
	}
	return int64(v)
}

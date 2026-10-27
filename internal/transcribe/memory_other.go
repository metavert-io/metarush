//go:build !darwin

package transcribe

import (
	"os"
	"strconv"
	"strings"
)

// physicalMemory is the machine's RAM in bytes (0 when unknown).
func physicalMemory() int64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "MemTotal:" {
			kb, _ := strconv.ParseInt(f[1], 10, 64)
			return kb << 10
		}
	}
	return 0
}

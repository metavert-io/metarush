//go:build linux

package transcribe

import (
	"os"
	"strconv"
	"strings"
)

// processPeakMemory is another process's (a child's) peak resident memory in
// bytes (VmHWM), 0 when unknown.
func processPeakMemory(pid int) int64 {
	if pid <= 0 {
		return 0
	}
	b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/status")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if f := strings.Fields(l); len(f) >= 2 && f[0] == "VmHWM:" {
			kb, _ := strconv.ParseInt(f[1], 10, 64)
			return kb << 10
		}
	}
	return 0
}

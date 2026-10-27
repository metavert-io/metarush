//go:build !darwin

package worker

import (
	"os"
	"strconv"
	"strings"
)

func memoryGB() float64 {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "MemTotal:") {
			f := strings.Fields(l)
			if len(f) >= 2 {
				kb, _ := strconv.ParseFloat(f[1], 64)
				return kb / (1 << 20)
			}
		}
	}
	return 0
}

package localdb

import (
	"os"
	"strings"
)

// readKernel reads the kernel's release and version (what uname says) and,
// on Ubuntu, its version signature.
func readKernel() Kernel {
	first := func(p string) string {
		b, err := os.ReadFile(p)
		if err != nil {
			return ""
		}
		line, _, _ := strings.Cut(string(b), "\n")
		return strings.TrimSpace(line)
	}
	return Kernel{Release: first("/proc/sys/kernel/osrelease"), Version: first("/proc/sys/kernel/version"), Signature: first("/proc/version_signature")}
}

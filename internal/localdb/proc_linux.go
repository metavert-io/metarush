package localdb

import (
	"os"
	"strconv"
	"strings"
)

// procInfo is a running process's program and arguments (/proc).
func procInfo(pid int) (exe string, args []string, ok bool) {
	dir := "/proc/" + strconv.Itoa(pid)
	exe, err := os.Readlink(dir + "/exe")
	if err != nil {
		return "", nil, false
	}
	b, err := os.ReadFile(dir + "/cmdline")
	if err != nil {
		return "", nil, false
	}
	return exe, strings.Split(strings.TrimRight(string(b), "\x00"), "\x00"), true
}

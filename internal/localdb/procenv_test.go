package localdb

import (
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
)

// processEnv is a running process's environment, as the system shows it.
func processEnv(t *testing.T, pid int) string {
	t.Helper()
	if pid == 0 {
		t.Fatal("no process")
	}
	switch runtime.GOOS {
	case "linux":
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/environ")
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	case "darwin":
		out, err := exec.Command("/bin/ps", "eww", "-o", "command=", "-p", strconv.Itoa(pid)).Output()
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	t.Skip("no way to read another process's environment here")
	return ""
}

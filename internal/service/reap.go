package service

import (
	"errors"
	"strings"
	"syscall"
	"time"
)

// With KillMode=process (the unit), stopping the service signals only the
// daemon, so the database on this computer (D254), which runs in the same
// group, is never stopped with it. When the daemon crashed, though, the
// other programs it ran (renders, ffmpeg, Claude Code) would be left behind:
// the unit's ExecStopPost runs `metarush service reap`, which stops every
// process left in the service's own group except the database server. It
// never signals a process that is no longer in that group: each process is
// held by a pidfd from the moment the list is read (Linux 5.3 and later),
// else its group is read again right before each signal (a number can be
// reused once a process exits).

// Reapable is what Reap would stop: every process in the group but this one
// and the database server (comm "mongod").
func Reapable(pids []int, self int, comm func(int) string) []int {
	var out []int
	for _, p := range pids {
		if p == self || p <= 1 || comm(p) == "mongod" {
			continue
		}
		out = append(out, p)
	}
	return out
}

// Cgroups is what /proc/self/cgroup says about this process.
type Cgroups struct {
	// Unified is the cgroup v2 path ("0::<path>"), "" when there is none (a
	// host with cgroup v1 only).
	Unified string
	// V1 says the host also (hybrid) or only uses cgroup v1 hierarchies.
	V1 bool
}

// ParseCgroups reads /proc/self/cgroup: "0::<path>" is the unified
// hierarchy (cgroup v2, alone or beside v1 on a hybrid host);
// "<n>:<controllers>:<path>" lines are v1 hierarchies.
func ParseCgroups(content string) Cgroups {
	var c Cgroups
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "0::"); ok {
			c.Unified = rest
			continue
		}
		c.V1 = true
	}
	return c
}

// UnifiedRoot is where the unified hierarchy is mounted: /sys/fs/cgroup on a
// cgroup v2 host, /sys/fs/cgroup/unified on a hybrid one; "" when neither
// is (exists says whether a path exists).
func UnifiedRoot(exists func(string) bool) string {
	for _, root := range []string{"/sys/fs/cgroup", "/sys/fs/cgroup/unified"} {
		if exists(root + "/cgroup.controllers") {
			return root
		}
	}
	return ""
}

// ErrNotInService refuses Reap outside the service's own group: run from a
// terminal it would reach the person's own programs.
var ErrNotInService = errors.New("this runs only as the background service's clean-up")

// reapTarget is one process to stop. Signal sends a signal only while it is
// still the process the list named, in the service's group; Gone says it has
// exited (or is no longer that process).
type reapTarget interface {
	Signal(sig syscall.Signal) error
	Gone() bool
	Close()
}

// reapAll asks every target to stop (TERM), waits up to wait, then KILLs the
// ones still there — through each target, so a number reused by an unrelated
// process in the meantime is never signalled.
func reapAll(targets []reapTarget, wait time.Duration, pause func(time.Duration)) int {
	signalled := 0
	for _, t := range targets {
		if t.Signal(syscall.SIGTERM) == nil {
			signalled++
		}
	}
	for waited := time.Duration(0); waited < wait; waited += 250 * time.Millisecond {
		left := false
		for _, t := range targets {
			if !t.Gone() {
				left = true
			}
		}
		if !left {
			break
		}
		pause(250 * time.Millisecond)
	}
	for _, t := range targets {
		if !t.Gone() {
			_ = t.Signal(syscall.SIGKILL)
		}
		t.Close()
	}
	return signalled
}

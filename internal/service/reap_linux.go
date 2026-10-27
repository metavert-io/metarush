package service

import (
	"errors"
	"os"
	"path"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// Reap stops what the daemon left in the service's group (systemd): TERM, up
// to ten seconds, then KILL; never the database server, never a process that
// left the group. unit is the service's name (Options.Label): the group must
// be that unit's. On a host with cgroup v1 only there is nothing to do (note
// says so).
func Reap(unit string) (pids []int, note string, err error) {
	if os.Getenv("METARUSH_SERVICE") != "systemd" {
		return nil, "", ErrNotInService
	}
	b, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return nil, "", ErrNotInService
	}
	cg := ParseCgroups(string(b))
	if cg.Unified == "" {
		return nil, "cgroup v1 only: systemd stops the service's programs itself here", nil
	}
	if path.Base(cg.Unified) != unit {
		return nil, "", ErrNotInService
	}
	root := UnifiedRoot(func(p string) bool { _, err := os.Stat(p); return err == nil })
	if root == "" {
		return nil, "no unified cgroup hierarchy is mounted: nothing to do", nil
	}
	procs, err := os.ReadFile(root + cg.Unified + "/cgroup.procs")
	if err != nil {
		return nil, "", err
	}
	var all []int
	for _, f := range strings.Fields(string(procs)) {
		if n, err := strconv.Atoi(f); err == nil {
			all = append(all, n)
		}
	}
	inGroup := func(pid int) bool {
		b, err := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/cgroup")
		return err == nil && ParseCgroups(string(b)).Unified == cg.Unified
	}
	var targets []reapTarget
	for _, pid := range Reapable(all, os.Getpid(), comm) {
		t := &linuxTarget{pid: pid, fd: -1, inGroup: inGroup}
		if fd, err := unix.PidfdOpen(pid, 0); err == nil {
			t.fd = fd
			// Held from now on; checked once more that it is the group's
			// process the list named (not a number reused in between).
			if !inGroup(pid) || comm(pid) == "mongod" {
				t.Close()
				continue
			}
		} else if !errors.Is(err, syscall.ENOSYS) && !errors.Is(err, syscall.EPERM) {
			continue // gone already
		}
		targets = append(targets, t)
		pids = append(pids, pid)
	}
	reapAll(targets, 10*time.Second, time.Sleep)
	return pids, "", nil
}

func comm(pid int) string {
	b, _ := os.ReadFile("/proc/" + strconv.Itoa(pid) + "/comm")
	return strings.TrimSpace(string(b))
}

// linuxTarget signals through its pidfd when it has one; without one (an
// older kernel) it reads the process's group again right before each signal.
type linuxTarget struct {
	pid     int
	fd      int
	inGroup func(int) bool
}

func (t *linuxTarget) Signal(sig syscall.Signal) error {
	if t.fd >= 0 {
		return unix.PidfdSendSignal(t.fd, sig, nil, 0)
	}
	if !t.inGroup(t.pid) || comm(t.pid) == "mongod" {
		return syscall.ESRCH
	}
	return syscall.Kill(t.pid, sig)
}

func (t *linuxTarget) Gone() bool {
	if t.fd >= 0 {
		return unix.PidfdSendSignal(t.fd, 0, nil, 0) != nil
	}
	return syscall.Kill(t.pid, 0) != nil || !t.inGroup(t.pid)
}

func (t *linuxTarget) Close() {
	if t.fd >= 0 {
		_ = unix.Close(t.fd)
		t.fd = -1
	}
}

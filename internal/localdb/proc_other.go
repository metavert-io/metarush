//go:build !darwin && !linux

package localdb

func procInfo(pid int) (string, []string, bool) { return "", nil, false }

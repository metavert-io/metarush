//go:build !unix

package cache

// ProcessAlive cannot inspect other processes on this platform, so it is
// conservative: every lease counts as live and its objects are never evicted.
func ProcessAlive(pid int) bool { return pid > 0 }

package localdb

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"testing"
)

// The ports homes on this computer chose for their databases, so two homes
// never pick the same one even while one of them is stopped:
// <the user's config folder>/MetaRush/database-ports.json, port → home. An
// entry counts only while that home's database is set up on that port
// (<home>/database/state.json says so); others are dropped when it is read.

// registryFile is where the list is; tests set it (under `go test` there is
// none unless a test names one, so tests never touch the person's own).
var registryFile = func() string {
	if testing.Testing() {
		return ""
	}
	d, err := os.UserConfigDir()
	if err != nil {
		return ""
	}
	return filepath.Join(d, "MetaRush", "database-ports.json")
}

// withRegistry reads the list under its lock, lets fn change it, and writes
// it back when fn says so.
func withRegistry(fn func(ports map[string]string) (write bool, err error)) error {
	p := registryFile()
	if p == "" {
		_, err := fn(map[string]string{})
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	lf, err := os.OpenFile(p+".lock", os.O_CREATE|os.O_RDONLY, 0o600)
	if err != nil {
		return err
	}
	defer lf.Close()
	if err := syscall.Flock(int(lf.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN)
	ports := map[string]string{}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &ports)
	}
	for port, home := range ports { // only homes still set up on that port
		if n, err := strconv.Atoi(port); err != nil || !setUpOn(home, n) {
			delete(ports, port)
		}
	}
	write, err := fn(ports)
	if err != nil || !write {
		return err
	}
	b, _ := json.MarshalIndent(ports, "", "  ")
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func setUpOn(home string, port int) bool {
	st, ok := New(home, 0, nil).ReadState()
	return ok && st.Port == port
}

// PickPort finds a port for a home's database: one between PortFirst and
// PortLast that no other home on this computer chose and nothing uses now.
func PickPort(home string) (int, error) {
	picked := 0
	err := withRegistry(func(ports map[string]string) (bool, error) {
		for p := PortFirst; p <= PortLast; p++ {
			if other, ok := ports[strconv.Itoa(p)]; ok && filepath.Clean(other) != filepath.Clean(home) {
				continue
			}
			if p != TestPort && portFree(p) {
				picked = p
				return false, nil
			}
		}
		return false, nil
	})
	if err != nil {
		return 0, err
	}
	if picked == 0 {
		return 0, fmt.Errorf("no free port was found for the database on this computer")
	}
	return picked, nil
}

// reserve records a home's port, refusing one another home on this computer
// has already chosen.
func reserve(home string, port int) error {
	return withRegistry(func(ports map[string]string) (bool, error) {
		key := strconv.Itoa(port)
		if other, ok := ports[key]; ok && filepath.Clean(other) != filepath.Clean(home) {
			return false, fmt.Errorf("another MetaRush on this computer keeps its database on port %d", port)
		}
		for p, h := range ports { // a home has one port
			if filepath.Clean(h) == filepath.Clean(home) {
				delete(ports, p)
			}
		}
		ports[key] = filepath.Clean(home)
		return true, nil
	})
}

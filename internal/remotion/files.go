package remotion

import (
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

// placeFile puts a copy of src at dst without using more space where it can:
// an APFS clone, else a hard link, else a copy. The result is a regular file
// (Remotion's own file server refuses links).
func placeFile(src, dst string) error {
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dst)
	if cloneOS(src, dst) == nil {
		return nil
	}
	if os.Link(src, dst) == nil {
		return nil
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// cloneTree copies a folder: one APFS clone where it can, else file by file
// (hard links, else copies). Links inside are copied as links.
func cloneTree(src, dst string) error {
	if cloneOS(src, dst) == nil {
		return nil
	}
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type()&os.ModeSymlink != 0:
			l, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(l, target)
		}
		if err := os.Link(p, target); err == nil {
			return nil
		}
		if err := copyFile(p, target); err != nil {
			return err
		}
		if info, err := d.Info(); err == nil && info.Mode()&0o111 != 0 {
			return os.Chmod(target, 0o755)
		}
		return nil
	})
}

// flock is a lock file held until release (released by the system when a
// process dies).
type flock struct{ f *os.File }

func (l *flock) release() {
	if l == nil || l.f == nil {
		return
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	_ = l.f.Close()
}

// lockFile takes an exclusive lock, waiting for another holder.
func lockFile(p string) (*flock, error) { return lockHow(p, syscall.LOCK_EX) }

// lockShared takes a shared lock (readers), waiting for an exclusive holder.
func lockShared(p string) (*flock, error) { return lockHow(p, syscall.LOCK_SH) }

func lockHow(p string, how int) (*flock, error) {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err := syscall.Flock(int(f.Fd()), how)
		if err == nil {
			return &flock{f: f}, nil
		}
		if !errors.Is(err, syscall.EINTR) {
			_ = f.Close()
			return nil, err
		}
	}
}

// relock changes a held lock (exclusive ↔ shared).
func (l *flock) relock(how int) error { return syscall.Flock(int(l.f.Fd()), how) }

// tryLockFile takes an exclusive lock without waiting: nil when someone holds it.
func tryLockFile(p string) (*flock, error) {
	f, err := os.OpenFile(p, os.O_CREATE|os.O_RDONLY, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		_ = f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, nil
		}
		return nil, err
	}
	return &flock{f: f}, nil
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

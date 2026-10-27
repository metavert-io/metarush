//go:build !darwin

package abilities

import (
	"os"
	"path/filepath"
)

// fdPath is an open file's path with its links followed.
func fdPath(f *os.File) string {
	p, err := filepath.EvalSymlinks(f.Name())
	if err != nil {
		return ""
	}
	return p
}

const foldCase = false

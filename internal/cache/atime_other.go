//go:build !darwin && !linux

package cache

import (
	"os"
	"time"
)

func atime(info os.FileInfo) time.Time { return info.ModTime() }

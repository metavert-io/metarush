package handover

import (
	"os"
	"syscall"
)

// NoHardLinks stands in for a drive without hard links (exFAT, FAT) until
// the returned func puts things back.
func NoHardLinks() func() {
	was := linkFile
	linkFile = func(from, to string) error {
		return &os.LinkError{Op: "link", Old: from, New: to, Err: syscall.ENOTSUP}
	}
	return func() { linkFile = was }
}

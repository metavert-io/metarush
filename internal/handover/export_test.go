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

// WhileReceiving runs f once the conversations came, before the handover is
// recorded as received, until the returned func puts things back.
func WhileReceiving(f func(handover string)) func() {
	receiveHook = f
	return func() { receiveHook = nil }
}

// AfterMarking runs f once an export marked its conversations, until the
// returned func puts things back.
func AfterMarking(f func(handover string)) func() {
	markedHook = f
	return func() { markedHook = nil }
}

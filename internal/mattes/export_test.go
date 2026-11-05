package mattes

import (
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
)

// FreeBytesForTest stands in for the room this computer's disks have.
func FreeBytesForTest(f func(dir string) (int64, bool)) (restore func()) {
	old := freeBytes
	freeBytes = f
	return func() { freeBytes = old }
}

// DisksForTest stands in for which disk holds a folder.
func DisksForTest(f func(dir string) (uint64, bool)) (restore func()) {
	old := diskOf
	diskOf = f
	return func() { diskOf = old }
}

// RoomWaits is how many times in a row a cut-out waits for a computer with room.
const RoomWaits = roomWaits

// RoomCheckForTest is roomCheck.
func RoomCheckForTest(a *app.App, m *models.Matte) error { return roomCheck(a, m) }

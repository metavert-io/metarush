package mattes

// FreeBytesForTest stands in for the room this computer has.
func FreeBytesForTest(f func(dir string) (int64, bool)) (restore func()) {
	old := freeBytes
	freeBytes = f
	return func() { freeBytes = old }
}

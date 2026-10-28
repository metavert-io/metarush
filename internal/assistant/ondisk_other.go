//go:build !darwin

package assistant

// onDiskPath is a folder's path with its links followed: elsewhere the file
// system keeps names as given, so that is what getcwd() says too.
func onDiskPath(dir string) string { return realPath(dir) }

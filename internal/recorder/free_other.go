//go:build !unix

package recorder

// freeBytes can't tell the free space here: the floor isn't checked.
func freeBytes(string) int64 { return -1 }

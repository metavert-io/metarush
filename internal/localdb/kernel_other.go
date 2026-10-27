//go:build !linux

package localdb

// readKernel is the zero Kernel off Linux: MongoDB's kernel check is Linux's.
func readKernel() Kernel { return Kernel{} }

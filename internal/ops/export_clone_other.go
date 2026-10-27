//go:build !darwin

package ops

func cloneFile(src, dst string) error { return errNoClone }

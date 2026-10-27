//go:build !darwin

package remotion

import "errors"

func cloneOS(src, dst string) error { return errors.New("clones are not supported here") }

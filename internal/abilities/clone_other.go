//go:build !darwin

package abilities

import "errors"

func cloneFile(src, dst string) error { return errors.New("clone not supported") }

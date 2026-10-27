//go:build !darwin

package cache

import "errors"

func cloneFile(src, dst string) error { return errors.New("clone not supported") }

//go:build !linux

package service

// Reap is the systemd service's clean-up: elsewhere there is none.
func Reap(unit string) ([]int, string, error) { return nil, "", ErrNotInService }

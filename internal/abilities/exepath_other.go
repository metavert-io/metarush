//go:build !darwin

package abilities

func darwinArgv(int) []string { return nil }

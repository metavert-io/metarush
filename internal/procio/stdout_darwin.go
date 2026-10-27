package procio

import "golang.org/x/sys/unix"

func dupOnto(from, to int) error { return unix.Dup2(from, to) }

package sandbox

import (
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"sync"
	"syscall"
)

// Forward runs inside a Linux sandbox whose network is its own (bubblewrap's
// --unshare-net leaves only a loopback device): it listens on listen (a
// loopback address), hands every connection to the proxy's socket at to (a
// file in the room, mounted in), then runs the program and exits with its
// status. The program reaches MetaRush's proxy at the same address it would
// on a Mac, and nothing else.
func Forward(listen, to string, argv []string) int {
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "sandbox-forward: no program to run")
		return 2
	}
	host, _, err := net.SplitHostPort(listen)
	if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
		fmt.Fprintln(os.Stderr, "sandbox-forward: it listens only on this computer's loopback address")
		return 2
	}
	ln, err := net.Listen("tcp", listen)
	if err != nil {
		fmt.Fprintln(os.Stderr, "sandbox-forward:", err)
		return 2
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go pipeTo(c, to)
		}
	}()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	cmd.Env = os.Environ()
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "sandbox-forward:", err)
		return 127
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range sigs {
			_ = cmd.Process.Signal(s)
		}
	}()
	err = cmd.Wait()
	signal.Stop(sigs)
	close(sigs)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if st, ok := ee.Sys().(syscall.WaitStatus); ok && st.Signaled() {
			return 128 + int(st.Signal())
		}
		return ee.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}

// ForwardMain runs the forwarder from its command line (the arguments after
// ForwardCommand: --listen <addr> --to <socket> -- <program> [args…]) and
// returns the exit status.
func ForwardMain(args []string) int {
	var listen, to string
	for len(args) > 0 {
		switch args[0] {
		case "--listen":
			if len(args) < 2 {
				return 2
			}
			listen, args = args[1], args[2:]
		case "--to":
			if len(args) < 2 {
				return 2
			}
			to, args = args[1], args[2:]
		case "--":
			return Forward(listen, to, args[1:])
		default:
			fmt.Fprintln(os.Stderr, "sandbox-forward: unexpected", args[0])
			return 2
		}
	}
	fmt.Fprintln(os.Stderr, "sandbox-forward: no program to run")
	return 2
}

// pipeTo joins a connection to the proxy's socket.
func pipeTo(c net.Conn, to string) {
	defer c.Close()
	u, err := net.Dial("unix", to)
	if err != nil {
		return
	}
	defer u.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(u, c)
		if cw, ok := u.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(c, u)
		if cw, ok := c.(interface{ CloseWrite() error }); ok {
			_ = cw.CloseWrite()
		}
	}()
	wg.Wait()
}

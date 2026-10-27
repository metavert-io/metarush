// Command fakeheygen runs the fake HeyGen API on a loopback address, for
// browser runs and trying stock presenters without a HeyGen account. It never
// talks to the network and accepts only the key given here.
//
//	go run ./internal/testutil/fakeheygen/cmd/fakeheygen -addr 127.0.0.1:5513 -key-file <file>
//	METARUSH_HEYGEN_BASE_URL=http://127.0.0.1:5513 metarush --home <test home> serve
//
// The key is read from a file (or stdin with -key-file -) rather than the
// command line, like every other key in MetaRush.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/metavert-io/metarush/internal/testutil/fakeheygen"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5513", "loopback address to listen on")
	keyFile := flag.String("key-file", "-", "file holding the only key the fake accepts (- for stdin)")
	flag.Parse()

	in := os.Stdin
	if *keyFile != "-" {
		f, err := os.Open(*keyFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakeheygen:", err)
			os.Exit(1)
		}
		defer f.Close()
		in = f
	}
	line, _ := bufio.NewReader(in).ReadString('\n')
	key := strings.TrimSpace(line)
	if len(key) < 16 {
		fmt.Fprintln(os.Stderr, "fakeheygen: the key must be at least 16 characters")
		os.Exit(1)
	}
	s, err := fakeheygen.NewAt(key, *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeheygen:", err)
		os.Exit(1)
	}
	defer s.Close()
	fmt.Printf("fake HeyGen API on %s (set METARUSH_HEYGEN_BASE_URL to it)\n", s.URL)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}

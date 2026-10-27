// Command fakedid runs the fake D-ID API on a loopback address, for browser
// runs and trying presenters without a D-ID account. It never talks to the
// network beyond links on this computer and accepts only the key given here.
//
//	go run ./internal/testutil/fakedid/cmd/fakedid -addr 127.0.0.1:5512 -key-file <file>
//	METARUSH_D_ID_BASE_URL=http://127.0.0.1:5512 metarush --home <test home> serve
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

	"github.com/metavert-io/metarush/internal/testutil/fakedid"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5512", "loopback address to listen on")
	keyFile := flag.String("key-file", "-", "file holding the only key the fake accepts (- for stdin)")
	presenter := flag.String("presenter", "", "a finished presenter the fake already has (id=name), made against a green screen")
	flag.Parse()

	in := os.Stdin
	if *keyFile != "-" {
		f, err := os.Open(*keyFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakedid:", err)
			os.Exit(1)
		}
		defer f.Close()
		in = f
	}
	line, _ := bufio.NewReader(in).ReadString('\n')
	key := strings.TrimSpace(line)
	if len(key) < 16 {
		fmt.Fprintln(os.Stderr, "fakedid: the key must be at least 16 characters")
		os.Exit(1)
	}
	s, err := fakedid.NewAt(key, *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakedid:", err)
		os.Exit(1)
	}
	defer s.Close()
	if id, name, ok := strings.Cut(*presenter, "="); ok {
		s.GreenPresenter(id, name)
	}
	fmt.Printf("fake D-ID API on %s (set METARUSH_D_ID_BASE_URL to it)\n", s.URL)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}

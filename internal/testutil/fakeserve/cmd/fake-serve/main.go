// Command fake-serve stands in for `tailscale serve` in the remote-access
// end-to-end run (scripts/remote-e2e.sh). See internal/testutil/fakeserve.
//
//	fake-serve -name studio-mac.tail0000.ts.net -listen 127.0.0.1:5151 -backend http://127.0.0.1:5150 \
//	  -user 'maya=maya@example.com=Maya Lindqvist=100.101.102.104' [-spoof-listen … -spoof-backend … -spoof-from fe80::1%lo0] [-spki-file f]
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/metavert-io/metarush/internal/testutil/fakeserve"
)

type users []fakeserve.User

func (u *users) String() string { return fmt.Sprint(*u) }
func (u *users) Set(v string) error {
	p := strings.SplitN(v, "=", 4)
	if len(p) != 4 {
		return fmt.Errorf("want key=login=name=ip, got %q", v)
	}
	*u = append(*u, fakeserve.User{Key: p[0], Login: p[1], Name: p[2], IP: p[3]})
	return nil
}

func main() {
	var o fakeserve.Options
	var us users
	var spkiFile string
	flag.StringVar(&o.Name, "name", "studio-mac.tail0000.ts.net", "the tailnet name")
	flag.StringVar(&o.Listen, "listen", "127.0.0.1:5151", "HTTPS address for browsers")
	flag.StringVar(&o.Backend, "backend", "http://127.0.0.1:5150", "MetaRush's own address")
	flag.Var(&us, "user", "key=login=name=ip (repeatable)")
	flag.StringVar(&o.SpoofListen, "spoof-listen", "", "HTTPS address of the hop that keeps the browser's headers")
	flag.StringVar(&o.SpoofBackend, "spoof-backend", "", "where that hop connects")
	flag.StringVar(&o.SpoofFrom, "spoof-from", "fe80::1%lo0", "the source address it connects from")
	flag.StringVar(&o.SpoofHost, "spoof-host", "", "the Host that hop claims (the public address)")
	flag.StringVar(&spkiFile, "spki-file", "", "write the certificate's SPKI hash here")
	flag.Parse()
	o.Users = us
	s, err := fakeserve.Start(o)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake serve:", err)
		os.Exit(1)
	}
	if spkiFile != "" {
		_ = os.WriteFile(spkiFile, []byte(s.SPKI), 0o644)
	}
	fmt.Printf("fake tailscale serve: https://%s (listening on %s) -> %s\n", o.Name, o.Listen, o.Backend)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
	s.Close()
}

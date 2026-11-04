// Command fakeoidc runs the stand-in sign-in provider (internal/testutil/
// fakeoidc) on this computer for a browser run (scripts/sso-e2e.sh): its page
// asks who signs in (an email, checked or not), and it checks the app's
// client ID and secret, the registered addresses, PKCE and one use per code
// like a real provider. The secret comes from FAKEOIDC_SECRET, made by the
// run; keys are made when it starts. It is never a real provider.
package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/metavert-io/metarush/internal/testutil/fakeoidc"
)

func main() {
	listen := flag.String("listen", "127.0.0.1:5791", "where it listens (this computer only)")
	clientID := flag.String("client-id", "metarush-e2e", "the app it knows")
	redirects := flag.String("redirects", "", "the redirect addresses registered, separated by commas")
	flag.Parse()
	secret := os.Getenv("FAKEOIDC_SECRET")
	s, err := fakeoidc.NewAt(*listen, *clientID, secret)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeoidc:", err)
		os.Exit(1)
	}
	s.Ask = true
	for _, r := range strings.Split(*redirects, ",") {
		if r = strings.TrimSpace(r); r != "" {
			s.Redirects = append(s.Redirects, r)
		}
	}
	fmt.Println("fakeoidc: issuer", s.Issuer)
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	<-stop
	s.Close()
}

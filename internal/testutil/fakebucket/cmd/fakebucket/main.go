// Command fakebucket runs a private S3-compatible bucket on a loopback
// address for browser runs of outside reviewers (D313): MetaRush's client
// needs its key, a browser needs a link signed with it, and the bucket checks
// each link's signature, signed headers and expiry like a cloud bucket does.
// Nothing leaves this computer and no cloud account is involved.
//
//	go run ./internal/testutil/fakebucket/cmd/fakebucket -addr 127.0.0.1:5591 -bucket media -key-id <id> -secret-file <file>
//	curl -X POST 'http://127.0.0.1:5591/__fakebucket/ahead?by=192h'   # its clock a week and a day ahead
//
// The secret is read from a file (or stdin with -secret-file -) rather than
// the command line, like every other key in MetaRush.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/metavert-io/metarush/internal/testutil/fakebucket"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:5591", "loopback address to listen on")
	bucket := flag.String("bucket", "media", "the bucket's name")
	keyID := flag.String("key-id", "", "the only key ID the bucket accepts")
	secretFile := flag.String("secret-file", "-", "file holding the key's secret (- for stdin)")
	flag.Parse()
	host, _, err := net.SplitHostPort(*addr)
	if err != nil || (host != "127.0.0.1" && host != "::1" && host != "localhost") {
		fmt.Fprintln(os.Stderr, "fakebucket: listen on a loopback address only")
		os.Exit(2)
	}
	in := os.Stdin
	if *secretFile != "-" {
		f, err := os.Open(*secretFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakebucket:", err)
			os.Exit(1)
		}
		defer f.Close()
		in = f
	}
	line, _ := bufio.NewReader(in).ReadString('\n')
	s, err := fakebucket.New(*keyID, strings.TrimSpace(line), *bucket, true)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakebucket:", err)
		os.Exit(2)
	}
	srv := &http.Server{Addr: *addr, Handler: s, ReadHeaderTimeout: 10 * time.Second}
	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
		<-c
		_ = srv.Close()
	}()
	fmt.Printf("fakebucket: bucket %s on http://%s\n", *bucket, *addr)
	if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		fmt.Fprintln(os.Stderr, "fakebucket:", err)
		os.Exit(1)
	}
}

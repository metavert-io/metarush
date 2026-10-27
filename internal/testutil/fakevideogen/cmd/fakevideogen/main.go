// Command fakevideogen runs the fake Gemini video API on a loopback address,
// for the acceptance run and the dashboard's browser checks (Generate a shot,
// looks, Settings → Integrations) without a Google account or key. It never
// talks to the network and accepts only the key given here.
//
//	go run ./internal/testutil/fakevideogen/cmd/fakevideogen -addr 127.0.0.1:4890 -key-file <file>
//	METARUSH_GEMINI_BASE_URL=http://127.0.0.1:4890 metarush --home <test home> serve
//
// -free-tier status plays a key whose Google project is on the free tier (every
// request accepted, every status check answered with Google's 429 of 2026-10-20);
// a prompt with [fake:free-tier] does the same for one request.
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

	"github.com/metavert-io/metarush/internal/testutil/fakevideogen"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4890", "loopback address to listen on (next to fakeeleven's 4889)")
	keyFile := flag.String("key-file", "-", "file holding the only key the fake accepts (- for stdin)")
	ready := flag.Int("ready-after", 1, "how many checks a video needs before it is finished")
	freeTier := flag.String("free-tier", "", "put the key's project on Google's free tier: status (requests accepted, every status check refused) or start (refused when sent)")
	flag.Parse()
	if *freeTier != "" && *freeTier != fakevideogen.FreeTierOnStatus && *freeTier != fakevideogen.FreeTierOnStart {
		fmt.Fprintln(os.Stderr, "fakevideogen: -free-tier is status or start")
		os.Exit(2)
	}

	in := os.Stdin
	if *keyFile != "-" {
		f, err := os.Open(*keyFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakevideogen:", err)
			os.Exit(1)
		}
		defer f.Close()
		in = f
	}
	line, _ := bufio.NewReader(in).ReadString('\n')
	key := strings.TrimSpace(line)
	if len(key) < 16 {
		fmt.Fprintln(os.Stderr, "fakevideogen: the key must be at least 16 characters")
		os.Exit(1)
	}
	s, err := fakevideogen.NewAt(key, *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakevideogen:", err)
		os.Exit(1)
	}
	s.ReadyAfter = *ready
	s.SetFreeTier(*freeTier)
	defer s.Close()
	fmt.Printf("fake Gemini video API on %s (set METARUSH_GEMINI_BASE_URL to it)\n", s.URL)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}

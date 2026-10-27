// Command fakeeleven runs the fake ElevenLabs API on a loopback address, for
// trying the dashboard's Integrations screen and narration flows (and taking
// screenshots of them) without an ElevenLabs account. It never talks to the
// network and accepts only the key given here.
//
//	go run ./internal/testutil/fakeeleven/cmd/fakeeleven -addr 127.0.0.1:4889 -key-file <file>
//	METARUSH_ELEVENLABS_BASE_URL=http://127.0.0.1:4889 metarush --home <test home> serve
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

	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:4889", "loopback address to listen on")
	keyFile := flag.String("key-file", "-", "file holding the only key the fake accepts (- for stdin)")
	speechDir := flag.String("speech-dir", "", "folder of speech made beforehand (<first 16 hex of SHA-256 of the words>.wav): AI speech of those words")
	flag.Parse()

	in := os.Stdin
	if *keyFile != "-" {
		f, err := os.Open(*keyFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "fakeeleven:", err)
			os.Exit(1)
		}
		defer f.Close()
		in = f
	}
	line, _ := bufio.NewReader(in).ReadString('\n')
	key := strings.TrimSpace(line)
	if len(key) < 16 {
		fmt.Fprintln(os.Stderr, "fakeeleven: the key must be at least 16 characters")
		os.Exit(1)
	}
	s, err := fakeeleven.NewAt(key, *addr)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fakeeleven:", err)
		os.Exit(1)
	}
	defer s.Close()
	s.SpeechDir = *speechDir
	fmt.Printf("fake ElevenLabs API on %s (set METARUSH_ELEVENLABS_BASE_URL to it)\n", s.URL)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	<-sig
}

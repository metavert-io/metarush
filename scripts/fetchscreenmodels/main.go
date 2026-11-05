// Command fetchscreenmodels downloads the open models that look at what's on
// screen, cut subjects out and tell people in footage from their background
// (internal/visionmodels.Files: the publisher's
// address, its SHA-256 and size) into a folder, for measuring them against
// Apple Vision (go test -tags screeneval ./internal/visionmodels/) and for
// computers set up by hand. Each file is checked against its pinned SHA-256
// before it is kept; a file already there and matching is left alone. It
// runs outside go test (tests never download), like scripts/fetch-speech.sh.
//
//	go run ./scripts/fetchscreenmodels [-part index|cut-out|matte|all] <folder>
//	METARUSH_SCREEN_MODELS=<folder> go test -tags screeneval -run TestCompareBackends -timeout 60m ./internal/visionmodels/
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/metavert-io/metarush/internal/visionmodels"
)

func main() {
	part := flag.String("part", "all", "index, cut-out, matte or all")
	flag.Parse()
	if flag.NArg() != 1 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/fetchscreenmodels [-part index|cut-out|matte|all] <folder>")
		os.Exit(2)
	}
	dir := flag.Arg(0)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		fail(err)
	}
	store := visionmodels.Store{Dir: dir}
	client := &http.Client{Timeout: 60 * time.Minute}
	for _, f := range visionmodels.Files {
		if *part != "all" && f.Part != *part {
			continue
		}
		if store.Verified(f) == nil {
			fmt.Printf("%s: here\n", f.Name)
			continue
		}
		fmt.Printf("%s: downloading %d MB from %s\n", f.Name, (f.Bytes+500_000)/1_000_000, f.URL)
		if err := fetch(client, f, store.Path(f)); err != nil {
			fail(fmt.Errorf("%s: %w", f.Name, err))
		}
	}
}

func fetch(c *http.Client, f visionmodels.File, dst string) error {
	resp, err := c.Get(f.URL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("answered %d", resp.StatusCode)
	}
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(resp.Body, f.Bytes+1))
	if cerr := tmp.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return err
	}
	if got := hex.EncodeToString(h.Sum(nil)); n != f.Bytes || got != f.SHA256 {
		return fmt.Errorf("not the pinned file (%d bytes, SHA-256 %s)", n, got)
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

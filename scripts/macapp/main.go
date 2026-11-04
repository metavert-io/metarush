// macapp is scripts/mac-app.sh's Go side (D477, D479): the bundle version Sparkle
// compares, and the appcast entry for a built MetaRush.app archive.
//
//	go run ./scripts/macapp bundle-version 0.16.0-dev.3        → 0.16.0.3
//	go run ./scripts/macapp appcast --archive MetaRush-0.16.0.zip --url <its address> \
//	    --short-version 0.16.0 --version 0.16.0 --minimum-system 13.0 --signature <EdDSA, base64> \
//	    [--changelog CHANGELOG.md] [--title MetaRush] [--link <project page>] [--date <RFC 3339>] --out appcast.xml
//
// It never signs anything: the signature comes from Sparkle's sign_update (scripts/mac-app.sh sign-update).
package main

import (
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/metavert-io/metarush/internal/macapp"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	switch os.Args[1] {
	case "bundle-version":
		if len(os.Args) != 3 {
			usage()
		}
		v, err := macapp.BundleVersion(os.Args[2])
		if err != nil {
			fail(err)
		}
		fmt.Println(v)
	case "appcast":
		if err := appcast(os.Args[2:]); err != nil {
			fail(err)
		}
	default:
		usage()
	}
}

func appcast(args []string) error {
	fs := flag.NewFlagSet("appcast", flag.ExitOnError)
	archive := fs.String("archive", "", "the zip Sparkle downloads")
	url := fs.String("url", "", "the archive's address once published")
	short := fs.String("short-version", "", "CFBundleShortVersionString (the MetaRush version)")
	bundle := fs.String("version", "", "CFBundleVersion (scripts/macapp bundle-version)")
	minSys := fs.String("minimum-system", "13.0", "LSMinimumSystemVersion")
	sig := fs.String("signature", "", "the archive's EdDSA signature (sign_update -p)")
	changelog := fs.String("changelog", "", "CHANGELOG.md, for what's new in this version")
	title := fs.String("title", "MetaRush", "the feed's title")
	link := fs.String("link", "", "the project's page")
	date := fs.String("date", "", "when it was published (RFC 3339; default now)")
	out := fs.String("out", "appcast.xml", "where to write the feed")
	_ = fs.Parse(args)
	if *archive == "" || *url == "" || *short == "" || *bundle == "" {
		return fmt.Errorf("--archive, --url, --short-version and --version are needed")
	}
	st, err := os.Stat(*archive)
	if err != nil {
		return err
	}
	when := time.Now().UTC()
	if *date != "" {
		if when, err = time.Parse(time.RFC3339, *date); err != nil {
			return fmt.Errorf("--date: %w", err)
		}
	}
	notes := ""
	if *changelog != "" {
		b, err := os.ReadFile(*changelog)
		if err != nil {
			return err
		}
		notes = macapp.Notes(string(b), *short)
	}
	feed := macapp.Feed{Title: *title, Link: *link, Items: []macapp.Item{{Title: *title + " " + *short, ShortVersion: *short, Version: *bundle,
		MinimumSystem: *minSys, Published: when, URL: *url, Length: st.Size(), EdSignature: *sig, NotesHTML: notes}}}
	b, err := feed.XML()
	if err != nil {
		return err
	}
	return os.WriteFile(*out, b, 0o644)
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: go run ./scripts/macapp bundle-version <version> | appcast --archive <zip> --url <address> --short-version <v> --version <bundle v> --signature <sig> [--changelog CHANGELOG.md] --out <file>")
	os.Exit(2)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "macapp:", err)
	os.Exit(1)
}

// Command sweeptestdbs finds throwaway test databases whose run is gone and,
// with --drop, drops them. Test runs drop their own databases, and every test
// process sweeps once before it makes its first one (internal/testutil, D194);
// this is the same sweep by hand, e.g. after a stopped script run. What counts
// as stale is in internal/testutil/testdbs. Only local metarush-test-*
// databases are looked at, and drops go through store.DropTestDatabase, which
// refuses anything but a local test database. It is a development helper (go
// run ./scripts/sweeptestdbs), not part of the metarush binary.
//
//	go run ./scripts/sweeptestdbs [--uri mongodb://127.0.0.1:27018/...] [--older 24h] [--legacy] [--drop]
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/testutil/testdbs"
)

func main() {
	uri := flag.String("uri", testutil.URI(), "the test MongoDB (local only)")
	older := flag.Duration("older", testdbs.DefaultOlder, "an unmarked test database made longer ago than this is stale")
	legacy := flag.Bool("legacy", false, "also count older-style test databases with no migrations as stale")
	drop := flag.Bool("drop", false, "drop the stale ones (without it, only list them)")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	found, err := testdbs.Sweep(ctx, *uri, testdbs.Options{Older: *older, Legacy: *legacy, Drop: *drop})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(Report(found, *drop))
}

// Report is what the command prints: one line per stale database and a total.
func Report(found []testdbs.Found, drop bool) string {
	out := ""
	stale, kept := 0, 0
	for _, f := range found {
		switch {
		case !f.Stale:
			kept++
			continue
		case f.Err != nil:
			out += fmt.Sprintf("kept   %s: %v\n", f.Name, f.Err)
		case f.Dropped:
			out += fmt.Sprintf("dropped %s (%s)\n", f.Name, f.Why)
		default:
			out += fmt.Sprintf("stale  %s (%s)\n", f.Name, f.Why)
		}
		stale++
	}
	verb := "stale"
	if drop {
		verb = "stale, dropped"
	}
	out += fmt.Sprintf("%d test databases: %d %s, %d kept\n", len(found), stale, verb, kept)
	if !drop && stale > 0 {
		out += "Run again with --drop to drop them.\n"
	}
	return out
}

// Command droptestdb drops one throwaway test database after a script run
// (scripts/acceptance.sh). It goes through store.DropTestDatabase, the only drop
// in the codebase, which refuses anything but a local metarush-test-* database.
// It is a development helper (go run ./scripts/droptestdb), not part of the
// metarush binary.
//
//	go run ./scripts/droptestdb <mongodb-uri> <metarush-test-…>
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/store"
)

func main() {
	if len(os.Args) != 3 {
		fmt.Fprintln(os.Stderr, "usage: go run ./scripts/droptestdb <mongodb-uri> <metarush-test-name>")
		os.Exit(2)
	}
	uri, name := os.Args[1], os.Args[2]
	if !strings.HasPrefix(name, store.TestDBPrefix) {
		fmt.Fprintf(os.Stderr, "refusing to drop %q: only %s* databases are dropped\n", name, store.TestDBPrefix)
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	db, err := store.Connect(ctx, uri, name)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer db.Close(ctx)
	if err := db.DropTestDatabase(ctx, uri); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Printf("dropped %s (a local test database)\n", name)
}

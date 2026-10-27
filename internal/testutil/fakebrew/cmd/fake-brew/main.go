// Command fake-brew writes the stand-in Homebrew for browser runs:
//
//	go run ./internal/testutil/fakebrew/cmd/fake-brew <brew path> <bin folder> <log>
//
// then point tools.brew (METARUSH_BREW) at it and put the bin folder in
// METARUSH_TOOL_DIRS. See internal/testutil/fakebrew.
package main

import (
	"fmt"
	"os"

	"github.com/metavert-io/metarush/internal/testutil/fakebrew"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "usage: fake-brew <brew path> <bin folder> <log>")
		os.Exit(2)
	}
	if err := os.MkdirAll(os.Args[2], 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := os.WriteFile(os.Args[1], []byte(fakebrew.Script(os.Args[2], os.Args[3])), 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

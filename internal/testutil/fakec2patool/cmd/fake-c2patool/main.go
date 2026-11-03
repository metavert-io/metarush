// Command fake-c2patool is the stand-in c2patool for the acceptance run and
// the browser runs (build it, then point tools.c2patool or METARUSH_C2PATOOL
// at it, with METARUSH_FAKE_C2PATOOL_STATE naming its state file when it
// should fail). See internal/testutil/fakec2patool for what it answers; its
// signer is the metarush binary itself, as with the real c2patool.
package main

import (
	"os"

	"github.com/metavert-io/metarush/internal/testutil/fakec2patool"
)

func main() { os.Exit(fakec2patool.Main(os.Args[1:], os.Stdout, os.Stderr)) }

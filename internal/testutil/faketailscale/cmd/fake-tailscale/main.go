// Command fake-tailscale is the stand-in Tailscale command for the remote-access
// end-to-end run (build it, then point tools.tailscale or METARUSH_TAILSCALE at
// it). See internal/testutil/faketailscale for what it answers.
package main

import (
	"os"

	"github.com/metavert-io/metarush/internal/testutil/faketailscale"
)

func main() { os.Exit(faketailscale.Main(os.Args[1:], os.Stdout, os.Stderr)) }

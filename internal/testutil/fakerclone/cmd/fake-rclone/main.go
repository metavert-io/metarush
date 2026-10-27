// Command fake-rclone is the stand-in rclone for the acceptance run and the
// browser runs (build it, then point tools.rclone or METARUSH_RCLONE at it,
// with METARUSH_FAKE_RCLONE_STATE naming its state file). See
// internal/testutil/fakerclone for what it answers.
package main

import (
	"os"

	"github.com/metavert-io/metarush/internal/testutil/fakerclone"
)

func main() { os.Exit(fakerclone.Main(os.Args[1:], os.Stdout, os.Stderr)) }

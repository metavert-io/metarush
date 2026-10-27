// Command fake-yt-dlp is the stand-in downloader for the acceptance run and the
// dashboard's end-to-end checks (build it, then point tools.ytDlp or
// METARUSH_YTDLP at it). See internal/testutil/fakeytdlp for what links do.
package main

import (
	"os"

	"github.com/metavert-io/metarush/internal/testutil/fakeytdlp"
)

func main() { os.Exit(fakeytdlp.Main(os.Args[1:], os.Stdout, os.Stderr)) }

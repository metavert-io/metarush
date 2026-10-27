// Command fakeclaude is a stand-in `claude` for end-to-end runs of the
// dashboard (scripts/voiceover-live-e2e.sh): point tools.claude
// (METARUSH_CLAUDE) at it and put a conversation script (fakeclaude.Script as
// JSON) next to it as <binary>.json, or name one in METARUSH_FAKECLAUDE_SCRIPT.
// It answers the availability probes (--version, --help, auth status) and
// otherwise speaks Claude Code's stream-json protocol, playing the script.
// It never calls a model.
package main

import (
	"fmt"
	"os"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
)

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "--version":
			fmt.Println("9.9.9 (Claude Code)")
			return
		case "--help":
			fmt.Println(fakeclaude.DefaultHelp)
			return
		case "auth":
			// <binary>.auth.json says who is signed in, when a run wants it (D239).
			if exe, err := os.Executable(); err == nil {
				if b, err := os.ReadFile(exe + ".auth.json"); err == nil {
					fmt.Println(string(b))
					return
				}
			}
			fmt.Println(`{"loggedIn": true, "authMethod": "claude.ai"}`)
			return
		}
	}
	script := os.Getenv("METARUSH_FAKECLAUDE_SCRIPT")
	if script == "" {
		if exe, err := os.Executable(); err == nil {
			script = exe + ".json"
		}
	}
	os.Exit(fakeclaude.Run(script))
}

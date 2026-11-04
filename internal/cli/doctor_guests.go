package cli

import (
	"fmt"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/guests"
	"github.com/metavert-io/metarush/internal/models"
)

// guestsCheck says whether people outside the workspace can come in as
// guests, and where this computer answers them (D503): 127.0.0.1 only, its
// own pages, Host check, cookie and limits; nothing reachable from the
// internet in this release.
func guestsCheck(cfg *config.Config, ws models.Workspace, add addCheck) {
	l := cfg.GuestListener()
	on := ws.Guests != nil && ws.Guests.On
	port, running := guests.ListenerRunning(cfg)
	switch {
	case !on && !l.Listen:
		add("security", "guests", "ok", "guests off; no guest listener here", "")
	case on && !l.Listen:
		add("security", "guests", "info", "guests on in the workspace; this computer doesn't answer them", "",
			"Guests are on in this workspace, but this computer doesn't answer them")
	case !on && l.Listen:
		add("security", "guests", "warn", "this computer is set to answer guests, but guests are off in the workspace", "`metarush guests on`, or `metarush guests off` here",
			"This computer is set to answer guests, but guests are off in this workspace", "Turn guests on or off in Settings → People → Guests.")
	case running && port == l.PortOrDefault():
		add("security", "guests", "ok", fmt.Sprintf("guest listener on %s:%d only (its own pages, Host check, cookie and limits; nothing reachable from the internet)", config.GuestHost, port), "",
			fmt.Sprintf("Guests are let in at this computer's own address (port %d): nothing is reachable from the internet", port))
	default:
		add("security", "guests", "info", fmt.Sprintf("guests on; the guest listener (%s:%d) answers once MetaRush runs in the background", config.GuestHost, l.PortOrDefault()), "start MetaRush (`metarush dashboard`)",
			"Guests are on: this computer answers them once MetaRush runs in the background")
	}
}

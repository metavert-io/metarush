package ops

import "github.com/metavert-io/metarush/internal/config"

// SetSwitchFails makes a move's own switch fail after the old database was
// marked moved (a switch that doesn't hold); the returned func undoes it.
func SetSwitchFails(err error) func() {
	old := switchFails
	switchFails = func() error { return err }
	return func() { switchFails = old }
}

// KeepLookAddressForTest replaces the old database's address kept for a
// move's looks (a test points it at a server that doesn't answer).
func KeepLookAddressForTest(cfg *config.Config, move, uri string) error {
	return keepLookAddress(cfg, move, uri)
}

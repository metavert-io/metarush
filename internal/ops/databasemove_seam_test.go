package ops

// SetSwitchFails makes a move's own switch fail after the old database was
// marked moved (a switch that doesn't hold); the returned func undoes it.
func SetSwitchFails(err error) func() {
	old := switchFails
	switchFails = func() error { return err }
	return func() { switchFails = old }
}

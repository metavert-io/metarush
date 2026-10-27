package localdb

// Error is a problem with the database on this computer said in plain words
// (Error), with what MongoDB or the system said kept apart for Details (Cause):
// people read the first, `--details` and `--json` carry the second.
type Error struct {
	Plain string
	// Next is what a person can do about it, when MetaRush knows (a hint).
	Next  string
	Cause error
}

func (e *Error) Error() string { return e.Plain }

// Unwrap is what MongoDB or the system said.
func (e *Error) Unwrap() error { return e.Cause }

// plainErr says a problem in plain words and keeps its cause for Details.
func plainErr(plain string, cause error) error { return &Error{Plain: plain, Cause: cause} }

package visual

import (
	"os"
	"testing"
)

// TestMain removes the folder the on-screen helper was built into for this
// run (helper), once every test is done with it.
func TestMain(m *testing.M) {
	code := m.Run()
	if helperDir != "" {
		_ = os.RemoveAll(helperDir)
	}
	os.Exit(code)
}

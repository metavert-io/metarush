package localmodels

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/providers"
)

// A browser run (METARUSH_NO_PAID_SERVICES=1) never downloads a model from
// the internet either, as under go test.
func TestBrowserRunsNeverDownload(t *testing.T) {
	was := underTest
	underTest = func() bool { return false } // as a browser run's server is
	t.Cleanup(func() { underTest = was })
	p := Pin{ID: "x", URL: "https://example.com/x.tar.bz2"}
	t.Setenv(providers.EnvNoPaidServices, "1")
	if _, err := (Store{}).source(p); err == nil || !strings.Contains(err.Error(), "never download") {
		t.Fatalf("a browser run: %v", err)
	}
	t.Setenv(providers.EnvNoPaidServices, "")
	if src, err := (Store{}).source(p); err != nil || src != p.URL {
		t.Fatalf("outside tests and browser runs: %q %v", src, err) // only worked out, never fetched
	}
}

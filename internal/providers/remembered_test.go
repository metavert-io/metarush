package providers

import (
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
)

// A key is taken for a secret — hidden in everything this process logs, left
// out of exports — once it is kept, or once the service takes it in a test.
// One the service refuses, or that StoreKey doesn't keep, was only sent: the
// call that sent it hides it in its own entries, and nothing more.
func TestOnlyKeysTakenAreRemembered(t *testing.T) {
	f := setup(t, "keychain")
	accepted := fakeKey("9c41")
	service := fakeeleven.New(accepted)
	t.Cleanup(service.Close)
	t.Setenv(EnvElevenLabsBaseURL, service.URL)
	refused, overridden, kept := fakeKey("r3fu"), fakeKey("0v3r"), fakeKey("k3pt")

	if _, err := call[Status](t, f.a, "provider.set", SetIn{Name: ElevenLabsName, Key: refused}, false); !apperr.Is(err, apperr.Provider) {
		t.Fatalf("a key the service refuses: %v", err)
	}
	if out, err := call[TestOut](t, f.a, "provider.test", TestIn{Name: ElevenLabsName, Key: refused}, false); err != nil || out.Result.OK {
		t.Fatalf("testing a key the service refuses: %+v %v", out, err)
	}
	if logbook.Remembered(refused) {
		t.Fatal("a key the service refused was taken for a secret")
	}
	// A key this machine's environment would override isn't kept.
	t.Setenv(EnvVar(ElevenLabsName), fakeKey("3nv0"))
	if _, err := StoreKey(f.a.Cfg, ElevenLabsName, overridden, false); !apperr.Is(err, apperr.Conflict) {
		t.Fatalf("a key the environment overrides: %v", err)
	}
	if logbook.Remembered(overridden) {
		t.Fatal("a key that wasn't kept was taken for a secret")
	}
	t.Setenv(EnvVar(ElevenLabsName), "")

	if out, err := call[TestOut](t, f.a, "provider.test", TestIn{Name: ElevenLabsName, Key: accepted}, false); err != nil || !out.Result.OK {
		t.Fatalf("testing a key the service takes: %+v %v", out, err)
	}
	if _, err := call[Status](t, f.a, "provider.set", SetIn{Name: ElevenLabsName, Key: kept, SkipTest: true}, false); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{accepted, kept} {
		if !logbook.Remembered(k) {
			t.Fatalf("a key taken isn't hidden: %s…", k[len(k)-4:])
		}
	}
}

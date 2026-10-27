package ops

import (
	"context"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/app"
)

// An offering's ID is a plain, unique name: a bad or repeated one stops
// MetaRush from starting, so the catalogue never holds two of one.
func TestOfferRefusesBadAndRepeatedIDs(t *testing.T) {
	panics := func(f func()) (msg string) {
		defer func() {
			if r := recover(); r != nil {
				msg, _ = r.(string)
			}
		}()
		f()
		return ""
	}
	if m := panics(func() { Offer("test.offer", Offering{ID: "Not An ID"}) }); !strings.Contains(m, "is no ID") {
		t.Fatalf("a bad ID: %q", m)
	}
	if m := panics(func() { Offer("test.offer", Offering{ID: "test-offer-once"}) }); m != "" {
		t.Fatalf("a good ID: %q", m)
	}
	if m := panics(func() { Offer("test.other", Offering{ID: "test-offer-once"}) }); !strings.Contains(m, "already test.offer's") {
		t.Fatalf("a repeated ID: %q", m)
	}
	found := false
	for _, o := range Offerings() {
		if o.ID == "test-offer-once" && o.Op == "test.offer" {
			found = true
		}
	}
	if !found {
		t.Fatal("the offering isn't listed with its operation")
	}
	ok := func(context.Context, *app.App) (bool, string) { return true, "" }
	RegisterReady("test-ready-once", ok)
	if fn, has := ReadyCheckFor("test-ready-once"); !has || fn == nil {
		t.Fatal("the check isn't registered")
	}
	if m := panics(func() { RegisterReady("test-ready-once", ok) }); !strings.Contains(m, "two checks") {
		t.Fatalf("a repeated check: %q", m)
	}
}

package catalog_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

const firstPartyFile = "firstparty.json"

// The first-party catalogue (D269) is generated from the operations'
// annotations (ops.Offer): METARUSH_UPDATE_CATALOGUE=1 writes
// firstparty.json; otherwise the test fails while it is out of date. An ID
// the file had never disappears: it is offered still, or retired.
func TestFirstPartyCatalogue(t *testing.T) {
	got, err := catalog.FirstPartyFromOfferings(ops.Offerings(), catalog.RetiredFirstParty, catalog.FirstPartyFileNow())
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(firstPartyFile)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("METARUSH_UPDATE_CATALOGUE") == "1" {
		if !bytes.Equal(got, want) {
			if err := os.WriteFile(firstPartyFile, got, 0o644); err != nil {
				t.Fatal(err)
			}
			t.Logf("wrote %s", firstPartyFile)
		}
		return
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("internal/catalog/firstparty.json is out of date (an ops.Offer annotation changed): run\n  METARUSH_UPDATE_CATALOGUE=1 go test ./internal/catalog -run TestFirstPartyCatalogue\nand commit the file")
	}
}

// About fifty to eighty (the AI kinds, presenters, connections, sharing for
// review and changed words grew it), in every group, each operation once as
// it is, words people read in plain words.
func TestFirstPartyCatalogueIsAMenu(t *testing.T) {
	fps := catalog.FirstParty()
	if n := len(fps); n < 45 || n > 80 {
		t.Errorf("%d first-party abilities; the starter catalogue is about fifty", n)
	}
	groups := map[string]int{}
	asItIs := map[string]string{}
	noKeys := 0
	for _, fp := range fps {
		groups[fp.Group]++
		if fp.Name != catalog.FirstPartyPrefix+fp.ID || fp.MadeWith != fp.Name {
			t.Errorf("%s: name %q, madeWith %q", fp.ID, fp.Name, fp.MadeWith)
		}
		if len(fp.Input) == 0 {
			if other, dup := asItIs[fp.Op]; dup {
				t.Errorf("%s and %s are both %s as it is: find would show the operation once", fp.ID, other, fp.Op)
			}
			asItIs[fp.Op] = fp.ID
		}
		if len(fp.Needs.Keys) == 0 {
			noKeys++
		}
		// What leaves this computer is said for everything that uses a key or
		// Claude Code; "costs nothing" only with no price and no key.
		if (len(fp.Needs.Keys) > 0 || fp.Needs.Claude) && !strings.Contains(fp.Needs.Leaves, "this computer") {
			t.Errorf("%s uses a key or Claude Code and doesn't say what leaves: %q", fp.ID, fp.Needs.Leaves)
		}
		if fp.Cost == catalog.CostFree && len(fp.Needs.Keys) > 0 {
			t.Errorf("%s uses a key and says it costs nothing", fp.ID)
		}
		if contains(fp.Needs.Works, "transcripts") && !contains(fp.Needs.Works, "editing") {
			t.Errorf("%s needs transcripts but not FFmpeg", fp.ID)
		}
		for _, s := range append([]string{fp.Title, fp.Does}, fp.Words...) {
			if f := plainwords.Check(s); len(f) > 0 {
				t.Errorf("%s: %q isn't plain: %v", fp.ID, s, f)
			}
		}
		if strings.HasSuffix(fp.Title, ".") || !strings.HasSuffix(fp.Does, ".") {
			t.Errorf("%s: a title without a full stop, a sentence with one, please (%q, %q)", fp.ID, fp.Title, fp.Does)
		}
	}
	for _, g := range catalog.Groups {
		if g.Key != catalog.GroupAdded && groups[g.Key] == 0 {
			t.Errorf("no first-party ability in %q", g.Title)
		}
	}
	if noKeys*4 < len(fps)*3 {
		t.Errorf("only %d of %d need no key; most of MetaRush works with none", noKeys, len(fps))
	}
}

// A first-party ability runs as the operation it is: its fixed fields set,
// another value for one refused, and describe leaves them out of the input.
func TestFirstPartyRunsAsItsOperation(t *testing.T) {
	ctx := context.Background()
	d, err := catalog.Describe(ctx, nil, "metarush/steady-footage")
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != catalog.KindFirstParty || d.RunWith != "asset.fixes.set" || !d.RunOwnInput || d.Group != catalog.GroupFix || d.Command == "" {
		t.Fatalf("describe: %+v", d)
	}
	if props := d.Input["properties"].(map[string]any); props["tick"] != nil || props["id"] == nil {
		t.Fatalf("the fixed field is left out of the input, the rest kept: %v", props)
	}
	op, in, err := catalog.RunTarget(d.Name, d, map[string]any{"id": "ast_x"})
	if err != nil || op.Name != "asset.fixes.set" || in["id"] != "ast_x" || in[catalog.RunInputKey] != nil {
		t.Fatalf("run target: %v %v %v", op, in, err)
	}
	if tick, _ := in["tick"].([]any); len(tick) != 1 || tick[0] != "steady" {
		if ts, ok := in["tick"].([]string); !ok || len(ts) != 1 || ts[0] != "steady" {
			t.Fatalf("tick: %#v", in["tick"])
		}
	}
	if _, _, err := catalog.RunTarget(d.Name, d, map[string]any{"id": "ast_x", "tick": []string{"grain"}}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("another value for a fixed field: %v", err)
	}
	// The same value is fine.
	if _, _, err := catalog.RunTarget(d.Name, d, map[string]any{"id": "ast_x", "tick": []any{"steady"}}); err != nil {
		t.Fatalf("the fixed value itself: %v", err)
	}
	// The operation reads its input without minding case — "TICK", and "ticK"
	// with the Kelvin sign, are tick to Go's JSON — so a fixed field in another
	// case is refused, not run beside it (a grain fix through steady-footage).
	var probe fixes.SetIn
	if err := json.Unmarshal([]byte(`{"tic\u212a":["grain"]}`), &probe); err != nil || len(probe.Tick) != 1 {
		t.Fatalf("the decoder this guards against: %+v %v", probe, err)
	}
	for _, k := range []string{"TICK", "Tick", "tic\u212a", "tick"} {
		_, in, err := catalog.RunTarget(d.Name, d, map[string]any{"id": "ast_x", k: []string{"grain"}})
		if k == "tick" {
			if err == nil {
				t.Fatalf("another value for the fixed field itself ran: %v", in)
			}
			continue
		}
		if apperr.From(err).Code != apperr.InvalidInput || !strings.Contains(apperr.From(err).Message, "leave "+k+" out") {
			t.Fatalf("%q beside the fixed tick: %v %v", k, in, err)
		}
	}
	// An operation that is a first-party ability as it is says so.
	m, err := catalog.Describe(ctx, nil, "footage.moments")
	if err != nil || m.Kind != catalog.KindOperation || m.FirstParty != "metarush/best-moments" || m.Group != catalog.GroupUnderstand {
		t.Fatalf("footage.moments: %+v %v", m, err)
	}
	if _, err := catalog.Describe(ctx, nil, "metarush/no-such-thing"); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("unknown: %v", err)
	}
}

// find shows an operation once: a first-party ability that is the operation
// as it is lends it its title, group and words; one that fixes fields is an
// entry of its own.
func TestFindShowsEachOperationOnce(t *testing.T) {
	ctx := context.Background()
	hits, err := catalog.Find(ctx, nil, catalog.Query{Text: "best moments highlights", Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, h := range hits {
		if h.Name == "metarush/best-moments" {
			t.Fatalf("the first-party ability shows beside its operation: %v", hits)
		}
		if seen[h.Name] {
			t.Fatalf("%s twice", h.Name)
		}
		seen[h.Name] = true
	}
	if !seen["footage.moments"] {
		t.Fatalf("footage.moments not found: %v", hits)
	}
	steady, _ := catalog.Find(ctx, nil, catalog.Query{Text: "steady my shaky footage"})
	if len(steady) == 0 || steady[0].Name != "metarush/steady-footage" || steady[0].Group != catalog.GroupFix {
		t.Fatalf("steady: %v", steady)
	}
	// Only the first-party catalogue: each ability is itself.
	only, _ := catalog.Find(ctx, nil, catalog.Query{Text: "best moments", Kinds: []string{catalog.KindFirstParty}})
	if len(only) == 0 || only[0].Name != "metarush/best-moments" {
		t.Fatalf("first-party only: %v", only)
	}
}

// docs/catalogue.md lists every first-party ability by ID, and none that
// isn't one.
func TestCatalogueDocNamesEveryAbility(t *testing.T) {
	b, err := os.ReadFile("../../docs/catalogue.md")
	if err != nil {
		t.Fatal(err)
	}
	doc := string(b)
	ids := map[string]bool{}
	for _, fp := range catalog.FirstParty() {
		ids[fp.ID] = true
		if !strings.Contains(doc, "| `"+fp.ID+"` |") {
			t.Errorf("docs/catalogue.md doesn't list %s", fp.ID)
		}
	}
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "| `") {
			id := strings.SplitN(line, "`", 3)[1]
			if !ids[id] {
				t.Errorf("docs/catalogue.md lists %s, which isn't a first-party ability", id)
			}
		}
	}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// The generator holds annotations to what every surface relies on: a key
// makes it "the service may charge", never free; a key or Claude Code needs
// what leaves; transcripts come with FFmpeg; partial support names systems.
func TestFirstPartyGeneratorRules(t *testing.T) {
	gen := func(n ops.Needs) (*catalog.FirstPartyFile, error) {
		b, err := catalog.FirstPartyFromOfferings([]ops.OpOffering{{Op: "footage.moments", Offering: ops.Offering{ID: "test-thing", Group: catalog.GroupMake,
			Title: "A test thing", Does: "Does a test thing.", Needs: n}}}, nil, nil)
		if err != nil {
			return nil, err
		}
		var f catalog.FirstPartyFile
		return &f, json.Unmarshal(b, &f)
	}
	f, err := gen(ops.Needs{Keys: []string{"elevenlabs"}, Leaves: "words leave this computer"})
	if err != nil || f.Abilities[0].Cost != catalog.CostKeyed {
		t.Fatalf("a key and no price: %+v %v", f, err)
	}
	if f, err := gen(ops.Needs{}); err != nil || f.Abilities[0].Cost != catalog.CostFree {
		t.Fatalf("nothing at all: %+v %v", f, err)
	}
	for _, c := range []struct {
		n    ops.Needs
		says string
	}{
		{ops.Needs{Keys: []string{"elevenlabs"}}, "say what leaves"},
		{ops.Needs{Claude: true}, "say what leaves"},
		{ops.Needs{Works: []string{"transcripts"}}, "FFmpeg too"},
		{ops.Needs{Partly: "only some of it"}, "name its systems"},
		{ops.Needs{Works: []string{"nothing-like-it"}}, "says nothing called"},
	} {
		if _, err := gen(c.n); err == nil || !strings.Contains(err.Error(), c.says) {
			t.Errorf("%+v: %v", c.n, err)
		}
	}
}

// Without FFmpeg, nothing that transcribes, measures or looks at footage
// says it works here (D270): the first run's transcripts item alone isn't
// enough, and the on-screen index needs what vision.CanRun needs.
func TestWorksHereWithoutFFmpeg(t *testing.T) {
	a := testapp.New(t)
	missing := "/nonexistent/metarush-test/ffmpeg"
	a.Tools = tools.New(config.Tools{FFmpeg: missing, FFprobe: missing})
	es, err := catalog.All(context.Background(), a, catalog.KindFirstParty)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]catalog.Entry{}
	for _, e := range es {
		byID[strings.TrimPrefix(e.Name, catalog.FirstPartyPrefix)] = e
	}
	for _, id := range []string{"transcripts", "best-moments", "who-speaks", "tighten-pauses", "remove-ums", "spoken-language", "faces", "whats-on-screen", "steady-footage"} {
		e, ok := byID[id]
		if !ok {
			t.Fatalf("no %s", id)
		}
		if e.WorksHere == nil || *e.WorksHere {
			t.Errorf("%s says it works here without FFmpeg", id)
			continue
		}
		want := "FFmpeg"
		if (id == "faces" || id == "whats-on-screen") && thisOS() != "darwin" {
			want = "a Mac"
		}
		if !strings.Contains(e.Missing, want) {
			t.Errorf("%s: %q doesn't say it needs %s", id, e.Missing, want)
		}
	}
	// What needs nothing of FFmpeg still works.
	if e := byID["find-and-replace"]; e.WorksHere == nil || !*e.WorksHere {
		t.Errorf("find-and-replace: %+v", e)
	}
}

func thisOS() string { return runtime.GOOS }

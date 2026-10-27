package abilities

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// The eval's sample abilities are manifests like any other.
func TestSamplesAreSoundManifests(t *testing.T) {
	vs := SampleVersions()
	if len(vs) < 6 {
		t.Fatalf("%d samples", len(vs))
	}
	safe, people := 0, 0
	for _, v := range vs {
		m, err := ParseManifest([]byte(v.Manifest))
		if err != nil {
			t.Fatalf("%s: %v", v.Name, err)
		}
		if ps := m.Validate(map[string]bool{"run.py": false}); len(ps) > 0 {
			t.Errorf("%s: %v", v.Name, ps)
		}
		e := entryOf(&v, &v)
		if e.Kind != KindAbility || !e.Changes || e.RunWith != "ability.run" || e.Group != catalog.GroupAdded || e.Blocked {
			t.Errorf("%s: %+v", v.Name, e)
		}
		if e.PeopleOnly {
			people++
		} else {
			safe++
		}
	}
	if safe == 0 || people == 0 {
		t.Errorf("the samples should hold both kinds of run: %d anyone may start, %d a person's", safe, people)
	}
}

// An ability joins find (D268): found with what it needs, refused by run
// until an owner approved it and a person set it up here, then run only
// through ability.run, with a plan first — never beyond what its manifest
// allows.
func TestAbilitiesJoinFind(t *testing.T) {
	a := newApp(t)
	needSandbox(t, a)
	ctx := context.Background()
	human := ops.Caller{Human: true}
	out := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: writeFolder(t, map[string]string{ManifestFile: cardManifest, "make.py": cardScript})}, ops.Caller{})
	if !out.OK {
		t.Fatalf("check: %+v", out.Problems)
	}
	hit := func(task string) *catalog.Hit {
		t.Helper()
		hs, err := catalog.Find(ctx, a, catalog.Query{Text: task, Limit: 20})
		if err != nil {
			t.Fatal(err)
		}
		for i := range hs {
			if hs[i].Name == "title-card" {
				return &hs[i]
			}
		}
		return nil
	}
	describe := func() *catalog.Description {
		t.Helper()
		d, err := catalog.Describe(ctx, a, "title-card")
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	// Waiting for an owner: found, says so, can't run.
	h := hit("a title card picture")
	if h == nil || h.Kind != KindAbility || !h.Blocked || h.WorksHere == nil || *h.WorksHere || h.Missing != "needs an owner's approval" ||
		!strings.Contains(catalog.Line(h.Entry), "needs an owner's approval") || !h.Changes || h.PeopleOnly {
		t.Fatalf("waiting: %+v", h)
	}
	if _, _, err := catalog.RunTarget("title-card", describe(), nil); apperr.From(err).Code != apperr.NotConfigured {
		t.Fatalf("run before approval: %v", err)
	}
	// A run is a change: read-only find leaves it out.
	if ro, _ := catalog.Find(ctx, a, catalog.Query{Text: "a title card picture", ReadOnly: true, Limit: 20}); func() bool {
		for _, x := range ro {
			if x.Name == "title-card" {
				return true
			}
		}
		return false
	}() {
		t.Fatal("read-only find returned an ability")
	}

	// Approved, not set up here.
	mustCall[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: "title-card@" + out.Version}, human)
	if h := hit("a title card picture"); h == nil || !h.Blocked || h.Missing != "needs setting up on this computer" {
		t.Fatalf("not set up: %+v", h)
	}
	// Set up: it works here, and run reaches it through ability.run.
	mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "title-card"}, human)
	h = hit("a title card picture")
	if h == nil || h.Blocked || h.WorksHere == nil || !*h.WorksHere || h.Missing != "" || h.Cost != catalog.CostFree || h.Leaves != "nothing leaves this computer" {
		t.Fatalf("ready: %+v", h)
	}
	d := describe()
	props := d.Input["properties"].(map[string]any)
	if d.RunWith != "ability.run" || d.RunOwnInput || props["plan"] == nil || props["inputs"].(map[string]any)["properties"].(map[string]any)["words"] == nil ||
		!strings.Contains(d.Note, "ability.plan") || d.MadeWith != "title-card@"+Short(out.Version) {
		t.Fatalf("describe: %+v", d)
	}
	p := mustCall[AbilityPlanOut](t, a, "ability.plan", AbilityPlanIn{Ref: "title-card", Inputs: map[string]any{"words": "Hello"}}, ops.Caller{})
	op, in, err := catalog.RunTarget("title-card", d, map[string]any{"inputs": map[string]any{"words": "Hello"}, "plan": p.Fingerprint})
	if err != nil || op.Name != "ability.run" {
		t.Fatalf("run target: %v %v", op, err)
	}
	raw, _ := json.Marshal(in)
	res, err := ops.Call(ctx, a, op.Name, raw, ops.Caller{})
	if err != nil {
		t.Fatalf("a free run that sends nothing starts from anyone: %v (%s)", err, raw)
	}
	if r := res.(*AbilityRunOut); r.Status != "queued" {
		t.Fatalf("run: %+v", r)
	}
	// Both forms at once are refused; without a plan, it says to plan first.
	both, _ := json.Marshal(map[string]any{"ref": "title-card", "plan": p.Fingerprint, "input": map[string]any{"plan": p.Fingerprint}})
	if _, err := ops.Call(ctx, a, "ability.run", both, ops.Caller{}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("both forms: %v", err)
	}
	if _, err := ops.Call(ctx, a, "ability.run", json.RawMessage(`{"ref":"title-card","input":{"inputs":{"words":"Hi"}}}`), ops.Caller{}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("no plan: %v", err)
	}

	// One that uses a key is a person's to do: find says so and run refuses
	// it, since ability.run would let anyone start it.
	keyed := strings.Replace(strings.Replace(cardManifest, "name: title-card", "name: key-card", 1), "cost: free",
		"cost: free\nkeys: [{name: cards, hosts: [api.example.com]}]\nhosts: [api.example.com]\nsends: [words]", 1)
	k := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: writeFolder(t, map[string]string{ManifestFile: keyed, "make.py": cardScript})}, ops.Caller{})
	mustCall[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: "key-card@" + k.Version}, human)
	kd, err := catalog.Describe(ctx, a, "key-card")
	if err != nil {
		t.Fatal(err)
	}
	if !kd.PeopleOnly || !kd.Changes || kd.Leaves != "words leave this computer" || !strings.Contains(kd.Missing, "needs setting up on this computer") {
		t.Fatalf("keyed: %+v", kd)
	}
	// "Free" in its manifest, but it uses a key: the service may charge, and
	// nothing says it costs nothing — find, describe, the menu, the list.
	if kd.Cost != catalog.CostKeyed {
		t.Fatalf("a keyed ability's cost: %q", kd.Cost)
	}
	if h := hitNamed(t, a, "a key card picture", "key-card"); h == nil || h.Cost != catalog.CostKeyed || !strings.Contains(catalog.Line(h.Entry), "the service may charge") {
		t.Fatalf("find: %+v", h)
	}
	menu, err := catalog.Menu(ctx, a, &catalog.CatalogueListIn{Kind: "added"})
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range menu.Items {
		if it.Name == "key-card" && (it.Cost != catalog.CostKeyed || !strings.Contains(it.Line, "the service may charge")) {
			t.Fatalf("menu: %+v", it)
		}
	}
	for _, r := range mustCall[AbilityListOut](t, a, "ability.list", AbilityListIn{}, ops.Caller{}).Abilities {
		if r.Name == "key-card" && (r.Cost != "keyed" || r.CostWords == "Free" || strings.Contains(r.Line, "; free;")) {
			t.Fatalf("the list says a keyed ability is free: %+v", r)
		}
		if r.Name == "title-card" && (r.Cost != "free" || r.CostWords != "Free") {
			t.Fatalf("a free one stays free: %+v", r)
		}
	}
	mustCall[AbilitySetupOut](t, a, "ability.setup", AbilitySetupIn{Ref: "key-card"}, human)
	kd, _ = catalog.Describe(ctx, a, "key-card")
	if kd.Missing != "needs the cards key on this computer" || !kd.Blocked {
		t.Fatalf("keyed, set up, no key: %+v", kd)
	}
	kd.Blocked = false // as if the key were there
	// A person's run, refused as one, with the command to hand to a person.
	_, _, err = catalog.RunTarget("key-card", kd, nil)
	if ae := apperr.From(err); ae.Code != apperr.HumanOnly || ae.Details.(map[string]any)["command"] != "metarush ability run key-card" || !strings.Contains(ae.Message, "Only a person starts key-card") {
		t.Fatalf("a person's run through find: %v", err)
	}
	// The ability's own name never reaches an operation by mistake.
	if _, err := catalog.Describe(ctx, a, "no-such-ability"); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("unknown: %v", err)
	}
}

func hitNamed(t *testing.T, a *app.App, task, name string) *catalog.Hit {
	t.Helper()
	hs, err := catalog.Find(context.Background(), a, catalog.Query{Text: task, Limit: 20})
	if err != nil {
		t.Fatal(err)
	}
	for i := range hs {
		if hs[i].Name == name {
			return &hs[i]
		}
	}
	return nil
}

// A name means one thing (D268): an added ability can't take a first-party
// ability's ID — refused when it is checked and when it would be approved —
// and the menu shows an added ability by its name before a built-in by its
// bare ID.
func TestFirstPartyNamesAreNotForAddedAbilities(t *testing.T) {
	a := newApp(t)
	ctx := context.Background()
	clash := strings.Replace(cardManifest, "name: title-card", "name: steady-footage", 1)
	out := mustCall[AbilityCheckOut](t, a, "ability.check", AbilityCheckIn{Path: writeFolder(t, map[string]string{ManifestFile: clash, "make.py": cardScript})}, ops.Caller{})
	if out.OK || out.Recorded || !strings.Contains(strings.Join(problemTexts(out.Problems), " "), "steady-footage is the name of one of MetaRush's own abilities") {
		t.Fatalf("check: %+v", out)
	}
	// One recorded before the rule (or by an older build) can't be approved.
	m, err := ParseManifest([]byte(strings.Replace(cardManifest, "name: title-card", "name: faces", 1)))
	if err != nil {
		t.Fatal(err)
	}
	hash := strings.Repeat("ab", 32)
	v := models.AbilityVersion{ID: docID(a.WorkspaceID(), hash), WorkspaceID: a.WorkspaceID(), Name: "faces", Hash: hash, Number: 1, Title: "Faces of my own",
		Does: m.Does, Search: m.Search, Facts: factsOf(m), Manifest: cardManifest, State: models.AbilityWaiting, Check: models.AbilityCheck{OK: true, At: now()},
		Decisions: []models.AbilityDecision{}, CreatedAt: now(), UpdatedAt: now()}
	if err := a.DB.Insert(ctx, store.CAbilities, &v); err != nil {
		t.Fatal(err)
	}
	if _, err := call[AbilityDecisionOut](t, a, "ability.approve", AbilityApproveIn{Ref: "faces@" + hash}, ops.Caller{Human: true}); apperr.From(err).Code != apperr.Validation ||
		!strings.Contains(apperr.From(err).Message, "name of one of MetaRush's own abilities") {
		t.Fatalf("approve: %v", err)
	}
	// The menu: the added one by its name, the built-in by its full name;
	// a bare ID no added ability has is the built-in.
	if s, err := catalog.ShowMenuItem(ctx, a, "faces"); err != nil || s.Item.Kind != KindAbility || s.Item.Title != "Faces of my own" {
		t.Fatalf("show faces: %+v %v", s, err)
	}
	if s, err := catalog.ShowMenuItem(ctx, a, "metarush/faces"); err != nil || s.Item.Kind != catalog.KindFirstParty {
		t.Fatalf("show metarush/faces: %+v %v", s, err)
	}
	if s, err := catalog.ShowMenuItem(ctx, a, "best-moments"); err != nil || s.Item.Kind != catalog.KindFirstParty || s.Item.ID != "best-moments" {
		t.Fatalf("show best-moments: %+v %v", s, err)
	}
	if !catalog.IsFirstPartyID("faces") || catalog.IsFirstPartyID("title-card") {
		t.Fatal("IsFirstPartyID")
	}
}

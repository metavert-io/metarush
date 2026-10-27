package catalog_test

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/ops"
)

func TestTermsSplitStemAndDropWhatSaysNothing(t *testing.T) {
	for _, c := range []struct {
		in   string
		want []string
	}{
		{"removeWords", []string{"remov", "word"}},
		{"footage.speakers.rename", []string{"footag", "speaker", "renam"}},
		{"What's in the Trash at 0:30?", []string{"trash"}},
		{"Rendering the rendered renders", []string{"render", "render", "render"}},
		{"colours, subtitles", []string{"color", "subtitl"}},
	} {
		if got := catalog.Terms(c.in, true); !reflect.DeepEqual(got, c.want) {
			t.Errorf("Terms(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// A phrase's synonyms count only when its words stand together in the
// request: "lower third" finds the graphics templates, "the third clip" does
// not.
func TestPhrasesCountOnlyTogether(t *testing.T) {
	es := []catalog.Entry{
		{Name: "edit.templates", Kind: "operation", About: "List the vetted motion-graphics templates"},
		{Name: "cut.delete", Kind: "operation", About: "Remove clips from the latest cut", Changes: true},
	}
	hits := catalog.Rank(es, "add a lower third", catalog.DefaultRanking, 0)
	if len(hits) == 0 || hits[0].Name != "edit.templates" {
		t.Fatalf("lower third: %v", hits)
	}
	for _, h := range catalog.Rank(es, "the third lower clip", catalog.DefaultRanking, 0) {
		if h.Name == "edit.templates" {
			t.Fatalf("the phrase's words apart still found the templates: %v", h)
		}
	}
}

// The same words always give the same answer, in the same order.
func TestFindIsDeterministic(t *testing.T) {
	ctx := context.Background()
	for _, task := range []string{"make a review copy", "who is speaking", "turn the music down", "rename"} {
		first, err := catalog.Find(ctx, nil, catalog.Query{Text: task})
		if err != nil {
			t.Fatal(err)
		}
		if len(first) == 0 || len(first) > catalog.DefaultLimit {
			t.Fatalf("%q: %d hits", task, len(first))
		}
		for range 5 {
			again, _ := catalog.Find(ctx, nil, catalog.Query{Text: task})
			if !reflect.DeepEqual(first, again) {
				t.Fatalf("%q gave another answer the second time", task)
			}
		}
	}
	if _, err := catalog.Find(ctx, nil, catalog.Query{Text: "  "}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("empty task: %v", err)
	}
	ro, _ := catalog.Find(ctx, nil, catalog.Query{Text: "delete a clip", ReadOnly: true, Limit: 50})
	if len(ro) > catalog.MaxLimit {
		t.Fatalf("limit not capped: %d", len(ro))
	}
	for _, h := range ro {
		if h.Changes {
			t.Fatalf("read-only find returned %s", h.Name)
		}
	}
}

// fakeSource is another catalogue (as abilities will be): its entries join
// find and describe, and run through an operation of their own.
type fakeSource struct{}

func (fakeSource) Kind() string { return "test-ability" }
func (fakeSource) Entries(context.Context, *app.App) ([]catalog.Entry, error) {
	return []catalog.Entry{{Name: "stock-music-finder", Kind: "test-ability", About: "Find licensed stock music by mood and tempo",
		Words: []string{"royalty free songs"}, Changes: true, RunWith: "ops.find", RunInput: map[string]any{"name": "stock-music-finder"}}}, nil
}
func (fakeSource) Describe(_ context.Context, _ *app.App, name string) (*catalog.Description, error) {
	if name != "stock-music-finder" {
		return nil, apperr.New(apperr.NotFound, "no such ability")
	}
	return &catalog.Description{Name: name, Kind: "test-ability", About: "Find licensed stock music by mood and tempo", Changes: true,
		Input: map[string]any{"type": "object"}, RunWith: "ops.find", RunInput: map[string]any{"name": name}}, nil
}

func TestAnotherCatalogueJoinsFind(t *testing.T) {
	ctx := context.Background()
	defer catalog.Register(fakeSource{})()
	hits, err := catalog.Find(ctx, nil, catalog.Query{Text: "find royalty free songs for the video"})
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) == 0 || hits[0].Name != "stock-music-finder" || hits[0].Kind != "test-ability" {
		t.Fatalf("the other catalogue's entry is not first: %v", hits)
	}
	if !strings.Contains(catalog.Line(hits[0].Entry), "changes something") {
		t.Fatalf("line: %s", catalog.Line(hits[0].Entry))
	}
	// Kinds limit the search; read-only leaves it out (it changes something).
	only, _ := catalog.Find(ctx, nil, catalog.Query{Text: "royalty free songs", Kinds: []string{catalog.KindOperation}})
	ro, _ := catalog.Find(ctx, nil, catalog.Query{Text: "royalty free songs", ReadOnly: true})
	for _, h := range append(only, ro...) {
		if h.Kind == "test-ability" {
			t.Fatalf("found despite kinds or read-only: %v", h)
		}
	}
	d, err := catalog.Describe(ctx, nil, "stock-music-finder")
	if err != nil || d.RunWith != "ops.find" {
		t.Fatalf("describe: %v %v", d, err)
	}
	in := catalog.RunInputFor(d.RunInput, map[string]any{"mood": "calm"})
	if in["name"] != "stock-music-finder" || in[catalog.RunInputKey].(map[string]any)["mood"] != "calm" {
		t.Fatalf("run input: %v", in)
	}
	// Operations still describe themselves, by name or MCP tool name.
	for _, n := range []string{"footage.moments", "footage_moments"} {
		if d, err := catalog.Describe(ctx, nil, n); err != nil || d.Name != "footage.moments" || d.Command != "metarush footage moments" || len(d.Returns) == 0 {
			t.Fatalf("describe %s: %+v %v", n, d, err)
		}
	}
	if _, err := catalog.Describe(ctx, nil, "no.such.thing"); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("unknown name: %v", err)
	}
}

func TestLinesStayOnOneLine(t *testing.T) {
	long := strings.Repeat("word ", 60)
	l := catalog.Line(catalog.Entry{Name: "x.y", About: long, PeopleOnly: true, OwnerOnly: true})
	if strings.Contains(l, "\n") || len([]rune(l)) > catalog.LineAbout+60 || !strings.HasSuffix(l, "(a person's to do, owners)") || !strings.Contains(l, "…") {
		t.Fatalf("line: %q", l)
	}
	if got := catalog.Short("Short enough"); got != "Short enough" {
		t.Fatalf("short: %q", got)
	}
}

// An example fills in nothing for the caller: no yes or no, number or choice
// (a consent's "adult" and "self" are the person's to say).
func TestExamplesAreOnlyPlaceholders(t *testing.T) {
	ctx := context.Background()
	for _, name := range []string{"consent.record", "voiceover.clone"} {
		d, err := catalog.Describe(ctx, nil, name)
		if err != nil {
			t.Fatal(err)
		}
		if len(d.Example) == 0 {
			t.Fatalf("%s has no example", name)
		}
		for k, v := range d.Example {
			s, ok := v.(string)
			if _, isList := v.([]any); isList {
				continue
			}
			if _, isObj := v.(map[string]any); isObj {
				continue
			}
			if !ok || !strings.HasPrefix(s, "<") || !strings.HasSuffix(s, ">") {
				t.Errorf("%s: example %s is %v, not a placeholder", name, k, v)
			}
		}
	}
	all, _ := catalog.All(ctx, nil, catalog.KindOperation)
	for _, e := range all {
		d, _ := catalog.Describe(ctx, nil, e.Name)
		for k, v := range d.Example {
			switch v.(type) {
			case bool, float64, int:
				t.Errorf("%s: example %s is filled in (%v)", e.Name, k, v)
			}
		}
	}
}

// The operations that check themselves first are a list, not a guess from
// their fields: each is a change with a confirmToken, and an operation with
// such a field that saves without it (brand.set) is not on it.
func TestChecksFirstIsAList(t *testing.T) {
	for _, n := range catalog.ChecksFirstNames() {
		op, ok := ops.Get(n)
		if !ok || !catalog.ChecksFirst(op) {
			t.Errorf("%s is listed as checking first, but is no change with a confirmToken", n)
		}
	}
	for _, n := range []string{"brand.set", "project.push", "project.create"} {
		if op, _ := ops.Get(n); catalog.ChecksFirst(op) {
			t.Errorf("%s doesn't check first", n)
		}
	}
	if d, _ := catalog.Describe(context.Background(), nil, "brand.set"); d.OwnCheck {
		t.Error("describe says brand.set checks itself")
	}
	if d, _ := catalog.Describe(context.Background(), nil, "cut.trim"); !d.OwnCheck {
		t.Error("describe says cut.trim doesn't check itself")
	}
}

// trickySource is a second catalogue whose entries test the rules: a name
// like an operation's tool name, and flags its operation doesn't hold.
type trickySource struct{}

var trickyEntries = []catalog.Entry{
	{Name: "footage_moments", Kind: "tricky", About: "Find stock footage of quiet moments", RunWith: "ops.find"},
	{Name: "quiet-change", Kind: "tricky", About: "Tidy the harbor archive", RunWith: "project.create"},
	{Name: "for-people", Kind: "tricky", About: "Sign the harbor release", PeopleOnly: true, RunWith: "ops.find"},
	{Name: "local-thing", Kind: "tricky", About: "Read the harbor drive", ThisComputerOnly: true, RunWith: "ops.find"},
	{Name: "owner-thing", Kind: "tricky", About: "Set the harbor key", OwnerOnly: true, RunWith: "ops.find"},
	{Name: "claims-change", Kind: "tricky", About: "Rename the harbor files", Changes: true, RunWith: "ops.find"},
	{Name: "fine", Kind: "tricky", About: "Look up harbor tides", RunWith: "ops.find"},
}

func (trickySource) Kind() string { return "tricky" }
func (trickySource) Entries(context.Context, *app.App) ([]catalog.Entry, error) {
	return trickyEntries, nil
}
func (trickySource) Describe(_ context.Context, _ *app.App, name string) (*catalog.Description, error) {
	for _, e := range trickyEntries {
		if e.Name == name {
			return &catalog.Description{Name: e.Name, Kind: e.Kind, About: e.About, Changes: e.Changes, PeopleOnly: e.PeopleOnly,
				OwnerOnly: e.OwnerOnly, ThisComputerOnly: e.ThisComputerOnly, RunWith: e.RunWith, RunInput: map[string]any{"task": e.Name}}, nil
		}
	}
	return nil, apperr.New(apperr.NotFound, "no such entry")
}

// Another catalogue's entry is never an operation by mistake, says what
// running it really does, and runs only when its operation holds callers to
// its own rules.
func TestAnotherCataloguesEntriesKeepTheirRules(t *testing.T) {
	ctx := context.Background()
	defer catalog.Register(trickySource{})()
	// A name like an operation's tool name is the entry's; the operation keeps its own name.
	if d, err := catalog.Describe(ctx, nil, "footage_moments"); err != nil || d.Kind != "tricky" {
		t.Fatalf("footage_moments: %+v %v", d, err)
	}
	if d, err := catalog.Describe(ctx, nil, "footage.moments"); err != nil || d.Kind != catalog.KindOperation {
		t.Fatalf("footage.moments: %+v %v", d, err)
	}
	if d, err := catalog.Describe(ctx, nil, "render_submit"); err != nil || d.Name != "render.submit" {
		t.Fatalf("an operation's tool name still finds it when nothing else has the name: %+v %v", d, err)
	}
	// What running it really does: quiet-change runs project.create.
	d, _ := catalog.Describe(ctx, nil, "quiet-change")
	if !d.Changes {
		t.Fatal("describe says quiet-change changes nothing")
	}
	ro, _ := catalog.Find(ctx, nil, catalog.Query{Text: "tidy the harbor archive", ReadOnly: true, Kinds: []string{"tricky"}})
	for _, h := range ro {
		if h.Name == "quiet-change" {
			t.Fatal("read-only find returned quiet-change, which runs project.create")
		}
	}
	all, _ := catalog.Find(ctx, nil, catalog.Query{Text: "tidy the harbor archive", Kinds: []string{"tricky"}})
	if len(all) == 0 || all[0].Name != "quiet-change" || !strings.Contains(catalog.Line(all[0].Entry), "changes something") {
		t.Fatalf("find: %v", all)
	}
	// Flags its operation doesn't hold callers to: refused — a person's to do
	// as that, with the command to hand to a person.
	for _, name := range []string{"for-people", "local-thing", "owner-thing", "claims-change"} {
		d, err := catalog.Describe(ctx, nil, name)
		if err != nil {
			t.Fatal(err)
		}
		want := apperr.Unauthorized
		if name == "for-people" {
			want = apperr.HumanOnly
			d.Command = "metarush harbor sign"
		}
		_, _, err = catalog.RunTarget(name, d, nil)
		if ae := apperr.From(err); ae.Code != want {
			t.Errorf("%s ran: %v", name, err)
		} else if name == "for-people" && (ae.Details.(map[string]any)["command"] != "metarush harbor sign" || !strings.Contains(ae.Message, "Only a person starts for-people")) {
			t.Errorf("a person's entry says so and how: %+v", ae)
		}
	}
	d, _ = catalog.Describe(ctx, nil, "fine")
	op, in, err := catalog.RunTarget("fine", d, map[string]any{"x": 1})
	if err != nil || op.Name != "ops.find" || in["task"] != "fine" || in[catalog.RunInputKey].(map[string]any)["x"] != 1 {
		t.Fatalf("fine: %v %v %v", op, in, err)
	}
}

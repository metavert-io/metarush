package recipes

import (
	"context"
	"encoding/json"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// lightApp is a workspace with nothing in it: enough for recipes written by
// hand, their versions and runs (no footage, no services).
func lightApp(t *testing.T) *app.App {
	t.Helper()
	keyring.MockInit()
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	ws := ids.New(ids.Workspace)
	cfg.Workspace = config.Ref{ID: ws, Name: "T"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "studio"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "Ada"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "media")}}
	cfg.Secrets = "env"
	a, err := app.OpenWith(context.Background(), cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	_ = a.DB.Insert(context.Background(), store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: time.Now().UTC()})
	return a
}

// changes flattens what Differences said, part by part ("inputs: New: …").
func changes(parts []RecipeChangePart) []string {
	var out []string
	for _, p := range parts {
		for _, c := range p.Changes {
			out = append(out, p.Part+": "+c)
		}
	}
	return out
}

func hasChange(t *testing.T, all []string, want string) {
	t.Helper()
	for _, c := range all {
		if strings.Contains(c, want) {
			return
		}
	}
	t.Errorf("no change says %q in:\n  %s", want, strings.Join(all, "\n  "))
}

// What changed between two versions is said part by part, in plain words:
// inputs (new, gone, a new default, a new limit, where it goes), sections
// (new, gone, chosen differently, in another order), narration, music and
// captions, a graphic's words, pictures, rules, sign-offs and hints, the title
// and the video's shape and length — and nothing when nothing changed.
func TestDifferencesSayWhatChanged(t *testing.T) {
	a := lightApp(t)
	ctx := context.Background()
	was := doc()
	was.Rules = []models.RecipeRule{{Text: "Under 35 seconds"}, {Text: "Captions at the top"}}
	was.Hints = []string{"Open on a wide shot."}
	if got := Differences(ctx, a, was, Clone(was)); len(got) != 0 {
		t.Fatalf("the same document changes nothing: %+v", got)
	}
	now := Clone(was)
	now.Title = "Gala recap"
	now.Length = &models.RecipeRange{Min: 25, Max: 35}
	now.Cut.Output.Preset = editspec.PresetLandscape
	// Inputs: a new one, one gone, a new default and limit, a second place it goes.
	now.Inputs = append(now.Inputs, models.RecipeInput{Name: "cta", Kind: models.RecipeWords, Label: "The call to action", Default: models.RecipeValue{Text: "Join us"}, MaxLength: 30})
	now.Bindings = append(now.Bindings, models.RecipeBinding{Input: "cta", To: "param:end.cta"})
	var kept []models.RecipeInput
	for _, in := range now.Inputs {
		switch in.Name {
		case "art":
			continue
		case "headline":
			in.Default.Text, in.MaxLength = "Spring gala", 40
		}
		kept = append(kept, in)
	}
	now.Inputs = kept
	var binds []models.RecipeBinding
	for _, b := range now.Bindings {
		if b.Input != "art" {
			binds = append(binds, b)
		}
	}
	now.Bindings = append(binds, models.RecipeBinding{Input: "headline", To: "title"})
	// Sections: moments chosen for something else, the end before the moments, a new one.
	now.Sections = []models.RecipeSection{now.Sections[0], now.Sections[2], now.Sections[1],
		{Name: "credits", Kind: models.RecipeFixed, Clips: []editspec.Clip{{ID: "credits", AssetID: astA, In: 5, Out: 7}}}}
	now.Sections[2].Purpose, now.Sections[2].Count = "the crowd cheering", &models.RecipeRange{Min: 3, Max: 5}
	// Sound: another narration line, captions on.
	now.Cut.Voiceover.Beats[1].Text = "See you in the spring."
	now.Cut.Captions = &editspec.Captions{Enabled: true, Position: "top"}
	// A graphic's words, a picture's version.
	for i := range now.Cut.Overlays {
		if now.Cut.Overlays[i].ID == "end" {
			now.Cut.Overlays[i].Params = json.RawMessage(`{"headline":"Spring gala","image":"art","cta":"Join us"}`)
		}
	}
	now.Cut.Resources = map[string]editspec.ResourcePin{"art": {ID: resA, SHA256: strings.Repeat("b", 64)}}
	now.Rules = []models.RecipeRule{{Text: "Captions at the top"}, {Text: "First names only"}}
	now.Signoffs = []string{"Legal OKs the claims"}
	now.Hints = []string{"Open on a wide shot.", "End on the logo."}

	parts := Differences(ctx, a, was, now)
	all := changes(parts)
	for _, want := range []string{
		"title: Called “Gala recap” now (was “Event recap”)",
		"video: Widescreen now (was vertical)",
		"video: Keeps to 25–35 s now",
		"inputs: New: the call to action (words, up to 30 characters), for the end card's call to action",
		"inputs: No longer changes each run: ",
		"inputs: The input “headline”: “Spring gala” unless a run gives another now (was “Gala night”)",
		"up to 40 characters now (was up to 80 characters)",
		"goes to the title card's title, the end card's headline and the video's title now",
		"sections: New section “credits”: the same every run, 2 s (1 clip: ",
		"sections: “moments”: for “the crowd cheering” now (was for “lively moments”)",
		"sections: “moments”: 3–5 moments now (was 2–4 moments)",
		"sections: In another order: “intro”, “end”, “moments” now",
		"sound: Narration line 2 says “See you in the spring.” now",
		"sound: Captions are on now",
		"graphics: The end card's call to action: “Join us” now",
		"graphics: The end card's headline: “Spring gala” now (was “Gala night”)",
		"pictures: The picture “art” is another version now",
		"rules: New rule: “First names only”",
		"rules: No longer a rule: “Under 35 seconds”",
		"signoffs: New sign-off: “Legal OKs the claims”",
		"hints: New hint: “End on the logo.”",
	} {
		hasChange(t, all, want)
	}
	if got := removedRules(was, now); !reflect.DeepEqual(got, []string{"Under 35 seconds"}) {
		t.Errorf("the rules it no longer keeps: %v", got)
	}
	for _, c := range all {
		if f := plainwords.CheckShown(c); len(f) > 0 {
			t.Errorf("not plain: %q %+v", c, f)
		}
	}
	// The order of the parts is the page's: inputs before sections, rules before hints.
	var order []string
	for _, p := range parts {
		order = append(order, p.Part)
	}
	if strings.Join(order, ",") != "title,video,inputs,sections,sound,graphics,pictures,rules,signoffs,hints" {
		t.Errorf("parts in order: %v", order)
	}
	// A long list stops at twelve and says how many more.
	many := Clone(was)
	for i := 0; i < 15; i++ {
		many.Hints = append(many.Hints, "Hint number "+string(rune('a'+i)))
	}
	for _, p := range Differences(ctx, a, was, many) {
		if p.Part == partHints && (len(p.Changes) != maxChanges+1 || p.Changes[maxChanges] != "and 3 more") {
			t.Errorf("a long list: %v", p.Changes)
		}
		if n := countChanges([]RecipeChangePart{p}); p.Part == partHints && n != 15 {
			t.Errorf("counted %d changes, want 15", n)
		}
	}
}

// recipe.diff compares a draft with the version it was made from (by default)
// or any two versions; recipe.update --dry-run says what a draft would change
// and saves nothing; a saved draft says what it changed.
func TestDiffAndDraftPreview(t *testing.T) {
	a := lightApp(t)
	ctx := context.Background()
	r, err := newRecipe(ctx, a, "gala", "Event recap", "")
	if err != nil {
		t.Fatal(err)
	}
	d := doc()
	d.Rules = []models.RecipeRule{{Text: "Under 35 seconds"}}
	d.Makes = MakesWords(d)
	v1, _, err := saveVersion(ctx, a, r, d, "", "first", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := makeCurrent(ctx, a, r, v1); err != nil {
		t.Fatal(err)
	}
	// The first version has nothing before it.
	first := must[RecipeDiffOut](t, a, "recipe.diff", RecipeDiffIn{Recipe: "gala"}, false)
	if first.From != nil || first.To.Number != 1 || !strings.Contains(first.Summary, "the first version") || len(first.Parts) != 0 {
		t.Fatalf("the first version: %+v", first)
	}
	// A dry run saves nothing and says what would change.
	patch := json.RawMessage(`{"rules":[{"text":"First names only"}]}`)
	dry := must[RecipeUpdateOut](t, a, "recipe.update", RecipeUpdateIn{RecipeRefIn: RecipeRefIn{Recipe: "gala"}, AddHints: []string{"End on the logo."}, Patch: patch, DryRun: true}, false)
	if !dry.DryRun || dry.Same || dry.Version.Number != 1 || dry.Recipe != "gala" || dry.Title != "Event recap" || !strings.Contains(dry.Message, "Nothing is saved yet") {
		t.Fatalf("dry run: %+v", dry)
	}
	all := changes(dry.Changes)
	hasChange(t, all, "hints: New hint: “End on the logo.”")
	hasChange(t, all, "rules: New rule: “First names only”")
	if !reflect.DeepEqual(dry.RemovedRules, []string{"Under 35 seconds"}) {
		t.Errorf("rules it would drop: %v", dry.RemovedRules)
	}
	if vs, _ := listVersions(ctx, a, r.ID); len(vs) != 1 {
		t.Fatalf("a dry run saves nothing: %d versions", len(vs))
	}
	same := must[RecipeUpdateOut](t, a, "recipe.update", RecipeUpdateIn{RecipeRefIn: RecipeRefIn{Recipe: "gala"}, AddHints: d.Hints, DryRun: true}, false)
	if !same.Same || !strings.Contains(same.Message, "Nothing would change") {
		t.Fatalf("nothing to change: %+v", same)
	}
	// Saved, the draft says what it changed; recipe.diff says the same.
	saved := must[RecipeUpdateOut](t, a, "recipe.update", RecipeUpdateIn{RecipeRefIn: RecipeRefIn{Recipe: "gala"}, AddHints: []string{"End on the logo."}, Patch: patch}, false)
	if saved.Version.Number != 2 || saved.Version.State != models.RecipeDraft || len(saved.Changes) == 0 || saved.Recipe != "gala" {
		t.Fatalf("saved: %+v", saved)
	}
	diff := must[RecipeDiffOut](t, a, "recipe.diff", RecipeDiffIn{Recipe: "gala"}, false)
	if diff.To.Number != 2 || diff.From == nil || diff.From.Number != 1 || diff.Same || diff.Changes != 3 ||
		!reflect.DeepEqual(diff.RemovedRules, []string{"Under 35 seconds"}) || !strings.HasPrefix(diff.Summary, "Version 2 changes 3 things from version 1: ") {
		t.Fatalf("the draft against the version it came from: %+v", diff)
	}
	if !reflect.DeepEqual(diff.Parts, saved.Changes) {
		t.Errorf("the draft's own changes and recipe.diff's differ:\n%+v\n%+v", diff.Parts, saved.Changes)
	}
	// Any two, either way round; a version with itself is the same.
	back := must[RecipeDiffOut](t, a, "recipe.diff", RecipeDiffIn{Recipe: "gala", To: "1", From: "draft"}, false)
	hasChange(t, changes(back.Parts), "hints: No longer a hint: “End on the logo.”")
	if back.To.Number != 1 || back.From.Number != 2 || !reflect.DeepEqual(back.RemovedRules, []string{"First names only"}) {
		t.Fatalf("back again: %+v", back)
	}
	self := must[RecipeDiffOut](t, a, "recipe.diff", RecipeDiffIn{Recipe: "gala", To: "current", From: "1"}, false)
	if !self.Same || len(self.Parts) != 0 {
		t.Fatalf("a version with itself: %+v", self)
	}
	if _, err := call[RecipeDiffOut](t, a, "recipe.diff", RecipeDiffIn{Recipe: "gala", To: "9"}, false); codeOf(err) != "E_NOT_FOUND" {
		t.Fatalf("no such version: %v", err)
	}
}

// Two runs side by side: each run's review copy to play, or why there is none.
func TestReviewCopiesSideBySide(t *testing.T) {
	a := lightApp(t)
	ctx := context.Background()
	now := time.Now().UTC()
	render := func(state string) string {
		rd := models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), ProjectID: ids.New(ids.Project), RevisionID: ids.New(ids.Revision), RevisionNumber: 3,
			Quality: "review", State: state, CreatedAt: now}
		if state == models.RenderAvailable {
			rd.Output = &models.RenderOutput{StorageKey: "renders/x.mp4", SHA256: "ab", Size: 10, Duration: 12, Width: 1080, Height: 1920, FPS: 30}
		}
		if err := a.DB.Insert(ctx, store.CRenders, rd); err != nil {
			t.Fatal(err)
		}
		return rd.ID
	}
	ready, going := render(models.RenderAvailable), render(models.RenderRendering)
	got := reviewCopies(ctx, a, &models.RecipeRun{Number: 1, Render: ready}, &models.RecipeRun{Number: 2, Render: going})
	if l := got[0]; l == nil || !l.Ready || l.VideoURL != "/media/renders/"+ready || l.ThumbURL == "" || l.Words != "ready, 12 s" || l.Width != 1080 || l.Version != 3 || l.Run != 1 {
		t.Fatalf("a review copy to play: %+v", l)
	}
	if r := got[1]; r == nil || r.Ready || r.VideoURL != "" || r.Words != "its review copy is still being made" {
		t.Fatalf("one still being made: %+v", r)
	}
	none := reviewCopies(ctx, a, &models.RecipeRun{Number: 3}, &models.RecipeRun{Number: 4, Render: ready, ProjectGone: true})
	if none[0] != nil || none[1] != nil {
		t.Fatalf("no review copy, or its project cleared out: %+v", none)
	}
}

// --input name=value is read the same everywhere: words, several clips or
// pictures (commas, or the name again), and a clear refusal.
func TestInputsFromFlags(t *testing.T) {
	got, err := InputsFromFlags([]string{`headline=Read free`, "art=hero, villain", "art=sidekick", "voice=Bram"})
	if err != nil {
		t.Fatal(err)
	}
	want := []RecipeInputValue{{Name: "headline", Text: "Read free", Items: []string{"Read free"}}, {Name: "art", Text: "sidekick", Items: []string{"hero", "villain", "sidekick"}},
		{Name: "voice", Text: "Bram", Items: []string{"Bram"}}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v", got)
	}
	if _, err := InputsFromFlags([]string{"headline"}); err == nil || !strings.Contains(err.Error(), "name=value") {
		t.Fatalf("no value: %v", err)
	}
}

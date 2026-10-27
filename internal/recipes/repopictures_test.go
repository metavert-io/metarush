package recipes

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// artRepo builds the fixture repository acme/widget (scripts/make-fixture-repo.sh)
// with the steps asked for, and points GitHub addresses at it.
func artRepo(t *testing.T, base string, steps ...string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	if base == "" {
		base = t.TempDir()
		steps = append([]string{""}, steps...)
	}
	for _, s := range steps {
		args := []string{filepath.Join("..", "..", "scripts", "make-fixture-repo.sh"), base}
		if s != "" {
			args = append(args, s)
		}
		if out, err := exec.Command("bash", args...).CombinedOutput(); err != nil {
			t.Fatalf("fixture repo: %v %s", err, out)
		}
	}
	t.Setenv("METARUSH_GIT_BASE", base)
	return base
}

// repoPicture is the picture a file of the repository was kept as.
func (f *fx) repoPicture(repoID, file string) *models.Resource {
	f.t.Helper()
	var r models.Resource
	if err := f.a.DB.C(store.CResources).FindOne(f.ctx, bson.M{"workspaceId": f.a.WorkspaceID(), "repoFile.repoId": repoID, "repoFile.path": "art/characters/" + file}).Decode(&r); err != nil {
		f.t.Fatalf("%s: %v", file, err)
	}
	return &r
}

func runInput(ins []models.RecipeRunInput, name string) *models.RecipeRunInput {
	for i := range ins {
		if ins[i].Name == name {
			return &ins[i]
		}
	}
	return nil
}

// Art kept in git (D216): a cut whose graphics show pictures a repository
// keeps from art/characters is saved as a recipe whose input takes them from
// that folder; a run records the commit and each picture's hash; after new
// art is pushed and the repository refreshed, running again puts Alice's new
// art in her place and, where Bob left, the next picture.
func TestPicturesFromARepositoryPath(t *testing.T) {
	f := setup(t)
	base := artRepo(t, "", "art")
	res, err := resources.FetchRepository(f.ctx, f.a, resources.FetchRequest{Kind: resources.FetchRepo, URL: "https://github.com/acme/widget", ProjectID: f.teaser,
		Pictures: []string{"art/characters"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo := res.Resources[0]
	commit1 := repo.Latest().Repo.Commit
	alice, bob := f.repoPicture(repo.ID, "alice.png"), f.repoPicture(repo.ID, "bob.jpg")
	// The cast cut: the title card shows Bob, the end card Alice.
	spec := &editspec.Spec{SchemaVersion: 1, Title: "The cast", Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30},
		Clips: []editspec.Clip{{ID: "hook", AssetID: f.interview, In: 1, Out: 4, Label: "the hook"}, {ID: "answer", AssetID: f.interview, In: 6, Out: 9, Label: "the answer"}},
		Overlays: []editspec.Overlay{
			{ID: "title", Template: editspec.TplTitleCard, At: 0, Duration: 2, Params: json.RawMessage(`{"title":"The cast","image":"widget-bob"}`)},
			{ID: "end", Template: editspec.TplEndCard, At: 3, Duration: 3, Params: json.RawMessage(`{"headline":"Meet them","cta":"Read free","image":"widget-alice"}`)}},
		Resources: map[string]editspec.ResourcePin{"widget-bob": {ID: bob.ID, SHA256: bob.Current}, "widget-alice": {ID: alice.ID, SHA256: alice.Current}}}
	rev := f.apply(editspec.Plan{ProjectID: f.teaser, Branch: "cast", Instruction: "the cast", Spec: spec})
	must[ops.RevisionSummary](t, f.a, "revision.approve", ops.RevisionApproveIn{ID: rev}, true)
	saved := must[RecipeSaveOut](t, f.a, "recipe.save", RecipeSaveIn{From: f.teaser, Branch: "cast", Title: "Cast"}, true)
	var cast *RecipeProposalItem
	for i, it := range saved.Proposal {
		if strings.Contains(it.What, "“art/characters” in acme/widget") {
			cast = &saved.Proposal[i]
		}
	}
	if cast == nil || !cast.Varies || !strings.Contains(cast.Why, "the pictures the repository keeps there") {
		t.Fatalf("one input for the folder: %+v", saved.Proposal)
	}
	show := must[RecipeShowOut](t, f.a, "recipe.show", RecipeRefIn{Recipe: "cast"}, false)
	var in *models.RecipeInput
	for i := range show.Doc.Inputs {
		if show.Doc.Inputs[i].Name == cast.Key {
			in = &show.Doc.Inputs[i]
		}
	}
	if in == nil || in.Kind != models.RecipePictures || in.From == nil || in.From.Repo != repo.ID || in.From.Path != "art/characters" || in.Count == nil || in.Count.Max != 2 ||
		strings.Join(in.Default.Items, ",") != bob.ID+","+alice.ID {
		t.Fatalf("input %+v", in)
	}
	var to []string
	for _, b := range show.Doc.Bindings {
		if b.Input == cast.Key {
			to = append(to, b.To)
		}
	}
	if strings.Join(to, ",") != "resource:widget-bob,resource:widget-alice" || show.Version.Check == nil || !show.Version.Check.OK {
		t.Fatalf("bindings %v / check %+v", to, show.Version.Check)
	}
	for _, row := range show.Inputs {
		if row.Name == cast.Key && row.From != "the pictures from “art/characters” in acme/widget at the commit it shows" {
			t.Fatalf("where it comes from: %q", row.From)
		}
	}
	// Run 1, the same art: the run records the commit and each picture's hash, and changes nothing.
	p1 := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "cast"}, false)
	x := runInput(p1.Inputs, cast.Key)
	if !p1.Ready || x == nil || x.Repo != repo.ID || x.Commit != commit1 || len(x.Items) != 2 || x.Items[0].ID != bob.ID || x.Items[0].SHA256 != bob.Current ||
		x.Items[0].Path != "art/characters/bob.jpg" || x.Items[1].ID != alice.ID || x.Changed || !strings.Contains(x.Words, "from “art/characters” in acme/widget, as it was on 12 Oct 2026") {
		t.Fatalf("plan %+v / %+v", p1.Problems, x)
	}
	r1 := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "cast"}, Fingerprint: p1.Fingerprint}, true).Run
	if got := runInput(r1.Inputs, cast.Key); got == nil || got.Commit != commit1 || got.Items[1].SHA256 != alice.Current {
		t.Fatalf("the run records the commit and the hashes: %+v", got)
	}
	f.finishReview(r1.ID)
	if done := f.advance(r1.ID); done.State != models.RunDone {
		t.Fatalf("run 1 %+v", done.Steps)
	}
	// New art pushed: Alice changed, Bob left, Frank joined; the repository refreshed.
	artRepo(t, base, "art-second")
	if _, err := resources.FetchRepository(f.ctx, f.a, resources.FetchRequest{Kind: resources.FetchRepo, URL: "https://github.com/acme/widget", ProjectID: f.teaser, ResourceID: repo.ID}, nil); err != nil {
		t.Fatal(err)
	}
	alice2, carol := f.repoPicture(repo.ID, "alice.png"), f.repoPicture(repo.ID, "carol.svg")
	if alice2.Current == alice.Current {
		t.Fatal("alice has new art")
	}
	p2 := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Repeats: "cast#1"}, false)
	x = runInput(p2.Inputs, cast.Key)
	if !p2.Ready || x == nil || x.Commit == commit1 || !x.Changed || len(x.Items) != 2 || x.Items[0].ID != carol.ID || x.Items[1].ID != alice.ID || x.Items[1].SHA256 != alice2.Current {
		t.Fatalf("again %+v / %+v", p2.Problems, x)
	}
	r2 := must[RecipeRunOut](t, f.a, "recipe.again", RecipeAgainIn{Run: "cast#1", Fingerprint: p2.Fingerprint}, true).Run
	if r2.Revision == "" {
		t.Fatalf("again made no version: %+v", r2.Steps)
	}
	pins := f.revision(r2.Revision).Spec.Resources
	if pins["widget-bob"].ID != carol.ID || pins["widget-alice"].ID != alice.ID || pins["widget-alice"].SHA256 != alice2.Current {
		t.Fatalf("pins %+v", pins)
	}
	// A folder the repository no longer keeps can't be taken from.
	if _, err := ops.CallTyped[ops.RepoPicturesOut](f.ctx, f.a, "resources.pictures.set", ops.RepoPicturesSetIn{ResourceRefIn: ops.ResourceRefIn{Ref: repo.ID, ProjectID: f.teaser},
		Drop: []string{"art/characters"}}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	p3 := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "cast"}, false)
	if p3.Ready || !strings.Contains(strings.Join(p3.Problems, " "), "The repository “acme/widget” doesn't keep the pictures in “art/characters”") {
		t.Fatalf("plan without the folder %+v", p3.Problems)
	}
}

// A Remotion picture setting (D209) bound to a picture a repository keeps: a
// run draws the same approved version with the repository's picture as the
// clip's own — no new code, no approval — and records the commit.
func TestRemotionPictureFromARepository(t *testing.T) {
	f := setup(t)
	artRepo(t, "", "art")
	res, err := resources.FetchRepository(f.ctx, f.a, resources.FetchRequest{Kind: resources.FetchRepo, URL: "https://github.com/acme/widget", ProjectID: f.teaser,
		Pictures: []string{"art/characters"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	repo := res.Resources[0]
	alice := f.repoPicture(repo.ID, "alice.png")
	ws := f.a.WorkspaceID()
	now := time.Now().UTC()
	src := models.RemotionSource{ID: ids.New(ids.RemotionSource), WorkspaceID: ws, ProjectID: f.teaser, Name: "opener", Title: "Opener", Versions: 1, CreatedAt: now, UpdatedAt: now}
	hash := strings.Repeat("ab", 32)
	ver := models.RemotionVersion{ID: ws + "/" + hash, WorkspaceID: ws, SourceID: src.ID, ProjectID: f.teaser, Hash: hash, Number: 1, Kind: "import", State: models.RemotionApproved,
		PackagesAllowed: true, At: now, CreatedAt: now, UpdatedAt: now,
		Compositions: []models.RemotionComposition{{ID: "Open", Width: 640, Height: 360, FPS: 30, DurationInFrames: 150, Duration: 5, Pictures: []string{"HERO"}}},
		Pictures:     []models.RemotionPicture{{Name: "HERO", File: "public/hero.png", Setting: true, Uses: []models.RemotionPictureUse{{File: "src/Open.tsx", Line: 4, How: "static", Wrapped: true}}}}}
	src.Head, src.HeadNumber, src.Approved, src.ApprovedNumber = hash, 1, hash, 1
	if err := f.a.DB.Insert(f.ctx, store.CRemotionSources, src); err != nil {
		t.Fatal(err)
	}
	if err := f.a.DB.Insert(f.ctx, store.CRemotionVersions, ver); err != nil {
		t.Fatal(err)
	}
	if err := f.a.DB.Update(f.ctx, store.CAssets, ws, f.intro, bson.M{"$set": bson.M{"source": models.AssetSource{Platform: models.PlatformRemotion,
		Remotion: &models.RemotionClipSource{SourceID: src.ID, SourceName: "opener", SourceTitle: "Opener", Version: hash, VersionNumber: 1, Kind: models.SourceRemotionRender, Composition: "Open"}}}}); err != nil {
		t.Fatal(err)
	}
	saved := f.save(RecipeSaveIn{})
	key := ""
	for _, it := range saved.Proposal {
		if strings.Contains(it.What, "HERO in “Open”") {
			key = it.Key
		}
	}
	if key == "" {
		t.Fatalf("proposal %+v", saved.Proposal)
	}
	// The picture setting takes Alice from the repository (a draft a person makes current).
	show := must[RecipeShowOut](t, f.a, "recipe.show", RecipeRefIn{Recipe: "app-teaser"}, false)
	inputs := show.Doc.Inputs
	for i := range inputs {
		if inputs[i].Name == key {
			inputs[i].From = &models.RecipeSource{Repo: repo.ID, Path: "art/characters/alice.png"}
			inputs[i].Count = &models.RecipeRange{Min: 1, Max: 1}
		}
	}
	patch, _ := json.Marshal(map[string]any{"inputs": inputs})
	must[RecipeUpdateOut](t, f.a, "recipe.update", RecipeUpdateIn{RecipeRefIn: RecipeRefIn{Recipe: "app-teaser"}, Patch: patch, Note: "art from git"}, true)
	must[RecipePublishOut](t, f.a, "recipe.publish", RecipeRefIn{Recipe: "app-teaser"}, true)
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	x := runInput(plan.Inputs, key)
	code := stepOf(&models.RecipeRun{Steps: plan.Steps}, stepCode)
	if !plan.Ready || x == nil || x.Commit != repo.Latest().Repo.Commit || len(x.Items) != 1 || x.Items[0].ID != alice.ID || code == nil ||
		!strings.Contains(code.Detail, "the new pictures (no new code to approve)") {
		t.Fatalf("plan %+v / %+v / %+v", plan.Problems, x, plan.Steps)
	}
	run := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: plan.Fingerprint}, true).Run
	drawing := stepOf(&run.RecipeRun, stepCode)
	var rr models.RemotionRender
	if err := f.a.DB.GetByID(f.ctx, store.CRemotionRenders, ws, ws+"/"+drawing.Work["brand"], &rr); err != nil {
		t.Fatal(err)
	}
	if rr.Version != hash || len(rr.OwnPictures) != 1 || rr.OwnPictures[0].Name != "HERO" || rr.OwnPictures[0].SHA256 != alice.Current {
		t.Fatalf("drawn with the repository's picture: %+v", rr)
	}
	if n, _ := f.a.DB.C(store.CRemotionVersions).CountDocuments(f.ctx, bson.M{"sourceId": src.ID}); n != 1 {
		t.Fatalf("%d versions: no new code", n)
	}
}

// The document's own shape: a repository source belongs to a pictures input
// and names a path a repository can have.
func TestRepositorySourceShape(t *testing.T) {
	d := &models.RecipeDoc{Format: models.RecipeFormat, Title: "T", Lands: models.RecipeLanding{Kind: models.RecipeLandsNewProject},
		Sections: []models.RecipeSection{{Name: "a", Kind: models.RecipeFixed, Clips: []editspec.Clip{{ID: "c", AssetID: "ast_x", Out: 1}}}},
		Inputs: []models.RecipeInput{
			{Name: "headline", Kind: models.RecipeWords, From: &models.RecipeSource{Repo: "res_x", Path: "art"}},
			{Name: "art", Kind: models.RecipePictures, From: &models.RecipeSource{Repo: "res_x", Folder: "fld_x", Path: "../up"}},
			{Name: "fine", Kind: models.RecipePictures, From: &models.RecipeSource{Repo: "res_x", Path: "art/characters"}},
		}}
	var got []string
	for _, is := range shapeIssues(d) {
		got = append(got, is.Where+": "+is.Message)
	}
	all := strings.Join(got, "\n")
	if len(got) != 3 || !strings.Contains(all, `headline: The input "headline" takes pictures from a repository, but it is words.`) ||
		!strings.Contains(all, "from a repository and from somewhere else") || !strings.Contains(all, "a path that isn't one a repository has") {
		t.Fatalf("%s", all)
	}
	// Places keep their pictures; empty ones take the next in order.
	usable := map[string]models.RecipeRunItem{"a": {ID: "a"}, "b": {ID: "b"}, "c": {ID: "c"}}
	var ids []string
	for _, it := range placePictures([]string{"a", "b", "c"}, usable, []string{"gone", "b"}, 2) {
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, ",") != "a,b" {
		t.Fatalf("places %v", ids)
	}
	ids = nil
	for _, it := range placePictures([]string{"a"}, map[string]models.RecipeRunItem{"a": {ID: "a"}}, nil, 3) {
		ids = append(ids, it.ID)
	}
	if strings.Join(ids, ",") != "a" {
		t.Fatalf("fewer pictures than places %v", ids)
	}
}

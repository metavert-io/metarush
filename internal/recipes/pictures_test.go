package recipes

import (
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// A clip made from a Remotion composition whose pictures are settings (D209):
// the recipe offers the picture as an input; the first run keeps the project's
// own art; making it again with new art draws the same approved version with
// the picture as the clip's own (remotion.use's `pictures`) — no new version
// of the code, no approval — and the new clip is the fixed section's in a new
// version of the same cut. The source's own pictures, and the cuts that follow
// them, don't change.
func TestNewArtWithoutAnApproval(t *testing.T) {
	f := setup(t)
	ws := f.a.WorkspaceID()
	now := time.Now().UTC()
	src := models.RemotionSource{ID: ids.New(ids.RemotionSource), WorkspaceID: ws, ProjectID: f.teaser, Name: "opener", Title: "Opener", Versions: 1, CreatedAt: now, UpdatedAt: now}
	hash := strings.Repeat("cd", 32)
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
	// The brand intro is a render of that composition, with the project's own art.
	if err := f.a.DB.Update(f.ctx, store.CAssets, ws, f.intro, bson.M{"$set": bson.M{"source": models.AssetSource{Platform: models.PlatformRemotion,
		Remotion: &models.RemotionClipSource{SourceID: src.ID, SourceName: "opener", SourceTitle: "Opener", Version: hash, VersionNumber: 1, Kind: models.SourceRemotionRender, Composition: "Open"}}}}); err != nil {
		t.Fatal(err)
	}
	saved := f.save(RecipeSaveIn{})
	var art *RecipeProposalItem
	for i, it := range saved.Proposal {
		if strings.Contains(it.What, "HERO in “Open”") {
			art = &saved.Proposal[i]
		}
	}
	if art == nil || !art.Varies || !strings.Contains(art.Why, "new art needs no new code") {
		t.Fatalf("the picture setting is an input: %+v", saved.Proposal)
	}
	show := must[RecipeShowOut](t, f.a, "recipe.show", RecipeRefIn{Recipe: "app-teaser"}, false)
	bound := false
	for _, b := range show.Doc.Bindings {
		bound = bound || (b.Input == art.Key && b.To == "pictures:brand.HERO")
	}
	if !bound || show.Version.Check == nil || !show.Version.Check.OK {
		t.Fatalf("bound to the clip's picture: %+v / %+v", show.Doc.Bindings, show.Version.Check)
	}
	// Run 1 keeps the project's own art: nothing to draw again.
	p1 := must[RecipePlanOut](t, f.a, "recipe.plan", RecipePlanIn{Recipe: "app-teaser"}, false)
	for _, s := range p1.Steps {
		if s.ID == stepCode {
			t.Fatalf("nothing changes: %+v", p1.Steps)
		}
	}
	r1 := must[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: RecipePlanIn{Recipe: "app-teaser"}, Fingerprint: p1.Fingerprint}, true).Run
	f.finishReview(r1.ID)
	if done := f.advance(r1.ID); done.State != models.RunDone {
		t.Fatalf("run 1 %+v", done.Steps)
	}
	// Again with new art.
	in := RecipePlanIn{Repeats: "app-teaser#1", Inputs: []RecipeInputValue{{Name: art.Key, Items: []string{"villain"}}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	code := stepOf(&models.RecipeRun{Steps: plan.Steps}, stepCode)
	if !plan.Ready || code == nil || !strings.Contains(code.Detail, "the new pictures (no new code to approve)") || plan.Estimate.About != "nothing" {
		t.Fatalf("plan %+v", plan)
	}
	r2 := must[RecipeRunOut](t, f.a, "recipe.again", RecipeAgainIn{Run: "app-teaser#1", Inputs: in.Inputs, Fingerprint: plan.Fingerprint}, true).Run
	drawing := stepOf(&r2.RecipeRun, stepCode)
	if drawing.State != models.StepRunning || drawing.Work["brand"] == "" || len(drawing.Jobs) != 1 || !strings.Contains(drawing.Detail, "the new pictures") {
		t.Fatalf("drawing it: %+v", drawing)
	}
	var rr models.RemotionRender
	if err := f.a.DB.GetByID(f.ctx, store.CRemotionRenders, ws, ws+"/"+drawing.Work["brand"], &rr); err != nil {
		t.Fatal(err)
	}
	if rr.Version != hash || rr.Composition != "Open" || len(rr.OwnPictures) != 1 || rr.OwnPictures[0].Name != "HERO" ||
		rr.OwnPictures[0].SHA256 != f.villain.Latest().SHA256 || !strings.Contains(rr.Pictures, `"HERO"`) {
		t.Fatalf("render %+v", rr)
	}
	// No new version of the code, nothing to approve; the source's own pictures stay.
	var after models.RemotionSource
	_ = f.a.DB.GetByID(f.ctx, store.CRemotionSources, ws, src.ID, &after)
	if after.Pictures != nil || after.Head != hash || after.Versions != 1 {
		t.Fatalf("the source changed: %+v", after)
	}
	if n, _ := f.a.DB.C(store.CRemotionVersions).CountDocuments(f.ctx, bson.M{"sourceId": src.ID}); n != 1 {
		t.Fatalf("%d versions", n)
	}
	if n, _ := f.a.DB.C(store.CTodos).CountDocuments(f.ctx, bson.M{"workspaceId": ws, "kind": models.TodoRemotionApproval, "state": models.TodoOpen}); n != 0 {
		t.Fatalf("%d approvals asked for", n)
	}
	// A worker draws it (here: the clip it made is ready).
	if _, err := f.a.DB.C(store.CRemotionRenders).UpdateOne(f.ctx, bson.M{"_id": ws + "/" + drawing.Work["brand"]}, bson.M{"$set": bson.M{"state": models.RemotionRenderDone, "assetId": f.interview2, "duration": 5.0}}); err != nil {
		t.Fatal(err)
	}
	moved := f.advance(r2.ID)
	if stepOf(moved, stepCode).State != models.StepDone || moved.Made["brand"].AssetID != f.interview2 {
		t.Fatalf("made %+v", moved.Made)
	}
	rev := f.revision(moved.Revision)
	if c := rev.Spec.Clips[0]; c.ID != "brand" || c.AssetID != f.interview2 || !c.Locked || rev.Branch != r1.Branch {
		t.Fatalf("the fixed section shows the new clip, in the same cut: %+v", c)
	}
	f.finishReview(r2.ID)
	if done := f.advance(r2.ID); done.State != models.RunDone {
		t.Fatalf("done %+v", done.Steps)
	}
}

// Pictures of a version whose pictures aren't settings yet: the check says
// a person makes them settings once, and a run that would change them can't
// start.
func TestArtNeedsPictureSettings(t *testing.T) {
	f := setup(t)
	ws := f.a.WorkspaceID()
	now := time.Now().UTC()
	src := models.RemotionSource{ID: ids.New(ids.RemotionSource), WorkspaceID: ws, ProjectID: f.teaser, Name: "opener", Title: "Opener", Versions: 1, CreatedAt: now, UpdatedAt: now}
	hash := strings.Repeat("ef", 32)
	ver := models.RemotionVersion{ID: ws + "/" + hash, WorkspaceID: ws, SourceID: src.ID, ProjectID: f.teaser, Hash: hash, Number: 1, Kind: "import", State: models.RemotionApproved,
		PackagesAllowed: true, At: now, CreatedAt: now, UpdatedAt: now,
		Compositions: []models.RemotionComposition{{ID: "Open", Width: 640, Height: 360, FPS: 30, DurationInFrames: 150, Duration: 5, Pictures: []string{"HERO"}}},
		Pictures:     []models.RemotionPicture{{Name: "HERO", File: "public/hero.png", Setting: true}}}
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
	// A later version (approved) doesn't read its pictures as settings: the clip moved to it.
	hash2 := strings.Repeat("fe", 32)
	ver2 := ver
	ver2.ID, ver2.Hash, ver2.Number = ws+"/"+hash2, hash2, 2
	ver2.Pictures = []models.RemotionPicture{{Name: "HERO", File: "public/hero.png"}}
	if err := f.a.DB.Insert(f.ctx, store.CRemotionVersions, ver2); err != nil {
		t.Fatal(err)
	}
	if err := f.a.DB.Update(f.ctx, store.CAssets, ws, f.intro, bson.M{"$set": bson.M{"source.remotion.version": hash2, "source.remotion.versionNumber": 2}}); err != nil {
		t.Fatal(err)
	}
	chk := must[RecipeCheckOut](t, f.a, "recipe.check", RecipeCheckIn{Recipe: "app-teaser"}, false)
	found := false
	for _, is := range chk.Check.Problems {
		found = found || (is.Code == "pictures_not_settings" && strings.Contains(is.Message, "aren't settings yet") && strings.Contains(is.Message, "Sources → “Opener” → Pictures"))
	}
	if !found {
		t.Fatalf("check %+v", chk.Check)
	}
	in := RecipePlanIn{Recipe: "app-teaser", Inputs: []RecipeInputValue{{Name: key, Items: []string{"villain"}}}}
	plan := must[RecipePlanOut](t, f.a, "recipe.plan", in, false)
	if plan.Ready {
		t.Fatalf("plan %+v", plan)
	}
	if _, err := call[RecipeRunOut](t, f.a, "recipe.run", RecipeRunIn{RecipePlanIn: in, Fingerprint: plan.Fingerprint}, true); codeOf(err) != apperr.Validation {
		t.Fatalf("can't start: %v", err)
	}
}

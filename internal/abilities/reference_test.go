package abilities

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// An ability never takes a reference — someone else's video, kept to learn
// from — or a picture taken from one (D456, D457): planned with one, the plan
// isn't ready and says why in plain words; a run planned while the clip was
// the person's own and marked a reference before it ran stops before anything
// is fetched or sent, saying why.
func TestAnAbilityNeverTakesAReferenceOrAPictureTakenFromOne(t *testing.T) {
	a := newApp(t)
	needSandbox(t, a)
	ctx := context.Background()
	looks := strings.Replace(strings.Replace(cardManifest, "name: title-card", "name: look-at", 1),
		"  words: {kind: words, max: 80, says: The words on the card}",
		"  words: {kind: words, max: 80, says: The words on the card}\n  clip: {kind: footage, optional: true, says: A clip to look at}\n  still: {kind: picture, optional: true, says: A picture to look at}", 1)
	approved(t, a, map[string]string{ManifestFile: looks, "make.py": cardScript})
	now := time.Now().UTC()
	clip := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "their-hook.mp4", Title: "Their hook", State: models.AssetAvailable,
		SHA256: strings.Repeat("a", 64), StorageKey: "k-their-hook", Size: 1, ProjectIDs: []string{}, Media: models.MediaInfo{Duration: 6, HasVideo: true, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, clip); err != nil {
		t.Fatal(err)
	}
	sha := strings.Repeat("b", 64)
	still := models.Resource{ID: ids.New(ids.Resource), WorkspaceID: a.WorkspaceID(), Name: "hook-still", Title: "Hook still", Kind: models.ResourceImage, Scope: models.ScopeWorkspace,
		Current: sha, Versions: []models.ResourceVersion{{SHA256: sha, Size: 1, MIME: "image/jpeg", Ext: "jpg", StorageKey: "k-hook-still", Stored: true, CreatedAt: now,
			Source: models.ResourceSource{Kind: models.FromFile, Name: "00m01.jpg", FrameOf: &models.ResourceFrame{AssetID: clip.ID, Title: "Their hook", At: 1}, At: now}}},
		Tags: []string{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CResources, still); err != nil {
		t.Fatal(err)
	}
	plan := func(inputs map[string]any) *AbilityPlanOut {
		t.Helper()
		return mustCall[AbilityPlanOut](t, a, "ability.plan", AbilityPlanIn{Ref: "look-at", Inputs: inputs}, ops.Caller{})
	}
	// The person's own footage: ready, and a run is queued.
	inputs := map[string]any{"words": "x", "clip": clip.ID}
	p := plan(inputs)
	if !p.Ready {
		t.Fatalf("their own clip: %+v", p.Problems)
	}
	run := mustCall[AbilityRunOut](t, a, "ability.run", AbilityRunIn{AbilityPlanIn: AbilityPlanIn{Ref: "look-at", Inputs: inputs}, Plan: p.Fingerprint}, ops.Caller{})
	// Marked a reference before the run starts: it stops, saying why.
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": clip.ID}, bson.M{"$set": bson.M{"reference": models.AssetReference{By: a.Actor, At: now, Why: models.ReferenceMarked}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := runJob(t, a, run.JobID); err == nil || !IsFinal(err) {
		t.Fatalf("a run of a clip marked since: %v", err)
	}
	r := mustCall[AbilityRunsOut](t, a, "ability.runs", AbilityRunsIn{ID: run.ID}, ops.Caller{}).Run
	if r.State != models.AbilityRunFailed || !strings.Contains(r.Error, "“Their hook” is a reference: someone else's video, kept to learn from, so MetaRush doesn't hand it to an ability") {
		t.Fatalf("the run: %s %q", r.State, r.Error)
	}
	// Planned again: not ready, for the clip and for the picture taken from it.
	if p := plan(inputs); p.Ready || !strings.Contains(strings.Join(p.Problems, " "), "“Their hook” is a reference: someone else's video") {
		t.Fatalf("a plan with a reference: %+v", p.Problems)
	}
	if p := plan(map[string]any{"words": "x", "still": "hook-still"}); p.Ready || !strings.Contains(strings.Join(p.Problems, " "), "“Hook still” is a frame of “Their hook”, a reference") {
		t.Fatalf("a plan with a picture taken from a reference: %+v", p.Problems)
	}
	_, err := resolveFile(ctx, a, InPicture, "hook-still", "", nil)
	if ae := apperr.From(err); ae == nil || ae.Command() != "metarush footage reference "+clip.ID+" --mine" || !ops.RefusedAsReference(err) {
		t.Fatalf("the refusal carries the command: %v", err)
	}
}

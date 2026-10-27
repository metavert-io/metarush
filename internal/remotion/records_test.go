package remotion

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func newSource(t *testing.T, a *app.App, name string) *models.RemotionSource {
	t.Helper()
	now := time.Now().UTC()
	s := &models.RemotionSource{ID: ids.New(ids.RemotionSource), WorkspaceID: a.WorkspaceID(), ProjectID: "prj_" + strings.Repeat("0", 26), Name: name, Title: name,
		Origin:  models.RemotionOrigin{Kind: models.RemotionFromGitHub, URL: "https://github.com/acme/" + name, Repo: "acme/" + name, Commit: "5318d00366fe4d8a1c1d1e8f04b2a7f1aa2f6c11"},
		Allowed: []models.RemotionAllowed{{Lock: strings.Repeat("1", 64), Import: true}}, CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
	if err := a.DB.Insert(context.Background(), store.CRemotionSources, s); err != nil {
		t.Fatal(err)
	}
	return s
}

func version(t *testing.T, a *app.App, s *models.RemotionSource, hashChar, parent string) *models.RemotionVersion {
	t.Helper()
	v := &models.RemotionVersion{Hash: strings.Repeat(hashChar, 64), Parent: parent, Kind: models.RemotionKindEdit, By: a.Actor, At: time.Now().UTC(), Lock: strings.Repeat("1", 64),
		PackagesAllowed: true, Read: &models.RemotionRead{OK: true}, Compositions: []models.RemotionComposition{{ID: "Intro", Width: 1920, Height: 1080, FPS: 30, DurationInFrames: 60, Duration: 2}}}
	if parent == "" {
		v.Kind = models.RemotionKindImport
	}
	got, isNew, err := insertVersion(context.Background(), a, s, v)
	if err != nil || !isNew {
		t.Fatalf("insert: %v %v", err, isNew)
	}
	return got
}

func TestVersionsDecisionsAndReferences(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	s := newSource(t, a, "teaser")
	v1 := version(t, a, s, "a", "")
	v2 := version(t, a, s, "b", v1.Hash)
	if v1.Number != 1 || v2.Number != 2 || s.Head != v2.Hash || s.HeadNumber != 2 {
		t.Fatalf("numbers and head: %d %d %s", v1.Number, v2.Number, s.Head)
	}
	if again, isNew, err := insertVersion(ctx, a, s, &models.RemotionVersion{Hash: v1.Hash}); err != nil || isNew || again.Number != 1 {
		t.Fatal("the same files are the same version")
	}
	// References: a number, a prefix, the original commit for the import, the newest.
	for ref, want := range map[string]int{"1": 1, "v2": 2, "aaaaaaaa": 1, "bbbbbbb": 2, "5318d00": 1, "head": 2, "latest": 2} {
		v, err := resolveVersion(ctx, a, s, ref)
		if err != nil || v.Number != want {
			t.Errorf("%q → %v %v", ref, v, err)
		}
	}
	if _, err := resolveVersion(ctx, a, s, "approved"); apperr.From(err).Code != apperr.Validation {
		t.Errorf("nothing approved yet: %v", err)
	}
	// People decide, by compare-and-swap; the source points at the newest approved.
	if _, err := decide(ctx, a, s, v1, verbRevoke, a.Actor, ""); apperr.From(err).Code != apperr.Conflict {
		t.Error("nothing to withdraw")
	}
	got, err := decide(ctx, a, s, v1, verbApprove, a.Actor, "looked at it")
	if err != nil || got.State != models.RemotionApproved || s.Approved != v1.Hash || s.ApprovedNumber != 1 {
		t.Fatalf("approve: %v %+v", err, got)
	}
	if v, _ := resolveVersion(ctx, a, s, ""); v.Number != 1 {
		t.Error("no version given: the newest approved")
	}
	if _, err := decide(ctx, a, s, v1, verbApprove, a.Actor, ""); apperr.From(err).Code != apperr.Conflict {
		t.Error("a stale state loses the race")
	}
	v2.Check = &models.RemotionCheck{OK: false}
	if err := setVersion(ctx, a, v2.Hash, map[string]any{"check": v2.Check}); err != nil {
		t.Fatal(err)
	}
	if _, err := decide(ctx, a, s, v2, verbApprove, a.Actor, ""); apperr.From(err).Code != apperr.Validation {
		t.Error("a version that failed its check can't be approved")
	}
	v1, _ = getVersion(ctx, a, v1.Hash)
	if _, err := decide(ctx, a, s, v1, verbRevoke, a.Actor, "wrong colors"); err != nil {
		t.Fatal(err)
	}
	if s.Approved != "" {
		t.Error("no approved version left")
	}
	noPk := &models.RemotionVersion{Number: 3, Hash: strings.Repeat("c", 64), PackagesAllowed: false, Read: &models.RemotionRead{OK: true}, State: models.RemotionWaiting}
	if _, err := decide(ctx, a, s, noPk, verbApprove, a.Actor, ""); apperr.From(err).Code != apperr.Validation {
		t.Error("packages nobody allowed block approval")
	}
}

func TestRendersFromUnapprovedVersionsAreRefused(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	s := newSource(t, a, "film")
	v1 := version(t, a, s, "d", "")
	clip := func(kind, ver string) models.Asset {
		return models.Asset{ID: ids.New(ids.Asset), Source: &models.AssetSource{Platform: models.PlatformRemotion,
			Remotion: &models.RemotionClipSource{SourceID: s.ID, SourceName: s.Name, SourceTitle: s.Title, Version: ver, VersionNumber: 1, Kind: kind, Composition: "Intro"}}}
	}
	if err := RequireApproved(ctx, a, []models.Asset{clip(models.SourceRemotionExisting, v1.Hash)}); err != nil {
		t.Errorf("videos the project already had are ordinary footage: %v", err)
	}
	err := RequireApproved(ctx, a, []models.Asset{clip(models.SourceRemotionRender, v1.Hash)})
	if ae := apperr.From(err); ae.Code != apperr.Validation || !strings.Contains(ae.Message, "no person has approved it") {
		t.Fatalf("waiting: %v", err)
	}
	if _, err := decide(ctx, a, s, v1, verbApprove, a.Actor, ""); err != nil {
		t.Fatal(err)
	}
	if err := RequireApproved(ctx, a, []models.Asset{clip(models.SourceRemotionRender, v1.Hash)}); err != nil {
		t.Fatalf("approved: %v", err)
	}
	if err := RequireApproved(ctx, a, []models.Asset{clip(models.SourceRemotionRender, strings.Repeat("e", 64))}); apperr.From(err).Code != apperr.Validation {
		t.Error("a version that is gone is refused")
	}
	// The cut sheet finds a clip only once it was made, from an approved version.
	if r := lookupClip(ctx, a, s.ProjectID, s.Name, "Intro", "", ""); r.Code != "remotion_not_made" {
		t.Errorf("not made yet: %+v", r)
	}
	if r := lookupClip(ctx, a, s.ProjectID, s.Name, "Outro", "", ""); r.Code != "remotion_unknown" || !strings.Contains(r.Hint, "Intro") {
		t.Errorf("unknown composition: %+v", r)
	}
	if r := lookupClip(ctx, a, s.ProjectID, "nope", "Intro", "", ""); r.Code != "remotion_unknown" {
		t.Errorf("unknown source: %+v", r)
	}
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), State: models.AssetAvailable, Media: models.MediaInfo{Duration: 2.02}}
	if err := a.DB.Insert(ctx, store.CAssets, &as); err != nil {
		t.Fatal(err)
	}
	key := renderKey(s, v1, "Intro", "", nil, variant{})
	if err := a.DB.Insert(ctx, store.CRemotionRenders, &models.RemotionRender{ID: docID(a.WorkspaceID(), key), WorkspaceID: a.WorkspaceID(), Key: key, SourceID: s.ID,
		Version: v1.Hash, Composition: "Intro", State: models.RemotionRenderDone, AssetID: as.ID}); err != nil {
		t.Fatal(err)
	}
	if r := lookupClip(ctx, a, s.ProjectID, s.Name, "Intro", "1", ""); r.AssetID != as.ID || r.Duration != 2.02 || r.Name != "intro-v1" {
		t.Errorf("made: %+v", r)
	}
}

func TestUnchangedCompositionsKeepTheirClips(t *testing.T) {
	parent := strings.Repeat("a", 64)
	nv := &models.RemotionVersion{Parent: parent, Check: &models.RemotionCheck{OK: true, Affected: []string{"A", "B", "C"}, Compared: []string{"A", "B"}, Changed: []string{"A"}}}
	rc := func(comp, ver, props string) *models.RemotionClipSource {
		return &models.RemotionClipSource{Composition: comp, Version: ver, Props: props}
	}
	for _, c := range []struct {
		clip *models.RemotionClipSource
		keep bool
	}{
		{rc("A", parent, ""), false},                  // changed
		{rc("B", parent, ""), true},                   // compared, the same
		{rc("C", parent, ""), false},                  // could change, not compared
		{rc("D", parent, ""), true},                   // out of the change's reach
		{rc("B", strings.Repeat("9", 64), ""), false}, // from an older version than the parent
		{rc("D", parent, `{"x":1}`), false},           // its own props: render it again
	} {
		if got := unchangedIn(nv, c.clip); got != c.keep {
			t.Errorf("%+v: keep=%v", c.clip, got)
		}
	}
}

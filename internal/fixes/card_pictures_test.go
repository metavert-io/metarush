package fixes_test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/visual"
)

var someone = ops.Caller{Human: true}

func newProject(t *testing.T, a *app.App, name string) models.Project {
	t.Helper()
	now := time.Now().UTC()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: name, Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(context.Background(), store.CProjects, p); err != nil {
		t.Fatal(err)
	}
	return p
}

// savePNG writes a picture and returns its path.
func savePNG(t *testing.T, dir, name string, img image.Image) string {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// badge is a small logo on a plain light background with room around it.
func badge(c color.NRGBA) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 300, 200))
	for y := 0; y < 200; y++ {
		for x := 0; x < 300; x++ {
			p := color.NRGBA{250, 250, 250, 255}
			if x >= 100 && x < 200 && y >= 70 && y < 130 {
				p = c
			}
			img.SetNRGBA(x, y, p)
		}
	}
	return img
}

// photo has no plain background (a gradient with a shape).
func photo(seed uint8) *image.NRGBA {
	img := image.NewNRGBA(image.Rect(0, 0, 240, 160))
	for y := 0; y < 160; y++ {
		for x := 0; x < 240; x++ {
			img.SetNRGBA(x, y, color.NRGBA{uint8(x) + seed, uint8(y * 3 / 2), uint8((x + y) / 2), 255})
		}
	}
	return img
}

func addPicture(t *testing.T, a *app.App, projectID, name string, img image.Image) *models.Resource {
	t.Helper()
	r, err := resources.AddFile(context.Background(), a, savePNG(t, t.TempDir(), name, img), resources.AddOptions{ProjectID: projectID, FileName: name, Via: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	return r.Resource
}

func fixesView(t *testing.T, a *app.App, id string) *fixes.View {
	t.Helper()
	v, err := ops.CallTyped[fixes.View](context.Background(), a, "asset.fixes", fixes.ShowIn{ID: id}, someone)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func setFixes(t *testing.T, a *app.App, in fixes.SetIn) *fixes.View {
	t.Helper()
	v, err := ops.CallTyped[fixes.View](context.Background(), a, "asset.fixes.set", in, someone)
	if err != nil {
		t.Fatalf("set %+v: %v", in, err)
	}
	return v
}

func setErr(a *app.App, in fixes.SetIn) *apperr.Error {
	_, err := ops.CallTyped[fixes.View](context.Background(), a, "asset.fixes.set", in, someone)
	if err == nil {
		return nil
	}
	return apperr.From(err)
}

func runPrepare(t *testing.T, a *app.App, jobID string) map[string]any {
	t.Helper()
	j, err := a.Queue.Get(context.Background(), jobID)
	if err != nil {
		t.Fatal(err)
	}
	var msgs []string
	res, err := fixes.Prepare(context.Background(), a, j, func(_ float64, m string) { msgs = append(msgs, m) })
	if err != nil {
		t.Fatalf("prepare: %v", err)
	}
	if len(msgs) == 0 {
		t.Fatal("prepare says how it goes")
	}
	return res
}

func getResource(t *testing.T, a *app.App, id string) models.Resource {
	t.Helper()
	var r models.Resource
	if err := a.DB.GetByID(context.Background(), store.CResources, a.WorkspaceID(), id, &r); err != nil {
		t.Fatal(err)
	}
	return r
}

// cutPinning saves a cut (the project's latest version) that pins a picture.
func cutPinning(t *testing.T, a *app.App, p models.Project, name string, r *models.Resource, sha string) {
	t.Helper()
	ctx := context.Background()
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: p.ID, Branch: "main", Number: 1, CreatedAt: time.Now().UTC(),
		Spec: editspec.Spec{SchemaVersion: 1, Resources: map[string]editspec.ResourcePin{name: {ID: r.ID, SHA256: sha}}}}
	if err := a.DB.Insert(ctx, store.CRevisions, rev); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), p.ID, bson.M{"$set": bson.M{"branches.main": models.BranchHead{Head: rev.ID}}}); err != nil {
		t.Fatal(err)
	}
}

// A logo's fixes (D152): marked from the picture itself, made as a new
// version (the original kept) that new uses get; unticking makes the
// original current again, ticking again reuses the fixed version, and cuts
// keep what they pinned until someone updates them.
func TestPictureFixesMakeANewVersion(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := newProject(t, a, "Launch")
	logo := addPicture(t, a, p.ID, "badge.png", badge(color.NRGBA{200, 30, 40, 255}))
	original := logo.Current
	cutPinning(t, a, p, "badge", logo, original)

	v := fixesView(t, a, logo.ID)
	if !v.Picture || v.Kind != models.SourcePicture || len(v.Fixes) != 3 || v.Using != "original" || v.UsingWords != "New uses get the original." {
		t.Fatalf("view: %+v", v)
	}
	for _, r := range v.Fixes {
		if !r.Fits || r.Why == "" {
			t.Fatalf("every picture fix fits this logo: %+v", v.Fixes)
		}
	}
	if len(v.Cuts) != 1 || v.Cuts[0].Plays != "original" || v.Cuts[0].PlaysWords != "uses the version new uses get" {
		t.Fatalf("cuts: %+v", v.Cuts)
	}
	all := []string{models.FixBackground, models.FixEdges, models.FixLarger}
	v = setFixes(t, a, fixes.SetIn{ID: logo.ID, Tick: all})
	if v.State != models.FixPreparing || len(v.JobIDs) != 1 || !strings.HasPrefix(v.Summary, "Making the fixed copy: removing the background") {
		t.Fatalf("ticked: %+v", v)
	}
	res := runPrepare(t, a, v.JobIDs[0])
	fixed, _ := res["sha256"].(string)
	r := getResource(t, a, logo.ID)
	if r.Current != fixed || fixed == original || len(r.Versions) != 2 || r.Fixes == nil || r.Fixes.Copy == nil || r.Version(original) == nil {
		t.Fatalf("the fixed version is current, the original kept: %+v", r)
	}
	nv := r.Version(fixed)
	if nv.Source.Kind != models.FromGenerated || len(nv.Source.Lineage) != 1 || nv.Source.Lineage[0].SHA256 != original || !nv.HasAlpha {
		t.Fatalf("the new version says where it came from: %+v", nv.Source)
	}
	v = fixesView(t, a, logo.ID)
	if v.Using != "fixed" || v.Copy == nil || len(v.Copy.Measures) != 3 || !strings.HasPrefix(v.UsingWords, "New uses get the fixed version: ") {
		t.Fatalf("after: %+v", v)
	}
	words := map[string]string{}
	for _, m := range v.Copy.Measures {
		words[m.Fix] = m.Words
	}
	if words[models.FixBackground] != "see-through background: no → yes" || !strings.HasPrefix(words[models.FixEdges], "empty space on a side: ") ||
		!strings.HasPrefix(words[models.FixLarger], "size: 300 × 200 → ") {
		t.Fatalf("measured: %v", words)
	}
	if v.Cuts[0].Plays != "other" || !strings.HasPrefix(v.Cuts[0].Offer, "Use the fixed picture in “") {
		t.Fatalf("the cut keeps the original: %+v", v.Cuts)
	}

	// The info card shows the picture, its versions and the cut.
	card, err := ops.CallTyped[fixes.Card](ctx, a, "asset.card", fixes.ShowIn{ID: logo.ID}, someone)
	if err != nil {
		t.Fatal(err)
	}
	if !card.Picture || card.NamedWords != "Its name in cuts is “badge”." || card.FileName == "" || !slices.Contains(card.Found, "2 versions.") ||
		len(card.Uses) != 1 || card.Fixes == nil || !strings.HasSuffix(card.Summary, ": a picture or logo") {
		t.Fatalf("card: %+v", card)
	}

	// Unticked: the original is current again; ticked again: the fixed
	// version is reused at once.
	v = setFixes(t, a, fixes.SetIn{ID: logo.ID, Only: &[]string{}})
	if r := getResource(t, a, logo.ID); r.Current != original || v.Using != "original" || v.Cuts[0].Plays != "original" {
		t.Fatalf("back to the original: current %s, view %+v", r.Current, v)
	}
	v = setFixes(t, a, fixes.SetIn{ID: logo.ID, Tick: all})
	if r := getResource(t, a, logo.ID); r.Current != fixed || len(v.JobIDs) != 0 || v.State != models.FixReady {
		t.Fatalf("reused: current %s, view %+v", r.Current, v)
	}
	// Again: another take is made.
	if v = setFixes(t, a, fixes.SetIn{ID: logo.ID, Again: true}); v.State != models.FixPreparing || len(v.JobIDs) != 1 {
		t.Fatalf("again: %+v", v)
	}

	// Refusals: a fix pictures don't have, a drawing, a picture that isn't there.
	if e := setErr(a, fixes.SetIn{ID: logo.ID, Tick: []string{models.FixSteady}}); e == nil || e.Code != apperr.InvalidInput || !strings.Contains(e.Message, "background, edges and larger") {
		t.Fatalf("steady on a picture: %+v", e)
	}
	svg := filepath.Join(t.TempDir(), "mark.svg")
	if err := os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10" fill="#c00"/></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	drawing, err := resources.AddFile(ctx, a, svg, resources.AddOptions{ProjectID: p.ID, Via: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if e := setErr(a, fixes.SetIn{ID: drawing.Resource.ID, Tick: all}); e == nil || !strings.Contains(e.Message, "is a drawing (SVG)") {
		t.Fatalf("a drawing: %+v", e)
	}
	if e := setErr(a, fixes.SetIn{ID: "res_" + strings.TrimPrefix(ids.New("x"), "x_"), Tick: all}); e == nil || e.Code != apperr.NotFound {
		t.Fatalf("a missing picture: %+v", e)
	}
	look, err := ops.CallTyped[fixes.LookOut](ctx, a, "asset.fixes.look", fixes.ShowIn{ID: logo.ID}, someone)
	if err != nil || look.View == nil || !look.View.Picture || look.JobID != "" {
		t.Fatalf("look at a picture again: %+v %v", look, err)
	}
}

// A photo's background isn't one plain colour: on a Mac the subject is cut
// out (Apple Vision, a stand-in here); without it, or when no subject is
// found, the background is kept and the copy says why.
func TestPhotoBackgroundNeedsTheSubject(t *testing.T) {
	a := testapp.New(t)
	p := newProject(t, a, "Launch")
	prev := fixes.SegmentSubject
	t.Cleanup(func() { fixes.SegmentSubject = prev })
	notes := func(r *models.Resource) []string {
		t.Helper()
		v := setFixes(t, a, fixes.SetIn{ID: r.ID, Tick: []string{models.FixBackground}})
		runPrepare(t, a, v.JobIDs[0])
		v = fixesView(t, a, r.ID)
		if v.Copy == nil {
			t.Fatalf("no copy: %+v", v)
		}
		return v.Copy.Notes
	}
	fixes.SegmentSubject = nil
	if n := notes(addPicture(t, a, p.ID, "garden.png", photo(0))); len(n) != 1 || !strings.Contains(n[0], "needs a Mac") {
		t.Fatalf("no subject finder: %v", n)
	}
	fixes.SegmentSubject = func(context.Context, *app.App, string, string) error { return fixes.ErrNoSubject }
	if n := notes(addPicture(t, a, p.ID, "sky.png", photo(40))); len(n) != 1 || !strings.Contains(n[0], "no clear subject") {
		t.Fatalf("no subject: %v", n)
	}
	fixes.SegmentSubject = func(_ context.Context, _ *app.App, in, out string) error {
		cut := image.NewNRGBA(image.Rect(0, 0, 240, 160))
		for y := 40; y < 120; y++ {
			for x := 60; x < 180; x++ {
				cut.SetNRGBA(x, y, color.NRGBA{200, 90, 30, 255})
			}
		}
		var buf bytes.Buffer
		_ = png.Encode(&buf, cut)
		return os.WriteFile(out, buf.Bytes(), 0o644)
	}
	r := addPicture(t, a, p.ID, "porch.png", photo(80))
	if n := notes(r); len(n) != 0 {
		t.Fatalf("cut out: %v", n)
	}
	var c models.SourceCopy
	if err := a.DB.GetByID(context.Background(), store.CSourceCopies, a.WorkspaceID(), getResource(t, a, r.ID).Fixes.Copy.ID, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.FixSteps.Steps) != 1 || c.FixSteps.Steps[0].Engine != "subject mask" {
		t.Fatalf("steps %+v", c.FixSteps.Steps)
	}
}

// Prepare jobs whose clip, picture or copy is gone fail for good, saying so.
func TestPrepareWithoutItsSourceFailsForGood(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := newProject(t, a, "Launch")
	logo := addPicture(t, a, p.ID, "badge.png", badge(color.NRGBA{20, 90, 200, 255}))
	copyID := ops.CopyID(a.WorkspaceID(), "0123456789abcdef", "feedface")
	if err := a.DB.Insert(ctx, store.CSourceCopies, models.SourceCopy{ID: copyID, WorkspaceID: a.WorkspaceID(), ResourceID: logo.ID, SourceSHA256: strings.Repeat("0", 64),
		Fingerprint: "feedface", State: models.FixPreparing, CreatedAt: time.Now().UTC()}); err != nil {
		t.Fatal(err)
	}
	clip := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "audio", Name: "a.wav", SHA256: ids.New("sha"), State: models.AssetAvailable,
		ProjectIDs: []string{}, Variants: map[string]models.Variant{}, CreatedAt: time.Now().UTC()}
	if err := a.DB.Insert(ctx, store.CAssets, clip); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		payload map[string]any
		want    string
	}{
		{map[string]any{"resourceId": logo.ID, "copyId": "cpy_missing"}, "fixed copy cpy_missing not found"},
		{map[string]any{"resourceId": "res_missing", "copyId": copyID}, "picture res_missing not found"},
		{map[string]any{"resourceId": logo.ID, "copyId": copyID}, "the version of “badge” the fixes were for isn't there any more"},
		{map[string]any{"assetId": "ast_missing", "copyId": copyID}, "clip ast_missing not found"},
		{map[string]any{"assetId": clip.ID, "copyId": "cpy_missing"}, "fixed copy cpy_missing not found"},
	}
	for _, c := range cases {
		_, err := fixes.Prepare(ctx, a, &models.Job{ID: ids.New(ids.Job), Type: jobs.TypePrepare, Payload: c.payload}, func(float64, string) {})
		var fe *fixes.FinalError
		if err == nil || err.Error() != c.want {
			t.Errorf("%v: %v", c.payload, err)
		} else if !errors.As(err, &fe) {
			t.Errorf("%v: not final: %v", c.payload, err)
		}
	}
}

// Ticking fixes on a clip: what isn't possible is refused in plain words.
func TestClipFixRefusals(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clip := func(name, state string, video, audio bool) models.Asset {
		t.Helper()
		as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: name + ".mov", Title: name, SHA256: ids.New("sha"), State: state,
			Media: models.MediaInfo{HasVideo: video, HasAudio: audio, Duration: 10}, ProjectIDs: []string{}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
		if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
			t.Fatal(err)
		}
		return as
	}
	walk := clip("Walk", models.AssetAvailable, true, true)
	silent := clip("Silent walk", models.AssetAvailable, true, false)
	voice := clip("Voice", models.AssetAvailable, false, true)
	arriving := clip("Arriving", models.AssetStaging, true, true)
	cases := []struct {
		in   fixes.SetIn
		code string
		says string
	}{
		{fixes.SetIn{ID: ids.New(ids.Asset), Tick: []string{models.FixSteady}}, apperr.NotFound, "MetaRush can't find that clip."},
		{fixes.SetIn{ID: arriving.ID, Tick: []string{models.FixSteady}}, apperr.Conflict, "“Arriving” is still being stored; fix it once it's ready."},
		{fixes.SetIn{ID: walk.ID, Kind: models.SourceVoice}, apperr.InvalidInput, "“Walk” has a picture: it is a camera video or a call or screen recording."},
		{fixes.SetIn{ID: voice.ID, Kind: models.SourceCamera}, apperr.InvalidInput, "“Voice” is sound only: it is a voice recording or music."},
		{fixes.SetIn{ID: walk.ID, Tick: []string{"sparkle"}}, apperr.InvalidInput, "MetaRush has no fix called \"sparkle\"."},
		{fixes.SetIn{ID: walk.ID, Kind: models.SourceCamera, Tick: []string{models.FixEnds}}, apperr.InvalidInput, "Trim silence at the ends isn't offered for a camera video."},
		{fixes.SetIn{ID: silent.ID, Kind: models.SourceCall, Tick: []string{models.FixVoices}}, apperr.InvalidInput, "Clean up voices needs sound, and “Silent walk” has none."},
	}
	for _, c := range cases {
		e := setErr(a, c.in)
		if e == nil || e.Code != c.code || e.Message != c.says {
			t.Errorf("%+v: %+v", c.in, e)
		}
	}
	// A clip's view before its first look says when it is measured; asking
	// to look again queues a fresh measurement.
	v := fixesView(t, a, walk.ID)
	if v.Measuring == "" || v.Summary != v.Measuring || len(v.Kinds) != 2 {
		t.Fatalf("before measuring: %+v", v)
	}
	look, err := ops.CallTyped[fixes.LookOut](ctx, a, "asset.fixes.look", fixes.ShowIn{ID: walk.ID}, someone)
	if err != nil || look.JobID == "" {
		t.Fatalf("look again: %+v %v", look, err)
	}
	again, err := ops.CallTyped[fixes.LookOut](ctx, a, "asset.fixes.look", fixes.ShowIn{ID: walk.ID}, someone)
	if err != nil || again.JobID == look.JobID {
		t.Fatalf("each look again is a fresh one: %+v %v", again, err)
	}
	if _, err := ops.CallTyped[fixes.LookOut](ctx, a, "asset.fixes.look", fixes.ShowIn{ID: ids.New(ids.Asset)}, someone); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("look at a missing clip: %v", err)
	}
	if _, err := ops.CallTyped[fixes.View](ctx, a, "asset.fixes", fixes.ShowIn{ID: ids.New(ids.Asset)}, someone); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("view a missing clip: %v", err)
	}
}

// Once a clip's first look is done, MetaRush measures it for its fixes —
// not while its first analyses still run, not twice, and never sound MetaRush
// made itself.
func TestMeasuringStartsAfterTheFirstLook(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	clip := func(name string, tags ...string) models.Asset {
		t.Helper()
		as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "audio", Name: name, SHA256: ids.New("sha") + "0123456789", State: models.AssetAvailable,
			Media: models.MediaInfo{HasAudio: true, Duration: 5}, Tags: tags, ProjectIDs: []string{}, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
		if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
			t.Fatal(err)
		}
		return as
	}
	inspections := func(id string) int64 {
		n, _ := a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": id, "type": fixes.TypeInspect})
		return n
	}
	busy, ready, made := clip("busy.wav"), clip("ready.wav"), clip("line-1.mp3", "voiceover", "tts")
	if _, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeTranscribe, IdempotencyKey: "tx:" + busy.ID, AssetID: busy.ID, Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{busy.ID, ready.ID, made.ID} {
		ops.SourceFactsChanged(ctx, a, id, ops.FactsAnalysis)
	}
	ops.SourceFactsChanged(ctx, a, ready.ID, ops.FactsIngested) // not the first look's end
	if inspections(busy.ID) != 0 || inspections(ready.ID) != 1 || inspections(made.ID) != 0 {
		t.Fatalf("inspections: busy %d ready %d made %d", inspections(busy.ID), inspections(ready.ID), inspections(made.ID))
	}
	// Measured already for these bytes: not again.
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), ready.ID, bson.M{"$set": bson.M{"fixes.markedAt": now, "fixes.marksFor": ready.SHA256}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.C(store.CJobs).DeleteMany(ctx, bson.M{"assetId": ready.ID}); err != nil {
		t.Fatal(err)
	}
	ops.SourceFactsChanged(ctx, a, ready.ID, ops.FactsAnalysis)
	if inspections(ready.ID) != 0 {
		t.Fatal("measured twice")
	}
}

// The info card (D151): who named it, where it came from, what MetaRush
// found (length, sound, people, words on screen, what it's about, the fixes'
// measurements), where it's used and its fixes.
func TestInfoCard(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := newProject(t, a, "Garden film")
	lufs := -24.0
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "IMG_4410.MOV", Title: "Tomato planting", TitleBy: models.TitleByMetaRush,
		SHA256: ids.New("sha"), State: models.AssetAvailable, ProjectIDs: []string{p.ID}, Variants: map[string]models.Variant{}, Via: "dashboard",
		CreatedAt: now.Add(-time.Hour), UpdatedAt: now, CreatedBy: models.Actor{UserName: "Maya"},
		Aliases: []models.AssetAlias{{Name: "tomatoes-copy.mov"}},
		Source:  &models.AssetSource{Platform: models.PlatformYouTube, URL: "https://www.youtube.com/watch?v=dQw4w9WgXcQ", Title: "Planting day", Uploader: "Garden club", License: "CC BY 4.0"},
		Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 125, Width: 1920, Height: 1080, FPS: 30, CreationTime: "2026-05-02T10:30:00Z", Device: "iPhone 15", Software: "17.4",
			Streams: []models.Stream{{Type: "video"}, {Type: "audio", Channels: 2}}},
		Transcript: &models.TranscriptStatus{State: "available", Words: 120},
		Fixes:      &models.SourceFixes{Marks: []models.FixMark{{Fix: models.FixSteady, Measured: "shake: 0.10% of the picture's width a frame"}, {Fix: models.FixColour}}},
	}
	insert := func(coll string, doc any) {
		t.Helper()
		if err := a.DB.Insert(ctx, coll, doc); err != nil {
			t.Fatal(err)
		}
	}
	insert(store.CAssets, as)
	insert(store.CInsights, models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: "technical", CreatedAt: now,
		Data: map[string]any{"loudness": map[string]any{"integratedLufs": lufs, "measured": true}}})
	idx := &visual.Index{Summary: "Two people in a garden.", Text: []visual.TextSpan{{Text: "SPRING PLANTING", From: 0, To: 6, Conf: 0.9}}}
	insert(store.CInsights, models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind, CreatedAt: now, Data: visual.ToMap(idx)})
	for _, sp := range []models.AssetSpeaker{{Label: "speaker_00", Name: "Maya"}, {Label: "speaker_01"}, {Label: "speaker_02", Minor: true}} {
		sp.ID, sp.WorkspaceID, sp.AssetID, sp.Run = ids.New(ids.Speaker), a.WorkspaceID(), as.ID, "run1"
		insert(store.CSpeakers, sp)
	}
	insert(store.CFaces, models.FaceTrack{ID: ids.New(ids.Face), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Track: 1, Name: "Sam", CreatedAt: now, UpdatedAt: now})
	for i, text := range []string{"We planted tomatoes in the garden today.", "These tomatoes need lots of sun.", "Water the tomatoes every morning."} {
		insert(store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Index: i, Text: text, CreatedAt: now})
	}

	card, err := ops.CallTyped[fixes.Card](ctx, a, "asset.card", fixes.ShowIn{ID: as.ID}, someone)
	if err != nil {
		t.Fatal(err)
	}
	if card.Title != "Tomato planting" || card.FileName != "IMG_4410.MOV" || !slices.Equal(card.Aliases, []string{"tomatoes-copy.mov"}) ||
		card.NamedWords != "Named by MetaRush from its first look; rename it any time." || card.Link != as.Source.URL || card.Recorded == nil {
		t.Fatalf("card: %+v", card)
	}
	origin := strings.Join(card.Origin, "\n")
	for _, want := range []string{"in the dashboard by Maya.", "Imported ", ": “Planting day” (CC BY 4.0).", "on an iPhone 15 (17.4)."} {
		if !strings.Contains(origin, want) {
			t.Errorf("origin lacks %q:\n%s", want, origin)
		}
	}
	found := strings.Join(card.Found, "\n")
	for _, want := range []string{"2 min 05 s, 1920 × 1080 at 30 frames a second.", "Stereo sound, on the quiet side.", "Voices: Maya; 1 voice nobody named yet; 1 face seen.",
		"On screen: Two people in a garden.", "Words on screen: “SPRING PLANTING”.", "What it's about: ", "Transcript: 120 words.", "Shake: 0.10% of the picture's width a frame (steady the camera)."} {
		if !strings.Contains(found, want) {
			t.Errorf("found lacks %q:\n%s", want, found)
		}
	}
	if !slices.Equal(card.People, []string{"Maya", "Sam"}) || !slices.Equal(card.OnScreen, []string{"SPRING PLANTING"}) || card.Topic == "" ||
		len(card.Uses) == 0 || card.Fixes == nil || !strings.HasPrefix(card.Summary, "“Tomato planting”: A camera video") {
		t.Fatalf("card: people %v screen %v topic %q uses %+v summary %q", card.People, card.OnScreen, card.Topic, card.Uses, card.Summary)
	}

	// Named by a person; made with generative video; from a Remotion project;
	// a device but no recording time; a transcript still being made.
	gen := as
	gen.ID, gen.SHA256, gen.TitleBy, gen.Aliases, gen.Transcript = ids.New(ids.Asset), ids.New("sha"), models.TitleByPerson, nil, &models.TranscriptStatus{State: "running"}
	gen.Media.CreationTime, gen.Media.Software = "", ""
	gen.Source = &models.AssetSource{Platform: models.PlatformGenerated, Generated: &models.GeneratedSource{Provider: "gemini", ProviderTitle: "Google Gemini", Prompt: "a fox in snow"}}
	insert(store.CAssets, gen)
	card, err = ops.CallTyped[fixes.Card](ctx, a, "asset.card", fixes.ShowIn{ID: gen.ID}, someone)
	if err != nil {
		t.Fatal(err)
	}
	origin, found = strings.Join(card.Origin, "\n"), strings.Join(card.Found, "\n")
	if card.NamedWords != "Named by a person." || !strings.Contains(origin, ", from the words “a fox in snow”.") || !strings.Contains(origin, "Recorded on an iPhone 15.") ||
		!strings.Contains(found, "The transcript is being made.") || card.Recorded != nil {
		t.Fatalf("generated: %+v", card)
	}
	if _, err := ops.CallTyped[fixes.Card](ctx, a, "asset.card", fixes.ShowIn{ID: ids.New(ids.Asset)}, someone); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("a missing clip: %v", err)
	}
	if _, err := ops.CallTyped[fixes.Card](ctx, a, "asset.card", fixes.ShowIn{ID: "res_missing"}, someone); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("a missing picture: %v", err)
	}
}

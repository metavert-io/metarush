package remotion

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"hash/crc32"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// writePNG writes a w×h picture of one colour.
func writePNG(t *testing.T, dir, name string, w, h int, c color.Color) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, c)
		}
	}
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

// hugePNG writes a tiny PNG whose header says it is w×h (only the header is
// read before it is refused).
func hugePNG(t *testing.T, dir string, w, h int) string {
	t.Helper()
	p := writePNG(t, dir, "huge.png", 2, 2, color.Black)
	b, _ := os.ReadFile(p)
	binary.BigEndian.PutUint32(b[16:20], uint32(w))
	binary.BigEndian.PutUint32(b[20:24], uint32(h))
	binary.BigEndian.PutUint32(b[29:33], crc32.ChecksumIEEE(b[12:29]))
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// pictureVersion is a version of a source whose code draws HERO and LOGO
// (settings) and BACKDROP (not yet): Intro draws HERO and LOGO, Outro LOGO.
func pictureVersion(t *testing.T, a *app.App, s *models.RemotionSource, hashChar, parent string) *models.RemotionVersion {
	t.Helper()
	v := &models.RemotionVersion{Hash: strings.Repeat(hashChar, 64), Parent: parent, Kind: models.RemotionKindEdit, By: a.Actor, At: time.Now().UTC(), Lock: strings.Repeat("1", 64),
		PackagesAllowed: true, Read: &models.RemotionRead{OK: true}, PicturesHelper: "src/metarush-pictures.ts", PicturesRead: true,
		Compositions: []models.RemotionComposition{
			{ID: "Intro", Width: 1920, Height: 1080, FPS: 30, DurationInFrames: 60, Duration: 2, Pictures: []string{"HERO", "LOGO"}},
			{ID: "Outro", Width: 1920, Height: 1080, FPS: 30, DurationInFrames: 60, Duration: 2, Pictures: []string{"BACKDROP", "LOGO"}},
		},
		Pictures: []models.RemotionPicture{
			{Name: "BACKDROP", File: "public/backdrop.jpg", Uses: []models.RemotionPictureUse{{File: "src/Outro.tsx", Line: 4, How: models.RemotionPictureStatic}}},
			{Name: "HERO", File: "public/hero.png", Setting: true, Uses: []models.RemotionPictureUse{{File: "src/Intro.tsx", Line: 9, How: models.RemotionPictureStatic, Wrapped: true}}},
			{Name: "LOGO", File: "src/art/logo.svg", Setting: true, Uses: []models.RemotionPictureUse{{File: "src/theme.ts", Line: 2, How: models.RemotionPictureImport, Wrapped: true}}},
		}}
	if parent == "" {
		v.Kind = models.RemotionKindImport
	}
	got, isNew, err := insertVersion(context.Background(), a, s, v)
	if err != nil || !isNew {
		t.Fatalf("insert: %v %v", err, isNew)
	}
	return got
}

func value(name, sha, ext string) models.RemotionPictureValue {
	return models.RemotionPictureValue{Name: name, ResourceID: "res_" + name, Resource: strings.ToLower(name), Title: "New " + strings.ToLower(name), SHA256: sha, Ext: ext, Size: 10, Width: 100, Height: 100}
}

// What a render draws in place of the project's own pictures: the source's,
// the clip's own over them, only names that are settings its composition
// draws; it is part of the render key, and a render with none keeps its key.
func TestPicturesForRenderKeysAndProps(t *testing.T) {
	a := testapp.New(t)
	s := newSource(t, a, "teaser")
	v := pictureVersion(t, a, s, "a", "")
	hero, logo, back := value("HERO", strings.Repeat("1", 64), "png"), value("LOGO", strings.Repeat("2", 64), "svg"), value("BACKDROP", strings.Repeat("3", 64), "jpg")
	if c, vals, n := picturesFor(s, v, "Intro", nil); c != "" || vals != nil || n != 0 {
		t.Fatalf("nothing replaced: %q %v %d", c, vals, n)
	}
	plain := renderKey(s, v, "Intro", "", nil, variant{})
	s.Pictures = &models.RemotionPictures{Number: 2, Values: []models.RemotionPictureValue{hero, logo, back}}
	c, vals, n := picturesFor(s, v, "Intro", nil)
	if c != `{"HERO":"`+hero.Served()+`","LOGO":"`+logo.Served()+`"}` || len(vals) != 2 || n != 2 {
		t.Fatalf("Intro draws HERO and LOGO: %q %v %d", c, vals, n)
	}
	if c, _, _ := picturesFor(s, v, "Outro", nil); c != `{"LOGO":"`+logo.Served()+`"}` {
		t.Fatalf("Outro: LOGO only (BACKDROP isn't a setting yet): %q", c)
	}
	with := renderKey(s, v, "Intro", "", nil, variant{Pictures: c})
	if with == plain || renderKey(s, v, "Intro", "", nil, variant{}) != plain {
		t.Fatal("the pictures are part of the key; none keeps the old key")
	}
	// The clip's own pictures win for that clip; an own one without a picture keeps the code's.
	own := value("HERO", strings.Repeat("4", 64), "webp")
	c2, vals2, n2 := picturesFor(s, v, "Intro", []models.RemotionPictureValue{own})
	if !strings.Contains(c2, own.Served()) || strings.Contains(c2, hero.Served()) || n2 != 2 || len(vals2) != 2 {
		t.Fatalf("own over the source's: %q %d", c2, n2)
	}
	c3, _, n3 := picturesFor(s, v, "Intro", []models.RemotionPictureValue{{Name: "HERO"}, {Name: "LOGO"}})
	if c3 != "" || n3 != 0 {
		t.Fatalf("own: the code's own pictures: %q %d", c3, n3)
	}
	if kept := ownDrawn(v, "Outro", []models.RemotionPictureValue{own, logo}); len(kept) != 1 || kept[0].Name != "LOGO" {
		t.Fatalf("Outro doesn't draw HERO: %+v", kept)
	}
	// The props the helper reads: each picture's address on the render's own server.
	props := renderProps(`{"title":"Hi"}`, `{"END.tagline":"Bye"}`, vals)
	pics, _ := props["metarushPictures"].(map[string]any)
	if props["title"] != "Hi" || props["metarushWords"] == nil || pics["HERO"] != "/__metarush_pictures/"+hero.Served() || pics["LOGO"] != "/__metarush_pictures/"+logo.Served() {
		t.Fatalf("props: %v", props)
	}
	if p := renderProps("", "", nil); p != nil {
		t.Fatalf("nothing to pass: %v", p)
	}
}

func TestPictureShapesAndWords(t *testing.T) {
	old := &pictureInfo{Width: 1000, Height: 1000, Format: "png"}
	if ps := shapeWords("HERO", models.RemotionPictureValue{Width: 1200, Height: 1180, Ext: "png"}, old); len(ps) != 0 {
		t.Fatalf("about the same shape: %+v", ps)
	}
	ps := shapeWords("HERO", models.RemotionPictureValue{Width: 1600, Height: 900, Ext: "png"}, old)
	if len(ps) != 1 || ps[0].Code != "picture_shape" || !strings.Contains(ps[0].Message, "wider") {
		t.Fatalf("wider: %+v", ps)
	}
	ps = shapeWords("HERO", models.RemotionPictureValue{Width: 300, Height: 300, Ext: "jpg"}, old)
	if len(ps) != 1 || ps[0].Code != "picture_small" {
		t.Fatalf("smaller: %+v", ps)
	}
	if ps := shapeWords("LOGO", models.RemotionPictureValue{Width: 30, Height: 30, Ext: "svg"}, old); len(ps) != 0 {
		t.Fatalf("an SVG is never soft: %+v", ps)
	}
	nv := value("HERO", strings.Repeat("1", 64), "png")
	if got := pictureWords(&nv); got != "“New hero”" || pictureWords(nil) != "the code's own picture" {
		t.Fatalf("words: %q", got)
	}
}

// The pictures op lists names, files and what replaces them; setting checks
// the names, the version's approval and the picture (a picture of the
// project's, a file only on this computer), then queues the work.
func TestPicturesListAndSetChecks(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	s := newSource(t, a, "teaser")
	v1 := pictureVersion(t, a, s, "a", "")
	call := func(name string, in any, c ops.Caller) (any, error) {
		b, _ := json.Marshal(in)
		return ops.Call(ctx, a, name, b, c)
	}
	// Not approved yet: nothing can be replaced for cuts.
	_, err := call("remotion.pictures.set", RemotionPicturesSetIn{Source: "teaser", Set: []RemotionPictureSet{{Name: "HERO", Resource: "x"}}}, ops.Caller{})
	if ae := apperr.From(err); ae == nil || ae.Code != apperr.Validation || !strings.Contains(ae.Message, "isn't approved") {
		t.Fatalf("unapproved: %v", err)
	}
	approve(t, a, s, v1)
	if err := a.DB.Insert(ctx, store.CProjects, &models.Project{ID: s.ProjectID, WorkspaceID: a.WorkspaceID(), Name: "Teaser"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	added, err := resources.AddFile(ctx, a, writePNG(t, dir, "new-hero.png", 40, 40, color.RGBA{200, 30, 30, 255}), resources.AddOptions{ProjectID: s.ProjectID, Via: "cli"})
	if err != nil || added.Resource == nil {
		t.Fatalf("add: %v %+v", err, added)
	}
	font := models.Resource{ID: ids.New(ids.Resource), WorkspaceID: a.WorkspaceID(), Name: "brand-font", Title: "Brand font", Kind: models.ResourceFont, Scope: models.ScopeProject, ProjectID: s.ProjectID,
		Versions: []models.ResourceVersion{{SHA256: strings.Repeat("f", 64), Size: 10, Ext: "ttf", Stored: true}}, Current: strings.Repeat("f", 64)}
	if err := a.DB.Insert(ctx, store.CResources, &font); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		set  []RemotionPictureSet
		code string
		want string
	}{
		{[]RemotionPictureSet{{Name: "NOPE", Resource: "new-hero"}}, apperr.NotFound, "no picture setting called NOPE"},
		{[]RemotionPictureSet{{Name: "BACKDROP", Resource: "new-hero"}}, apperr.NotFound, "no picture setting called BACKDROP"},
		{[]RemotionPictureSet{{Name: "HERO", Resource: "new-hero"}, {Name: "HERO"}}, apperr.InvalidInput, "changed twice"},
		{[]RemotionPictureSet{{Name: "HERO", Resource: "brand-font"}}, apperr.InvalidInput, "is a font"},
		{[]RemotionPictureSet{{Name: "HERO", Resource: "nothing-like-it"}}, apperr.NotFound, "no resource called"},
	} {
		_, err := call("remotion.pictures.set", RemotionPicturesSetIn{Source: "teaser", Set: c.set}, ops.Caller{})
		if ae := apperr.From(err); ae == nil || ae.Code != c.code || !strings.Contains(ae.Message, c.want) {
			t.Errorf("%+v: %v", c.set, err)
		}
	}
	// A file is given only on the computer it is on.
	_, err = call("remotion.pictures.set", RemotionPicturesSetIn{Source: "teaser", Set: []RemotionPictureSet{{Name: "HERO", File: filepath.Join(dir, "new-hero.png")}}}, ops.Caller{Remote: true})
	if ae := apperr.From(err); ae == nil || !strings.Contains(ae.Message, "only be given on the computer it is on") {
		t.Fatalf("a file over the network: %v", err)
	}
	// A picture too large for a video is refused, plainly.
	if _, err := call("remotion.pictures.set", RemotionPicturesSetIn{Source: "teaser", Set: []RemotionPictureSet{{Name: "HERO", File: hugePNG(t, dir, 9000, 8000)}}}, ops.Caller{}); err == nil || !strings.Contains(err.Error(), "more than a video needs") {
		t.Fatalf("too many pixels: %v", err)
	}
	// A good one: the work is queued with the picture's value.
	out, err := call("remotion.pictures.set", RemotionPicturesSetIn{Source: "teaser", Set: []RemotionPictureSet{{Name: "HERO", Resource: "new-hero"}, {Name: "LOGO", File: writePNG(t, dir, "logo.png", 30, 30, color.White)}}, CheckOnly: true}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	j, err := a.Queue.Get(ctx, out.(*RemotionTaskOut).JobID)
	if err != nil || j.Payload["task"] != taskPictures || j.Payload["checkOnly"] != true {
		t.Fatalf("job: %v %+v", err, j)
	}
	var set []models.RemotionPictureValue
	_ = json.Unmarshal([]byte(j.Payload["set"].(string)), &set)
	if len(set) != 2 || set[0].Name != "HERO" || set[0].SHA256 != added.SHA256 || set[0].Ext != "png" || set[0].Width != 40 || set[1].Name != "LOGO" || set[1].Resource == "" {
		t.Fatalf("values: %+v", set)
	}
	// The list: settings with where they are drawn; what can still become one.
	lo, err := call("remotion.pictures", RemotionPicturesIn{Source: "teaser"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	l := lo.(*RemotionPicturesOut)
	if !l.HasSettings || len(l.Pictures) != 3 || l.More != 1 || !strings.Contains(l.Message, "2 pictures") || !strings.Contains(l.Message, "1 more picture can become a setting") {
		t.Fatalf("list: %+v", l)
	}
	if hero := l.Pictures[1]; hero.Name != "HERO" || hero.Changed || strings.Join(hero.Used, ",") != "Intro" || hero.OriginalURL != "/media/remotion/"+v1.Hash+"/original/HERO" {
		t.Fatalf("HERO: %+v", hero)
	}
	// Setup again wraps only what isn't a setting yet; asking for a setting is refused.
	if _, err := call("remotion.pictures.setup", RemotionPicturesSetupIn{Source: "teaser", Names: []string{"HERO"}}, ops.Caller{}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("HERO is a setting already: %v", err)
	}
	so, err := call("remotion.pictures.setup", RemotionPicturesSetupIn{Source: "teaser"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if j, _ := a.Queue.Get(ctx, so.(*RemotionTaskOut).JobID); j == nil || j.Payload["task"] != taskPicturesSetup {
		t.Fatalf("setup job: %+v", j)
	}
	// A version read by an older build has its pictures read once, when someone looks (never on Claude's channel).
	old := newSource(t, a, "older")
	ov := version(t, a, old, "d", "")
	ro, err := call("remotion.pictures", RemotionPicturesIn{Source: "older"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if r := ro.(*RemotionPicturesOut); !r.Reading || r.JobID == "" || !strings.Contains(r.Message, "is reading which pictures") {
		t.Fatalf("reading: %+v", r)
	}
	again, _ := call("remotion.pictures", RemotionPicturesIn{Source: "older"}, ops.Caller{})
	if again.(*RemotionPicturesOut).JobID != ro.(*RemotionPicturesOut).JobID {
		t.Fatal("one read per version")
	}
	if j, _ := a.Queue.Get(ctx, ro.(*RemotionPicturesOut).JobID); j == nil || j.Payload["task"] != taskPicturesRead || j.Payload["version"] != ov.Hash {
		t.Fatalf("the read: %+v", j)
	}
	claude := *a
	claude.Actor.Via = models.ViaAssistant
	co, err := picturesOp(ctx, &claude, &RemotionPicturesIn{Source: "older"})
	if err != nil || !co.Reading || co.JobID != "" {
		t.Fatalf("Claude's channel starts nothing: %v %+v", err, co)
	}

	// A clip's own pictures (given for this clip only) need settings too.
	plain := newSource(t, a, "plain")
	pv := version(t, a, plain, "c", "")
	approve(t, a, plain, pv)
	_, err = call("remotion.render", RemotionRenderIn{Source: "plain", Composition: "Intro", Pictures: []RemotionPictureSet{{Name: "HERO", Resource: "new-hero"}}}, ops.Caller{})
	if ae := apperr.From(err); ae == nil || !strings.Contains(ae.Message, "aren't settings yet") {
		t.Fatalf("own pictures without settings: %v", err)
	}
}

// A change of pictures is recorded (no new version of the code), and the
// latest cut's clip of a composition that draws one is made again with it;
// putting the code's own picture back is a change too.
func TestPicturesChangeMovesTheLatestCut(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	s := newSource(t, a, "teaser")
	v1 := pictureVersion(t, a, s, "a", "")
	approve(t, a, s, v1)
	var checked [][]models.RemotionPictureValue
	saved := checkPictures
	checkPictures = func(_ context.Context, _ *app.App, _ *models.RemotionSource, v *models.RemotionVersion, set []models.RemotionPictureValue, _, _ map[string]models.RemotionPictureValue, _ func(float64, string)) (*models.RemotionWordsCheck, error) {
		checked = append(checked, set)
		return &models.RemotionWordsCheck{OK: true, Version: v.Hash}, nil
	}
	t.Cleanup(func() { checkPictures = saved })
	// The project's latest cut plays a clip of Intro made from version 1 with the project's own pictures.
	p := models.Project{ID: s.ProjectID, WorkspaceID: a.WorkspaceID(), Name: "Teaser"}
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{s.ProjectID}, State: models.AssetAvailable, Media: models.MediaInfo{Duration: 2},
		Source: &models.AssetSource{Platform: models.PlatformRemotion, Remotion: &models.RemotionClipSource{SourceID: s.ID, SourceName: s.Name, Version: v1.Hash, VersionNumber: 1,
			Kind: models.SourceRemotionRender, Composition: "Intro", RenderKey: renderKey(s, v1, "Intro", "", nil, variant{})}}}
	rev := models.Revision{ID: ids.New(ids.Revision), WorkspaceID: a.WorkspaceID(), ProjectID: s.ProjectID, Branch: "main", Number: 1,
		Spec: editspec.Spec{SchemaVersion: editspec.SchemaVersion, Clips: []editspec.Clip{{ID: "intro", AssetID: as.ID, In: 0, Out: 2}}}}
	p.Branches = map[string]models.BranchHead{"main": {Head: rev.ID}}
	for coll, doc := range map[string]any{store.CProjects: &p, store.CAssets: &as, store.CRevisions: &rev} {
		if err := a.DB.Insert(ctx, coll, doc); err != nil {
			t.Fatal(err)
		}
	}
	dir := t.TempDir()
	added, err := resources.AddFile(ctx, a, writePNG(t, dir, "new-hero.png", 40, 40, color.RGBA{20, 120, 220, 255}), resources.AddOptions{ProjectID: s.ProjectID, Via: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	run := func(set []RemotionPictureSet, checkOnly bool) map[string]any {
		t.Helper()
		o, err := ops.CallTyped[RemotionTaskOut](ctx, a, "remotion.pictures.set", RemotionPicturesSetIn{Source: "teaser", Set: set, CheckOnly: checkOnly}, ops.Caller{})
		if err != nil {
			t.Fatal(err)
		}
		j, err := a.Queue.Get(ctx, o.JobID)
		if err != nil {
			t.Fatal(err)
		}
		fresh, _ := getSource(ctx, a, s.ID)
		res, err := runPicturesTask(ctx, a, fresh, v1, j, func(float64, string) {})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	// Only checked: nothing changes, the check is kept to show.
	res := run([]RemotionPictureSet{{Name: "HERO", Resource: added.Resource.Name}}, true)
	fresh, _ := getSource(ctx, a, s.ID)
	if fresh.Pictures != nil || fresh.PicturesPending == nil || fresh.PicturesPending.Number != 0 || res["checkOnly"] != true || len(checked) != 1 {
		t.Fatalf("checked only: %+v %+v", fresh.Pictures, res)
	}
	// Made: recorded as change 1, and the cut's Intro clip is made again with the new picture.
	res = run([]RemotionPictureSet{{Name: "HERO", Resource: added.Resource.Name}}, false)
	fresh, _ = getSource(ctx, a, s.ID)
	if fresh.Pictures == nil || fresh.Pictures.Number != 1 || len(fresh.Pictures.Values) != 1 || fresh.Pictures.Values[0].SHA256 != added.SHA256 || fresh.PicturesPending != nil {
		t.Fatalf("recorded: %+v", fresh.Pictures)
	}
	if msg := res["message"].(string); !strings.Contains(msg, "HERO: the code's own picture became “new-hero”") || !strings.Contains(msg, "again with its pictures as changed") {
		t.Fatalf("message: %s", msg)
	}
	pics, vals, n := picturesFor(fresh, v1, "Intro", nil)
	key := renderKey(fresh, v1, "Intro", "", nil, variant{Pictures: pics})
	r, err := getRender(ctx, a, key)
	if err != nil || r.State != models.RemotionRenderQueued || r.Pictures != pics || len(r.PictureValues) != 1 || r.PicturesNumber != 1 || n != 1 || len(vals) != 1 ||
		len(r.AddTo) != 1 || !r.AddTo[0].Update {
		t.Fatalf("the clip is made again with the new picture: %v %+v", err, r)
	}
	if vs, _ := versionsOf(ctx, a, s.ID); len(vs) != 1 {
		t.Fatalf("no new version of the code: %d", len(vs))
	}
	// Back to the code's own picture: change 2, nothing replaced any more.
	run([]RemotionPictureSet{{Name: "HERO"}}, false)
	fresh, _ = getSource(ctx, a, s.ID)
	if fresh.Pictures.Number != 2 || len(fresh.Pictures.Values) != 0 || len(fresh.Pictures.Changes) != 2 {
		t.Fatalf("back to the code's: %+v", fresh.Pictures)
	}
	var audits []models.AuditEvent
	_ = a.DB.Find(ctx, store.CAudit, a.WorkspaceID(), bson.M{"action": "remotion.pictures.change"}, &audits)
	if len(audits) != 2 {
		t.Fatalf("audited: %d", len(audits))
	}
}

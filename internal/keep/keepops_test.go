package keep_test

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/improve"
	_ "github.com/metavert-io/metarush/internal/intake" // add.links
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func call[T any](t *testing.T, a *app.App, op string, in any) *T {
	t.Helper()
	out, err := ops.CallTyped[T](context.Background(), a, op, in, ops.Caller{})
	if err != nil {
		t.Fatalf("%s: %v", op, err)
	}
	return out
}

// storedPNG puts a small picture in the media store, as assistant.attach does.
func storedPNG(t *testing.T, a *app.App, name string) models.KeepFile {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for x := 0; x < 64; x++ {
		for y := 0; y < 64; y++ {
			img.Set(x, y, color.RGBA{R: 0x1d, G: 0x4e, B: 0x89, A: 0xff})
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	sha, err := media.HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.Media.PutFile(context.Background(), media.ObjectKey(a.WorkspaceID(), sha), p, media.PutOptions{ContentType: "image/png", SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	return models.KeepFile{Name: name, SHA256: sha, Size: int64(buf.Len()), MIME: "image/png", Width: 64, Height: 64}
}

// keep.add keeps every kind of thing through the ordinary ops (D166), records
// it, and says so; the same thing again changes nothing.
func TestKeepEveryKind(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Awareness"})
	pid := p.Project.ID
	now := time.Now().UTC()
	lena := models.Person{ID: ids.New(ids.Person), WorkspaceID: a.WorkspaceID(), Name: "Lena Okoye", NameKey: "lena okoye", CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CPeople, lena); err != nil {
		t.Fatal(err)
	}
	add := func(in ops.KeepAddIn) *ops.KeepAddOut {
		t.Helper()
		in.ProjectID = orID(in.ProjectID, pid)
		return call[ops.KeepAddOut](t, a, "keep.add", in)
	}

	// Where it goes carries its shape.
	b := add(ops.KeepAddIn{Kind: models.KeepBrief, Detail: models.KeepDetail{Audience: "Members, neighbours and the public",
		Deliverables: []models.Deliverable{{Name: "by email"}, {Name: "on the website"}, {Name: "social shorts"}}}})
	if b.Item.State != models.KeepKept || !strings.Contains(b.Summary, "brief") {
		t.Fatalf("brief: %+v", b)
	}
	shown := call[ops.ProjectShowOut](t, a, "project.show", ops.ProjectShowIn{ID: pid})
	ds := shown.Project.Brief.Deliverables
	if len(ds) != 3 || ds[0].Preset != "landscape-1080p" || ds[2].Preset != "vertical-1080p" || ds[2].Platform != "shorts" {
		t.Fatalf("deliverables: %+v", ds)
	}
	if shown.Project.Brief.Audience != "Members, neighbours and the public" || !contains(shown.Project.Brief.Platforms, "shorts") {
		t.Fatalf("brief: %+v", shown.Project.Brief)
	}

	// A rule, measured from its words; a check; the same words again are the same rule.
	add(ops.KeepAddIn{Kind: models.KeepRule, Text: "Captions at the top"})
	add(ops.KeepAddIn{Kind: models.KeepCheck, Text: "No split-second flashes of the wrong person at clip edges"})
	again := add(ops.KeepAddIn{Kind: models.KeepRule, Text: "captions at the top"})
	if !again.Already {
		t.Fatalf("the same rule twice: %+v", again)
	}
	rules := call[ops.RulesOut](t, a, "project.rules.list", ops.RulesListIn{ProjectID: pid})
	if len(rules.Rules) != 2 || rules.Rules[0].Measure != "captions-top" || !rules.Rules[1].Check || rules.Rules[1].Measure != "no-flashes" {
		t.Fatalf("rules: %+v", rules.Rules)
	}
	// "Every project using these settings": a house rule of the settings it uses (D193), not the project's.
	house := add(ops.KeepAddIn{Kind: models.KeepRule, Text: "The name bar never animates", Where: "settings"})
	if !strings.Contains(house.Summary, "house rule") {
		t.Fatalf("house rule: %+v", house)
	}
	hr := call[ops.HouseRulesOut](t, a, "content.rules.list", ops.HouseRulesIn{ProjectID: pid})
	if len(hr.Rules) != 1 || hr.Rules[0].Text != "The name bar never animates" || hr.ProjectID != pid {
		t.Fatalf("house rules: %+v", hr)
	}
	if again := call[ops.RulesOut](t, a, "project.rules.list", ops.RulesListIn{ProjectID: pid}); len(again.Rules) != 2 {
		t.Fatalf("the project's own rules didn't change: %+v", again.Rules)
	}

	// A fact about a person goes into their notes.
	add(ops.KeepAddIn{Kind: models.KeepPerson, Text: "Volunteers at FernBrook; runs the garden club", Detail: models.KeepDetail{Person: "Lena"}})
	var pr models.Person
	if err := a.DB.GetByID(ctx, store.CPeople, a.WorkspaceID(), lena.ID, &pr); err != nil || !strings.Contains(pr.Notes, "runs the garden club") {
		t.Fatalf("notes: %q %v", pr.Notes, err)
	}

	// A sign-off, an idea for MetaRush, a spelling.
	add(ops.KeepAddIn{Kind: models.KeepSignoff, Text: "Lena OKs the final version"})
	so := call[ops.SignoffsOut](t, a, "cut.signoffs.list", ops.SignoffsIn{ProjectID: pid})
	if so.Open != 1 || so.Signoffs[0].Text != "Lena OKs the final version" {
		t.Fatalf("sign-offs: %+v", so)
	}
	idea := add(ops.KeepAddIn{Kind: models.KeepImprove, Text: "Steady shaky footage", Detail: models.KeepDetail{Why: "The phone footage of the walk shakes."}})
	if idea.Item.ProjectID != "" {
		t.Fatalf("ideas are the workspace's: %+v", idea.Item)
	}
	ideas := call[improve.ListOut](t, a, "improve.list", improve.ListIn{})
	if len(ideas.Items) != 1 || ideas.Items[0].Title != "Steady shaky footage" {
		t.Fatalf("improvement list: %+v", ideas.Items)
	}
	add(ops.KeepAddIn{Kind: models.KeepSpelling, Text: "FernBrook", Detail: models.KeepDetail{HeardAs: []string{"Fern Brook"}}})
	sp := call[ops.SpellingListOut](t, a, "transcript.spelling.list", ops.SpellingListIn{ProjectID: pid})
	if len(sp.Entries) != 1 || sp.Entries[0].Word != "FernBrook" {
		t.Fatalf("spelling list: %+v", sp.Entries)
	}

	// A picture the person attached goes into the sources.
	f := storedPNG(t, a, "fernbrook-photo.png")
	pic := add(ops.KeepAddIn{Kind: models.KeepPicture, Text: f.Name, Detail: models.KeepDetail{Attachment: f.SHA256, File: &f}})
	if !strings.Contains(pic.Summary, "sources") {
		t.Fatalf("picture: %+v", pic)
	}
	res := call[ops.ResourceListOut](t, a, "resources.list", ops.ResourceListIn{ProjectID: pid})
	found := false
	for _, r := range res.Resources {
		if r.Kind == models.ResourceImage {
			found = true
		}
	}
	if !found {
		t.Fatalf("the picture should be a resource of the project: %+v", res.Resources)
	}

	// A website: brought in by the ordinary link intake (a snapshot job).
	src := add(ops.KeepAddIn{Kind: models.KeepSource, Text: "https://www.fernbrook.example/about", Detail: models.KeepDetail{URL: "https://www.fernbrook.example/about"}})
	if src.Item.State != models.KeepKept || src.Summary == "" {
		t.Fatalf("source: %+v", src)
	}

	list := call[ops.KeepListOut](t, a, "keep.list", ops.KeepListIn{ProjectID: pid})
	kept := 0
	for _, it := range list.Items {
		if it.State == models.KeepKept {
			kept++
		}
	}
	if kept != 8 { // brief, rule, house rule, check, signoff, spelling, picture, source (person and idea are the workspace's)
		t.Fatalf("kept in the project: %d of %+v", kept, list.Items)
	}

	// Declined is never offered again, but a person can still keep it.
	d := call[ops.KeepAddOut](t, a, "keep.decline", ops.KeepDeclineIn{ProjectID: pid, Kind: models.KeepRule, Text: "Nothing too long for an email"})
	if d.Item.State != models.KeepDeclined {
		t.Fatalf("declined: %+v", d)
	}
	k := add(ops.KeepAddIn{Kind: models.KeepRule, Text: "Nothing too long for an email"})
	if k.Item.State != models.KeepKept || k.Item.ID != d.Item.ID {
		t.Fatalf("kept after all: %+v", k.Item)
	}
}

// A logo kept for the brand kit (D165, D166): it is kept in the sources, the
// project's own kit takes it with colours from the logo, and the latest cut
// takes them as a new version (the card was the person's OK).
func TestKeepLogoForTheBrandKit(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Charity"})
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "talk.mov",
		SHA256: strings.Repeat("ab", 32), Size: 1000, MIME: "video/quicktime", StorageKey: "k", State: models.AssetAvailable,
		Media:    models.MediaInfo{Container: "mov", Duration: 30, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	plan := json.RawMessage(`{"projectId":"` + p.Project.ID + `","spec":{"schemaVersion":1,"output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"a","assetId":"` + as.ID + `","in":1,"out":6}]}}`)
	chk := call[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: plan})
	first := call[ops.EditApplyOut](t, a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken})

	f := storedPNG(t, a, "charity-logo.png")
	out := call[ops.KeepAddOut](t, a, "keep.add", ops.KeepAddIn{ProjectID: p.Project.ID, Kind: models.KeepPicture, Text: f.Name,
		Detail: models.KeepDetail{Attachment: f.SHA256, File: &f, BrandKit: true}})
	if out.RevisionID == "" || out.RevisionID == first.Revision.ID || !strings.Contains(out.Summary, "brand's logo") {
		t.Fatalf("kept for the brand and put on the cut: %+v", out)
	}
	kit := call[ops.BrandShowOut](t, a, "brand.show", ops.BrandShowIn{ProjectID: p.Project.ID})
	if kit.ReadOnly || kit.From == nil || kit.From.ID != p.Project.ID || kit.Logo == nil || kit.Logo.Kind != models.ResourceImage || kit.Brand.Accent == "" {
		t.Fatalf("the project's own settings: %+v", kit)
	}
	rev := call[ops.RevisionShowOut](t, a, "revision.show", ops.RevisionShowIn{ID: out.RevisionID})
	if rev.Spec.Brand == nil || rev.Spec.Brand.Accent != kit.Brand.Accent {
		t.Fatalf("the latest cut has the kit's colours: %+v", rev.Spec.Brand)
	}
}

func orID(s, d string) string {
	if s == "" {
		return d
	}
	return s
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x {
			return true
		}
	}
	return false
}

// A promise made to the client is kept as a measured rule marked promise
// (D301), for the project or every project using its settings; one MetaRush
// can't measure is refused with the way to keep it as a sign-off.
func TestKeepPromise(t *testing.T) {
	a := testapp.New(t)
	p := call[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Client spots"})
	pid := p.Project.ID
	out := call[ops.KeepAddOut](t, a, "keep.add", ops.KeepAddIn{ProjectID: pid, Kind: models.KeepPromise, Text: "Under 60 seconds for shorts"})
	if out.Item.State != models.KeepKept || !strings.Contains(out.Summary, "promise") {
		t.Fatalf("kept: %+v", out)
	}
	rules := call[ops.RulesOut](t, a, "project.rules.list", ops.RulesListIn{ProjectID: pid})
	if len(rules.Rules) != 1 || !rules.Rules[0].Promise || rules.Rules[0].Measure != "max-length:60@vertical" {
		t.Fatalf("the promise: %+v", rules.Rules)
	}
	call[ops.KeepAddOut](t, a, "keep.add", ops.KeepAddIn{ProjectID: pid, Kind: models.KeepPromise, Text: "Logo in the last 3 s", Where: "settings"})
	hr := call[ops.HouseRulesOut](t, a, "content.rules.list", ops.HouseRulesIn{ProjectID: pid})
	if len(hr.Rules) != 1 || !hr.Rules[0].Promise || hr.Rules[0].Measure != "logo-end:3" {
		t.Fatalf("the house promise: %+v", hr.Rules)
	}
	b, _ := json.Marshal(ops.KeepAddIn{ProjectID: pid, Kind: models.KeepPromise, Text: "Warm and friendly"})
	if _, err := ops.Call(context.Background(), a, "keep.add", b, ops.Caller{}); err == nil || !strings.Contains(apperr.From(err).Hint, "sign-off") {
		t.Fatalf("an unmeasurable promise: %v", err)
	}
	// A rule with the same words becomes a promise.
	call[ops.RuleAddOut](t, a, "project.rules.add", ops.RuleAddIn{ProjectID: pid, Text: "No faces cut off"})
	up := call[ops.RuleAddOut](t, a, "project.rules.add", ops.RuleAddIn{ProjectID: pid, Text: "no faces cut off", Promise: true})
	if !up.Created || !up.Rule.Promise || up.Rule.Measure != "faces-in-frame" || len(up.Rules) != 2 {
		t.Fatalf("a rule became a promise: %+v", up)
	}
}

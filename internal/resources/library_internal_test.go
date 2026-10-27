package resources

import (
	"context"
	"image"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func project(t *testing.T, a *app.App, name string) models.Project {
	t.Helper()
	now := time.Now().UTC()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: name, Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(context.Background(), store.CProjects, p); err != nil {
		t.Fatal(err)
	}
	return p
}

// add adds a picture file under a name, to a project ("" the workspace library).
func add(t *testing.T, a *app.App, projectID, name string, img image.Image, o AddOptions) *models.Resource {
	t.Helper()
	o.ProjectID, o.FileName, o.Via = projectID, name, "cli"
	r, err := AddFile(context.Background(), a, writePNG(t, t.TempDir(), name, img), o)
	if err != nil {
		t.Fatal(err)
	}
	return r.Resource
}

func reload(t *testing.T, a *app.App, r *models.Resource) *models.Resource {
	t.Helper()
	var got models.Resource
	if err := a.DB.GetByID(context.Background(), store.CResources, a.WorkspaceID(), r.ID, &got); err != nil {
		t.Fatal(err)
	}
	return &got
}

func names(es []Entry) []string {
	var out []string
	for _, e := range es {
		out = append(out, e.Origin+":"+e.Resource.Name)
	}
	return out
}

// What a project can use: its own resources first, then what a linked
// project brings, then the workspace library; filters narrow it; a name is
// found in that order.
func TestVisibleAndFind(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	film, brand := project(t, a, "Film"), project(t, a, "Brand")
	own := add(t, a, film.ID, "title-card.png", testPhoto(64, 36), AddOptions{Tags: []string{"titles"}})
	add(t, a, film.ID, "logo.png", testLogo(80, 40), AddOptions{Description: "The film's own mark"})
	theirs := add(t, a, brand.ID, "brand-logo.png", testLogo(90, 40), AddOptions{})
	add(t, a, "", "logo.png", testLogo(70, 30), AddOptions{})
	// Brand isn't linked yet: Film doesn't see it.
	view, err := projectlinks.Load(ctx, a, film.ID)
	if err != nil {
		t.Fatal(err)
	}
	all, err := Visible(ctx, a, film.ID, view, ListOptions{})
	if err != nil || !slices.Equal(names(all), []string{"project:logo", "project:title-card", "workspace:logo"}) {
		t.Fatalf("before linking: %v %v", names(all), err)
	}
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CProjectLinks, models.ProjectLink{ID: ids.New(ids.ProjectLink), WorkspaceID: a.WorkspaceID(), ProjectID: film.ID, SourceID: brand.ID,
		Whole: true, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if view, err = projectlinks.Load(ctx, a, film.ID); err != nil {
		t.Fatal(err)
	}
	all, _ = Visible(ctx, a, film.ID, view, ListOptions{})
	if !slices.Equal(names(all), []string{"project:logo", "project:title-card", "linked:brand-logo", "workspace:logo"}) || all[2].SourceID != brand.ID {
		t.Fatalf("linked: %v", names(all))
	}
	for _, c := range []struct {
		o    ListOptions
		want []string
	}{
		{ListOptions{Only: "project"}, []string{"project:logo", "project:title-card"}},
		{ListOptions{Only: FromWorkspace}, []string{"workspace:logo"}},
		{ListOptions{Only: FromLinked}, []string{"linked:brand-logo"}},
		{ListOptions{Tag: "Titles"}, []string{"project:title-card"}},
		{ListOptions{Query: "film's mark"}, []string{"project:logo"}},
		{ListOptions{Query: "nothing like this"}, nil},
		{ListOptions{Kind: models.ResourceSVG}, nil},
	} {
		got, err := Visible(ctx, a, film.ID, view, c.o)
		if err != nil || !slices.Equal(names(got), c.want) {
			t.Errorf("%+v: %v %v", c.o, names(got), err)
		}
	}
	ws, _ := Visible(ctx, a, "", nil, ListOptions{})
	if !slices.Equal(names(ws), []string{"workspace:logo"}) {
		t.Fatalf("the workspace library: %v", names(ws))
	}

	// Find: the project's own first, by name or title; an ID from anywhere.
	if e, err := Find(ctx, a, film.ID, view, "logo"); err != nil || e.Origin != FromHere {
		t.Fatalf("own first: %+v %v", e, err)
	}
	if e, err := Find(ctx, a, film.ID, view, "Brand-logo"); err != nil || e.Origin != FromLinked || e.Resource.ID != theirs.ID {
		t.Fatalf("by title: %+v %v", e, err)
	}
	if e, err := Find(ctx, a, film.ID, view, own.ID); err != nil || e.Origin != FromHere {
		t.Fatalf("by ID: %+v %v", e, err)
	}
	if e, err := Find(ctx, a, film.ID, nil, theirs.ID); err != nil || e.Origin != FromLinked || e.SourceID != brand.ID {
		t.Fatalf("another project's by ID: %+v %v", e, err)
	}
	for _, c := range []struct {
		project, ref, says string
		code               string
	}{
		{film.ID, "  ", "Say which resource: its short name or its title.", apperr.InvalidInput},
		{film.ID, ids.New(ids.Resource), "That resource was not found. It may have been removed.", apperr.NotFound},
		{film.ID, "poster", `There's no resource called "poster" here.`, apperr.NotFound},
		{"", "poster", `There's no resource called "poster" in the workspace library.`, apperr.NotFound},
	} {
		_, err := Find(ctx, a, c.project, view, c.ref)
		if e := apperr.From(err); e.Code != c.code || e.Message != c.says {
			t.Errorf("%q: %+v", c.ref, e)
		}
	}
}

// Words people edit: title, short name, tags (removing a suggestion is a
// person saying "not this"), description and licence, each checked.
func TestUpdatePromoteAndRemove(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	film := project(t, a, "Film")
	r := add(t, a, film.ID, "acme-logo.png", testLogo(80, 40), AddOptions{Tags: []string{"brand"}})
	if !slices.ContainsFunc(r.AutoTags, func(t models.ResourceTag) bool { return t.Tag == "logo" }) {
		t.Fatalf("suggested tags: %+v", r.AutoTags)
	}
	str := func(s string) *string { return &s }
	if err := Update(ctx, a, r, UpdateFields{Title: str("  Acme   mark "), Name: str("acme-mark"), AddTags: []string{"Hero", "brand"}, RemoveTags: []string{"logo"},
		Description: str(" Use on dark backgrounds. "), License: str(" Ours ")}); err != nil {
		t.Fatal(err)
	}
	got := reload(t, a, r)
	if got.Title != "Acme mark" || got.Name != "acme-mark" || !slices.Equal(got.Tags, []string{"brand", "hero"}) || got.Description != "Use on dark backgrounds." || got.License != "Ours" ||
		slices.ContainsFunc(got.AutoTags, func(t models.ResourceTag) bool { return t.Tag == "logo" }) {
		t.Fatalf("updated: %+v", got)
	}
	if err := Update(ctx, a, r, UpdateFields{Tags: &[]string{}}); err != nil || len(reload(t, a, r).Tags) != 0 {
		t.Fatalf("tags cleared: %v", err)
	}
	other := add(t, a, film.ID, "other.png", testPhoto(40, 30), AddOptions{})
	for _, c := range []struct {
		f    UpdateFields
		code string
	}{
		{UpdateFields{Title: str("   ")}, apperr.InvalidInput},
		{UpdateFields{Title: str(strings.Repeat("x", 201))}, apperr.InvalidInput},
		{UpdateFields{Name: str("Not A Name")}, apperr.InvalidInput},
		{UpdateFields{Description: str(strings.Repeat("d", 2001))}, apperr.InvalidInput},
		{UpdateFields{License: str(strings.Repeat("l", 501))}, apperr.InvalidInput},
		{UpdateFields{Name: str("acme-mark")}, apperr.Conflict},
	} {
		if err := Update(ctx, a, other, c.f); apperr.From(err).Code != c.code {
			t.Errorf("%+v: %v", c.f, err)
		}
	}

	// Into the workspace library: a name it already has gets a number.
	add(t, a, "", "acme-mark.png", testLogo(60, 30), AddOptions{Name: "acme-mark"})
	if err := Promote(ctx, a, r); err != nil || r.Scope != models.ScopeWorkspace || r.Name != "acme-mark-2" || r.PromotedFrom != film.ID || r.ProjectID != "" {
		t.Fatalf("promoted: %+v %v", r, err)
	}
	if err := Promote(ctx, a, r); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("again: %v", err)
	}

	// Removed: hidden, its name freed; restored: back under its name, or a
	// numbered one when the name was taken meanwhile.
	if err := Remove(ctx, a, other, false); err != nil || !other.Removed || !strings.HasPrefix(other.Name, "other-removed-") {
		t.Fatalf("removed: %+v %v", other, err)
	}
	if err := Remove(ctx, a, other, false); err != nil {
		t.Fatalf("removing twice is fine: %v", err)
	}
	if list, _ := Visible(ctx, a, film.ID, nil, ListOptions{}); slices.ContainsFunc(list, func(e Entry) bool { return e.Resource.ID == other.ID }) {
		t.Fatal("a removed resource isn't listed")
	}
	if list, _ := Visible(ctx, a, film.ID, nil, ListOptions{Removed: true}); !slices.ContainsFunc(list, func(e Entry) bool { return e.Resource.ID == other.ID }) {
		t.Fatal("…unless asked")
	}
	add(t, a, film.ID, "other.png", testPhoto(41, 30), AddOptions{})
	if err := Remove(ctx, a, other, true); err != nil || other.Removed || other.Name != "other-2" {
		t.Fatalf("restored: %+v %v", other, err)
	}
	long := add(t, a, film.ID, "a-very-long-name-for-a-picture-that-goes-on-and-on.png", testPhoto(42, 30), AddOptions{})
	if err := Remove(ctx, a, long, false); err != nil || len(long.Name) > 48 || !strings.Contains(long.Name, "-removed-") {
		t.Fatalf("a long name: %q %v", long.Name, err)
	}
	if err := Remove(ctx, a, long, true); err != nil || strings.Contains(long.Name, "-removed-") {
		t.Fatalf("restored long: %q %v", long.Name, err)
	}
}

// A version's file on this machine: from the cache, from the media store,
// from its linked folder when it was kept back, or a plain reason.
func TestLocalFileAdoptAndUpload(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	film := project(t, a, "Film")
	r := add(t, a, film.ID, "badge.png", testLogo(80, 40), AddOptions{})
	p, v, err := LocalFile(ctx, a, r, "", 0)
	if _, serr := os.Stat(p); err != nil || v.SHA256 != r.Current || serr != nil {
		t.Fatalf("latest: %q %v", p, err)
	}
	if _, _, err := LocalFile(ctx, a, r, strings.Repeat("0", 64), 0); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("a version it doesn't have: %v", err)
	}
	if _, _, err := LocalFile(ctx, a, r, "", 2); apperr.From(err).Code != apperr.NotConfigured || !strings.Contains(err.Error(), "aren't pictures yet") {
		t.Fatalf("a page of a picture: %v", err)
	}
	pdf := &models.Resource{ID: ids.New(ids.Resource), Title: "Deck", Current: "sha", Versions: []models.ResourceVersion{{SHA256: "sha", Pages: []models.ResourcePage{{Number: 1}}}}}
	if _, _, err := LocalFile(ctx, a, pdf, "", 3); apperr.From(err).Message != "Deck has 1 pages as pictures, so there's no page 3." {
		t.Fatalf("no such page: %v", err)
	}

	// A large file from a linked folder, kept on this machine until a cut uses it.
	dir := t.TempDir()
	big := writePNG(t, dir, "poster.png", testPhoto(120, 80))
	folderID := ids.New(ids.LinkedFolder)
	kept, err := AddFile(ctx, a, big, AddOptions{ProjectID: film.ID, FileName: "poster.png", Defer: true, Folder: &models.ResourceFolderRef{FolderID: folderID, RelPath: "poster.png"}})
	if err != nil || kept.Resource.Latest().Stored || len(kept.Resource.Latest().Holders) != 1 {
		t.Fatalf("kept back: %+v %v", kept, err)
	}
	k := kept.Resource
	if err := UploadVersion(ctx, a, k, "nope"); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("upload of a missing version: %v", err)
	}
	// This machine's cache let it go, and the folder isn't known here.
	if err := os.Remove(a.Cache.Path(k.Current)); err != nil {
		t.Fatal(err)
	}
	if err := AdoptFromFolder(a, k, k.Current); err == nil || !strings.Contains(err.Error(), "doesn't have") {
		t.Fatalf("no folder here: %v", err)
	}
	if err := UploadVersion(ctx, a, k, k.Current); apperr.From(err).Code != apperr.NotConfigured {
		t.Fatalf("upload without the file: %v", err)
	}
	if err := a.DB.Insert(ctx, store.CInstallations, models.Installation{ID: a.Cfg.Installation.ID, WorkspaceID: a.WorkspaceID(), Name: "Ada's laptop"}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LocalFile(ctx, a, k, "", 0); apperr.From(err).Message != "poster is still only on Ada's laptop; it's uploaded when a cut first uses it." {
		t.Fatalf("still only there: %v", err)
	}
	// With the folder linked here: adopted back from it (and checked).
	a.Cfg.Resources.Folders = []config.FolderPath{{ID: folderID, Path: dir}}
	if p, _, err := LocalFile(ctx, a, k, "", 0); err != nil || p == "" {
		t.Fatalf("adopted from the folder: %q %v", p, err)
	}
	if err := UploadVersion(ctx, a, k, k.Current); err != nil || !reload(t, a, k).Latest().Stored {
		t.Fatalf("uploaded: %v", err)
	}
	if err := UploadVersion(ctx, a, k, k.Current); err != nil {
		t.Fatalf("uploading what is stored is nothing to do: %v", err)
	}
	// The file changed in the folder, or went away: plain reasons.
	writePNG(t, dir, "poster.png", testPhoto(121, 80))
	if err := AdoptFromFolder(a, k, k.Current); err == nil || !strings.Contains(err.Error(), "changed in its folder") {
		t.Fatalf("changed: %v", err)
	}
	if err := os.Remove(big); err != nil {
		t.Fatal(err)
	}
	if err := AdoptFromFolder(a, k, k.Current); err == nil || !strings.Contains(err.Error(), "no longer in its folder") {
		t.Fatalf("gone: %v", err)
	}
	if err := AdoptFromFolder(a, r, r.Current); err == nil {
		t.Fatal("a resource from no folder can't be adopted")
	}
	if holdersText(ctx, a, nil) != "another machine" || holdersText(ctx, a, []string{"ins_unknown"}) != "another machine" {
		t.Fatal("holdersText")
	}
	if short("0123456789abcdef") != "01234567" || short("abc") != "abc" || ObjectKey(a.WorkspaceID(), "ab") == "" {
		t.Fatal("short / ObjectKey")
	}
}

// Looking again re-reads the latest version and re-runs the taggers; the
// tags a file name gave are kept.
func TestRefreshLooksAgain(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	r := add(t, a, "", "team-logo.png", testLogo(80, 40), AddOptions{})
	if _, err := a.DB.C(store.CResources).UpdateOne(ctx, bson.M{"_id": r.ID}, bson.M{"$set": bson.M{"versions.0.width": 1, "autoTags": []models.ResourceTag{{Tag: "old", Source: "visual"}, {Tag: "logo", Source: "filename"}}}}); err != nil {
		t.Fatal(err)
	}
	r = reload(t, a, r)
	if _, err := Refresh(ctx, a, r); err != nil {
		t.Fatal(err)
	}
	if r.Latest().Width != 80 || slices.ContainsFunc(r.AutoTags, func(t models.ResourceTag) bool { return t.Tag == "old" }) ||
		!slices.ContainsFunc(r.AutoTags, func(t models.ResourceTag) bool { return t.Tag == "logo" && t.Source == "filename" }) {
		t.Fatalf("refreshed: %+v %+v", r.Latest(), r.AutoTags)
	}
	svg := filepath.Join(t.TempDir(), "mark.svg")
	if err := os.WriteFile(svg, []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 40 20"><rect width="40" height="20" fill="#e04030"/></svg>`), 0o644); err != nil {
		t.Fatal(err)
	}
	sr, err := AddFile(ctx, a, svg, AddOptions{Via: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Refresh(ctx, a, sr.Resource); err != nil || sr.Resource.Latest().Width != 40 {
		t.Fatalf("an SVG: %+v %v", sr.Resource.Latest(), err)
	}
	if _, err := Refresh(ctx, a, &models.Resource{Title: "Empty"}); apperr.From(err).Code != apperr.NotFound {
		t.Fatalf("no versions: %v", err)
	}
}

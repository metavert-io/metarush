package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Brand kits (D82, D165): the colours a website suggests (the same every
// time), the nearest bundled font, a project's own kit before the
// workspace's, and a website's suggestion taken in one step and put on the
// latest cut as a checked change.

func TestPickColorsFromASite(t *testing.T) {
	// The fixture site: a theme colour and CSS brand names, a dark header behind the logo.
	site := []models.SiteColor{
		{Hex: "#E2552D", From: "theme-color", Weight: 2.7}, {Hex: "#2D6AE2", From: "css:--brand-accent", Weight: 0.7}, {Hex: "#1B1B1F", From: "header", Weight: 0.75},
		{Hex: "#FFF4E8", From: "logo", Weight: 0.7}, {Hex: "#FFFAF5", From: "background", Weight: 0.5}, {Hex: "#2EC27E", From: "section", Weight: 0.2},
	}
	c := pickColors(site, "#1B1B1F", true)
	if c.Primary != "#E2552D" || c.Accent != "#2D6AE2" || c.Background != "#1B1B1F" || c.Text != "#FFF4E8" {
		t.Fatalf("picked %+v", c)
	}
	if contrastRatio(c.Text, c.Background) < 4.5 || contrastRatio(c.Accent, c.Background) < 3 {
		t.Fatalf("text %.2f, accent %.2f on the background", contrastRatio(c.Text, c.Background), contrastRatio(c.Accent, c.Background))
	}
	if len(c.Why) != 4 || !strings.Contains(c.Why[0], "behind the logo in the site's header") || !strings.Contains(c.Why[1], "theme colour") {
		t.Fatalf("why %q", c.Why)
	}
	// The same every time, whatever order the colours come in.
	rev := slices.Clone(site)
	slices.Reverse(rev)
	if again := pickColors(rev, "#1B1B1F", true); fmt.Sprint(again) != fmt.Sprint(c) {
		t.Fatalf("order changed the pick: %+v vs %+v", again, c)
	}
	// A light site with one brand colour: white header, dark text; the accent is the primary again.
	light := pickColors([]models.SiteColor{{Hex: "#0A7CFF", From: "css:--primary", Weight: 0.9}, {Hex: "#FFFFFF", From: "header", Weight: 0.8}, {Hex: "#222222", From: "header-text", Weight: 0.3},
		{Hex: "#0B6BDB", From: "button", Weight: 0.3}, {Hex: "#888888", From: "link", Weight: 0.1}}, "#FFFFFF", true)
	if light.Primary != "#0A7CFF" || light.Background != "#FFFFFF" || light.Text != "#222222" || light.Accent != "#0A7CFF" {
		t.Fatalf("light site %+v", light)
	}
	// A logo on a colourful header: the background is the page's own neutral.
	loud := pickColors([]models.SiteColor{{Hex: "#7B2FF7", From: "header", Weight: 1}, {Hex: "#7B2FF7", From: "logo", Weight: 0.8}, {Hex: "#FFFFFF", From: "background", Weight: 0.5},
		{Hex: "#111111", From: "text", Weight: 0.5}, {Hex: "#F5C211", From: "button", Weight: 0.4}}, "#7B2FF7", true)
	if loud.Background != "#FFFFFF" || loud.Primary != "#7B2FF7" || loud.Text != "#111111" || loud.Accent != "#7B2FF7" {
		t.Fatalf("a yellow too light to stand out on white is not the accent: %+v", loud)
	}
	// Nothing to go on: MetaRush's own.
	if none := pickColors(nil, "", false); none.Primary != editspec.DefaultBrand.Primary || none.Accent != editspec.DefaultBrand.Accent || none.Background != editspec.DefaultBrand.Background {
		t.Fatalf("no colours %+v", none)
	}
	// A black-and-white brand: no strong colour, readable text.
	mono := pickColors([]models.SiteColor{{Hex: "#000000", From: "logo", Weight: 0.8}, {Hex: "#FFFFFF", From: "background", Weight: 0.5}, {Hex: "#000000", From: "text", Weight: 0.5}}, "#FFFFFF", false)
	if mono.Background != "#FFFFFF" || mono.Text != "#000000" || mono.Primary != "#000000" || mono.Accent != "#000000" {
		t.Fatalf("black and white %+v", mono)
	}
}

func TestNearestFontAndPickFont(t *testing.T) {
	for family, want := range map[string]string{"Fraunces": "Fraunces", "Playfair Display": "Fraunces", "Source Serif 4": "Fraunces", "Roboto Slab": "Fraunces", "Open Sans": "Inter",
		"Alegreya Sans": "Inter", "Segoe UI": "Inter", "Inter": "Inter", "JetBrains Mono": "JetBrains Mono", "IBM Plex Mono": "JetBrains Mono", "": ""} {
		if got := nearestFont(family); got != want {
			t.Errorf("%q: %q, want %q", family, got, want)
		}
	}
	alive := func(string) bool { return true }
	if f, r, why := pickFont([]models.SiteFont{{Family: "Fraunces", From: "h1", Google: true, ResourceID: "res_x"}}, alive); f != "Fraunces" || r != "res_x" || !strings.Contains(why, "Google Fonts") {
		t.Fatalf("a kept Google font: %q %q %q", f, r, why)
	}
	if f, r, why := pickFont([]models.SiteFont{{Family: "Brand Sans", From: "h1"}, {Family: "Fraunces", From: "body", Google: true}}, alive); f != "Inter" || r != "" || !strings.Contains(why, "only named") {
		t.Fatalf("the site's own font: %q %q %q", f, r, why)
	}
	if f, _, why := pickFont([]models.SiteFont{{Family: "Fraunces", From: "h1", Google: true}}, alive); f != "Fraunces" || why != "Font: Fraunces, the font of its headings." {
		t.Fatalf("a bundled family: %q %q", f, why)
	}
	if f, r, _ := pickFont(nil, alive); f != "" || r != "" {
		t.Fatalf("no fonts: %q %q", f, r)
	}
	if contrastRatio("#000000", "#FFFFFF") < 20.9 || contrastRatio("#777777", "#777777") != 1 {
		t.Fatal("contrast")
	}
}

func svgFile(t *testing.T, name, fill string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	svg := `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64"><rect width="64" height="64" rx="14" fill="` + fill +
		`"/><path d="M14 20 L22 46 L32 28 L42 46 L50 20" fill="none" stroke="#FFF4E8" stroke-width="6"/></svg>`
	if err := os.WriteFile(p, []byte(svg), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func addResource(t *testing.T, a *app.App, file string, o resources.AddOptions) *models.Resource {
	t.Helper()
	res, err := resources.AddFile(context.Background(), a, file, o)
	if err != nil || res.Resource == nil {
		t.Fatalf("add %s: %v %+v", file, err, res)
	}
	return res.Resource
}

// The brand is part of Content Settings (D193): KitFor, brand.show, new cuts
// and brand.set read and change the settings a project uses; a project that
// uses another's is refused; without a project, the default's change (and the
// answer says so); a project's own logo stays in the project, and projects
// using those settings may pin it.
func TestBrandFollowsContentSettings(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	wsLogo := addResource(t, a, svgFile(t, "logo.svg", "#123456"), resources.AddOptions{Scope: models.ScopeWorkspace, FileName: "logo.svg"})
	plain := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Plain"}).Project
	client := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Client"}).Project
	if plain.Content == nil || plain.Content.Uses != models.ContentOwn || client.Content != nil {
		t.Fatalf("the first project holds the settings, the next uses the default: %+v %+v", plain.Content, client.Content)
	}
	logo := wsLogo.ID
	dark, accent, text, bg, inter := "#111111", "#E0A458", "#F7F5F0", "#0B0B0E", "Inter"
	set := call[BrandShowOut](t, a, "brand.set", BrandSetIn{Logo: &logo, Primary: &dark, Accent: &accent, Text: &text, Background: &bg, Font: &inter})
	if set.From == nil || set.From.ID != plain.ID || !strings.Contains(set.Message, "These are “Plain”'s settings, which new projects use.") || !strings.Contains(set.Message, "Also used by 1 project") {
		t.Fatalf("the default's brand: %+v", set)
	}
	for pid, want := range map[string]string{"": "#111111", plain.ID: "#111111", client.ID: "#111111"} {
		k, err := KitFor(ctx, a, pid)
		if err != nil || k.Primary != want {
			t.Fatalf("KitFor(%q) = %+v %v, want primary %s", pid, k, err, want)
		}
	}
	if s := call[BrandShowOut](t, a, "brand.show", BrandShowIn{ProjectID: "Client"}); !s.ReadOnly || s.From == nil || s.From.Name != "Plain" || s.Brand.Primary != "#111111" || !strings.Contains(s.Message, "change it there") {
		t.Fatalf("client uses Plain's: %+v", s)
	}
	green := "#00AA55"
	err := callErr(a, "brand.set", BrandSetIn{ProjectID: "Client", Accent: &green})
	if ae := apperr.From(err); ae.Code != apperr.Conflict || !strings.Contains(ae.Message, "uses “Plain”'s settings") || !strings.Contains(ae.Command(), "content own") {
		t.Fatalf("a follower can't change the brand: %v", err)
	}
	// Its own settings start empty; its brand is then its own.
	call[ContentShowOut](t, a, "content.own", ContentOwnIn{ProjectID: client.ID})
	if k, _ := KitFor(ctx, a, client.ID); !kitEmpty(k) {
		t.Fatalf("own settings start empty: %+v", k)
	}
	own := call[BrandShowOut](t, a, "brand.set", BrandSetIn{ProjectID: "Client", Accent: &green})
	if own.ReadOnly || own.From == nil || own.From.ID != client.ID || own.Kit.Accent != green {
		t.Fatalf("client's own brand: %+v", own)
	}
	if k, _ := KitFor(ctx, a, plain.ID); k.Accent != "#E0A458" {
		t.Fatalf("Plain's brand changed: %+v", k)
	}
	// A project's logo stays in the project.
	cl := addResource(t, a, svgFile(t, "client-logo.svg", "#E2552D"), resources.AddOptions{ProjectID: client.ID, FileName: "client-logo.svg"})
	ref := cl.Name
	out := call[BrandShowOut](t, a, "brand.set", BrandSetIn{ProjectID: client.ID, Logo: &ref})
	var r models.Resource
	if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), cl.ID, &r); err != nil || r.Scope != models.ScopeProject || out.Kit.Logo != cl.ID || out.Logo == nil || out.Logo.Origin != resources.FromHere {
		t.Fatalf("the project's logo: %+v (%+v)", r, out.Logo)
	}
	// New cuts start with the brand of the settings they use.
	third := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Third"}).Project
	call[ContentShowOut](t, a, "content.follow", ContentFollowIn{ProjectID: third.ID, Follow: client.ID})
	for pid, want := range map[string]string{client.ID: green, plain.ID: "#E0A458", third.ID: green} {
		s := &editspec.Spec{}
		applyKitToNewCut(ctx, a, s, pid)
		if s.Brand == nil || s.Brand.Accent != want {
			t.Fatalf("a new cut of %s: %+v", pid, s.Brand)
		}
	}
	// Third follows Client: Client's own logo (in Client's resources) is one its cuts may pin.
	spec := &editspec.Spec{Resources: map[string]editspec.ResourcePin{BrandLogoPin: {ID: cl.ID, SHA256: cl.Current}}}
	rf, err := loadResourceFacts(ctx, a, third.ID, &projectlinks.View{Names: map[string]string{}}, spec)
	if err != nil {
		t.Fatal(err)
	}
	if ri, ok := rf.info(cl.ID, cl.Current); !ok || !ri.Visible {
		t.Fatalf("the brand's logo is visible to a follower: %+v %v", ri, ok)
	}
	if rf2, _ := loadResourceFacts(ctx, a, plain.ID, &projectlinks.View{Names: map[string]string{}}, spec); rf2 != nil {
		if ri, _ := rf2.info(cl.ID, cl.Current); ri.Visible {
			t.Fatal("another project's logo isn't visible to a project that doesn't use it")
		}
	}
}

func callErr(a *app.App, op string, in any) error {
	b, _ := json.Marshal(in)
	_, err := Call(context.Background(), a, op, b, Caller{})
	return err
}

// siteWithBrand stores what a snapshot of widget.example keeps (D165): the
// logo and icon pictures, a Google font's file, and the page with its brand.
func siteWithBrand(t *testing.T, a *app.App, projectID string) (page, logo, icon, font *models.Resource) {
	t.Helper()
	src := models.ResourceSource{Kind: models.FromURL, URL: "https://widget.example/logo.svg", Site: "widget.example"}
	logo = addResource(t, a, svgFile(t, "logo-1.svg", "#E2552D"), resources.AddOptions{ProjectID: projectID, Name: "widget-example-logo", Title: "Widget logo", FileName: "logo-1.svg", Source: src})
	src.URL = "https://widget.example/icon.svg"
	icon = addResource(t, a, svgFile(t, "logo-2.svg", "#1B1B1F"), resources.AddOptions{ProjectID: projectID, Name: "widget-example-icon", Title: "Widget icon", FileName: "logo-2.svg", Source: src})
	fp := filepath.Join(t.TempDir(), "Fraunces.woff2")
	if err := os.WriteFile(fp, append([]byte("wOF2"), []byte(strings.Repeat("f", 300))...), 0o644); err != nil {
		t.Fatal(err)
	}
	font = addResource(t, a, fp, resources.AddOptions{ProjectID: projectID, Name: "widget-example-font", FileName: "Fraunces.woff2", License: resources.GoogleFontLicense,
		Source: models.ResourceSource{Kind: models.FromURL, URL: "https://fonts.gstatic.com/s/fraunces/v1/latin-400.woff2", Site: "fonts.google.com"}})
	now := time.Now().UTC()
	sha := strings.Repeat("ab", 32)
	brand := &models.SiteBrand{
		Logos: []models.SiteLogo{
			{ResourceID: logo.ID, Name: logo.Name, SHA256: logo.Current, Kind: "img", Score: 13.8, URL: "https://widget.example/logo.svg", Hints: []string{"says logo", "links home", "in the header"}, Backdrop: "#1B1B1F"},
			{ResourceID: icon.ID, Name: icon.Name, SHA256: icon.Current, Kind: "touch-icon", Score: 3.3, URL: "https://widget.example/icon.svg", Hints: []string{"its app icon"}},
		},
		Colors: []models.SiteColor{{Hex: "#E2552D", From: "theme-color", Weight: 1.9}, {Hex: "#2D6AE2", From: "css:--brand-accent", Weight: 0.7}, {Hex: "#1B1B1F", From: "header", Weight: 0.75},
			{Hex: "#FFF4E8", From: "header-text", Weight: 0.1}, {Hex: "#FFFAF5", From: "background", Weight: 0.5}},
		Fonts: []models.SiteFont{{Family: "Fraunces", From: "h1", Google: true, ResourceID: font.ID}, {Family: "Segoe UI", From: "body"}},
	}
	p := models.Resource{ID: ids.New(ids.Resource), WorkspaceID: a.WorkspaceID(), Name: "widget-example", Title: "Widget: build it once", Kind: models.ResourceWebsite, Scope: models.ScopeProject,
		ProjectID: projectID, Current: sha, CreatedAt: now, UpdatedAt: now, Versions: []models.ResourceVersion{{SHA256: sha, Size: 10, MIME: "image/png", Ext: "png", Stored: true, Width: 2880, Height: 1800,
			Site:   &models.SiteSnapshot{URL: "https://widget.example", Site: "widget.example", Title: "Widget: build it once", SiteName: "Widget", ThemeColors: []string{"#E2552D"}, TakenAt: now, Via: "fetch", Brand: brand},
			Source: models.ResourceSource{Kind: models.FromURL, URL: "https://widget.example", Site: "widget.example", At: now}, CreatedAt: now}}}
	insert(t, a, store.CResources, p)
	return &p, logo, icon, font
}

// A website's brand kit, suggested and taken in one step: brand.suggest says
// what and why; brand.set with fromSite and apply checks the change to the
// latest cut first (nothing saved, the kit included) and saves both with the
// confirm token — a normal new version; the dashboard does it in one call.
func TestBrandSuggestAndTakeItInOneStep(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Widget tour"}).Project
	talk := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.ID}, Kind: "video", Name: "talk.mov", State: models.AssetAvailable,
		Media: models.MediaInfo{Duration: 12, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}}, Variants: map[string]models.Variant{},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()})
	insert(t, a, store.CAssets, talk)
	first := savePlan(t, a, fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},"clips":[{"id":"talk","assetId":%q,"in":0,"out":6}]}}`, p.ID, talk.ID))
	page, logo, icon, font := siteWithBrand(t, a, p.ID)

	sug := call[BrandSuggestOut](t, a, "brand.suggest", BrandSuggestIn{ProjectID: "Widget tour"})
	k := sug.Kit
	if k.Logo != logo.ID || k.Primary != "#E2552D" || k.Accent != "#2D6AE2" || k.Background != "#1B1B1F" || k.Text != "#FFF4E8" || k.Font != "Fraunces" || k.FontResource != font.ID {
		t.Fatalf("suggested %+v (why %q)", k, sug.Why)
	}
	if sug.Site.ID != page.ID || sug.Logo == nil || sug.Logo.ID != logo.ID || sug.FontFile == nil || len(sug.Logos) != 2 || !sug.Logos[0].Chosen || sug.Logos[1].Resource.ID != icon.ID || sug.InUse {
		t.Fatalf("suggestion %+v", sug)
	}
	if len(sug.Why) != 6 || !strings.HasPrefix(sug.Why[0], "Logo: “Widget logo”, the picture in the site's header") || !strings.Contains(sug.Message, "widget.example") {
		t.Fatalf("why %q, message %q", sug.Why, sug.Message)
	}
	if byName := call[BrandSuggestOut](t, a, "brand.suggest", BrandSuggestIn{From: "widget-example", ProjectID: p.ID}); fmt.Sprint(byName.Kit) != fmt.Sprint(sug.Kit) {
		t.Fatalf("by name: %+v", byName.Kit)
	}
	if err := callErr(a, "brand.suggest", BrandSuggestIn{From: logo.ID}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("a picture is not a web page snapshot: %v", err)
	}

	// Outside the dashboard, without a token: checked, nothing saved (the kit included).
	in := BrandSetIn{ProjectID: p.ID, FromSite: "widget-example", Apply: true}
	dry := call[BrandShowOut](t, a, "brand.set", in)
	if !dry.Pending || dry.Cut == nil || dry.Cut.Saved || dry.Cut.Check == nil || dry.Cut.Check.ConfirmToken == "" || !strings.HasPrefix(dry.Message, "Nothing is saved yet") {
		t.Fatalf("checked: %+v %+v", dry, dry.Cut)
	}
	if k, _ := KitFor(ctx, a, p.ID); !kitEmpty(k) {
		t.Fatalf("nothing saved before the token: %+v", k)
	}
	in.ConfirmToken = dry.Cut.Check.ConfirmToken
	done := call[BrandShowOut](t, a, "brand.set", in)
	if done.Pending || done.Cut == nil || !done.Cut.Saved || done.Cut.Applied == nil || done.Cut.Applied.Revision.Number != first.Revision.Number+1 || done.From == nil || done.From.ID != p.ID {
		t.Fatalf("saved: %+v %+v", done, done.Cut)
	}
	if rec, err := content.Record(ctx, a, p.ID); err != nil || rec == nil || rec.BrandKit == nil || rec.BrandKit.Logo != logo.ID || rec.BrandKit.Primary != "#E2552D" || !rec.BrandKit.ColorsEdited {
		t.Fatalf("the project's own settings: %+v %v", rec, err)
	}
	spec := specOf(t, a, done.Cut.Applied.Revision.ID)
	if spec.Brand == nil || spec.Brand.Primary != "#E2552D" || spec.Brand.Accent != "#2D6AE2" || spec.Brand.FontFile != BrandFontPin ||
		spec.Resources[BrandLogoPin].ID != logo.ID || spec.Resources[BrandFontPin].ID != font.ID {
		t.Fatalf("the cut's brand %+v resources %+v", spec.Brand, spec.Resources)
	}
	var lr models.Resource
	if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), logo.ID, &lr); err != nil || lr.Scope != models.ScopeProject {
		t.Fatalf("the site's logo stays in the project: %+v", lr)
	}
	if again := call[BrandSuggestOut](t, a, "brand.suggest", BrandSuggestIn{ProjectID: p.ID}); !again.InUse || !strings.Contains(again.Message, "already uses it") {
		t.Fatalf("in use: %+v", again)
	}
	if noop := call[CutEditOut](t, a, "brand.apply", BrandApplyIn{CutEditCommon: CutEditCommon{ProjectID: p.ID}}); !noop.NoChange || !strings.Contains(noop.Summary, "this project's brand kit") {
		t.Fatalf("brand.apply uses the project's own kit: %+v", noop)
	}
	if show := call[BrandShowOut](t, a, "brand.show", BrandShowIn{ProjectID: p.ID}); len(show.Sites) != 1 || show.Sites[0].ID != page.ID || show.Sites[0].Logos != 2 {
		t.Fatalf("sites %+v", show.Sites)
	}

	// The dashboard takes it in one call (after its own styled confirm).
	other := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Second client"}).Project
	clipTwo := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{other.ID}, Kind: "video", Name: "two.mov", State: models.AssetAvailable,
		Media: models.MediaInfo{Duration: 12, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}}, Variants: map[string]models.Variant{},
		CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC()})
	insert(t, a, store.CAssets, clipTwo)
	savePlan(t, a, fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},"clips":[{"id":"two","assetId":%q,"in":0,"out":6}]}}`, other.ID, clipTwo.ID))
	// A project that uses another's settings can't change their brand; with its own it can.
	if err := callErr(a, "brand.set", BrandSetIn{ProjectID: other.ID, FromSite: page.ID}); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("a follower's brand: %v", err)
	}
	call[ContentShowOut](t, a, "content.own", ContentOwnIn{ProjectID: other.ID})
	// Another project's site: its logo and font can't be used here until the projects are linked.
	if err := callErr(a, "brand.set", BrandSetIn{ProjectID: other.ID, FromSite: page.ID, Apply: true}); apperr.From(err).Code != apperr.InvalidInput || !strings.Contains(apperr.From(err).Message, "“Widget tour”") {
		t.Fatalf("another project's site: %v", err)
	}
	call[ProjectLinkOut](t, a, "project.link", ProjectLinkIn{ProjectID: other.ID, From: p.ID})
	via := a.Actor.Via
	a.Actor.Via = "dashboard"
	oneCall := call[BrandShowOut](t, a, "brand.set", BrandSetIn{ProjectID: other.ID, FromSite: page.ID, Apply: true})
	a.Actor.Via = via
	// Its cut started with the default's brand (Widget's), which its own is now too: nothing to change.
	if oneCall.Pending || oneCall.Cut == nil || !(oneCall.Cut.Saved || oneCall.Cut.NoChange) {
		t.Fatalf("one call: %+v %+v", oneCall, oneCall.Cut)
	}
	// A dry run saves nothing, without apply too.
	blue := "#0000FF"
	if d := call[BrandShowOut](t, a, "brand.set", BrandSetIn{ProjectID: other.ID, Primary: &blue, DryRun: true}); !d.Pending || d.Kit.Primary != blue {
		t.Fatalf("dry run %+v", d)
	}
	if k, _ := KitFor(ctx, a, other.ID); k.Primary != "#E2552D" {
		t.Fatalf("the dry run changed the kit: %+v", k)
	}
}

package resources

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func writeFile(t *testing.T, p string, b []byte) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func iconPNG(t *testing.T, c color.RGBA) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 64))
	for y := 4; y < 60; y++ {
		for x := 4; x < 60; x++ {
			img.Set(x, y, c)
		}
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

const headerLogo = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64" width="64" height="64"><script>alert(1)</script>` +
	`<rect width="64" height="64" rx="14" fill="#E2552D"/><path d="M14 20 L22 46 L32 28 L42 46 L50 20" fill="none" stroke="#FFF4E8" stroke-width="6"/></svg>`

const footerLogo = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 120 32" width="120" height="32"><circle cx="16" cy="16" r="14" fill="rgb(226, 85, 45)"/>` +
	`<rect x="38" y="10" width="76" height="12" rx="6" fill="rgb(45, 106, 226)"/></svg>`

// brandShot is a snapshot's first page as render/snapshot.mjs reports it:
// its screenshot, and the brand it read (two logos, an icon, a Google font).
func brandShot(t *testing.T, work string) PageShot {
	t.Helper()
	dir := filepath.Join(work, "page-01")
	desktop := writeFile(t, filepath.Join(dir, "desktop.png"), screenPNG(t, color.RGBA{0xe2, 0x55, 0x2d, 255}))
	text := writeFile(t, filepath.Join(dir, "text.txt"), []byte("Build it once."))
	logo1 := writeFile(t, filepath.Join(dir, "logo-1.svg"), []byte(headerLogo))
	logo2 := writeFile(t, filepath.Join(dir, "logo-2.svg"), []byte(footerLogo))
	icon := writeFile(t, filepath.Join(dir, "logo-3.png"), iconPNG(t, color.RGBA{0x1b, 0x1b, 0x1f, 255}))
	font := writeFile(t, filepath.Join(dir, "Fraunces.woff2"), append([]byte("wOF2"), bytes.Repeat([]byte{7}, 256)...))
	outside := writeFile(t, filepath.Join(t.TempDir(), "elsewhere.svg"), []byte(headerLogo))
	return PageShot{URL: "https://widget.example/", Requested: "https://widget.example/", Title: "Widget: build it once", SiteName: "Widget", ThemeColor: "#E2552D",
		TextFile: text, Shots: map[string]string{models.ShotDesktop: desktop},
		Brand: &PageBrand{
			Logos: []PageLogo{
				{File: outside, URL: "https://widget.example/x.svg", Kind: "img", Score: 20}, // not the snapshot's own file: never kept
				{File: logo1, URL: "https://widget.example/logo.svg", Kind: "img", Score: 13.8, Width: 32, Height: 32, Hints: []string{"says logo", "links home", "in the header"}, Backdrop: "#1b1b1f"},
				{File: logo2, Kind: "svg", Score: 9.8, Width: 120, Height: 32, Hints: []string{"says logo", "links home"}, Backdrop: "#fffaf5"},
				{File: icon, URL: "https://widget.example/icon.png", Kind: "touch-icon", Score: 3.3, Hints: []string{"its app icon"}},
			},
			Colors: []PageColor{{"#e2552d", "theme-color", 1}, {"#e2552d", "css:--brand-primary", 0.9}, {"#2d6ae2", "css:--brand-accent", 0.7}, {"#1b1b1f", "header", 0.2},
				{"#fff4e8", "header-text", 0.1}, {"#fffaf5", "background", 0.5}, {"#1b1b1f", "text", 0.5}, {"not a colour", "link", 1}},
			Fonts: []PageFont{{Family: "Fraunces", From: "h1", Google: true, File: font, URL: "https://fonts.gstatic.com/s/fraunces/v1/latin-400.woff2"}, {Family: "Segoe UI", From: "body"}},
		}}
}

func TestMergeSiteColors(t *testing.T) {
	got := MergeSiteColors([]models.SiteColor{{Hex: "#1b1b1f", From: "text", Weight: 0.5}, {Hex: "#E2552D", From: "logo", Weight: 0.8}, {Hex: "#e2552d", From: "theme-color", Weight: 1},
		{Hex: "#1B1B1F", From: "header", Weight: 0.25}, {Hex: "#2d6ae2", From: "css:--accent", Weight: 0.7}, {Hex: "red", From: "link", Weight: 1}, {Hex: "#00ff00", From: "link", Weight: 0}}, 0)
	want := []models.SiteColor{{Hex: "#E2552D", From: "theme-color", Weight: 1.8}, {Hex: "#2D6AE2", From: "css:--accent", Weight: 0.7}, {Hex: "#1B1B1F", From: "header", Weight: 0.75}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%d: got %+v, want %+v (all %+v)", i, got[i], want[i], got)
		}
	}
	if n := len(MergeSiteColors(got, 2)); n != 2 {
		t.Fatalf("at most 2: %d", n)
	}
	if siteSlug("https://www.Widget.example/pricing") != "widget-example" || siteSlug("http://127.0.0.1:5380/a/b") != "r-127-0-0-1" {
		t.Fatalf("site slugs: %s %s", siteSlug("https://www.Widget.example/pricing"), siteSlug("http://127.0.0.1:5380/a/b"))
	}
	for _, c := range [][4]string{
		{"https://widget.example/assets/apple-touch-icon.png?v=2", "x-icon", "/w/logo-3.png", "apple-touch-icon.png"},
		{"https://widget.example/brand/Acme%20Logo.svg", "x-logo", "/w/logo-1.svg", "Acme Logo.svg"},
		{"", "x-logo-2", "/w/logo-2.svg", "x-logo-2.svg"},
		{"https://widget.example/logo", "x-logo", "/w/logo-1.png", "x-logo.png"},
		{"data:image/png;base64,AAAA", "x-logo", "/w/logo-1.png", "x-logo.png"},
	} {
		if got := pictureFileName(c[0], c[1], c[2]); got != c[3] {
			t.Errorf("pictureFileName(%q) = %q, want %q", c[0], got, c[3])
		}
	}
}

// A snapshot's brand becomes pictures next to the page (named after the site,
// tagged, from the page), a Google font file, and colours and fonts on the
// page's version; the same pictures again are the same versions, a changed
// logo a new version, and a picture a person removed stays removed.
func TestKeepBrandFromASnapshot(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Widget tour", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CProjects, p); err != nil {
		t.Fatal(err)
	}
	work := t.TempDir()
	shot := brandShot(t, work)
	req := FetchRequest{Kind: FetchPage, URL: shot.URL, ProjectID: p.ID}
	page, status, err := storePage(ctx, a, req, shot, now, "fetch", false)
	if err != nil || status != StatusAdded {
		t.Fatalf("page: %s %v", status, err)
	}
	brand, notes := keepBrand(ctx, a, work, page, shot)
	if brand == nil {
		t.Fatalf("no brand (%v)", notes)
	}
	var names, kinds []string
	for _, l := range brand.Logos {
		names, kinds = append(names, l.Name), append(kinds, l.Kind)
	}
	if strings.Join(names, ",") != "widget-example-logo,widget-example-logo-2,widget-example-icon" || strings.Join(kinds, ",") != "img,svg,touch-icon" {
		t.Fatalf("logos %v %v (notes %v)", names, kinds, notes)
	}
	if len(notes) == 0 || !strings.Contains(notes[0], "“widget-example-logo”") || !strings.Contains(notes[0], "the font “Fraunces”") {
		t.Fatalf("notes %q", notes)
	}
	get := func(id string) models.Resource {
		t.Helper()
		var r models.Resource
		if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), id, &r); err != nil {
			t.Fatal(err)
		}
		return r
	}
	hasTag := func(r models.Resource, tag string) bool {
		for _, x := range r.AutoTags {
			if x.Tag == tag && x.Source == "page" {
				return true
			}
		}
		return false
	}
	logo := get(brand.Logos[0].ResourceID)
	v := logo.Latest()
	if logo.Scope != models.ScopeProject || logo.ProjectID != p.ID || logo.Kind != models.ResourceSVG || logo.Title != "Widget logo" || !hasTag(logo, "logo") {
		t.Fatalf("logo %+v", logo)
	}
	if v.Source.Kind != models.FromURL || v.Source.URL != "https://widget.example/logo.svg" || v.Source.Site != "widget.example" || len(v.Source.Lineage) != 1 || v.Source.Lineage[0].ResourceID != page.ID {
		t.Fatalf("logo source %+v", v.Source)
	}
	if len(v.Changes) == 0 || v.OriginalSHA256 == "" || brand.Logos[0].SHA256 != v.SHA256 || brand.Logos[0].Backdrop != "#1B1B1F" {
		t.Fatalf("an SVG from a page is cleaned like any: changes %v, logo %+v", v.Changes, brand.Logos[0])
	}
	drawn := get(brand.Logos[1].ResourceID)
	if drawn.Latest().Source.URL != "https://widget.example" || drawn.Title != "Widget logo 2" {
		t.Fatalf("a logo drawn in the page comes from the page: %+v %q", drawn.Latest().Source, drawn.Title)
	}
	icon := get(brand.Logos[2].ResourceID)
	if icon.Kind != models.ResourceImage || icon.Title != "Widget icon" || !hasTag(icon, "icon") {
		t.Fatalf("icon %+v", icon)
	}
	if len(brand.Fonts) != 2 || brand.Fonts[0].Family != "Fraunces" || !brand.Fonts[0].Google || brand.Fonts[0].ResourceID == "" || brand.Fonts[1].ResourceID != "" {
		t.Fatalf("fonts %+v", brand.Fonts)
	}
	font := get(brand.Fonts[0].ResourceID)
	if font.Kind != models.ResourceFont || font.Name != "widget-example-font" || font.License != GoogleFontLicense || font.Latest().Family != "Fraunces" || font.ProjectID != p.ID {
		t.Fatalf("font %+v", font)
	}
	if c := brand.Colors[0]; c.Hex != "#E2552D" || c.From != "theme-color" || c.Weight < 2.6 {
		t.Fatalf("the theme colour first, with the logo's own colours added: %+v", brand.Colors)
	}
	fromLogo := false
	for _, c := range brand.Colors {
		fromLogo = fromLogo || (c.From == "logo" && c.Hex == "#FFF4E8")
	}
	if !fromLogo {
		t.Fatalf("the logo's main colours count: %+v", brand.Colors)
	}
	// On the page's version, for brand.suggest.
	saved := get(page.ID)
	if b := saved.Latest().Site.Brand; b == nil || len(b.Logos) != 3 || b.Logos[0].ResourceID != logo.ID || len(b.Fonts) != 2 {
		t.Fatalf("page brand %+v", saved.Latest().Site.Brand)
	}

	// The same snapshot again: the same pictures, nothing new.
	page2, status, err := storePage(ctx, a, req, shot, now, "fetch", false)
	if err != nil || status != StatusSame {
		t.Fatalf("again: %s %v", status, err)
	}
	again, _ := keepBrand(ctx, a, work, page2, shot)
	for i, l := range again.Logos {
		if l.ResourceID != brand.Logos[i].ResourceID || len(get(l.ResourceID).Versions) != 1 {
			t.Fatalf("again %d: %+v", i, l)
		}
	}
	// A changed logo is a new version of the same resource; a removed icon stays removed.
	writeFile(t, shot.Brand.Logos[1].File, []byte(strings.ReplaceAll(headerLogo, "#E2552D", "#2D6AE2")))
	if err := Remove(ctx, a, &icon, false); err != nil {
		t.Fatal(err)
	}
	third, _ := keepBrand(ctx, a, work, page2, shot)
	if len(third.Logos) != 2 || third.Logos[0].ResourceID != logo.ID || len(get(logo.ID).Versions) != 2 {
		t.Fatalf("changed logo / removed icon: %+v", third.Logos)
	}
	for _, l := range third.Logos {
		if l.ResourceID == icon.ID {
			t.Fatal("a removed icon came back")
		}
	}
}

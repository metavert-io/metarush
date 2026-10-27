package resources

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// A website's logo, colours and fonts (D165). The snapshot's first page also
// reads the site's brand in the same filtering browser (render/web/brand.mjs):
// its most logo-like pictures (written next to the screenshots), its colours
// with where each came from, and its fonts (a Google font's regular weight
// downloaded; a site's own font only named). keepBrand keeps the pictures as
// resources of the page's scope, named after the site (acme-com-logo,
// acme-com-logo-2, acme-com-icon), tagged logo or icon, with the picture's own
// address and the page as their source; a Google font file as a font resource
// (acme-com-font); and records all of it on the page's version
// (SiteSnapshot.Brand), where brand.suggest reads it.

// PageBrand is what render/snapshot.mjs read about a page's brand.
type PageBrand struct {
	Logos  []PageLogo  `json:"logos"`
	Colors []PageColor `json:"colors"`
	Fonts  []PageFont  `json:"fonts"`
}

// PageLogo is one logo or icon picture the snapshot wrote.
type PageLogo struct {
	File     string   `json:"file"`
	URL      string   `json:"url"`
	Kind     string   `json:"kind"`
	Hints    []string `json:"hints"`
	Width    float64  `json:"width"`
	Height   float64  `json:"height"`
	Score    float64  `json:"score"`
	Backdrop string   `json:"backdrop"`
}

// PageColor is a colour of the page and where it came from.
type PageColor struct {
	Hex    string  `json:"hex"`
	From   string  `json:"from"`
	Weight float64 `json:"weight"`
}

// PageFont is a font family of the page; File is a Google font's downloaded file.
type PageFont struct {
	Family string `json:"family"`
	From   string `json:"from"`
	Google bool   `json:"google"`
	File   string `json:"file,omitempty"`
	URL    string `json:"url,omitempty"`
}

// maxBrandLogos is how many logo and icon pictures a snapshot keeps.
const maxBrandLogos = 3

// GoogleFontLicense is the licence note a Google font's file is kept with.
const GoogleFontLicense = "Google Fonts (open licence)"

// iconKinds are a page's own icons (in its head); the rest are logos.
var iconKinds = map[string]bool{"touch-icon": true, "icon": true, "mask-icon": true}

// colorSources rank where a colour came from, strongest first.
var colorSources = []string{"theme-color", "mask-icon", "css", "logo", "header", "button", "section", "link", "header-text", "background", "text"}

// ColorSourceRank orders a colour's source (theme-color first); unknown sources last.
func ColorSourceRank(from string) int {
	if strings.HasPrefix(from, "css") {
		from = "css"
	}
	for i, s := range colorSources {
		if s == from {
			return i
		}
	}
	return len(colorSources)
}

// MergeSiteColors gives one entry per colour (weights added, the strongest
// source kept), the page's declared colours (theme colour, its CSS colour
// names) first, then by weight; at most max.
func MergeSiteColors(list []models.SiteColor, max int) []models.SiteColor {
	type acc struct {
		c    models.SiteColor
		rank int
	}
	by := map[string]*acc{}
	var order []string
	for _, c := range list {
		hex := normColor(c.Hex)
		if hex == "" || !(c.Weight > 0) {
			continue
		}
		r := ColorSourceRank(c.From)
		if cur, ok := by[hex]; ok {
			cur.c.Weight += c.Weight
			if r < cur.rank {
				cur.c.From, cur.rank = c.From, r
			}
			continue
		}
		by[hex] = &acc{models.SiteColor{Hex: hex, From: c.From, Weight: c.Weight}, r}
		order = append(order, hex)
	}
	out := make([]*acc, 0, len(order))
	for _, h := range order {
		out = append(out, by[h])
	}
	declared := func(a *acc) int {
		if a.rank <= ColorSourceRank("css") {
			return 0
		}
		return 1
	}
	sort.SliceStable(out, func(i, j int) bool {
		if di, dj := declared(out[i]), declared(out[j]); di != dj {
			return di < dj
		}
		if out[i].c.Weight != out[j].c.Weight {
			return out[i].c.Weight > out[j].c.Weight
		}
		return out[i].c.Hex < out[j].c.Hex
	})
	res := make([]models.SiteColor, 0, len(out))
	for _, a := range out {
		if max > 0 && len(res) >= max {
			break
		}
		a.c.Weight = float64(int(a.c.Weight*1000+0.5)) / 1000
		res = append(res, a.c)
	}
	return res
}

// siteSlug names a site's things: "acme-com" for any page of acme.com.
func siteSlug(pageURL string) string {
	u, err := url.Parse(pageURL)
	if err != nil || u.Host == "" {
		return "site"
	}
	return pageSlug(&url.URL{Scheme: u.Scheme, Host: u.Host})
}

// logoWeights are how much a logo's main colours count, most used first.
var logoWeights = []float64{0.8, 0.6, 0.45, 0.35, 0.25, 0.2}

// keepBrand keeps a snapshot's brand: the logo and icon pictures and a Google
// font file as resources next to the page, and the colours and fonts on the
// page's version. Nothing here fails the snapshot: what can't be kept is said
// in the notes.
func keepBrand(ctx context.Context, a *app.App, work string, page *models.Resource, p PageShot) (*models.SiteBrand, []string) {
	if p.Brand == nil || page == nil {
		return nil, nil
	}
	sha, err := media.HashFile(p.Shots[models.ShotDesktop])
	if err != nil {
		return nil, nil
	}
	final := canonicalPage(p.URL)
	site := hostOf(final)
	slug := siteSlug(final)
	who := firstNonEmpty(clip(p.SiteName, 80), site)
	var pageVersion *models.ResourceVersion
	if v := page.Version(sha); v != nil {
		pageVersion = v
	}
	brand := &models.SiteBrand{}
	var notes, keptNames []string
	logos := append([]PageLogo(nil), p.Brand.Logos...)
	sort.SliceStable(logos, func(i, j int) bool { return logos[i].Score > logos[j].Score })
	counts := map[string]int{}
	for _, l := range logos {
		if len(brand.Logos) >= maxBrandLogos {
			break
		}
		if !insideDir(work, l.File) {
			continue
		}
		role := "logo"
		switch {
		case iconKinds[l.Kind]:
			role = "icon"
		case l.Kind == "preview":
			role = "preview"
		}
		counts[role]++
		name, title := slug+"-"+role, who+" "+map[string]string{"logo": "logo", "icon": "icon", "preview": "preview picture"}[role]
		if n := counts[role]; n > 1 {
			name, title = numbered(name, n), fmt.Sprintf("%s %d", title, n)
		}
		src := models.ResourceSource{Kind: models.FromURL, Name: pictureFileName(l.URL, name, l.File), URL: firstNonEmpty(l.URL, final), Site: site}
		if pageVersion != nil {
			src.Lineage = []models.ResourceLineage{{ResourceID: page.ID, SHA256: sha, Step: "found on the page " + pageWords(final)}}
		}
		res, err := keepPicture(ctx, a, page, l.File, name, title, src)
		if err != nil {
			notes = append(notes, fmt.Sprintf("The %s on %s couldn't be kept: %s", map[string]string{"logo": "logo", "icon": "icon", "preview": "preview picture"}[role], site, apperr.From(err).Message))
			continue
		}
		if res == nil {
			continue // a person removed this picture before: it stays removed
		}
		r := res.Resource
		if res.Status == StatusAdded || res.Status == StatusVersion {
			tag := models.ResourceTag{Tag: "logo", Source: "page", Confidence: 0.85, Why: "the page shows it as its logo"}
			if role == "icon" {
				tag = models.ResourceTag{Tag: "icon", Source: "page", Confidence: 0.85, Why: "the site uses it as its icon"}
			}
			if role != "preview" || containsStr(l.Hints, "says logo") {
				r.AutoTags = mergeAuto(r.AutoTags, []models.ResourceTag{tag})
				_ = a.DB.Update(ctx, store.CResources, a.WorkspaceID(), r.ID, bson.M{"$set": bson.M{"autoTags": r.AutoTags}})
			}
		}
		brand.Logos = append(brand.Logos, models.SiteLogo{ResourceID: r.ID, Name: r.Name, SHA256: res.SHA256, Kind: l.Kind, Score: l.Score, URL: l.URL,
			Hints: clipList(l.Hints, 12), Backdrop: normColor(l.Backdrop)})
		keptNames = append(keptNames, "“"+r.Name+"”")
	}
	colors := make([]models.SiteColor, 0, len(p.Brand.Colors)+6)
	for _, c := range p.Brand.Colors {
		colors = append(colors, models.SiteColor{Hex: c.Hex, From: clip(c.From, 80), Weight: c.Weight})
	}
	// The logo's own main colours count too (after the page's declared ones).
	for _, l := range brand.Logos {
		if iconKinds[l.Kind] || l.Kind == "preview" {
			continue
		}
		var r models.Resource
		if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), l.ResourceID, &r) == nil {
			if v := r.Version(l.SHA256); v != nil {
				for i, c := range v.Colors {
					if i < len(logoWeights) {
						colors = append(colors, models.SiteColor{Hex: c, From: "logo", Weight: logoWeights[i]})
					}
				}
			}
		}
		break // the most logo-like one
	}
	brand.Colors = MergeSiteColors(colors, 20)
	fontKept := false
	for _, f := range p.Brand.Fonts {
		if len(brand.Fonts) >= 8 || strings.TrimSpace(f.Family) == "" {
			continue
		}
		sf := models.SiteFont{Family: clip(strings.TrimSpace(f.Family), 80), From: clip(f.From, 20), Google: f.Google}
		if f.Google && f.File != "" && !fontKept && insideDir(work, f.File) {
			src := models.ResourceSource{Kind: models.FromURL, Name: filepath.Base(f.File), URL: f.URL, Site: "fonts.google.com"}
			if res, err := keepFont(ctx, a, page, f.File, slug+"-font", sf.Family, src); err != nil {
				notes = append(notes, fmt.Sprintf("The font %s couldn't be kept: %s", sf.Family, apperr.From(err).Message))
			} else if res != nil {
				sf.ResourceID = res.Resource.ID
				fontKept = true
				keptNames = append(keptNames, "the font “"+sf.Family+"”")
			}
		}
		brand.Fonts = append(brand.Fonts, sf)
	}
	if len(brand.Logos)+len(brand.Colors)+len(brand.Fonts) == 0 {
		return nil, notes
	}
	// On the page's version (a snapshot that looks exactly as before gets it too).
	if pageVersion != nil {
		if _, err := a.DB.C(store.CResources).UpdateOne(ctx, bson.M{"_id": page.ID, "workspaceId": a.WorkspaceID(), "versions.sha256": sha},
			bson.M{"$set": bson.M{"versions.$.site.brand": brand}}); err == nil && pageVersion.Site != nil {
			pageVersion.Site.Brand = brand
		}
	}
	if len(keptNames) > 0 {
		notes = append([]string{fmt.Sprintf("Kept %s from the brand of %s.", joinWords(keptNames), site)}, notes...)
	}
	return brand, notes
}

// keepPicture adds a logo or icon picture next to the page (its scope), named
// name, or finds it there already (the same picture again is the same
// version). nil, nil: a person removed this picture before.
func keepPicture(ctx context.Context, a *app.App, page *models.Resource, file, name, title string, src models.ResourceSource) (*AddResult, error) {
	if raw, err := media.HashFile(file); err == nil && removedHere(ctx, a, page, raw) {
		return nil, nil
	}
	o := AddOptions{Scope: page.Scope, ProjectID: page.ProjectID, Name: name, Title: title, FileName: src.Name, Source: src}
	res, err := AddFile(ctx, a, file, o)
	for n := 2; err != nil && apperr.From(err).Code == apperr.Conflict && n <= 5; n++ {
		// That name is a different kind of thing here (an SVG logo, now a PNG): number it.
		o.Name = numbered(name, n)
		res, err = AddFile(ctx, a, file, o)
	}
	if errors.Is(err, ErrNotResource) {
		return nil, apperr.New(apperr.InvalidInput, "it isn't a picture MetaRush can use")
	}
	if err != nil {
		return nil, err
	}
	if res.Resource == nil {
		return nil, apperr.New(apperr.InvalidInput, "%s", res.Message)
	}
	return res, nil
}

// keepFont adds a Google font's file next to the page with its licence note.
func keepFont(ctx context.Context, a *app.App, page *models.Resource, file, name, family string, src models.ResourceSource) (*AddResult, error) {
	if raw, err := media.HashFile(file); err == nil && removedHere(ctx, a, page, raw) {
		return nil, nil
	}
	o := AddOptions{Scope: page.Scope, ProjectID: page.ProjectID, Name: name, Title: family, FileName: filepath.Base(file), License: GoogleFontLicense, Source: src}
	res, err := AddFile(ctx, a, file, o)
	for n := 2; err != nil && apperr.From(err).Code == apperr.Conflict && n <= 5; n++ {
		o.Name = numbered(name, n)
		res, err = AddFile(ctx, a, file, o)
	}
	if err != nil {
		return nil, err
	}
	if res.Resource == nil || res.Resource.Kind != models.ResourceFont {
		return nil, apperr.New(apperr.InvalidInput, "it isn't a font file")
	}
	return res, nil
}

// removedHere reports a picture (by the hash of its file as it came) that a
// person removed from the page's project or library: a new snapshot doesn't
// bring it back.
func removedHere(ctx context.Context, a *app.App, page *models.Resource, sha string) bool {
	filter := bson.M{"removed": true, "$or": bson.A{bson.M{"versions.sha256": sha}, bson.M{"versions.originalSha256": sha}}, "scope": page.Scope}
	if page.Scope == models.ScopeProject {
		filter["projectId"] = page.ProjectID
	}
	var list []models.Resource
	return a.DB.Find(ctx, store.CResources, a.WorkspaceID(), filter, &list) == nil && len(list) > 0
}

// pictureFileName is the name a kept picture arrived under: its own file name
// on the site ("apple-touch-icon.png"), else the resource's name ("acme-com-logo.svg"
// for a logo drawn in the page) — what the file-name tag suggestions read.
func pictureFileName(rawURL, name, file string) string {
	if u, err := url.Parse(rawURL); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
		if b := path.Base(u.Path); b != "" && b != "/" && b != "." && pictureExt[strings.ToLower(path.Ext(b))] {
			if unescaped, err := url.PathUnescape(b); err == nil {
				b = unescaped
			}
			return sanitizeFileName(b)
		}
	}
	return name + filepath.Ext(file)
}

// insideDir reports a file inside dir (the snapshot's own folder).
func insideDir(dir, file string) bool {
	if dir == "" || file == "" {
		return false
	}
	rel, err := filepath.Rel(dir, file)
	return err == nil && rel != "." && !strings.HasPrefix(rel, "..") && !filepath.IsAbs(rel)
}

func clipList(xs []string, n int) []string {
	var out []string
	for _, x := range xs {
		if len(out) >= n {
			break
		}
		if x = clip(strings.TrimSpace(x), 60); x != "" {
			out = append(out, x)
		}
	}
	return out
}

// joinWords is "a", "a and b", "a, b and c".
func joinWords(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

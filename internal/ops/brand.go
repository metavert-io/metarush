package ops

import (
	"context"
	"fmt"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// The brand kit (D82): the workspace's logos, colours (taken from the logo,
// editable) and font — or a project's own kit (D165), for a team with several
// clients. It feeds a cut's brand tokens (spec.brand) and the resource pins
// its templates use; new cuts start with the project's kit when it has one,
// else the workspace's, and "Use the brand kit" (brand.apply) puts it into an
// existing cut as a checked change. A website snapshot suggests a kit
// (brand.suggest: its logo, four colours and a font, with why) that brand.set
// takes in one step (fromSite) and can put on the latest cut at once (apply).

// Pin names the brand kit gives its resources in a cut: the named logos
// (D198) and the font file.
const (
	BrandLogoPin         = "logo"
	BrandLogoDarkPin     = "logo-dark"
	BrandLogoLightPin    = "logo-light"
	BrandLogoIconPin     = "logo-icon"
	BrandLogoWordmarkPin = "logo-wordmark"
	BrandFontPin         = "brand-font"
)

// LogoPin is the name a cut pins a named logo under (D198).
func LogoPin(which string) string {
	switch which {
	case models.LogoDark:
		return BrandLogoDarkPin
	case models.LogoLight:
		return BrandLogoLightPin
	case models.LogoIcon:
		return BrandLogoIconPin
	case models.LogoWordmark:
		return BrandLogoWordmarkPin
	}
	return BrandLogoPin
}

// BrandLogoOut is one of the brand's named logos (D198).
type BrandLogoOut struct {
	Which string `json:"which" jsonschema:"enum=main,enum=dark,enum=light,enum=icon,enum=wordmark"`
	// Label is how people see it ("For dark backgrounds").
	Label string `json:"label"`
	// Pin is the name cuts use for it (logo, logo-dark…).
	Pin      string       `json:"pin"`
	Resource *ResourceRow `json:"resource,omitempty"`
}

// BrandShowIn names whose brand kit to show.
type BrandShowIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name): the brand of the Content Settings it uses (its own\\, another project's or the default's; from says whose). Empty: the default's\\, what new projects use."`
}

// BrandShowOut is the brand kit with its resources and the tokens cuts get.
type BrandShowOut struct {
	Kit          models.BrandKit `json:"kit"`
	Logo         *ResourceRow    `json:"logo,omitempty"`
	LogoDark     *ResourceRow    `json:"logoDark,omitempty"`
	LogoLight    *ResourceRow    `json:"logoLight,omitempty"`
	LogoIcon     *ResourceRow    `json:"logoIcon,omitempty"`
	LogoWordmark *ResourceRow    `json:"logoWordmark,omitempty"`
	FontFile     *ResourceRow    `json:"fontFile,omitempty"`
	// Logos are the five named logos (D198) in the order people see them,
	// each with its picture when there is one.
	Logos []BrandLogoOut `json:"logos"`
	// Brand is what a new cut's brand becomes.
	Brand editspec.Brand `json:"brand"`
	Empty bool           `json:"empty"`
	// ProjectID and ProjectName are the project asked about.
	ProjectID   string `json:"projectId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	// From is the project whose Content Settings hold this brand (D193).
	From *ContentProjectRef `json:"from,omitempty"`
	// ReadOnly: the project asked about uses another project's settings.
	ReadOnly bool `json:"readOnly,omitempty"`
	// UsedBy are the other projects using the same settings.
	UsedBy []ContentProjectRef `json:"usedBy,omitempty"`
	// LatestCuts are, after a change, the latest cuts of the projects using
	// these settings that don't have the brand yet: saved versions never
	// change, so each is an offer (brand.apply).
	LatestCuts []BrandCutOffer `json:"latestCuts,omitempty"`
	// Sites are the project's website snapshots that found a logo, colours or
	// fonts, newest first: brand.suggest makes a kit from one.
	Sites []BrandSiteRef `json:"sites,omitempty"`
	// Cut is brand.set's change to the project's latest cut (apply): checked
	// (with its confirm token), or saved as a new version.
	Cut *CutEditOut `json:"cut,omitempty"`
	// Pending: brand.set saved nothing yet (a dry run, or the change to the cut waits for its confirm token).
	Pending bool   `json:"pending,omitempty"`
	Message string `json:"message"`
}

// BrandCutOffer is a project's latest cut that could use the new brand.
type BrandCutOffer struct {
	ProjectID   string `json:"projectId"`
	ProjectName string `json:"projectName"`
	Branch      string `json:"branch"`
	// Version is the cut's latest version number.
	Version int    `json:"version"`
	Title   string `json:"title,omitempty" jsonschema:"description=The cut's name."`
}

// BrandSiteRef is a website snapshot a brand kit can be suggested from.
type BrandSiteRef struct {
	ID      string    `json:"id"`
	Name    string    `json:"name"`
	Title   string    `json:"title"`
	Site    string    `json:"site" jsonschema:"description=The site (example.com)."`
	URL     string    `json:"url"`
	TakenAt time.Time `json:"takenAt"`
	Logos   int       `json:"logos" jsonschema:"description=How many logo and icon pictures the snapshot kept."`
}

// BrandSetIn changes the brand kit. Resources are named like everywhere
// (short name, title or ID); "" clears.
type BrandSetIn struct {
	Logo         *string `json:"logo,omitempty" jsonschema:"description=The main logo: a resource's name or ID; empty clears it."`
	LogoDark     *string `json:"logoDark,omitempty" jsonschema:"description=The logo for dark backgrounds (usually light or white)."`
	LogoLight    *string `json:"logoLight,omitempty" jsonschema:"description=The logo for light backgrounds (usually dark)."`
	LogoIcon     *string `json:"logoIcon,omitempty" jsonschema:"description=A square icon of the brand."`
	LogoWordmark *string `json:"logoWordmark,omitempty" jsonschema:"description=The brand's name written out (a wordmark)."`
	FontResource *string `json:"fontResource,omitempty" jsonschema:"description=A font file from the library."`
	Font         *string `json:"font,omitempty" jsonschema:"enum=Inter,enum=Fraunces,enum=JetBrains Mono,enum=,description=A bundled font (empty clears it)."`
	Primary      *string `json:"primary,omitempty" jsonschema:"description=#RRGGBB (empty clears)."`
	Accent       *string `json:"accent,omitempty"`
	Text         *string `json:"text,omitempty"`
	Background   *string `json:"background,omitempty"`
	// FromLogo takes the colours from the logo again (also after a person changed them).
	FromLogo bool `json:"fromLogo,omitempty"`
	// ProjectID changes the brand of that project's own Content Settings (D193).
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own Content Settings: change their brand (every project using them gets it). A project that uses another's settings is refused: change them there\\, or give it its own. Empty: the default's\\, what new projects use."`
	// FromSite takes the kit a website snapshot suggests (brand.suggest) in one step.
	FromSite string `json:"fromSite,omitempty" jsonschema:"description=A website snapshot (its name\\, title or ID): take the logo\\, colours and font it suggests."`
	// Apply also puts the kit on the project's latest cut as a checked change.
	Apply        bool   `json:"apply,omitempty" jsonschema:"description=Also put the kit on the project's latest cut as a checked change (a new version). Outside the dashboard\\, without confirmToken nothing is saved yet: the answer carries the change and its token."`
	Branch       string `json:"branch,omitempty" jsonschema:"description=With apply: the cut's branch (default main)."`
	DryRun       bool   `json:"dryRun,omitempty" jsonschema:"description=Only check: the kit it would be and\\, with apply\\, the change to the cut; nothing is saved."`
	ConfirmToken string `json:"confirmToken,omitempty" jsonschema:"description=With apply: the token from a check of exactly this change."`
	Instruction  string `json:"instruction,omitempty" jsonschema:"maxLength=2000,description=With apply: what was asked (recorded with the new version)."`
}

// BrandSuggestIn asks for a brand kit made from a website snapshot.
type BrandSuggestIn struct {
	From      string `json:"from,omitempty" jsonschema:"description=A website snapshot: its resource name\\, title or ID. Default: the project's newest snapshot that found a logo\\, colours or fonts."`
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=The project (ID or name) the kit would be for; it also finds the page by name."`
}

// BrandSuggestOut is a proposed brand kit and why (nothing is saved).
type BrandSuggestOut struct {
	// Kit is the proposal: brand.set with fromSite takes it.
	Kit models.BrandKit `json:"kit"`
	// Brand is what a cut's graphics would get.
	Brand editspec.Brand `json:"brand"`
	// Why says, one plain sentence each, where the logo, colours and font come from.
	Why []string `json:"why"`
	// Site is the website snapshot it comes from.
	Site ResourceRow `json:"site"`
	// Logo and FontFile are the proposed logo and font file.
	Logo     *ResourceRow `json:"logo,omitempty"`
	FontFile *ResourceRow `json:"fontFile,omitempty"`
	// Logos are the pictures the snapshot kept (the proposed one marked chosen).
	Logos  []BrandSuggestLogo `json:"logos"`
	Colors []models.SiteColor `json:"colors" jsonschema:"description=The site's colours\\, with where each came from."`
	Fonts  []models.SiteFont  `json:"fonts"`
	// ProjectID and ProjectName are the project the kit would be for.
	ProjectID   string `json:"projectId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	// InUse: the project's own brand kit already is this suggestion.
	InUse   bool   `json:"inUse"`
	Message string `json:"message"`
}

// BrandSuggestLogo is one logo or icon picture a snapshot kept.
type BrandSuggestLogo struct {
	Resource ResourceRow `json:"resource"`
	Kind     string      `json:"kind" jsonschema:"enum=svg,enum=img,enum=background,enum=touch-icon,enum=icon,enum=mask-icon,enum=preview"`
	Score    float64     `json:"score"`
	URL      string      `json:"url,omitempty" jsonschema:"description=The picture's address on the site (empty: drawn in the page)."`
	Hints    []string    `json:"hints,omitempty"`
	Chosen   bool        `json:"chosen"`
}

// kitFor is the brand a project's cuts use — the brand of the Content
// Settings it uses (D193): its own, another project's or the default's — and
// whose settings hold it ("" none set up). projectID "" is the default's.
func kitFor(ctx context.Context, a *app.App, projectID string) (*models.BrandKit, string, error) {
	rec, owner, err := content.For(ctx, a, projectID)
	if err != nil {
		return nil, "", err
	}
	if rec.BrandKit == nil {
		return &models.BrandKit{}, owner, nil
	}
	k := *rec.BrandKit
	return &k, owner, nil
}

// KitFor is the brand kit a project's cuts and graphics use: the brand of the
// Content Settings it uses (D193). projectID "" is the default's.
func KitFor(ctx context.Context, a *app.App, projectID string) (*models.BrandKit, error) {
	k, _, err := kitFor(ctx, a, projectID)
	return k, err
}

// kitEmpty reports a brand kit nobody set up.
func kitEmpty(k *models.BrandKit) bool { return models.BrandKitEmpty(k) }

// kitResourceIDs are the resources of the brand a project uses: its cuts may
// pin them wherever they live (the settings may be another project's).
func kitResourceIDs(ctx context.Context, a *app.App, projectID string) map[string]bool {
	out := map[string]bool{}
	if projectID == "" {
		return out
	}
	k, _, err := kitFor(ctx, a, projectID)
	if err != nil || k == nil {
		return out
	}
	for _, id := range []string{k.Logo, k.LogoDark, k.LogoLight, k.LogoIcon, k.LogoWordmark, k.FontResource} {
		if id != "" {
			out[id] = true
		}
	}
	return out
}

// KitBrand is the brand tokens a cut gets from the kit (fontFile names the pin).
func KitBrand(k *models.BrandKit) *editspec.Brand {
	if kitEmpty(k) {
		return nil
	}
	b := &editspec.Brand{Primary: k.Primary, Accent: k.Accent, Text: k.Text, Background: k.Background, Font: k.Font}
	if k.FontResource != "" {
		b.FontFile = BrandFontPin
	}
	// The logos made for dark and light backgrounds (D198): graphics showing
	// the main logo show the one made for their background.
	if k.Logo != "" {
		b.Logo = BrandLogoPin
		if k.LogoDark != "" {
			b.LogoDark = BrandLogoDarkPin
		}
		if k.LogoLight != "" {
			b.LogoLight = BrandLogoLightPin
		}
	}
	return b
}

// dropUnpinnedLogos clears the brand's logo names that name nothing the cut pins.
func dropUnpinnedLogos(b *editspec.Brand, pinned func(string) bool) {
	if b == nil {
		return
	}
	for _, f := range []*string{&b.LogoDark, &b.LogoLight} {
		if *f != "" && !pinned(*f) {
			*f = ""
		}
	}
	if b.Logo != "" && !pinned(b.Logo) {
		b.Logo, b.LogoDark, b.LogoLight = "", "", ""
	}
}

// kitTokens is the brand a cut ends up with: MetaRush's defaults with the kit's values over them.
func kitTokens(k *models.BrandKit) editspec.Brand {
	b := editspec.DefaultBrand
	if kb := KitBrand(k); kb != nil {
		for _, kv := range [][2]*string{{&b.Primary, &kb.Primary}, {&b.Accent, &kb.Accent}, {&b.Text, &kb.Text}, {&b.Background, &kb.Background}, {&b.Font, &kb.Font}} {
			if *kv[1] != "" {
				*kv[0] = *kv[1]
			}
		}
		b.FontFile = kb.FontFile
	}
	return b
}

// kitPins are the resource pins the kit gives a cut (each resource's current version).
func kitPins(ctx context.Context, a *app.App, k *models.BrandKit) map[string]editspec.ResourcePin {
	out := map[string]editspec.ResourcePin{}
	add := func(name, id string) {
		if id == "" {
			return
		}
		var r models.Resource
		if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), id, &r) != nil || r.Removed {
			return
		}
		if v := r.Latest(); v != nil {
			out[name] = editspec.ResourcePin{ID: r.ID, SHA256: v.SHA256}
		}
	}
	add(BrandLogoPin, k.Logo)
	add(BrandLogoDarkPin, k.LogoDark)
	add(BrandLogoLightPin, k.LogoLight)
	add(BrandLogoIconPin, k.LogoIcon)
	add(BrandLogoWordmarkPin, k.LogoWordmark)
	add(BrandFontPin, k.FontResource)
	return out
}

// applyKitToNewCut gives a brand-new cut the brand kit's tokens and pins,
// when it brings no brand of its own (D82; like NewCutDefaults for the mix):
// the project's own kit when it has one (D165), else the workspace's.
func applyKitToNewCut(ctx context.Context, a *app.App, s *editspec.Spec, projectID ...string) {
	pid := ""
	if len(projectID) > 0 {
		pid = projectID[0]
	}
	k, err := KitFor(ctx, a, pid)
	if err != nil || kitEmpty(k) {
		return
	}
	if s.Brand == nil {
		s.Brand = KitBrand(k)
	}
	for n, pin := range kitPins(ctx, a, k) {
		if _, taken := s.Resources[n]; taken {
			continue
		}
		if s.Resources == nil {
			s.Resources = map[string]editspec.ResourcePin{}
		}
		s.Resources[n] = pin
	}
	if s.Brand != nil && s.Brand.FontFile == BrandFontPin {
		if _, ok := s.Resources[BrandFontPin]; !ok {
			s.Brand.FontFile = ""
		}
	}
	dropUnpinnedLogos(s.Brand, func(n string) bool { _, ok := s.Resources[n]; return ok })
}

// colorsFromLogo picks brand tokens from a logo's colours: the most saturated
// becomes the accent, a light one the primary (titles), the darkest a dark
// background, and the text colour follows the background.
func colorsFromLogo(colors []string) (primary, accent, text, background string) {
	d := editspec.DefaultBrand
	primary, accent, text, background = d.Primary, d.Accent, d.Text, d.Background
	type hsl struct {
		hex  string
		s, l float64
	}
	var cs []hsl
	for _, c := range colors {
		r, g, b, ok := parseHex(c)
		if !ok {
			continue
		}
		s, l := satLight(r, g, b)
		cs = append(cs, hsl{strings.ToUpper(c), s, l})
	}
	bestSat := -1.0
	for _, c := range cs {
		if c.s > 0.25 && c.l > 0.2 && c.l < 0.85 && c.s > bestSat {
			bestSat, accent = c.s, c.hex
		}
	}
	for _, c := range cs {
		if c.l > 0.82 && c.hex != accent {
			primary = c.hex
			break
		}
	}
	darkest := 1.0
	for _, c := range cs {
		if c.l < 0.16 && c.l < darkest {
			darkest, background = c.l, c.hex
		}
	}
	if _, _, l := hexLight(background); l > 0.5 {
		text = "#141414"
	}
	return primary, accent, text, background
}

func parseHex(c string) (r, g, b float64, ok bool) {
	c = strings.TrimPrefix(strings.TrimSpace(c), "#")
	if len(c) != 6 {
		return 0, 0, 0, false
	}
	v, err := strconv.ParseUint(c, 16, 32)
	if err != nil {
		return 0, 0, 0, false
	}
	return float64(v>>16&0xff) / 255, float64(v>>8&0xff) / 255, float64(v&0xff) / 255, true
}

func satLight(r, g, b float64) (float64, float64) {
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	l := (mx + mn) / 2
	if mx == mn {
		return 0, l
	}
	d := mx - mn
	if l > 0.5 {
		return d / (2 - mx - mn), l
	}
	return d / (mx + mn), l
}

func hexLight(c string) (float64, float64, float64) {
	r, g, b, ok := parseHex(c)
	if !ok {
		return 0, 0, 0
	}
	s, l := satLight(r, g, b)
	return s, 0, l
}

func validHexColor(s string) bool {
	_, _, _, ok := parseHex(s)
	return ok && strings.HasPrefix(strings.TrimSpace(s), "#")
}

// hueOf is a colour's hue in degrees (0 for greys).
func hueOf(r, g, b float64) float64 {
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	d := mx - mn
	if d == 0 {
		return 0
	}
	var h float64
	switch mx {
	case r:
		h = math.Mod((g-b)/d, 6)
	case g:
		h = (b-r)/d + 2
	default:
		h = (r-g)/d + 4
	}
	h *= 60
	if h < 0 {
		h += 360
	}
	return h
}

func hueDist(a, b float64) float64 {
	d := math.Abs(a - b)
	if d > 180 {
		d = 360 - d
	}
	return d
}

// relLuminance is a colour's relative luminance (WCAG).
func relLuminance(hex string) float64 {
	r, g, b, ok := parseHex(hex)
	if !ok {
		return 0
	}
	lin := func(c float64) float64 {
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b)
}

// contrastRatio is the WCAG contrast of two colours: 1 (none) to 21.
func contrastRatio(a, b string) float64 {
	la, lb := relLuminance(a), relLuminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// ---- a brand kit from a website (D165) ----

// colorFact is a site colour as the choices weigh it.
type colorFact struct {
	hex    string
	from   string
	weight float64
	// tier: 0 the page's declared colours (theme colour, its CSS colour
	// names), 1 the logo's, 2 the rest of the page.
	tier               int
	chroma, light, hue float64
}

func factOf(c models.SiteColor) (colorFact, bool) {
	r, g, b, ok := parseHex(c.Hex)
	if !ok || !strings.HasPrefix(strings.TrimSpace(c.Hex), "#") {
		return colorFact{}, false
	}
	mx, mn := math.Max(r, math.Max(g, b)), math.Min(r, math.Min(g, b))
	f := colorFact{hex: strings.ToUpper(strings.TrimSpace(c.Hex)), from: c.From, weight: c.Weight, chroma: mx - mn, light: (mx + mn) / 2, hue: hueOf(r, g, b), tier: 2}
	switch {
	case resources.ColorSourceRank(c.From) <= resources.ColorSourceRank("css"):
		f.tier = 0
	case c.From == "logo":
		f.tier = 1
	}
	return f, true
}

// colourful: a brand colour, not a grey, a near-black or a near-white.
func (f colorFact) colourful() bool { return f.chroma >= 0.2 && f.light >= 0.15 && f.light <= 0.85 }
func (f colorFact) neutral() bool   { return f.chroma < 0.15 }
func (f colorFact) score() float64  { return f.weight * (0.4 + 0.6*f.chroma) }

// colorChoice is a kit's four colours, with why.
type colorChoice struct {
	Primary, Accent, Text, Background string
	Why                               []string
}

// colorFromWords says where a site colour came from, plainly.
func colorFromWords(from string) string {
	switch {
	case from == "theme-color":
		return "the site's theme colour"
	case from == "mask-icon":
		return "the colour of the site's pinned-tab icon"
	case strings.HasPrefix(from, "css"):
		return "a colour named in the site's styles"
	case from == "logo":
		return "the logo's main colour"
	case from == "header":
		return "the colour of the site's header"
	case from == "button":
		return "the colour of its buttons"
	case from == "section":
		return "a large part of its first screen"
	case from == "link":
		return "the colour of its links"
	case from == "header-text":
		return "the header's text"
	case from == "background":
		return "the page's background"
	case from == "text":
		return "the page's text"
	}
	return "a colour of the page"
}

// pickColors chooses a kit's four colours from a site's (D165), the same way
// every time. Primary: the most saturated, most used brand colour — the
// page's declared colours (theme colour, its CSS colour names) first, then
// the logo's, then the rest of the page (header, buttons, links). Background:
// the neutral the logo sits on (the header's or the page's), else the
// header's or the page's own neutral, else its darkest. Text: the page's text
// colours first, readable on the background (contrast at least 4.5), else
// near-white or near-black. Accent: a second brand colour of another hue that
// stands out on the background (contrast at least 3), else the primary.
func pickColors(colors []models.SiteColor, logoBackdrop string, logoInHeader bool) colorChoice {
	var facts []colorFact
	for _, c := range colors {
		if f, ok := factOf(c); ok && f.weight > 0 {
			facts = append(facts, f)
		}
	}
	var brandCols []colorFact
	for _, f := range facts {
		if f.colourful() {
			brandCols = append(brandCols, f)
		}
	}
	sort.SliceStable(brandCols, func(i, j int) bool {
		a, b := brandCols[i], brandCols[j]
		if a.tier != b.tier {
			return a.tier < b.tier
		}
		if sa, sb := a.score(), b.score(); sa != sb {
			return sa > sb
		}
		return a.hex < b.hex
	})
	best := func(keep func(colorFact) bool, better func(a, b colorFact) bool) (colorFact, bool) {
		var out colorFact
		found := false
		for _, f := range facts {
			if keep(f) && (!found || better(f, out)) {
				out, found = f, true
			}
		}
		return out, found
	}
	heavier := func(a, b colorFact) bool { return a.weight > b.weight || (a.weight == b.weight && a.hex < b.hex) }
	d := editspec.DefaultBrand
	if len(facts) == 0 {
		return colorChoice{Primary: d.Primary, Accent: d.Accent, Text: d.Text, Background: d.Background, Why: []string{"Colours: the page gave none, so these are MetaRush's own."}}
	}
	var out colorChoice

	// Background.
	if bf, ok := factOf(models.SiteColor{Hex: logoBackdrop, Weight: 1}); ok && bf.neutral() {
		out.Background = bf.hex
		where := "on the page"
		if logoInHeader {
			where = "in the site's header"
		}
		out.Why = append(out.Why, fmt.Sprintf("Background %s: the colour behind the logo %s.", bf.hex, where))
	} else if f, ok := best(func(f colorFact) bool { return f.from == "header" && f.neutral() }, heavier); ok {
		out.Background = f.hex
		out.Why = append(out.Why, fmt.Sprintf("Background %s: %s.", f.hex, colorFromWords(f.from)))
	} else if f, ok := best(func(f colorFact) bool { return f.from == "background" && f.neutral() }, heavier); ok {
		out.Background = f.hex
		out.Why = append(out.Why, fmt.Sprintf("Background %s: %s.", f.hex, colorFromWords(f.from)))
	} else if f, ok := best(func(f colorFact) bool { return f.neutral() && f.light < 0.25 }, heavier); ok {
		out.Background = f.hex
		out.Why = append(out.Why, fmt.Sprintf("Background %s: a dark colour of the page.", f.hex))
	} else {
		out.Background = d.Background
		out.Why = append(out.Why, fmt.Sprintf("Background %s: MetaRush's own, as the site has no plain background colour.", d.Background))
	}

	// Primary.
	primary, colourful := colorFact{}, false
	if len(brandCols) > 0 {
		primary, colourful = brandCols[0], true
		out.Primary = primary.hex
		out.Why = append(out.Why, fmt.Sprintf("Primary %s: %s, its strongest brand colour.", primary.hex, colorFromWords(primary.from)))
	} else if f, ok := best(func(f colorFact) bool { return f.tier <= 1 }, heavier); ok {
		primary = f
		out.Primary = f.hex
		out.Why = append(out.Why, fmt.Sprintf("Primary %s: %s (the site has no strong colour).", f.hex, colorFromWords(f.from)))
	} else {
		out.Primary = d.Primary
		out.Why = append(out.Why, fmt.Sprintf("Primary %s: MetaRush's own, as the site has no strong colour.", d.Primary))
	}

	// Text.
	type textCand struct {
		f    colorFact
		pref int
	}
	var texts []textCand
	for _, f := range facts {
		if f.chroma >= 0.25 || contrastRatio(f.hex, out.Background) < 4.5 {
			continue
		}
		pref := 1
		if f.from == "text" || f.from == "header-text" {
			pref = 0
		}
		texts = append(texts, textCand{f, pref})
	}
	sort.SliceStable(texts, func(i, j int) bool {
		a, b := texts[i], texts[j]
		if a.pref != b.pref {
			return a.pref < b.pref
		}
		if a.f.weight != b.f.weight {
			return a.f.weight > b.f.weight
		}
		return a.f.hex < b.f.hex
	})
	if len(texts) > 0 {
		out.Text = texts[0].f.hex
		out.Why = append(out.Why, fmt.Sprintf("Text %s: %s, easy to read on the background.", out.Text, colorFromWords(texts[0].f.from)))
	} else {
		out.Text = "#F7F5F0"
		if contrastRatio("#141414", out.Background) > contrastRatio(out.Text, out.Background) {
			out.Text = "#141414"
		}
		out.Why = append(out.Why, fmt.Sprintf("Text %s: easy to read on the background (none of the page's colours is).", out.Text))
	}

	// Accent.
	for _, f := range brandCols {
		if f.hex == out.Primary || (colourful && hueDist(f.hue, primary.hue) < 30 && math.Abs(f.light-primary.light) < 0.25) {
			continue // the primary, or too close to it
		}
		if contrastRatio(f.hex, out.Background) < 3 {
			continue
		}
		out.Accent = f.hex
		out.Why = append(out.Why, fmt.Sprintf("Accent %s: %s, a second brand colour that stands out on the background.", f.hex, colorFromWords(f.from)))
		break
	}
	if out.Accent == "" {
		switch {
		case colourful:
			out.Accent = out.Primary
			out.Why = append(out.Why, fmt.Sprintf("Accent %s: the primary again, as the site has no second brand colour that stands out on the background.", out.Accent))
		case contrastRatio(out.Primary, out.Background) >= 3:
			out.Accent = out.Primary
			out.Why = append(out.Why, fmt.Sprintf("Accent %s: the primary again (the site has no strong colour).", out.Accent))
		default:
			out.Accent = out.Text
			out.Why = append(out.Why, fmt.Sprintf("Accent %s: the text colour (the site has no strong colour that stands out on the background).", out.Accent))
		}
	}
	return out
}

// nearestFont is the bundled font closest to a family: JetBrains Mono for a
// monospaced one, Fraunces for a serif, else Inter.
func nearestFont(family string) string {
	f := strings.ToLower(strings.TrimSpace(family))
	if f == "" {
		return ""
	}
	for _, w := range []string{"mono", "code", "courier", "consol", "menlo", "monaco", "inconsolata"} {
		if strings.Contains(f, w) {
			return "JetBrains Mono"
		}
	}
	if strings.Contains(f, "sans") {
		return "Inter"
	}
	for _, w := range []string{"serif", "georgia", "times", "garamond", "baskerville", "playfair", "merriweather", "lora", "fraunces", "crimson", "cormorant", "caslon",
		"bodoni", "didot", "spectral", "abril", "cardo", "cinzel", "domine", "vollkorn", "bitter", "slab", "prata", "gelasio", "literata", "newsreader", "tiempos",
		"charter", "cambria", "palatino", "antiqua", "minion", "freight", "canela", "recoleta", "alegreya", "gloock"} {
		if strings.Contains(f, w) {
			return "Fraunces"
		}
	}
	return "Inter"
}

// fontWhere says what a page writes in a family, plainly.
func fontWhere(from string) string {
	switch from {
	case "h1", "h2":
		return "the font of its headings"
	case "body":
		return "the font of its text"
	case "nav":
		return "the font of its menu"
	case "google":
		return "a Google font it links"
	}
	return "a font it loads"
}

// pickFont chooses the kit's font from a site's: the kept Google font file
// (with the nearest bundled font for anything that can't use a file), else
// the nearest bundled font to the family its headings (or text) use.
func pickFont(fonts []models.SiteFont, fileAlive func(id string) bool) (font, fontResource, why string) {
	for _, f := range fonts {
		if f.ResourceID != "" && fileAlive(f.ResourceID) {
			return nearestFont(f.Family), f.ResourceID, fmt.Sprintf("Font: “%s”, %s, its file kept from Google Fonts (an open licence).", f.Family, fontWhere(f.From))
		}
	}
	if len(fonts) == 0 {
		return "", "", "Font: the page names none, so graphics keep MetaRush's own."
	}
	f := fonts[0]
	near := nearestFont(f.Family)
	switch {
	case strings.EqualFold(near, f.Family):
		return near, "", fmt.Sprintf("Font: %s, %s.", near, fontWhere(f.From))
	case f.Google:
		return near, "", fmt.Sprintf("Font: %s, the nearest of MetaRush's own to “%s”, %s (its file wasn't kept: the page didn't load it).", near, f.Family, fontWhere(f.From))
	}
	return near, "", fmt.Sprintf("Font: %s, the nearest of MetaRush's own to “%s”, %s (the site's own font is only named: its licence is unknown).", near, f.Family, fontWhere(f.From))
}

// logoWhy says where the proposed logo comes from, plainly.
func logoWhy(l models.SiteLogo, title string) string {
	has := func(h string) bool {
		for _, x := range l.Hints {
			if x == h {
				return true
			}
		}
		return false
	}
	switch {
	case l.Kind == "touch-icon" || l.Kind == "icon" || l.Kind == "mask-icon":
		return fmt.Sprintf("Logo: “%s”, the site's own icon (the page shows no logo of its own).", title)
	case l.Kind == "preview":
		return fmt.Sprintf("Logo: “%s”, the page's preview picture (it shows no logo of its own).", title)
	case has("links home") && has("in the header"):
		return fmt.Sprintf("Logo: “%s”, the picture in the site's header that links to its home page.", title)
	case has("says logo"):
		return fmt.Sprintf("Logo: “%s”, the picture the page calls its logo.", title)
	case has("links home"):
		return fmt.Sprintf("Logo: “%s”, the picture that links to the site's home page.", title)
	}
	return fmt.Sprintf("Logo: “%s”, the most logo-like picture on the page.", title)
}

// originFor is where a resource comes from as a project sees it.
func originFor(r *models.Resource, projectID string, view *projectlinks.View) resources.Entry {
	e := resources.Entry{Resource: *r, Origin: resources.FromWorkspace}
	switch {
	case r.Scope == models.ScopeProject && projectID != "" && r.ProjectID == projectID:
		e.Origin = resources.FromHere
	case r.Scope == models.ScopeProject:
		e.Origin, e.SourceID = resources.FromLinked, r.ProjectID
		if view != nil {
			if src, ok := view.From(projectlinks.ResourceItem(r)); ok && src != "" {
				e.SourceID = src
			}
		}
	}
	return e
}

// brandSites are a project's website snapshots that read a brand, newest first.
func brandSites(ctx context.Context, a *app.App, projectID string) []BrandSiteRef {
	var list []models.Resource
	if a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"kind": models.ResourceWebsite, "scope": models.ScopeProject, "projectId": projectID,
		"removed": bson.M{"$ne": true}, "versions.site.brand": bson.M{"$exists": true}}, &list) != nil {
		return nil
	}
	var out []BrandSiteRef
	for _, r := range list {
		v := r.Latest()
		if v == nil || v.Site == nil || v.Site.Brand == nil {
			continue
		}
		out = append(out, BrandSiteRef{ID: r.ID, Name: r.Name, Title: r.Title, Site: v.Site.Site, URL: v.Site.URL, TakenAt: v.Site.TakenAt, Logos: len(v.Site.Brand.Logos)})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if !out[i].TakenAt.Equal(out[j].TakenAt) {
			return out[i].TakenAt.After(out[j].TakenAt)
		}
		return out[i].Name < out[j].Name
	})
	if len(out) > 5 {
		out = out[:5]
	}
	return out
}

// suggestBrand makes a brand kit from a website snapshot (brand.suggest).
// p is the project the kit would be for (nil: none named, and the page is the
// workspace library's).
func suggestBrand(ctx context.Context, a *app.App, from string, p *models.Project) (*BrandSuggestOut, *models.Project, error) {
	pid := ""
	if p != nil {
		pid = p.ID
	}
	var e *resources.Entry
	var view *projectlinks.View
	if strings.TrimSpace(from) == "" {
		if p == nil {
			return nil, nil, apperr.New(apperr.InvalidInput, "Say which web page snapshot to take the brand from, or which project's.")
		}
		sites := brandSites(ctx, a, p.ID)
		if len(sites) == 0 {
			return nil, nil, apperr.New(apperr.NotFound, "“%s” has no web page snapshot with a logo, colours or fonts yet.", p.Name).
				WithHint("Add the site's address to the project (paste it in its Add box), then try again.").WithCommand("metarush resources add <address> --project <project> --wait")
		}
		from = sites[0].ID
	}
	found, v, err := findResource(ctx, a, ResourceRefIn{Ref: from, ProjectID: pid})
	if err != nil {
		return nil, nil, err
	}
	e, view = found, v
	r := &e.Resource
	if r.Kind != models.ResourceWebsite {
		return nil, nil, apperr.New(apperr.InvalidInput, "“%s” is a %s, not a web page snapshot.", r.Title, resources.KindWord(r.Kind)).
			WithHint("A brand kit is suggested from a snapshot of the site's home page.")
	}
	if p != nil && r.Scope == models.ScopeProject && r.ProjectID != p.ID {
		// Its logo and font live where the page does: the project must be able to use them.
		if _, linked := view.From(projectlinks.ResourceItem(r)); !linked {
			owner := "another project"
			var op models.Project
			if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), r.ProjectID, &op) == nil {
				owner = "“" + op.Name + "”"
			}
			return nil, nil, apperr.New(apperr.InvalidInput, "“%s” is in %s, so “%s” can't use its logo and font.", r.Title, owner, p.Name).
				WithHint("Let this project use that one (link it), or add the site to this project.").WithCommand("metarush project link <this project> <other project>")
		}
	}
	if p == nil && r.Scope == models.ScopeProject {
		var owner models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), r.ProjectID, &owner) == nil {
			p, pid = &owner, owner.ID
			if lv, err := projectlinks.Load(ctx, a, owner.ID); err == nil {
				view = lv
			}
		}
	}
	ver := r.Latest()
	if ver == nil || ver.Site == nil {
		return nil, nil, apperr.New(apperr.InvalidInput, "“%s” has no snapshot to take a brand from.", r.Title)
	}
	ss := ver.Site
	b := ss.Brand
	var why []string
	if b == nil {
		// A snapshot taken before MetaRush read brands: its theme colour and its screenshot's colours.
		b = &models.SiteBrand{}
		for i, c := range ss.ThemeColors {
			b.Colors = append(b.Colors, models.SiteColor{Hex: c, From: "section", Weight: math.Max(0.1, 1-0.15*float64(i))})
		}
		why = append(why, "This snapshot is older than MetaRush's reading of logos and fonts: take a new snapshot of the page to find them.")
	}
	folders := folderNames(ctx, a)
	alive := map[string]*models.Resource{}
	get := func(id string) *models.Resource {
		if id == "" {
			return nil
		}
		if x, ok := alive[id]; ok {
			return x
		}
		var res models.Resource
		if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), id, &res) != nil || res.Removed {
			alive[id] = nil
			return nil
		}
		alive[id] = &res
		return &res
	}
	rowOf := func(res *models.Resource) ResourceRow { return ResourceRowOf(originFor(res, pid, view), view, folders) }
	out := &BrandSuggestOut{Site: rowOf(r), Logos: []BrandSuggestLogo{}, Fonts: []models.SiteFont{}, Why: []string{}}
	// The logo: the most logo-like picture still there; an icon only when the page shows no logo.
	isIcon := func(l models.SiteLogo) bool {
		return l.Kind == "touch-icon" || l.Kind == "icon" || l.Kind == "mask-icon" || l.Kind == "preview"
	}
	chosen, firstLogo := -1, -1
	for i, l := range b.Logos {
		if !isIcon(l) && firstLogo < 0 {
			firstLogo = i
		}
	}
	for _, wantIcon := range []bool{false, true} {
		for i, l := range b.Logos {
			if chosen < 0 && isIcon(l) == wantIcon && get(l.ResourceID) != nil {
				chosen = i
			}
		}
	}
	for i, l := range b.Logos {
		res := get(l.ResourceID)
		if res == nil {
			continue
		}
		out.Logos = append(out.Logos, BrandSuggestLogo{Resource: rowOf(res), Kind: l.Kind, Score: l.Score, URL: l.URL, Hints: l.Hints, Chosen: i == chosen})
	}
	colors := append([]models.SiteColor(nil), b.Colors...)
	var logoColors []string
	backdrop, inHeader := "", false
	if chosen >= 0 {
		l := b.Logos[chosen]
		res := get(l.ResourceID)
		row := rowOf(res)
		out.Logo = &row
		out.Kit.Logo = res.ID
		lv := res.Version(l.SHA256)
		if lv == nil {
			lv = res.Latest()
		}
		// The snapshot counted the most logo-like picture's colours already
		// (from: logo); another one chosen now adds its own.
		counted := chosen == firstLogo && slices.ContainsFunc(b.Colors, func(c models.SiteColor) bool { return c.From == "logo" })
		if lv != nil {
			logoColors = lv.Colors
			for i, c := range lv.Colors {
				if i < 6 && !counted {
					colors = append(colors, models.SiteColor{Hex: c, From: "logo", Weight: math.Max(0.2, 0.8-0.15*float64(i))})
				}
			}
		}
		backdrop, inHeader = l.Backdrop, containsString(l.Hints, "in the header")
		why = append(why, logoWhy(l, res.Title))
	} else {
		why = append(why, "Logo: MetaRush found none on the page; add the logo's file and choose it as the brand logo.")
	}
	merged := resources.MergeSiteColors(colors, 20)
	if b.Colors != nil {
		out.Colors = resources.MergeSiteColors(b.Colors, 20)
	} else {
		out.Colors = []models.SiteColor{}
	}
	cc := pickColors(merged, backdrop, inHeader)
	out.Kit.Primary, out.Kit.Accent, out.Kit.Text, out.Kit.Background = cc.Primary, cc.Accent, cc.Text, cc.Background
	out.Kit.Suggested = logoColors
	why = append(why, cc.Why...)
	font, fontRes, fontWhy := pickFont(b.Fonts, func(id string) bool { return get(id) != nil })
	out.Kit.Font, out.Kit.FontResource = font, fontRes
	if fontRes != "" {
		row := rowOf(get(fontRes))
		out.FontFile = &row
	}
	why = append(why, fontWhy)
	if b.Fonts != nil {
		out.Fonts = b.Fonts
	}
	out.Why = why
	out.Brand = kitTokens(&out.Kit)
	parts := []string{}
	if out.Kit.Logo != "" {
		parts = append(parts, "its logo")
	}
	parts = append(parts, "its colours")
	switch {
	case fontRes != "" || (font != "" && len(b.Fonts) > 0 && strings.EqualFold(font, b.Fonts[0].Family)):
		parts = append(parts, "its font")
	case font != "":
		parts = append(parts, "the nearest font MetaRush has")
	}
	out.Message = fmt.Sprintf("A brand kit from %s: %s.", firstNonEmpty(ss.Site, r.Title), joinAnd(parts))
	if p != nil {
		out.ProjectID, out.ProjectName = p.ID, p.Name
		if k, owner, err := kitFor(ctx, a, p.ID); err == nil && owner == p.ID {
			out.InUse = sameKit(k, &out.Kit)
		}
		if out.InUse {
			out.Message += fmt.Sprintf(" “%s” already uses it.", p.Name)
		}
	}
	return out, p, nil
}

// sameKit reports a project kit that already is the suggestion.
func sameKit(have, want *models.BrandKit) bool {
	if have == nil || want == nil || kitEmpty(have) {
		return false
	}
	eq := strings.EqualFold
	return have.Logo == want.Logo && eq(have.Primary, want.Primary) && eq(have.Accent, want.Accent) && eq(have.Text, want.Text) && eq(have.Background, want.Background) &&
		have.Font == want.Font && have.FontResource == want.FontResource
}

// brandShow is a brand as brand.show answers: its resources, the tokens new
// cuts get, and whose Content Settings hold it (owner; p the project asked
// about, nil for the default's).
func brandShow(ctx context.Context, a *app.App, k *models.BrandKit, p *models.Project, owner string) *BrandShowOut {
	out := &BrandShowOut{Kit: *k, Empty: kitEmpty(k)}
	view := &projectlinks.View{Names: map[string]string{}}
	g, _ := content.Load(ctx, a)
	pid := owner
	if p != nil {
		out.ProjectID, out.ProjectName, pid = p.ID, p.Name, p.ID
		out.Sites = brandSites(ctx, a, p.ID)
	}
	if pid != "" {
		if v, err := projectlinks.Load(ctx, a, pid); err == nil {
			view = v
		}
	}
	if g != nil {
		out.From = refOf(g, owner)
		out.ReadOnly = p != nil && owner != p.ID
		if owner != "" {
			for _, id := range g.UsedBy(owner) {
				if p != nil && id == p.ID {
					continue
				}
				out.UsedBy = append(out.UsedBy, *refOf(g, id))
			}
		}
	}
	folders := folderNames(ctx, a)
	row := func(id string) *ResourceRow {
		if id == "" {
			return nil
		}
		var r models.Resource
		if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), id, &r) != nil {
			return nil
		}
		x := ResourceRowOf(originFor(&r, pid, view), view, folders)
		return &x
	}
	out.Logo, out.LogoDark, out.FontFile = row(k.Logo), row(k.LogoDark), row(k.FontResource)
	out.LogoLight, out.LogoIcon, out.LogoWordmark = row(k.LogoLight), row(k.LogoIcon), row(k.LogoWordmark)
	out.Logos = make([]BrandLogoOut, 0, len(models.LogoKinds))
	for _, which := range models.LogoKinds {
		lo := BrandLogoOut{Which: which, Label: models.LogoLabel(which), Pin: LogoPin(which)}
		if id := *k.LogoField(which); id != "" {
			lo.Resource = row(id)
		}
		out.Logos = append(out.Logos, lo)
	}
	out.Brand = kitTokens(k)
	fromName := ""
	if out.From != nil {
		fromName = out.From.Name
	}
	switch {
	case owner == "":
		out.Message = "No project's settings are used here yet: give the project its own settings, then add a logo and MetaRush takes its colours from it."
	case out.ReadOnly:
		out.Message = fmt.Sprintf("“%s” uses the brand of “%s”'s settings: change it there, or give “%s” settings of its own.", p.Name, fromName, p.Name)
	case out.Empty:
		out.Message = fmt.Sprintf("No brand in “%s”'s settings yet: add your logo and MetaRush takes its colours from it.", fromName)
	case p == nil:
		out.Message = fmt.Sprintf("New projects start with “%s”'s brand. Use it in an existing cut with “Use the brand kit”.", fromName)
	default:
		out.Message = "New cuts start with this brand. Use it in an existing cut with “Use the brand kit”."
	}
	if n := len(out.UsedBy); n > 0 && !out.ReadOnly {
		out.Message += fmt.Sprintf(" Also used by %s.", pluralWord(n, "project"))
	}
	return out
}

// kitResource resolves a resource for a brand: whatever the project whose
// settings hold it can use (its own resources, the workspace library, linked
// projects'), where it is. Projects that use those settings can show it too.
func kitResource(ctx context.Context, a *app.App, ref string, wantFont bool, p *models.Project) (string, string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return "", "", nil
	}
	note := ""
	var r *models.Resource
	if e, _, err := findResource(ctx, a, ResourceRefIn{Ref: ref, ProjectID: p.ID}); err == nil {
		r = &e.Resource
	} else {
		// Another project's picture or font (kept from a conversation in a
		// project that uses these settings): the brand is for every project
		// using them, so it moves into the workspace library.
		var x models.Resource
		if ids.Valid(ids.Resource, ref) && a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), ref, &x) == nil && !x.Removed && x.Scope == models.ScopeProject {
			if perr := resources.Promote(ctx, a, &x); perr != nil {
				return "", "", perr
			}
			r = &x
			note = x.Title + " is in the workspace library now (the brand is used by every project using these settings)."
		} else {
			return "", "", err
		}
	}
	if wantFont && r.Kind != models.ResourceFont {
		return "", "", apperr.New(apperr.InvalidInput, "%s is a %s, not a font.", r.Title, resources.KindWord(r.Kind))
	}
	if !wantFont && r.Kind == models.ResourceFont {
		return "", "", apperr.New(apperr.InvalidInput, "%s is a font, not a logo.", r.Title)
	}
	if !wantFont && (r.Kind == models.ResourceRepo || r.Kind == models.ResourceWebsite) {
		return "", "", apperr.New(apperr.InvalidInput, "%s is a %s, not a picture: a logo is a picture, an SVG or a PDF.", r.Title, resources.KindWord(r.Kind))
	}
	return r.ID, note, nil
}

// changesKit reports a brand.set that changes something in the kit itself.
func (in *BrandSetIn) changesKit() bool {
	return in.Logo != nil || in.LogoDark != nil || in.LogoLight != nil || in.LogoIcon != nil || in.LogoWordmark != nil || in.FontResource != nil || in.Font != nil ||
		in.Primary != nil || in.Accent != nil || in.Text != nil || in.Background != nil || in.FromLogo || strings.TrimSpace(in.FromSite) != ""
}

// brandSet is brand.set: the brand of a project's own Content Settings (or,
// without a project, the default's), taken from a website snapshot in one
// step with fromSite, and with apply the change that puts it on the project's
// latest cut. The kit is saved together with that change: a dry run, or a
// change still waiting for its confirm token, saves nothing.
func brandSet(ctx context.Context, a *app.App, in *BrandSetIn) (*BrandShowOut, error) {
	pid, err := projectRefID(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	target, g, err := content.EditTarget(ctx, a, pid)
	if err != nil {
		return nil, err
	}
	p, err := resolveProject(ctx, a, target.ProjectID)
	if err != nil {
		return nil, err
	}
	k0, _, err := kitFor(ctx, a, p.ID)
	if err != nil {
		return nil, err
	}
	k := *k0
	var notes []string
	if strings.TrimSpace(in.FromSite) != "" {
		sug, _, err := suggestBrand(ctx, a, in.FromSite, p)
		if err != nil {
			return nil, err
		}
		if sug.Kit.Logo != "" {
			k.Logo = sug.Kit.Logo
		}
		k.Primary, k.Accent, k.Text, k.Background = sug.Kit.Primary, sug.Kit.Accent, sug.Kit.Text, sug.Kit.Background
		k.Font, k.FontResource, k.Suggested = sug.Kit.Font, sug.Kit.FontResource, sug.Kit.Suggested
		// The site's colours are a choice the person took: a new logo keeps them.
		k.ColorsEdited = true
		site := sug.Site.Title
		if sug.Site.Site != nil && sug.Site.Site.Site != "" {
			site = sug.Site.Site.Site
		}
		notes = append(notes, fmt.Sprintf("Took the logo, colours and font %s suggests.", site))
	}
	setRes := func(dst *string, ref *string, font bool) error {
		if ref == nil {
			return nil
		}
		id, note, err := kitResource(ctx, a, *ref, font, p)
		if err != nil {
			return err
		}
		*dst = id
		if note != "" {
			notes = append(notes, note)
		}
		return nil
	}
	oldLogo := k.Logo
	if err := setRes(&k.Logo, in.Logo, false); err != nil {
		return nil, err
	}
	if err := setRes(&k.LogoDark, in.LogoDark, false); err != nil {
		return nil, err
	}
	if err := setRes(&k.LogoLight, in.LogoLight, false); err != nil {
		return nil, err
	}
	if err := setRes(&k.LogoIcon, in.LogoIcon, false); err != nil {
		return nil, err
	}
	if err := setRes(&k.LogoWordmark, in.LogoWordmark, false); err != nil {
		return nil, err
	}
	if err := setRes(&k.FontResource, in.FontResource, true); err != nil {
		return nil, err
	}
	if in.Font != nil {
		f := strings.TrimSpace(*in.Font)
		switch f {
		case "", "Inter", "Fraunces", "JetBrains Mono":
			k.Font = f
		default:
			return nil, apperr.New(apperr.InvalidInput, "The bundled fonts are Inter, Fraunces and JetBrains Mono; for another font, add its file and use it as the brand font.")
		}
	}
	edited := false
	for _, kv := range []struct {
		dst *string
		v   *string
	}{{&k.Primary, in.Primary}, {&k.Accent, in.Accent}, {&k.Text, in.Text}, {&k.Background, in.Background}} {
		if kv.v == nil {
			continue
		}
		c := strings.ToUpper(strings.TrimSpace(*kv.v))
		if c != "" && !validHexColor(c) {
			return nil, apperr.New(apperr.InvalidInput, "%q isn't a colour; write it as #RRGGBB.", *kv.v)
		}
		*kv.dst = c
		edited = true
	}
	// The logo's colours: suggested always, applied unless a person (or a site) chose colours.
	var logoColors []string
	if k.Logo != "" {
		var r models.Resource
		if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), k.Logo, &r) == nil {
			if v := r.Latest(); v != nil {
				logoColors = v.Colors
			}
		}
	}
	logoChanged := k.Logo != oldLogo
	if logoChanged || in.FromLogo {
		k.Suggested = logoColors
	}
	if in.FromLogo {
		k.ColorsEdited = false
	}
	if edited {
		k.ColorsEdited = true
	}
	if (logoChanged || in.FromLogo) && !k.ColorsEdited && len(logoColors) > 0 {
		k.Primary, k.Accent, k.Text, k.Background = colorsFromLogo(logoColors)
		notes = append(notes, "Colours taken from the logo; change any of them and MetaRush keeps yours.")
	}
	k.UpdatedAt, k.UpdatedBy = time.Now().UTC(), a.Actor

	// With apply: the change to the latest cut, checked now and saved together with the kit.
	var cut *CutEditOut
	pending := in.DryRun
	if in.Apply {
		if p.Branches[firstNonEmpty(in.Branch, "main")].Head == "" {
			notes = append(notes, fmt.Sprintf("“%s” has no cut yet: its first cut starts with this brand.", p.Name))
		} else if kitEmpty(&k) {
			notes = append(notes, "There's nothing in the brand to put on the cut yet.")
		} else {
			c := CutEditCommon{ProjectID: p.ID, Branch: in.Branch, DryRun: in.DryRun, ConfirmToken: in.ConfirmToken, Instruction: firstNonEmpty(in.Instruction, "Use the brand kit")}
			cut, err = putKitOnCut(ctx, a, c, &k, true)
			if err != nil {
				return nil, err
			}
			if !cut.Saved && !cut.NoChange {
				pending = true // checked: it saves with its confirm token
			}
		}
	}
	changed := in.changesKit()
	if !pending && changed {
		if err := saveKit(ctx, a, p, &k); err != nil {
			return nil, err
		}
	}
	shown := p
	if target.Default && pid == "" {
		shown = nil
	}
	out := brandShow(ctx, a, &k, shown, p.ID)
	out.Cut, out.Pending = cut, pending
	if !pending && changed {
		skip := ""
		if cut != nil && cut.Saved {
			skip = p.ID
		}
		out.LatestCuts = brandOffers(ctx, a, p.ID, &k, skip)
	}
	switch {
	case pending && cut != nil && cut.Check != nil && !cut.Check.Valid:
		out.Message = "Nothing was saved: the change to the cut can't be made yet. " + cut.Summary
	case pending && cut != nil:
		out.Message = "Nothing is saved yet: this is the brand, and the change that puts it on the cut. " + strings.Join(notes, " ")
	case pending:
		out.Message = "Nothing is saved yet: this is how the brand would be. " + strings.Join(notes, " ")
	case len(notes) > 0:
		out.Message = strings.Join(notes, " ")
	case !changed:
		// Nothing changed in the brand: brandShow says what it is.
	default:
		out.Message = fmt.Sprintf("Saved the brand of “%s”'s settings.", p.Name)
	}
	if !pending && changed {
		if note := target.Note(g); note != "" {
			out.Message = strings.TrimSpace(out.Message + " " + note)
		}
	}
	if cut != nil && cut.Saved && cut.Applied != nil {
		out.Message = strings.TrimSpace(out.Message + " " + fmt.Sprintf("The cut has it now: version %d.", cut.Applied.Revision.Number))
	}
	if n := len(out.LatestCuts); n > 0 {
		out.Message = strings.TrimSpace(out.Message + " " + fmt.Sprintf("Saved versions don't change: %s can use the new brand on %s latest cut.", pluralWord(n, "project"), map[bool]string{true: "its", false: "their"}[n == 1]))
	}
	out.Message = strings.TrimSpace(out.Message)
	return out, nil
}

// saveKit stores the brand of a project's own Content Settings.
func saveKit(ctx context.Context, a *app.App, p *models.Project, k *models.BrandKit) error {
	if err := content.Save(ctx, a, p.ID, bson.M{"brandKit": k}, nil); err != nil {
		return err
	}
	a.Audit(ctx, "brand.set", "project", p.ID, p.ID, map[string]any{"logo": k.Logo, "logoDark": k.LogoDark, "logoLight": k.LogoLight, "logoIcon": k.LogoIcon,
		"logoWordmark": k.LogoWordmark, "font": k.Font, "fontResource": k.FontResource, "colorsEdited": k.ColorsEdited})
	return nil
}

// brandOffers are the latest cuts of the projects using owner's settings that
// would change with the brand k (skip: one already done).
func brandOffers(ctx context.Context, a *app.App, owner string, k *models.BrandKit, skip string) []BrandCutOffer {
	users, _, err := contentUsers(ctx, a, owner)
	if err != nil || kitEmpty(k) {
		return nil
	}
	var out []BrandCutOffer
	for _, id := range users {
		if id == skip {
			continue
		}
		t, err := loadEditTarget(ctx, a, CutEditCommon{ProjectID: id})
		if err != nil || t.project.Archived {
			continue
		}
		if len(kitOps(ctx, a, t, k)) == 0 {
			continue
		}
		out = append(out, BrandCutOffer{ProjectID: id, ProjectName: t.project.Name, Branch: t.branch, Version: t.rev.Number, Title: t.project.Branches[t.branch].Title})
	}
	return out
}

func init() {
	Def("brand.show", "Show a brand kit — the brand of the Content Settings a project uses (its own, another project's or the default's) or, without a project, the default's — its logos, colours and font, the brand new cuts get, and the project's website snapshots a kit can be suggested from", Flags{}, func(ctx context.Context, a *app.App, in *BrandShowIn) (*BrandShowOut, error) {
		var p *models.Project
		pid := ""
		if strings.TrimSpace(in.ProjectID) != "" {
			x, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			p, pid = x, x.ID
		}
		k, owner, err := kitFor(ctx, a, pid)
		if err != nil {
			return nil, err
		}
		return brandShow(ctx, a, k, p, owner), nil
	})
	Def("brand.set", "Change the brand of a project's own Content Settings (without a project, the default's — what new projects use): logos (colours are taken from the logo unless a person set them), colours and font; or take what a website snapshot suggests (fromSite) and put it on the project's latest cut (apply, a checked change)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *BrandSetIn) (*BrandShowOut, error) {
		return brandSet(ctx, a, in)
	})
	Def("brand.suggest", "Suggest a brand kit from a website snapshot: its logo, four colours (primary, accent, text, background) and a font, each with why; nothing is saved (brand.set with fromSite takes it)", Flags{}, func(ctx context.Context, a *app.App, in *BrandSuggestIn) (*BrandSuggestOut, error) {
		var p *models.Project
		if strings.TrimSpace(in.ProjectID) != "" {
			x, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			p = x
		}
		out, _, err := suggestBrand(ctx, a, in.From, p)
		return out, err
	})
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("brand.suggest", Offering{ID: "brand-from-a-site", Group: "make", Title: "A website's brand",
		Does:  "Suggests a brand kit from a web page you brought in: its logo, colours and font, each with why.",
		Words: []string{"brand colours", "logo from a website", "style from a site"}})
}

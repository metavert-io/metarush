package editspec

import (
	"encoding/json"
	"math"
	"strconv"
	"strings"
)

// Logos made for their background (D198). A brand can have a main logo and
// logos made for dark and for light backgrounds (and a square icon and a
// wordmark, which graphics name themselves). A cut whose brand names its main
// logo (brand.logo) lets every vetted template that shows that logo show the
// one made for its background instead: the logo for dark backgrounds on a dark
// background, the one for light backgrounds on a light one — by contrast with
// the colour the template draws behind the logo — else the main one. Go makes
// the choice when it resolves the plan, so the renderer draws the plan as it is
// and the preview matches. Cuts without brand.logo (every cut made before)
// show exactly the resource each graphic names.

// logoOnBackground are the templates that draw their logo on the brand's
// background colour: cards and slides fill with it, lower thirds and the name
// bar lay it behind the words. logo.corner sits on the footage, whose colour
// isn't known, so it keeps the main logo.
var logoOnBackground = map[string]bool{
	TplTitleCard: true, TplEndCard: true, TplLowerThirdClean: true, TplLowerThirdCinematic: true,
	TplNameBar: true, TplSlideTitle: true,
}

// LogoBackground is the colour a template draws its logo on ("" when it is the
// footage's, which isn't known: logo.corner and project motion graphics).
func LogoBackground(template string, b Brand) string {
	if !logoOnBackground[template] {
		return ""
	}
	return b.Background
}

// Luminance is a #RRGGBB colour's relative luminance (WCAG): 0 black, 1 white.
// ok is false for anything that isn't such a colour.
func Luminance(hex string) (float64, bool) {
	h := strings.TrimPrefix(strings.TrimSpace(hex), "#")
	if len(h) != 6 {
		return 0, false
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return 0, false
	}
	lin := func(c float64) float64 {
		if c <= 0.03928 {
			return c / 12.92
		}
		return math.Pow((c+0.055)/1.055, 2.4)
	}
	r, g, b := float64(v>>16&0xff)/255, float64(v>>8&0xff)/255, float64(v&0xff)/255
	return 0.2126*lin(r) + 0.7152*lin(g) + 0.0722*lin(b), true
}

// DarkBackground reports a colour white stands out on more than black does
// (WCAG contrast): a logo made for dark backgrounds belongs on it. ok is false
// when the colour isn't known.
func DarkBackground(hex string) (dark, ok bool) {
	l, ok := Luminance(hex)
	if !ok {
		return false, false
	}
	return (1.05)/(l+0.05) >= (l+0.05)/0.05, true
}

// PickLogo is the resource a template showing ref draws (D198): when ref is the
// brand's main logo, the logo made for the template's background if the cut
// has it (pinned reports a resource the cut pins), else ref itself.
func PickLogo(b Brand, template, ref string, pinned func(name string) bool) string {
	if b.Logo == "" || ref != b.Logo {
		return ref
	}
	dark, ok := DarkBackground(LogoBackground(template, b))
	if !ok {
		return ref
	}
	want := b.LogoLight
	if dark {
		want = b.LogoDark
	}
	if want == "" || want == ref || (pinned != nil && !pinned(want)) {
		return ref
	}
	return want
}

// pickPlanLogo is a graphic's fields as the plan draws them: its logo the one
// made for its background (PickLogo). b is the cut's brand over MetaRush's
// defaults; fields without a logo, or a logo that stays, come back unchanged
// (the same bytes).
func pickPlanLogo(params json.RawMessage, b Brand, template string, pins map[string]ResourcePin) json.RawMessage {
	if b.Logo == "" || len(params) == 0 || !logoTemplate(template) {
		return params
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(params, &m) != nil {
		return params
	}
	raw, ok := m["logo"]
	if !ok {
		return params
	}
	var ref string
	if json.Unmarshal(raw, &ref) != nil || ref == "" {
		return params
	}
	got := PickLogo(b, template, ref, func(n string) bool { _, ok := pins[n]; return ok })
	if got == ref {
		return params
	}
	m["logo"], _ = json.Marshal(got)
	out, err := json.Marshal(m)
	if err != nil {
		return params
	}
	return out
}

// logoTemplate reports a vetted template with a logo field.
func logoTemplate(id string) bool {
	t, ok := TemplateByID(id)
	if !ok {
		return false
	}
	for _, p := range t.ResourceParams {
		if p == "logo" {
			return true
		}
	}
	return false
}

// BrandLogoNames are the brand's logo names a cut's brand gives (main, for dark
// and for light backgrounds), in that order, those that are set.
func BrandLogoNames(b *Brand) []string {
	if b == nil {
		return nil
	}
	var out []string
	for _, n := range []string{b.Logo, b.LogoDark, b.LogoLight} {
		if n != "" {
			out = append(out, n)
		}
	}
	return out
}

package editspec

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Resource kinds as the spec sees them (models.Resource* use the same values).
const (
	ResourceKindImage = "image"
	ResourceKindSVG   = "svg"
	ResourceKindPDF   = "pdf"
	ResourceKindFont  = "font"
	// A repository isn't drawn (slides show the code pictures and diagrams
	// made from it); a website snapshot draws its screenshots (name#2, #3).
	ResourceKindRepo    = "repo"
	ResourceKindWebsite = "website"
)

// hasPages reports a kind whose page pictures a graphic can name (name#3).
func hasPages(kind string) bool { return kind == ResourceKindPDF || kind == ResourceKindWebsite }

// ResourceNamePattern is a pin's short name.
var ResourceNamePattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,47}$`)

var resourceRefPattern = regexp.MustCompile(`^([a-z][a-z0-9_-]{0,47})(?:#([1-9][0-9]{0,2}))?$`)

// ParseResourceRef splits a parameter value: "logo" → (logo, 0), "deck#3" → (deck, 3).
func ParseResourceRef(ref string) (name string, page int, ok bool) {
	m := resourceRefPattern.FindStringSubmatch(strings.TrimSpace(ref))
	if m == nil {
		return "", 0, false
	}
	if m[2] != "" {
		page, _ = strconv.Atoi(m[2])
	}
	return m[1], page, true
}

// ResourceInfo is what validation and planning know about one pinned
// resource version.
type ResourceInfo struct {
	ID    string
	Name  string // its name in the library (may differ from the pin's short name)
	Title string
	Kind  string // image | svg | pdf | font
	// Version is the pinned version's facts; Known is false when that version
	// isn't one of the resource's.
	Known  bool
	SHA256 string
	MIME   string
	Width  int
	Height int
	Pages  []PlanResourcePage
	Family string
	// URL is a website snapshot's page (shown in a slide's browser frame).
	URL string
	// Visible: the project can use it (its own, the workspace library's, or
	// through a project link).
	Visible bool
	Removed bool
	// Available: every machine can get the bytes (stored), or a machine that
	// has them can upload them.
	Available bool
	// Latest is the resource's newest version ("a newer version is available").
	Latest string
}

// ResourceFacts is optionally implemented by Facts: the resources a spec pins.
type ResourceFacts interface {
	Resource(id, sha string) (ResourceInfo, bool)
}

func resourceInfo(f Facts, pin ResourcePin) (ResourceInfo, bool) {
	if rf, ok := f.(ResourceFacts); ok && f != nil {
		return rf.Resource(pin.ID, pin.SHA256)
	}
	return ResourceInfo{}, false
}

// resourceParamsOf lists a template's parameters marked format=metarush-resource.
func resourceParamsOf(params any) []string {
	if params == nil {
		return nil
	}
	t := reflect.TypeOf(params)
	var out []string
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if strings.Contains(f.Tag.Get("jsonschema"), "format="+ResourceFormat) {
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			out = append(out, name)
		}
	}
	return out
}

// ResourceParamsInSchema lists the top-level parameters of a JSON Schema that
// name resources (format metarush-resource): how project motion graphics
// (part 2) declare theirs.
func ResourceParamsInSchema(schema json.RawMessage) []string {
	var s struct {
		Properties map[string]struct {
			Format string `json:"format"`
		} `json:"properties"`
	}
	if json.Unmarshal(schema, &s) != nil {
		return nil
	}
	var out []string
	for k, p := range s.Properties {
		if p.Format == ResourceFormat {
			out = append(out, k)
		}
	}
	sort.Strings(out)
	return out
}

// ResourceUse is one place a cut names a resource.
type ResourceUse struct {
	Name    string // the pin's short name
	Page    int    // a PDF page (0: the file itself)
	Path    string // JSON pointer (/overlays/2/params/logo, /brand/fontFile)
	Overlay string // the graphic's ID
	Param   string
	Font    bool // brand.fontFile
	// Motion: a name a project motion graphic's code uses (useResource("logo"), D107).
	Motion string
	// BrandLogo: one of the brand's logos (brand.logo, logoDark, logoLight; D198).
	BrandLogo bool
}

// ResourceUses lists every place the spec names a resource, in spec order.
// Motion graphics need their parameter schema (facts) to say which params do.
func ResourceUses(s *Spec, facts Facts) []ResourceUse {
	var out []ResourceUse
	for i, o := range s.Overlays {
		var params []string
		switch {
		case o.Motion != nil:
			// Names the component's own code uses (checked when it was
			// checked), then its shared libraries' (a theme's fonts, D171).
			code := func(info MotionInfo) {
				for _, ref := range info.Resources {
					name, page, ok := ParseResourceRef(ref)
					if !ok {
						name = ref
					}
					out = append(out, ResourceUse{Name: name, Page: page, Path: fmt.Sprintf("/overlays/%d/motion", i), Overlay: o.ID, Motion: o.Motion.Name})
				}
				for _, name := range info.Fonts {
					out = append(out, ResourceUse{Name: name, Path: fmt.Sprintf("/overlays/%d/motion", i), Overlay: o.ID, Motion: o.Motion.Name, Font: true})
				}
			}
			if info, ok := motionInfo(facts, o.Motion.Hash); ok {
				params = ResourceParamsInSchema(info.Schema)
				code(info)
			}
			libs := make([]string, 0, len(o.Motion.Libs))
			for n := range o.Motion.Libs {
				libs = append(libs, n)
			}
			sort.Strings(libs)
			for _, n := range libs {
				if info, ok := motionInfo(facts, o.Motion.Libs[n]); ok {
					code(info)
				}
			}
		default:
			if t, ok := TemplateByID(o.Template); ok {
				params = t.ResourceParams
			}
		}
		if len(params) == 0 || len(o.Params) == 0 {
			continue
		}
		var m map[string]json.RawMessage
		if json.Unmarshal(o.Params, &m) != nil {
			continue
		}
		for _, p := range params {
			raw, ok := m[p]
			if !ok {
				continue
			}
			var v string
			if json.Unmarshal(raw, &v) != nil || v == "" {
				continue
			}
			name, page, ok := ParseResourceRef(v)
			if !ok {
				name = v
			}
			out = append(out, ResourceUse{Name: name, Page: page, Path: fmt.Sprintf("/overlays/%d/params/%s", i, p), Overlay: o.ID, Param: p})
		}
	}
	if s.Brand != nil && s.Brand.FontFile != "" {
		out = append(out, ResourceUse{Name: s.Brand.FontFile, Path: "/brand/fontFile", Font: true})
	}
	if b := s.Brand; b != nil && b.Logo != "" {
		// The brand's logos (D198): a graphic showing the main one may show
		// one made for its background, so the cut uses them all.
		for _, l := range []struct{ name, field string }{{b.Logo, "logo"}, {b.LogoDark, "logoDark"}, {b.LogoLight, "logoLight"}} {
			if l.name != "" {
				out = append(out, ResourceUse{Name: l.name, Path: "/brand/" + l.field, Param: l.field, BrandLogo: true})
			}
		}
	}
	return out
}

// ResourceNames lists the pinned short names, sorted.
func ResourceNames(s *Spec) []string {
	out := make([]string, 0, len(s.Resources))
	for k := range s.Resources {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ResourceIDs lists the distinct resources a spec pins, sorted by name.
func ResourceIDs(s *Spec) []string {
	var out []string
	seen := map[string]bool{}
	for _, n := range ResourceNames(s) {
		id := s.Resources[n].ID
		if !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// validateResources checks the pins and every place a resource is named.
func validateResources(s *Spec, facts Facts) []Issue {
	var out []Issue
	for _, n := range ResourceNames(s) {
		pin := s.Resources[n]
		p := "/resources/" + n
		if !ResourceNamePattern.MatchString(n) {
			out = append(out, errI("bad_resource_name", p, "%q can't be a resource's short name: use lowercase letters, digits, - and _, starting with a letter.", n))
			continue
		}
		info, ok := resourceInfo(facts, pin)
		switch {
		case !ok:
			out = append(out, errI("resource_unknown", p, "The cut uses a resource called %s that isn't in this workspace any more; add the file again or stop using it.", n))
			continue
		case !info.Known:
			out = append(out, errI("resource_version_unknown", p+"/sha256", "The version of %s (%s) that this cut uses (%s) isn't in the library; use the version the library has now.", n, info.Title, shortHash(pin.SHA256)))
			continue
		case !info.Visible:
			out = append(out, errI("resource_not_in_project", p, "%s (%s) belongs to another project; link that project to this one first, or add the file to this project.", n, info.Title))
		case !info.Available:
			out = append(out, warnI("resource_not_uploaded", p, "%s (%s) is only on the machine that has its folder; it uploads when this cut is saved, so other machines can render it.", n, info.Title))
		}
		if info.Removed {
			out = append(out, warnI("resource_removed", p, "%s (%s) was removed from the library, but this cut still uses it.", n, info.Title))
		}
	}
	for _, u := range ResourceUses(s, facts) {
		pin, ok := s.Resources[u.Name]
		if !ok {
			if !ResourceNamePattern.MatchString(u.Name) {
				out = append(out, errI("bad_resource_name", u.Path, "%q isn't a resource name (lowercase letters, digits, - and _; a PDF page as name#3).", u.Name))
				continue
			}
			if u.Motion != "" {
				out = append(out, errI("resource_unpinned", u.Path, "The motion graphic %s uses %s, which isn't one of this cut's resources yet; add it to the cut's resources (checking a cut sheet does that for you).", u.Motion, u.Name))
				continue
			}
			if u.BrandLogo {
				out = append(out, errI("resource_unpinned", u.Path, "The brand's logo %s isn't one of this cut's resources; add it to the cut's resources, or take it out of the brand (Use the brand kit does both).", u.Name))
				continue
			}
			out = append(out, errI("resource_unpinned", u.Path, "%s isn't one of this cut's resources yet; add it to the cut's resources (checking a cut sheet does that for you).", u.Name))
			continue
		}
		info, ok := resourceInfo(facts, pin)
		if !ok || !info.Known {
			continue // reported on the pin
		}
		what := "the graphic " + u.Overlay
		if u.Motion != "" {
			what = "the motion graphic " + u.Motion
		}
		if u.BrandLogo {
			what = "the brand's logo"
		}
		switch {
		case u.Font && info.Kind != ResourceKindFont:
			out = append(out, errI("resource_kind", u.Path, "%s is a %s, not a font; choose a font file.", u.Name, kindWord(info.Kind)))
		case !u.Font && info.Kind == ResourceKindFont:
			out = append(out, errI("resource_kind", u.Path, "%s is a font, but %s needs a picture there.", u.Name, what))
		case !u.Font && info.Kind == ResourceKindRepo:
			out = append(out, errI("resource_kind", u.Path, "%s is a code repository, not a picture; show a code picture, diagram or stats card made from it instead.", u.Name))
		case u.Page > 0 && !hasPages(info.Kind):
			out = append(out, errI("resource_page", u.Path, "%s is a %s, which has no pages (#%d is for PDFs and web page snapshots).", u.Name, kindWord(info.Kind), u.Page))
		case u.Page > 0 && info.Kind == ResourceKindWebsite && u.Page > len(info.Pages):
			out = append(out, errI("resource_page", u.Path, "%s has %d pictures (#1 for a computer, #2 for a phone, #3 the whole page); there is no #%d.", u.Name, len(info.Pages), u.Page))
		case u.Page > 0 && u.Page > len(info.Pages):
			if len(info.Pages) == 0 {
				out = append(out, errI("resource_page", u.Path, "The pages of %s haven't been turned into pictures yet; that needs the free poppler tool on this machine, then refresh the PDF.", u.Name))
			} else {
				out = append(out, errI("resource_page", u.Path, "%s has %d pages; there is no page %d.", u.Name, len(info.Pages), u.Page))
			}
		case u.Page == 0 && info.Kind == ResourceKindPDF:
			if len(info.Pages) == 0 {
				out = append(out, errI("resource_page", u.Path, "%s is a PDF whose pages haven't been turned into pictures yet; that needs the free poppler tool on this machine, then refresh the PDF.", u.Name))
			}
		}
	}
	return out
}

func kindWord(k string) string {
	switch k {
	case ResourceKindSVG:
		return "vector picture"
	case ResourceKindPDF:
		return "PDF"
	case ResourceKindFont:
		return "font"
	case ResourceKindRepo:
		return "repository"
	case ResourceKindWebsite:
		return "web page snapshot"
	}
	return "picture"
}

// PlanResource is a pinned resource as the renderer draws it.
type PlanResource struct {
	ID     string `json:"id"`
	SHA256 string `json:"sha256"`
	Kind   string `json:"kind" jsonschema:"enum=image,enum=svg,enum=pdf,enum=font,enum=repo,enum=website"`
	MIME   string `json:"mime,omitempty"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
	// Pages are a PDF's page pictures (page 1 first).
	Pages []PlanResourcePage `json:"pages,omitempty"`
	// Family is a font's family name.
	Family string `json:"family,omitempty"`
	Title  string `json:"title,omitempty"`
	// URL is a web page snapshot's address (a slide's browser frame shows it).
	URL string `json:"url,omitempty"`
}

// PlanResourcePage is one page picture of a PDF.
type PlanResourcePage struct {
	Number int    `json:"number"`
	SHA256 string `json:"sha256"`
	Width  int    `json:"width"`
	Height int    `json:"height"`
}

// planResources resolves the pins for the renderer.
func planResources(s *Spec, facts Facts) map[string]PlanResource {
	if len(s.Resources) == 0 {
		return nil
	}
	out := map[string]PlanResource{}
	for _, n := range ResourceNames(s) {
		pin := s.Resources[n]
		pr := PlanResource{ID: pin.ID, SHA256: pin.SHA256, Kind: ResourceKindImage}
		if info, ok := resourceInfo(facts, pin); ok && info.Known {
			pr.Kind, pr.MIME, pr.Width, pr.Height, pr.Pages, pr.Family, pr.Title, pr.URL = info.Kind, info.MIME, info.Width, info.Height, info.Pages, info.Family, info.Title, info.URL
		}
		out[n] = pr
	}
	return out
}

// ResourceFiles lists the files a plan draws: each pinned version's file (or
// a PDF's page pictures that are used) by SHA-256, the keys of
// RenderPlan.ResourceSources.
func ResourceFiles(plan *RenderPlan, s *Spec, facts Facts) []string {
	var out []string
	seen := map[string]bool{}
	add := func(sha string) {
		if sha != "" && !seen[sha] {
			seen[sha] = true
			out = append(out, sha)
		}
	}
	for _, u := range ResourceUses(s, facts) {
		pr, ok := plan.Resources[u.Name]
		if !ok {
			continue
		}
		if u.Page > 0 {
			if u.Page <= len(pr.Pages) {
				add(pr.Pages[u.Page-1].SHA256)
			}
			continue
		}
		if pr.Kind == ResourceKindPDF {
			if len(pr.Pages) > 0 {
				add(pr.Pages[0].SHA256)
			}
			continue
		}
		add(pr.SHA256)
	}
	return out
}

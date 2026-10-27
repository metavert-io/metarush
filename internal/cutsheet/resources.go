package cutsheet

import (
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Resources (D82) and nested cuts (D84) in a cut sheet.
//
// The settings pin every resource the cut uses, like footage, at its exact
// version:
//
//	resources:
//	  logo: res_… sha256:<64 hex>   # “Acme logo” · SVG · a newer version is available
//
// and graphics name them in their fields (`· with title "Hello", logo "logo"`;
// a PDF page is "deck#3"). A name used without a pin is looked up when the
// sheet is checked (ResourceLookup) and pinned to the version the library has
// now, with a note, so "use our logo" is one word.
//
// A clip can play another project's cut, pinned to one version:
//
//	cuts:
//	  brand: prj_…/rev_…   # “Brand intro” · Brand · version 4 · 0:12.5
//	## Clips
//	3. intro: cut from brand 0:00 → 0:12.5
//
// `cut from Brand r4` (a project's name and a version number) works without a
// pin when the sheet is checked (CutLookup).

// ResourceDetails is what a sheet's comments say about a pinned resource.
type ResourceDetails struct {
	Title string
	Kind  string // image | svg | pdf | font
	// Newer: the library has a newer version than the pinned one.
	Newer bool
	// From names the linked project it comes from ("" for this project or the workspace library).
	From string
}

// MotionResourceLookup is an optional part of Info: the resource names a
// project motion graphic's code uses (pinned like a graphic's fields).
type MotionResourceLookup interface {
	MotionResources(hash string) []string
}

// ResourceLookup is an optional part of Info: the resources the project can use.
type ResourceLookup interface {
	ResourceDetails(id, sha string) (ResourceDetails, bool)
	// ResourceByName finds a resource by its short name or title and returns
	// its current version, for names a sheet uses without a pin.
	ResourceByName(name string) (editspec.ResourcePin, bool)
}

// CutDetails is what a sheet says about a nested cut.
type CutDetails struct {
	ProjectName string
	Title       string
	Number      int
	Duration    float64
	// NewerNumber is the newest version of that cut when it is newer.
	NewerNumber int
}

// CutLookup is an optional part of Info: other projects' cuts.
type CutLookup interface {
	CutDetails(key string) (CutDetails, bool)
	// FindCut resolves a project (name or ID) and a version number (0: the
	// latest) to a nested cut's source key; problem says why not, in words.
	FindCut(project string, number int) (key, problem string)
}

type resourcePin struct {
	pin  editspec.ResourcePin
	span Span
}

type cutPin struct {
	key  string
	span Span
}

var (
	resourceIDRe = regexp.MustCompile(`^res_[0-9a-z]{26}$`)
	fullShaRe    = regexp.MustCompile(`^sha256:([0-9a-f]{64})$`)
	cutKeyRe     = regexp.MustCompile(`^(prj_[0-9a-z]{26})/(rev_[0-9a-z]{26})$`)
	revWordRe    = regexp.MustCompile(`^(?i:r|v|version)?(\d+)$`)
)

// ---- resources ----

// resourceList reads `resources:` — name → res_… sha256:<full>.
func (p *parser) resourceList(v *yaml.Node, off int) {
	if v.Kind != yaml.MappingNode {
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_resource", "resources lists the pictures, logos and fonts the cut uses, with their exact versions, one per line.", "Example:   logo: res_… sha256:… (copy both from a sheet MetaRush wrote)")
		return
	}
	for j := 0; j+1 < len(v.Content); j += 2 {
		k, val := v.Content[j], v.Content[j+1]
		name := k.Value
		ks := nodeSpan(k, off)
		if !editspec.ResourceNamePattern.MatchString(name) {
			p.errAt(ks.Line, ks.Col, ks.EndCol, "bad_resource", fmt.Sprintf("%q can't be a resource's short name here.", name), "Use lowercase letters, digits, - and _, starting with a letter (like logo or team-photo).")
			continue
		}
		if _, dup := p.resources[name]; dup {
			p.errAt(ks.Line, ks.Col, ks.EndCol, "duplicate", fmt.Sprintf("The resource %s is listed twice.", name), "List each one once.")
			continue
		}
		s, _ := scalarText(val)
		parts := strings.Fields(s)
		if len(parts) != 2 || !resourceIDRe.MatchString(parts[0]) || !fullShaRe.MatchString(parts[1]) {
			p.errAt(val.Line+off, val.Column, val.Column+max(1, len([]rune(s))), "bad_resource", fmt.Sprintf("%s should be a resource ID (res_…) and its exact version (sha256: and 64 characters).", name), "Copy the line from a sheet MetaRush wrote, or leave the name out here: a graphic that uses it pins the current version when the sheet is checked.")
			continue
		}
		p.resources[name] = resourcePin{pin: editspec.ResourcePin{ID: parts[0], SHA256: fullShaRe.FindStringSubmatch(parts[1])[1]}, span: ks}
	}
}

func (p *printer) resourceSettings() {
	names := editspec.ResourceNames(p.s)
	if len(names) == 0 {
		return
	}
	p.line("resources:")
	width := 0
	for _, n := range names {
		width = max(width, len(n))
	}
	rl, _ := p.o.Info.(ResourceLookup)
	for _, n := range names {
		pin := p.s.Resources[n]
		l := "  " + n + ":" + strings.Repeat(" ", width-len(n)+1) + pin.ID + " sha256:" + pin.SHA256
		if rl != nil && p.o.Info != nil {
			if d, ok := rl.ResourceDetails(pin.ID, pin.SHA256); ok {
				var c []string
				if t := oneLine(d.Title); t != "" {
					c = append(c, "“"+t+"”")
				}
				if k := resourceKindWord(d.Kind); k != "" {
					c = append(c, k)
				}
				if d.From != "" {
					c = append(c, "from "+oneLine(d.From))
				}
				if d.Newer {
					c = append(c, "a newer version is available")
				}
				if len(c) > 0 {
					l += "   # " + strings.Join(c, " · ")
				}
			}
		}
		p.line(l)
	}
}

func resourceKindWord(k string) string {
	switch k {
	case editspec.ResourceKindSVG:
		return "SVG"
	case editspec.ResourceKindPDF:
		return "PDF"
	case editspec.ResourceKindFont:
		return "font"
	case editspec.ResourceKindImage:
		return "picture"
	}
	return ""
}

// pinResources sets spec.resources from the settings, and pins names that
// graphics use without a pin (when the sheet is checked with a lookup).
func (p *parser) pinResources(spec *editspec.Spec) {
	if len(p.resources) > 0 {
		spec.Resources = map[string]editspec.ResourcePin{}
		for n, rp := range p.resources {
			spec.Resources[n] = rp.pin
			p.sm.Keys["resources."+n] = rp.span
		}
	}
	rl, _ := p.o.Info.(ResourceLookup)
	uses := editspec.ResourceUses(spec, nil)
	// Names a project motion graphic's code uses (useResource("logo"), D107).
	if ml, ok := p.o.Info.(MotionResourceLookup); ok && p.o.Info != nil {
		for i, o := range spec.Overlays {
			if o.Motion == nil {
				continue
			}
			// Its own code's names, then its shared libraries' (fonts, D171).
			hashes := []string{o.Motion.Hash}
			libs := make([]string, 0, len(o.Motion.Libs))
			for n := range o.Motion.Libs {
				libs = append(libs, n)
			}
			sort.Strings(libs)
			for _, n := range libs {
				hashes = append(hashes, o.Motion.Libs[n])
			}
			for _, h := range hashes {
				for _, ref := range ml.MotionResources(h) {
					name, page, ok := editspec.ParseResourceRef(ref)
					if !ok {
						continue
					}
					uses = append(uses, editspec.ResourceUse{Name: name, Page: page, Path: fmt.Sprintf("/overlays/%d/motion", i), Overlay: o.ID, Motion: o.Motion.Name})
				}
			}
		}
	}
	for _, u := range uses {
		if _, ok := spec.Resources[u.Name]; ok || !editspec.ResourceNamePattern.MatchString(u.Name) {
			continue
		}
		line := p.lineOfUse(u)
		if rl == nil || p.o.Info == nil {
			continue // validation reports the unpinned name
		}
		pin, ok := rl.ResourceByName(u.Name)
		if !ok {
			p.errAt(line, 1, 2, "resource_unknown", fmt.Sprintf("There's no resource called %s in this project.", u.Name), "The project's Resources tab shows what it can use; add the file there first if it's missing.")
			continue
		}
		if spec.Resources == nil {
			spec.Resources = map[string]editspec.ResourcePin{}
		}
		spec.Resources[u.Name] = pin
		p.warnAt(line, 1, 2, "resource_pinned", fmt.Sprintf("Pinned %s to the version the library has now.", u.Name), "The settings list it under resources: once the sheet is saved; a newer version later is only used when you update the cut to it.")
	}
}

// lineOfUse is the sheet line of a graphic (or the brand) that names a resource.
func (p *parser) lineOfUse(u editspec.ResourceUse) int {
	if u.Font || u.BrandLogo {
		if sp, ok := p.sm.Keys["brand"]; ok {
			return sp.Line
		}
		return 1
	}
	if pos, ok := p.sm.Overlays[u.Overlay]; ok {
		return pos.Line
	}
	return 1
}

// ---- nested cuts ----

// cutList reads `cuts:` — name → prj_…/rev_….
func (p *parser) cutList(v *yaml.Node, off int) {
	if v.Kind != yaml.MappingNode {
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_cut", "cuts lists other projects' cuts this cut plays, each at its exact version, one per line.", "Example:   brand: prj_…/rev_…")
		return
	}
	for j := 0; j+1 < len(v.Content); j += 2 {
		k, val := v.Content[j], v.Content[j+1]
		name := k.Value
		ks := nodeSpan(k, off)
		if !validFootageName(name) {
			p.errAt(ks.Line, ks.Col, ks.EndCol, "bad_cut", fmt.Sprintf("%q can't name a cut here.", name), "Use lowercase letters, digits and dashes (like brand-intro).")
			continue
		}
		if _, dup := p.cutPins[name]; dup {
			p.errAt(ks.Line, ks.Col, ks.EndCol, "duplicate", fmt.Sprintf("The cut %s is listed twice.", name), "List each one once.")
			continue
		}
		s, _ := scalarText(val)
		m := cutKeyRe.FindStringSubmatch(strings.TrimSpace(s))
		if m == nil {
			p.errAt(val.Line+off, val.Column, val.Column+max(1, len([]rune(s))), "bad_cut", fmt.Sprintf("%s should be a project and one of its versions: prj_…/rev_….", name), "Copy the line from a sheet MetaRush wrote, or write cut from <project> r<version> on the clip line instead.")
			continue
		}
		p.cutPins[name] = cutPin{key: editspec.NestedKey(m[1], m[2]), span: ks}
		p.cutOrder = append(p.cutOrder, name)
	}
}

// startsNested reports a clip line that plays a cut: `cut from …`, or `cut
// <name>` with a name listed under cuts:.
func (p *parser) startsNested(head []token, i int) bool {
	if i+1 >= len(head) || !head[i].is("cut") {
		return false
	}
	if head[i+1].is("from") {
		return true
	}
	_, pinned := p.cutPins[head[i+1].text]
	return head[i+1].kind == tWord && pinned
}

// cutRef reads `cut [from] <name | project words> [rN]` at head[i] and
// returns the source key and how many tokens it used.
func (p *parser) cutRef(head []token, i, line int) (string, int, bool) {
	j := i + 1
	if j < len(head) && head[j].is("from") {
		j++
	}
	start := j
	var words []string
	number := 0
	for j < len(head) && head[j].kind == tWord && !looksLikeTime(head[j].text) && !head[j].is("from", "to") {
		if m := revWordRe.FindStringSubmatch(head[j].text); m != nil && len(words) > 0 {
			number, _ = strconv.Atoi(m[1])
			j++
			break
		}
		words = append(words, head[j].text)
		j++
	}
	if len(words) == 0 {
		c := head[i].col
		p.errAt(line, c, head[min(len(head)-1, i+1)].end, "bad_cut", "Say which cut this clip plays: cut from <name> (from the cuts: list) or cut from <project> r<version>.", "Like: 3. intro: cut from brand 0:00 → 0:12.5")
		return "", 0, false
	}
	sp := Span{line, head[start].col, head[j-1].end}
	if len(words) == 1 && number == 0 {
		if cp, ok := p.cutPins[words[0]]; ok {
			return cp.key, j - i, true
		}
	}
	cl, _ := p.o.Info.(CutLookup)
	if cl == nil || p.o.Info == nil {
		var names []string
		names = append(names, p.cutOrder...)
		hint := "List it under cuts: in the settings (prj_…/rev_…), or check the sheet in a project so MetaRush can look the cut up."
		if len(names) > 0 {
			hint = "Use one of: " + strings.Join(names, ", ") + "."
		}
		p.errAt(sp.Line, sp.Col, sp.EndCol, "unknown_cut", fmt.Sprintf("There's no cut called %q in this sheet.", strings.Join(words, " ")), hint)
		return "", 0, false
	}
	key, problem := cl.FindCut(strings.Join(words, " "), number)
	if key == "" {
		if problem == "" {
			problem = fmt.Sprintf("MetaRush can't find a cut called %q.", strings.Join(words, " "))
		}
		p.errAt(sp.Line, sp.Col, sp.EndCol, "unknown_cut", problem, "Write the other project's name and a version, like cut from Brand r4 (the latest when the version is left out).")
		return "", 0, false
	}
	return key, j - i, true
}

func looksLikeTime(s string) bool {
	return s != "" && (s[0] >= '0' && s[0] <= '9') && (strings.Contains(s, ":") || strings.HasSuffix(s, "s") || strings.ContainsAny(s, "."))
}

// cutNames gives each nested cut a short name: from its project's name, with
// the version when one project's cut is nested at two versions.
type cutNames struct {
	byKey map[string]string
	order []string
}

func assignCutNames(s *editspec.Spec, info Info) *cutNames {
	cn := &cutNames{byKey: map[string]string{}}
	keys := editspec.NestedKeys(s)
	if len(keys) == 0 {
		return cn
	}
	cl, _ := info.(CutLookup)
	perProject := map[string]int{}
	for _, k := range keys {
		pid, _, _ := editspec.NestedRef(k)
		perProject[pid]++
	}
	used := map[string]bool{}
	for _, k := range keys {
		pid, _, _ := editspec.NestedRef(k)
		base := ""
		number := 0
		if cl != nil && info != nil {
			if d, ok := cl.CutDetails(k); ok {
				base, number = slug(d.ProjectName), d.Number
			}
		}
		if base == "" || !validFootageName(base) {
			base = "other-cut"
		}
		if perProject[pid] > 1 && number > 0 {
			base += "-r" + strconv.Itoa(number)
		}
		name := base
		for n := 2; used[name]; n++ {
			name = base + "-" + strconv.Itoa(n)
		}
		used[name] = true
		cn.byKey[k] = name
		cn.order = append(cn.order, k)
	}
	return cn
}

func (p *printer) cutSettings() {
	if len(p.cuts.order) == 0 {
		return
	}
	p.line("cuts:")
	width := 0
	for _, k := range p.cuts.order {
		width = max(width, len(p.cuts.byKey[k]))
	}
	cl, _ := p.o.Info.(CutLookup)
	for _, k := range p.cuts.order {
		n := p.cuts.byKey[k]
		pid, rid, _ := editspec.NestedRef(k)
		l := "  " + n + ":" + strings.Repeat(" ", width-len(n)+1) + pid + "/" + rid
		if cl != nil && p.o.Info != nil {
			if d, ok := cl.CutDetails(k); ok {
				var c []string
				if t := oneLine(d.Title); t != "" {
					c = append(c, "“"+t+"”")
				}
				if pn := oneLine(d.ProjectName); pn != "" {
					c = append(c, pn)
				}
				if d.Number > 0 {
					c = append(c, "version "+strconv.Itoa(d.Number))
				}
				if d.Duration > 0 {
					c = append(c, fmtClock(math.Round(d.Duration*10)/10))
				}
				if d.NewerNumber > d.Number {
					c = append(c, fmt.Sprintf("a newer version (%d) is available", d.NewerNumber))
				}
				if len(c) > 0 {
					l += "   # " + strings.Join(c, " · ")
				}
			}
		}
		p.line(l)
	}
}

// nestedQuote is the derived line under a nested clip.
func (p *printer) nestedQuote(c editspec.Clip) (string, bool) {
	cl, _ := p.o.Info.(CutLookup)
	if cl == nil || p.o.Info == nil {
		return "", false
	}
	d, ok := cl.CutDetails(c.AssetID)
	if !ok {
		return "", false
	}
	what := oneLine(d.Title)
	if what == "" {
		what = "the cut"
	}
	s := "> " + what
	if pn := oneLine(d.ProjectName); pn != "" {
		s += " from " + pn
	}
	if d.Number > 0 {
		s += ", version " + strconv.Itoa(d.Number)
	}
	if d.NewerNumber > d.Number {
		s += fmt.Sprintf(" (version %d is newer)", d.NewerNumber)
	}
	return s, true
}

// nestedPinsUsed lists the cut names the clips use (for unused-pin notes).
func sortedCutNames(m map[string]cutPin) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

var _ = json.Marshal

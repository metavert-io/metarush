package cutsheet

import (
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/metavert-io/metarush/internal/editspec"
)

// reservedNames cannot be footage names: they are keywords in evidence lines.
var reservedNames = map[string]bool{"from": true, "to": true, "quote": true, "note": true, "ref": true}

// slugName turns a file name into a short footage name: "Interview A (take 2).mov"
// → "interview-a-take-2". It is "" when nothing usable is left.
func slugName(file string) string {
	base := file
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "."); i > 0 && len(base)-i <= 6 {
		base = base[:i]
	}
	return slug(base)
}

// slugTitle turns a clip's title into a short footage name: "Ada's interview
// (take 2)" → "adas-interview-take-2", "Été à Paris" → "ete-a-paris". The
// length a descriptive name ends with (D150) is left out: "Lena's call with
// Maya (32 min)" → "lenas-call-with-maya".
func slugTitle(title string) string { return slug(lengthSuffixRe.ReplaceAllString(title, "")) }

// lengthSuffixRe is the length at the end of a descriptive name: "(45 s)",
// "(32 min)", "(1 h 05 min)".
var lengthSuffixRe = regexp.MustCompile(`\s*\((\d+ h )?\d+ (s|min)\)\s*$`)

func slug(text string) string {
	var b strings.Builder
	dash := false
	for _, r := range norm.NFD.String(strings.ToLower(text)) {
		switch {
		case unicode.Is(unicode.Mn, r), r == '\'', r == '’':
			// accents (é → e) and apostrophes (Ada's → adas) don't split words
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			dash = false
		case !dash && b.Len() > 0:
			b.WriteByte('-')
			dash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	if reservedNames[s] {
		s += "-footage"
	}
	return s
}

// fileTitle is the title a file gets by default: its name without folders or
// extension (the same rule as the library's titles).
func fileTitle(name string) string {
	base := name
	if i := strings.LastIndexAny(base, `/\`); i >= 0 {
		base = base[i+1:]
	}
	if i := strings.LastIndex(base, "."); i > 0 {
		base = base[:i]
	}
	if base = strings.TrimSpace(base); base == "" {
		return name
	}
	return base
}

// footageBase is the name MetaRush gives a piece of footage: made from its
// title, else from its file name, else "".
func footageBase(ft Footage) string {
	if s := slugTitle(ft.Title); s != "" {
		return s
	}
	return slugName(ft.Name)
}

// validFootageName: what the parser accepts as a footage name.
func validFootageName(s string) bool {
	if s == "" || len(s) > 64 || strings.HasPrefix(s, "ast_") || reservedNames[s] {
		return false
	}
	for _, r := range s {
		if !((r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '-' || r == '_' || r == '.') {
			return false
		}
	}
	return true
}

// footageNames assigns short names to every asset a spec references, in order
// of first use.
type footageNames struct {
	byID  map[string]string
	order []string
}

func (f *footageNames) name(id string) string {
	if n, ok := f.byID[id]; ok {
		return n
	}
	return id
}

func specAssets(s *editspec.Spec) []string {
	var out []string
	seen := map[string]bool{}
	add := func(id string) {
		if id != "" && !editspec.IsNested(id) && !editspec.IsConversation(id) && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	for _, c := range s.Clips {
		add(c.AssetID)
	}
	// The conversations' recordings and b-roll (D99).
	for _, id := range editspec.ConversationAssetIDs(s) {
		add(id)
	}
	for _, c := range s.Clips {
		for _, e := range c.Evidence {
			add(e.AssetID)
		}
	}
	for _, a := range s.Audio {
		add(a.AssetID)
	}
	for _, o := range s.Overlays {
		for _, e := range o.Evidence {
			add(e.AssetID)
		}
	}
	// The footage a graphic's changes follow (D163), after everything else so
	// adding them never renames footage a cut already names.
	for _, o := range s.Overlays {
		for _, c := range o.Changes {
			add(c.Footage)
		}
	}
	if v := s.Voiceover; v != nil {
		if c := v.Continuous; c != nil {
			add(c.AssetID)
			if c.Processed != nil {
				add(c.Processed.AssetID)
			}
			add(c.Anchor.AssetID)
		}
		for _, b := range v.Beats {
			if t := b.Take; t != nil {
				add(t.AssetID)
				if t.Processed != nil {
					add(t.Processed.AssetID)
				}
			}
			add(b.Anchor.AssetID)
			for _, e := range b.Evidence {
				add(e.AssetID)
			}
		}
	}
	return out
}

func assignNames(s *editspec.Spec, info Info) *footageNames { return assignNamesWith(s, info, nil) }

// assignNamesWith names the spec's footage first (so adding footage to a
// project never renames what a cut uses), then the extra asset IDs.
func assignNamesWith(s *editspec.Spec, info Info, extra []string) *footageNames {
	f := &footageNames{byID: map[string]string{}}
	used := map[string]bool{}
	all := specAssets(s)
	seen := map[string]bool{}
	for _, id := range all {
		seen[id] = true
	}
	for _, id := range extra {
		if id != "" && !seen[id] {
			seen[id] = true
			all = append(all, id)
		}
	}
	for i, id := range all {
		base := "footage-" + strconv.Itoa(i+1)
		if info != nil {
			if ft, ok := info.Footage(id); ok {
				if b := footageBase(ft); b != "" {
					base = b
				}
			}
		}
		name := base
		for n := 2; used[name]; n++ {
			name = base + "-" + strconv.Itoa(n)
		}
		used[name] = true
		f.byID[id] = name
		f.order = append(f.order, id)
	}
	return f
}

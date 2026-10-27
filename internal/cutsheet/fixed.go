package cutsheet

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Fixed copies (D152) in a cut sheet: the footage the cut plays from a fixed
// copy (a steadier picture, cleaned-up voices), by its footage name, pinned
// to the copy's exact version:
//
//	footage:
//	  kitchen: ast_… sha256:9f2c4e1a7b3d5f60   # “Kitchen chat” · IMG_0042.MOV · 1:02
//	fixed:
//	  kitchen: sha256:<64 hex>   # steadier picture, balanced colour
//
// Footage not listed plays its original.

// FixedLookup is an optional part of Info: a fixed copy's fixes in words.
type FixedLookup interface {
	FixedWords(assetID, sha string) string
}

type fixedPin struct {
	name string
	sha  string
	span Span
}

// fixedList reads `fixed:` — footage name → sha256:<full>. The names are
// resolved once the whole settings block is in (pinFixed).
func (p *parser) fixedList(v *yaml.Node, off int) {
	if v.Kind != yaml.MappingNode {
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_fixed", "fixed lists the footage the cut plays from a fixed copy, with the copy's exact version, one per line.", "Example:   kitchen: sha256:… (copy the line from a sheet MetaRush wrote)")
		return
	}
	seen := map[string]bool{}
	for j := 0; j+1 < len(v.Content); j += 2 {
		k, val := v.Content[j], v.Content[j+1]
		name := k.Value
		ks := nodeSpan(k, off)
		if seen[name] {
			p.errAt(ks.Line, ks.Col, ks.EndCol, "duplicate", fmt.Sprintf("The footage %s is listed twice under fixed.", name), "List each piece of footage once.")
			continue
		}
		seen[name] = true
		s, _ := scalarText(val)
		h := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "sha256:"))
		if len(h) != 64 || !hexOnlyRe.MatchString(h) {
			p.errAt(val.Line+off, val.Column, val.Column+max(1, len([]rune(s))), "bad_fixed", fmt.Sprintf("%s should be its fixed copy's full version: sha256: and 64 characters.", name), "Copy the line from a sheet MetaRush wrote, or remove it to play the original.")
			continue
		}
		p.fixed = append(p.fixed, fixedPin{name: name, sha: h, span: ks})
	}
}

// pinFixed sets spec.fixed from the settings, naming footage as the footage
// list does (or by its ast_ ID).
func (p *parser) pinFixed(spec *editspec.Spec) {
	for _, fp := range p.fixed {
		id := ""
		switch {
		case assetIDRe.MatchString(fp.name):
			id = fp.name
		default:
			if ref, ok := p.footage[fp.name]; ok {
				id = ref.AssetID
			}
		}
		if id == "" {
			p.errAt(fp.span.Line, fp.span.Col, fp.span.EndCol, "bad_fixed", fmt.Sprintf("%s isn't in the footage list.", fp.name), "Use a name from footage:, the way MetaRush wrote it.")
			continue
		}
		if spec.Fixed == nil {
			spec.Fixed = map[string]editspec.FixedPin{}
		}
		spec.Fixed[id] = editspec.FixedPin{SHA256: fp.sha}
		p.sm.Keys["fixed."+id] = fp.span
	}
}

func (p *printer) fixedSettings() {
	ids := editspec.FixedAssets(p.s)
	if len(ids) == 0 {
		return
	}
	p.line("fixed:")
	width := 0
	for _, id := range ids {
		width = max(width, len(p.names.name(id)))
	}
	fl, _ := p.o.Info.(FixedLookup)
	for _, id := range ids {
		n := p.names.name(id)
		sha := p.s.Fixed[id].SHA256
		l := "  " + n + ":" + strings.Repeat(" ", width-len(n)+1) + "sha256:" + sha
		if fl != nil && p.o.Info != nil {
			if w := oneLine(fl.FixedWords(id, sha)); w != "" {
				l += "   # " + w
			}
		}
		p.line(l)
	}
}

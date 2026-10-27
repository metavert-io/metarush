package cutsheet

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Project motion graphics (D68) in a cut sheet. A graphic names the component
// and its version in ## Graphics,
//
//	- route: motion route-map@3fa9c2d1 at 0:12 for 4 s · with from "Lisbon", to "Porto"
//
// and the settings pin every short version to the full hash, like footage:
//
//	motion:
//	  route-map: sha256:3fa9c2d1…   # approved by Ada · 18 Oct 2026
//
// so the sheet stays lossless (Parse(Print(spec)) keeps the spec hash). A
// short version without a settings entry is looked up in the workspace when
// the sheet is checked (MotionLookup); a full 64-character version needs
// neither.

// MotionLibraryLookup is an optional part of Info (D171): the shared
// libraries a graphic imports, and the version a cut sheet pins when a line
// doesn't say (the newest approved).
type MotionLibraryLookup interface {
	MotionLibraries(hash string) []string
	LatestMotionLibrary(name string) string
}

// MotionLookup is an optional part of Info: the motion graphics this workspace
// has checked.
type MotionLookup interface {
	// MotionVersions lists the full hashes of checked versions that start with prefix.
	MotionVersions(prefix string) []string
	// MotionNote says where a version stands, for the derived settings comment
	// ("approved by Ada · 18 Oct 2026", "waiting for a person").
	MotionNote(hash string) string
}

var (
	motionNameRe = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)
	hexOnlyRe    = regexp.MustCompile(`^[0-9a-f]+$`)
)

func validMotionName(n string) bool { return len(n) <= 40 && motionNameRe.MatchString(n) }

type motionPin struct {
	hash string
	span Span
}

// motionList reads the `motion:` settings: name → sha256:<full hash>.
func (p *parser) motionList(v *yaml.Node, off int) {
	if v.Kind != yaml.MappingNode {
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_motion", "motion lists the project's motion graphics and their exact versions, one per line.", "Example:   route-map: sha256:3fa9c2d1… (its version, as shown under Motion graphics in the dashboard)")
		return
	}
	for j := 0; j+1 < len(v.Content); j += 2 {
		k, val := v.Content[j], v.Content[j+1]
		name := k.Value
		ks := nodeSpan(k, off)
		if !validMotionName(name) {
			p.errAt(ks.Line, ks.Col, ks.EndCol, "bad_motion", fmt.Sprintf("%q can't name a motion graphic.", name), "Use the component's folder name: lowercase letters, digits and dashes (like route-map).")
			continue
		}
		if _, dup := p.motion[name]; dup {
			p.errAt(ks.Line, ks.Col, ks.EndCol, "duplicate", fmt.Sprintf("The motion graphic %s is listed twice.", name), "List each one once; a cut that uses two versions of one graphic writes the full version on each graphics line instead.")
			continue
		}
		s, _ := scalarText(val)
		h := strings.ToLower(strings.TrimPrefix(strings.TrimSpace(s), "sha256:"))
		if len(h) != 64 || !hexOnlyRe.MatchString(h) {
			p.errAt(val.Line+off, val.Column, val.Column+max(1, len([]rune(s))), "bad_motion", fmt.Sprintf("%s should be its full version: sha256: and 64 characters.", name), "Copy the line from a sheet MetaRush wrote, or the version shown under Motion graphics in the dashboard.")
			continue
		}
		p.motion[name] = motionPin{hash: h, span: ks}
	}
}

// motionRef reads `motion name@version` (or `motion: name@version`, or just
// `name@version`) at head[i]. It returns the tokens used, or ok=false after
// reporting a problem.
func (p *parser) motionRef(it *item, head []token, i, line int) (int, bool) {
	used := 0
	if i < len(head) && head[i].kind == tWord && (strings.EqualFold(head[i].text, "motion") || strings.EqualFold(head[i].text, "motion:")) {
		used = 1
	}
	if i+used >= len(head) || head[i+used].kind != tWord || !strings.Contains(head[i+used].text, "@") {
		c := head[i].col
		p.errAt(line, c, head[i].end, "bad_motion", "Name the motion graphic and its version after motion, like motion route-map@3fa9c2d1.", "The version is the one shown under Motion graphics in the dashboard (the first 8 characters are enough).")
		return 0, false
	}
	t := head[i+used]
	// name@version, then the shared libraries it is pinned to: +theme@version (D171).
	parts := strings.Split(t.text, "+")
	var mr editspec.MotionRef
	for k, part := range parts {
		name, ver, _ := strings.Cut(part, "@")
		ver = strings.ToLower(strings.TrimPrefix(ver, "sha256:"))
		switch {
		case !validMotionName(name):
			p.errAt(line, t.col, t.end, "bad_motion", fmt.Sprintf("%q can't name a motion graphic or a shared library.", name), "Use the component's folder name: lowercase letters, digits and dashes (like route-map).")
			return 0, false
		case len(ver) < 8 || len(ver) > 64 || !hexOnlyRe.MatchString(ver):
			p.errAt(line, t.col, t.end, "bad_motion", fmt.Sprintf("%q isn't a version MetaRush can look up.", part), "Write the first 8 or more characters of its version (under Motion graphics in the dashboard), like route-map@3fa9c2d1.")
			return 0, false
		}
		full, ok := p.resolveMotion(name, ver, t, line)
		if !ok {
			return 0, false
		}
		if k == 0 {
			mr = editspec.MotionRef{Name: name, Hash: full}
			continue
		}
		if mr.Libs == nil {
			mr.Libs = map[string]string{}
		}
		mr.Libs[name] = full
	}
	// A line that names no library: the libraries the graphic imports, at the
	// version listed under motion: or the newest approved one.
	if ll, ok := p.o.Info.(MotionLibraryLookup); ok && p.o.Info != nil {
		for _, l := range ll.MotionLibraries(mr.Hash) {
			if _, has := mr.Libs[l]; has {
				continue
			}
			h := ""
			if pin, ok := p.motion[l]; ok {
				h = pin.hash
			} else {
				h = ll.LatestMotionLibrary(l)
			}
			if h != "" {
				if mr.Libs == nil {
					mr.Libs = map[string]string{}
				}
				mr.Libs[l] = h
			}
		}
	}
	ref, _ := json.Marshal(mr)
	p.set(it, "motion", ref, Span{line, head[i].col, t.end}, "The motion graphic")
	return used + 1, true
}

func (p *parser) resolveMotion(name, ver string, t token, line int) (string, bool) {
	pin, pinned := p.motion[name]
	if len(ver) == 64 {
		if pinned && pin.hash != ver {
			p.errAt(line, t.col, t.end, "motion_mismatch", fmt.Sprintf("%s here isn't the version listed under motion: at the top.", t.text), "Use one version: change this line or the motion: entry.")
			return "", false
		}
		return ver, true
	}
	if pinned {
		if !strings.HasPrefix(pin.hash, ver) {
			p.errAt(line, t.col, t.end, "motion_mismatch", fmt.Sprintf("%s isn't the version of %s listed under motion: at the top (%s).", t.text, name, pin.hash[:8]), "Use the same version on this line and under motion:, or remove the motion: entry.")
			return "", false
		}
		return pin.hash, true
	}
	if ml, ok := p.o.Info.(MotionLookup); ok && p.o.Info != nil {
		switch vs := ml.MotionVersions(ver); len(vs) {
		case 1:
			return vs[0], true
		case 0:
		default:
			p.errAt(line, t.col, t.end, "motion_ambiguous", fmt.Sprintf("More than one checked version starts with %s.", ver), "Write more of the version.")
			return "", false
		}
	}
	p.errAt(line, t.col, t.end, "motion_unknown", fmt.Sprintf("MetaRush doesn't know the motion graphic %s.", t.text), "Check it first so MetaRush knows it (checking shows the version to use), or list its full version under motion: in the settings.")
	return "", false
}

// motionPins is what the printer lists under motion: — each graphic name used
// with a single version. A name used with several versions is written in
// full on each line instead.
func motionPins(s *editspec.Spec) (pins map[string]string, order []string) {
	byName := map[string]map[string]bool{}
	add := func(name, hash string) {
		if !validMotionName(name) {
			return
		}
		if byName[name] == nil {
			byName[name] = map[string]bool{}
			order = append(order, name)
		}
		byName[name][hash] = true
	}
	for _, o := range s.Overlays {
		if o.Motion == nil {
			continue
		}
		add(o.Motion.Name, o.Motion.Hash)
		// Its shared libraries are pinned the same way (D171).
		libs := make([]string, 0, len(o.Motion.Libs))
		for n := range o.Motion.Libs {
			libs = append(libs, n)
		}
		sort.Strings(libs)
		for _, n := range libs {
			add(n, o.Motion.Libs[n])
		}
	}
	pins = map[string]string{}
	var kept []string
	for _, n := range order {
		if len(byName[n]) != 1 {
			continue
		}
		for h := range byName[n] {
			pins[n] = h
		}
		kept = append(kept, n)
	}
	sort.Strings(kept)
	return pins, kept
}

func (p *printer) motionSettings() {
	pins, order := motionPins(p.s)
	if len(order) == 0 {
		return
	}
	p.line("motion:")
	width := 0
	for _, n := range order {
		width = max(width, len(n))
	}
	ml, _ := p.o.Info.(MotionLookup)
	for _, n := range order {
		l := "  " + n + ":" + strings.Repeat(" ", width-len(n)+1) + "sha256:" + pins[n]
		if ml != nil && p.o.Info != nil {
			if note := oneLine(ml.MotionNote(pins[n])); note != "" {
				l += "   # " + note
			}
		}
		p.line(l)
	}
}

// motionToken is how a graphics line names its motion graphic, or "" when the
// reference can only travel in the {…} block.
func motionToken(o editspec.Overlay, pins map[string]string) string {
	m := o.Motion
	if m == nil || !validMotionName(m.Name) || len(m.Hash) != 64 || !hexOnlyRe.MatchString(m.Hash) || o.Template != "" {
		return ""
	}
	ver := func(name, hash string) string {
		if pins[name] == hash {
			return name + "@" + hash[:8]
		}
		return name + "@" + hash
	}
	tok := "motion " + ver(m.Name, m.Hash)
	libs := make([]string, 0, len(m.Libs))
	for n := range m.Libs {
		libs = append(libs, n)
	}
	sort.Strings(libs)
	for _, n := range libs {
		h := m.Libs[n]
		if !validMotionName(n) || len(h) != 64 || !hexOnlyRe.MatchString(h) {
			return "" // only the {…} block can carry it
		}
		tok += "+" + ver(n, h)
	}
	return tok
}

package cutsheet

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Compositions of Remotion sources (D135) in a cut sheet. A composition
// becomes a clip by rendering it, and the clip is footage, so the sheet
// MetaRush writes names it in the footage list like any clip (its comment
// says which source, composition and version it was made from). To put a
// composition into a cut, a clip line may name it instead of footage:
//
//	4. teaser: remotion kite KiteTeaserV3 0:00 → 0:12
//	5. promo: remotion kite KiteTeaserV3@3fa9c2d1 · with headline "Ship faster"
//	6. intro: remotion kite Open
//
// remotion <source> <composition>[@<version>]: the source's short name, the
// composition's ID and optionally the version (its number like @3, or 7+
// characters of it; without one, the newest approved version). The range is
// optional (the whole composition); `· with …` gives its props. When the sheet
// is checked (RemotionLookup), the line resolves to the clip MetaRush made of
// exactly that, and the sheet written back uses the footage name.

// RemotionLookup is an optional part of Info: the compositions of the
// project's Remotion sources.
type RemotionLookup interface {
	// RemotionClip finds the clip made of a composition: version "" is the
	// newest approved one; props is canonical JSON ("" for none).
	RemotionClip(source, composition, version, props string) RemotionResolved
}

// RemotionResolved is what a composition named in a sheet resolved to: the
// clip and its length, or a problem (code, plain message, hint).
type RemotionResolved struct {
	AssetID  string
	Name     string // a footage name for it (when the sheet has none)
	Duration float64
	Code     string
	Message  string
	Hint     string
}

var (
	remotionSourceRe = regexp.MustCompile(`^[a-z][a-z0-9-]{0,39}$`)
	remotionCompRe   = regexp.MustCompile(`^[A-Za-z0-9\p{Han}\p{Hiragana}\p{Katakana}\p{Hangul}_-]{1,200}$`)
	remotionVerRe    = regexp.MustCompile(`^(?:\d{1,6}|[0-9a-f]{7,64})$`)
)

// startsRemotion reports a clip line that names a composition. Footage that
// happens to be called "remotion" followed by its times stays footage.
func (p *parser) startsRemotion(head []token, i int) bool {
	if i >= len(head) || !(head[i].is("remotion") || head[i].is("remotion:")) {
		return false
	}
	if _, isFootage := p.footage[head[i].text]; isFootage && (i+1 >= len(head) || head[i+1].kind != tWord || looksLikeTime(head[i+1].text) || head[i+1].is("from")) {
		return false
	}
	return true
}

// remotionClip reads `remotion <source> <composition>[@version]` at head[i]
// (props from a `with …` setting) and resolves it. It returns the clip, its
// length and how many tokens it used.
func (p *parser) remotionClip(it *item, head []token, i, line int, props string) (RemotionResolved, int, bool) {
	start := head[i]
	if i+2 >= len(head) || head[i+1].kind != tWord || head[i+2].kind != tWord {
		p.errAt(line, start.col, head[len(head)-1].end, "bad_remotion", "Name the Remotion project and the composition after remotion, like remotion kite Intro.", "metarush remotion list shows the projects (short names) and their compositions.")
		return RemotionResolved{}, 0, false
	}
	src, ct := head[i+1], head[i+2]
	comp, ver, _ := strings.Cut(ct.text, "@")
	ver = strings.ToLower(ver)
	switch {
	case !remotionSourceRe.MatchString(src.text):
		p.errAt(line, src.col, src.end, "bad_remotion", fmt.Sprintf("%q isn't a Remotion project's short name.", src.text), "Use the short name metarush remotion list shows (lowercase, like kite).")
		return RemotionResolved{}, 0, false
	case !remotionCompRe.MatchString(comp):
		p.errAt(line, ct.col, ct.end, "bad_remotion", fmt.Sprintf("%q isn't a composition's ID.", comp), "Use the composition's ID as metarush remotion list shows it (like KiteTeaser).")
		return RemotionResolved{}, 0, false
	case strings.Contains(ct.text, "@") && !remotionVerRe.MatchString(ver):
		p.errAt(line, ct.col, ct.end, "bad_remotion", fmt.Sprintf("%q isn't a version MetaRush can look up.", ver), "Write its number (like @3) or at least 7 characters of it.")
		return RemotionResolved{}, 0, false
	}
	rl, ok := p.o.Info.(RemotionLookup)
	if !ok || p.o.Info == nil {
		p.errAt(line, start.col, ct.end, "remotion_unknown", "This sheet names a composition of a Remotion project, which only a check against the project can turn into its clip.", "Check the sheet with metarush cut check (it finds the clip made of it).")
		return RemotionResolved{}, 0, false
	}
	r := rl.RemotionClip(src.text, comp, ver, props)
	if r.AssetID == "" {
		code := r.Code
		if code == "" {
			code = "remotion_unknown"
		}
		p.errAt(line, start.col, ct.end, code, r.Message, r.Hint)
		return r, 0, false
	}
	// Name the clip in the footage list (the sheet written back uses it).
	if p.nameOf(r.AssetID) == "" {
		name := r.Name
		if name == "" || !validFootageName(name) {
			name = "remotion-clip"
		}
		base := name
		for n := 2; ; n++ {
			if _, taken := p.footage[name]; !taken {
				break
			}
			name = fmt.Sprintf("%s-%d", base, n)
		}
		ref := FootageRef{Name: name, AssetID: r.AssetID, Line: line}
		p.footage[name] = ref
		p.fnames = append(p.fnames, name)
		p.sheet.Footage = append(p.sheet.Footage, ref)
	}
	return r, 3, true
}

// withJSON reads `with name "Ada", count 3` into a JSON object.
func (p *parser) withJSON(seg []token, line int, what string) (string, bool) {
	var parts []string
	seen := map[string]bool{}
	ts := seg[1:]
	for k := 0; k < len(ts); {
		if ts[k].kind != tWord || k+1 >= len(ts) {
			p.errAt(line, ts[k].col, ts[len(ts)-1].end, "bad_params", fmt.Sprintf("Write %s as name value pairs, like with name \"Ada\", title \"Engineer\".", what), "")
			return "", false
		}
		key := ts[k].text
		if seen[key] {
			p.errAt(line, ts[k].col, ts[k].end, "duplicate", key+" is given twice.", "")
			return "", false
		}
		seen[key] = true
		val := ts[k+1]
		var raw string
		switch val.kind {
		case tString:
			raw = quote(val.text)
		case tBlock, tList:
			raw = string(val.raw)
		case tWord:
			switch {
			case val.text == "true" || val.text == "false" || val.text == "null":
				raw = val.text
			case jsonNumberRe.FindString(val.text) == val.text:
				raw = val.text
			default:
				p.errAt(line, val.col, val.end, "bad_params", fmt.Sprintf("Put %s's text in quotes: %s.", key, quote(val.text)), "")
				return "", false
			}
		default:
			p.errAt(line, val.col, val.end, "bad_params", "A value is missing after "+key+".", "")
			return "", false
		}
		parts = append(parts, quote(key)+":"+raw)
		k += 2
		if k < len(ts) {
			if ts[k].kind != tComma {
				p.errAt(line, ts[k].col, ts[k].end, "bad_params", fmt.Sprintf("Separate %s with commas.", what), "")
				return "", false
			}
			k++
		}
	}
	return "{" + strings.Join(parts, ",") + "}", true
}

// canonicalJSON writes a JSON object with sorted keys ("" for none).
func canonicalJSON(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil || len(m) == 0 {
		return ""
	}
	b, _ := json.Marshal(m)
	return string(b)
}

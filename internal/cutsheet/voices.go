package cutsheet

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Per-person sound in the mix settings (mix.voices), one line per person or
// whole recording:
//
//	mix:
//	  voices:
//	    Maya: +3 dB, clean 80%
//	    "recording interview-b": studio, original plays
//
// A level is in decibels; a clearer voice is its mode (clean, studio, cloud)
// with an optional strength; "original plays" keeps the settings but plays the
// original (A/B); "normal" changes nothing. The processed copies themselves
// (mix.enhanced) are written by `sound enhance` and travel as data.

const recordingPrefix = "recording "

// voicesWords spells mix.voices, or reports false when an entry has no
// friendly spelling (the whole list then travels as data).
func (p *printer) voicesWords(vs []editspec.VoiceSound) ([]string, bool) {
	lines := []string{"voices:"}
	for _, v := range vs {
		f := fieldsOf(v)
		var key string
		switch {
		case v.Name != "" && v.AssetID == "" && strings.TrimSpace(v.Name) == v.Name && !strings.HasPrefix(strings.ToLower(v.Name), recordingPrefix):
			key = yamlKey(v.Name)
			f.take("name")
		case v.Name == "" && v.AssetID != "":
			key = yamlKey(recordingPrefix + p.names.name(v.AssetID))
			f.take("assetId")
		default:
			return nil, false
		}
		var parts []string
		if v.GainDB != nil {
			parts = append(parts, signedNum(*v.GainDB)+" dB")
			f.take("gainDb")
		}
		if e := v.Enhance; e != nil {
			if !contains(editspec.EnhanceModes, e.Mode) || !fieldsOnly(e, "mode", "strength", "off") {
				return nil, false
			}
			w := e.Mode
			if e.Strength != nil {
				w += " " + fmtNum(math.Round(*e.Strength*10000)/100) + "%"
			}
			parts = append(parts, w)
			if e.Off {
				parts = append(parts, "original plays")
			}
			f.take("enhance")
		}
		if f.block() != "" {
			return nil, false
		}
		if len(parts) == 0 {
			parts = []string{"normal"}
		}
		lines = append(lines, "  "+key+": "+strings.Join(parts, ", "))
	}
	return lines, true
}

func signedNum(v float64) string {
	if v > 0 {
		return "+" + fmtNum(v)
	}
	return fmtNum(v)
}

// voicesValue reads `voices:` (kept until the whole settings block is read, so
// footage names resolve wherever the footage list is).
func (p *parser) voicesValue(v *yaml.Node, off int) (json.RawMessage, bool) {
	if v.Kind != yaml.MappingNode {
		p.errAt(v.Line+off, v.Column, v.Column+1, "bad_value", "voices is a list of people, one per line.", "Like:  voices:\n      Maya: +3 dB, clean 80%")
		return nil, false
	}
	var out []map[string]any
	seen := map[string]bool{}
	ok := true
	for j := 0; j+1 < len(v.Content); j += 2 {
		k, val := v.Content[j], v.Content[j+1]
		ks := nodeSpan(k, off)
		entry := map[string]any{}
		name := strings.TrimSpace(k.Value)
		switch {
		case name == "":
			p.errAt(ks.Line, ks.Col, ks.EndCol, "bad_value", "Each line of voices starts with a person's name.", "")
			ok = false
			continue
		case strings.HasPrefix(strings.ToLower(name), recordingPrefix):
			ref := strings.TrimSpace(name[len(recordingPrefix):])
			id, found := p.resolveFootage(token{kind: tWord, text: ref, col: ks.Col, end: ks.EndCol}, ks.Line)
			if !found {
				ok = false
				continue
			}
			entry["assetId"] = id
		default:
			entry["name"] = name
		}
		dupKey := strings.ToLower(name)
		if seen[dupKey] {
			p.errAt(ks.Line, ks.Col, ks.EndCol, "duplicate", fmt.Sprintf("%s is in voices twice.", name), "Keep one line per person.")
			ok = false
			continue
		}
		seen[dupKey] = true
		s, isScalar := scalarText(val)
		if !isScalar {
			p.errAt(val.Line+off, val.Column, val.Column+1, "bad_value", fmt.Sprintf("Say how %s sounds, like +3 dB, clean 80%%.", name), "")
			ok = false
			continue
		}
		bad := func(msg string) {
			p.errAt(val.Line+off, val.Column, val.Column+max(1, len(val.Value)), "bad_value", msg,
				"A voice is a level (+3 dB), a clearer voice (clean, studio or cloud, with a strength like 80%), and original plays for the A/B; or normal.")
			ok = false
		}
		var enh map[string]any
		good := true
		for _, part := range strings.Split(s, ",") {
			w := strings.ToLower(strings.Join(strings.Fields(part), " "))
			fs := strings.Fields(w)
			switch {
			case w == "normal" && strings.TrimSpace(s) == strings.TrimSpace(part):
			case w == "original plays" || w == "original":
				if enh == nil || enh["off"] != nil {
					bad("original plays goes after a clearer voice (clean, studio or cloud).")
					good = false
				} else {
					enh["off"] = true
				}
			case strings.HasSuffix(w, "db"):
				x, okNum := parseNum(strings.TrimSpace(strings.TrimSuffix(w, "db")))
				if !okNum || entry["gainDb"] != nil {
					bad(fmt.Sprintf("%q isn't a level MetaRush can read.", strings.TrimSpace(part)))
					good = false
					continue
				}
				entry["gainDb"] = x
			case len(fs) >= 1 && len(fs) <= 2 && contains(editspec.EnhanceModes, fs[0]):
				if enh != nil {
					bad("A voice has one way of being made clearer.")
					good = false
					continue
				}
				enh = map[string]any{"mode": fs[0]}
				if len(fs) == 2 {
					x, okNum := parseNum(strings.TrimSuffix(fs[1], "%"))
					if !okNum || !strings.HasSuffix(fs[1], "%") || x < 0 || x > 100 {
						bad("The strength is a percentage, like clean 80%.")
						good = false
						continue
					}
					enh["strength"] = math.Round(x*100) / 10000
				}
			default:
				bad(fmt.Sprintf("%q isn't something MetaRush can do to a voice.", strings.TrimSpace(part)))
				good = false
			}
		}
		if !good {
			continue
		}
		if enh != nil {
			entry["enhance"] = enh
		}
		out = append(out, entry)
	}
	if !ok {
		return nil, false
	}
	b, _ := json.Marshal(out)
	return b, true
}

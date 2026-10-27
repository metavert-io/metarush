package cutsheet

import (
	"fmt"
	"math"
	"reflect"
	"regexp"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
)

var (
	pointerRe     = regexp.MustCompile(`^/(clips|audio|overlays)/(\d+)(?:/([A-Za-z]+))?`)
	beatPointerRe = regexp.MustCompile(`^/voiceover/beats/(\d+)(?:/([A-Za-z]+))?`)
	// lineWhoRe matches how validation names a narration line
	// (editspec.LineName: `Narration line 3 (“It started in a garage.”)`, with a
	// small n inside a sentence); the sheet names it by its ID, as its bullet does.
	lineWhoRe = regexp.MustCompile(`([Nn])arration line \d+(?: \(“[^\n]*?”\))?`)
)

// lineByID swaps validation's name for a narration line (its number and
// words) for the sheet's (its ID), wherever the message names it.
func lineByID(msg string, i int, b editspec.Beat) string {
	who := editspec.LineName(i, b.Text)
	if strings.Contains(msg, who) || strings.Contains(msg, "n"+who[1:]) {
		msg = strings.ReplaceAll(msg, who, "Narration line "+b.ID)
		return strings.ReplaceAll(msg, "n"+who[1:], "narration line "+b.ID)
	}
	// A message made from other words of the line (an older text): its number
	// and the quoted words up to the first ”).
	return lineWhoRe.ReplaceAllString(msg, "${1}arration line "+b.ID)
}

// Locate turns a validation finding (a JSON pointer into spec) into a problem
// on the right line of the sheet, in plain words for the common cases. spec is
// the spec that was validated (after any rebase); items are found by ID.
func Locate(is editspec.Issue, spec *editspec.Spec, sm *SourceMap, info Info, name func(string) string) Problem {
	p := Problem{Severity: is.Severity, Code: is.Code, Message: is.Message, Path: is.Path}
	if name == nil {
		// Without the sheet's names, footage is named by its title.
		name = func(id string) string {
			if info != nil {
				if ft, ok := info.Footage(id); ok && strings.TrimSpace(ft.Title) != "" {
					return ft.Title
				}
			}
			return "the footage"
		}
	}
	path := strings.TrimPrefix(is.Path, "/spec")
	var c *editspec.Clip
	var o *editspec.Overlay
	var a *editspec.AudioTrack
	if m := pointerRe.FindStringSubmatch(path); m != nil && sm != nil {
		i, _ := strconv.Atoi(m[2])
		var pos *ItemPos
		switch m[1] {
		case "clips":
			if i < len(spec.Clips) {
				c = &spec.Clips[i]
				pos = sm.Clips[c.ID]
			}
		case "audio":
			if i < len(spec.Audio) {
				a = &spec.Audio[i]
				pos = sm.Audio[a.ID]
			}
		case "overlays":
			if i < len(spec.Overlays) {
				o = &spec.Overlays[i]
				pos = sm.Overlays[o.ID]
			}
		}
		if pos != nil {
			p.Line, p.Col, p.EndCol = pos.Line, 1, 2
			if sp, ok := pos.Fields[m[3]]; ok && m[3] != "" {
				p.Col, p.EndCol = sp.Col, sp.EndCol
			} else if sp, ok := pos.Fields["id"]; ok {
				// About the item as a whole: mark its name.
				p.Col, p.EndCol = sp.Col, sp.EndCol
			}
		}
	} else if m := beatPointerRe.FindStringSubmatch(path); m != nil && sm != nil && spec.Voiceover != nil {
		i, _ := strconv.Atoi(m[1])
		if i < len(spec.Voiceover.Beats) {
			p.Message = lineByID(p.Message, i, spec.Voiceover.Beats[i])
			if pos := sm.Beats[spec.Voiceover.Beats[i].ID]; pos != nil {
				p.Line, p.Col, p.EndCol = pos.Line, 1, 2
				if sp, ok := pos.Fields[m[2]]; ok && m[2] != "" {
					p.Col, p.EndCol = sp.Col, sp.EndCol
				} else if sp, ok := pos.Fields["id"]; ok {
					p.Col, p.EndCol = sp.Col, sp.EndCol
				}
			}
		}
	} else if sm != nil {
		key := strings.Split(strings.TrimPrefix(path, "/"), "/")[0]
		if sp, ok := sm.Keys[key]; ok {
			p.Line, p.Col, p.EndCol = sp.Line, sp.Col, sp.EndCol
		}
		if key == "captions" && strings.HasPrefix(path, "/captions/segments/") {
			parts := strings.Split(path, "/")
			if len(parts) > 3 {
				if i, err := strconv.Atoi(parts[3]); err == nil && i < len(sm.Segments) {
					p.Line, p.Col, p.EndCol = sm.Segments[i].Line, sm.Segments[i].Col, sm.Segments[i].EndCol
				}
			}
		}
	}
	footage := func(id string) (string, Footage) {
		var ft Footage
		if info != nil {
			ft, _ = info.Footage(id)
		}
		return name(id), ft
	}
	switch is.Code {
	case "empty_range":
		if c != nil {
			p.Message = fmt.Sprintf("%s ends before it starts (%s → %s).", c.ID, fmtClock(c.In), fmtClock(c.Out))
			p.Hint = "The second time must be later than the first."
		}
	case "out_of_range":
		if c != nil {
			n, ft := footage(c.AssetID)
			p.Message = fmt.Sprintf("%s ends at %s, but %s is only %s long.", c.ID, fmtClock(c.Out), n, fmtClock(math.Round(ft.Duration*1000)/1000))
			p.Hint = "End the clip earlier."
		} else if a != nil {
			n, ft := footage(a.AssetID)
			p.Message = fmt.Sprintf("The sound %s ends at %s, but %s is only %s long.", a.ID, fmtClock(a.Out), n, fmtClock(math.Round(ft.Duration*1000)/1000))
		}
	// Findings about a narration take or a clearer copy (no clip or sound here)
	// keep validation's own words, which name what they are about.
	case "missing_asset":
		if c != nil || a != nil {
			p.Message = fmt.Sprintf("There's no footage %s in this workspace.", name(assetOf(c, a)))
			p.Hint = "Check the footage list in the settings; new footage has to be imported first."
		}
	case "asset_not_ready":
		if c != nil || a != nil {
			p.Hint = "Wait for it to finish, then check again."
		}
	case "asset_not_in_project":
		if c != nil || a != nil {
			n, _ := footage(assetOf(c, a))
			p.Message = fmt.Sprintf("%s isn't part of this project yet.", n)
			p.Hint = "Add the footage to this project first."
		}
	case "not_video":
		if c != nil {
			n, _ := footage(c.AssetID)
			p.Message = fmt.Sprintf("%s has no picture, so it can't be a clip.", n)
			p.Hint = "Put audio-only footage in ## Sound."
		}
	case "too_short":
		if c != nil {
			p.Message = fmt.Sprintf("%s is too short to show (less than two frames).", c.ID)
			p.Hint = "Make it longer or remove it."
		}
	case "heavy_crop":
		if c != nil {
			n, _ := footage(c.AssetID)
			p.Message = fmt.Sprintf("Cropping %s to this shape cuts off a lot of the picture.", n)
			p.Hint = "Say what to keep with · focus 40% across or · focus on faces (look at a frame first), or show the whole picture with · fit."
		}
	case "transition_clamped":
		if c != nil {
			p.Message = fmt.Sprintf("The dissolve into %s is longer than the clips around it allow, so it will be shortened.", c.ID)
			p.Hint = "Use a shorter dissolve or longer clips."
		}
	case "locked":
		p.Hint = "Locked parts stay as they are unless the person asks to unlock them."
	case "rights_unknown":
		if a != nil {
			p.Message = fmt.Sprintf("The music %s has no rights recorded.", a.ID)
			p.Hint = "Add a line under it: Rights: license \"…\" · source \"…\"."
		}
	case "starts_after_end":
		switch {
		case o != nil:
			p.Message = fmt.Sprintf("The graphic %s starts after the video ends.", o.ID)
		case a != nil:
			p.Message = fmt.Sprintf("The sound %s starts after the video ends.", a.ID)
		}
		p.Hint = "Move it earlier."
	case "trimmed_to_end":
		switch {
		case o != nil:
			p.Message = fmt.Sprintf("The graphic %s runs past the end of the video and will be cut short.", o.ID)
		case a != nil:
			p.Message = fmt.Sprintf("The sound %s runs past the end of the video and will be cut short.", a.ID)
		}
	case "overlay_short":
		if o != nil {
			p.Message = fmt.Sprintf("The graphic %s is on screen very briefly; it may be hard to read.", o.ID)
		}
	case "vfr_source":
		if c != nil {
			n, _ := footage(c.AssetID)
			p.Message = fmt.Sprintf("%s changes its frame rate while it plays; MetaRush will make a steady copy before the final render.", n)
		}
	case "hdr_source":
		if c != nil {
			n, _ := footage(c.AssetID)
			p.Message = fmt.Sprintf("%s is HDR; the video is made in standard range, so very bright parts may look flat.", n)
		}
	case "no_clips":
		p.Message = "A cut needs at least one clip."
		p.Hint = "Add a numbered line under ## Clips, like 1. hook: interview-a 0:11.6 → 0:18.4."
	case "captions_no_evidence":
		p.Message = "Captions are on, but some footage has no transcript yet, so they would be left out."
		p.Hint = "Analyze the project's footage first to make its transcripts."
	case "captions_downgraded":
		p.Message = "Emphasis captions need word timing, which this footage doesn't have; plain captions will be used."
	case "schema":
		parts := strings.Split(path, "/")
		if msg := rangeMessage(parts[len(parts)-1]); msg != "" {
			who := ""
			switch {
			case c != nil:
				who = " (" + c.ID + ")"
			case a != nil:
				who = " (" + a.ID + ")"
			case o != nil:
				who = " (" + o.ID + ")"
			}
			p.Message = msg + who
		} // else: validation already says it in words (editspec's schemaMessage).
	}
	// Findings read as sentences, like the sheet's own problems.
	if t := strings.TrimSpace(p.Message); t != "" && !strings.ContainsAny(t[len(t)-1:], ".?!") && !strings.HasSuffix(t, "…") {
		p.Message = t + "."
	}
	return p
}

// fieldWords names spec fields for people, with the unit the sheet uses.
var fieldWords = map[string][2]string{
	"speed": {"Speed", "×"}, "volume": {"Volume", "%"}, "zoom": {"Zoom", "×"}, "focusX": {"Focus across", "%"}, "focusY": {"Focus down", "%"},
	"fadeOut": {"A fade-out", " s"}, "fadeIn": {"A fade-in", " s"}, "gainDb": {"A sound's volume", " dB"}, "duckDb": {"How much it dips under voices", " dB"},
	"duration": {"The length", " s"}, "maxWordsPerLine": {"Words per line", ""},
}

// rangeMessage says the allowed range of a field, read from the spec's own
// schema tags (so it can never disagree with validation).
func rangeMessage(field string) string {
	w, ok := fieldWords[field]
	if !ok {
		return ""
	}
	for _, v := range []any{editspec.Clip{}, editspec.Framing{}, editspec.Transition{}, editspec.AudioTrack{}, editspec.Overlay{}, editspec.Captions{}} {
		t := reflect.TypeOf(v)
		for i := 0; i < t.NumField(); i++ {
			name, _, _ := strings.Cut(t.Field(i).Tag.Get("json"), ",")
			if name != field {
				continue
			}
			var lo, hi string
			for _, part := range strings.Split(t.Field(i).Tag.Get("jsonschema"), ",") {
				if v, ok := strings.CutPrefix(part, "minimum="); ok {
					lo = v
				}
				if v, ok := strings.CutPrefix(part, "maximum="); ok {
					hi = v
				}
			}
			if lo == "" || hi == "" {
				return ""
			}
			if w[1] == "%" {
				lo, hi = shiftDecimal(lo, 2), shiftDecimal(hi, 2)
			}
			return fmt.Sprintf("%s can be from %s%s to %s%s.", w[0], lo, w[1], hi, w[1])
		}
	}
	return ""
}

func assetOf(c *editspec.Clip, a *editspec.AudioTrack) string {
	switch {
	case c != nil:
		return c.AssetID
	case a != nil:
		return a.AssetID
	}
	return ""
}

// NameFunc returns a footage naming function for a sheet (the names a printed
// sheet uses). Extra lists more assets to name after the ones the spec uses.
func NameFunc(s *editspec.Spec, info Info, extra []string) func(string) string {
	n := assignNamesWith(s, info, extra)
	cn := assignCutNames(s, info)
	cl, _ := info.(CutLookup)
	return func(id string) string {
		if editspec.IsNested(id) {
			// A cut from another project: how people say it ("the Brand intro cut").
			if cl != nil && info != nil {
				if d, ok := cl.CutDetails(id); ok {
					if t := oneLine(d.Title); t != "" {
						return "the cut “" + t + "” from " + oneLine(d.ProjectName)
					}
					return "the cut from " + oneLine(d.ProjectName)
				}
			}
			if n, ok := cn.byKey[id]; ok {
				return "the cut " + n
			}
			return "a cut from another project"
		}
		return n.name(id)
	}
}

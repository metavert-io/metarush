package qa

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// The words findings use for people (skills/_shared/voice.md): clips by their
// name in the cut, footage by its title, graphics by what they show, lengths
// and amounts the way people say them. The exact numbers stay in the report's
// measurements and checks (--details, --json).

// clipName names a clip of the cut by its name there: the “hook” clip.
func clipName(id string) string { return "the “" + id + "” clip" }

// clipNames names one or more clips: the “hook” clip, the “hook” and “advice” clips.
func clipNames(ids []string) string {
	switch len(ids) {
	case 0:
		return "the clip"
	case 1:
		return clipName(ids[0])
	}
	q := make([]string, len(ids))
	for i, id := range ids {
		q[i] = "“" + id + "”"
	}
	return "the " + joinAnd(q) + " clips"
}

// footage names a clip's footage by its title (“Garden interview”), or "the
// footage" when its title is not known.
func (r *run) footage(assetID string) string {
	if n := strings.TrimSpace(r.in.AssetNames[assetID]); n != "" {
		return "“" + n + "”"
	}
	return "the footage"
}

// joinAnd joins words as people list them: a, b and c.
func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// count says how many: 1 caption line, 2 caption lines.
func count(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return strconv.Itoa(n) + " " + word + "s"
}

// seconds says a length the way people do: 1 second, 0.4 seconds, 2.5 seconds.
func seconds(v float64) string {
	v = math.Abs(v)
	var s string
	switch {
	case v < 0.095:
		s = strconv.FormatFloat(v, 'f', 2, 64)
	case v < 9.95:
		s = strconv.FormatFloat(v, 'f', 1, 64)
	default:
		s = strconv.FormatFloat(v, 'f', 0, 64)
	}
	s = strings.TrimSuffix(s, ".0")
	if s == "1" {
		return "1 second"
	}
	return s + " seconds"
}

// amount says how big a level change is: a little, (nothing), a lot.
func amount(db float64) string {
	switch db = math.Abs(db); {
	case db <= 3:
		return " a little"
	case db <= 8:
		return ""
	}
	return " a lot"
}

// platformName is a target platform in words.
func platformName(p string) string {
	if n, ok := map[string]string{"shorts": "YouTube Shorts", "tiktok": "TikTok", "reels": "Instagram Reels", "youtube": "YouTube"}[p]; ok {
		return n
	}
	return "web video"
}

// graphicName names a graphic by what it shows: the name title “Sam Lee”, the end card.
func graphicName(o editspec.PlanOverlay) string {
	if o.Motion != nil {
		return "the motion graphic “" + o.Motion.Name + "”"
	}
	p := map[string]any{}
	_ = json.Unmarshal(o.Params, &p)
	text := func(keys ...string) string {
		for _, k := range keys {
			if v, _ := p[k].(string); strings.TrimSpace(v) != "" {
				return strings.Join(strings.Fields(v), " ")
			}
		}
		return ""
	}
	named := func(kind string, keys ...string) string {
		if t := text(keys...); t != "" {
			return "the " + kind + " “" + trunc(t, 32) + "”"
		}
		return "the " + kind
	}
	switch o.Template {
	case editspec.TplLowerThirdClean, editspec.TplLowerThirdCinematic:
		return named("name title", "name", "title")
	case editspec.TplQuoteKinetic:
		return named("quote", "text")
	case editspec.TplEndCard:
		return named("end card", "headline", "cta")
	case editspec.TplDataCard:
		return named("number card", "value", "label")
	case editspec.TplTitleCard:
		return named("title", "title")
	case editspec.TplLogoCorner:
		return "the corner logo"
	case editspec.TplSlideTitle:
		return named("title slide", "title")
	case editspec.TplSlideBullets:
		return named("slide", "title")
	case editspec.TplSlideCode:
		return named("code slide", "title")
	case editspec.TplSlideArchitecture:
		return named("diagram slide", "title")
	case editspec.TplSlideScreenshot:
		return named("screenshot slide", "title")
	case editspec.TplSlideQuote:
		return named("quote slide", "quote", "text")
	}
	return "the graphic “" + o.ID + "”"
}

// upperFirst starts a sentence.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// findingsWords counts findings the way the dashboard does: 1 problem and 2
// things worth checking.
func findingsWords(errs, warns, notes int) string {
	var parts []string
	if errs > 0 {
		parts = append(parts, count(errs, "problem"))
	}
	if warns > 0 {
		parts = append(parts, count(warns, "thing")+" worth checking")
	}
	if notes > 0 {
		parts = append(parts, count(notes, "note"))
	}
	return joinAnd(parts)
}

// passed says, for each check, what passing means for the video.
var passed = map[string]string{
	"conformance":       "Length, size and frame rate match the plan.",
	"black_frames":      "No black frames the cut doesn't plan.",
	"frozen_frames":     "The picture never freezes where the cut doesn't plan it.",
	"loudness":          "How loud it sounds suits where it's going.",
	"dialogue_silence":  "No silence where someone should be speaking.",
	"narration":         "The narration is clear and plays where it belongs.",
	"transitions":       "Transitions and sound changes play as planned.",
	"cut_loudness":      "Voices stay about equally loud across the cuts.",
	"speech_continuity": "Every word the cut plays was heard, none twice.",
	"layout":            "Captions and graphics stay out of each other's way.",
	"safe_area":         "Captions and graphics stay clear of the apps' buttons.",
	"on_screen":         "Faces stay in the picture and clear of captions and graphics, and the captions don't cover text in the footage.",
	"caption_words":     "The captions have every fix made to the transcript.",
	"call_footage":      "No split-second flashes at the clips' edges, the call app's printed names are covered, and the name bar names who is on screen.",
	"project_rules":     "The video keeps the project's rules.",
	"house_style":       "The video follows the house style: captions, and a name bar that looks the way it says.",
}

// checkDetail says what a check found: what passing means, or how many findings.
func checkDetail(name string, findings []*models.QAFinding) string {
	errs, warns, notes := 0, 0, 0
	for _, f := range findings {
		switch f.Severity {
		case models.QAError:
			errs++
		case models.QAWarning:
			warns++
		default:
			notes++
		}
	}
	if errs+warns+notes == 0 {
		if s := passed[name]; s != "" {
			return s
		}
		return "Nothing to report."
	}
	return upperFirst(findingsWords(errs, warns, notes)) + "."
}

// sentence starts a reason with a capital and ends it with a stop.
func sentence(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	s = upperFirst(s)
	if !strings.ContainsAny(s[len(s)-1:], ".?!)”") {
		s += "."
	}
	return s
}

// frames says a number of frames: 1 frame, 3 frames.
func frames(n int) string { return count(n, "frame") }

// sizeWords is a picture size: 1080×1920.
func sizeWords(w, h int) string { return fmt.Sprintf("%d×%d", w, h) }

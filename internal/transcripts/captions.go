package transcripts

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// The caption check (D148): before a version goes out, a quick list of the
// words in its captions worth a look: words the computer wasn't sure it heard,
// names and terms that aren't on the spelling lists, and words fixed in the
// transcript since the version was made (its captions still say the old ones).

// Caption issue kinds.
const (
	CaptionUnsure = "unsure"
	CaptionName   = "name"
	CaptionFixed  = "fixed"
)

// CaptionIssue is one word (or name) in a version's captions worth a look.
type CaptionIssue struct {
	Kind string `json:"kind" jsonschema:"enum=unsure,enum=name,enum=fixed"`
	// Word is as the captions show it; Now (fixed words) what the transcript says now.
	Word  string `json:"word"`
	Now   string `json:"now,omitempty"`
	Times int    `json:"times"`
	// At are where it shows (seconds into the video), the first few.
	At []float64 `json:"at"`
	// AssetID, From and To: where it is said the first time (to listen and fix it).
	AssetID string  `json:"assetId,omitempty"`
	From    float64 `json:"from"`
	To      float64 `json:"to"`
	// Confidence is how sure the computer was (unsure words: the lowest, 0 to 1).
	Confidence float64 `json:"confidence,omitempty"`
	// Says is the issue in plain words.
	Says string `json:"says"`
}

// unsureBelow: words the computer was less sure of than this are listed.
const unsureBelow = 0.5

// notNames are capitalized words that are not names to check.
var notNames = map[string]bool{"i": true, "im": true, "ive": true, "ill": true, "id": true, "ok": true, "okay": true, "mr": true, "mrs": true, "ms": true, "dr": true,
	"monday": true, "tuesday": true, "wednesday": true, "thursday": true, "friday": true, "saturday": true, "sunday": true,
	"january": true, "february": true, "march": true, "april": true, "may": true, "june": true, "july": true, "august": true, "september": true,
	"october": true, "november": true, "december": true, "christmas": true, "english": true, "god": true}

// CheckCaptions lists the words of a plan's captions worth a look. The plan
// must come from Resolve (its caption words know where they are said).
func CheckCaptions(ctx context.Context, a *app.App, plan *editspec.RenderPlan, projectIDs []string) ([]CaptionIssue, error) {
	if plan == nil || plan.Captions == nil || len(plan.Captions.Lines) == 0 {
		return []CaptionIssue{}, nil
	}
	fps := float64(plan.FPS)
	if fps <= 0 {
		fps = 30
	}
	listed := map[string]bool{}
	entries, err := ListsFor(ctx, a, projectIDs)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		listed[e.Key] = true
		for _, t := range strings.Fields(e.Word) {
			listed[Norm(t)] = true
		}
	}
	var people []models.Person
	_ = a.DB.Find(ctx, store.CPeople, a.WorkspaceID(), bson.M{}, &people, options.Find().SetProjection(bson.M{"name": 1}))
	for _, p := range people {
		for _, t := range strings.Fields(p.Name) {
			listed[Norm(t)] = true
		}
	}
	units := map[string][]Unit{}
	unitsOf := func(id string) []Unit {
		if us, ok := units[id]; ok {
			return us
		}
		segs, err := LoadSegments(ctx, a, id)
		if err != nil {
			segs = nil
		}
		us := Units(segs)
		units[id] = us
		return us
	}
	byKey := map[string]*CaptionIssue{}
	var order []string
	add := func(kind, key string, w editspec.PlanWord, now string, conf float64) {
		k := kind + "\x00" + key
		x := byKey[k]
		if x == nil {
			x = &CaptionIssue{Kind: kind, Word: w.Text, Now: now, AssetID: w.Src.AssetID, From: w.Src.From, To: w.Src.To, At: []float64{}, Confidence: conf}
			byKey[k] = x
			order = append(order, k)
		}
		x.Times++
		if len(x.At) < 5 {
			x.At = append(x.At, math.Round(float64(w.From)/fps*10)/10)
		}
		if conf > 0 && conf < x.Confidence {
			x.Confidence = conf
		}
	}
	type fixedRun struct {
		asset  string
		lo, hi int
		issue  *CaptionIssue
	}
	var lastFixed *fixedRun
	for _, line := range plan.Captions.Lines {
		for _, w := range line.Words {
			if w.Src == nil || w.Src.AssetID == "" {
				lastFixed = nil
				continue
			}
			us := unitsOf(w.Src.AssetID)
			k := sort.Search(len(us), func(k int) bool { return us[k].To > w.Src.From+0.0015 })
			if k < len(us) && near(us[k].From, w.Src.From) && near(us[k].To, w.Src.To) && us[k].Text == strings.TrimSpace(w.Text) {
				lastFixed = nil
				u := us[k]
				switch {
				case u.Fixed:
					// a person wrote it
				case u.Score > 0 && u.Score < unsureBelow:
					add(CaptionUnsure, Norm(u.Text), w, "", u.Score)
				case looksLikeName(us, k) && !listed[Norm(coreOf(u.Text))]:
					add(CaptionName, Norm(coreOf(u.Text)), w, "", 0)
				}
				continue
			}
			// The transcript says something else there now: fixed since the
			// version. Caption words over the same fixed words are one item
			// ("nobody was" → "nobodywas").
			lo, hi := -1, -1
			for k, u := range us {
				if u.To > w.Src.From+0.001 && u.From < w.Src.To-0.001 {
					if lo < 0 {
						lo = k
					}
					hi = k
				}
			}
			if lo < 0 {
				lastFixed = nil
				continue
			}
			if lastFixed != nil && lastFixed.asset == w.Src.AssetID && lastFixed.lo == lo && lastFixed.hi == hi {
				lastFixed.issue.Word += " " + strings.TrimSpace(w.Text)
				lastFixed.issue.To = math.Max(lastFixed.issue.To, w.Src.To)
				continue
			}
			now := Text(us, lo, hi)
			if now != strings.TrimSpace(w.Text) {
				k := CaptionFixed + "\x00" + w.Src.AssetID + "\x00" + fmt.Sprint(lo)
				add(CaptionFixed, k, w, now, 0)
				lastFixed = &fixedRun{asset: w.Src.AssetID, lo: lo, hi: hi, issue: byKey[CaptionFixed+"\x00"+k]}
			}
		}
	}
	out := make([]CaptionIssue, 0, len(order))
	for _, k := range order {
		x := byKey[k]
		x.Says = captionSays(*x)
		out = append(out, *x)
	}
	rank := map[string]int{CaptionFixed: 0, CaptionUnsure: 1, CaptionName: 2}
	sort.SliceStable(out, func(i, j int) bool { return rank[out[i].Kind] < rank[out[j].Kind] })
	if len(out) > 60 {
		out = out[:60]
	}
	return out, nil
}

func coreOf(text string) string {
	_, c, _ := Split(text)
	return c
}

// looksLikeName: a word with a capital that doesn't start a sentence.
func looksLikeName(us []Unit, k int) bool {
	core := coreOf(us[k].Text)
	rs := []rune(core)
	if len(rs) < 3 || !unicode.IsUpper(rs[0]) || notNames[Norm(core)] {
		return false
	}
	if k == 0 {
		return false
	}
	prev := strings.TrimSpace(us[k-1].Text)
	if strings.HasSuffix(prev, ".") || strings.HasSuffix(prev, "?") || strings.HasSuffix(prev, "!") || strings.HasSuffix(prev, ":") {
		return false
	}
	return us[k].From-us[k-1].To < 2 // after a long pause it may start a sentence
}

func captionSays(x CaptionIssue) string {
	when := ""
	if len(x.At) > 0 {
		when = " (at " + cutsheet.Clock(x.At[0]) + ")"
	}
	times := "once"
	if x.Times > 1 {
		times = fmt.Sprintf("%d times", x.Times)
	}
	switch x.Kind {
	case CaptionFixed:
		return fmt.Sprintf("The captions say “%s” where the transcript now says “%s”%s. Make a new version to use the fixed words.", x.Word, x.Now, when)
	case CaptionUnsure:
		if x.Times > 1 {
			return fmt.Sprintf("The computer wasn't sure it heard “%s” right (%s, first%s). Listen and fix it if it's wrong.", x.Word, times, when)
		}
		return fmt.Sprintf("The computer wasn't sure it heard “%s” right%s. Listen and fix it if it's wrong.", x.Word, when)
	}
	return fmt.Sprintf("“%s” appears %s in the captions. Is it spelled right?", coreOf(x.Word), times)
}

// Cue is one caption line for a captions file (seconds into the video).
type Cue struct {
	From, To float64
	Text     string
	Speaker  string
	// Words are the line's words when it has words said in a speaker's voice
	// copy in place of what they said (D311), each marked; nil otherwise.
	Words []CueWord
	// vtt is Text with the said words in italics, escaped for WebVTT (Marked).
	vtt string
}

// CueWord is a word of a caption line, and whether the cut says it in the
// speaker's voice copy in place of what they said.
type CueWord struct {
	Text string
	Said bool
}

// Cues turns a plan's captions into the lines of a captions file.
func Cues(c *editspec.PlanCaptions, fps int) []Cue {
	if c == nil {
		return nil
	}
	f := float64(fps)
	if f <= 0 {
		f = 30
	}
	out := make([]Cue, 0, len(c.Lines))
	for _, l := range c.Lines {
		text := strings.Join(strings.Fields(l.Text), " ")
		if text == "" || l.To <= l.From {
			continue
		}
		cue := Cue{From: float64(l.From) / f, To: float64(l.To) / f, Text: text}
		if c.ShowSpeakers {
			cue.Speaker = l.Speaker
		}
		for _, w := range l.Words {
			if w.Src != nil && w.Src.Said {
				cue.Words = make([]CueWord, len(l.Words))
				for k, x := range l.Words {
					cue.Words[k] = CueWord{Text: x.Text, Said: x.Src != nil && x.Src.Said}
				}
				break
			}
		}
		out = append(out, cue)
	}
	return out
}

func stamp(t float64, sep string) string {
	ms := int64(math.Round(t * 1000))
	h, ms := ms/3_600_000, ms%3_600_000
	m, ms := ms/60_000, ms%60_000
	s, ms := ms/1000, ms%1000
	return fmt.Sprintf("%02d:%02d:%02d%s%03d", h, m, s, sep, ms)
}

// SRT writes cues as a SubRip captions file. A right-to-left line's mark
// (RLM) stays at the very start of the line, before the speaker's name, so
// the whole line runs right to left.
func SRT(cues []Cue) string {
	var b strings.Builder
	for i, c := range cues {
		text := c.Text
		if c.Speaker != "" {
			if rest, ok := strings.CutPrefix(text, RLM); ok {
				text = RLM + c.Speaker + ": " + rest
			} else {
				text = c.Speaker + ": " + text
			}
		}
		fmt.Fprintf(&b, "%d\n%s --> %s\n%s\n\n", i+1, stamp(c.From, ","), stamp(c.To, ","), text)
	}
	return b.String()
}

// vttEsc escapes text for a WebVTT cue.
var vttEsc = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// Marked shows the words said in a speaker's voice copy in place of what they
// said in italics (D311: the captions' optional marker); both SubRip and
// WebVTT players show <i>.
func Marked(cues []Cue) []Cue {
	out := make([]Cue, len(cues))
	for i, c := range cues {
		out[i] = c
		if len(c.Words) == 0 {
			continue
		}
		raw, esc := make([]string, len(c.Words)), make([]string, len(c.Words))
		for k, w := range c.Words {
			raw[k], esc[k] = w.Text, vttEsc.Replace(w.Text)
			if w.Said {
				raw[k], esc[k] = "<i>"+raw[k]+"</i>", "<i>"+esc[k]+"</i>"
			}
		}
		out[i].Text, out[i].vtt = strings.Join(raw, " "), strings.Join(esc, " ")
	}
	return out
}

// Changed is a change of words a captions file's video plays, for its note:
// Consent is "held" (in force), "withdrawn" (no longer), "elsewhere" (in
// force, but not for this video's project), or "" (not known).
type Changed struct {
	Who, Words, Before, Consent string
}

// ChangedNote says, for a WebVTT file's NOTE (never shown), where the video
// says words someone never said, and whether their consent still holds: ""
// when it says none.
func ChangedNote(cs []Changed) string {
	if len(cs) == 0 {
		return ""
	}
	var parts []string
	for _, c := range cs {
		who := c.Who
		if who == "" {
			who = "the speaker"
		}
		switch c.Consent {
		case "held":
			who += ", with their consent"
		case "withdrawn":
			who += ", whose consent is no longer in force"
		case "elsewhere":
			who += ", whose consent doesn't cover this video"
		}
		parts = append(parts, fmt.Sprintf("“%s” in place of “%s” (%s)", c.Words, c.Before, who))
	}
	return "Words changed with AI in the speaker's own voice copy: " + strings.Join(parts, "; ") + "."
}

// VTT writes cues as a WebVTT captions file (speakers as voice tags).
func VTT(cues []Cue) string {
	esc := vttEsc
	var b strings.Builder
	b.WriteString("WEBVTT\n\n")
	for _, c := range cues {
		text := esc.Replace(c.Text)
		if c.vtt != "" {
			text = c.vtt
		}
		if c.Speaker != "" {
			text = "<v " + esc.Replace(c.Speaker) + ">" + text
		}
		fmt.Fprintf(&b, "%s --> %s\n%s\n\n", stamp(c.From, "."), stamp(c.To, "."), text)
	}
	return b.String()
}

// CaptionsSpec is the spec a captions file is made from: the cut's own
// captions when it shows them, else captions of what is said (and the
// narration) with the usual settings.
func CaptionsSpec(s *editspec.Spec) *editspec.Spec {
	c := editspec.Clone(s)
	if c.Captions != nil && c.Captions.Enabled {
		return c
	}
	src := "transcript"
	if c.Voiceover != nil && len(c.Voiceover.Beats) > 0 {
		src = "both"
	}
	if c.Captions != nil && c.Captions.Source != "" && c.Captions.Source != "manual" {
		src = c.Captions.Source
	}
	c.Captions = &editspec.Captions{Enabled: true, Source: src, MaxWordsPerLine: 7}
	editspec.Normalize(c)
	return c
}

// CaptionFilePlatforms are the places that take a captions file next to the
// video (TikTok and Reels show captions drawn into the picture only).
func CaptionFileAllowed(platform string) bool {
	switch platform {
	case "tiktok", "reels":
		return false
	}
	return true
}

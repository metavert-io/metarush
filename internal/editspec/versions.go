package editspec

import (
	"fmt"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/textalign"
)

// Script versions (D86). The person's script is kept exactly as they gave it
// (Voiceover.Original); Claude can propose a version fitted to the video
// (Voiceover.Fitted), which the person takes line by line. One version is in
// use (Voiceover.Active; its lines are Voiceover.Beats) and the other waits in
// Voiceover.Parked with its sound and places, so switching loses nothing.

// ActiveVersion is the script version the lines are (original by default).
func (v *Voiceover) ActiveVersion() string {
	if v != nil && v.Active == VersionFitted {
		return VersionFitted
	}
	return VersionOriginal
}

// OriginalLines are the person's own lines: the lines in use, or the parked
// ones while Claude's version is in use.
func (v *Voiceover) OriginalLines() []Beat {
	if v == nil {
		return nil
	}
	if v.ActiveVersion() == VersionFitted {
		return v.Parked
	}
	return v.Beats
}

// Line finds a fitted line by the ID of the line it fits.
func (f *FittedScript) Line(id string) *FittedLine {
	if f == nil {
		return nil
	}
	for i := range f.Lines {
		if f.Lines[i].ID == id {
			return &f.Lines[i]
		}
	}
	return nil
}

// Changed counts the fitted lines that differ from the person's (words or start).
func (f *FittedScript) Changed() int {
	n := 0
	if f == nil {
		return 0
	}
	for _, l := range f.Lines {
		if !SameWords(l.Original, l.Text) || l.Anchor != nil {
			n++
		}
	}
	return n
}

// FittedBeats builds the lines of the fitted version from the person's lines:
// each takes Claude's words and start point, or stays the person's own where
// they kept it (or Claude has nothing for it). The rest of a line (how to say
// it, its voice, level, lock) carries over; its sound carries over only while
// the words stay the same, and a line placed by a recording keeps that place.
func FittedBeats(orig []Beat, f *FittedScript) []Beat {
	out := make([]Beat, 0, len(orig))
	for _, b := range orig {
		nb := cloneBeat(b)
		fl := f.Line(b.ID)
		if fl == nil || fl.Kept || b.Locked {
			out = append(out, nb)
			continue
		}
		applyFittedLine(&nb, b, fl)
		out = append(out, nb)
	}
	return out
}

// applyFittedLine puts Claude's version of one line on nb (a copy of the
// person's line b).
func applyFittedLine(nb *Beat, b Beat, fl *FittedLine) {
	recorded := b.Take != nil && (b.Take.Source == "recorded" || b.Take.Source == "imported")
	same := SameWords(b.Text, fl.Text)
	nb.Text = fl.Text
	if !(same && recorded) {
		if fl.Anchor != nil {
			nb.Anchor = *fl.Anchor
		} else {
			nb.Anchor = Anchor{Kind: AnchorAfter}
		}
	}
	if !same {
		nb.Take, nb.State = nil, "draft"
	}
}

// SwitchLine changes one line of the fitted version in use between the
// person's words and Claude's (keep = the person's own). The line keeps its
// sound (as when its words are edited); its start point follows the version
// unless a recording placed it.
func SwitchLine(cur Beat, orig *Beat, fl *FittedLine, keep bool) Beat {
	nb := cloneBeat(cur)
	recorded := cur.Take != nil && (cur.Take.Source == "recorded" || cur.Take.Source == "imported")
	if keep {
		if orig == nil {
			return nb
		}
		nb.Text = orig.Text
		if !recorded {
			nb.Anchor = orig.Anchor
		}
		return nb
	}
	nb.Text = fl.Text
	if !recorded {
		if fl.Anchor != nil {
			nb.Anchor = *fl.Anchor
		} else {
			nb.Anchor = Anchor{Kind: AnchorAfter}
		}
	}
	return nb
}

func cloneBeat(b Beat) Beat {
	return Clone(&Spec{Voiceover: &Voiceover{Beats: []Beat{b}}}).Voiceover.Beats[0]
}

// SameWords reports whether two texts say the same words (case, punctuation
// and fillers aside).
func SameWords(a, b string) bool {
	x, y := normWords(a), normWords(b)
	if len(x) != len(y) {
		return false
	}
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

func normWords(s string) []string {
	var out []string
	for _, t := range textalign.Tokens(s) {
		if n := textalign.Normalize(t); n != "" && !textalign.IsFiller(n) {
			out = append(out, n)
		}
	}
	return out
}

// WordOp is one word of a comparison of two versions of a line.
type WordOp struct {
	Text string `json:"text"`
	Op   string `json:"op" jsonschema:"enum=same,enum=del,enum=add,description=same: in both; del: only in the person's line; add: only in Claude's."`
	// Was is the person's spelling of a word both have, when it differs
	// ("silence," for Claude's "silence.").
	Was string `json:"was,omitempty"`
}

// DiffWords compares two versions of a line word by word (the longest run of
// words they share, case and punctuation aside): the person's words come out
// as same/del, Claude's as same/add, in reading order.
func DiffWords(a, b string) []WordOp {
	x, y := strings.Fields(a), strings.Fields(b)
	nx, ny := make([]string, len(x)), make([]string, len(y))
	for i, w := range x {
		nx[i] = textalign.Normalize(w)
	}
	for i, w := range y {
		ny[i] = textalign.Normalize(w)
	}
	// Longest common subsequence on the normalized words.
	l := make([][]int, len(x)+1)
	for i := range l {
		l[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if nx[i] == ny[j] && nx[i] != "" {
				l[i][j] = l[i+1][j+1] + 1
			} else {
				l[i][j] = max(l[i+1][j], l[i][j+1])
			}
		}
	}
	var out []WordOp
	i, j := 0, 0
	for i < len(x) && j < len(y) {
		switch {
		case nx[i] == ny[j] && nx[i] != "":
			op := WordOp{Text: y[j], Op: "same"}
			if x[i] != y[j] {
				op.Was = x[i]
			}
			out = append(out, op)
			i, j = i+1, j+1
		case l[i+1][j] >= l[i][j+1]:
			out = append(out, WordOp{Text: x[i], Op: "del"})
			i++
		default:
			out = append(out, WordOp{Text: y[j], Op: "add"})
			j++
		}
	}
	for ; i < len(x); i++ {
		out = append(out, WordOp{Text: x[i], Op: "del"})
	}
	for ; j < len(y); j++ {
		out = append(out, WordOp{Text: y[j], Op: "add"})
	}
	return out
}

// AnchorFrame resolves one start point on a cut's timeline: prevEnd is where
// the line before it ends (-1 for none). problem says why it cannot be placed.
func AnchorFrame(s *Spec, clips []PlanClip, a Anchor, prevEnd int) (int, string) {
	return resolveAnchor(a, clips, float64(s.Output.FPS), prevEnd, s.beatGap())
}

// PinAnchor is the start point for a line spoken at timeline second t (D85):
// tied to the shot playing there, with the offset kept to the millisecond, so
// the line moves with its shot when the cut is re-edited. Further than a minute
// into a long shot it is the moment in that shot's footage; outside every shot,
// the clock time.
func PinAnchor(s *Spec, clips []PlanClip, t float64) Anchor {
	fps := float64(s.Output.FPS)
	t = math.Max(0, t)
	f := int(math.Floor(t*fps + 1e-6))
	var hit *PlanClip
	for i := range clips {
		pc := &clips[i]
		if f >= pc.From && f < pc.From+pc.DurationInFrames && (hit == nil || pc.From >= hit.From) {
			hit = pc // the latest-starting clip: the incoming one in a transition
		}
	}
	if hit == nil && len(clips) > 0 && f >= clips[len(clips)-1].From {
		hit = &clips[len(clips)-1]
	}
	if hit == nil {
		at := round3(t)
		return Anchor{Kind: AnchorTime, At: &at}
	}
	off := round3(t - float64(hit.From)/fps)
	if math.Abs(off) <= 60 {
		if off == 0 {
			return Anchor{Kind: AnchorClip, ClipID: hit.ID}
		}
		return Anchor{Kind: AnchorClip, ClipID: hit.ID, Offset: off}
	}
	rate := hit.PlaybackRate
	if rate <= 0 {
		rate = 1
	}
	src := round3(hit.SourceIn + (t-float64(hit.From)/fps)*rate)
	if src < hit.SourceOut {
		return Anchor{Kind: AnchorMoment, AssetID: hit.AssetID, ClipID: hit.ID, At: &src}
	}
	at := round3(t)
	return Anchor{Kind: AnchorTime, At: &at}
}

// validateVersions checks the script versions: the version in use names one
// that exists, Claude's lines are well formed, and the parked lines too (they
// are not part of the cut, so their sound is checked when they come back).
func validateVersions(v *Voiceover) []Issue {
	var out []Issue
	switch v.Active {
	case "", VersionOriginal:
	case VersionFitted:
		if v.Fitted == nil {
			out = append(out, errI("version_missing", "/voiceover/active", "The narration is set to use Claude's fitted script, but there is no fitted script."))
		}
	default:
		out = append(out, errI("version_unknown", "/voiceover/active", "The narration names a script version MetaRush doesn't know (%q); use original or fitted.", v.Active))
	}
	if f := v.Fitted; f != nil {
		seen := map[string]bool{}
		for i, l := range f.Lines {
			p := fmt.Sprintf("/voiceover/fitted/lines/%d", i)
			if seen[l.ID] {
				out = append(out, errI("duplicate_id", p+"/id", "Claude's fitted script has the line “%s” twice; keep one.", l.ID))
			}
			seen[l.ID] = true
			if strings.TrimSpace(l.Text) == "" {
				out = append(out, errI("beat_empty", p+"/text", "Claude's version of the line “%s” has no words.", l.ID))
			}
			if l.Anchor != nil {
				out = append(out, anchorShape(*l.Anchor, p+"/anchor", fmt.Sprintf("Claude's version of the line “%s”", l.ID))...)
			}
		}
	}
	seen := map[string]bool{}
	for i, b := range v.Parked {
		p := fmt.Sprintf("/voiceover/parked/%d", i)
		if seen[b.ID] {
			out = append(out, errI("duplicate_id", p+"/id", "The script version not in use has the line “%s” twice; keep one.", b.ID))
		}
		seen[b.ID] = true
		if strings.TrimSpace(b.Text) == "" {
			out = append(out, errI("beat_empty", p+"/text", "The line “%s” of the script version not in use has no words.", b.ID))
		}
		out = append(out, anchorShape(b.Anchor, p+"/anchor", fmt.Sprintf("The line “%s” of the script version not in use", b.ID))...)
	}
	return out
}

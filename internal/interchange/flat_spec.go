package interchange

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Use is the footage a timeline's source becomes: a clip in the Library.
type Use struct {
	AssetID string
	// Offset is subtracted from the timeline's source times (an EDL's
	// timecode: where the file's first frame is).
	Offset float64
	// Duration is how long the clip is (0: unknown).
	Duration float64
	// Name is the clip's name, for notes.
	Name string
}

// SpecOptions say what each source is and what to leave out.
type SpecOptions struct {
	// Uses maps a source key to its clip; sources without one are skipped.
	Uses map[string]Use
	// Title is the cut's title (default: the timeline's name).
	Title string
}

// SpecResult is the cut and what didn't make it in.
type SpecResult struct {
	Spec *editspec.Spec
	// Lost adds to Flat.Lost: shots and sounds left out, values adjusted.
	Lost []string
	// Chapters and notes moved to where they fall in the cut.
	Chapters []FlatChapter
	Notes    []FlatNote
	// ClipFor maps a shot index to its clip's index (-1: left out).
	ClipFor []int
}

var specIDRe = regexp.MustCompile(`[^a-z0-9]+`)

// SpecID makes a short ID from a name ("Sam's answer" → "sam-s-answer").
func SpecID(name, fallback string, taken map[string]bool) string {
	id := strings.Trim(specIDRe.ReplaceAllString(strings.ToLower(name), "-"), "-")
	if len(id) > 40 {
		id = strings.Trim(id[:40], "-")
	}
	if id == "" || id[0] < 'a' || id[0] > 'z' {
		id = fallback + id
		id = strings.Trim(id, "-")
	}
	base := id
	for n := 2; taken[id]; n++ {
		id = fmt.Sprintf("%s-%d", base, n)
	}
	taken[id] = true
	return id
}

// OutputFPS is the frame rate a cut renders at for a timeline's rate (23.976
// → 24, 29.97 → 30, 59.94 → 60; others the nearest MetaRush renders).
func OutputFPS(fps float64) int {
	best, bestD := 30, math.Inf(1)
	for _, c := range []int{24, 25, 30, 50, 60} {
		if d := math.Abs(float64(c) - fps); d < bestD {
			best, bestD = c, d
		}
	}
	return best
}

// Spec makes the cut: the shots whose footage is known in order, their
// transitions and split edits, and the added sounds.
func (f *Flat) Spec(o SpecOptions) *SpecResult {
	res := &SpecResult{ClipFor: make([]int, len(f.Shots))}
	var lostSoFar lostList
	lost := func(format string, args ...any) { lostSoFar.add(fmt.Sprintf(format, args...)) }
	defer func() { res.Lost = lostSoFar.done() }()
	fps := OutputFPS(f.FPS)
	out := editspec.Output{Preset: editspec.PresetLandscape, FPS: fps}
	if f.Width > 0 && f.Height > 0 {
		switch {
		case f.Height > f.Width:
			out.Preset = editspec.PresetVertical
		case f.Height == f.Width:
			out.Preset = editspec.PresetSquare
		}
	}
	spec := &editspec.Spec{SchemaVersion: 1, Title: clip(firstNonEmptyStr(o.Title, f.Name, "Imported timeline"), 200), Output: out}
	res.Spec = spec
	if math.Abs(f.FPS-float64(fps)) > 0.05 && !(math.Abs(f.FPS*1001/1000-float64(fps)) < 0.05) {
		lost("the timeline runs at %.3g frames a second; the cut renders at %d", f.FPS, fps)
	}
	taken := map[string]bool{}
	// Shots whose footage is left out are taken out; what follows moves up.
	var removed [][2]float64 // stretches of the flat timing taken out
	skippedBySource := map[string][2]float64{}
	for i, sh := range f.Shots {
		res.ClipFor[i] = -1
		u, ok := o.Uses[sh.Source]
		if !ok || u.AssetID == "" {
			removed = append(removed, [2]float64{sh.flatStart(), sh.flatStart() + sh.Length})
			x := skippedBySource[sh.Name]
			skippedBySource[sh.Name] = [2]float64{x[0] + 1, x[1] + sh.Length}
			continue
		}
		if len(spec.Clips) >= 500 {
			removed = append(removed, [2]float64{sh.flatStart(), sh.flatStart() + sh.Length})
			lost("a cut holds at most 500 clips: the shots from %s on were left out", clockSec(sh.flatStart()))
			continue
		}
		speed := sh.Speed
		if speed < 0.25 {
			speed = 0.25
		} else if speed > 4 {
			speed = 4
		}
		in := sh.In - u.Offset - sh.HandleBefore*speed
		outPt := sh.In - u.Offset + (sh.Length+sh.HandleAfter)*speed
		if in < 0 {
			if sh.In-u.Offset < 0 {
				lost("“%s” at %s starts before its footage does: it starts with the footage", sh.Name, clockSec(sh.flatStart()))
			}
			in = 0
		}
		if u.Duration > 0 && outPt > u.Duration+0.05 {
			lost("“%s” at %s runs past the end of its footage: it ends with the footage", sh.Name, clockSec(sh.flatStart()))
			outPt = u.Duration
		}
		if outPt-in < 1.0/float64(fps) {
			removed = append(removed, [2]float64{sh.flatStart(), sh.flatStart() + sh.Length})
			lost("“%s” at %s has no footage left to show, so it was left out", sh.Name, clockSec(sh.flatStart()))
			continue
		}
		c := editspec.Clip{ID: SpecID(sh.Name, "clip-", taken), AssetID: u.AssetID, In: round6(in), Out: round6(outPt), Muted: !sh.OwnSound, Label: clip(sh.Name, 120)}
		if speed != 1 {
			c.Speed = round6(speed)
		}
		if sh.OwnSound && sh.Level != nil && math.Abs(*sh.Level) > 0.1 {
			v := math.Min(2, Gain(*sh.Level))
			if Gain(*sh.Level) > 2 {
				lost("the sound of “%s” at %s is louder than MetaRush raises a clip (+6 dB): it plays at +6 dB", sh.Name, clockSec(sh.flatStart()))
			}
			v = round6(v)
			c.Volume = &v
		}
		if t := transitionFor(sh.Enter); t != nil {
			if t.Duration > 5 {
				lost("the %s at %s lasts %s; MetaRush's longest is 5 seconds", joinWords(sh.Enter), clockSec(sh.flatStart()), secondsWords(t.Duration))
				t.Duration = 5
			}
			c.Transition = t
		}
		if sh.Lead > 0 || sh.Carry > 0 {
			if c.Transition == nil {
				c.Transition = &editspec.Transition{Type: editspec.TransitionCut}
			}
			mode, d := editspec.SoundLead, sh.Lead
			if sh.Carry > 0 {
				mode, d = editspec.SoundCarry, sh.Carry
			}
			c.Transition.Audio = &editspec.SoundTransition{Mode: mode, Duration: round3f(math.Min(d, maxSplit))}
		}
		if sh.FadeOut > 0 {
			c.FadeOut = round3f(math.Min(sh.FadeOut, 5))
		}
		res.ClipFor[i] = len(spec.Clips)
		spec.Clips = append(spec.Clips, c)
	}
	names := make([]string, 0, len(skippedBySource))
	for n := range skippedBySource {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		x := skippedBySource[n]
		lost("“%s” (%d shot%s, %s) was left out: its footage isn't in the Library", n, int(x[0]), plural(int(x[0])), secondsWords(x[1]))
	}
	// A clip that now comes first can't cross from anything.
	if len(spec.Clips) > 0 && spec.Clips[0].Transition != nil && editspec.IsOverlapTransition(spec.Clips[0].Transition.Type) {
		spec.Clips[0].Transition.Type = editspec.TransitionFade
	}
	moved := closing(removed)
	// Sounds.
	for _, so := range f.Sounds {
		u, ok := o.Uses[so.Source]
		if !ok || u.AssetID == "" {
			lost("the sound “%s” at %s was left out: its file isn't in the Library", so.Name, clockSec(so.At))
			continue
		}
		if len(spec.Audio) >= 60 {
			lost("a cut holds at most 60 added sounds: “%s” at %s was left out", so.Name, clockSec(so.At))
			continue
		}
		if so.Speed != 1 {
			lost("the sound “%s” at %s plays at its normal speed (added sounds don't change speed)", so.Name, clockSec(so.At))
		}
		in := so.In - u.Offset
		length := so.Length
		at := moved(so.At)
		end := moved(so.At + so.Length)
		if end-at < length-1e-3 {
			lost("the sound “%s” at %s is shorter: footage left out of the cut took part of it", so.Name, clockSec(so.At))
			length = end - at
		}
		if in < 0 {
			length += in
			in = 0
		}
		outPt := in + length
		if u.Duration > 0 && outPt > u.Duration+0.05 {
			outPt = u.Duration
		}
		if outPt-in < 0.05 {
			continue
		}
		t := editspec.AudioTrack{ID: SpecID(so.Name, "sound-", taken), AssetID: u.AssetID, Role: specRole(so.Role), In: round6(in), Out: round6(outPt), At: round6(math.Max(0, at)), Fit: editspec.TrackFitManual}
		if so.Level != nil && math.Abs(*so.Level) > 0.05 {
			g := math.Max(-60, math.Min(12, round1(*so.Level)))
			t.GainDB = &g
		}
		if so.FadeIn > 0 {
			t.FadeIn = round3f(math.Min(so.FadeIn, 10))
		}
		if so.FadeOut > 0 {
			t.FadeOut = round3f(math.Min(so.FadeOut, 10))
		}
		spec.Audio = append(spec.Audio, t)
	}
	for _, c := range f.Chapters {
		res.Chapters = append(res.Chapters, FlatChapter{At: round3f(moved(c.At)), Title: c.Title})
	}
	for _, n := range f.Notes {
		ci := -1
		if n.Shot >= 0 && n.Shot < len(res.ClipFor) {
			ci = res.ClipFor[n.Shot]
		}
		at := moved(n.At)
		res.Notes = append(res.Notes, FlatNote{At: round3f(at), Shot: ci, Text: n.Text})
		text := fmt.Sprintf("%s (marker): %s", clockSec(at), n.Text)
		switch {
		case ci >= 0:
			c := &spec.Clips[ci]
			c.Notes = joinNote(c.Notes, text)
		default:
			spec.Notes = joinNote(spec.Notes, text)
		}
	}
	if len(spec.Clips) == 0 {
		lost("none of the timeline's footage is in the Library, so there is nothing to make a cut from")
	}
	return res
}

// flatStart is where the shot's picture starts on the flat timing (before
// MetaRush pulls it in for a transition).
func (s Shot) flatStart() float64 { return s.At + s.HandleBefore }

func joinNote(have, add string) string {
	if have == "" {
		return clip(add, 4000)
	}
	return clip(have+"\n"+add, 4000)
}

func transitionFor(j Join) *editspec.Transition {
	d := round3f(j.Before + j.After)
	t := &editspec.Transition{Duration: d, Direction: j.Direction, Easing: j.Easing}
	switch j.Kind {
	case "":
		return nil
	case TransFadeIn:
		t.Type = editspec.TransitionFade
	case TransDipBlack:
		t.Type = editspec.TransitionDip
	case TransDipWhite:
		t.Type = editspec.TransitionDipWhite
	case TransWipe:
		t.Type = editspec.TransitionWipe
	case TransSlide:
		t.Type = editspec.TransitionSlide
	case TransPush:
		t.Type = editspec.TransitionPush
	case TransZoom:
		t.Type = editspec.TransitionZoom
	case TransWhip:
		t.Type = editspec.TransitionWhip
	default:
		t.Type = editspec.TransitionCrossfade
	}
	if j.From != "" && j.From != t.Type {
		// MetaRush's own export says exactly which transition it was.
		switch j.From {
		case editspec.TransitionCrossfade, editspec.TransitionFade, editspec.TransitionDip, editspec.TransitionDipWhite, editspec.TransitionWipe,
			editspec.TransitionSlide, editspec.TransitionPush, editspec.TransitionZoom, editspec.TransitionWhip:
			t.Type = j.From
		}
	}
	if !transitionTakesDirection(t.Type) {
		t.Direction = ""
	}
	if d <= 0 {
		return nil
	}
	return t
}

func transitionTakesDirection(t string) bool {
	switch t {
	case editspec.TransitionWipe, editspec.TransitionSlide, editspec.TransitionPush, editspec.TransitionWhip:
		return true
	}
	return false
}

func specRole(r string) string {
	switch r {
	case RoleMusic:
		return editspec.RoleMusic
	case RoleEffects:
		return editspec.RoleSFX
	case RoleNarration:
		return editspec.RoleVoiceover
	}
	return editspec.RoleDialogue
}

func round6(v float64) float64  { return math.Round(v*1e6) / 1e6 }
func round3f(v float64) float64 { return math.Round(v*1000) / 1000 }

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

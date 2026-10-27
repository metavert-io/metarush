package editspec

import "math"

// Music timing. A track placed by hand (fit absent or manual) plays exactly
// in → out from at, as tracks always have. A track with fit auto is timed to
// the video here, at plan time, so it keeps fitting when the cut changes:
//
//   - it starts at at (normally the start of the video), with a short fade-in;
//   - a track longer than the room it has is cut so it ends with the video, on
//     the last beat found in the music within the final stretch when there is
//     one (MusicFacts), with a smooth fade that finishes there;
//   - a shorter track plays to its end and fades out early, or — with loop —
//     repeats (each repeat blends into the next) and the last repeat ends with
//     the video like a longer track.

// MusicFacts is optionally implemented by Facts: the beats (seconds into the
// file) found in a music file when it was added (beats-1: one tempo, the first
// quarter hour). Tracks without an ending use them, as they always did.
// Without them auto-fit tracks still end with the video, just not on a beat.
type MusicFacts interface {
	Beats(assetID string) (beats []float64, ok bool)
}

// MusicGrid is the whole track's analysis (beats-2, D120): beats at a tempo
// that may change, where bars start and where phrases start (seconds into
// the file). Tracks with an ending use it.
type MusicGrid struct {
	Beats     []float64 `json:"beats"`
	Downbeats []float64 `json:"downbeats,omitempty"`
	Phrases   []float64 `json:"phrases,omitempty"`
}

// MusicGridFacts is optionally implemented by Facts: a music file's grid.
type MusicGridFacts interface {
	MusicGrid(assetID string) (*MusicGrid, bool)
}

// Music fit defaults (seconds).
const (
	MusicFitFadeIn    = 0.3  // a short fade-in so the first note never clicks
	MusicFitFadeOut   = 2.5  // the smooth fade at the end
	MusicBeatWindow   = 1.5  // how far before the video's end an ending beat may be
	MusicBarWindow    = 2.5  // … an ending bar line (ending: phrase)
	MusicPhraseWindow = 4.0  // … the start of the next phrase (ending: phrase)
	MusicLoopBlend    = 0.08 // the blend between two repeats of a looped track
	musicMinPlay      = 2.0  // never end on a beat so early that the music barely plays
	musicMaxLoopPlay  = 200  // repeats at most (a very short loop in a long video)
)

// What a fitted track ends on (PlanAudio.EndsOn, FitInfo.EndsOn).
const (
	EndsOnPhrase = "phrase"
	EndsOnBar    = "bar"
	EndsOnBeat   = "beat"
)

// trackPiece is one play of a track on the timeline (frames).
type trackPiece struct {
	id         string
	from, dur  int
	trim       int // output frames into the file
	fadeIn     int
	fadeOut    int
	linearIn   bool // repeat blends: equal and opposite linear ramps
	linearOut  bool
	piece      int
	endsOnBeat bool
	endsOn     string // phrase | bar | beat (tracks with an ending)
	auto       bool
}

// endPoint is a place a fitted track may end (seconds into the file) and what it is.
type endPoint struct {
	at     float64
	kind   string
	window float64
}

// endPoints lists where a track with an ending may end, best kind first:
// for phrase, the starts of phrases, then bar lines, then beats; for beat,
// the beats. Each kind has its own reach before the video's end.
func endPoints(ending string, g *MusicGrid) [][]endPoint {
	if g == nil {
		return nil
	}
	kind := func(times []float64, k string, w float64) []endPoint {
		out := make([]endPoint, len(times))
		for i, t := range times {
			out[i] = endPoint{at: t, kind: k, window: w}
		}
		return out
	}
	if ending == EndingBeat {
		return [][]endPoint{kind(g.Beats, EndsOnBeat, MusicBeatWindow)}
	}
	return [][]endPoint{kind(g.Phrases, EndsOnPhrase, MusicPhraseWindow), kind(g.Downbeats, EndsOnBar, MusicBarWindow), kind(g.Beats, EndsOnBeat, MusicBeatWindow)}
}

// trackPieces is where a track plays: one piece for a track placed by hand
// (exactly the values tracks always had), one or more for fit auto.
func trackPieces(a AudioTrack, fps float64, total int, facts Facts) []trackPiece {
	from := int(math.Round(a.At * fps))
	if a.Fit != TrackFitAuto {
		dur := int(math.Round((a.Out - a.In) * fps))
		if from+dur > total {
			dur = total - from
		}
		if dur <= 0 {
			return nil
		}
		return []trackPiece{{id: a.ID, from: from, dur: dur, trim: int(math.Round(a.In * fps)),
			fadeIn: int(math.Round(a.FadeIn * fps)), fadeOut: int(math.Round(a.FadeOut * fps))}}
	}
	if from >= total {
		return nil
	}
	in, out := a.In, a.Out
	if facts != nil {
		if af, ok := facts.Asset(a.AssetID); ok && af.Duration > 0 && (out <= in || out > af.Duration) {
			out = af.Duration
		}
	}
	if out <= in {
		return nil
	}
	var beats []float64
	var points [][]endPoint
	if a.Ending != "" {
		if gf, ok := facts.(MusicGridFacts); ok && facts != nil {
			if g, ok := gf.MusicGrid(a.AssetID); ok {
				points = endPoints(a.Ending, g)
			}
		}
	} else if mf, ok := facts.(MusicFacts); ok && facts != nil {
		beats, _ = mf.Beats(a.AssetID)
	}
	fadeIn := MusicFitFadeIn
	if a.FadeIn > 0 {
		fadeIn = a.FadeIn
	}
	fadeOut := MusicFitFadeOut
	if a.FadeOut > 0 {
		fadeOut = a.FadeOut
	}
	room := total - from
	avail := int(math.Round((out - in) * fps))
	blend := max(1, int(math.Round(MusicLoopBlend*fps)))
	// ending cuts a play of [in, …) that starts at timeline frame start so it
	// stops by the end of the video: on a beat near the end when there is one
	// (tracks with an ending: the start of the next phrase within reach, else
	// a bar line, else a beat).
	ending := func(start int, playFrom float64) (dur int, onBeat bool, on string) {
		left := total - start // frames this play may last at most
		end := playFrom + float64(left)/fps
		if a.Ending != "" {
			for _, kind := range points {
				best, what := -1.0, ""
				for _, p := range kind {
					window := math.Min(p.window, float64(left)/fps/4)
					if p.at <= end+1e-6 && p.at >= end-window && p.at-playFrom >= musicMinPlay && p.at > best {
						best, what = p.at, p.kind
					}
				}
				if best > 0 {
					return min(left, int(math.Round((best-playFrom)*fps))), true, what
				}
			}
			return left, false, ""
		}
		window := math.Min(MusicBeatWindow, float64(left)/fps/4)
		best := -1.0
		for _, b := range beats {
			if b <= end+1e-6 && b >= end-window && b-playFrom >= musicMinPlay && b > best {
				best = b
			}
		}
		if best > 0 {
			return min(left, int(math.Round((best-playFrom)*fps))), true, ""
		}
		return left, false, ""
	}
	fadeFrames := func(sec float64, dur int) int { return min(int(math.Round(sec*fps)), dur/3) }
	switch {
	case avail >= room: // longer than the room: one play, ending with the video
		dur, onBeat, on := ending(from, in)
		if dur <= 0 {
			return nil
		}
		return []trackPiece{{id: a.ID, from: from, dur: dur, trim: int(math.Round(in * fps)), fadeIn: fadeFrames(fadeIn, dur),
			fadeOut: fadeFrames(fadeOut, dur), endsOnBeat: onBeat, endsOn: on, auto: true}}
	case !a.Loop: // shorter: plays to its end and fades out early
		return []trackPiece{{id: a.ID, from: from, dur: avail, trim: int(math.Round(in * fps)), fadeIn: fadeFrames(fadeIn, avail),
			fadeOut: fadeFrames(fadeOut, avail), auto: true}}
	}
	// Looped: repeats that overlap by the blend until the video ends.
	if avail <= 2*blend {
		return nil
	}
	var ps []trackPiece
	start := from
	for k := 0; k < musicMaxLoopPlay && start < total; k++ {
		p := trackPiece{id: a.ID, from: start, dur: avail, trim: int(math.Round(in * fps)), piece: k, auto: true}
		if k > 0 {
			p.id = a.ID + "~" + itoa(k+1)
			p.fadeIn, p.linearIn = blend, true
		} else {
			p.fadeIn = fadeFrames(fadeIn, avail)
		}
		if start+avail >= total { // the last repeat ends with the video
			p.dur, p.endsOnBeat, p.endsOn = ending(start, in)
			if p.dur <= 0 {
				break
			}
			p.fadeOut = fadeFrames(fadeOut, p.dur)
			ps = append(ps, p)
			break
		}
		p.fadeOut, p.linearOut = blend, true
		ps = append(ps, p)
		start += avail - blend
	}
	return ps
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

// pieceFade is a piece's fade gain at element frame f.
func (p trackPiece) fade(f int) float64 {
	v := 1.0
	if p.fadeIn > 0 {
		x := float64(f) / float64(p.fadeIn)
		if p.linearIn {
			v *= math.Max(0, math.Min(1, x))
		} else {
			v *= smoothstep(x)
		}
	}
	if p.fadeOut > 0 {
		x := float64(p.dur-f) / float64(p.fadeOut)
		if p.linearOut {
			v *= math.Max(0, math.Min(1, x))
		} else {
			v *= smoothstep(x)
		}
	}
	return v
}

// FitInfo describes where an auto-fit track plays (for people and the
// dashboard): from and to on the timeline (seconds), whether it loops and
// whether it ends on a beat.
type FitInfo struct {
	From, To   float64
	Repeats    int
	EndsOnBeat bool
	EndsOn     string // phrase | bar | beat (tracks with an ending)
	EndsEarly  bool   // a shorter track that stops before the video does
}

// MusicFit reports how a track is placed in a cut (any fit).
func MusicFit(s *Spec, a AudioTrack, facts Facts) (FitInfo, bool) {
	fps := float64(s.Output.FPS)
	if fps <= 0 {
		fps = 30
	}
	_, total := Timeline(s)
	ps := trackPieces(a, fps, total, facts)
	if len(ps) == 0 {
		return FitInfo{}, false
	}
	last := ps[len(ps)-1]
	fi := FitInfo{From: float64(ps[0].from) / fps, To: float64(last.from+last.dur) / fps, Repeats: len(ps), EndsOnBeat: last.endsOnBeat, EndsOn: last.endsOn}
	// Ending on the music's own beat, bar or phrase near the end is not early.
	fi.EndsEarly = !last.endsOnBeat && last.from+last.dur < total-int(math.Round(MusicBeatWindow*fps))
	return fi, true
}

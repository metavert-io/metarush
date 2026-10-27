package editspec

import (
	"encoding/json"
	"fmt"
	"math"
)

// Fit strategies.
const (
	FixExtendShot   = "extend-shot"   // lengthen the shot under the line so the next line moves later
	FixSpeedUp      = "speed-up"      // play generated speech slightly faster (≤ 8%)
	FixShorten      = "shorten"       // rewrite the line with fewer words
	FixStartEarlier = "start-earlier" // start the line a little earlier, into a pause
	FixMoveNext     = "move-next"     // move the next line (when it sits at a fixed clock time)
)

// BeatFit is the fit report for one line.
type BeatFit struct {
	BeatID  string  `json:"beatId"`
	Line    int     `json:"line"`
	Text    string  `json:"text"`
	Words   int     `json:"words"`
	Start   float64 `json:"start"`  // timeline seconds
	Window  float64 `json:"window"` // seconds before the next line
	Needed  float64 `json:"needed"` // seconds the line takes
	Basis   string  `json:"basis" jsonschema:"enum=take,enum=estimate"`
	Status  string  `json:"status" jsonschema:"enum=ok,enum=tight,enum=over,enum=unresolved"`
	Message string  `json:"message"`
	// Short is the message as the line itself shows it ("This line has 28 words…").
	Short     string   `json:"short"`
	Fixes     []FitFix `json:"fixes,omitempty"`
	Preferred string   `json:"preferred,omitempty" jsonschema:"description=The fix the fit policy picks (empty for ask)."`
	// IntoNextShot is how far a line's sound runs past the end of the shot it
	// starts on, into the next one (seconds; set only past 0.3 s, for lines with
	// sound that otherwise fit), and NextShot names that shot. Fixes then holds
	// the ways to end it in its own shot.
	IntoNextShot float64 `json:"intoNextShot,omitempty"`
	NextShot     string  `json:"nextShot,omitempty"`
}

// shotOverrunTolerance is how far a line may run into the next shot unflagged.
const shotOverrunTolerance = 0.3

// FitFix is one way to make a line fit, as edit ops when it needs no rewriting.
type FitFix struct {
	Strategy    string  `json:"strategy" jsonschema:"enum=extend-shot,enum=speed-up,enum=shorten,enum=start-earlier,enum=move-next"`
	Summary     string  `json:"summary"`
	Ops         []Op    `json:"ops,omitempty"`
	TargetWords int     `json:"targetWords,omitempty"`
	Speed       float64 `json:"speed,omitempty"`
	Seconds     float64 `json:"seconds,omitempty"`
}

func setJSON(v map[string]any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// FitReport checks every line against the time it has and proposes fixes per
// the voiceover fit policy. It needs the asset durations from facts to know how
// far a shot can be extended.
func FitReport(s *Spec, facts Facts) []BeatFit {
	if s.Voiceover == nil {
		return nil
	}
	Normalize(s)
	clips, total := Timeline(s)
	fps := float64(s.Output.FPS)
	beats := ResolveBeats(s, clips, total, facts)
	policy := s.FitPolicy()
	wpm := s.SpeakingRate()
	var out []BeatFit
	for i, rb := range beats {
		b := s.Voiceover.Beats[rb.Index]
		f := BeatFit{BeatID: b.ID, Line: rb.Index + 1, Text: b.Text, Words: rb.Words, Start: round3(float64(rb.Start) / fps),
			Window: round3(float64(rb.Window()) / fps), Needed: round3(float64(rb.Speech) / fps), Basis: "take", Status: fitStatus(rb)}
		if rb.Estimated {
			f.Basis = "estimate"
		}
		say := func(who string) string {
			switch f.Status {
			case "unresolved":
				return who + " can't be placed: " + rb.Problem
			case "ok":
				return fmt.Sprintf("%s fits (%.1f of %.1f seconds)", who, f.Needed, f.Window)
			case "tight":
				return fmt.Sprintf("%s just fits (%.1f of %.1f seconds) — %s", who, f.Needed, f.Window, rb.pauseBefore())
			}
			return overMessage(who, rb, f.Window, f.Needed, total, fps)
		}
		f.Message, f.Short = say(lineName(rb.Index, b.Text)), say("This line")
		if f.Status == "over" {
			f.Fixes = fitFixes(s, clips, beats, i, facts, fps, wpm)
			f.Preferred = preferredFix(policy, b, f.Fixes)
		} else if over, next, ok := shotOverrun(s, clips, rb, total, fps); ok {
			f.IntoNextShot, f.NextShot = over, next
			f.Message = fmt.Sprintf("%s runs %.1f seconds into the next shot (%s)", lineName(rb.Index, b.Text), over, next)
			f.Short = fmt.Sprintf("This line runs %.1f seconds into the next shot (%s)", over, next)
			f.Fixes = shotFixes(s, clips, beats, i, facts, fps, over)
		}
		out = append(out, f)
	}
	return out
}

// shotOverrun reports how far a line with sound runs past the end of the shot
// it starts on into the next one, and names that shot.
func shotOverrun(s *Spec, clips []PlanClip, rb ResolvedBeat, total int, fps float64) (float64, string, bool) {
	if rb.Problem != "" || rb.Estimated || rb.Clip == "" {
		return 0, "", false
	}
	for k, pc := range clips {
		if pc.ID != rb.Clip {
			continue
		}
		end := pc.From + pc.DurationInFrames
		over := float64(rb.End()-end) / fps
		if end >= total || over <= shotOverrunTolerance {
			return 0, "", false
		}
		next := ""
		for j := k + 1; j < len(clips); j++ {
			if end >= clips[j].From && end < clips[j].From+clips[j].DurationInFrames {
				next = shotWords(s, clips[j].ID)
				break
			}
		}
		if next == "" {
			return 0, "", false
		}
		return math.Round(over*10) / 10, next, true
	}
	return 0, "", false
}

// shotWords names a clip the way people see it: "shot 3 (“Payoff”)".
func shotWords(s *Spec, id string) string {
	for i, c := range s.Clips {
		if c.ID == id {
			if c.Label != "" {
				return fmt.Sprintf("shot %d (“%s”)", i+1, c.Label)
			}
			return fmt.Sprintf("shot %d", i+1)
		}
	}
	return "the next shot"
}

// shotFixes are the ways to end a line in the shot it starts on: hold that
// shot longer, or start the line earlier into the pause before it.
func shotFixes(s *Spec, clips []PlanClip, beats []ResolvedBeat, i int, facts Facts, fps, over float64) []FitFix {
	rb := beats[i]
	b := s.Voiceover.Beats[rb.Index]
	var fixes []FitFix
	for k, pc := range clips {
		if pc.ID != rb.Clip {
			continue
		}
		spec := s.Clips[k]
		need := (over + 0.15) * pc.PlaybackRate
		avail := math.Inf(1)
		if facts != nil {
			if a, ok := facts.Asset(spec.AssetID); ok && a.Duration > 0 {
				avail = a.Duration - spec.Out
			}
		}
		if !spec.Locked && avail >= need {
			fixes = append(fixes, FitFix{Strategy: FixExtendShot, Seconds: round3(need / pc.PlaybackRate),
				Summary: fmt.Sprintf("Hold %s %.1f seconds longer so the line ends before the cut", shotWords(s, pc.ID), need/pc.PlaybackRate),
				Ops:     []Op{{Op: "updateClip", ID: spec.ID, Set: setJSON(map[string]any{"out": round3(spec.Out + need)})}}})
		}
		break
	}
	if b.Anchor.Kind != AnchorAfter {
		free := float64(rb.Start-prevEnd(beats, i))/fps - 0.3
		if move := math.Min(free, over+0.1); move >= over {
			a := b.Anchor
			a.Offset = round3(a.Offset - move)
			if math.Abs(a.Offset) <= 60 {
				fixes = append(fixes, FitFix{Strategy: FixStartEarlier, Seconds: round3(move),
					Summary: fmt.Sprintf("Start it %.1f seconds earlier, in the pause before it", move),
					Ops:     []Op{{Op: "moveBeat", ID: b.ID, Anchor: &a}}})
			}
		}
	}
	return fixes
}

// overMessage says how a line runs over, in plain words: up to the next line,
// a later line that now plays first (rearranged shots), or the end of the video.
func overMessage(who string, rb ResolvedBeat, window, needed float64, total int, fps float64) string {
	if rb.Start >= total {
		return fmt.Sprintf("%s would start at %s, after the video ends (%s)", who, Clock(float64(rb.Start)/fps), Clock(float64(total)/fps))
	}
	if rb.Estimated {
		return fmt.Sprintf("%s has %d words but only %.1f seconds %s — it needs about %.1f seconds", who, rb.Words, window, rb.Until(), needed)
	}
	return fmt.Sprintf("%s's recording runs %.1f seconds but there are only %.1f seconds %s", who, needed, window, rb.Until())
}

func fitFixes(s *Spec, clips []PlanClip, beats []ResolvedBeat, i int, facts Facts, fps, wpm float64) []FitFix {
	rb := beats[i]
	b := s.Voiceover.Beats[rb.Index]
	deficit := float64(rb.Speech-(rb.WindowEnd-rb.Start)) / fps
	var fixes []FitFix
	// The line whose start ends this one's time (the next in time, not
	// necessarily the next in the script).
	var next *ResolvedBeat
	if rb.Next >= 0 {
		next = &beats[rb.Next]
	}
	var nextAnchor *Anchor
	if next != nil {
		a := s.Voiceover.Beats[next.Index].Anchor
		nextAnchor = &a
	}
	// Extend the shot that is on screen when the line would run over.
	movable := nextAnchor == nil || nextAnchor.Kind != AnchorTime
	if movable {
		end := rb.WindowEnd - 1
		for k := len(clips) - 1; k >= 0; k-- {
			pc := clips[k]
			if end < pc.From || end >= pc.From+pc.DurationInFrames {
				continue
			}
			// A next line anchored to a moment inside this very clip does not move.
			if nextAnchor != nil && nextAnchor.Kind == AnchorMoment && nextAnchor.AssetID == pc.AssetID && nextAnchor.At != nil &&
				*nextAnchor.At >= pc.SourceIn && *nextAnchor.At < pc.SourceOut {
				break
			}
			if nextAnchor != nil && nextAnchor.Kind == AnchorClip && nextAnchor.ClipID == pc.ID && nextAnchor.Edge != "end" {
				break
			}
			spec := s.Clips[k]
			need := (deficit + 0.15) * pc.PlaybackRate
			avail := math.Inf(1)
			if facts != nil {
				if a, ok := facts.Asset(spec.AssetID); ok && a.Duration > 0 {
					avail = a.Duration - spec.Out
				}
			}
			if spec.Locked || avail < 0.2 {
				break
			}
			add := math.Min(need, avail)
			newOut := round3(spec.Out + add)
			fx := FitFix{Strategy: FixExtendShot, Seconds: round3(add / pc.PlaybackRate),
				Ops: []Op{{Op: "updateClip", ID: spec.ID, Set: setJSON(map[string]any{"out": newOut})}}}
			if add < need {
				fx.Summary = fmt.Sprintf("Hold the shot under it %.1f seconds longer (all the footage there is; it still runs %.1f seconds over)", add/pc.PlaybackRate, deficit-add/pc.PlaybackRate)
			} else {
				fx.Summary = fmt.Sprintf("Hold the shot under it %.1f seconds longer so the next part starts later", add/pc.PlaybackRate)
			}
			fixes = append(fixes, fx)
			break
		}
	}
	// Start earlier, into the pause before it.
	if b.Anchor.Kind != AnchorAfter {
		free := float64(rb.Start-prevEnd(beats, i))/fps - 0.3
		if free >= 0.2 {
			move := math.Min(free, deficit+0.1)
			a := b.Anchor
			a.Offset = round3(a.Offset - move)
			fixes = append(fixes, FitFix{Strategy: FixStartEarlier, Seconds: round3(move),
				Summary: fmt.Sprintf("Start it %.1f seconds earlier, in the pause before it", move),
				Ops:     []Op{{Op: "moveBeat", ID: b.ID, Anchor: &a}}})
		}
	}
	// Speed up generated speech by at most 8%.
	if t := b.Take; t != nil && (t.Source == "tts" || t.Source == "scratch") {
		speed := float64(rb.Speech) / float64(max(1, rb.Window())) * t.Rate()
		if speed <= MaxTTSSpeedUp+1e-9 {
			nt := *t
			nt.Speed = math.Ceil(speed*1000) / 1000
			raw, _ := json.Marshal(map[string]any{"take": nt})
			fixes = append(fixes, FitFix{Strategy: FixSpeedUp, Speed: nt.Speed,
				Summary: fmt.Sprintf("Play the voice %.0f%% faster (barely noticeable at this amount)", (nt.Speed/t.Rate()-1)*100),
				Ops:     []Op{{Op: "updateBeat", ID: b.ID, Set: raw}}})
		}
	}
	// Move the next line when it sits at a fixed clock time.
	if nextAnchor != nil && nextAnchor.Kind == AnchorTime && nextAnchor.At != nil {
		a := *nextAnchor
		at := round3(*a.At + deficit + 0.15)
		a.At = &at
		fixes = append(fixes, FitFix{Strategy: FixMoveNext, Seconds: round3(deficit + 0.15),
			Summary: fmt.Sprintf("Move the next line %.1f seconds later", deficit+0.15),
			Ops:     []Op{{Op: "moveBeat", ID: s.Voiceover.Beats[next.Index].ID, Anchor: &a}}})
	}
	// Shorten: the words that fit at the narrator's pace.
	rate := wpm
	if !rb.Estimated && rb.Speech > 0 && rb.Words > 0 {
		rate = float64(rb.Words) / (float64(rb.Speech) / fps) * 60
	}
	target := int(math.Floor(float64(rb.Window()) / fps * rate / 60 * 0.95))
	if target >= 1 && target < rb.Words {
		fixes = append(fixes, FitFix{Strategy: FixShorten, TargetWords: target,
			Summary: fmt.Sprintf("Shorten it to about %d words (it has %d)", target, rb.Words)})
	}
	return fixes
}

func preferredFix(policy string, b Beat, fixes []FitFix) string {
	has := func(st string) bool {
		for _, f := range fixes {
			if f.Strategy == st {
				return true
			}
		}
		return false
	}
	pick := func(order ...string) string {
		for _, st := range order {
			if has(st) {
				return st
			}
		}
		return ""
	}
	switch policy {
	case FitAsk:
		return ""
	case FitShorten:
		return pick(FixShorten)
	}
	if b.Take == nil {
		return pick(FixShorten, FixStartEarlier, FixExtendShot, FixMoveNext)
	}
	switch b.Take.Source {
	case "tts", "scratch":
		return pick(FixSpeedUp, FixExtendShot, FixStartEarlier, FixMoveNext, FixShorten)
	}
	return pick(FixExtendShot, FixStartEarlier, FixMoveNext, FixShorten)
}

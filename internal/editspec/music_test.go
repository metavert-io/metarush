package editspec

import (
	"math"
	"testing"
)

func musicPlan(t *testing.T, a AudioTrack, beats []float64, videoSec float64) (*RenderPlan, []PlanAudio) {
	t.Helper()
	f := talkFacts()
	f.beats[astBed] = beats
	s := &Spec{Output: Output{Preset: PresetLandscape, FPS: 30}, Clips: []Clip{{ID: "talk", AssetID: astTalk, In: 0, Out: videoSec}}, Audio: []AudioTrack{a}}
	Normalize(s)
	plan := Resolve(s, f, "", "", "")
	var out []PlanAudio
	for _, x := range plan.Audio {
		if x.Role == RoleMusic {
			out = append(out, x)
		}
	}
	return plan, out
}

// A longer track ends with the video, on the last beat within the final
// stretch, with a smooth fade that finishes there.
func TestAutoFitLongerTrackEndsOnABeatNearTheEnd(t *testing.T) {
	beats := []float64{}
	for b := 0.25; b < 30; b += 0.5 { // 120 bpm, offset a quarter second
		beats = append(beats, b)
	}
	a := AudioTrack{ID: "bed", AssetID: astBed, Role: RoleMusic, In: 0, Out: 30, At: 0, Fit: TrackFitAuto}
	plan, ms := musicPlan(t, a, beats, 10)
	if len(ms) != 1 {
		t.Fatalf("one play, got %d", len(ms))
	}
	m := ms[0]
	end := float64(m.From+m.DurationInFrames) / 30
	if !m.EndsOnBeat || m.Fit != TrackFitAuto {
		t.Fatalf("expected an ending on a beat: %+v", m)
	}
	if math.Abs(end-9.75) > 1.0/30 {
		t.Fatalf("ends at %.3f s, want the beat at 9.75 s (the video ends at 10 s)", end)
	}
	if float64(plan.DurationInFrames)/30-end > MusicBeatWindow {
		t.Fatalf("ends too early")
	}
	if g := GainAt(m.Gain, float64(m.DurationInFrames-1)); g > 0.01 {
		t.Fatalf("the fade has finished by the end: %.4f", g)
	}
	if g := GainAt(m.Gain, float64(m.DurationInFrames)-MusicFitFadeOut*30-2); g < 0.95 {
		t.Fatalf("full level before the fade: %.3f", g)
	}
	if m.FadeOutFrames != int(math.Round(MusicFitFadeOut*30)) || m.FadeInFrames != int(math.Round(MusicFitFadeIn*30)) {
		t.Fatalf("fades: in %d out %d", m.FadeInFrames, m.FadeOutFrames)
	}
	// Without beats it ends exactly with the video.
	_, ms = musicPlan(t, a, nil, 10)
	if ms[0].EndsOnBeat || ms[0].From+ms[0].DurationInFrames != 300 {
		t.Fatalf("no beats: %+v", ms[0])
	}
	// The fit follows the cut: a longer video, a later ending.
	_, ms = musicPlan(t, a, beats, 14)
	if end := float64(ms[0].From+ms[0].DurationInFrames) / 30; math.Abs(end-13.75) > 1.0/30 {
		t.Fatalf("14 s video: ends at %.3f", end)
	}
}

// A track with an ending (D120) ends where the music starts its next phrase
// when one is within reach, else at a bar line, else on a beat — from the
// whole track's grid, never from the beats-1 beats — and says which; a track
// without one ends exactly as before.
func TestAutoFitEndsOnAPhraseThenABarThenABeat(t *testing.T) {
	var beats, downs, phrases []float64
	for b := 0.25; b < 60; b += 0.5 { // 120 bpm, 4/4, phrases of 4 bars (8 s)
		beats = append(beats, b)
		if k := int(math.Round((b - 0.25) / 0.5)); k%4 == 0 {
			downs = append(downs, b)
			if k%16 == 0 {
				phrases = append(phrases, b)
			}
		}
	}
	run := func(ending string, videoSec float64, g *MusicGrid, legacy []float64) (PlanAudio, FitInfo) {
		t.Helper()
		f := talkFacts()
		f.assets[astTalk] = AssetFacts{ID: astTalk, Name: "talk.mov", Kind: "video", State: "available", InProject: true, Duration: 120, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30}
		f.assets[astBed] = AssetFacts{ID: astBed, Name: "bed.m4a", Kind: "audio", State: "available", InProject: true, Duration: 60, HasAudio: true}
		f.grids = map[string]*MusicGrid{}
		if g != nil {
			f.grids[astBed] = g
		}
		if legacy != nil {
			f.beats[astBed] = legacy
		}
		a := AudioTrack{ID: "bed", AssetID: astBed, Role: RoleMusic, In: 0, Out: 60, At: 0, Fit: TrackFitAuto, Ending: ending}
		s := &Spec{Output: Output{Preset: PresetLandscape, FPS: 30}, Clips: []Clip{{ID: "talk", AssetID: astTalk, In: 0, Out: videoSec}}, Audio: []AudioTrack{a}}
		Normalize(s)
		plan := Resolve(s, f, "", "", "")
		fi, _ := MusicFit(s, s.Audio[0], f)
		for _, x := range plan.Audio {
			if x.Role == RoleMusic {
				return x, fi
			}
		}
		t.Fatal("no music")
		return PlanAudio{}, fi
	}
	end := func(p PlanAudio) float64 { return float64(p.From+p.DurationInFrames) / 30 }
	grid := &MusicGrid{Beats: beats, Downbeats: downs, Phrases: phrases}
	// The video ends at 35 s: the phrase starting at 32.25 s is within 4 s.
	p, fi := run(EndingPhrase, 35, grid, nil)
	if math.Abs(end(p)-32.25) > 1.0/30 || p.EndsOn != EndsOnPhrase || !p.EndsOnBeat || fi.EndsOn != EndsOnPhrase || fi.EndsEarly {
		t.Fatalf("phrase: ends at %.3f (%s), fit %+v", end(p), p.EndsOn, fi)
	}
	// At 31 s no phrase start is within 4 s (24.25 is 6.75 s back): the bar line at 30.25.
	if p, _ = run(EndingPhrase, 31, grid, nil); math.Abs(end(p)-30.25) > 1.0/30 || p.EndsOn != EndsOnBar {
		t.Fatalf("bar: ends at %.3f (%s)", end(p), p.EndsOn)
	}
	// Without bars: the last beat within 1.5 s.
	if p, _ = run(EndingPhrase, 31, &MusicGrid{Beats: beats}, nil); math.Abs(end(p)-30.75) > 1.0/30 || p.EndsOn != EndsOnBeat {
		t.Fatalf("beat: ends at %.3f (%s)", end(p), p.EndsOn)
	}
	// ending beat: the last beat, even with phrases there.
	if p, _ = run(EndingBeat, 35, grid, nil); math.Abs(end(p)-34.75) > 1.0/30 || p.EndsOn != EndsOnBeat {
		t.Fatalf("ending beat: ends at %.3f (%s)", end(p), p.EndsOn)
	}
	// An ending never reads the beats-1 beats, and no grid ends with the video.
	if p, _ = run(EndingPhrase, 35, nil, []float64{34.9}); end(p) != 35 || p.EndsOnBeat || p.EndsOn != "" {
		t.Fatalf("no grid: ends at %.3f (%+v)", end(p), p)
	}
	// Without an ending: beats-1 as before, and the grid is never read.
	if p, _ = run("", 35, grid, []float64{34.6}); math.Abs(end(p)-34.6) > 1.0/30 || p.EndsOn != "" || !p.EndsOnBeat {
		t.Fatalf("legacy: ends at %.3f (%+v)", end(p), p)
	}
}

// A shorter track fades out early; with loop it repeats until the video ends.
func TestAutoFitShorterTrackFadesEarlyOrLoops(t *testing.T) {
	f := talkFacts()
	f.assets[astBed] = AssetFacts{ID: astBed, Name: "short.m4a", Kind: "audio", State: "available", InProject: true, Duration: 4, HasAudio: true}
	s := &Spec{Output: Output{Preset: PresetLandscape, FPS: 30}, Clips: []Clip{{ID: "talk", AssetID: astTalk, In: 0, Out: 10}},
		Audio: []AudioTrack{{ID: "bed", AssetID: astBed, Role: RoleMusic, In: 0, Out: 4, At: 0, Fit: TrackFitAuto}}}
	Normalize(s)
	plan := Resolve(s, f, "", "", "")
	if len(plan.Audio) != 1 || plan.Audio[0].DurationInFrames != 120 || plan.Audio[0].FadeOutFrames == 0 {
		t.Fatalf("a shorter track plays once and fades out: %+v", plan.Audio)
	}
	if fi, ok := MusicFit(s, s.Audio[0], f); !ok || !fi.EndsEarly || fi.Repeats != 1 {
		t.Fatalf("fit info %+v", fi)
	}
	s.Audio[0].Loop = true
	plan = Resolve(s, f, "", "", "")
	if len(plan.Audio) < 3 {
		t.Fatalf("a looped 4 s track in a 10 s video plays 3 times: %+v", plan.Audio)
	}
	last := plan.Audio[len(plan.Audio)-1]
	if last.From+last.DurationInFrames != plan.DurationInFrames {
		t.Fatalf("the last repeat ends with the video: %d+%d vs %d", last.From, last.DurationInFrames, plan.DurationInFrames)
	}
	ids := map[string]bool{}
	for i, p := range plan.Audio {
		if ids[p.ID] {
			t.Fatalf("duplicate element id %s", p.ID)
		}
		ids[p.ID] = true
		if i > 0 {
			prev := plan.Audio[i-1]
			if overlap := prev.From + prev.DurationInFrames - p.From; overlap < 1 || overlap > 4 {
				t.Fatalf("repeats blend over a few frames, got %d", overlap)
			}
			// Equal and opposite ramps: the sum stays at the level while one hands over.
			mid := p.From + 1
			a := GainAt(prev.Gain, float64(mid-prev.From))
			b := GainAt(p.Gain, float64(mid-p.From))
			if math.Abs(a+b-GainAt(prev.Gain, float64(prev.DurationInFrames/2))) > 0.05 {
				t.Fatalf("the blend dips or bumps: %.3f + %.3f", a, b)
			}
		}
	}
}

// Placed by hand: exactly as tracks always played (the golden plans cover the
// rest), and the duck switch turns the music down under speech or never.
func TestManualTrackAndDuckSwitch(t *testing.T) {
	yes, no := true, false
	a := AudioTrack{ID: "bed", AssetID: astBed, Role: RoleMusic, In: 2, Out: 12, At: 1, FadeIn: 1}
	_, ms := musicPlan(t, a, nil, 10)
	if len(ms) != 1 || ms[0].From != 30 || ms[0].TrimBefore != 60 || ms[0].DurationInFrames != 270 || ms[0].Fit != "" {
		t.Fatalf("manual placement changed: %+v", ms)
	}
	// Legacy mixer (no narration, no mix): duck on dips under the clip's speech.
	a.Duck = &yes
	_, ms = musicPlan(t, a, nil, 10)
	inSpeech := GainAt(ms[0].Gain, 2*30-30) // timeline 2 s: Maya speaks
	inGap := GainAt(ms[0].Gain, 9.5*30-30)  // timeline 9.5 s: nobody (after 8.5 s + release)
	if !(inSpeech < inGap*0.5) {
		t.Fatalf("duck on: %.3f in speech vs %.3f in a pause", inSpeech, inGap)
	}
	a.Duck = &no
	_, ms = musicPlan(t, a, nil, 10)
	if GainAt(ms[0].Gain, 2*30-30) < 0.99 {
		t.Fatalf("duck off still dips")
	}
}

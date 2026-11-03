package breakdown

import (
	"math"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/plainwords"
)

// shortVideo is a vertical short's evidence: a question as the hook, an intro,
// three points of a list, a call to action and an end card; a title at the
// top, captions burned in low, three words at a time in capitals, music under
// the voice. It mirrors tests/fixtures/gen/reference-short.mp4.
func shortVideo(looked bool) Input {
	type line struct {
		from, to float64
		text     string
		caption  string
	}
	ls := []line{
		{0.4, 2.9, "Why do people stop watching so fast?", "WHY DO PEOPLE"},
		{3.5, 5.6, "Here are three fixes that work.", "THREE FIXES"},
		{6.8, 8.9, "First, lead with your strongest moment.", "STRONGEST MOMENT"},
		{10.8, 12.6, "Second, cut every slow pause.", "CUT EVERY PAUSE"},
		{14.8, 16.9, "Third, add big captions for silent viewers.", "BIG CAPTIONS"},
		{18.8, 20.9, "Follow for more tips every week.", "FOLLOW FOR MORE"},
	}
	in := Input{Duration: 26, Width: 720, Height: 1280, HasVideo: true, HasAudio: true, Heard: true, Measured: true, Looked: looked,
		Names: map[string]string{}, Cuts: []float64{3.2, 6.5, 10.5, 14.5, 18.5, 22}}
	for _, l := range ls {
		ws := strings.Fields(l.text)
		step := (l.to - l.from) / float64(len(ws))
		for i, w := range ws {
			in.Words = append(in.Words, Word{Text: w, From: l.from + step*float64(i), To: l.from + step*float64(i+1) - 0.05, Speaker: "speaker_0"})
		}
		if looked {
			in.Text = append(in.Text, Text{Text: l.caption, From: l.from, To: l.to, Box: [4]float64{0.1, 0.76, 0.8, 0.044}})
		}
	}
	if looked {
		in.Text = append(in.Text, Text{Text: "STOP THE SCROLL", From: 0, To: 3.2, Box: [4]float64{0.1, 0.08, 0.8, 0.06}},
			Text{Text: "FOLLOW FOR MORE", From: 22.1, To: 26, Box: [4]float64{0.1, 0.45, 0.8, 0.07}})
		in.Shots = []Shot{{Type: "close-up", From: 0, To: 6.5, Faces: 1, People: 1}, {Type: "wide", From: 6.5, To: 18.5}, {Type: "close-up", From: 18.5, To: 22, Faces: 1, People: 1},
			{Type: "none", From: 22, To: 26}}
	}
	// Loudness every 0.1 s: the voice at -18, music at -31 in the pauses and over the end card.
	l := &Loudness{Step: 0.1}
	for k := 0; k < 260; k++ {
		end := float64(k+1) * 0.1
		v := -31.0
		for _, x := range ls {
			if end-0.4 >= x.from-0.2 && end <= x.to+0.2 {
				v = -18
			}
		}
		l.Values = append(l.Values, v)
	}
	in.Loudness = l
	return in
}

func near(a, b float64) bool { return math.Abs(a-b) <= 0.3 }

// A point whose first word wasn't heard ("Third," lost by the recognizer, as
// Parakeet loses it on the fixture) still gets its part: the intro announced
// three fixes, so the body has three parts, each a point.
func TestCardKeepsAnAnnouncedListWhenAPointIsNotHeard(t *testing.T) {
	in := shortVideo(true)
	var ws []Word
	for _, w := range in.Words {
		if w.Text != "Third," {
			ws = append(ws, w)
		}
	}
	in.Words = ws
	c := Build(in)
	var parts []WhatSection
	for _, s := range c.Sections {
		if s.Kind == KindBody {
			parts = append(parts, s)
		}
	}
	if len(parts) != 3 || !near(parts[0].From, 6.5) || !near(parts[1].From, 10.5) || !near(parts[2].From, 14.5) || !near(parts[2].To, 18.5) {
		t.Fatalf("three parts at the cuts: %+v", parts)
	}
	if !strings.HasPrefix(parts[2].Does, "the third point") {
		t.Errorf("the unheard point is still a point: %q", parts[2].Does)
	}
	// Without the announcement, the parts follow the length and the points heard.
	for i, w := range in.Words {
		if w.Text == "three" {
			in.Words[i].Text = "some"
		}
	}
	if n := countKind(Build(in).Sections, KindBody); n != 2 {
		t.Errorf("two points heard, nothing announced: %d parts", n)
	}
}

func countKind(ss []WhatSection, kind string) int {
	n := 0
	for _, s := range ss {
		if s.Kind == kind {
			n++
		}
	}
	return n
}

func TestCardFindsTheSectionsOfAShort(t *testing.T) {
	c := Build(shortVideo(true))
	want := []struct {
		kind, name string
		from, to   float64
	}{{KindHook, "hook", 0, 3.2}, {KindIntro, "intro", 3.2, 6.5}, {KindBody, "part-1", 6.5, 10.5}, {KindBody, "part-2", 10.5, 14.5}, {KindBody, "part-3", 14.5, 18.5},
		{KindCTA, "call-to-action", 18.5, 22}, {KindEnd, "end-card", 22, 26}}
	if len(c.Sections) != len(want) {
		for _, s := range c.Sections {
			t.Logf("%s %s %.1f–%.1f (%s): %s", s.Kind, s.Name, s.From, s.To, s.Found, s.Does)
		}
		t.Fatalf("%d sections, want %d", len(c.Sections), len(want))
	}
	for i, w := range want {
		s := c.Sections[i]
		if s.Kind != w.kind || s.Name != w.name || !near(s.From, w.from) || !near(s.To, w.to) {
			t.Errorf("section %d: %s %s %.1f–%.1f, want %s %s %.1f–%.1f", i, s.Kind, s.Name, s.From, s.To, w.kind, w.name, w.from, w.to)
		}
	}
	if s := c.Sections[0]; !s.Question || !strings.HasPrefix(s.Does, "a question to camera") {
		t.Errorf("the hook asks a question to camera: %+v", s)
	}
	if s := c.Sections[1]; s.Does != "says what the video will show" {
		t.Errorf("the intro says what comes: %q", s.Does)
	}
	if s := c.Sections[2]; !strings.HasPrefix(s.Does, "the first point") {
		t.Errorf("the body is a list: %q", s.Does)
	}
	if c.Shape != "vertical" || c.Length != 26 {
		t.Errorf("shape and length: %s %v", c.Shape, c.Length)
	}
	if p := c.Pacing; p.Shots != 7 || !near(p.ShotSeconds, 3.7) || p.Words != 37 || p.WordsPerMinute < 160 || p.WordsPerMinute > 200 {
		t.Errorf("pacing: %+v", p)
	}
	if len(c.Speakers) != 1 || c.Speakers[0].Share != 1 {
		t.Errorf("one voice: %+v", c.Speakers)
	}
	if cp := c.Captions; !cp.Burned || cp.Position != "lower" || !cp.Capitals || cp.Look != "bold" || cp.WordsAtATime < 2 || cp.WordsAtATime > 3 || cp.Covers < 0.9 {
		t.Errorf("captions: %+v", cp)
	}
	kinds := map[string]string{}
	for _, g := range c.Graphics {
		kinds[g.Text] = g.Kind
	}
	if kinds["STOP THE SCROLL"] != GraphicTitle || kinds["FOLLOW FOR MORE"] != GraphicEndCard || len(c.Graphics) != 2 {
		t.Errorf("the title and the end card's words, nothing else (captions aren't graphics): %+v", c.Graphics)
	}
	if s := c.Sound; s.Bed != BedSound || !near(s.Below, 13) || !s.Throughout {
		t.Errorf("music under the voice: %+v", s)
	}
	if e := c.End; e == nil || !near(e.From, 22) || len(e.Text) != 1 || !e.Sound {
		t.Errorf("the end card: %+v", e)
	}
	if !c.Ready || len(c.Missing) != 0 {
		t.Errorf("ready, nothing missing: %+v %+v", c.Waiting, c.Missing)
	}
	for _, l := range c.Lines {
		t.Log(l)
		if ps := plainwords.CheckShown(l); len(ps) > 0 {
			t.Errorf("not plain: %q: %v", l, ps)
		}
	}
}

// Without the on-screen index the card says what it can't know, and still
// finds the sections from the speech, the cuts and the pauses.
func TestCardWithoutLooking(t *testing.T) {
	in := shortVideo(false)
	in.CantHere = []string{"what's on screen (that needs MetaRush on a Mac)"}
	c := Build(in)
	if len(c.Sections) != 7 || c.Sections[0].Does != "a question" {
		t.Fatalf("sections without the index: %+v", c.Sections)
	}
	if c.Captions.Looked || c.Captions.Burned || !strings.Contains(c.Captions.Said, "hasn't looked") {
		t.Errorf("captions unknown: %+v", c.Captions)
	}
	if len(c.Graphics) != 0 || len(c.Missing) != 1 {
		t.Errorf("no graphics known; what's missing said: %+v %+v", c.Graphics, c.Missing)
	}
	if !strings.Contains(strings.Join(c.Lines, "\n"), "Not looked at here") {
		t.Errorf("the card says what it couldn't look at: %v", c.Lines)
	}
}

// A video nobody speaks in, a talking head without cuts, and an empty one.
func TestCardOddVideos(t *testing.T) {
	pictures := Input{Duration: 12, Width: 1920, Height: 1080, HasVideo: true, HasAudio: true, Heard: true, Measured: true, Cuts: []float64{4, 8},
		Loudness: &Loudness{Step: 0.1, Values: repeat(-28, 120)}}
	c := Build(pictures)
	if c.Shape != "landscape" || len(c.Sections) == 0 || c.Sections[0].Does != "pictures, nobody speaks" || c.Sound.Bed != BedSound || !c.Sound.Throughout {
		t.Errorf("pictures and music: %+v %+v", c.Sections, c.Sound)
	}
	var ws []Word
	for i := 0; i < 60; i++ {
		ws = append(ws, Word{Text: "word", From: 0.5 + float64(i)*0.33, To: 0.5 + float64(i)*0.33 + 0.25, Speaker: "speaker_0"})
	}
	ws[59].Text = "end."
	head := Input{Duration: 21, Width: 1080, Height: 1080, HasVideo: true, HasAudio: true, Heard: true, Measured: true, Words: ws}
	c = Build(head)
	if c.Shape != "square" || c.Pacing.Shots != 1 || c.Sound.Measured || len(c.Sections) < 2 {
		t.Errorf("a square talking head with no loudness: %+v %+v", c.Pacing, c.Sections)
	}
	if c := Build(Input{}); len(c.Sections) != 0 || c.Summary == "" {
		t.Errorf("an empty input: %+v", c)
	}
}

func repeat(v float64, n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = v
	}
	return out
}

// A list of six points names each part plainly: the sixth is "point 6".
func TestCardNamesEveryPointOfALongList(t *testing.T) {
	in := Input{Duration: 64, Width: 720, Height: 1280, HasVideo: true, HasAudio: true, Heard: true, Measured: true, Names: map[string]string{}}
	lines := []string{"Why do videos fail?", "Here are six things to fix.", "First, open strong.", "Second, cut pauses.", "Third, add captions.",
		"Next, show faces.", "Next, use music.", "Finally, end clearly.", "Follow for more."}
	at := 0.5
	for i, l := range lines {
		ws := strings.Fields(l)
		for k, w := range ws {
			in.Words = append(in.Words, Word{Text: w, From: at + float64(k)*0.4, To: at + float64(k)*0.4 + 0.35, Speaker: "speaker_0"})
		}
		at += float64(len(ws))*0.4 + 1.6
		if i >= 1 {
			in.Cuts = append(in.Cuts, at-0.8)
		}
	}
	c := Build(in)
	var parts []WhatSection
	for _, s := range c.Sections {
		if s.Kind == KindBody {
			parts = append(parts, s)
		}
	}
	if len(parts) != 6 {
		t.Fatalf("six points, six parts: %+v", c.Sections)
	}
	if !strings.HasPrefix(parts[0].Does, "the first point: ") || !strings.HasPrefix(parts[5].Does, "point 6: ") || strings.Contains(parts[5].Does, "point,") {
		t.Errorf("the points' words: %q … %q", parts[0].Does, parts[5].Does)
	}
	for _, s := range c.Sections {
		if f := plainwords.CheckShown(s.Does); len(f) > 0 {
			t.Errorf("not plain: %q %+v", s.Does, f)
		}
	}
}

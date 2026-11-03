// Package breakdown says what a video does (D399): its length and shape, its
// sections with times (a hook, an intro, the body's parts, a call to action, an
// end card), its pacing, who speaks and how much, its captions, the titles and
// other words on screen, what plays under the voices and how it ends — read from
// the evidence MetaRush already keeps (the timed transcript and its voices, the
// technical evidence's cuts, pauses and loudness, the on-screen index where one
// runs), deterministically and on this computer: no AI service is asked. The
// card speaks plain words and keeps the numbers beside them for Details.
//
// It is how "Start from a video you like" learns the shape of someone else's
// video; it works the same on any clip.
package breakdown

import (
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// Input is what a card is made from (Load gathers it from the database).
type Input struct {
	// Duration is the video's length; Width and Height its picture as it plays.
	Duration           float64
	Width, Height      int
	HasVideo, HasAudio bool
	// Words are every word heard, in time order, with the voice that says it.
	Words []Word
	// Names are the voices' names a person gave or MetaRush recognized, by label.
	Names map[string]string
	// Heard: the transcript is there (a video nobody speaks in has one too).
	Heard bool
	// Measured: the technical evidence is there (cuts, pauses, loudness).
	Measured bool
	Cuts     []float64
	Silences []Span
	Black    []Span
	Loudness *Loudness
	// Looked: the on-screen index is there (Apple Vision, on a Mac).
	Looked bool
	Text   []Text
	Shots  []Shot
	// Waiting are the looks still running; CantHere the ones no computer of
	// the workspace can make (in plain words).
	Waiting  []string
	CantHere []string
}

// Word is one word heard.
type Word struct {
	Text     string
	From, To float64
	Speaker  string
}

// Span is a stretch of the video (seconds).
type Span struct{ From, To float64 }

// Text is words on screen: what they say, when, and where (x, y, width and
// height from the top-left, 0 to 1).
type Text struct {
	Text     string
	From, To float64
	Box      [4]float64
	Changing bool
}

// Shot is the on-screen index's shot: its type and the most faces and people seen.
type Shot struct {
	Type          string
	From, To      float64
	Faces, People int
}

// Loudness is how loud the video is every Step seconds (each value the 400 ms
// ending there, in dB as loudness meters read it).
type Loudness struct {
	Step   float64
	Values []float64
}

// Kinds of section (WhatSection.Kind).
const (
	KindHook  = "hook"
	KindIntro = "intro"
	KindBody  = "body"
	KindCTA   = "call-to-action"
	KindEnd   = "end-card"
)

// WhatItDoes is the card: what a video does, in plain words, with the
// numbers for Details.
type WhatItDoes struct {
	// Ready: every look it reads is done (else Waiting says which run).
	Ready   bool     `json:"ready"`
	Waiting []string `json:"waiting"`
	// Missing are the looks no computer of the workspace can make, so the card
	// says nothing about them (what's on screen needs a Mac).
	Missing []string `json:"missing"`
	// Summary is the card in two sentences; Lines the card in plain words, one
	// thing a line (what the terminal prints).
	Summary string   `json:"summary"`
	Lines   []string `json:"lines"`
	// Length is how long it is (seconds); Shape vertical, landscape or square
	// and ShapeWords the shape as people say it; Width and Height its picture.
	Length     float64 `json:"length"`
	Shape      string  `json:"shape,omitempty" jsonschema:"enum=,enum=vertical,enum=landscape,enum=square"`
	ShapeWords string  `json:"shapeWords,omitempty"`
	Width      int     `json:"width,omitempty"`
	Height     int     `json:"height,omitempty"`
	// Sections are its parts in order, with their times.
	Sections []WhatSection `json:"sections"`
	Pacing   WhatPacing    `json:"pacing"`
	Speakers []WhatSpeaker `json:"speakers"`
	Captions WhatCaptions  `json:"captions"`
	// Graphics are the titles and other words seen on screen (not captions).
	Graphics []WhatGraphic `json:"graphics"`
	Sound    WhatSound     `json:"sound"`
	// End is how it ends: an end card, when it has one.
	End *WhatEnd `json:"end,omitempty"`
}

// WhatSection is one part of the video.
type WhatSection struct {
	Kind string `json:"kind" jsonschema:"enum=hook,enum=intro,enum=body,enum=call-to-action,enum=end-card"`
	// Name is a short name (hook, intro, part-1…, call-to-action, end-card);
	// Title names it for people ("The hook", "Part 2 of 3").
	Name   string  `json:"name"`
	Title  string  `json:"title"`
	From   float64 `json:"from"`
	To     float64 `json:"to"`
	Length float64 `json:"length"`
	// Does says what happens in it, in MetaRush's own words (never the
	// video's): "a question to camera, with words on screen".
	Does string `json:"does"`
	// Found says how MetaRush found where it ends.
	Found string `json:"found"`
	// Shots, Words and Speech (the share of it someone speaks, 0 to 1); Question:
	// it asks one; Text: words on screen in it (captions aside); Faces the most
	// faces seen at once; Shot the shot type seen most (close-up, medium, wide).
	Shots    int     `json:"shots"`
	Words    int     `json:"words"`
	Speech   float64 `json:"speech"`
	Question bool    `json:"question,omitempty"`
	Text     bool    `json:"text,omitempty"`
	Faces    int     `json:"faces,omitempty"`
	Shot     string  `json:"shot,omitempty"`
}

// WhatPacing is how fast it moves.
type WhatPacing struct {
	// Shots counts its shots; ShotSeconds is a shot's average length, MedianShot
	// the middle one, Shortest and Longest the extremes (seconds).
	Shots       int     `json:"shots"`
	ShotSeconds float64 `json:"shotSeconds"`
	MedianShot  float64 `json:"medianShot"`
	Shortest    float64 `json:"shortest"`
	Longest     float64 `json:"longest"`
	// Words heard, WordsPerMinute while someone speaks, Talking the share of the
	// video someone speaks (0 to 1), Pauses of 0.6 s or more and the Longest.
	Words          int     `json:"words"`
	WordsPerMinute float64 `json:"wordsPerMinute"`
	Talking        float64 `json:"talking"`
	Pauses         int     `json:"pauses"`
	LongestPause   float64 `json:"longestPause"`
	Said           string  `json:"said"`
}

// WhatSpeaker is one voice and how much it speaks.
type WhatSpeaker struct {
	Label   string  `json:"label"`
	Name    string  `json:"name,omitempty"`
	Seconds float64 `json:"seconds"`
	Share   float64 `json:"share" jsonschema:"description=Its share of the speech\\, 0 to 1."`
}

// WhatCaptions says whether captions are burned into the picture and how they look.
type WhatCaptions struct {
	// Looked: the on-screen index was there to say.
	Looked bool `json:"looked"`
	Burned bool `json:"burned"`
	// Covers is the share of the speech they cover (0 to 1); WordsAtATime how
	// many words show at once; Position bottom, lower, middle or top; Capitals
	// in capital letters; Size small, medium or large; Look the closest of
	// MetaRush's caption looks.
	Covers       float64 `json:"covers"`
	WordsAtATime float64 `json:"wordsAtATime"`
	Position     string  `json:"position,omitempty" jsonschema:"enum=,enum=bottom,enum=lower,enum=middle,enum=top"`
	Capitals     bool    `json:"capitals,omitempty"`
	Size         string  `json:"size,omitempty" jsonschema:"enum=,enum=small,enum=medium,enum=large"`
	Look         string  `json:"look,omitempty"`
	Said         string  `json:"said"`
}

// Kinds of words on screen (WhatGraphic.Kind).
const (
	GraphicTitle      = "title"       // large words near the start (or opening a part)
	GraphicLowerThird = "lower-third" // a name or a label low on one side while someone speaks
	GraphicEndCard    = "end-card"    // words on the end card
	GraphicLabel      = "label"       // a name or handle that stays on screen
	GraphicWords      = "words"       // other words on screen
)

// WhatGraphic is words seen on screen that aren't captions.
type WhatGraphic struct {
	Kind string  `json:"kind" jsonschema:"enum=title,enum=lower-third,enum=end-card,enum=label,enum=words"`
	From float64 `json:"from"`
	To   float64 `json:"to"`
	// Where is top, middle or bottom (and left or right when it is to one side).
	Where string `json:"where"`
	// Text is what it says: the video's own words, shown as theirs.
	Text string `json:"text"`
	Size string `json:"size" jsonschema:"enum=small,enum=medium,enum=large"`
}

// What plays under the voices (WhatSound.Bed).
const (
	BedNone  = "none"  // the pauses are quiet
	BedRoom  = "room"  // only quiet room sound
	BedSound = "sound" // something keeps playing: likely music
)

// WhatSound is what plays under the voices, from the loudness MetaRush measured.
type WhatSound struct {
	Measured bool `json:"measured"`
	// Bed is what plays in the pauses (none, room, sound); Speech how loud the
	// voices are and Under how loud the pauses are (dB, as a loudness meter
	// reads them); Below how much quieter the pauses are than the voices.
	Bed    string   `json:"bed,omitempty" jsonschema:"enum=,enum=none,enum=room,enum=sound"`
	Speech *float64 `json:"speech,omitempty"`
	Under  *float64 `json:"under,omitempty"`
	Below  float64  `json:"below,omitempty"`
	// Throughout: sound plays all the way through where nobody speaks too.
	Throughout bool   `json:"throughout,omitempty"`
	Said       string `json:"said"`
}

// WhatEnd is how the video ends.
type WhatEnd struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
	// Text are the words on it (the video's own); Sound: something still plays.
	Text  []string `json:"text,omitempty"`
	Sound bool     `json:"sound,omitempty"`
	Said  string   `json:"said"`
}

// Thresholds. Changing one changes cards: they are read from the evidence
// every time, so nothing stored goes stale.
const (
	pauseMin     = 0.6 // a gap in the speech this long is a pause
	speechJoin   = 0.6 // words closer than this are one stretch of speech
	sentenceGap  = 0.8 // a gap this long ends a sentence even without a full stop
	endCardMin   = 1.5 // an end card lasts at least this long after the last word
	quietDB      = -55 // pauses at or under this are quiet
	roomDB       = -42 // … and up to this, room sound
	floorDB      = -69 // a loudness value at or under this is silence
	captionMatch = 0.6 // share of the words on screen that are being said: a caption
)

// Build makes the card.
func Build(in Input) *WhatItDoes {
	c := &WhatItDoes{Waiting: nonNil(in.Waiting), Missing: nonNil(in.CantHere), Sections: []WhatSection{}, Speakers: []WhatSpeaker{}, Graphics: []WhatGraphic{}, Lines: []string{}}
	c.Ready = len(in.Waiting) == 0
	c.Length = round1(in.Duration)
	c.Width, c.Height = in.Width, in.Height
	c.Shape, c.ShapeWords = shapeOf(in.Width, in.Height)
	b := newBuilder(in)
	c.Pacing = b.pacing()
	c.Speakers = b.speakers()
	c.Captions = b.captions()
	c.Sections = b.sections()
	c.Graphics = b.graphics()
	c.Sound = b.sound()
	c.End = b.end(c.Graphics, c.Sound)
	c.Summary = summary(c, in)
	c.Lines = lines(c, in)
	return c
}

// builder holds what every part of the card reads.
type builder struct {
	in        Input
	dur       float64
	words     []Word
	speech    []Span // stretches of speech
	pauses    []Span // gaps between them of pauseMin or more
	sentences []sentence
	cuts      []float64
	capTexts  []Text // on-screen text that is captions
	others    []Text // the rest
	// Sections' edges once found (for graphics and the end).
	hookEnd, endStart float64
	hasEnd            bool
	parts             []WhatSection
}

type sentence struct {
	from, to float64
	words    []Word
	question bool
}

func newBuilder(in Input) *builder {
	b := &builder{in: in, dur: math.Max(in.Duration, 0)}
	for _, w := range in.Words {
		if strings.TrimSpace(w.Text) == "" || w.To < w.From {
			continue
		}
		b.words = append(b.words, w)
	}
	sort.SliceStable(b.words, func(i, j int) bool { return b.words[i].From < b.words[j].From })
	if b.dur <= 0 && len(b.words) > 0 {
		b.dur = b.words[len(b.words)-1].To
	}
	for _, w := range b.words {
		if n := len(b.speech); n > 0 && w.From-b.speech[n-1].To < speechJoin {
			b.speech[n-1].To = math.Max(b.speech[n-1].To, w.To)
			continue
		}
		b.speech = append(b.speech, Span{w.From, w.To})
	}
	for i := 1; i < len(b.speech); i++ {
		if g := b.speech[i].From - b.speech[i-1].To; g >= pauseMin {
			b.pauses = append(b.pauses, Span{b.speech[i-1].To, b.speech[i].From})
		}
	}
	b.sentences = splitSentences(b.words)
	for _, t := range in.Cuts {
		if t > 0.2 && t < b.dur-0.2 {
			b.cuts = append(b.cuts, t)
		}
	}
	sort.Float64s(b.cuts)
	b.cuts = dedupe(b.cuts, 0.2)
	for _, t := range in.Text {
		if b.isCaption(t) {
			b.capTexts = append(b.capTexts, t)
		} else {
			b.others = append(b.others, t)
		}
	}
	return b
}

// splitSentences groups words into sentences: a full stop, question or
// exclamation mark ends one, and so does a long gap.
func splitSentences(ws []Word) []sentence {
	var out []sentence
	var cur *sentence
	for i, w := range ws {
		if cur == nil {
			out = append(out, sentence{from: w.From})
			cur = &out[len(out)-1]
		}
		cur.words = append(cur.words, w)
		cur.to = w.To
		end := strings.TrimRight(strings.TrimSpace(w.Text), "\"'”’)]»")
		last := i == len(ws)-1
		gap := !last && ws[i+1].From-w.To >= sentenceGap
		switch {
		case strings.HasSuffix(end, "?"):
			cur.question = true
			cur = nil
		case strings.HasSuffix(end, ".") || strings.HasSuffix(end, "!") || strings.HasSuffix(end, "…") || gap:
			cur = nil
		}
	}
	return out
}

// ---- pacing and voices ----

func (b *builder) speechSeconds(from, to float64) float64 {
	t := 0.0
	for _, s := range b.speech {
		t += overlap(s.From, s.To, from, to)
	}
	return t
}

func (b *builder) pacing() WhatPacing {
	p := WhatPacing{Words: len(b.words)}
	edges := append([]float64{0}, b.cuts...)
	edges = append(edges, b.dur)
	var lens []float64
	for i := 1; i < len(edges); i++ {
		if l := edges[i] - edges[i-1]; l > 0.05 {
			lens = append(lens, l)
		}
	}
	if b.in.Measured || len(b.cuts) > 0 {
		p.Shots = len(lens)
		if p.Shots > 0 {
			p.ShotSeconds = round1(b.dur / float64(p.Shots))
			p.MedianShot = round1(median(lens))
			p.Shortest, p.Longest = round1(minOf(lens)), round1(maxOf(lens))
		}
	}
	talk := b.speechSeconds(0, b.dur)
	if b.dur > 0 {
		p.Talking = round2(talk / b.dur)
	}
	if talk > 1 && len(b.words) > 0 {
		p.WordsPerMinute = math.Round(float64(len(b.words)) / (talk / 60))
	}
	p.Pauses = len(b.pauses)
	for _, g := range b.pauses {
		p.LongestPause = math.Max(p.LongestPause, round1(g.To-g.From))
	}
	var parts []string
	switch {
	case !b.in.Measured && len(b.cuts) == 0:
		parts = append(parts, "its cuts aren't measured yet")
	case p.Shots <= 1:
		parts = append(parts, "one shot all the way through")
	default:
		parts = append(parts, fmt.Sprintf("a new shot about every %s (%d shots, %s)", secs(p.ShotSeconds), p.Shots, cutPace(p.ShotSeconds)))
	}
	switch {
	case !b.in.Heard:
	case len(b.words) == 0:
		parts = append(parts, "nobody speaks")
	default:
		parts = append(parts, fmt.Sprintf("about %.0f words a minute while someone speaks (%s)", p.WordsPerMinute, talkPace(p.WordsPerMinute)))
		parts = append(parts, fmt.Sprintf("someone speaks %s of the time", percent(p.Talking)))
		if p.Pauses > 0 {
			parts = append(parts, fmt.Sprintf("%s, the longest %s", plural(p.Pauses, "pause"), secs(p.LongestPause)))
		} else {
			parts = append(parts, "no pauses")
		}
	}
	p.Said = upperFirst(joinParts(parts)) + "."
	return p
}

func cutPace(avg float64) string {
	switch {
	case avg < 2:
		return "very quick"
	case avg < 4:
		return "quick"
	case avg < 8:
		return "steady"
	}
	return "long takes"
}

func talkPace(wpm float64) string {
	switch {
	case wpm < 120:
		return "unhurried"
	case wpm < 165:
		return "a conversational pace"
	case wpm < 200:
		return "brisk"
	}
	return "very fast"
}

func (b *builder) speakers() []WhatSpeaker {
	secsBy := map[string]float64{}
	var order []string
	for i, w := range b.words {
		label := w.Speaker
		if label == "" {
			label = "voice"
		}
		if _, ok := secsBy[label]; !ok {
			order = append(order, label)
		}
		d := w.To - w.From
		if i+1 < len(b.words) && b.words[i+1].Speaker == w.Speaker {
			if g := b.words[i+1].From - w.To; g > 0 && g < 0.3 {
				d += g
			}
		}
		secsBy[label] += d
	}
	total := 0.0
	for _, s := range secsBy {
		total += s
	}
	out := []WhatSpeaker{}
	for _, l := range order {
		sp := WhatSpeaker{Label: l, Name: b.in.Names[l], Seconds: round1(secsBy[l])}
		if total > 0 {
			sp.Share = round2(secsBy[l] / total)
		}
		out = append(out, sp)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Seconds > out[j].Seconds })
	return out
}

// ---- captions ----

var tokenRe = regexp.MustCompile(`[\p{L}\p{N}']+`)

func tokens(s string) []string {
	var out []string
	for _, t := range tokenRe.FindAllString(strings.ToLower(s), -1) {
		t = strings.Trim(t, "'")
		if t != "" {
			out = append(out, t)
		}
	}
	return out
}

// isCaption reports words on screen that are what is being said then.
func (b *builder) isCaption(t Text) bool {
	toks := tokens(t.Text)
	if len(toks) == 0 || len(b.words) == 0 {
		return false
	}
	said := map[string]bool{}
	for _, w := range b.words {
		if w.From < t.To+0.5 && w.To > t.From-0.5 {
			for _, x := range tokens(w.Text) {
				said[x] = true
			}
		}
	}
	n := 0
	for _, x := range toks {
		if said[x] {
			n++
		}
	}
	return float64(n)/float64(len(toks)) >= captionMatch
}

func (b *builder) captions() WhatCaptions {
	c := WhatCaptions{Looked: b.in.Looked}
	switch {
	case !b.in.HasVideo:
		c.Said = "It has no picture, so no captions."
		return c
	case !b.in.Looked:
		c.Said = "MetaRush hasn't looked at what's on screen, so it can't say whether captions are burned in."
		return c
	case len(b.words) == 0:
		c.Said = "Nobody speaks, so there's nothing to caption."
		return c
	}
	talk := b.speechSeconds(0, b.dur)
	var covered []Span
	var counts, ys, hs []float64
	letters, upper := 0, 0
	for _, t := range b.capTexts {
		covered = append(covered, Span{t.From, t.To})
		counts = append(counts, float64(len(tokens(t.Text))))
		ys = append(ys, t.Box[1]+t.Box[3]/2)
		hs = append(hs, t.Box[3])
		for _, r := range t.Text {
			if unicode.IsLetter(r) {
				letters++
				if unicode.IsUpper(r) {
					upper++
				}
			}
		}
	}
	cov := 0.0
	for _, s := range mergeSpans(covered, 0.3) {
		cov += b.speechSeconds(s.From, s.To)
	}
	if talk > 0 {
		c.Covers = round2(math.Min(1, cov/talk))
	}
	c.Burned = len(b.capTexts) >= 2 && c.Covers >= 0.3
	if !c.Burned {
		c.Said = "No captions are burned into the picture."
		return c
	}
	c.WordsAtATime = round1(median(counts))
	y := median(ys)
	switch {
	case y >= 0.8:
		c.Position = "bottom"
	case y >= 0.6:
		c.Position = "lower"
	case y >= 0.38:
		c.Position = "middle"
	default:
		c.Position = "top"
	}
	c.Capitals = letters > 0 && float64(upper)/float64(letters) >= 0.8
	switch h := median(hs); {
	case h >= 0.055:
		c.Size = "large"
	case h <= 0.03:
		c.Size = "small"
	default:
		c.Size = "medium"
	}
	switch {
	case c.Capitals && c.WordsAtATime <= 4:
		c.Look = "bold"
	case c.WordsAtATime <= 2:
		c.Look = "highlight"
	case c.WordsAtATime >= 8:
		c.Look = "subtitle"
	default:
		c.Look = "clean"
	}
	style := fmt.Sprintf("%s, about %s at a time", positionWords(c.Position), plural(int(math.Round(c.WordsAtATime)), "word"))
	if c.Capitals {
		style += ", in capitals"
	}
	if c.Size != "medium" {
		style += ", " + c.Size
	}
	c.Said = fmt.Sprintf("Captions are burned into the picture over %s of the speech: %s.", percent(c.Covers), style)
	return c
}

func positionWords(p string) string {
	switch p {
	case "bottom":
		return "at the bottom"
	case "lower":
		return "low in the picture"
	case "middle":
		return "in the middle"
	}
	return "at the top"
}

// ---- sections ----

// mark is a place a section could end, and how sure that is.
type mark struct {
	at       float64
	strength float64
	why      string
}

// cutNear is a cut within [from, to] closest to at.
func (b *builder) cutNear(at, from, to float64) (float64, bool) {
	best, found := 0.0, false
	for _, c := range b.cuts {
		if c >= from && c <= to && (!found || math.Abs(c-at) < math.Abs(best-at)) {
			best, found = c, true
		}
	}
	return best, found
}

// edges are the places sections may end in (from, to): pauses (in the middle,
// or at a cut inside), cuts (stronger where a sentence ends), sentence ends,
// and new words on screen.
func (b *builder) edges(from, to float64) []mark {
	var out []mark
	for _, p := range b.pauses {
		mid := (p.From + p.To) / 2
		why, at := "a pause", mid
		if c, ok := b.cutNear(mid, p.From, p.To); ok {
			at, why = c, "a pause and a cut"
		}
		if at > from && at < to {
			out = append(out, mark{at, 1 + (p.To - p.From), why})
		}
	}
	for _, c := range b.cuts {
		if c <= from || c >= to {
			continue
		}
		s, why := 0.4, "a cut"
		for _, se := range b.sentences {
			if math.Abs(se.to-c) <= 0.6 || math.Abs(se.from-c) <= 0.6 {
				s, why = 1.0, "a cut between sentences"
				break
			}
		}
		out = append(out, mark{c, s, why})
	}
	for i, se := range b.sentences {
		if i == 0 {
			continue
		}
		at := (b.sentences[i-1].to + se.from) / 2
		if at > from && at < to {
			s := 0.3
			if listWord(se) {
				s = 1.5
			}
			out = append(out, mark{at, s, "a new sentence"})
		}
	}
	for _, t := range b.others {
		if t.Box[3] >= 0.04 && t.From > from+0.3 && t.From < to-0.3 {
			out = append(out, mark{t.From, 1.2, "words on screen"})
		}
	}
	return out
}

var listWords = map[string]bool{"first": true, "firstly": true, "second": true, "secondly": true, "third": true, "thirdly": true, "next": true, "finally": true,
	"lastly": true, "another": true, "step": true, "tip": true, "number": true, "one": false}

// listWord reports a sentence that starts a point of a list ("Second, …").
func listWord(s sentence) bool {
	if len(s.words) == 0 {
		return false
	}
	t := tokens(s.words[0].Text)
	return len(t) > 0 && listWords[t[0]]
}

var (
	ctaRe   = regexp.MustCompile(`(?i)\b(subscribe|follow|like and|hit the|smash|comment|link in|link below|in the description|check out|sign up|download|visit|click|share this|join|try it|get started|learn more|shop|buy|order now|book a|register|tap|head to|go to|don't forget|let me know|save this|turn on)\b`)
	introRe = regexp.MustCompile(`(?i)\b(today|in this video|going to|gonna|let me|i'll|we'll|here's|here is|here are|show you|ways|tips|steps|things|reasons|three|four|five)\b`)
)

func (s sentence) text() string {
	var parts []string
	for _, w := range s.words {
		parts = append(parts, w.Text)
	}
	return strings.Join(parts, " ")
}

func (b *builder) sections() []WhatSection {
	d := b.dur
	if d <= 0 {
		return []WhatSection{}
	}
	lastWord := 0.0
	if n := len(b.words); n > 0 {
		lastWord = b.words[n-1].To
	}
	// The end card: the tail after the last word, when it is long enough and
	// the picture says so (a cut, words on screen, black) or it is long.
	b.endStart = d
	tailFrom := lastWord
	if len(b.words) == 0 && len(b.cuts) > 0 {
		tailFrom = b.cuts[len(b.cuts)-1] - 0.01
	}
	if tail := d - tailFrom; (len(b.words) > 0 || tailFrom > d*0.7) && tail >= endCardMin {
		start := tailFrom + 0.2
		cut, hasCut := b.cutNear(tailFrom, tailFrom-0.3, tailFrom+tail*0.6)
		if hasCut {
			start = cut
		}
		text := false
		for _, t := range b.others {
			if t.To > start+0.3 && t.From < d {
				text = true
			}
		}
		black := false
		for _, s := range b.in.Black {
			if s.To > start && s.From < d {
				black = true
			}
		}
		if (hasCut || text || black || tail >= 3) && d-start >= 1 {
			b.endStart, b.hasEnd = round1(start), true
		}
	}
	bodyTo := b.endStart
	// The hook: the first beat, to the first sentence's end (or the first
	// pause or cut) within a fifth of the video (3–8 s).
	limit := math.Min(math.Max(0.2*d, 3), 8)
	if d < 6 {
		limit = d / 2
	}
	limit = math.Min(limit, bodyTo-0.5)
	b.hookEnd = math.Min(3, limit)
	hookWhy := "the first three seconds"
	if len(b.sentences) > 0 && b.sentences[0].from < 1.5 && b.sentences[0].to >= 1.2 && b.sentences[0].to <= limit {
		end := b.sentences[0].to + 0.1
		if c, ok := b.cutNear(b.sentences[0].to, b.sentences[0].to-0.3, b.sentences[0].to+0.6); ok {
			end = c
		} else if len(b.sentences) > 1 {
			end = math.Min((b.sentences[0].to+b.sentences[1].from)/2, b.sentences[0].to+0.6)
		}
		b.hookEnd, hookWhy = end, "the end of the first sentence"
	} else if ms := b.edges(1.2, limit+0.01); len(ms) > 0 {
		sort.SliceStable(ms, func(i, j int) bool { return ms[i].at < ms[j].at })
		for _, m := range ms {
			if m.strength >= 0.4 && m.why != "a new sentence" {
				b.hookEnd, hookWhy = m.at, "the first "+strings.TrimPrefix(m.why, "a ")
				break
			}
		}
	}
	b.hookEnd = round1(math.Max(0.5, math.Min(b.hookEnd, bodyTo)))
	var out []WhatSection
	out = append(out, b.section(KindHook, "hook", "The hook", 0, b.hookEnd, hookWhy))
	from := b.hookEnd
	// The call to action: the last stretch of speech asking people to do
	// something, in the last 40% of the video.
	ctaFrom := bodyTo
	for i, se := range b.sentences {
		if se.from < math.Max(b.hookEnd+1, 0.6*d) || se.from >= bodyTo || !ctaRe.MatchString(se.text()) {
			continue
		}
		at := se.from - 0.1
		if i > 0 {
			at = (b.sentences[i-1].to + se.from) / 2
		}
		if c, ok := b.cutNear(se.from, se.from-0.8, se.from+0.2); ok {
			at = c
		}
		ctaFrom = round1(math.Max(at, from+1))
		break
	}
	// The intro: the next stretch after the hook, to the strongest edge in
	// the next few seconds, when the video is long enough to have one.
	if d >= 15 && ctaFrom-from >= 8 {
		lo, hi := from+2, from+math.Min(10, 0.3*(ctaFrom-from))
		best := mark{}
		for _, m := range b.edges(lo, hi) {
			if m.strength > best.strength || (m.strength == best.strength && m.at < best.at) {
				best = m
			}
		}
		if best.at > 0 && ctaFrom-best.at >= 4 {
			out = append(out, b.section(KindIntro, "intro", "The intro", from, round1(best.at), "the first "+strings.TrimPrefix(best.why, "a ")+" after the hook"))
			from = round1(best.at)
		}
	}
	// The body, in parts at its strongest edges.
	if l := ctaFrom - from; l >= 1 {
		n := int(math.Round(l / 8))
		if d > 120 {
			n = int(math.Round(l / 40))
		}
		// A list ("First, … Second, …") has a part per point.
		if k := listCount(b.sentences, from, ctaFrom); k >= 2 {
			n = max(n, k)
		}
		n = max(1, min(n, 6))
		minLen := math.Max(2.5, l/float64(2*n))
		ms := b.edges(from+minLen, ctaFrom-minLen)
		sort.SliceStable(ms, func(i, j int) bool {
			if ms[i].strength != ms[j].strength {
				return ms[i].strength > ms[j].strength
			}
			return ms[i].at < ms[j].at
		})
		var cuts []mark
		for _, m := range ms {
			if len(cuts) >= n-1 {
				break
			}
			ok := true
			for _, c := range cuts {
				if math.Abs(c.at-m.at) < minLen {
					ok = false
				}
			}
			if ok {
				cuts = append(cuts, m)
			}
		}
		sort.SliceStable(cuts, func(i, j int) bool { return cuts[i].at < cuts[j].at })
		edges := append([]mark{{at: from}}, cuts...)
		edges = append(edges, mark{at: ctaFrom})
		count := len(edges) - 1
		for i := 0; i < count; i++ {
			name, title := "body", "The body"
			if count > 1 {
				name, title = fmt.Sprintf("part-%d", i+1), fmt.Sprintf("Part %d of %d", i+1, count)
			}
			why := "where the call to action begins"
			switch {
			case i < count-1:
				why = edges[i+1].why
			case !b.hasEnd && ctaFrom >= d:
				why = "the end of the video"
			case ctaFrom >= bodyTo:
				why = "where the end card begins"
			}
			s := b.section(KindBody, name, title, round1(edges[i].at), round1(edges[i+1].at), why)
			if count > 1 && listWordIn(b.sentences, s.From, s.To) {
				s.Does = ordinal(i+1) + " point: " + s.Does
			}
			out = append(out, s)
		}
	}
	if ctaFrom < bodyTo {
		out = append(out, b.section(KindCTA, "call-to-action", "The call to action", ctaFrom, bodyTo, "where it asks people to do something"))
	}
	if b.hasEnd {
		out = append(out, b.section(KindEnd, "end-card", "The end card", b.endStart, round1(d), "after the last words"))
	}
	b.parts = out
	return out
}

func listCount(ss []sentence, from, to float64) int {
	n := 0
	for _, s := range ss {
		if s.from >= from-0.3 && s.from < to && listWord(s) {
			n++
		}
	}
	return n
}

func listWordIn(ss []sentence, from, to float64) bool {
	for _, s := range ss {
		if s.from >= from-0.3 && s.from < to && listWord(s) {
			return true
		}
	}
	return false
}

func ordinal(n int) string {
	switch n {
	case 1:
		return "the first"
	case 2:
		return "the second"
	case 3:
		return "the third"
	case 4:
		return "the fourth"
	case 5:
		return "the fifth"
	}
	return fmt.Sprintf("point %d,", n)
}

// section measures one section and says what happens in it.
func (b *builder) section(kind, name, title string, from, to float64, found string) WhatSection {
	s := WhatSection{Kind: kind, Name: name, Title: title, From: round1(from), To: round1(to), Length: round1(to - from), Found: found}
	if to <= from {
		return s
	}
	s.Shots = 1
	for _, c := range b.cuts {
		if c > from+0.15 && c < to-0.15 {
			s.Shots++
		}
	}
	voices := map[string]bool{}
	for _, w := range b.words {
		if mid := (w.From + w.To) / 2; mid >= from && mid < to {
			s.Words++
			voices[w.Speaker] = true
		}
	}
	for _, se := range b.sentences {
		if mid := (se.from + se.to) / 2; mid >= from && mid < to && se.question {
			s.Question = true
		}
	}
	s.Speech = round2(b.speechSeconds(from, to) / (to - from))
	for _, t := range b.others {
		if overlap(t.From, t.To, from, to) > 0.3 {
			s.Text = true
		}
	}
	shotSecs := map[string]float64{}
	for _, sh := range b.in.Shots {
		if o := overlap(sh.From, sh.To, from, to); o > 0 {
			shotSecs[sh.Type] += o
			s.Faces = max(s.Faces, sh.Faces)
		}
	}
	best := 0.0
	for _, t := range []string{"close-up", "medium", "wide", "none"} {
		if shotSecs[t] > best {
			best, s.Shot = shotSecs[t], t
		}
	}
	s.Does = b.does(s, len(voices))
	return s
}

// does says what happens in a section, in MetaRush's own words.
func (b *builder) does(s WhatSection, voices int) string {
	var what string
	toCamera := b.in.Looked && s.Faces >= 1 && (s.Shot == "close-up" || s.Shot == "medium")
	switch {
	case s.Kind == KindEnd:
		what = "the video ends on a closing shot"
		if s.Text {
			what = "words on screen close the video"
		}
	case s.Speech < 0.25:
		what = "pictures, nobody speaks"
		if s.Text {
			what = "words on screen over pictures"
		}
	case s.Kind == KindHook && s.Question && toCamera:
		what = "a question to camera"
	case s.Kind == KindHook && s.Question:
		what = "a question"
	case s.Kind == KindCTA:
		what = "asks people to do something next (follow, subscribe, visit)"
	case voices >= 2:
		what = fmt.Sprintf("%d people talk", voices)
		if voices == 2 {
			what = "two people talk"
		}
	case toCamera:
		what = "one person speaks to camera"
	case b.in.Looked && s.Faces == 0:
		what = "a voice over pictures"
	default:
		what = "one voice speaks"
	}
	if s.Kind == KindIntro && s.Speech >= 0.25 {
		for _, se := range b.sentences {
			if mid := (se.from + se.to) / 2; mid >= s.From && mid < s.To && introRe.MatchString(se.text()) {
				what = "says what the video will show"
				break
			}
		}
	}
	if s.Kind == KindHook && !s.Question && s.Speech >= 0.25 && !toCamera {
		what = "an opening line"
	} else if s.Kind == KindHook && !s.Question && toCamera {
		what = "an opening line to camera"
	}
	var with []string
	if s.Text && s.Kind != KindEnd && s.Speech >= 0.25 {
		with = append(with, "with words on screen")
	}
	if s.Shots >= 3 && s.Length > 0 {
		with = append(with, fmt.Sprintf("a new shot about every %s", secs(round1(s.Length/float64(s.Shots)))))
	}
	if len(with) > 0 {
		what += ", " + strings.Join(with, ", ")
	}
	return what
}

// ---- graphics and the end ----

func (b *builder) graphics() []WhatGraphic {
	type group struct {
		text     string
		from, to float64
		boxes    [][4]float64
	}
	var gs []*group
	byText := map[string][]*group{}
	ts := append([]Text(nil), b.others...)
	sort.SliceStable(ts, func(i, j int) bool { return ts[i].From < ts[j].From })
	for _, t := range ts {
		key := strings.Join(tokens(t.Text), " ")
		if key == "" {
			continue
		}
		var g *group
		for _, x := range byText[key] {
			if t.From-x.to < 1.5 {
				g = x
			}
		}
		if g == nil {
			g = &group{text: strings.Join(strings.Fields(t.Text), " "), from: t.From, to: t.To}
			gs = append(gs, g)
			byText[key] = append(byText[key], g)
		}
		g.to = math.Max(g.to, t.To)
		g.boxes = append(g.boxes, t.Box)
	}
	out := []WhatGraphic{}
	for _, g := range gs {
		var xs, ys, hs []float64
		for _, bx := range g.boxes {
			xs, ys, hs = append(xs, bx[0]+bx[2]/2), append(ys, bx[1]+bx[3]/2), append(hs, bx[3])
		}
		x, y, h := median(xs), median(ys), median(hs)
		gr := WhatGraphic{From: round1(g.from), To: round1(g.to), Text: shorten(g.text, 80), Where: where(x, y)}
		switch {
		case h >= 0.055:
			gr.Size = "large"
		case h <= 0.03:
			gr.Size = "small"
		default:
			gr.Size = "medium"
		}
		switch {
		case g.to-g.from >= 0.6*b.dur:
			gr.Kind = GraphicLabel
		case b.hasEnd && g.from >= b.endStart-0.3:
			gr.Kind = GraphicEndCard
		case h >= 0.04 && (g.from < math.Max(b.hookEnd, 0.15*b.dur)+0.5 || b.startsPart(g.from)):
			gr.Kind = GraphicTitle
		case y >= 0.6 && y <= 0.92 && x < 0.55 && b.speechSeconds(g.from, g.to) > 0:
			gr.Kind = GraphicLowerThird
		default:
			gr.Kind = GraphicWords
		}
		out = append(out, gr)
		if len(out) == 24 {
			break
		}
	}
	return out
}

func (b *builder) startsPart(t float64) bool {
	for _, s := range b.parts {
		if s.Kind == KindBody && math.Abs(s.From-t) <= 1 {
			return true
		}
	}
	return false
}

func where(x, y float64) string {
	v := "middle"
	switch {
	case y < 0.33:
		v = "top"
	case y > 0.67:
		v = "bottom"
	}
	switch {
	case x < 0.35:
		return v + " left"
	case x > 0.65:
		return v + " right"
	}
	return v
}

func (b *builder) end(gs []WhatGraphic, snd WhatSound) *WhatEnd {
	if !b.hasEnd {
		return nil
	}
	e := &WhatEnd{From: b.endStart, To: round1(b.dur)}
	for _, g := range gs {
		if g.Kind == GraphicEndCard {
			e.Text = append(e.Text, g.Text)
		}
	}
	if l := b.in.Loudness; l != nil && l.Step > 0 {
		var vs []float64
		for k, v := range l.Values {
			at := float64(k+1) * l.Step
			if at-0.4 >= e.From && at <= e.To {
				vs = append(vs, v)
			}
		}
		e.Sound = len(vs) > 0 && median(vs) > quietDB
	}
	say := fmt.Sprintf("It ends on an end card for %s", secs(round1(e.To-e.From)))
	switch {
	case len(e.Text) > 0:
		say += ": words on screen (" + quoteAll(e.Text) + ")"
	default:
		say += ", nobody speaking"
	}
	if e.Sound {
		say += ", sound still playing"
	}
	e.Said = say + "."
	return e
}

// ---- sound ----

func (b *builder) sound() WhatSound {
	s := WhatSound{Measured: b.in.Measured && b.in.Loudness != nil && b.in.Loudness.Step > 0}
	if !b.in.HasAudio {
		s.Said = "It has no sound."
		return s
	}
	if !s.Measured {
		s.Said = "MetaRush hasn't measured its sound yet."
		return s
	}
	l := b.in.Loudness
	var speech, gaps, all []float64
	gapSpans := append([]Span(nil), b.pauses...)
	if len(b.speech) > 0 {
		if b.speech[0].From >= pauseMin {
			gapSpans = append(gapSpans, Span{0, b.speech[0].From})
		}
		if last := b.speech[len(b.speech)-1].To; b.endStart-last >= pauseMin {
			gapSpans = append(gapSpans, Span{last, b.endStart})
		}
	}
	for k, v := range l.Values {
		end := float64(k+1) * l.Step
		start := end - 0.4
		if start < 0 || end > b.dur+l.Step {
			continue
		}
		all = append(all, v)
		for _, sp := range b.speech {
			if start >= sp.From && end <= sp.To {
				speech = append(speech, v)
				break
			}
		}
		for _, g := range gapSpans {
			if start >= g.From+0.05 && end <= g.To-0.05 {
				gaps = append(gaps, math.Max(v, floorDB-1))
				break
			}
		}
	}
	if len(b.words) == 0 {
		quiet := 0
		for _, v := range all {
			if v <= quietDB {
				quiet++
			}
		}
		if len(all) > 0 && float64(quiet)/float64(len(all)) < 0.5 {
			s.Bed, s.Throughout = BedSound, true
			s.Said = "Nobody speaks; sound plays all the way through (likely music)."
		} else {
			s.Bed = BedNone
			s.Said = "Nobody speaks, and it is mostly quiet."
		}
		return s
	}
	if len(speech) > 0 {
		v := round1(median(speech))
		s.Speech = &v
	}
	if len(gaps) < 5 || s.Speech == nil {
		s.Said = "The voices hardly pause, so MetaRush can't hear what plays under them."
		return s
	}
	under := round1(median(gaps))
	s.Under = &under
	s.Below = round1(math.Max(0, *s.Speech-under))
	switch {
	case under <= quietDB:
		s.Bed = BedNone
		s.Said = "The pauses are quiet: nothing plays under the voices."
	case under <= roomDB:
		s.Bed = BedRoom
		s.Said = fmt.Sprintf("Only quiet room sound in the pauses, about %.0f dB under the voices.", s.Below)
	default:
		s.Bed = BedSound
		s.Said = fmt.Sprintf("Something keeps playing under the voices, likely music, about %.0f dB quieter than the words.", s.Below)
	}
	if s.Bed == BedSound && b.hasEnd {
		s.Throughout = true
	}
	return s
}

// ---- words ----

func summary(c *WhatItDoes, in Input) string {
	what := "video"
	if c.Shape != "" {
		what = c.Shape + " video"
	}
	head := fmt.Sprintf("A %s %s", lengthWords(c.Length), what)
	if len(c.Sections) == 0 {
		return head + "."
	}
	var parts []string
	body := 0
	for _, s := range c.Sections {
		if s.Kind == KindBody {
			body++
		}
	}
	for _, s := range c.Sections {
		switch s.Kind {
		case KindHook:
			parts = append(parts, fmt.Sprintf("a %s hook (%s)", lengthWords(s.Length), firstClause(s.Does)))
		case KindIntro:
			parts = append(parts, fmt.Sprintf("a %s intro", lengthWords(s.Length)))
		case KindBody:
			if s.Name == "body" || s.Name == "part-1" {
				if body > 1 {
					parts = append(parts, fmt.Sprintf("%d parts", body))
				} else {
					parts = append(parts, fmt.Sprintf("a %s body", lengthWords(s.Length)))
				}
			}
		case KindCTA:
			parts = append(parts, "a call to action")
		case KindEnd:
			parts = append(parts, fmt.Sprintf("a %s end card", lengthWords(s.Length)))
		}
	}
	out := head + ": " + joinParts(parts) + "."
	var more []string
	if c.Pacing.Shots > 1 {
		more = append(more, fmt.Sprintf("a new shot about every %s", secs(c.Pacing.ShotSeconds)))
	}
	switch n := len(c.Speakers); {
	case n == 1:
		more = append(more, fmt.Sprintf("one voice, %s", talkPace(c.Pacing.WordsPerMinute)))
	case n > 1:
		more = append(more, fmt.Sprintf("%d voices", n))
	}
	if c.Captions.Burned {
		more = append(more, "captions burned in")
	}
	if c.Sound.Bed == BedSound {
		more = append(more, "something plays under the voices")
	}
	if len(more) > 0 {
		out += " " + upperFirst(strings.Join(more, "; ")) + "."
	}
	return out
}

func firstClause(s string) string {
	if i := strings.Index(s, ","); i > 0 {
		return s[:i]
	}
	return s
}

// lines is the card in plain words, one thing a line.
func lines(c *WhatItDoes, in Input) []string {
	var out []string
	out = append(out, c.Summary)
	if len(c.Waiting) > 0 {
		out = append(out, "Still looking at it: "+joinParts(c.Waiting)+"; the card fills in as each is done.")
	}
	for _, s := range c.Sections {
		out = append(out, fmt.Sprintf("%s, %s–%s (%s): %s.", s.Title, clock(s.From), clock(s.To), secs(s.Length), s.Does))
	}
	out = append(out, "Pacing: "+lowerFirst(c.Pacing.Said))
	switch n := len(c.Speakers); {
	case n == 1:
		out = append(out, fmt.Sprintf("Who speaks: one voice%s, all the way.", nameIn(c.Speakers[0])))
	case n > 1:
		var parts []string
		for i, sp := range c.Speakers {
			if i == 4 {
				parts = append(parts, fmt.Sprintf("%d more", n-4))
				break
			}
			parts = append(parts, fmt.Sprintf("%s %s", speakerWords(sp, i), percent(sp.Share)))
		}
		out = append(out, "Who speaks: "+joinParts(parts)+".")
	}
	out = append(out, c.Captions.Said)
	if len(c.Graphics) > 0 {
		var parts []string
		for i, g := range c.Graphics {
			if i == 5 {
				parts = append(parts, fmt.Sprintf("%d more", len(c.Graphics)-5))
				break
			}
			parts = append(parts, fmt.Sprintf("%s “%s” at %s (%s)", graphicWords(g.Kind), g.Text, clock(g.From), g.Where))
		}
		out = append(out, "On screen: "+joinParts(parts)+".")
	} else if in.Looked {
		out = append(out, "On screen: no titles or other words besides captions.")
	}
	out = append(out, c.Sound.Said)
	if c.End != nil {
		out = append(out, c.End.Said)
	}
	if len(c.Missing) > 0 {
		out = append(out, "Not looked at here: "+joinParts(c.Missing)+".")
	}
	return out
}

func nameIn(sp WhatSpeaker) string {
	if sp.Name != "" {
		return " (" + sp.Name + ")"
	}
	return ""
}

func speakerWords(sp WhatSpeaker, i int) string {
	if sp.Name != "" {
		return sp.Name
	}
	return []string{"the first voice", "the second voice", "the third voice", "the fourth voice"}[min(i, 3)]
}

func graphicWords(k string) string {
	switch k {
	case GraphicTitle:
		return "a title"
	case GraphicLowerThird:
		return "a lower third"
	case GraphicEndCard:
		return "the end card's words"
	case GraphicLabel:
		return "a label that stays"
	}
	return "words"
}

func shapeOf(w, h int) (string, string) {
	if w <= 0 || h <= 0 {
		return "", ""
	}
	r := float64(w) / float64(h)
	switch {
	case r > 1.15:
		return "landscape", "widescreen (" + ratio(w, h) + ")"
	case r < 0.87:
		return "vertical", "vertical (" + ratio(w, h) + ")"
	}
	return "square", "square (" + ratio(w, h) + ")"
}

func ratio(w, h int) string {
	g := gcd(w, h)
	a, b := w/g, h/g
	if a > 50 || b > 50 {
		r := float64(w) / float64(h)
		for _, c := range [][2]int{{16, 9}, {9, 16}, {4, 3}, {3, 4}, {1, 1}, {4, 5}, {5, 4}, {21, 9}} {
			if math.Abs(r-float64(c[0])/float64(c[1])) < 0.03 {
				return fmt.Sprintf("%d:%d", c[0], c[1])
			}
		}
		return fmt.Sprintf("%d×%d", w, h)
	}
	return fmt.Sprintf("%d:%d", a, b)
}

func gcd(a, b int) int {
	for b != 0 {
		a, b = b, a%b
	}
	if a == 0 {
		return 1
	}
	return a
}

// ---- small helpers ----

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

func overlap(a0, a1, b0, b1 float64) float64 { return math.Max(0, math.Min(a1, b1)-math.Max(a0, b0)) }

func mergeSpans(ss []Span, gap float64) []Span {
	sort.SliceStable(ss, func(i, j int) bool { return ss[i].From < ss[j].From })
	var out []Span
	for _, s := range ss {
		if n := len(out); n > 0 && s.From-out[n-1].To <= gap {
			out[n-1].To = math.Max(out[n-1].To, s.To)
			continue
		}
		out = append(out, s)
	}
	return out
}

func dedupe(xs []float64, within float64) []float64 {
	var out []float64
	for _, x := range xs {
		if n := len(out); n > 0 && x-out[n-1] < within {
			continue
		}
		out = append(out, x)
	}
	return out
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	n := len(s)
	if n%2 == 1 {
		return s[n/2]
	}
	return (s[n/2-1] + s[n/2]) / 2
}

func minOf(xs []float64) float64 {
	m := math.Inf(1)
	for _, x := range xs {
		m = math.Min(m, x)
	}
	return m
}

func maxOf(xs []float64) float64 {
	m := math.Inf(-1)
	for _, x := range xs {
		m = math.Max(m, x)
	}
	return m
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
func round2(v float64) float64 { return math.Round(v*100) / 100 }

// secs says a length plainly ("2.5 s", "12 s", "1 min 30 s").
func secs(s float64) string {
	if s >= 60 {
		m := int(s) / 60
		r := s - float64(m*60)
		if r < 0.5 {
			return fmt.Sprintf("%d min", m)
		}
		return fmt.Sprintf("%d min %.0f s", m, r)
	}
	if s >= 10 || s == math.Trunc(s) {
		return fmt.Sprintf("%.0f s", s)
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", s), ".0") + " s"
}

// lengthWords says a length as an adjective ("30-second", "2-minute").
func lengthWords(s float64) string {
	if s >= 90 {
		return fmt.Sprintf("%.0f-minute", math.Round(s/60))
	}
	if s >= 10 || s == math.Trunc(s) {
		return fmt.Sprintf("%.0f-second", math.Round(s))
	}
	return strings.TrimSuffix(fmt.Sprintf("%.1f", s), ".0") + "-second"
}

// clock says a time in the video ("0:03.5", "1:02").
func clock(t float64) string {
	m := int(t) / 60
	s := t - float64(m*60)
	if math.Abs(s-math.Round(s)) < 0.05 {
		return fmt.Sprintf("%d:%02d", m, int(math.Round(s)))
	}
	return fmt.Sprintf("%d:%04.1f", m, s)
}

func percent(v float64) string { return fmt.Sprintf("%.0f%%", math.Round(v*100)) }

func plural(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

func joinParts(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func quoteAll(xs []string) string {
	q := make([]string, len(xs))
	for i, x := range xs {
		q[i] = "“" + x + "”"
	}
	return joinParts(q)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	r[0] = unicode.ToUpper(r[0])
	return string(r)
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if len(r) > 1 && unicode.IsUpper(r[1]) {
		return s // an initialism stays
	}
	r[0] = unicode.ToLower(r[0])
	return string(r)
}

func shorten(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

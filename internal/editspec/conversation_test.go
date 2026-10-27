package editspec

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
)

const (
	astJ  = "ast_01jabcdefghjkmnpqrstvwxyj0"
	astS  = "ast_01jabcdefghjkmnpqrstvwxys0"
	astG  = "ast_01jabcdefghjkmnpqrstvwxyg0"
	astBR = "ast_01jabcdefghjkmnpqrstvwxyb0"
	cnvID = "cnv_01jabcdefghjkmnpqrstvwxyz0"
)

// convFakeFacts adds loudness, momentary loudness and face boxes to fakeFacts.
type convFakeFacts struct {
	fakeFacts
	loud   map[string][2]float64
	series map[string][]float64
	faces  map[string][4]float64
}

func (f convFakeFacts) Loudness(id string) (float64, float64, bool) {
	l, ok := f.loud[id]
	return l[0], l[1], ok
}

func (f convFakeFacts) RangeLoudness(id string, from, to float64) (float64, bool) {
	l, ok := f.loud[id]
	return l[0], ok
}

func (f convFakeFacts) LoudnessSeries(id string) (float64, []float64, bool) {
	s, ok := f.series[id]
	return 0.1, s, ok
}

func (f convFakeFacts) FaceBox(id string, from, to float64) ([4]float64, bool) {
	b, ok := f.faces[id]
	return b, ok
}

func spokenWords(start float64, text string, gap float64) []TranscriptWord {
	var out []TranscriptWord
	t := start
	for _, w := range strings.Fields(text) {
		d := 0.12 + 0.05*float64(len(w))
		out = append(out, TranscriptWord{Text: w, From: round3(t), To: round3(t + d)})
		t += d + gap
	}
	return out
}

func seg(speaker string, ws []TranscriptWord) TranscriptSegment {
	var texts []string
	for _, w := range ws {
		texts = append(texts, w.Text)
	}
	return TranscriptSegment{From: ws[0].From, To: ws[len(ws)-1].To, Text: strings.Join(texts, " "), Speaker: speaker, Words: ws}
}

// convFixture is a two-person call: Maya asks, Sam (who started recording
// half a second later, on a clock 100 ppm fast) answers; each microphone
// picked up a word of the other.
func convFixture() (convFakeFacts, *Spec) {
	jWords := spokenWords(1.0, "So tell me about it", 0.05)
	sOwn := spokenWords((3.0-0.5)*1.0001, "Well we built it twice actually", 0.06)
	sBleed := []TranscriptWord{{Text: "tell", From: (1.45 - 0.5) * 1.0001, To: (1.6 - 0.5) * 1.0001}} // Maya's word in Sam's microphone
	jBleed := []TranscriptWord{{Text: "built", From: 3.75, To: 3.95}}                                 // Sam's word in Maya's
	f := convFakeFacts{
		fakeFacts: fakeFacts{assets: map[string]AssetFacts{
			astJ:  {ID: astJ, Name: "maya.mp4", Kind: "video", State: "available", InProject: true, Duration: 12, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, FPS: 30, TranscriptPrecision: "word"},
			astS:  {ID: astS, Name: "sam.mp4", Kind: "video", State: "available", InProject: true, Duration: 12, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, FPS: 30, TranscriptPrecision: "word"},
			astBR: {ID: astBR, Name: "garage.mp4", Kind: "video", State: "available", InProject: true, Duration: 20, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30},
		}, tx: map[string][]TranscriptSegment{
			astJ: {seg("Maya", jWords), seg("Speaker 2", jBleed)},
			astS: {seg("speaker_1", sBleed), seg("speaker_0", sOwn)},
		}},
		loud:  map[string][2]float64{astJ: {-20, -3}, astS: {-26, -6}},
		faces: map[string][4]float64{astJ: {0.40, 0.20, 0.20, 0.36}, astS: {0.55, 0.30, 0.16, 0.28}},
	}
	s := &Spec{Output: Output{Preset: PresetLandscape, Framing: FramingFaces}, Clips: []Clip{{ID: "talk", AssetID: ConvKey("call"), In: 0, Out: 8,
		Shots: []Shot{{At: 0, Member: "maya"}, {At: 3.3, Member: "sam", Rationale: "Sam answers."}, {At: 5, Layout: LayoutSideBySide}}}},
		Conversations: map[string]ConversationPin{"call": {ID: cnvID, Gate: NewGate(), Members: []ConversationMember{
			{ID: "maya", Name: "Maya", AssetID: astJ, Start: 0, Tag: "Host", Framing: &Framing{Auto: FramingFaces}},
			{ID: "sam", Name: "Sam", AssetID: astS, Start: 0.5, Drift: 100, Framing: &Framing{Auto: FramingFaces}},
		}}},
		Captions: &Captions{Enabled: true, ShowSpeakers: true},
	}
	Normalize(s)
	return f, s
}

func TestConversationResolves(t *testing.T) {
	f, s := convFixture()
	if is := Validate(s, f, nil, false); HasErrors(is) {
		t.Fatalf("unexpected errors: %v", is)
	}
	plan := Resolve(Clone(s), f, "rev_x", "prj_x", Hash(s))
	pc := plan.Clips[0]
	if pc.Conversation == nil || len(pc.Conversation.Shots) != 3 {
		t.Fatalf("shots: %+v", pc.Conversation)
	}
	sh := pc.Conversation.Shots
	if sh[0].From != 0 || sh[1].From != 99 || sh[2].From != 150 || sh[2].From+sh[2].DurationInFrames != 240 {
		t.Fatalf("shot frames: %+v", sh)
	}
	if sh[1].Tiles[0].Member != "sam" || sh[1].Tiles[0].TrimBefore != int(math.Round((3.3-0.5)*1.0001*30)) || sh[1].Tiles[0].PlaybackRate != 1.0001 {
		t.Fatalf("sam's tile: %+v", sh[1].Tiles[0])
	}
	if sh[2].Kind != "layout" || len(sh[2].Tiles) != 2 || sh[2].Tiles[0].Tag == nil || sh[2].Tiles[0].Tag.Title != "Host" {
		t.Fatalf("layout: %+v", sh[2])
	}
	// The faces sit on one eye line and are the same size in both tiles.
	eye := func(tl PlanTile, box [4]float64) (float64, float64) {
		scale := tl.Video.W / 1280
		return tl.Video.Y + (box[1]+box[3]*eyesInFace)*720*scale, box[3] * 720 * scale
	}
	ej, hj := eye(sh[2].Tiles[0], f.faces[astJ])
	es, hs := eye(sh[2].Tiles[1], f.faces[astS])
	if math.Abs(ej-es) > 2 || math.Abs(hj-hs) > 2 {
		t.Fatalf("headroom doesn't match: eyes %.1f vs %.1f, faces %.1f vs %.1f", ej, es, hj, hs)
	}
	if pc.Gain != nil || pc.Audio != nil {
		t.Fatalf("the conversation clip's picture plays silently: %+v", pc)
	}
	var j, sam *PlanAudio
	for i := range plan.Audio {
		switch plan.Audio[i].ID {
		case "talk/maya":
			j = &plan.Audio[i]
		case "talk/sam":
			sam = &plan.Audio[i]
		}
	}
	if j == nil || sam == nil {
		t.Fatalf("member sounds: %+v", plan.Audio)
	}
	if j.From != 0 || j.TrimBefore != 0 || sam.From != 15 || sam.TrimBefore != 0 || sam.PlaybackRate != 1.0001 || sam.Conversation != "talk" || sam.Member != "sam" {
		t.Fatalf("member timing: maya %+v sam %+v", j, sam)
	}
	// While Maya asks (1 s in), Sam's track is down by the gate; hers is open.
	gj := GainAt(j.Gain, 30)
	gs := GainAt(sam.Gain, float64(30-sam.From))
	if gj < 0.5 || gs > gj*0.2 {
		t.Fatalf("gate: maya %.3f sam %.3f", gj, gs)
	}
	// Captions are each person's own words, named, without the picked-up copies.
	var lines []string
	for _, l := range plan.Captions.Lines {
		lines = append(lines, l.Speaker+": "+l.Text)
	}
	got := strings.Join(lines, " | ")
	if !strings.Contains(got, "Maya: So tell me") || !strings.Contains(got, "Sam: Well we built") || strings.Count(got, "tell") != 1 || strings.Count(got, "built") != 1 {
		t.Fatalf("captions: %s", got)
	}
	// Sam's sound is leveled toward Maya's (one of two; the median is the quieter).
	if len(plan.Mix.Buses) == 0 {
		t.Fatal("mix missing")
	}
}

func TestConversationEditsKeepThePicture(t *testing.T) {
	_, s := convFixture()
	next, _, err := ApplyOps(s, []Op{{Op: "splitClip", ID: "talk", At: ptr(4.0)}}, false)
	if err != nil {
		t.Fatal(err)
	}
	l, r := next.Clips[0], next.Clips[1]
	if len(l.Shots) != 2 || l.Shots[1].At != 3.3 || len(r.Shots) != 2 || r.Shots[0].At != 4 || r.Shots[0].Member != "sam" || r.Shots[1].Layout != LayoutSideBySide {
		t.Fatalf("split shots: left %+v right %+v", l.Shots, r.Shots)
	}
	ops, err := DiffOps(s, next)
	if err != nil {
		t.Fatal(err)
	}
	back, _, err := ApplyOps(s, ops, false)
	if err != nil || Hash(back) != Hash(next) {
		t.Fatalf("diff ops don't reproduce the split: %v %+v", err, ops)
	}
	// Removing a stretch across a shot change: the part after it starts with the shot it starts in.
	cut, _, err := ApplyOps(s, []Op{{Op: "deleteRange", From: ptr(3.0), To: ptr(5.5)}}, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(cut.Clips) != 2 || cut.Clips[1].Shots[0].At != 5.5 || cut.Clips[1].Shots[0].Layout != LayoutSideBySide {
		t.Fatalf("delete range: %+v", cut.Clips)
	}
	// A new alignment is one setConversation op.
	moved := Clone(s)
	pin := moved.Conversations["call"]
	pin.Members[1].Start = 0.533
	moved.Conversations["call"] = pin
	ops, err = DiffOps(s, moved)
	if err != nil || len(ops) != 1 || ops[0].Op != "setConversation" || ops[0].ID != "call" {
		t.Fatalf("pin change: %v %+v", err, ops)
	}
	b, _ := json.Marshal(ops[0])
	if !strings.Contains(string(b), `"start":0.533`) {
		t.Fatalf("op: %s", b)
	}
}

func TestConversationValidation(t *testing.T) {
	f, s := convFixture()
	bad := Clone(s)
	bad.Clips[0].Speed = 2
	bad.Clips[0].Shots = append(bad.Clips[0].Shots, Shot{At: 6, Member: "nobody"}, Shot{At: 7, Layout: LayoutPiP, Members: []string{"maya"}}, Shot{At: 7.5, Member: "sam", Card: "both"})
	bad.Clips = append(bad.Clips, Clip{ID: "x", AssetID: ConvKey("other"), In: 0, Out: 1})
	Normalize(bad)
	codes := map[string]bool{}
	for _, is := range Validate(bad, f, nil, false) {
		codes[is.Code] = true
	}
	for _, want := range []string{"conversation_speed", "conversation_member", "layout_members", "shot_kind", "conversation_unknown"} {
		if !codes[want] {
			t.Errorf("expected %s, got %v", want, codes)
		}
	}
}

// Specs without conversations get their facts back unwrapped (old cuts
// resolve exactly as before; the legacy golden test checks the plans).
func TestNoConversationNoWrapping(t *testing.T) {
	s := baseSpec()
	f := facts()
	if _, wrapped := wrapFacts(s, f).(*convFacts); wrapped {
		t.Fatal("a spec without conversations must not be wrapped")
	}
	if HasConversations(s) {
		t.Fatal("no conversations here")
	}
}

func TestLookMatrix(t *testing.T) {
	if LookMatrix(&Look{}) != nil || LookMatrix(nil) != nil {
		t.Fatal("neutral looks are nil")
	}
	l := &Look{Exposure: 1.1, Contrast: 0.9, Saturation: 1.2, Warmth: 0.05, Tint: -0.02}
	grey := ApplyLook(&Look{Exposure: 1.2}, [3]float64{0.5, 0.5, 0.5})
	if math.Abs(grey[0]-0.6) > 1e-6 || math.Abs(grey[1]-0.6) > 1e-6 {
		t.Fatalf("exposure: %v", grey)
	}
	warm := ApplyLook(&Look{Warmth: 0.1}, [3]float64{0.5, 0.5, 0.5})
	if !(warm[0] > warm[1] && warm[1] > warm[2]) {
		t.Fatalf("warmth: %v", warm)
	}
	if m := LookMatrix(l); len(m) != 20 || m[18] != 1 {
		t.Fatalf("matrix: %v", m)
	}
}

func TestLayoutTiles(t *testing.T) {
	for _, tc := range []struct {
		layout string
		n      int
		w, h   int
	}{{LayoutSideBySide, 2, 1920, 1080}, {LayoutStacked, 2, 1080, 1920}, {LayoutGrid, 4, 1920, 1080}, {LayoutGrid, 3, 1920, 1080}, {LayoutPiP, 2, 1920, 1080}, {LayoutPiP, 2, 1080, 1920}, {LayoutGrid, 9, 1080, 1920}} {
		rs := LayoutTiles(tc.layout, tc.n, tc.w, tc.h)
		if len(rs) != tc.n {
			t.Fatalf("%s %d: %v", tc.layout, tc.n, rs)
		}
		for _, r := range rs {
			if r.X < 0 || r.Y < 0 || r.X+r.W > float64(tc.w)+0.5 || r.Y+r.H > float64(tc.h)+0.5 || r.W <= 0 || r.H <= 0 {
				t.Fatalf("%s %d: tile outside the frame: %v", tc.layout, tc.n, r)
			}
		}
		if tc.layout != LayoutPiP {
			for i := range rs {
				for j := i + 1; j < len(rs); j++ {
					a, b := rs[i], rs[j]
					if a.X < b.X+b.W-0.5 && b.X < a.X+a.W-0.5 && a.Y < b.Y+b.H-0.5 && b.Y < a.Y+a.H-0.5 {
						t.Fatalf("%s %d: tiles overlap: %v %v", tc.layout, tc.n, a, b)
					}
				}
			}
		}
	}
	// A crop never shows: the tile stays inside the cropped picture.
	v, _ := placePicture(960, 1080, 1280, 720, &Crop{Top: 0.1, Bottom: 0.1, Left: 0.05}, nil, nil, tileFrame{faceShare: tileFace, eyes: tileEyes, maxZoom: tileMaxZoom})
	scale := v.W / 1280
	if v.X > -0.05*1280*scale+0.5 || v.Y > -0.1*720*scale+0.5 || v.Y+0.9*720*scale < 1080-0.5 {
		t.Fatalf("crop shows: %+v", v)
	}
}

// Real recognizers transcribe the other person's voice coming out of the
// speakers too, often as a second voice that says more than the member
// (their guest talks longer). The loudness of each track decides whose words
// are whose: Maya's own short question stays, Sam's long answer heard
// quietly in her microphone goes — even where the recognizer heard it wrong.
func TestPickedUpVoicesByLoudness(t *testing.T) {
	f, s := convFixture()
	// Maya's recording: her question (speaker_0, loud) and Sam's answer
	// picked up (speaker_1, 20 dB down, more words, and misheard).
	jOwn := spokenWords(1.0, "So tell me about it", 0.05)
	jBleed := spokenWords(3.2, "Well we built it twice actually and then a third time", 0.06)
	jBleed[2].Text = "bill" // misheard
	f.tx[astJ] = []TranscriptSegment{seg("speaker_0", jOwn), seg("speaker_1", jBleed)}
	// Sam's recording (starts 0.5 s in, 100 ppm fast): his answer, loud.
	sOwn := spokenWords((3.0-0.5)*1.0001, "Well we built it twice actually and then a third time", 0.06)
	f.tx[astS] = []TranscriptSegment{seg("speaker_0", sOwn)}
	level := func(dur float64, loud [][2]float64, at float64) []float64 {
		v := make([]float64, int(dur*10))
		for k := range v {
			v[k] = -60
			t := float64(k+1)*0.1 - 0.2
			for _, l := range loud {
				if t >= l[0] && t < l[1] {
					v[k] = at
				}
			}
		}
		return v
	}
	jEnd := jOwn[len(jOwn)-1].To
	bleedFrom, bleedTo := jBleed[0].From, jBleed[len(jBleed)-1].To
	j := level(12, [][2]float64{{1.0, jEnd}}, -20)
	for k := range j { // the answer in her microphone, 20 dB under her voice
		t := float64(k+1)*0.1 - 0.2
		if t >= bleedFrom && t < bleedTo {
			j[k] = -40
		}
	}
	f.series = map[string][]float64{astJ: j, astS: level(12, [][2]float64{{sOwn[0].From, sOwn[len(sOwn)-1].To}}, -22)}
	words := Transcripts(s, f)(ConvKey("call"))
	var maya, sam []string
	for _, sg := range words {
		for _, w := range sg.Words {
			if sg.Speaker == "Maya" {
				maya = append(maya, w.Text)
			} else {
				sam = append(sam, w.Text)
			}
		}
	}
	if strings.Join(maya, " ") != "So tell me about it" {
		t.Fatalf("Maya's words: %v", maya)
	}
	if strings.Join(sam, " ") != "Well we built it twice actually and then a third time" {
		t.Fatalf("Sam's words: %v", sam)
	}
}

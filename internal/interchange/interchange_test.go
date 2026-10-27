package interchange

import (
	"encoding/json"
	"encoding/xml"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

const (
	astA = "ast_aaaaaaaaaaaaaaaaaaaaaaaaaa" // 29.97 fps interview, stereo
	astB = "ast_bbbbbbbbbbbbbbbbbbbbbbbbbb" // 30 fps interview, mono
	astC = "ast_cccccccccccccccccccccccccc" // vertical b-roll, stereo
	astM = "ast_mmmmmmmmmmmmmmmmmmmmmmmmmm" // music, stereo 44.1 kHz
	astV = "ast_vvvvvvvvvvvvvvvvvvvvvvvvvv" // narration, mono
)

type testFacts struct {
	assets   map[string]editspec.AssetFacts
	channels map[string]int
}

func (f testFacts) Asset(id string) (editspec.AssetFacts, bool) { a, ok := f.assets[id]; return a, ok }
func (f testFacts) AudioChannels(id string) int                 { return f.channels[id] }

// Transcript: the interviews talk all the time (a word every half second), so
// music ducks under them.
func (f testFacts) Transcript(id string) []editspec.TranscriptSegment {
	if id != astA && id != astB {
		return nil
	}
	seg := editspec.TranscriptSegment{From: 0, To: f.assets[id].Duration, Text: "words", Speaker: "Sam"}
	for t := 0.0; t+0.4 < seg.To; t += 0.5 {
		seg.Words = append(seg.Words, editspec.TranscriptWord{Text: "word", From: t, To: t + 0.4})
	}
	return []editspec.TranscriptSegment{seg}
}

func fixtureFacts() testFacts {
	return testFacts{
		assets: map[string]editspec.AssetFacts{
			astA: {ID: astA, Name: "Interview A", Kind: "video", State: "available", InProject: true, Duration: 60, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30000.0 / 1001},
			astB: {ID: astB, Name: "Interview B", Kind: "video", State: "available", InProject: true, Duration: 40, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30},
			astC: {ID: astC, Name: "Workshop b-roll", Kind: "video", State: "available", InProject: true, Duration: 10, HasVideo: true, HasAudio: true, Width: 1080, Height: 1920, FPS: 30},
			astM: {ID: astM, Name: "Warm bed", Kind: "audio", State: "available", InProject: true, Duration: 90, HasAudio: true, RightsRecorded: true},
			astV: {ID: astV, Name: "Narration take", Kind: "audio", State: "available", InProject: true, Duration: 5, HasAudio: true},
		},
		channels: map[string]int{astA: 2, astB: 1, astC: 2, astM: 2, astV: 1},
	}
}

func fixtureMedia() map[string]*Media {
	return map[string]*Media{
		astA: {ID: astA, Name: "Interview A.mov", Path: "/footage/Interview A.mov", Duration: 60, FPS: 30000.0 / 1001, Width: 1920, Height: 1080, HasVideo: true, HasAudio: true, Channels: 2, SampleRate: 48000},
		astB: {ID: astB, Name: "Interview B.mp4", Path: "/footage/Interview B.mp4", Duration: 40, FPS: 30, Width: 1920, Height: 1080, HasVideo: true, HasAudio: true, Channels: 1, SampleRate: 48000},
		astC: {ID: astC, Name: "Workshop b-roll.mp4", Path: "/footage/Workshop b-roll.mp4", Duration: 10, FPS: 30, Width: 1080, Height: 1920, HasVideo: true, HasAudio: true, Channels: 2, SampleRate: 48000},
		astM: {ID: astM, Name: "Warm bed.m4a", Path: "/footage/Warm bed.m4a", Duration: 90, HasAudio: true, Channels: 2, SampleRate: 44100},
		astV: {ID: astV, Name: "Narration take.wav", Path: "/footage/Narration take.wav", Duration: 5, HasAudio: true, Channels: 1, SampleRate: 48000},
	}
}

func f64(v float64) *float64 { return &v }

// fixtureSpec uses every feature the export carries.
func fixtureSpec() *editspec.Spec {
	return &editspec.Spec{
		SchemaVersion: 1, Title: "Shop story",
		Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30},
		Clips: []editspec.Clip{
			{ID: "intro", AssetID: astA, In: 2, Out: 6, Transition: &editspec.Transition{Type: editspec.TransitionFade, Duration: 0.5}},
			{ID: "answer", AssetID: astB, In: 10, Out: 16, Label: "Sam's answer", Transition: &editspec.Transition{Type: editspec.TransitionCrossfade, Duration: 0.4}},
			{ID: "broll", AssetID: astC, In: 1, Out: 4, Muted: true, Transition: &editspec.Transition{Type: editspec.TransitionDip, Duration: 0.6}},
			{ID: "reply", AssetID: astB, In: 20, Out: 25, Transition: &editspec.Transition{Type: editspec.TransitionCut, Audio: &editspec.SoundTransition{Mode: editspec.SoundLead, Duration: 0.4}}},
			{ID: "quick", AssetID: astA, In: 30, Out: 34, Speed: 2, Transition: &editspec.Transition{Type: editspec.TransitionWipe, Duration: 0.3, Direction: editspec.DirLeft}},
			{ID: "outro", AssetID: astB, In: 30, Out: 35, FadeOut: 1, Transition: &editspec.Transition{Type: editspec.TransitionCut, Audio: &editspec.SoundTransition{Mode: editspec.SoundCarry, Duration: 0.3}}},
		},
		Audio: []editspec.AudioTrack{
			{ID: "music", AssetID: astM, Role: editspec.RoleMusic, In: 0, Out: 20, At: 0, GainDB: f64(-12), FadeIn: 1, FadeOut: 2, DuckUnderDialogue: true},
			{ID: "vo", AssetID: astV, Role: editspec.RoleVoiceover, In: 0, Out: 3, At: 8},
		},
		Captions: &editspec.Captions{Enabled: true, Source: "manual", ShowSpeakers: true, Segments: []editspec.CaptionSegment{
			{From: 0.5, To: 2.0, Text: "Welcome to the shop"}, {From: 6.0, To: 8.5, Text: "We build robots", Speaker: "Sam"}}},
		Overlays: []editspec.Overlay{
			{ID: "who", Template: editspec.TplLowerThirdClean, At: 1, Duration: 3, Params: json.RawMessage(`{"name":"Sam Lee","title":"Host"}`)},
			{ID: "end", Template: editspec.TplTitleCard, At: 20, Duration: 2, Params: json.RawMessage(`{"title":"The end"}`)},
		},
	}
}

func fixtureTimeline(t *testing.T) (*Timeline, *editspec.RenderPlan) {
	t.Helper()
	plan := editspec.Resolve(fixtureSpec(), fixtureFacts(), "rev_test", "prj_test", "hash")
	media := fixtureMedia()
	tl, err := Build(plan, BuildOptions{Name: "Shop story (version 3)", Names: map[string]string{"answer": "Sam's answer"}, Media: func(id string) *Media { return media[id] }})
	if err != nil {
		t.Fatal(err)
	}
	return tl, plan
}

func TestBuildCarriesTheCut(t *testing.T) {
	tl, plan := fixtureTimeline(t)
	if tl.Duration != plan.DurationInFrames || tl.FPS != 30 || tl.Width != 1920 {
		t.Fatalf("timeline %d frames %d fps %dx%d", tl.Duration, tl.FPS, tl.Width, tl.Height)
	}
	pic := tl.Video[0]
	if len(pic.Items) != 6 {
		t.Fatalf("%d picture items", len(pic.Items))
	}
	kinds := []string{}
	for _, c := range pic.Cuts {
		kinds = append(kinds, c.Kind)
	}
	if strings.Join(kinds, ",") != ",dissolve,dip-black,,wipe," {
		t.Fatalf("cuts %v", kinds)
	}
	if pic.Items[0].FadeIn != 15 || pic.Cuts[1].Overlap != 12 || pic.Cuts[2].DipOut != 9 || pic.Cuts[2].DipIn != 9 || pic.Cuts[4].Overlap != 9 {
		t.Fatalf("fade %d overlap %d dip %d/%d wipe %d", pic.Items[0].FadeIn, pic.Cuts[1].Overlap, pic.Cuts[2].DipOut, pic.Cuts[2].DipIn, pic.Cuts[4].Overlap)
	}
	if pic.Items[1].FadeOut != 0 && pic.Items[1].FadeOut != 9 { // the dip's first half is the cut's, not a fade of its own
		t.Fatalf("answer fade-out %d", pic.Items[1].FadeOut)
	}
	if pic.Items[1].FadeOut != 0 {
		t.Fatal("a dip's first half must not also be a fade-out")
	}
	if pic.Items[5].FadeOut != 30 || pic.Items[4].Speed != 2 || pic.Items[1].Name != "Sam's answer" {
		t.Fatalf("outro fade %d, quick speed %v, name %q", pic.Items[5].FadeOut, pic.Items[4].Speed, pic.Items[1].Name)
	}
	// Source in points are where MetaRush plays from (frames at the timeline rate).
	if pic.Items[1].Source != 300 || pic.Items[3].Source != 600 {
		t.Fatalf("sources %v %v", pic.Items[1].Source, pic.Items[3].Source)
	}
	// Sound: the muted b-roll has none; the J-cut's sound starts before its picture.
	var clipSound []*Item
	for _, tr := range tl.Audio {
		if tr.Role == RoleClipSound {
			clipSound = append(clipSound, tr.Items...)
		}
	}
	if len(clipSound) != 5 {
		t.Fatalf("%d clip sounds", len(clipSound))
	}
	var reply *Item
	for _, s := range clipSound {
		if s.Link == "clip:reply" {
			reply = s
		}
	}
	if reply == nil || reply.Start >= pic.Items[3].Start || pic.Items[3].Start-reply.Start < 11 {
		t.Fatalf("J-cut: sound at %v, picture at %d", reply, pic.Items[3].Start)
	}
	roles := map[string]int{}
	for _, tr := range tl.Audio {
		roles[tr.Role] += len(tr.Items)
	}
	if roles[RoleMusic] != 1 || roles[RoleNarration] != 1 {
		t.Fatalf("roles %v", roles)
	}
	var music *Item
	for _, tr := range tl.Audio {
		if tr.Role == RoleMusic {
			music = tr.Items[0]
		}
	}
	if music.FadeIn < 25 || music.FadeOut < 50 || len(music.Keys) == 0 || music.LevelDB > -11 {
		t.Fatalf("music: fades %d/%d, level %.1f dB, %d keys (ducked under the dialogue)", music.FadeIn, music.FadeOut, music.LevelDB, len(music.Keys))
	}
	if len(tl.Captions) != 2 || len(tl.Markers) != 4 {
		t.Fatalf("captions %d markers %d", len(tl.Captions), len(tl.Markers))
	}
	if !strings.Contains(tl.Markers[1].Name, "Lower third: Sam Lee, Host") && !strings.Contains(tl.Markers[0].Name, "Lower third") {
		t.Fatalf("markers %+v", tl.Markers)
	}
}

// Layout: overlaps become transitions centred on the edit point, sources move
// with the edit point, and every frame of the timeline is covered once.
func TestLayoutCentresTransitions(t *testing.T) {
	tl, _ := fixtureTimeline(t)
	var notes []string
	segs := layoutTrack(tl.Video[0], tl.FPS, &notes, false)
	covered := 0
	var trans []segment
	for _, s := range segs {
		switch s.kind {
		case segItem:
			if s.start != covered {
				t.Fatalf("item %s starts at %d, previous ended at %d", s.item.Name, s.start, covered)
			}
			covered = s.end()
		case segTrans:
			trans = append(trans, s)
		}
	}
	if covered != tl.Duration {
		t.Fatalf("picture covers %d of %d frames", covered, tl.Duration)
	}
	got := []string{}
	for _, s := range trans {
		got = append(got, s.trans)
	}
	if strings.Join(got, ",") != "fade-in,dissolve,dip-black,wipe,fade-out" {
		t.Fatalf("transitions %v", got)
	}
	d := trans[1]
	if d.before != 6 || d.after != 6 {
		t.Fatalf("dissolve %d/%d", d.before, d.after)
	}
	// The answer clip starts at the edit point, 6 frames into its own window.
	for _, s := range segs {
		if s.kind == segItem && s.item.Link == "clip:answer" && (s.source != 306 || s.start != d.cut) {
			t.Fatalf("answer at %d from %v (cut %d)", s.start, s.source, d.cut)
		}
	}
	w := trans[3]
	if w.before+w.after != 9 {
		t.Fatalf("wipe %d+%d", w.before, w.after)
	}
}

// A fade through black needs footage beyond each clip's own range; when the
// footage ends there the fade is shortened, and a note says so.
func TestDipShrinksToTheFootageThereIs(t *testing.T) {
	m := &Media{ID: "x", Name: "x.mov", Path: "/x.mov", Duration: 4, FPS: 30, HasVideo: true}
	a := &Item{Name: "a", Media: m, Start: 0, Duration: 60, Source: 60, Speed: 1} // ends at the media's last frame
	b := &Item{Name: "b", Media: m, Start: 60, Duration: 60, Source: 3, Speed: 1} // 3 frames of footage before it
	tr := &Track{Kind: KindVideo, Items: []*Item{a, b}, Cuts: []Cut{{}, {Kind: TransDipBlack, DipOut: 10, DipIn: 10}}}
	var notes []string
	segs := layoutTrack(tr, 30, &notes, false)
	var dip *segment
	for i := range segs {
		if segs[i].kind == segTrans {
			dip = &segs[i]
		}
	}
	if dip == nil || dip.before != 3 || dip.after != 0 {
		t.Fatalf("dip %+v", dip)
	}
	if len(notes) != 1 || !strings.Contains(notes[0], "shorter") {
		t.Fatalf("notes %v", notes)
	}
}

// Clearer voices (D88): where a processed copy plays instead of a recording's
// own sound (a clip's or an added dialogue recording's), the export uses the
// copy at the same timing; the picture keeps the original and nothing else
// moves. (Narration plays its cleaned take as its own asset already.)
func TestClearerVoicesExportTheCopy(t *testing.T) {
	const copyB, copyV = "ast_eeeeeeeeeeeeeeeeeeeeeeeeee", "ast_ffffffffffffffffffffffffff"
	media := fixtureMedia()
	media[copyB] = &Media{ID: copyB, Name: "Interview B (clearer).wav", Path: "/footage/Interview B (clearer).wav", Duration: 35, HasAudio: true, Channels: 1, SampleRate: 48000}
	media[copyV] = &Media{ID: copyV, Name: "Narration take (clearer).wav", Path: "/footage/Narration take (clearer).wav", Duration: 5, HasAudio: true, Channels: 1, SampleRate: 48000}
	build := func(s *editspec.Spec, facts testFacts) *Timeline {
		plan := editspec.Resolve(s, facts, "rev_test", "prj_test", "hash")
		tl, err := Build(plan, BuildOptions{Name: "Shop story", Media: func(id string) *Media { return media[id] }})
		if err != nil {
			t.Fatal(err)
		}
		return tl
	}
	byLink := func(tracks []*Track) map[string]*Item {
		out := map[string]*Item{}
		for _, tr := range tracks {
			for _, it := range tr.Items {
				out[it.Link] = it
			}
		}
		return out
	}
	spec := func() *editspec.Spec {
		s := fixtureSpec()
		s.Audio[1].Role = editspec.RoleDialogue // someone's voice recorded apart
		return s
	}
	before := build(spec(), fixtureFacts())
	s, facts := spec(), fixtureFacts()
	facts.assets[copyB] = editspec.AssetFacts{ID: copyB, Name: "Interview B (clearer)", Kind: "audio", State: "available", Duration: 35, HasAudio: true}
	facts.assets[copyV] = editspec.AssetFacts{ID: copyV, Name: "Narration take (clearer)", Kind: "audio", State: "available", Duration: 5, HasAudio: true}
	facts.channels[copyB], facts.channels[copyV] = 1, 1
	s.Mix = &editspec.Mix{Enhanced: []editspec.EnhancedSound{
		{AssetID: astB, Start: 5, Variant: copyB}, // from 5 s into Interview B: 150 frames earlier in the copy
		{AssetID: astV, Start: 0, Variant: copyV},
	}}
	after := build(s, facts)

	was, now := byLink(before.Audio), byLink(after.Audio)
	if len(was) != len(now) || len(was) == 0 {
		t.Fatalf("%d sound items before, %d after", len(was), len(now))
	}
	swapped := map[string]int{}
	for link, b := range was {
		a := now[link]
		if a == nil || a.Start != b.Start || a.Duration != b.Duration || a.FadeIn != b.FadeIn || a.FadeOut != b.FadeOut || math.Abs(a.LevelDB-b.LevelDB) > 0.05 {
			t.Fatalf("%s moved or changed level: %+v, was %+v", link, a, b)
		}
		switch b.Media.ID {
		case astB:
			if a.Media.ID != copyB || a.Source != b.Source-150 {
				t.Fatalf("%s: plays %s from %v (was %v), want the copy 150 frames earlier", link, a.Media.ID, a.Source, b.Source)
			}
		case astV:
			if a.Media.ID != copyV || a.Source != b.Source {
				t.Fatalf("the dialogue recording plays %s from %v", a.Media.ID, a.Source)
			}
		default:
			if a.Media.ID != b.Media.ID || a.Source != b.Source {
				t.Fatalf("%s should be untouched: %s from %v", link, a.Media.ID, a.Source)
			}
		}
		swapped[a.Media.ID]++
	}
	if swapped[copyB] != 3 || swapped[copyV] != 1 {
		t.Fatalf("clearer copies used %v, want Interview B's three clips and the dialogue recording", swapped)
	}
	for link, p := range byLink(after.Video) {
		if p.Media != nil && p.Media.ID != byLink(before.Video)[link].Media.ID {
			t.Fatalf("picture %s changed to %s", link, p.Media.ID)
		}
	}
	listed := map[string]bool{}
	for _, m := range after.Media {
		listed[m.ID] = true
	}
	if !listed[copyB] || !listed[copyV] || !listed[astB] || listed[astV] {
		t.Fatalf("media listed %v: want both copies and Interview B (its picture), not the dialogue's original", listed)
	}
	files := writeNamed(t, after, "clearer")
	if !strings.Contains(string(files[FormatOTIO]), "Interview B (clearer).wav") {
		t.Fatal("OTIO should reference the clearer copy")
	}
	// FCPXML plays a clip's own sound from the picture's file, so a clip whose
	// sound is a copy plays its picture muted with the copy connected below.
	fcp := func(b []byte) (copies, muted, own int, lanes map[string]string) {
		_, x := xmlElems(t, b)
		titles := map[string]string{}
		for _, a := range x["asset"] {
			titles[a["id"]] = a["name"]
		}
		lanes = map[string]string{}
		for _, a := range x["asset-clip"] {
			switch title := titles[a["ref"]]; {
			case title == "Interview B (clearer)" && a["lane"] == "-1":
				copies++
			case title == "Interview B" && a["lane"] == "" && a["srcEnable"] == "video":
				muted++
			case title == "Interview B" && a["lane"] == "" && a["srcEnable"] == "":
				own++
			default:
				lanes[title] = a["lane"]
			}
		}
		return copies, muted, own, lanes
	}
	if copies, muted, own, _ := fcp(writeNamed(t, before, "clearer-before")[FormatFCPXML]); copies != 0 || muted != 0 || own != 3 {
		t.Fatalf("FCPXML before: %d copies, %d muted, %d with their own sound", copies, muted, own)
	}
	copies, muted, own, lanes := fcp(files[FormatFCPXML])
	if copies != 3 || muted != 3 || own != 0 || lanes["Narration take (clearer)"] != "-2" {
		t.Fatalf("FCPXML: %d copies in lane -1, %d muted pictures, %d with their own sound; other lanes %v", copies, muted, own, lanes)
	}
}

func writeAll(t *testing.T, tl *Timeline) map[string][]byte { return writeNamed(t, tl, "fixture") }

// writeNamed writes every format; with INTERCHANGE_OUT the files (and the
// layout a reader should find) go there for scripts/interchange-check.sh.
func writeNamed(t *testing.T, tl *Timeline, name string) map[string][]byte {
	t.Helper()
	out := map[string][]byte{}
	for _, f := range Formats {
		var b []byte
		var err error
		switch f {
		case FormatOTIO:
			b, _, err = WriteOTIO(tl, map[string]any{"revision": 3})
		case FormatFCPXML:
			b, _, err = WriteFCPXML(tl, nil)
		case FormatXMEML:
			b, _, err = WriteXMEML(tl, nil)
		}
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		out[f] = b
	}
	if dir := os.Getenv("INTERCHANGE_OUT"); dir != "" { // scripts/interchange-check.sh validates these
		for f, b := range out {
			if err := os.WriteFile(filepath.Join(dir, name+Extension(f)), b, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		exp, _ := json.MarshalIndent(expected(tl), "", "  ")
		if err := os.WriteFile(filepath.Join(dir, name+".expected.json"), exp, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

// expected is what a reader of each file should find (for the OpenTimelineIO
// round trip in scripts/interchange-check.sh).
func expected(tl *Timeline) map[string]any {
	type seg struct {
		Kind     string  `json:"kind"`
		Name     string  `json:"name,omitempty"`
		Start    int     `json:"start"`
		Duration int     `json:"duration"`
		Source   float64 `json:"source"`
		Speed    float64 `json:"speed,omitempty"`
		Before   int     `json:"before,omitempty"`
		After    int     `json:"after,omitempty"`
	}
	tracks := []map[string]any{}
	var notes []string
	for _, group := range [][]*Track{tl.Video, tl.Audio} {
		for _, t := range group {
			var segs []seg
			for _, s := range layoutTrack(t, tl.FPS, &notes, false) {
				switch s.kind {
				case segItem:
					segs = append(segs, seg{Kind: "clip", Name: s.item.Name, Start: s.start, Duration: s.duration, Source: s.source, Speed: s.item.Speed})
				case segGap:
					segs = append(segs, seg{Kind: "gap", Start: s.start, Duration: s.duration})
				case segTrans:
					segs = append(segs, seg{Kind: "transition", Start: s.cut, Before: s.before, After: s.after})
				}
			}
			tracks = append(tracks, map[string]any{"name": t.Name, "kind": t.Kind, "role": t.Role, "segments": segs})
		}
	}
	var fcpPicture []seg
	for _, s := range layoutTrack(tl.Video[0], tl.FPS, &notes, true) {
		if s.kind == segItem {
			fcpPicture = append(fcpPicture, seg{Kind: "clip", Name: s.item.Name, Start: s.start, Duration: s.duration, Source: s.source, Speed: s.item.Speed})
		}
	}
	// Clip sounds that play another file than their picture (clearer voices)
	// are connected clips in FCPXML.
	picture := map[string]string{}
	for _, it := range tl.Video[0].Items {
		if it.Media != nil {
			picture[it.Link] = it.Media.ID
		}
	}
	detached := 0
	for _, t := range tl.Audio {
		for _, it := range t.Items {
			if id, ok := picture[it.Link]; ok && t.Role == RoleClipSound && it.Media != nil && it.Media.ID != id {
				detached++
			}
		}
	}
	return map[string]any{"fps": tl.FPS, "duration": tl.Duration, "tracks": tracks, "fcpxmlPicture": fcpPicture, "fcpxmlDetached": detached, "markers": len(tl.Markers)}
}

func TestOTIOStructure(t *testing.T) {
	tl, _ := fixtureTimeline(t)
	files := writeAll(t, tl)
	var doc map[string]any
	if err := json.Unmarshal(files[FormatOTIO], &doc); err != nil {
		t.Fatal(err)
	}
	if doc["OTIO_SCHEMA"] != "Timeline.1" {
		t.Fatalf("schema %v", doc["OTIO_SCHEMA"])
	}
	stack := doc["tracks"].(map[string]any)
	tracks := stack["children"].([]any)
	if len(tracks) < 4 || len(stack["markers"].([]any)) != 4 {
		t.Fatalf("%d tracks, %d markers", len(tracks), len(stack["markers"].([]any)))
	}
	v1 := tracks[0].(map[string]any)
	frames := 0.0
	clips := 0
	for _, c := range v1["children"].([]any) {
		m := c.(map[string]any)
		switch m["OTIO_SCHEMA"] {
		case "Clip.1":
			clips++
			d := m["source_range"].(map[string]any)["duration"].(map[string]any)
			frames += d["value"].(float64)
			ref := m["media_reference"].(map[string]any)
			if !strings.HasPrefix(ref["target_url"].(string), "file:///footage/") || ref["OTIO_SCHEMA"] != "ExternalReference.1" {
				t.Fatalf("media reference %v", ref)
			}
		case "Gap.1":
			frames += m["source_range"].(map[string]any)["duration"].(map[string]any)["value"].(float64)
		}
	}
	if clips != 6 || int(frames) != tl.Duration {
		t.Fatalf("%d clips covering %v of %d frames", clips, frames, tl.Duration)
	}
	if !strings.Contains(string(files[FormatOTIO]), `"LinearTimeWarp.1"`) || !strings.Contains(string(files[FormatOTIO]), `"time_scalar": 2`) {
		t.Fatal("the 2x clip should carry a LinearTimeWarp")
	}
}

// xmlElems counts elements by name and collects attributes of some.
func xmlElems(t *testing.T, b []byte) (map[string]int, map[string][]map[string]string) {
	t.Helper()
	counts := map[string]int{}
	attrs := map[string][]map[string]string{}
	dec := xml.NewDecoder(strings.NewReader(string(b)))
	dec.Strict = true
	for {
		tok, err := dec.Token()
		if err != nil {
			if err.Error() == "EOF" {
				break
			}
			t.Fatalf("xml: %v", err)
		}
		if se, ok := tok.(xml.StartElement); ok {
			counts[se.Name.Local]++
			a := map[string]string{}
			for _, x := range se.Attr {
				a[x.Name.Local] = x.Value
			}
			attrs[se.Name.Local] = append(attrs[se.Name.Local], a)
		}
	}
	return counts, attrs
}

func TestFCPXMLStructure(t *testing.T) {
	tl, _ := fixtureTimeline(t)
	b := writeAll(t, tl)[FormatFCPXML]
	if !strings.HasPrefix(string(b), "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<!DOCTYPE fcpxml>\n<fcpxml version=\"1.8\">") {
		t.Fatalf("header %q", string(b[:80]))
	}
	counts, attrs := xmlElems(t, b)
	if counts["asset"] != 5 || counts["transition"] != 2 || counts["marker"] != 4 {
		t.Fatalf("counts %v", counts)
	}
	split := 0
	conn := 0
	for _, a := range attrs["asset-clip"] {
		if a["audioStart"] != "" {
			split++
		}
		if a["lane"] != "" {
			conn++
		}
	}
	if split < 2 || conn != 2 {
		t.Fatalf("asset-clips with split sound %d, connected %d: %v", split, conn, attrs["asset-clip"])
	}
	if counts["adjust-blend"] < 3 || counts["adjust-volume"] < 6 || counts["timeMap"] != 1 {
		t.Fatalf("opacity fades %d, volumes %d, time maps %d", counts["adjust-blend"], counts["adjust-volume"], counts["timeMap"])
	}
	for _, a := range attrs["format"] {
		if a["frameDuration"] == "1001/30000s" {
			return
		}
	}
	t.Fatal("the 29.97 fps footage needs its own format")
}

func TestXMEMLStructure(t *testing.T) {
	tl, _ := fixtureTimeline(t)
	b := writeAll(t, tl)[FormatXMEML]
	counts, _ := xmlElems(t, b)
	// 6 picture clipitems; every sound clipitem twice (a stereo pair of tracks).
	sounds := 0
	for _, tr := range tl.Audio {
		sounds += len(tr.Items)
	}
	if counts["clipitem"] != 6+2*sounds {
		t.Fatalf("%d clipitems for 6 pictures and %d sounds", counts["clipitem"], sounds)
	}
	if counts["file"] < 5 || counts["marker"] != 4 || counts["transitionitem"] < 5 {
		t.Fatalf("counts %v", counts)
	}
	s := string(b)
	for _, want := range []string{"<effectid>audiolevels</effectid>", "<alignment>start-black</alignment>", "<alignment>end-black</alignment>",
		"<effectid>Dip to Color Dissolve</effectid>", "<pathurl>file://localhost/footage/Interview%20A.mov</pathurl>", "<timebase>30</timebase>", "<ntsc>TRUE</ntsc>",
		"<effectid>timeremap</effectid>", "<linkclipref>"} {
		if !strings.Contains(s, want) {
			t.Errorf("xmeml lacks %s", want)
		}
	}
}

func TestSRT(t *testing.T) {
	tl, _ := fixtureTimeline(t)
	s := string(WriteSRT(tl))
	want := "1\n00:00:00,500 --> 00:00:02,000\nWelcome to the shop\n\n2\n00:00:06,000 --> 00:00:08,500\nWe build robots\n\n"
	if s != want {
		t.Fatalf("srt:\n%s", s)
	}
}

// A nested cut becomes a compound clip in every format.
func TestNestedCut(t *testing.T) {
	m := &Media{ID: "x", Name: "x.mov", Path: "/x.mov", Duration: 20, FPS: 30, HasVideo: true, HasAudio: true, Channels: 2, SampleRate: 48000}
	inner := &Timeline{Name: "Inner cut", FPS: 30, Width: 1920, Height: 1080, Duration: 90,
		Video: []*Track{{Name: "Picture", Kind: KindVideo, Role: RolePicture, Items: []*Item{{Name: "x", Media: m, Start: 0, Duration: 90, Source: 30, Speed: 1, Link: "clip:x"}}, Cuts: []Cut{{}}}}}
	outer := &Timeline{Name: "Outer cut", FPS: 30, Width: 1920, Height: 1080, Duration: 150, Media: []*Media{m},
		Video: []*Track{{Name: "Picture", Kind: KindVideo, Role: RolePicture, Cuts: []Cut{{}, {}}, Items: []*Item{
			{Name: "x", Media: m, Start: 0, Duration: 60, Source: 0, Speed: 1, Link: "clip:a"},
			{Name: "Inner cut", Nested: inner, Start: 60, Duration: 90, Source: 0, Speed: 1, Link: "clip:n"}}}}}
	files := writeNamed(t, outer, "nested")
	if !strings.Contains(string(files[FormatOTIO]), `"name": "Inner cut"`) || strings.Count(string(files[FormatOTIO]), `"OTIO_SCHEMA": "Stack.1"`) != 2 {
		t.Fatal("OTIO: the nested cut should be a Stack inside the track")
	}
	c, _ := xmlElems(t, files[FormatFCPXML])
	if c["ref-clip"] != 1 || c["media"] != 1 || c["sequence"] != 2 {
		t.Fatalf("FCPXML: %v", c)
	}
	c, _ = xmlElems(t, files[FormatXMEML])
	if c["sequence"] != 2 {
		t.Fatalf("xmeml: %v", c)
	}
}

func TestTimeFormats(t *testing.T) {
	if got := fcpTime(95, 30); got != "19/6s" {
		t.Fatal(got)
	}
	if got := fcpTime(90, 30); got != "3s" {
		t.Fatal(got)
	}
	s, d := onGrid(10, gridFor(30000.0/1001)) // 300 frames of 1001/30000 s = 10.01 s
	if s != "1001/100s" || math.Abs(d-0.01) > 1e-9 {
		t.Fatalf("on grid %s %v", s, d)
	}
	if b, n := xmTimebase(29.97); b != 30 || n != "TRUE" {
		t.Fatal(b, n)
	}
	if PathURL("/a b/c.mov") != "file://localhost/a%20b/c.mov" || FileURL("/a b/c.mov") != "file:///a%20b/c.mov" {
		t.Fatal(PathURL("/a b/c.mov"), FileURL("/a b/c.mov"))
	}
	if Timecode(3725*30+7, 30) != "01:02:05:07" {
		t.Fatal(Timecode(3725*30+7, 30))
	}
}

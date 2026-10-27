package cutsheet

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/editspec/specgen"
)

const (
	testProject = "prj_01jabcdefghjkmnpqrstvwxyz0"
	testBase    = "rev_01jabcdefghjkmnpqrstvwxyz0"
)

// Every spec fixture in testdata/ survives Print → Parse unchanged, with and
// without footage info.
func TestRoundTripFixtures(t *testing.T) {
	files, _ := filepath.Glob(filepath.Join("testdata", "*.spec.json"))
	if len(files) < 3 {
		t.Fatalf("expected spec fixtures in testdata, found %d", len(files))
	}
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		var s editspec.Spec
		dec := json.NewDecoder(strings.NewReader(string(raw)))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&s); err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		name := filepath.Base(f)
		mustRoundTrip(t, name, &s, Options{ProjectID: testProject, BaseRevisionID: testBase, BaseNote: "r3 · 18 Oct 2026"})
		text := mustRoundTrip(t, name+" (with info)", &s, Options{ProjectID: testProject, BaseRevisionID: testBase, Info: testInfo()})
		golden := strings.TrimSuffix(f, ".spec.json") + ".cut.md"
		if os.Getenv("UPDATE_GOLDEN") == "1" {
			_ = os.WriteFile(golden, []byte(text), 0o644)
		}
		if want, err := os.ReadFile(golden); err == nil && string(want) != text {
			t.Errorf("%s: printed sheet differs from %s (UPDATE_GOLDEN=1 to accept):\n%s", name, golden, text)
		}
	}
}

// Random valid specs (odd floats, unicode, quotes, Markdown-looking text…)
// round-trip exactly.
func TestRoundTripRandom(t *testing.T) {
	for seed := int64(1); seed <= 1500; seed++ {
		g := specgen.New(seed)
		if seed%3 == 0 {
			g.Plain = true
		}
		g.Versions = seed%2 == 1 // script versions: the original block, fitted and parked lines (D86)
		s := g.Spec()
		o := Options{ProjectID: testProject, BaseRevisionID: testBase}
		if seed%2 == 0 {
			o.Info = testInfo()
		}
		mustRoundTrip(t, "seed "+itoa(int(seed)), s, o)
	}
}

func FuzzRoundTrip(f *testing.F) {
	for _, seed := range []int64{1, 2, 3, 42, 99, 1234, 98765} {
		f.Add(seed, false)
		f.Add(seed, true)
	}
	f.Fuzz(func(t *testing.T, seed int64, plain bool) {
		g := specgen.New(seed)
		g.Plain = plain
		mustRoundTrip(t, "fuzz", g.Spec(), Options{Info: testInfo()})
	})
}

// The parser never panics and never loses text silently on arbitrary input:
// whatever it accepts, it can print and read back to the same spec.
func FuzzParse(f *testing.F) {
	g := specgen.New(5)
	f.Add(Print(g.Spec(), Options{Info: testInfo()}))
	f.Add("---\ncut: 1\n---\n## Clips\n1. a: footage-1 0:00 → 0:01\n")
	f.Add(Print(everyFieldSpec(), Options{Info: testInfo()}))
	f.Add("---\ncut: 1\n---\n## Clips\n1. a: footage-1 0:00 → 0:01\n↳ cut · Sam's voice starts 0.4 s early with a 0.2 s blend\n2. b: footage-1 0:02 → 0:03\n" +
		"↳ slide left 0.4 s · slows down · voice carries 0.3 s\n3. c: footage-1 0:04 → 0:05\n↳ cut · sound changes in a pause within 1 s with no blend\n4. d: footage-1 0:06 → 0:07\n")
	f.Fuzz(func(t *testing.T, text string) {
		sheet, probs := Parse(text, ParseOptions{Info: testInfo()})
		if HasErrors(probs) || sheet.Spec == nil {
			return
		}
		back := Print(sheet.Spec, Options{ProjectID: sheet.ProjectID, BaseRevisionID: sheet.BaseRevisionID, Branch: sheet.Branch, Info: testInfo()})
		again, probs := Parse(back, ParseOptions{Info: testInfo()})
		if HasErrors(probs) {
			t.Fatalf("reprinted sheet does not parse:\n%s\n%s", back, problemsText(probs))
		}
		if editspec.Hash(again.Spec) != editspec.Hash(sheet.Spec) {
			t.Fatalf("reprint changed the spec:\n%s", back)
		}
	})
}

// Completeness: every exported field of the spec and its nested types, set to
// a non-zero valid value, survives the round trip — and has a registered
// cut-sheet spelling (Spellings). A new field fails this test until it gets one.
func TestRoundTripEveryField(t *testing.T) {
	seen := map[string]bool{}
	var walk func(rt reflect.Type, prefix string)
	walk = func(rt reflect.Type, prefix string) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct || rt == reflect.TypeOf(json.RawMessage{}) {
			return
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			if !f.IsExported() {
				continue
			}
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			path := prefix + "." + name
			seen[path] = true
			if _, ok := Spellings[path]; !ok {
				t.Errorf("spec field %s has no cut-sheet spelling: add one to the printer/parser and to Spellings (or list it there as \"block\")", path)
			}
			walk(f.Type, path)
		}
	}
	walk(reflect.TypeOf(editspec.Spec{}), "spec")
	for path := range Spellings {
		if !seen[path] {
			t.Errorf("Spellings lists %s, which is not a spec field anymore", path)
		}
	}
	// Set every field (one at a time and all at once) and round-trip.
	full := everyFieldSpec()
	mustRoundTrip(t, "every field", full, Options{ProjectID: testProject, BaseRevisionID: testBase, Info: testInfo()})
	mustRoundTrip(t, "every field, no info", full, Options{})
	for _, path := range sortedKeys(Spellings) {
		s := minimalSpec()
		if setField(s, full, path) {
			mustRoundTrip(t, "only "+path, s, Options{Info: testInfo()})
		}
	}
}

// Any field can also be written in an item's {…} block under its JSON name, and
// means the same as the friendly spelling.
func TestBlockSpellingIsEquivalent(t *testing.T) {
	full := everyFieldSpec()
	text := Print(full, Options{})
	for _, pair := range [][2]string{
		{` · label "The question"`, ` · {label: "The question"}`},
		{` · speed 1.5×`, ` · {speed: 1.5}`},
		{` · volume 80%`, ` · {"volume": 0.8}`},
		{` · fades out 0.75 s`, ` · {fadeOut: 0.75}`},
		{` · volume -14 dB`, ` · {gainDb: -14}`},
	} {
		if !strings.Contains(text, pair[0]) {
			t.Fatalf("expected %q in:\n%s", pair[0], text)
		}
		alt := strings.Replace(text, pair[0], pair[1], 1)
		sheet, probs := Parse(alt, ParseOptions{})
		if HasErrors(probs) {
			t.Fatalf("%s: %s", pair[1], problemsText(probs))
		}
		if editspec.Hash(sheet.Spec) != editspec.Hash(full) {
			t.Fatalf("%s does not mean the same as %s", pair[1], pair[0])
		}
	}
	// A field given both ways is an error, not a silent pick.
	both := strings.Replace(text, ` · label "The question"`, ` · label "The question" · {label: "Other"}`, 1)
	if _, probs := Parse(both, ParseOptions{}); !hasCode(probs, "duplicate") {
		t.Fatalf("expected a duplicate error, got %s", problemsText(probs))
	}
}

func hasCode(ps []Problem, code string) bool {
	for _, p := range ps {
		if p.Code == code {
			return true
		}
	}
	return false
}

func minimalSpec() *editspec.Spec {
	s := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Preset: editspec.PresetVertical}, Clips: []editspec.Clip{{ID: "a", AssetID: specgen.Assets[0], In: 1, Out: 2}}}
	editspec.Normalize(s)
	return s
}

func f64(v float64) *float64 { return &v }

// everyFieldSpec sets every field of every type to a non-zero valid value.
func everyFieldSpec() *editspec.Spec {
	yes, no := true, false
	ev := []editspec.Evidence{{Kind: "transcript", AssetID: specgen.Assets[0], From: f64(1.25), To: f64(3.5), Ref: "seg_1", Quote: "We built it twice.", Note: "the hook"}}
	s := &editspec.Spec{
		SchemaVersion: 1,
		Title:         "Every field",
		Output:        editspec.Output{Preset: editspec.PresetLandscape, Width: 1280, Height: 720, FPS: 25, Background: "#101010", Platform: "youtube", Framing: editspec.FramingFaces},
		Clips: []editspec.Clip{
			{ID: "hook", AssetID: specgen.Assets[0], In: 1.2, Out: 4.8, Speed: 1.5, Volume: f64(0.8), Muted: true,
				Framing:    &editspec.Framing{Mode: "fill", Auto: editspec.FramingFaces, FocusX: f64(0.3), FocusY: f64(0.45), Zoom: 1.2},
				Transition: &editspec.Transition{Type: editspec.TransitionFade, Duration: 0.5, Easing: editspec.EaseOut, Audio: &editspec.SoundTransition{Mode: editspec.SoundLead, Duration: 0.3, Blend: f64(0.15)}},
				FadeOut:    0.75, Label: "The question",
				Rationale: "It opens on the question.", Evidence: ev, Locked: true, Notes: "Keep the breath.\n\nSecond paragraph.",
				Shots: []editspec.Shot{{At: 1.2, Member: "maya"}}, MutedMembers: []string{"maya", "sam"}},
			{ID: "broll", AssetID: specgen.Assets[1], In: 0, Out: 2.5, Transition: &editspec.Transition{Type: editspec.TransitionCrossfade, Duration: 0.4}},
			{ID: "end", AssetID: specgen.Assets[0], In: 10, Out: 13, Transition: &editspec.Transition{Type: editspec.TransitionDip, Duration: 1}},
			{ID: "react", AssetID: specgen.Assets[1], In: 3, Out: 5.5, Transition: &editspec.Transition{Type: editspec.TransitionSlide, Duration: 0.4, Direction: editspec.DirRight,
				Easing: editspec.EaseLinear, Audio: &editspec.SoundTransition{Mode: editspec.SoundCarry, Duration: 0.25}}},
			{ID: "close", AssetID: specgen.Assets[0], In: 14, Out: 16, Transition: &editspec.Transition{Type: editspec.TransitionWhip, Duration: 0.3,
				Audio: &editspec.SoundTransition{Mode: editspec.SoundAuto, Duration: 0.8, Blend: f64(0)}}},
			{ID: "talk", AssetID: editspec.ConvKey("call"), In: 2, Out: 40, MutedMembers: []string{"maya"}, Shots: []editspec.Shot{
				{At: 2, Layout: editspec.LayoutSideBySide, Members: []string{"maya", "sam"}, Rationale: "Open on both of them."},
				{At: 4.2, Member: "maya"},
				{At: 6, Member: "card", Dissolve: 0.3},
				{At: 9.5, Layout: editspec.LayoutGrid},
				{At: 12, Layout: editspec.LayoutPiP, Members: []string{"sam", "maya"}},
				{At: 15, Layout: editspec.LayoutStacked},
				{At: 20, Card: "What did you build first?", Members: []string{"sam"}, Rationale: "The question, without her voice."},
				{At: 26, Card: "Plain card"},
				{At: 30, BRoll: &editspec.ShotBRoll{AssetID: specgen.Assets[1], In: 3.5}, Rationale: "Cutaway."},
			}},
		},
		Conversations: map[string]editspec.ConversationPin{
			"call": {ID: "cnv_01jabcdefghjkmnpqrstvwxyz0", NameTags: "off", LayoutStyle: &editspec.LayoutStyle{
				PiP:        &editspec.PiPStyle{Corner: "top-left", Size: "small", Shape: "circle", Frame: "accent"},
				SideBySide: &editspec.SideBySideStyle{Gap: "wide", Frame: "text", Backdrop: "brand", Rounded: true}}, Gate: &editspec.ConversationGate{Mode: editspec.GateSpeaking, DepthDB: f64(18)}, Members: []editspec.ConversationMember{
				{ID: "maya", Name: "Maya", AssetID: specgen.Assets[0], Use: editspec.UseBoth, Start: 0, Tag: "Host", PersonID: "per_01jabcdefghjkmnpqrstvwxyz0",
					Framing: &editspec.Framing{Mode: "fill", Auto: editspec.FramingFaces, FocusX: f64(0.4), FocusY: f64(0.35), Zoom: 1.1}},
				{ID: "sam", Name: "Sam “the builder”", AssetID: specgen.Assets[1], Start: 12.345, Drift: -20.5, Crop: &editspec.Crop{Left: 0.05, Top: 0.06, Right: 0.02, Bottom: 0.08},
					Look: &editspec.Look{Exposure: 1.063, Contrast: 0.98, Saturation: 1.04, Warmth: -0.03, Tint: 0.01}, Muted: true},
				{ID: "card", Name: "Gallery view", AssetID: specgen.Assets[2], Use: editspec.UsePicture, Start: -1.5, Crop: &editspec.Crop{}},
				{ID: "voice", Name: "Phone", AssetID: specgen.Assets[3], Use: editspec.UseSound, Start: 0.2},
			}},
			"second": {ID: "cnv_01jabcdefghjkmnpqrstvwxyz1", Gate: &editspec.ConversationGate{Mode: editspec.GateOff}, NameTags: "on", Members: []editspec.ConversationMember{
				{ID: "solo", Name: "Solo", AssetID: specgen.Assets[0], Start: 3}}},
			"third": {ID: "cnv_01jabcdefghjkmnpqrstvwxyz2", Gate: &editspec.ConversationGate{Mode: editspec.GateSpeaking}, Members: []editspec.ConversationMember{
				{ID: "solo", Name: "Solo", AssetID: specgen.Assets[0], Start: 3}}},
		},
		Audio: []editspec.AudioTrack{{ID: "bed", AssetID: specgen.Assets[2], Role: editspec.RoleMusic, In: 0.5, Out: 20, At: 0.25, GainDB: f64(-14), FadeIn: 1, FadeOut: 2,
			DuckUnderDialogue: true, DuckDB: f64(-12), Rights: &editspec.Rights{License: "CC BY 4.0", Attribution: "A composer", Source: "a library"},
			Rationale: "Warmth under the talk.", Locked: true, Notes: "Swap for the final mix.", Fit: editspec.TrackFitAuto, Loop: true, Duck: &yes},
			{ID: "sting", AssetID: specgen.Assets[2], Role: editspec.RoleSFX, In: 0, Out: 1, At: 3, Fit: editspec.TrackFitManual, Duck: &no}},
		Captions: &editspec.Captions{Enabled: true, Style: editspec.CaptionEmphasis, Position: "middle", Size: editspec.CaptionLarge, MaxWordsPerLine: 4, Uppercase: true, Source: "manual",
			Segments: []editspec.CaptionSegment{{From: 0.5, To: 2, Text: "We built it twice.", Speaker: "Ada"}}, Emphasis: []string{"twice"}, ShowSpeakers: true, Locked: true},
		Overlays: []editspec.Overlay{{ID: "who", Template: editspec.TplLowerThirdClean, Role: "lower-third:guest", At: 0.5, Duration: 3, Params: json.RawMessage(`{"name":"Ada Lovelace","title":"Engineer","align":"left","logo":"logo"}`),
			Rationale: "Introduce her.", Evidence: ev, Locked: true, Notes: "Check the spelling."},
			{ID: "route", Motion: &editspec.MotionRef{Name: "route-map", Hash: strings.Repeat("3fa9c2d1", 8)}, Role: editspec.PackTitleCard, At: 6, Duration: 4, Params: json.RawMessage(`{"from":"Lisbon","to":"Porto"}`)},
			// A role the layout pack hasn't filled yet (D264).
			{ID: "chapter", Role: editspec.PackChapterCard, At: 10, Duration: 3, Params: json.RawMessage(`{"title":"Part 2"}`)},
			// Graphics that last a section or the whole video, changing while on screen (D163).
			{ID: "names", Template: editspec.TplNameBar, Span: &editspec.OverlaySpan{}, Params: json.RawMessage(`{"name":"Ada","cover":{"x":0,"y":0.9,"w":0.31,"h":0.08},"style":"band"}`),
				Changes: []editspec.OverlayChange{{Footage: specgen.Assets[0], At: 2.5, Params: json.RawMessage(`{"name":"Grace"}`)}, {Footage: specgen.Assets[0], At: 11.033, Params: json.RawMessage(`{"name":"","hide":true}`)}}},
			{ID: "section", Template: editspec.TplLogoCorner, Span: &editspec.OverlaySpan{From: "broll", To: "react"}, At: 0.5, Params: json.RawMessage(`{"logo":"logo"}`)},
			{ID: "tail", Template: editspec.TplNameBar, Span: &editspec.OverlaySpan{From: "end"}, Duration: 2, Params: json.RawMessage(`{"name":"Ada"}`)}},
		Brand: &editspec.Brand{Primary: "#F2EDE4", Accent: "#E0A458", Text: "#F7F5F0", Background: "#0B0B0E", Font: "Fraunces", FontFile: "brand-font",
			Logo: "logo", LogoDark: "logo-dark", LogoLight: "logo-light"},
		Resources: map[string]editspec.ResourcePin{
			"logo":       {ID: specgen.Resources[0], SHA256: strings.Repeat("ab", 32)},
			"brand-font": {ID: specgen.Resources[1], SHA256: strings.Repeat("cd", 32)},
			"logo-dark":  {ID: specgen.Resources[2], SHA256: strings.Repeat("ef", 32)},
			"logo-light": {ID: specgen.Resources[2], SHA256: strings.Repeat("12", 32)},
		},
		Fixed: map[string]editspec.FixedPin{specgen.Assets[0]: {SHA256: strings.Repeat("ef", 32)}},
		Notes: "An intimate opening that keeps the pause.",
		Voiceover: &editspec.Voiceover{
			Beats: []editspec.Beat{
				{ID: "intro", Text: "When I started, I had no idea.", Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "hook", Edge: "end", Offset: 0.5},
					Delivery: "warm, a little slower", Rationale: "Sets up the doubt.", Evidence: ev,
					Voice: &editspec.VoiceRef{Provider: "elevenlabs", VoiceID: "voice123", Name: "Rachel", ModelID: "eleven_multilingual_v2",
						Settings: &editspec.TTSVoiceSettings{Stability: f64(0.5), SimilarityBoost: f64(0.75), Style: f64(0.2), Speed: f64(1.05), SpeakerBoost: &yes, OutputFormat: "mp3_44100_128"}},
					Take: &editspec.TakeRef{TakeID: "tk_01jabcdefghjkmnpqrstvwxyz0", AssetID: specgen.Assets[3], In: 0.5, Out: 2.75, Speed: 1.04, Source: "tts", AIVoice: true,
						Processed: &editspec.ProcessedRef{AssetID: specgen.Assets[2], Preset: "voice-clean", Offset: 0.12}, UseProcessed: true},
					GainDB: f64(-2), State: "generated", Locked: true},
				{ID: "laugh", Text: "She laughs.", Anchor: editspec.Anchor{Kind: editspec.AnchorMoment, AssetID: specgen.Assets[0], At: f64(12.3), ClipID: "hook", Quote: "right after she laughs", Ref: "seg_7", Offset: -0.25},
					Voice: &editspec.VoiceRef{Provider: "local", Name: "Samantha"}},
				{ID: "next", Text: "Then a question.", Anchor: editspec.Anchor{Kind: editspec.AnchorAfter, Gap: f64(0.8)}},
			},
			Settings: &editspec.VoiceoverSettings{SpeakingRateWPM: 162.5, RateSource: "measured", FitPolicy: "ask", Gap: 0.3},
			Layout:   editspec.LayoutContinuous,
			Continuous: &editspec.ContinuousVO{AssetID: specgen.Assets[3], In: 0, Out: 42.5, Anchor: editspec.Anchor{Kind: editspec.AnchorTime, At: f64(0.5)}, GainDB: f64(-1),
				Source: "recorded", AIVoice: true, Processed: &editspec.ProcessedRef{AssetID: specgen.Assets[2], Preset: "voice-clean"}, UseProcessed: true},
			Locked: true,
			// The script exactly as given: blank lines, indentation, a fence of its
			// own, markup and a trailing newline all come back unchanged.
			Original: "[@hook] (warm) When I started, I had no idea.\n\n  She laughs.   \n```\nnot a fence for us\n```\n\tThen a question.\n",
			Fitted: &editspec.FittedScript{Note: "Tied each line to its shot; trimmed line 1.", Lines: []editspec.FittedLine{
				{ID: "intro", Original: "When I started, I had no idea.", Text: "When I started, I had no idea at all.", Reason: "Fills the shot.",
					Anchor: &editspec.Anchor{Kind: editspec.AnchorMoment, AssetID: specgen.Assets[0], At: f64(12.3), ClipID: "hook", Quote: "she smiles", Ref: "seg_2", Offset: 0.4, Edge: "end", Gap: f64(0.2)}},
				{ID: "laugh", Original: "She laughs.", Text: "She laughs.", Kept: true},
			}},
			Active: editspec.VersionFitted,
			Parked: []editspec.Beat{
				{ID: "intro", Text: "When I started, I had no idea.", Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "hook", Edge: "end", Offset: 0.5},
					Delivery: "warm", Rationale: "The person's own line.", Evidence: ev,
					Voice: &editspec.VoiceRef{Provider: "elevenlabs", VoiceID: "voice123", Name: "Rachel", ModelID: "eleven_multilingual_v2",
						Settings: &editspec.TTSVoiceSettings{Stability: f64(0.5), SimilarityBoost: f64(0.75), Style: f64(0.2), Speed: f64(1.05), SpeakerBoost: &yes, OutputFormat: "mp3_44100_128"}},
					Take: &editspec.TakeRef{TakeID: "tk_01jabcdefghjkmnpqrstvwxyz1", AssetID: specgen.Assets[3], In: 0.5, Out: 2.75, Speed: 1.04, Source: "recorded", AIVoice: true,
						Processed: &editspec.ProcessedRef{AssetID: specgen.Assets[2], Preset: "voice-clean", Offset: 0.12}, UseProcessed: true},
					GainDB: f64(-2), State: "recorded", Locked: true},
			},
		},
		Mix: &editspec.Mix{Source: &editspec.Bus{GainDB: f64(-3), Mute: true, Solo: true}, Voiceover: &editspec.Bus{GainDB: f64(2)}, Music: &editspec.Bus{GainDB: f64(-6), Mute: true},
			SFX: &editspec.Bus{Solo: true}, MuteSource: true,
			Ducking:  &editspec.Ducking{Enabled: &yes, MusicUnderVoiceoverDB: f64(-14), SourceUnderVoiceoverDB: f64(-18), MusicUnderDialogueDB: f64(-12), Attack: f64(0.25), Release: f64(0.5)},
			Loudness: &editspec.MasterLoudness{TargetLUFS: f64(-14), TruePeakDBTP: f64(-1), Normalize: &yes}, LevelSpeech: &yes, CutBlend: f64(0.15), Locked: true,
			Voices: []editspec.VoiceSound{
				{Name: "Maya", GainDB: f64(3), Enhance: &editspec.Enhance{Mode: editspec.EnhanceClean, Strength: f64(0.8), Off: true}},
				{Name: "Speaker 2", GainDB: f64(-2.5)},
				{AssetID: specgen.Assets[1], Enhance: &editspec.Enhance{Mode: editspec.EnhanceStudio}},
				{Name: "Dr. Ruth: guest", Enhance: &editspec.Enhance{Mode: editspec.EnhanceCloud, Strength: f64(0.65)}},
			},
			Enhanced: []editspec.EnhancedSound{{AssetID: specgen.Assets[0], Start: 12, Variant: specgen.Assets[3], Settings: "Maya: clean 80%"}}},
	}
	editspec.Normalize(s)
	return s
}

// setField copies one field (by path) from full into s, creating containers.
func setField(s, full *editspec.Spec, path string) bool {
	parts := strings.Split(path, ".")[1:]
	switch parts[0] {
	case "clips":
		if len(parts) == 1 {
			s.Clips = full.Clips
			return true
		}
		return copyJSONField(&s.Clips[0], full.Clips[0], parts[1:])
	case "audio":
		s.Audio = full.Audio
		return true
	case "overlays":
		s.Overlays = full.Overlays
		return true
	case "captions":
		s.Captions = full.Captions
		return true
	case "brand":
		s.Brand = full.Brand
		return true
	case "output":
		s.Output = full.Output
		return true
	case "resources":
		s.Resources = full.Resources
		return true
	default:
		return copyJSONField(s, *full, parts)
	}
}

func copyJSONField(dst any, src any, parts []string) bool {
	sb, _ := json.Marshal(src)
	var sm map[string]json.RawMessage
	_ = json.Unmarshal(sb, &sm)
	v, ok := sm[parts[0]]
	if !ok {
		return false
	}
	db, _ := json.Marshal(dst)
	var dm map[string]json.RawMessage
	_ = json.Unmarshal(db, &dm)
	dm[parts[0]] = v
	mb, _ := json.Marshal(dm)
	return json.Unmarshal(mb, dst) == nil
}

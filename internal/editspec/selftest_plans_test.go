package editspec

import (
	"bytes"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The Remotion self-test (video/render/selftest.mjs) renders real Go-resolved
// plans for the transition library, split edits and blends at cuts, so it checks
// the TypeScript side against exactly what Go plans. This test writes them to
// video/render/fixtures/transitions.json and fails when that file is stale:
//
//	go test ./internal/editspec -run TestSelftestPlans -update
const selftestPlansPath = "../../video/render/fixtures/transitions.json"

// selftestFacts: solid-color clips with their own tone (the self-test generates
// the media): red 550 Hz, blue 700 Hz, green 850 Hz, 6 s each, 1280x720.
type selftestFacts struct{}

func (selftestFacts) Asset(id string) (AssetFacts, bool) {
	switch id {
	case "red", "blue", "green":
		return AssetFacts{ID: id, Name: id + ".mp4", Kind: "video", State: "available", InProject: true, Duration: 6, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, FPS: 30}, true
	case "mover":
		return AssetFacts{ID: id, Name: "mover.mp4", Kind: "video", State: "available", InProject: true, Duration: 6, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, FPS: 30}, true
	case "voice-a", "voice-b":
		// Two people's own microphones for the "sync" variants (D119): bursts of
		// noise at times of their own, so where each plays in the render can be
		// measured to the sample.
		return AssetFacts{ID: id, Name: id + ".wav", Kind: "audio", State: "available", InProject: true, Duration: 8, HasAudio: true}, true
	}
	return AssetFacts{}, false
}

// moverX is where the self-test's mover (a red square, 120 px, sliding right at
// 160 px a second from x = 100) has its middle at t seconds (0–1 of the width).
func moverX(t float64) float64 { return (100 + 160*t + 60) / 1280 }

// Subject is the mover, seen twice a second like an index would (D115).
func (selftestFacts) Subject(id string, from, to float64) ([]SubjectSample, []float64, bool) {
	if id != "mover" {
		return nil, nil, false
	}
	var out []SubjectSample
	for t := 0.25; t < 6; t += 0.5 {
		if t >= from && t < to {
			out = append(out, SubjectSample{At: t, X: moverX(t), Y: 0.5})
		}
	}
	return out, nil, len(out) > 0
}

func (selftestFacts) Transcript(string) []TranscriptSegment { return nil }

// Footage: Rosa's webcam (red) shows the call app's printed name bottom left
// (conversation-names, D182).
func (selftestFacts) Footage(id string) (*FootageShots, bool) {
	if id != "red" {
		return nil, false
	}
	return &FootageShots{Switches: []PictureSwitch{}, Shots: []FootageShot{{From: 0, To: 10, Person: "Rosa", By: "printed", Printed: "Rosa M."}},
		Names: &PrintedNames{Corner: "bottom-left", Box: CoverBox{X: 0.03, Y: 0.82, W: 0.34, H: 0.12}, Shots: 1}}, true
}

// The self-test's resources (it writes the files): an SVG logo, a picture and
// a font, pinned by these made-up hashes.
var (
	selftestLogoSHA  = strings.Repeat("a1", 32)
	selftestPhotoSHA = strings.Repeat("b2", 32)
	selftestFontSHA  = strings.Repeat("c3", 32)
	// Slides (D108): a code picture, a diagram and a web page snapshot (its
	// desktop file plus three page pictures: desktop, phone, the whole page).
	selftestCodeSHA    = strings.Repeat("d4", 32)
	selftestDiagramSHA = strings.Repeat("e5", 32)
	selftestSiteSHA    = strings.Repeat("f6", 32)
	selftestSitePhone  = strings.Repeat("a7", 32)
	selftestSiteFull   = strings.Repeat("b8", 32)
	selftestRes        = map[string]ResourceInfo{
		"res_selftestlogozzzzzzzzzzzzzz": {ID: "res_selftestlogozzzzzzzzzzzzzz", Name: "logo", Title: "Selftest logo", Kind: ResourceKindSVG, SHA256: selftestLogoSHA, MIME: "image/svg+xml", Width: 400, Height: 160},
		"res_selftestphotoaaaaaaaaaaaaa": {ID: "res_selftestphotoaaaaaaaaaaaaa", Name: "photo", Title: "Selftest photo", Kind: ResourceKindImage, SHA256: selftestPhotoSHA, MIME: "image/png", Width: 640, Height: 360},
		"res_selftestfontzzzzzzzzzzzzzz": {ID: "res_selftestfontzzzzzzzzzzzzzz", Name: "brandfont", Title: "Selftest font", Kind: ResourceKindFont, SHA256: selftestFontSHA, MIME: "font/woff2", Family: "Fraunces"},
		"res_selftestcodeaaaaaaaaaaaaaa": {ID: "res_selftestcodeaaaaaaaaaaaaaa", Name: "main-loop", Title: "main.go, lines 12-40", Kind: ResourceKindImage, SHA256: selftestCodeSHA, MIME: "image/png", Width: 1600, Height: 1000},
		"res_selftestdiagramaaaaaaaaaaa": {ID: "res_selftestdiagramaaaaaaaaaaa", Name: "architecture", Title: "How it fits together", Kind: ResourceKindSVG, SHA256: selftestDiagramSHA, MIME: "image/svg+xml", Width: 960, Height: 540},
		"res_selftestsiteaaaaaaaaaaaaaa": {ID: "res_selftestsiteaaaaaaaaaaaaaa", Name: "homepage", Title: "Acme: build it once", Kind: ResourceKindWebsite, SHA256: selftestSiteSHA, MIME: "image/png", Width: 1440, Height: 900,
			URL: "https://acme.example/", Pages: []PlanResourcePage{{Number: 1, SHA256: selftestSiteSHA, Width: 1440, Height: 900}, {Number: 2, SHA256: selftestSitePhone, Width: 390, Height: 844}, {Number: 3, SHA256: selftestSiteFull, Width: 1440, Height: 3600}}},
	}
)

func (selftestFacts) Resource(id, sha string) (ResourceInfo, bool) {
	r, ok := selftestRes[id]
	if !ok {
		return ResourceInfo{}, false
	}
	r.Known, r.Visible, r.Available, r.Latest = sha == r.SHA256, true, true, r.SHA256
	return r, true
}

// The nested self-test plays "Brand intro" (green 2 s, then blue 2 s, under a
// lower third) from 1 s in, after 2 s of red.
const (
	selftestBrandProject = "prj_selftestbrandprojectaaaaaa"
	selftestIntroRev     = "rev_selftestintrorevisionaaaaa"
)

func selftestIntro() *Spec {
	s := &Spec{Output: Output{Width: 1280, Height: 720, FPS: 30, Platform: "youtube"}, Clips: []Clip{
		{ID: "g", AssetID: "green", In: 0.5, Out: 2.5},
		{ID: "b", AssetID: "blue", In: 0.5, Out: 2.5},
	}, Overlays: []Overlay{
		{ID: "who", Template: TplLowerThirdClean, At: 0.3, Duration: 3.4, Params: json.RawMessage(`{"name":"Inner cut","title":"Played inside another cut"}`)},
	}}
	Normalize(s)
	return s
}

func (selftestFacts) Nested(projectID, revisionID string) (*NestedSource, bool) {
	if projectID != selftestBrandProject || revisionID != selftestIntroRev {
		return &NestedSource{ProjectID: projectID, RevisionID: revisionID, Missing: true}, true
	}
	return &NestedSource{ProjectID: projectID, RevisionID: revisionID, ProjectName: "Brand", Title: "Brand intro", Number: 4,
		Spec: selftestIntro(), Facts: selftestFacts{}, Visible: true, LatestID: revisionID, LatestNumber: 4}, true
}

func selftestSpecs() map[string]*Spec {
	out := map[string]*Spec{}
	out16 := Output{Width: 1280, Height: 720, FPS: 30, Platform: "youtube"}
	clip := func(id, asset string, in, outT float64, tr *Transition) Clip {
		return Clip{ID: id, AssetID: asset, In: in, Out: outT, Transition: tr}
	}
	// Resources (D79–D83): a title card with the logo, the logo in the
	// top-right corner, an end card over a picture, and a brand font file.
	out["resources"] = &Spec{Output: out16, Clips: []Clip{clip("a", "blue", 0.5, 5.5, nil)},
		Resources: map[string]ResourcePin{
			"logo":      {ID: "res_selftestlogozzzzzzzzzzzzzz", SHA256: selftestLogoSHA},
			"photo":     {ID: "res_selftestphotoaaaaaaaaaaaaa", SHA256: selftestPhotoSHA},
			"brandfont": {ID: "res_selftestfontzzzzzzzzzzzzzz", SHA256: selftestFontSHA},
		},
		Brand: &Brand{FontFile: "brandfont"},
		Overlays: []Overlay{
			{ID: "title", Template: TplTitleCard, At: 0, Duration: 2, Params: json.RawMessage(`{"title":"Resources","subtitle":"A logo, a picture and a font","logo":"logo"}`)},
			{ID: "corner", Template: TplLogoCorner, At: 2.2, Duration: 1.6, Params: json.RawMessage(`{"logo":"logo"}`)},
			{ID: "end", Template: TplEndCard, At: 4, Duration: 1, Params: json.RawMessage(`{"headline":"Thanks for watching","logo":"logo","image":"photo"}`)},
		}}
	// Slides (D108): each slide in turn over footage it hides, landscape and
	// vertical: a title, points, code, a diagram, a page on a computer, the
	// whole page scrolling, the page on a phone, and a quote.
	slideRes := map[string]ResourcePin{
		"logo":         {ID: "res_selftestlogozzzzzzzzzzzzzz", SHA256: selftestLogoSHA},
		"photo":        {ID: "res_selftestphotoaaaaaaaaaaaaa", SHA256: selftestPhotoSHA},
		"main-loop":    {ID: "res_selftestcodeaaaaaaaaaaaaaa", SHA256: selftestCodeSHA},
		"architecture": {ID: "res_selftestdiagramaaaaaaaaaaa", SHA256: selftestDiagramSHA},
		"homepage":     {ID: "res_selftestsiteaaaaaaaaaaaaaa", SHA256: selftestSiteSHA},
	}
	slides := func(out Output) *Spec {
		return &Spec{Output: out, Clips: []Clip{clip("a", "blue", 0.2, 5.8, nil), clip("b", "green", 0.2, 5.8, nil), clip("c", "red", 0.2, 5.8, nil)},
			Resources: slideRes, Brand: &Brand{Primary: "#F2EDE4", Accent: "#E0A458", Text: "#F7F5F0", Background: "#14161C", Font: "Inter"},
			Overlays: []Overlay{
				{ID: "title", Template: TplSlideTitle, At: 0, Duration: 2, Params: json.RawMessage(`{"title":"How Widget works","subtitle":"A tour of the code in four minutes","kicker":"Part 1","logo":"logo"}`)},
				{ID: "points", Template: TplSlideBullets, At: 2, Duration: 2.4, Params: json.RawMessage(`{"title":"Why it is fast","bullets":["Everything happens on your computer","Files are stored once, by their hash","Renders run in a locked-down browser"]}`)},
				{ID: "code", Template: TplSlideCode, At: 4.4, Duration: 2.2, Params: json.RawMessage(`{"title":"The main loop","code":"main-loop","caption":"Every job is leased, run and recorded"}`)},
				{ID: "arch", Template: TplSlideArchitecture, At: 6.6, Duration: 2.2, Params: json.RawMessage(`{"title":"How it fits together","diagram":"architecture"}`)},
				{ID: "site", Template: TplSlideScreenshot, At: 8.8, Duration: 2, Params: json.RawMessage(`{"title":"The homepage","image":"homepage"}`)},
				{ID: "full", Template: TplSlideScreenshot, At: 10.8, Duration: 2.4, Params: json.RawMessage(`{"image":"homepage#3","caption":"The whole page"}`)},
				{ID: "phone", Template: TplSlideScreenshot, At: 13.2, Duration: 2, Params: json.RawMessage(`{"title":"On a phone","image":"homepage#2"}`)},
				{ID: "quote", Template: TplSlideQuote, At: 15.2, Duration: 1.6, Params: json.RawMessage(`{"text":"It turned a week of editing into an afternoon.","attribution":"Ada Park","role":"Producer","image":"photo"}`)},
			}}
	}
	out["slides"] = slides(out16)
	out["slides-vertical"] = slides(Output{Width: 720, Height: 1280, FPS: 30, Platform: "shorts"})
	// The name bar (D162, D163): one graphic for the whole video over red, blue
	// and green, its name changing at moments in the footage — Rosa on red (its
	// own fields), Ben from blue's 0.5 s (its clip's first frame), and on green
	// its own fields again until green's 1.5 s, then Gil.
	out["name-bar"] = &Spec{Output: out16, Clips: []Clip{clip("a", "red", 0.5, 2.5, nil), clip("b", "blue", 0.5, 2.5, nil), clip("c", "green", 0.5, 2.5, nil)},
		Brand: &Brand{Primary: "#F2EDE4", Accent: "#E0A458", Text: "#F7F5F0", Background: "#14161C", Font: "Inter"},
		Overlays: []Overlay{{ID: "names", Template: TplNameBar, Span: &OverlaySpan{}, Params: json.RawMessage(`{"name":"Rosa","cover":{"x":0,"y":0.86,"w":0.3,"h":0.14}}`),
			Changes: []OverlayChange{
				{Footage: "blue", At: 0.5, Params: json.RawMessage(`{"name":"Ben"}`)},
				{Footage: "green", At: 1.5, Params: json.RawMessage(`{"name":"Gil"}`)},
			}}}}
	// The house style (D198, D199): the name bar about half as tall over a
	// printed name that sits above the bottom edge (the footage shows below
	// it), never animating — each new name is all there on its first frame —
	// large captions at the top, and a title card on the brand's dark
	// background showing the logo made for dark backgrounds (the photo here,
	// so it can't be mistaken for the main logo).
	out["house-style"] = &Spec{Output: out16, Clips: []Clip{clip("a", "red", 0.5, 2.5, nil), clip("b", "blue", 0.5, 2.5, nil), clip("c", "green", 0.5, 2.5, nil)},
		Brand: &Brand{Primary: "#F2EDE4", Accent: "#E0A458", Text: "#F7F5F0", Background: "#14161C", Font: "Inter", Logo: "logo", LogoDark: "logo-dark"},
		Resources: map[string]ResourcePin{
			"logo":      {ID: "res_selftestlogozzzzzzzzzzzzzz", SHA256: selftestLogoSHA},
			"logo-dark": {ID: "res_selftestphotoaaaaaaaaaaaaa", SHA256: selftestPhotoSHA},
		},
		Captions: &Captions{Enabled: true, Source: "manual", Position: "top", Size: CaptionLarge, Segments: []CaptionSegment{{From: 0.2, To: 5.8, Text: "Large captions"}}},
		Overlays: []Overlay{
			{ID: "names", Template: TplNameBar, Span: &OverlaySpan{}, Params: json.RawMessage(`{"name":"Rosa","cover":{"x":0,"y":0.8,"w":0.3,"h":0.08},"height":"half","animate":"never"}`),
				Changes: []OverlayChange{
					{Footage: "blue", At: 0.5, Params: json.RawMessage(`{"name":"Ben"}`)},
					{Footage: "green", At: 1.5, Params: json.RawMessage(`{"name":"Gil"}`)},
				}},
			{ID: "title", Template: TplTitleCard, At: 0.2, Duration: 1.5, Params: json.RawMessage(`{"title":"House style","logo":"logo"}`)},
		}}
	// A nested cut (D84): 2 s of red, then Brand intro from 1 s in for 3 s.
	out["nested"] = &Spec{Output: out16, Clips: []Clip{
		clip("a", "red", 1, 3, nil),
		clip("intro", NestedKey(selftestBrandProject, selftestIntroRev), 1, 4, nil),
	}}
	// A conversation (D99): Rosa (red, 550 Hz), Ben (blue, 700 Hz; his
	// recording starts 0.5 s in) and Gil (green, 850 Hz, brightened). The
	// picture switches — Rosa, Ben, side by side, picture in picture, the grid
	// (dissolving in), a card — while all three tones play throughout.
	out["conversation"] = &Spec{Output: out16, Conversations: map[string]ConversationPin{"call": {ID: "cnv_selftestconversationaaaaaa", Members: []ConversationMember{
		{ID: "rosa", Name: "Rosa", AssetID: "red", Tag: "Host"},
		{ID: "ben", Name: "Ben", AssetID: "blue", Start: 0.5},
		{ID: "gil", Name: "Gil", AssetID: "green", Look: &Look{Exposure: 1.25}},
	}}}, Clips: []Clip{{ID: "talk", AssetID: ConvKey("call"), In: 0.5, Out: 6, Shots: []Shot{
		{At: 0.5, Member: "rosa"},
		{At: 1.5, Member: "ben"},
		{At: 2.5, Layout: LayoutSideBySide, Members: []string{"rosa", "ben"}},
		{At: 3.5, Layout: LayoutPiP, Members: []string{"ben", "rosa"}},
		{At: 4.5, Layout: LayoutGrid, Dissolve: 0.2},
		{At: 5.3, Card: "Why tomatoes?", Members: []string{"ben"}},
	}}}}
	// Covering printed names in a layout (D182): Rosa's webcam shows a printed
	// name; with the cut's name bar, her tile's own tag covers it, opaque.
	out["conversation-names"] = &Spec{Output: out16, Conversations: map[string]ConversationPin{"call": {ID: "cnv_selftestconversationnamesa", Members: []ConversationMember{
		{ID: "rosa", Name: "Rosa Moreno", AssetID: "red"},
		{ID: "ben", Name: "Ben", AssetID: "blue", Start: 0.5},
	}}}, Clips: []Clip{{ID: "talk", AssetID: ConvKey("call"), In: 0.5, Out: 2.5, Shots: []Shot{{At: 0.5, Layout: LayoutSideBySide, Members: []string{"rosa", "ben"}}}}},
		Overlays: []Overlay{{ID: "names", Template: TplNameBar, Span: &OverlaySpan{}, Params: json.RawMessage(`{"hide":true}`)}}}
	// A layout pack's looks (D263–D265): side by side with a wide gap, the
	// brand's (magenta) background between the pictures and a line in its
	// (yellow) accent; then picture in picture, Rosa large in a round inset
	// bottom left with her name tag inside the circle; then the end.screen
	// template over green footage with room for the platform's elements (outlined).
	out["layout-pack"] = &Spec{Output: out16, Brand: &Brand{Background: "#E0218A", Accent: "#FFD400"},
		Conversations: map[string]ConversationPin{"call": {ID: "cnv_selftestlayoutpackaaaaaaaa", Members: []ConversationMember{
			{ID: "rosa", Name: "Rosa", AssetID: "red"},
			{ID: "ben", Name: "Ben", AssetID: "blue", Start: 0.5},
		}, LayoutStyle: &LayoutStyle{SideBySide: &SideBySideStyle{Gap: "wide", Backdrop: "brand", Frame: "accent"}, PiP: &PiPStyle{Corner: "bottom-left", Size: "large", Shape: "circle", Frame: "accent"}}}},
		Clips: []Clip{
			{ID: "talk", AssetID: ConvKey("call"), In: 0.5, Out: 3.5, Shots: []Shot{
				{At: 0.5, Layout: LayoutSideBySide, Members: []string{"rosa", "ben"}},
				{At: 2, Layout: LayoutPiP, Members: []string{"ben", "rosa"}},
			}},
			clip("tail", "green", 0.5, 6, nil),
		},
		Overlays: []Overlay{{ID: "end", Template: TplEndScreen, Role: PackEndScreen, At: 3.2, Duration: 5.3, Params: json.RawMessage(`{"headline":"Watch the next one","subline":"New every week","slots":"two-videos","outline":true}`)}}}
	// Placing recordings to the sample (D119): Ada's microphone starts with the
	// call, Bo's 0.713 s in (not on a frame: 21.39 frames at 30 fps). The same
	// cut without and with sampleAligned, and with Bo's clock 208.3 ppm fast:
	// the self-test finds where each recording plays in the render.
	sync := func(aligned bool, drift float64) *Spec {
		return &Spec{Output: out16, Conversations: map[string]ConversationPin{"call": {ID: "cnv_selftestsyncaaaaaaaaaaaaaa", SampleAligned: aligned, Members: []ConversationMember{
			{ID: "ada", Name: "Ada", AssetID: "voice-a", Use: UseSound},
			{ID: "bo", Name: "Bo", AssetID: "voice-b", Use: UseSound, Start: 0.713, Drift: drift},
		}}}, Clips: []Clip{{ID: "talk", AssetID: ConvKey("call"), In: 0, Out: 6, Shots: []Shot{{At: 0, Card: "Two microphones"}}}}}
	}
	out["sync-frame"] = sync(false, 0)
	out["sync-frame-drift"] = sync(false, 208.3)
	out["sync-sample"] = sync(true, 0)
	out["sync-drift"] = sync(true, 208.3)
	out["transitions"] = &Spec{Output: out16, Clips: []Clip{
		clip("start", "red", 0.5, 2.5, &Transition{Type: TransitionFade, Duration: 0.5}),
		clip("dissolve", "blue", 0.5, 2.5, &Transition{Type: TransitionCrossfade, Duration: 0.5}),
		clip("wipe", "green", 0.5, 2.5, &Transition{Type: TransitionWipe, Duration: 0.5, Direction: DirLeft}),
		clip("slide", "red", 0.5, 2.5, &Transition{Type: TransitionSlide, Duration: 0.5, Direction: DirUp}),
		clip("push", "blue", 0.5, 2.5, &Transition{Type: TransitionPush, Duration: 0.5, Direction: DirRight}),
		clip("zoom", "green", 0.5, 2.5, &Transition{Type: TransitionZoom, Duration: 0.4}),
		clip("whip", "red", 0.5, 2.5, &Transition{Type: TransitionWhip, Duration: 0.3, Direction: DirLeft}),
		clip("white", "blue", 0.5, 2.5, &Transition{Type: TransitionDipWhite, Duration: 0.8}),
		clip("black", "green", 0.5, 2.5, &Transition{Type: TransitionDip, Duration: 0.8}),
		{ID: "end", AssetID: "red", In: 0.5, Out: 2.5, Transition: &Transition{Type: TransitionCut}, FadeOut: 1},
	}}
	// A J-cut (blue's tone leads its picture by 0.4 s) and an L-cut (blue's tone
	// carries 0.3 s over green), blended over 0.1 s.
	out["splits"] = &Spec{Output: out16, Clips: []Clip{
		clip("a", "red", 1, 3, nil),
		clip("b", "blue", 1, 3, &Transition{Type: TransitionCut, Audio: &SoundTransition{Mode: SoundLead, Duration: 0.4}}),
		clip("c", "green", 1, 3, &Transition{Type: TransitionCut, Audio: &SoundTransition{Mode: SoundCarry, Duration: 0.3}}),
	}}
	// The same straight cut without and with a blend (clicks).
	// Framing that follows movement (D115): a vertical crop of the mover follows
	// it across the picture; the same crop held in the middle loses it.
	vertical := Output{Width: 720, Height: 1280, FPS: 30, Platform: "shorts"}
	follow, center := vertical, vertical
	follow.Framing, center.Framing = FramingFollow, FramingCenter
	out["follow"] = &Spec{Output: follow, Clips: []Clip{clip("walk", "mover", 0, 6, nil)}}
	out["follow-center"] = &Spec{Output: center, Clips: []Clip{clip("walk", "mover", 0, 6, nil)}}
	hard := []Clip{clip("a", "red", 1, 3, nil), clip("b", "blue", 1, 3, nil)}
	out["cut-hard"] = &Spec{Output: out16, Clips: append([]Clip(nil), hard...)}
	blend := DefaultCutBlend
	out["cut-blend"] = &Spec{Output: out16, Clips: append([]Clip(nil), hard...), Mix: &Mix{CutBlend: &blend}}
	for _, s := range out {
		Normalize(s)
	}
	return out
}

func TestSelftestPlans(t *testing.T) {
	plans := map[string]*RenderPlan{}
	media := map[string]string{"red": "red.mp4", "blue": "blue.mp4", "green": "green.mp4", "mover": "mover.mp4", "voice-a": "voice-a.wav", "voice-b": "voice-b.wav"}
	// The drift copies a render worker makes before it renders (D119): the
	// self-test runs the same ffmpeg arguments ({in} and {out} filled in).
	type driftCopy struct {
		DriftCopy
		File string   `json:"file"`
		Args []string `json:"args"`
	}
	drift := map[string][]driftCopy{}
	for name, s := range selftestSpecs() {
		if is := Validate(s, selftestFacts{}, nil, false); HasErrors(is) {
			t.Fatalf("%s: %+v", name, is)
		}
		p := Resolve(Clone(s), selftestFacts{}, "rev_selftest_"+name, "prj_selftest", Hash(s))
		p.Media = "original"
		if strings.HasPrefix(name, "sync-") {
			// What the dashboard's preview plays (no drift copies there):
			// web/scripts/sync-preview-check.mjs measures it in a browser.
			pv := Resolve(Clone(s), selftestFacts{}, "rev_selftest_"+name, "prj_selftest", Hash(s))
			pv.Media = "original"
			plans[name+"-preview"] = pv
		}
		for _, c := range DriftCopies(p) {
			file := name + "-" + c.Key + ".wav"
			media[c.Key] = file
			drift[name] = append(drift[name], driftCopy{DriftCopy: c, File: file, Args: c.FFmpegArgs("{in}", "{out}")})
		}
		plans[name] = p
	}
	// Where the mover's middle is in the rendered frame (0–1 of the output),
	// by the crop Go planned (CropRect, the renderer's placement): the
	// render must show it there.
	expect := map[string][]map[string]float64{}
	for _, name := range []string{"follow", "follow-center"} {
		p := plans[name]
		c := p.Clips[0]
		for _, f := range []int{15, 45, 90, 135, 165} {
			left, top, w, h := CropRect(c.Framing, float64(c.SourceWidth), float64(c.SourceHeight), float64(p.Width), float64(p.Height), float64(f))
			x := (left + moverX(float64(f)/30)*w) / float64(p.Width)
			y := (top + 0.5*h) / float64(p.Height)
			half := 60.0 / 1280 * w / float64(p.Width) // half the square's width, in the output
			expect[name] = append(expect[name], map[string]float64{"frame": float64(f), "x": math.Round(x*10000) / 10000, "y": math.Round(y*10000) / 10000, "half": math.Round(half*10000) / 10000})
		}
	}
	doc := map[string]any{
		"generatedBy": "go test ./internal/editspec -run TestSelftestPlans -update (do not edit)",
		"media":       media,
		"expect":      expect,
		"driftCopies": drift,
		"resources": map[string]string{selftestLogoSHA: "logo.svg", selftestPhotoSHA: "photo.png", selftestFontSHA: "brand.woff2", selftestCodeSHA: "code.png",
			selftestDiagramSHA: "diagram.svg", selftestSiteSHA: "site-desktop.png", selftestSitePhone: "site-phone.png", selftestSiteFull: "site-full.png"},
		"plans": plans,
	}
	b, _ := json.MarshalIndent(doc, "", "  ")
	b = append(b, '\n')
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(selftestPlansPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(selftestPlansPath, b, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	cur, err := os.ReadFile(selftestPlansPath)
	if err != nil {
		t.Skipf("video package not present: %v", err)
	}
	if !bytes.Equal(cur, b) {
		t.Fatal("video/render/fixtures/transitions.json is stale: go test ./internal/editspec -run TestSelftestPlans -update")
	}
}

package fixes

import (
	"errors"
	"image"
	"image/color"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/visual"
)

// Balancing colour lifts a dark picture, calms a bright one and leaves a
// balanced one's brightness alone; the levels never stretch too far.
func TestColourFilter(t *testing.T) {
	dark := &ColourFacts{Low: [3]float64{0.02, 0.03, 0.05}, High: [3]float64{0.5, 0.55, 0.65}, Mean: [3]float64{0.12, 0.13, 0.2}}
	if f := colourFilter(dark); !strings.HasPrefix(f, "colorlevels=rimin=0.020:gimin=0.030:bimin=0.050:rimax=0.700:gimax=0.700:bimax=0.700") || !strings.Contains(f, ",eq=gamma=") {
		t.Fatalf("dark: %s", f)
	}
	bright := &ColourFacts{Low: [3]float64{0.2, 0.2, 0.2}, High: [3]float64{1, 1, 1}, Mean: [3]float64{0.9, 0.9, 0.9}}
	if f := colourFilter(bright); !strings.Contains(f, "rimin=0.120") || !strings.Contains(f, ",eq=gamma=") {
		t.Fatalf("bright: %s", f)
	}
	balanced := &ColourFacts{Low: [3]float64{0.05, 0.05, 0.05}, High: [3]float64{0.95, 0.95, 0.95}, Mean: [3]float64{0.5, 0.5, 0.5}}
	if f := colourFilter(balanced); strings.Contains(f, "eq=") {
		t.Fatalf("balanced: %s", f)
	}
	narrow := &ColourFacts{Low: [3]float64{0.1, 0.1, 0.1}, High: [3]float64{0.7, 0.7, 0.2}, Mean: [3]float64{0.4, 0.4, 0.15}}
	if f := colourFilter(narrow); !strings.Contains(f, "bimax=0.700") {
		t.Fatalf("a narrow channel keeps a usable range: %s", f)
	}
}

func TestSpeakerGains(t *testing.T) {
	odd := speakerGains(&SoundFacts{Speakers: map[string]float64{"s0": -20, "s1": -30, "s2": -25}})
	if odd["s0"] != -5 || odd["s1"] != 5 || odd["s2"] != 0 {
		t.Fatalf("to the middle one: %v", odd)
	}
	even := speakerGains(&SoundFacts{Speakers: map[string]float64{"s0": -10, "s1": -50}})
	if even["s0"] != -12 || even["s1"] != 12 {
		t.Fatalf("at most 12 dB either way: %v", even)
	}
}

func TestSmallFixWords(t *testing.T) {
	if timesWords(1) != "once" || timesWords(2) != "twice" || timesWords(5) != "5 times" {
		t.Fatal("timesWords")
	}
	hdr := videoEncoder(&models.Asset{Media: models.MediaInfo{HDR: true, Streams: []models.Stream{{Type: "video", ColorPrim: "bt2020", ColorTRC: "smpte2084", ColorSpace: "bt2020nc"}}}})
	if hdr[1] != "libx265" || !strings.Contains(strings.Join(hdr, " "), "-color_primaries bt2020 -color_trc smpte2084 -colorspace bt2020nc") {
		t.Fatalf("HDR: %v", hdr)
	}
	if ten := videoEncoder(&models.Asset{Media: models.MediaInfo{Streams: []models.Stream{{Type: "audio"}, {Type: "video", PixFmt: "yuv420p10le"}}}}); ten[1] != "libx265" {
		t.Fatalf("10-bit: %v", ten)
	}
	if eight := videoEncoder(&models.Asset{Media: models.MediaInfo{Streams: []models.Stream{{Type: "video", PixFmt: "yuv420p"}}}}); eight[1] != "libx264" {
		t.Fatalf("8-bit: %v", eight)
	}
	for want, mean := range map[string][3]float64{"warm (orange)": {0.6, 0.5, 0.3}, "cold (blue)": {0.3, 0.4, 0.6}, "green": {0.3, 0.6, 0.4}, "magenta": {0.6, 0.3, 0.5}, "to one colour": {0.5, 0.5, 0.5}} {
		if got := castWords(&ColourFacts{Mean: mean}); got != want {
			t.Errorf("castWords(%v) = %q, want %q", mean, got, want)
		}
	}
	if nameOf(map[string]string{"speaker_00": "Ada"}, "speaker_00") != "Ada" || nameOf(nil, "speaker_01") != "Speaker 2" {
		t.Fatal("nameOf")
	}
	for _, c := range []struct {
		tr   models.SoundTrim
		want string
	}{
		{models.SoundTrim{Start: 1.2, End: 28, Duration: 30}, "Plays from its first sound (1.2 seconds in) to its last (2.0 seconds before the end)."},
		{models.SoundTrim{Start: 12, End: 30, Duration: 30}, "Plays from its first sound, 12 seconds in."},
		{models.SoundTrim{Start: 0, End: 27.5, Duration: 30}, "Stops with its last sound, 2.5 seconds before the end."},
		{models.SoundTrim{Start: 0, End: 30, Duration: 30}, "It has no silence at the ends."},
	} {
		if got := trimWords(&c.tr); got != c.want {
			t.Errorf("trimWords(%+v) = %q", c.tr, got)
		}
	}
	rev := &models.Revision{Spec: editspec.Spec{Audio: []editspec.AudioTrack{{AssetID: "ast_other", In: 0, Out: 30}, {AssetID: "ast_song", In: 1.2, Out: 28}}}}
	if endsOffer(rev, "ast_song", &models.SoundTrim{Start: 1.2, End: 28}) || !endsOffer(rev, "ast_song", &models.SoundTrim{Start: 2, End: 28}) || !endsOffer(rev, "ast_song", &models.SoundTrim{Start: 1.2, End: 20}) {
		t.Fatal("endsOffer")
	}
	for want, fs := range map[string][]string{"steadier picture": {models.FixSteady}, "fixed picture": {models.FixSteady, models.FixColour},
		"fixed sound": {models.FixHum, models.FixLoudness}, "fixed copy": {models.FixSteady, models.FixHum}} {
		if got := OfferWords(fs); got != want {
			t.Errorf("OfferWords(%v) = %q, want %q", fs, got, want)
		}
	}
	if got := Doing([]string{models.FixEnds, models.FixLarger, models.FixEdges, models.FixBackground, models.FixHum, models.FixLoudness, models.FixBlocks,
		models.FixVoices, models.FixVolumes, models.FixGrain}); !strings.HasPrefix(got, "Smoothing the blocky picture") || strings.Contains(got, "Fixing") {
		t.Fatalf("Doing: %q", got)
	}
	if Doing(nil) != "Fixing" {
		t.Fatal("Doing with nothing")
	}
	if pic, snd := affects([]string{models.FixSteady}, []string{models.FixHum}); !pic || !snd {
		t.Fatal("affects")
	}
	if pic, snd := affects([]string{models.FixHum}, []string{models.FixHum}); pic || snd {
		t.Fatal("nothing changed")
	}
	if joinAnd(nil) != "" || joinAnd([]string{"a"}) != "a" || joinAnd([]string{"a", "b", "c"}) != "a, b and c" {
		t.Fatal("joinAnd")
	}
	if firstOf(nil) != "" || firstOf([]string{"x", "y"}) != "x" || len(orEmpty(nil)) != 0 || capFirst("") != "" || round(1.23456, 2) != 1.23 {
		t.Fatal("small helpers")
	}
	if mediaKey(nil) != "original" || mediaKey(&models.SourceCopy{SHA256: strings.Repeat("ab", 32)}) != strings.Repeat("ab", 8) {
		t.Fatal("mediaKey")
	}
	if !makesCopy(models.FixSteady) || makesCopy(models.FixEnds) || makesCopy("sparkle") {
		t.Fatal("makesCopy")
	}
	if resourceKindWords(models.ResourceSVG) != "drawing (SVG)" || resourceKindWords(models.ResourcePDF) != "PDF" || resourceKindWords(models.ResourceFont) != "font" || resourceKindWords("x") != "x" {
		t.Fatal("resourceKindWords")
	}
	if hexColour(color.NRGBA{255, 16, 1, 255}) != "#FF1001" || yesNo(true) != "yes" || yesNo(false) != "no" || firstNonEmpty(" ", "b") != "b" || firstNonEmpty() != "" {
		t.Fatal("picture helpers")
	}
}

// A failure that trying again won't fix says what happened; others say how
// to try again.
func TestFailures(t *testing.T) {
	err := Final(errors.New("the version the fixes were for isn't there any more"))
	var fe *FinalError
	if !errors.As(err, &fe) || errors.Unwrap(err) == nil || err.Error() != "the version the fixes were for isn't there any more" {
		t.Fatal("Final")
	}
	if plainFailure(err) != "The version the fixes were for isn't there any more." {
		t.Fatalf("final: %q", plainFailure(err))
	}
	if plainFailure(errors.New("exit status 1")) != "Something went wrong while making it; untick and tick the fixes to try again." {
		t.Fatal("other")
	}
}

// What a fix view says at a glance.
func TestViewSummary(t *testing.T) {
	cases := []struct {
		v    View
		want string
	}{
		{View{State: models.FixPreparing, Ticked: []string{models.FixSteady}}, "Making the fixed copy: steadying the camera."},
		{View{State: models.FixFailed, Error: "It ran out of room."}, "The fixed copy couldn't be made. It ran out of room."},
		{View{Using: "fixed", Copy: &CopyView{Words: "steadied"}}, "Uses its fixed copy: steadied."},
		{View{Fixes: []Row{{Fits: true}, {Fits: true, Ticked: true}}}, "One fix fits it."},
		{View{Fixes: []Row{{Fits: true}, {Fits: true}}}, "2 fixes fit it."},
		{View{Measuring: "MetaRush measures it soon."}, "MetaRush measures it soon."},
		{View{}, "Nothing needs fixing."},
	}
	for _, c := range cases {
		if got := summary(&c.v); got != c.want {
			t.Errorf("%q, want %q", got, c.want)
		}
	}
}

// The info card's plain lines.
func TestCardWords(t *testing.T) {
	if articled("") != "" || articled("iPhone 15") != "an iPhone 15" || articled("Canon R6") != "a Canon R6" {
		t.Fatal("articled")
	}
	for via, want := range map[string]string{"dashboard": "in the dashboard", "cli": "from a terminal", "watch": "from a watched folder", models.ViaExtension: "from the browser extension",
		"worker": "by MetaRush", models.ViaAssistant: "through Claude", models.ViaAssistantAuto: "through Claude", "mcp": ""} {
		if got := viaWords(via); got != want {
			t.Errorf("viaWords(%q) = %q", via, got)
		}
	}
	for sec, want := range map[float64]string{42.4: "42 seconds", 125: "2 min 05 s", 3725: "1 h 02 min"} {
		if got := clockWords(sec); got != want {
			t.Errorf("clockWords(%v) = %q", sec, got)
		}
	}
	if trimFloat(29.97) != "29.97" || trimFloat(30.001) != "30" {
		t.Fatal("trimFloat")
	}
	video := &models.Asset{Media: models.MediaInfo{HasVideo: true, Width: 1920, Height: 1080, FPS: 29.97, Duration: 125, HDR: true, VFR: true}}
	if got := lengthLine(video, models.SourceCamera); got != "A camera video, 2 min 05 s, 1920 × 1080 at 29.97 frames a second, HDR, its frame rate changes while it plays." {
		t.Fatalf("lengthLine: %q", got)
	}
	if got := lengthLine(&models.Asset{Media: models.MediaInfo{HasAudio: true, Duration: 9}}, models.SourceVoice); !strings.HasPrefix(got, "A ") || !strings.HasSuffix(got, ", 9 seconds.") {
		t.Fatalf("lengthLine sound: %q", got)
	}
	for ct, ok := range map[string]bool{"2026-10-20T09:15:00.000000Z": true, "2026-10-20T09:15:00Z": true, "2026-10-20 09:15:00": true, "1970-01-01T00:00:00Z": false, "": false} {
		if _, got := recorded(&models.Asset{Media: models.MediaInfo{CreationTime: ct}}); got != ok {
			t.Errorf("recorded(%q) = %v", ct, got)
		}
	}
	if clipText("short", 10) != "short" || clipText("a longer line of words", 8) != "a longe…" {
		t.Fatal("clipText")
	}
	idx := &visual.Index{Text: []visual.TextSpan{
		{Text: "SPRING  LAUNCH", From: 0, To: 8, Conf: 0.9},
		{Text: "SPRING LAUNCH", From: 20, To: 22, Conf: 0.9},
		{Text: "12:01", From: 0, To: 30, Conf: 0.9, Changing: true},
		{Text: "ok", From: 0, To: 30, Conf: 0.9},
		{Text: "blurry", From: 0, To: 30, Conf: 0.3},
		{Text: "www.example.com", From: 3, To: 9, Conf: 0.8},
		{Text: "Ada Park, director", From: 4, To: 9, Conf: 0.8},
		{Text: strings.Repeat("long caption ", 10), From: 0, To: 1, Conf: 0.8},
	}}
	got := screenWords(idx)
	if len(got) != 3 || got[0] != "SPRING LAUNCH" || got[1] != "www.example.com" || got[2] != "Ada Park, director" {
		t.Fatalf("screenWords: %q", got)
	}
}

// A picture's sharpness: an even picture has none, a pattern some.
func TestSharpness(t *testing.T) {
	flat := image.NewNRGBA(image.Rect(0, 0, 50, 50))
	pattern := image.NewNRGBA(image.Rect(0, 0, 50, 50))
	for y := 0; y < 50; y++ {
		for x := 0; x < 50; x++ {
			flat.SetNRGBA(x, y, color.NRGBA{100, 100, 100, 255})
			if (x+y)%2 == 0 {
				pattern.SetNRGBA(x, y, color.NRGBA{255, 255, 255, 255})
			} else {
				pattern.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 255})
			}
		}
	}
	if sharpness(flat) != 0 || sharpness(pattern) < 100 || sharpness(image.NewNRGBA(image.Rect(0, 0, 1, 1))) != 0 {
		t.Fatalf("sharpness %v %v", sharpness(flat), sharpness(pattern))
	}
}

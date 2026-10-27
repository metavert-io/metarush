package measures

import (
	"testing"

	"github.com/metavert-io/metarush/internal/plainwords"
)

// What people say becomes what the quality check measures (D166, D300); a
// promise MetaRush can't measure gets nothing (it stays a sign-off).
func TestFor(t *testing.T) {
	for _, c := range []struct{ said, want string }{
		{"Captions on every video", Captions},
		{"Every video has captions", Captions},
		{"Always subtitled", Captions},
		{"Burned-in captions on every video", Captions},
		{"Open captions on every video", Captions},
		{"Captions are already in the footage", "captions:burned-in"},
		{"The footage comes with its own subtitles", "captions:burned-in"},
		{"Captions at the top", CaptionsTop},
		{"Subtitles at the bottom", CaptionsBottom},
		{"Captions in yellow", ""},
		{"Under 60 seconds for shorts", "max-length:60@vertical"},
		{"Shorts under 45 s", "max-length:45@vertical"},
		{"Keep it under 2 minutes", "max-length:120"},
		{"Logo in the last 3 s", "logo-end:3"},
		{"End card at the end of every video", "logo-end:3"},
		{"Our logo in the final 5 seconds", "logo-end:5"},
		{"Logo within the first 4 seconds", "logo-start:4"},
		{"No music louder than speech", "speech-over-music:0"},
		{"Voices at least 8 dB above the music", "speech-over-music:8"},
		{"Nothing generated without disclosure", Disclosed},
		{"Label AI clips", Disclosed},
		{"No faces cut off", FacesInFrame},
		{"At least 1080p", "min-size:1080"},
		{"Deliver in 4K", "min-size:2160"},
		{"30 fps or more", "min-fps:30"},
		{"At least 25 frames a second", "min-fps:25"},
		{"First names only on screen", FirstNames},
		{"No split-second flashes", NoFlashes},
		{"Widescreen video", "shape:landscape"},
		{"Lena OKs the final cut", ""},
		{"The tone is warm", ""},
	} {
		got := For(c.said)
		if got != c.want {
			t.Errorf("%q: %q, want %q", c.said, got, c.want)
		}
		if !Valid(got) {
			t.Errorf("%q: %q isn't valid", c.said, got)
		}
		if got != "" {
			w := Words(got)
			if w == "" {
				t.Errorf("%q: no words", got)
			}
			if f := plainwords.Check(w); len(f) > 0 {
				t.Errorf("%q: %v", w, f)
			}
		}
	}
}

func TestParse(t *testing.T) {
	for _, ok := range []string{"", "captions", "captions:burned-in", "max-length:60", "max-length:59.5@square", "logo-end:3", "logo-start:1.5", "speech-over-music:0", "speech-over-music:12",
		"disclosed", "faces-in-frame", "min-size:720", "min-fps:23.976", "shape:vertical", "first-names"} {
		if !Valid(ok) {
			t.Errorf("%q refused", ok)
		}
		if m, has := Parse(ok); has && m.String() != ok {
			t.Errorf("%q writes back as %q", ok, m.String())
		}
	}
	for _, bad := range []string{"captions:3", "max-length:0", "max-length:60@round", "logo-end:", "logo-end:-1", "speech-over-music:99", "min-size:10", "min-fps:0", "shape:round", "nonsense"} {
		if Valid(bad) {
			t.Errorf("%q accepted", bad)
		}
	}
	if Words("logo-end:1") != "a logo or an end card is on screen for the last second" || Words("max-length:60@vertical") != "the video is at most a minute when it is vertical" {
		t.Errorf("words: %q / %q", Words("logo-end:1"), Words("max-length:60@vertical"))
	}
}

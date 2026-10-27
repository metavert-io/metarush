package cutsheet

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/editspec/specgen"
)

// Derived lines name the people speaking: the quote under a clip and the
// footage comment in the settings. Neither is read back.
func TestDerivedLinesNameSpeakers(t *testing.T) {
	a := specgen.Assets[0]
	info := fakeInfo{
		footage: map[string]Footage{a: {Name: "interview.mov", SHA256: strings.Repeat("ab", 32), Duration: 30, Kind: "video", Speakers: []string{"Ada", "Speaker 2"}}},
		tx: map[string][]editspec.TranscriptSegment{a: {
			{From: 0, To: 2, Speaker: "Ada", Words: words("So@0.1-0.3 what@0.35-0.6 would@0.65-0.9 you@0.95-1.1 build?@1.15-1.6")},
			{From: 2.5, To: 4, Speaker: "Speaker 2", Words: words("A@2.6-2.7 robot.@2.75-3.2")},
			{From: 5, To: 9, Speaker: "Ada", Words: words("When@5.0-5.2 I@5.25-5.3 started@5.35-5.7 building@5.75-6.1 my@6.15-6.3 first@6.35-6.6 robot@6.65-7.0 I@7.1-7.2")},
			{From: 9, To: 12, Speaker: "Speaker 2", Words: words("had@9.0-9.2 no@9.25-9.4 idea@9.45-9.8 at@9.85-9.95 all.@10.0-10.4")},
			{From: 13, To: 17, Speaker: "Ada", Words: words("We@13.0-13.2 built@13.25-13.5 it@13.55-13.6 twice@13.65-14.0 and@14.1-14.2 then@14.25-14.5 we@14.55-14.7 built@14.75-15.0 it@15.05-15.1 again@15.15-15.6 very@15.7-15.9 quickly.@15.95-16.5")},
		}},
	}
	spec := &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Title: "Who speaks", Clips: []editspec.Clip{
		{ID: "ask", AssetID: a, In: 0, Out: 4},
		{ID: "doubt", AssetID: a, In: 5, Out: 12},
		{ID: "twice", AssetID: a, In: 13, Out: 17},
	}}
	text := mustRoundTrip(t, "speakers", spec, Options{Info: info})
	for _, want := range []string{
		"# interview.mov · 0:30 · Ada, Speaker 2",
		"> Ada: “So what would you build?” Speaker 2: “A robot.”",                    // short: every change of voice
		"> Ada: “When I started building my …” … Speaker 2: “… had no idea at all.”", // long: the last voice when it differs
		"> Ada: “We built it twice and …” … “… built it again very quickly.”",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("sheet lacks %q:\n%s", want, text)
		}
	}
}

package cutsheet

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/editspec/specgen"
)

// fakeInfo is footage and transcripts for tests.
type fakeInfo struct {
	footage map[string]Footage
	tx      map[string][]editspec.TranscriptSegment
}

func (f fakeInfo) Footage(id string) (Footage, bool) { ft, ok := f.footage[id]; return ft, ok }
func (f fakeInfo) Transcript(id string) []editspec.TranscriptSegment {
	if s, ok := f.tx[id]; ok {
		return s
	}
	return nil
}

// words builds timed words from "text@from-to" pairs.
func words(spec string) []editspec.TranscriptWord {
	var out []editspec.TranscriptWord
	for _, part := range strings.Fields(spec) {
		w, times, _ := strings.Cut(part, "@")
		a, b, _ := strings.Cut(times, "-")
		fa, _ := parseNum(a)
		fb, _ := parseNum(b)
		out = append(out, editspec.TranscriptWord{Text: w, From: fa, To: fb})
	}
	return out
}

func testInfo() fakeInfo {
	a, b, c, d := specgen.Assets[0], specgen.Assets[1], specgen.Assets[2], specgen.Assets[3]
	return fakeInfo{
		footage: map[string]Footage{
			a: {Name: "interview-a.mov", SHA256: "9f2c4e1a7b3d5f60aa11bb22cc33dd44ee55ff6677889900aabbccddeeff0011", Duration: 27.31, Kind: "video"},
			b: {Name: "broll-c.mp4", SHA256: "0d1e2f3a4b5c6d7e8f90a1b2c3d4e5f60718293a4b5c6d7e8f9a0b1c2d3e4f50", Duration: 10, Kind: "video"},
			c: {Name: "music-bed.m4a", SHA256: "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff", Duration: 30, Kind: "audio"},
			d: {Name: "Interview A.mov", SHA256: "aaaa222233334444555566667777888899990000aaaabbbbccccddddeeeeffff", Duration: 60, Kind: "video"},
		},
		tx: map[string][]editspec.TranscriptSegment{
			a: {
				{From: 0.5, To: 5.2, Text: "When I started building my first robot I had no idea.", Words: words("When@0.5-0.7 I@0.72-0.8 started@0.82-1.2 building@1.22-1.7 my@1.72-1.85 first@1.87-2.2 robot@2.22-2.7 I@3.1-3.2 had@3.22-3.4 no@3.42-3.6 idea.@3.62-4.1")},
				{From: 6, To: 9, Text: "We did it very quickly, because I'm going to fail.", Words: words("We@6.0-6.2 did@6.22-6.4 it@6.42-6.5 very@6.6-6.9 quickly,@6.92-7.5 because@8.0-8.4 I'm@8.42-8.6 going@8.62-8.9 to@8.92-9.0 fail.@9.02-9.5")},
				{From: 10, To: 13, Text: "So we built it twice, so we built it very quickly.", Words: words("So@10.0-10.2 we@10.22-10.35 built@10.37-10.7 it@10.72-10.8 twice,@10.82-11.3 so@11.6-11.75 we@11.77-11.9 built@11.92-12.2 it@12.22-12.3 very@12.32-12.6 quickly.@12.62-13.1")},
				{From: 16, To: 19, Text: "What would you build if nobody was watching?"},
			},
			b: {},
		},
	}
}

func mustRoundTrip(t *testing.T, name string, s *editspec.Spec, o Options) string {
	t.Helper()
	want := editspec.Clone(s)
	editspec.Normalize(want)
	text := Print(want, o)
	sheet, probs := Parse(text, ParseOptions{Info: o.Info})
	if HasErrors(probs) {
		t.Fatalf("%s: printed sheet does not parse:\n%s\nproblems: %s", name, text, problemsText(probs))
	}
	if editspec.Hash(sheet.Spec) != editspec.Hash(want) {
		wb, _ := json.MarshalIndent(want, "", " ")
		gb, _ := json.MarshalIndent(sheet.Spec, "", " ")
		t.Fatalf("%s: round trip changed the spec\nsheet:\n%s\nwant: %s\ngot:  %s", name, text, wb, gb)
	}
	if again := Print(sheet.Spec, o); again != text {
		t.Fatalf("%s: printing is not stable:\nfirst:\n%s\nsecond:\n%s", name, text, again)
	}
	if sheet.BaseRevisionID != o.BaseRevisionID || sheet.ProjectID != o.ProjectID {
		t.Fatalf("%s: base/project lost: %q %q", name, sheet.BaseRevisionID, sheet.ProjectID)
	}
	return text
}

func problemsText(ps []Problem) string {
	var b strings.Builder
	for _, p := range ps {
		b.WriteString("\n  ")
		b.WriteString(p.Severity + " " + p.Code + " ")
		b.WriteString(strings.TrimSpace(strings.Join([]string{itoa(p.Line) + ":" + itoa(p.Col), p.Message, p.Hint}, " ")))
	}
	return b.String()
}

func itoa(n int) string { b, _ := json.Marshal(n); return string(b) }

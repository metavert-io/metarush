package cutsheet

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec/specgen"
)

// remotionInfo is footage plus the clips made of Remotion compositions.
type remotionInfo struct {
	fakeInfo
	made  map[string]RemotionResolved // "source composition version props" → clip
	asked *[]string
}

func (r remotionInfo) RemotionClip(source, composition, version, props string) RemotionResolved {
	k := strings.Join([]string{source, composition, version, props}, " ")
	if r.asked != nil {
		*r.asked = append(*r.asked, k)
	}
	if c, ok := r.made[k]; ok {
		return c
	}
	return RemotionResolved{Code: "remotion_not_made", Message: "MetaRush hasn't made it into a clip yet."}
}

func TestRemotionClipLines(t *testing.T) {
	intro, card := specgen.Assets[1], specgen.Assets[2]
	var asked []string
	info := remotionInfo{fakeInfo: testInfo(), asked: &asked, made: map[string]RemotionResolved{
		"teaser Intro  ": {AssetID: intro, Duration: 4.35, Name: "intro-v2"},
		`teaser Card 3 {"seconds":2,"title":"Hi"}`: {AssetID: card, Duration: 2, Name: "card-v3"},
	}}
	sheet := `---
cut: 1
project: ` + testProject + `
footage:
  interview-a: ` + specgen.Assets[0] + `
---
## Clips
1. hook: interview-a 0:01 → 0:04
2. intro: remotion teaser Intro
3. card: remotion teaser Card@3 0:00.5 → 0:01.5 · with title "Hi", seconds 2 · muted
`
	s, probs := Parse(sheet, ParseOptions{Info: info})
	if HasErrors(probs) {
		t.Fatal(problemsText(probs))
	}
	c := s.Spec.Clips
	if c[1].AssetID != intro || c[1].In != 0 || c[1].Out != 4.35 {
		t.Fatalf("no range: the whole composition: %+v", c[1])
	}
	if c[2].AssetID != card || c[2].In != 0.5 || c[2].Out != 1.5 || !c[2].Muted {
		t.Fatalf("a range, props and settings: %+v", c[2])
	}
	if len(asked) != 2 || asked[1] != `teaser Card 3 {"seconds":2,"title":"Hi"}` {
		t.Fatalf("props are passed in their canonical form: %q", asked)
	}
	var names []string
	for _, f := range s.Footage {
		names = append(names, f.Name)
	}
	if strings.Join(names, ",") != "interview-a,intro-v2,card-v3" {
		t.Fatalf("the clips are named in the footage list: %v", names)
	}
	// The sheet MetaRush writes uses footage names, and reads back the same.
	text := mustRoundTrip(t, "remotion clips", s.Spec, Options{ProjectID: testProject, BaseRevisionID: testBase, Info: info})
	if strings.Contains(text, "remotion teaser") {
		t.Fatalf("the canonical sheet names footage:\n%s", text)
	}

	for _, bad := range []struct{ line, code string }{
		{"2. intro: remotion teaser Outro", "remotion_not_made"},
		{"2. intro: remotion Teaser Intro", "bad_remotion"},
		{"2. intro: remotion teaser Intro@xyz", "bad_remotion"},
		{"2. intro: remotion teaser", "bad_remotion"},
	} {
		src := strings.Replace(sheet, "2. intro: remotion teaser Intro", bad.line, 1)
		if _, probs := Parse(src, ParseOptions{Info: info}); !hasCode(probs, bad.code) {
			t.Errorf("%q: expected %s, got %s", bad.line, bad.code, problemsText(probs))
		}
	}
	if _, probs := Parse(sheet, ParseOptions{Info: testInfo()}); !hasCode(probs, "remotion_unknown") {
		t.Errorf("without the projects to look in: %s", problemsText(probs))
	}
	// Footage that happens to be called "remotion" is still footage.
	plain := strings.Replace(strings.Replace(sheet, "interview-a:", "remotion:", 1), "1. hook: interview-a", "1. hook: remotion", 1)
	plain = strings.Replace(plain, "2. intro: remotion teaser Intro\n", "", 1)
	plain = strings.Replace(plain, "3. card: remotion teaser Card@3 0:00.5 → 0:01.5 · with title \"Hi\", seconds 2 · muted\n", "", 1)
	if s, probs := Parse(plain, ParseOptions{Info: info}); HasErrors(probs) || s.Spec.Clips[0].AssetID != specgen.Assets[0] {
		t.Fatalf("footage called remotion: %s", problemsText(probs))
	}
}

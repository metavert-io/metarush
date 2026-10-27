package cutsheet

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/editspec/specgen"
)

func TestFootageNamesFromTitles(t *testing.T) {
	for in, want := range map[string]string{
		"Ada's interview (take 2)": "adas-interview-take-2",
		"Été à Paris":              "ete-a-paris",
		"  Garage — wide  ":        "garage-wide",
		"2024 recap":               "2024-recap",
		"from":                     "from-footage",
		"インタビュー":                   "",
		"":                         "",
	} {
		if got := slugTitle(in); got != want {
			t.Errorf("slugTitle(%q) = %q, want %q", in, got, want)
		}
	}
	if got := slugName("Interview A (take 2).mov"); got != "interview-a-take-2" {
		t.Errorf("slugName = %q", got)
	}

	a, b, c := specgen.Assets[0], specgen.Assets[1], specgen.Assets[2]
	info := fakeInfo{footage: map[string]Footage{
		a: {Name: "IMG_0001.MOV", Title: "Ada's interview", SHA256: strings.Repeat("ab", 32), Duration: 27, Kind: "video"},
		b: {Name: "broll-c.mp4", Title: "broll-c", SHA256: strings.Repeat("cd", 32), Duration: 10, Kind: "video"},
		c: {Name: "garage.mov", Title: "インタビュー", SHA256: strings.Repeat("ef", 32), Duration: 12, Kind: "video"},
	}}
	spec := &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Title: "Titles", Clips: []editspec.Clip{
		{ID: "hook", AssetID: a, In: 1, Out: 4},
		{ID: "broll", AssetID: b, In: 0, Out: 2},
		{ID: "garage", AssetID: c, In: 0, Out: 3},
	}}
	text := mustRoundTrip(t, "titles", spec, Options{Info: info})
	for _, want := range []string{
		"adas-interview: " + a, // named after the title the person gave it
		"“Ada's interview” · IMG_0001.MOV",
		"broll-c: ",                           // a default title is the file name: shown once
		"garage: ",                            // a title with nothing usable falls back to the file name
		"“インタビュー” · garage.mov",               // but the title is still shown
		"1. hook: adas-interview 0:01 → 0:04", // the body uses the short name
	} {
		if !strings.Contains(text, want) {
			t.Errorf("sheet lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "“broll-c”") {
		t.Errorf("a default title is repeated:\n%s", text)
	}
}

package recipes

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/editspec/specgen"
	"github.com/metavert-io/metarush/internal/models"
)

// The recipe sheet (D213): printing then reading a recipe version keeps its
// fingerprint (the document's SHA-256), for the recipes MetaRush saves and for
// odd ones written by hand; printing again is stable; what it can't read is a
// problem on a line.

// sheetInfo is footage for the cut part (the cut sheet's names and hashes).
type sheetInfo map[string]cutsheet.Footage

func (s sheetInfo) Footage(id string) (cutsheet.Footage, bool)     { f, ok := s[id]; return f, ok }
func (s sheetInfo) Transcript(string) []editspec.TranscriptSegment { return nil }

func testSheetInfo() sheetInfo {
	return sheetInfo{
		specgen.Assets[0]: {Name: "interview.mp4", SHA256: strings.Repeat("9f2c", 16), Duration: 27.3, Kind: "video"},
		specgen.Assets[1]: {Name: "intro.mov", SHA256: strings.Repeat("0d1e", 16), Duration: 5, Kind: "video"},
		specgen.Assets[2]: {Name: "bed.m4a", SHA256: strings.Repeat("1111", 16), Duration: 30, Kind: "audio"},
		specgen.Assets[3]: {Name: "interview-2.mp4", SHA256: strings.Repeat("aaaa", 16), Duration: 12, Kind: "video"},
	}
}

func f64p(v float64) *float64 { return &v }
func boolp(v bool) *bool      { return &v }

// teaserDoc is a recipe like the ones saved from a project: a fixed intro, a
// chosen section of the project's footage, an end card with a picture, two
// narration lines, music and a pack of shorts.
func teaserDoc() *models.RecipeDoc {
	a := specgen.Assets
	d := &models.RecipeDoc{Format: models.RecipeFormat, Title: "App teaser", Makes: "one widescreen video, 7–11 s",
		Lands:    models.RecipeLanding{Kind: models.RecipeLandsProject, ProjectID: "prj_01jabcdefghjkmnpqrstvwxyz0"},
		Settings: "prj_01jabcdefghjkmnpqrstvwxyz0",
		Length:   &models.RecipeRange{Min: 7, Max: 11},
		Inputs: []models.RecipeInput{
			{Name: "footage", Kind: models.RecipeFootage, Label: "The footage", Default: models.RecipeValue{Items: []string{a[0], a[3]}}, Count: &models.RecipeRange{Min: 1}},
			{Name: "headline", Kind: models.RecipeWords, Label: "The headline on the end card", Default: models.RecipeValue{Text: "Read free"}, MaxLength: 40},
			{Name: "tone", Kind: models.RecipeChoice, Default: models.RecipeValue{Text: "warm"}, Choices: []string{"warm", "bright · loud"}, Required: true},
			{Name: "hero", Kind: models.RecipePictures, Label: "The pictures from “Character art”", Default: models.RecipeValue{Items: []string{specgen.Resources[0]}},
				From: &models.RecipeSource{Folder: "fld_01jabcdefghjkmnpqrstvwxyz0"}, Count: &models.RecipeRange{Min: 1, Max: 1}},
			{Name: "voice", Kind: models.RecipeVoice, Label: "The AI voice", Default: models.RecipeValue{Text: "Ada", Items: []string{"voice-ada-0001"}}},
		},
		Sections: []models.RecipeSection{
			{Name: "brand", Kind: models.RecipeFixed, Purpose: "Brand intro", Clips: []editspec.Clip{{ID: "brand", AssetID: a[1], In: 0, Out: 3, Label: "Brand intro"}}},
			{Name: "moments", Kind: models.RecipeChosen, From: "footage", Purpose: "the hook; the answer", Count: &models.RecipeRange{Min: 1, Max: 3}, Length: &models.RecipeRange{Min: 4, Max: 8},
				Clips: []editspec.Clip{{ID: "hook", AssetID: a[0], In: 1, Out: 4, Label: "the hook"}, {ID: "answer", AssetID: a[0], In: 6, Out: 9, Label: "the answer"}}},
		},
		Cut: models.RecipeCut{Title: "App teaser", Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30},
			Audio: []editspec.AudioTrack{{ID: "bed", AssetID: a[2], Role: editspec.RoleMusic, In: 0, Out: 9, GainDB: f64p(-18)}},
			Overlays: []editspec.Overlay{
				{ID: "title", Template: editspec.TplTitleCard, Span: &editspec.OverlaySpan{From: "hook", To: "hook"}, Duration: 2, Params: json.RawMessage(`{"title":"Stories for tonight"}`)},
				{ID: "end", Template: editspec.TplEndCard, Span: &editspec.OverlaySpan{From: "answer", To: "answer"}, Duration: 3, Params: json.RawMessage(`{"headline":"Read free","cta":"Start now","image":"hero"}`)}},
			Resources: map[string]editspec.ResourcePin{"hero": {ID: specgen.Resources[0], SHA256: strings.Repeat("ab", 32)}},
			Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{
				{ID: "line-1", Text: "Meet the app.", Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: "hook", Offset: 0.2},
					Voice: &editspec.VoiceRef{Provider: "elevenlabs", VoiceID: "voice-ada-0001", Name: "Ada"}},
				{ID: "line-2", Text: "Read free tonight.", Anchor: editspec.Anchor{Kind: editspec.AnchorAfter}}}},
			Captions: &editspec.Captions{Enabled: true, Position: "top"},
		},
		Bindings: []models.RecipeBinding{{Input: "headline", To: "param:end.headline"}, {Input: "hero", To: "resource:hero"}, {Input: "voice", To: "voice"}},
		Rules:    []models.RecipeRule{{Text: "Under 35 seconds", Measure: "max-length:35"}, {Text: "Captions at the top · always", Check: true}},
		Signoffs: []string{"Lena OKs the final version"},
		Hints:    []string{"Name the app in the first two seconds.", "- a hint that looks like a list"},
		From:     &models.RecipeFrom{ProjectID: "prj_01jabcdefghjkmnpqrstvwxyz0", Branch: "main", RevisionID: "rev_01jabcdefghjkmnpqrstvwxyz0", Number: 4, Approved: true},
		Pack: []models.RecipeOutput{{Name: "shorts", Kind: models.RecipeShorts, Count: &models.RecipeRange{Min: 1, Max: 3}, Length: &models.RecipeRange{Min: 15, Max: 60},
			Purpose: "a question and its answer", Shape: "vertical", Captions: boolp(true), Named: "{title} — short {k}"}},
	}
	NormalizeDoc(d)
	return d
}

// mustSheetRoundTrip prints a document, reads it back and checks the fingerprint.
func mustSheetRoundTrip(t *testing.T, name string, d *models.RecipeDoc, o SheetOptions) string {
	t.Helper()
	_, want := Canonical(d)
	text := PrintSheet(d, o)
	sheet, probs := ParseSheet(text, SheetParseOptions{Info: o.Info})
	if cutsheet.HasErrors(probs) {
		t.Fatalf("%s: the printed sheet doesn't read:\n%s\nproblems: %s", name, text, problemsWords(probs))
	}
	if _, got := Canonical(sheet.Doc); got != want {
		wb, _ := json.MarshalIndent(d, "", " ")
		gb, _ := json.MarshalIndent(sheet.Doc, "", " ")
		t.Fatalf("%s: the round trip changed the recipe\nsheet:\n%s\nwant: %s\ngot:  %s", name, text, wb, gb)
	}
	if again := PrintSheet(sheet.Doc, o); again != text {
		t.Fatalf("%s: printing isn't stable:\nfirst:\n%s\nsecond:\n%s", name, text, again)
	}
	if sheet.Name != o.Name || sheet.Base != o.Base {
		t.Fatalf("%s: name/base lost: %q %q", name, sheet.Name, sheet.Base)
	}
	return text
}

func problemsWords(ps []cutsheet.Problem) string {
	var b strings.Builder
	for _, p := range ps {
		fmt.Fprintf(&b, "\n  %s %s %d:%d %s %s", p.Severity, p.Code, p.Line, p.Col, p.Message, p.Hint)
	}
	return b.String()
}

func TestSheetRoundTripAndGolden(t *testing.T) {
	d := teaserDoc()
	titles := map[string]string{"prj_01jabcdefghjkmnpqrstvwxyz0": "App teaser", "fld_01jabcdefghjkmnpqrstvwxyz0": "Character art",
		specgen.Assets[0]: "interview.mp4", specgen.Assets[3]: "interview-2.mp4", specgen.Resources[0]: "hero"}
	o := SheetOptions{Name: "app-teaser", Base: strings.Repeat("3f9a", 16), BaseNote: "version 2, current", Info: testSheetInfo(),
		Title: func(id string) string { return titles[id] }}
	text := mustSheetRoundTrip(t, "teaser", d, o)
	mustSheetRoundTrip(t, "teaser, no info", d, SheetOptions{Name: "app-teaser"})
	golden := filepath.Join("testdata", "teaser.recipe.md")
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		_ = os.MkdirAll("testdata", 0o755)
		_ = os.WriteFile(golden, []byte(text), 0o644)
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("golden %s: %v (UPDATE_GOLDEN=1 writes it)", golden, err)
	}
	if string(want) != text {
		t.Errorf("the printed sheet differs from %s (UPDATE_GOLDEN=1 to accept):\n%s", golden, text)
	}
	// The golden file reads back as the same recipe (what people keep in git).
	sheet, probs := ParseSheet(string(want), SheetParseOptions{Info: testSheetInfo()})
	if cutsheet.HasErrors(probs) {
		t.Fatalf("golden: %s", problemsWords(probs))
	}
	if _, h := Canonical(sheet.Doc); h != func() string { _, x := Canonical(d); return x }() {
		t.Fatal("the golden sheet is another recipe")
	}
}

// Odd documents keep their fingerprint too: empty but present lists and
// ranges, unknown kinds, names that need quotes, words with quotes, · and #.
func TestSheetRoundTripOddDocuments(t *testing.T) {
	odd := teaserDoc()
	odd.Format = "metarush.recipe/2"
	odd.Title = "  A title # with “quotes” and \"more\"  "
	odd.Makes = ""
	odd.Length = &models.RecipeRange{}
	odd.Lands = models.RecipeLanding{Kind: models.RecipeLandsNewProject, Name: "{title} — {date}", ProjectID: "prj_01jabcdefghjkmnpqrstvwxyz1"}
	odd.Inputs = append(odd.Inputs,
		models.RecipeInput{Name: "Not A Name", Kind: "mystery", Default: models.RecipeValue{Text: "line one\nline two"}, From: &models.RecipeSource{}, Count: &models.RecipeRange{}},
		models.RecipeInput{Name: "count", Kind: models.RecipeWords, Label: "count", MaxLength: -1, From: &models.RecipeSource{Collection: "col_1", Project: "prj_x"}},
		models.RecipeInput{Name: "art", Kind: models.RecipePictures, From: &models.RecipeSource{Repo: "res_01jabcdefghjkmnpqrstvwxyz0", Path: "art/characters with spaces"}},
	)
	odd.Sections[0].Kind = "odd kind"
	odd.Sections = append(odd.Sections, models.RecipeSection{Name: "later", Kind: models.RecipeChosen, From: "count", Length: &models.RecipeRange{Max: 12.25}})
	odd.Bindings = append(odd.Bindings, models.RecipeBinding{Input: "Not A Name", To: "param:a graphic.title"})
	odd.Rules = append(odd.Rules, models.RecipeRule{Text: "\"quoted\" rule"})
	odd.Signoffs = append(odd.Signoffs, " padded ", "")
	odd.From = &models.RecipeFrom{}
	odd.Pack = append(odd.Pack, models.RecipeOutput{Name: "more", Kind: "teasers", Captions: boolp(false), Conversation: "a call", Style: "podcast", Count: &models.RecipeRange{Min: 2, Max: 2}})
	mustSheetRoundTrip(t, "odd", odd, SheetOptions{Name: "odd one", Info: testSheetInfo()})

	// No inputs, sections or cut at all (nil lists stay nil; empty ones stay empty).
	bare := &models.RecipeDoc{Format: models.RecipeFormat, Title: "Bare"}
	NormalizeDoc(bare)
	mustSheetRoundTrip(t, "bare", bare, SheetOptions{})
	empty := &models.RecipeDoc{Format: models.RecipeFormat, Title: "Empty", Inputs: []models.RecipeInput{}, Sections: []models.RecipeSection{}}
	NormalizeDoc(empty)
	mustSheetRoundTrip(t, "empty lists", empty, SheetOptions{})
}

// Random recipes made from random cuts (specgen: odd floats, unicode, quotes,
// text that looks like Markdown) keep their fingerprint.
func TestSheetRoundTripRandom(t *testing.T) {
	for seed := int64(1); seed <= 300; seed++ {
		g := specgen.New(seed)
		if seed%3 == 0 {
			g.Plain = true
		}
		s := g.Spec()
		d := randomDoc(rand.New(rand.NewSource(seed)), s)
		o := SheetOptions{Name: "r" + fmt.Sprint(seed)}
		if seed%2 == 0 {
			o.Info = testSheetInfo()
		}
		mustSheetRoundTrip(t, fmt.Sprintf("seed %d", seed), d, o)
	}
}

func randomDoc(r *rand.Rand, s *editspec.Spec) *models.RecipeDoc {
	words := []string{"Read free", "a · b", "quote \" inside", "#hash", "> not derived", "- not a list", "ünïcödé ✓", "tab\there", "", "x"}
	pick := func() string { return words[r.Intn(len(words))] }
	d := &models.RecipeDoc{Format: models.RecipeFormat, Title: pick(), Makes: pick()}
	if d.Title == "" {
		d.Title = "T"
	}
	if r.Intn(2) == 0 {
		d.Lands = models.RecipeLanding{Kind: models.RecipeLandsProject, ProjectID: "prj_01jabcdefghjkmnpqrstvwxyz0"}
	} else {
		d.Lands = models.RecipeLanding{Kind: models.RecipeLandsNewProject, Name: pick()}
	}
	if r.Intn(2) == 0 {
		d.Length = &models.RecipeRange{Min: float64(r.Intn(20)) + r.Float64(), Max: float64(r.Intn(90))}
	}
	kinds := []string{models.RecipeWords, models.RecipeChoice, models.RecipeFootage, models.RecipePictures, models.RecipeVoice}
	for i := 0; i < r.Intn(5); i++ {
		in := models.RecipeInput{Name: fmt.Sprintf("in-%d", i), Kind: kinds[r.Intn(len(kinds))], Label: pick(), Default: models.RecipeValue{Text: pick()}}
		if r.Intn(2) == 0 {
			in.Default.Items = []string{specgen.Assets[r.Intn(4)], "with space"}
		}
		if r.Intn(3) == 0 {
			in.Choices = []string{pick(), pick()}
		}
		if r.Intn(3) == 0 {
			in.Count = &models.RecipeRange{Min: float64(r.Intn(3)), Max: float64(r.Intn(5))}
		}
		in.Required = r.Intn(2) == 0
		d.Inputs = append(d.Inputs, in)
		if r.Intn(2) == 0 {
			d.Bindings = append(d.Bindings, models.RecipeBinding{Input: in.Name, To: fmt.Sprintf("param:g%d.title", i)})
		}
	}
	// Sections: the cut's clips in runs.
	for i := 0; i < len(s.Clips); {
		n := 1 + r.Intn(3)
		if i+n > len(s.Clips) {
			n = len(s.Clips) - i
		}
		sec := models.RecipeSection{Name: fmt.Sprintf("s-%d", i), Kind: models.RecipeFixed, Purpose: pick(), Clips: s.Clips[i : i+n]}
		if r.Intn(2) == 0 {
			sec.Kind, sec.From, sec.Count = models.RecipeChosen, "in-0", &models.RecipeRange{Min: 1, Max: float64(n + 1)}
		}
		d.Sections = append(d.Sections, sec)
		i += n
	}
	if r.Intn(3) == 0 {
		d.Sections = append(d.Sections, models.RecipeSection{Name: "empty-chosen", Kind: models.RecipeChosen, From: "in-0"})
	}
	setCut(d, s)
	for i := 0; i < r.Intn(3); i++ {
		d.Rules = append(d.Rules, models.RecipeRule{Text: pick() + "!", Measure: "max-length:35", Check: r.Intn(2) == 0})
		d.Hints = append(d.Hints, pick()+".")
		d.Signoffs = append(d.Signoffs, pick())
	}
	if r.Intn(2) == 0 {
		d.Pack = []models.RecipeOutput{{Name: "shorts", Kind: models.RecipeShorts, Count: &models.RecipeRange{Max: 3}, Purpose: pick(), Named: pick()}}
		if r.Intn(2) == 0 {
			d.Pack[0].Footage = "in-0" // shorts of a footage input (D295)
		}
	}
	NormalizeDoc(d)
	return d
}

// Every field of a recipe has a spelling in the sheet (its cut is the cut
// sheet's): a new field fails this test until it gets one.
func TestSheetSpellsEveryField(t *testing.T) {
	spelled := map[string]bool{
		"doc.format": true, "doc.title": true, "doc.makes": true, "doc.lands": true, "doc.lands.kind": true, "doc.lands.projectId": true, "doc.lands.name": true,
		"doc.settings": true, "doc.length": true, "doc.length.min": true, "doc.length.max": true,
		"doc.inputs": true, "doc.inputs.name": true, "doc.inputs.kind": true, "doc.inputs.label": true, "doc.inputs.required": true, "doc.inputs.default": true,
		"doc.inputs.default.text": true, "doc.inputs.default.items": true, "doc.inputs.from": true, "doc.inputs.from.folder": true, "doc.inputs.from.collection": true,
		"doc.inputs.from.project": true, "doc.inputs.from.repo": true, "doc.inputs.from.path": true, "doc.inputs.from.drive": true, "doc.inputs.from.dropbox": true, "doc.inputs.maxLength": true, "doc.inputs.choices": true, "doc.inputs.count": true, "doc.inputs.count.min": true, "doc.inputs.count.max": true,
		"doc.sections": true, "doc.sections.name": true, "doc.sections.kind": true, "doc.sections.purpose": true, "doc.sections.clips": true, "doc.sections.from": true,
		"doc.sections.count": true, "doc.sections.count.min": true, "doc.sections.count.max": true, "doc.sections.length": true, "doc.sections.length.min": true, "doc.sections.length.max": true,
		"doc.cut": true, "doc.bindings": true, "doc.bindings.input": true, "doc.bindings.to": true,
		"doc.rules": true, "doc.rules.text": true, "doc.rules.measure": true, "doc.rules.check": true, "doc.signoffs": true, "doc.hints": true,
		"doc.from": true, "doc.from.projectId": true, "doc.from.branch": true, "doc.from.revisionId": true, "doc.from.number": true, "doc.from.approved": true,
		"doc.pack": true, "doc.pack.name": true, "doc.pack.kind": true, "doc.pack.conversation": true, "doc.pack.count": true, "doc.pack.count.min": true, "doc.pack.count.max": true,
		"doc.pack.length": true, "doc.pack.length.min": true, "doc.pack.length.max": true, "doc.pack.purpose": true, "doc.pack.shape": true, "doc.pack.style": true,
		"doc.pack.captions": true, "doc.pack.named": true, "doc.pack.footage": true,
	}
	seen := map[string]bool{}
	var walk func(rt reflect.Type, prefix string)
	walk = func(rt reflect.Type, prefix string) {
		for rt.Kind() == reflect.Pointer || rt.Kind() == reflect.Slice || rt.Kind() == reflect.Map {
			rt = rt.Elem()
		}
		if rt.Kind() != reflect.Struct {
			return
		}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
			path := prefix + "." + name
			seen[path] = true
			if !spelled[path] {
				t.Errorf("the recipe field %s has no spelling in the recipe sheet: add one to the printer and the parser, and here", path)
			}
			if path == "doc.cut" || path == "doc.sections.clips" {
				continue // the cut sheet's
			}
			walk(f.Type, path)
		}
	}
	walk(reflect.TypeOf(models.RecipeDoc{}), "doc")
	for p := range spelled {
		if !seen[p] {
			t.Errorf("%s is listed but isn't a recipe field any more", p)
		}
	}
}

// What the sheet can't read is a problem on its line, never dropped.
func TestSheetProblems(t *testing.T) {
	d := teaserDoc()
	text := PrintSheet(d, SheetOptions{Name: "app-teaser", Info: testSheetInfo()})
	lineOf := func(text, prefix string) int {
		for i, l := range strings.Split(text, "\n") {
			if strings.HasPrefix(l, prefix) {
				return i + 1
			}
		}
		return 0
	}
	cases := []struct {
		name, from, to, code string
		line                 string // the line the problem is on starts with this
	}{
		{"unknown clause", "· label \"The headline on the end card\"", "· labelled \"x\"", "unknown_clause", "- headline:"},
		{"unknown heading", "## Hints", "## Tips", "unknown_heading", "## Tips"},
		{"unknown setting", "makes:", "mates:", "unknown_setting", "mates:"},
		{"bad range", "count 1–3 · length 4–8 s", "count one–three · length 4–8 s", "bad_value", "2. moments:"},
		{"a clip no section has", "· clips hook, answer", "· clips hook", "clip_without_section", "3. answer:"},
		{"a clip the cut doesn't have", "· clips hook, answer", "· clips hook, answer, gone", "clip_missing", "2. moments:"},
		{"out of order", "· clips hook, answer", "· clips answer, hook", "clips_out_of_order", "2. hook:"},
		{"twice", "· up to 40 characters", "· up to 40 characters · up to 30 characters", "duplicate", "- headline:"},
		{"no marker", "recipe: 1\n", "", "missing_marker", "name:"},
	}
	for _, c := range cases {
		if !strings.Contains(text, c.from) {
			t.Fatalf("%s: %q isn't in the sheet:\n%s", c.name, c.from, text)
		}
		bad := strings.Replace(text, c.from, c.to, 1)
		_, probs := ParseSheet(bad, SheetParseOptions{Info: testSheetInfo()})
		found := false
		for _, p := range probs {
			if p.Code == c.code {
				found = true
				if want := lineOf(bad, c.line); c.line != "" && want != 0 && p.Line != want && !(c.code == "missing_marker") {
					t.Errorf("%s: %s on line %d, want %d (%s)", c.name, c.code, p.Line, want, p.Message)
				}
			}
		}
		if !found {
			t.Errorf("%s: no %s problem in %s", c.name, c.code, problemsWords(probs))
		}
	}
	// A problem in the cut is on the recipe sheet's line.
	bad := strings.Replace(text, "1. brand:", "1. brand: nonsense here", 1)
	if _, probs := ParseSheet(bad, SheetParseOptions{}); !cutsheet.HasErrors(probs) {
		t.Fatal("a broken cut line is a problem")
	}
	cutBad := strings.Replace(text, "## Clips", "## Clips\n\n1. what: no-such-footage 0:00 → 0:01", 1)
	_, probs := ParseSheet(cutBad, SheetParseOptions{Info: testSheetInfo()})
	at := lineOf(cutBad, "1. what:")
	ok := false
	for _, p := range probs {
		if p.Severity == "error" && p.Line == at {
			ok = true
		}
	}
	if !ok {
		t.Fatalf("the cut's problem is on line %d of the recipe sheet: %s", at, problemsWords(probs))
	}
}

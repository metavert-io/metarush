package recipes

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// The starter recipes' source (D293, D296). Each starter is written here as a
// recipe document with its example files; TestStarterFiles keeps
// internal/recipes/starters/<name>/ in step with it — the recipe file's
// recipe.json (metarush.recipe-file/1, D219), its sheet, starter.json for
// people, and files/<sha256> — so what ships is a recipe file like any other:
//
//	METARUSH_UPDATE_STARTERS=1 go test ./internal/recipes -run TestStarterFiles
//
// writes them (the example clips are made with ffmpeg the first time and kept
// after, so a newer ffmpeg never changes them); otherwise a file out of step
// fails the test. A starter whose document changes gets a new version: its
// lineage keeps the earlier ones, so a newer MetaRush's starter comes into a
// workspace as a draft of the same recipe. Everything in them is invented: no
// real people, organisations or code.

// starterMade is when the starters' files say they were made.
var starterMade = time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)

// starterID is a starter's own ID for a thing its file holds (the import maps
// each to what it becomes in the workspace).
func starterID(prefix, name string) string {
	sum := sha256.Sum256([]byte("metarush-starter:" + name))
	return prefix + hex.EncodeToString(sum[:])[:26]
}

// starterClip is example footage a starter holds: made with ffmpeg the first time.
type starterClip struct {
	id, title, file string
	seconds         float64
	ffmpeg          []string // the source arguments (lavfi inputs)
}

// starterPicture is an example picture a starter holds (an SVG written here)
// and where it says it came from.
type starterPicture struct {
	id, name, title, file, svg string
	source                     models.ResourceSource
}

// exampleRepo is the invented repository the release starter's examples say
// they were made from, at an invented commit.
const (
	exampleRepo   = "https://github.com/acme/widget"
	exampleCommit = "1a2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d"
)

// madeFromRepo is the provenance of an example made from the invented
// repository, as `resources make` records a real one's (repo@commit:path:lines).
func madeFromRepo(kind, file string, from, to int) models.ResourceSource {
	src := models.ResourceSource{Kind: models.FromGenerated, Tool: "an example made for MetaRush's starter recipes", URL: exampleRepo, Site: "github.com",
		Commit: exampleCommit, License: "MIT", Author: "acme", MadeWith: &models.ResourceMadeWith{Kind: kind}}
	if file != "" {
		src.File, src.Lines = file, fmt.Sprintf("%d-%d", from, to)
		src.MadeWith.File, src.MadeWith.From, src.MadeWith.To, src.MadeWith.Language = file, from, to, "go"
	}
	return src
}

// starterDef is one starter: what people read, its document and its example files.
type starterDef struct {
	info     StarterInfo
	doc      *models.RecipeDoc
	clips    []starterClip
	pictures []starterPicture
}

func jsonRaw(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func f64(v float64) *float64 { return &v }

// callEpisodeStarter: a call recording becomes a cleaned-up episode, a pack of
// shorts, captions in the house style and delivery notes (D296).
func callEpisodeStarter() starterDef {
	call := starterID("ast_", "call-episode/example-call")
	doc := &models.RecipeDoc{Format: models.RecipeFormat, Title: "A call becomes an episode with shorts",
		Makes:  "one widescreen episode as long as the call needs",
		Lands:  models.RecipeLanding{Kind: models.RecipeLandsNewProject, Name: "{episode}"},
		Length: &models.RecipeRange{Min: 60, Max: 10800},
		Inputs: []models.RecipeInput{
			{Name: "call", Kind: models.RecipeFootage, Label: "The call recording", Required: true, Default: models.RecipeValue{Items: []string{call}},
				Count: &models.RecipeRange{Min: 1, Max: 1}},
			{Name: "episode", Kind: models.RecipeWords, Label: "The episode's title", Required: true, Default: models.RecipeValue{Text: "Episode 1: an example call"}, MaxLength: 80},
		},
		Sections: []models.RecipeSection{
			{Name: "episode", Kind: models.RecipeChosen, From: "call", Count: &models.RecipeRange{Min: 1, Max: 12}, Length: &models.RecipeRange{Min: 60, Max: 10800},
				Purpose: "the whole conversation in its order, cleaned up: false starts, crosstalk, long silences and stretches off the subject taken out"},
		},
		Cut: models.RecipeCut{Title: "Episode 1: an example call",
			Output:   editspec.Output{Preset: editspec.PresetLandscape, FPS: 30, Platform: "youtube"},
			Captions: &editspec.Captions{Enabled: true},
			Overlays: []editspec.Overlay{{ID: "intro", Template: editspec.TplTitleCard, Role: "title-card", At: 0, Duration: 4,
				Params: jsonRaw(map[string]string{"title": "Episode 1: an example call"})}},
			Mix: &editspec.Mix{Voices: []editspec.VoiceSound{{AssetID: call, Enhance: &editspec.Enhance{Mode: editspec.EnhanceCloud}}}},
		},
		Bindings: []models.RecipeBinding{{Input: "episode", To: models.BindTitle}, {Input: "episode", To: "param:intro.title"}},
		Pack: []models.RecipeOutput{{Name: "shorts", Kind: models.RecipeShorts, Footage: "call", Count: &models.RecipeRange{Min: 1, Max: 3},
			Length: &models.RecipeRange{Min: 20, Max: 60}, Shape: "vertical",
			Purpose: "a moment that stands on its own without the rest of the call: a clear claim, a question and its answer, a laugh",
			Named:   "{title}, short {k}"}},
		Signoffs: []string{
			"The delivery notes are written: a title, a description, chapters from 0:00 and show notes",
			"Names and spellings in the captions are checked",
		},
		Hints: []string{
			"Keep the whole conversation in its order. Take out false starts, crosstalk, long silences and stretches off the subject through the cut's sheet, then the ums and the long pauses.",
			"Write the episode's delivery notes from its transcript: a title, a description, chapters starting at 0:00 and show notes.",
			"Keep or change the shorts' first picks: each must stand on its own without the rest of the call.",
			"Voices are made clearer in the cloud; a sample of one stretch comes first, and a person approves it before the rest.",
		},
	}
	return starterDef{
		info: StarterInfo{Name: "call-episode", Title: doc.Title, Makes: doc.Makes + ", and up to 3 vertical shorts", Order: 1,
			Summary: "Give it a call recording and the episode's title: Claude cleans the conversation up in its order, MetaRush makes the voices clearer (a sample first), captions it in your house style, picks shorts that stand on their own, and the delivery notes — chapters and show notes — wait to be written before it goes out.",
			Gives:   []string{"a call recording", "the episode's title"},
			Uses: []string{"metarush/transcripts", "metarush/remove-ums", "metarush/tighten-pauses", "metarush/cloud-voices", "metarush/caption-looks",
				"metarush/best-moments", "metarush/shorts", "metarush/delivery-notes", "metarush/quality-check"}},
		doc: doc,
		clips: []starterClip{{id: call, title: "An example call (a plain picture, no sound)", file: "example-call.mp4", seconds: 8,
			ffmpeg: []string{"-f", "lavfi", "-i", "color=c=0x1f2a36:s=640x360:r=30:d=8", "-f", "lavfi", "-i", "anullsrc=r=48000:cl=mono"}}},
	}
}

// releaseVideoStarter: a repository's tag becomes code slides, a diagram, a
// narrated walkthrough (the AI voice priced first) and an end card (D296).
func releaseVideoStarter() starterDef {
	backdrop := starterID("ast_", "release-video/backdrop")
	code1, code2, diagram := starterID("res_", "release-video/code-1"), starterID("res_", "release-video/code-2"), starterID("res_", "release-video/diagram")
	clip := func(id string, secs float64) editspec.Clip {
		return editspec.Clip{ID: id, AssetID: backdrop, In: 0, Out: secs, Muted: true}
	}
	slide := func(id, template, role, from string, params map[string]string) editspec.Overlay {
		return editspec.Overlay{ID: id, Template: template, Role: role, Span: &editspec.OverlaySpan{From: from, To: from}, At: 0, Duration: 0, Params: jsonRaw(params)}
	}
	line := func(id, clipID, text string) editspec.Beat {
		return editspec.Beat{ID: id, Text: text, Anchor: editspec.Anchor{Kind: editspec.AnchorClip, ClipID: clipID, Offset: 0.6}, State: "draft"}
	}
	words := func(name, label, def string, max int) models.RecipeInput {
		return models.RecipeInput{Name: name, Kind: models.RecipeWords, Label: label, Default: models.RecipeValue{Text: def}, MaxLength: max}
	}
	doc := &models.RecipeDoc{Format: models.RecipeFormat, Title: "A release tag becomes a release video",
		Makes:  "one widescreen video, 45–90 s: the release's title, two code slides, a diagram and an end card, narrated in an AI voice",
		Lands:  models.RecipeLanding{Kind: models.RecipeLandsNewProject, Name: "{release}: release video"},
		Length: &models.RecipeRange{Min: 45, Max: 90},
		Inputs: []models.RecipeInput{
			words("release", "The release, as the title slide shows it", "Widget 2.4", 80),
			words("headline", "What the release is about, in a line", "Saves that never wait, and retries that back off", 140),
			{Name: "code", Kind: models.RecipePictures, Label: "The two code slides, made from the repository at the tag", Required: true,
				Default: models.RecipeValue{Items: []string{code1, code2}}, Count: &models.RecipeRange{Min: 2, Max: 2}},
			words("change-1", "The first change's title", "Saves take the next free worker", 80),
			words("where-1", "Where the first change is: repository@commit:file:lines", "github.com/acme/widget@1a2b3c4d:queue/worker.go:18-26", 140),
			words("change-2", "The second change's title", "Retries back off on their own", 80),
			words("where-2", "Where the second change is: repository@commit:file:lines", "github.com/acme/widget@1a2b3c4d:queue/retry.go:9-15", 140),
			{Name: "diagram", Kind: models.RecipePictures, Label: "The diagram of how the pieces fit", Required: true,
				Default: models.RecipeValue{Items: []string{diagram}}, Count: &models.RecipeRange{Min: 1, Max: 1}},
			words("diagram-title", "The diagram's title", "How a save travels", 80),
			words("line-intro", "The narration over the title", "Widget 2.4 is out. Here is what changed, and why your saves will feel faster from today, in under a minute.", 400),
			words("line-1", "The narration over the first code slide", "Saves no longer wait for the whole queue. Each job now takes the next free worker the moment it is ready, so a busy hour stays quick.", 400),
			words("line-2", "The narration over the second code slide", "Retries back off on their own. When a service is slow, Widget waits a little longer each time instead of sending a flood of requests.", 400),
			words("line-how", "The narration over the diagram", "Here is how the pieces fit together: the queue hands each job to a worker, and every result lands in one store you can trust.", 400),
			words("line-end", "The narration over the end card", "That is Widget 2.4. The release notes have everything else, with thanks to everyone who tried the early builds.", 400),
			{Name: "voice", Kind: models.RecipeVoice, Label: "The AI voice (default: the narration voice in Content Settings)"},
			words("cta", "The end card's headline", "Read the release notes", 80),
			words("link", "Where to read them", "example.com/releases", 40),
		},
		Sections: []models.RecipeSection{
			{Name: "intro", Kind: models.RecipeFixed, Purpose: "the release's title", Clips: []editspec.Clip{clip("intro", 7)}},
			{Name: "change-1", Kind: models.RecipeFixed, Purpose: "the first change, as code", Clips: []editspec.Clip{clip("change-1", 11)}},
			{Name: "change-2", Kind: models.RecipeFixed, Purpose: "the second change, as code", Clips: []editspec.Clip{clip("change-2", 11)}},
			{Name: "how", Kind: models.RecipeFixed, Purpose: "how the pieces fit", Clips: []editspec.Clip{clip("how", 10)}},
			{Name: "end", Kind: models.RecipeFixed, Purpose: "where to read more", Clips: []editspec.Clip{clip("end", 8)}},
		},
		Cut: models.RecipeCut{Title: "Widget 2.4",
			Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30, Platform: "youtube"},
			Overlays: []editspec.Overlay{
				slide("title", editspec.TplSlideTitle, "title-card", "intro", map[string]string{"title": "Widget 2.4", "subtitle": "Saves that never wait, and retries that back off", "kicker": "What's new"}),
				slide("code-1", editspec.TplSlideCode, "", "change-1", map[string]string{"title": "Saves take the next free worker", "code": "code-1", "caption": "github.com/acme/widget@1a2b3c4d:queue/worker.go:18-26"}),
				slide("code-2", editspec.TplSlideCode, "", "change-2", map[string]string{"title": "Retries back off on their own", "code": "code-2", "caption": "github.com/acme/widget@1a2b3c4d:queue/retry.go:9-15"}),
				slide("arch", editspec.TplSlideArchitecture, "", "how", map[string]string{"title": "How a save travels", "diagram": "diagram"}),
				slide("end-card", editspec.TplEndCard, "end-screen", "end", map[string]string{"headline": "Read the release notes", "cta": "Read more", "handle": "example.com/releases"}),
			},
			Resources: map[string]editspec.ResourcePin{"code-1": {ID: code1}, "code-2": {ID: code2}, "diagram": {ID: diagram}},
			Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{
				line("l-intro", "intro", "Widget 2.4 is out. Here is what changed, and why your saves will feel faster from today, in under a minute."),
				line("l-1", "change-1", "Saves no longer wait for the whole queue. Each job now takes the next free worker the moment it is ready, so a busy hour stays quick."),
				line("l-2", "change-2", "Retries back off on their own. When a service is slow, Widget waits a little longer each time instead of sending a flood of requests."),
				line("l-how", "how", "Here is how the pieces fit together: the queue hands each job to a worker, and every result lands in one store you can trust."),
				line("l-end", "end", "That is Widget 2.4. The release notes have everything else, with thanks to everyone who tried the early builds."),
			}},
		},
		Bindings: []models.RecipeBinding{
			{Input: "release", To: models.BindTitle}, {Input: "release", To: "param:title.title"}, {Input: "headline", To: "param:title.subtitle"},
			{Input: "code", To: "resource:code-1"}, {Input: "code", To: "resource:code-2"},
			{Input: "change-1", To: "param:code-1.title"}, {Input: "where-1", To: "param:code-1.caption"},
			{Input: "change-2", To: "param:code-2.title"}, {Input: "where-2", To: "param:code-2.caption"},
			{Input: "diagram", To: "resource:diagram"}, {Input: "diagram-title", To: "param:arch.title"},
			{Input: "line-intro", To: "line:l-intro"}, {Input: "line-1", To: "line:l-1"}, {Input: "line-2", To: "line:l-2"},
			{Input: "line-how", To: "line:l-how"}, {Input: "line-end", To: "line:l-end"},
			{Input: "voice", To: models.BindVoice},
			{Input: "cta", To: "param:end-card.headline"}, {Input: "link", To: "param:end-card.handle"},
		},
		Rules:    []models.RecipeRule{{Text: "Under 90 seconds", Measure: "max-length:90"}},
		Signoffs: []string{"The code on the slides is the tag's: each slide says its repository, commit, file and lines"},
		Hints: []string{
			"Fetch the repository at the release's tag, then make the two code slides from the changes that matter most and a diagram of how they fit; each keeps its repository, commit, file and lines.",
			"Write one narration line per slide, said the way a person would explain it, short enough to fit its slide.",
			"The AI voice is priced first, and a short sample line comes first; a person approves it before the rest.",
		},
	}
	sv := func(name, title, file, svg string, id string, src models.ResourceSource) starterPicture {
		return starterPicture{id: id, name: name, title: title, file: file, svg: svg, source: src}
	}
	return starterDef{
		info: StarterInfo{Name: "release-video", Title: doc.Title, Makes: doc.Makes, Order: 2,
			Summary: "Give it a repository's tag: Claude fetches the repository at the tag, makes two code slides and a diagram (each keeps its repository and commit — repo@commit:path:lines for a code slide, shown in its caption) and writes a line of narration for each slide; MetaRush reads them in an AI voice (priced first, a sample line first) over a title slide and an end card in your layout pack.",
			Gives:   []string{"a repository's tag", "two code slides and a diagram made from it", "a line of narration per slide"},
			Uses:    []string{"metarush/github-repositories", "metarush/code-slides", "metarush/diagrams", "metarush/ai-narration", "metarush/quality-check"}},
		doc: doc,
		clips: []starterClip{{id: backdrop, title: "A plain backdrop for slides", file: "backdrop.mp4", seconds: 12,
			ffmpeg: []string{"-f", "lavfi", "-i", "color=c=0x10151c:s=1280x720:r=30:d=12", "-f", "lavfi", "-i", "anullsrc=r=48000:cl=mono"}}},
		pictures: []starterPicture{
			sv("example-code-1", "An example code slide (Widget's worker loop)", "example-code-1.svg", codeSVG("queue/worker.go", 18, []string{
				"func (w *Worker) Run(ctx context.Context) error {",
				"\tfor {",
				"\t\tjob, err := w.queue.Next(ctx)",
				"\t\tif err != nil {",
				"\t\t\treturn err",
				"\t\t}",
				"\t\tgo w.handle(ctx, job)",
				"\t}",
				"}"}), code1, madeFromRepo("code", "queue/worker.go", 18, 26)),
			sv("example-code-2", "An example code slide (Widget's retries)", "example-code-2.svg", codeSVG("queue/retry.go", 9, []string{
				"func backoff(attempt int) time.Duration {",
				"\twait := time.Second << attempt",
				"\tif wait > time.Minute {",
				"\t\twait = time.Minute",
				"\t}",
				"\treturn wait",
				"}"}), code2, madeFromRepo("code", "queue/retry.go", 9, 15)),
			sv("example-diagram", "An example diagram (how a save travels)", "example-diagram.svg", diagramSVG(), diagram, madeFromRepo("diagram", "", 0, 0)),
		},
	}
}

func starterDefs() []starterDef { return []starterDef{callEpisodeStarter(), releaseVideoStarter()} }

// codeSVG draws an invented code panel with line numbers.
func codeSVG(file string, first int, lines []string) string {
	var b bytes.Buffer
	h := 70 + 34*len(lines)
	fmt.Fprintf(&b, `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="%d" viewBox="0 0 1200 %d">`, h, h)
	fmt.Fprintf(&b, `<rect x="0" y="0" width="1200" height="%d" rx="18" fill="#161b22"/>`, h)
	fmt.Fprintf(&b, `<text x="32" y="40" font-family="monospace" font-size="20" fill="#8b949e">acme/widget @ %s · %s, lines %d–%d</text>`, exampleCommit[:8], file, first, first+len(lines)-1)
	for i, l := range lines {
		y := 84 + 34*i
		fmt.Fprintf(&b, `<text x="32" y="%d" font-family="monospace" font-size="22" fill="#6e7681">%d</text>`, y, first+i)
		indent := 0
		for len(l) > 0 && l[0] == '\t' {
			indent++
			l = l[1:]
		}
		fmt.Fprintf(&b, `<text x="%d" y="%d" font-family="monospace" font-size="22" fill="#e6edf3">%s</text>`, 96+indent*32, y, xmlEscape(l))
	}
	b.WriteString(`</svg>`)
	return b.String()
}

// diagramSVG draws an invented diagram: a queue, workers and a store.
func diagramSVG() string {
	box := func(x, y int, label string) string {
		return fmt.Sprintf(`<rect x="%d" y="%d" width="240" height="100" rx="16" fill="#1f6feb"/><text x="%d" y="%d" font-family="sans-serif" font-size="28" fill="#ffffff" text-anchor="middle">%s</text>`,
			x, y, x+120, y+60, label)
	}
	arrow := func(x1, x2, y int) string {
		return fmt.Sprintf(`<line x1="%d" y1="%d" x2="%d" y2="%d" stroke="#8b949e" stroke-width="6"/><polygon points="%d,%d %d,%d %d,%d" fill="#8b949e"/>`,
			x1, y, x2-14, y, x2, y, x2-18, y-12, x2-18, y+12)
	}
	return `<svg xmlns="http://www.w3.org/2000/svg" width="1200" height="400" viewBox="0 0 1200 400">` +
		box(40, 150, "Queue") + arrow(280, 480, 200) + box(480, 150, "Workers") + arrow(720, 920, 200) + box(920, 150, "Store") + `</svg>`
}

func xmlEscape(s string) string {
	var b bytes.Buffer
	for _, r := range s {
		switch r {
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '&':
			b.WriteString("&amp;")
		case '"':
			b.WriteString("&quot;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// buildStarter writes (update) or checks one starter's files; it returns
// what is out of step.
func buildStarter(t *testing.T, def starterDef, update bool) []string {
	t.Helper()
	dir := filepath.Join("starters", def.info.Name)
	files := filepath.Join(dir, "files")
	var prev RecipeFile
	if b, err := os.ReadFile(filepath.Join(dir, recipeJSON)); err == nil {
		_ = json.Unmarshal(b, &prev)
	}
	doc := Clone(def.doc)
	f := RecipeFile{Format: RecipeFileFormat, Made: starterMade, MetaRush: "starter", Workspace: "MetaRush's starter recipes",
		Pictures: []RecipeFilePicture{}, Footage: []RecipeFileFootage{}, Code: []RecipeFileCode{}, LeftOut: []RecipeFileLeftOut{}, Files: []RecipeFileBlob{}}
	f.Recipe.Name, f.Recipe.Title, f.Recipe.Makes = def.info.Name, doc.Title, doc.Makes
	blob := func(data []byte) (string, int64) {
		sum := sha256.Sum256(data)
		sha := hex.EncodeToString(sum[:])
		if update {
			if err := os.MkdirAll(files, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(files, sha), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		f.Files = append(f.Files, RecipeFileBlob{SHA256: sha, Size: int64(len(data))})
		return sha, int64(len(data))
	}
	for _, p := range def.pictures {
		sha, size := blob([]byte(p.svg))
		src := p.source
		if src.Kind == "" {
			src = models.ResourceSource{Kind: "file"}
		}
		src.Name = p.file
		f.Pictures = append(f.Pictures, RecipeFilePicture{ID: p.id, Name: p.name, Title: p.title, Kind: editspec.ResourceKindSVG, SHA256: sha, Size: size, FileName: p.file,
			License: "Made for MetaRush's starter recipes: free to use", Source: src})
		for name, pin := range doc.Cut.Resources {
			if pin.ID == p.id {
				doc.Cut.Resources[name] = editspec.ResourcePin{ID: p.id, SHA256: sha}
			}
		}
	}
	for _, c := range def.clips {
		var data []byte
		for _, old := range prev.Footage {
			if old.ID == c.id {
				data, _ = os.ReadFile(filepath.Join(files, old.SHA256)) // kept: a newer ffmpeg never changes it
			}
		}
		if data == nil {
			if !update {
				return []string{c.file + " isn't made yet"}
			}
			data = makeStarterClip(t, c)
		}
		sha, size := blob(data)
		f.Footage = append(f.Footage, RecipeFileFootage{ID: c.id, Title: c.title, FileName: c.file, Kind: "video", SHA256: sha, Size: size, Duration: c.seconds,
			Input: inputDefault(doc, c.id)})
	}
	sort.Slice(f.Files, func(i, j int) bool { return f.Files[i].SHA256 < f.Files[j].SHA256 })
	NormalizeDoc(doc)
	f.Doc = *doc
	_, hash := Canonical(doc)
	f.Lineage = append([]string{}, prev.Lineage...)
	if len(f.Lineage) == 0 || f.Lineage[len(f.Lineage)-1] != hash {
		f.Lineage = append(f.Lineage, hash)
	}
	f.Version.Number, f.Version.Hash, f.Version.State = len(f.Lineage), hash, models.RecipeCurrent
	f.Version.Note = "MetaRush's starter recipe"
	desc, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	info, _ := json.MarshalIndent(def.info, "", "  ")
	sheet := PrintSheet(doc, SheetOptions{Name: def.info.Name, BaseNote: fmt.Sprintf("version %d, MetaRush's starter recipe", f.Version.Number)})
	want := map[string][]byte{recipeJSON: append(desc, '\n'), "starter.json": append(info, '\n'), SheetFile(def.info.Name): []byte(sheet)}
	var stale []string
	for name, b := range want {
		p := filepath.Join(dir, name)
		if update {
			if err := os.WriteFile(p, b, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if got, err := os.ReadFile(p); err != nil || !bytes.Equal(got, b) {
			stale = append(stale, filepath.Join(def.info.Name, name))
		}
	}
	if update {
		// Files nothing names any more go.
		keep := map[string]bool{}
		for _, b := range f.Files {
			keep[b.SHA256] = true
		}
		ents, _ := os.ReadDir(files)
		for _, e := range ents {
			if !keep[e.Name()] {
				_ = os.Remove(filepath.Join(files, e.Name()))
			}
		}
	}
	return stale
}

// inputDefault reports footage only an input gives by default.
func inputDefault(d *models.RecipeDoc, id string) bool {
	for _, in := range d.Inputs {
		for _, it := range in.Default.Items {
			if it == id {
				for _, s := range d.Sections {
					for _, c := range s.Clips {
						if c.AssetID == id && s.Kind == models.RecipeFixed {
							return false
						}
					}
				}
				return true
			}
		}
	}
	return false
}

// makeStarterClip makes an example clip with ffmpeg: small, silent, no metadata.
func makeStarterClip(t *testing.T, c starterClip) []byte {
	t.Helper()
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) {
		t.Fatal("making a starter's example clip needs ffmpeg")
	}
	out := filepath.Join(t.TempDir(), c.file)
	args := append([]string{"-hide_banner", "-loglevel", "error", "-y"}, c.ffmpeg...)
	args = append(args, "-t", fmt.Sprint(c.seconds), "-c:v", "libx264", "-preset", "veryslow", "-crf", "32", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "24k",
		"-shortest", "-map_metadata", "-1", "-fflags", "+bitexact", "-flags:v", "+bitexact", "-flags:a", "+bitexact", "-movflags", "+faststart", out)
	if b, err := exec.Command(ts.Path(tools.FFmpeg), args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// The starters' files are in step with their source here.
func TestStarterFiles(t *testing.T) {
	update := os.Getenv("METARUSH_UPDATE_STARTERS") == "1"
	var stale []string
	for _, def := range starterDefs() {
		stale = append(stale, buildStarter(t, def, update)...)
	}
	if len(stale) > 0 {
		t.Fatalf("the starter recipes' files are out of step with starters_def_test.go: %v\nwrite them: METARUSH_UPDATE_STARTERS=1 go test ./internal/recipes -run TestStarterFiles", stale)
	}
}

package visual

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
)

// A still picture reads like one frame of a clip: labels at the index's
// thresholds, most confident first; its confident text from the top down.
func TestStillReadsLabelsAndText(t *testing.T) {
	f := vision.Frame{
		Labels: []vision.Label{{ID: "dog", Conf: 0.62, Precise: true}, {ID: "beach", Conf: 0.9}, {ID: "kite", Conf: 0.2, Precise: true}},
		Text: []vision.Text{
			{S: "OPEN  DAILY", Conf: 0.9, Box: vision.Box{0.1, 0.6, 0.3, 0.1}},
			{S: "SURF SHOP", Conf: 0.95, Box: vision.Box{0.2, 0.1, 0.5, 0.1}},
			{S: "blurry", Conf: 0.2, Box: vision.Box{0.1, 0.9, 0.2, 0.05}},
		},
	}
	labels, text := Still(f)
	var got []string
	for _, l := range labels {
		got = append(got, l.Label)
	}
	if !slices.Equal(got, []string{"beach", "dog"}) {
		t.Fatalf("labels %v (a weak label is left out; most confident first)", got)
	}
	if labels[0].Kind != KindScene || labels[1].Kind != KindThing {
		t.Fatalf("kinds %+v", labels)
	}
	if text != "SURF SHOP\nOPEN DAILY" {
		t.Fatalf("text %q", text)
	}
}

// A picture added to the resources library on a Mac gets what Apple Vision
// sees as suggested tags and the words in it as its text, with one run of the
// helper for both (real helper; macOS only).
func TestPictureResourcesGetVisionTagsAndText(t *testing.T) {
	ts := helper(t)
	src := filepath.Join("..", "..", "tests", "fixtures", "gen", "visual-a.mp4")
	if _, err := os.Stat(src); err != nil {
		t.Skip("visual-a.mp4 not generated (scripts/make-fixtures.sh)")
	}
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	// Two moments of the fixture as pictures someone added: the EXIT sign, and the dog.
	frame := func(at, name string) string {
		p := filepath.Join(t.TempDir(), name)
		if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-loglevel", "error", "-ss", at, "-i", src, "-frames:v", "1", "-y", p}}); err != nil {
			t.Fatal(err)
		}
		return p
	}
	look := func(path, sha string) ([]models.ResourceTag, string) {
		in := resources.TagInput{Name: filepath.Base(path), Kind: models.ResourceImage, Version: &models.ResourceVersion{SHA256: sha}, Path: path, Tools: ts}
		var seen []models.ResourceTag
		for _, tg := range resources.AutoTags(context.Background(), in) {
			if tg.Source == "vision" {
				seen = append(seen, tg)
			}
		}
		text := resources.FindText(context.Background(), in)
		t.Logf("%s: tags %+v text %q", filepath.Base(path), seen, text)
		return seen, text
	}
	if _, text := look(frame("23", "exit-sign.png"), "still-test-exit-sign"); !strings.Contains(strings.ToUpper(text), "EXIT") {
		t.Fatalf("the sign's words aren't the picture's text: %q", text)
	}
	dogTags, _ := look(frame("27", "dog.png"), "still-test-dog")
	if !slices.ContainsFunc(dogTags, func(tg models.ResourceTag) bool { return tg.Tag == "dog" && tg.Why != "" }) || len(dogTags) > maxStillTags {
		t.Fatalf("vision tags for the dog %+v", dogTags)
	}
	looks.Lock()
	_, remembered := looks.m["still-test-dog"]
	looks.Unlock()
	if !remembered {
		t.Fatal("the look wasn't shared by the tagger and the text finder")
	}
	// Elsewhere (no helper) nothing is suggested and nothing fails.
	none := resources.TagInput{Name: "exit-sign.png", Kind: models.ResourceImage, Version: &models.ResourceVersion{SHA256: "still-test-none"}, Path: frame("23", "again.png"), Tools: &tools.Set{}}
	if got := (stillTagger{}).Tags(context.Background(), none); got != nil {
		t.Fatalf("tags without the helper: %+v", got)
	}
}

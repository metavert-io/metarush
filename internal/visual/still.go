package visual

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
)

// A picture added to the resources library (D79) is looked at once by the
// same on-device helper as clips: what Apple Vision sees becomes suggested
// tags, the words in it become the version's text, which search reads. On a
// machine without the helper (not a Mac, or not built yet) nothing happens;
// `metarush resources refresh` looks again later.

func init() {
	resources.RegisterTagger(stillTagger{})
	resources.RegisterTextFinder(stillText{})
}

// StillLabel is something seen in a still picture.
type StillLabel struct {
	Label string  // plain words (palm tree, dog, person)
	Kind  string  // scene, thing, animal or person
	Conf  float64 // 0–1
}

// maxStillTags bounds the tags one picture suggests.
const maxStillTags = 8

// stillSide is the long side of the JPEG the helper reads.
const stillSide = 1600

// Still reads one picture the way the index reads a clip's frames: its labels
// at the same thresholds, most confident first, and its confident lines of
// text from the top of the picture down.
func Still(f vision.Frame) (labels []StillLabel, text string) {
	for _, o := range seen(f) {
		labels = append(labels, StillLabel{Label: o.label, Kind: o.kind, Conf: o.conf})
	}
	sort.SliceStable(labels, func(i, j int) bool { return labels[i].Conf > labels[j].Conf })
	var lines []vision.Text
	for _, t := range f.Text {
		if t.Conf >= textMin && strings.TrimSpace(t.S) != "" {
			lines = append(lines, t)
		}
	}
	sort.SliceStable(lines, func(i, j int) bool {
		if math.Abs(lines[i].Box[1]-lines[j].Box[1]) > 0.01 {
			return lines[i].Box[1] < lines[j].Box[1]
		}
		return lines[i].Box[0] < lines[j].Box[0]
	})
	parts := make([]string, 0, len(lines))
	for _, t := range lines {
		parts = append(parts, strings.Join(strings.Fields(t.S), " "))
	}
	return labels, strings.Join(parts, "\n")
}

// looks remembers the last pictures looked at (by SHA-256), so the tagger and
// the text finder of one add share a single run of the helper.
var looks = struct {
	sync.Mutex
	m     map[string]*vision.Frame
	order []string
}{m: map[string]*vision.Frame{}}

const lookMemo = 32

// lookAt runs the helper on a picture resource's file (nil when this machine
// can't, or the picture couldn't be read). Pictures and website snapshots
// (their desktop screenshot) are read as they are; an SVG is drawn as a
// picture first (sips, macOS); a PDF is looked at through its first page
// picture (D108).
func lookAt(ctx context.Context, in resources.TagInput) *vision.Frame {
	if in.Tools == nil || in.Version == nil || in.Path == "" || !in.Tools.Has(tools.Vision) {
		return nil
	}
	src := ""
	switch in.Kind {
	case models.ResourceImage, models.ResourceWebsite:
		src = in.Path
	case models.ResourcePDF:
		if len(in.Pages) > 0 {
			src = in.Pages[0]
		}
	case models.ResourceSVG:
		src = in.Path // rasterized below
	}
	if src == "" {
		return nil
	}
	key := in.Version.SHA256
	looks.Lock()
	f, ok := looks.m[key]
	looks.Unlock()
	if ok {
		return f
	}
	if in.Kind == models.ResourceSVG {
		png, cleanup, err := rasterizeSVG(ctx, in.Tools, in.Path, in.Version.Width, in.Version.Height)
		if err != nil {
			return nil
		}
		defer cleanup()
		src = png
	}
	f = analyzeStill(ctx, in.Tools, src)
	looks.Lock()
	if _, had := looks.m[key]; !had {
		looks.order = append(looks.order, key)
		if len(looks.order) > lookMemo {
			delete(looks.m, looks.order[0])
			looks.order = looks.order[1:]
		}
	}
	looks.m[key] = f
	looks.Unlock()
	return f
}

// rasterizeSVG draws a (sanitized) SVG as a PNG with sips, large enough for
// Apple Vision to read its shapes and words: small logos are drawn bigger.
func rasterizeSVG(ctx context.Context, ts *tools.Set, svg string, w, h int) (string, func(), error) {
	if !ts.Has(tools.Sips) {
		return "", func() {}, fmt.Errorf("sips isn't here")
	}
	dir, err := os.MkdirTemp("", "metarush-svg-")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	in := filepath.Join(dir, "picture.svg")
	b, err := os.ReadFile(svg)
	if err != nil {
		cleanup()
		return "", func() {}, err
	}
	if err := os.WriteFile(in, b, 0o600); err != nil {
		cleanup()
		return "", func() {}, err
	}
	out := filepath.Join(dir, "picture.png")
	side := stillSide
	if longest := max(w, h); longest > 0 && longest < side {
		side = min(stillSide, max(longest*3, 800))
	}
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if _, err := ts.Run(cctx, tools.Spec{Tool: tools.Sips, Args: []string{"-s", "format", "png", "-Z", fmt.Sprint(side), in, "--out", out}}); err != nil {
		cleanup()
		return "", func() {}, err
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		cleanup()
		return "", func() {}, fmt.Errorf("sips drew nothing")
	}
	return out, cleanup, nil
}

func analyzeStill(ctx context.Context, ts *tools.Set, path string) *vision.Frame {
	b, err := resources.StillJPEG(path, stillSide)
	if err != nil {
		return nil
	}
	dir, err := os.MkdirTemp("", "metarush-still-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	if err := os.WriteFile(filepath.Join(dir, "still.jpg"), b, 0o600); err != nil {
		return nil
	}
	cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	var got *vision.Frame
	if err := vision.Analyze(cctx, ts, dir, vision.Options{}, func(f vision.Frame) {
		if f.Error == "" {
			got = &f
		}
	}); err != nil {
		return nil
	}
	return got
}

// lookPages is what the helper saw: one frame for a picture, an SVG or a web
// page; for a PDF, its first pages (up to three), each looked at once.
func lookPages(ctx context.Context, in resources.TagInput) []*vision.Frame {
	if in.Kind != models.ResourcePDF {
		if f := lookAt(ctx, in); f != nil {
			return []*vision.Frame{f}
		}
		return nil
	}
	var out []*vision.Frame
	for i, p := range in.Pages {
		if i >= 3 || in.Version == nil || i >= len(in.Version.Pages) {
			break
		}
		page := in
		page.Kind, page.Path = models.ResourceImage, p
		v := *in.Version
		v.SHA256 = in.Version.Pages[i].SHA256
		page.Version = &v
		if f := lookAt(ctx, page); f != nil {
			out = append(out, f)
		}
	}
	return out
}

// stillTagger suggests what Apple Vision sees in a picture.
type stillTagger struct{}

func (stillTagger) Name() string { return "vision" }

func (stillTagger) Tags(ctx context.Context, in resources.TagInput) []models.ResourceTag {
	best := map[string]StillLabel{}
	var order []string
	for _, f := range lookPages(ctx, in) {
		labels, _ := Still(*f)
		for _, l := range labels {
			if cur, ok := best[l.Label]; !ok || l.Conf > cur.Conf {
				if !ok {
					order = append(order, l.Label)
				}
				best[l.Label] = l
			}
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return best[order[i]].Conf > best[order[j]].Conf })
	var out []models.ResourceTag
	for _, name := range order {
		if len(out) == maxStillTags {
			break
		}
		l := best[name]
		pct := math.Round(l.Conf * 100)
		why := fmt.Sprintf("Apple Vision on this Mac sees it (%.0f%% sure)", pct)
		if in.Kind == models.ResourcePDF {
			why = fmt.Sprintf("Apple Vision on this Mac sees it on its first pages (%.0f%% sure)", pct)
		}
		out = append(out, models.ResourceTag{Tag: l.Label, Source: "vision", Confidence: pct / 100, Why: why})
	}
	return out
}

// stillText reads the words in a picture (a PDF's first pages, one after the other).
type stillText struct{}

func (stillText) Text(ctx context.Context, in resources.TagInput) (string, error) {
	var parts []string
	for _, f := range lookPages(ctx, in) {
		if _, text := Still(*f); strings.TrimSpace(text) != "" {
			parts = append(parts, text)
		}
	}
	text := strings.Join(parts, "\n\n")
	if r := []rune(text); len(r) > 2000 {
		text = string(r[:2000])
	}
	return text, nil
}

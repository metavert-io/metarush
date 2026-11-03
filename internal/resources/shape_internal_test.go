package resources

import (
	"context"
	"fmt"
	"os"
	"slices"
	"testing"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
)

// The slides for a vertical cut are vertical (D393): a code slide, a card and
// a diagram made for a vertical cut are drawn to fit it — the drawing is told
// the shape (render/slideshape.mjs narrows it and turns a flowchart to run
// down) and how it was made records it, so a newer commit's version keeps it.
// Made for a wide cut, or for none, they are drawn exactly as always.
func TestSlidesForAVerticalCutAreVertical(t *testing.T) {
	a, p, repo, _ := widget(t)
	ctx := context.Background()
	var inputs []map[string]any
	drawn := 0
	drawSlide = func(_ context.Context, _ *app.App, kind string, input any, out string) (*genResult, error) {
		in := input.(map[string]any)
		inputs = append(inputs, in)
		drawn++
		w, h := 1236, 760 // what a wide slide comes out as
		if in["shape"] == "vertical" {
			w, h = 1076, 1504
		}
		// Every drawing differs (the library keeps the same bytes once).
		if kind == MakeDiagram {
			svg := fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 %d %d"></svg>`, w, h+drawn)
			return &genResult{Width: w, Height: h, Versions: map[string]string{"mermaid": "test"}}, os.WriteFile(out, []byte(svg), 0o644)
		}
		writePNG(t, "", out, testLogo(w/4+drawn, h/4))
		return &genResult{Width: w, Height: h, Versions: map[string]string{"shiki": "test"}}, nil
	}
	defer func() { drawSlide = runGenerator }()

	for k, c := range []struct {
		name  string
		shape editspec.Shape
		want  any // the shape the drawing is told (nil: nothing, drawn as always)
	}{{"for a vertical cut", editspec.ShapeVertical, "vertical"}, {"for a square cut", editspec.ShapeSquare, "square"}, {"for a wide cut", editspec.ShapeLandscape, nil}, {"for no cut", "", nil}} {
		inputs = nil
		for _, o := range []MakeOptions{
			{Kind: MakeCode, Repo: repo, File: "cmd/widget/main.go", From: 1, To: 20, ProjectID: p.ID, Shape: c.shape},
			{Kind: MakeStats, Repo: repo, ProjectID: p.ID, Shape: c.shape},
			{Kind: MakeDiagram, Repo: repo, Source: "flowchart LR\n  A --> B", ProjectID: p.ID, Shape: c.shape},
		} {
			o.Name = fmt.Sprintf("%s-%d", o.Kind, k) // a resource of its own each time
			res, err := Make(ctx, a, o)
			if err != nil {
				t.Fatalf("%s, %s: %v", c.name, o.Kind, err)
			}
			mw := res.Add.Resource.Latest().Source.MadeWith
			if want, _ := c.want.(string); mw == nil || mw.Shape != want {
				t.Errorf("%s, %s: how it was made records the shape %q, want %q", c.name, o.Kind, mw.Shape, want)
			}
			notes := slices.Contains(res.Notes, "Drawn to fit a "+c.shape.Word()+" cut.")
			if notes != (c.want != nil) {
				t.Errorf("%s, %s: notes %q", c.name, o.Kind, res.Notes)
			}
		}
		if len(inputs) != 3 {
			t.Fatalf("%s: drew %d slides", c.name, len(inputs))
		}
		for i, in := range inputs {
			if got, ok := in["shape"]; (c.want == nil && ok) || (c.want != nil && got != c.want) {
				t.Errorf("%s: slide %d was told the shape %v, want %v", c.name, i, got, c.want)
			}
		}
	}
	if d2ForShape("direction: right\na -> b", editspec.ShapeVertical) != "direction: down\na -> b" ||
		d2ForShape("direction: left\na -> b", editspec.ShapeVertical) != "direction: up\na -> b" ||
		d2ForShape("direction: right\na -> b", editspec.ShapeSquare) != "direction: right\na -> b" ||
		d2ForShape("a -> b", editspec.ShapeVertical) != "a -> b" {
		t.Fatal("a D2 diagram for a vertical cut runs down; nothing else changes")
	}
}

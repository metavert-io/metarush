package editspec

import (
	"encoding/json"
	"os"
	"testing"
)

// The one shape rule (D376) against the cases web/src/lib/shape.ts is held to.
func TestShapeVectors(t *testing.T) {
	b, err := os.ReadFile("testdata/shapes.json")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Sizes []struct {
			Width, Height int
			Shape         Shape
			Word, Ratio   string
			Aspect        float64
		}
		Outputs []struct {
			Output        Output
			Shape         Shape
			Width, Height int
		}
		Presets map[Shape]string
	}
	if err := json.Unmarshal(b, &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Sizes) == 0 || len(v.Outputs) == 0 || len(v.Presets) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range v.Sizes {
		if got := ShapeOf(c.Width, c.Height); got != c.Shape {
			t.Errorf("ShapeOf(%d, %d) = %q, want %q", c.Width, c.Height, got, c.Shape)
		}
		s := ShapeOfSize(c.Width, c.Height)
		if c.Shape == "" {
			if s != (OutputShape{}) {
				t.Errorf("ShapeOfSize(%d, %d) = %+v, want nothing", c.Width, c.Height, s)
			}
			continue
		}
		if s.Shape != c.Shape || s.Word != c.Word || s.Ratio != c.Ratio || s.Aspect != c.Aspect || s.Width != c.Width || s.Height != c.Height {
			t.Errorf("ShapeOfSize(%d, %d) = %+v, want %s %s %s %v", c.Width, c.Height, s, c.Shape, c.Word, c.Ratio, c.Aspect)
		}
	}
	for _, c := range v.Outputs {
		s := c.Output.Shape()
		if s.Shape != c.Shape || s.Width != c.Width || s.Height != c.Height {
			t.Errorf("%+v.Shape() = %+v, want %s %dx%d", c.Output, s, c.Shape, c.Width, c.Height)
		}
	}
	for shape, preset := range v.Presets {
		if got := PresetFor(shape); got != preset {
			t.Errorf("PresetFor(%q) = %q, want %q", shape, got, preset)
		}
		if preset != "" {
			if got := (Output{Preset: preset}).Shape().Shape; got != shape {
				t.Errorf("the preset %s is %q, not %q", preset, got, shape)
			}
		}
	}
}

func TestParseShape(t *testing.T) {
	for in, want := range map[string]string{"vertical": "1080x1920", "widescreen": "1920x1080", "landscape": "1920x1080", "square": "1080x1080",
		"1080x1350": "1080x1350", "landscape-1080p": "1920x1080", "1080x01350": "", "10x10": "", "wide": "", "": ""} {
		s, ok := ParseShape(in)
		if got := s.Key(); got != want || ok != (want != "") {
			t.Errorf("ParseShape(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
}

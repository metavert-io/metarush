package interchange

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

// A 4:5 timeline stays 4:5 (D389): a timeline's own size decides the cut's
// shape — a preset's ratio at its size, any other ratio kept with its short
// side 1080, a size MetaRush can't make exactly as near as it gets, said; a
// timeline that says no size takes the project's shape, else widescreen.
func TestA45TimelineStays45(t *testing.T) {
	for _, c := range []struct {
		name          string
		w, h          int
		frame         editspec.Output
		preset        string
		width, height int
		lost          string
	}{
		{"4:5", 1080, 1350, editspec.Output{}, "", 1080, 1350, ""},
		{"4:5 at another size", 864, 1080, editspec.Output{}, "", 1080, 1350, ""},
		{"9:16", 1080, 1920, editspec.Output{}, editspec.PresetVertical, 0, 0, ""},
		{"16:9 in 4K", 3840, 2160, editspec.Output{}, editspec.PresetLandscape, 0, 0, ""},
		{"square", 1080, 1080, editspec.Output{}, editspec.PresetSquare, 0, 0, ""},
		{"4:3", 1440, 1080, editspec.Output{}, "", 1440, 1080, ""},
		{"scope", 2048, 858, editspec.Output{}, "", 2578, 1080, "the timeline is 2048×858, a shape MetaRush can't make exactly: the cut is 2578×1080, as near as it gets"},
		{"no size: the project's", 0, 0, editspec.Output{Preset: editspec.PresetVertical}, editspec.PresetVertical, 0, 0, ""},
		{"no size: the project's 4:5", 0, 0, editspec.Output{Width: 1080, Height: 1350}, "", 1080, 1350, ""},
		{"no size, nothing known: widescreen", 0, 0, editspec.Output{}, editspec.PresetLandscape, 0, 0, ""},
	} {
		f := &Flat{Name: "Ad", FPS: 30, Width: c.w, Height: c.h}
		res := f.Spec(SpecOptions{Frame: c.frame})
		o := res.Spec.Output
		if o.Preset != c.preset || o.Width != c.width || o.Height != c.height || o.FPS != 30 {
			t.Errorf("%s: %+v, want %s %dx%d", c.name, o, c.preset, c.width, c.height)
		}
		lost := strings.Join(res.Lost, "\n") // (the timeline here has no footage: that is said too)
		if said := strings.Contains(lost, "a shape MetaRush can't make exactly"); said != (c.lost != "") || !strings.Contains(lost, c.lost) {
			t.Errorf("%s: lost %q, want %q", c.name, lost, c.lost)
		}
	}
}

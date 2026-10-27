package qa

import (
	"encoding/json"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

// A nested cut's graphics are checked at their time on this timeline, cut to
// the nested clip, under the clip's name; the corner logo has its own small box.
func TestLayoutSeesNestedGraphicsAndCornerLogo(t *testing.T) {
	key := "cut:prj_aaaaaaaaaaaaaaaaaaaaaaaaaa/rev_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	p := &editspec.RenderPlan{FPS: 30, Width: 1080, Height: 1920, DurationInFrames: 300,
		Clips: []editspec.PlanClip{{ID: "intro", AssetID: key, From: 60, DurationInFrames: 90, TrimBefore: 30}},
		Overlays: []editspec.PlanOverlay{{ID: "mark", Template: editspec.TplLogoCorner, From: 0, DurationInFrames: 300,
			Params: json.RawMessage(`{"logo":"logo","corner":"top-left"}`)}},
		Nested: map[string]*editspec.NestedPlan{key: {Plan: &editspec.RenderPlan{Overlays: []editspec.PlanOverlay{
			{ID: "who", Template: editspec.TplLowerThirdClean, From: 0, DurationInFrames: 60},    // before the clip starts in that cut: cut to 30 frames
			{ID: "late", Template: editspec.TplLowerThirdClean, From: 200, DurationInFrames: 30}, // after the clip: not on screen
		}}}},
	}
	all, nested := layoutOverlays(p)
	if len(all) != 2 || all[1].ID != "intro/who" || !nested["intro/who"] || all[1].From != 60 || all[1].DurationInFrames != 30 {
		t.Fatalf("overlays %+v", all)
	}
	g := newGeom(p)
	b := g.boxOf(p.Overlays[0])
	if b.L != g.content.L || b.T != g.content.T || b.R-b.L != 108 || b.B-b.T > 65 {
		t.Fatalf("corner logo box %+v (content %+v)", b, g.content)
	}
}

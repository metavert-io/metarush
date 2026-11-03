package qa

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// A render that shows a motion graphic in a shape it was never drawn in gets
// a plain warning over the graphic's time, with the command that draws it
// there (D379).
func TestMotionShapeFinding(t *testing.T) {
	gap := editspec.MotionShapeGap{Name: "route-map", Hash: strings.Repeat("3f", 32), OverlayIDs: []string{"a", "b"}, Undrawn: true,
		Shape: editspec.ShapeOfSize(1920, 1080), Drawn: []editspec.OutputShape{editspec.ShapeOfSize(1080, 1920)}}
	plan := &editspec.RenderPlan{FPS: 30, Width: 1920, Height: 1080, Overlays: []editspec.PlanOverlay{{ID: "a", From: 30, DurationInFrames: 60}, {ID: "b", From: 300, DurationInFrames: 90}, {ID: "c", From: 0, DurationInFrames: 30}}}
	r := &run{in: Input{MotionShapes: []editspec.MotionShapeGap{gap}}, plan: plan, fps: 30}
	r.motionShapes()
	if len(r.pend) != 1 {
		t.Fatalf("findings: %+v", r.pend)
	}
	f := r.pend[0]
	if f.Kind != "motion_shape" || f.Severity != models.QAWarning || f.From != 1 || f.To != 13 || strings.Join(f.OverlayIDs, ",") != "a,b" {
		t.Fatalf("the finding: %+v", f)
	}
	if !strings.Contains(f.Message, "never widescreen (16:9)") || f.Command != "metarush motion check --version route-map@3f3f3f3f --shape 1920x1080" {
		t.Fatalf("words and command: %q %q", f.Message, f.Command)
	}
	if plainCheck := strings.Contains(f.Message, "--"); plainCheck {
		t.Fatalf("a flag in the words: %q", f.Message)
	}
	// Drawn widescreen since, but a person approved it only vertical (D381): only their approval clears it.
	gap.Undrawn, gap.Drawn, gap.DrawnAt = false, append(gap.Drawn, editspec.ShapeOfSize(1920, 1080)), editspec.ShapeOfSize(1920, 1080)
	gap.Approved = []editspec.OutputShape{editspec.ShapeOfSize(1080, 1920)}
	r = &run{in: Input{MotionShapes: []editspec.MotionShapeGap{gap}}, plan: plan, fps: 30}
	r.motionShapes()
	if len(r.pend) != 1 || r.pend[0].Command != "metarush motion approve route-map@3f3f3f3f --shape 1920x1080" || !strings.Contains(r.pend[0].Message, "approved it only vertical (9:16)") {
		t.Fatalf("drawn, not approved: %+v", r.pend)
	}
}

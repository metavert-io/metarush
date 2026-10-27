package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Workers refuse, with a plain and final error, to render (or QA) a cut that
// uses a motion graphic no person approved — or whose approval was withdrawn.
func TestWorkerRefusesUnapprovedMotion(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	h := strings.Repeat("7c", 32)
	if _, err := motion.SaveCheck(ctx, a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: h, Name: "count-up", Region: "center",
		MinDuration: 1, MaxDuration: 5, SchemaJSON: `{"type":"object"}`, Check: models.MotionCheckInfo{At: time.Now().UTC(), By: a.Actor}}); err != nil {
		t.Fatal(err)
	}
	rev := &models.Revision{ID: "rev_01jabcdefghjkmnpqrstvwxyz0", ProjectID: "prj_01jabcdefghjkmnpqrstvwxyz0", Spec: editspec.Spec{SchemaVersion: 1,
		Output:   editspec.Output{Preset: editspec.PresetVertical, Width: 1080, Height: 1920, FPS: 30},
		Clips:    []editspec.Clip{{ID: "a", AssetID: "ast_01jabcdefghjkmnpqrstvwxyz0", In: 0, Out: 2}},
		Overlays: []editspec.Overlay{{ID: "subs", Motion: &editspec.MotionRef{Name: "count-up", Hash: h}, At: 0, Duration: 2, Params: json.RawMessage(`{}`)}}}}
	rd := &models.Render{ID: "rnd_01jabcdefghjkmnpqrstvwxyz0", RevisionID: rev.ID}
	refused := func(want string) {
		t.Helper()
		_, _, _, _, err := resolveForRender(ctx, a, rd, rev)
		var p *Permanent
		if !errors.As(err, &p) || !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "count-up") {
			t.Fatalf("want a final refusal mentioning %q, got %v", want, err)
		}
	}
	refused("no person has approved it yet")
	if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), h, motion.Approve, a.Actor, ""); err != nil {
		t.Fatal(err)
	}
	// Approved: the refusal is gone (the footage itself is missing in this test).
	if _, _, _, _, err := resolveForRender(ctx, a, rd, rev); err == nil || strings.Contains(err.Error(), "count-up") {
		t.Fatalf("approved version still refused: %v", err)
	}
	if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), h, motion.Revoke, a.Actor, ""); err != nil {
		t.Fatal(err)
	}
	refused("its approval was withdrawn")
}

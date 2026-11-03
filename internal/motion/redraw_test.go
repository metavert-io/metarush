package motion_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// The reviewer's case (D381): a version checked square with a preview and
// approved by a person, then drawn again by no one in particular in three
// other shapes without previews. Everything the person saw stays exactly as
// it was, the new shapes are only added, the approval covers what the person
// approved — square — and only a person's approval adds a shape to it.
func TestRedrawOnlyAddsAndApprovalCoversWhatAPersonSaw(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	hash := strings.Repeat("5a", 32)
	obj := func(seed string, w, h int) models.MotionObject {
		return models.MotionObject{Key: "k-" + seed, SHA256: strings.Repeat(seed, 32)[:64], Size: 10, Width: w, Height: h}
	}
	frames := func(label string, w, h int, seeds ...string) []models.MotionFrame {
		var out []models.MotionFrame
		for i, s := range seeds {
			out = append(out, models.MotionFrame{Label: fmt.Sprintf("frame %d", i+1), Object: obj(s, w, h), Shape: label})
		}
		return out
	}
	video := obj("ee", 540, 540)
	check := func(shapes []models.MotionShape, fs []models.MotionFrame, v *models.MotionObject, warn ...models.MotionProblem) *models.MotionComponent {
		t.Helper()
		c, err := motion.SaveCheck(ctx, a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: hash, Name: "badge", Region: "center", MinDuration: 1, MaxDuration: 5,
			SchemaJSON: `{}`, Files: []models.MotionFile{}, Imports: []models.MotionImport{}, Archive: obj("aa", 0, 0), Sheet: &models.MotionObject{Key: "sheet", SHA256: strings.Repeat("bb", 32)},
			Frames: fs, Video: v, Shapes: shapes, Warnings: warn, Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC(), Width: shapes[0].Width, Height: shapes[0].Height}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	first := check([]models.MotionShape{{Label: "square", Width: 1080, Height: 1080, Video: &video}}, frames("square", 1080, 1080, "01", "02", "03"), &video)
	approved, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), hash, motion.Approve, a.Actor, "looks right")
	if err != nil || approved.State != models.MotionApproved || strings.Join(approved.ApprovedShapes, ",") != "1080x1080" {
		t.Fatalf("approve: %v %+v", err, approved)
	}

	// Drawn again in 1080x1350, 1920x1080 and 1080x1920, no previews, by no person.
	again := check([]models.MotionShape{{Label: "vertical", Width: 1080, Height: 1350}, {Label: "widescreen", Width: 1920, Height: 1080}, {Label: "vertical", Width: 1080, Height: 1920}},
		append(append(frames("vertical", 1080, 1350, "11", "12"), frames("widescreen", 1920, 1080, "21")...), frames("vertical", 1080, 1920, "31")...), nil,
		models.MotionProblem{Severity: "warning", Code: "safe_area", Message: "In the widescreen shape: close to the edge."})
	if again.State != models.MotionApproved || len(again.Decisions) != 1 {
		t.Fatalf("drawing again keeps the decision: %+v", again)
	}
	if again.Video == nil || again.Video.SHA256 != video.SHA256 || again.Sheet == nil || again.Check.Width != 1080 || again.Check.Height != 1080 {
		t.Fatalf("the preview, sheet and check a person saw stay: %+v %+v %+v", again.Video, again.Sheet, again.Check)
	}
	for i, f := range first.Frames {
		if again.Frames[i].Object.SHA256 != f.Object.SHA256 || again.Frames[i].Label != f.Label {
			t.Fatalf("frame %d changed: %+v → %+v", i, f, again.Frames[i])
		}
	}
	if len(again.Frames) != 7 || len(again.Shapes) != 4 || again.Shapes[0].Video == nil || again.Shapes[0].Width != 1080 || again.Shapes[0].Height != 1080 {
		t.Fatalf("only added: %d frames, shapes %+v", len(again.Frames), again.Shapes)
	}
	if got := fmt.Sprint(again.Shapes[1].Label, "|", again.Shapes[3].Label); got != "vertical 4:5|vertical 9:16" {
		t.Fatalf("two vertical shapes are named apart: %s", got)
	}
	if strings.Join(again.ApprovedShapes, ",") != "1080x1080" || len(again.Draws) != 1 || strings.Join(again.Draws[0].Shapes, ",") != "1080x1350,1920x1080,1080x1920" {
		t.Fatalf("the approval still covers square only, the draw is recorded: %v %+v", again.ApprovedShapes, again.Draws)
	}
	if len(again.Warnings) != 1 {
		t.Fatalf("new notes are added: %+v", again.Warnings)
	}

	// The same shapes again (any size of the same ratios): nothing new.
	if same := check([]models.MotionShape{{Label: "widescreen", Width: 1280, Height: 720}}, frames("widescreen", 1280, 720, "41"), nil); len(same.Frames) != 7 || len(same.Draws) != 1 {
		t.Fatalf("a ratio drawn before isn't drawn twice: %d frames, %d draws", len(same.Frames), len(same.Draws))
	}

	// A widescreen cut is noted until a person approves it in that shape.
	cut := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Width: 1920, Height: 1080}, Overlays: []editspec.Overlay{{ID: "b", Motion: &editspec.MotionRef{Name: "badge", Hash: hash}}}}
	infos, err := motion.Infos(ctx, a.DB, a.WorkspaceID(), []string{hash})
	if err != nil {
		t.Fatal(err)
	}
	if gaps := editspec.MotionShapeGaps(cut, infoFacts(infos), 1920, 1080); len(gaps) != 1 || gaps[0].Undrawn || !strings.Contains(gaps[0].Command(), "motion approve badge@5a5a5a5a --shape 1920x1080") {
		t.Fatalf("drawn widescreen, approved square: %+v", gaps)
	}
	// Approving without a shape when it's approved: says so, and points at the shapes waiting.
	if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), hash, motion.Approve, a.Actor, ""); apperr.From(err).Code != apperr.Conflict || !strings.Contains(apperr.From(err).Command(), "--shape 1080x1350") {
		t.Fatalf("approve again with no shape: %v", err)
	}
	// A shape it was never drawn in can't be approved.
	if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), hash, motion.Approve, a.Actor, "", "1440x1080"); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("approve a shape never drawn: %v", err)
	}
	// A person approves it widescreen (a 1280x720 size names the same ratio).
	more, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), hash, motion.Approve, a.Actor, "seen widescreen", "1280x720")
	if err != nil || strings.Join(more.ApprovedShapes, ",") != "1080x1080,1920x1080" || len(more.Decisions) != 2 || strings.Join(more.Decisions[1].Shapes, ",") != "1920x1080" {
		t.Fatalf("approve widescreen too: %v %+v", err, more)
	}
	infos, _ = motion.Infos(ctx, a.DB, a.WorkspaceID(), []string{hash})
	if gaps := editspec.MotionShapeGaps(cut, infoFacts(infos), 1920, 1080); len(gaps) != 0 {
		t.Fatalf("approved widescreen: %+v", gaps)
	}
	if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), hash, motion.Approve, a.Actor, "", "widescreen"); apperr.From(err).Code != apperr.Conflict {
		t.Fatalf("approve a shape twice: %v", err)
	}
}

// An approved version from before approved shapes were recorded covers the
// shapes it was drawn in; the first drawing again writes that down first.
func TestOlderApprovalsCoverWhatWasDrawn(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	hash := strings.Repeat("6b", 32)
	fr := models.MotionFrame{Label: "middle", Object: models.MotionObject{Key: "k", SHA256: strings.Repeat("01", 32), Width: 1080, Height: 1920}}
	if _, err := motion.SaveCheck(ctx, a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: hash, Name: "old", Region: "center", SchemaJSON: `{}`,
		Frames: []models.MotionFrame{fr}, Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC(), Width: 1080, Height: 1920}}); err != nil {
		t.Fatal(err)
	}
	// Approved the way older builds did: no shapes recorded.
	if _, err := a.DB.C("motion_components").UpdateOne(ctx, map[string]any{"hash": hash}, map[string]any{"$set": map[string]any{"state": models.MotionApproved}}); err != nil {
		t.Fatal(err)
	}
	c, _ := motion.Get(ctx, a.DB, a.WorkspaceID(), hash)
	if strings.Join(c.ApprovedSizes(), ",") != "1080x1920" {
		t.Fatalf("an older approval covers what was drawn: %v", c.ApprovedSizes())
	}
	wide := models.MotionFrame{Label: "middle", Object: models.MotionObject{Key: "k2", SHA256: strings.Repeat("02", 32), Width: 1920, Height: 1080}}
	after, err := motion.SaveCheck(ctx, a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: hash, Name: "old", Region: "center", SchemaJSON: `{}`,
		Frames: []models.MotionFrame{wide}, Shapes: []models.MotionShape{{Label: "widescreen", Width: 1920, Height: 1080}}, Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC(), Width: 1920, Height: 1080}})
	if err != nil || strings.Join(after.ApprovedShapes, ",") != "1080x1920" || strings.Join(after.ApprovedSizes(), ",") != "1080x1920" || len(after.Shapes) != 2 || after.Shapes[0].Label != "vertical" {
		t.Fatalf("drawn again: %v %+v", err, after)
	}
}

type infoFacts map[string]editspec.MotionInfo

func (f infoFacts) Asset(string) (editspec.AssetFacts, bool)       { return editspec.AssetFacts{}, false }
func (f infoFacts) Transcript(string) []editspec.TranscriptSegment { return nil }
func (f infoFacts) Motion(h string) (editspec.MotionInfo, bool)    { i, ok := f[h]; return i, ok }

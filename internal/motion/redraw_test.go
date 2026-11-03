package motion_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
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
	// Approved the way older builds did, after its check: no shapes recorded.
	if _, err := a.DB.C("motion_components").UpdateOne(ctx, map[string]any{"hash": hash}, map[string]any{"$set": map[string]any{"state": models.MotionApproved, "approvedAt": time.Now().UTC().Add(time.Minute)}}); err != nil {
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

// putOld writes a version the way 0.15.0 left it: frames in one shape, a
// check time, maybe an approval time, and no approved shapes recorded.
func putOld(t *testing.T, a *app.App, hash, name, state string, checkedAt time.Time, approvedAt *time.Time, w, h int) {
	t.Helper()
	c := models.MotionComponent{ID: a.WorkspaceID() + "/" + hash, WorkspaceID: a.WorkspaceID(), Hash: hash, Name: name, Region: "center", SchemaJSON: `{}`, State: state,
		Frames: []models.MotionFrame{{Label: "middle", Object: models.MotionObject{Key: "k-" + name, SHA256: hash, Width: w, Height: h}}},
		Check:  models.MotionCheckInfo{By: a.Actor, At: checkedAt, Width: w, Height: h}, ApprovedAt: approvedAt, CreatedAt: checkedAt, UpdatedAt: checkedAt}
	if approvedAt != nil {
		c.ApprovedBy = &a.Actor
		c.Decisions = []models.MotionDecision{{State: models.MotionApproved, By: a.Actor, At: *approvedAt}}
	}
	if err := a.DB.Insert(context.Background(), "motion_components", c); err != nil {
		t.Fatal(err)
	}
}

func gapsFor(t *testing.T, a *app.App, hash string, w, h int) []editspec.MotionShapeGap {
	t.Helper()
	infos, err := motion.Infos(context.Background(), a.DB, a.WorkspaceID(), []string{hash})
	if err != nil {
		t.Fatal(err)
	}
	cut := &editspec.Spec{SchemaVersion: 1, Output: editspec.Output{Width: w, Height: h}, Overlays: []editspec.Overlay{{ID: "g", Motion: &editspec.MotionRef{Name: "g", Hash: hash}}}}
	return editspec.MotionShapeGaps(cut, infoFacts(infos), w, h)
}

// A (D384): approving with no shapes named — a caller that sends none —
// covers only the first check's shapes, never one drawn afterwards that the
// person may not have seen.
func TestApprovingWithNoShapesNamedCoversOnlyTheFirstCheck(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	hash := strings.Repeat("7c", 32)
	save := func(w, h int, seed string) {
		t.Helper()
		if _, err := motion.SaveCheck(ctx, a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: hash, Name: "card", Region: "center", SchemaJSON: `{}`,
			Shapes: []models.MotionShape{{Width: w, Height: h}}, Frames: []models.MotionFrame{{Label: "middle", Object: models.MotionObject{Key: "k" + seed, SHA256: strings.Repeat(seed, 32), Width: w, Height: h}}},
			Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC(), Width: w, Height: h}}); err != nil {
			t.Fatal(err)
		}
	}
	save(1080, 1920, "01") // checked vertical, waiting for a person
	save(1920, 1080, "02") // drawn widescreen afterwards (Auto, a safe step)
	c, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), hash, motion.Approve, a.Actor, "")
	if err != nil || strings.Join(c.ApprovedShapes, ",") != "1080x1920" || strings.Join(c.Decisions[0].Shapes, ",") != "1080x1920" {
		t.Fatalf("approved with no shapes named: %v %+v", err, c)
	}
	if gaps := gapsFor(t, a, hash, 1920, 1080); len(gaps) != 1 || gaps[0].Undrawn {
		t.Fatalf("the widescreen drawing still waits for a person: %+v", gaps)
	}
}

// B (D384): on 0.15.0 checking the same code again replaced its frames, so a
// version approved vertical and re-checked widescreen afterwards holds frames
// nobody approved: its approval covers no shape until a person approves one.
func TestApprovalOlderThanItsFramesCoversNoShape(t *testing.T) {
	a := testapp.New(t)
	approved := time.Date(2026, 10, 21, 9, 0, 0, 0, time.UTC)
	hash := strings.Repeat("8d", 32)
	putOld(t, a, hash, "rechecked", models.MotionApproved, approved.Add(26*time.Hour), &approved, 1920, 1080)
	c, err := motion.Get(context.Background(), a.DB, a.WorkspaceID(), hash)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.ApprovedSizes(); len(got) != 0 {
		t.Fatalf("frames newer than the approval are covered: %v", got)
	}
	gaps := gapsFor(t, a, hash, 1920, 1080)
	if len(gaps) != 1 || gaps[0].Undrawn || !strings.Contains(gaps[0].Command(), "motion approve rechecked@8d8d8d8d --shape 1920x1080") || !strings.Contains(gaps[0].Words(), "no person has approved it in this shape") {
		t.Fatalf("a widescreen cut asks a person to approve it widescreen: %+v", gaps)
	}
	// Its first drawing again doesn't write a coverage nobody gave.
	after, err := motion.SaveCheck(context.Background(), a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: hash, Name: "rechecked", Region: "center", SchemaJSON: `{}`,
		Shapes: []models.MotionShape{{Width: 1080, Height: 1920}}, Frames: []models.MotionFrame{{Label: "middle", Object: models.MotionObject{Key: "k9", SHA256: strings.Repeat("09", 32), Width: 1080, Height: 1920}}},
		Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC(), Width: 1080, Height: 1920}})
	if err != nil || after.ApprovedShapes != nil || len(after.ApprovedSizes()) != 0 {
		t.Fatalf("drawn again: %v %v", err, after.ApprovedShapes)
	}
}

// B, as the live workspace holds them: four graphics checked on 20 October,
// three approved after their one check the next day, one still waiting. The
// approved ones count as approved in the shape they were drawn in (1080×1920)
// and nothing else; the waiting one in nothing.
func TestApprovedAfterItsOneCheckCoversThatShapeOnly(t *testing.T) {
	a := testapp.New(t)
	checked := time.Date(2026, 10, 20, 15, 4, 0, 0, time.UTC)
	for i, name := range []string{"lower-third", "title-card", "end-card", "stat-card"} {
		hash := strings.Repeat(fmt.Sprintf("%02x", 0xa0+i), 32)
		var approvedAt *time.Time
		state := models.MotionWaiting
		if i < 3 {
			at := checked.Add(time.Duration(18+i) * time.Hour)
			approvedAt, state = &at, models.MotionApproved
		}
		putOld(t, a, hash, name, state, checked.Add(time.Duration(i)*time.Minute), approvedAt, 1080, 1920)
		c, err := motion.Get(context.Background(), a.DB, a.WorkspaceID(), hash)
		if err != nil {
			t.Fatal(err)
		}
		want := ""
		if i < 3 {
			want = "1080x1920"
		}
		if got := strings.Join(c.ApprovedSizes(), ","); got != want {
			t.Fatalf("%s: approved in %q, want %q", name, got, want)
		}
		if i < 3 {
			if gaps := gapsFor(t, a, hash, 1080, 1920); len(gaps) != 0 {
				t.Fatalf("%s in a vertical cut: %+v", name, gaps)
			}
			if gaps := gapsFor(t, a, hash, 1920, 1080); len(gaps) != 1 || !gaps[0].Undrawn {
				t.Fatalf("%s in a widescreen cut: %+v", name, gaps)
			}
		}
	}
}

// Minor (D381): checking the folder again with the same code merges like a
// redraw, so a full version is refused in plain words — never trimmed, and
// never by dropping a shape a person approved.
func TestRecheckingAFullVersionIsRefusedNotTrimmed(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	hash := strings.Repeat("9e", 32)
	var shapes []models.MotionShape
	var frames []models.MotionFrame
	for i := 0; i < motion.MaxDrawnShapes; i++ {
		w := 1080 + 2*i*37
		shapes = append(shapes, models.MotionShape{Width: w, Height: 1080})
		frames = append(frames, models.MotionFrame{Label: "middle", Object: models.MotionObject{Key: fmt.Sprint("k", i), SHA256: strings.Repeat(fmt.Sprintf("%02x", i), 32), Width: w, Height: 1080}})
	}
	if _, err := motion.SaveCheck(ctx, a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: hash, Name: "full", Region: "center", SchemaJSON: `{}`,
		Shapes: shapes, Frames: frames, Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC(), Width: 1080, Height: 1080}}); err != nil {
		t.Fatal(err)
	}
	if _, err := motion.Decide(ctx, a.DB, a.WorkspaceID(), hash, motion.Approve, a.Actor, "", "1080x1080"); err != nil {
		t.Fatal(err)
	}
	_, err := motion.SaveCheck(ctx, a.DB, &models.MotionComponent{WorkspaceID: a.WorkspaceID(), Hash: hash, Name: "full", Region: "center", SchemaJSON: `{}`,
		Shapes: []models.MotionShape{{Width: 1080, Height: 1920}}, Frames: []models.MotionFrame{{Label: "middle", Object: models.MotionObject{Key: "kv", SHA256: strings.Repeat("ff", 32), Width: 1080, Height: 1920}}},
		Check: models.MotionCheckInfo{By: a.Actor, At: time.Now().UTC(), Width: 1080, Height: 1920}})
	if e := apperr.From(err); e.Code != apperr.Conflict || !strings.Contains(e.Message, "at most 9") {
		t.Fatalf("a tenth shape: %v", err)
	}
	c, _ := motion.Get(ctx, a.DB, a.WorkspaceID(), hash)
	if len(c.Shapes) != motion.MaxDrawnShapes || len(c.Frames) != motion.MaxDrawnShapes || strings.Join(c.ApprovedShapes, ",") != "1080x1080" {
		t.Fatalf("the version changed: %d shapes, %d frames, approved %v", len(c.Shapes), len(c.Frames), c.ApprovedShapes)
	}
}

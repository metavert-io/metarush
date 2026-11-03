package conversation

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// A call cut starts in its project's shape, and its captions follow the
// house style (D386, D390): widescreen footage makes a widescreen cut for
// YouTube with people side by side and no captions, as before; a brief that
// says social shorts makes it vertical for Shorts, stacked, with captions; a
// house style that says no captions (or captions, in its look) decides over
// the shape; and a cut that replaces another keeps that cut's shape.
func TestACallCutStartsInTheProjectsShape(t *testing.T) {
	c := newCall(t)
	ctx := context.Background()
	cv := scall[CallOut](t, c.a, "conversation.create", CallCreateIn{ProjectID: c.project, Members: []CallMemberIn{{AssetID: c.maya, Name: "Maya"}, {AssetID: c.sam, Name: "Sam"}}})
	cut := func(in CallCutIn) *editspec.Spec {
		t.Helper()
		in.CallRefIn, in.Style, in.NameTags = CallRefIn{Conversation: cv.Conversation.ID}, "podcast", new(bool)
		po := scall[CallPlanOut](t, c.a, "conversation.cut", in)
		if po.Result == nil || po.Result.Plan.Spec == nil {
			t.Fatalf("no plan: %+v", po)
		}
		return po.Result.Plan.Spec
	}
	splits := func(s *editspec.Spec) map[string]int {
		n := map[string]int{}
		for _, cl := range s.Clips {
			for _, sh := range cl.Shots {
				if sh.Layout != "" {
					n[sh.Layout]++
				}
			}
		}
		return n
	}

	wide := cut(CallCutIn{})
	if o := wide.Output; o.Preset != editspec.PresetLandscape || o.Platform != "youtube" || wide.Captions != nil || splits(wide)[editspec.LayoutStacked] > 0 || splits(wide)[editspec.LayoutSideBySide] == 0 {
		t.Fatalf("widescreen footage: %+v captions %+v layouts %v", o, wide.Captions, splits(wide))
	}

	if err := c.a.DB.Update(ctx, store.CProjects, c.a.WorkspaceID(), c.project, bson.M{"$set": bson.M{"brief.deliverables": []models.Deliverable{{Name: "Social shorts", Preset: editspec.PresetVertical, Platform: "shorts"}}}}); err != nil {
		t.Fatal(err)
	}
	tall := cut(CallCutIn{})
	if o := tall.Output; o.Preset != editspec.PresetVertical || o.Platform != "shorts" || tall.Captions == nil || !tall.Captions.Enabled ||
		splits(tall)[editspec.LayoutSideBySide] > 0 || splits(tall)[editspec.LayoutStacked] == 0 {
		t.Fatalf("a vertical project: %+v captions %+v layouts %v", o, tall.Captions, splits(tall))
	}

	// The project holds the Content Settings new projects use (as project.create does, D192).
	if _, err := store.MoveContentSettings(ctx, c.a.DB, c.a.WorkspaceID()); err != nil {
		t.Fatal(err)
	}
	off, on, boxed := "off", "on", "boxed"
	scall[ops.ContentStyleOut](t, c.a, "content.style.set", ops.ContentStyleSetIn{Captions: &off})
	if s := cut(CallCutIn{}); s.Captions != nil {
		t.Fatalf("the house style says no captions: %+v", s.Captions)
	}
	scall[ops.ContentStyleOut](t, c.a, "content.style.set", ops.ContentStyleSetIn{Captions: &on, CaptionLook: &boxed})
	if s := cut(CallCutIn{Shape: "landscape"}); s.Captions == nil || s.Captions.Style != editspec.CaptionBoxed || s.Output.Preset != editspec.PresetLandscape {
		t.Fatalf("the house style says captions, boxed: %+v %+v", s.Captions, s.Output)
	}
	no := false
	if s := cut(CallCutIn{Captions: &no}); s.Captions != nil {
		t.Fatal("asked for none: none")
	}

	// Saved vertical, then the brief turns widescreen: cut again, it stays the cut's shape.
	in := CallCutIn{CallRefIn: CallRefIn{Conversation: cv.Conversation.ID}, Style: "podcast", NameTags: new(bool), Apply: true}
	if po := scall[CallPlanOut](t, c.a, "conversation.cut", in); po.Applied == nil || po.Applied.Problem != "" {
		t.Fatalf("not applied: %+v", po)
	}
	if err := c.a.DB.Update(ctx, store.CProjects, c.a.WorkspaceID(), c.project, bson.M{"$unset": bson.M{"brief.deliverables": ""}}); err != nil {
		t.Fatal(err)
	}
	if s := cut(CallCutIn{}); s.Output.Preset != editspec.PresetVertical {
		t.Fatalf("a cut that replaces the vertical cut keeps its shape: %+v", s.Output)
	}
}

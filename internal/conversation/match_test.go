package conversation

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// faces stores a visual index for a recording with a face where the fixture
// draws it (the Mac's index would find it there), every second.
func (c *call) faces(t *testing.T, assetID string, dur float64) {
	t.Helper()
	idx := visual.Index{Version: "test", Source: "original", Step: 1, Duration: dur}
	for at := 0.0; at < dur; at++ {
		idx.Faces = append(idx.Faces, visual.FaceSample{At: at, Boxes: []vision.Box{{0.402, 0.236, 0.195, 0.444}}, Tracks: []int{1}})
	}
	ins := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: c.a.WorkspaceID(), AssetID: assetID, Kind: visual.InsightKind, Provider: "test", Digest: "test",
		Data: visual.ToMap(&idx), CreatedAt: time.Now().UTC()}
	if err := c.a.DB.Insert(context.Background(), store.CInsights, ins); err != nil {
		t.Fatal(err)
	}
}

// The generated call's webcams differ on purpose: Maya's is warm and
// bright, Sam's cool, darker and less saturated. Matching brings Sam's face
// region close to Maya's (measured before and after), and the cut plays
// the correction.
func TestMatchBringsTheWebcamsTogether(t *testing.T) {
	c := newCall(t)
	cv := scall[CallOut](t, c.a, "conversation.create", CallCreateIn{ProjectID: c.project, Members: []CallMemberIn{{AssetID: c.maya, Name: "Maya"}, {AssetID: c.sam, Name: "Sam"}}})
	id := cv.Conversation.ID
	c.faces(t, c.maya, c.tr.Length)
	c.faces(t, c.sam, c.tr.Length)
	po := scall[CallPlanOut](t, c.a, "conversation.cut", CallCutIn{CallRefIn: CallRefIn{Conversation: id}, Style: "podcast", NameTags: new(bool), Apply: true})
	if po.Applied == nil || po.Applied.Problem != "" {
		t.Fatalf("cut: %s", po.Summary)
	}
	out := scall[CallMatchOut](t, c.a, "conversation.match", CallMatchIn{CallRefIn: CallRefIn{Conversation: id}, Reference: "Maya", Apply: true})
	t.Log(out.Summary)
	var sam CallMatchMember
	for _, m := range out.Members {
		t.Logf("%s: %s (ΔE %.1f → %.1f, L* %.1f → %.1f, look %+v, %d frames)", m.Name, m.Text, m.Before, m.After, m.LightBefore, m.LightAfter, m.Look, m.Frames)
		if m.Member == "sam" {
			sam = m
		}
	}
	if sam.Look == nil || sam.Look.Exposure <= 1 || sam.After >= sam.Before/2 || sam.LightAfter > 2 || sam.Frames < 10 || !sam.Faces {
		t.Fatalf("Sam's match: %+v", sam)
	}
	if out.Applied == nil || out.Applied.Problem != "" {
		t.Fatalf("not in the cut: %s", out.Summary)
	}
	after := revisionSpec(t, c, out.Applied.RevisionID)
	var got *editspec.Look
	for _, m := range after.Conversations["call"].Members {
		if m.ID == "sam" {
			got = m.Look
		}
	}
	if got == nil || math.Abs(got.Exposure-sam.Look.Exposure) > 1e-9 {
		t.Fatalf("the cut's look for Sam: %+v", got)
	}
	// The renderer's matrix is what was measured.
	m := editspec.LookMatrix(got)
	if len(m) != 20 || m[0] <= 1 {
		t.Fatalf("matrix: %v", m)
	}
	// Off again.
	off := scall[CallMatchOut](t, c.a, "conversation.match", CallMatchIn{CallRefIn: CallRefIn{Conversation: id}, Off: true, Apply: true})
	if off.Applied == nil || off.Applied.Problem != "" {
		t.Fatalf("off: %s", off.Summary)
	}
	if s := revisionSpec(t, c, off.Applied.RevisionID); s.Conversations["call"].Members[1].Look != nil {
		t.Fatalf("still corrected: %+v", s.Conversations["call"].Members[1])
	}
}

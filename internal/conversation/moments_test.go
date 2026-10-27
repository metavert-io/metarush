package conversation

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

// Best moments leave out the call talk and lead with the question, its answer
// and the laugh; a short made from one is a new project with a vertical cut
// of its own (captions with names), or the episode's stretch nested.
func TestMomentsAndShorts(t *testing.T) {
	c := newCall(t)
	cv := scall[CallOut](t, c.a, "conversation.create", CallCreateIn{ProjectID: c.project, Members: []CallMemberIn{{AssetID: c.maya, Name: "Maya"}, {AssetID: c.sam, Name: "Sam"}}})
	id := cv.Conversation.ID
	ep := scall[CallPlanOut](t, c.a, "conversation.cut", CallCutIn{CallRefIn: CallRefIn{Conversation: id}, Style: "podcast", Apply: true})
	if ep.Applied == nil || ep.Applied.Problem != "" {
		t.Fatalf("episode: %s", ep.Summary)
	}
	mo := scall[CallMomentsOut](t, c.a, "conversation.moments", CallMomentsIn{CallRefIn: CallRefIn{Conversation: id}, Min: 5, Max: 20})
	t.Log(mo.Summary)
	for _, m := range mo.Moments {
		t.Logf("%.2f %s %v | %s", m.Score, m.When, m.Why, m.Text)
		if strings.Contains(m.Text, "can you hear me") {
			t.Fatalf("call talk is not a moment: %+v", m)
		}
	}
	if len(mo.Moments) < 2 || !strings.HasPrefix(mo.Moments[0].First, "What surprised you the most?") || !strings.Contains(strings.Join(mo.Moments[0].Why, ","), "a question and its answer") {
		t.Fatalf("best moment: %+v", mo.Moments)
	}
	best := mo.Moments[0]
	// Without apply nothing is made.
	dry := scall[CallShortOut](t, c.a, "conversation.short", CallShortIn{CallRefIn: CallRefIn{Conversation: id}, From: best.From, To: best.To})
	if dry.ProjectID != "" || !strings.HasPrefix(dry.Summary, "Ready: a vertical short") {
		t.Fatalf("dry run: %+v", dry)
	}
	sh := scall[CallShortOut](t, c.a, "conversation.short", CallShortIn{CallRefIn: CallRefIn{Conversation: id}, From: best.From, To: best.To, Apply: true})
	t.Log(sh.Summary)
	if sh.ProjectID == "" || sh.Applied == nil || sh.Applied.Problem != "" {
		t.Fatalf("short: %+v", sh)
	}
	s := revisionSpec(t, c, sh.Applied.RevisionID)
	if s.Output.Preset != editspec.PresetVertical || s.Captions == nil || !s.Captions.Enabled || len(s.Clips) == 0 || !editspec.IsConversation(s.Clips[0].AssetID) {
		t.Fatalf("short's cut: %+v", s)
	}
	if s.Clips[0].In < best.From-0.01 || s.Clips[len(s.Clips)-1].Out > best.To+0.01 {
		t.Fatalf("short's stretch %.2f–%.2f, moment %.2f–%.2f", s.Clips[0].In, s.Clips[len(s.Clips)-1].Out, best.From, best.To)
	}
	// Nested: the episode's own stretch.
	nest := scall[CallShortOut](t, c.a, "conversation.short", CallShortIn{CallRefIn: CallRefIn{Conversation: id}, From: best.From, To: best.To, Nest: true, Apply: true})
	if nest.Applied == nil || nest.Applied.Problem != "" {
		t.Fatalf("nested short: %+v %+v", nest.Summary, nest.Result)
	}
	ns := revisionSpec(t, c, nest.Applied.RevisionID)
	if len(ns.Clips) != 1 || !strings.HasPrefix(ns.Clips[0].AssetID, editspec.NestedPrefix) || ns.Output.Preset != editspec.PresetLandscape {
		t.Fatalf("nested short's cut: %+v", ns)
	}
	// The conversation knows the new projects play it.
	got := scall[CallOut](t, c.a, "conversation.show", CallRefIn{Conversation: id})
	if len(got.Conversation.ProjectIDs) != 3 {
		t.Fatalf("projects: %v", got.Conversation.ProjectIDs)
	}

	// Settings on the episode's cut: microphones always on, no name tags, Sam's picture cropped.
	off := false
	set := scall[CallPlanOut](t, c.a, "conversation.settings", CallSettingsIn{Project: c.project, Quieter: &off, NameTags: &off, Member: "Sam", Crop: &editspec.Crop{Bottom: 0.1}, Apply: true})
	if set.Applied == nil || set.Applied.Problem != "" {
		t.Fatalf("settings: %s", set.Summary)
	}
	es := revisionSpec(t, c, set.Applied.RevisionID)
	pin := es.Conversations["call"]
	if pin.Gate != nil || pin.NameTags != "off" || pin.Members[1].Crop == nil || pin.Members[1].Crop.Bottom != 0.1 {
		t.Fatalf("settings in the cut: %+v", pin)
	}
	on := true
	depth := 12.0
	set = scall[CallPlanOut](t, c.a, "conversation.settings", CallSettingsIn{Project: c.project, Quieter: &on, Depth: &depth, Apply: true})
	es = revisionSpec(t, c, set.Applied.RevisionID)
	if pin := es.Conversations["call"]; pin.GateDepth() != 12 || !strings.Contains(set.Summary, "12 dB quieter") {
		t.Fatalf("depth: %s %+v", set.Summary, pin.Gate)
	}
}

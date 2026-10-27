package assistant

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Written scene descriptions: off until a person turns them on; then Claude
// Code (here the fake) writes a sentence or two per scene into its proposals
// folder, MetaRush keeps them (searchable), and the conversation is archived.
// Claude in a session can save its own descriptions through the same op.
func TestDescribeScenes(t *testing.T) {
	a := testApp(t, "")
	scenes := `[{"from":0,"to":6,"text":"A woman with glasses speaks into a microphone."},
	            {"from":6,"to":14,"text":"A slow look along a beach with palm trees."},
	            {"from":14,"to":30,"text":"A man in a blue flight suit walks into a grey room."}]`
	m := withFake(t, a, fakeclaude.Options{Script: &fakeclaude.Script{Default: &fakeclaude.Turn{Steps: []fakeclaude.Step{
		{Write: &fakeclaude.File{Name: DescribeFile, Content: scenes}},
		{Text: "Described 3 scenes."},
	}}}})
	_ = m
	ctx := context.Background()
	prj, asset, _ := fixture(t, a)

	// Off by default: asking describes nothing and says why.
	out, err := ops.CallTyped[DescribeOut](ctx, a, "footage.describe", DescribeIn{AssetID: asset}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Jobs) != 0 || len(out.Skipped) != 1 || !strings.Contains(out.Summary, "off") {
		t.Fatalf("off by default: %+v", out)
	}
	// Turning it on is for people (it uses their Claude Code usage).
	on := true
	if _, err := ops.CallTyped[DescribeSettingsOut](ctx, a, "footage.describe.configure", DescribeConfigureIn{ProjectID: prj, On: &on}, ops.Caller{}); !apperr.Is(err, apperr.HumanOnly) {
		t.Fatalf("configure without a person: %v", err)
	}
	st, err := ops.CallTyped[DescribeSettingsOut](ctx, a, "footage.describe.configure", DescribeConfigureIn{ProjectID: prj, On: &on}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if !st.On || st.Workspace || st.Project == nil || !*st.Project || !strings.Contains(st.Note, "Claude Code usage") {
		t.Fatalf("settings: %+v", st)
	}
	// Queued for this machine (its Claude Code).
	out, err = ops.CallTyped[DescribeOut](ctx, a, "footage.describe", DescribeIn{AssetID: asset}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Jobs) != 1 {
		t.Fatalf("queued: %+v", out)
	}
	j, err := a.Queue.Get(ctx, out.Jobs[0])
	if err != nil || j.Type != jobs.TypeDescribe || j.Installation != a.Cfg.Installation.ID {
		t.Fatalf("pinned describe job: %+v %v", j, err)
	}
	// The run (what the worker does): Claude writes the file, MetaRush keeps the scenes.
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), asset, &as); err != nil {
		t.Fatal(err)
	}
	run, err := ops.DescribeRunner(ctx, a, &as, prj)
	if err != nil {
		t.Fatal(err)
	}
	if len(run.Scenes) != 3 {
		t.Fatalf("scenes: %+v", run.Scenes)
	}
	if n, err := ops.SaveDescriptions(ctx, a, &as, run.Scenes, "claude-code", run.Model); err != nil || n != 3 {
		t.Fatalf("save: %d %v", n, err)
	}
	d, err := m.Get(ctx, run.ConversationID, false, false)
	if err != nil || !d.Archived {
		t.Fatalf("the describe conversation is archived when done: %+v %v", d, err)
	}
	if !strings.Contains(d.FirstPrompt, as.ID) || !strings.Contains(d.FirstPrompt, "footage sheet") {
		t.Fatalf("prompt: %s", d.FirstPrompt)
	}
	// Searchable, and part of the readout.
	s, err := ops.CallTyped[ops.SearchOut](ctx, a, "search", ops.SearchIn{Query: "palm trees"}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Visuals) != 1 || s.Visuals[0].MatchedIn != "description" || s.Visuals[0].From != 6 {
		t.Fatalf("search: %+v", s.Visuals)
	}
	v, err := ops.CallTyped[ops.VisualsOut](ctx, a, "footage.visuals", ops.VisualsIn{AssetID: asset}, ops.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	if v.Described == nil || len(v.Described.Scenes) != 3 {
		t.Fatalf("readout descriptions: %+v", v.Described)
	}
	// Claude in a session saves its own descriptions through the same op (setting off or on).
	off := false
	if _, err := ops.CallTyped[DescribeSettingsOut](ctx, a, "footage.describe.configure", DescribeConfigureIn{ProjectID: prj, On: &off}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	saved, err := ops.CallTyped[DescribeOut](ctx, a, "footage.describe", DescribeIn{AssetID: asset, Scenes: []ops.SceneDescription{{From: 0, To: 30, Text: "  One long  take of a kitchen.  "}}}, ops.Caller{})
	if err != nil || saved.Saved != 1 {
		t.Fatalf("save from a session: %+v %v", saved, err)
	}
	if got := ops.LoadDescriptions(ctx, a, asset); got == nil || len(got.Scenes) != 1 || got.Scenes[0].Text != "One long take of a kitchen." {
		t.Fatalf("replaced and tidied: %+v", got)
	}
	// Nonsense is refused with a plain reason.
	if _, err := ops.CallTyped[DescribeOut](ctx, a, "footage.describe", DescribeIn{AssetID: asset, Scenes: []ops.SceneDescription{{From: 9, To: 3, Text: "x"}}}, ops.Caller{}); !apperr.Is(err, apperr.InvalidInput) {
		t.Fatalf("bad times: %v", err)
	}
	// The dashboard assistant cannot save or configure (it proposes, people apply).
	ra := *a
	ra.Actor.Via = models.ViaAssistant
	if _, err := ops.CallTyped[DescribeOut](ctx, &ra, "footage.describe", DescribeIn{AssetID: asset}, ops.Caller{}); !apperr.Is(err, apperr.Unauthorized) {
		t.Fatalf("assistant channel: %v", err)
	}
	var n int64
	n, _ = a.DB.C(store.CInsights).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": asset, "kind": ops.DescriptionKind})
	if n != 1 {
		t.Fatalf("one description insight per clip: %d", n)
	}
}

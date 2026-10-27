package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/conversation"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// callFix is a project with a call: two webcam recordings of one talk with
// word-timed transcripts (no media files: nothing here plays or processes
// them), a conversation of them that starts together with four clean-up
// suggestions, an automatic podcast cut as the latest version, and a music
// file in the workspace.
type callFix struct {
	project, conv, maya, sam, music, head string
}

func callFixture(t *testing.T, a *app.App) callFix {
	t.Helper()
	ctx := context.Background()
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "Garden show"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	f := callFix{project: p.Project.ID}
	now := time.Now().UTC()
	asset := func(name, title string, video bool, dur float64) string {
		as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{f.project}, Kind: "video", Name: name, Title: title,
			Size: 4096, MIME: "video/mp4", StorageKey: "k-" + name, State: models.AssetAvailable,
			Media:    models.MediaInfo{Container: "mp4", Duration: dur, HasVideo: video, HasAudio: true, Width: 1280, Height: 720, FPS: 30, Streams: []models.Stream{}},
			Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
		if !video {
			as.Kind, as.MIME, as.Media.Width, as.Media.Height, as.Media.FPS = "audio", "audio/mp4", 0, 0, 0
		}
		as.SHA256 = fmt.Sprintf("%x", sha256.Sum256([]byte(as.ID)))
		if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
			t.Fatal(err)
		}
		return as.ID
	}
	f.maya = asset("riverside_maya_raw-video-cfr_garden_show_0001.mp4", "", true, 62)
	f.sam = asset("riverside_sam_raw-video-cfr_garden_show_0002.mp4", "", true, 62)
	f.music = asset("upbeat-bed.m4a", "Upbeat bed", false, 95)
	// Turns, each on its speaker's own recording (the same clock: they start together).
	type turn struct {
		who      string
		from, to float64
		text     string
	}
	turns := []turn{
		{"maya", 0.4, 2.2, "Hi Sam, can you hear me okay?"},
		{"sam", 2.6, 5.0, "Yes, I can hear you now. Sorry, I was on mute."},
		{"maya", 5.4, 11.8, "Great. Welcome to the garden show. Today we are talking about tomatoes on a balcony."},
		{"sam", 12.3, 24.0, "Thanks for having me. I have been growing them on my balcony for ten years. The trick is a deep pot, good soil and a lot of sun."},
		{"maya", 28.5, 30.4, "What surprised you the most?"},
		{"sam", 30.9, 41.5, "That cherry tomatoes grow almost anywhere, even in a bucket. My neighbor laughed at me, and then she started her own."},
		{"maya", 42.0, 50.5, "Let us look at your setup. Where do the boxes come from, and how deep are they?"},
		{"sam", 51.0, 61.0, "I built the boxes myself from old pallets. Each one is about forty centimeters deep, which is plenty for the roots."},
	}
	segs := map[string]int{}
	for _, tu := range turns {
		asset := map[string]string{"maya": f.maya, "sam": f.sam}[tu.who]
		toks := strings.Fields(tu.text)
		var ws []models.Word
		step := (tu.to - tu.from) / float64(len(toks))
		for i, w := range toks {
			a0 := tu.from + step*float64(i)
			ws = append(ws, models.Word{Text: w, From: math.Round(a0*1000) / 1000, To: math.Round((a0+step-0.04)*1000) / 1000})
		}
		seg := models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: asset, Index: segs[asset], From: tu.from, To: tu.to, Text: tu.text,
			Speaker: "speaker_0", Words: ws, Provider: "test", Model: "truth", CreatedAt: now}
		segs[asset]++
		if err := a.DB.Insert(ctx, store.CSegments, seg); err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{f.maya, f.sam} {
		st := models.TranscriptStatus{State: "available", Provider: "test", Precision: "word", Language: "en", Segments: segs[id], Speakers: 1, UpdatedAt: now}
		if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), id, bson.M{"$set": bson.M{"transcript": st}}); err != nil {
			t.Fatal(err)
		}
	}
	cc, err := ops.CallTyped[conversation.CallOut](ctx, a, "conversation.create", conversation.CallCreateIn{ProjectID: f.project, Name: "Garden call", Align: "together",
		Members: []conversation.CallMemberIn{{AssetID: f.maya, Name: "Maya"}, {AssetID: f.sam, Name: "Sam"}}}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	f.conv = cc.Conversation.ID
	var sam string
	for _, m := range cc.Conversation.Members {
		if m.Name == "Sam" {
			sam = m.ID
		}
	}
	sugg := []models.CleanupSuggestion{
		{ID: "c1", Kind: conversation.KindCallTalk, From: 0.4, To: 2.2, Text: "Talk about the call itself, not for the audience.", Quote: "Hi Sam, can you hear me okay?", Fix: conversation.FixCut, State: "open"},
		{ID: "c2", Kind: conversation.KindDeadAir, From: 24.0, To: 28.5, Text: "Nobody speaks for 4.5 s.", Fix: conversation.FixCut, Keep: 0.6, State: "open"},
		{ID: "c3", Kind: conversation.KindFreeze, Member: sam, From: 33.0, To: 35.0, Text: "Sam's picture freezes for 2 s.", Fix: conversation.FixSwitch, State: "open"},
		{ID: "c4", Kind: conversation.KindChrome, Member: sam, From: 0, To: 62, Text: "Sam's picture shows the app's labels (Sam): crop the bottom 10% off.", Fix: conversation.FixCrop,
			Crop: &editspec.Crop{Bottom: 0.1}, Quote: "Sam", State: "open"},
	}
	if err := a.DB.Update(ctx, store.CConversations, a.WorkspaceID(), f.conv, bson.M{"$set": bson.M{"cleanup": sugg}}); err != nil {
		t.Fatal(err)
	}
	cut, err := ops.CallTyped[conversation.CallPlanOut](ctx, a, "conversation.cut", conversation.CallCutIn{CallRefIn: conversation.CallRefIn{Conversation: f.conv}, Style: "podcast", Apply: true}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if cut.Applied == nil || cut.Applied.RevisionID == "" {
		t.Fatalf("the fixture's cut: %+v", cut)
	}
	// A music bed placed by hand, so changing its level needs nothing analysed.
	plan, _ := json.Marshal(editspec.Plan{BaseRevisionID: cut.Applied.RevisionID, Instruction: "music bed", Ops: []editspec.Op{{Op: "addAudio",
		Track: &editspec.AudioTrack{ID: "music", AssetID: f.music, Role: editspec.RoleMusic, In: 0, Out: 40, At: 0}}}})
	ap, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: plan}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	f.head = ap.Revision.ID
	return f
}

// cardsOf indexes a conversation's cards by command and checks every card
// speaks plainly: no IDs, flags, commands, paths, codes or jargon in what the
// person reads.
func cardsOf(t *testing.T, cv *Conversation) (map[string][]AssistantAction, []string) {
	t.Helper()
	by := map[string][]AssistantAction{}
	var kinds []string
	for _, act := range cv.Actions {
		if testing.Verbose() {
			t.Logf("%s [%s %s] %q %q %q", act.Kind, act.State, act.Op, act.Title, act.Details, act.Unavailable)
		}
		by[act.Kind] = append(by[act.Kind], act)
		kinds = append(kinds, act.Kind)
		texts := append([]string{act.Title, act.Unavailable}, act.Details...)
		for _, s := range texts {
			for _, bad := range []string{"rev_", "ast_", "prj_", "cnv_", "--", "LUFS", "ducking", "DNSMOS", "de-ess"} {
				if strings.Contains(s, bad) {
					t.Errorf("card %q says %q (%q)", act.Kind, s, bad)
				}
			}
		}
	}
	return by, kinds
}

// actAsPerson runs a card as a person on the dashboard (the human channel).
func actAsPerson(t *testing.T, a *app.App, convID, actionID string) AssistantAction {
	t.Helper()
	dash := *a
	dash.Actor.Via = "dashboard"
	in, _ := json.Marshal(ConversationActIn{ID: convID, ActionID: actionID, Quiet: true})
	res, err := ops.Call(context.Background(), &dash, "assistant.act", in, ops.Caller{Human: true, Remote: true})
	if err != nil {
		t.Fatal(err)
	}
	return res.(*ConversationActOut).Action
}

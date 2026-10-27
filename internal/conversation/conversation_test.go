package conversation

import (
	"context"
	"encoding/json"
	"math"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/worker"
)

// call is the generated online call ingested into a test workspace: each
// recording's evidence (loudness) measured and its transcript the truth's
// words (spread evenly over each turn), with one word of Maya's that Sam's
// microphone picked up transcribed as a second voice in his recording.
type call struct {
	a       *app.App
	project string
	tr      truth
	maya    string
	sam     string
	gallery string
}

func newCall(t *testing.T) *call {
	t.Helper()
	gen, tr := fixtures(t)
	testTools(t)
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	_ = a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: now})
	pid := ids.New(ids.Project)
	if err := a.DB.Insert(ctx, store.CProjects, models.Project{ID: pid, WorkspaceID: ws, Name: "Garden show", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	c := &call{a: a, project: pid, tr: tr}
	add := func(file string) string {
		r := ingest.File(ctx, a, filepath.Join(gen, file), ingest.Options{ProjectID: pid, Via: "cli"})
		if r.AssetID == "" {
			t.Fatalf("ingest %s: %+v", file, r)
		}
		return r.AssetID
	}
	c.maya, c.sam, c.gallery = add("maya.mov"), add("sam.mov"), add("gallery.mp4")
	for _, id := range []string{c.maya, c.sam, c.gallery} {
		c.runJobs(t, id, "evidence")
	}
	// Transcripts: each person's turns on their recording's clock.
	samClock := func(t float64) float64 { return (t - tr.Members[1].Start) * (1 + tr.Members[1].Drift/1e6) }
	var jt, st []turnAt
	for _, x := range tr.Turns {
		switch x.Speaker {
		case "Maya":
			jt = append(jt, turnAt{x.Start, x.End, x.Text, "speaker_0"})
		case "Sam":
			st = append(st, turnAt{samClock(x.Start), samClock(x.End), x.Text, "speaker_0"})
		}
		if strings.HasPrefix(x.Text, "Great. Welcome") {
			// Sam's microphone heard "Welcome to the garden show" from his speakers, late.
			st = append(st, turnAt{samClock(x.Start + 0.5 + tr.Delay), samClock(x.Start + 2.4 + tr.Delay), "Welcome to the garden show.", "speaker_1"})
		}
	}
	c.transcript(t, c.maya, jt)
	c.transcript(t, c.sam, st)
	return c
}

type turnAt struct {
	from, to float64
	text     string
	label    string
}

// transcript stores turns as word-timed segments (words spread over each turn by length).
func (c *call) transcript(t *testing.T, assetID string, turns []turnAt) {
	t.Helper()
	ctx := context.Background()
	words := 0
	for i, tu := range turns {
		toks := strings.Fields(tu.text)
		total := 0
		for _, w := range toks {
			total += len(w) + 1
		}
		var ws []models.Word
		acc := 0
		for _, w := range toks {
			a := tu.from + (tu.to-tu.from)*float64(acc)/float64(total)
			acc += len(w) + 1
			b := tu.from + (tu.to-tu.from)*float64(acc)/float64(total) - 0.03
			ws = append(ws, models.Word{Text: w, From: math.Round(a*1000) / 1000, To: math.Round(b*1000) / 1000})
		}
		words += len(ws)
		seg := models.TranscriptSegment{ID: ids.New("seg"), WorkspaceID: c.a.WorkspaceID(), AssetID: assetID, Index: i, From: tu.from, To: tu.to, Text: tu.text,
			Speaker: tu.label, Words: ws, Provider: "test", Model: "truth", CreatedAt: time.Now().UTC()}
		if err := c.a.DB.Insert(ctx, store.CSegments, seg); err != nil {
			t.Fatal(err)
		}
	}
	st := models.TranscriptStatus{State: "available", Provider: "test", Precision: "word", Language: "en", Segments: len(turns), Words: words, Speakers: 1, UpdatedAt: time.Now().UTC()}
	if err := c.a.DB.Update(ctx, store.CAssets, c.a.WorkspaceID(), assetID, bson.M{"$set": bson.M{"transcript": st}}); err != nil {
		t.Fatal(err)
	}
}

// runJobs runs an asset's queued jobs of one type here.
func (c *call) runJobs(t *testing.T, assetID, typ string) {
	t.Helper()
	ctx := context.Background()
	js, err := c.a.Queue.List(ctx, bson.M{"assetId": assetID, "type": typ}, 10)
	if err != nil {
		t.Fatal(err)
	}
	for _, j := range js {
		w := worker.New(c.a)
		w.OnlyJobID, w.IgnoreBattery, w.AcceptsHeavy = j.ID, true, true
		w.Log = func(string, ...any) {}
		if err := w.Run(ctx); err != nil {
			t.Fatal(err)
		}
		if got, _ := c.a.Queue.Get(ctx, j.ID); got.State != models.JobSucceeded {
			t.Fatalf("%s job for %s: %s %s", typ, assetID, got.State, got.Error)
		}
	}
}

func scall[O any](t *testing.T, a *app.App, name string, in any) *O {
	t.Helper()
	out, err := ops.CallTyped[O](context.Background(), a, name, in, ops.Caller{Human: true})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	return out
}

func TestCreateLinesUpTheCall(t *testing.T) {
	c := newCall(t)
	out := scall[CallOut](t, c.a, "conversation.create", CallCreateIn{ProjectID: c.project, Members: []CallMemberIn{{AssetID: c.maya, Name: "Maya"}, {AssetID: c.sam, Name: "Sam"}}})
	v := out.Conversation
	if len(v.Members) != 2 || v.Members[0].ID != "maya" || v.Members[1].ID != "sam" {
		t.Fatalf("members: %+v", v.Members)
	}
	got := v.Members[1].Start - v.Members[0].Start
	t.Logf("%s | sam starts %.4f after maya (truth %.3f), %s", out.Summary, got, c.tr.Members[1].Start, v.Members[1].Text)
	if math.Abs(got-c.tr.Members[1].Start) > frame {
		t.Fatalf("sam's start: %.4f, truth %.3f", got, c.tr.Members[1].Start)
	}
	if v.Members[1].Method != "two-way" || v.Sure == "unsure" {
		t.Fatalf("method %s, %s", v.Members[1].Method, v.Sure)
	}
	// With the gallery view (the call's mix) as the reference.
	out = scall[CallOut](t, c.a, "conversation.create", CallCreateIn{ProjectID: c.project, Name: "With the gallery", Members: []CallMemberIn{{AssetID: c.maya}, {AssetID: c.sam}, {AssetID: c.gallery, Use: "picture"}}})
	v = out.Conversation
	var j, s, g CallMemberInfo
	for _, m := range v.Members {
		switch m.AssetID {
		case c.maya:
			j = m
		case c.sam:
			s = m
		case c.gallery:
			g = m
		}
	}
	t.Logf("gallery reference: maya %.4f, sam %.4f, gallery %.4f (%s)", j.Start, s.Start, g.Start, s.Method)
	if g.Method != "reference" || math.Abs((g.Start-j.Start)-c.tr.Members[2].Start) > frame || math.Abs((s.Start-j.Start)-(c.tr.Members[1].Start+c.tr.Delay)) > frame {
		t.Fatalf("against the mix: %+v", v.Members)
	}
	// A nudge moves one member and says so; showing it again agrees.
	n := scall[CallOut](t, c.a, "conversation.nudge", CallNudgeIn{CallRefIn: CallRefIn{Conversation: v.ID}, Member: "sam", Frames: 1})
	if d := n.Conversation.Members[1].Start - s.Start; math.Abs(d-1.0/30) > 0.0011 {
		t.Fatalf("a frame later: %.4f (%s)", d, n.Summary)
	}
}

func TestAutoCutPlaysEveryoneAndFollowsTheSpeaker(t *testing.T) {
	c := newCall(t)
	cv := scall[CallOut](t, c.a, "conversation.create", CallCreateIn{ProjectID: c.project, Members: []CallMemberIn{{AssetID: c.maya, Name: "Maya"}, {AssetID: c.sam, Name: "Sam"}}})
	po := scall[CallPlanOut](t, c.a, "conversation.cut", CallCutIn{CallRefIn: CallRefIn{Conversation: cv.Conversation.ID}, Style: "podcast", Apply: true})
	if po.Applied == nil || po.Applied.Problem != "" {
		t.Fatalf("not applied: %s %+v %+v", po.Summary, po.Applied, po.Result.Check.Errors)
	}
	for _, w := range po.Why {
		t.Log(w)
	}
	plan := scall[editspec.RenderPlan](t, c.a, "revision.plan", ops.RevisionPlanIn{ID: po.Applied.RevisionID})
	if len(plan.Clips) != 1 || plan.Clips[0].Conversation == nil || len(plan.Clips[0].Conversation.Shots) < 4 {
		t.Fatalf("clips: %+v", plan.Clips)
	}
	shots := plan.Clips[0].Conversation.Shots
	if shots[0].Kind != "layout" {
		t.Fatalf("the podcast opens on both: %+v", shots[0])
	}
	// Every person's own microphone plays the whole clip.
	var members []string
	for _, au := range plan.Audio {
		if au.Conversation != "" {
			members = append(members, au.Member)
		}
	}
	if strings.Join(members, ",") != "maya,sam" {
		t.Fatalf("member sounds: %v", members)
	}
	// While Sam answers (about 0:20), the picture shows Sam (or a reaction) and
	// never cuts inside a word; at the laugh both are on screen.
	fps := float64(plan.FPS)
	at := func(t0 float64) editspec.PlanShot {
		f := int(math.Round(t0 * fps))
		for i := len(shots) - 1; i >= 0; i-- {
			if shots[i].From <= f {
				return shots[i]
			}
		}
		return shots[0]
	}
	if sh := at(16); sh.Kind != "member" || sh.Tiles[0].Member != "sam" {
		t.Fatalf("at 0:16 Sam is answering: %+v", sh)
	}
	if sh := at(32.2); sh.Kind != "layout" {
		t.Fatalf("at the laugh both are on screen: %+v", sh)
	}
	// The spec spells out each choice.
	var spec editspec.Spec
	var rev models.Revision
	if err := c.a.DB.GetByID(context.Background(), store.CRevisions, c.a.WorkspaceID(), po.Applied.RevisionID, &rev); err != nil {
		t.Fatal(err)
	}
	spec = rev.Spec
	for _, sh := range spec.Clips[0].Shots {
		if sh.Rationale == "" {
			t.Fatalf("a shot without its reason: %+v", sh)
		}
	}
	b, _ := json.Marshal(spec.Conversations["call"].Gate)
	if !strings.Contains(string(b), `"speaking"`) {
		t.Fatalf("new conversation cuts gate by who is speaking: %s", b)
	}
}

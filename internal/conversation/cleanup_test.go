package conversation

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// Clean-up finds what the generated call leaves behind — the opening "can you
// hear me?" exchange, the long pause after the laugh, Sam's frozen webcam and
// his sound dropout — and accepting them edits the cut: the talk and the
// dropout are cut out, the pause shortened, Maya shown over the freeze.
func TestCleanupFindsAndFixesTheCallsGlitches(t *testing.T) {
	c := newCall(t)
	cv := scall[CallOut](t, c.a, "conversation.create", CallCreateIn{ProjectID: c.project, Members: []CallMemberIn{{AssetID: c.maya, Name: "Maya"}, {AssetID: c.sam, Name: "Sam"}}})
	id := cv.Conversation.ID
	po := scall[CallPlanOut](t, c.a, "conversation.cut", CallCutIn{CallRefIn: CallRefIn{Conversation: id}, Style: "podcast", NameTags: new(bool), Apply: true})
	if po.Applied == nil || po.Applied.Problem != "" {
		t.Fatalf("cut: %s", po.Summary)
	}
	out := scall[CallCleanupOut](t, c.a, "conversation.cleanup", CallCleanupIn{CallRefIn: CallRefIn{Conversation: id}})
	t.Log(out.Summary)
	for _, s := range out.Suggestions {
		t.Logf("%s %-9s %-6s %s %s | %s | %s", s.ID, s.Kind, s.Fix, s.When, s.Who, s.Text, s.Quote)
	}
	for _, n := range out.Notes {
		t.Log("note: " + n)
	}
	by := map[string][]CleanupInfo{}
	for _, s := range out.Suggestions {
		by[s.Kind] = append(by[s.Kind], s)
	}
	// The call talk: Maya's question, Sam's answer and her "Great." — one
	// stretch, up to "Welcome to the garden show".
	talk := by[KindCallTalk]
	if len(talk) != 1 || talk[0].From > 0.7 || talk[0].To < 7 || talk[0].To > 8.5 || !strings.Contains(talk[0].Quote, "can you hear me") || !strings.Contains(talk[0].Quote, "I was on mute") || strings.Contains(talk[0].Quote, "Welcome") {
		t.Fatalf("call talk: %+v", talk)
	}
	// The pause after the laugh (32.7–36.5).
	var pause *CleanupInfo
	for i, s := range by[KindDeadAir] {
		if s.From > 32 && s.To < 37 {
			pause = &by[KindDeadAir][i]
		}
	}
	if pause == nil || pause.To-pause.From < 3 || pause.Keep != deadAirKeep {
		t.Fatalf("dead air: %+v", by[KindDeadAir])
	}
	// Sam's frozen webcam (30–32) and his dropout, on the conversation's clock.
	fr := by[KindFreeze]
	if len(fr) != 1 || fr[0].Member != "sam" || math.Abs(fr[0].From-c.tr.Freeze[0]) > 0.15 || math.Abs(fr[0].To-c.tr.Freeze[1]) > 0.15 || fr[0].Fix != FixSwitch {
		t.Fatalf("freeze (truth %v): %+v", c.tr.Freeze, fr)
	}
	// The dropout swallows part of a word, so it is flagged, not cut.
	dr := by[KindDropout]
	if len(dr) != 1 || dr[0].Member != "sam" || math.Abs(dr[0].From-c.tr.Dropout[0]) > 0.02 || math.Abs(dr[0].To-c.tr.Dropout[1]) > 0.02 || dr[0].Fix != FixFlag {
		t.Fatalf("dropout (truth %v): %+v", c.tr.Dropout, dr)
	}
	t.Logf("dropout found at %.3f–%.3f (truth %.3f–%.3f); freeze %.3f–%.3f (truth %.1f–%.1f)", dr[0].From, dr[0].To, c.tr.Dropout[0], c.tr.Dropout[1], fr[0].From, fr[0].To, c.tr.Freeze[0], c.tr.Freeze[1])

	// Running it again keeps the same suggestions (and their IDs).
	again := scall[CallCleanupOut](t, c.a, "conversation.cleanup", CallCleanupIn{CallRefIn: CallRefIn{Conversation: id}})
	if len(again.Suggestions) != len(out.Suggestions) {
		t.Fatalf("again: %d vs %d", len(again.Suggestions), len(out.Suggestions))
	}

	// Skip one, accept the rest of the four into the cut.
	sk := scall[CallDecideOut](t, c.a, "conversation.decide", CallDecideIn{CallRefIn: CallRefIn{Conversation: id}, IDs: []string{pause.ID}, Decision: "skip"})
	if !strings.Contains(sk.Summary, "Skipped 1 suggestion") {
		t.Fatalf("skip: %s", sk.Summary)
	}
	before := revisionSpec(t, c, po.Applied.RevisionID)
	_, total0 := editspec.Timeline(before)
	ac := scall[CallDecideOut](t, c.a, "conversation.decide", CallDecideIn{CallRefIn: CallRefIn{Conversation: id}, IDs: []string{talk[0].ID, fr[0].ID, dr[0].ID}, Decision: "accept", Apply: true})
	t.Log(ac.Summary)
	if ac.Applied == nil || ac.Applied.Problem != "" {
		t.Fatalf("accept: %s %+v", ac.Summary, ac.Result)
	}
	after := revisionSpec(t, c, ac.Applied.RevisionID)
	_, total1 := editspec.Timeline(after)
	cutSecs := float64(total0-total1) / float64(after.Output.FPS)
	want := talk[0].To - talk[0].From
	if math.Abs(cutSecs-want) > 0.1 {
		t.Fatalf("cut %.2f s out, expected %.2f", cutSecs, want)
	}
	// Maya is on screen over Sam's freeze, and the words "can you hear me" are gone.
	shown := false
	for _, cl := range after.Clips {
		for _, sh := range cl.Shots {
			if sh.Member == "maya" && math.Abs(sh.At-fr[0].From) < 0.01 {
				shown = true
			}
		}
	}
	if !shown {
		t.Fatalf("no shot of Maya over the freeze: %+v", after.Clips)
	}
	words, _, _ := editspec.ExpectedSpeech(after, loadFacts(t, c, after))
	var heard []string
	for _, w := range words {
		heard = append(heard, w.Text)
	}
	if strings.Contains(strings.ToLower(strings.Join(heard, " ")), "can you hear me") {
		t.Fatalf("the call talk still plays: %v", heard[:12])
	}
	// The decisions are on the conversation.
	got := scall[CallOut](t, c.a, "conversation.show", CallRefIn{Conversation: id})
	if got.Conversation.Cleanup.Accepted != 3 || got.Conversation.Cleanup.Skipped != 1 {
		t.Fatalf("decisions: %+v", got.Conversation.Cleanup)
	}
}

func revisionSpec(t *testing.T, c *call, id string) *editspec.Spec {
	t.Helper()
	var rev models.Revision
	if err := c.a.DB.GetByID(context.Background(), store.CRevisions, c.a.WorkspaceID(), id, &rev); err != nil {
		t.Fatal(err)
	}
	s := editspec.Clone(&rev.Spec)
	editspec.Normalize(s)
	return s
}

func loadFacts(t *testing.T, c *call, s *editspec.Spec) editspec.Facts {
	t.Helper()
	f, err := ops.LoadFacts(context.Background(), c.a, c.project, s)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// The app's chrome: text that stays at an edge (a name label at the bottom
// left, a recording banner at the top) is cropped off; text in the middle or
// text that changes (a slide being shared) is not.
func TestChromeIsCroppedOff(t *testing.T) {
	m := editspec.ConversationMember{ID: "sam", Name: "Sam"}
	idx := &visual.Index{Duration: 60, Text: []visual.TextSpan{
		{Text: "Sam", From: 0, To: 60, Box: vision.Box{0.02, 0.906, 0.125, 0.061}},
		{Text: "● Recording...", From: 2, To: 58, Box: vision.Box{0.01, 0.02, 0.23, 0.06}},
		{Text: "TOMATOES", From: 0, To: 60, Box: vision.Box{0.4, 0.45, 0.2, 0.08}},
		{Text: "Slide 3", From: 0, To: 60, Box: vision.Box{0.85, 0.9, 0.1, 0.05}, Changing: true},
		{Text: "brief", From: 10, To: 14, Box: vision.Box{0.85, 0.9, 0.1, 0.05}},
	}}
	s := chrome(m, idx, 60)
	if s == nil || s.Fix != FixCrop || s.Crop == nil {
		t.Fatalf("no crop: %+v", s)
	}
	if math.Abs(s.Crop.Bottom-0.104) > 0.002 || math.Abs(s.Crop.Top-0.09) > 0.002 || s.Crop.Left != 0 || s.Crop.Right != 0 {
		t.Fatalf("crop: %+v", s.Crop)
	}
	if !strings.Contains(s.Text, "crop the top 9% and bottom 10% off") || strings.Contains(s.Quote, "TOMATOES") {
		t.Fatalf("says: %s (%s)", s.Text, s.Quote)
	}
	if chrome(m, &visual.Index{Duration: 60}, 60) != nil {
		t.Fatal("no text, no crop")
	}
}

// A dropout in a pause (nobody's words lost) is cut; one inside a word is
// flagged; digital silence outside someone's talk (a muted microphone) is not
// a dropout.
func TestDropouts(t *testing.T) {
	m := editspec.ConversationMember{ID: "sam", Name: "Sam", Start: 1}
	snd := &sound{db: make([]float32, 3000), dur: 30}
	for k := range snd.db {
		snd.db[k] = -55 // background
	}
	for _, r := range [][2]int{{500, 1500}, {2000, 2600}} { // he talks 5–15 s and 20–26 s of his recording
		for k := r[0]; k < r[1]; k++ {
			snd.db[k] = -25
		}
	}
	silence := func(a, b int) {
		for k := a; k < b; k++ {
			snd.db[k] = -120
		}
	}
	silence(800, 830)   // 8.0–8.3 s: in a pause between words
	silence(1200, 1240) // 12.0–12.4 s: inside a word
	silence(1700, 1800) // 17–18 s: between his turns (his microphone muted)
	silence(2300, 2304) // 40 ms: too short to matter
	v := &editspec.ConversationView{
		Activity: map[string][]editspec.Span{"sam": {{From: 6, To: 16}, {From: 21, To: 27}}},
		Words: []editspec.TranscriptSegment{{Speaker: "Sam", From: 6, To: 16, Words: []editspec.TranscriptWord{
			{Text: "well", From: 8.6, To: 9.0}, {Text: "tomatoes", From: 12.9, To: 13.6}, {Text: "grow", From: 13.7, To: 14}}}},
	}
	got := dropouts(m, snd, v)
	if len(got) != 2 {
		t.Fatalf("dropouts: %+v", got)
	}
	if got[0].Fix != FixCut || math.Abs(got[0].From-9) > 0.02 || math.Abs(got[0].To-9.3) > 0.02 {
		t.Fatalf("in a pause: %+v", got[0])
	}
	if got[1].Fix != FixFlag || !strings.Contains(got[1].Text, "in the middle of a word") {
		t.Fatalf("inside a word: %+v", got[1])
	}
}

// Call talk is found sentence by sentence, with the short reply to it.
func TestCallTalk(t *testing.T) {
	seg := func(who string, from float64, text string) editspec.TranscriptSegment {
		var ws []editspec.TranscriptWord
		at := from
		for _, w := range strings.Fields(text) {
			ws = append(ws, editspec.TranscriptWord{Text: w, From: at, To: at + 0.3})
			at += 0.35
		}
		return editspec.TranscriptSegment{Speaker: who, From: from, To: at, Text: text, Words: ws}
	}
	segs := []editspec.TranscriptSegment{
		seg("Ana", 0, "Okay. Are we recording? I think we’re recording."),
		seg("Bo", 3, "Yep, all good."),
		seg("Ana", 5, "Welcome to the show. Today we talk about bread."),
		seg("Bo", 9, "Thanks. Oh sorry, you're breaking up a bit. Anyway, bread is great."),
		seg("Ana", 40, "So what did you hear me say about it?"),
	}
	got := callTalk(segs)
	if len(got) != 2 {
		t.Fatalf("found: %+v", got)
	}
	if got[0].From > 0.4 || got[0].To < 4 || got[0].To > 5 || !strings.Contains(got[0].Quote, "Yep, all good.") {
		t.Fatalf("recording check and reply: %+v", got[0])
	}
	if got[1].Quote != "Oh sorry, you're breaking up a bit." {
		t.Fatalf("breaking up: %+v", got[1])
	}
}

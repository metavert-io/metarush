package ops_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/transcripts"
	"github.com/metavert-io/metarush/internal/voice"
)

// txClip is a clip with a word-timed transcript: one line per call to line().
func txClip(t *testing.T, a *app.App, title string, projectID string, lines ...[]models.Word) models.Asset {
	t.Helper()
	ctx := context.Background()
	as := clip(t, a, title, projectID)
	words := 0
	for i, ws := range lines {
		words += len(ws)
		s := models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Index: i, From: ws[0].From, To: ws[len(ws)-1].To,
			Text: transcripts.JoinWords(ws), Words: ws, Speaker: "speaker_00", Provider: "parakeet", Model: "test", CreatedAt: time.Now().UTC()}
		if err := a.DB.Insert(ctx, store.CSegments, s); err != nil {
			t.Fatal(err)
		}
	}
	st := models.TranscriptStatus{State: "available", Provider: "parakeet", Model: "test", Precision: "word", Language: "en", Segments: len(lines), Words: words, UpdatedAt: time.Now().UTC()}
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"transcript": st}}); err != nil {
		t.Fatal(err)
	}
	as.Transcript = &st
	return as
}

func wd(text string, from, to, p float64) models.Word {
	return models.Word{Text: text, From: from, To: to, Score: p}
}

// captionedCut saves a cut playing the clip's first three seconds with captions on.
func captionedCut(t *testing.T, a *app.App, projectID, assetID string) string {
	t.Helper()
	spec := editspec.Spec{SchemaVersion: 1, Title: "Garden story", Output: editspec.Output{Preset: "vertical-1080p", Width: 1080, Height: 1920, FPS: 30, Platform: "youtube"},
		Clips:    []editspec.Clip{{ID: "talk", AssetID: assetID, In: 0, Out: 3}},
		Captions: &editspec.Captions{Enabled: true, Source: "transcript", MaxWordsPerLine: 6}}
	plan, _ := json.Marshal(editspec.Plan{ProjectID: projectID, Instruction: "test cut", Spec: &spec})
	chk := do[ops.EditCheckOut](t, a, "edit.check", ops.EditCheckIn{Plan: plan})
	if !chk.Valid {
		t.Fatalf("cut not valid: %+v", chk.Errors)
	}
	return do[ops.EditApplyOut](t, a, "edit.apply", ops.EditApplyIn{Plan: plan, ConfirmToken: chk.ConfirmToken}).Revision.ID
}

func captionText(p *editspec.RenderPlan) string {
	if p.Captions == nil {
		return ""
	}
	var lines []string
	for _, l := range p.Captions.Lines {
		lines = append(lines, l.Text)
	}
	return strings.Join(lines, " / ")
}

// A saved version keeps the words it was made with (D146); a fix waits for
// one new version (D148) and then reaches its captions; the old version
// still plays the old words, and so does a version saved before pins.
func TestTextFixesWaitForANewVersion(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := project(t, a, "Garden story")
	as := txClip(t, a, "Interview", p.ID, []models.Word{wd("We", 0.1, 0.3, 0.9), wd("met", 0.35, 0.6, 0.9), wd("Fern", 0.7, 0.9, 0.9), wd("Brook", 0.95, 1.3, 0.3), wd("at", 1.4, 1.5, 0.9), wd("Elmwood.", 1.6, 2.2, 0.9)})
	r1 := captionedCut(t, a, p.ID, as.ID)
	var rev1 models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), r1, &rev1)
	if len(rev1.Transcripts) != 1 || rev1.Transcripts[0].AssetID != as.ID || rev1.Transcripts[0].Hash == "" {
		t.Fatalf("pins %+v", rev1.Transcripts)
	}
	before := do[editspec.RenderPlan](t, a, "revision.plan", ops.RevisionPlanIn{ID: r1})
	if got := captionText(before); got != "We met Fern Brook at Elmwood." {
		t.Fatalf("captions %q", got)
	}
	// Nothing waits yet.
	if pend := do[ops.TranscriptPendingOut](t, a, "transcript.pending", ops.TranscriptPendingIn{ProjectID: p.ID}); pend.Count != 0 || !pend.Latest {
		t.Fatalf("pending %+v", pend)
	}
	// A fix by find and replace (a join), with the word onto the spelling list.
	rep := do[ops.TranscriptReplaceOut](t, a, "transcript.replace", ops.TranscriptReplaceIn{Query: "Fern Brook", With: "FernBrook", ProjectID: p.ID, Spelling: true})
	if rep.Changed != 1 || rep.Spelling == nil || rep.Spelling.Word != "FernBrook" || !strings.Contains(rep.Summary, "FernBrook") {
		t.Fatalf("replace %+v", rep)
	}
	// The version still plays its words: preview and render read the pins.
	if got := captionText(do[editspec.RenderPlan](t, a, "revision.plan", ops.RevisionPlanIn{ID: r1})); got != "We met Fern Brook at Elmwood." {
		t.Fatalf("the saved version changed: %q", got)
	}
	pend := do[ops.TranscriptPendingOut](t, a, "transcript.pending", ops.TranscriptPendingIn{ProjectID: p.ID})
	if pend.Count != 1 || len(pend.Places) != 1 || pend.Places[0].AfterText != "FernBrook" || pend.Places[0].At != 0.7 || !strings.Contains(pend.Summary, "1 text fix since version 1") {
		t.Fatalf("pending %+v", pend)
	}
	// Captions files: the version's words, or today's (no render).
	if f := do[ops.CaptionsFileOut](t, a, "captions.file", ops.CaptionsFileIn{ProjectID: p.ID, Words: "version"}); !strings.Contains(f.Content, "We met Fern Brook") {
		t.Fatalf("version words file %q", f.Content)
	}
	now := do[ops.CaptionsFileOut](t, a, "captions.file", ops.CaptionsFileIn{ProjectID: p.ID, Format: "vtt"})
	if !strings.HasPrefix(now.Content, "WEBVTT") || !strings.Contains(now.Content, "We met FernBrook") || now.Fixes != 1 || now.Note == "" {
		t.Fatalf("today's file %+v", now)
	}
	// The caption check of the version: the fixed word waits; the unsure word is FernBrook's part.
	chk := do[ops.CaptionsCheckOut](t, a, "captions.check", ops.CaptionsCheckIn{ProjectID: p.ID})
	kinds := map[string]int{}
	for _, is := range chk.Issues {
		kinds[is.Kind]++
	}
	if kinds[transcripts.CaptionFixed] == 0 || kinds[transcripts.CaptionName] != 1 {
		t.Fatalf("check %+v", chk.Issues)
	}
	// One new version for the fixes.
	ap := do[ops.TranscriptPendingApplyOut](t, a, "transcript.pending.apply", ops.TranscriptPendingApplyIn{ProjectID: p.ID, NoRender: true})
	if ap.Revision.Number != 2 || ap.Fixes != 1 {
		t.Fatalf("apply %+v", ap)
	}
	if got := captionText(do[editspec.RenderPlan](t, a, "revision.plan", ops.RevisionPlanIn{ID: ap.Revision.ID})); got != "We met FernBrook at Elmwood." {
		t.Fatalf("new version captions %q", got)
	}
	if got := captionText(do[editspec.RenderPlan](t, a, "revision.plan", ops.RevisionPlanIn{ID: r1})); got != "We met Fern Brook at Elmwood." {
		t.Fatalf("old version changed after the new one: %q", got)
	}
	if pend := do[ops.TranscriptPendingOut](t, a, "transcript.pending", ops.TranscriptPendingIn{ProjectID: p.ID}); pend.Count != 0 {
		t.Fatalf("still pending %+v", pend)
	}
	if _, err := ops.CallTyped[ops.TranscriptPendingApplyOut](ctx, a, "transcript.pending.apply", ops.TranscriptPendingApplyIn{ProjectID: p.ID, NoRender: true}, ops.Caller{Human: true}); err == nil {
		t.Fatal("a second new version with nothing waiting")
	}
	// A version saved before pins existed reads the machine's words.
	legacy := rev1
	legacy.ID, legacy.Number, legacy.Transcripts = ids.New(ids.Revision), 99, nil
	if err := a.DB.Insert(ctx, store.CRevisions, legacy); err != nil {
		t.Fatal(err)
	}
	if got := captionText(do[editspec.RenderPlan](t, a, "revision.plan", ops.RevisionPlanIn{ID: legacy.ID})); got != "We met Fern Brook at Elmwood." {
		t.Fatalf("legacy version %q", got)
	}
	// History and undo through the ops.
	h := do[ops.TranscriptHistoryOut](t, a, "transcript.history", ops.TranscriptHistoryIn{AssetID: as.ID})
	if len(h.Entries) != 1 || !h.Entries[0].CanUndo || !strings.Contains(h.Entries[0].Says, "FernBrook") || h.Original == nil {
		t.Fatalf("history %+v", h)
	}
	u := do[ops.TranscriptUndoOut](t, a, "transcript.undo", ops.TranscriptUndoIn{BatchID: rep.BatchID})
	if u.Undone != 1 || !strings.Contains(u.Summary, "Fern Brook") {
		t.Fatalf("undo %+v", u)
	}
	// Undoing is a change version 2 doesn't have: it waits too.
	if pend := do[ops.TranscriptPendingOut](t, a, "transcript.pending", ops.TranscriptPendingIn{ProjectID: p.ID}); pend.Count != 1 || !pend.Places[0].TakenBack {
		t.Fatalf("undo pending %+v", pend)
	}
}

// Fixing the same thing again and again suggests a spelling list entry, as a
// question in the To-dos; answering it puts the word on the list.
func TestRepeatedFixesSuggestASpellingEntry(t *testing.T) {
	a := testapp.New(t)
	p := project(t, a, "Builder story")
	c1 := txClip(t, a, "One", p.ID, []models.Word{wd("ask", 0, 0.2, 0.9), wd("Kestrel", 0.3, 0.6, 0.4), wd("by", 0.65, 1, 0.4)})
	c2 := txClip(t, a, "Two", p.ID, []models.Word{wd("Kestrel", 0.3, 0.6, 0.4), wd("by", 0.65, 1, 0.4)})
	c3 := txClip(t, a, "Three", p.ID, []models.Word{wd("kesselbee", 0.3, 1, 0.4)})
	var last *ops.TranscriptFixOut
	for _, c := range []models.Asset{c1, c2, c3} {
		last = do[ops.TranscriptFixOut](t, a, "transcript.fix", ops.TranscriptFixIn{AssetID: c.ID, From: 0.3, To: 1, Text: "Kestrelby", ProjectID: p.ID})
	}
	if last.Suggestion == nil || last.Suggestion.Word != "Kestrelby" || last.Suggestion.Times != 3 {
		t.Fatalf("suggestion %+v", last)
	}
	list := do[ops.TodoListOut](t, a, "todos.list", ops.TodoListIn{ProjectID: p.ID})
	var q *models.Todo
	for i := range list.Todos {
		if list.Todos[i].Kind == models.TodoSpelling {
			q = &list.Todos[i]
		}
	}
	if q == nil || !strings.Contains(q.Question, "3 times") {
		t.Fatalf("no spelling question: %+v", list.Todos)
	}
	ans := do[ops.TodoAnswerOut](t, a, "todos.answer", ops.TodoAnswerIn{ID: q.ID, Answer: "add"})
	if !strings.Contains(ans.Done, "Kestrelby") {
		t.Fatalf("answer %+v", ans)
	}
	sl := do[ops.SpellingListOut](t, a, "transcript.spelling.list", ops.SpellingListIn{ProjectID: p.ID})
	if len(sl.Entries) != 1 || sl.Entries[0].Word != "Kestrelby" || sl.ReadOnly {
		t.Fatalf("list %+v", sl)
	}
}

// "This part is Lena, not Maya" is a fix like any other (D145): recorded
// with who and when, waiting for a new version, and undone through the
// history, after which the words are the voice's that had them and nothing
// waits any more.
func TestWhoSaysAStretchIsAFixThatCanBeUndone(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := project(t, a, "Garden story")
	as := txClip(t, a, "Interview", p.ID,
		[]models.Word{wd("We", 0.1, 0.3, 0.9), wd("met", 0.35, 0.6, 0.9), wd("at", 0.7, 0.9, 0.9)},
		[]models.Word{wd("the", 1.4, 1.5, 0.9), wd("Maple", 1.6, 1.9, 0.9), wd("Grove.", 1.95, 2.4, 0.9)})
	res := &voice.Result{Raw: 1, Clusters: []voice.Cluster{{Label: "speaker_00", Seconds: 2.3, Turns: 2, First: 0.1}},
		Turns: []voice.Turn{{From: 0.1, To: 0.9, Label: "speaker_00"}, {From: 1.4, To: 2.4, Label: "speaker_00"}}}
	if err := speakers.Store(ctx, a, &as, &speakers.Detection{Run: ids.New(ids.Run), Result: res, Decision: map[string]voice.Decision{}, Raw: 1}, "test"); err != nil {
		t.Fatal(err)
	}
	captionedCut(t, a, p.ID, as.ID)
	names := func() []string {
		tx := do[ops.TranscriptOut](t, a, "footage.transcript", ops.TranscriptIn{AssetID: as.ID})
		var out []string
		for _, s := range tx.Segments {
			n := s.SpeakerName
			if n == "" {
				n = "-"
			}
			out = append(out, n)
		}
		return out
	}
	who := do[ops.TranscriptSpeakerOut](t, a, "transcript.speaker", ops.TranscriptSpeakerIn{SpeakerAssignIn: ops.SpeakerAssignIn{AssetID: as.ID, From: 1.4, To: 2.4, Person: "Lena", NewPerson: true}, ProjectID: p.ID})
	if who.FixID == "" || strings.Join(names(), ",") != "-,Lena" {
		t.Fatalf("the second line is Lena's: %+v %v", who, names())
	}
	h := do[ops.TranscriptHistoryOut](t, a, "transcript.history", ops.TranscriptHistoryIn{AssetID: as.ID})
	if len(h.Entries) != 1 || h.Entries[0].Kind != models.FixSpeaker || !h.Entries[0].CanUndo || !strings.Contains(h.Entries[0].Says, "Lena") {
		t.Fatalf("history %+v", h.Entries)
	}
	if pend := do[ops.TranscriptPendingOut](t, a, "transcript.pending", ops.TranscriptPendingIn{ProjectID: p.ID}); pend.Count != 1 || pend.Places[0].Kind != models.FixSpeaker {
		t.Fatalf("who speaks waits for a new version too: %+v", pend)
	}
	u := do[ops.TranscriptUndoOut](t, a, "transcript.undo", ops.TranscriptUndoIn{FixID: who.FixID})
	if u.Undone != 1 || u.Summary != "Undone: “Speaker 1” says those words again." {
		t.Fatalf("undo %+v", u)
	}
	if got := strings.Join(names(), ","); got != "-,-" {
		t.Fatalf("after the undo nobody named says anything: %s", got)
	}
	var segs []models.TranscriptSegment
	_ = a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": as.ID}, &segs)
	for _, s := range segs {
		if s.Speaker != "speaker_00" {
			t.Fatalf("every line is the clip's own voice again: %+v", s)
		}
	}
	if pend := do[ops.TranscriptPendingOut](t, a, "transcript.pending", ops.TranscriptPendingIn{ProjectID: p.ID}); pend.Count != 0 {
		t.Fatalf("an undone fix waits for nothing: %+v", pend)
	}
}

// Delivery writes captions files where the place takes them; "Update the
// captions file" rewrites them with later fixes, without a new render.
func TestDeliveredCaptionsFilesFollowFixes(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := project(t, a, "Garden story")
	as := txClip(t, a, "Interview", p.ID, []models.Word{wd("We", 0.1, 0.3, 0.9), wd("met", 0.35, 0.6, 0.9), wd("Fern", 0.7, 0.9, 0.9), wd("Brook", 0.95, 1.3, 0.9)})
	rid := captionedCut(t, a, p.ID, as.ID)
	var r models.Revision
	_ = a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rid, &r)
	// A delivered video with its record (as deliver.export writes it).
	dir := t.TempDir()
	sc := ops.DeliverySidecar{Format: "metarush.delivery/1", File: "garden_r1.mp4"}
	sc.Revision.ID, sc.Revision.Number = r.ID, r.Number
	b, _ := json.Marshal(sc)
	if err := os.WriteFile(filepath.Join(dir, "garden_r1.json"), b, 0o644); err != nil {
		t.Fatal(err)
	}
	do[ops.TranscriptFixOut](t, a, "transcript.fix", ops.TranscriptFixIn{AssetID: as.ID, From: 0.7, To: 1.3, Text: "FernBrook"})
	out, err := ops.CallTyped[ops.DeliverCaptionsOut](ctx, a, "deliver.captions", ops.DeliverCaptionsIn{Path: dir}, ops.Caller{Human: true})
	if err != nil || len(out.Files) != 1 || out.Files[0].Fixes != 1 || out.Note == "" {
		t.Fatalf("deliver captions %+v %v", out, err)
	}
	srt, _ := os.ReadFile(filepath.Join(dir, "garden_r1.srt"))
	if !strings.Contains(string(srt), "We met FernBrook") || !strings.Contains(string(srt), "00:00:00,") {
		t.Fatalf("srt %q", srt)
	}
	var after ops.DeliverySidecar
	nb, _ := os.ReadFile(filepath.Join(dir, "garden_r1.json"))
	_ = json.Unmarshal(nb, &after)
	if after.Captions == nil || after.Captions.Words != "now" || after.Captions.Fixes != 1 || !after.Captions.BurnedIn {
		t.Fatalf("record %+v", after.Captions)
	}
}

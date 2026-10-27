package ops

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
	"github.com/metavert-io/metarush/internal/voice"
)

// callView is who the picture shows from when to when in a speaker-view call,
// what their face looks like, the name under it and their voice.
type callView struct {
	from, to float64
	print    []float32
	tile     string
	label    string
}

// speakerCall stores a clip recorded in a call's speaker view: one face at a
// time with a cut at every switch, the name tile at the bottom left, who talks
// meanwhile (the transcript) and the voices (unnamed).
func speakerCall(t *testing.T, a *app.App, projectID, title string, views []callView) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	dur := views[len(views)-1].to
	pids := []string{}
	if projectID != "" {
		pids = []string{projectID}
	}
	as := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: pids, Kind: "video", Name: strings.ToLower(title) + ".mp4", Title: title,
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: dur, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
	insert(t, a, store.CAssets, as)
	var samples []visual.Sample
	var cuts []float64
	for i, v := range views {
		if i > 0 {
			cuts = append(cuts, v.from)
		}
	}
	for s := 0.5; s < dur; s++ {
		f := vision.Frame{W: 1280, H: 720}
		var prints [][]float32
		for _, v := range views {
			if s >= v.from && s < v.to {
				q := 0.7
				f.Faces = append(f.Faces, vision.Face{Conf: 1, Box: vision.Box{0.35, 0.15, 0.3, 0.5}, Quality: &q})
				prints = append(prints, v.print)
				if v.tile != "" {
					f.Text = append(f.Text, vision.Text{S: v.tile, Conf: 0.9, Box: vision.Box{0.02, 0.9, 0.16, 0.05}})
				}
			}
		}
		samples = append(samples, visual.Sample{At: s, Frame: f, Prints: prints})
	}
	idx := visual.Build(samples, 1, dur, cuts, "proxy")
	insert(t, a, store.CInsights, models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind,
		Provider: "apple-vision", Digest: "d", Data: visual.ToMap(idx), CreatedAt: now})
	if err := faces.Save(ctx, a, as.ID, idx); err != nil {
		t.Fatal(err)
	}
	labels := map[string]bool{}
	for i, v := range views {
		insert(t, a, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Index: i, From: v.from, To: v.to - 0.2,
			Speaker: v.label, Text: "We grew tomatoes on the balcony this year.", CreatedAt: now})
		if !labels[v.label] {
			labels[v.label] = true
			insert(t, a, store.CSpeakers, models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Run: "run_1", Label: v.label,
				Seconds: 20, Samples: []models.TimeSpan{{From: v.from, To: v.from + 3}}, CreatedAt: now, UpdatedAt: now})
		}
	}
	return as.ID
}

// alternating is a call where two people take turns, each turn its own view.
func alternating(turns int, first, second callView) []callView {
	var out []callView
	at := 0.0
	for i := 0; i < turns; i++ {
		v := first
		if i%2 == 1 {
			v = second
		}
		d := 4.0 + float64(i%3)
		v.from, v.to = at, at+d
		out = append(out, v)
		at += d
	}
	return out
}

// The grid's questions and one click for the names a call shows (D155–D157):
// one question per person, nothing named or learned before a person accepts,
// the shown names accepted for everyone in the clip at once (adding the people
// nobody had named yet), and the voices then suggested from the faces — never
// named from them.
func TestFaceGroupsAndTheNamesACallShows(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Garden"})
	clip := speakerCall(t, a, p.Project.ID, "Garden call", alternating(12,
		callView{print: lookAlong(0, 40, 0.3), tile: "Ada Park", label: "speaker_00"},
		callView{print: lookAlong(1, 41, 0.3), tile: "Ben Okafor", label: "speaker_01"}))
	if _, err := faces.Candidates(ctx, a, clip); err != nil {
		t.Fatal(err)
	}
	gs := call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{ProjectID: p.Project.ID})
	if len(gs.Groups) != 2 || gs.Summary != "2 people to name in 1 clip." {
		t.Fatalf("one question per person: %s %+v", gs.Summary, gs.Groups)
	}
	for _, g := range gs.Groups {
		if g.Context.Appearances != 6 || len(g.Context.Faces) < 4 || g.Context.Shown == "" || !g.Suggestions[0].New {
			t.Fatalf("seen 6 times, with samples and the name shown: %+v", g.Context)
		}
	}
	if len(gs.Shown) != 1 || gs.Shown[0].Summary != "Use the names shown in “Garden call”: Ada Park, Ben Okafor (adds 2 new people to your people)." &&
		gs.Shown[0].Summary != "Use the names shown in “Garden call”: Ben Okafor, Ada Park (adds 2 new people to your people)." {
		t.Fatalf("one click for the names shown: %+v", gs.Shown)
	}
	// A cut of the first seconds (Ada's) is saved after the questions were
	// asked: hers is marked "In your cut" and comes first, nothing asked again.
	savePlan(t, a, fmt.Sprintf(`{"projectId":%q,"instruction":"the opening","spec":{"schemaVersion":1,"title":"Garden","output":{"preset":"landscape-1080p","fps":30},"clips":[{"id":"open","assetId":%q,"in":0,"out":3}]}}`,
		p.Project.ID, clip))
	gs = call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{ProjectID: p.Project.ID})
	if len(gs.Groups) != 2 || !gs.Groups[0].Context.InCut || gs.Groups[0].Context.Shown != "Ada Park" || gs.Groups[0].Priority != 2 || gs.Groups[1].Context.InCut {
		t.Fatalf("the person in the cut first: %+v", gs.Groups)
	}
	// Nothing is named or learned from what the video shows before a person accepts.
	if ps := call[PeopleListOut](t, a, "people.list", PeopleListIn{}); len(ps.People) != 0 {
		t.Fatalf("nobody added yet: %+v", ps.People)
	}
	for _, f := range call[FacesOut](t, a, "footage.faces", FacesIn{AssetID: clip}).Faces {
		if f.Name != "" || f.Recognized != nil || f.Question == "" || f.Maybe == nil || f.Maybe.By != "shown" {
			t.Fatalf("a face asked about, named by nobody: %+v", f)
		}
	}
	out := call[TodoAcceptShownOut](t, a, "todos.acceptShown", TodoAcceptShownIn{AssetID: clip})
	if len(out.Accepted) != 2 || !out.Accepted[0].Added || out.Accepted[0].Named != 6 ||
		!strings.HasPrefix(out.Summary, "Named 2 people in “Garden call” from the names the video shows:") || !strings.HasSuffix(out.Summary, "2 new people added to your people.") {
		t.Fatalf("accepted: %+v", out)
	}
	for _, f := range call[FacesOut](t, a, "footage.faces", FacesIn{AssetID: clip}).Faces {
		if f.Name == "" || f.Question != "" {
			t.Fatalf("every face named by the one click: %+v", f)
		}
	}
	if gs := call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{}); len(gs.Groups) != 0 {
		t.Fatalf("nothing left to ask: %+v", gs.Groups)
	}
	// The voices are not named from the faces: they are suggested.
	vs, _ := speakers.Voices(ctx, a, clip, false)
	for _, v := range vs {
		if v.PersonID != "" {
			t.Fatalf("a voice was named from a face: %+v", v)
		}
	}
	if err := speakers.Reconcile(ctx, a, clip); err != nil {
		t.Fatal(err)
	}
	var vqs []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"assetId": clip, "context.label": "speaker_00", "kind": models.TodoSpeakerUnnamed, "state": models.TodoOpen}, &vqs); err != nil {
		t.Fatal(err)
	}
	if len(vqs) != 1 || len(vqs[0].Suggestions) == 0 || vqs[0].Suggestions[0].Label != "Ada Park (on screen while this voice talks)" {
		t.Fatalf("the voice's question suggests the face named on screen while it talks: %+v", vqs)
	}
}

// Answering a question about a group of faces through the normal to-do
// operations: the voice heard is a quick pick (D189), and picking it names the
// group (a face that looks less like the others is set aside and asked about
// on its own); "not Ada" on that face keeps it from being asked again; a typed
// name names the other group.
func TestAnswerFaceGroupQuestions(t *testing.T) {
	a := testapp.New(t)
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Interviews"})
	views := alternating(10, callView{print: lookAlong(0, 40, 0.3), label: "speaker_00"}, callView{print: lookAlong(1, 41, 0.3), label: "speaker_01"})
	views[8].print = lookAlong(0, 42, 2.55) // Ada at an odd angle (35% alike): grouped by her voice, but looks less like her other faces
	clip := speakerCall(t, a, p.Project.ID, "Kitchen", views)
	ada := call[SpeakerRenameOut](t, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: clip, Speaker: "speaker_00", Name: "Ada"}).Person
	gs := call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: clip}).Groups
	var adaQ, whoQ models.Todo
	for _, g := range gs {
		switch g.Context.Label {
		case "speaker_00":
			adaQ = g
		case "speaker_01":
			whoQ = g
		}
	}
	if adaQ.ID == "" || whoQ.ID == "" || adaQ.Question != "Who is this?" || adaQ.Context.Appearances != 5 || !strings.Contains(adaQ.Detail, "Ada is talking while this face is on screen") ||
		len(adaQ.Suggestions) == 0 || adaQ.Suggestions[0].Label != "Ada (talking then)" {
		t.Fatalf("Ada's question (her voice a quick pick) and the other's: %+v", gs)
	}
	ans := call[TodoAnswerOut](t, a, "todos.answer", TodoAnswerIn{ID: adaQ.ID, Suggestion: 1})
	if ans.Done != "These 4 faces are Ada now (in “Kitchen”). One face that looks less like the others is left for a question of its own." {
		t.Fatalf("done: %q", ans.Done)
	}
	gs = call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: clip}).Groups
	var aside models.Todo
	for _, g := range gs {
		if g.Context.Appearances == 1 {
			aside = g
		}
	}
	if aside.ID == "" || aside.Question != "Who is this?" || len(aside.Suggestions) == 0 || aside.Suggestions[0].PersonID != ada.ID ||
		!strings.Contains(aside.Detail, "left out when you named Ada's other faces") {
		t.Fatalf("the face set aside is asked about on its own: %+v", gs)
	}
	// Not Ada (and, the voice heard being Ada's, it isn't asked "Who is this?" either).
	call[FaceOut](t, a, "footage.faces.reject", FaceRejectIn{FaceID: aside.Context.FaceID, Person: "Ada"})
	for _, g := range call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: clip}).Groups {
		if g.Context.PersonID == ada.ID || g.Context.Appearances == 1 {
			t.Fatalf("not asked again: %+v", g)
		}
	}
	// The other person: a typed name names all of their faces.
	ans = call[TodoAnswerOut](t, a, "todos.answer", TodoAnswerIn{ID: whoQ.ID, Answer: "Ben", NewPerson: true})
	if ans.Done != "These 5 faces are Ben now (in “Kitchen”)." {
		t.Fatalf("typed: %q", ans.Done)
	}
	if n := len(call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: clip}).Groups); n != 0 {
		t.Fatalf("left: %d", n)
	}
}

// "Same as…" joins two questions about faces of one clip; Skip skips a group's
// faces for good (re-indexing keeps it).
func TestSameAsAndSkip(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	views := alternating(8, callView{print: lookAlong(0, 40, 0.3), label: "speaker_00"}, callView{print: lookAlong(1, 41, 0.3), label: "speaker_01"})
	// The second person changes cameras halfway: their two looks are two questions.
	views[5].print, views[7].print, views[5].label, views[7].label = lookAlong(9, 43, 0.3), lookAlong(9, 44, 0.3), "speaker_02", "speaker_02"
	clip := speakerCall(t, a, "", "Studio", views)
	if _, err := faces.Candidates(ctx, a, clip); err != nil {
		t.Fatal(err)
	}
	gs := call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: clip}).Groups
	if len(gs) != 3 {
		t.Fatalf("three questions: %+v", gs)
	}
	var two, one []models.Todo
	for _, g := range gs {
		if g.Context.Appearances == 2 {
			two = append(two, g)
		} else {
			one = append(one, g)
		}
	}
	if len(two) != 2 {
		t.Fatalf("the second person's two looks: %+v", gs)
	}
	ans := call[TodoAnswerOut](t, a, "todos.answer", TodoAnswerIn{ID: two[1].ID, SameAs: two[0].ID})
	if !strings.HasPrefix(ans.Done, "One question for both now") {
		t.Fatalf("same as: %q", ans.Done)
	}
	gs = call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: clip}).Groups
	if len(gs) != 2 {
		t.Fatalf("two questions now: %+v", gs)
	}
	var joined models.Todo
	for _, g := range gs {
		if g.Context.Appearances == 4 {
			joined = g
		}
	}
	if joined.ID == "" {
		t.Fatalf("one question for the four faces: %+v", gs)
	}
	if _, err := CallTyped[TodoAnswerOut](ctx, a, "todos.answer", TodoAnswerIn{ID: joined.ID, SameAs: joined.ID}, Caller{}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("same as itself: %v", err)
	}
	sk := call[TodoAnswerOut](t, a, "todos.dismiss", TodoDismissIn{ID: joined.ID})
	if sk.Done != "Skipped. We won't ask about these faces again." {
		t.Fatalf("skip: %q", sk.Done)
	}
	if _, err := faces.Candidates(ctx, a, clip); err != nil {
		t.Fatal(err)
	}
	for _, g := range call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: clip}).Groups {
		if g.Context.GroupID == joined.Context.GroupID || g.Context.Label != "speaker_00" {
			t.Fatalf("skipped faces asked again: %+v", g)
		}
	}
	// Indexed again: still skipped.
	st := visual.Load(ctx, a.DB, a.WorkspaceID(), clip)
	if err := faces.Save(ctx, a, clip, st.Index); err != nil {
		t.Fatal(err)
	}
	if _, err := faces.Candidates(ctx, a, clip); err != nil {
		t.Fatal(err)
	}
	gs = call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: clip}).Groups
	if len(gs) != 1 || gs[0].Context.Label != "speaker_00" {
		t.Fatalf("indexed again: only the first person is asked about: %+v", gs)
	}
}

// learnedVoice stores a clip with one voice of a given sound (fingerprint
// direction and pitch) and returns the clip.
func learnedVoice(t *testing.T, a *app.App, name string, dir int, pitch float64) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	as := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{}, Kind: "video", Name: name, State: models.AssetAvailable,
		Media:    models.MediaInfo{Duration: 20, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
	insert(t, a, store.CAssets, as)
	insert(t, a, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, From: 0.5, To: 19, Speaker: "speaker_00",
		Text: "A long story about the garden.", CreatedAt: now})
	det := &speakers.Detection{Run: ids.New(ids.Run), Result: &voice.Result{Raw: 1, Clusters: []voice.Cluster{testVoice("speaker_00", dir, 0.5, 19)}}, Decision: map[string]voice.Decision{}, Prints: true}
	if err := speakers.Store(ctx, a, &as, det, "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.C(store.CSpeakers).UpdateMany(ctx, bson.M{"assetId": as.ID}, bson.M{"$set": bson.M{"pitch": pitch, "voiced": 8.0}}); err != nil {
		t.Fatal(err)
	}
	return as.ID
}

// "Is this really Maya?" (D158): the dialog's terminal twin lists what her
// voice is learned from — here a man's voice named Maya by mistake, marked
// as unlike this voice — and taking it back relearns her voice at once, so the
// warning goes away and the naming that raised it goes through.
func TestHearWhatAVoiceIsLearnedFromAndTakeItBack(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	wrong := learnedVoice(t, a, "board.mov", 2, 110)                                                                             // a man's voice…
	call[SpeakerRenameOut](t, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: wrong, Speaker: "Speaker 1", Name: "Maya"}) // …named Maya by mistake
	hers := learnedVoice(t, a, "garden.mov", 1, 205)
	_, err := CallTyped[SpeakerRenameOut](ctx, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: hers, Speaker: "Speaker 1", Name: "Maya"}, Caller{})
	e := apperr.From(err)
	d, _ := e.Details.(map[string]any)
	if e.Code != apperr.ConfirmRequired || d["reason"] != "voices_differ" || d["assetId"] != hers || d["speaker"] != "speaker_00" || d["personId"] == "" {
		t.Fatalf("the warning names what it compares: %v %+v", err, e.Details)
	}
	pid := d["personId"].(string)
	l := call[PersonLearnedOut](t, a, "people.learned", PersonLearnedIn{ID: "Maya", AssetID: hers, Speaker: "speaker_00"})
	if len(l.Voices) != 1 || l.Voices[0].AssetID != wrong || !l.Voices[0].Odd || l.Voice == nil || !l.Voice.Differ || !l.Still || l.Voices[0].Sample == nil {
		t.Fatalf("learned: %+v voice %+v", l.Voices, l.Voice)
	}
	if !strings.Contains(l.Summary, "One may be someone else: Speaker 1 in “board”") || !strings.Contains(l.Summary, "This voice still sounds quite different from Maya's") {
		t.Fatalf("summary: %q", l.Summary)
	}
	un := call[PersonUnlearnOut](t, a, "people.unlearn", PersonUnlearnIn{ID: pid, AssetID: wrong, Speaker: "speaker_00", Name: "Theo", NewPerson: true,
		AgainstAssetID: hers, AgainstSpeaker: "speaker_00"})
	if un.Still || len(un.Voices) != 0 || un.Done != "That voice is Theo now, not Maya; MetaRush learned Maya's voice again." {
		t.Fatalf("unlearned: %+v", un)
	}
	ren := call[SpeakerRenameOut](t, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: hers, Speaker: "Speaker 1", Name: "Maya"})
	if ren.Person.ID != pid {
		t.Fatalf("the naming goes through without the warning: %+v", ren)
	}
	// Nothing to take back without saying who it is.
	if _, err := CallTyped[PersonUnlearnOut](ctx, a, "people.unlearn", PersonUnlearnIn{ID: pid, AssetID: hers, Speaker: "speaker_00"}, Caller{}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("who is it then? %v", err)
	}
}

// "Not Maya?" on a face named by its looks (D158): the faces her face is
// learned from, the one it looks most like marked; taking that one back (it
// was someone else) relearns her face, and the face is no longer named after her.
func TestSeeWhatAFaceIsLearnedFromAndTakeItBack(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Faces"})
	real := indexedFace(t, a, p.Project.ID, "real.mov", lookAlong(0, 50, 0.1))
	wrongClip := indexedFace(t, a, p.Project.ID, "wrong.mov", lookAlong(7, 51, 0.1))
	for _, c := range []string{real, wrongClip} {
		f := call[FacesOut](t, a, "footage.faces", FacesIn{AssetID: c}).Faces[0]
		call[FaceOut](t, a, "footage.faces.name", FaceNameIn{FaceID: f.ID, Name: "Maya"})
	}
	look := indexedFace(t, a, p.Project.ID, "lookalike.mov", lookAlong(7, 52, 0.12))
	if _, _, err := faces.Recognize(ctx, a, look); err != nil {
		t.Fatal(err)
	}
	lf := call[FacesOut](t, a, "footage.faces", FacesIn{AssetID: look}).Faces[0]
	if lf.Recognized == nil || lf.Recognized.Name != "Maya" {
		t.Fatalf("named by its looks after the wrong face: %+v", lf)
	}
	l := call[PersonLearnedOut](t, a, "people.learned", PersonLearnedIn{ID: "Maya", FaceID: lf.ID})
	if len(l.Faces) != 2 || !l.Faces[0].Closest || l.Faces[0].AssetID != wrongClip || l.Face == nil || !l.Face.Recognized || !l.Still {
		t.Fatalf("learned faces: %+v face %+v", l.Faces, l.Face)
	}
	un := call[PersonUnlearnOut](t, a, "people.unlearn", PersonUnlearnIn{ID: "Maya", FaceID: l.Faces[0].FaceID, Name: "Rex", NewPerson: true, AgainstFaceID: lf.ID})
	if un.Still || len(un.Faces) != 1 || un.Faces[0].AssetID != real || un.Done != "That face isn't Maya: they're Rex now. MetaRush learned Maya's face again." {
		t.Fatalf("unlearned: %+v", un)
	}
	lf = call[FacesOut](t, a, "footage.faces", FacesIn{AssetID: look}).Faces[0]
	if lf.Recognized != nil && lf.Recognized.Name == "Maya" {
		t.Fatalf("no longer named after her: %+v", lf)
	}
}

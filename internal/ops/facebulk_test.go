package ops

import (
	"context"
	"slices"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/visual"
)

// offers says whether any open face question of a clip suggests or offers a name.
func offers(t *testing.T, gs []models.Todo, name string) bool {
	t.Helper()
	for _, g := range gs {
		if g.Context.PersonName == name {
			return true
		}
		for _, s := range g.Suggestions {
			if s.Value == name || strings.HasPrefix(s.Label, name+" (") {
				return true
			}
		}
	}
	return false
}

// "None of these are Ada Park" and "Skip all" (D189): every open question
// that suggests her, across the project's clips, is answered no at once (she
// is a name the calls show, not in People yet), scored, and never suggested
// for those faces again; then skipping every question of a clip, and of the
// project, leaves nothing to ask there.
func TestNoneOfTheseAreAndSkipAll(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Garden"})
	c1 := speakerCall(t, a, p.Project.ID, "Monday call", alternating(8,
		callView{print: lookAlong(0, 40, 0.3), tile: "Ada Park", label: "speaker_00"},
		callView{print: lookAlong(1, 41, 0.3), tile: "Ben Okafor", label: "speaker_01"}))
	c2 := speakerCall(t, a, p.Project.ID, "Tuesday call", alternating(8,
		callView{print: lookAlong(2, 42, 0.3), tile: "Ada Park", label: "speaker_00"},
		callView{print: lookAlong(3, 43, 0.3), tile: "Cleo Marsh", label: "speaker_01"}))
	for _, c := range []string{c1, c2} {
		if _, err := faces.Candidates(ctx, a, c); err != nil {
			t.Fatal(err)
		}
	}
	gs := call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{ProjectID: p.Project.ID}).Groups
	if len(gs) != 4 || !offers(t, gs, "Ada Park") {
		t.Fatalf("four people to name, Ada Park in both calls: %+v", gs)
	}
	out := call[FacesAnsweredOut](t, a, "todos.notPerson", TodoNotPersonIn{Person: "Ada Park", ProjectID: p.Project.ID})
	if out.Answered != 2 || out.Clips != 2 || out.Faces != 8 || out.Summary != "Noted: none of the 2 people asked about (in 2 clips) are Ada Park. MetaRush won't suggest Ada Park for those faces again." {
		t.Fatalf("none of these are Ada Park: %+v", out)
	}
	gs = call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{ProjectID: p.Project.ID}).Groups
	if offers(t, gs, "Ada Park") {
		t.Fatalf("Ada Park is never offered for those faces again: %+v", gs)
	}
	var answered []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"kind": models.TodoFaceGroup, "answer": "no"}, &answered); err != nil {
		t.Fatal(err)
	}
	if len(answered) != 2 || len(answered[0].Guesses) != 1 || answered[0].Guesses[0].Agreed || !slices.Contains(answered[0].Guesses[0].Basis, models.BasisTile) {
		t.Fatalf("each answer is scored (the name tile was wrong): %+v", answered)
	}
	if ev := call[PeopleEvidenceOut](t, a, "people.evidence", PeopleEvidenceIn{}); ev.Kinds[2].Kind != models.BasisTile || ev.Kinds[2].Answers != 2 || ev.Kinds[2].Right != 0 || len(ev.Muted) != 0 {
		t.Fatalf("the name tile: right 0 of 2, not judged yet: %+v", ev)
	}
	// Skip all in one clip, then in the project.
	if _, err := CallTyped[FacesAnsweredOut](ctx, a, "todos.skipFaces", TodoSkipFacesIn{}, Caller{}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("a clip or a project: %v", err)
	}
	sk := call[FacesAnsweredOut](t, a, "todos.skipFaces", TodoSkipFacesIn{AssetID: c1})
	if sk.Answered == 0 || sk.Clips != 1 || !strings.HasPrefix(sk.Summary, "Skipped ") || !strings.HasSuffix(sk.Summary, "about faces in “Monday call”. We won't ask about those faces again.") {
		t.Fatalf("skip all in the clip: %+v", sk)
	}
	if gs := call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: c1}).Groups; len(gs) != 0 {
		t.Fatalf("nothing left in the clip: %+v", gs)
	}
	sk = call[FacesAnsweredOut](t, a, "todos.skipFaces", TodoSkipFacesIn{ProjectID: p.Project.ID})
	if sk.Answered == 0 || sk.Clips != 1 {
		t.Fatalf("skip all in the project (the other clip's): %+v", sk)
	}
	for _, c := range []string{c1, c2} {
		if _, err := faces.Candidates(ctx, a, c); err != nil {
			t.Fatal(err)
		}
	}
	if gs := call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{ProjectID: p.Project.ID}).Groups; len(gs) != 0 {
		t.Fatalf("skipped faces aren't asked about again: %+v", gs)
	}
}

// "Not Ada anywhere in this clip" (D189): Ada talks in a call but her face is
// never shown (the picture shows a listener while she talks). Said once, she
// is never suggested or offered for the clip's faces — also after the clip is
// looked at again — her voice is no evidence there, and the answer that
// offered her is scored. Naming one of its faces Ada later takes it back.
func TestNotAnywhereInThisClip(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	views := alternating(8, callView{print: lookAlong(5, 40, 0.3), label: "speaker_00"}, callView{print: lookAlong(1, 41, 0.3), label: "speaker_01"})
	clip := speakerCall(t, a, "", "Radio call", views)
	ada := call[SpeakerRenameOut](t, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: clip, Speaker: "speaker_00", Name: "Ada"}).Person
	gs := call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: clip}).Groups
	if !offers(t, gs, "Ada") {
		t.Fatalf("Ada's voice makes her a quick pick for the listener: %+v", gs)
	}
	out := call[FacesAnsweredOut](t, a, "footage.faces.notHere", FacesNotHereIn{AssetID: clip, Person: "Ada"})
	if out.Faces == 0 || out.Kept != 0 || out.Summary != "Noted: Ada isn't on screen in “Radio call”. MetaRush won't suggest Ada for its faces, or go by Ada's voice there." {
		t.Fatalf("not Ada anywhere in this clip: %+v", out)
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), clip, &as); err != nil || !slices.Contains(as.NotOnScreen, ada.ID) {
		t.Fatalf("remembered on the clip: %+v %v", as.NotOnScreen, err)
	}
	if gs := call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: clip}).Groups; offers(t, gs, "Ada") {
		t.Fatalf("Ada isn't offered again: %+v", gs)
	}
	// Looked at again: new face records; still not Ada.
	st := visual.Load(ctx, a.DB, a.WorkspaceID(), clip)
	if err := faces.Save(ctx, a, clip, st.Index); err != nil {
		t.Fatal(err)
	}
	if _, err := faces.Candidates(ctx, a, clip); err != nil {
		t.Fatal(err)
	}
	if gs := call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: clip}).Groups; offers(t, gs, "Ada") {
		t.Fatalf("looked at again, Ada still isn't offered: %+v", gs)
	}
	// A person names one of its faces Ada after all: the clip forgets "not here".
	fs := call[FacesOut](t, a, "footage.faces", FacesIn{AssetID: clip}).Faces
	call[FaceOut](t, a, "footage.faces.name", FaceNameIn{FaceID: fs[0].ID, Name: "Ada"})
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), clip, &as); err != nil || slices.Contains(as.NotOnScreen, ada.ID) {
		t.Fatalf("named on screen: no longer “not here”: %+v %v", as.NotOnScreen, err)
	}
}

// An answer to "Who is this?" that names someone else than the voice's
// person is kept score of: the voice heard was wrong (D189).
func TestAnswersKeepScore(t *testing.T) {
	a := testapp.New(t)
	views := alternating(8, callView{print: lookAlong(5, 40, 0.3), label: "speaker_00"}, callView{print: lookAlong(1, 41, 0.3), label: "speaker_01"})
	clip := speakerCall(t, a, "", "Listener call", views)
	call[SpeakerRenameOut](t, a, "footage.speakers.rename", SpeakerRenameIn{AssetID: clip, Speaker: "speaker_00", Name: "Ada"})
	var q models.Todo
	for _, g := range call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: clip}).Groups {
		if g.Context.Label == "speaker_00" {
			q = g
		}
	}
	if q.ID == "" || q.Question != "Who is this?" {
		t.Fatalf("the listener's question: %+v", q)
	}
	call[TodoAnswerOut](t, a, "todos.answer", TodoAnswerIn{ID: q.ID, Answer: "Dee", NewPerson: true})
	var got models.Todo
	if err := a.DB.GetByID(context.Background(), store.CTodos, a.WorkspaceID(), q.ID, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Scored || len(got.Guesses) != 1 || got.Guesses[0].Name != "Ada" || got.Guesses[0].Agreed || got.Guesses[0].Basis[0] != models.BasisVoiceCue {
		t.Fatalf("Ada (talking then) was wrong: %+v", got.Guesses)
	}
	ev := call[PeopleEvidenceOut](t, a, "people.evidence", PeopleEvidenceIn{})
	if ev.Kinds[0].Kind != models.BasisVoiceCue || ev.Kinds[0].Answers != 1 || ev.Kinds[0].Right != 0 || ev.Kinds[0].Words != "Right 0 of 1 times so far." {
		t.Fatalf("the voice cue's score: %+v", ev.Kinds[0])
	}
}

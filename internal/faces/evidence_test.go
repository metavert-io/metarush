package faces

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/todos"
)

// The voice cue needs 6 seconds (D189): a voice heard for less while a face
// is alone on screen says only that someone speaks; it must also be most of
// the talking then (a gate, not how sure).
func TestVoiceCueNeedsSixSeconds(t *testing.T) {
	tie := func(by map[string]float64) *voiceTie {
		v := &voiceTie{by: by}
		for _, s := range by {
			v.total += s
		}
		return v
	}
	if l, _, _ := tie(map[string]float64{"a": 5}).strongest(); l != "" {
		t.Fatalf("5 seconds is no voice cue: %q", l)
	}
	if l, _, _ := tie(map[string]float64{"a": 5}).strongestAt(SpeaksSeconds); l != "a" {
		t.Fatalf("…but someone speaks: %q", l)
	}
	if l, sec, share := tie(map[string]float64{"a": 6, "b": 1}).strongest(); l != "a" || sec != 6 || share < 0.85 {
		t.Fatalf("6 seconds, most of the talking: %q %.1f %.2f", l, sec, share)
	}
	if l, _, _ := tie(map[string]float64{"a": 6, "b": 6}).strongest(); l != "" {
		t.Fatalf("half the talking is no cue: %q", l)
	}
}

// nameVoiceAuto names one voice of a clip after a person, recognized by voice
// (not confirmed by a person).
func nameVoiceAuto(t *testing.T, a *app.App, assetID, label string, p models.Person) {
	t.Helper()
	if _, err := a.DB.C(store.CSpeakers).UpdateOne(context.Background(), bson.M{"assetId": assetID, "label": label},
		bson.M{"$set": bson.M{"personId": p.ID, "name": p.Name, "source": models.SpeakerByAuto}}); err != nil {
		t.Fatal(err)
	}
}

// suggests lists a question's answers, the "no" left out.
func suggests(q *models.Todo) []string {
	var out []string
	for _, s := range q.Suggestions {
		if s.Value != "no" {
			out = append(out, s.Label)
		}
	}
	return out
}

// "Is this …?" needs the looks or a name shown (D189): in a call without name
// tiles, the person whose faces look like faces a person confirmed elsewhere
// is asked about by name (the voice adds to it), and the person known only by
// voice is asked "Who is this?" with their name as a quick pick.
func TestLooksMakeASuggestionTheVoiceAloneDoesNot(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ada, ben := person(t, a, "Ada Park"), person(t, a, "Ben Okafor")
	// Ada confirmed in another clip, from another angle (about 50% alike: asked, not named).
	other := indexed(t, a, "Garden", 6, nil, onScreen{left, 0, 6, look(0, 90, 0.85)})
	if _, err := Confirm(ctx, a, faceOf(t, a, other, 1).ID, ada); err != nil {
		t.Fatal(err)
	}
	ps := []participant{{"Ada Park", 0, "", "speaker_00"}, {"Ben Okafor", 1, "", "speaker_01"}}
	turns := []callTurn{{0, 7, "We grew tomatoes."}, {1, 7, "On the balcony?"}, {0, 7, "Yes, all summer."}, {1, 7, "Nice."}}
	asset, _ := speakerViewCall(t, a, "Balcony call", ps, turns, nil)
	nameVoice(t, a, asset, "speaker_00", ada)
	nameVoice(t, a, asset, "speaker_01", ben)
	if _, err := Candidates(ctx, a, asset); err != nil {
		t.Fatal(err)
	}
	qs := openQuestions(t, a, asset)
	adaQ, who := question(qs, "Is this Ada Park?"), question(qs, "Who is this?")
	if len(qs) != 2 || adaQ == nil || who == nil {
		t.Fatalf("Ada by her looks, Ben asked who he is: %+v", qs)
	}
	if b := adaQ.Suggestions[0].Basis; !slices.Contains(b, models.BasisLooks) || !slices.Contains(b, models.BasisVoiceCue) {
		t.Fatalf("Ada's suggestion rests on her looks and her voice: %v", b)
	}
	if got := suggests(who); len(got) == 0 || got[0] != "Ben Okafor (talking then)" || who.Suggestions[0].PersonID != ben.ID {
		t.Fatalf("Ben's voice is a quick pick, not the question: %v", got)
	}
	tracks, _ := Load(ctx, a, asset)
	for _, f := range tracks {
		if f.Candidate != nil && f.Candidate.PersonID == ben.ID {
			t.Fatalf("no “maybe Ben” from his voice alone: %+v", f.Candidate)
		}
	}
}

// Faces named by their looks show that a voice is someone else's (D189): in a
// clip where Lena's faces are named by looks (none confirmed there), a
// listener shown while Lena talks isn't asked "Who is this?" with "Lena
// (talking then)" — her faces here look nothing like the listener, so the
// voice is hers, not the listener's, and the listener doesn't speak.
func TestLooksNamedFacesMakeTheVoiceSomeoneElses(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	lena := person(t, a, "Lena")
	c1 := indexed(t, a, "Porch", 6, nil, onScreen{left, 0, 6, look(0, 50, 0.1)})
	if _, err := Confirm(ctx, a, faceOf(t, a, c1, 1).ID, lena); err != nil {
		t.Fatal(err)
	}
	// Lena talks all along; the picture shows her, then a listener for 8 s, then her.
	c2 := indexed(t, a, "Kitchen call", 30, []float64{10, 18},
		onScreen{left, 0, 10, look(0, 52, 0.2)}, onScreen{left, 10, 18, look(5, 53, 0.2)}, onScreen{left, 18, 30, look(0, 54, 0.2)})
	talk(t, a, c2, "speaker_00", 0.1, 29.9)
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CSpeakers, models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: a.WorkspaceID(), AssetID: c2, Run: "run_1", Label: "speaker_00",
		PersonID: lena.ID, Name: lena.Name, Source: models.SpeakerByPerson, Samples: []models.TimeSpan{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Recognize(ctx, a, c2); err != nil {
		t.Fatal(err)
	}
	if f1, f3 := faceOf(t, a, c2, 1), faceOf(t, a, c2, 3); f1.Recognized == nil || f3.Recognized == nil || f1.Confirmed() {
		t.Fatalf("Lena's faces here are named by their looks: %+v / %+v", f1.Recognized, f3.Recognized)
	}
	if qs := openQuestions(t, a, c2); len(qs) != 0 {
		t.Fatalf("the listener is not asked about with Lena's voice: %+v", qs)
	}
	if f := faceOf(t, a, c2, 2); f.Candidate != nil {
		t.Fatalf("no “maybe Lena” for the listener: %+v", f.Candidate)
	}
}

// A late picture switch (D189): Iris's faces carry her name tile while Maya
// is talking. The question is "Is this Iris Vale?" with no "Maya Lindqvist
// (talking then)" against the name shown; Maya's
// own faces, known only by her voice, are asked "Who is this?".
func TestNoTalkingThenAgainstTheNameShown(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	maya := person(t, a, "Maya Lindqvist")
	// Iris's views carry Maya's voice (speaker_01), and so do Maya's.
	ps := []participant{{"Iris Vale", 0, "Iris Vale", "speaker_01"}, {"Maya Lindqvist", 1, "", "speaker_01"}}
	turns := []callTurn{{0, 9, "So the garden."}, {1, 7, "Right."}, {0, 9, "And the beds."}, {1, 7, "Yes."}}
	asset, _ := speakerViewCall(t, a, "Call with Iris", ps, turns, nil)
	nameVoiceAuto(t, a, asset, "speaker_01", maya)
	if _, err := Candidates(ctx, a, asset); err != nil {
		t.Fatal(err)
	}
	qs := openQuestions(t, a, asset)
	shown, who := question(qs, "Is this Iris Vale?"), question(qs, "Who is this?")
	if shown == nil || who == nil || len(qs) != 2 {
		t.Fatalf("Iris by the name shown, Maya's faces their own question: %+v", qs)
	}
	for _, s := range shown.Suggestions {
		if strings.Contains(s.Label, "Maya") {
			t.Fatalf("no “(talking then)” against the name shown: %v", suggests(shown))
		}
	}
	if got := suggests(who); len(got) == 0 || got[0] != "Maya Lindqvist (talking then)" {
		t.Fatalf("Maya's voice is a pick on her own faces' question: %v", got)
	}
}

// After two answers in a clip said a face isn't Ada where it rested on her
// voice, her voice is no evidence there (D189): not offered as "(talking
// then)", nor as someone who talks in the clip.
func TestVoiceCueOffAfterTwoNos(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ada := person(t, a, "Ada Park")
	ps := []participant{{"Ada Park", 0, "", "speaker_00"}, {"Ben Okafor", 1, "", "speaker_01"}}
	turns := []callTurn{{0, 8, "One."}, {1, 3, "Two."}, {0, 8, "Three."}}
	asset, _ := speakerViewCall(t, a, "Cue call", ps, turns, nil)
	nameVoice(t, a, asset, "speaker_00", ada)
	if _, err := Candidates(ctx, a, asset); err != nil {
		t.Fatal(err)
	}
	if q := question(openQuestions(t, a, asset), "Who is this?"); q == nil || len(q.Suggestions) == 0 || q.Suggestions[0].Label != "Ada Park (talking then)" {
		t.Fatalf("Ada's voice is a pick at first: %+v", q)
	}
	// Two answers in this clip said "not Ada" where it rested on her voice.
	now := time.Now().UTC()
	for i := 0; i < todos.CueOffAfter; i++ {
		if err := a.DB.Insert(ctx, store.CTodos, models.Todo{ID: ids.New(ids.Todo), WorkspaceID: a.WorkspaceID(), Kind: models.TodoFaceGroup, Scope: "asset", AssetID: asset,
			ProjectIDs: []string{}, GroupKey: fmt.Sprintf("facegroup:%s:old%d:who", asset, i), Question: "Who is this?", State: models.TodoAnswered, Resolution: models.ResolvedAnswered,
			Answer: "Ben", Suggestions: []models.TodoSuggestion{}, Scored: true, Guesses: []models.TodoGuess{{Name: ada.Name, PersonID: ada.ID, Basis: []string{models.BasisVoiceCue}, Seconds: 8}},
			CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
	}
	if off := todos.CueOff(ctx, a, asset); !off[ada.ID] {
		t.Fatalf("Ada's voice is off in this clip: %v", off)
	}
	if _, err := Candidates(ctx, a, asset); err != nil {
		t.Fatal(err)
	}
	for _, q := range openQuestions(t, a, asset) {
		for _, s := range q.Suggestions {
			if s.PersonID == ada.ID {
				t.Fatalf("Ada offered again in this clip: %v", suggests(&q))
			}
		}
	}
}

// A muted kind of evidence no longer suggests a name (D189): with the looks
// right 6 of 10 times in this workspace, a face that looks like Ada is asked
// "Who is this?" with Ada as a pick, and the question says why plainly.
func TestMutedLooksOnlyOfferAPick(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ada := person(t, a, "Ada Park")
	other := indexed(t, a, "Garden", 6, nil, onScreen{left, 0, 6, look(0, 90, 0.85)})
	if _, err := Confirm(ctx, a, faceOf(t, a, other, 1).ID, ada); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	for i := 0; i < 10; i++ {
		id := ids.New(ids.Todo)
		if err := a.DB.Insert(ctx, store.CTodos, models.Todo{ID: id, WorkspaceID: a.WorkspaceID(), Kind: models.TodoFaceGroup, Scope: "asset", AssetID: "ast_elsewhere",
			ProjectIDs: []string{}, GroupKey: fmt.Sprintf("facegroup:elsewhere:%d:x", i), Question: "Is this Bo?", State: models.TodoAnswered, Resolution: models.ResolvedAnswered,
			Suggestions: []models.TodoSuggestion{}, CreatedAt: now, UpdatedAt: now}); err != nil {
			t.Fatal(err)
		}
		if err := todos.Record(ctx, a, id, []models.TodoGuess{{Name: "Bo", Basis: []string{models.BasisLooks}, Agreed: i < 6, Main: true}}); err != nil {
			t.Fatal(err)
		}
	}
	ps := []participant{{"Ada Park", 0, "", "speaker_00"}}
	asset, _ := speakerViewCall(t, a, "Solo", ps, []callTurn{{0, 9, "Hello there."}}, nil)
	if _, err := Candidates(ctx, a, asset); err != nil {
		t.Fatal(err)
	}
	qs := openQuestions(t, a, asset)
	if len(qs) != 1 || qs[0].Question != "Who is this?" || len(qs[0].Suggestions) == 0 || !strings.HasPrefix(qs[0].Suggestions[0].Label, "Ada Park (looks ") {
		t.Fatalf("the looks only offer Ada as a pick: %+v", qs)
	}
	if !strings.Contains(qs[0].Detail, "Going by how faces look was right 6 of 10 times here") {
		t.Fatalf("said plainly: %q", qs[0].Detail)
	}
}

// Who is talking never holds back a name by looks (D189): Ada's face, very
// much like the face a person confirmed as hers, is named by its looks even
// while Ben talks the whole time it is on screen (in calls the picture often
// shows the listener).
func TestTheVoiceNeverHoldsBackANameByLooks(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ada, ben := person(t, a, "Ada"), person(t, a, "Ben")
	c1 := indexed(t, a, "Porch", 6, nil, onScreen{left, 0, 6, look(0, 50, 0.1)})
	if _, err := Confirm(ctx, a, faceOf(t, a, c1, 1).ID, ada); err != nil {
		t.Fatal(err)
	}
	c2 := indexed(t, a, "Listening", 12, nil, onScreen{left, 0, 12, look(0, 52, 0.2)})
	talk(t, a, c2, "speaker_00", 0.1, 11.9)
	now := time.Now().UTC()
	if err := a.DB.Insert(ctx, store.CSpeakers, models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: a.WorkspaceID(), AssetID: c2, Run: "run_1", Label: "speaker_00",
		PersonID: ben.ID, Name: ben.Name, Source: models.SpeakerByPerson, Samples: []models.TimeSpan{}, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := Recognize(ctx, a, c2); err != nil {
		t.Fatal(err)
	}
	if f := faceOf(t, a, c2, 1); f.Recognized == nil || f.Recognized.PersonID != ada.ID {
		t.Fatalf("Ada by her looks, whoever talks: %+v", f.Recognized)
	}
	if qs := openQuestions(t, a, c2); len(qs) != 0 {
		t.Fatalf("nothing to ask: %+v", qs)
	}
}

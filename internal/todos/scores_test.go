package todos_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/todos"
)

func answeredTodo(t *testing.T, a *app.App, tdo models.Todo) models.Todo {
	t.Helper()
	now := time.Now().UTC()
	tdo.ID, tdo.WorkspaceID, tdo.Scope, tdo.State, tdo.Resolution = ids.New(ids.Todo), a.WorkspaceID(), "asset", models.TodoAnswered, models.ResolvedAnswered
	if tdo.ProjectIDs == nil {
		tdo.ProjectIDs = []string{}
	}
	if tdo.GroupKey == "" {
		tdo.GroupKey = "k:" + tdo.ID
	}
	tdo.CreatedAt, tdo.UpdatedAt = now, now
	if err := a.DB.Insert(context.Background(), store.CTodos, tdo); err != nil {
		t.Fatal(err)
	}
	return tdo
}

func score(t *testing.T, ks []todos.KindScore, kind string) todos.KindScore {
	t.Helper()
	for _, k := range ks {
		if k.Kind == kind {
			return k
		}
	}
	t.Fatalf("no score for %s", kind)
	return todos.KindScore{}
}

// Keeping score (D189): an answer judges the names a question offered by what
// they rested on — yes (or the same person picked) is right, no is wrong for
// the suggestion and says nothing of the others, another name makes every
// offered name wrong but the one picked; picks without evidence aren't judged.
func TestGrade(t *testing.T) {
	q := &models.Todo{Kind: models.TodoFaceGroup, Context: models.TodoContext{PersonID: "per_a", PersonName: "Ada", Seconds: 8},
		Suggestions: []models.TodoSuggestion{
			{Label: "Yes, it's Ada", Value: "yes", PersonID: "per_a", Basis: []string{models.BasisLooks, models.BasisVoiceCue}},
			{Label: "Ben (shown in the video)", Value: "Ben", PersonID: "per_b", Basis: []string{models.BasisTile}},
			{Label: "Cleo (talks in this clip)", Value: "Cleo", PersonID: "per_c"},
			{Label: "No, someone else", Value: "no"},
		}}
	gs := todos.Grade(q, "per_a", "Ada", false)
	if len(gs) != 2 || !gs[0].Main || !gs[0].Agreed || gs[0].Seconds != 8 || gs[1].Agreed {
		t.Fatalf("yes: %+v", gs)
	}
	if gs := todos.Grade(q, "", "", true); len(gs) != 1 || gs[0].Agreed || !gs[0].Main {
		t.Fatalf("no: only the suggestion is wrong: %+v", gs)
	}
	if gs := todos.Grade(q, "per_b", "Ben", false); len(gs) != 2 || gs[0].Agreed || !gs[1].Agreed {
		t.Fatalf("the name shown was right: %+v", gs)
	}
	if gs := todos.Grade(&models.Todo{Kind: models.TodoMusicRights}, "", "x", false); gs != nil {
		t.Fatalf("only questions about people: %+v", gs)
	}
}

// Scores and muting (D189): a kind of evidence right less than 80% of the
// time over at least 10 answers is muted; fewer answers don't judge it; the
// voice heard for less than 6 seconds (answers from before) is left out of
// its score; answers given before MetaRush kept score are scored from what
// their questions said, once; two "not Ada" answers in a clip that rested on
// her voice switch her voice off there.
func TestScoresMutingAndBackfill(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	record := func(basis string, right, wrong int, seconds float64) {
		for i := 0; i < right+wrong; i++ {
			tdo := answeredTodo(t, a, models.Todo{Kind: models.TodoFaceGroup, AssetID: "ast_x", Question: "Is this Bo?"})
			if err := todos.Record(ctx, a, tdo.ID, []models.TodoGuess{{Name: "Bo", PersonID: "per_bo", Basis: []string{basis}, Agreed: i < right, Main: true, Seconds: seconds}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	record(models.BasisLooks, 7, 3, 0)     // 70% over 10: muted
	record(models.BasisVoiceCue, 9, 1, 8)  // 90%: kept
	record(models.BasisVoiceCue, 1, 24, 3) // under 6 seconds: from before, left out
	record(models.BasisTile, 1, 2, 0)      // 3 answers: not judged yet
	ks, err := todos.Scores(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	if l := score(t, ks, models.BasisLooks); !l.Muted || l.Answers != 10 || l.Right != 7 || l.Words != "Right 7 of 10 times: MetaRush no longer suggests names from it (it can still be a quick pick)." {
		t.Fatalf("looks: %+v", l)
	}
	if v := score(t, ks, models.BasisVoiceCue); v.Muted || v.Answers != 10 || v.Older != 25 {
		t.Fatalf("the voice cue: %+v", v)
	}
	if ti := score(t, ks, models.BasisTile); ti.Muted || ti.Answers != 3 {
		t.Fatalf("the name tile: %+v", ti)
	}
	if m := todos.Muted(ctx, a); len(m) != 1 || m[models.BasisLooks].Answers != 10 {
		t.Fatalf("muted: %v", m)
	}
	if n := todos.MutedNote(score(t, ks, models.BasisLooks)); n != "Going by how faces look was right 7 of 10 times here, so MetaRush only offers those names as picks now." {
		t.Fatalf("note: %q", n)
	}

	// Answers from before D189, scored from what their questions said.
	fp := answeredTodo(t, a, models.Todo{Kind: models.TodoFacePerson, AssetID: "ast_call", Question: "Is this Lena?", Answer: "no",
		Detail:      "In “Call”, this face is the only one on screen while Lena is talking (about 3 seconds). If it is Lena, MetaRush puts that name on this face.",
		Context:     models.TodoContext{PersonID: "per_j", PersonName: "Lena", Seconds: 3},
		Suggestions: []models.TodoSuggestion{{Label: "Yes, it's Lena", Value: "yes", PersonID: "per_j"}, {Label: "No, someone else", Value: "no"}}})
	sm := answeredTodo(t, a, models.Todo{Kind: models.TodoSpeakerMatch, AssetID: "ast_talk", Question: "Is this Maya?", Answer: "Theo",
		Context: models.TodoContext{PersonID: "per_maya", PersonName: "Maya"},
		Suggestions: []models.TodoSuggestion{{Label: "Yes, it's Maya", Value: "Maya", PersonID: "per_maya"}, {Label: "Theo (68% match)", Value: "Theo", PersonID: "per_theo"},
			{Label: "Ada (in this project)", Value: "Ada", PersonID: "per_ada"}}})
	fg := answeredTodo(t, a, models.Todo{Kind: models.TodoFaceGroup, AssetID: "ast_call", Question: "Is this Iris?", Answer: "yes",
		Context: models.TodoContext{PersonID: "per_m", PersonName: "Iris", Evidence: []models.TodoEvidence{
			{Kind: "shown", Text: "The video shows “Iris Vale” under this face.", Name: "Iris", PersonID: "per_m"},
			{Kind: "voice", Text: "Maya is talking while this face is on screen (about 19 seconds).", Name: "Maya", PersonID: "per_maya"}}},
		Suggestions: []models.TodoSuggestion{{Label: "Yes, it's Iris", Value: "yes", PersonID: "per_m"}, {Label: "Maya (talking then)", Value: "Maya", PersonID: "per_maya"},
			{Label: "No, someone else", Value: "no"}}})
	n, err := todos.Backfill(ctx, a)
	if err != nil || n != 3 {
		t.Fatalf("backfilled %d: %v", n, err)
	}
	if n, _ := todos.Backfill(ctx, a); n != 0 {
		t.Fatalf("scored once: %d", n)
	}
	got := func(id string) models.Todo {
		var x models.Todo
		if err := a.DB.GetByID(ctx, store.CTodos, a.WorkspaceID(), id, &x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	if g := got(fp.ID).Guesses; len(g) != 1 || g[0].Agreed || fmt.Sprint(g[0].Basis) != "[voice_cue]" || g[0].Seconds != 3 {
		t.Fatalf("“Is this Lena?” answered no, on a 3-second voice: %+v", g)
	}
	if g := got(sm.ID).Guesses; len(g) != 2 || g[0].Agreed || !g[1].Agreed || fmt.Sprint(g[1].Basis) != "[voice]" {
		t.Fatalf("“Is this Maya?” answered with the second voice match: %+v", g)
	}
	if g := got(fg.ID).Guesses; len(g) != 2 || !g[0].Agreed || fmt.Sprint(g[0].Basis) != "[tile]" || g[1].Agreed || fmt.Sprint(g[1].Basis) != "[voice_cue]" {
		t.Fatalf("Iris by her name tile, not Maya talking then: %+v", g)
	}

	// The voice is off in a clip after two "not Ada" answers resting on it.
	for i := 0; i < todos.CueOffAfter; i++ {
		tdo := answeredTodo(t, a, models.Todo{Kind: models.TodoFaceGroup, AssetID: "ast_cue", Question: "Who is this?"})
		if off := todos.CueOff(ctx, a, "ast_cue"); off["per_ada"] {
			t.Fatalf("off after %d answers", i)
		}
		if err := todos.Record(ctx, a, tdo.ID, []models.TodoGuess{{Name: "Ada", PersonID: "per_ada", Basis: []string{models.BasisVoiceCue}, Seconds: 9}}); err != nil {
			t.Fatal(err)
		}
	}
	if off := todos.CueOff(ctx, a, "ast_cue"); !off["per_ada"] || len(todos.CueOff(ctx, a, "ast_other")) != 0 {
		t.Fatalf("off in that clip only: %v", off)
	}
}

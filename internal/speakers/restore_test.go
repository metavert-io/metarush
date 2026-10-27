package speakers

import (
	"context"
	"testing"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
)

// "This part is Lena" in a clip of one voice: the rest of the clip sounds
// just like the stretch she was given, so it is recognized as Lena and
// combined with her voice automatically. Undoing the fix takes all of that
// back (D145): every word is the clip's own voice again, nothing is combined,
// and Lena keeps no voice from it.
func TestUndoingWhoSaysAStretchTakesBackWhatFollowed(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	x := seedWords(t, a, "one-voice.mov", project, 12, []seg{{0, 5, "speaker_00"}, {5, 10, "speaker_00"}})
	withSpeech(t, map[string][]speech{x.ID: {{0, 10, 7}}})
	if err := Store(ctx, a, x, detection(cluster("speaker_00", 7, 0, 10)), "test"); err != nil {
		t.Fatal(err)
	}
	ar, err := Assign(ctx, a, AssignInput{AssetID: x.ID, From: 5, To: 9, Name: "Lena", NewPerson: true})
	if err != nil {
		t.Fatal(err)
	}
	if ar.Moved != 8 || ar.Person == nil || len(ar.Before) != 1 || ar.Before[0].Label != "speaker_00" {
		t.Fatalf("assign: moved %d, before %+v", ar.Moved, ar.Before)
	}
	fix := models.SpeakerFix{Before: ar.Before, Label: ar.Voice.Label, Name: ar.Person.Name, PersonID: ar.Person.ID, Run: ar.Run, Voices: ar.Voices}
	names := namesByTime(t, a, x.ID)
	if names[5] != "Lena" || names[8.5] != "Lena" {
		t.Fatalf("the stretch is Lena's: %v", names)
	}
	merged := false
	if vs, _ := Voices(ctx, a, x.ID, true); len(vs) == 2 {
		for _, v := range vs {
			merged = merged || (v.Label == "speaker_00" && v.MergedInto == fix.Label && v.MergedBy == models.SpeakerByAuto)
		}
	}
	if !merged || names[0] != "Lena" {
		t.Fatalf("the same voice elsewhere in the clip should be recognized as Lena and combined with hers (what the undo must take back): %v", names)
	}

	var as models.Asset
	if err := a.DB.GetByID(ctx, "assets", a.WorkspaceID(), x.ID, &as); err != nil {
		t.Fatal(err)
	}
	segs, err := loadSegments(ctx, a, x.ID)
	if err != nil {
		t.Fatal(err)
	}
	if why := CanRestore(&as, segs, 5, 9, fix); why != "" {
		t.Fatalf("it can be undone: %s", why)
	}
	if _, err := Restore(ctx, a, x.ID, 5, 9, fix); err != nil {
		t.Fatal(err)
	}
	for at, n := range namesByTime(t, a, x.ID) {
		if n != "" {
			t.Fatalf("after the undo no word is anyone's (the voice was never named): %q at %.1f s", n, at)
		}
	}
	segs, _ = loadSegments(ctx, a, x.ID)
	for _, s := range segs {
		if s.Speaker != "speaker_00" || s.SpeakerFrom != "" {
			t.Fatalf("every line is the clip's own voice again: %+v", s)
		}
	}
	vs, err := Voices(ctx, a, x.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 || vs[0].Label != "speaker_00" || vs[0].Name != "" || vs[0].MergedInto != "" || vs[0].Words != 20 || len(vs[0].Confirmed) != 0 {
		t.Fatalf("one voice, whole, nobody's, nothing confirmed: %+v", vs)
	}
	if p := profileOf(t, a, ar.Person.ID); len(p.Exemplars) != 0 {
		t.Fatalf("Lena keeps no voice from the undone fix: %d exemplars", len(p.Exemplars))
	}
}

package e2e

import (
	"context"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// A name mix-up, with real voices (a woman, S1, and a man, S2): the man's
// voice is named with the woman's name, then her voice gets the same name
// (merged), and a recording of him alone is said to be "Daniel". Fixing who
// said what and rebuilding from confirmed clips must leave two voices that
// do not sound alike at all, with no "same person?" question left.
func TestWrongNameMergeFixAndRebuild(t *testing.T) {
	in, ts := setup(t)
	a := in.app
	wav, turns := speechFixtures(t, ts)
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("voice fingerprints need the sherpa-onnx C library (macOS/Linux)")
	}
	dir := t.TempDir()
	proj := call[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Mix-up"})
	both := process(t, a, cut(t, ts, wav, filepath.Join(dir, "both.m4a"), 0, turns[len(turns)-1].End+0.4), proj.Project.ID)
	// Him alone: his last two turns.
	alone := process(t, a, cut(t, ts, wav, filepath.Join(dir, "him.m4a"), turns[5].Start-0.3, turns[7].End+0.3), proj.Project.ID)
	sp := call[ops.SpeakersOut](t, a, "footage.speakers", ops.SpeakersIn{AssetID: both})
	if len(sp.Speakers) != 2 {
		t.Fatalf("want 2 voices, got %+v", sp.Speakers)
	}
	labelOf := map[string]string{}
	for _, v := range sp.Speakers {
		labelOf[truthAt(turns, 0, (v.Samples[0].From+v.Samples[0].To)/2)] = v.Label
	}
	// The wrong name first (his voice called by her name), then hers too: merged.
	call[ops.SpeakerRenameOut](t, a, "footage.speakers.rename", ops.SpeakerRenameIn{AssetID: both, Speaker: labelOf["S2"], Name: "Samantha"})
	_, err := ops.CallTyped[ops.SpeakerRenameOut](context.Background(), a, "footage.speakers.rename", ops.SpeakerRenameIn{AssetID: both, Speaker: labelOf["S1"], Name: "Samantha"}, ops.Caller{})
	if ae := apperr.From(err); err == nil || ae.Code != apperr.ConfirmRequired || !strings.Contains(ae.Message, "one is much lower") {
		t.Fatalf("a second voice with the same name must be confirmed, saying the voices sound different: %v", err)
	}
	call[ops.SpeakerRenameOut](t, a, "footage.speakers.rename", ops.SpeakerRenameIn{AssetID: both, Speaker: labelOf["S1"], Name: "Samantha", SamePerson: true})
	samantha := call[ops.PersonShowOut](t, a, "people.show", ops.PersonIn{ID: "Samantha"}).Person.ID
	solo := call[ops.SpeakersOut](t, a, "footage.speakers", ops.SpeakersIn{AssetID: alone})
	if len(solo.Speakers) != 1 {
		t.Fatalf("him alone: %+v", solo.Speakers)
	}
	if solo.Speakers[0].PersonID == samantha {
		call[ops.PersonOut](t, a, "people.split", ops.PersonSplitIn{ID: samantha, AssetID: alone, Speaker: solo.Speakers[0].Label, Name: "Daniel", NewPerson: true})
	} else {
		call[ops.SpeakerRenameOut](t, a, "footage.speakers.rename", ops.SpeakerRenameIn{AssetID: alone, Speaker: solo.Speakers[0].Label, Name: "Daniel", NewPerson: true})
	}
	before := call[ops.PersonCompareOut](t, a, "people.compare", ops.PersonPairIn{A: "Samantha", B: "Daniel"})
	if before.Score < 0.40 {
		t.Fatalf("with his voice in her profile they should sound alike first: %.2f", before.Score)
	}

	// The fix: every line of his in the first recording is Daniel's.
	for _, tt := range turns {
		if tt.Speaker == "S2" {
			out := call[ops.SpeakerAssignOut](t, a, "footage.speakers.assign", ops.SpeakerAssignIn{AssetID: both, From: tt.Start - 0.1, To: tt.End + 0.1, Person: "Daniel"})
			if !out.Relearned {
				t.Fatal("the voices should be learned again from the confirmed lines")
			}
		}
	}
	rb := call[ops.PersonRebuildOut](t, a, "people.rebuild", ops.PersonRebuildIn{ID: "Samantha"})
	if rb.Report == nil || rb.Report.Voices == 0 || len(rb.Report.Conflicts) != 0 {
		t.Fatalf("rebuild: %+v", rb)
	}
	after := call[ops.PersonCompareOut](t, a, "people.compare", ops.PersonPairIn{A: "Samantha", B: "Daniel"})
	t.Logf("Samantha vs Daniel: %.2f before the fix, %.2f after; pitch %.0f Hz vs %.0f Hz", before.Score, after.Score, after.PitchA, after.PitchB)
	if after.Score >= 0.40 || !after.PitchDiffers {
		t.Fatalf("after fixing and rebuilding they must not sound alike: %+v", after)
	}
	if left := call[ops.TodoListOut](t, a, "todos.list", ops.TodoListIn{Kind: models.TodoDuplicatePerson}).Todos; len(left) != 0 {
		t.Fatalf("no 'same person?' question may remain: %+v", left)
	}
	tr := call[ops.TranscriptOut](t, a, "footage.transcript", ops.TranscriptIn{AssetID: both, Words: true})
	right, total := 0, 0
	for _, s := range tr.Segments {
		for _, w := range s.Words {
			if who := truthAt(turns, 0, (w.From+w.To)/2); who != "" {
				total++
				if map[string]string{"S1": "Samantha", "S2": "Daniel"}[who] == s.SpeakerName {
					right++
				}
			}
		}
	}
	if total == 0 || float64(right)/float64(total) < 0.95 {
		t.Fatalf("words labeled correctly after the fix: %d/%d", right, total)
	}
}

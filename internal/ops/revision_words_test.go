package ops_test

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A version saved before the wording changed (D109) shows its warnings in
// today's plain words on the version page. The saved list — codes, places and
// severities — is exactly what was saved, and the saved version is untouched.
func TestVersionPageSaysOldWarningsInTodaysWords(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := project(t, a, "Garden story")
	c := clip(t, a, "Garage interview", p.ID) // landscape footage in a vertical cut: cropped hard
	rev := saveCut(t, a, p.ID, "", c.ID)
	old := []editspec.Issue{
		{Severity: "warning", Code: "heavy_crop", Path: "/clips/0/framing", Message: "fill crops 68% of garage-interview.mov; set framing.focusX/focusY on the subject, or framing.mode=fit"},
		{Severity: "warning", Code: "retired_check", Path: "/clips/0", Message: "a warning this version of MetaRush no longer gives"},
		{Severity: "warning", Code: "rights_unknown", Path: "/audio/0/rights", Message: "music bed has no rights/attribution recorded (set them on the track, or on the file with `metarush asset rights`)"},
		{Severity: "warning", Code: "captions_no_evidence", Path: "/captions/x", Message: "no transcript with timing exists for every spoken clip, so captions will be omitted; run `metarush footage analyze` first"},
	}
	if err := a.DB.Update(ctx, store.CRevisions, a.WorkspaceID(), rev, bson.M{"$set": bson.M{"warnings": old}}); err != nil {
		t.Fatal(err)
	}
	out := do[ops.RevisionShowOut](t, a, "revision.show", ops.RevisionShowIn{ID: rev})
	ws := out.Summary.Warnings
	if len(ws) != 4 || ws[0].Code != "heavy_crop" || ws[0].Path != "/clips/0/framing" || ws[0].Severity != "warning" || ws[1].Code != "retired_check" || ws[3].Code != "captions_no_evidence" {
		t.Fatalf("the saved list changed: %+v", ws)
	}
	if ws[0].Message == old[0].Message || strings.Contains(ws[0].Message, "framing.") {
		t.Errorf("the crop warning still reads as it was saved: %q", ws[0].Message)
	}
	if ws[1].Message != old[1].Message {
		t.Errorf("a warning the check no longer gives keeps its saved words, got %q", ws[1].Message)
	}
	// Fixed since it was saved: the saved words stay, without the commands in them.
	for i, want := range map[int]string{2: "Music bed has no rights/attribution recorded.", 3: "No transcript with timing exists for every spoken clip, so captions will be omitted."} {
		if ws[i].Message != want {
			t.Errorf("warning %d: got %q, want %q", i, ws[i].Message, want)
		}
	}
	var saved models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rev, &saved); err != nil || saved.Warnings[0].Message != old[0].Message {
		t.Fatalf("the saved version was rewritten: %v %+v", err, saved.Warnings)
	}
}

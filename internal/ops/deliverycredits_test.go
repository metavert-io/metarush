package ops_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Footage an ability brought in with a licence that asks for credit (D283):
// the delivery's credits say the credit line, and the .notes.md file carries
// it, ready to paste — a file of its own when the version has no notes.
func TestDeliveryCreditsFromAbilities(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := project(t, a, "Coast film")
	as := txClip(t, a, "Waves", p.ID, []models.Word{wd("The", 0.1, 0.3, 0.9), wd("sea.", 0.35, 0.9, 0.9)})
	made := &models.AbilityMadeWith{Ability: "open-media@0123abcd", Name: "open-media", Title: "Public-domain and openly licensed media", RunID: "abr_1",
		Licence: "CC BY-SA 4.0", Source: "https://commons.wikimedia.org/wiki/File:Waves.webm", Author: "Example Walker",
		Credit: "“Waves” by Example Walker, CC BY-SA 4.0, via Wikimedia Commons"}
	src := models.AssetSource{Platform: models.PlatformAbility, URL: made.Source, License: made.Licence, Uploader: made.Author, Method: "ability", Tool: "MetaRush ability " + made.Ability, MadeWith: made}
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"source": src}}); err != nil {
		t.Fatal(err)
	}
	rid := captionedCut(t, a, p.ID, as.ID)
	reviewRender(t, a, rid)
	person := ops.Caller{Human: true}
	want := "Includes footage brought in with Public-domain and openly licensed media — “Waves” by Example Walker, CC BY-SA 4.0, via Wikimedia Commons: https://commons.wikimedia.org/wiki/File:Waves.webm"

	dir := t.TempDir()
	out, err := ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: dir, Quality: "review", AllowDraft: true}, person)
	if err != nil || len(out.Files) != 1 || len(out.Credits) != 1 || out.Credits[0] != want || !strings.Contains(out.Note, "credits its outside files ask for") {
		t.Fatalf("deliver %+v %v", out, err)
	}
	notes := filepath.Join(dir, strings.TrimSuffix(filepath.Base(out.Files[0].Path), filepath.Ext(out.Files[0].Path))+".notes.md")
	b, err := os.ReadFile(notes)
	if err != nil || !strings.Contains(string(b), "## Credits\n\n- "+want+"\n") || !strings.Contains(string(b), "ready to paste") {
		t.Fatalf("the notes file %q %v", b, err)
	}

	// With delivery notes, the credits follow them in the same file.
	do[ops.DeliveryNotesOut](t, a, "delivery.notes.set", ops.DeliveryNotesSetIn{ProjectID: p.ID, Notes: ops.DeliveryNotesDraft{Title: "The coast", Description: "Waves."}})
	dir = t.TempDir()
	out, err = ops.CallTyped[ops.DeliverOut](ctx, a, "deliver.export", ops.DeliverIn{RevisionIDs: []string{rid}, Out: dir, Quality: "review", AllowDraft: true}, person)
	if err != nil || len(out.Files) != 1 {
		t.Fatalf("deliver %+v %v", out, err)
	}
	notes = filepath.Join(dir, strings.TrimSuffix(filepath.Base(out.Files[0].Path), filepath.Ext(out.Files[0].Path))+".notes.md")
	b, _ = os.ReadFile(notes)
	if s := string(b); !strings.HasPrefix(s, "# The coast") || !strings.Contains(s, "Waves.") || !strings.HasSuffix(s, "## Credits\n\n- "+want+"\n") {
		t.Fatalf("notes with credits %q", b)
	}
}

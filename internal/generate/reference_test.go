package generate_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Nothing is made from a reference — someone else's video, kept to learn from
// (D441): a shot from one of its frames, between two of its frames, or that
// continues it is refused in plain words before anything is priced or sent,
// with the command a person runs if the video is theirs.
func TestNothingIsGeneratedFromAReference(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	theirs := e.clipIn(t, "theirs", e.project, 3, true)
	if err := e.a.DB.Update(ctx, store.CAssets, e.a.WorkspaceID(), theirs.ID, bson.M{"$set": bson.M{"reference": models.AssetReference{By: e.a.Actor, At: time.Now().UTC(), Why: models.ReferenceStartFrom}}}); err != nil {
		t.Fatal(err)
	}
	refused := func(what string, err error) {
		t.Helper()
		ae := apperr.From(err)
		if ae == nil || ae.Code != apperr.Conflict || !strings.Contains(ae.Message, "a reference: someone else's video, kept to learn from") ||
			ae.Command() != "metarush footage reference "+theirs.ID+" --mine" {
			t.Errorf("%s: refused, with the command behind Details: %v", what, err)
		}
	}
	frame := &generate.FrameRef{AssetID: theirs.ID, At: 1}
	_, err := call[generate.GenEstimate](t, e.a, "generate.estimate", generate.GenShotIn{AspectRatio: "16:9", ProjectID: e.project, Prompt: "a red paper boat", From: frame, Seconds: 4}, false)
	refused("a shot from its frame", err)
	mine := e.clipIn(t, "mine", e.project, 3, true)
	_, err = call[generate.GenEstimate](t, e.a, "generate.estimate", generate.GenShotIn{AspectRatio: "16:9", ProjectID: e.project, Prompt: "a red paper boat",
		From: &generate.FrameRef{AssetID: mine.ID, At: 1}, To: frame, Seconds: 4}, false)
	refused("a shot ending on its frame", err)
	_, err = call[generate.GenRunOut](t, e.a, "generate.extend", generate.GenExtendIn{ID: theirs.ID, Seconds: 4, DryRun: true}, false)
	refused("continuing it", err)
}

package aiwork_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// AI work never takes a reference — someone else's video, kept to learn from
// (D441): dubbing it, changing its voices, transcribing it in the cloud or
// copying a voice from it is refused in plain words before anything is priced
// or sent, with the command a person runs if the video is theirs.
func TestAIWorkNeverTakesAReference(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	theirs := e.sound(t, "theirs", 3)
	if err := e.a.DB.Update(ctx, store.CAssets, e.a.WorkspaceID(), theirs.ID, bson.M{"$set": bson.M{"reference": models.AssetReference{By: e.a.Actor, At: time.Now().UTC(), Why: models.ReferenceStartFrom}}}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []aiwork.MakeIn{
		{Kind: aiservices.KindDubbing, ProjectID: e.project, Model: "fake/fake-dubbing", Clip: theirs.ID, Options: models.GenOptions{TargetLanguage: "es"}},
		{Kind: aiservices.KindVoiceChange, ProjectID: e.project, Model: "fake/fake-voice-change", Clip: theirs.ID, Options: models.GenOptions{Voice: "Kore"}},
		{Kind: aiservices.KindTranscription, ProjectID: e.project, Model: "fake/fake-transcription", Clip: theirs.ID},
	} {
		_, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
		ae := apperr.From(err)
		if ae == nil || ae.Code != apperr.Conflict || !strings.Contains(ae.Message, "a reference: someone else's video, kept to learn from") ||
			ae.Command() != "metarush footage reference "+theirs.ID+" --mine" {
			t.Errorf("%s from a reference: %v", in.Kind, err)
		}
	}
}

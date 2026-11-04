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
	"github.com/metavert-io/metarush/internal/generate"
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

// Nor does it take a picture taken from a reference (D457): a frame of
// someone else's video sent with a picture request, or changed (its
// background taken away, made larger), is refused at the estimate the same
// way, before anything is priced or sent.
func TestAIWorkNeverTakesAPictureTakenFromAReference(t *testing.T) {
	e := setup(t)
	ctx := context.Background()
	theirs := e.sound(t, "theirs", 3)
	if err := e.a.DB.Update(ctx, store.CAssets, e.a.WorkspaceID(), theirs.ID, bson.M{"$set": bson.M{"reference": models.AssetReference{By: e.a.Actor, At: time.Now().UTC(), Why: models.ReferenceStartFrom}}}); err != nil {
		t.Fatal(err)
	}
	pic := e.picture(t, "hook-still", 48, 32)
	if _, err := e.a.DB.C(store.CResources).UpdateOne(ctx, bson.M{"_id": pic.ID}, bson.M{"$set": bson.M{"versions.$[].source.frameOf": models.ResourceFrame{AssetID: theirs.ID, At: 1}}}); err != nil {
		t.Fatal(err)
	}
	for _, in := range []aiwork.MakeIn{
		{Kind: aiservices.KindPicture, ProjectID: e.project, Model: "fake/fake-picture", Prompt: "the same, in watercolour", Pictures: []string{pic.Name}},
		{Kind: aiservices.KindBackground, ProjectID: e.project, Pictures: []string{pic.Name}},
		{Kind: aiservices.KindUpscale, ProjectID: e.project, Pictures: []string{pic.Name}, Options: models.GenOptions{Scale: 4}},
	} {
		_, err := call[aiwork.AIEstimate](t, e.a, "ai.estimate", in, false)
		ae := apperr.From(err)
		if ae == nil || ae.Code != apperr.Conflict || !strings.Contains(ae.Message, "is a frame of “theirs”, a reference: someone else's video, kept to learn from") ||
			ae.Command() != "metarush footage reference "+theirs.ID+" --mine" {
			t.Errorf("%s of a picture taken from a reference: %v", in.Kind, err)
		}
	}
	// Kept by AI work's keeper too (what a plan made before it was marked would send).
	if _, _, _, err := generate.Picture(ctx, e.a, e.project, pic.Name); apperr.From(err) == nil || !strings.Contains(apperr.From(err).Message, "a reference: someone else's video") {
		t.Errorf("keeping the picture to send: %v", err)
	}
}

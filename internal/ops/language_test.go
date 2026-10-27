package ops

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// A clip's language (D243): said plainly with how MetaRush knows; setting it
// keeps who set it and asks for the words to be written down again in that
// language; the same language again changes nothing.
func TestFootageLanguage(t *testing.T) {
	a, _, video, music := todoApp(t)
	ctx := context.Background()
	at := time.Now().UTC()
	set := func(id string, v bson.M) {
		t.Helper()
		if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), id, bson.M{"$set": v}); err != nil {
			t.Fatal(err)
		}
	}
	show := func(id string) *FootageLanguageOut {
		t.Helper()
		out, err := CallTyped[FootageLanguageOut](ctx, a, "footage.language", FootageLanguageIn{AssetID: id}, Caller{})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	// Transcribed before languages were listened for: the transcript's says.
	set(video, bson.M{"transcript": models.TranscriptStatus{State: "available", Provider: "parakeet", Model: "tdt-0.6b-v2-int8", Language: "en", Words: 40, UpdatedAt: at}})
	if out := show(video); out.Name != "English" || !strings.Contains(out.Note, "wasn't listened for") || out.Language != nil {
		t.Fatalf("older clip: %+v", out)
	}
	// Heard, sure.
	set(video, bson.M{"language": models.SpokenLanguage{Code: "es", By: models.LanguageHeard, Score: 0.98, Listened: 60, At: at},
		"transcript.language": "es", "transcript.provider": "whisper.cpp"})
	if out := show(video); out.Name != "Spanish" || !out.Sure || !strings.Contains(out.Note, "heard") || out.WordsIn != "" {
		t.Fatalf("heard: %+v", out)
	}
	// Heard another language, not surely: the workspace's language, and what it sounded like.
	set(video, bson.M{"language": models.SpokenLanguage{Code: "en", By: models.LanguageSettings, Heard: "fr", Score: 0.52, At: at}, "transcript.language": "en"})
	if out := show(video); out.Sure || out.HeardName != "French" || !strings.Contains(out.Note, "sounded like French") {
		t.Fatalf("unsure: %+v", out)
	}
	if out, err := CallTyped[FootageLanguageOut](ctx, a, "footage.language", FootageLanguageIn{AssetID: video, Choices: true}, Caller{}); err != nil || len(out.Choices) < 90 || out.Choices[0].Code != "en" {
		t.Fatalf("choices: %v %+v", err, out)
	}

	// A person sets it: by name or code, kept with who, a transcription asked for.
	res, err := CallTyped[FootageLanguageSetOut](ctx, a, "footage.language.set", FootageLanguageSetIn{AssetID: video, Language: "French"}, Caller{})
	if err != nil || res.Language != "fr" || res.JobID == "" || !strings.Contains(res.Note, "again in French") {
		t.Fatalf("set: %v %+v", err, res)
	}
	j, err := a.Queue.Get(ctx, res.JobID)
	if err != nil || j.Type != jobs.TypeTranscribe || j.AssetID != video || j.Payload["language"] != "fr" || j.Payload["languageBy"] != models.LanguagePerson {
		t.Fatalf("job: %+v %v", j, err)
	}
	var as models.Asset
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), video, &as)
	if as.Language == nil || as.Language.Code != "fr" || as.Language.By != models.LanguagePerson || as.Language.UserID != a.Actor.UserID {
		t.Fatalf("stored: %+v", as.Language)
	}
	if out := show(video); out.WordsIn != "English" || !strings.Contains(out.Note, "still in English") {
		t.Fatalf("words not written down again yet: %+v", out)
	}
	// Once the words are French, setting French again is nothing to do.
	set(video, bson.M{"transcript.language": "fr"})
	again, err := CallTyped[FootageLanguageSetOut](ctx, a, "footage.language.set", FootageLanguageSetIn{AssetID: video, Language: "fr-CA"}, Caller{})
	if err != nil || again.JobID != "" || !strings.Contains(again.Note, "already") {
		t.Fatalf("same again: %v %+v", err, again)
	}
	// Unknown languages and clips without sound are refused plainly.
	if _, err := CallTyped[FootageLanguageSetOut](ctx, a, "footage.language.set", FootageLanguageSetIn{AssetID: video, Language: "Klingon"}, Caller{}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("unknown language: %v", err)
	}
	set(music, bson.M{"media.hasAudio": false})
	if _, err := CallTyped[FootageLanguageSetOut](ctx, a, "footage.language.set", FootageLanguageSetIn{AssetID: music, Language: "es"}, Caller{}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("no sound: %v", err)
	}
}

func TestSpokenLanguageCode(t *testing.T) {
	for in, want := range map[string]string{"es": "es", "ES": "es", "es-419": "es", "pt_BR": "pt", "Spanish": "es", "japanese": "ja", "Haitian Creole": "ht", "yue": "yue"} {
		if got, ok := SpokenLanguageCode(in); !ok || got != want {
			t.Errorf("%q: %q %v, want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "xx", "Klingon", "e"} {
		if got, ok := SpokenLanguageCode(bad); ok {
			t.Errorf("%q read as %q", bad, got)
		}
	}
}

package worker

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/transcribe"
)

type heardAs struct {
	h     transcribe.Heard
	err   error
	calls int
}

func (d *heardAs) Detect(context.Context, string, string) (transcribe.Heard, error) {
	d.calls++
	return d.h, d.err
}

// recognizer is a stand-in that records the language it was asked for;
// during, when set, runs while it "listens" (something happening meanwhile).
type recognizer struct {
	name   string
	asked  []string
	during func()
}

func (r *recognizer) Name() string  { return r.name }
func (r *recognizer) Model() string { return "test" }
func (r *recognizer) Transcribe(_ context.Context, _, _, language string) (*transcribe.Transcript, error) {
	r.asked = append(r.asked, language)
	if r.during != nil {
		r.during()
	}
	return &transcribe.Transcript{Language: language, Segments: []transcribe.Segment{{From: 0.2, To: 1.0, Text: "Hola.",
		Words: []models.Word{{Text: "Hola.", From: 0.2, To: 1.0, Score: 0.9}}}}}, nil
}

// A person's language wins, then the one heard before, then what is heard
// now when it is sure, else the workspace's (saying what it sounded like).
func TestClipLanguage(t *testing.T) {
	en, other := &recognizer{name: "english"}, &recognizer{name: "other"}
	w := &Worker{Log: func(string, ...any) {}}
	pick := func(det transcribe.LanguageDetector, as *models.Asset, payload map[string]any) models.SpokenLanguage {
		w.Langs = transcribe.LanguagesWith("en", en, other, det)
		return clipLanguage(context.Background(), w, as, &models.Job{Payload: payload}, "a.wav", t.TempDir())
	}
	sure := &heardAs{h: transcribe.Heard{Language: "es", Score: 0.97, Listened: 60}}
	if l := pick(sure, &models.Asset{}, nil); l.Code != "es" || l.By != models.LanguageHeard || l.Score != 0.97 {
		t.Fatalf("heard surely: %+v", l)
	}
	unsure := &heardAs{h: transcribe.Heard{Language: "fr", Score: 0.5}}
	if l := pick(unsure, &models.Asset{}, nil); l.Code != "en" || l.By != models.LanguageSettings || l.Heard != "fr" {
		t.Fatalf("not sure: %+v", l)
	}
	english := &heardAs{h: transcribe.Heard{Language: "en", Score: 0.6}}
	if l := pick(english, &models.Asset{}, nil); l.Code != "en" || l.By != models.LanguageHeard {
		t.Fatalf("the workspace's language heard, even unsurely: %+v", l)
	}
	if l := pick(&heardAs{}, &models.Asset{}, nil); l.Code != "en" || l.By != models.LanguageSettings || l.Heard != "" {
		t.Fatalf("too little speech: %+v", l)
	}
	if l := pick(&heardAs{err: errors.New("no")}, &models.Asset{}, nil); l.Code != "en" || l.By != models.LanguageSettings {
		t.Fatalf("detection failed: %+v", l)
	}
	if l := pick(nil, &models.Asset{}, nil); l.Code != "en" || l.By != models.LanguageSettings {
		t.Fatalf("no detection: %+v", l)
	}
	before := &models.Asset{Language: &models.SpokenLanguage{Code: "de", By: models.LanguageHeard, Score: 0.9}}
	sure.calls = 0
	if l := pick(sure, before, nil); l.Code != "de" || sure.calls != 0 {
		t.Fatalf("heard before stays, without listening again: %+v %d", l, sure.calls)
	}
	person := &models.Asset{Language: &models.SpokenLanguage{Code: "pt", By: models.LanguagePerson, UserID: "usr_1"}}
	if l := pick(sure, person, nil); l.Code != "pt" || l.By != models.LanguagePerson {
		t.Fatalf("a person's: %+v", l)
	}
	if l := pick(sure, person, map[string]any{"language": "ja", "languageBy": "person", "userId": "usr_2"}); l.Code != "ja" || l.UserID != "usr_2" {
		t.Fatalf("the job's: %+v", l)
	}
	// Settings that left the language to the workspace listen again.
	if l := pick(sure, &models.Asset{Language: &models.SpokenLanguage{Code: "en", By: models.LanguageSettings}}, nil); l.Code != "es" {
		t.Fatalf("listened again: %+v", l)
	}
}

func TestCutLanguage(t *testing.T) {
	words := func(n int) []editspec.TranscriptWord { return make([]editspec.TranscriptWord, n) }
	assets := []models.Asset{
		{ID: "a", Language: &models.SpokenLanguage{Code: "es"}},
		{ID: "b", Transcript: &models.TranscriptStatus{Language: "en"}},
		{ID: "c", Language: &models.SpokenLanguage{Code: "es"}},
		{ID: "d", Language: &models.SpokenLanguage{Code: "fr"}},
	}
	got := cutLanguage(assets, map[string][]editspec.TranscriptWord{"a": words(30), "b": words(50), "c": words(30), "d": words(5)}, "en")
	if got != "es" {
		t.Fatalf("most words: %q", got)
	}
	if got := cutLanguage(assets, nil, "de"); got != "de" {
		t.Fatalf("no words: %q", got)
	}
}

// The transcribe job writes a clip down with the recognizer for its language
// and keeps the language on the clip; a person's language transcribes it
// again with the other recognizer (D243).
func TestTranscribeHandlerUsesTheClipsLanguage(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plaza.wav")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"aevalsrc=0.25*sin(2*PI*220*t)*(0.6+0.4*sin(2*PI*3*t)):s=48000:d=4", "-c:a", "pcm_s16le", path).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	r := ingest.File(ctx, a, path, ingest.Options{Via: "cli"})
	if r.AssetID == "" {
		t.Fatalf("ingest: %+v", r)
	}
	load := func() *models.Asset {
		t.Helper()
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, ws, r.AssetID, &as); err != nil {
			t.Fatal(err)
		}
		return &as
	}
	en, other := &recognizer{name: "english"}, &recognizer{name: "other"}
	w := New(a)
	w.Spk = nil
	w.Tx, w.Langs = en, transcribe.LanguagesWith("en", en, other, &heardAs{h: transcribe.Heard{Language: "es", Score: 0.96, Listened: 4}})
	run := func(j *models.Job) map[string]any {
		t.Helper()
		out, err := transcribeHandler(ctx, w, j, func(float64, string) {})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	out := run(&models.Job{ID: ids.New(ids.Job), Type: jobs.TypeTranscribe, AssetID: r.AssetID, Payload: map[string]any{"assetId": r.AssetID}})
	as := load()
	if len(other.asked) != 1 || other.asked[0] != "es" || len(en.asked) != 0 {
		t.Fatalf("Spanish goes to the other recognizer: en %v other %v", en.asked, other.asked)
	}
	if as.Language == nil || as.Language.Code != "es" || as.Language.By != models.LanguageHeard || as.Transcript.Provider != "other" || as.Transcript.Language != "es" {
		t.Fatalf("stored: %+v %+v", as.Language, as.Transcript)
	}
	if out["languageBy"] != models.LanguageHeard || out["language"] != "es" {
		t.Fatalf("job result: %+v", out)
	}
	// A person says it is English: it is written down again on the English recognizer.
	set, err := ops.CallTyped[ops.FootageLanguageSetOut](ctx, a, "footage.language.set", ops.FootageLanguageSetIn{AssetID: r.AssetID, Language: "English"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	j, err := a.Queue.Get(ctx, set.JobID)
	if err != nil {
		t.Fatal(err)
	}
	run(j)
	as = load()
	if len(en.asked) != 1 || en.asked[0] != "en" || as.Language.By != models.LanguagePerson || as.Transcript.Provider != "english" {
		t.Fatalf("a person's English: en %v, %+v %+v", en.asked, as.Language, as.Transcript)
	}
	// Without a recognizer for the language, the job says so plainly.
	w.Langs = transcribe.LanguagesWith("en", en, nil, nil)
	_, err = transcribeHandler(ctx, w, &models.Job{ID: ids.New(ids.Job), Type: jobs.TypeTranscribe, AssetID: r.AssetID,
		Payload: map[string]any{"assetId": r.AssetID, "language": "ja"}}, func(float64, string) {})
	var p *Permanent
	if !errors.As(err, &p) {
		t.Fatalf("no Japanese here: %v", err)
	}
}

// A person who sets a clip's language while a transcription of it runs gets
// the words in their language: the running job's words (heard as Spanish)
// are dropped with a note and never written over the person's choice; the
// job their choice queued writes French (D243).
func TestALanguageSetWhileTranscribingWins(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	if err := a.DB.Insert(ctx, store.CWorkspaces, models.Workspace{ID: ws, WorkspaceID: ws, Name: "T", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "plaza.wav")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i",
		"aevalsrc=0.25*sin(2*PI*330*t):s=48000:d=4", "-c:a", "pcm_s16le", path).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	r := ingest.File(ctx, a, path, ingest.Options{Via: "cli"})
	if r.AssetID == "" {
		t.Fatalf("ingest: %+v", r)
	}
	en, other := &recognizer{name: "english"}, &recognizer{name: "other"}
	var personJob string
	other.during = func() {
		if personJob != "" {
			return // the person's own job runs undisturbed
		}
		out, err := ops.CallTyped[ops.FootageLanguageSetOut](ctx, a, "footage.language.set", ops.FootageLanguageSetIn{AssetID: r.AssetID, Language: "fr"}, ops.Caller{Human: true})
		if err != nil {
			t.Error(err)
			return
		}
		personJob = out.JobID
	}
	w := New(a)
	w.Spk = nil
	w.Tx, w.Langs = en, transcribe.LanguagesWith("en", en, other, &heardAs{h: transcribe.Heard{Language: "es", Score: 0.97}})
	time.Sleep(5 * time.Millisecond)
	out, err := transcribeHandler(ctx, w, &models.Job{ID: ids.New(ids.Job), Type: jobs.TypeTranscribe, AssetID: r.AssetID, Payload: map[string]any{"assetId": r.AssetID}}, func(float64, string) {})
	if err != nil {
		t.Fatal(err)
	}
	var as models.Asset
	_ = a.DB.GetByID(ctx, store.CAssets, ws, r.AssetID, &as)
	n, _ := a.DB.C(store.CSegments).CountDocuments(ctx, bson.M{"assetId": r.AssetID})
	if out["superseded"] != true || n != 0 || as.Language == nil || as.Language.Code != "fr" || as.Language.By != models.LanguagePerson {
		t.Fatalf("the running job's words are dropped: %+v, %d lines, %+v", out, n, as.Language)
	}
	if note, _ := out["note"].(string); note == "" {
		t.Fatal("the job says why")
	}
	// The person's job writes French.
	j, err := a.Queue.Get(ctx, personJob)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := transcribeHandler(ctx, w, j, func(float64, string) {}); err != nil {
		t.Fatal(err)
	}
	_ = a.DB.GetByID(ctx, store.CAssets, ws, r.AssetID, &as)
	n, _ = a.DB.C(store.CSegments).CountDocuments(ctx, bson.M{"assetId": r.AssetID})
	if n == 0 || as.Transcript.Language != "fr" || as.Language.Code != "fr" || as.Language.By != models.LanguagePerson {
		t.Fatalf("French: %d lines, %+v %+v", n, as.Transcript, as.Language)
	}
}

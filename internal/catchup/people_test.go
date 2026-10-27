package catchup

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// After the upgrade to D189, the open questions about people are looked at
// again with the new rules: "Is this Ada?" that rested on her voice alone
// becomes "Who is this?" with Ada as a quick pick, the junk answers read from
// the transcript ("I'm", "You're") go, a question a person answered stays
// answered (and is scored), and running it again changes nothing.
func TestPersonQuestionsCatchUp(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	// A clip: one face alone on screen for 20 s while Ada talks, then a voice nobody named.
	asset := ids.New(ids.Asset)
	as := models.Asset{ID: asset, WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{}, Kind: "video", Name: "call.mp4", Title: "Call", SHA256: ids.New("sha"), State: models.AssetAvailable,
		Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 30, Width: 1280, Height: 720}, Variants: map[string]models.Variant{},
		Transcript: &models.TranscriptStatus{State: "available", Segments: 2}, Speakers: &models.SpeakersStatus{State: "available", Count: 2}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	q := 0.7
	var samples []visual.Sample
	for s := 0.5; s < 20; s++ {
		samples = append(samples, visual.Sample{At: s, Frame: vision.Frame{W: 1280, H: 720, Faces: []vision.Face{{Conf: 1, Box: vision.Box{0.35, 0.15, 0.3, 0.5}, Quality: &q}}}})
	}
	idx := visual.Build(samples, 1, 30, nil, "proxy")
	if err := a.DB.Insert(ctx, store.CInsights, models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: asset, Kind: visual.InsightKind, Provider: "apple-vision",
		Digest: "d", Data: visual.ToMap(idx), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := faces.Save(ctx, a, asset, idx); err != nil {
		t.Fatal(err)
	}
	ada := models.Person{ID: ids.New(ids.Person), WorkspaceID: a.WorkspaceID(), Name: "Ada", NameKey: "ada", CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CPeople, ada); err != nil {
		t.Fatal(err)
	}
	for i, s := range []models.TranscriptSegment{{From: 0.2, To: 19.8, Speaker: "speaker_00", Text: "Welcome to the garden call."},
		{From: 20.2, To: 29, Speaker: "speaker_01", Text: "I'm I'm going to share my screen."}} {
		s.ID, s.WorkspaceID, s.AssetID, s.Index = ids.New(ids.Segment), a.WorkspaceID(), asset, i
		if err := a.DB.Insert(ctx, store.CSegments, s); err != nil {
			t.Fatal(err)
		}
	}
	for _, v := range []models.AssetSpeaker{{Label: "speaker_00", PersonID: ada.ID, Name: ada.Name, Source: models.SpeakerByPerson, Seconds: 19.6}, {Label: "speaker_01", Seconds: 8.8}} {
		v.ID, v.WorkspaceID, v.AssetID, v.Run, v.Samples, v.CreatedAt, v.UpdatedAt = ids.New(ids.Speaker), a.WorkspaceID(), asset, "run_1", []models.TimeSpan{}, now, now
		if err := a.DB.Insert(ctx, store.CSpeakers, v); err != nil {
			t.Fatal(err)
		}
	}
	todo := func(t0 models.Todo) models.Todo {
		t0.ID, t0.WorkspaceID, t0.Scope, t0.AssetID, t0.ProjectIDs, t0.CreatedAt, t0.UpdatedAt = ids.New(ids.Todo), a.WorkspaceID(), "asset", asset, []string{}, now, now
		if t0.State == "" {
			t0.State = models.TodoOpen
		}
		if err := a.DB.Insert(ctx, store.CTodos, t0); err != nil {
			t.Fatal(err)
		}
		return t0
	}
	// What the earlier rules left: "Is this Ada?" from her voice alone, with junk
	// answers from the transcript; the other voice's question offering "I'm".
	old := todo(models.Todo{Kind: models.TodoFaceGroup, GroupKey: "facegroup:" + asset + ":fgr_old:" + ada.ID, Question: "Is this Ada?", FreeText: true, Priority: 3,
		Context: models.TodoContext{GroupID: "fgr_old", PersonID: ada.ID, PersonName: "Ada"},
		Suggestions: []models.TodoSuggestion{{Label: "Yes, it's Ada", Value: "yes", PersonID: ada.ID}, {Label: "I'm (said in the video)", Value: "I'm", New: true},
			{Label: "No, someone else", Value: "no"}}})
	voiceQ := todo(models.Todo{Kind: models.TodoSpeakerUnnamed, GroupKey: "speaker:" + asset + ":speaker_01@run_1", Question: "Who is Speaker 2 in “Call”?", FreeText: true, Priority: 1,
		Context:     models.TodoContext{Label: "speaker_01", Speaker: "Speaker 2"},
		Suggestions: []models.TodoSuggestion{{Label: "I'm (says “I'm I'm”)", Value: "I'm", New: true}, {Label: "Ada (in this project)", Value: "Ada", PersonID: ada.ID}}})
	answered := todo(models.Todo{Kind: models.TodoFacePerson, GroupKey: "face:" + asset + ":0.5:" + ada.ID, Question: "Is this Ada?", State: models.TodoAnswered,
		Resolution: models.ResolvedAnswered, Answer: "no",
		Detail:      "In “Call”, this face is the only one on screen while Ada is talking (about 2 seconds). If it is Ada, MetaRush puts that name on this face.",
		Context:     models.TodoContext{PersonID: ada.ID, PersonName: "Ada", Seconds: 2},
		Suggestions: []models.TodoSuggestion{{Label: "Yes, it's Ada", Value: "yes", PersonID: ada.ID}, {Label: "No, someone else", Value: "no"}}})

	steps := []Step{{Name: "person-questions", Version: 1, Run: personQuestions}}
	outs, err := RunSteps(ctx, a, steps)
	if err != nil || !outs[0].Ran || outs[0].Error != "" {
		t.Fatalf("ran: %+v %v", outs, err)
	}
	get := func(id string) models.Todo {
		var x models.Todo
		if err := a.DB.GetByID(ctx, store.CTodos, a.WorkspaceID(), id, &x); err != nil {
			t.Fatal(err)
		}
		return x
	}
	if o := get(old.ID); o.State == models.TodoOpen {
		t.Fatalf("the voice-only “Is this Ada?” no longer qualifies: %+v", o)
	}
	var open []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"assetId": asset, "state": models.TodoOpen}, &open); err != nil {
		t.Fatal(err)
	}
	var who *models.Todo
	for i := range open {
		if open[i].Kind == models.TodoFaceGroup {
			who = &open[i]
		}
		for _, s := range open[i].Suggestions {
			if s.Value == "I'm" || strings.HasPrefix(s.Label, "I'm") {
				t.Fatalf("junk answers go: %+v", open[i].Suggestions)
			}
		}
	}
	if who == nil || who.Question != "Who is this?" || len(who.Suggestions) == 0 || who.Suggestions[0].Label != "Ada (talking then)" {
		t.Fatalf("asked again as “Who is this?” with Ada as a pick: %+v", open)
	}
	if v := get(voiceQ.ID); v.State != models.TodoOpen {
		t.Fatalf("the voice's question stays open (without the junk): %+v", v)
	}
	if x := get(answered.ID); x.State != models.TodoAnswered || x.Answer != "no" || !x.Scored || len(x.Guesses) != 1 || x.Guesses[0].Agreed {
		t.Fatalf("the answered question stays answered, scored: %+v", x)
	}
	if !strings.Contains(outs[0].Summary, "kept score of 1 earlier answer") || !strings.Contains(outs[0].Summary, "closed 1 question about people") {
		t.Fatalf("summary: %q", outs[0].Summary)
	}
	// Again: nothing changes (the step itself, and the record).
	if s, err := personQuestions(ctx, a); err != nil || s != "" {
		t.Fatalf("again: %q %v", s, err)
	}
	var again []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"assetId": asset, "state": models.TodoOpen}, &again); err != nil || len(again) != len(open) {
		t.Fatalf("the same open questions: %d vs %d %v", len(again), len(open), err)
	}
	if outs, _ := RunSteps(ctx, a, steps); outs[0].Ran {
		t.Fatal("ran twice")
	}
}

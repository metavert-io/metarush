package faces

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

func q(v float64) *float64 { return &v }

// clip stores an asset with an on-screen index: face A alone for 0–6 s, then
// two faces together (A and B) for 6–10 s, then B alone for 10–13 s.
func clip(t *testing.T, a *app.App) (string, *visual.Index) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{}, Kind: "video", Name: "talk.mov", Title: "Talk",
		SHA256: strings.Repeat("cd", 32), Size: 1, MIME: "video/quicktime", StorageKey: "k", State: models.AssetAvailable,
		Media: models.MediaInfo{Duration: 13, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, Streams: []models.Stream{}}, Variants: map[string]models.Variant{},
		CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	faceA := vision.Face{Conf: 1, Box: vision.Box{0.2, 0.2, 0.2, 0.35}, Quality: q(0.6)}
	faceB := vision.Face{Conf: 1, Box: vision.Box{0.65, 0.25, 0.15, 0.3}, Quality: q(0.5)}
	var samples []visual.Sample
	for i := 0; i < 13; i++ {
		at := float64(i) + 0.5
		f := vision.Frame{W: 1280, H: 720}
		switch {
		case at < 6:
			f.Faces = []vision.Face{faceA}
		case at < 10:
			f.Faces = []vision.Face{faceA, faceB}
		default:
			f.Faces = []vision.Face{faceB}
		}
		samples = append(samples, visual.Sample{At: at, Frame: f})
	}
	idx := visual.Build(samples, 1, 13, nil, "proxy")
	ins := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind, Provider: "apple-vision",
		Digest: "d", Data: visual.ToMap(idx), CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CInsights, ins); err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, a, as.ID, idx); err != nil {
		t.Fatal(err)
	}
	return as.ID, idx
}

// voices names the clip's voices: speaker_00 speaks 0–6 s (and 6–10 s), speaker_01 10–13 s.
func voices(t *testing.T, a *app.App, assetID string, names map[string]models.Person) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	segs := []models.TranscriptSegment{
		{From: 0.2, To: 5.9, Speaker: "speaker_00", Text: "I started in a garage."},
		{From: 6.1, To: 9.8, Speaker: "speaker_00", Text: "And then it grew."},
		{From: 10.1, To: 12.9, Speaker: "speaker_01", Text: "That is how we met."},
	}
	for i, s := range segs {
		s.ID, s.WorkspaceID, s.AssetID, s.Index = ids.New(ids.Segment), a.WorkspaceID(), assetID, i
		if err := a.DB.Insert(ctx, store.CSegments, s); err != nil {
			t.Fatal(err)
		}
	}
	for label, p := range names {
		v := models.AssetSpeaker{ID: ids.New(ids.Speaker), WorkspaceID: a.WorkspaceID(), AssetID: assetID, Run: "run_1", Label: label, Name: p.Name, PersonID: p.ID,
			Source: models.SpeakerByPerson, Samples: []models.TimeSpan{}, CreatedAt: now, UpdatedAt: now}
		if err := a.DB.Insert(ctx, store.CSpeakers, v); err != nil {
			t.Fatal(err)
		}
	}
}

func person(t *testing.T, a *app.App, name string) models.Person {
	t.Helper()
	now := time.Now().UTC()
	p := models.Person{ID: ids.New(ids.Person), WorkspaceID: a.WorkspaceID(), Name: name, NameKey: strings.ToLower(name), CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(context.Background(), store.CPeople, p); err != nil {
		t.Fatal(err)
	}
	return p
}

func openQuestions(t *testing.T, a *app.App, assetID string) []models.Todo {
	t.Helper()
	var ts []models.Todo
	if err := a.DB.Find(context.Background(), store.CTodos, a.WorkspaceID(), bson.M{"assetId": assetID, "kind": bson.M{"$in": bson.A{models.TodoFacePerson, models.TodoFaceGroup}},
		"state": models.TodoOpen}, &ts); err != nil {
		t.Fatal(err)
	}
	return ts
}

func question(qs []models.Todo, q string) *models.Todo {
	for i := range qs {
		if qs[i].Question == q {
			return &qs[i]
		}
	}
	return nil
}

// A face alone on screen while one named voice speaks is asked "Who is
// this?" with that name as a quick pick — the voice alone never asks "Is this
// Ada?" (D189); a face alone on screen while a voice nobody named speaks is
// asked "Who is this?" too; faces that share the screen give no vote (D155,
// D157).
func TestCandidatesFromVoices(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	asset, _ := clip(t, a)
	if n, err := Candidates(ctx, a, asset); err != nil || n != 0 {
		t.Fatalf("nobody heard yet, no questions: %d %v", n, err)
	}
	ada := person(t, a, "Ada")
	voices(t, a, asset, map[string]models.Person{"speaker_00": ada}) // speaker_01 stays unnamed
	n, err := Candidates(ctx, a, asset)
	if err != nil || n != 2 {
		t.Fatalf("one question per person who speaks (Ada's face, and the face of the voice nobody named): %d %v", n, err)
	}
	qs := openQuestions(t, a, asset)
	var q, other *models.Todo
	for i := range qs {
		switch qs[i].Context.Label {
		case "speaker_00":
			q = &qs[i]
		case "speaker_01":
			other = &qs[i]
		}
	}
	if len(qs) != 2 || q == nil || q.Question != "Who is this?" || q.Kind != models.TodoFaceGroup || q.Context.PersonID != "" || len(q.Context.Box) != 4 || q.Context.FrameAt == nil ||
		q.Context.Appearances != 1 || len(q.Context.Faces) != 1 || q.Context.GroupID == "" {
		t.Fatalf("the voice alone asks who it is, never “Is this Ada?”: %+v", qs)
	}
	if len(q.Suggestions) == 0 || q.Suggestions[0].PersonID != ada.ID || q.Suggestions[0].Label != "Ada (talking then)" || fmt.Sprint(q.Suggestions[0].Basis) != "[voice_cue]" {
		t.Fatalf("Ada is a quick pick, resting on her voice: %+v", q.Suggestions)
	}
	if !strings.Contains(q.Detail, "Seen once in “Talk”") || !strings.Contains(q.Detail, "Ada is talking while this face is on screen") {
		t.Fatalf("plain detail: %q", q.Detail)
	}
	if other == nil || other.Question != "Who is this?" || len(other.Suggestions) != 1 || other.Suggestions[0].PersonID != ada.ID {
		t.Fatalf("the other face is asked about, with the people talking in the clip as quick answers: %+v", other)
	}
	tracks, err := Load(ctx, a, asset)
	if err != nil {
		t.Fatal(err)
	}
	asked := tracks[1]
	if asked.Candidate != nil || tracks[2].Candidate != nil {
		t.Fatalf("no “maybe” name from the voice alone: %+v / %+v", asked.Candidate, tracks[2].Candidate)
	}
	// Asking again changes nothing (idempotent).
	if n, _ := Candidates(ctx, a, asset); n != 2 || len(openQuestions(t, a, asset)) != 2 {
		t.Fatal("asked twice")
	}
	// "No": the face is not Ada, the question closes and is not asked again —
	// the voice heard with it is someone else's, so it isn't asked "Who is this?" either.
	if _, err := Reject(ctx, a, asked.ID, ada.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := Candidates(ctx, a, asset); n != 1 {
		t.Fatal("asked again after “not Ada”")
	}
	for _, q := range openQuestions(t, a, asset) {
		if q.Context.Label == "speaker_00" {
			t.Fatalf("asked again after “not Ada”: %+v", q)
		}
	}
	// A person says who it is: the name sticks, and survives indexing again.
	bo := person(t, a, "Bo")
	if _, err := Confirm(ctx, a, asked.ID, bo); err != nil {
		t.Fatal(err)
	}
	_, idx := clip2(t, a, asset)
	if err := Save(ctx, a, asset, idx); err != nil {
		t.Fatal(err)
	}
	tracks, _ = Load(ctx, a, asset)
	if f := tracks[1]; f.Name != "Bo" || f.PersonID != bo.ID || !contains(f.Rejected, ada.ID) {
		t.Fatalf("indexed again: the confirmed name and the rejection stay with the face: %+v", f)
	}
	confirmed, _, questions, err := OfPerson(ctx, a, bo.ID)
	if err != nil || len(confirmed) != 1 || len(questions) != 0 {
		t.Fatalf("Bo's faces: %d confirmed, %d asked, %v", len(confirmed), len(questions), err)
	}
	// Renames and merges follow the face.
	Rename(ctx, a, bo.ID, "Bo Diddley")
	tracks, _ = Load(ctx, a, asset)
	if tracks[1].Name != "Bo Diddley" {
		t.Fatalf("rename: %+v", tracks[1])
	}
	if best := BestOfPeople(ctx, a, []string{bo.ID}); best[bo.ID].ID != tracks[1].ID {
		t.Fatalf("best face: %+v", best)
	}
	Merge(ctx, a, bo.ID, ada.ID, "Ada")
	tracks, _ = Load(ctx, a, asset)
	if tracks[1].PersonID != ada.ID || tracks[1].Name != "Ada" {
		t.Fatalf("merge: %+v", tracks[1])
	}
	// The readout names confirmed faces, and open questions as "maybe".
	names := Namer(tracks)
	if n, sure := names(1); n != "Ada" || !sure {
		t.Fatalf("namer: %q %v", n, sure)
	}
}

// clip2 is the same index made again (a re-index produces new track records).
func clip2(t *testing.T, a *app.App, asset string) (string, *visual.Index) {
	t.Helper()
	st := visual.Load(context.Background(), a.DB, a.WorkspaceID(), asset)
	if st == nil {
		t.Fatal("no index")
	}
	return asset, st.Index
}

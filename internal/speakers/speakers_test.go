package speakers

import (
	"context"
	"errors"
	"math"
	"path/filepath"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/voice"
)

func testApp(t *testing.T) *app.App {
	t.Helper()
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace = config.Ref{ID: ids.New(ids.Workspace), Name: "T"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "test-machine"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "tester"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	cfg.Media = config.Media{Provider: "filesystem", Filesystem: config.Filesystem{Root: filepath.Join(h, "media")}}
	a, err := app.OpenWith(context.Background(), cfg, "cli")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	return a
}

// voiceVec is a synthetic voice: one dominant direction per person.
func voiceVec(person int) []float32 {
	v := make([]float32, 32)
	for i := range v {
		v[i] = float32(0.05 * math.Sin(float64(person*97+i*13)))
	}
	v[person%32] += 1
	voice.Normalize(v)
	return v
}

func jitter(v []float32, k int) []float32 {
	out := append([]float32(nil), v...)
	out[(k*7)%len(out)] += 0.12
	voice.Normalize(out)
	return out
}

// cluster builds a fingerprinted voice of a person spoken in [from, to).
func cluster(label string, person int, from, to float64) voice.Cluster {
	c := voice.Cluster{Label: label, Seconds: to - from, Embedded: to - from - 0.3, Turns: 1, First: from, Samples: []voice.Span{{From: from + 0.2, To: math.Min(to, from+4)}}}
	base := voiceVec(person)
	for k := 0; k < 4; k++ {
		c.Exemplars = append(c.Exemplars, voice.Exemplar{Span: voice.Span{From: from + float64(k), To: from + float64(k) + 2.5}, Vec: jitter(base, k)})
	}
	vs := [][]float32{}
	for _, e := range c.Exemplars {
		vs = append(vs, e.Vec)
	}
	c.Centroid = voice.Mean(vs, nil)
	return c
}

type seg struct {
	from, to float64
	label    string
}

// seedAsset inserts an available asset with labeled transcript segments.
func seedAsset(t *testing.T, a *app.App, name, project string, segs []seg) *models.Asset {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{project}, Kind: "video", Name: name, SHA256: ids.New("sha"), State: models.AssetAvailable,
		Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 60}, Variants: map[string]models.Variant{},
		Transcript: &models.TranscriptStatus{State: "available", Precision: "word", Segments: len(segs)}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	for i, s := range segs {
		ts := models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Index: i, From: s.from, To: s.to, Text: "words", Speaker: s.label,
			Words: []models.Word{{Text: "words", From: s.from, To: s.to}}, CreatedAt: now}
		if err := a.DB.Insert(ctx, store.CSegments, ts); err != nil {
			t.Fatal(err)
		}
	}
	return &as
}

func detection(clusters ...voice.Cluster) *Detection {
	res := &voice.Result{Raw: len(clusters)}
	for _, c := range clusters {
		res.Clusters = append(res.Clusters, c)
		res.Turns = append(res.Turns, voice.Turn{From: c.First, To: c.First + c.Seconds, Label: c.Label})
	}
	return &Detection{Run: ids.New(ids.Run), Result: res, Decision: map[string]voice.Decision{}, Raw: len(clusters), Prints: true}
}

func openTodos(t *testing.T, a *app.App, assetID string) []models.Todo {
	t.Helper()
	var ts []models.Todo
	if err := a.DB.Find(context.Background(), store.CTodos, a.WorkspaceID(), bson.M{"assetId": assetID, "state": models.TodoOpen}, &ts); err != nil {
		t.Fatal(err)
	}
	return ts
}

func segNames(t *testing.T, a *app.App, assetID string) map[string]string {
	t.Helper()
	var ss []models.TranscriptSegment
	if err := a.DB.Find(context.Background(), store.CSegments, a.WorkspaceID(), bson.M{"assetId": assetID}, &ss); err != nil {
		t.Fatal(err)
	}
	out := map[string]string{}
	for _, s := range ss {
		out[s.Speaker] = s.SpeakerName
	}
	return out
}

func TestNamingFlowsEverywhereAndIsRemembered(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	_ = a.DB.Insert(ctx, store.CProjects, models.Project{ID: project, WorkspaceID: a.WorkspaceID(), Name: "Interviews", Branches: map[string]models.BranchHead{}})

	// Video 1: Ada (person 1) and Ben (person 2).
	v1 := seedAsset(t, a, "first.mov", project, []seg{{0, 10, "speaker_00"}, {10.5, 20, "speaker_01"}})
	if err := Store(ctx, a, v1, detection(cluster("speaker_00", 1, 0, 10), cluster("speaker_01", 2, 10.5, 20)), "test"); err != nil {
		t.Fatal(err)
	}
	if n := len(openTodos(t, a, v1.ID)); n != 2 {
		t.Fatalf("two unnamed voices should ask two questions, got %d", n)
	}
	// Re-checking creates no duplicates (idempotent group keys).
	if err := Reconcile(ctx, a, v1.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(openTodos(t, a, v1.ID)); n != 2 {
		t.Fatalf("reconcile duplicated to-dos: %d", n)
	}
	// Video 2 (processed before anyone named anybody): the same two people.
	v2 := seedAsset(t, a, "second.mov", project, []seg{{0, 8, "speaker_00"}, {9, 30, "speaker_01"}})
	if err := Store(ctx, a, v2, detection(cluster("speaker_00", 2, 0, 8), cluster("speaker_01", 1, 9, 30)), "test"); err != nil {
		t.Fatal(err)
	}

	res, err := Name(ctx, a, NameInput{AssetID: v1.ID, Label: "Speaker 1", Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	// Ada appears in 2 recordings: named in video 1, recognized in video 2.
	if !res.CreatedNew || res.Person.Name != "Ada" || res.Person.Recordings != 2 || res.Person.Seconds != 31 || len(res.Person.Exemplars) == 0 {
		t.Fatalf("person not created/enrolled: %s, %d recordings, %.0f s, %d exemplars", res.Person.Name, res.Person.Recordings, res.Person.Seconds, len(res.Person.Exemplars))
	}
	if got := segNames(t, a, v1.ID)["speaker_00"]; got != "Ada" {
		t.Fatalf("transcript segments should carry the name, got %q", got)
	}
	// Naming once applies everywhere: Ada's voice in video 2 is recognized.
	if len(res.Recognized) != 1 || res.Recognized[0] != v2.ID {
		t.Fatalf("Ada should be recognized in the second video: %v", res.Recognized)
	}
	if got := segNames(t, a, v2.ID)["speaker_01"]; got != "Ada" {
		t.Fatalf("recognized name missing from video 2 transcript: %q", got)
	}
	vs, _ := Voices(ctx, a, v2.ID, false)
	for _, v := range vs {
		if v.Label == "speaker_01" && (v.Source != models.SpeakerByAuto || v.Score < voice.DefaultThresholds.Auto) {
			t.Fatalf("expected an automatic label with a score: %+v", v)
		}
	}
	// The question about Ada's voice in video 1 is closed; Ben's is still open.
	if ts := openTodos(t, a, v1.ID); len(ts) != 1 || ts[0].Context.Label != "speaker_01" {
		t.Fatalf("open to-dos after naming Ada: %+v", ts)
	}
	// Naming Ben in video 2 labels him in video 1 too, closing every question.
	if _, err := Name(ctx, a, NameInput{AssetID: v2.ID, Label: "speaker_00", Name: "Ben"}); err != nil {
		t.Fatal(err)
	}
	if n := len(openTodos(t, a, v1.ID)) + len(openTodos(t, a, v2.ID)); n != 0 {
		t.Fatalf("no questions should remain, got %d", n)
	}
	if got := segNames(t, a, v1.ID)["speaker_01"]; got != "Ben" {
		t.Fatalf("Ben not recognized in video 1: %q", got)
	}

	// Renaming the person renames them everywhere.
	if _, err := RenamePerson(ctx, a, res.Person.ID, "Ada Lovelace"); err != nil {
		t.Fatal(err)
	}
	if segNames(t, a, v1.ID)["speaker_00"] != "Ada Lovelace" || segNames(t, a, v2.ID)["speaker_01"] != "Ada Lovelace" {
		t.Fatalf("rename did not propagate: %v %v", segNames(t, a, v1.ID), segNames(t, a, v2.ID))
	}
	ps, _ := People(ctx, a, true)
	if len(ps) != 2 {
		t.Fatalf("want 2 people, got %d", len(ps))
	}
}

func TestSameNameTwiceMergesVoices(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	// The diarizer split one person (3) into two voices; person 4 is someone else.
	v := seedAsset(t, a, "split.mov", project, []seg{{0, 5, "speaker_00"}, {6, 18, "speaker_01"}, {19, 23, "speaker_02"}})
	if err := Store(ctx, a, v, detection(cluster("speaker_00", 3, 0, 5), cluster("speaker_01", 4, 6, 18), cluster("speaker_02", 3, 19, 23)), "test"); err != nil {
		t.Fatal(err)
	}
	// Labeling one sample of Theo also recognizes his other voice in the same
	// video: the two collapse into one speaker. The voice a person named is the
	// one kept (its fingerprint is what Theo's profile learns from).
	if _, err := Name(ctx, a, NameInput{AssetID: v.ID, Label: "speaker_02", Name: "Theo"}); err != nil {
		t.Fatal(err)
	}
	vs, _ := Voices(ctx, a, v.ID, false)
	if len(vs) != 2 || vs[1].Label != "speaker_02" || vs[1].Name != "Theo" || vs[1].Source != models.SpeakerByPerson {
		t.Fatalf("want Theo's two voices merged into speaker_02, got %d voices (%+v)", len(vs), vs)
	}
	names := segNames(t, a, v.ID)
	if _, still := names["speaker_00"]; still || names["speaker_02"] != "Theo" {
		t.Fatalf("segments of the merged voice must be relabeled and named: %v", names)
	}
	var st models.Asset
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), v.ID, &st)
	if st.Speakers == nil || st.Speakers.Count != 2 || st.Speakers.Named != 1 {
		t.Fatalf("status not refreshed: %+v", st.Speakers)
	}
	if ts := openTodos(t, a, v.ID); len(ts) != 1 {
		t.Fatalf("only the other voice's question should remain: %d", len(ts))
	}
	// Giving a different-sounding voice the same name is the person's call, so
	// it is asked first; confirmed, it merges too.
	var cn *ConfirmNeeded
	if _, err := Name(ctx, a, NameInput{AssetID: v.ID, Label: "speaker_01", Name: "theo"}); !errors.As(err, &cn) || cn.Reason != "same_name_in_clip" || cn.Other != "Theo" {
		t.Fatalf("a second voice with the same name must be confirmed first, got %v", err)
	}
	res, err := Name(ctx, a, NameInput{AssetID: v.ID, Label: "speaker_01", Name: "theo", Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	// Theo's voice (speaker_02, which took in speaker_00) starts first, so it is kept.
	if res.MergedWith != "speaker_02" || res.Voice.Label != "speaker_02" || res.Dropped != "speaker_01" || res.CreatedNew {
		t.Fatalf("explicit same name should merge into the existing Theo (merged with %q, kept %q, dropped %q, new person %v)", res.MergedWith, res.Voice.Label, res.Dropped, res.CreatedNew)
	}
	if vs, _ := Voices(ctx, a, v.ID, false); len(vs) != 1 || vs[0].Turns != 3 {
		t.Fatalf("want one voice with all three turns, got %d", len(vs))
	}
	if names := segNames(t, a, v.ID); len(names) != 1 || names["speaker_02"] != "Theo" {
		t.Fatalf("all segments should be Theo's: %v", names)
	}
	if ts := openTodos(t, a, v.ID); len(ts) != 0 {
		t.Fatalf("no questions should remain: %d", len(ts))
	}
}

func TestRedetectionClosesStaleQuestionsAndRejectsStick(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	v := seedAsset(t, a, "a.mov", project, []seg{{0, 10, "speaker_00"}, {11, 20, "speaker_01"}})
	if err := Store(ctx, a, v, detection(cluster("speaker_00", 5, 0, 10), cluster("speaker_01", 6, 11, 20)), "test"); err != nil {
		t.Fatal(err)
	}
	before := openTodos(t, a, v.ID)
	// A new detection run (e.g. after a speaker-count hint) replaces the voices.
	if err := Store(ctx, a, v, detection(cluster("speaker_00", 5, 0, 20)), "test"); err != nil {
		t.Fatal(err)
	}
	after := openTodos(t, a, v.ID)
	if len(after) != 1 || after[0].GroupKey == before[0].GroupKey {
		t.Fatalf("old questions should close and one new one open: before %d, after %+v", len(before), after)
	}
	var stale models.Todo
	if err := a.DB.C(store.CTodos).FindOne(ctx, bson.M{"groupKey": before[0].GroupKey}).Decode(&stale); err != nil || stale.Resolution != models.ResolvedObsolete {
		t.Fatalf("stale question should be closed as obsolete: %+v %v", stale, err)
	}
	// A dismissed question is never asked again.
	if _, err := todos.Finish(ctx, a, after[0].ID, models.TodoDismissed, ""); err != nil {
		t.Fatal(err)
	}
	if err := Reconcile(ctx, a, v.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(openTodos(t, a, v.ID)); n != 0 {
		t.Fatalf("dismissed question came back: %d open", n)
	}
	// A person with a similar voice is suggested; rejecting removes the suggestion for good.
	other := seedAsset(t, a, "b.mov", project, []seg{{0, 12, "speaker_00"}})
	if err := Store(ctx, a, other, detection(cluster("speaker_00", 5, 0, 12)), "test"); err != nil {
		t.Fatal(err)
	}
	res, err := Name(ctx, a, NameInput{AssetID: other.ID, Label: "speaker_00", Name: "Cleo"})
	if err != nil {
		t.Fatal(err)
	}
	vs, _ := Voices(ctx, a, v.ID, false)
	if vs[0].PersonID != res.Person.ID {
		t.Fatalf("Cleo should be recognized in a.mov: %+v", vs[0])
	}
	if _, err := Reject(ctx, a, v.ID, "speaker_00", res.Person.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshLibrary(ctx, a); err != nil {
		t.Fatal(err)
	}
	vs, _ = Voices(ctx, a, v.ID, false)
	if vs[0].PersonID != "" || len(vs[0].Suggestions) != 0 || len(vs[0].Rejected) != 1 {
		t.Fatalf("rejection did not stick: %+v", vs[0])
	}
}

func TestMergeAndSplitPeople(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	v1 := seedAsset(t, a, "one.mov", project, []seg{{0, 10, "speaker_00"}})
	v2 := seedAsset(t, a, "two.mov", project, []seg{{0, 10, "speaker_00"}, {11, 20, "speaker_01"}})
	_ = Store(ctx, a, v1, detection(cluster("speaker_00", 7, 0, 10)), "test")
	_ = Store(ctx, a, v2, detection(cluster("speaker_00", 8, 0, 10), cluster("speaker_01", 9, 11, 20)), "test")
	p1, _ := Name(ctx, a, NameInput{AssetID: v1.ID, Label: "speaker_00", Name: "Dee"})
	// "D." is close to "Dee": naming someone that way is refused unless they are someone new.
	var closeName *CloseName
	if _, err := Name(ctx, a, NameInput{AssetID: v2.ID, Label: "speaker_00", Name: "D."}); !errors.As(err, &closeName) || closeName.Suggest[0].Name != "Dee" {
		t.Fatalf("a name close to Dee should ask whether it is Dee, got %v", err)
	}
	p2, _ := Name(ctx, a, NameInput{AssetID: v2.ID, Label: "speaker_00", Name: "D.", NewPerson: true})
	merged, err := MergePeople(ctx, a, p2.Person.ID, p1.Person.ID)
	if err != nil {
		t.Fatal(err)
	}
	if merged.Name != "Dee" || merged.Recordings != 2 {
		t.Fatalf("merge: %+v", merged)
	}
	if segNames(t, a, v2.ID)["speaker_00"] != "Dee" {
		t.Fatalf("merged name not applied: %v", segNames(t, a, v2.ID))
	}
	// Split: that voice in two.mov is really Eli.
	eli, err := SplitPerson(ctx, a, SplitInput{PersonID: merged.ID, AssetID: v2.ID, Label: "speaker_00", Name: "Eli"})
	if err != nil {
		t.Fatal(err)
	}
	if eli.Name != "Eli" || segNames(t, a, v2.ID)["speaker_00"] != "Eli" {
		t.Fatalf("split: %+v %v", eli, segNames(t, a, v2.ID))
	}
	dee, _ := loadPerson(ctx, a, merged.ID)
	if dee.Recordings != 1 {
		t.Fatalf("Dee should be left with one recording, got %d", dee.Recordings)
	}
}

func TestResolveLabelAcceptsPlainNames(t *testing.T) {
	vs := []models.AssetSpeaker{{Label: "speaker_00", Name: "Ada"}, {Label: "speaker_01"}}
	for in, want := range map[string]string{"speaker_01": "speaker_01", "Speaker 2": "speaker_01", "2": "speaker_01", "ada": "speaker_00", "voice 1": "speaker_00"} {
		v, err := ResolveLabel(vs, in)
		if err != nil || v.Label != want {
			t.Fatalf("%q → %v, %v", in, v, err)
		}
	}
	if _, err := ResolveLabel(vs, "Speaker 9"); err == nil {
		t.Fatal("unknown voice should fail")
	}
}

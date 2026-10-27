package speakers

import (
	"context"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voice"
)

// Synthetic sound that says who speaks: in a person's stretches every sample is
// 0.01 × their number, and the fake extractor turns that back into the
// person's voice vector. So re-learning a voice from its own words gives
// exactly the voices of the people who said them.
type speech struct {
	from, to float64
	person   int
}

type fakeExtractor struct{}

func (fakeExtractor) Embed(s []float32) ([]float32, error) {
	if len(s) < voice.MinSamples {
		return nil, voice.ErrTooShort
	}
	sum := 0.0
	for _, x := range s {
		sum += float64(x)
	}
	p := int(math.Round(sum / float64(len(s)) * 100))
	if p <= 0 {
		return nil, voice.ErrTooShort
	}
	return voiceVec(p), nil
}

func withSpeech(t *testing.T, sound map[string][]speech) {
	t.Helper()
	restore := SetAudioForTests(func(ctx context.Context, a *app.App, as *models.Asset) ([]float32, error) {
		n := int(as.Media.Duration * voice.SampleRate)
		pcm := make([]float32, n)
		for _, s := range sound[as.ID] {
			for i := int(s.from * voice.SampleRate); i < int(s.to*voice.SampleRate) && i < n; i++ {
				pcm[i] = float32(0.01 * float64(s.person))
			}
		}
		return pcm, nil
	}, func(*app.App) voice.Embedder { return fakeExtractor{} })
	t.Cleanup(restore)
}

// seedWords inserts a clip whose segments have one word every half second.
func seedWords(t *testing.T, a *app.App, name, project string, dur float64, segs []seg) *models.Asset {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{project}, Kind: "video", Name: name, SHA256: ids.New("sha"), State: models.AssetAvailable,
		Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: dur}, Variants: map[string]models.Variant{},
		Transcript: &models.TranscriptStatus{State: "available", Precision: "word", Segments: len(segs)}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	for i, s := range segs {
		var ws []models.Word
		text := ""
		for f := s.from; f+0.4 <= s.to; f += 0.5 {
			w := fmt.Sprintf("w%d", int(f*10))
			ws = append(ws, models.Word{Text: w, From: f, To: f + 0.4})
			text += w + " "
		}
		ts := models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Index: i, From: s.from, To: s.to, Text: text, Speaker: s.label, Words: ws, CreatedAt: now}
		if err := a.DB.Insert(ctx, store.CSegments, ts); err != nil {
			t.Fatal(err)
		}
	}
	return &as
}

func voiceOf(t *testing.T, a *app.App, assetID, label string) models.AssetSpeaker {
	t.Helper()
	vs, err := Voices(context.Background(), a, assetID, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range vs {
		if v.Label == label {
			return v
		}
	}
	t.Fatalf("no voice %s in %s: %+v", label, assetID, vs)
	return models.AssetSpeaker{}
}

func profileOf(t *testing.T, a *app.App, personID string) voice.Profile {
	t.Helper()
	ps, err := LoadProfiles(context.Background(), a)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range ps {
		if p.ID == personID {
			return p
		}
	}
	return voice.Profile{ID: personID}
}

func namesByTime(t *testing.T, a *app.App, assetID string) map[float64]string {
	t.Helper()
	var ss []models.TranscriptSegment
	if err := a.DB.Find(context.Background(), store.CSegments, a.WorkspaceID(), bson.M{"assetId": assetID}, &ss); err != nil {
		t.Fatal(err)
	}
	out := map[float64]string{}
	for _, s := range ss {
		for _, w := range s.Words {
			out[w.From] = s.SpeakerName
		}
	}
	return out
}

func todosOfKind(t *testing.T, a *app.App, kind string) []models.Todo {
	t.Helper()
	var ts []models.Todo
	if err := a.DB.Find(context.Background(), store.CTodos, a.WorkspaceID(), bson.M{"kind": kind}, &ts); err != nil {
		t.Fatal(err)
	}
	return ts
}

// A name mix-up: in a clip of two people a man's voice is named
// "Maya", then hers too (they are merged), and a clip of only him is
// then labeled Maya. Fixing who said what in the first clip must take his
// voice out of Maya's profile completely, close the "same person?"
// question it caused, and move the automatic label.
func TestFixingWhoSaidWhatCleansAWrongProfile(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	_ = a.DB.Insert(ctx, store.CProjects, models.Project{ID: project, WorkspaceID: a.WorkspaceID(), Name: "Interviews", Branches: map[string]models.BranchHead{}})
	// Person 1 is Theo, person 2 is Maya.
	x := seedWords(t, a, "IMG_2391.mov", project, 14, []seg{{0, 5, "speaker_00"}, {6, 12, "speaker_01"}})
	y := seedWords(t, a, "IMG_2392.mov", project, 9, []seg{{0, 8, "speaker_00"}})
	z := seedWords(t, a, "IMG_2393.mov", project, 9, []seg{{0, 8, "speaker_00"}})
	withSpeech(t, map[string][]speech{x.ID: {{0, 5, 1}, {6, 12, 2}}, y.ID: {{0, 8, 1}}, z.ID: {{0, 8, 1}}})
	for _, c := range []struct {
		as *models.Asset
		d  *Detection
	}{{x, detection(cluster("speaker_00", 1, 0, 5), cluster("speaker_01", 2, 6, 12))}, {y, detection(cluster("speaker_00", 1, 0, 8))}, {z, detection(cluster("speaker_00", 1, 0, 8))}} {
		if err := Store(ctx, a, c.as, c.d, "test"); err != nil {
			t.Fatal(err)
		}
	}
	// The wrong name, then the same name for the second voice (confirmed: merged).
	res, err := Name(ctx, a, NameInput{AssetID: x.ID, Label: "speaker_00", Name: "Maya"})
	if err != nil {
		t.Fatal(err)
	}
	maya := res.Person.ID
	if voiceOf(t, a, y.ID, "speaker_00").PersonID != maya {
		t.Fatal("the clips of Theo alone should be recognized as Maya (that is the bug being fixed)")
	}
	if _, err := Name(ctx, a, NameInput{AssetID: x.ID, Label: "speaker_01", Name: "Maya"}); err == nil {
		t.Fatal("a second voice named Maya in the same clip must be confirmed")
	}
	if _, err := Name(ctx, a, NameInput{AssetID: x.ID, Label: "speaker_01", Name: "Maya", Confirm: true}); err != nil {
		t.Fatal(err)
	}
	// He says the second clip is him (Theo): now two profiles share his voice.
	theo, err := SplitPerson(ctx, a, SplitInput{PersonID: maya, AssetID: y.ID, Name: "Theo"})
	if err != nil {
		t.Fatal(err)
	}
	if open := openOf(todosOfKind(t, a, models.TodoDuplicatePerson)); len(open) != 1 {
		t.Fatalf("Maya's profile holds Theo's voice, so a 'same person?' question opens: %+v", open)
	}

	// The fix: his lines in the first clip are Theo's.
	ar, err := Assign(ctx, a, AssignInput{AssetID: x.ID, From: 0, To: 5.5, PersonID: theo.ID})
	if err != nil {
		t.Fatal(err)
	}
	if ar.Moved != 10 || !ar.Relearned || ar.Person.ID != theo.ID {
		t.Fatalf("assign: moved %d, relearned %v", ar.Moved, ar.Relearned)
	}
	names := namesByTime(t, a, x.ID)
	if names[0] != "Theo" || names[4.5] != "Theo" || names[6] != "Maya" || names[11.5] != "Maya" {
		t.Fatalf("lines should be Theo's then Maya's: %v", names)
	}
	// Maya's voice is now only hers; the two people no longer sound alike.
	jp, op := profileOf(t, a, maya), profileOf(t, a, theo.ID)
	if len(jp.Exemplars) == 0 || voice.Cosine(jp.Centroid, voiceVec(2)) < 0.95 || voice.Cosine(jp.Centroid, voiceVec(1)) > 0.2 {
		t.Fatalf("Maya's profile should be person 2 only: cos(2)=%.2f cos(1)=%.2f", voice.Cosine(jp.Centroid, voiceVec(2)), voice.Cosine(jp.Centroid, voiceVec(1)))
	}
	if s := voice.Score(voice.Cluster{Centroid: jp.Centroid}, op).Score; s >= voice.DefaultThresholds.Suggest {
		t.Fatalf("Maya and Theo should not sound alike any more: %.2f", s)
	}
	dups := todosOfKind(t, a, models.TodoDuplicatePerson)
	if len(openOf(dups)) != 0 || dups[0].Resolution != models.ResolvedObsolete {
		t.Fatalf("the 'same person?' question should close itself as obsolete: %+v", dups)
	}
	// The third clip (him alone, never confirmed) follows the corrected voices.
	if v := voiceOf(t, a, z.ID, "speaker_00"); v.PersonID != theo.ID || v.Source != models.SpeakerByAuto {
		t.Fatalf("the clip of him alone should be recognized as Theo now: %+v", v)
	}
	// Rebuilding from confirmed clips agrees, and finds nothing left to fix.
	lib, rep, err := RebuildPerson(ctx, a, maya)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Voices == 0 || len(rep.Conflicts) != 0 || len(lib.Dropped) != 0 {
		t.Fatalf("rebuild: %+v %+v", rep, lib)
	}
}

func openOf(ts []models.Todo) []models.Todo {
	var out []models.Todo
	for _, t := range ts {
		if t.State == models.TodoOpen {
			out = append(out, t)
		}
	}
	return out
}

// "Not sure" gives words to a voice nobody named (a question); lines are split
// where the speaker changes.
func TestAssignSplitsLinesAndUnsureAsks(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	x := seedWords(t, a, "talk.mov", project, 12, []seg{{0, 10, "speaker_00"}})
	withSpeech(t, map[string][]speech{x.ID: {{0, 6, 3}, {6, 10, 4}}})
	if err := Store(ctx, a, x, detection(cluster("speaker_00", 3, 0, 10)), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Name(ctx, a, NameInput{AssetID: x.ID, Label: "speaker_00", Name: "Kim"}); err != nil {
		t.Fatal(err)
	}
	if _, err := Assign(ctx, a, AssignInput{AssetID: x.ID, From: 20, To: 21, Unsure: true}); !errors.Is(err, ErrNoWords) {
		t.Fatalf("nothing is said there: %v", err)
	}
	ar, err := Assign(ctx, a, AssignInput{AssetID: x.ID, From: 6, To: 10, Unsure: true})
	if err != nil {
		t.Fatal(err)
	}
	if ar.Person != nil || ar.Voice.Name != "" || !ar.Voice.Manual || ar.Moved != 8 {
		t.Fatalf("unsure: %+v", ar)
	}
	segs, _ := loadSegments(ctx, a, x.ID)
	if len(segs) != 2 || segs[0].Speaker != "speaker_00" || segs[1].Speaker != ar.Voice.Label || segs[1].From != 6 {
		t.Fatalf("the line should be split at 6 s: %+v", segs)
	}
	// The new voice is a question; Kim's voice learned only from her part.
	if ts := openTodos(t, a, x.ID); len(ts) != 1 || ts[0].Context.Label != ar.Voice.Label {
		t.Fatalf("want one question about the new voice: %+v", ts)
	}
	kim := voiceOf(t, a, x.ID, "speaker_00")
	if voice.Cosine(kim.Centroid, voiceVec(3)) < 0.95 || kim.Words != 12 {
		t.Fatalf("Kim's voice should be person 3 from her 12 words: cos %.2f, %d words", voice.Cosine(kim.Centroid, voiceVec(3)), kim.Words)
	}
	if len(kim.Confirmed) != 0 || len(ar.Voice.Confirmed) != 1 {
		t.Fatalf("confirmed spans: Kim %+v, new %+v", kim.Confirmed, ar.Voice.Confirmed)
	}
}

// Undo after giving two voices the same name: both come back as they were.
func TestUnmergeRestoresBothVoices(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	x := seedWords(t, a, "pair.mov", project, 14, []seg{{0, 5, "speaker_00"}, {6, 12, "speaker_01"}})
	withSpeech(t, map[string][]speech{x.ID: {{0, 5, 5}, {6, 12, 6}}})
	if err := Store(ctx, a, x, detection(cluster("speaker_00", 5, 0, 5), cluster("speaker_01", 6, 6, 12)), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Name(ctx, a, NameInput{AssetID: x.ID, Label: "speaker_00", Name: "Ada"}); err != nil {
		t.Fatal(err)
	}
	res, err := Name(ctx, a, NameInput{AssetID: x.ID, Label: "speaker_01", Name: "Ada", Confirm: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped != "speaker_01" {
		t.Fatalf("dropped %q", res.Dropped)
	}
	if vs, _ := Voices(ctx, a, x.ID, false); len(vs) != 1 || vs[0].Words != 22 {
		t.Fatalf("merged: %+v", vs)
	}
	if _, err := Unmerge(ctx, a, x.ID, "speaker_01"); err != nil {
		t.Fatal(err)
	}
	vs, _ := Voices(ctx, a, x.ID, false)
	if len(vs) != 2 || vs[0].Name != "Ada" || vs[0].Words != 10 || vs[1].Name != "" || vs[1].MergedInto != "" || vs[1].Before != nil {
		t.Fatalf("after undo: %+v", vs)
	}
	names := namesByTime(t, a, x.ID)
	if names[1] != "Ada" || names[7] != "" {
		t.Fatalf("words should be back with their voices: %v", names)
	}
	if ts := openTodos(t, a, x.ID); len(ts) != 1 || ts[0].Context.Label != "speaker_01" {
		t.Fatalf("the second voice is asked about again: %+v", ts)
	}
	if _, err := Unmerge(ctx, a, x.ID, "speaker_01"); !errors.Is(err, ErrNotMerged) {
		t.Fatalf("nothing to undo twice: %v", err)
	}
}

// Finding the voices again keeps what a person said: the confirmed name moves
// to the new voice that covers the same speech (whatever its label), and a
// "not Ben" stays (so the question does not come back).
func TestRedetectionCarriesNamesAndRejections(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	x := seedWords(t, a, "x.mov", project, 14, []seg{{0, 5, "speaker_00"}, {6, 12, "speaker_01"}})
	other := seedWords(t, a, "other.mov", project, 9, []seg{{0, 8, "speaker_00"}})
	if err := Store(ctx, a, x, detection(cluster("speaker_00", 7, 0, 5), cluster("speaker_01", 8, 6, 12)), "test"); err != nil {
		t.Fatal(err)
	}
	if err := Store(ctx, a, other, detection(cluster("speaker_00", 8, 0, 8)), "test"); err != nil {
		t.Fatal(err)
	}
	ada, err := Name(ctx, a, NameInput{AssetID: x.ID, Label: "speaker_00", Name: "Ada"})
	if err != nil {
		t.Fatal(err)
	}
	ben, err := Name(ctx, a, NameInput{AssetID: other.ID, Label: "speaker_00", Name: "Ben"})
	if err != nil {
		t.Fatal(err)
	}
	if v := voiceOf(t, a, x.ID, "speaker_01"); v.PersonID != ben.Person.ID {
		t.Fatalf("x's second voice sounds like Ben: %+v", v)
	}
	if _, err := Reject(ctx, a, x.ID, "speaker_01", ben.Person.ID); err != nil {
		t.Fatal(err)
	}
	// Detection runs again; this time the labels come out the other way round.
	prev := CapturePrevious(ctx, a, x.ID)
	segs, _ := loadSegments(ctx, a, x.ID)
	for i := range segs {
		segs[i].SpeakerName = ""
		if segs[i].Speaker == "speaker_00" {
			segs[i].Speaker = "speaker_01"
		} else {
			segs[i].Speaker = "speaker_00"
		}
	}
	if err := replaceSegments(ctx, a, x.ID, segs); err != nil {
		t.Fatal(err)
	}
	d := detection(cluster("speaker_00", 8, 6, 12), cluster("speaker_01", 7, 0, 5))
	d.Previous = prev
	d.Decision["speaker_00"] = voice.Decision{Auto: &voice.Match{PersonID: ben.Person.ID, Name: "Ben", Score: 0.95}}
	if _, err := StoreWith(ctx, a, x, d, "test"); err != nil {
		t.Fatal(err)
	}
	if v := voiceOf(t, a, x.ID, "speaker_01"); v.PersonID != ada.Person.ID || v.Source != models.SpeakerByPerson {
		t.Fatalf("Ada's confirmed name should carry over to the voice that says her words: %+v", v)
	}
	v := voiceOf(t, a, x.ID, "speaker_00")
	if v.PersonID != "" || !contains(v.Rejected, ben.Person.ID) {
		t.Fatalf("'not Ben' should carry over and block the automatic label: %+v", v)
	}
	for _, tt := range openTodos(t, a, x.ID) {
		for _, s := range tt.Suggestions {
			if s.PersonID == ben.Person.ID {
				t.Fatalf("Ben must not be suggested again: %+v", tt)
			}
		}
	}
	if names := namesByTime(t, a, x.ID); names[1] != "Ada" {
		t.Fatalf("names on the transcript: %v", names)
	}
}

// "Same person?" answered "no" is final: keyed on the two people, it never
// comes back, not after their voices are found again either.
func TestDifferentPeopleAreNeverAskedAgain(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	x := seedWords(t, a, "x.mov", project, 9, []seg{{0, 8, "speaker_00"}})
	y := seedWords(t, a, "y.mov", project, 9, []seg{{0, 8, "speaker_00"}})
	// Twins: the same synthetic voice named twice.
	_ = Store(ctx, a, x, detection(cluster("speaker_00", 9, 0, 8)), "test")
	_ = Store(ctx, a, y, detection(cluster("speaker_00", 9, 0, 8)), "test")
	p1, err := Name(ctx, a, NameInput{AssetID: x.ID, Label: "speaker_00", Name: "Fay"})
	if err != nil {
		t.Fatal(err)
	}
	p2, err := Name(ctx, a, NameInput{AssetID: y.ID, Label: "speaker_00", Name: "Gus", NewPerson: true})
	if err != nil {
		t.Fatal(err)
	}
	dups := openOf(todosOfKind(t, a, models.TodoDuplicatePerson))
	if len(dups) != 1 || dups[0].Suggestions[0].Value != "different" {
		t.Fatalf("want one 'same person?' question with 'No, not the same' first: %+v", dups)
	}
	if err := MarkDistinct(ctx, a, p1.Person.ID, p2.Person.ID); err != nil {
		t.Fatal(err)
	}
	// Voices found again in both clips, profiles rebuilt.
	for _, as := range []*models.Asset{x, y} {
		d := detection(cluster("speaker_00", 9, 0, 8))
		d.Previous = CapturePrevious(ctx, a, as.ID)
		if _, err := StoreWith(ctx, a, as, d, "test"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := RebuildAll(ctx, a); err != nil {
		t.Fatal(err)
	}
	if open := openOf(todosOfKind(t, a, models.TodoDuplicatePerson)); len(open) != 0 {
		t.Fatalf("the answered pair came back: %+v", open)
	}
	if v := voiceOf(t, a, y.ID, "speaker_00"); v.PersonID != p2.Person.ID || v.Source != models.SpeakerByPerson {
		t.Fatalf("Gus's confirmed name should survive the new detection: %+v", v)
	}
}

// A profile that still holds what a wrong name once enrolled (as earlier
// versions left it) is cleaned by rebuilding from confirmed voices.
func TestRebuildDropsWhatAWrongNameLeftBehind(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	x := seedWords(t, a, "x.mov", project, 9, []seg{{0, 8, "speaker_00"}})
	_ = Store(ctx, a, x, detection(cluster("speaker_00", 11, 0, 8)), "test")
	res, err := Name(ctx, a, NameInput{AssetID: x.ID, Label: "speaker_00", Name: "Hal"})
	if err != nil {
		t.Fatal(err)
	}
	// Earlier versions forgot to remove exemplars a since-corrected voice added.
	stale := models.VoiceExemplar{Source: "ast_gone/speaker_03", Vec: voiceVec(12), Seconds: 3}
	if err := a.DB.Update(ctx, store.CPeople, a.WorkspaceID(), res.Person.ID, bson.M{"$push": bson.M{"exemplars": stale}}); err != nil {
		t.Fatal(err)
	}
	rep, err := RebuildProfile(ctx, a, res.Person.ID)
	if err != nil {
		t.Fatal(err)
	}
	p, _ := loadPerson(ctx, a, res.Person.ID)
	for _, e := range p.Exemplars {
		if e.Source == stale.Source {
			t.Fatal("a stale exemplar survived the rebuild")
		}
	}
	if rep.Voices != 1 || rep.Clips != 1 || len(p.Exemplars) != 4 {
		t.Fatalf("rebuild used %+v; %d exemplars", rep, len(p.Exemplars))
	}
}

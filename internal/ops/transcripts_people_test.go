package ops

import (
	"context"
	"fmt"
	"math"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/transcripts"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
	"github.com/metavert-io/metarush/internal/voice"
)

// Fixing who says a stretch (D145) in clips the people questions read too
// (D155–D159): a face on screen while the voice talks, the same voice in
// another clip.

// talkingHead stores a clip in which one person talks for dur seconds (a word
// every half second, one voice nobody named) with their face on screen all
// along, as ingest, the on-screen index and speaker detection leave it.
func talkingHead(t *testing.T, a *app.App, projectID, title string, dur float64, print []float32) models.Asset {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	as := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{projectID}, Kind: "video", Name: strings.ToLower(title) + ".mov", Title: title,
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: dur, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now,
		Transcript: &models.TranscriptStatus{State: "available", Provider: "parakeet", Model: "test", Precision: "word", Language: "en", Segments: 2, UpdatedAt: now}})
	insert(t, a, store.CAssets, as)
	half := dur / 2
	for i, line := range [][2]float64{{0, half}, {half, dur}} {
		var ws []models.Word
		for f := line[0]; f+0.4 <= line[1]+1e-9; f += 0.5 {
			ws = append(ws, models.Word{Text: fmt.Sprintf("w%d", int(math.Round(f*10))), From: f, To: f + 0.4, Score: 0.9})
		}
		insert(t, a, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Index: i, From: ws[0].From, To: ws[len(ws)-1].To,
			Text: transcripts.JoinWords(ws), Words: ws, Speaker: "speaker_00", Provider: "parakeet", Model: "test", CreatedAt: now})
	}
	var samples []visual.Sample
	for s := 0.5; s < dur; s++ {
		q := 0.7
		f := vision.Frame{W: 1280, H: 720, Faces: []vision.Face{{Conf: 1, Box: vision.Box{0.35, 0.15, 0.3, 0.5}, Quality: &q}}}
		samples = append(samples, visual.Sample{At: s, Frame: f, Prints: [][]float32{print}})
	}
	idx := visual.Build(samples, 1, dur, nil, "proxy")
	insert(t, a, store.CInsights, models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind,
		Provider: "apple-vision", Digest: "d", Data: visual.ToMap(idx), CreatedAt: now})
	if err := faces.Save(ctx, a, as.ID, idx); err != nil {
		t.Fatal(err)
	}
	det := &speakers.Detection{Run: ids.New(ids.Run), Result: &voice.Result{Raw: 1, Clusters: []voice.Cluster{testVoice("speaker_00", 1, 0, dur)}}, Decision: map[string]voice.Decision{}, Prints: true}
	if err := speakers.Store(ctx, a, &as, det, "test"); err != nil {
		t.Fatal(err)
	}
	return as
}

// personPrint fingerprints talkingHead's sound: every sample of a person's
// speech is 0.01 × their number, and the print is testVoice's for them. With
// broken set it fails the way a fingerprint model that stopped working would.
type personPrint struct{ broken *bool }

func (e personPrint) Embed(s []float32) ([]float32, error) {
	if e.broken != nil && *e.broken {
		return nil, fmt.Errorf("the fingerprint model stopped")
	}
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
	return testVoice("print", p, 0, 4).Exemplars[0].Vec, nil
}

// oneVoiceSound gives every clip person 1's sound and the fingerprints above.
// The first time the sound is read after *watch is set — when the voices are
// learned again after a change of who says what — watch runs (once), so a
// test sees everything as it is at that moment.
func oneVoiceSound(t *testing.T, watch *func(ctx context.Context), broken *bool) {
	t.Helper()
	restore := speakers.SetAudioForTests(func(ctx context.Context, a *app.App, as *models.Asset) ([]float32, error) {
		if w := *watch; w != nil {
			*watch = nil
			w(ctx)
		}
		pcm := make([]float32, int(as.Media.Duration*voice.SampleRate))
		for i := range pcm {
			pcm[i] = 0.01
		}
		return pcm, nil
	}, func(*app.App) voice.Embedder { return personPrint{broken: broken} })
	t.Cleanup(restore)
}

// moment is what a clip and version 1 of its cut looked like at one moment.
type moment struct {
	seen    bool
	err     error
	waiting []string          // fixes waiting for a new version of version 1: "speaker Speaker 1→Lena"
	said    map[string]string // who says each word on the clip's lines (by when it starts): a name, or ""
}

func momentOf(ctx context.Context, a *app.App, projectID, revisionID, assetID string) moment {
	m := moment{seen: true, waiting: []string{}, said: map[string]string{}}
	pend, err := CallTyped[TranscriptPendingOut](ctx, a, "transcript.pending", TranscriptPendingIn{ProjectID: projectID, Revision: revisionID}, Caller{})
	if err != nil {
		m.err = err
		return m
	}
	for _, f := range pend.Places {
		m.waiting = append(m.waiting, fmt.Sprintf("%s %s→%s", f.Kind, f.BeforeText, f.AfterText))
	}
	var segs []models.TranscriptSegment
	if m.err = a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": assetID}, &segs); m.err != nil {
		return m
	}
	for _, s := range segs {
		for _, w := range s.Words {
			m.said[fmt.Sprintf("%.1f", w.From)] = s.SpeakerName
		}
	}
	return m
}

// facesNamedOrAsked says how a clip's faces stand: any named or recognized by
// their looks, and the questions about them that suggest someone.
func facesNamedOrAsked(t *testing.T, a *app.App, assetID string) (named []string, suggested []string) {
	t.Helper()
	for _, f := range call[FacesOut](t, a, "footage.faces", FacesIn{AssetID: assetID}).Faces {
		if f.Name != "" {
			named = append(named, f.Name)
		}
		if f.Recognized != nil {
			named = append(named, "looks like "+f.Recognized.Name)
		}
	}
	for _, g := range call[TodoGroupsOut](t, a, "todos.groups", TodoGroupsIn{AssetID: assetID}).Groups {
		if g.Context.PersonName != "" {
			suggested = append(suggested, g.Context.PersonName)
			continue
		}
		// "Who is this?": the name the voice heard points at is a quick pick (D189).
		for _, s := range g.Suggestions {
			if slices.Contains(s.Basis, models.BasisVoiceCue) {
				suggested = append(suggested, s.Value)
			}
		}
	}
	return named, suggested
}

func waitingIs(got []string, want ...string) bool {
	return strings.Join(got, " | ") == strings.Join(want, " | ")
}

// "This part is Lena", undone, in an interview the people questions read too
// (a face on screen while the voice talks; the same voice in another clip).
// The fix is in the history and waits for a new version from the moment its
// words move, and its undo stops waiting from the moment they are back: the
// voices learn again after (D145), and the people questions reading them
// (D155–D159) can't make the record lag behind. Undone, the voice is as it
// was, nothing is learned from it (D60), the faces are named by nobody and no
// question suggests Lena any more; a version made while the voices learned
// again has every fix, so nothing waits for it.
func TestUndoingWhoSaysAStretchWaitsForNothing(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	var watch func(ctx context.Context)
	oneVoiceSound(t, &watch, nil)
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Garden story"})
	iv := talkingHead(t, a, p.Project.ID, "Interview", 10, lookAlong(0, 40, 0.3))
	walk := talkingHead(t, a, p.Project.ID, "Walk", 8, lookAlong(1, 41, 0.3))
	before := voiceOf(t, a, iv.ID, "speaker_00")
	r1 := savePlan(t, a, fmt.Sprintf(`{"projectId":%q,"instruction":"the opening","spec":{"schemaVersion":1,"title":"Garden story","output":{"preset":"vertical-1080p","fps":30},
		"clips":[{"id":"talk","assetId":%q,"in":0,"out":3}],"captions":{"enabled":true,"style":"clean","position":"lower","maxWordsPerLine":5}}}`, p.Project.ID, iv.ID)).Revision.ID
	if named, asked := facesNamedOrAsked(t, a, iv.ID); len(named) != 0 || len(asked) != 0 {
		t.Fatalf("nobody's face yet, nobody suggested: %v %v", named, asked)
	}
	call[TranscriptFixOut](t, a, "transcript.fix", TranscriptFixIn{AssetID: iv.ID, From: 0, To: 0.4, Text: "Hello", ProjectID: p.Project.ID})

	// The fix: recorded and waiting, with Lena's name on its lines, before the voices learn from it.
	var fixing moment
	watch = func(ctx context.Context) { fixing = momentOf(ctx, a, p.Project.ID, r1, iv.ID) }
	who := call[TranscriptSpeakerOut](t, a, "transcript.speaker", TranscriptSpeakerIn{SpeakerAssignIn: SpeakerAssignIn{AssetID: iv.ID, From: 1, To: 2.6, Person: "Lena", NewPerson: true}, ProjectID: p.Project.ID})
	if who.FixID == "" || who.Moved != 3 || !who.Relearned {
		t.Fatalf("this part is Lena: %+v", who)
	}
	if !fixing.seen || fixing.err != nil || !waitingIs(fixing.waiting, "words w0→Hello", "speaker Speaker 1→Lena") {
		t.Fatalf("while the voices learned from the fix it was already recorded and waiting: %+v", fixing)
	}
	if fixing.said["1.0"] != "Lena" || fixing.said["2.0"] != "Lena" || fixing.said["0.5"] != "" || fixing.said["2.5"] != "" {
		t.Fatalf("…and its lines already said Lena: %v", fixing.said)
	}
	lena := call[PersonLearnedOut](t, a, "people.learned", PersonLearnedIn{ID: "Lena"})
	if len(lena.Voices) != 1 || lena.Voices[0].AssetID != iv.ID {
		t.Fatalf("Lena's voice is learned from the stretch a person gave her: %+v", lena.Voices)
	}
	// What followed: the rest of the interview and the walk sound like her, so
	// they are recognized as Lena (the interview's two voices combined); the
	// faces are only suggested to be her, never named from it.
	if vs, _ := speakers.Voices(ctx, a, iv.ID, false); len(vs) != 1 || vs[0].Name != "Lena" {
		t.Fatalf("the interview is Lena's: %+v", vs)
	}
	if v := voiceOf(t, a, walk.ID, "speaker_00"); v.Name != "Lena" || v.Source != models.SpeakerByAuto {
		t.Fatalf("the walk is recognized as Lena, not confirmed: %+v", v)
	}
	if named, asked := facesNamedOrAsked(t, a, iv.ID); len(named) != 0 || strings.Join(asked, ",") != "Lena" {
		t.Fatalf("the face is asked about (Lena talks while it is on screen), named by nobody: %v %v", named, asked)
	}

	// The undo: recorded with the words going back, so nothing of it waits; a
	// version made right then has every fix.
	var undoing moment
	var v2 *TranscriptPendingApplyOut
	var v2err error
	watch = func(ctx context.Context) {
		undoing = momentOf(ctx, a, p.Project.ID, r1, iv.ID)
		v2, v2err = CallTyped[TranscriptPendingApplyOut](ctx, a, "transcript.pending.apply", TranscriptPendingApplyIn{ProjectID: p.Project.ID, NoRender: true}, Caller{})
	}
	u := call[TranscriptUndoOut](t, a, "transcript.undo", TranscriptUndoIn{FixID: who.FixID})
	if u.Undone != 1 || u.Summary != "Undone: “Speaker 1” says those words again." {
		t.Fatalf("undo %+v", u)
	}
	if !undoing.seen || undoing.err != nil || !waitingIs(undoing.waiting, "words w0→Hello") {
		t.Fatalf("while the voices learned again the undo was already recorded: %+v", undoing)
	}
	for at, n := range undoing.said {
		if n != "" {
			t.Fatalf("…and the words were back with the voice that had them: %q at %s", n, at)
		}
	}
	if v2err != nil || v2.Revision.Number != 2 || v2.Fixes != 1 {
		t.Fatalf("a new version made meanwhile: %+v %v", v2, v2err)
	}
	if pend := call[TranscriptPendingOut](t, a, "transcript.pending", TranscriptPendingIn{ProjectID: p.Project.ID}); pend.Number != 2 || pend.Count != 0 {
		t.Fatalf("nothing waits for version 2: %+v", pend)
	}
	if pend := call[TranscriptPendingOut](t, a, "transcript.pending", TranscriptPendingIn{ProjectID: p.Project.ID, Revision: r1}); pend.Count != 1 || pend.Places[0].Kind != models.FixWords {
		t.Fatalf("version 1 waits for the retyped word only: %+v", pend)
	}
	h := call[TranscriptHistoryOut](t, a, "transcript.history", TranscriptHistoryIn{AssetID: iv.ID})
	for _, e := range h.Entries {
		if e.Kind == models.FixSpeaker && (e.Undone == nil || e.CanUndo) {
			t.Fatalf("the history says it was undone: %+v", e)
		}
	}

	// The voice is as it was; Lena learned nothing from it, and nothing that
	// followed from her stays.
	vs, err := speakers.Voices(ctx, a, iv.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(vs) != 1 {
		t.Fatalf("one voice again: %+v", vs)
	}
	v := vs[0]
	if v.ID != before.ID || v.Label != before.Label || v.Name != "" || v.PersonID != "" || v.Source != "" || v.MergedInto != "" || len(v.Confirmed) != 0 || v.Words != before.Words {
		t.Fatalf("the voice as before the fix: %+v (before: %+v)", v, before)
	}
	var segs []models.TranscriptSegment
	if err := a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": iv.ID}, &segs); err != nil {
		t.Fatal(err)
	}
	for _, s := range segs {
		if s.Speaker != "speaker_00" || s.SpeakerName != "" || s.SpeakerFrom != "" {
			t.Fatalf("every line is the voice's own again, nobody's: %+v", s)
		}
	}
	if l := call[PersonLearnedOut](t, a, "people.learned", PersonLearnedIn{ID: "Lena"}); len(l.Voices) != 0 || len(l.Faces) != 0 {
		t.Fatalf("Lena's voice and face are learned from nothing: %+v %+v", l.Voices, l.Faces)
	}
	if ps, _ := speakers.LoadProfiles(ctx, a); len(ps) != 0 {
		t.Fatalf("nobody's voice profile keeps anything of it: %+v", ps)
	}
	if w := voiceOf(t, a, walk.ID, "speaker_00"); w.Name != "" || w.PersonID != "" {
		t.Fatalf("the walk isn't recognized as Lena any more: %+v", w)
	}
	for _, c := range []string{iv.ID, walk.ID} {
		if named, asked := facesNamedOrAsked(t, a, c); len(named) != 0 || len(asked) != 0 {
			t.Fatalf("no face named, nobody suggested: %v %v", named, asked)
		}
	}
}

// When the voices can't be learned again after an undo of who says a stretch
// (the fingerprint model fails, say), the undo stays done and recorded — the
// words, the history and what waits for a new version agree — and the error
// says so and what to do; Rebuild voice then takes back what was learned from
// the stretch.
func TestUndoOfWhoSaysAStretchStaysRecordedWhenTheVoicesCantBeLearned(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	var watch func(ctx context.Context)
	broken := false
	oneVoiceSound(t, &watch, &broken)
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Garden story"})
	iv := talkingHead(t, a, p.Project.ID, "Interview", 10, lookAlong(0, 40, 0.3))
	walk := talkingHead(t, a, p.Project.ID, "Walk", 8, lookAlong(1, 41, 0.3))
	r1 := savePlan(t, a, fmt.Sprintf(`{"projectId":%q,"instruction":"the opening","spec":{"schemaVersion":1,"title":"Garden story","output":{"preset":"vertical-1080p","fps":30},
		"clips":[{"id":"talk","assetId":%q,"in":0,"out":3}],"captions":{"enabled":true,"style":"clean","position":"lower","maxWordsPerLine":5}}}`, p.Project.ID, iv.ID)).Revision.ID
	who := call[TranscriptSpeakerOut](t, a, "transcript.speaker", TranscriptSpeakerIn{SpeakerAssignIn: SpeakerAssignIn{AssetID: iv.ID, From: 1, To: 2.6, Person: "Lena", NewPerson: true}, ProjectID: p.Project.ID})
	if v := voiceOf(t, a, walk.ID, "speaker_00"); v.Name != "Lena" {
		t.Fatalf("the walk is recognized as Lena: %+v", v)
	}
	broken = true
	_, err := CallTyped[TranscriptUndoOut](ctx, a, "transcript.undo", TranscriptUndoIn{FixID: who.FixID}, Caller{})
	e := apperr.From(err)
	if e == nil || e.Code != apperr.Internal || e.Message != "The change is saved, but the voices couldn't be learned again from it." ||
		e.Hint != "Use Rebuild voice on the People page to learn them again." || e.Command() != "metarush people rebuild --all" || e.Details.(map[string]any)["saved"] != true {
		t.Fatalf("the error says the change is saved and what to do: %+v", e)
	}
	now := momentOf(ctx, a, p.Project.ID, r1, iv.ID)
	if now.err != nil || len(now.waiting) != 0 {
		t.Fatalf("the undo is recorded: nothing waits: %+v", now)
	}
	for at, n := range now.said {
		if n != "" {
			t.Fatalf("the words are back with the voice that had them: %q at %s", n, at)
		}
	}
	for _, en := range call[TranscriptHistoryOut](t, a, "transcript.history", TranscriptHistoryIn{AssetID: iv.ID}).Entries {
		if en.Kind == models.FixSpeaker && en.Undone == nil {
			t.Fatalf("the history says it was undone: %+v", en)
		}
	}
	broken = false
	call[PersonRebuildOut](t, a, "people.rebuild", PersonRebuildIn{All: true})
	if ps, _ := speakers.LoadProfiles(ctx, a); len(ps) != 0 {
		t.Fatalf("rebuilt, nobody's voice keeps anything of the stretch: %+v", ps)
	}
	if v := voiceOf(t, a, walk.ID, "speaker_00"); v.Name != "" || v.PersonID != "" {
		t.Fatalf("rebuilt, the walk isn't recognized as Lena any more: %+v", v)
	}
}

// voiceOf returns a clip's voice with that label.
func voiceOf(t *testing.T, a *app.App, assetID, label string) models.AssetSpeaker {
	t.Helper()
	vs, err := speakers.Voices(context.Background(), a, assetID, true)
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

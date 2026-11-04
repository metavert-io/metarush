package speakers

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
	"github.com/metavert-io/metarush/internal/voice"
)

func markReference(t *testing.T, as *models.Asset) bson.M {
	t.Helper()
	return bson.M{"$set": bson.M{"reference": models.AssetReference{At: time.Now().UTC(), Why: models.ReferenceStartFrom}}}
}

// A reference's voices — someone else's video, kept to learn from (D443) —
// never take the names of the person's people, never count as their
// appearances and never teach a profile; nobody in it is asked about, and
// naming a voice in it is refused until a person says the video is theirs. A
// clip marked after MetaRush recognized someone in it forgets that.
func TestAReferencesVoicesNeverMatchOrTeach(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	ref := seedAsset(t, a, "their-video.mp4", project, []seg{{0, 12, "speaker_00"}})
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": ref.ID}, markReference(t, ref)); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), ref.ID, ref); err != nil {
		t.Fatal(err)
	}
	if err := Store(ctx, a, ref, detection(cluster("speaker_00", 5, 0, 12)), "test"); err != nil {
		t.Fatal(err)
	}
	own := seedAsset(t, a, "mine.mov", project, []seg{{0, 12, "speaker_00"}})
	if err := Store(ctx, a, own, detection(cluster("speaker_00", 5, 0, 12)), "test"); err != nil {
		t.Fatal(err)
	}
	cleo, err := Name(ctx, a, NameInput{AssetID: own.ID, Label: "speaker_00", Name: "Cleo"})
	if err != nil {
		t.Fatal(err)
	}
	unnamed := func(what, assetID string) {
		t.Helper()
		vs, err := Voices(ctx, a, assetID, false)
		if err != nil || len(vs) != 1 {
			t.Fatalf("%s: voices %+v %v", what, vs, err)
		}
		if v := vs[0]; v.PersonID != "" || v.Name != "" || v.Source != "" || len(v.Suggestions) != 0 || len(v.Centroid) != 0 || len(v.Exemplars) != 0 || v.Model != "" {
			t.Errorf("%s: the stranger's voice is nobody's and keeps no fingerprint: %+v", what, v)
		}
		if names := segNames(t, a, assetID); names["speaker_00"] != "" {
			t.Errorf("%s: its transcript names someone: %v", what, names)
		}
		if ts := openTodos(t, a, assetID); len(ts) != 0 {
			t.Errorf("%s: asked about: %+v", what, ts)
		}
	}
	unnamed("a reference, after Cleo is named in the person's own footage", ref.ID)
	if recs, _, _ := appearances(ctx, a, cleo.Person.ID); recs != 1 {
		t.Errorf("Cleo's appearances count only the person's own footage: %d", recs)
	}

	// Naming a voice in a reference is refused, and teaches nothing.
	if _, err := Name(ctx, a, NameInput{AssetID: ref.ID, Label: "speaker_00", Name: "Famous Creator"}); apperr.From(err) == nil || apperr.From(err).Code != apperr.Conflict ||
		apperr.From(err).Command() != "metarush footage reference "+ref.ID+" --mine" {
		t.Fatalf("naming a stranger in a reference: %v", err)
	}
	ps, _ := LoadProfiles(ctx, a)
	for _, p := range ps {
		if p.Name == "Famous Creator" {
			t.Fatalf("a profile from a reference: %+v", p)
		}
	}

	// A clip MetaRush recognized Cleo in, then marked a reference: it forgets her.
	later := seedAsset(t, a, "their-other.mp4", project, []seg{{0, 12, "speaker_00"}})
	if err := Store(ctx, a, later, detection(cluster("speaker_00", 5, 0, 12)), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := RefreshLibrary(ctx, a); err != nil {
		t.Fatal(err)
	}
	if vs, _ := Voices(ctx, a, later.ID, false); len(vs) != 1 || vs[0].PersonID != cleo.Person.ID || vs[0].Source != models.SpeakerByAuto {
		t.Fatalf("recognized as Cleo before it was marked: %+v", vs)
	}
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": later.ID}, markReference(t, later)); err != nil {
		t.Fatal(err)
	}
	if err := Reconcile(ctx, a, later.ID); err != nil { // what marking a reference does
		t.Fatal(err)
	}
	unnamed("a clip marked a reference after Cleo was recognized in it", later.ID)
	if _, err := RefreshLibrary(ctx, a); err != nil {
		t.Fatal(err)
	}
	unnamed("…and after the library is scored again", later.ID)
	if recs, _, _ := appearances(ctx, a, cleo.Person.ID); recs != 1 {
		t.Errorf("Cleo's appearances after it was marked: %d", recs)
	}
}

// Mia's case (D455): Mia was named only in a clip that is then marked a
// reference. Her voice profile had only that clip's fingerprints, so it is
// rebuilt from what remains — nothing: she has no voice profile, the person's
// own clip MetaRush had named Mia by her voice is asked about again, and a new
// clip that sounds like her isn't named Mia. Ben, named in that clip and in the
// person's own footage, keeps a profile made only from his own clip, and every
// name a person gave stays where they gave it.
func TestMarkingAReferenceTakesBackWhatItsVoicesTaught(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	// Still the person's own footage: Mia and Ben are named in it.
	interview := seedAsset(t, a, "interview.mp4", project, []seg{{0, 12, "speaker_00"}, {13, 25, "speaker_01"}})
	if err := Store(ctx, a, interview, detection(cluster("speaker_00", 7, 0, 12), cluster("speaker_01", 9, 13, 25)), "test"); err != nil {
		t.Fatal(err)
	}
	mia, err := Name(ctx, a, NameInput{AssetID: interview.ID, Label: "speaker_00", Name: "Mia"})
	if err != nil {
		t.Fatal(err)
	}
	ben, err := Name(ctx, a, NameInput{AssetID: interview.ID, Label: "speaker_01", Name: "Ben"})
	if err != nil {
		t.Fatal(err)
	}
	// Ben in the person's own podcast, named by a person there too.
	podcast := seedAsset(t, a, "podcast.mov", project, []seg{{0, 12, "speaker_00"}})
	if err := Store(ctx, a, podcast, detection(cluster("speaker_00", 9, 0, 12)), "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := Name(ctx, a, NameInput{AssetID: podcast.ID, Label: "speaker_00", Name: "Ben", PersonID: ben.Person.ID, Confirm: true}); err != nil {
		t.Fatal(err)
	}
	// An own call MetaRush named Mia by her voice.
	call := seedAsset(t, a, "call.mov", project, []seg{{0, 12, "speaker_00"}})
	if err := Store(ctx, a, call, detection(cluster("speaker_00", 7, 0, 12)), "test"); err != nil {
		t.Fatal(err)
	}
	if v := voiceOf(t, a, call.ID, "speaker_00"); v.PersonID != mia.Person.ID || v.Source != models.SpeakerByAuto {
		t.Fatalf("before: the call's voice is Mia by her voice: %+v", v)
	}
	if p := profileOf(t, a, mia.Person.ID); len(p.Exemplars) == 0 {
		t.Fatalf("before: Mia's voice is known from the interview")
	}

	// The interview turns out to be someone else's video.
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": interview.ID}, markReference(t, interview)); err != nil {
		t.Fatal(err)
	}
	if err := Reconcile(ctx, a, interview.ID); err != nil { // what marking a reference does
		t.Fatal(err)
	}

	// Mia has no voice profile left: nothing recognizes her.
	if p := profileOf(t, a, mia.Person.ID); len(p.Exemplars) != 0 {
		t.Errorf("Mia's voice is still learned from the reference: %d fingerprints from %v", len(p.Exemplars), p.Exemplars[0].Source)
	}
	var stored models.Person
	if err := a.DB.GetByID(ctx, store.CPeople, a.WorkspaceID(), mia.Person.ID, &stored); err != nil || len(stored.Exemplars) != 0 || len(stored.Centroid) != 0 || stored.Recordings != 0 {
		t.Errorf("Mia's stored profile: %d fingerprints, centroid %d, in %d recordings (%v)", len(stored.Exemplars), len(stored.Centroid), stored.Recordings, err)
	}
	// The call isn't Mia by her voice any more: it is asked about.
	if v := voiceOf(t, a, call.ID, "speaker_00"); v.PersonID != "" || v.Source != "" || v.Name != "" || len(v.Suggestions) != 0 {
		t.Errorf("the call still names Mia from the reference's fingerprints: %+v", v)
	}
	if names := segNames(t, a, call.ID); names["speaker_00"] != "" {
		t.Errorf("the call's transcript still names Mia: %v", names)
	}
	if qs := openTodos(t, a, call.ID); len(qs) != 1 || qs[0].Kind != models.TodoSpeakerUnnamed {
		t.Errorf("the call's voice is asked about again: %+v", qs)
	}
	// Ben's profile is his own clip's only, and his name there stays.
	bp := profileOf(t, a, ben.Person.ID)
	if len(bp.Exemplars) == 0 {
		t.Errorf("Ben, named in his own podcast, keeps a voice profile")
	}
	for _, e := range bp.Exemplars {
		if !strings.HasPrefix(e.Source, podcast.ID+"/") {
			t.Errorf("Ben's profile keeps a fingerprint from %s", e.Source)
		}
	}
	if v := voiceOf(t, a, podcast.ID, "speaker_00"); v.PersonID != ben.Person.ID || v.Source != models.SpeakerByPerson {
		t.Errorf("the name a person gave in their own podcast changed: %+v", v)
	}
	// The names a person gave in the reference stay, with no fingerprints.
	for _, l := range []string{"speaker_00", "speaker_01"} {
		if v := voiceOf(t, a, interview.ID, l); v.Source != models.SpeakerByPerson || v.PersonID == "" || len(v.Exemplars) != 0 || len(v.Centroid) != 0 {
			t.Errorf("the reference's %s: %+v", l, v)
		}
	}
	// A new clip that sounds like Mia isn't named or suggested as her.
	later := seedAsset(t, a, "later.mov", project, []seg{{0, 12, "speaker_00"}})
	if err := Store(ctx, a, later, detection(cluster("speaker_00", 7, 0, 12)), "test"); err != nil {
		t.Fatal(err)
	}
	if v := voiceOf(t, a, later.ID, "speaker_00"); v.PersonID != "" || len(v.Suggestions) != 0 {
		t.Errorf("a new clip is matched to Mia: %+v", v)
	}
	// Marking finished half-way (the fingerprints already gone) is finished next time.
	if _, err := a.DB.C(store.CPeople).UpdateOne(ctx, bson.M{"_id": mia.Person.ID}, bson.M{"$set": bson.M{"exemplars": []models.VoiceExemplar{{Source: interview.ID + "/speaker_00", Vec: voiceVec(7), Seconds: 2}},
		"centroid": voiceVec(7), "model": voice.ModelName}}); err != nil {
		t.Fatal(err)
	}
	if err := Reconcile(ctx, a, interview.ID); err != nil {
		t.Fatal(err)
	}
	if p := profileOf(t, a, mia.Person.ID); len(p.Exemplars) != 0 {
		t.Errorf("a profile left with the reference's fingerprints isn't rebuilt the next time: %d", len(p.Exemplars))
	}
}

// A voice question a person dismissed is never asked again (D458), also when
// the clip is heard again — here a clip marked a reference that the person
// then says is theirs, which listens to it again: the voice that took the
// dismissed voice's words keeps the dismissal; the other voice is asked about.
func TestADismissedVoiceQuestionStaysDismissedWhenTheClipIsHeardAgain(t *testing.T) {
	a := testApp(t)
	ctx := context.Background()
	project := ids.New(ids.Project)
	walk := seedAsset(t, a, "walk.mov", project, []seg{{0, 10, "speaker_00"}, {11, 20, "speaker_01"}})
	if err := Store(ctx, a, walk, detection(cluster("speaker_00", 5, 0, 10), cluster("speaker_01", 6, 11, 20)), "test"); err != nil {
		t.Fatal(err)
	}
	var dismissed models.Todo
	for _, q := range openTodos(t, a, walk.ID) {
		if q.Context.Label == "speaker_01" {
			dismissed = q
		}
	}
	if dismissed.ID == "" {
		t.Fatalf("the second voice is asked about: %+v", openTodos(t, a, walk.ID))
	}
	if _, err := todos.Finish(ctx, a, dismissed.ID, models.TodoDismissed, "a passer-by"); err != nil {
		t.Fatal(err)
	}
	// Marked a reference, then the person says it's theirs: it is heard again.
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": walk.ID}, markReference(t, walk)); err != nil {
		t.Fatal(err)
	}
	if err := Reconcile(ctx, a, walk.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": walk.ID}, bson.M{"$unset": bson.M{"reference": ""}}); err != nil {
		t.Fatal(err)
	}
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), walk.ID, walk); err != nil {
		t.Fatal(err)
	}
	if err := Reconcile(ctx, a, walk.ID); err != nil {
		t.Fatal(err)
	}
	again := detection(cluster("speaker_00", 5, 0, 10), cluster("speaker_01", 6, 11, 20))
	again.Previous = CapturePrevious(ctx, a, walk.ID) // what the speaker job does
	if err := Store(ctx, a, walk, again, "test"); err != nil {
		t.Fatal(err)
	}
	qs := openTodos(t, a, walk.ID)
	if len(qs) != 1 || qs[0].Context.Label != "speaker_00" || !strings.HasSuffix(qs[0].GroupKey, "@"+again.Run) {
		t.Fatalf("only the voice never dismissed is asked about, as heard now: %+v", qs)
	}
	var kept models.Todo
	if err := a.DB.C(store.CTodos).FindOne(ctx, bson.M{"groupKey": "speaker:" + walk.ID + ":speaker_01@" + again.Run}).Decode(&kept); err != nil ||
		kept.State != models.TodoDismissed || kept.Resolution != models.ResolvedDismissed || kept.Answer != "a passer-by" {
		t.Fatalf("the dismissal is kept for the voice heard again: %+v %v", kept, err)
	}
	// And it stays so when the questions are brought up to date again.
	if err := Reconcile(ctx, a, walk.ID); err != nil {
		t.Fatal(err)
	}
	if n := len(openTodos(t, a, walk.ID)); n != 1 {
		t.Fatalf("a dismissed question came back: %d open", n)
	}
}

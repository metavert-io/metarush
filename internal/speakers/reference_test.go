package speakers

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
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

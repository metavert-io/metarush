package faces

import (
	"context"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// A reference's faces — someone else's video, kept to learn from (D443) — are
// never asked about, recognized or named, and keep no fingerprints: marking a
// clip a reference takes them away, and naming a face in it is refused until a
// person says the video is theirs.
func TestAReferencesFacesAreNeverRecognizedOrNamed(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	asset, _ := clip(t, a)
	ada := person(t, a, "Ada")
	voices(t, a, asset, map[string]models.Person{"speaker_00": ada})
	if n, err := Candidates(ctx, a, asset); err != nil || n == 0 {
		t.Fatalf("its own footage: faces asked about: %d %v", n, err)
	}
	// Fingerprints, a recognition and a candidate, as an earlier look left them.
	if _, err := a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"assetId": asset}, bson.M{"$set": bson.M{"prints": []models.FacePrint{{At: 1, Q: 0.6, V: []float32{1, 0, 0}}},
		"printModel": "sface", "recognized": models.FaceRecognized{PersonID: ada.ID, Name: "Ada", Score: 0.7}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": asset}, bson.M{"$set": bson.M{"reference": models.AssetReference{At: time.Now().UTC(), Why: models.ReferenceStartFrom}}}); err != nil {
		t.Fatal(err)
	}
	if n, err := Candidates(ctx, a, asset); err != nil || n != 0 || len(openQuestions(t, a, asset)) != 0 {
		t.Fatalf("a reference is asked about: %d %v %+v", n, err, openQuestions(t, a, asset))
	}
	fs, err := List(ctx, a, asset)
	if err != nil || len(fs) == 0 {
		t.Fatalf("faces %+v %v", fs, err)
	}
	for _, f := range fs {
		if len(f.Prints) != 0 || f.PrintModel != "" || f.Recognized != nil || f.Candidate != nil {
			t.Errorf("a reference's face keeps a fingerprint or a guess: %+v", f)
		}
	}
	refused := func(what string, err error) {
		t.Helper()
		if ae := apperr.From(err); ae == nil || ae.Code != apperr.Conflict || ae.Command() != "metarush footage reference "+asset+" --mine" {
			t.Errorf("%s: refused: %v", what, err)
		}
	}
	_, err = Confirm(ctx, a, fs[0].ID, ada)
	refused("naming a face", err)
	_, err = NameGroup(ctx, a, asset, "fgr_any", ada)
	refused("naming a group of faces", err)
}

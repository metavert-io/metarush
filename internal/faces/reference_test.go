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

// Ada's case (D455): a person named Ada's face only in a clip that is then
// marked a reference. What that face taught goes at once: the person's own
// clip where MetaRush named Ada by her looks no longer names her, and a new clip
// that looks like her isn't named her either. Bo, whose face a person named in
// their own clip, is still recognized, and the faces a person named stay named
// where they named them.
func TestMarkingAReferenceTakesBackWhatItsFacesTaught(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ada, bo := person(t, a, "Ada"), person(t, a, "Bo")
	// Still the person's own footage: Ada named there by a person.
	garden := indexed(t, a, "Garden", 6, nil, onScreen{left, 0, 6, look(0, 50, 0.1)})
	if _, err := Confirm(ctx, a, faceOf(t, a, garden, 1).ID, ada); err != nil {
		t.Fatal(err)
	}
	// Bo, named by a person in their own kitchen clip.
	kitchen := indexed(t, a, "Kitchen", 6, nil, onScreen{left, 0, 6, look(1, 51, 0.1)})
	if _, err := Confirm(ctx, a, faceOf(t, a, kitchen, 1).ID, bo); err != nil {
		t.Fatal(err)
	}
	// An own clip where MetaRush named Ada and Bo by their looks.
	porch := indexed(t, a, "Porch", 12, []float64{6}, onScreen{left, 0, 6, look(0, 52, 0.3)}, onScreen{right, 6, 12, look(1, 53, 0.3)})
	if _, _, err := Recognize(ctx, a, porch); err != nil {
		t.Fatal(err)
	}
	if f := faceOf(t, a, porch, 1); f.Recognized == nil || f.Recognized.PersonID != ada.ID {
		t.Fatalf("before: the porch's first face is Ada by her looks: %+v", f)
	}
	if f := faceOf(t, a, porch, 2); f.Recognized == nil || f.Recognized.PersonID != bo.ID {
		t.Fatalf("before: the porch's second face is Bo by his looks: %+v", f)
	}

	// The garden turns out to be someone else's video.
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": garden}, bson.M{"$set": bson.M{"reference": models.AssetReference{At: time.Now().UTC(), Why: models.ReferenceMarked}}}); err != nil {
		t.Fatal(err)
	}
	if _, err := Candidates(ctx, a, garden); err != nil { // what marking a reference does
		t.Fatal(err)
	}

	if f := faceOf(t, a, porch, 1); f.Recognized != nil || (f.Candidate != nil && f.Candidate.PersonID == ada.ID) {
		t.Errorf("the porch still names Ada from the reference's face: %+v %+v", f.Recognized, f.Candidate)
	}
	if f := faceOf(t, a, porch, 2); f.Recognized == nil || f.Recognized.PersonID != bo.ID {
		t.Errorf("Bo, taught by the person's own clip, is no longer recognized: %+v", f)
	}
	if f := faceOf(t, a, kitchen, 1); !f.Confirmed() || f.PersonID != bo.ID {
		t.Errorf("the face a person named in their own clip changed: %+v", f)
	}
	if f := faceOf(t, a, garden, 1); !f.Confirmed() || f.PersonID != ada.ID || len(f.Prints) != 0 || f.PrintModel != "" {
		t.Errorf("the reference's face keeps the name a person gave, without a fingerprint: %+v", f)
	}
	rec, err := loadRecognizer(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rec.people {
		if p.ID == ada.ID {
			t.Errorf("Ada's looks are still learned from the reference: %d fingerprints", len(p.Exemplars))
		}
	}
	// A new clip that looks like Ada isn't named her or asked about as her by looks.
	beach := indexed(t, a, "Beach", 6, nil, onScreen{left, 0, 6, look(0, 54, 0.2)})
	if _, _, err := Recognize(ctx, a, beach); err != nil {
		t.Fatal(err)
	}
	if f := faceOf(t, a, beach, 1); f.Recognized != nil || (f.Candidate != nil && f.Candidate.PersonID == ada.ID) {
		t.Errorf("a new clip is matched to Ada: %+v %+v", f.Recognized, f.Candidate)
	}
}

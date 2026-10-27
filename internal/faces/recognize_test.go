package faces

import (
	"context"
	"crypto/sha256"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// look is a face fingerprint mostly along axis a with mix of axis b
// (cosine with the pure axis a: sqrt(1-mix²)).
func look(a, b int, mix float64) []float32 {
	v := make([]float32, faceid.Dim)
	v[a] = float32(math.Sqrt(1 - mix*mix))
	v[b] += float32(mix)
	faceid.Normalize(v)
	return v
}

// onScreen is a face in a clip: where, when, and what it looks like.
type onScreen struct {
	box      vision.Box
	from, to float64
	print    []float32
}

// clearer: in clips indexed by the tests, a face whose fingerprint is one of
// these is this clear (0.7 otherwise).
var clearer = map[*float32]float64{}

// indexed stores a clip whose index shows these faces (one frame a second),
// with their fingerprints, as the worker would.
func indexed(t *testing.T, a *app.App, title string, dur float64, cuts []float64, people ...onScreen) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{}, Kind: "video", Name: strings.ToLower(title) + ".mov", Title: title,
		SHA256: fmt.Sprintf("%x", sha256.Sum256([]byte(title))), Size: 1, MIME: "video/quicktime", StorageKey: "k-" + title, State: models.AssetAvailable,
		Media: models.MediaInfo{Duration: dur, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, Streams: []models.Stream{}}, Variants: map[string]models.Variant{},
		CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	var samples []visual.Sample
	for at := 0.5; at < dur; at++ {
		f := vision.Frame{W: 1280, H: 720}
		var prints [][]float32
		for _, p := range people {
			if at >= p.from && at < p.to {
				q := 0.7
				if c, ok := clearer[&p.print[0]]; ok {
					q = c
				}
				f.Faces = append(f.Faces, vision.Face{Conf: 1, Box: p.box, Quality: &q})
				prints = append(prints, p.print)
			}
		}
		samples = append(samples, visual.Sample{At: at, Frame: f, Prints: prints})
	}
	idx := visual.Build(samples, 1, dur, cuts, "proxy")
	ins := models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind, Provider: "apple-vision",
		Digest: "d", Data: visual.ToMap(idx), CreatedAt: now}
	if err := a.DB.Insert(ctx, store.CInsights, ins); err != nil {
		t.Fatal(err)
	}
	if err := Save(ctx, a, as.ID, idx); err != nil {
		t.Fatal(err)
	}
	return as.ID
}

// talk adds a stretch of a voice to a clip's transcript.
func talk(t *testing.T, a *app.App, assetID, label string, from, to float64) {
	t.Helper()
	n, _ := a.DB.C(store.CSegments).CountDocuments(context.Background(), bson.M{"assetId": assetID})
	s := models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: assetID, Index: int(n), From: from, To: to, Speaker: label, Text: "We grew them on the porch."}
	if err := a.DB.Insert(context.Background(), store.CSegments, s); err != nil {
		t.Fatal(err)
	}
}

func faceOf(t *testing.T, a *app.App, assetID string, track int) models.FaceTrack {
	t.Helper()
	tracks, err := Load(context.Background(), a, assetID)
	if err != nil {
		t.Fatal(err)
	}
	f, ok := tracks[track]
	if !ok {
		t.Fatalf("no track %d in %s", track, assetID)
	}
	return f
}

func questionsAbout(t *testing.T, a *app.App, assetID string) []models.Todo {
	t.Helper()
	return openQuestions(t, a, assetID)
}

var (
	left  = vision.Box{0.1, 0.2, 0.2, 0.35}
	right = vision.Box{0.65, 0.2, 0.2, 0.35}
)

// Faces are recognized across clips the way voices are (D114): only faces a
// person confirmed teach what someone looks like; a close, clear match is named
// automatically; a middling one is asked about; strangers are left alone; two
// faces on screen together are never both the same person; "Not Ada?" takes
// the name away for good; and a wrong confirmation is found, asked about
// again and stops naming other faces.
func TestRecognizesFacesAcrossClips(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ada, bo := person(t, a, "Ada"), person(t, a, "Bo")
	// Clip 1: Ada, confirmed by a person.
	c1 := indexed(t, a, "Garden", 6, nil, onScreen{left, 0, 6, look(0, 50, 0.1)})
	if _, err := Confirm(ctx, a, faceOf(t, a, c1, 1).ID, ada); err != nil {
		t.Fatal(err)
	}
	// Clip 2: Bo, confirmed too.
	c2 := indexed(t, a, "Kitchen", 6, nil, onScreen{left, 0, 6, look(1, 51, 0.1)})
	if _, err := Confirm(ctx, a, faceOf(t, a, c2, 1).ID, bo); err != nil {
		t.Fatal(err)
	}
	// Clip 3: Ada again (very alike), someone a bit like Ada who talks, and a
	// stranger who never does.
	c3 := indexed(t, a, "Porch", 12, []float64{4, 8},
		onScreen{left, 0, 4, look(0, 52, 0.45)},
		onScreen{right, 4, 8, look(0, 53, 0.85)},
		onScreen{left, 8, 12, look(7, 54, 0.1)})
	talk(t, a, c3, "speaker_00", 4.1, 7.9)
	n, fresh, err := Recognize(ctx, a, c3)
	if err != nil {
		t.Fatal(err)
	}
	if f := faceOf(t, a, c3, 1); f.Recognized == nil || f.Recognized.PersonID != ada.ID || f.PersonID != "" || f.ConfirmedBy != nil {
		t.Fatalf("Ada's face is named by its looks (not confirmed): %+v", f)
	}
	if len(fresh) != 1 || fresh[0] != ada.ID {
		t.Fatalf("Ada was recognized for the first time here: %v", fresh)
	}
	if f := faceOf(t, a, c3, 2); f.Recognized != nil || f.Candidate == nil || f.Candidate.PersonID != ada.ID || f.Candidate.By != "looks" {
		t.Fatalf("a middling match is asked about: %+v", f)
	}
	if f := faceOf(t, a, c3, 3); f.Recognized != nil || f.Candidate != nil {
		t.Fatalf("a stranger is left alone: %+v", f)
	}
	qs := questionsAbout(t, a, c3)
	if n != 1 || len(qs) != 1 || qs[0].Question != "Is this Ada?" || !strings.Contains(qs[0].Detail, "Looks like Ada in other clips") {
		t.Fatalf("one question (the face that talks), in plain words: %d %+v", n, qs)
	}
	names := Namer(mustLoad(t, a, c3))
	if got, sure := names(1); got != "looks like Ada" || !sure {
		t.Fatalf("the readout says who it looks like: %q", got)
	}
	// Two faces at once that both look like Ada: only the closer one is named.
	c4 := indexed(t, a, "Party", 6, nil, onScreen{left, 0, 6, look(0, 55, 0.3)}, onScreen{right, 0, 6, look(0, 56, 0.2)})
	if _, _, err := Recognize(ctx, a, c4); err != nil {
		t.Fatal(err)
	}
	f1, f2 := faceOf(t, a, c4, 1), faceOf(t, a, c4, 2)
	if (f1.Recognized != nil) == (f2.Recognized != nil) || (f2.Recognized == nil) {
		t.Fatalf("only the closer face is Ada: %+v / %+v", f1.Recognized, f2.Recognized)
	}
	if f1.Candidate == nil || f1.Candidate.PersonID != ada.ID {
		t.Fatalf("the other is asked about: %+v", f1)
	}
	// "Not Ada?" on an automatic name: it goes, and Ada is never suggested for that face again.
	auto := faceOf(t, a, c3, 1)
	if _, err := Reject(ctx, a, auto.ID, ada.ID); err != nil {
		t.Fatal(err)
	}
	if f := faceOf(t, a, c3, 1); f.Recognized != nil || (f.Candidate != nil && f.Candidate.PersonID == ada.ID) || !contains(f.Rejected, ada.ID) {
		t.Fatalf("not Ada: %+v", f)
	}
	// Automatic names never teach: the profile is still only clip 1's face.
	rec, err := loadRecognizer(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range rec.people {
		if p.ID == ada.ID && len(p.Exemplars) > faceid.MaxExemplarsPerFace {
			t.Fatalf("Ada learned from automatic names: %d exemplars", len(p.Exemplars))
		}
	}

	// A wrong confirmation: the stranger of clip 3 is named Ada by mistake, then
	// two more real Ada faces are confirmed. The stranger's face looks unlike
	// Ada's others, so MetaRush asks again and stops using it…
	stranger := faceOf(t, a, c3, 3)
	if _, err := Confirm(ctx, a, stranger.ID, ada); err != nil {
		t.Fatal(err)
	}
	// …meanwhile it would name the stranger's look-alike Ada (only two Ada faces so far).
	c5 := indexed(t, a, "Stranger again", 6, nil, onScreen{left, 0, 6, look(7, 57, 0.15)})
	if _, _, err := Recognize(ctx, a, c5); err != nil {
		t.Fatal(err)
	}
	if f := faceOf(t, a, c5, 1); f.Recognized == nil && f.Candidate == nil {
		t.Fatalf("with the wrong confirmation in Ada's faces, her look-alike is matched: %+v", f)
	}
	c6 := indexed(t, a, "Beach", 6, nil, onScreen{left, 0, 6, look(0, 58, 0.2)})
	if _, err := Confirm(ctx, a, faceOf(t, a, c6, 1).ID, ada); err != nil {
		t.Fatal(err)
	}
	if f := faceOf(t, a, c5, 1); f.Recognized != nil || (f.Candidate != nil && f.Candidate.PersonID == ada.ID) {
		t.Fatalf("the wrong confirmation no longer names others once it stands out: %+v", f)
	}
	var recheck []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"kind": models.TodoFacePerson, "state": models.TodoOpen, "context.faceId": stranger.ID}, &recheck); err != nil {
		t.Fatal(err)
	}
	if len(recheck) != 1 || recheck[0].Question != "Is this really Ada?" {
		t.Fatalf("the odd confirmed face is asked about again: %+v", recheck)
	}
	// "No": it isn't Ada; the question closes.
	if _, err := Reject(ctx, a, stranger.ID, ada.ID); err != nil {
		t.Fatal(err)
	}
	if f := faceOf(t, a, c3, 3); f.PersonID != "" || f.Confirmed() {
		t.Fatalf("no longer Ada: %+v", f)
	}
	var open int64
	open, _ = a.DB.C(store.CTodos).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "state": models.TodoOpen, "context.faceId": stranger.ID})
	if open != 0 {
		t.Fatalf("the question about it is closed: %d", open)
	}
	// Renaming follows names given by looks.
	c7 := indexed(t, a, "Library", 6, nil, onScreen{left, 0, 6, look(1, 59, 0.3)})
	if _, _, err := Recognize(ctx, a, c7); err != nil {
		t.Fatal(err)
	}
	if f := faceOf(t, a, c7, 1); f.Recognized == nil || f.Recognized.PersonID != bo.ID {
		t.Fatalf("Bo's face: %+v", f)
	}
	Rename(ctx, a, bo.ID, "Bo Diddley")
	if f := faceOf(t, a, c7, 1); f.Recognized == nil || f.Recognized.Name != "Bo Diddley" {
		t.Fatalf("renamed: %+v", f.Recognized)
	}
	_, recognized, _, err := OfPerson(ctx, a, bo.ID)
	if err != nil || len(recognized) != 1 || len(recognized[0].Prints) != 0 {
		t.Fatalf("Bo's page lists the face named by looks (fingerprints never leave the server): %d %v", len(recognized), err)
	}
}

func mustLoad(t *testing.T, a *app.App, assetID string) map[int]models.FaceTrack {
	t.Helper()
	m, err := Load(context.Background(), a, assetID)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

// A face whose fingerprints came from another model is never compared.
func TestOtherModelsAreNotCompared(t *testing.T) {
	f := models.FaceTrack{PrintModel: "some-other-model", Prints: []models.FacePrint{{Q: 1, V: look(0, 1, 0.1)}}}
	if Vector(f) != nil {
		t.Fatal("prints from another model must not be used")
	}
	f.PrintModel = faceid.ModelName
	if v := Vector(f); len(v) != faceid.Dim {
		t.Fatal(fmt.Sprint("prints of this model are used: ", len(v)))
	}
}

// Clips indexed before frames were taken at their moments can hold, at the end
// of one person's face, a frame of the next shot: in a call's speaker view, the
// next speaker. That fingerprint is left out (PrintKeep), so confirming Ada's
// face doesn't teach Bo's looks as hers and Bo's face is not named Ada.
func TestTheNextShotsFaceAtTheEndTeachesNothing(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	ada := person(t, a, "Ada")
	adaLooks, boLooks := look(0, 50, 0.1), look(1, 0, 0.3) // about 0.3 alike: one track to the index
	// Ada's picture, its last frame Bo's (clearer than the others).
	clearer[&boLooks[0]] = 0.9
	defer delete(clearer, &boLooks[0])
	c1 := indexed(t, a, "Call", 6, nil, onScreen{left, 0, 5.5, adaLooks}, onScreen{left, 5.5, 6, boLooks})
	f := faceOf(t, a, c1, 1)
	if len(f.Prints) != 6 || len(usablePrints(f.Prints)) != 5 || usablePrints(f.Prints)[4].At != 4.5 {
		t.Fatalf("one face with Bo's fingerprint at its end, left out: %d prints, %d usable", len(f.Prints), len(usablePrints(f.Prints)))
	}
	if _, err := Confirm(ctx, a, f.ID, ada); err != nil {
		t.Fatal(err)
	}
	// Bo, elsewhere.
	c2 := indexed(t, a, "Kitchen", 6, nil, onScreen{left, 0, 6, look(1, 0, 0.32)})
	if _, _, err := Recognize(ctx, a, c2); err != nil {
		t.Fatal(err)
	}
	if b := faceOf(t, a, c2, 1); b.Recognized != nil || (b.Candidate != nil && b.Candidate.PersonID == ada.ID) {
		t.Fatalf("Bo is not Ada: %+v %+v", b.Recognized, b.Candidate)
	}
	// Two fingerprints unlike each other: the later one goes; alike ones stay.
	two := usablePrints([]models.FacePrint{{At: 1, Q: 0.5, V: adaLooks}, {At: 2, Q: 0.9, V: boLooks}})
	if len(two) != 1 || two[0].At != 1 {
		t.Fatalf("the later of two unlike fingerprints goes: %+v", two)
	}
	if same := usablePrints([]models.FacePrint{{At: 1, Q: 0.5, V: adaLooks}, {At: 2, Q: 0.5, V: look(0, 51, 0.3)}, {At: 3, Q: 0.5, V: look(0, 52, 0.4)}}); len(same) != 3 {
		t.Fatalf("one person's fingerprints all stay: %d", len(same))
	}
}

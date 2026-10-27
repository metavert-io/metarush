package ops

import (
	"context"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// lookAlong is a face fingerprint mostly along axis a, a little along b.
func lookAlong(a, b int, mix float32) []float32 {
	v := make([]float32, faceid.Dim)
	v[a], v[b] = 1, mix
	faceid.Normalize(v)
	return v
}

// indexedFace stores a clip whose index shows one face (with its fingerprint)
// for six seconds, and its faces, as the worker would.
func indexedFace(t *testing.T, a *app.App, projectID, name string, print []float32) string {
	t.Helper()
	now := time.Now().UTC()
	as := fill(models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{projectID}, Kind: "video", Name: name,
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 6, HasVideo: true, HasAudio: true, Width: 1280, Height: 720, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now})
	insert(t, a, store.CAssets, as)
	var samples []visual.Sample
	for i := 0; i < 6; i++ {
		q := 0.7
		f := vision.Frame{W: 1280, H: 720, Faces: []vision.Face{{Conf: 1, Box: vision.Box{0.4, 0.2, 0.2, 0.35}, Quality: &q}}}
		samples = append(samples, visual.Sample{At: float64(i) + 0.5, Frame: f, Prints: [][]float32{print}})
	}
	idx := visual.Build(samples, 1, 6, nil, "proxy")
	insert(t, a, store.CInsights, models.Insight{ID: ids.New(ids.Insight), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Kind: visual.InsightKind,
		Provider: "apple-vision", Digest: "d", Data: visual.ToMap(idx), CreatedAt: now})
	if err := faces.Save(context.Background(), a, as.ID, idx); err != nil {
		t.Fatal(err)
	}
	return as.ID
}

// Naming a face says where else that person's face was recognized (D114), so
// the person knows there is something to check; a face like no one else's
// gets the short answer.
func TestNamingAFaceSaysWhereElseItIsRecognized(t *testing.T) {
	a := testapp.New(t)
	p := call[ProjectOut](t, a, "project.create", ProjectCreateIn{Name: "Garden"})
	one := indexedFace(t, a, p.Project.ID, "one.mov", lookAlong(0, 50, 0.1))
	indexedFace(t, a, p.Project.ID, "two.mov", lookAlong(0, 51, 0.12))
	indexedFace(t, a, p.Project.ID, "three.mov", lookAlong(0, 52, 0.08))
	stranger := indexedFace(t, a, p.Project.ID, "four.mov", lookAlong(9, 53, 0.1))

	fs := call[FacesOut](t, a, "footage.faces", FacesIn{AssetID: one})
	if len(fs.Faces) != 1 {
		t.Fatalf("one face in the first clip: %+v", fs.Faces)
	}
	out := call[FaceOut](t, a, "footage.faces.name", FaceNameIn{FaceID: fs.Faces[0].ID, Name: "Ada"})
	if want := "That face is Ada now. Ada's face was also recognized in 2 other clips (not confirmed yet)."; out.Done != want {
		t.Fatalf("naming says where else she was recognized:\n got  %q\n want %q", out.Done, want)
	}
	fs = call[FacesOut](t, a, "footage.faces", FacesIn{AssetID: stranger})
	out = call[FaceOut](t, a, "footage.faces.name", FaceNameIn{FaceID: fs.Faces[0].ID, Name: "Bo"})
	if want := "That face is Bo now."; out.Done != want {
		t.Fatalf("no one else looks like Bo:\n got  %q\n want %q", out.Done, want)
	}
}

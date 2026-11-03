package faces

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// A face's fingerprints say what found the face and its five points (D448):
// Apple Vision's index (and an index from before it was recorded), or the
// open models'. Matching treats both alike; the tag is for measurements.
func TestPrintsSayWhatFoundTheFace(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{}, Kind: "video", Name: "porch.mov", Title: "Porch",
		SHA256: strings.Repeat("ef", 32), Size: 1, MIME: "video/quicktime", StorageKey: "k", State: models.AssetAvailable,
		Media: models.MediaInfo{Duration: 4, HasVideo: true, Width: 1280, Height: 720, Streams: []models.Stream{}}, Variants: map[string]models.Variant{},
		CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	vec := make([]float32, faceid.Dim)
	vec[0], vec[1] = 0.8, 0.6
	var samples []visual.Sample
	for i := 0; i < 4; i++ {
		f := vision.Frame{W: 1280, H: 720, Faces: []vision.Face{{Conf: 1, Box: vision.Box{0.3, 0.2, 0.2, 0.35}, Quality: q(0.7)}}}
		samples = append(samples, visual.Sample{At: float64(i) + 0.5, Frame: f, Prints: [][]float32{vec}})
	}
	idx := visual.Build(samples, 1, 4, nil, "proxy")
	for _, c := range []struct{ backend, want string }{
		{"", visual.BackendAppleVision}, {visual.BackendAppleVision, visual.BackendAppleVision}, {visual.BackendOpenModels, visual.BackendOpenModels},
	} {
		idx.Backend = c.backend
		if err := Save(ctx, a, as.ID, idx); err != nil {
			t.Fatal(err)
		}
		fs, err := Load(ctx, a, as.ID)
		if err != nil || len(fs) != 1 {
			t.Fatalf("an index by %q: %v %d faces", c.backend, err, len(fs))
		}
		for _, f := range fs {
			if len(f.Prints) == 0 || f.PrintsBy != c.want {
				t.Fatalf("an index by %q: %d prints by %q", c.backend, len(f.Prints), f.PrintsBy)
			}
		}
	}
}

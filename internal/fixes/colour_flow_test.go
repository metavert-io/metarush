package fixes_test

import (
	"context"
	"testing"

	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/models"
)

// A dark camera clip with a few bright highlights (a cellar with a lamp):
// "Balance colour and brightness" fits ("it's dark"), and the fixed copy
// comes out brighter. It came out darker before: the brightness step passed
// ffmpeg a gamma under 1, which darkens.
func TestDarkClipComesOutBrighter(t *testing.T) {
	f := newFlow(t)
	dark := f.ffmpeg("cellar.mp4", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=15:duration=4", "-vf", "eq=brightness=-0.3:contrast=0.6,drawbox=x=12:y=12:w=48:h=32:color=white:t=fill",
		"-c:v", "libx264", "-pix_fmt", "yuv420p")
	as := f.ingest(dark)
	f.tick(fixes.SetIn{ID: as.ID, Kind: models.SourceCamera})
	f.measure(f.asset(as.ID))
	if row := f.row(f.view(as.ID), models.FixColour); !row.Fits {
		t.Fatalf("colour should fit a dark clip: %+v", row)
	}
	v := f.tick(fixes.SetIn{ID: as.ID, Tick: []string{models.FixColour}})
	f.prepare(v.JobIDs[0])
	as = f.asset(as.ID)
	if !as.Fixed() {
		t.Fatalf("the clip uses its fixed copy: %+v", as.Fixes)
	}
	before := f.brightness(as.StorageKey, as.SHA256, as.Size, as.Media.Duration)
	c := f.copyOf(as)
	after := f.brightness(c.StorageKey, c.SHA256, c.Size, as.Media.Duration)
	if after <= before {
		t.Fatalf("the balanced copy is darker: brightness %.3f → %.3f (%s)", before, after, stepOf(c, models.FixColour).Params["filter"])
	}
}

// brightness is a stored file's average brightness (0–1), as the colour fix measures it.
func (f *flow) brightness(key, sha string, size int64, dur float64) float64 {
	f.t.Helper()
	ctx := context.Background()
	p, err := f.a.Cache.Ensure(ctx, f.a.Media, key, sha, size, nil)
	if err != nil {
		f.t.Fatal(err)
	}
	c, err := fixes.Colour(ctx, f.a.Tools, p, dur)
	if err != nil {
		f.t.Fatal(err)
	}
	return c.Brightness
}

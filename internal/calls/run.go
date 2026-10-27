package calls

import (
	"context"
	"math"

	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/visual"
)

// Input is what finding a footage's switches needs.
type Input struct {
	Asset    *models.Asset
	Evidence *evidence.Stored
	Index    *visual.Index // nil when no Mac looked at the footage yet
	// Path is the file to read frames from; Source says which it is (proxy or
	// original) and FPS its frame rate.
	Path   string
	Source string
	FPS    float64
}

// Worth reports whether a footage needs switches at all: picture, and at
// least one cut found by the evidence (or the on-screen index).
func Worth(as *models.Asset, ev *evidence.Stored, idx *visual.Index) bool {
	if as == nil || !as.Media.HasVideo || ev == nil {
		return false
	}
	for _, vc := range ev.VisualChanges {
		if vc.Kind == "cut" {
			return true
		}
	}
	for _, sc := range ev.Scenes {
		if sc.Score >= exactScene {
			return true
		}
	}
	return idx != nil && len(idx.Cuts) > 0
}

// Compute finds a footage's switches (D160) with ffmpeg reading in.Path.
func Compute(ctx context.Context, ts *tools.Set, in Input, scratch string) (*Stored, error) {
	cs := Candidates(in.Evidence, in.Index)
	fps := in.FPS
	if fps <= 0 {
		fps = in.Asset.Media.FPS
	}
	if fps <= 0 {
		fps = 30
	}
	sw, n, err := Detect(ctx, cs, in.Asset.Media.Duration, fps, FFmpegDecoder(ts, in.Path, scratch))
	if err != nil {
		return nil, err
	}
	st := &Stored{Version: Version, Source: in.Source, FPS: fps, Duration: in.Asset.Media.Duration, Switches: sw, Refined: n,
		Precision: precision(in.Asset, in.Source, fps)}
	if in.Index != nil {
		st.Index = in.Index.Version
	}
	return st, nil
}

// precision is how far an original's switch can be from one found in the
// file read: none when that file has the original's own frames (the original,
// or a preview copy at the original's constant frame rate), else one frame of
// the file read.
func precision(as *models.Asset, source string, fps float64) float64 {
	if source == "original" {
		return 0
	}
	if !as.Media.VFR && as.Media.FPS > 0 && math.Abs(as.Media.FPS-fps) < 0.01 {
		return 0
	}
	return r3(1 / math.Max(1, fps))
}

//go:build screeneval

package visionmodels

// The measurement of fills (D445): the faceset's photos made into the shots
// editors make — a landscape shot made vertical (9:16), square or 4:5, a
// portrait shot made landscape — over a blurred, enlarged copy of
// themselves, and three sets that aren't fills (the photos as they are, their
// landscape crops, and those blurred all over). For each: how many faces the
// open models find in the copy that Apple Vision doesn't, with the fill rule
// and without it, and whether the rule drops any face Apple Vision found in
// the picture. Opt-in, as TestCompareBackends:
//
//	EVAL_FACESET=<folder> METARUSH_SCREEN_MODELS=<folder> [METARUSH_VISION=<helper>] \
//	  go test -tags screeneval -run TestFillsOnTheFaceset -v -timeout 60m ./internal/visionmodels/

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/onnx/onnxtest"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
)

// faceSetShot is one way of making a photo into a frame: its ffmpeg filter
// (a photo's crop around its main face goes where LAND or PORT stands) and,
// for a fill, where its sharp picture is (pixels).
type faceSetShot struct {
	name    string
	graph   string
	picture *[4]float64
}

func faceSetShots() []faceSetShot {
	fill := func(w, h int, kind, fg string, sigma float64) string {
		return fmt.Sprintf("[0:v]%s,split[a][b];[a]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,gblur=sigma=%g[bg];[b]%s[fg];[bg][fg]overlay=(W-w)/2:(H-h)/2",
			kind, w, h, w, h, sigma, fg)
	}
	return []faceSetShot{
		{"9:16 fill, blur 8", fill(540, 960, "LAND", "scale=540:-2", 8), &[4]float64{0, 328, 540, 304}},
		{"9:16 fill, blur 16", fill(540, 960, "LAND", "scale=540:-2", 16), &[4]float64{0, 328, 540, 304}},
		{"1:1 fill, blur 8", fill(720, 720, "LAND", "scale=720:-2", 8), &[4]float64{0, 157, 720, 406}},
		{"4:5 fill, blur 8", fill(576, 720, "LAND", "scale=576:-2", 8), &[4]float64{0, 198, 576, 324}},
		{"portrait made landscape, blur 8", fill(960, 540, "PORT", "scale=-2:540", 8), &[4]float64{328, 0, 304, 540}},
		{"as they are", "[0:v]scale='min(960,iw)':-2", nil},
		{"landscape crops", "[0:v]LAND", nil},
		{"blurred all over (4)", "[0:v]scale='min(960,iw)':-2,gblur=sigma=4", nil},
	}
}

func TestFillsOnTheFaceset(t *testing.T) {
	set := os.Getenv("EVAL_FACESET")
	if set == "" {
		t.Skip("set EVAL_FACESET to the faceset's photos (tools/vision/eval/faces.py --folder keeps them)")
	}
	store := Store{Dir: os.Getenv("METARUSH_SCREEN_MODELS")}
	if err := store.Ready(PartIndex); err != nil {
		t.Skip(err)
	}
	e, err := OpenEngine(onnxtest.Library(t), store.IndexPaths())
	if err != nil {
		t.Fatal(err)
	}
	ts := tools.New(config.Tools{Vision: os.Getenv("METARUSH_VISION")})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg isn't here")
	}
	apple := runtime.GOOS == "darwin" && ts.Has(tools.Vision)
	photos, _ := filepath.Glob(filepath.Join(set, "*.jpg"))
	sort.Strings(photos)
	// Each photo's main face (the open models', on the photo as it is), for its crops.
	centres := map[string][2]float64{}
	for _, ph := range photos {
		p := readPicture(t, ph)
		c := [2]float64{0.5, 0.4}
		if found, err := e.faces.find(p); err == nil {
			best := 0.0
			for _, fa := range found {
				if fa.score >= FaceMin && fa.box[2]*fa.box[3] > best {
					best = fa.box[2] * fa.box[3]
					c = [2]float64{(fa.box[0] + fa.box[2]/2) / float64(p.w), (fa.box[1] + fa.box[3]/2) / float64(p.h)}
				}
			}
		}
		centres[ph] = c
	}
	for _, shot := range faceSetShots() {
		dir := t.TempDir()
		for _, ph := range photos {
			c := centres[ph]
			land := fmt.Sprintf("crop=w='min(iw,ih*16/9)':h='min(ih,iw*9/16)':x='max(0,min(iw-ow,iw*%.4f-ow/2))':y='max(0,min(ih-oh,ih*%.4f-oh*0.4))',scale=960:540,setsar=1", c[0], c[1])
			port := fmt.Sprintf("crop=w='min(iw,ih*9/16)':h='min(ih,iw*16/9)':x='max(0,min(iw-ow,iw*%.4f-ow/2))':y='max(0,min(ih-oh,ih*%.4f-oh*0.35))',scale=540:960,setsar=1", c[0], c[1])
			graph := strings.ReplaceAll(strings.ReplaceAll(shot.graph, "LAND", land), "PORT", port)
			if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-i", ph,
				"-filter_complex", graph, "-frames:v", "1", "-q:v", "3", filepath.Join(dir, filepath.Base(ph))}}); err != nil {
				t.Fatal(err)
			}
		}
		appleFaces := map[string][]vision.Face{}
		if apple {
			if err := vision.Analyze(context.Background(), ts, dir, vision.Options{Text: "off"}, func(f vision.Frame) { appleFaces[f.File] = f.Faces }); err != nil {
				t.Fatal(err)
			}
		}
		var frames, fills, wrongFills, before, after, copyBefore, copyAfter, appleN, appleDropped int
		frames = len(photos)
		for _, ph := range photos {
			name := filepath.Base(ph)
			p := readPicture(t, filepath.Join(dir, name))
			pic, isFill := sharpPicture(p)
			if isFill {
				fills++
				if shot.picture == nil {
					wrongFills++
				}
			}
			af := appleFaces[name]
			appleN += len(af)
			found, err := e.faces.find(p)
			if err != nil {
				t.Fatal(err)
			}
			for _, fa := range found {
				if fa.score < FaceMin || featureless(fa.score, alignedSharpness(p, ordered(fa.pts))) {
					continue
				}
				before++
				drop := isFill && outsidePicture(fa.box, pic)
				if !drop {
					after++
				}
				fb := toFace(fa, p.w, p.h, 0).Box
				matched := false
				for _, a := range af {
					if boxOverlap(fb, a.Box) >= 0.3 {
						matched = true
					}
				}
				inCopy := shot.picture != nil && outsidePicture(fa.box, *shot.picture)
				switch {
				case matched && drop && !inCopy:
					appleDropped++
				case !matched && inCopy:
					copyBefore++
					if !drop {
						copyAfter++
					}
				}
			}
		}
		t.Logf("%-32s %3d frames, found as fills %3d (wrongly %d); open faces %3d → %3d with the rule; in the blurred copy and not Apple Vision's %3d → %3d; Apple Vision's faces %3d, of them dropped %d",
			shot.name, frames, fills, wrongFills, before, after, copyBefore, copyAfter, appleN, appleDropped)
	}
}

func readPicture(t *testing.T, path string) *picture {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return toPicture(img)
}

// boxOverlap is the IoU of two boxes (0–1 of the frame).
func boxOverlap(a, b vision.Box) float64 {
	ix := math.Max(0, math.Min(a[0]+a[2], b[0]+b[2])-math.Max(a[0], b[0]))
	iy := math.Max(0, math.Min(a[1]+a[3], b[1]+b[3])-math.Max(a[1], b[1]))
	in := ix * iy
	u := a[2]*a[3] + b[2]*b[3] - in
	if u <= 0 {
		return 0
	}
	return in / u
}

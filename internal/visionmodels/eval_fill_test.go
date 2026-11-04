//go:build screeneval

package visionmodels

// The measurement of fills (D445, D465): the faceset's photos made into the
// shots editors make — a landscape shot made vertical (9:16), square or 4:5,
// a portrait shot made landscape — over a blurred, enlarged copy of
// themselves; the same with something sharp laid over the copy (a face cam in
// the top or bottom band, MetaRush's vertical picture in picture with the
// inset at the top left) and a plain vertical picture in picture; and three
// sets that aren't fills (the photos as they are, their landscape crops, and
// those blurred all over). For each: how many faces the open models find in
// the copy that Apple Vision doesn't, with the fill rule and without it, and
// whether the rule drops any face Apple Vision found (in the picture, or the
// inset's). Opt-in, as TestCompareBackends:
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
// (a photo's crop around its main face goes where LAND or PORT stands; a
// second photo's, for an inset, where INSET_PORT, CAM110 or CAM160 stands),
// for a fill where its sharp picture is, and where an inset is (pixels).
type faceSetShot struct {
	name    string
	graph   string
	picture *[4]float64
	inset   *[4]float64
}

func faceSetShots() []faceSetShot {
	fill := func(w, h int, kind, fg string, sigma float64) string {
		return fmt.Sprintf("[0:v]%s,split[a][b];[a]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,gblur=sigma=%g[bg];[b]%s[fg];[bg][fg]overlay=(W-w)/2:(H-h)/2",
			kind, w, h, w, h, sigma, fg)
	}
	v916 := &[4]float64{0, 328, 540, 304}
	pip := "[1:v]INSET_PORT,scale=180:320,pad=188:328:4:4:color=white[c];[f][c]overlay=24:24"
	return []faceSetShot{
		{"9:16 fill, blur 8", fill(540, 960, "LAND", "scale=540:-2", 8), v916, nil},
		{"9:16 fill, blur 16", fill(540, 960, "LAND", "scale=540:-2", 16), v916, nil},
		{"1:1 fill, blur 8", fill(720, 720, "LAND", "scale=720:-2", 8), &[4]float64{0, 157, 720, 406}, nil},
		{"4:5 fill, blur 8", fill(576, 720, "LAND", "scale=576:-2", 8), &[4]float64{0, 198, 576, 324}, nil},
		{"portrait made landscape, blur 8", fill(960, 540, "PORT", "scale=-2:540", 8), &[4]float64{328, 0, 304, 540}, nil},
		{"9:16 fill + 110 px face cam, top", fill(540, 960, "LAND", "scale=540:-2", 16) + "[f];[1:v]CAM110[c];[f][c]overlay=30:120", v916, &[4]float64{30, 120, 110, 110}},
		{"9:16 fill + 160 px face cam, bottom", fill(540, 960, "LAND", "scale=540:-2", 16) + "[f];[1:v]CAM160[c];[f][c]overlay=W-w-30:700", v916, &[4]float64{350, 700, 160, 160}},
		{"vertical picture in picture over a fill", fill(540, 960, "LAND", "scale=540:-2", 16) + "[f];" + pip, v916, &[4]float64{24, 24, 188, 328}},
		{"vertical picture in picture", "[0:v]PORT[f];" + pip, nil, &[4]float64{24, 24, 188, 328}},
		{"as they are", "[0:v]scale='min(960,iw)':-2", nil, nil},
		{"landscape crops", "[0:v]LAND", nil, nil},
		{"blurred all over (4)", "[0:v]scale='min(960,iw)':-2,gblur=sigma=4", nil, nil},
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
	land := func(c [2]float64) string {
		return fmt.Sprintf("crop=w='min(iw,ih*16/9)':h='min(ih,iw*9/16)':x='max(0,min(iw-ow,iw*%.4f-ow/2))':y='max(0,min(ih-oh,ih*%.4f-oh*0.4))',scale=960:540,setsar=1", c[0], c[1])
	}
	port := func(c [2]float64) string {
		return fmt.Sprintf("crop=w='min(iw,ih*9/16)':h='min(ih,iw*16/9)':x='max(0,min(iw-ow,iw*%.4f-ow/2))':y='max(0,min(ih-oh,ih*%.4f-oh*0.35))',scale=540:960,setsar=1", c[0], c[1])
	}
	cam := func(c [2]float64, side int) string { // a head-and-shoulders square, as a face cam shows
		return fmt.Sprintf("crop=w='min(iw,ih)*0.5':h='min(iw,ih)*0.5':x='max(0,min(iw-ow,iw*%.4f-ow/2))':y='max(0,min(ih-oh,ih*%.4f-oh*0.45))',scale=%d:%d,setsar=1", c[0], c[1], side, side)
	}
	for _, shot := range faceSetShots() {
		dir := t.TempDir()
		for i, ph := range photos {
			// The inset shows another photo: seven on, so mostly someone else.
			other := photos[(i+7)%len(photos)]
			c, o := centres[ph], centres[other]
			graph := strings.NewReplacer("INSET_PORT", port(o), "CAM110", cam(o, 110), "CAM160", cam(o, 160), "LAND", land(c), "PORT", port(c)).Replace(shot.graph)
			args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-i", ph}
			if strings.Contains(graph, "[1:v]") {
				args = append(args, "-i", other)
			}
			args = append(args, "-filter_complex", graph, "-frames:v", "1", "-q:v", "3", filepath.Join(dir, filepath.Base(ph)))
			if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: args}); err != nil {
				t.Fatal(err)
			}
		}
		appleFaces := map[string][]vision.Face{}
		if apple {
			if err := vision.Analyze(context.Background(), ts, dir, vision.Options{Text: "off"}, func(f vision.Frame) { appleFaces[f.File] = f.Faces }); err != nil {
				t.Fatal(err)
			}
		}
		var frames, fills, wrongFills, before, after, copyBefore, copyAfter, appleN, appleDropped, insetN, insetDropped, outsideDropped int
		frames = len(photos)
		for _, ph := range photos {
			name := filepath.Base(ph)
			p := readPicture(t, filepath.Join(dir, name))
			fl, isFill := findFill(p)
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
				drop := isFill && fl.inCopy(fa.box)
				if !drop {
					after++
				}
				// Without D465's look at the face itself, every face outside
				// the picture went.
				outside := isFill && outsidePicture(fa.box, fl.pic)
				fb := toFace(fa, p.w, p.h, 0).Box
				matched := false
				for _, a := range af {
					if boxOverlap(fb, a.Box) >= 0.3 {
						matched = true
					}
				}
				inInset := shot.inset != nil && centreIn(fa.box, *shot.inset)
				inCopy := shot.picture != nil && outsidePicture(fa.box, *shot.picture) && !inInset
				switch {
				case matched && !inCopy:
					if inInset {
						insetN++
					}
					if drop {
						appleDropped++
						if inInset {
							insetDropped++
						}
					}
					if outside {
						outsideDropped++
					}
				case !matched && inCopy:
					copyBefore++
					if !drop {
						copyAfter++
					}
				}
			}
		}
		t.Logf("%-40s %3d frames, found as fills %3d (wrongly %d); open faces %3d → %3d with the rule; in the blurred copy and not Apple Vision's %3d → %3d; Apple Vision's faces %3d (the inset's %3d), of them dropped %d (the inset's %d; dropping every face outside the picture: %d)",
			shot.name, frames, fills, wrongFills, before, after, copyBefore, copyAfter, appleN, insetN, appleDropped, insetDropped, outsideDropped)
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

// centreIn says a box's middle is inside r (pixels).
func centreIn(b, r [4]float64) bool {
	cx, cy := b[0]+b[2]/2, b[1]+b[3]/2
	return cx >= r[0] && cx <= r[0]+r[2] && cy >= r[1] && cy <= r[1]+r[3]
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

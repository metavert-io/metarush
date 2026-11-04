//go:build screeneval

package visionmodels_test

// The measurement behind the open models (D412): Apple Vision and the open
// models look at the same frames, the fixtures' and the call's, and every
// number docs/visual-index.md quotes is printed here. Opt-in and never in CI:
// it needs the pinned models (go run ./scripts/fetchscreenmodels <folder>),
// onnxruntime, the face model, the generated fixtures (scripts/make-fixtures.sh,
// scripts/make-call-fixtures.sh) and, for the comparison, a Mac.
//
//	go run ./scripts/fetchscreenmodels <folder>
//	METARUSH_SCREEN_MODELS=<folder> go test -tags screeneval -run TestCompareBackends -v -timeout 60m ./internal/visionmodels/
//
// EVAL_FACESET=<folder of the faceset's photos> (tools/vision/eval/faces.py
// --folder keeps them) adds the faceset's portraits to the face comparison.

import (
	"context"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/looker"
	"github.com/metavert-io/metarush/internal/onnx/onnxtest"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visionmodels"
	"github.com/metavert-io/metarush/internal/visual"
)

// fill is a way editors fit a landscape shot to another shape: the shot in
// the middle at the frame's width, over a copy of it enlarged to cover the
// frame and blurred (sigma in the frame's pixels).
type fill struct {
	name  string
	w, h  int
	sigma float64
}

var fills = []fill{{"9:16", 540, 960, 20}, {"1:1", 720, 720, 16}, {"4:5", 576, 720, 16}}

// inCopy says a face (0–1 of the frame) has its middle outside the shot, in
// the blurred copy (the shot is 16:9 at the frame's width).
func (f fill) inCopy(b vision.Box) bool {
	ph := float64(f.w) * 9 / 16 / float64(f.h)
	cy := b[1] + b[3]/2
	return cy < (1-ph)/2 || cy > (1+ph)/2
}

// fillVideo makes the fill of a video.
func fillVideo(t *testing.T, ts *tools.Set, src string, f fill) string {
	t.Helper()
	out := filepath.Join(t.TempDir(), "fill.mp4")
	graph := fmt.Sprintf("[0:v]split[a][b];[a]scale=%d:%d:force_original_aspect_ratio=increase,crop=%d:%d,gblur=sigma=%g[bg];[b]scale=%d:-2[fg];[bg][fg]overlay=(W-w)/2:(H-h)/2",
		f.w, f.h, f.w, f.h, f.sigma, f.w)
	if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-i", src,
		"-filter_complex", graph, "-an", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "18", out}}); err != nil {
		t.Fatal(err)
	}
	return out
}

func fixture(t *testing.T, rel string) string {
	t.Helper()
	p := filepath.Join("..", "..", "tests", "fixtures", rel)
	if _, err := os.Stat(p); err != nil {
		t.Skipf("%s isn't here (scripts/make-fixtures.sh, scripts/make-call-fixtures.sh)", rel)
	}
	abs, _ := filepath.Abs(p)
	return abs
}

// lookers are the two ways of looking, ready for the measurement.
type lookers struct {
	apple  looker.Looker // nil off a Mac
	open   *visionmodels.Engine
	openLk looker.Looker
	ts     *tools.Set
	prints visual.Printer
	store  visionmodels.Store
}

func setUp(t *testing.T) *lookers {
	t.Helper()
	dir := os.Getenv("METARUSH_SCREEN_MODELS")
	if dir == "" {
		t.Skip("set METARUSH_SCREEN_MODELS to a folder with the pinned models (go run ./scripts/fetchscreenmodels <folder>)")
	}
	lib := onnxtest.Library(t)
	store := visionmodels.Store{Dir: dir}
	if err := store.Ready(visionmodels.PartIndex); err != nil {
		t.Skip(err)
	}
	e, err := visionmodels.OpenEngine(lib, store.IndexPaths())
	if err != nil {
		t.Fatal(err)
	}
	ts := tools.New(config.Tools{Vision: os.Getenv("METARUSH_VISION")})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg isn't here")
	}
	l := &lookers{open: e, openLk: looker.ForTest(looker.OpenModels, looker.SeesOpen, e.Analyze), ts: ts, store: store}
	if runtime.GOOS == "darwin" {
		if !ts.Has(tools.Vision) {
			home := t.TempDir()
			if _, err := vision.Build(context.Background(), ts, home); err != nil {
				t.Logf("no Apple Vision helper (%v): the open models are measured alone", err)
			}
		}
		if vision.Rich(context.Background(), ts) {
			l.apple = looker.ForTest(looker.AppleVision, nil, func(ctx context.Context, dir string, o vision.Options, each func(vision.Frame)) error {
				return vision.Analyze(ctx, ts, dir, o, each)
			})
		}
	}
	model := os.Getenv("METARUSH_FACE_MODEL")
	if model == "" {
		model = filepath.Join(os.TempDir(), "metarush-faceid-test-cache", faceid.ModelFile)
	}
	if _, err := os.Stat(model); err == nil {
		if p, err := faceid.Open(lib, model); err == nil {
			l.prints = p
		}
	}
	return l
}

// framesOf takes a video's frames at 1 fps (960 px wide, as the index does).
func framesOf(t *testing.T, ts *tools.Set, src string, every float64) string {
	t.Helper()
	dir := t.TempDir()
	scale := "scale='if(gt(iw,ih),960,-2)':'if(gt(iw,ih),-2,960)'"
	if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-i", src,
		"-vf", fmt.Sprintf("fps=1/%g:round=up,%s", every, scale), "-q:v", "3", filepath.Join(dir, "f%05d.jpg")}}); err != nil {
		t.Fatal(err)
	}
	return dir
}

type faceStats struct {
	frames, appleFaces, openFaces, matched, appleOnly, openOnly int
	ious, lmErr, sideRatio, centreDY, centreDX                  []float64
	qApple, qOpen                                               []float64
}

func boxIoU(a, b vision.Box, w, h float64) float64 {
	ax0, ay0, ax1, ay1 := a[0]*w, a[1]*h, (a[0]+a[2])*w, (a[1]+a[3])*h
	bx0, by0, bx1, by1 := b[0]*w, b[1]*h, (b[0]+b[2])*w, (b[1]+b[3])*h
	ix := math.Max(0, math.Min(ax1, bx1)-math.Max(ax0, bx0))
	iy := math.Max(0, math.Min(ay1, by1)-math.Max(ay0, by0))
	inter := ix * iy
	u := (ax1-ax0)*(ay1-ay0) + (bx1-bx0)*(by1-by0) - inter
	if u <= 0 {
		return 0
	}
	return inter / u
}

// compareFaces matches each frame's faces (by IoU, best first) and adds up
// how alike the two ways' faces are.
func (s *faceStats) compare(a, o vision.Frame) {
	s.frames++
	w, h := float64(a.W), float64(a.H)
	var af, of []vision.Face
	for _, f := range a.Faces {
		if f.Conf >= 0.5 {
			af = append(af, f)
		}
	}
	of = o.Faces
	s.appleFaces += len(af)
	s.openFaces += len(of)
	type pair struct {
		i, j int
		iou  float64
	}
	var ps []pair
	for i := range af {
		for j := range of {
			if v := boxIoU(af[i].Box, of[j].Box, w, h); v >= 0.2 {
				ps = append(ps, pair{i, j, v})
			}
		}
	}
	sort.Slice(ps, func(x, y int) bool { return ps[x].iou > ps[y].iou })
	ua, uo := map[int]bool{}, map[int]bool{}
	for _, p := range ps {
		if ua[p.i] || uo[p.j] {
			continue
		}
		ua[p.i], uo[p.j] = true, true
		s.matched++
		s.ious = append(s.ious, p.iou)
		fa, fo := af[p.i], of[p.j]
		sa, so := fa.Box[3]*h, fo.Box[3]*h
		s.sideRatio = append(s.sideRatio, sa/so)
		s.centreDY = append(s.centreDY, ((fa.Box[1]+fa.Box[3]/2)-(fo.Box[1]+fo.Box[3]/2))*h/so)
		s.centreDX = append(s.centreDX, ((fa.Box[0]+fa.Box[2]/2)-(fo.Box[0]+fo.Box[2]/2))*w/so)
		if len(fa.LM) == 5 && len(fo.LM) == 5 {
			eyes := math.Hypot((fa.LM[1][0]-fa.LM[0][0])*w, (fa.LM[1][1]-fa.LM[0][1])*h)
			if eyes > 0 {
				d := 0.0
				for k := 0; k < 5; k++ {
					d += math.Hypot((fa.LM[k][0]-fo.LM[k][0])*w, (fa.LM[k][1]-fo.LM[k][1])*h)
				}
				s.lmErr = append(s.lmErr, d/5/eyes)
			}
		}
		if fa.Quality != nil && fo.Quality != nil {
			s.qApple, s.qOpen = append(s.qApple, *fa.Quality), append(s.qOpen, *fo.Quality)
		}
	}
	s.appleOnly += len(af) - len(ua)
	s.openOnly += len(of) - len(uo)
}

func mean(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	s := 0.0
	for _, x := range v {
		s += x
	}
	return s / float64(len(v))
}

func median(v []float64) float64 {
	if len(v) == 0 {
		return math.NaN()
	}
	c := append([]float64(nil), v...)
	sort.Float64s(c)
	return c[len(c)/2]
}

func pearson(a, b []float64) float64 {
	ma, mb := mean(a), mean(b)
	var sab, saa, sbb float64
	for i := range a {
		sab += (a[i] - ma) * (b[i] - mb)
		saa += (a[i] - ma) * (a[i] - ma)
		sbb += (b[i] - mb) * (b[i] - mb)
	}
	return sab / math.Sqrt(saa*sbb)
}

func below(v []float64, x float64) float64 {
	n := 0
	for _, y := range v {
		if y < x {
			n++
		}
	}
	return float64(n) / float64(max(1, len(v)))
}

// lookAll runs a looker over a folder.
func lookAll(t *testing.T, lk looker.Looker, dir string, o vision.Options) (map[string]vision.Frame, time.Duration) {
	t.Helper()
	out := map[string]vision.Frame{}
	start := time.Now()
	if err := lk.Analyze(context.Background(), dir, o, func(f vision.Frame) { out[f.File] = f }); err != nil {
		t.Fatal(err)
	}
	return out, time.Since(start)
}

// TestCompareBackends is the measurement (D412): faces, their points and
// boxes, how usable each face is, the words on screen, the shot types and
// recognition across clips, Apple Vision against the open models.
func TestCompareBackends(t *testing.T) {
	l := setUp(t)
	videos := map[string]string{
		"visual-a":    fixture(t, "gen/visual-a.mp4"),
		"visual-b":    fixture(t, "gen/visual-b.mp4"),
		"call-tiles":  fixture(t, "gen/call-speaker-view.mp4"),
		"garden-call": fixture(t, "gen/call/garden-call.mp4"),
	}
	var all faceStats
	var appleTime, openTime time.Duration
	nFrames := 0
	if l.apple != nil {
		for _, name := range []string{"visual-a", "visual-b", "call-tiles", "garden-call"} {
			dir := framesOf(t, l.ts, videos[name], 1)
			a, ta := lookAll(t, l.apple, dir, vision.Options{AnyLanguage: true, Landmarks: true, Attention: true})
			o, to := lookAll(t, l.openLk, dir, vision.Options{AnyLanguage: true, Landmarks: true, Attention: true})
			appleTime, openTime, nFrames = appleTime+ta, openTime+to, nFrames+len(a)
			var s faceStats
			for f, af := range a {
				s.compare(af, o[f])
				all.compare(af, o[f])
			}
			t.Logf("%s: %d frames; faces Apple %d, open %d, both %d (Apple only %d, open only %d); box IoU median %.2f; landmarks off by %.3f of the eye distance",
				name, s.frames, s.appleFaces, s.openFaces, s.matched, s.appleOnly, s.openOnly, median(s.ious), mean(s.lmErr))
		}
		if dir := os.Getenv("EVAL_FACESET"); dir != "" {
			a, _ := lookAll(t, l.apple, dir, vision.Options{Text: "off", Landmarks: true})
			o, _ := lookAll(t, l.openLk, dir, vision.Options{Text: "off", Landmarks: true})
			var s faceStats
			for f, af := range a {
				s.compare(af, o[f])
				all.compare(af, o[f])
			}
			t.Logf("faceset: %d photos; faces Apple %d, open %d, both %d (Apple only %d, open only %d); box IoU median %.2f; landmarks off by %.3f of the eye distance",
				s.frames, s.appleFaces, s.openFaces, s.matched, s.appleOnly, s.openOnly, median(s.ious), mean(s.lmErr))
		}
		t.Logf("ALL FACES: %d frames; Apple %d faces, open %d, both %d: the open models found %.1f%% of Apple Vision's faces, %d more of their own",
			all.frames, all.appleFaces, all.openFaces, all.matched, 100*float64(all.matched)/float64(max(1, all.appleFaces)), all.openOnly)
		t.Logf("BOXES: IoU mean %.3f median %.3f; Apple's side / the open box's side: median %.3f (mean %.3f); centre lower by %.3f, across by %.3f of the side (medians)",
			mean(all.ious), median(all.ious), median(all.sideRatio), mean(all.sideRatio), median(all.centreDY), median(all.centreDX))
		t.Logf("LANDMARKS: mean error %.3f of the eye distance (median %.3f) over %d faces", mean(all.lmErr), median(all.lmErr), len(all.lmErr))
		t.Logf("QUALITY: correlation %.2f; mean Apple %.3f, open %.3f; under 0.15 (no fingerprint): Apple %.1f%%, open %.1f%%",
			pearson(all.qApple, all.qOpen), mean(all.qApple), mean(all.qOpen), 100*below(all.qApple, 0.15), 100*below(all.qOpen, 0.15))
		t.Logf("TIME: Apple Vision %.0f ms a frame, the open models %.0f ms a frame (%d frames; this computer's load: see uptime)",
			float64(appleTime.Milliseconds())/float64(nFrames), float64(openTime.Milliseconds())/float64(nFrames), nFrames)

		// Fills (D445): visual-b's talking head and the garden call made
		// vertical, square and 4:5 over a blurred, enlarged copy of themselves,
		// as editors do. Faces in the copy are counted apart from the picture's.
		for _, k := range []struct {
			video string
			dur   float64
			cuts  []float64
			f     fill
		}{
			{"visual-b", 24, []float64{8, 14, 20}, fills[0]}, {"visual-b", 24, []float64{8, 14, 20}, fills[1]}, {"visual-b", 24, []float64{8, 14, 20}, fills[2]},
			{"garden-call", 71.5, nil, fills[0]}, {"garden-call", 71.5, nil, fills[1]}, {"garden-call", 71.5, nil, fills[2]},
		} {
			f := k.f
			src := fillVideo(t, l.ts, videos[k.video], f)
			dir := framesOf(t, l.ts, src, 1)
			a, _ := lookAll(t, l.apple, dir, vision.Options{Text: "off", Landmarks: true})
			o, _ := lookAll(t, l.openLk, dir, vision.Options{Text: "off", Landmarks: true})
			var s faceStats
			inCopy := map[string]int{}
			for name, af := range a {
				s.compare(af, o[name])
				for way, fr := range map[string]vision.Frame{"apple": af, "open": o[name]} {
					for _, fc := range fr.Faces {
						if fc.Conf >= 0.5 && f.inCopy(fc.Box) {
							inCopy[way]++
						}
					}
				}
			}
			t.Logf("FILL %s %s: %d frames; faces Apple %d, open %d, both %d (Apple only %d, open only %d); in the blurred copy: Apple %d, open %d",
				k.video, f.name, s.frames, s.appleFaces, s.openFaces, s.matched, s.appleOnly, s.openOnly, inCopy["apple"], inCopy["open"])
			// End to end: what the index says, and where following movement looks.
			for _, way := range []string{"apple", "open"} {
				lk := map[string]looker.Looker{"apple": l.apple, "open": l.openLk}[way]
				ix, err := visual.Run(context.Background(), l.ts, src, k.dur, nil, k.cuts, "original", t.TempDir(), nil, visual.RunOptions{Looker: lk})
				if err != nil {
					t.Fatal(err)
				}
				outside, all := 0, 0
				for _, fs := range ix.Faces {
					for _, b := range fs.Boxes {
						all++
						if f.inCopy(b) {
							outside++
						}
					}
				}
				t.Logf("FILL %s %s indexed with %s: %s; faces in the blurred copy %d of %d", k.video, f.name, way, ix.Summary, outside, all)
			}
		}
	}

	// The index each way, end to end: the words on screen, shot types,
	// following, and the people recognized across the two recordings.
	type run struct {
		cuts  []float64
		extra []float64
		dur   float64
	}
	runs := map[string]run{
		"visual-a":    {cuts: []float64{6, 14, 21, 25}, extra: []float64{6.4, 9.7, 13.3, 14.4, 17.3, 21.4, 25.4}, dur: 30},
		"visual-b":    {cuts: []float64{8, 14, 20}, dur: 24},
		"call-tiles":  {cuts: []float64{6, 12, 18}, dur: 24},
		"garden-call": {dur: 71.5},
	}
	idx := map[string]map[string]*visual.Index{"apple": {}, "open": {}}
	for name, r := range runs {
		for _, way := range []string{"apple", "open"} {
			lk := l.openLk
			if way == "apple" {
				if l.apple == nil {
					continue
				}
				lk = l.apple
			}
			start := time.Now()
			ix, err := visual.Run(context.Background(), l.ts, videos[name], r.dur, r.extra, r.cuts, "original", t.TempDir(), nil, visual.RunOptions{Prints: l.prints, Looker: lk})
			if err != nil {
				t.Fatalf("%s %s: %v", name, way, err)
			}
			idx[way][name] = ix
			t.Logf("%s with %s: %d frames in %.1f s; %s", name, way, ix.Frames, time.Since(start).Seconds(), ix.Summary)
		}
	}
	// The words on screen the fixtures show (their truth files), read each way.
	truth := []struct {
		clip     string
		text     string
		from, to float64
	}{
		{"visual-a", "EXIT", 21, 25}, {"visual-b", "SALE ENDS SUNDAY", 8, 14},
		{"call-tiles", "LENA O", 0, 6}, {"call-tiles", "MAYA L", 6, 12}, {"call-tiles", "LENA O", 12, 18}, {"call-tiles", "MAYA L", 18, 24},
		{"garden-call", "Garden club weekly call", 0, 2},
		{"garden-call", "Ada Park", 2, 7.6}, {"garden-call", "Ben Okafor", 7.7, 13}, {"garden-call", "Ada Park", 13.1, 17}, {"garden-call", "Ben Okafor", 17.1, 22.3},
		{"garden-call", "Cleo Marsh", 22.4, 26.4}, {"garden-call", "Ada Park", 38.7, 48}, {"garden-call", "Ben Okafor", 38.7, 48}, {"garden-call", "Cleo Marsh", 38.7, 48},
	}
	for _, way := range []string{"apple", "open"} {
		if len(idx[way]) == 0 {
			continue
		}
		words, found := 0, 0
		var missed []string
		for _, tr := range truth {
			ix := idx[way][tr.clip]
			for _, w := range visual.Words(tr.text) {
				words++
				ok := false
				for _, sp := range ix.Text {
					if sp.To > tr.from && sp.From < tr.to {
						for _, x := range visual.Words(sp.Text) {
							if x == w {
								ok = true
							}
						}
					}
				}
				if ok {
					found++
				} else {
					missed = append(missed, fmt.Sprintf("%s %q at %.0f s", tr.clip, w, tr.from))
				}
			}
		}
		t.Logf("WORDS ON SCREEN (%s): %d of %d words read (%.0f%%); missed %v", way, found, words, 100*float64(found)/float64(words), missed)
	}
	// Shot types, every half second, where both looked.
	if len(idx["apple"]) > 0 {
		agree, n := 0, 0
		var differ []string
		for name, r := range runs {
			for at := 0.25; at < r.dur; at += 0.5 {
				a, o := shotAt(idx["apple"][name], at), shotAt(idx["open"][name], at)
				if a == "" || o == "" {
					continue
				}
				n++
				if a == o {
					agree++
				} else if len(differ) < 12 {
					differ = append(differ, fmt.Sprintf("%s %.2f s: %s / %s", name, at, a, o))
				}
			}
		}
		t.Logf("SHOT TYPES: agree at %d of %d moments (%.1f%%); differ %v", agree, n, 100*float64(agree)/float64(max(1, n)), differ)
	}
	// Recognition through SFace, from the open models' points: the same two
	// people in two recordings, and someone else.
	if l.prints != nil {
		for _, way := range []string{"apple", "open"} {
			ia, ib := idx[way]["visual-a"], idx[way]["visual-b"]
			if ia == nil || ib == nil {
				continue
			}
			vec := func(tr *visual.Track) []float32 {
				var vs [][]float32
				var ws []float64
				for _, p := range tr.Prints {
					vs, ws = append(vs, p.Vec), append(ws, p.Quality)
				}
				return faceid.Mean(vs, ws)
			}
			at := func(ix *visual.Index, s float64) *visual.Track {
				for i := range ix.Tracks {
					if ix.Tracks[i].From <= s && s < ix.Tracks[i].To && len(ix.Tracks[i].Prints) > 0 {
						return &ix.Tracks[i]
					}
				}
				return nil
			}
			head, walkIn := at(ia, 3), at(ia, 19)
			walking, edge, low, stranger := at(ib, 4), at(ib, 11), at(ib, 17), at(ib, 22)
			if head == nil || walkIn == nil || walking == nil || edge == nil || low == nil || stranger == nil {
				t.Errorf("%s: a face without fingerprints: a %+v b %+v", way, ia.Tracks, ib.Tracks)
				continue
			}
			th := faceid.DefaultThresholds
			same := []float64{faceid.Cosine(vec(head), vec(walking)), faceid.Cosine(vec(head), vec(low)), faceid.Cosine(vec(walkIn), vec(edge))}
			diff := []float64{faceid.Cosine(vec(head), vec(stranger)), faceid.Cosine(vec(walkIn), vec(stranger)), faceid.Cosine(vec(head), vec(edge)), faceid.Cosine(vec(walkIn), vec(walking))}
			t.Logf("RECOGNITION (%s): the same people %.2f %.2f %.2f (named automatically from %.2f with one person known); others %.2f %.2f %.2f %.2f (asked about from %.2f)",
				way, same[0], same[1], same[2], th.FewAuto, diff[0], diff[1], diff[2], diff[3], th.Suggest)
			for _, v := range same {
				if v < th.FewAuto {
					t.Errorf("%s: the same person at %.3f, under %.2f", way, v, th.FewAuto)
				}
			}
			for _, v := range diff {
				if v >= th.Suggest {
					t.Errorf("%s: different people at %.3f, would be asked about", way, v)
				}
			}
		}
	}
}

func shotAt(ix *visual.Index, at float64) string {
	if ix == nil {
		return ""
	}
	for _, s := range ix.Shots {
		if at >= s.From && at < s.To {
			return s.Type
		}
	}
	return ""
}

// TestCompareCutOuts measures BiRefNet's cut-outs against Apple Vision's
// subject lifting on the fixtures' pictures (D414): how much of the subject
// they agree on, and the time a picture takes.
func TestCompareCutOuts(t *testing.T) {
	l := setUp(t)
	if err := l.store.Ready(visionmodels.PartCutOut); err != nil {
		t.Skip(err)
	}
	m, err := visionmodels.OpenMasker(onnxtest.Library(t), l.store.CutOutPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"portrait.jpg", "person.jpg", "dog.jpg", "beach.jpg", "portrait-b.jpg", "person-b.jpg", "portrait-c.jpg"} {
		in := fixture(t, "images/"+name)
		out := filepath.Join(t.TempDir(), "open.png")
		start := time.Now()
		found, err := m.CutOut(in, out)
		if err != nil {
			t.Fatal(err)
		}
		took := time.Since(start)
		line := fmt.Sprintf("%s: the open model found a subject: %v (%.1f s)", name, found, took.Seconds())
		if runtime.GOOS == "darwin" && l.ts.Has(tools.Vision) {
			ap := filepath.Join(t.TempDir(), "apple.png")
			af, err := vision.Mask(context.Background(), l.ts, in, ap)
			if err == nil {
				line += fmt.Sprintf("; Apple Vision: %v", af)
				if af && found {
					line += fmt.Sprintf("; the subjects overlap %.3f (IoU)", maskIoU(t, ap, out))
				}
			}
		}
		t.Log(line)
	}
}

func maskIoU(t *testing.T, a, b string) float64 {
	t.Helper()
	read := func(p string) image.Image {
		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		img, _, err := image.Decode(f)
		if err != nil {
			t.Fatal(err)
		}
		return img
	}
	ia, ib := read(a), read(b)
	r := ia.Bounds()
	inter, union := 0, 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			_, _, _, aa := ia.At(x, y).RGBA()
			_, _, _, ab := ib.At(x-r.Min.X+ib.Bounds().Min.X, y-r.Min.Y+ib.Bounds().Min.Y).RGBA()
			ina, inb := aa > 0x8000, ab > 0x8000
			if ina && inb {
				inter++
			}
			if ina || inb {
				union++
			}
		}
	}
	return float64(inter) / float64(max(1, union))
}

var _ = strings.TrimSpace

package fixes

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/tools"
)

// The stretches of a call recording (D190): cut at every switch, slivers kept
// as they are, the last one running to the end.
func TestPlanStretches(t *testing.T) {
	got := planStretches([]int{90, 95, 200, 0, 200})
	want := []stretch{{0, 90, true}, {90, 95, false}, {95, 200, true}, {200, -1, true}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("stretches: %v, want %v", got, want)
	}
	// Two slivers in a row are one kept stretch.
	got = planStretches([]int{30, 33, 36})
	want = []stretch{{0, 30, true}, {30, 36, false}, {36, -1, true}}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("slivers: %v, want %v", got, want)
	}
	if got := planStretches(nil); fmt.Sprint(got) != fmt.Sprint([]stretch{{0, -1, true}}) {
		t.Fatalf("no switches: one stretch: %v", got)
	}
}

// stabilizerHere is vid.stab when this machine has it (the configured ffmpeg,
// or the pinned archive in METARUSH_STABILIZER_ZIP), else nil (the basic
// steadying is tested instead).
func stabilizerHere(t *testing.T, ts *tools.Set) *StabStatus {
	t.Helper()
	s := &Stabilizer{Home: t.TempDir(), Tools: ts}
	if zip := os.Getenv("METARUSH_STABILIZER_ZIP"); zip != "" {
		if _, err := os.Stat(zip); err != nil {
			return nil
		}
	}
	st, _ := s.Ensure(context.Background(), nil)
	if !st.Ready {
		return nil
	}
	return &st
}

// grayFrames decodes every frame of a file as small gray pictures.
func grayFrames(t *testing.T, path string) [][]byte {
	t.Helper()
	out, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-i", path, "-vf", "scale=160:90,format=gray", "-fps_mode", "passthrough", "-f", "rawvideo", "-").Output()
	if err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	const n = 160 * 90
	var fs [][]byte
	for len(out) >= n {
		fs = append(fs, out[:n])
		out = out[n:]
	}
	return fs
}

// A call recording's picture switches; steadying must never carry one
// picture's camera path into the next (D190). The fixture: two shaky pictures
// joined by a hard cut, in pairs that share one side of the switch and differ
// on the other (X = A|B, Y = A|C, W = A2|B). Steadied stretch by stretch, the
// shared side comes out the same whatever is on the other side of the switch —
// frame for frame, with the switch on its own frame and every frame kept —
// while steadying the whole recording at once lets the next picture change
// the frames before the switch (vid.stab smooths the path over a second on
// both sides).
func TestSteadyingStaysWithinEachPicture(t *testing.T) {
	ts := needFFmpeg(t)
	ctx := context.Background()
	dir := t.TempDir()
	// Three webcams that shake differently (and a fourth like the first, but
	// another moment of it), 3 s each at 30 fps.
	// Each webcam is a photograph (tests/fixtures/images) that a hand-held
	// camera shakes across.
	cam := func(name, photo, jitter string) string {
		return gen(t, dir, name, "-loop", "1", "-framerate", "30", "-t", "3", "-i", filepath.Join("..", "..", "tests", "fixtures", "images", photo),
			"-vf", "scale=720:420:force_original_aspect_ratio=increase,crop=720:420,crop=640:360:"+jitter, "-c:v", "libx264", "-qp", "0", "-preset", "ultrafast", "-pix_fmt", "yuv420p")
	}
	a := cam("a.mp4", "beach.jpg", "x='40+16*sin(n*1.7)*cos(n*0.6)':y='30+12*sin(n*2.1+1)'")
	a2 := cam("a2.mp4", "dog.jpg", "x='40+10*sin(n*1.3)':y='30+8*cos(n*1.9)'")
	b := cam("b.mp4", "portrait.jpg", "x='40+14*sin(n*2.3)':y='30+10*sin(n*1.1+2)'")
	c := cam("c.mp4", "person.jpg", "x='40+22*sin(n*0.9)*sin(n*2.9)':y='30+20*cos(n*1.4)'")
	join := func(name, first, second string) string {
		return gen(t, dir, name, "-i", first, "-i", second, "-filter_complex", "[0:v][1:v]concat=n=2:v=1:a=0[v]", "-map", "[v]", "-c:v", "libx264", "-qp", "0", "-preset", "ultrafast", "-pix_fmt", "yuv420p")
	}
	x, y, w := join("x.mp4", a, b), join("y.mp4", a, c), join("w.mp4", a2, b)
	const fps, cut, total = 30.0, 90, 180

	st := stabilizerHere(t, ts)
	run := stretchRun{keep: "null", tool: tools.FFmpeg, steady: func(string) string { return "deshake=rx=48:ry=48" }}
	engine := "deshake"
	if st != nil {
		engine = "vid.stab"
		run.tool = tools.Stabilizer
		run.detect = func(m string) string {
			return "vidstabdetect=shakiness=8:accuracy=15:stepsize=6:mincontrast=0.25:result=" + m
		}
		run.steady = func(m string) string {
			return "vidstabtransform=input=" + m + ":smoothing=30:optzoom=1:interpol=bicubic"
		}
	}
	enc := []string{"-c:v", "libx264", "-qp", "0", "-preset", "ultrafast", "-pix_fmt", "yuv420p"}
	steady := func(in string, parts []stretch) string {
		t.Helper()
		work := filepath.Join(dir, "work-"+filepath.Base(in)+fmt.Sprint(len(parts)))
		if err := os.MkdirAll(work, 0o755); err != nil {
			t.Fatal(err)
		}
		out := filepath.Join(work, "steady.mp4")
		n, err := steadyByStretch(ctx, ts, in, work, fps, total, parts, run, rawPix(enc), enc, out, func(float64, string) {})
		if err != nil {
			t.Fatalf("steady %s: %v", in, err)
		}
		if n != len(parts) {
			t.Fatalf("steadied %d of %d stretches", n, len(parts))
		}
		return out
	}
	parts := planStretches([]int{cut})
	xs, ys, ws := grayFrames(t, steady(x, parts)), grayFrames(t, steady(y, parts)), grayFrames(t, steady(w, parts))
	in := grayFrames(t, x)
	if len(xs) != total || len(ys) != total || len(ws) != total || len(in) != total {
		t.Fatalf("every frame kept: %d %d %d of %d", len(xs), len(ys), len(ws), total)
	}
	worst := func(p, q [][]byte, from, to int) float64 {
		m := 0.0
		for i := from; i < to; i++ {
			m = max(m, meanAbsDiff(p[i], q[i]))
		}
		return m
	}
	if d := worst(xs, ys, 0, cut); d > 0.5 {
		t.Fatalf("%s: the picture before the switch changed with what comes after it (worst frame %.2f levels apart)", engine, d)
	}
	if d := worst(xs, ws, cut, total); d > 0.5 {
		t.Fatalf("%s: the picture after the switch changed with what came before it (worst frame %.2f levels apart)", engine, d)
	}
	// The switch stays on its frame: the last frame before it shows A, the first after it B.
	if meanAbsDiff(xs[cut-1], in[cut-1]) >= meanAbsDiff(xs[cut-1], in[cut]) || meanAbsDiff(xs[cut], in[cut]) >= meanAbsDiff(xs[cut], in[cut-1]) {
		t.Fatalf("%s: the switch moved off its frame", engine)
	}
	// Steadied: less shake within each stretch than the original.
	before, err := ShakeBetween(ctx, ts, x, 6, fps, []float64{3})
	if err != nil {
		t.Fatal(err)
	}
	after, err := ShakeBetween(ctx, ts, filepath.Join(dir, "work-x.mp42", "steady.mp4"), 6, fps, []float64{3})
	if err != nil {
		t.Fatal(err)
	}
	if after >= before*0.7 {
		t.Fatalf("%s: shake %.3f%% → %.3f%%: not steadier", engine, before, after)
	}
	t.Logf("%s: shake %.3f%% → %.3f%% of the width a frame, measured between the switches", engine, before, after)

	// The whole recording at once: what follows the switch reaches back into
	// the picture before it (vid.stab), which is what the stretches prevent.
	if st != nil {
		whole := []stretch{{0, -1, true}}
		xw, yw := grayFrames(t, steady(x, whole)), grayFrames(t, steady(y, whole))
		d := worst(xw, yw, 0, cut)
		if d <= 0.5 {
			t.Fatalf("the fixture should show the problem: steadied as one, the frames before the switch differ by %.2f levels only", d)
		}
		t.Logf("steadied as one recording, the frames before the switch differ by up to %.1f levels with what follows it; by stretch, they don't", d)
	}
}

// The joined stream is exactly the stretches' frames: a kept stretch is the
// original, frame for frame (the accurate seek lands on the first frame of
// each stretch, never one early or late).
func TestStretchesKeepEveryFrameInPlace(t *testing.T) {
	ts := needFFmpeg(t)
	dir := t.TempDir()
	// Every frame different: a counter burnt into a moving test card.
	src := gen(t, dir, "count.mp4", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=4", "-c:v", "libx264", "-qp", "0", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-g", "50")
	parts := []stretch{{0, 17, false}, {17, 18, false}, {18, 61, false}, {61, -1, false}}
	enc := []string{"-c:v", "libx264", "-qp", "0", "-preset", "ultrafast", "-pix_fmt", "yuv420p"}
	out := filepath.Join(dir, "joined.mp4")
	if _, err := steadyByStretch(context.Background(), ts, src, dir, 25, 100, parts, stretchRun{keep: "null"}, rawPix(enc), enc, out, func(float64, string) {}); err != nil {
		t.Fatal(err)
	}
	a, b := grayFrames(t, src), grayFrames(t, out)
	if len(a) != 100 || len(b) != 100 {
		t.Fatalf("frames: %d and %d", len(a), len(b))
	}
	for i := range a {
		if !bytes.Equal(a[i], b[i]) {
			t.Fatalf("frame %d moved (%.2f levels apart)", i, meanAbsDiff(a[i], b[i]))
		}
	}
}

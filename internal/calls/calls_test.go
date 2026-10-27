package calls

import (
	"context"
	"math"
	"path/filepath"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// speakerView writes a 10 s, 30 fps clip whose picture switches like a call
// app's speaker view: person A, a two-frame flash of B at 3 s, A again, B from
// 6 s, A from 9 s (live pictures carry sensor noise, as webcams do).
func speakerView(t *testing.T, ts *tools.Set) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "call.mp4")
	b := "enable='between(n\\,90\\,91)+between(n\\,180\\,269)'"
	vf := "noise=alls=10:allf=t,drawbox=x=0:y=0:w=iw:h=ih:color=0xb06a3a:t=fill:" + b + ",drawbox=x=260:y=60:w=120:h=160:color=0xe8c4a0:t=fill,format=yuv420p"
	args := []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error", "-f", "lavfi", "-i", "color=c=0x2f4f7f:s=640x360:r=30:d=10", "-vf", vf,
		"-c:v", "libx264", "-preset", "veryfast", "-g", "30", "-pix_fmt", "yuv420p", p}
	if _, err := ts.Run(context.Background(), tools.Spec{Tool: tools.FFmpeg, Args: args}); err != nil {
		t.Fatal(err)
	}
	return p
}

func wantSwitches(t *testing.T, got []Switch, want []float64) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("switches %+v, want at %v", got, want)
	}
	for i, w := range want {
		if math.Abs(got[i].At-w) > 0.0015 {
			t.Fatalf("switch %d at %.4f, want %.4f (%+v)", i, got[i].At, w, got)
		}
		if math.Abs(got[i].At-got[i].Last-1.0/30) > 0.0015 {
			t.Fatalf("switch %d: last frame %.4f is not the frame before %.4f", i, got[i].Last, got[i].At)
		}
	}
}

func TestSwitchesAreFoundToTheFrame(t *testing.T) {
	ts := tools.New(config.Tools{})
	if !ts.Has(tools.FFmpeg) {
		t.Skip("ffmpeg not installed")
	}
	ctx := context.Background()
	p := speakerView(t, ts)
	want := []float64{3.0, 92.0 / 30, 6.0, 9.0}

	// From the evidence MetaRush already has (its full-rate scene changes).
	mi := &models.MediaInfo{Duration: 10, HasVideo: true}
	rep, err := evidence.Analyze(ctx, ts, p, "proxy", t.TempDir(), mi)
	if err != nil {
		t.Fatal(err)
	}
	ev := rep.Stored()
	sw, _, err := Detect(ctx, Candidates(&ev, nil), 10, 30, FFmpegDecoder(ts, p, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	wantSwitches(t, sw, want)

	// From rough candidates only (what the on-screen index's once-a-second
	// look gives): decoding around each finds the exact frames, the flash's
	// two switches included.
	rough := []candidate{{at: 2.7}, {at: 6.3}, {at: 8.8}}
	sw, n, err := Detect(ctx, rough, 10, 30, FFmpegDecoder(ts, p, t.TempDir()))
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("decoded %d stretches, want 3", n)
	}
	wantSwitches(t, sw, want)
	for _, s := range sw {
		if s.By != "refined" || s.Was == nil {
			t.Fatalf("a refined switch should say where it was: %+v", s)
		}
	}
}

func TestPeaksKeepBothSwitchesOfAFlash(t *testing.T) {
	fs := []frameScore{{0, 0}, {1, 0.01}, {2, 0.02}, {3, 0.8}, {4, 0.79}, {5, 0.01}, {6, 0.02}, {7, 0.01}}
	got := peaks(fs)
	if len(got) != 2 || got[0] != 3 || got[1] != 4 {
		t.Fatalf("peaks %v, want [3 4]", got)
	}
	// Steady movement is nobody's switch.
	if p := peaks([]frameScore{{0, 0}, {1, 0.08}, {2, 0.09}, {3, 0.1}, {4, 0.09}, {5, 0.08}}); len(p) != 0 {
		t.Fatalf("movement read as switches: %v", p)
	}
}

// A call recording's index: Lena's printed name bottom left while she is on
// screen, Maya's while she is, and a clock top right that keeps changing.
func callIndex() *visual.Index {
	box := func(x, y, w, h float64) vision.Box { return vision.Box{x, y, w, h} }
	return &visual.Index{Version: visual.Version, Duration: 30, Step: 1,
		Text: []visual.TextSpan{
			{Text: "Lena O.", From: 0.5, To: 9.5, Box: box(0.02, 0.92, 0.12, 0.04)},
			{Text: "Maya Lindqvist", From: 10.5, To: 15.5, Box: box(0.02, 0.92, 0.16, 0.04)},
			{Text: "Lena O.", From: 16.5, To: 29.5, Box: box(0.02, 0.92, 0.12, 0.04)},
			{Text: "10:42", From: 0.5, To: 29.5, Box: box(0.9, 0.03, 0.06, 0.03), Changing: true},
		},
		Faces: []visual.FaceSample{{At: 5, Boxes: []vision.Box{box(0.4, 0.2, 0.2, 0.4)}, Tracks: []int{1}}, {At: 12, Boxes: []vision.Box{box(0.4, 0.2, 0.2, 0.4)}, Tracks: []int{2}}},
	}
}

func TestWhoIsOnScreenAndThePrintedNames(t *testing.T) {
	st := &Stored{Duration: 30, Switches: []Switch{{At: 10, Last: 9.967}, {At: 16, Last: 15.967}, {At: 16.6, Last: 16.567}, {At: 17.1, Last: 17.067}}}
	idx := callIndex()
	people := []string{"Lena", "Maya Lindqvist"}
	segs := []editspec.TranscriptSegment{{From: 16.6, To: 17.0, Text: "Mm-hmm.", Speaker: "Maya Lindqvist"}}
	names := func(track int) (string, bool) {
		switch track {
		case 1:
			return "Lena", true
		case 2:
			return "Lena", false // recognized by looks only
		}
		return "", false
	}
	shots := Shots(st, idx, names, segs, people, 30)
	if len(shots) != 5 {
		t.Fatalf("shots %+v", shots)
	}
	if shots[0].Person != "Lena" || shots[0].By != "face" {
		t.Fatalf("first shot: %+v", shots[0])
	}
	// Nobody named the face in the second shot, and its look alone says
	// Lena: the printed name, matched to People, is surer.
	if shots[1].Person != "Maya Lindqvist" || shots[1].By != "printed" || shots[1].Printed != "Maya Lindqvist" {
		t.Fatalf("second shot: %+v", shots[1])
	}
	// A half-second flip: who talks wins over a printed name the index may have
	// read in the shot before.
	if shots[3].Person != "Maya Lindqvist" || shots[3].By != "voice" {
		t.Fatalf("the flip: %+v", shots[3])
	}
	pn := NameTiles(st, idx)
	if pn == nil || pn.Corner != "bottom-left" {
		t.Fatalf("printed names: %+v", pn)
	}
	// The area reaches the picture's left edge and covers every tile with room.
	if pn.Box.X != 0 || pn.Box.Y > 0.92-0.01 || pn.Box.Y+pn.Box.H < 0.96 || pn.Box.X+pn.Box.W < 0.18 {
		t.Fatalf("area to cover: %+v", pn.Box)
	}
	// A clip with only the clock has no printed names.
	idx.Text = idx.Text[3:]
	if NameTiles(st, idx) != nil {
		t.Fatal("a changing clock was read as printed names")
	}
	// Without a printed name, a face recognized by looks says who it is (unsure).
	idx.Text = nil
	if sh := Shots(st, idx, names, segs, people, 30); sh[1].Person != "Lena" || sh[1].By != "looks" {
		t.Fatalf("looks only: %+v", sh[1])
	}
}

func TestPrecision(t *testing.T) {
	cfr := &models.Asset{Media: models.MediaInfo{FPS: 30}}
	vfr := &models.Asset{Media: models.MediaInfo{FPS: 29.4, VFR: true}}
	if precision(cfr, "proxy", 30) != 0 || precision(cfr, "original", 30) != 0 {
		t.Fatal("a preview copy at the original's own rate is exact")
	}
	if p := precision(vfr, "proxy", 30); p < 0.033 || p > 0.034 {
		t.Fatalf("variable-rate footage: precision %v, want one frame", p)
	}
}

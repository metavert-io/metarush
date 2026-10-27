package evidence

import (
	"math"
	"math/rand"
	"testing"
)

// texture is a deterministic colorful pattern, w×h, sampled at (x+dx, y).
type texture struct {
	w, h int
	pix  []byte
}

func newTexture(w, h int, seed int64) *texture {
	r := rand.New(rand.NewSource(seed))
	t := &texture{w: w, h: h, pix: make([]byte, w*h*3)}
	// Smooth blobs of color: a few sine waves per channel.
	type wave struct{ fx, fy, ph, amp float64 }
	var waves [3][4]wave
	for c := range waves {
		for i := range waves[c] {
			waves[c][i] = wave{fx: r.Float64() * 0.25, fy: r.Float64() * 0.25, ph: r.Float64() * 6.28, amp: 30 + r.Float64()*40}
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			for c := 0; c < 3; c++ {
				v := 128.0
				for _, wv := range waves[c] {
					v += wv.amp * math.Sin(wv.fx*float64(x)+wv.fy*float64(y)+wv.ph)
				}
				t.pix[(y*w+x)*3+c] = byte(math.Max(0, math.Min(255, v)))
			}
		}
	}
	return t
}

// frame cuts a visualSize×visualSize window at dx, adding noise (sensor grain).
func (t *texture) frame(dx int, noise float64, r *rand.Rand) []byte {
	out := make([]byte, SampleBytes())
	for y := 0; y < visualSize; y++ {
		for x := 0; x < visualSize; x++ {
			sx := (x + dx) % t.w
			for c := 0; c < 3; c++ {
				v := float64(t.pix[(y*t.w+sx)*3+c]) + r.NormFloat64()*noise
				out[(y*visualSize+x)*3+c] = byte(math.Max(0, math.Min(255, v)))
			}
		}
	}
	return out
}

// box paints a solid rectangle (a person walking in, in miniature).
func box(f []byte, x0, y0, w, h int, rgb [3]byte) {
	for y := y0; y < y0+h; y++ {
		for x := x0; x < x0+w; x++ {
			if x >= 0 && x < visualSize && y >= 0 && y < visualSize {
				copy(f[(y*visualSize+x)*3:], rgb[:])
			}
		}
	}
}

func run(frames [][]byte) []VisualChange {
	d := NewVisualDetector(visualFPS)
	for _, f := range frames {
		d.Add(f)
	}
	ch, _ := d.Finish(float64(len(frames))/visualFPS, nil)
	return ch
}

func kinds(ch []VisualChange) []string {
	var out []string
	for _, c := range ch {
		out = append(out, c.Kind)
	}
	return out
}

func TestStaticTalkingHeadHasNoEvents(t *testing.T) {
	r := rand.New(rand.NewSource(1))
	tex := newTexture(64, 64, 7)
	var frames [][]byte
	for k := 0; k < 20*visualFPS; k++ {
		f := tex.frame(0, 3, r)
		// A small "mouth" region flickers while talking, and the head sways a pixel.
		if k%2 == 0 {
			box(f, 28, 40, 8, 3, [3]byte{90, 40, 40})
		}
		if k%12 < 6 {
			f = tex.frame(1, 3, r)
			box(f, 28, 40, 8, 3, [3]byte{90, 40, 40})
		}
		frames = append(frames, f)
	}
	if ch := run(frames); len(ch) != 0 {
		t.Fatalf("static talking head: want no events, got %+v", ch)
	}
}

func TestHardCut(t *testing.T) {
	r := rand.New(rand.NewSource(2))
	a, b := newTexture(64, 64, 11), newTexture(64, 64, 99)
	var frames [][]byte
	for k := 0; k < 6*visualFPS; k++ {
		if k < 3*visualFPS {
			frames = append(frames, a.frame(0, 2, r))
		} else {
			frames = append(frames, b.frame(0, 2, r))
		}
	}
	ch := run(frames)
	if len(ch) != 1 || ch[0].Kind != ChangeCut || math.Abs(ch[0].At-3) > 0.2 {
		t.Fatalf("want one cut near 3 s, got %+v", ch)
	}
	if len(ch[0].Frames) != 2 || ch[0].Frames[0] >= 3 || ch[0].Frames[1] <= 3 {
		t.Fatalf("frames before and after the cut: %v", ch[0].Frames)
	}
}

func TestSlowPanIsGradual(t *testing.T) {
	r := rand.New(rand.NewSource(3))
	wide := newTexture(400, 64, 5)
	var frames [][]byte
	// 2 s still, 8 s pan at one sample pixel per sample (64 px = a frame width every ~10 s), 2 s still.
	dx := 0
	for k := 0; k < 12*visualFPS; k++ {
		if k >= 2*visualFPS && k < 10*visualFPS {
			dx++
		}
		frames = append(frames, wide.frame(dx, 2, r))
	}
	ch := run(frames)
	if len(ch) != 1 || ch[0].Kind != ChangeGradual {
		t.Fatalf("want one gradual change, got %v %+v", kinds(ch), ch)
	}
	if ch[0].From > 2.6 || ch[0].To < 8 {
		t.Fatalf("the pan runs from about 2 s to 10 s: %+v", ch[0])
	}
	if len(ch[0].Frames) != 3 {
		t.Fatalf("frames: %v", ch[0].Frames)
	}
}

func TestSomeoneWalkingInIsMovement(t *testing.T) {
	r := rand.New(rand.NewSource(4))
	bg := newTexture(64, 64, 21)
	var frames [][]byte
	for k := 0; k < 10*visualFPS; k++ {
		f := bg.frame(0, 2, r)
		// Walks in from the right between 4 s and 5.5 s, then stands still.
		x := 64
		switch {
		case k >= 4*visualFPS && k < int(5.5*visualFPS):
			x = 64 - (k-4*visualFPS)*4
		case k >= int(5.5*visualFPS):
			x = 64 - int(1.5*visualFPS)*4
		}
		box(f, x, 10, 18, 50, [3]byte{220, 180, 150})
		frames = append(frames, f)
	}
	ch := run(frames)
	if len(ch) != 1 || ch[0].Kind != ChangeMotion || ch[0].From > 4.3 || ch[0].To < 5 {
		t.Fatalf("want one movement around 4–5.5 s, got %v %+v", kinds(ch), ch)
	}
}

func TestSteadyMotionIsNotABurst(t *testing.T) {
	r := rand.New(rand.NewSource(5))
	bg := newTexture(64, 64, 31)
	var frames [][]byte
	for k := 0; k < 15*visualFPS; k++ {
		f := bg.frame(0, 2, r)
		// A block bouncing across the frame the whole time (like the test pattern's moving square).
		x := int(30 + 25*math.Sin(float64(k)*0.4))
		box(f, x, 30, 10, 10, [3]byte{255, 176, 0})
		frames = append(frames, f)
	}
	for _, c := range run(frames) {
		if c.Kind == ChangeMotion {
			t.Fatalf("steady motion is this shot's normal: %+v", c)
		}
	}
}

func TestActivityPerSecond(t *testing.T) {
	r := rand.New(rand.NewSource(6))
	bg := newTexture(64, 64, 41)
	d := NewVisualDetector(visualFPS)
	for k := 0; k < 4*visualFPS; k++ {
		f := bg.frame(0, 1, r)
		if k >= 2*visualFPS {
			box(f, (k*7)%50, 20, 14, 14, [3]byte{250, 250, 250})
		}
		d.Add(f)
	}
	_, act := d.Finish(4, nil)
	if len(act.Values) != 4 || act.Values[0] > 0.1 || act.Values[3] <= act.Values[0] {
		t.Fatalf("activity should rise when something moves: %v", act.Values)
	}
}

func TestStrongSceneBecomesACut(t *testing.T) {
	r := rand.New(rand.NewSource(7))
	a := newTexture(64, 64, 51)
	var frames [][]byte
	for k := 0; k < 4*visualFPS; k++ {
		frames = append(frames, a.frame(0, 1, r))
	}
	d := NewVisualDetector(visualFPS)
	for _, f := range frames {
		d.Add(f)
	}
	ch, _ := d.Finish(4, []Scene{{At: 2.0, Score: 0.7}})
	if len(ch) != 1 || ch[0].Kind != ChangeCut || ch[0].At != 2 {
		t.Fatalf("a strong full-rate scene change counts: %+v", ch)
	}
}

package evidence

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// Visual changes (D71): the scene detector above only notices hard cuts, and
// phone footage is usually one continuous shot, so people walking in, pans and
// zooms were invisible. The picture is sampled a few times a second at a tiny
// size and three things are measured:
//   - cuts: an abrupt change between two samples that stands out from the samples
//     around it (fast movement changes every sample a little; a cut changes one a lot);
//   - gradual changes: the picture drifting away from the last keyframe without a
//     cut (a slow pan or zoom, light changing, someone slowly coming into view);
//   - movement: a burst of motion well above what is normal for that shot.
// Plus the activity per second (how much the picture moves). All of it is
// deterministic and local (ffmpeg plus arithmetic), like the other detectors.

// Visual change kinds.
const (
	ChangeCut     = "cut"
	ChangeGradual = "gradual"
	ChangeMotion  = "motion"
)

// VisualChange is a moment the picture changes.
type VisualChange struct {
	At    float64 `json:"at" bson:"at" jsonschema:"description=When the change is clearest (source seconds)."`
	From  float64 `json:"from" bson:"from" jsonschema:"description=Where it starts (a cut: the same as at)."`
	To    float64 `json:"to" bson:"to" jsonschema:"description=Where it ends (a cut: the same as at)."`
	Score float64 `json:"score" bson:"score" jsonschema:"description=How big the change is, 0 to 1."`
	Kind  string  `json:"kind" bson:"kind" jsonschema:"enum=cut,enum=gradual,enum=motion"`
	// Frames are source times worth looking at: just before and after a cut,
	// the start, middle and end of a slow change, the start, peak and end of movement.
	Frames []float64 `json:"frames" bson:"frames"`
}

// Activity is how much the picture moves, per Step seconds (0 still … 1 very busy).
type Activity struct {
	Step   float64   `json:"step" bson:"step"`
	Values []float64 `json:"values" bson:"values"`
}

// Detector settings. Changing any of them changes what is found: bump Version.
const (
	visualFPS  = 6  // samples per second
	visualSize = 64 // samples are visualSize × visualSize RGB (aspect is irrelevant for differences)
	sigN       = 16 // keyframe signature: sigN × sigN gray
	histBins   = 16 // per channel

	cutMin       = 0.26 // a cut changes at least this much between two samples…
	cutContrast  = 2.5  // …and this many times more than the samples around it
	cutFloor     = 0.06
	driftMin     = 0.30  // a gradual change: this far from the last keyframe,
	driftArea    = 0.18  // or this much of the picture changed a lot since it
	areaDelta    = 16    // a signature cell "changed" between samples: by at least this much (of 255)
	keyDelta     = 24    // …and since the keyframe
	driftMoving  = 0.06  // moving: changed by half this over the last second…
	movingArea   = 0.05  // …or this much of the picture changed over it
	longChange   = 20.0  // a slow change running on longer than this is split where each new view is reached
	motionTimes  = 3.0   // movement: the changed part of the picture is this many times the shot's normal…
	motionAbove  = 0.025 // …and at least this much above it…
	motionFloor  = 0.03  // …and at least this much of the picture
	motionSecs   = 0.5   // for at least this long
	mergeSecs    = 1.0   // movement and a slow change this close are one event
	maxEvents    = 600
	scaleMAD     = 0.12 // activity 1.0 = this mean absolute difference between samples
	sceneSnap    = 0.4  // a cut takes the exact time of a scene the full-rate detector found this close
	strongScene  = 0.5  // full-rate scene changes this strong are cuts even if the sampled pass missed them
	visualRounds = 1000
)

type vsample struct {
	t    float64
	sig  [sigN * sigN]uint8
	hist [histBins * 3]float32
	mad  float64 // vs the previous sample, 0–1
	cut  float64 // cut score vs the previous sample, 0–1
	area float64 // share of the picture that changed a lot vs the previous sample
}

// VisualDetector measures a stream of visualSize×visualSize RGB samples taken
// visualFPS times a second. Add each sample in order, then Finish.
type VisualDetector struct {
	fps     float64
	prevRaw []byte
	samples []vsample
}

// NewVisualDetector starts a detector for samples taken fps times a second.
func NewVisualDetector(fps float64) *VisualDetector {
	return &VisualDetector{fps: fps}
}

// SampleBytes is the size of one sample (RGB).
func SampleBytes() int { return visualSize * visualSize * 3 }

// Add measures one sample (visualSize × visualSize RGB, row-major).
func (d *VisualDetector) Add(rgb []byte) {
	s := vsample{t: float64(len(d.samples)) / d.fps}
	s.sig, s.hist = features(rgb)
	if d.prevRaw != nil {
		s.mad = meanAbsDiff(d.prevRaw, rgb)
		p := &d.samples[len(d.samples)-1]
		s.cut = changeScore(&p.sig, &p.hist, &s.sig, &s.hist)
		s.area = areaFrac(&p.sig, &s.sig, areaDelta)
	} else {
		d.prevRaw = make([]byte, len(rgb))
	}
	copy(d.prevRaw, rgb)
	d.samples = append(d.samples, s)
}

func features(rgb []byte) (sig [sigN * sigN]uint8, hist [histBins * 3]float32) {
	const block = visualSize / sigN
	var acc [sigN * sigN]float64
	n := visualSize * visualSize
	for i := 0; i < n; i++ {
		r, g, b := rgb[3*i], rgb[3*i+1], rgb[3*i+2]
		x, y := i%visualSize, i/visualSize
		acc[(y/block)*sigN+x/block] += 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(b)
		hist[int(r)*histBins/256]++
		hist[histBins+int(g)*histBins/256]++
		hist[2*histBins+int(b)*histBins/256]++
	}
	for i := range acc {
		sig[i] = uint8(math.Round(acc[i] / (block * block)))
	}
	for i := range hist {
		hist[i] /= float32(n)
	}
	return
}

func meanAbsDiff(a, b []byte) float64 {
	sum := 0
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d < 0 {
			d = -d
		}
		sum += d
	}
	return float64(sum) / float64(len(a)) / 255
}

// changeScore compares two samples: half the color distribution, half the
// layout of light and dark (0 the same … 1 completely different).
func changeScore(sa *[sigN * sigN]uint8, ha *[histBins * 3]float32, sb *[sigN * sigN]uint8, hb *[histBins * 3]float32) float64 {
	h := 0.0
	for i := range ha {
		h += math.Abs(float64(ha[i] - hb[i]))
	}
	h /= 6 // three channels, each an L1 distance of at most 2
	s := 0.0
	for i := range sa {
		s += math.Abs(float64(sa[i]) - float64(sb[i]))
	}
	s /= float64(len(sa)) * 255
	return math.Min(1, 0.5*h+0.5*math.Min(1, 2*s))
}

// areaFrac is the share of signature cells whose brightness differs by at least delta.
func areaFrac(a, b *[sigN * sigN]uint8, delta int) float64 {
	n := 0
	for i := range a {
		d := int(a[i]) - int(b[i])
		if d >= delta || d <= -delta {
			n++
		}
	}
	return float64(n) / float64(len(a))
}

func round3(v float64) float64 { return math.Round(v*visualRounds) / visualRounds }
func round1(v float64) float64 { return math.Round(v*10) / 10 }

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	c := append([]float64(nil), xs...)
	sort.Float64s(c)
	if len(c)%2 == 1 {
		return c[len(c)/2]
	}
	return (c[len(c)/2-1] + c[len(c)/2]) / 2
}

// Finish finds the changes. duration bounds the frame times; scenes are the
// full-rate scene detector's findings (exact cut times).
func (d *VisualDetector) Finish(duration float64, scenes []Scene) ([]VisualChange, *Activity) {
	s := d.samples
	act := &Activity{Step: 1, Values: []float64{}}
	if len(s) < 2 {
		return []VisualChange{}, act
	}
	if duration <= 0 {
		duration = s[len(s)-1].t + 1/d.fps
	}
	clampT := func(t float64) float64 { return round1(math.Max(0, math.Min(duration-0.05, t))) }
	half := 1 / (2 * d.fps)

	// 1. Cuts: a sample that changed a lot, and much more than its neighbors.
	isCut := make([]bool, len(s))
	var cuts []int
	for k := 1; k < len(s); k++ {
		c := s[k].cut
		if c < cutMin {
			continue
		}
		var around []float64
		for j := k - 6; j <= k+6; j++ {
			if j >= 1 && j < len(s) && (j < k-1 || j > k+1) {
				around = append(around, s[j].cut)
			}
		}
		if c < cutContrast*median(around)+cutFloor {
			continue
		}
		// The strongest sample of its neighborhood.
		peak := true
		for j := k - 2; j <= k+2; j++ {
			if j != k && j >= 1 && j < len(s) && s[j].cut > c {
				peak = false
			}
		}
		if peak {
			isCut[k] = true
			cuts = append(cuts, k)
		}
	}
	var out []VisualChange
	cutTimes := []float64{}
	for _, k := range cuts {
		at := s[k].t - half
		best := math.Inf(1)
		for _, sc := range scenes {
			if dd := math.Abs(sc.At - at); dd <= sceneSnap && dd < best {
				best, at = dd, sc.At
			}
		}
		cutTimes = append(cutTimes, at)
		out = append(out, VisualChange{At: round3(at), From: round3(at), To: round3(at), Score: round3(s[k].cut), Kind: ChangeCut,
			Frames: []float64{clampT(at - 0.4), clampT(at + 0.4)}})
	}
	for _, sc := range scenes {
		if sc.Score < strongScene {
			continue
		}
		near := false
		for _, t := range cutTimes {
			if math.Abs(t-sc.At) <= 0.5 {
				near = true
				break
			}
		}
		if !near {
			cutTimes = append(cutTimes, sc.At)
			out = append(out, VisualChange{At: round3(sc.At), From: round3(sc.At), To: round3(sc.At), Score: round3(math.Min(1, sc.Score)), Kind: ChangeCut,
				Frames: []float64{clampT(sc.At - 0.4), clampT(sc.At + 0.4)}})
			// The sample closest after it starts a new shot.
			if k := int(math.Ceil(sc.At*d.fps + 0.5)); k > 0 && k < len(s) {
				isCut[k] = true
			}
		}
	}

	// Shots: runs of samples between cuts.
	type shot struct{ a, b int } // [a, b)
	var shots []shot
	start := 0
	for k := 1; k < len(s); k++ {
		if isCut[k] {
			shots = append(shots, shot{start, k})
			start = k
		}
	}
	shots = append(shots, shot{start, len(s)})

	// 2. Gradual changes and 3. movement, per shot.
	win := max(1, int(math.Round(d.fps)))
	minRun := int(math.Ceil(motionSecs * d.fps))
	var spans []VisualChange
	for _, sh := range shots {
		// Is the picture moving? Compared with a second earlier, so a slow pan
		// (a little each sample) counts as moving and a grainy still does not.
		moving := make([]bool, sh.b-sh.a)
		for k := sh.a + 1; k < sh.b; k++ {
			j := max(sh.a, k-win)
			moving[k-sh.a] = changeScore(&s[j].sig, &s[j].hist, &s[k].sig, &s[k].hist) >= driftMoving/2 || areaFrac(&s[j].sig, &s[k].sig, keyDelta) >= movingArea
		}
		// The run of moving samples around k, as times (the window is centered).
		run := func(k int) (float64, float64) {
			a, b := k, k
			for a > sh.a+1 && moving[a-1-sh.a] {
				a--
			}
			for b+1 < sh.b && moving[b+1-sh.a] {
				b++
			}
			lag := float64(win) / (2 * d.fps)
			return math.Max(s[sh.a].t, s[a].t-lag), math.Max(s[a].t, s[b].t-lag)
		}
		// Drift from the keyframe: each time the picture has wandered far enough,
		// that is a new view. Views reached while it keeps moving are one change
		// (a whole pan), split only when it runs on for a long time.
		key := sh.a
		var cur *VisualChange
		for k := sh.a + 1; k < sh.b; k++ {
			dr := changeScore(&s[key].sig, &s[key].hist, &s[k].sig, &s[k].hist)
			ar := areaFrac(&s[key].sig, &s[k].sig, keyDelta)
			if dr < driftMin && ar < driftArea {
				continue
			}
			sc := math.Max(dr, ar)
			from, to := run(k)
			switch {
			case cur != nil && from <= cur.To+0.05 && s[k].t-cur.From <= longChange:
				cur.To, cur.Score = math.Max(cur.To, to), math.Max(cur.Score, sc)
			case cur != nil && from <= cur.To+0.05:
				// One long movement: close this part where this view was reached.
				cur.To = s[k].t
				spans = append(spans, *cur)
				cur = &VisualChange{From: s[k].t, At: s[k].t, To: math.Max(s[k].t, to), Score: sc, Kind: ChangeGradual}
			default:
				if cur != nil {
					spans = append(spans, *cur)
				}
				cur = &VisualChange{From: from, At: s[k].t, To: math.Max(s[k].t, to), Score: sc, Kind: ChangeGradual}
			}
			key = k
		}
		if cur != nil {
			spans = append(spans, *cur)
		}

		// Movement: much more of the picture changing than is normal for this shot, for a while.
		var areas []float64
		for k := sh.a + 1; k < sh.b; k++ {
			if !isCut[k] && (k+1 >= len(s) || !isCut[k+1]) {
				areas = append(areas, s[k].area)
			}
		}
		base := median(areas)
		thr := math.Max(math.Max(motionTimes*base, base+motionAbove), motionFloor)
		runStart, gap, peakK := -1, 0, -1
		flush := func(end int) {
			if runStart >= 0 && end-runStart+1 >= minRun {
				pk := s[peakK]
				spans = append(spans, VisualChange{From: s[runStart].t - half, To: s[end].t, At: pk.t, Kind: ChangeMotion,
					Score: math.Min(1, 2*(pk.area-base))})
			}
			runStart, gap, peakK = -1, 0, -1
		}
		lastHot := -1
		for k := sh.a + 1; k < sh.b; k++ {
			hot := s[k].area >= thr && !isCut[k] && (k+1 >= len(s) || !isCut[k+1])
			switch {
			case hot && runStart < 0:
				runStart, peakK, gap, lastHot = k, k, 0, k
			case hot:
				gap, lastHot = 0, k
				if s[k].area > s[peakK].area {
					peakK = k
				}
			case runStart >= 0:
				gap++
				if gap > 1 {
					flush(lastHot)
				}
			}
		}
		if runStart >= 0 {
			flush(lastHot)
		}
	}

	// Movement and a slow change at (nearly) the same time are one event: movement
	// caused it (someone walked in). Keep movement's peak, the union of the spans.
	sort.Slice(spans, func(i, j int) bool { return spans[i].From < spans[j].From })
	var merged []VisualChange
	for _, sp := range spans {
		if n := len(merged); n > 0 {
			last := &merged[n-1]
			if sp.From <= last.To+mergeSecs && (sp.Kind != last.Kind || sp.Kind == ChangeMotion) && sameShot(cutTimes, last.From, sp.To) {
				if sp.Kind == ChangeMotion && last.Kind != ChangeMotion {
					last.At, last.Kind = sp.At, ChangeMotion
				}
				last.To = math.Max(last.To, sp.To)
				last.Score = math.Max(last.Score, sp.Score)
				continue
			}
		}
		merged = append(merged, sp)
	}
	for _, sp := range merged {
		sp.From, sp.To = math.Max(0, sp.From), math.Min(duration, sp.To)
		switch sp.Kind {
		case ChangeGradual:
			sp.Frames = []float64{clampT(sp.From), clampT((sp.From + sp.To) / 2), clampT(sp.To)}
		default:
			sp.Frames = []float64{clampT(sp.From), clampT(sp.At), clampT(sp.To)}
		}
		sp.At, sp.From, sp.To, sp.Score = round3(sp.At), round3(sp.From), round3(sp.To), round3(sp.Score)
		out = append(out, sp)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	if len(out) > maxEvents {
		sort.SliceStable(out, func(i, j int) bool { return out[i].Score > out[j].Score })
		out = out[:maxEvents]
		sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	}
	if out == nil {
		out = []VisualChange{}
	}

	// Activity per second (cuts are not movement).
	secs := int(math.Ceil(duration))
	sum, cnt := make([]float64, secs), make([]int, secs)
	for k := 1; k < len(s); k++ {
		if isCut[k] {
			continue
		}
		i := int(s[k].t)
		if i >= 0 && i < secs {
			sum[i] += s[k].mad
			cnt[i]++
		}
	}
	act.Values = make([]float64, secs)
	for i := range act.Values {
		if cnt[i] > 0 {
			act.Values[i] = round3(math.Min(1, sum[i]/float64(cnt[i])/scaleMAD))
		}
	}
	return out, act
}

// sameShot reports whether no cut falls strictly inside (a, b).
func sameShot(cuts []float64, a, b float64) bool {
	for _, c := range cuts {
		if c > a+0.05 && c < b-0.05 {
			return false
		}
	}
	return true
}

// detectVisual samples the picture with ffmpeg and runs the detector.
func detectVisual(ctx context.Context, t *tools.Set, path string, mi *models.MediaInfo, scenes []Scene) ([]VisualChange, *Activity, error) {
	args := []string{"-hide_banner", "-nostdin", "-nostats", "-loglevel", "error", "-i", path, "-map", "0:v:0", "-an", "-sn",
		"-vf", fmt.Sprintf("fps=%d,scale=%d:%d:flags=area,format=rgb24", visualFPS, visualSize, visualSize), "-f", "rawvideo", "-"}
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		_, err := t.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args, Stdout: pw, Background: true})
		pw.CloseWithError(err)
		errc <- err
	}()
	det := NewVisualDetector(visualFPS)
	buf := make([]byte, SampleBytes())
	for {
		if _, err := io.ReadFull(pr, buf); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				_ = pr.CloseWithError(err)
				<-errc
				return nil, nil, err
			}
			break
		}
		det.Add(buf)
	}
	_ = pr.Close()
	if err := <-errc; err != nil {
		return nil, nil, err
	}
	ch, act := det.Finish(mi.Duration, scenes)
	return ch, act, nil
}

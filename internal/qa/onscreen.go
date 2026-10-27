package qa

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visual"
)

// What's on screen in the render (D116): faces cut off by the crop, captions
// or graphics over faces, and captions over text that is in the footage
// itself. MetaRush knows where the footage's faces and text are (the
// on-screen index) and exactly how the plan crops and places every clip,
// caption band and graphic, so it maps the footage into the rendered picture
// frame by frame; where a Mac can look, the rendered frames themselves are
// read with the same on-device helper to confirm what the geometry says.

// Looker reads the JPEG frames in a folder with the on-screen helper.
type Looker func(ctx context.Context, dir string) ([]vision.Frame, error)

// Thresholds of the on-screen checks.
const (
	faceCutVisible  = 0.8  // a main face showing less than this share of itself is cut off by the crop
	faceCoverShare  = 0.25 // a face this much under captions or a graphic is covered
	textCoverShare  = 0.3  // footage text this much under the caption band sits under the captions
	onScreenStep    = 0.5  // seconds between moments checked
	onScreenGap     = 1.0  // moments closer than this join into one stretch
	onScreenMinSpan = 0.75 // a stretch shorter than this isn't reported (a passing moment)
	renderLookMax   = 40   // rendered frames looked at, at most
)

type rect struct{ x0, y0, x1, y1 float64 }

func (a rect) area() float64 { return math.Max(0, a.x1-a.x0) * math.Max(0, a.y1-a.y0) }
func (a rect) and(b rect) rect {
	return rect{math.Max(a.x0, b.x0), math.Max(a.y0, b.y0), math.Min(a.x1, b.x1), math.Min(a.y1, b.y1)}
}

func boxRect(b box) rect { return rect{b.L, b.T, b.R, b.B} }

// hit is one moment something is wrong.
type hit struct {
	kind  string
	key   string // what it is about: the clip, the graphic, the text
	clip  string
	over  string // the overlay, for graphics over faces
	who   string // whose face (or "")
	text  string // the footage text under the captions
	side  string // where a face is cut: left, right, top or bottom
	frame int
	low   bool // the face or text is in the lower half
}

type stretch struct {
	hit
	from, to int
	frames   []int
}

// onScreen runs the on-screen checks.
func (r *run) onScreen(ctx context.Context) {
	const name = "on_screen"
	kinds := []string{"face_cut_off", "faces_covered", "graphic_covers_face", "text_under_captions"}
	if len(r.in.OnScreen) == 0 {
		why := "MetaRush hasn't looked at what's on screen in this footage yet (that needs a Mac), so faces and text in the footage weren't checked against the crop, the captions and the graphics"
		if r.in.Look != nil {
			why = "None of this cut's footage has been looked at yet, so faces and text in the footage weren't checked against the crop, the captions and the graphics"
		}
		r.skip(name, models.QAHeuristic, why)
		return
	}
	hits := r.onScreenHits()
	spans := joinHits(hits, int(math.Round(onScreenGap*r.fps)), int(math.Round(onScreenMinSpan*r.fps)))
	seen := r.lookAtRender(ctx, spans)
	for _, s := range spans {
		r.reportOnScreen(s, seen)
	}
	basis := models.QAHeuristic
	if seen != nil {
		basis = models.QAObserved
	}
	r.check(name, basis, fmt.Sprintf("the footage's faces and text (on-screen index) placed through each clip's crop every %.1f s: a main face showing < %.0f%% of itself; faces ≥ %.0f%% under the caption band or a graphic; footage text ≥ %.0f%% under the caption band; stretches ≥ %.2f s; rendered frames read with Apple Vision when a Mac can",
		onScreenStep, faceCutVisible*100, faceCoverShare*100, textCoverShare*100, onScreenMinSpan), kinds...)
}

// onScreenHits steps through the cut and maps the footage's faces and text
// into the rendered picture.
func (r *run) onScreenHits() []hit {
	p := r.plan
	g := newGeom(p)
	W, H := float64(p.Width), float64(p.Height)
	frameR := rect{0, 0, W, H}
	step := max(1, int(math.Round(onScreenStep*r.fps)))
	overlays, _ := layoutOverlays(p)
	var out []hit
	for f := 0; f < p.DurationInFrames; f += step {
		cs := r.clipsAt(f, f+1)
		if len(cs) != 1 {
			continue // nothing, or a transition between two clips
		}
		c := cs[0]
		if c.Conversation != nil || editspec.IsNested(c.AssetID) || c.SourceWidth <= 0 || c.SourceHeight <= 0 {
			continue
		}
		idx := r.in.OnScreen[c.AssetID]
		if idx == nil {
			continue
		}
		srcT := r.srcTime(c, f)
		left, top, w, h := editspec.CropRect(c.Framing, float64(c.SourceWidth), float64(c.SourceHeight), W, H, float64(f-c.From))
		place := func(b vision.Box) rect {
			return rect{left + b[0]*w, top + b[1]*h, left + (b[0]+b[2])*w, top + (b[1]+b[3])*h}
		}
		var caption *box
		if cl := r.captionAt(f); cl {
			z := g.captionZone(p.Captions.Position, p.Captions.Style, p.Captions.Size)
			caption = &z
		}
		// Faces at this moment of the footage.
		if fs := faceSampleNear(idx, srcT); fs != nil {
			mainK, mainA := -1, 0.0
			for k, b := range fs.Boxes {
				if a := b[2] * b[3]; a > mainA {
					mainK, mainA = k, a
				}
			}
			for k, b := range fs.Boxes {
				fr := place(b)
				full := fr.area()
				if full <= 0 {
					continue
				}
				vis := fr.and(frameR)
				shown := vis.area() / full
				who := r.faceName(c.AssetID, fs, k)
				if k == mainK && shown < faceCutVisible && (shown > 0.02 || len(fs.Boxes) == 1) {
					out = append(out, hit{kind: "face_cut_off", key: c.ID, clip: c.ID, who: who, side: cutSide(fr, frameR), frame: f})
				}
				if shown < 0.5 {
					continue
				}
				low := (fr.y0+fr.y1)/2 > H/2
				if caption != nil && vis.and(boxRect(*caption)).area()/vis.area() >= faceCoverShare {
					out = append(out, hit{kind: "faces_covered", key: c.ID, clip: c.ID, who: who, frame: f, low: low})
				}
				for _, o := range overlays {
					if f < o.From || f >= o.From+o.DurationInFrames || regionOfOverlay(o) == "full" {
						continue
					}
					if vis.and(boxRect(g.boxOf(o))).area()/vis.area() >= faceCoverShare {
						out = append(out, hit{kind: "graphic_covers_face", key: o.ID, clip: c.ID, over: o.ID, who: who, frame: f, low: low})
					}
				}
			}
		}
		// Text in the footage under the captions.
		if caption != nil {
			for _, t := range idx.Text {
				if srcT < t.From || srcT >= t.To || (t.Changing && visual.DigitHeavy(t.Text)) {
					continue
				}
				tr := place(t.Box).and(frameR)
				if tr.area() <= 0 {
					continue
				}
				if tr.and(boxRect(*caption)).area()/tr.area() >= textCoverShare {
					out = append(out, hit{kind: "text_under_captions", key: c.ID + "/" + t.Text, clip: c.ID, text: t.Text, frame: f, low: (tr.y0+tr.y1)/2 > H/2})
				}
			}
		}
	}
	return out
}

// captionAt reports a caption line on screen at frame f.
func (r *run) captionAt(f int) bool {
	if r.plan.Captions == nil {
		return false
	}
	for _, l := range r.plan.Captions.Lines {
		if f >= l.From && f < l.To {
			return true
		}
	}
	return false
}

// faceSampleNear is where the faces are at source time t (the nearest frame
// the index looked at, when it is close enough).
func faceSampleNear(idx *visual.Index, t float64) *visual.FaceSample {
	tol := math.Max(0.6, idx.Step*0.6)
	i := sort.Search(len(idx.Faces), func(i int) bool { return idx.Faces[i].At >= t })
	best, bestD := -1, tol
	for _, k := range []int{i - 1, i} {
		if k >= 0 && k < len(idx.Faces) {
			if d := math.Abs(idx.Faces[k].At - t); d <= bestD {
				best, bestD = k, d
			}
		}
	}
	if best < 0 {
		return nil
	}
	return &idx.Faces[best]
}

func (r *run) faceName(assetID string, fs *visual.FaceSample, k int) string {
	if k >= len(fs.Tracks) {
		return ""
	}
	return r.in.FaceNames[assetID][fs.Tracks[k]]
}

// cutSide is the edge of the picture a face is cut at.
func cutSide(f, frame rect) string {
	best, side := 0.0, ""
	for _, s := range []struct {
		name string
		over float64
	}{{"left", frame.x0 - f.x0}, {"right", f.x1 - frame.x1}, {"top", frame.y0 - f.y0}, {"bottom", f.y1 - frame.y1}} {
		if s.over > best {
			best, side = s.over, s.name
		}
	}
	return side
}

// joinHits joins moments of the same thing into stretches.
func joinHits(hits []hit, gap, minLen int) []stretch {
	by := map[string][]hit{}
	var order []string
	for _, h := range hits {
		k := h.kind + "|" + h.key + "|" + h.who
		if _, ok := by[k]; !ok {
			order = append(order, k)
		}
		by[k] = append(by[k], h)
	}
	var out []stretch
	for _, k := range order {
		hs := by[k]
		sort.Slice(hs, func(i, j int) bool { return hs[i].frame < hs[j].frame })
		cur := stretch{hit: hs[0], from: hs[0].frame, to: hs[0].frame, frames: []int{hs[0].frame}}
		flush := func() {
			if cur.to-cur.from+1 >= minLen || len(cur.frames) >= 2 {
				out = append(out, cur)
			}
		}
		for _, h := range hs[1:] {
			if h.frame-cur.to <= gap {
				cur.to = h.frame
				cur.frames = append(cur.frames, h.frame)
				if h.low {
					cur.low = true
				}
				continue
			}
			flush()
			cur = stretch{hit: h, from: h.frame, to: h.frame, frames: []int{h.frame}}
		}
		flush()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].from < out[j].from })
	return out
}

// lookAtRender reads the rendered frames in the middle of each stretch with the
// on-screen helper (nil when it can't: not a Mac, or nothing to look at).
func (r *run) lookAtRender(ctx context.Context, spans []stretch) map[int]vision.Frame {
	if r.in.Look == nil || len(spans) == 0 || !r.mi.HasVideo || r.in.Scratch == "" || !r.t.Has(tools.FFmpeg) {
		return nil
	}
	var want []int
	have := map[int]bool{}
	for _, s := range spans {
		for _, f := range []int{s.frames[len(s.frames)/2], s.from, s.to} {
			if !have[f] && len(want) < renderLookMax {
				have[f] = true
				want = append(want, f)
			}
		}
	}
	dir, err := os.MkdirTemp(r.in.Scratch, "qa-look-")
	if err != nil {
		return nil
	}
	defer os.RemoveAll(dir)
	at := map[string]int{}
	for i, f := range want {
		name := fmt.Sprintf("r%05d.jpg", i+1)
		t := (float64(f) + 0.5) / r.fps
		_, err := r.t.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error", "-ss", fmt.Sprintf("%.3f", t), "-i", r.in.Path,
			"-frames:v", "1", "-map", "0:v:0", "-q:v", "3", filepath.Join(dir, name)}, Background: true})
		if err != nil {
			continue
		}
		at[name] = f
	}
	frames, err := r.in.Look(ctx, dir)
	if err != nil {
		return nil
	}
	out := map[int]vision.Frame{}
	for _, fr := range frames {
		if f, ok := at[fr.File]; ok && fr.Error == "" {
			out[f] = fr
		}
	}
	return out
}

// observed says whether the rendered frames show what a stretch says.
func (r *run) observed(s stretch, seen map[int]vision.Frame) bool {
	if seen == nil {
		return false
	}
	g := newGeom(r.plan)
	W, H := float64(r.plan.Width), float64(r.plan.Height)
	for _, f := range s.frames {
		fr, ok := seen[f]
		if !ok {
			continue
		}
		toPx := func(b vision.Box) rect { return rect{b[0] * W, b[1] * H, (b[0] + b[2]) * W, (b[1] + b[3]) * H} }
		switch s.kind {
		case "face_cut_off":
			for _, fc := range fr.Faces {
				if fc.Conf >= 0.5 && (fc.Box[0] < 0.01 || fc.Box[0]+fc.Box[2] > 0.99 || fc.Box[1] < 0.01 || fc.Box[1]+fc.Box[3] > 0.99) {
					return true // a face at the very edge of the rendered picture
				}
			}
		case "faces_covered", "graphic_covers_face":
			for _, fc := range fr.Faces {
				for _, t := range fr.Text {
					if toPx(fc.Box).and(toPx(t.Box)).area() > 0 {
						return true // words drawn over a face in the rendered picture
					}
				}
			}
		case "text_under_captions":
			if r.plan.Captions == nil {
				continue
			}
			zone := boxRect(g.captionZone(r.plan.Captions.Position, r.plan.Captions.Style, r.plan.Captions.Size))
			lines := 0
			for _, t := range fr.Text {
				if toPx(t.Box).and(zone).area() > 0 {
					lines++
				}
				if similarText(t.S, s.text) {
					return true // the footage's own words read in the caption band
				}
			}
			if lines >= 2 {
				return true
			}
		}
	}
	return false
}

func similarText(a, b string) bool {
	norm := func(s string) string { return strings.ToLower(strings.Join(strings.Fields(s), " ")) }
	a, b = norm(a), norm(b)
	if a == "" || b == "" {
		return false
	}
	return strings.Contains(a, b) || strings.Contains(b, a)
}

// reportOnScreen turns a stretch into a finding, with a fix as an edit.
func (r *run) reportOnScreen(s stretch, seen map[int]vision.Frame) {
	from, to := float64(s.from)/r.fps, float64(s.to+1)/r.fps
	f := &models.QAFinding{Severity: models.QAWarning, Kind: s.kind, Basis: models.QAHeuristic, From: from, To: to, ClipIDs: []string{s.clip}}
	if r.observed(s, seen) {
		f.Basis = models.QAObserved
	}
	face := "a face"
	if s.who != "" {
		face = s.who + "'s face"
	}
	switch s.kind {
	case "face_cut_off":
		edge := map[string]string{"left": "at the left edge", "right": "at the right edge", "top": "at the top", "bottom": "at the bottom"}[s.side]
		f.Message = fmt.Sprintf("Around %s the crop cuts off %s %s (in %s).", clock(from), face, edge, clipName(s.clip))
		f.Fix = r.followFix(s.clip)
	case "faces_covered":
		f.Message = fmt.Sprintf("Around %s the captions cover %s (in %s).", clock(from), face, clipName(s.clip))
		f.Fix = r.captionsAway(s, "faces")
	case "graphic_covers_face":
		o := r.overlay(s.over)
		f.OverlayIDs = []string{s.over}
		name := "a graphic"
		if o != nil {
			name = graphicName(*o)
		}
		f.Message = fmt.Sprintf("Around %s %s covers %s.", clock(from), name, face)
		f.Fix = r.graphicAway(s, o)
	case "text_under_captions":
		f.Message = fmt.Sprintf("Around %s the captions sit over text in the footage (“%s”), so both are hard to read.", clock(from), trunc(s.text, 40))
		f.Fix = r.captionsAway(s, "text")
	}
	r.add(f)
}

func (r *run) overlay(id string) *editspec.PlanOverlay {
	for i := range r.plan.Overlays {
		if r.plan.Overlays[i].ID == id {
			return &r.plan.Overlays[i]
		}
	}
	return nil
}

// followFix lets the clip's crop follow its subject (D115), or says what else helps.
func (r *run) followFix(clipID string) *models.QAFix {
	c := r.specClip(clipID)
	if c == nil || r.in.Spec == nil {
		return &models.QAFix{Summary: "Let the crop follow the subject (Follow movement), choose what to keep with a focus point, or show the whole picture."}
	}
	if c.Locked {
		return &models.QAFix{Summary: fmt.Sprintf("%s is locked: unlock it to let its crop follow the subject, or show the whole picture.", upperFirst(clipName(clipID)))}
	}
	if editspec.FollowsMovement(*c, r.in.Spec.Output) {
		return &models.QAFix{Summary: fmt.Sprintf("The crop of %s already follows the subject but can't keep the face in: show the whole picture (fit) or zoom out.", clipName(clipID)),
			Ops: []editspec.Op{setOp("updateClip", clipID, map[string]any{"framing": map[string]any{"mode": "fit"}})}}
	}
	return &models.QAFix{Summary: fmt.Sprintf("Let the crop of %s follow the subject as it moves (a new version).", clipName(clipID)),
		Ops: []editspec.Op{setOp("updateClip", clipID, map[string]any{"framing": map[string]any{"auto": editspec.FramingFollow}})}}
}

// captionsAway moves the captions to the band that keeps clear of what they
// cover (faces or the footage's text) at the stretch's moments.
func (r *run) captionsAway(s stretch, what string) *models.QAFix {
	sp := r.in.Spec
	if sp == nil || sp.Captions == nil {
		return &models.QAFix{Summary: "Move the captions higher or lower on the screen."}
	}
	if sp.Captions.Locked {
		return &models.QAFix{Summary: "Move the captions higher or lower on the screen (they are locked, so no change is suggested)."}
	}
	g := newGeom(r.plan)
	cur := r.plan.Captions.Position
	for _, pos := range []string{"top", "bottom", "middle"} {
		if pos == cur || (pos == "bottom" && cur == "lower") {
			continue
		}
		if !r.zoneHits(s, boxRect(g.captionZone(pos, r.plan.Captions.Style, r.plan.Captions.Size)), what) {
			c := *sp.Captions
			c.Position = pos
			where := map[string]string{"top": "to the top of the screen", "bottom": "to the bottom of the screen", "middle": "to the middle of the screen"}[pos]
			return &models.QAFix{Summary: fmt.Sprintf("Move the captions %s, keeping the rest of how they look.", where), Ops: []editspec.Op{{Op: "setCaptions", Captions: &c}}}
		}
	}
	return &models.QAFix{Summary: "No place on the screen keeps the captions clear here: shorten the stretch, crop the clip differently, or accept it."}
}

// zoneHits reports whether a caption band at another place would still cover
// what the stretch is about at its moments.
func (r *run) zoneHits(s stretch, zone rect, what string) bool {
	W, H := float64(r.plan.Width), float64(r.plan.Height)
	frameR := rect{0, 0, W, H}
	for _, f := range s.frames {
		cs := r.clipsAt(f, f+1)
		if len(cs) != 1 {
			continue
		}
		c := cs[0]
		idx := r.in.OnScreen[c.AssetID]
		if idx == nil {
			continue
		}
		srcT := r.srcTime(c, f)
		left, top, w, h := editspec.CropRect(c.Framing, float64(c.SourceWidth), float64(c.SourceHeight), W, H, float64(f-c.From))
		place := func(b vision.Box) rect {
			return rect{left + b[0]*w, top + b[1]*h, left + (b[0]+b[2])*w, top + (b[1]+b[3])*h}
		}
		if what == "faces" {
			if fs := faceSampleNear(idx, srcT); fs != nil {
				for _, b := range fs.Boxes {
					vis := place(b).and(frameR)
					if vis.area() > 0 && vis.and(zone).area()/vis.area() >= faceCoverShare {
						return true
					}
				}
			}
			continue
		}
		for _, t := range idx.Text {
			if srcT >= t.From && srcT < t.To {
				tr := place(t.Box).and(frameR)
				if tr.area() > 0 && tr.and(zone).area()/tr.area() >= textCoverShare {
					return true
				}
			}
		}
	}
	return false
}

// graphicAway moves a graphic to a moment nearby where it covers no face, when
// there is one in its clip, or says what else helps.
func (r *run) graphicAway(s stretch, o *editspec.PlanOverlay) *models.QAFix {
	if o == nil || r.specOverlay(o.ID) == nil || r.overlayLocked(o.ID) {
		return &models.QAFix{Summary: "Move the graphic to a moment when it covers no face, or crop the clip so the face sits higher."}
	}
	return &models.QAFix{Summary: fmt.Sprintf("Move %s to a moment when nobody's face is where it sits, or crop the clip so the face is higher in the picture.", graphicName(*o))}
}

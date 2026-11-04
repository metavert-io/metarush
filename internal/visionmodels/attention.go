package visionmodels

import (
	"errors"
	"math"
	"sort"

	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/vision"
)

// Where the eye is drawn: U²-Net-p (Qin et al., "U²-Net: Going Deeper with
// Nested U-Structure for Salient Object Detection", Pattern Recognition 2020;
// Apache-2.0) draws a map of what stands out in the frame, 0–1 per pixel at
// 320×320. Apple Vision's attention map is trained on where people look;
// U²-Net-p's on what stands out as an object, which is what framing that
// follows movement needs when no face is seen (D115): the middle of what
// stands out and the region that stands out most. The heat map's middle and
// spread are worked out as the helper works them out (weighted by the square
// of the heat, so the hottest places decide).

const salSide = 320

// salObject: a region of the map at least this hot is an object; the three
// biggest are kept, like the helper's salient objects.
const salObject = 0.5

type saliency struct{ m *onnx.Model }

// attention is where the eye is drawn in a picture.
func (s *saliency) attention(p *picture) (*vision.Attention, error) {
	q := p.resized(salSide, salSide)
	// rembg's U²-Net input: the picture over its brightest value, then
	// ImageNet's mean and spread.
	n := imageNet
	if peak := maxValue(q.px); peak > 0 {
		n.scale = 1 / peak
	}
	outs, err := s.m.Run(q.tensor(n, salSide, salSide), []int64{1, 3, salSide, salSide})
	if err != nil {
		return nil, err
	}
	if len(outs) == 0 || len(outs[0].Data) < salSide*salSide {
		return nil, errors.New("the attention model answered in an unexpected shape")
	}
	return attentionOf(outs[0].Data[:salSide*salSide], salSide, salSide), nil
}

func maxValue(v []float32) float32 {
	m := float32(0)
	for _, x := range v {
		if x > m {
			m = x
		}
	}
	return m
}

// attentionOf reads a heat map (0–1, w×h, rows from the top) as the helper
// does: its weighted middle and spread, its peak, and its hottest regions.
func attentionOf(heat []float32, w, h int) *vision.Attention {
	out := &vision.Attention{Objects: []vision.Detection{}}
	var sw, sx, sy, peak float64
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := math.Max(0, float64(heat[y*w+x]))
			peak = math.Max(peak, v)
			wt := v * v
			sw += wt
			sx += wt * (float64(x) + 0.5) / float64(w)
			sy += wt * (float64(y) + 0.5) / float64(h)
		}
	}
	out.Peak = r3(math.Min(1, peak))
	if sw > 0 {
		cx, cy := sx/sw, sy/sw
		var vx, vy float64
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				v := math.Max(0, float64(heat[y*w+x]))
				wt := v * v
				dx, dy := (float64(x)+0.5)/float64(w)-cx, (float64(y)+0.5)/float64(h)-cy
				vx += wt * dx * dx
				vy += wt * dy * dy
			}
		}
		out.At = [2]float64{r3(cx), r3(cy)}
		out.Spread = [2]float64{r3(math.Sqrt(vx / sw)), r3(math.Sqrt(vy / sw))}
	}
	regs := components(heat, w, h, salObject)
	type obj struct {
		r    region
		mass float64
		mean float64
	}
	var objs []obj
	for _, r := range regs {
		var m float64
		k := 0
		for y := r.y0; y <= r.y1; y++ {
			for x := r.x0; x <= r.x1; x++ {
				if v := float64(heat[y*w+x]); v > salObject {
					m += v
					k++
				}
			}
		}
		if k < 4 {
			continue // a speck
		}
		objs = append(objs, obj{r, m, m / float64(k)})
	}
	sort.SliceStable(objs, func(i, j int) bool { return objs[i].mass > objs[j].mass })
	for i, o := range objs {
		if i == 3 {
			break
		}
		out.Objects = append(out.Objects, vision.Detection{Conf: r3(o.mean),
			Box: vision.Box{r3(float64(o.r.x0) / float64(w)), r3(float64(o.r.y0) / float64(h)),
				r3(float64(o.r.x1-o.r.x0+1) / float64(w)), r3(float64(o.r.y1-o.r.y0+1) / float64(h))}})
	}
	return out
}

// faceAttention is where the eye is drawn in a frame with faces: the faces,
// the biggest weighing most (by the square of their area, as the heat of the
// helper's map does), as strong as the surest of them, the three biggest as
// the regions that stand out.
func faceAttention(faces []vision.Face) *vision.Attention {
	out := &vision.Attention{Objects: []vision.Detection{}}
	var sw, sx, sy float64
	order := make([]int, len(faces))
	for i, f := range faces {
		order[i] = i
		a := f.Box[2] * f.Box[3]
		w := a * a
		sw += w
		sx += w * (f.Box[0] + f.Box[2]/2)
		sy += w * (f.Box[1] + f.Box[3]/2)
		out.Peak = math.Max(out.Peak, f.Conf)
	}
	if sw <= 0 {
		return out
	}
	cx, cy := sx/sw, sy/sw
	var vx, vy float64
	for _, f := range faces {
		a := f.Box[2] * f.Box[3]
		w := a * a
		dx, dy := f.Box[0]+f.Box[2]/2-cx, f.Box[1]+f.Box[3]/2-cy
		// A face's own size spreads the heat (a uniform box: width²/12).
		vx += w * (dx*dx + f.Box[2]*f.Box[2]/12)
		vy += w * (dy*dy + f.Box[3]*f.Box[3]/12)
	}
	out.At = [2]float64{r3(cx), r3(cy)}
	out.Spread = [2]float64{r3(math.Sqrt(vx / sw)), r3(math.Sqrt(vy / sw))}
	out.Peak = r3(out.Peak)
	sort.SliceStable(order, func(i, j int) bool {
		return faces[order[i]].Box[2]*faces[order[i]].Box[3] > faces[order[j]].Box[2]*faces[order[j]].Box[3]
	})
	for k, i := range order {
		if k == 3 {
			break
		}
		out.Objects = append(out.Objects, vision.Detection{Conf: faces[i].Conf, Box: faces[i].Box})
	}
	return out
}

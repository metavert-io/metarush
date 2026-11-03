package visionmodels

import (
	"errors"
	"fmt"
	"math"
	"sort"

	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/vision"
)

// YuNet (Wu, Peng and Yu, "YuNet: A Tiny Millisecond-level Face Detector",
// Machine Intelligence Research 2023; OpenCV Zoo's face_detection_yunet, MIT)
// finds faces with their five points — the eyes, the tip of the nose and the
// mouth corners — the points face recognition aligns a face by (D406). It
// reads blue, green and red at 0–255 at any size that is a multiple of 32 and
// answers, at strides 8, 16 and 32, a score, a box and five points per cell;
// this is OpenCV's FaceDetectorYN decoding, written in Go.

// YuNet sees faces of about 10 to 300 pixels (its training), so a frame is
// looked at as it is and at half its size, which finds faces up to about 600
// pixels tall, then the same face found twice is kept once.
var faceScales = []float64{1, 0.5}

const (
	// faceCandidate is the lowest score a face is kept at before faces seen
	// twice are merged; FaceMin is the lowest a frame reports (D407: chosen
	// against Apple Vision's faces on the fixtures and the faceset).
	faceCandidate = 0.5
	// faceNMS: two boxes that overlap this much are one face.
	faceNMS = 0.3
	// maxFaces bounds the faces of one frame.
	maxFaces = 64
)

// FaceMin is the lowest YuNet score at which a face counts.
const FaceMin = 0.6

// A shape YuNet is unsure of (under blobScore) whose aligned face has no
// detail at all (sharpness under blobSharp) isn't a face: a call's blurred,
// enlarged copy of the picture behind it shows round shapes YuNet scores
// 0.50–0.66 at a sharpness under 2, which Apple Vision never takes for faces.
// Of the 806 shapes YuNet found in the fixtures' frames and the faceset, each
// was under 2 (those shapes, and specks a few pixels wide) or 9 and over
// (D407).
const (
	blobScore = 0.7
	blobSharp = 5.0
)

// featureless says a shape found as a face is one of those.
func featureless(score, sharpness float64) bool { return score < blobScore && sharpness < blobSharp }

// faceFinder runs YuNet.
type faceFinder struct {
	m *onnx.Model
	// out maps cls_8, obj_16, bbox_32, kps_8… to the model's outputs.
	out map[string]int
}

func newFaceFinder(m *onnx.Model) (*faceFinder, error) {
	f := &faceFinder{m: m, out: map[string]int{}}
	for i, n := range m.Outputs {
		f.out[n] = i
	}
	for _, s := range []int{8, 16, 32} {
		for _, k := range []string{"cls", "obj", "bbox", "kps"} {
			if _, ok := f.out[fmt.Sprintf("%s_%d", k, s)]; !ok {
				return nil, fmt.Errorf("the face model has no output %s_%d", k, s)
			}
		}
	}
	return f, nil
}

// faceAt is one face found, in pixels of the frame.
type faceAt struct {
	box   [4]float64 // x, y, w, h (YuNet's box: brow line to chin, ears out)
	pts   [5][2]float64
	score float64
}

// find looks for faces in a picture at each scale.
func (f *faceFinder) find(p *picture) ([]faceAt, error) {
	var all []faceAt
	for _, s := range faceScales {
		w, h := int(math.Round(float64(p.w)*s)), int(math.Round(float64(p.h)*s))
		if s < 1 && min(w, h) < 64 {
			continue // too small to show a face the full size didn't
		}
		q := p.resized(w, h)
		got, err := f.run(q)
		if err != nil {
			return nil, err
		}
		// Back to the frame's pixels (pixel centres aligned).
		fx, fy := float64(p.w)/float64(w), float64(p.h)/float64(h)
		for _, g := range got {
			if s != 1 {
				g.box = [4]float64{(g.box[0]+0.5)*fx - 0.5, (g.box[1]+0.5)*fy - 0.5, g.box[2] * fx, g.box[3] * fy}
				for k := range g.pts {
					g.pts[k] = [2]float64{(g.pts[k][0]+0.5)*fx - 0.5, (g.pts[k][1]+0.5)*fy - 0.5}
				}
			}
			all = append(all, g)
		}
	}
	return nms(all, faceNMS, maxFaces), nil
}

// run is one pass of YuNet over a picture (padded to a multiple of 32 at the
// right and bottom, as OpenCV pads it).
func (f *faceFinder) run(p *picture) ([]faceAt, error) {
	W, H := roundUp(p.w, 32), roundUp(p.h, 32)
	outs, err := f.m.Run(p.tensor(rawBGR, W, H), []int64{1, 3, int64(H), int64(W)})
	if err != nil {
		return nil, err
	}
	return decodeYuNet(func(name string) []float32 {
		if i, ok := f.out[name]; ok && i < len(outs) {
			return outs[i].Data
		}
		return nil
	}, W, H)
}

// decodeYuNet turns YuNet's answer for a W×H input (out names a tensor's
// values) into faces: at each stride, a cell's score is √(class × object),
// its box is offset from the cell and sized by exp() of the answer, and its
// five points are offsets from the cell, all in strides (OpenCV's
// FaceDetectorYN::postProcess).
func decodeYuNet(out func(name string) []float32, W, H int) ([]faceAt, error) {
	var faces []faceAt
	for _, s := range []int{8, 16, 32} {
		cols, rows := W/s, H/s
		cls, obj := out(fmt.Sprintf("cls_%d", s)), out(fmt.Sprintf("obj_%d", s))
		box, kps := out(fmt.Sprintf("bbox_%d", s)), out(fmt.Sprintf("kps_%d", s))
		if len(cls) < rows*cols || len(obj) < rows*cols || len(box) < 4*rows*cols || len(kps) < 10*rows*cols {
			return nil, errors.New("the face model answered in an unexpected shape")
		}
		st := float64(s)
		for r := 0; r < rows; r++ {
			for c := 0; c < cols; c++ {
				i := r*cols + c
				score := math.Sqrt(clamp01(float64(cls[i])) * clamp01(float64(obj[i])))
				if score < faceCandidate {
					continue
				}
				cx, cy := (float64(c)+float64(box[4*i]))*st, (float64(r)+float64(box[4*i+1]))*st
				bw, bh := math.Exp(float64(box[4*i+2]))*st, math.Exp(float64(box[4*i+3]))*st
				fa := faceAt{box: [4]float64{cx - bw/2, cy - bh/2, bw, bh}, score: score}
				for n := 0; n < 5; n++ {
					fa.pts[n] = [2]float64{(float64(kps[10*i+2*n]) + float64(c)) * st, (float64(kps[10*i+2*n+1]) + float64(r)) * st}
				}
				faces = append(faces, fa)
			}
		}
	}
	return nms(faces, faceNMS, maxFaces), nil
}

// nms keeps the best of faces that overlap, best first.
func nms(faces []faceAt, overlap float64, keep int) []faceAt {
	sort.SliceStable(faces, func(i, j int) bool { return faces[i].score > faces[j].score })
	var out []faceAt
	for _, f := range faces {
		dup := false
		for _, k := range out {
			if boxIoU(f.box, k.box) > overlap {
				dup = true
				break
			}
		}
		if !dup {
			out = append(out, f)
			if len(out) == keep {
				break
			}
		}
	}
	return out
}

func boxIoU(a, b [4]float64) float64 {
	x1, y1 := math.Max(a[0], b[0]), math.Max(a[1], b[1])
	x2, y2 := math.Min(a[0]+a[2], b[0]+b[2]), math.Min(a[1]+a[3], b[1]+b[3])
	inter := math.Max(0, x2-x1) * math.Max(0, y2-y1)
	u := a[2]*a[3] + b[2]*b[3] - inter
	if u <= 0 {
		return 0
	}
	return inter / u
}

// ordered puts the five points in the order face recognition aligns by, as
// the on-screen helper writes them: the eye on the picture's left, the other
// eye, the nose tip, then the mouth corners along the line between the eyes
// (YuNet already answers in that order for an upright face; a face upside
// down or turned is put right the same way the helper does it).
func ordered(p [5][2]float64) [5][2]float64 {
	l, r := p[0], p[1]
	if l[0] > r[0] {
		l, r = r, l
	}
	ax, ay := r[0]-l[0], r[1]-l[1]
	n := math.Max(1e-6, math.Hypot(ax, ay))
	ux, uy := ax/n, ay/n
	m1, m2 := p[3], p[4]
	if m1[0]*ux+m1[1]*uy > m2[0]*ux+m2[1]*uy {
		m1, m2 = m2, m1
	}
	return [5][2]float64{l, r, p[2], m1, m2}
}

// toFace is a face found as the helper says it: 0–1 from the top-left, the
// box in Apple Vision's terms (calibrated, D407), the five points, the score,
// and how usable the face is as a picture.
func toFace(fa faceAt, w, h int, quality float64) vision.Face {
	pts := ordered(fa.pts)
	box := appleBox(fa.box, pts)
	W, H := float64(w), float64(h)
	f := vision.Face{
		Conf: r3(fa.score),
		Box:  vision.Box{r3(box[0] / W), r3(box[1] / H), r3(box[2] / W), r3(box[3] / H)},
		LMC:  r3(fa.score),
	}
	q := r3(quality)
	f.Quality = &q
	for _, pt := range pts {
		// Pixel centres on whole numbers (faceid.Points takes them back exactly).
		f.LM = append(f.LM, [2]float64{r4((pt[0] + 0.5) / W), r4((pt[1] + 0.5) / H)})
	}
	roll := r3(math.Atan2(pts[1][1]-pts[0][1], pts[1][0]-pts[0][0]))
	f.Roll = &roll
	return f
}

// Apple Vision's face box is a square from about the eyebrows to the chin;
// YuNet's is taller and narrower (hairline to chin). The index keeps Apple
// Vision's meaning (shot types, the on-screen checks and framing read box
// sizes): the square's side and its middle come from YuNet's box by the
// ratios measured between the two on the same faces (D407).
const (
	appleSide   = 0.89 // the square's side, as a share of YuNet's box height
	appleCenter = 0.08 // its middle lies this share of YuNet's height lower
)

// appleBox is YuNet's box as Apple Vision would draw it, in pixels.
func appleBox(b [4]float64, _ [5][2]float64) [4]float64 {
	side := appleSide * b[3]
	cx := b[0] + b[2]/2
	cy := b[1] + b[3]/2 + appleCenter*b[3]
	return [4]float64{cx - side/2, cy - side/2, side, side}
}

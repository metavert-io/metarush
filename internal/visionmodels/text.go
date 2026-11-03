package visionmodels

import (
	"errors"
	"math"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/onnx"
	"github.com/metavert-io/metarush/internal/vision"
)

// Text on screen is read with PaddleOCR's PP-OCR models (Apache-2.0, D406): a
// detector draws a map of where text is, the map's text lines become boxes
// (PaddleOCR's DB post-processing, with the line's box kept upright as the
// helper's boxes are), and a recognizer reads each line as a sequence of
// characters (CTC, greedy). This is PaddleOCR's own pipeline, written in Go.

// textFinder runs the detector and the recognizer.
type textFinder struct {
	det, rec *onnx.Model
	// chars are the recognizer's characters: index 0 is CTC's blank, then
	// the dictionary, then a space.
	chars []string
	// The detector's settings (its inference.yml).
	thresh, boxThresh, unclip float64
}

const (
	// detSide: the detector reads the frame with its long side at most this
	// (frames are 960 wide; PaddleOCR's own setting).
	detSide = 960
	// recHeight is the height the recognizer reads a line at; recMinWidth
	// the width it pads short lines to; recMaxRatio caps a line's width.
	recHeight   = 48
	recMinWidth = 320
	recMaxRatio = 40.0
	// textMinHeight: lines shorter than this share of the picture's height
	// are left out (the helper's minimumTextHeight).
	textMinHeight = 0.02
	// maxLines bounds the lines read in one frame.
	maxLines = 60
)

func newTextFinder(det, rec *onnx.Model, chars []string, thresh, boxThresh, unclip float64) (*textFinder, error) {
	if len(chars) < 3 {
		return nil, errors.New("the text recognizer has no characters")
	}
	return &textFinder{det: det, rec: rec, chars: chars, thresh: thresh, boxThresh: boxThresh, unclip: unclip}, nil
}

// line is a text line found, in pixels of the frame.
type line struct {
	x0, y0, x1, y1 float64
	score          float64
}

// read finds and reads the text lines of a picture.
func (t *textFinder) read(p *picture) ([]vision.Text, error) {
	lines, err := t.find(p)
	if err != nil {
		return nil, err
	}
	var out []vision.Text
	for _, l := range lines {
		if (l.y1-l.y0)/float64(p.h) < textMinHeight {
			continue
		}
		s, conf, err := t.recognize(p, l)
		if err != nil {
			return nil, err
		}
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		out = append(out, vision.Text{S: s, Conf: r3(calibratedText(conf, s)),
			Box: vision.Box{r3(l.x0 / float64(p.w)), r3(l.y0 / float64(p.h)), r3((l.x1 - l.x0) / float64(p.w)), r3((l.y1 - l.y0) / float64(p.h))}})
	}
	return out, nil
}

// find runs the detector and turns its map into upright line boxes.
func (t *textFinder) find(p *picture) ([]line, error) {
	ratio := 1.0
	if long := max(p.w, p.h); long > detSide {
		ratio = float64(detSide) / float64(long)
	}
	W := max(32, int(math.Round(float64(p.w)*ratio/32))*32)
	H := max(32, int(math.Round(float64(p.h)*ratio/32))*32)
	q := p.resized(W, H)
	outs, err := t.det.Run(q.tensor(paddleDet, W, H), []int64{1, 3, int64(H), int64(W)})
	if err != nil {
		return nil, err
	}
	if len(outs) == 0 || len(outs[0].Data) < W*H {
		return nil, errors.New("the text detector answered in an unexpected shape")
	}
	prob := outs[0].Data[:W*H]
	sx, sy := float64(p.w)/float64(W), float64(p.h)/float64(H)
	var lines []line
	for _, c := range components(prob, W, H, float32(t.thresh)) {
		if len(lines) >= 1000 {
			break
		}
		w, h := float64(c.x1-c.x0+1), float64(c.y1-c.y0+1)
		if math.Min(w, h) < 3 {
			continue
		}
		// The line's score: the map's mean over its box (PaddleOCR's fast mode).
		var sum float64
		for y := c.y0; y <= c.y1; y++ {
			for x := c.x0; x <= c.x1; x++ {
				sum += float64(prob[y*W+x])
			}
		}
		score := sum / (w * h)
		if score < t.boxThresh {
			continue
		}
		// The map marks a line's shrunken middle: grow it back by
		// area × ratio / perimeter on every side (DB's unclip).
		d := w * h * t.unclip / (2 * (w + h))
		x0, y0, x1, y1 := float64(c.x0)-d, float64(c.y0)-d, float64(c.x1+1)+d, float64(c.y1+1)+d
		if math.Min(x1-x0, y1-y0) < 5 {
			continue
		}
		lines = append(lines, line{
			x0: math.Max(0, x0*sx), y0: math.Max(0, y0*sy),
			x1: math.Min(float64(p.w), x1*sx), y1: math.Min(float64(p.h), y1*sy), score: score})
	}
	// Top to bottom, left to right (the order the helper reads in).
	sort.SliceStable(lines, func(i, j int) bool {
		if math.Abs(lines[i].y0-lines[j].y0) > 10 {
			return lines[i].y0 < lines[j].y0
		}
		return lines[i].x0 < lines[j].x0
	})
	if len(lines) > maxLines {
		sort.SliceStable(lines, func(i, j int) bool { return lines[i].score > lines[j].score })
		lines = lines[:maxLines]
	}
	return lines, nil
}

// region is a connected part of the map above the threshold.
type region struct {
	x0, y0, x1, y1 int
	n              int
}

// components labels the map's pixels above thr (8-connected) and returns each
// region's box.
func components(prob []float32, W, H int, thr float32) []region {
	seen := make([]bool, W*H)
	var out []region
	stack := make([]int, 0, 1024)
	for i := range prob {
		if seen[i] || prob[i] <= thr {
			continue
		}
		r := region{x0: i % W, y0: i / W, x1: i % W, y1: i / W}
		stack = append(stack[:0], i)
		seen[i] = true
		for len(stack) > 0 {
			k := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			x, y := k%W, k/W
			r.n++
			r.x0, r.x1, r.y0, r.y1 = min(r.x0, x), max(r.x1, x), min(r.y0, y), max(r.y1, y)
			for dy := -1; dy <= 1; dy++ {
				for dx := -1; dx <= 1; dx++ {
					nx, ny := x+dx, y+dy
					if (dx == 0 && dy == 0) || nx < 0 || ny < 0 || nx >= W || ny >= H {
						continue
					}
					j := ny*W + nx
					if !seen[j] && prob[j] > thr {
						seen[j] = true
						stack = append(stack, j)
					}
				}
			}
		}
		out = append(out, r)
	}
	return out
}

// recognize reads one line: its crop at the recognizer's height (a crop
// taller than wide is turned, as PaddleOCR does), padded to its width.
func (t *textFinder) recognize(p *picture, l line) (string, float64, error) {
	crop := p.crop(int(math.Floor(l.x0)), int(math.Floor(l.y0)), int(math.Ceil(l.x1)), int(math.Ceil(l.y1)))
	if crop.w < 2 || crop.h < 2 {
		return "", 0, nil
	}
	if float64(crop.h)/float64(crop.w) >= 1.5 {
		crop = crop.turned()
	}
	ratio := math.Min(recMaxRatio, float64(crop.w)/float64(crop.h))
	w := max(1, int(math.Ceil(recHeight*ratio)))
	W := max(recMinWidth, w)
	in := crop.resized(w, recHeight).tensor(paddleRec, W, recHeight)
	outs, err := t.rec.Run(in, []int64{1, 3, recHeight, int64(W)})
	if err != nil {
		return "", 0, err
	}
	if len(outs) == 0 || len(outs[0].Shape) != 3 {
		return "", 0, errors.New("the text recognizer answered in an unexpected shape")
	}
	steps, classes := int(outs[0].Shape[1]), int(outs[0].Shape[2])
	if classes != len(t.chars) || len(outs[0].Data) < steps*classes {
		return "", 0, errors.New("the text recognizer doesn't match its characters")
	}
	return ctcGreedy(outs[0].Data, steps, classes, t.chars)
}

// ctcGreedy reads the most likely character at every step, drops repeats and
// blanks, and scores the line by the mean probability of the characters kept
// (PaddleOCR's CTCLabelDecode).
func ctcGreedy(probs []float32, steps, classes int, chars []string) (string, float64, error) {
	var b strings.Builder
	var sum float64
	n, prev := 0, -1
	for s := 0; s < steps; s++ {
		row := probs[s*classes : (s+1)*classes]
		best, bp := 0, row[0]
		for k := 1; k < classes; k++ {
			if row[k] > bp {
				best, bp = k, row[k]
			}
		}
		if best != prev && best != 0 {
			b.WriteString(chars[best])
			sum += float64(bp)
			n++
		}
		prev = best
	}
	if n == 0 {
		return "", 0, nil
	}
	return b.String(), sum / float64(n), nil
}

// turned is the picture turned a quarter to the left (a vertical line reads
// left to right).
func (p *picture) turned() *picture {
	out := &picture{w: p.h, h: p.w, px: make([]float32, len(p.px))}
	for y := 0; y < p.h; y++ {
		for x := 0; x < p.w; x++ {
			// (x, y) → (y, w−1−x)
			d := 3 * ((p.w-1-x)*out.w + y)
			s := 3 * (y*p.w + x)
			copy(out.px[d:d+3], p.px[s:s+3])
		}
	}
	return out
}

// calibratedText puts PaddleOCR's line score on the scale of the helper's
// text confidence (D407): Apple Vision says 1 for a line it reads cleanly and
// 0.3–0.5 for a misreading, and the index and the names shown in the video
// read text by those numbers (a lone reading counts from 0.6, a name from
// 0.8). PaddleOCR's mean character probability is above 0.95 for clean lines
// and falls through the 0.8s when characters are guessed; very short lines
// are less certain than their score says.
func calibratedText(score float64, s string) float64 {
	v := clamp01((score - textScoreFloor) / (textScoreClean - textScoreFloor))
	if n := utf8.RuneCountInString(strings.ReplaceAll(s, " ", "")); n < 3 {
		v *= 0.8
	}
	return v
}

const (
	textScoreFloor = 0.55 // a line scored this or less is a guess (0)
	textScoreClean = 0.95 // a line scored this or more reads clean (1)
)

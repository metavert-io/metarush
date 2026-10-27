package visual

import (
	"context"
	"fmt"
	"image"
	"image/jpeg"
	"math"
	"os"
	"path/filepath"
	"sort"

	"github.com/metavert-io/metarush/internal/faceid"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
)

// Sampling: about one frame a second (every 2 s past an hour), plus the moments
// the picture changes. Frames are 960 px wide (on-screen text needs the pixels).
const (
	batchFrames = 60
	frameWidth  = 960
	maxRegular  = 3600
)

// StepFor is the spacing of the regular samples for a clip of dur seconds.
func StepFor(dur float64) float64 {
	step := 1.0
	for dur/step > maxRegular {
		step *= 2
	}
	return step
}

// batchArgs are ffmpeg's arguments for n frames at start, start+step, … in one
// decode (input seeking is exact). round=up makes each frame the one on screen
// at its moment: by default the fps filter keeps the last frame before the
// moment half a step later, so a frame just before a cut showed the next shot
// (the next speaker's face in a call's speaker view).
func batchArgs(src string, start float64, n int, step float64, scale, pattern string) []string {
	return []string{"-ss", fmt.Sprintf("%.3f", start), "-i", src, "-t", fmt.Sprintf("%.3f", float64(n)*step), "-map", "0:v:0",
		"-vf", fmt.Sprintf("fps=1/%g:round=up,%s", step, scale), "-frames:v", fmt.Sprint(n), "-q:v", "3", pattern}
}

// SampleTimes lists the regular sample times plus extra ones (visual changes)
// that are not within a third of a step of a regular one.
func SampleTimes(dur, step float64, extra []float64) (regular, more []float64) {
	end := dur - 0.05
	for t := step / 2; t < end; t += step {
		regular = append(regular, math.Round(t*1000)/1000)
	}
	if len(regular) == 0 && dur > 0 {
		regular = []float64{math.Round(dur/2*1000) / 1000}
	}
	sort.Float64s(extra)
	for _, t := range extra {
		if t < 0 || t > end {
			continue
		}
		k := math.Round((t - step/2) / step)
		if math.Abs(t-(k*step+step/2)) <= step/3 {
			continue
		}
		if n := len(more); n > 0 && t-more[n-1] < step/3 {
			continue
		}
		more = append(more, math.Round(t*1000)/1000)
	}
	return regular, more
}

// Progress is told how many frames have been looked at.
type Progress func(done, total int)

// Printer makes a face fingerprint from a frame and the face's five points
// (pixels): faceid.Embedder.
type Printer interface {
	Embed(img image.Image, pts [5][2]float64) ([]float32, error)
}

// RunOptions are what an index run asks for beyond the basics.
type RunOptions struct {
	// Prints makes face fingerprints (nil: faces are found but not fingerprinted).
	Prints Printer
}

// Faces smaller than faceid.MinEyes between the eyes (pixels of the frame
// looked at), or with a capture quality below printQuality, get no
// fingerprint: they are mostly blur, and a wrong fingerprint is worse than none.
const (
	printQuality = 0.15
	printPerShot = 4 // faces fingerprinted per frame, biggest first
)

// printFaces fingerprints the usable faces of a frame whose landmarks were found.
func printFaces(p Printer, path string, f vision.Frame) [][]float32 {
	var want []int
	for k, fc := range f.Faces {
		if fc.Conf < faceMin || len(fc.LM) != 5 || (fc.Quality != nil && *fc.Quality < printQuality) {
			continue
		}
		want = append(want, k)
	}
	if len(want) == 0 {
		return nil
	}
	sort.SliceStable(want, func(i, j int) bool { return f.Faces[want[i]].Box[3] > f.Faces[want[j]].Box[3] })
	if len(want) > printPerShot {
		want = want[:printPerShot]
	}
	fh, err := os.Open(path)
	if err != nil {
		return nil
	}
	img, err := jpeg.Decode(fh)
	fh.Close()
	if err != nil {
		return nil
	}
	b := img.Bounds()
	out := make([][]float32, len(f.Faces))
	n := 0
	for _, k := range want {
		pts, ok := faceid.Points(f.Faces[k].LM, b.Dx(), b.Dy())
		if !ok || faceid.EyeDistance(pts) < faceid.MinEyes {
			continue
		}
		if v, err := p.Embed(img, pts); err == nil {
			out[k] = v
			n++
		}
	}
	if n == 0 {
		return nil
	}
	return out
}

// Run extracts the frames of a local video, looks at them with the helper and
// builds the index.
func Run(ctx context.Context, ts *tools.Set, src string, dur float64, extra, cuts []float64, source, scratch string, progress Progress, o RunOptions) (*Index, error) {
	if !ts.Has(tools.FFmpeg) {
		return nil, &tools.ErrMissing{Name: tools.FFmpeg}
	}
	step := StepFor(dur)
	regular, more := SampleTimes(dur, step, extra)
	total := len(regular) + len(more)
	if progress == nil {
		progress = func(int, int) {}
	}
	var samples []Sample
	vo := vision.Options{AnyLanguage: true, Landmarks: o.Prints != nil, Attention: true, Hands: true, Pose: true}
	look := func(dir string, at map[string]float64) error {
		return vision.Analyze(ctx, ts, dir, vo, func(f vision.Frame) {
			if t, ok := at[f.File]; ok {
				s := Sample{At: t, Frame: f}
				if o.Prints != nil && f.Error == "" {
					// The frame is still on disk: the helper answers one frame at a time.
					s.Prints = printFaces(o.Prints, filepath.Join(dir, f.File), f)
				}
				samples = append(samples, s)
			}
		})
	}
	ff := func(args ...string) error {
		_, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: append([]string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error"}, args...), Background: true})
		return err
	}
	scale := fmt.Sprintf("scale='if(gt(iw,ih),%d,-2)':'if(gt(iw,ih),-2,%d)'", frameWidth, frameWidth)
	done := 0
	for b := 0; b < len(regular); b += batchFrames {
		n := min(batchFrames, len(regular)-b)
		dir, err := os.MkdirTemp(scratch, "look-")
		if err != nil {
			return nil, err
		}
		err = ff(batchArgs(src, regular[b], n, step, scale, filepath.Join(dir, "r%05d.jpg"))...)
		if err != nil {
			os.RemoveAll(dir)
			return nil, err
		}
		at := map[string]float64{}
		for j := 0; j < n; j++ {
			at[fmt.Sprintf("r%05d.jpg", j+1)] = regular[b+j]
		}
		err = look(dir, at)
		os.RemoveAll(dir)
		if err != nil {
			return nil, err
		}
		done += n
		progress(done, total)
	}
	if len(more) > 0 {
		dir, err := os.MkdirTemp(scratch, "look-")
		if err != nil {
			return nil, err
		}
		defer os.RemoveAll(dir)
		at := map[string]float64{}
		for j, t := range more {
			name := fmt.Sprintf("x%05d.jpg", j+1)
			if err := ff("-ss", fmt.Sprintf("%.3f", t), "-i", src, "-frames:v", "1", "-map", "0:v:0", "-vf", scale, "-q:v", "3", filepath.Join(dir, name)); err != nil {
				return nil, err
			}
			at[name] = t
		}
		if err := look(dir, at); err != nil {
			return nil, err
		}
		done += len(more)
		progress(done, total)
	}
	return Build(samples, step, dur, cuts, source), nil
}

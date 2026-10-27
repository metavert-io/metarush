package fixes

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"strconv"

	"github.com/metavert-io/metarush/internal/tools"
)

// Decoding for the measurements: ffmpeg writes raw frames or samples to a
// pipe, read here. Every argument is MetaRush's own (file paths it chose and
// numbers).

func secArg(v float64) string { return strconv.FormatFloat(math.Max(0, v), 'f', 3, 64) }

// frameAt decodes one frame at t seconds as raw pixels: gray (1 byte a pixel)
// or rgb24 (3), scaled to w×h (w or h may be -2 to keep the shape; 0 keeps the
// size). It returns the pixels and the size.
func frameAt(ctx context.Context, ts *tools.Set, src string, t float64, w, h int, rgb bool) ([]byte, int, int, error) {
	pix := "gray"
	bpp := 1
	if rgb {
		pix, bpp = "rgb24", 3
	}
	vf := "format=" + pix
	if w != 0 || h != 0 {
		vf = fmt.Sprintf("scale=%d:%d:flags=area,%s", w, h, vf)
	}
	// The size comes from a second, tiny run that prints it (ffprobe-free):
	// ask for one frame as PAM, whose header says the size.
	var out bytes.Buffer
	_, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Stdout: &out,
		Args: []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-ss", secArg(t), "-i", src, "-frames:v", "1", "-an", "-vf", vf, "-c:v", "pam", "-f", "image2pipe", "-"}})
	if err != nil {
		return nil, 0, 0, err
	}
	data, fw, fh, err := readPAM(out.Bytes(), bpp)
	return data, fw, fh, err
}

// readPAM reads one PAM image (P7) with depth bpp.
func readPAM(b []byte, bpp int) ([]byte, int, int, error) {
	var w, h, depth int
	rest := b
	for {
		i := bytes.IndexByte(rest, '\n')
		if i < 0 {
			return nil, 0, 0, fmt.Errorf("not a PAM image")
		}
		line := string(rest[:i])
		rest = rest[i+1:]
		var v int
		switch {
		case line == "ENDHDR":
			if w <= 0 || h <= 0 || depth != bpp || len(rest) < w*h*bpp {
				return nil, 0, 0, fmt.Errorf("unexpected PAM image %dx%dx%d (%d bytes)", w, h, depth, len(rest))
			}
			return rest[:w*h*bpp], w, h, nil
		case sscan(line, "WIDTH %d", &v):
			w = v
		case sscan(line, "HEIGHT %d", &v):
			h = v
		case sscan(line, "DEPTH %d", &v):
			depth = v
		}
	}
}

func sscan(line, format string, v *int) bool {
	n, err := fmt.Sscanf(line, format, v)
	return err == nil && n == 1
}

// grayRun decodes a stretch of video (from, for dur seconds) as gray frames
// scaled to width w (height keeping the shape, rounded to even), all of them
// at the video's own rate. It returns the frames and their size.
func grayRun(ctx context.Context, ts *tools.Set, src string, from, dur float64, w int) ([][]byte, int, int, error) {
	first, fw, fh, err := frameAt(ctx, ts, src, from, w, -2, false)
	if err != nil {
		return nil, 0, 0, err
	}
	_ = first
	var out bytes.Buffer
	_, err = ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Stdout: &out,
		Args: []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-ss", secArg(from), "-t", secArg(dur), "-i", src, "-an",
			"-vf", fmt.Sprintf("scale=%d:%d:flags=area,format=gray", fw, fh), "-fps_mode", "passthrough", "-f", "rawvideo", "-"}})
	if err != nil {
		return nil, 0, 0, err
	}
	b := out.Bytes()
	n := fw * fh
	var frames [][]byte
	for len(b) >= n {
		frames = append(frames, b[:n])
		b = b[n:]
	}
	return frames, fw, fh, nil
}

// audioLevels decodes a file's sound (mono, 16 kHz) and returns its level
// every step seconds, in dB below full scale (-120 for digital silence).
func audioLevels(ctx context.Context, ts *tools.Set, src string, step float64) ([]float64, error) {
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		_, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Stdout: pw,
			Args: []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-i", src, "-vn", "-map", "0:a:0", "-ac", "1", "-ar", "16000", "-f", "f32le", "-"}})
		pw.CloseWithError(err)
		errc <- err
	}()
	per := int(16000 * step)
	if per < 1 {
		per = 1
	}
	var out []float64
	buf := make([]byte, 4*per)
	for {
		n, err := io.ReadFull(pr, buf)
		if n >= 4 {
			sum := 0.0
			k := n / 4
			for i := 0; i < k; i++ {
				v := float64(math.Float32frombits(binary.LittleEndian.Uint32(buf[4*i:])))
				sum += v * v
			}
			out = append(out, toDB(sum/float64(k)))
		}
		if err != nil {
			break
		}
	}
	if err := <-errc; err != nil {
		return nil, err
	}
	return out, nil
}

// audioSamples decodes up to max seconds of a file's sound, mono at rate.
func audioSamples(ctx context.Context, ts *tools.Set, src string, rate int, from, max float64) ([]float32, error) {
	var out bytes.Buffer
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error"}
	if from > 0 {
		args = append(args, "-ss", secArg(from))
	}
	args = append(args, "-i", src, "-vn", "-map", "0:a:0", "-ac", "1", "-ar", strconv.Itoa(rate))
	if max > 0 {
		args = append(args, "-t", secArg(max))
	}
	args = append(args, "-f", "f32le", "-")
	if _, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Stdout: &out, Args: args}); err != nil {
		return nil, err
	}
	b := out.Bytes()
	s := make([]float32, len(b)/4)
	for i := range s {
		s[i] = math.Float32frombits(binary.LittleEndian.Uint32(b[4*i:]))
	}
	return s, nil
}

// toDB turns a mean square into dB (full scale 0, silence -120).
func toDB(ms float64) float64 {
	if ms <= 1e-12 {
		return -120
	}
	return 10 * math.Log10(ms)
}

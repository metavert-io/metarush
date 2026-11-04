// Package mattingtest stands in for the models that tell people from their
// background (internal/matting) in tests: a model whose "person" is what is
// bright in the frame, and footage made at test time of a bright shape
// moving over a dark background, so a test knows what the matte must say.
package mattingtest

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/metavert-io/metarush/internal/matting"
)

// Model is a stand-in model: a pixel is the person when it is bright. Size
// reads frames at Width×Height (64×36 when unset); FailAt makes the frame
// with that number (from 1) fail; Nobody answers empty mattes.
type Model struct {
	Width, Height int
	FailAt        int
	Nobody        bool
	// Opened counts the streams opened, Frames the frames read.
	Opened, Frames *atomic.Int64
}

// New is a stand-in model with its counters.
func New() *Model { return &Model{Opened: new(atomic.Int64), Frames: new(atomic.Int64)} }

func (m *Model) Size(w, h int) (int, int) {
	if m.Width > 0 && m.Height > 0 {
		return m.Width, m.Height
	}
	return 64, 36
}

func (m *Model) Format() string { return "rgb24" }

func (m *Model) Open(ctx context.Context, mw, mh int) (matting.Stream, error) {
	if m.Opened != nil {
		m.Opened.Add(1)
	}
	return &stream{m: m, ctx: ctx, n: mw * mh}, nil
}

type stream struct {
	m   *Model
	ctx context.Context
	n   int
	at  int
}

// ErrFailed is the stand-in's failure.
var ErrFailed = errors.New("the stand-in model failed")

func (s *stream) Next(frame []byte) ([]byte, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	s.at++
	if s.m.Frames != nil {
		s.m.Frames.Add(1)
	}
	if s.m.FailAt > 0 && s.at == s.m.FailAt {
		return nil, ErrFailed
	}
	out := make([]byte, s.n)
	if s.m.Nobody {
		return out, nil
	}
	for i := range out {
		r, g, b := int(frame[3*i]), int(frame[3*i+1]), int(frame[3*i+2])
		if (r+g+b)/3 > 128 {
			out[i] = 255
		}
	}
	return out, nil
}

func (s *stream) Close() error { return nil }

// Footage writes an MP4 (H.264) of a white box moving right over black, at
// w×h and fps, so long; the test is skipped without ffmpeg.
func Footage(t testing.TB, dir string, seconds float64, w, h, fps int) string {
	t.Helper()
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("ffmpeg isn't installed here")
	}
	out := filepath.Join(dir, "footage.mp4")
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=black:s=%dx%d:r=%d:d=%.3f", w, h, fps, seconds),
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=white:s=%dx%d:r=%d:d=%.3f", w/4, h/2, fps, seconds),
		"-filter_complex", fmt.Sprintf("[0:v][1:v]overlay=x='10+t*%d':y=%d:shortest=1", w/4, h/4),
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-movflags", "+faststart", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("making footage: %v: %s", err, b)
	}
	return out
}

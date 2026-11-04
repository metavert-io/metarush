package matting

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"sync"

	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/vision"
	"github.com/metavert-io/metarush/internal/visionmodels"
)

// AppleLong is the long side Apple Vision reads frames at: its accurate
// level refines the edges at about this size, and larger frames only cost
// time (D507).
const AppleLong = 1280

// Apple is Apple Vision's person segmentation through MetaRush's helper
// (protocol 3, macOS 12 or later): one request for the whole stream, so what
// it saw in the frames before steadies the next.
type Apple struct {
	Tools   *tools.Set
	Quality string // vision.SegmentAccurate (default), Balanced or Fast
}

// Size is the frame scaled so its long side is at most AppleLong.
func (m Apple) Size(w, h int) (int, int) {
	if long := max(w, h); long > AppleLong {
		f := float64(AppleLong) / float64(long)
		return Even(int(math.Round(float64(w) * f))), Even(int(math.Round(float64(h) * f)))
	}
	return Even(w), Even(h)
}

// Format: the helper reads blue, green, red and an unused byte.
func (Apple) Format() string { return "bgra" }

// Open starts the helper on a stream of mw×mh frames.
func (m Apple) Open(ctx context.Context, mw, mh int) (Stream, error) {
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	s := &appleStream{in: inW, out: outR, size: mw * mh, done: make(chan struct{})}
	go func() {
		defer close(s.done)
		s.err = vision.Segment(ctx, m.Tools, mw, mh, m.Quality, inR, outW)
		inR.CloseWithError(errOr(s.err, io.EOF))
		outW.CloseWithError(errOr(s.err, io.EOF))
	}()
	return s, nil
}

type appleStream struct {
	in   *io.PipeWriter
	out  *io.PipeReader
	size int
	done chan struct{}
	err  error
	once sync.Once
}

func (s *appleStream) Next(frame []byte) ([]byte, error) {
	if _, err := s.in.Write(frame); err != nil {
		return nil, s.why(err)
	}
	m := make([]byte, s.size)
	if _, err := io.ReadFull(s.out, m); err != nil {
		return nil, s.why(err)
	}
	return m, nil
}

// why is the helper's own error when it stopped, else err.
func (s *appleStream) why(err error) error {
	select {
	case <-s.done:
		if s.err != nil {
			return fmt.Errorf("Apple Vision stopped: %w", s.err)
		}
	default:
	}
	return err
}

func (s *appleStream) Close() error {
	s.once.Do(func() { _ = s.in.Close() })
	<-s.done
	_ = s.out.Close()
	return s.err
}

// MODNet is MODNet through onnxruntime (lib), from its pinned model file.
type MODNet struct {
	Lib, Path string
}

// Size is MODNet's own: the short side at 512, each side a multiple of 32.
func (MODNet) Size(w, h int) (int, int) { return visionmodels.MatteSize(w, h) }

// Format: MODNet reads red, green and blue.
func (MODNet) Format() string { return "rgb24" }

// Open loads the model (once per process).
func (m MODNet) Open(ctx context.Context, mw, mh int) (Stream, error) {
	k, err := visionmodels.OpenMatter(m.Lib, m.Path)
	if err != nil {
		return nil, err
	}
	return &modnetStream{ctx: ctx, k: k, w: mw, h: mh}, nil
}

type modnetStream struct {
	ctx  context.Context
	k    *visionmodels.Matter
	w, h int
}

func (s *modnetStream) Next(frame []byte) ([]byte, error) {
	if err := s.ctx.Err(); err != nil {
		return nil, err
	}
	return s.k.MatteRGB(frame, s.w, s.h)
}

func (s *modnetStream) Close() error { return nil }

// ErrNoModel: no model that tells people from their background is set up on
// this computer.
var ErrNoModel = errors.New("no model that tells people from their background is set up on this computer")

// Component is the component that adds MODNet to a computer (Settings →
// Components, D506); on a Mac Apple Vision does it, with the on-screen helper.
const Component = "video-cut-outs"

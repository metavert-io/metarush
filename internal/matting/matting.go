// Package matting tells the people in footage from their background, frame
// after frame (background removal for video, D505): ffmpeg reads the clip's
// frames at the size a model reads them, the model answers a matte per frame
// (how much of each pixel is a person), the mattes are smoothed over time so
// edges don't shimmer where the picture holds still (D508), and ffmpeg writes
// them as a matte video of the same frames at the clip's own rate. Two models
// do it on a computer: Apple Vision's person segmentation through MetaRush's
// helper on a Mac (D507), and MODNet through onnxruntime anywhere (D506).
//
// Nothing here talks to the database or the media store: the AI services
// adapter (internal/aiservices/local) runs it for a request, the cut-out job
// (internal/mattes) keeps what it makes.
package matting

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/metavert-io/metarush/internal/tools"
)

// Model tells people from their background in frames.
type Model interface {
	// Size is the size it reads the frames of a w×h picture at; the mattes
	// come back at that size.
	Size(w, h int) (int, int)
	// Format is the pixel format it reads: "bgra" or "rgb24".
	Format() string
	// Open starts a stream of frames of mw×mh (Size's).
	Open(ctx context.Context, mw, mh int) (Stream, error)
}

// Stream makes mattes of frames in order.
type Stream interface {
	// Next is the matte of the next frame: mw×mh bytes, 0 the background,
	// 255 a person.
	Next(frame []byte) ([]byte, error)
	// Close ends the stream.
	Close() error
}

// Bytes is how many bytes a pixel takes in a format.
func Bytes(format string) int {
	if format == "bgra" {
		return 4
	}
	return 3
}

// Video is what a clip's file says about its picture.
type Video struct {
	Width, Height int // as shown (rotation applied)
	// Rate is the frame rate as a fraction (30000/1001); FPS the same as a number.
	Rate     string
	FPS      float64
	Duration float64
	// The colours' description, carried over to what is made from the file.
	ColorSpace, ColorTransfer, ColorPrimaries, ColorRange string
}

// Probe reads a file's picture.
func Probe(ctx context.Context, ts *tools.Set, path string) (*Video, error) {
	res, err := ts.Run(ctx, tools.Spec{Tool: tools.FFprobe, Args: []string{"-v", "error", "-select_streams", "v:0",
		"-show_entries", "stream=width,height,r_frame_rate,avg_frame_rate,color_space,color_transfer,color_primaries,color_range:stream_side_data=rotation:format=duration",
		"-of", "json", path}})
	if err != nil {
		return nil, err
	}
	var out struct {
		Streams []struct {
			Width          int    `json:"width"`
			Height         int    `json:"height"`
			RFrameRate     string `json:"r_frame_rate"`
			AvgFrameRate   string `json:"avg_frame_rate"`
			ColorSpace     string `json:"color_space"`
			ColorTransfer  string `json:"color_transfer"`
			ColorPrimaries string `json:"color_primaries"`
			ColorRange     string `json:"color_range"`
			SideData       []struct {
				Rotation float64 `json:"rotation"`
			} `json:"side_data_list"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
		} `json:"format"`
	}
	if err := json.Unmarshal(res.Stdout, &out); err != nil {
		return nil, fmt.Errorf("unreadable ffprobe output: %w", err)
	}
	if len(out.Streams) == 0 || out.Streams[0].Width <= 0 || out.Streams[0].Height <= 0 {
		return nil, errors.New("the file has no picture")
	}
	s := out.Streams[0]
	v := &Video{Width: s.Width, Height: s.Height, Rate: s.RFrameRate, ColorSpace: known(s.ColorSpace), ColorTransfer: known(s.ColorTransfer),
		ColorPrimaries: known(s.ColorPrimaries), ColorRange: known(s.ColorRange)}
	for _, sd := range s.SideData {
		if r := int(math.Abs(sd.Rotation)) % 180; r == 90 {
			v.Width, v.Height = v.Height, v.Width
		}
	}
	v.FPS = rateOf(v.Rate)
	if v.FPS <= 0 || v.FPS > 240 {
		v.Rate, v.FPS = s.AvgFrameRate, rateOf(s.AvgFrameRate)
	}
	if v.FPS <= 0 || v.FPS > 240 {
		return nil, errors.New("the file doesn't say its frame rate")
	}
	v.Duration, _ = strconv.ParseFloat(out.Format.Duration, 64)
	return v, nil
}

func known(s string) string {
	if s == "unknown" {
		return ""
	}
	return s
}

func rateOf(r string) float64 {
	n, d, ok := strings.Cut(r, "/")
	if !ok {
		v, _ := strconv.ParseFloat(r, 64)
		return v
	}
	a, _ := strconv.ParseFloat(n, 64)
	b, _ := strconv.ParseFloat(d, 64)
	if b == 0 {
		return 0
	}
	return a / b
}

// Even rounds a size down to an even number (what the encoders take), at least 2.
func Even(n int) int { return max(2, n-n%2) }

// Job is one matte to make.
type Job struct {
	Tools *tools.Set
	// Src is the clip's file; From and To the stretch, in seconds (To 0:
	// to the end).
	Src      string
	From, To float64
	Model    Model
	// Matte is where the matte video is written (an MP4).
	Matte string
	// Progress is told how many frames are done of about how many.
	Progress func(done, total int)
}

// Result is how making a matte went.
type Result struct {
	Video *Video
	// First is the clip's frame the matte starts at (0 the first); Frames
	// how many it holds, one per frame from there.
	First  int
	Frames int
	// MatteWidth and MatteHeight are the matte video's size (the model's).
	MatteWidth, MatteHeight int
	Measures                Measures
}

// Measures are what making a matte measured.
type Measures struct {
	Seconds    float64
	MsPerFrame float64 // the model's time per frame
	// People is the share of frames where someone was found.
	People float64
	// FlickerBefore and FlickerAfter are how much the matte's edges changed
	// from frame to frame where the picture held still (mean, 0–255), before
	// and after smoothing.
	FlickerBefore, FlickerAfter float64
}

// Run makes the matte of a stretch of a clip: one smoothed matte per frame,
// written as a video at the clip's rate.
func Run(ctx context.Context, j Job) (*Result, error) {
	start := time.Now()
	v, err := Probe(ctx, j.Tools, j.Src)
	if err != nil {
		return nil, err
	}
	first, count, err := Frames(v, j.From, j.To)
	if err != nil {
		return nil, err
	}
	mw, mh := j.Model.Size(v.Width, v.Height)
	mw, mh = Even(mw), Even(mh)
	format := j.Model.Format()
	px := Bytes(format)
	total := count
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	stream, err := j.Model.Open(ctx, mw, mh)
	if err != nil {
		return nil, err
	}
	streamOpen := true
	defer func() {
		if streamOpen {
			_ = stream.Close()
		}
	}()

	// The decoder writes frames into dec; the encoder reads mattes from enc.
	decR, decW := io.Pipe()
	encR, encW := io.Pipe()
	var wg sync.WaitGroup
	var decErr, encErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		args := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error"}, SeekArgs(v, first)...)
		args = append(args, "-i", j.Src, "-map", "0:v:0", "-an", "-sn", "-dn", "-frames:v", strconv.Itoa(count),
			"-vf", fmt.Sprintf("scale=%d:%d:flags=area,format=%s", mw, mh, format), "-f", "rawvideo", "pipe:1")
		_, decErr = j.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Stdout: decW, Args: args})
		decW.CloseWithError(errOr(decErr, io.EOF))
	}()
	go func() {
		defer wg.Done()
		_, encErr = j.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Stdin: encR, Args: []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y",
			"-f", "rawvideo", "-pix_fmt", "gray", "-s", fmt.Sprintf("%dx%d", mw, mh), "-framerate", v.Rate, "-i", "pipe:0",
			"-c:v", "libx264", "-preset", "veryfast", "-crf", "8", "-pix_fmt", "yuv420p", "-movflags", "+faststart", j.Matte}})
		encR.CloseWithError(errOr(encErr, io.EOF))
	}()

	frame := make([]byte, mw*mh*px)
	sm := newSmoother(mw, mh)
	res := &Result{Video: v, First: first, MatteWidth: mw, MatteHeight: mh}
	var modelTime time.Duration
	people := 0
	var loopErr error
	for {
		if err := ctx.Err(); err != nil {
			loopErr = err
			break
		}
		if _, err := io.ReadFull(decR, frame); err != nil {
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				loopErr = err
			}
			break
		}
		t0 := time.Now()
		raw, err := stream.Next(frame)
		modelTime += time.Since(t0)
		if err != nil {
			loopErr = err
			break
		}
		if len(raw) != mw*mh {
			loopErr = fmt.Errorf("the model answered %d values for a %d×%d frame", len(raw), mw, mh)
			break
		}
		m := sm.next(raw, frame, px, format)
		if someone(m) {
			people++
		}
		if _, err := encW.Write(m); err != nil {
			loopErr = err
			break
		}
		res.Frames++
		if j.Progress != nil && (res.Frames%10 == 0 || res.Frames == 1) {
			j.Progress(res.Frames, max(total, res.Frames))
		}
	}
	closeErr := stream.Close()
	streamOpen = false
	if loopErr != nil {
		cancel()
	}
	_ = encW.Close()
	_ = decR.Close()
	wg.Wait()
	switch {
	case ctx.Err() != nil && loopErr != nil && errors.Is(loopErr, context.Canceled):
		return nil, loopErr
	case loopErr != nil:
		return nil, loopErr
	case closeErr != nil:
		return nil, closeErr
	case decErr != nil:
		return nil, fmt.Errorf("reading the clip's frames: %w", decErr)
	case encErr != nil:
		return nil, fmt.Errorf("writing the matte: %w", encErr)
	case res.Frames == 0:
		return nil, errors.New("the clip gave no frames in that stretch")
	}
	if j.Progress != nil {
		j.Progress(res.Frames, res.Frames)
	}
	res.Measures = Measures{Seconds: time.Since(start).Seconds(), MsPerFrame: float64(modelTime.Milliseconds()) / float64(res.Frames),
		People: float64(people) / float64(res.Frames), FlickerBefore: sm.flicker(false), FlickerAfter: sm.flicker(true)}
	return res, nil
}

// Frames is the stretch [from, to) of a clip in its own frames: the first
// frame it holds and how many (to 0: to the end). A stretch starts at the
// frame showing at from and ends with the one showing just before to.
func Frames(v *Video, from, to float64) (first, count int, err error) {
	if to <= 0 || (v.Duration > 0 && to > v.Duration) {
		to = v.Duration
	}
	from = math.Max(0, from)
	first = int(math.Floor(from*v.FPS + 1e-6))
	last := int(math.Ceil(to*v.FPS - 1e-6))
	if last <= first {
		return 0, 0, errors.New("the stretch of the clip is empty")
	}
	return first, last - first, nil
}

// SeekArgs are ffmpeg's input options that start a clip at one of its
// frames: half a frame before it, so the frame itself is the first kept
// whatever the rounding (nothing for the first frame).
func SeekArgs(v *Video, first int) []string {
	if first <= 0 {
		return nil
	}
	return []string{"-ss", strconv.FormatFloat((float64(first)-0.5)/v.FPS, 'f', 6, 64)}
}

// FrameTime is when a frame of the clip shows, in seconds.
func (v *Video) FrameTime(frame int) float64 { return float64(frame) / v.FPS }

func errOr(err, def error) error {
	if err != nil {
		return err
	}
	return def
}

// someoneShare: a frame has someone in it when at least this share of it is
// mostly a person (a stray soft speck isn't anyone).
const someoneShare = 0.002

func someone(m []byte) bool {
	n := 0
	for _, v := range m {
		if v >= 128 {
			n++
		}
	}
	return float64(n) >= someoneShare*float64(len(m))
}

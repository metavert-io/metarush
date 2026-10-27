package fixes

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/calls"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/visual"
)

// Steadying a call recording (D190). A call's picture switches between
// people (speaker view: a hard cut at every switch, D160); steadying the
// whole recording at once would carry the camera path of one picture into
// the next (vid.stab smooths the path over about a second on both sides, and
// the switch itself reads as one huge jump). So each stretch between two
// switches is steadied on its own — its own motion, its own path, its own
// zoom — and the stretches are put back together frame for frame into one
// copy: the same frames, the same timing, the switches where they were.

// minSteadyFrames: a stretch shorter than this (a sliver of another picture
// at a switch) is kept as it is; there is nothing to steady in it.
const minSteadyFrames = 12

// stretch is a run of frames [from, to) of a recording (to < 0: to the end),
// steadied on its own or kept as it is.
type stretch struct {
	from, to int
	steady   bool
}

// planStretches splits a recording's frames at its switches (the first frame
// of each new picture), each stretch steadied on its own; stretches too short
// to steady are kept as they are, and neighbouring kept stretches are one.
func planStretches(switches []int) []stretch {
	sw := append([]int(nil), switches...)
	sort.Ints(sw)
	var out []stretch
	add := func(from, to int) {
		if to >= 0 && to <= from {
			return
		}
		steady := to < 0 || to-from >= minSteadyFrames
		if n := len(out); n > 0 && !steady && !out[n-1].steady {
			out[n-1].to = to
			return
		}
		out = append(out, stretch{from: from, to: to, steady: steady})
	}
	at := 0
	for _, k := range sw {
		if k <= at {
			continue
		}
		add(at, k)
		at = k
	}
	add(at, -1)
	return out
}

// switchFrames finds where a call recording's picture switches, as frame
// numbers of the file steadied (in, at fps): the switches insight (D160), or
// found now from the evidence and the on-screen index when it isn't there yet
// (and kept, as the switches job would). Switches found on a file with other
// frames (a preview copy at another frame rate) are put on in's own frame
// first, so no stretch holds a frame of the next picture.
func switchFrames(ctx context.Context, a *app.App, as *models.Asset, in, dir string, fps float64) ([]int, string, error) {
	st := calls.Load(ctx, a.DB, a.WorkspaceID(), as.ID)
	if st == nil {
		ev := evidence.LoadStored(ctx, a.DB, a.WorkspaceID(), []string{as.ID})[as.ID]
		var idx *visual.Index
		if vs := visual.Load(ctx, a.DB, a.WorkspaceID(), as.ID); vs != nil {
			idx = vs.Index
		}
		if !calls.Worth(as, ev, idx) {
			return nil, "", nil
		}
		found, err := calls.Compute(ctx, a.Tools, calls.Input{Asset: as, Evidence: ev, Index: idx, Path: in, Source: "original", FPS: fps}, filepath.Join(dir, "switches"))
		if err != nil {
			return nil, "", err
		}
		_, _ = calls.Save(ctx, a.DB, a.WorkspaceID(), as.ID, found, a.Tools.Version(ctx, tools.FFmpeg))
		st = found
	}
	var times []float64
	for _, s := range st.Switches {
		times = append(times, s.At)
	}
	if st.Precision > 0 && len(times) > 0 {
		var err error
		if times, err = snapSwitches(ctx, a.Tools, in, filepath.Join(dir, "snap"), times, fps, st.Precision); err != nil {
			return nil, st.Version, err
		}
	}
	frames := make([]int, 0, len(times))
	for _, t := range times {
		frames = append(frames, int(math.Round(t*fps)))
	}
	return frames, st.Version, nil
}

var (
	snapPTS   = regexp.MustCompile(`pts_time:([0-9.eE+-]+)`)
	snapScore = regexp.MustCompile(`lavfi\.scene_score=([0-9.eE+-]+)`)
)

// snapSwitches moves each switch to the frame of in whose picture changes
// most within precision (and two frames) of where it was found.
func snapSwitches(ctx context.Context, ts *tools.Set, in, dir string, times []float64, fps, precision float64) ([]float64, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	frame := 1 / math.Max(1, fps)
	reach := precision + 2*frame
	out := make([]float64, 0, len(times))
	for i, t := range times {
		lo := math.Max(0, t-reach-frame)
		file := filepath.Join(dir, fmt.Sprintf("snap-%d.txt", i))
		filter := fmt.Sprintf("scale=160:-2,select='gte(scene\\,0)',metadata=mode=print:file=%s", filterPath(file))
		if _, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Args: []string{"-hide_banner", "-nostdin", "-nostats", "-loglevel", "error",
			"-ss", secArg(lo), "-t", secArg(2*reach + 2*frame), "-i", in, "-map", "0:v:0", "-an", "-vf", filter, "-f", "null", "-"}}); err != nil {
			return nil, err
		}
		b, _ := os.ReadFile(file)
		best, bestScore, at, first := t, -1.0, -1.0, true
		for _, line := range strings.Split(string(b), "\n") {
			if m := snapPTS.FindStringSubmatch(line); m != nil {
				v, _ := strconv.ParseFloat(m[1], 64)
				at = lo + v
				continue
			}
			if m := snapScore.FindStringSubmatch(line); m != nil && at >= 0 {
				if first { // the first frame after a seek has nothing before it
					first = false
					continue
				}
				v, _ := strconv.ParseFloat(m[1], 64)
				if math.Abs(at-t) <= reach && v > bestScore {
					best, bestScore = at, v
				}
			}
		}
		out = append(out, best)
	}
	return out, nil
}

// filterPath escapes a path for an ffmpeg filter argument.
func filterPath(p string) string {
	p = filepath.ToSlash(p)
	return strings.NewReplacer(`\`, `\\`, `:`, `\:`, `'`, `\'`, `,`, `\,`, `[`, `\[`, `]`, `\]`, `;`, `\;`).Replace(p)
}

// stretchRun is how one stretch is steadied: the programs and filters for it.
type stretchRun struct {
	// tool is the ffmpeg that steadies (the stabilizer for vid.stab).
	tool string
	// detect is the motion pass's filter chain for a motion file ("" none: one pass).
	detect func(motion string) string
	// steady is the filter chain that makes the steady picture (from the motion file).
	steady func(motion string) string
	// keep is the chain for a stretch kept as it is (the other fixes only).
	keep string
}

// steadyByStretch steadies each stretch of in on its own and writes the
// stretches, frame after frame, into one encoded picture (out): every stretch
// is decoded from its first frame (an accurate seek a quarter frame early),
// exactly its frames are taken, and they go to a single encoder as raw
// frames, so the copy has the original's frames, rate and timing. pix is the
// raw frames' pixel format (it matches the encoder's).
func steadyByStretch(ctx context.Context, ts *tools.Set, in, dir string, fps float64, total int, parts []stretch, run stretchRun, pix string, enc []string, out string,
	report func(float64, string)) (steadied int, err error) {
	pr, pw := io.Pipe()
	encErr := make(chan error, 1)
	go func() {
		args := append([]string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-f", "yuv4mpegpipe", "-i", "-"}, enc...)
		_, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Background: true, Stdin: pr, Args: append(args, out)})
		pr.CloseWithError(err)
		encErr <- err
	}()
	j := &y4mJoin{w: bufio.NewWriterSize(pw, 4<<20)}
	fail := func(e error) (int, error) {
		pw.CloseWithError(e)
		<-encErr
		return steadied, e
	}
	done := 0
	for i, s := range parts {
		input := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y"}
		if s.from > 0 {
			input = append(input, "-ss", strconv.FormatFloat((float64(s.from)-0.25)/fps, 'f', 6, 64))
		}
		input = append(input, "-i", in, "-an")
		// The filters never see a frame past the stretch: a motion pass that read
		// on into the next picture would put its motion into this one's path.
		only := func(chain string) string { return chain }
		// The last stretch runs to the end: bound it by the recording's own
		// frame count too, so ffmpeg can't pad it out to the stream's length.
		if s.to < 0 && total > s.from {
			s.to = total
		}
		if n := s.to - s.from; s.to >= 0 {
			input = append(input, "-frames:v", strconv.Itoa(n))
			only = func(chain string) string { return fmt.Sprintf("trim=end_frame=%d,%s", n, chain) }
		}
		chain := only(run.keep)
		tool := tools.FFmpeg
		if s.steady {
			motion := fmt.Sprintf("motion-%d.trf", i)
			tool = run.tool
			if run.detect != nil {
				if _, err := ts.Run(ctx, tools.Spec{Tool: run.tool, Dir: dir, Background: true, Args: append(append([]string{}, input...), "-vf", only(run.detect(motion)), "-f", "null", "-")}); err != nil {
					return fail(fmt.Errorf("steadying (finding the motion between %s and %s) failed: %w", clock(float64(s.from)/fps), endClock(s, fps), err))
				}
			}
			chain = only(run.steady(motion))
			steadied++
		}
		// passthrough: every decoded frame once, none repeated or dropped to
		// fit a frame rate (call recordings often vary theirs).
		args := append(append([]string{}, input...), "-vf", chain, "-fps_mode", "passthrough", "-pix_fmt", pix, "-strict", "-1", "-f", "yuv4mpegpipe", "-")
		sr, sw := io.Pipe()
		runErr := make(chan error, 1)
		go func() {
			_, err := ts.Run(ctx, tools.Spec{Tool: tool, Dir: dir, Background: true, Stdout: sw, Args: args})
			sw.CloseWithError(err)
			runErr <- err
		}()
		n, cerr := j.copyStretch(sr)
		sr.CloseWithError(cerr)
		if rerr := <-runErr; rerr != nil {
			return fail(fmt.Errorf("steadying %s–%s failed: %w", clock(float64(s.from)/fps), endClock(s, fps), rerr))
		}
		if cerr != nil {
			return fail(cerr)
		}
		if want := s.to - s.from; s.to >= 0 && n != want {
			return fail(fmt.Errorf("the stretch at %s gave %d frames instead of %d", clock(float64(s.from)/fps), n, want))
		}
		done += n
		if total > 0 {
			report(math.Min(1, float64(done)/float64(total)), "steadying the camera, one picture at a time")
		}
	}
	if err := j.w.Flush(); err != nil {
		return fail(err)
	}
	pw.Close()
	if err := <-encErr; err != nil {
		return steadied, fmt.Errorf("putting the steady picture together failed: %w", err)
	}
	return steadied, nil
}

func clock(s float64) string {
	m := int(s) / 60
	return fmt.Sprintf("%d:%04.1f", m, s-float64(m*60))
}

func endClock(s stretch, fps float64) string {
	if s.to < 0 {
		return "the end"
	}
	return clock(float64(s.to) / fps)
}

// y4mJoin writes several YUV4MPEG2 streams of the same size and format as
// one: the first stream's header, then every stream's frames in order.
type y4mJoin struct {
	w      *bufio.Writer
	header string // the first stream's header (without its parameters that may differ)
	frame  int    // bytes per frame
	frames int
}

// copyStretch reads one stream and writes its frames; it returns how many.
func (j *y4mJoin) copyStretch(r io.Reader) (int, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	head, err := br.ReadString('\n')
	if err != nil {
		if errors.Is(err, io.EOF) && head == "" {
			return 0, nil // no frames at all (past the end)
		}
		return 0, err
	}
	if !strings.HasPrefix(head, "YUV4MPEG2 ") {
		return 0, errors.New("the steadied picture came out in an unexpected form")
	}
	size, key, err := y4mFrameSize(head)
	if err != nil {
		return 0, err
	}
	switch {
	case j.header == "":
		j.header, j.frame = key, size
		if _, err := j.w.WriteString(head); err != nil {
			return 0, err
		}
	case key != j.header:
		return 0, fmt.Errorf("a stretch came out in another size or format (%q, expected %q)", strings.TrimSpace(key), strings.TrimSpace(j.header))
	}
	buf := make([]byte, size)
	n := 0
	for {
		line, err := br.ReadSlice('\n')
		if errors.Is(err, io.EOF) && len(line) == 0 {
			return n, nil
		}
		if err != nil {
			return n, err
		}
		if !bytes.HasPrefix(line, []byte("FRAME")) {
			return n, errors.New("the steadied picture came out in an unexpected form")
		}
		if _, err := io.ReadFull(br, buf); err != nil {
			return n, err
		}
		if _, err := j.w.WriteString("FRAME\n"); err != nil {
			return n, err
		}
		if _, err := j.w.Write(buf); err != nil {
			return n, err
		}
		n++
		j.frames++
	}
}

// y4mFrameSize reads a YUV4MPEG2 header: the bytes of one frame, and the
// parameters every stretch must share (size, rate, interlacing, aspect,
// colour format).
func y4mFrameSize(head string) (int, string, error) {
	var w, h int
	cs := "420jpeg"
	var keep []string
	for _, f := range strings.Fields(strings.TrimSpace(head))[1:] {
		switch f[0] {
		case 'W':
			w, _ = strconv.Atoi(f[1:])
		case 'H':
			h, _ = strconv.Atoi(f[1:])
		case 'C':
			cs = f[1:]
		case 'X':
			continue // comments and extensions (colour range) may differ
		}
		keep = append(keep, f)
	}
	if w <= 0 || h <= 0 {
		return 0, "", errors.New("the steadied picture has no size")
	}
	bps := 1
	for _, deep := range []string{"p9", "p10", "p12", "p14", "p16", "mono16"} {
		if strings.Contains(cs, deep) {
			bps = 2
		}
	}
	var planes float64
	switch {
	case strings.HasPrefix(cs, "420"):
		planes = 1.5
	case strings.HasPrefix(cs, "422"):
		planes = 2
	case strings.HasPrefix(cs, "444alpha"):
		planes = 4
	case strings.HasPrefix(cs, "444"):
		planes = 3
	case strings.HasPrefix(cs, "mono"):
		planes = 1
	default:
		return 0, "", fmt.Errorf("the steadied picture's colour format %q isn't one MetaRush reads", cs)
	}
	cw, ch := (w+1)/2, (h+1)/2
	size := 0
	switch planes {
	case 1.5:
		size = (w*h + 2*cw*ch) * bps
	case 2:
		size = (w*h + 2*cw*h) * bps
	default:
		size = int(float64(w*h)*planes) * bps
	}
	return size, strings.Join(keep, " ") + "\n", nil
}

// rawPix is the pixel format of the frames passed between the stretches and
// the encoder: the encoder's own.
func rawPix(enc []string) string {
	for i := 0; i+1 < len(enc); i++ {
		if enc[i] == "-pix_fmt" {
			return enc[i+1]
		}
	}
	return "yuv420p"
}

package matting

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// A cut-out (D509): the clip's own picture with the matte as its alpha, in a
// WebM file with VP9's alpha channel — what Chrome's video plays in the
// dashboard's preview and Remotion's compositor reads in renders.

// CutOutSize is a cut-out's size for a clip shown at w×h: at most long on
// its long side, both even.
func CutOutSize(w, h, long int) (int, int) {
	if l := max(w, h); l > long {
		f := float64(long) / float64(l)
		w, h = int(math.Round(float64(w)*f)), int(math.Round(float64(h)*f))
	}
	return Even(w), Even(h)
}

// OnGrid is the filter that puts a clip's frames on its own frame rate's
// grid, starting at 0: the frame the clip shows at each tick of its rate
// (nearest), whatever the times its file gives them — a millisecond time
// base puts them just before the grid (WebM, Matroska, screen and browser
// recordings), a variable rate between it — and its last frame held where
// the file runs on past the picture (its sound longer), so a stretch has
// every frame Frames counts. The matte's decode and the cut-out's picture
// both read the clip through it, so frame N of one is frame N of the other.
func (v *Video) OnGrid() string {
	return "setpts=PTS-STARTPTS,fps=" + v.Rate + ",tpad=stop=-1:stop_mode=clone"
}

// ByIndex is the filter that times frames by their number at the clip's
// rate (frame N at N/rate exactly), so two streams of the same frames pair
// one to one.
func (v *Video) ByIndex() string {
	tb := ""
	if n, d, ok := strings.Cut(v.Rate, "/"); ok {
		if a, err1 := strconv.Atoi(n); err1 == nil {
			if b, err2 := strconv.Atoi(d); err2 == nil && a > 0 && b > 0 {
				tb = fmt.Sprintf("%d/%d", b, a)
			}
		}
	}
	if tb == "" {
		tb = "1/" + strconv.FormatFloat(v.FPS, 'f', -1, 64)
	}
	return "settb=" + tb + ",setpts=N"
}

// CutOutArgs are ffmpeg's arguments that keep a stretch of a clip (count
// frames from its frame first) with the matte as its alpha, frame for frame,
// at w×h: the picture on the clip's grid like the matte's decode, both timed
// by frame number so alphamerge pairs each frame with its own matte, the
// picture scaled with lanczos, the matte with bicubic, VP9 at CRF 22 with a
// keyframe every second and no alt-ref frames (their hidden frames break the
// alpha), its colours described as the clip's. The caller adds what reports
// progress and the output's path.
func CutOutArgs(v *Video, first, count int, src, matte string, w, h int) []string {
	gop := max(1, int(math.Round(v.FPS)))
	args := append([]string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error"}, SeekArgs(v, first)...)
	args = append(args, "-i", src, "-i", matte, "-filter_complex",
		fmt.Sprintf("[0:v]%s,%s,scale=%d:%d:flags=lanczos,format=yuv420p[c];[1:v]%s,scale=%d:%d:flags=bicubic,format=gray[m];[c][m]alphamerge,format=yuva420p[o]",
			v.OnGrid(), v.ByIndex(), w, h, v.ByIndex(), w, h),
		"-map", "[o]", "-frames:v", strconv.Itoa(count), "-fps_mode", "passthrough", "-an", "-sn", "-dn",
		"-c:v", "libvpx-vp9", "-pix_fmt", "yuva420p", "-b:v", "0", "-crf", "22", "-deadline", "good", "-cpu-used", "4", "-row-mt", "1",
		"-auto-alt-ref", "0", "-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop))
	for _, kv := range [][2]string{{"-colorspace", v.ColorSpace}, {"-color_trc", v.ColorTransfer}, {"-color_primaries", v.ColorPrimaries}, {"-color_range", v.ColorRange}} {
		if kv[1] != "" {
			args = append(args, kv[0], kv[1])
		}
	}
	return args
}

package matting

import (
	"fmt"
	"math"
	"strconv"
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

// CutOutArgs are ffmpeg's arguments that keep a stretch of a clip (count
// frames from its frame first) with the matte as its alpha, frame for frame,
// at w×h: the picture scaled with lanczos, the matte with bicubic, VP9 at
// CRF 22 with a keyframe every second and no alt-ref frames (their hidden
// frames break the alpha), its colours described as the clip's. The caller
// adds what reports progress and the output's path.
func CutOutArgs(v *Video, first, count int, src, matte string, w, h int) []string {
	gop := max(1, int(math.Round(v.FPS)))
	args := append([]string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error"}, SeekArgs(v, first)...)
	args = append(args, "-i", src, "-i", matte, "-filter_complex",
		fmt.Sprintf("[0:v]setpts=PTS-STARTPTS,scale=%d:%d:flags=lanczos,format=yuv420p[c];[1:v]setpts=PTS-STARTPTS,scale=%d:%d:flags=bicubic,format=gray[m];[c][m]alphamerge,format=yuva420p[o]", w, h, w, h),
		"-map", "[o]", "-frames:v", strconv.Itoa(count), "-an", "-sn", "-dn",
		"-c:v", "libvpx-vp9", "-pix_fmt", "yuva420p", "-b:v", "0", "-crf", "22", "-deadline", "good", "-cpu-used", "4", "-row-mt", "1",
		"-auto-alt-ref", "0", "-g", strconv.Itoa(gop), "-keyint_min", strconv.Itoa(gop))
	for _, kv := range [][2]string{{"-colorspace", v.ColorSpace}, {"-color_trc", v.ColorTransfer}, {"-color_primaries", v.ColorPrimaries}, {"-color_range", v.ColorRange}} {
		if kv[1] != "" {
			args = append(args, kv[0], kv[1])
		}
	}
	return args
}

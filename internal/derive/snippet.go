package derive

import (
	"context"
	"fmt"
	"math"
)

// MaxSnippet is the longest listening snippet (seconds).
const MaxSnippet = 15.0

// AudioSnippet cuts [from, to) of in's first audio stream into a small mono
// WAV for listening (24 kHz, 16-bit, with short fades so it doesn't click).
func (d *Deriver) AudioSnippet(ctx context.Context, in, out string, from, to float64) error {
	if from < 0 || to <= from || to-from > MaxSnippet+1e-6 {
		return fmt.Errorf("a listening sample must start before it ends and last at most %.0f seconds (asked for %.3f–%.3f)", MaxSnippet, from, to)
	}
	dur := to - from
	fade := math.Min(0.06, dur/4)
	af := fmt.Sprintf("afade=t=in:d=%.3f,afade=t=out:st=%.3f:d=%.3f", fade, math.Max(0, dur-fade), fade)
	return d.ffmpeg(ctx, "-ss", fmt.Sprintf("%.3f", from), "-t", fmt.Sprintf("%.3f", dur), "-i", in, "-map", "0:a:0", "-vn",
		"-ac", "1", "-ar", "24000", "-af", af, "-c:a", "pcm_s16le", out)
}

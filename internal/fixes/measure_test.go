package fixes

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// The measurements behind the marks (D153), on made-up media: a steady and a
// shaky picture, a clean and a grainy one, a clean and a blocky one, a cold
// one; a hum, background noise, and silence at the ends of a sound.

func needFFmpeg(t *testing.T) *tools.Set {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	return tools.New(config.Tools{})
}

// gen runs ffmpeg to make a test file in dir.
func gen(t *testing.T, dir, name string, args ...string) string {
	t.Helper()
	out := filepath.Join(dir, name)
	cmd := exec.Command("ffmpeg", append(append([]string{"-hide_banner", "-loglevel", "error", "-y"}, args...), out)...)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg %s: %v\n%s", name, err, b)
	}
	return out
}

func TestShakeTellsSteadyFromShaky(t *testing.T) {
	ts := needFFmpeg(t)
	ctx := context.Background()
	dir := t.TempDir()
	// A still scene (a test card, shot from a tripod), and the same scene from
	// a hand that shakes: the frame jumps a few pixels every frame.
	still := "testsrc=size=720x420:rate=30:duration=8"
	steady := gen(t, dir, "steady.mp4", "-f", "lavfi", "-i", still, "-vf", "crop=640:360:40:30", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "18")
	shaky := gen(t, dir, "shaky.mp4", "-f", "lavfi", "-i", still, "-vf", "crop=640:360:x='40+18*sin(n*1.9)*cos(n*0.7)':y='30+14*sin(n*2.3+1)'", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "18")
	s0, err := Shake(ctx, ts, steady, 8)
	if err != nil {
		t.Fatal(err)
	}
	s1, err := Shake(ctx, ts, shaky, 8)
	if err != nil {
		t.Fatal(err)
	}
	if s0 >= ShakeFits || s1 < ShakeFits || s1 < 5*s0 {
		t.Fatalf("shake: steady %.3f%%, shaky %.3f%% (fits from %.2f%%)", s0, s1, ShakeFits)
	}
}

func TestGrainBlocksAndColour(t *testing.T) {
	ts := needFFmpeg(t)
	ctx := context.Background()
	dir := t.TempDir()
	// A test card turned a little, so its edges don't line up with the
	// encoder's 8-pixel blocks (in real footage they never do on purpose).
	src := "testsrc2=size=1400x800:rate=25:duration=4,rotate=0.07,crop=1280:720"
	clean := gen(t, dir, "clean.mp4", "-f", "lavfi", "-i", src, "-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "12")
	grainy := gen(t, dir, "grainy.mp4", "-f", "lavfi", "-i", src, "-vf", "noise=alls=28:allf=t+u", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "12")
	blocky := gen(t, dir, "blocky.mp4", "-f", "lavfi", "-i", src, "-vf", "noise=alls=12:allf=t", "-c:v", "mpeg2video", "-q:v", "31", "-qmin", "31", "-qmax", "31")
	cold := gen(t, dir, "cold.mp4", "-f", "lavfi", "-i", src, "-vf", "colorbalance=rs=-0.3:rm=-0.3:rh=-0.2:bs=0.3:bm=0.3:bh=0.2", "-c:v", "libx264", "-pix_fmt", "yuv420p", "-crf", "12")

	g0, err := Grain(ctx, ts, clean, 4)
	if err != nil {
		t.Fatal(err)
	}
	g1, err := Grain(ctx, ts, grainy, 4)
	if err != nil {
		t.Fatal(err)
	}
	if g0 >= GrainFits || g1 < GrainFits {
		t.Fatalf("grain: clean %.2f, grainy %.2f (fits from %.1f)", g0, g1, GrainFits)
	}
	b0, err := Blockiness(ctx, ts, clean, 4)
	if err != nil {
		t.Fatal(err)
	}
	b1, err := Blockiness(ctx, ts, blocky, 4)
	if err != nil {
		t.Fatal(err)
	}
	if b0 >= BlocksFits || b1 < BlocksFits {
		t.Fatalf("blockiness: clean %.2f, blocky %.2f (fits from %.2f)", b0, b1, BlocksFits)
	}
	c0, err := Colour(ctx, ts, clean, 4)
	if err != nil {
		t.Fatal(err)
	}
	c1, err := Colour(ctx, ts, cold, 4)
	if err != nil {
		t.Fatal(err)
	}
	if c0.Cast >= CastFits || c1.Cast < CastFits || !strings.Contains(castWords(c1), "cold") {
		t.Fatalf("colour: clean %+v, cold %+v (%q)", c0, c1, castWords(c1))
	}
}

func TestHumNoiseAndSilentEnds(t *testing.T) {
	ts := needFFmpeg(t)
	ctx := context.Background()
	dir := t.TempDir()
	hum := gen(t, dir, "hum.wav", "-f", "lavfi", "-i", "aevalsrc=0.08*sin(2*PI*60*t)+0.04*sin(2*PI*120*t)+0.03*sin(2*PI*180*t)+0.01*(random(0)-0.5):s=16000:d=8", "-c:a", "pcm_s16le")
	noise := gen(t, dir, "noise.wav", "-f", "lavfi", "-i", "anoisesrc=color=pink:amplitude=0.05:duration=8:sample_rate=16000", "-c:a", "pcm_s16le")
	h1, f0, err := Hum(ctx, ts, hum)
	if err != nil {
		t.Fatal(err)
	}
	h0, _, err := Hum(ctx, ts, noise)
	if err != nil {
		t.Fatal(err)
	}
	if h1 < HumFits || h0 >= HumFits || f0 != 60 {
		t.Fatalf("hum: %.1f dB at %d Hz, noise %.1f dB (fits from %.0f)", h1, f0, h0, HumFits)
	}
	// Two seconds of silence, three of a tone, one and a half of silence.
	ends := gen(t, dir, "ends.wav", "-f", "lavfi", "-i", "aevalsrc='if(between(t,2,5),0.3*sin(2*PI*440*t),0)':s=16000:d=6.5", "-c:a", "pcm_s16le")
	sf, err := Sound(ctx, ts, ends, nil)
	if err != nil {
		t.Fatal(err)
	}
	if sf.Start < 1.8 || sf.Start > 2.2 || sf.End < 4.8 || sf.End > 5.2 {
		t.Fatalf("ends: %+v", sf)
	}
	if _, err := os.Stat(ends); err != nil {
		t.Fatal(err)
	}
}

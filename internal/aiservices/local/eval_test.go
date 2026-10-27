//go:build localeval

package local_test

// Measurements of the models that run on this computer (D278–D280), on the
// real programs and model files. Nothing here runs in ordinary tests. The
// pinned archives are served from a folder on this computer (never the
// internet from go test): download them once by hand into EVAL_MODELS
// (the URLs and checksums are internal/localmodels/pins.go), then
//
//	EVAL_MODELS=<folder of archives> EVAL_PICTURES=<folder of PNGs> \
//	  go test -tags localeval -run TestMeasure -v -timeout 60m ./internal/aiservices/local/
//
// Voices: speed (the real-time factor) on five narration sentences. Larger
// pictures: each picture is made four times smaller, then four times larger
// again with the model and with Catmull-Rom (what MetaRush's fixes use), and
// both are compared with the original (PSNR, higher is closer). Background
// removal: the time Apple Vision takes. Results are in docs/ai-services.md.

import (
	"context"
	"fmt"
	"image"
	"image/png"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/image/draw"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/local"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/localmodels"
	"github.com/metavert-io/metarush/internal/tools"
)

var sentences = []string{
	"Welcome back to the garden. Today we're planting tomatoes, basil and two rows of runner beans along the south fence.",
	"The new release ships on Thursday, the fourteenth of November, with faster renders and captions in six languages.",
	"Before you start, check that the soil is moist but not wet, and that each seedling has at least three true leaves.",
	"Our team measured every step twice, so the numbers you see here are the ones we trust: about forty percent faster.",
	"If you have questions, leave them in the comments, and we'll answer the best ones in next week's episode.",
}

func evalEnv(t *testing.T) aiservices.Env {
	t.Helper()
	dir := os.Getenv("EVAL_MODELS")
	if dir == "" {
		t.Skip("set EVAL_MODELS to a folder holding the pinned archives")
	}
	srv := httptest.NewServer(http.FileServer(http.Dir(dir)))
	t.Cleanup(srv.Close)
	t.Setenv(localmodels.EnvBase, srv.URL)
	home := os.Getenv("EVAL_HOME")
	if home == "" {
		home = t.TempDir()
	}
	s := localmodels.Store{Home: home}
	for _, p := range s.Here() {
		start := time.Now()
		if err := s.Setup(context.Background(), p, nil); err != nil {
			t.Fatalf("set up %s: %v", p.ID, err)
		}
		t.Logf("set up %s (%s) in %.1f s", p.ID, localmodels.Size(p.Bytes), time.Since(start).Seconds())
	}
	return aiservices.Env{Tools: tools.New(config.Tools{}), Cfg: &config.Config{}, Home: home, Scratch: t.TempDir()}
}

func TestMeasureVoices(t *testing.T) {
	e := evalEnv(t)
	a, _ := aiservices.Get(local.Name, aiservices.KindVoice)
	ctx := aiservices.WithEnv(context.Background(), e)
	out := os.Getenv("EVAL_OUT")
	for _, c := range []struct{ model, voice string }{{local.ModelKokoro, "af_heart"}, {local.ModelKitten, "expr-voice-2-f"}} {
		var wall, heard float64
		for i, s := range sentences {
			start := time.Now()
			tk, err := a.Submit(ctx, "", &aiservices.Request{Kind: aiservices.KindVoice, Model: c.model, Prompt: s, Options: aiservices.Options{Voice: c.voice}})
			if err != nil {
				t.Fatalf("%s: %v", c.model, err)
			}
			wall += time.Since(start).Seconds()
			heard += tk.Ready.Usage.Seconds
			if out != "" {
				_ = os.WriteFile(filepath.Join(out, fmt.Sprintf("%s-%d.wav", c.model, i)), tk.Ready.Outputs[0].Data, 0o600)
			}
		}
		t.Logf("%s (%s): %.1f s of speech in %.1f s, real-time factor %.2f", c.model, c.voice, heard, wall, wall/heard)
	}
}

func TestMeasureUpscale(t *testing.T) {
	e := evalEnv(t)
	pics := os.Getenv("EVAL_PICTURES")
	if pics == "" {
		t.Skip("set EVAL_PICTURES to a folder of PNG pictures")
	}
	s := local.Stores(e)
	p, _ := s.Find(local.UpscalePin)
	run, err := local.OpenUpscaler(e, s, p)
	if err != nil {
		t.Fatal(err)
	}
	files, _ := filepath.Glob(filepath.Join(pics, "*.png"))
	var sumModel, sumCubic, secs float64
	for _, f := range files {
		orig := readPNG(t, f)
		b := orig.Bounds()
		small := image.NewNRGBA(image.Rect(0, 0, b.Dx()/4, b.Dy()/4))
		draw.BiLinear.Scale(small, small.Rect, orig, b, draw.Src, nil)
		start := time.Now()
		big, err := local.Upscale(context.Background(), run, small, 4)
		if err != nil {
			t.Fatal(err)
		}
		secs += time.Since(start).Seconds()
		cubic := image.NewNRGBA(image.Rect(0, 0, small.Rect.Dx()*4, small.Rect.Dy()*4))
		draw.CatmullRom.Scale(cubic, cubic.Rect, small, small.Rect, draw.Src, nil)
		if out := os.Getenv("EVAL_OUT"); out != "" {
			for name, img := range map[string]image.Image{"model": big, "catmull-rom": cubic} {
				if w, err := os.Create(filepath.Join(out, strings.TrimSuffix(filepath.Base(f), ".png")+"-"+name+".png")); err == nil {
					_ = png.Encode(w, img)
					w.Close()
				}
			}
		}
		pm, pc := psnr(orig, big), psnr(orig, cubic)
		sumModel, sumCubic = sumModel+pm, sumCubic+pc
		t.Logf("%s: model %.2f dB, Catmull-Rom %.2f dB (%dx%d → %dx%d in %.2f s)", filepath.Base(f), pm, pc, small.Rect.Dx(), small.Rect.Dy(), big.Rect.Dx(), big.Rect.Dy(), time.Since(start).Seconds())
	}
	n := float64(len(files))
	t.Logf("average: model %.2f dB, Catmull-Rom %.2f dB; %.2f s a picture", sumModel/n, sumCubic/n, secs/n)
	// Time for a large picture: 1024 × 1024 made 4× larger.
	img := readPNG(t, files[0])
	large := image.NewNRGBA(image.Rect(0, 0, 1024, 1024))
	draw.CatmullRom.Scale(large, large.Rect, img, img.Bounds(), draw.Src, nil)
	start := time.Now()
	if _, err := local.Upscale(context.Background(), run, large, 4); err != nil {
		t.Fatal(err)
	}
	t.Logf("1024 × 1024 → 4096 × 4096 in %.1f s", time.Since(start).Seconds())
}

func TestMeasureBackground(t *testing.T) {
	pics := os.Getenv("EVAL_PICTURES")
	if pics == "" {
		t.Skip("set EVAL_PICTURES to a folder of PNG pictures")
	}
	e := aiservices.Env{Tools: tools.New(config.Tools{}), Cfg: &config.Config{}, Home: t.TempDir(), Scratch: t.TempDir()}
	if err := local.MaskerReady(e); err != nil {
		t.Skip(err)
	}
	a, _ := aiservices.Get(local.Name, aiservices.KindBackground)
	files, _ := filepath.Glob(filepath.Join(pics, "*.png"))
	var secs float64
	found := 0
	for _, f := range files {
		start := time.Now()
		_, err := a.Submit(aiservices.WithEnv(context.Background(), e), "", &aiservices.Request{Kind: aiservices.KindBackground, Model: local.ModelSubject,
			Inputs: []aiservices.Media{{Role: aiservices.RolePicture, Path: f, MIME: "image/png"}}})
		secs += time.Since(start).Seconds()
		if err == nil {
			found++
		} else if !strings.Contains(err.Error(), "No subject") {
			t.Fatalf("%s: %v", f, err)
		}
	}
	t.Logf("background taken away from %d of %d pictures, %.2f s a picture (the first includes building the helper when needed)", found, len(files), secs/float64(len(files)))
}

func readPNG(t *testing.T, path string) image.Image {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// psnr compares two pictures of the same size, in decibels (higher is closer).
func psnr(a, b image.Image) float64 {
	r := a.Bounds()
	var se float64
	n := 0
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			ar, ag, ab, _ := a.At(x, y).RGBA()
			br, bg, bb, _ := b.At(x-r.Min.X, y-r.Min.Y).RGBA()
			for _, d := range []float64{float64(ar>>8) - float64(br>>8), float64(ag>>8) - float64(bg>>8), float64(ab>>8) - float64(bb>>8)} {
				se += d * d
			}
			n += 3
		}
	}
	mse := se / float64(n)
	if mse == 0 {
		return math.Inf(1)
	}
	return 10 * math.Log10(255*255/mse)
}

// TestMeasureTranscription writes speech fixtures down through the adapter
// (scripts/fetch-speech.sh; EVAL_SPEECH=tests/fixtures/speech) and says how
// long it took. Accuracy is the recognizers' own, measured by D10's and
// D244's method.
func TestMeasureTranscription(t *testing.T) {
	dir := os.Getenv("EVAL_SPEECH")
	if dir == "" {
		t.Skip("set EVAL_SPEECH (scripts/fetch-speech.sh)")
	}
	e := aiservices.Env{Tools: tools.New(config.Tools{}), Cfg: &config.Config{}, Home: t.TempDir()}
	a, _ := aiservices.Get(local.Name, aiservices.KindTranscription)
	for _, c := range []struct{ id, model, lang string }{{"en-gb1", local.ModelParakeet, "en"}, {"en-gb1", local.ModelWhisper, "en"}, {"es-tito", local.ModelWhisper, "es"}, {"fr-ruth", local.ModelWhisper, "fr"}} {
		wav := filepath.Join(dir, c.id+".wav")
		b, err := os.ReadFile(wav)
		if err != nil {
			t.Fatal(err)
		}
		heard := aiservices.WAVSeconds(b)
		e.Scratch = t.TempDir()
		start := time.Now()
		tk, err := a.Submit(aiservices.WithEnv(context.Background(), e), "", &aiservices.Request{Kind: aiservices.KindTranscription, Model: c.model,
			Inputs: []aiservices.Media{{Role: aiservices.RoleAudio, Path: wav, Seconds: heard}}, Options: aiservices.Options{Language: c.lang}})
		if err != nil {
			t.Fatalf("%s %s: %v", c.id, c.model, err)
		}
		took := time.Since(start).Seconds()
		t.Logf("%s with %s: %.0f s heard in %.1f s (real-time factor %.3f), %d words", c.id, c.model, heard, took, took/heard, len(tk.Ready.Outputs[0].Transcript.Words))
	}
}

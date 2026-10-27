package local_test

import (
	"context"
	"image"
	"image/color"
	"os"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/aiconformance"
	"github.com/metavert-io/metarush/internal/aiservices/local"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/testutil/fakelocal"
	"github.com/metavert-io/metarush/internal/tools"
)

func TestOfflineVoicesConform(t *testing.T) {
	e, args := fakelocal.Voices(t)
	a, err := aiservices.Get(local.Name, aiservices.KindVoice)
	if err != nil {
		t.Fatal(err)
	}
	aiconformance.Run(t, aiconformance.Harness{Adapter: a, Env: &e})
	b, _ := os.ReadFile(args)
	said := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(said) < 3 || said[len(said)-2] != "--" || !strings.HasPrefix(said[0], "--num-threads=") {
		t.Errorf("the words don't come last, after --: %q", said)
	}
}

// A voice is chosen by its speaker ID, a language picks its usual voice, and
// British voices read with the British word list.
func TestOfflineVoiceSettings(t *testing.T) {
	e, args := fakelocal.Voices(t)
	a, _ := aiservices.Get(local.Name, aiservices.KindVoice)
	ctx := aiservices.WithEnv(context.Background(), e)
	cases := []struct {
		model, voice, language string
		want                   []string
	}{
		{local.ModelKokoro, "", "", []string{"--sid=3", "--kokoro-lang=en", "lexicon-us-en.txt"}},
		{local.ModelKokoro, "bm_george", "", []string{"--sid=26", "--kokoro-lang=en", "lexicon-gb-en.txt"}},
		{local.ModelKokoro, "", "es-ES", []string{"--sid=28", "--kokoro-lang=es"}},
		{local.ModelKitten, "expr-voice-4-m", "", []string{"--sid=4", "--kitten-model="}},
	}
	for _, c := range cases {
		req := &aiservices.Request{Kind: aiservices.KindVoice, Model: c.model, Prompt: "-- not an option, words", Options: aiservices.Options{Voice: c.voice, Language: c.language}}
		if _, err := a.Submit(ctx, "", req); err != nil {
			t.Fatalf("%+v: %v", c, err)
		}
		b, _ := os.ReadFile(args)
		for _, w := range c.want {
			if !strings.Contains(string(b), w) {
				t.Errorf("%+v: no %q in %s", c, w, b)
			}
		}
	}
	req := &aiservices.Request{Kind: aiservices.KindVoice, Model: local.ModelKokoro, Prompt: "hi", Options: aiservices.Options{Voice: "jf_alpha"}}
	if _, err := a.Submit(ctx, "", req); err == nil {
		t.Error("a Japanese voice the program can't speak was used")
	}
}

func TestOfflineVoicesSayHowToSetUp(t *testing.T) {
	s := fakelocal.Store(t)
	a, _ := aiservices.Get(local.Name, aiservices.KindVoice)
	err := a.(aiservices.Here).Ready(aiservices.Env{Tools: &tools.Set{}, Home: s.Home}, local.ModelKitten)
	if err == nil || !strings.Contains(err.Error(), "isn't set up on this computer yet: setting it up downloads 1 MB once") {
		t.Fatalf("%v", err)
	}
}

func TestBackgroundRemovalConforms(t *testing.T) {
	handed := fakelocal.Masker(t)
	e := aiservices.Env{Tools: &tools.Set{}, Cfg: &config.Config{}, Home: t.TempDir()}
	a, _ := aiservices.Get(local.Name, aiservices.KindBackground)
	aiconformance.Run(t, aiconformance.Harness{Adapter: a, Env: &e})
	if len(*handed) == 0 || (*handed)[0] != "image/png" {
		t.Fatalf("the mask was handed %v", *handed)
	}
	local.Masker = func(context.Context, aiservices.Env, string, string) (bool, error) { return false, nil }
	m, _ := aiservices.ModelFor(local.Name, aiservices.KindBackground, "")
	req := aiconformance.SampleRequest(t, m, t.TempDir())
	if _, err := a.Submit(aiservices.WithEnv(context.Background(), e), "", req); err == nil || !strings.Contains(err.Error(), "No subject was found") {
		t.Errorf("no subject: %v", err)
	}
}

func TestUpscaleConforms(t *testing.T) {
	e := fakelocal.Upscaler(t)
	a, _ := aiservices.Get(local.Name, aiservices.KindUpscale)
	aiconformance.Run(t, aiconformance.Harness{Adapter: a, Env: &e})
}

// Tiles meet without seams: with a model that only repeats pixels, the
// result is exactly each pixel repeated, across every tile edge, at any size.
func TestUpscaleTilesMeetExactly(t *testing.T) {
	for _, size := range [][2]int{{37, 21}, {128, 128}, {300, 130}, {241, 513}} {
		src := image.NewNRGBA(image.Rect(0, 0, size[0], size[1]))
		for y := 0; y < size[1]; y++ {
			for x := 0; x < size[0]; x++ {
				src.SetNRGBA(x, y, color.NRGBA{uint8(x * 7), uint8(y * 5), uint8(x ^ y), 255})
			}
		}
		out, err := local.Upscale(context.Background(), fakelocal.Nearest, src, 4)
		if err != nil {
			t.Fatal(err)
		}
		if out.Rect.Dx() != size[0]*4 || out.Rect.Dy() != size[1]*4 {
			t.Fatalf("%v: size %v", size, out.Rect)
		}
		for y := 0; y < out.Rect.Dy(); y += 3 {
			for x := 0; x < out.Rect.Dx(); x += 3 {
				if got, want := out.NRGBAAt(x, y), src.NRGBAAt(x/4, y/4); got != want {
					t.Fatalf("%v: pixel %d,%d = %v, want %v", size, x, y, got, want)
				}
			}
		}
		half, err := local.Upscale(context.Background(), fakelocal.Nearest, src, 2)
		if err != nil || half.Rect.Dx() != size[0]*2 {
			t.Fatalf("2×: %v %v", half.Rect, err)
		}
	}
	// See-through parts are carried over.
	src := image.NewNRGBA(image.Rect(0, 0, 20, 20))
	src.SetNRGBA(5, 5, color.NRGBA{255, 0, 0, 255})
	out, err := local.Upscale(context.Background(), fakelocal.Nearest, src, 4)
	if err != nil {
		t.Fatal(err)
	}
	if out.NRGBAAt(70, 70).A != 0 || out.NRGBAAt(21, 21).A < 200 {
		t.Errorf("alpha: far %v, the dot %v", out.NRGBAAt(70, 70), out.NRGBAAt(21, 21))
	}
}

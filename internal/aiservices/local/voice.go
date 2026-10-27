package local

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/localmodels"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Offline voices (D280): open voice models that run on this computer through
// sherpa-onnx's speech program, chosen for a licence that allows commercial
// use (Apache-2.0), how they sound and their size — Kokoro v1.0 (41 of its 54
// voices, in the six languages the program can speak; 350 MB) and Kitten nano
// (8 English voices, 31 MB), both measured in docs/ai-services.md. The program and the models are pinned
// downloads a person sets up once (internal/localmodels).

// Model IDs.
const (
	ModelKokoro = "kokoro-v1"
	ModelKitten = "kitten-nano"
)

// ProgramPin is the speech program's download.
const ProgramPin = "voices-program"

// kokoroVoices are Kokoro v1.0's voices in speaker-ID order (the model's own
// metadata); the first letter is the language, the second f or m.
var kokoroVoices = strings.Split("af_alloy,af_aoede,af_bella,af_heart,af_jessica,af_kore,af_nicole,af_nova,af_river,af_sarah,af_sky,am_adam,am_echo,am_eric,am_fenrir,am_liam,am_michael,am_onyx,am_puck,am_santa,bf_alice,bf_emma,bf_isabella,bf_lily,bm_daniel,bm_fable,bm_george,bm_lewis,ef_dora,em_alex,ff_siwis,hf_alpha,hf_beta,hm_omega,hm_psi,if_sara,im_nicola,jf_alpha,jf_gongitsune,jf_nezumi,jf_tebukuro,jm_kumo,pf_dora,pm_alex,pm_santa,zf_xiaobei,zf_xiaoni,zf_xiaoxiao,zf_xiaoyi,zm_yunjian,zm_yunxi,zm_yunxia,zm_yunyang,em_santa", ",")

// kittenVoices are Kitten nano v0.8's voices in speaker-ID order.
var kittenVoices = strings.Split("expr-voice-2-m,expr-voice-2-f,expr-voice-3-m,expr-voice-3-f,expr-voice-4-m,expr-voice-4-f,expr-voice-5-m,expr-voice-5-f", ",")

// kokoroLanguage is the language a Kokoro voice speaks, by its first letter
// (Japanese and Chinese need front ends the program lacks: not offered).
var kokoroLanguage = map[byte]string{'a': "en", 'b': "en", 'e': "es", 'f': "fr", 'h': "hi", 'i': "it", 'p': "pt-br"}

// kokoroDefault is each language's voice when none is chosen.
var kokoroDefault = map[string]string{"en": "af_heart", "es": "ef_dora", "fr": "ff_siwis", "hi": "hf_alpha", "it": "if_sara", "pt": "pf_dora"}

type voice struct{ base }

func init() { aiservices.Register(&voice{base{aiservices.KindVoice}}) }

// pinsFor are the downloads a model needs: the program, then the model.
func (a *voice) pinsFor(e aiservices.Env, model string) ([]localmodels.Pin, error) {
	s := Stores(e)
	prog, ok := s.Find(ProgramPin)
	if !ok {
		return nil, apperr.New(apperr.NotConfigured, "Offline voices don't run on this kind of computer yet.").WithDetails(map[string]any{"platform": runtime.GOOS + "/" + runtime.GOARCH})
	}
	m, ok := s.Find(firstNonEmpty(model, ModelKokoro))
	if !ok {
		return nil, apperr.New(apperr.InvalidInput, "MetaRush has no offline voice model called “%s”.", model)
	}
	return []localmodels.Pin{prog, m}, nil
}

// Ready says whether the program and the model are set up here, unchanged.
func (a *voice) Ready(e aiservices.Env, model string) error {
	ps, err := a.pinsFor(e, model)
	if err != nil {
		return err
	}
	s := Stores(e)
	for _, p := range ps {
		if err := s.Ready(p); err != nil {
			return err
		}
	}
	return nil
}

// Submit says the words with an offline voice.
func (a *voice) Submit(ctx context.Context, _ string, req *aiservices.Request) (*aiservices.Ticket, error) {
	text := strings.TrimSpace(req.Prompt)
	if text == "" {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "There are no words to say."}
	}
	e, done, err := env(ctx, "AI speech")
	if err != nil {
		return nil, err
	}
	defer done()
	model := firstNonEmpty(req.Model, ModelKokoro)
	if err := a.Ready(e, model); err != nil {
		return nil, notHere(err)
	}
	ps, _ := a.pinsFor(e, model)
	s := Stores(e)
	if !e.Tools.Use(tools.SherpaTTS, s.Path(ps[0], ps[0].Program)) {
		return nil, notHere(apperr.New(apperr.NotConfigured, "The offline voices' program can't run on this computer."))
	}
	in := func(rel string) string { return s.Path(ps[1], rel) }
	args, err := voiceArgs(model, in, req.Options.Voice, req.Options.Language)
	if err != nil {
		return nil, err
	}
	out := filepath.Join(e.Scratch, "speech.wav")
	threads := min(4, max(1, runtime.NumCPU()/2))
	args = append([]string{"--num-threads=" + strconv.Itoa(threads)}, args...)
	// The words come last, after "--", so words starting with a dash stay words.
	args = append(args, "--output-filename="+out, "--", text)
	if _, err := e.Tools.Run(ctx, tools.Spec{Tool: tools.SherpaTTS, Args: args, Background: true}); err != nil {
		return nil, err // retried: nothing left this computer
	}
	b, err := os.ReadFile(out)
	if err != nil || aiservices.WAVSeconds(b) <= 0 {
		return nil, &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "The offline voice made no sound.", Hint: "Try other words or another voice."}
	}
	st := &aiservices.Status{State: aiservices.StateDone, Outputs: []aiservices.Output{{MIME: "audio/wav", Data: b}},
		Usage: aiservices.Usage{Known: true, Seconds: aiservices.WAVSeconds(b), Characters: int64(len([]rune(text)))}}
	return ready(st), nil
}

// voiceArgs are the program's settings for a model, a voice and a language:
// the voice by its speaker ID, the language from the voice (Kokoro) or the
// language's usual voice when none was chosen.
func voiceArgs(model string, in func(string) string, voiceName, language string) ([]string, error) {
	lang := strings.ToLower(strings.SplitN(strings.SplitN(language, "-", 2)[0], "_", 2)[0])
	switch model {
	case ModelKitten:
		name := firstNonEmpty(voiceName, "expr-voice-2-f")
		sid := indexOf(kittenVoices, name)
		if sid < 0 {
			return nil, apperr.New(apperr.InvalidInput, "Kitten nano has no voice “%s”.", name).WithDetails(map[string]any{"voices": kittenVoices})
		}
		return []string{"--kitten-model=" + in("model.int8.onnx"), "--kitten-voices=" + in("voices.bin"), "--kitten-tokens=" + in("tokens.txt"),
			"--kitten-data-dir=" + in("espeak-ng-data"), "--sid=" + strconv.Itoa(sid)}, nil
	default:
		name := voiceName
		if name == "" {
			name = kokoroDefault[firstNonEmpty(lang, "en")]
			if name == "" {
				return nil, apperr.New(apperr.InvalidInput, "Kokoro has no voices in that language.")
			}
		}
		sid := indexOf(kokoroVoices, name)
		speaks := ""
		if sid >= 0 {
			speaks = kokoroLanguage[name[0]]
		}
		if speaks == "" {
			return nil, apperr.New(apperr.InvalidInput, "Kokoro has no voice “%s” MetaRush can use.", name)
		}
		lexicon := in("lexicon-us-en.txt")
		if name[0] == 'b' {
			lexicon = in("lexicon-gb-en.txt")
		}
		args := []string{"--kokoro-model=" + in("model.onnx"), "--kokoro-voices=" + in("voices.bin"), "--kokoro-tokens=" + in("tokens.txt"),
			"--kokoro-data-dir=" + in("espeak-ng-data"), "--kokoro-lang=" + speaks, "--sid=" + strconv.Itoa(sid)}
		if speaks == "en" {
			args = append(args, "--kokoro-lexicon="+lexicon)
		}
		return args, nil
	}
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

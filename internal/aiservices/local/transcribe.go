package local

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/transcribe"
	"github.com/metavert-io/metarush/internal/videogen"
)

// The local recognizers as a transcription provider (D278): the estimate for
// a transcript in the cloud offers "on this computer, free" beside the cloud
// services, and choosing it writes the clip down with the recognizer the
// ordinary transcribe job uses. Its words reach the clip the same way a
// cloud transcript's do (D228), so voices, people's fixes and spelling lists
// are handled once, for every recognizer.

// Model IDs and the recognizer each one is.
const (
	ModelParakeet = "parakeet-v2"
	ModelWhisper  = "whisper-cpp"
)

var recognizerOf = map[string]string{ModelParakeet: "parakeet", ModelWhisper: "whispercpp"}

// Recognizer finds the recognizer for a model (tests stand one in): nil and
// a plain error when it isn't set up here.
var Recognizer = func(e aiservices.Env, model, language string) (transcribe.Provider, error) {
	name, ok := recognizerOf[model]
	if !ok {
		return nil, apperr.New(apperr.InvalidInput, "MetaRush has no recognizer called “%s” on this computer.", model)
	}
	var p transcribe.Provider
	if e.Cfg != nil && e.Tools != nil {
		p = transcribe.Recognizer(e.Cfg.Transcription, e.Tools, name)
	}
	if p == nil {
		what := map[string]string{"parakeet": "Parakeet", "whispercpp": "whisper.cpp"}[name]
		return nil, apperr.New(apperr.NotConfigured, "%s isn't set up on this computer, so it can't write the words down here.", what).
			WithHint("Add it in Settings → Components.").WithCommand(map[string]string{"parakeet": "metarush components add transcription", "whispercpp": "metarush components add other-languages"}[name])
	}
	if model == ModelWhisper && language != "" && !transcribe.IsEnglish(language) && !transcribe.Multilingual(p) {
		return nil, apperr.New(apperr.NotConfigured, "This computer's whisper.cpp model writes only English.").
			WithHint("Add “Transcripts in other languages” in Settings → Components: it brings a model that writes every language.").WithCommand("metarush components add other-languages")
	}
	return p, nil
}

type transcription struct{ base }

func init() { aiservices.Register(&transcription{base{aiservices.KindTranscription}}) }

// Ready says whether the recognizer is set up on this computer.
func (a *transcription) Ready(e aiservices.Env, model string) error {
	if model == "" {
		model = ModelParakeet
	}
	_, err := Recognizer(e, model, "")
	return err
}

// Submit writes the recording down on this computer.
func (a *transcription) Submit(ctx context.Context, _ string, req *aiservices.Request) (*aiservices.Ticket, error) {
	in := req.Input(aiservices.RoleAudio)
	if in == nil {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "There's no recording to write down."}
	}
	e, done, err := env(ctx, "a transcript")
	if err != nil {
		return nil, err
	}
	defer done()
	language := strings.TrimSpace(req.Options.Language)
	if req.Model == ModelParakeet {
		language = "en"
	}
	rec, err := Recognizer(e, firstNonEmpty(req.Model, ModelParakeet), language)
	if err != nil {
		return nil, notHere(err)
	}
	// The recognizers read 16 kHz mono WAV.
	wav := filepath.Join(e.Scratch, "heard.wav")
	if err := (&derive.Deriver{T: e.Tools}).ExtractAudioWAV(ctx, in.Path, wav); err != nil {
		return nil, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't read the recording's sound.")
	}
	tr, err := rec.Transcribe(ctx, wav, e.Scratch, language)
	if err != nil {
		return nil, err // retried, like the ordinary transcribe job
	}
	out := &aiservices.Transcript{Language: firstNonEmpty(tr.Language, language), Recognizer: rec.Name(), RecognizerModel: rec.Model()}
	var texts []string
	for _, s := range tr.Segments {
		texts = append(texts, strings.TrimSpace(s.Text))
		for _, w := range s.Words {
			out.Words = append(out.Words, aiservices.Word{Text: w.Text, Start: w.From, End: w.To})
		}
		if len(s.Words) == 0 && strings.TrimSpace(s.Text) != "" {
			// A recognizer without word times: the line as one stretch.
			out.Words = append(out.Words, aiservices.Word{Text: strings.TrimSpace(s.Text), Start: s.From, End: s.To})
		}
	}
	out.Text = strings.TrimSpace(strings.Join(texts, " "))
	if len(out.Words) == 0 {
		return nil, &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "No words were heard in the recording on this computer."}
	}
	st := &aiservices.Status{State: aiservices.StateDone, Outputs: []aiservices.Output{{MIME: "application/json", Language: out.Language, Transcript: out}},
		Usage: aiservices.Usage{Known: true, Seconds: in.Seconds}}
	return ready(st), nil
}

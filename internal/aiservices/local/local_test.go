package local_test

import (
	"context"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/aiconformance"
	"github.com/metavert-io/metarush/internal/aiservices/local"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/transcribe"
)

// heard is a stand-in recognizer: it writes down the same three words for
// any recording, and remembers what it was asked.
type heard struct {
	name, model string
	asked       []string
}

func (h *heard) Name() string  { return h.name }
func (h *heard) Model() string { return h.model }
func (h *heard) Transcribe(_ context.Context, wav, _, language string) (*transcribe.Transcript, error) {
	h.asked = append(h.asked, language)
	if !strings.HasSuffix(wav, ".wav") {
		panic("the recognizer was handed something other than a WAV: " + wav)
	}
	return &transcribe.Transcript{Language: "en", Segments: []transcribe.Segment{{From: 0.2, To: 1.4, Text: "plant the tomatoes",
		Words: []models.Word{{Text: "plant", From: 0.2, To: 0.5}, {Text: "the", From: 0.5, To: 0.6}, {Text: "tomatoes", From: 0.6, To: 1.4}}}}}, nil
}

func standIn(t *testing.T) (*heard, aiservices.Env) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
	h := &heard{name: "parakeet", model: "tdt-0.6b-v2-int8"}
	old := local.Recognizer
	local.Recognizer = func(_ aiservices.Env, model, _ string) (transcribe.Provider, error) {
		if model == local.ModelWhisper {
			return &heard{name: "whisper.cpp", model: "large-v3-turbo"}, nil
		}
		return h, nil
	}
	t.Cleanup(func() { local.Recognizer = old })
	return h, aiservices.Env{Tools: tools.New(config.Tools{}), Cfg: &config.Config{}, Home: t.TempDir(), Scratch: t.TempDir()}
}

func TestTranscriptionOnThisComputerConforms(t *testing.T) {
	h, e := standIn(t)
	a, err := aiservices.Get(local.Name, aiservices.KindTranscription)
	if err != nil {
		t.Fatal(err)
	}
	aiconformance.Run(t, aiconformance.Harness{Adapter: a, Env: &e})
	if len(h.asked) == 0 || h.asked[0] != "en" {
		t.Errorf("Parakeet was asked %v, want English", h.asked)
	}
}

func TestTranscriptSaysWhichRecognizerWroteIt(t *testing.T) {
	_, e := standIn(t)
	a, _ := aiservices.Get(local.Name, aiservices.KindTranscription)
	m, _ := aiservices.ModelFor(local.Name, aiservices.KindTranscription, local.ModelWhisper)
	req := aiconformance.SampleRequest(t, m, t.TempDir())
	req.Options.Language = "es"
	tk, err := a.Submit(aiservices.WithEnv(context.Background(), e), "", req)
	if err != nil {
		t.Fatal(err)
	}
	tr := tk.Ready.Outputs[0].Transcript
	if tr.Recognizer != "whisper.cpp" || tr.RecognizerModel != "large-v3-turbo" || len(tr.Words) != 3 || tr.Text != "plant the tomatoes" {
		t.Errorf("transcript %+v", tr)
	}
	if !tk.Ready.Usage.Known || tk.Ready.Usage.Seconds <= 0 {
		t.Errorf("usage %+v", tk.Ready.Usage)
	}
}

// Without the recognizer installed it says so, with the step that sets it
// up, and never pretends to have run.
func TestNotSetUpSaysHow(t *testing.T) {
	a, _ := aiservices.Get(local.Name, aiservices.KindTranscription)
	here := a.(aiservices.Here)
	e := aiservices.Env{Tools: &tools.Set{}, Cfg: &config.Config{}}
	err := here.Ready(e, local.ModelParakeet)
	if err == nil {
		t.Fatal("Parakeet ready with no programs at all")
	}
	ae := apperr.From(err)
	if ae.Code != apperr.NotConfigured || !strings.Contains(ae.Message, "Parakeet isn't set up on this computer") {
		t.Errorf("%+v", ae)
	}
	if d, _ := ae.Details.(map[string]any); d["command"] != "metarush components add transcription" { // the one step that adds it (D326)
		t.Errorf("no setup command: %+v", ae.Details)
	}
	if err := aiservices.ReadyHere(e, "elevenlabs", aiservices.KindTranscription, "scribe_v2"); err != nil {
		t.Errorf("a cloud model's readiness is its key's business: %v", err)
	}
}

// The estimate reads "free" and the alternatives say "on this computer".
func TestFreeOnThisComputer(t *testing.T) {
	m, err := aiservices.ModelFor(local.Name, aiservices.KindTranscription, local.ModelParakeet)
	if err != nil {
		t.Fatal(err)
	}
	req := &aiservices.Request{Kind: aiservices.KindTranscription, Model: m.ID, Inputs: []aiservices.Media{{Role: aiservices.RoleAudio, Seconds: 600}}}
	est, err := aiservices.EstimateFor(local.Name, m, req, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if est.USD != 0 || !strings.Contains(est.Rate, "free") || est.Units != "10 minutes heard" {
		t.Errorf("estimate %+v", est)
	}
	cloud, _ := aiservices.ModelFor("elevenlabs", aiservices.KindTranscription, "scribe_v2")
	req.Model = cloud.ID
	alts := aiservices.AlternativesWhere(cloud, req, 0.04, time.Now(), func(*aiservices.Model) bool { return true })
	found := false
	for _, alt := range alts {
		if alt.Provider == local.Name {
			found = alt.Why == "cheaper" && strings.Contains(alt.Summary, "on this computer: free")
		}
	}
	if !found {
		t.Errorf("the local recognizer isn't the cheaper alternative: %+v", alts)
	}
}

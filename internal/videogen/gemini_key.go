package videogen

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/metavert-io/metarush/internal/providers"
)

// geminiKey is Google Gemini in the integrations registry: where its key
// lives (this machine's keychain, or the installation's private file;
// METARUSH_GEMINI_API_KEY overrides both), how it is checked and how Settings
// describes it. The key is never stored anywhere else.
type geminiKey struct{}

func init() { providers.Register(geminiKey{}) }

func (geminiKey) Name() string     { return GeminiName }
func (geminiKey) Title() string    { return GeminiTitle }
func (geminiKey) Category() string { return providers.CategoryVideo }
func (geminiKey) Summary() string {
	return "AI-generated video clips for your projects: a shot from a few words, from a frame of your footage or between two frames, a clip extended or changed by an instruction. The same key makes pictures, AI speech, music and copies of a voice (with the person's consent), and writes down recordings in the cloud when you ask. You pay Google for what it makes, and MetaRush shows the price before anything is made."
}
func (geminiKey) KeyHelp() string {
	return "Create one in Google AI Studio under API keys. Making video needs a Google project with billing turned on (the paid tier)."
}

// KeyHosts is where the Gemini key may go (an ability that names it, D235).
func (geminiKey) KeyHosts() []string { return []string{"generativelanguage.googleapis.com"} }

func (geminiKey) Capabilities() []string {
	return []string{"text-to-video", "image-to-video", "references", "between-frames", "extend", "edit", "pictures", "speech", "voice-copies", "music", "transcription"}
}

// Test checks a key by listing the models it may use (free) and says whether
// the video model is among them. That list doesn't show whether the key's
// project can make video: a free-tier project lists the video model too, and
// Google turns it away only once a shot is asked for (2026-10-20). Nothing
// Google offers to an API key tells the tier without spending or starting
// something (no tier or quota read, no free probe), so the check says what it
// knows and the first shot tells the rest (KindTier, D178).
func (geminiKey) Test(ctx context.Context, key string) (*providers.TestResult, error) {
	models, err := Gemini{}.ListModels(ctx, key)
	var ve *Error
	switch {
	case errors.As(err, &ve) && ve.Kind == KindKey:
		return &providers.TestResult{OK: false, Message: "Google did not accept this key. Check that it was copied completely and hasn't been deleted."}, nil
	case errors.As(err, &ve) && ve.Kind == KindPermission && strings.Contains(ve.Message, "blocked this key's project"):
		return &providers.TestResult{OK: false, Message: ve.Message}, nil
	case errors.As(err, &ve) && ve.Kind == KindPermission:
		return &providers.TestResult{OK: false, Message: "This key can't use the Gemini API. Check its project in Google AI Studio."}, nil
	case errors.As(err, &ve) && ve.Kind == KindTier:
		return &providers.TestResult{OK: false, Message: ve.Message}, nil
	case err != nil:
		return nil, err
	}
	info, _ := Info(GeminiName)
	model, title := "", ""
	if info != nil {
		model = info.Defaults.Model
		if m, err := ModelInfo(GeminiName, model); err == nil {
			title = m.Title
		}
	}
	if slices.Contains(models, model) {
		return &providers.TestResult{OK: true, Message: "Connected. Google makes video with " + title + " only for projects with billing turned on; MetaRush tells you on the first shot if this one can't."}, nil
	}
	return &providers.TestResult{OK: true, Message: "Connected, but this key's project doesn't offer " + title + " yet. Making video needs billing turned on (the paid tier)."}, nil
}

// What the Gemini API's other services (internal/aiservices/google, D224)
// share with video: the error reading, the Files API and its clean-up, all
// with the key only in the header to Google's own host.

// ClassifyGoogle turns an error answer from the Gemini API into a plain-words
// Error (video's words; other kinds reword them by Kind).
func ClassifyGoogle(status int, body []byte, header http.Header, phase string) *Error {
	return classifyHTTP(status, body, header, phase)
}

// Upload sends a file through the Files API (checked against its SHA-256,
// waited on until it can be used) and returns its name and address.
func (g Gemini) Upload(ctx context.Context, key string, m Media) (name, uri string, err error) {
	c, err := g.client(key)
	if err != nil {
		return "", "", err
	}
	f, err := c.upload(ctx, m)
	if err != nil {
		return "", "", err
	}
	return f.Name, f.URI, nil
}

// DeleteFiles deletes files at Google (best effort; Google deletes them after two days anyway).
func (g Gemini) DeleteFiles(ctx context.Context, key string, names []string) error {
	c, err := g.client(key)
	if err != nil {
		return err
	}
	return c.deleteFiles(ctx, names)
}

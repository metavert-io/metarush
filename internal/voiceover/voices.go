package voiceover

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/providers"
)

// VoicesIn lists voices: ElevenLabs (the account's voices, or the public
// library with library=true) or the free local scratch voices.
type VoicesIn struct {
	Provider  string `json:"provider,omitempty" jsonschema:"enum=elevenlabs,enum=local,description=elevenlabs (default) or local (free scratch voices)."`
	Search    string `json:"search,omitempty" jsonschema:"maxLength=120"`
	Library   bool   `json:"library,omitempty" jsonschema:"description=Search ElevenLabs' public voice library instead of your account."`
	PageToken string `json:"pageToken,omitempty" jsonschema:"maxLength=400"`
}

// VoiceView is one voice for pickers.
type VoiceView struct {
	Provider    string            `json:"provider"`
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Category    string            `json:"category,omitempty"`
	Description string            `json:"description,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
	Traits      string            `json:"traits,omitempty"` // plain summary: "female · british · middle aged · narration"
	HasPreview  bool              `json:"hasPreview"`
	Favorite    bool              `json:"favorite,omitempty"`
	Library     bool              `json:"library,omitempty"`
	OwnerID     string            `json:"ownerId,omitempty"`
	Cloned      bool              `json:"cloned,omitempty"`
	Locale      string            `json:"locale,omitempty"`
}

// VoicesOut is a page of voices.
type VoicesOut struct {
	Provider      string      `json:"provider"`
	Voices        []VoiceView `json:"voices"`
	NextPageToken string      `json:"nextPageToken,omitempty"`
	Note          string      `json:"note,omitempty"`
}

// previewURLs remembers the sample URLs of recently listed voices, so a voice
// found by a library search can be played (the library cannot be looked up by
// voice ID). The URLs point at the provider's public sample files.
var previewURLs = struct {
	sync.Mutex
	m map[string]string
}{m: map[string]string{}}

func rememberPreviews(vs []providers.Voice) {
	previewURLs.Lock()
	defer previewURLs.Unlock()
	if len(previewURLs.m) > 5000 {
		previewURLs.m = map[string]string{}
	}
	for _, v := range vs {
		if v.PreviewURL != "" {
			previewURLs.m[v.VoiceID] = v.PreviewURL
		}
	}
}

func rememberedPreview(id string) string {
	previewURLs.Lock()
	defer previewURLs.Unlock()
	return previewURLs.m[id]
}

func traits(l map[string]string) string {
	var parts []string
	for _, k := range []string{"gender", "accent", "age", "descriptive", "use_case", "language"} {
		if v := strings.TrimSpace(l[k]); v != "" {
			parts = append(parts, strings.ReplaceAll(v, "_", " "))
		}
	}
	return strings.Join(parts, " · ")
}

func listVoices(ctx context.Context, a *app.App, in *VoicesIn) (*VoicesOut, error) {
	if in.Provider == "local" {
		vs, err := LocalVoices(ctx, a)
		if err != nil {
			return nil, err
		}
		out := &VoicesOut{Provider: "local", Voices: []VoiceView{}, Note: "Free system voices for timing and review (scratch narration). They sound robotic on purpose — use ElevenLabs or a recording for the final video."}
		q := strings.ToLower(in.Search)
		for _, v := range vs {
			if q != "" && !strings.Contains(strings.ToLower(v.Name+" "+v.Locale), q) {
				continue
			}
			out.Voices = append(out.Voices, VoiceView{Provider: "local", ID: v.Name, Name: v.Name, Locale: v.Locale, Description: v.Sample, Traits: v.Locale, HasPreview: true})
		}
		return out, nil
	}
	c, err := providers.ElevenClientFor(ctx, a)
	if err != nil {
		return nil, err
	}
	ws, _, _, _ := providers.LoadSettings(ctx, a, providers.ElevenLabsName, "")
	fav := map[string]bool{}
	if ws != nil {
		for _, f := range ws.Favorites {
			fav[f.VoiceID] = true
		}
	}
	var page *providers.VoicesPage
	if in.Library {
		pg := 0
		fmt.Sscanf(in.PageToken, "%d", &pg)
		page, err = c.SharedVoices(ctx, in.Search, pg, 30)
	} else {
		page, err = c.Voices(ctx, in.Search, in.PageToken, 100, nil)
	}
	if err != nil {
		return nil, err
	}
	rememberPreviews(page.Voices)
	if !in.Library {
		providers.SortVoices(page.Voices, fav)
	}
	out := &VoicesOut{Provider: providers.ElevenLabsName, Voices: []VoiceView{}, NextPageToken: page.NextPageToken}
	for _, v := range page.Voices {
		out.Voices = append(out.Voices, VoiceView{Provider: providers.ElevenLabsName, ID: v.VoiceID, Name: v.Name, Category: v.Category, Description: v.Description,
			Labels: v.Labels, Traits: traits(v.Labels), HasPreview: v.HasPreview, Favorite: fav[v.VoiceID], Library: v.Library, OwnerID: v.OwnerID, Cloned: v.Cloned})
	}
	if in.Library {
		out.Note = "Voices from ElevenLabs' public library are added to your account when you choose one."
	}
	return out, nil
}

// ModelsOut lists the account's ElevenLabs models with what they can do.
type ModelsOut struct {
	Models  []providers.Model `json:"models"`
	Default string            `json:"default,omitempty"`
	Note    string            `json:"note"`
}

func listModels(ctx context.Context, a *app.App) (*ModelsOut, error) {
	c, err := providers.ElevenClientFor(ctx, a)
	if err != nil {
		return nil, err
	}
	ms, err := c.Models(ctx)
	if err != nil {
		return nil, err
	}
	out := &ModelsOut{Models: []providers.Model{}, Note: "Credits per character: 1 means one credit per character; lower is cheaper (and usually a little less natural)."}
	for _, m := range ms {
		if m.TTS {
			out.Models = append(out.Models, m)
		}
	}
	if d := providers.PickDefaultModel(ms); d != nil {
		out.Default = d.ModelID
	}
	return out, nil
}

// PreviewIn fetches a voice sample.
type PreviewIn struct {
	Provider string `json:"provider,omitempty" jsonschema:"enum=elevenlabs,enum=local"`
	Voice    string `json:"voice" jsonschema:"minLength=1,maxLength=120,description=Voice ID (ElevenLabs) or name (local)."`
	Library  bool   `json:"library,omitempty"`
	Out      string `json:"out,omitempty" jsonschema:"description=File to write (default: the scratch folder)."`
}

// PreviewOut is a downloaded sample.
type PreviewOut struct {
	Path        string `json:"path"`
	ContentType string `json:"contentType"`
	Bytes       int    `json:"bytes"`
}

// PreviewAudio returns a voice's sample bytes (the dashboard streams these; the
// API key never leaves the daemon and previews are fetched without it).
func PreviewAudio(ctx context.Context, a *app.App, provider, voice string, library bool) ([]byte, string, error) {
	if provider == "local" {
		eng, err := localEngine(a)
		if err != nil {
			return nil, "", err
		}
		path, done, err := scratchFile(a, "preview.wav")
		if err != nil {
			return nil, "", err
		}
		defer done()
		if err := synthLocal(ctx, a, eng, voice, 165, "Hello! This is how this voice sounds in your video.", path); err != nil {
			return nil, "", apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't play the %s voice.", voice)
		}
		b, err := os.ReadFile(path)
		return b, "audio/wav", err
	}
	c, err := providers.ElevenClientFor(ctx, a)
	if err != nil {
		return nil, "", err
	}
	if u := rememberedPreview(voice); u != "" {
		return providers.FetchPreview(ctx, u, 8<<20)
	}
	var vs []providers.Voice
	if library {
		pg, err := c.SharedVoices(ctx, "", 0, 100)
		if err != nil {
			return nil, "", err
		}
		vs = pg.Voices
	} else {
		pg, err := c.Voices(ctx, "", "", 100, []string{voice})
		if err != nil {
			return nil, "", err
		}
		vs = pg.Voices
	}
	rememberPreviews(vs)
	for _, v := range vs {
		if v.VoiceID == voice {
			if v.PreviewURL == "" {
				return nil, "", apperr.New(apperr.NotFound, "%s has no sample to play.", v.Name)
			}
			return providers.FetchPreview(ctx, v.PreviewURL, 8<<20)
		}
	}
	return nil, "", apperr.New(apperr.NotFound, "That voice isn't in your ElevenLabs account.").WithDetails(map[string]any{"voice": voice})
}

func preview(ctx context.Context, a *app.App, in *PreviewIn) (*PreviewOut, error) {
	b, ct, err := PreviewAudio(ctx, a, in.Provider, in.Voice, in.Library)
	if err != nil {
		return nil, err
	}
	ext := map[string]string{"audio/wav": ".wav", "audio/x-wav": ".wav", "audio/ogg": ".ogg"}[ct]
	if ext == "" {
		ext = ".mp3"
	}
	path := in.Out
	if path == "" {
		dir := filepath.Join(a.Cfg.Scratch.Dir, "voice-previews")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		path = filepath.Join(dir, safeName(in.Voice)+ext)
	}
	if err := os.WriteFile(path, b, 0o644); err != nil {
		return nil, err
	}
	return &PreviewOut{Path: path, ContentType: ct, Bytes: len(b)}, nil
}

func safeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r == '-' || r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
		} else {
			b.WriteRune('-')
		}
	}
	return b.String()
}

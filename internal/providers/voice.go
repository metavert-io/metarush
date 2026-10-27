package providers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// ResolvedVoice is the voice, model and settings one generation uses, after
// applying beat → project → workspace precedence field by field.
type ResolvedVoice struct {
	Provider     string                    `json:"provider"`
	VoiceID      string                    `json:"voiceId,omitempty"`
	VoiceName    string                    `json:"voiceName,omitempty"`
	ModelID      string                    `json:"modelId,omitempty"`
	ModelName    string                    `json:"modelName,omitempty"`
	Settings     editspec.TTSVoiceSettings `json:"settings"`
	OutputFormat string                    `json:"outputFormat"`
	Region       string                    `json:"region,omitempty"`
	// From says where each choice came from: beat (the line's own) | settings
	// (the project's Content Settings, D193) | default.
	From map[string]string `json:"from"`
}

// ResolveVoice merges the voice choices of the Content Settings in use (ws),
// a project's provider settings (proj: its budget; any voice choice there is
// from before D193) and a line's own voice (each may be nil): the line wins.
func ResolveVoice(ws, proj *models.ProviderSettings, beat *editspec.VoiceRef) ResolvedVoice {
	r := ResolvedVoice{Provider: ElevenLabsName, OutputFormat: DefaultOutputFormat, From: map[string]string{}}
	type layer struct {
		scope     string
		voiceID   string
		voiceName string
		modelID   string
		modelName string
		s         *editspec.TTSVoiceSettings
	}
	var layers []layer
	if ws != nil {
		layers = append(layers, layer{"settings", ws.VoiceID, ws.VoiceName, ws.ModelID, ws.ModelName, ws.Voice})
		r.Region = ws.Region
	}
	if proj != nil {
		layers = append(layers, layer{"project", proj.VoiceID, proj.VoiceName, proj.ModelID, proj.ModelName, proj.Voice})
	}
	if beat != nil && beat.Provider == ElevenLabsName {
		layers = append(layers, layer{"beat", beat.VoiceID, beat.Name, beat.ModelID, "", beat.Settings})
	}
	for _, l := range layers {
		if l.voiceID != "" {
			r.VoiceID, r.VoiceName, r.From["voice"] = l.voiceID, l.voiceName, l.scope
		}
		if l.modelID != "" {
			r.ModelID, r.ModelName, r.From["model"] = l.modelID, l.modelName, l.scope
		}
		if s := l.s; s != nil {
			set := func(key string, dst **float64, v *float64) {
				if v != nil {
					x := *v
					*dst, r.From[key] = &x, l.scope
				}
			}
			set("stability", &r.Settings.Stability, s.Stability)
			set("similarityBoost", &r.Settings.SimilarityBoost, s.SimilarityBoost)
			set("style", &r.Settings.Style, s.Style)
			set("speed", &r.Settings.Speed, s.Speed)
			if s.SpeakerBoost != nil {
				b := *s.SpeakerBoost
				r.Settings.SpeakerBoost, r.From["speakerBoost"] = &b, l.scope
			}
			if s.OutputFormat != "" {
				r.OutputFormat, r.From["outputFormat"] = s.OutputFormat, l.scope
			}
		}
	}
	return r
}

// SettingsKey is the canonical form of the voice settings and output format;
// any change gives a new key (and so a new TTS cache key).
func (r ResolvedVoice) SettingsKey() string {
	s := r.Settings
	s.OutputFormat = r.OutputFormat
	b, _ := json.Marshal(s)
	return string(b)
}

// CacheKey identifies a generation: provider, model, voice, settings and text.
// Unchanged lines map to the same key, so they are never paid for twice.
func CacheKey(r ResolvedVoice, text string) string {
	h := sha256.New()
	fmt.Fprintf(h, "%s\x00%s\x00%s\x00%s\x00%s", r.Provider, r.ModelID, r.VoiceID, r.SettingsKey(), strings.TrimSpace(text))
	return hex.EncodeToString(h.Sum(nil))
}

// FindModel returns a model by ID.
func FindModel(models []Model, id string) *Model {
	for i := range models {
		if models[i].ModelID == id {
			return &models[i]
		}
	}
	return nil
}

// ApplyDefaultModel fills an unset model with PickDefaultModel from the live list.
func (r *ResolvedVoice) ApplyDefaultModel(models []Model) {
	if r.ModelID != "" {
		return
	}
	if m := PickDefaultModel(models); m != nil {
		r.ModelID, r.ModelName, r.From["model"] = m.ModelID, m.Name, "default"
	}
}

// ValidateVoice checks a resolution against the live model capabilities and a
// line's text. It returns warnings (settings the model ignores) or an error.
func ValidateVoice(r ResolvedVoice, models []Model, text string) ([]string, error) {
	var warns []string
	if r.VoiceID == "" {
		// "voice is chosen": job details advise choosing a voice on these words.
		return nil, apperr.New(apperr.NotConfigured, "No ElevenLabs voice is chosen yet.").
			WithHint("Pick the narration voice in Settings → Integrations.").WithCommand("metarush providers settings elevenlabs --voice <name>")
	}
	m := FindModel(models, r.ModelID)
	switch {
	case r.ModelID == "":
		return nil, apperr.New(apperr.NotConfigured, "MetaRush couldn't choose an ElevenLabs model: your account has none that turns text into speech.")
	case m == nil:
		model := "The ElevenLabs model you chose"
		if r.ModelName != "" {
			model = "The ElevenLabs model " + r.ModelName
		}
		return nil, apperr.New(apperr.InvalidInput, "%s isn't available to your account any more.", model).
			WithHint("Choose another model in Settings → Integrations.").WithDetails(map[string]any{"modelId": r.ModelID}).WithCommand("metarush voiceover models")
	case !m.TTS:
		return nil, apperr.New(apperr.InvalidInput, "The ElevenLabs model %s can't turn text into speech.", m.Name)
	}
	if n := len([]rune(text)); m.MaxChars > 0 && n > m.MaxChars {
		return nil, apperr.New(apperr.InvalidInput, "This line has %d characters, more than %s takes at once (%d). Split it into two lines.", n, m.Name, m.MaxChars)
	}
	if r.Settings.Style != nil && *r.Settings.Style > 0 && !m.CanUseStyle {
		warns = append(warns, fmt.Sprintf("%s ignores the speaking style setting", m.Name))
	}
	if r.Settings.SpeakerBoost != nil && *r.Settings.SpeakerBoost && !m.CanUseBoost {
		warns = append(warns, fmt.Sprintf("%s ignores “Make it sound more like the original voice”", m.Name))
	}
	if s := r.Settings.Speed; s != nil && (*s < 0.7 || *s > 1.2) {
		return nil, apperr.New(apperr.InvalidInput, "The voice speed must be between 0.7 and 1.2.")
	}
	for _, v := range []*float64{r.Settings.Stability, r.Settings.SimilarityBoost, r.Settings.Style} {
		if v != nil && (*v < 0 || *v > 1) {
			return nil, apperr.New(apperr.InvalidInput, "The voice settings must be between 0 and 1.")
		}
	}
	return warns, nil
}

// ToAPI converts settings to the ElevenLabs request, dropping what the model ignores.
func ToAPI(s editspec.TTSVoiceSettings, m *Model) TTSVoiceSettings {
	out := TTSVoiceSettings{Stability: s.Stability, SimilarityBoost: s.SimilarityBoost, Speed: s.Speed}
	if m == nil || m.CanUseStyle {
		out.Style = s.Style
	}
	if m == nil || m.CanUseBoost {
		out.UseSpeakerBoost = s.SpeakerBoost
	}
	return out
}

// Credits estimates what a line costs with a model (characters × multiplier).
func Credits(text string, m *Model) float64 {
	mult := 1.0
	if m != nil && m.CostMultiplier > 0 {
		mult = m.CostMultiplier
	}
	return float64(len([]rune(strings.TrimSpace(text)))) * mult
}

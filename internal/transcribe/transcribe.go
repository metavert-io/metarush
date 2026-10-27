// Package transcribe defines local transcript providers. Transcription runs on the
// user's machine by default: no footage leaves it and no paid API is involved.
package transcribe

import (
	"context"
	"strings"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// Segment is a timed transcript segment in source seconds.
type Segment struct {
	From, To float64
	Text     string
	Speaker  string
	Words    []models.Word
}

// Transcript is a provider's result.
type Transcript struct {
	Language string
	Segments []Segment
	// Engine says how the model ran (ModeResident, ModePerBatch; "" for
	// providers that only have one way).
	Engine string
	// Hints is how many spelling hints the transcriber was given.
	Hints int
}

// How a provider runs its model.
const (
	// ModeResident: the model stays loaded in this process between batches and jobs.
	ModeResident = "resident"
	// ModePerBatch: one program run per batch of chunks.
	ModePerBatch = "per-batch"
)

// Provider transcribes 16 kHz mono WAV audio.
type Provider interface {
	Name() string
	Model() string
	Transcribe(ctx context.Context, wav, scratch, language string) (*Transcript, error)
}

// Hinted is a provider that can be told words to expect (the names and terms
// on the spelling lists, D147), so it writes them as listed. Parakeet takes
// them as hotwords; whisper.cpp does not: its prompt only works without
// MetaRush's no-carried-text setting, and with it word times got worse and
// noise turned into made-up sentences (measured; docs/transcripts.md).
type Hinted interface {
	TranscribeHinted(ctx context.Context, wav, scratch, language string, hints []string) (*Transcript, error)
}

// TakesHints reports whether a provider uses spelling hints.
func TakesHints(p Provider) bool {
	_, ok := p.(Hinted)
	return ok
}

// Moder is a provider that can say how it runs its model (ModeResident or
// ModePerBatch) and, after a transcription, why it could not keep the model in
// memory ("" when it did).
type Moder interface {
	Mode() string
	Fallback() string
}

// WhyNotResident says why a provider can't keep its model loaded between
// clips on this machine ("" when it can, when that is turned off, or for a
// provider with only one way).
func WhyNotResident(p Provider) string {
	var s *modelServer
	var err error
	switch v := p.(type) {
	case *whisperCPP:
		if !v.resident {
			return ""
		}
		s, err = whisperServerFor(v.t, v.model, whisperThreads())
	case *parakeet:
		if !v.resident {
			return ""
		}
		s, err = serverFor(v.t, v.lib, v.dir, parakeetThreads())
	default:
		return ""
	}
	if err == nil {
		s.mu.Lock()
		err = s.disabled
		s.mu.Unlock()
	}
	if err == nil {
		return ""
	}
	return strings.TrimPrefix(err.Error(), ErrResidentUnavailable.Error()+": ")
}

// Select picks the configured provider, or the best available one for "auto".
// It returns nil when nothing usable is installed (transcription is then simply
// unavailable and reported honestly as such).
func Select(cfg config.Transcription, t *tools.Set) Provider {
	candidates := map[string]func() Provider{
		"whispercpp": func() Provider { return newWhisperCPP(t, cfg.Model, cfg.KeepModelLoaded()) },
		"parakeet":   func() Provider { return newParakeet(t, cfg.Model, cfg.KeepModelLoaded()) },
	}
	switch cfg.Provider {
	case "none":
		return nil
	case "", "auto":
		if l := cfg.Language; l != "" && l != "auto" && !strings.HasPrefix(l, "en") {
			return candidates["whispercpp"]() // Parakeet v2 is English-only
		}
		for _, name := range AutoOrder {
			if p := candidates[name](); p != nil {
				return p
			}
		}
		return nil
	default:
		if f, ok := candidates[cfg.Provider]; ok {
			return f()
		}
		return nil
	}
}

// AutoOrder is the preference order for provider "auto". Parakeet v2 won the
// comparison for English (timing, accuracy, no hallucinations);
// whisper.cpp is the multilingual fallback.
var AutoOrder = []string{"parakeet", "whispercpp"}

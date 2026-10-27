package speakers

import (
	"context"
	"os"
	"path/filepath"
	"sync"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/voice"
)

var (
	sharedMu      sync.Mutex
	sharedEngines = map[string]*Engine{}
)

// SharedEngine is this process's speaker engine for an installation's tools
// (opened once, so the fingerprint model loads once): nil when speaker
// detection is not installed here.
func SharedEngine(a *app.App) *Engine {
	key := a.Tools.ModelsDir()
	sharedMu.Lock()
	defer sharedMu.Unlock()
	if e, ok := sharedEngines[key]; ok {
		return e
	}
	e := NewEngine(a)
	sharedEngines[key] = e
	return e
}

// Hooks for tests: where a clip's sound and the fingerprint extractor come from.
var (
	pcmFor   = loadPCM
	embedFor = embedder
)

// SetAudioForTests replaces how a clip's sound is read and how voices are
// fingerprinted (tests use synthetic sound and a fake extractor). It returns a
// function that restores the real ones.
func SetAudioForTests(pcm func(ctx context.Context, a *app.App, as *models.Asset) ([]float32, error), emb func(a *app.App) voice.Embedder) func() {
	p, e := pcmFor, embedFor
	pcmFor, embedFor = pcm, emb
	return func() { pcmFor, embedFor = p, e }
}

// embedder is the voice-fingerprint extractor on this machine (nil when
// unavailable: voices are then relabeled but not re-learned).
func embedder(a *app.App) voice.Embedder {
	e := SharedEngine(a)
	if e == nil {
		return nil
	}
	if ex := e.Extractor(); ex != nil {
		return ex
	}
	return nil
}

// loadPCM extracts a clip's sound as 16 kHz mono samples on this machine (from
// the cached preview copy or the original).
func loadPCM(ctx context.Context, a *app.App, as *models.Asset) ([]float32, error) {
	src := ""
	if px, ok := as.Variants[models.VariantProxy]; ok && as.Media.HasVideo {
		if p, err := a.Cache.Ensure(ctx, a.Media, px.StorageKey, px.SHA256, px.Size, nil); err == nil {
			src = p
		}
	}
	if src == "" {
		p, err := a.Cache.Ensure(ctx, a.Media, as.StorageKey, as.SHA256, as.Size, nil)
		if err != nil {
			return nil, err
		}
		src = p
	}
	dir := filepath.Join(a.Cfg.Scratch.Dir, "voices", ids.New("tmp"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	wav := filepath.Join(dir, "audio.wav")
	if err := (&derive.Deriver{T: a.Tools}).ExtractAudioWAV(ctx, src, wav); err != nil {
		return nil, err
	}
	return voice.ReadWAV(wav)
}

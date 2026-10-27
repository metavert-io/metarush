//go:build voiceeval

package transcribe

import (
	"context"
	"encoding/json"
	"os"
	"testing"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
)

// TestEvalWords transcribes EVAL_WAV with Parakeet and writes the words to
// EVAL_OUT (evaluation harness only; run with -tags voiceeval).
func TestEvalWords(t *testing.T) {
	wav, out := os.Getenv("EVAL_WAV"), os.Getenv("EVAL_OUT")
	if wav == "" || out == "" {
		t.Skip("EVAL_WAV/EVAL_OUT not set")
	}
	p := newParakeet(tools.New(config.Tools{}), "", os.Getenv("EVAL_RESIDENT") != "0")
	if p == nil {
		t.Skip("parakeet not installed")
	}
	tr, err := p.Transcribe(context.Background(), wav, t.TempDir(), "en")
	if err != nil {
		t.Fatal(err)
	}
	var words []models.Word
	for _, s := range tr.Segments {
		words = append(words, s.Words...)
	}
	b, _ := json.Marshal(words)
	if err := os.WriteFile(out, b, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Logf("%d words", len(words))
}

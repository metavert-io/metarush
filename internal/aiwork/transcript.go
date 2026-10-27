package aiwork

import (
	"context"
	"encoding/json"
	"os"
	"strings"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/transcribe"
)

// Transcription in the cloud is opt-in (local stays the default, D10, D228):
// the AI work keeps the provider's words as a file, and the clip's ordinary
// transcribe job takes them instead of running the local recognizer, so the
// voices, people's word fixes and the spelling lists are handled exactly as
// for a local transcript.

// CloudTranscript reads the words a piece of transcription work kept, as a
// transcript in segments, with the provider and model to record.
func CloudTranscript(ctx context.Context, a *app.App, genID string) (*transcribe.Transcript, string, string, error) {
	g, err := loadGen(ctx, a, genID)
	if err != nil {
		return nil, "", "", err
	}
	if g.Kind != aiservices.KindTranscription || g.Outcome == nil || g.Outcome.State != models.GenDone {
		return nil, "", "", apperr.New(apperr.Conflict, "That transcription in the cloud isn't ready.")
	}
	var out *models.GenOutput
	for i := range g.Outcome.Outputs {
		if g.Outcome.Outputs[i].StorageKey != "" {
			out = &g.Outcome.Outputs[i]
		}
	}
	if out == nil {
		return nil, "", "", apperr.New(apperr.NotFound, "The cloud transcript wasn't kept.")
	}
	path, err := generate.Local(ctx, a, out.StorageKey, out.SHA256, out.Size)
	if err != nil {
		return nil, "", "", err
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, "", "", err
	}
	var tr aiservices.Transcript
	if err := json.Unmarshal(b, &tr); err != nil {
		return nil, "", "", apperr.Wrap(apperr.Internal, err, "MetaRush couldn't read the cloud transcript it kept.")
	}
	if tr.Recognizer != "" {
		// Written on this computer (D278): named as a local transcript is.
		return &transcribe.Transcript{Language: tr.Language, Segments: Segments(tr.Words)}, tr.Recognizer, tr.RecognizerModel, nil
	}
	return &transcribe.Transcript{Language: tr.Language, Segments: Segments(tr.Words)}, g.Provider, g.Model, nil
}

// Segments groups timed words into lines the way the local recognizers do:
// a new line at a change of speaker, a pause of more than 0.7 seconds, or
// after a sentence once a line has a dozen words (25 at most).
func Segments(words []aiservices.Word) []transcribe.Segment {
	var out []transcribe.Segment
	var cur *transcribe.Segment
	flush := func() {
		if cur != nil && len(cur.Words) > 0 {
			var texts []string
			for _, w := range cur.Words {
				texts = append(texts, w.Text)
			}
			cur.Text = strings.Join(texts, " ")
			out = append(out, *cur)
		}
		cur = nil
	}
	for _, w := range words {
		text := strings.TrimSpace(w.Text)
		if text == "" {
			continue
		}
		if cur != nil {
			last := cur.Words[len(cur.Words)-1]
			n := len(cur.Words)
			if w.Speaker != cur.Speaker || w.Start-last.To > 0.7 || n >= 25 || (n >= 12 && strings.ContainsAny(last.Text[len(last.Text)-1:], ".?!")) {
				flush()
			}
		}
		if cur == nil {
			cur = &transcribe.Segment{From: w.Start, Speaker: w.Speaker}
		}
		cur.Words = append(cur.Words, models.Word{Text: text, From: w.Start, To: w.End})
		cur.To = w.End
	}
	flush()
	return out
}

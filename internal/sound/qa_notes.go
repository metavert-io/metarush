package sound

import (
	"context"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/worker"
)

func init() { worker.ClearerVoices = clearerVoicesIn }

// clearerVoicesIn is what QA says about the clearer voices a render plays:
// one sentence per person (and the narration), from the measurements taken
// when the copies were made.
func clearerVoicesIn(ctx context.Context, a *app.App, plan *editspec.RenderPlan) []string {
	seen := map[string]bool{}
	var copies, narration []CopyView
	add := func(id string, voice bool) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		r := copyRecordByVariant(ctx, a, id)
		if r == nil || len(r.Scores) == 0 {
			return
		}
		if voice {
			narration = append(narration, CopyView{Scores: r.Scores})
		} else {
			copies = append(copies, CopyView{Scores: r.Scores})
		}
	}
	for _, c := range plan.Clips {
		if c.Audio != nil && c.Audio.Variant != nil {
			add(c.Audio.Variant.AssetID, false)
		}
	}
	for _, au := range plan.Audio {
		switch {
		case au.Variant != nil:
			add(au.Variant.AssetID, false)
		case au.Role == editspec.RoleVoiceover:
			add(au.AssetID, true) // a narration take's clearer version plays as the take
		}
	}
	var out []string
	for _, s := range append(mergeScores(copies), mergeScores(narration)...) {
		head := "Clearer voice"
		switch s.Speaker {
		case "":
		case "Narration":
			head += " for the narration"
		default:
			head += " for " + s.Speaker
		}
		sum := s.Summary
		if sum != "" {
			sum = strings.ToLower(sum[:1]) + sum[1:]
		}
		out = append(out, head+" ("+modeWords(s.Mode)+"): "+sum)
	}
	return out
}

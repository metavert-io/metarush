package recipes

import (
	"context"
	"fmt"
	"math"

	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/sound"
)

// Clearer voices for new footage (D88–D91): a recipe whose cut makes voices
// clearer gets the same settings for the footage a run brings in. On this
// computer (clean, studio) it is free; with ElevenLabs (cloud) it is priced
// the way sound prices it (D208: credits per minute of sound at ElevenLabs'
// pay-as-you-go price) as part of the estimate — an upper bound, the new
// footage's length in the cut.

const stepSound = "sound"

// clearerVoices are the cut's voice settings that make a voice clearer.
func clearerVoices(m *editspec.Mix) []editspec.VoiceSound {
	if m == nil {
		return nil
	}
	var out []editspec.VoiceSound
	for _, v := range m.Voices {
		if v.Enhance != nil && !v.Enhance.Off {
			out = append(out, v)
		}
	}
	return out
}

// newFootageSeconds is about how much of the cut new footage fills: the
// chosen sections whose footage changed — at most their length, and at most
// as long as the footage the run gives them (an episode section that may be
// hours long plays a 40-minute call) — and fixed clips given new footage.
func newFootageSeconds(d *models.RecipeDoc, values map[string]*models.RecipeRunInput, changed map[string]bool) float64 {
	t := 0.0
	for _, s := range d.Sections {
		switch {
		case s.Kind == models.RecipeChosen && changed[s.From]:
			secs := sectionSeconds(s)
			if s.Length != nil && s.Length.Max > 0 {
				secs = s.Length.Max
			}
			if v := values[s.From]; v != nil {
				given := 0.0
				for _, it := range v.Items {
					given += it.Duration
				}
				if given > 0 && given < secs {
					secs = given
				}
			}
			t += secs
		case s.Kind == models.RecipeFixed:
			for _, c := range s.Clips {
				for _, b := range d.Bindings {
					if p, ok := ParseBinding(b.To); ok && p.Kind == models.BindClip && p.Where == c.ID && changed[b.Input] {
						t += clipSeconds(c)
					}
				}
			}
		}
	}
	return t
}

// planSound adds the clearer voices' step and its estimate when new footage needs them.
func planSound(p *planned) (needed bool, detail string) {
	voices := clearerVoices(p.Doc.Cut.Mix)
	secs := newFootageSeconds(p.Doc, p.Values, p.Changed)
	if len(voices) == 0 || secs <= 0 {
		return false, ""
	}
	var local []string
	cloud := costs.Of()
	for _, v := range voices {
		who := v.Name
		if who == "" {
			who = "a recording"
		}
		if v.Enhance.Mode == editspec.EnhanceCloud {
			credits := sound.CloudCredits(secs)
			cloud.Append(costs.Part{Service: providers.ElevenLabsTitle, What: "a clearer voice", Model: providers.IsolationModel,
				Source: fmt.Sprintf("%s's voice in up to %s of new footage", who, seconds(math.Ceil(secs))), Amount: credits, Unit: "credits",
				Units: fmt.Sprintf("up to %s of sound (%s)", seconds(math.Ceil(secs)), costs.Count(credits, "credit", "credits")),
				Rate:  providers.IsolationRate(), USD: providers.IsolationUSD(credits)})
			continue
		}
		local = append(local, who)
	}
	if !cloud.Empty() {
		cloud.Note(providers.PlanCreditsNote)
		p.addEstimate(stepSound, cloud)
	}
	detail = "Clearer voices for the new footage, as the recipe has them."
	if len(local) > 0 {
		detail += " " + upperFirst(joinAnd(local)) + " on this computer (free)."
	}
	return true, detail
}

// soundStep makes the voices clearer in the run's cut as the recipe has them,
// within what the person agreed to.
func (e *engine) soundStep(ctx context.Context, st *models.RecipeStep) (bool, error) {
	rev, err := e.headOf(ctx)
	if err != nil {
		return false, err
	}
	voices := clearerVoices(rev.Spec.Mix)
	if len(voices) == 0 {
		st.State, st.Detail = models.StepSkipped, "The cut makes no voice clearer."
		return true, nil
	}
	if st.Work == nil {
		st.Work = map[string]string{}
	}
	cv, err := e.clearer(ctx, st, rev.ID, voices, "")
	if cv.revision != "" {
		e.run.Revision, e.run.RevisionNumber = cv.revision, cv.number
	}
	if err != nil || !cv.done {
		return false, err
	}
	st.Detail = fmt.Sprintf("%s made clearer in the new footage: %s.", plural(len(voices)-len(cv.skipped), "voice"), spentWords(st.Spent))
	if len(cv.skipped) > 0 {
		st.Detail += " " + upperFirst(joinAnd(cv.skipped)) + " isn't in the new footage, so that setting waits for when they are."
	}
	return true, nil
}

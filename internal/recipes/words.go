package recipes

import (
	"fmt"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// Plain words for recipes and runs (D109): what people read in the CLI, the
// dashboard and Claude's answers. IDs, hashes and flags stay in --details and
// --json.

// kindWords names an input kind.
func kindWords(kind string) string {
	switch kind {
	case models.RecipeWords:
		return "words"
	case models.RecipeChoice:
		return "a choice"
	case models.RecipeFootage:
		return "footage"
	case models.RecipePictures:
		return "pictures"
	case models.RecipeVoice:
		return "a voice"
	}
	return kind
}

// fieldWords names a graphic's field.
var fieldWords = map[string]string{
	"headline": "headline", "title": "title", "subtitle": "subtitle", "subline": "second line", "name": "name",
	"text": "words", "kicker": "label above the title", "label": "label", "value": "number", "context": "line under the number",
	"caption": "caption", "attribution": "who said it", "role": "their role", "cta": "call to action", "handle": "handle",
	"url": "address", "source": "source", "bullets": "points",
}

// graphicWords names a graphic ("the end card").
func graphicWords(o *editspec.Overlay) string {
	if o == nil {
		return "a graphic"
	}
	if o.Motion != nil {
		return "the graphic “" + o.Motion.Name + "”"
	}
	switch {
	case o.Template == editspec.TplTitleCard:
		return "the title card"
	case o.Template == editspec.TplEndCard:
		return "the end card"
	case strings.HasPrefix(o.Template, "lower-third"):
		return "the name title"
	case o.Template == editspec.TplQuoteKinetic:
		return "the quote"
	case o.Template == editspec.TplDataCard:
		return "the number card"
	case o.Template == editspec.TplLogoCorner:
		return "the logo in the corner"
	case o.Template == editspec.TplNameBar:
		return "the name bar"
	case strings.HasPrefix(o.Template, "slide."):
		return "the " + strings.TrimPrefix(o.Template, "slide.") + " slide"
	}
	return "a graphic"
}

// targetWords says where a binding goes, plainly.
func targetWords(d *models.RecipeDoc, b Binding) string {
	switch b.Kind {
	case models.BindClip:
		return "the footage of clip " + quoteName(b.Where)
	case models.BindParam:
		f := fieldWords[b.Field]
		if f == "" {
			f = b.Field
		}
		return graphicWords(overlayByID(d, b.Where)) + "'s " + f
	case models.BindResource:
		return "the picture " + quoteName(b.Where)
	case models.BindWords:
		return "the words " + b.Field + " in clip " + quoteName(b.Where)
	case models.BindPictures:
		return "the pictures " + b.Field + " in clip " + quoteName(b.Where)
	case models.BindLine:
		return "a narration line"
	case models.BindVoice:
		return "the narration's voice"
	case models.BindTitle:
		return "the video's title"
	case models.BindPrompt:
		return "the generated shot " + quoteName(b.Where)
	}
	return "somewhere"
}

func quoteName(s string) string { return "“" + s + "”" }

// creditWords says ElevenLabs credits ("312 ElevenLabs credits").
func creditWords(c float64) string {
	n := int(math.Ceil(c - 1e-9))
	if n == 1 {
		return "1 ElevenLabs credit"
	}
	return fmt.Sprintf("%s ElevenLabs credits", costs.Thousands(float64(n)))
}

// spentWords says what was spent: "$0.42", "$0.42 (120 ElevenLabs
// credits)", "nothing".
func spentWords(c models.RecipeSpent) string {
	if c.USD <= 0 && c.Credits <= 0 {
		return "nothing"
	}
	s := costs.Money(c.USD)
	if c.Credits > 0 {
		s += " (" + creditWords(c.Credits) + ")"
	}
	return s
}

func addSpent(a, b models.RecipeSpent) models.RecipeSpent {
	return models.RecipeSpent{USD: costs.Round(a.USD + b.USD), Credits: roundCredits(a.Credits + b.Credits)}
}

func roundCredits(v float64) float64 { return math.Round(v*10) / 10 }

// stepLabels say what each step does.
var stepLabels = map[string]string{
	stepInputs:   "Bring in the inputs",
	stepPlace:    "Make a place for the video",
	stepSample:   "Make a sample first",
	stepApprove:  "You approve the sample",
	stepCode:     "Make clips from code with the new words and pictures",
	stepShots:    "Make the generated shots",
	stepAssemble: "Put the video together",
	stepVoice:    "Make the AI voice",
	stepClaude:   "Claude chooses the moments",
	stepSound:    "Make the voices clearer",
	stepReview:   "Make a review copy and check it",
	stepPack:     "Make the shorts",
	stepFinish:   "Ask for your review",
}

// stepWho says who does a step.
var stepWho = map[string]string{
	stepInputs: "metarush", stepPlace: "metarush", stepSample: "metarush", stepApprove: "person", stepCode: "metarush", stepShots: "metarush", stepAssemble: "metarush",
	stepVoice: "metarush", stepClaude: "claude", stepSound: "metarush", stepReview: "metarush", stepPack: "metarush", stepFinish: "metarush",
}

// stateWords says where a run stands.
func stateWords(r *models.RecipeRun) string {
	switch r.State {
	case models.RunPlanned:
		return "Planned"
	case models.RunRunning:
		for _, s := range r.Steps {
			if s.State == models.StepRunning {
				return s.Label
			}
		}
		return "Making it"
	case models.RunWaiting:
		if r.Waiting != "" {
			return r.Waiting
		}
		return "Waiting"
	case models.RunDone:
		return "Ready for your review"
	case models.RunStopped:
		return "Stopped"
	case models.RunFailed:
		return "Didn't finish"
	}
	return r.State
}

// runName names a run for people ("run 4 of “App teaser”").
func runName(r *models.RecipeRun) string {
	return fmt.Sprintf("run %d of “%s”", r.Number, r.RecipeTitle)
}

// plural says a count with its noun ("1 picture", "3 pictures").
func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	many := one + "s"
	switch {
	case strings.HasSuffix(one, "y") && !strings.HasSuffix(one, "ey"):
		many = strings.TrimSuffix(one, "y") + "ies"
	case strings.HasSuffix(one, "s") || strings.HasSuffix(one, "sh") || strings.HasSuffix(one, "ch"):
		many = one + "es"
	}
	return fmt.Sprintf("%d %s", n, many)
}

// upperFirst starts a sentence with a capital.
func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if r[0] >= 'a' && r[0] <= 'z' {
		r[0] = r[0] - 'a' + 'A'
	}
	return string(r)
}

// joinAnd joins words with commas and "and".
func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	case 2:
		return xs[0] + " and " + xs[1]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// shorten cuts words to n characters with an ellipsis.
func shorten(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

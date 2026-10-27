package qa

import (
	"encoding/json"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// The house style (D199) in the quality check: what new cuts start with is
// Content Settings', and a version can stray from it (made before the style,
// or changed since). The check measures what it can: captions missing when
// the style says they are on, a name bar that animates when it says never,
// and a name bar taller than it says. Each finding carries the change that
// fixes it; "Use the house style on the latest cut" does them all.

// houseStyle measures the house style where the check can.
func (r *run) houseStyle() {
	const name = "house_style"
	h := r.in.Style
	packed := r.housePack()
	if h.Empty() {
		if packed {
			r.check(name, models.QAHeuristic, packMeasures, packKinds...)
			return
		}
		r.skip(name, models.QAHeuristic, "the project's Content Settings have no house style")
		return
	}
	captionsOn := h.Captions != nil && h.Captions.On != nil && *h.Captions.On
	captionLook := h.Captions != nil && (h.Captions.Look != "" || h.Captions.Size != "" || h.Captions.Position != "")
	nb := h.NameBar
	still := nb != nil && nb.Animate == editspec.NameBarStill
	half := nb != nil && nb.Height == editspec.NameBarHalf
	if !captionsOn && !captionLook && !still && !half && packed {
		r.check(name, models.QAHeuristic, packMeasures, packKinds...)
		return
	}
	if !captionsOn && !captionLook && !still && !half {
		r.skip(name, models.QAHeuristic, "the house style sets nothing the quality check measures (it measures captions on and how they look, a name bar that never animates and a half-height name bar)")
		return
	}
	whose := "The house style"
	if r.in.StyleOf != "" {
		whose = "The house style of " + quote([]string{r.in.StyleOf}) + "'s settings"
	}
	if captionsOn {
		if sc := r.in.Spec.Captions; sc == nil || !sc.Enabled {
			fix := &models.QAFix{Summary: "Turn the captions on, the way the house style has them."}
			if sc == nil || !sc.Locked {
				nc := editspec.Captions{Enabled: true}
				if sc != nil {
					nc = *sc
					nc.Enabled = true
				}
				c := h.Captions
				if nc.Style == "" {
					nc.Style = c.Look
				}
				if nc.Position == "" {
					nc.Position = c.Position
				}
				if nc.Size == "" && c.Size != editspec.CaptionMedium {
					nc.Size = c.Size
				}
				fix.Ops = []editspec.Op{{Op: "setCaptions", Captions: &nc}}
			} else {
				fix.Summary = "The captions are locked: unlock them to turn them on."
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "style_captions", Basis: models.QAHeuristic, From: 0, To: r.total(),
				Message: whose + " has captions on, but this version has none.", Fix: fix})
		}
	}
	// The captions' look, size and place (D245): a cut may choose its own, so
	// a difference is a note with the change that follows the house style.
	if sc := r.in.Spec.Captions; captionLook && sc != nil && sc.Enabled {
		if nc, says := editspec.CaptionStyleDiff(sc, h); nc != nil {
			fix := &models.QAFix{Summary: "Draw the captions the way the house style has them."}
			if sc.Locked {
				fix.Summary = "The captions are locked: unlock them to change how they look."
			} else {
				fix.Ops = []editspec.Op{{Op: "setCaptions", Captions: nc}}
			}
			r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "style_caption_look", Basis: models.QAHeuristic, From: 0, To: r.total(),
				Message: upperFirst(whose) + " draws captions differently: " + strings.Join(says, ", and ") + ". If this cut chose its own, nothing needs doing.", Fix: fix})
		}
	}
	if still || half {
		for _, o := range r.in.Spec.Overlays {
			if o.Template != editspec.TplNameBar {
				continue
			}
			_, height, animate := editspec.NameBarLookOf(o)
			var says []string
			params := map[string]any{}
			_ = json.Unmarshal(o.Params, &params)
			if still && animate != editspec.NameBarStill {
				how := "fades names in"
				if animate == editspec.NameBarSlide {
					how = "slides names in"
				}
				says = append(says, "it "+how+" although the house style says it never animates")
				params["animate"] = editspec.NameBarStill
			}
			if half && height != editspec.NameBarHalf {
				says = append(says, "it is full height, taller than the house style's half height")
				params["height"] = editspec.NameBarHalf
			}
			if len(says) == 0 {
				continue
			}
			fix := &models.QAFix{Summary: "Make the name bar the way the house style has it."}
			if o.Locked {
				fix.Summary = "The name bar is locked: unlock it to change it."
			} else {
				set, _ := json.Marshal(map[string]any{"params": params})
				fix.Ops = []editspec.Op{{Op: "updateOverlay", ID: o.ID, Set: set}}
			}
			from, to := o.At, o.At+o.Duration
			if o.Span != nil {
				from, to = 0, r.total()
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "style_name_bar", Basis: models.QAHeuristic, From: from, To: to, OverlayIDs: []string{o.ID},
				Message: upperFirst(whose) + ": the name bar doesn't follow it — " + strings.Join(says, ", and ") + ".", Fix: fix})
		}
	}
	r.check(name, models.QAHeuristic, "what the house style says that MetaRush can measure: captions on and how they look, a name bar that never animates, a half-height name bar; "+packMeasures,
		append([]string{"style_captions", "style_caption_look", "style_name_bar"}, packKinds...)...)
}

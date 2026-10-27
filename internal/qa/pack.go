package qa

import (
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// The layout pack (D267) in the house style check: a graphic that names a
// role the pack doesn't have (MetaRush's own drew it), or one drawn with
// another graphic than the pack's or with the pack's look changed. A cut may
// choose its own, so each is a note, with the change that makes the graphic
// follow the pack ("Use the layout pack on the latest cut" does them all).

const packMeasures = "graphics that name a part of the layout pack: a part the pack doesn't have, or another graphic or look than the pack's"

var packKinds = []string{"pack_role", "pack_override"}

// housePack notes the graphics that don't follow the layout pack. It reports
// whether there was anything to look at (graphics that name a role).
func (r *run) housePack() bool {
	if len(r.in.PackDrifts) == 0 {
		return r.namesRoles()
	}
	whose := "MetaRush's own layout pack"
	if r.in.PackOf != "" {
		whose = "The layout pack of " + quote([]string{r.in.PackOf}) + "'s settings"
	}
	for _, d := range r.in.PackDrifts {
		o, ok := r.packOverlay(d.Overlay)
		if !ok {
			continue
		}
		from, to := o.At, o.At+o.Duration
		if o.Span != nil {
			from, to = 0, r.total()
		}
		role := editspec.RoleWords(d.Role)
		switch {
		case d.Other || len(d.Fields) > 0:
			var says string
			if d.Other {
				says = "the " + role + " here is drawn with another graphic than the pack's"
			} else {
				says = "the " + role + " here changes how the pack draws it (" + strings.Join(d.Fields, ", ") + ")"
			}
			fix := &models.QAFix{Summary: "Draw it the way the layout pack has it."}
			if o.Locked {
				fix.Summary = "The graphic is locked: unlock it to change it."
			} else {
				fix.Ops = d.Ops
			}
			r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "pack_override", Basis: models.QAHeuristic, From: from, To: to, OverlayIDs: []string{o.ID},
				Message: upperFirst(whose) + ": " + says + ". If this cut chose its own, nothing needs doing.", Fix: fix})
		case d.Outside:
			r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "pack_role", Basis: models.QAHeuristic, From: from, To: to, OverlayIDs: []string{o.ID},
				Message: upperFirst(whose) + " has no " + role + ", so the " + role + " here is drawn with MetaRush's own; add one to the pack if this client should have its own.",
				Fix:     &models.QAFix{Summary: "Add the " + role + " to the layout pack (Settings → Layout pack)."}})
		}
	}
	return true
}

// namesRoles reports a cut with a graphic that names a layout pack role.
func (r *run) namesRoles() bool {
	if r.in.Spec == nil {
		return false
	}
	for _, o := range r.in.Spec.Overlays {
		if o.Role != "" {
			return true
		}
	}
	return false
}

func (r *run) packOverlay(id string) (editspec.Overlay, bool) {
	if r.in.Spec == nil {
		return editspec.Overlay{}, false
	}
	for _, o := range r.in.Spec.Overlays {
		if o.ID == id {
			return o, true
		}
	}
	return editspec.Overlay{}, false
}

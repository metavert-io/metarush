package qa

import (
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/models"
)

// backgrounds looks at every clip whose background the cut replaces (D510):
// its cut-out must be there, made and long enough (else the people can't be
// shown: an error), and made from the media the cut plays for the clip (else
// the people come from another picture than the one under them: a warning,
// fixed by making it again). One note says where the background is replaced
// and what found the people, for the person's information: platforms ask
// people to say so when a scene looks real.
func (r *run) backgrounds() {
	s := r.in.Spec
	if s == nil {
		return
	}
	any := false
	type span struct{ from, to float64 }
	var spans []span
	var clips, models_ []string
	for i, c := range s.Clips {
		b := c.Background
		if b == nil {
			continue
		}
		any = true
		from, to := r.clipSpan(c.ID)
		spans = append(spans, span{from, to})
		clips = append(clips, c.ID)
		who := fmt.Sprintf("clip %d", i+1)
		if c.Label != "" {
			who = "“" + c.Label + "”"
		}
		fix := fmt.Sprintf("metarush cut background <project> --clip %s --with %s", c.ID, b.With)
		if r.in.Mattes == nil {
			continue
		}
		info, ok := r.in.Mattes(b.Matte)
		f := &models.QAFinding{Kind: "background_matte", Basis: models.QAHeuristic, From: from, To: to, ClipIDs: []string{c.ID}}
		switch {
		case !ok:
			f.Severity = models.QAError
			f.Message = fmt.Sprintf("The people in %s come from a cut-out MetaRush doesn't have any more, so the background can't be replaced there: make the cut-out again, or take the background out.", who)
		case !info.Ready:
			f.Severity = models.QAError
			f.Message = fmt.Sprintf("The cut-out of %s isn't made yet, so the background can't be replaced there: wait for it, or take the background out.", who)
		case c.In < info.From-0.02 || c.Out > info.To+0.02:
			f.Severity = models.QAError
			f.Message = fmt.Sprintf("The cut-out of %s covers %s to %s of its footage, and the clip plays %s to %s: make a cut-out of that part.", who,
				clock(info.From), clock(info.To), clock(c.In), clock(c.Out))
		case info.Fixed != s.FixedSHA(c.AssetID):
			f.Severity, f.Kind = models.QAWarning, "background_matte_stale"
			f.Message = fmt.Sprintf("The people in %s come from %s, but the cut plays %s under them: make the cut-out again so they match.", who,
				matteMedia(info.Fixed), matteMedia(s.FixedSHA(c.AssetID)))
		default:
			if info.Model != "" && !containsStr(models_, info.Model) {
				models_ = append(models_, info.Model)
			}
			continue
		}
		f.Command = fix
		r.add(f)
	}
	if !any {
		return
	}
	var when []string
	for i, sp := range spans {
		if i == 4 {
			when = append(when, "…")
			break
		}
		when = append(when, clock(sp.from)+" to "+clock(sp.to))
	}
	msg := fmt.Sprintf("The background behind the people is replaced in %s (%s)", count(len(spans), "clip"), strings.Join(when, ", "))
	if len(models_) > 0 {
		msg += ", the people found with " + strings.Join(models_, " and ")
	}
	msg += ". Say so when you post it if the place looks real: platforms ask for it when a scene didn't happen where it seems to."
	r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "background_replaced", Basis: models.QAHeuristic, From: spans[0].from, To: spans[len(spans)-1].to, ClipIDs: clips, Message: msg})
	r.check("backgrounds", models.QAHeuristic, "every replaced background's cut-out is there, made, covers its clip and follows the media the cut plays", "background_matte", "background_matte_stale")
}

// clipSpan is where a clip of the spec plays in the render, in seconds.
func (r *run) clipSpan(id string) (float64, float64) {
	for _, pc := range r.plan.Clips {
		if pc.ID == id {
			return float64(pc.From) / r.fps, float64(pc.From+pc.DurationInFrames) / r.fps
		}
	}
	return 0, 0
}

func matteMedia(fixed string) string {
	if fixed == "" {
		return "the clip as recorded"
	}
	return "a fixed copy of the clip"
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

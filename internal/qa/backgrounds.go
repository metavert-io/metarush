package qa

import (
	"fmt"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// backgrounds looks at every clip whose background the cut replaces (D510),
// the cuts it plays included:
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
	// The cuts this one plays (nested, at any depth): their cut-outs must be
	// there and made too — a version is fixed, but a cut-out can be deleted
	// since — and their replaced backgrounds are part of this video's note.
	for _, nb := range r.nestedBackgrounds() {
		any = true
		spans = append(spans, span{nb.from, nb.to})
		if !containsStr(clips, nb.outer) {
			clips = append(clips, nb.outer)
		}
		if r.in.Mattes == nil {
			continue
		}
		who := "a clip of “" + nb.title + "”, the cut played here"
		info, ok := r.in.Mattes(nb.cutOut)
		f := &models.QAFinding{Kind: "background_matte", Severity: models.QAError, Basis: models.QAHeuristic, From: nb.from, To: nb.to, ClipIDs: []string{nb.outer}}
		switch {
		case !ok:
			f.Message = fmt.Sprintf("The people in %s come from a cut-out MetaRush doesn't have any more, so the background can't be replaced there: make it again in that cut, or play a version of it without the background.", who)
		case !info.Ready:
			f.Message = fmt.Sprintf("The cut-out of %s isn't made yet, so the background can't be replaced there: wait for it, or play a version of that cut without the background.", who)
		default:
			if info.Model != "" && !containsStr(models_, info.Model) {
				models_ = append(models_, info.Model)
			}
			continue
		}
		r.add(f)
	}
	if !any {
		return
	}
	sort.SliceStable(spans, func(i, j int) bool { return spans[i].from < spans[j].from })
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

// nestedBackground is a clip of a cut this one plays whose background is
// replaced: its cut-out, where it plays here (seconds), the clip of this
// cut that plays that cut, and that cut's name.
type nestedBackground struct {
	cutOut       string
	from, to     float64
	outer, title string
}

// nestedBackgrounds walks the cuts this one plays (nested, at any depth) for
// clips whose background is replaced, as shownClips walks their clips.
func (r *run) nestedBackgrounds() []nestedBackground {
	var out []nestedBackground
	var walk func(p *editspec.RenderPlan, offset, lo, hi, depth int, outer, title string)
	walk = func(p *editspec.RenderPlan, offset, lo, hi, depth int, outer, title string) {
		if p == nil || depth > 6 {
			return
		}
		for _, c := range p.Clips {
			from, to := max(offset+c.From, lo), min(offset+c.From+c.DurationInFrames, hi)
			if to <= from {
				continue
			}
			if editspec.IsNested(c.AssetID) {
				np := p.Nested[c.AssetID]
				if np == nil {
					np = r.plan.Nested[c.AssetID]
				}
				if np == nil {
					continue
				}
				o := outer
				if depth == 0 {
					o = c.ID
				}
				name := np.Title
				if name == "" {
					name = np.ProjectName
				}
				if name == "" {
					name = "another cut"
				}
				walk(np.Plan, offset+c.From-c.TrimBefore, from, to, depth+1, o, name)
				continue
			}
			if b := c.Background; depth > 0 && b != nil && b.CutOut != "" {
				out = append(out, nestedBackground{cutOut: b.CutOut, from: float64(from) / r.fps, to: float64(to) / r.fps, outer: outer, title: title})
			}
		}
	}
	walk(r.plan, 0, 0, r.plan.DurationInFrames, 0, "", "")
	return out
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

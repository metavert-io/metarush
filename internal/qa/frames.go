package qa

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// fspan is a [from, to) interval in timeline frames with a reason.
type fspan struct {
	from, to int
	why      string
}

// expectedDark returns timeline spans where the plan itself makes the picture
// dark: fades from the background, dip-to-black transitions, fade-outs and
// full-frame cards. Detected black inside them is intentional.
func expectedDark(p *editspec.RenderPlan) []fspan {
	var out []fspan
	for i, c := range p.Clips {
		d := c.TransitionIn.DurationInFrames
		switch c.TransitionIn.Type {
		case editspec.TransitionFade:
			if d > 0 {
				out = append(out, fspan{c.From, c.From + d, "fade-in of " + c.ID})
			}
		case editspec.TransitionDip:
			in := d - d/2
			from := c.From
			if i > 0 {
				from -= p.Clips[i-1].FadeOutFrames
			}
			if c.From+in > from {
				out = append(out, fspan{from, c.From + in, "dip-to-black into " + c.ID})
			}
		}
		if c.FadeOutFrames > 0 && i+1 < len(p.Clips) && p.Clips[i+1].TransitionIn.Type == editspec.TransitionDipWhite {
			continue // fades to white, not black
		}
		if c.FadeOutFrames > 0 {
			end := c.From + c.DurationInFrames
			out = append(out, fspan{end - c.FadeOutFrames, end, "fade-out of " + c.ID})
		}
	}
	for _, o := range p.Overlays {
		if t, ok := editspec.TemplateByID(o.Template); ok && t.Region == "full" {
			out = append(out, fspan{o.From, o.From + o.DurationInFrames, "full-frame card " + o.ID + " (" + o.Template + ")"})
		} else if o.Motion != nil && o.Motion.Region == "full" {
			out = append(out, fspan{o.From, o.From + o.DurationInFrames, "full-frame motion graphic " + o.ID + " (" + o.Motion.Name + ")"})
		}
	}
	return out
}

// expectedStatic returns spans where a frozen picture is planned: full-frame
// cards, every planned dark span (black is static too) and dips through white.
func expectedStatic(p *editspec.RenderPlan) []fspan {
	out := expectedDark(p)
	for i, c := range p.Clips {
		if c.TransitionIn.Type != editspec.TransitionDipWhite {
			continue
		}
		d := c.TransitionIn.DurationInFrames
		from := c.From
		if i > 0 {
			from -= p.Clips[i-1].FadeOutFrames
		}
		out = append(out, fspan{from, c.From + d - d/2, "dip-to-white into " + c.ID})
	}
	return out
}

// sourceSpans maps source-time evidence spans (black or freezes) of each clip's
// asset onto the timeline.
func (r *run) sourceSpans(kind string) []fspan {
	var out []fspan
	for _, c := range r.plan.Clips {
		ev, ok := r.in.Source[c.AssetID]
		if !ok || !ev.Available {
			continue
		}
		list := ev.Black
		if kind == "freeze" {
			list = ev.Freezes
		}
		for _, s := range list {
			from, to := math.Max(s.From, c.SourceIn), math.Min(s.To, c.SourceOut)
			if to <= from {
				continue
			}
			f0 := c.From + int(math.Round((from-c.SourceIn)/c.PlaybackRate*r.fps))
			f1 := c.From + int(math.Round((to-c.SourceIn)/c.PlaybackRate*r.fps))
			if f1 > f0 {
				out = append(out, fspan{f0, f1, fmt.Sprintf("%s in source %s at %.2f–%.2fs (clip %s)", kind, r.assetName(c.AssetID), from, to, c.ID)})
			}
		}
	}
	return out
}

func (r *run) assetName(id string) string {
	if n := r.in.AssetNames[id]; n != "" {
		return n
	}
	return id
}

// subtract returns the parts of [from,to) not covered by spans (widened by tol).
func subtract(from, to int, spans []fspan, tol int) [][2]int {
	type iv struct{ a, b int }
	var cover []iv
	for _, s := range spans {
		a, b := s.from-tol, s.to+tol
		if b > from && a < to {
			cover = append(cover, iv{a, b})
		}
	}
	sort.Slice(cover, func(i, j int) bool { return cover[i].a < cover[j].a })
	var out [][2]int
	cur := from
	for _, c := range cover {
		if c.a > cur {
			out = append(out, [2]int{cur, min(c.a, to)})
		}
		if c.b > cur {
			cur = c.b
		}
		if cur >= to {
			break
		}
	}
	if cur < to {
		out = append(out, [2]int{cur, to})
	}
	return out
}

// covering lists the reasons of spans overlapping [from,to).
func covering(from, to int, spans []fspan, tol int) []string {
	var out []string
	for _, s := range spans {
		if s.from-tol < to && s.to+tol > from && !contains(out, s.why) {
			out = append(out, s.why)
		}
	}
	return out
}

func coveredFraction(from, to int, spans []fspan, tol int) float64 {
	if to <= from {
		return 0
	}
	rest := 0
	for _, g := range subtract(from, to, spans, tol) {
		rest += g[1] - g[0]
	}
	return 1 - float64(rest)/float64(to-from)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

// clipsAt returns the plan clips visible in [from,to) (in timeline order).
func (r *run) clipsAt(from, to int) []editspec.PlanClip {
	var out []editspec.PlanClip
	for _, c := range r.plan.Clips {
		if c.From < to && c.From+c.DurationInFrames > from {
			out = append(out, c)
		}
	}
	return out
}

func (r *run) srcTime(c editspec.PlanClip, f int) float64 {
	return round3(c.SourceIn + float64(f-c.From)/r.fps*c.PlaybackRate)
}

func (r *run) specClip(id string) *editspec.Clip {
	if r.in.Spec == nil {
		return nil
	}
	for i := range r.in.Spec.Clips {
		if r.in.Spec.Clips[i].ID == id {
			return &r.in.Spec.Clips[i]
		}
	}
	return nil
}

func ids(cs []editspec.PlanClip) []string {
	out := make([]string, 0, len(cs))
	for _, c := range cs {
		out = append(out, c.ID)
	}
	return out
}

func setOp(op, id string, set map[string]any) editspec.Op {
	b, _ := json.Marshal(set)
	return editspec.Op{Op: op, ID: id, Set: b}
}

// trimFix suggests ops that cut [f0,f1) out of the clip it falls in.
func (r *run) trimFix(c editspec.PlanClip, f0, f1 int, what string) *models.QAFix {
	s0, s1 := r.srcTime(c, max(f0, c.From)), r.srcTime(c, min(f1, c.From+c.DurationInFrames))
	sc := r.specClip(c.ID)
	clip := clipName(c.ID)
	base := fmt.Sprintf("The %s frames are in the footage itself (%s, around %s into it).", what, r.footage(c.AssetID), clock(s0))
	if sc != nil && sc.Locked {
		return &models.QAFix{Summary: base + " " + upperFirst(clip) + " is locked: unlock it first to change it."}
	}
	head := f0-c.From <= ExplainTolFrames
	tail := c.From+c.DurationInFrames-f1 <= ExplainTolFrames
	switch {
	case head && tail:
		return &models.QAFix{Summary: base + " The whole clip is affected: use another part of the footage, or remove " + clip + ".",
			Ops: []editspec.Op{{Op: "removeClip", ID: c.ID}}}
	case head:
		return &models.QAFix{Summary: base + " Start " + clip + " a little later, after them.", Ops: []editspec.Op{setOp("updateClip", c.ID, map[string]any{"in": s1})}}
	case tail:
		return &models.QAFix{Summary: base + " End " + clip + " a little earlier, before them.", Ops: []editspec.Op{setOp("updateClip", c.ID, map[string]any{"out": s0})}}
	}
	fix := &models.QAFix{Summary: base + " Split " + clip + " around them, so they're left out."}
	if sc != nil {
		next := editspec.Clone(&editspec.Spec{Clips: []editspec.Clip{*sc}}).Clips[0]
		next.ID = uniqueID(r.in.Spec, c.ID+"-b")
		next.In, next.Transition, next.Rationale, next.Evidence, next.Label = s1, nil, "", nil, ""
		fix.Ops = []editspec.Op{setOp("updateClip", c.ID, map[string]any{"out": s0}), {Op: "insertClip", After: c.ID, Clip: &next}}
	}
	return fix
}

func uniqueID(s *editspec.Spec, want string) string {
	used := map[string]bool{}
	if s != nil {
		for _, c := range s.Clips {
			used[c.ID] = true
		}
		for _, a := range s.Audio {
			used[a.ID] = true
		}
		for _, o := range s.Overlays {
			used[o.ID] = true
		}
	}
	id := want
	for i := 2; used[id]; i++ {
		id = fmt.Sprintf("%s%d", want, i)
	}
	return id
}

func (r *run) noEvidence(cs []editspec.PlanClip) []string {
	var out []string
	for _, c := range cs {
		if ev, ok := r.in.Source[c.AssetID]; !ok || !ev.Available {
			if !contains(out, c.AssetID) {
				out = append(out, c.AssetID)
			}
		}
	}
	return out
}

func (r *run) rerenderHint(cs []editspec.PlanClip, f0 int) string {
	parts := []string{"Nothing in the cut or in the footage explains it, so the render most likely went wrong: render again and check again."}
	if len(cs) > 0 {
		c := cs[len(cs)-1]
		parts = append(parts, fmt.Sprintf("If it happens again, look at %s around %s into it.", r.footage(c.AssetID), clock(r.srcTime(c, f0))))
	}
	if miss := r.noEvidence(cs); len(miss) > 0 {
		names := make([]string, len(miss))
		for i, id := range miss {
			names[i] = r.footage(id)
		}
		parts = append(parts, "MetaRush hasn't looked through "+joinAnd(names)+" for dark or still parts yet: analyzing the footage shows whether it is dark or still there itself.")
	}
	return strings.Join(parts, " ")
}

// blackFrames classifies every detected black span.
func (r *run) blackFrames() {
	if !r.mi.HasVideo {
		r.skip("black_frames", models.QAObserved, "the render has no picture")
		return
	}
	planned := expectedDark(r.plan)
	source := r.sourceSpans("black")
	for _, b := range r.det.Black {
		f0, f1 := int(math.Round(b.From*r.fps)), int(math.Round(b.To*r.fps))
		if f1 <= f0 {
			f1 = f0 + 1
		}
		rest := subtract(f0, f1, planned, ExplainTolFrames)
		why := covering(f0, f1, planned, ExplainTolFrames)
		if len(rest) == 0 {
			r.rep.Measurements.Black = append(r.rep.Measurements.Black, models.QASpan{From: round3(b.From), To: round3(b.To), Explained: strings.Join(why, "; ")})
			continue
		}
		r.rep.Measurements.Black = append(r.rep.Measurements.Black, models.QASpan{From: round3(b.From), To: round3(b.To), Explained: strings.Join(why, "; ")})
		for _, g := range rest {
			// A one-frame sliver touching a planned span is the tail of that fade.
			if g[1]-g[0] <= 1 && len(why) > 0 {
				continue
			}
			from, to := float64(g[0])/r.fps, float64(g[1])/r.fps
			cs := r.clipsAt(g[0], g[1])
			n := g[1] - g[0]
			if coveredFraction(g[0], g[1], source, 1) >= 0.8 {
				msg := fmt.Sprintf("Around %s there are %s, and they are in the footage itself.", clock(from), count(n, "black frame"))
				if n == 1 {
					msg = fmt.Sprintf("Around %s there is a black frame, and it is in the footage itself.", clock(from))
				}
				if len(cs) > 0 {
					c := cs[len(cs)-1]
					msg = strings.TrimSuffix(msg, ".") + fmt.Sprintf(" (%s, in %s).", r.footage(c.AssetID), clipName(c.ID))
				}
				f := r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "source_black", From: from, To: to, ClipIDs: ids(cs), Message: msg})
				if len(cs) > 0 {
					f.Fix = r.trimFix(cs[len(cs)-1], g[0], g[1], "black")
				}
				continue
			}
			where := ""
			if near := r.nearCut(g[0], g[1]); near != "" {
				where = " " + near
			} else if len(cs) > 0 {
				where = " in " + clipName(cs[len(cs)-1].ID)
			}
			sev, kind, msg := models.QAError, "black_frames", fmt.Sprintf("Around %s the picture goes black for %s (%s)%s, which the cut doesn't plan.", clock(from), seconds(to-from), frames(n), where)
			if n < FlashFrames {
				sev, kind, msg = models.QAWarning, "flash_frame", fmt.Sprintf("Around %s there's a %d-frame flash of black%s.", clock(from), n, where)
			}
			r.add(&models.QAFinding{Severity: sev, Kind: kind, From: from, To: to, ClipIDs: ids(cs), Message: msg, Fix: &models.QAFix{Summary: r.rerenderHint(cs, g[0])}})
		}
	}
	r.check("black_frames", models.QAObserved, fmt.Sprintf("blackdetect pix_th=%.2f pic_th=%.2f; planned fades/dips/full-frame cards ±%d frames are intentional; <%d unexplained frames = flash warning, more = error", BlackPixelTh, BlackPictureTh, ExplainTolFrames, FlashFrames),
		"black_frames", "flash_frame", "source_black")
}

// nearCut names the cut within two frames of [f0,f1), if any: "at the cut
// from the “hook” clip to the “garden” clip".
func (r *run) nearCut(f0, f1 int) string {
	for i, c := range r.plan.Clips {
		if i == 0 {
			continue
		}
		if c.From >= f0-ExplainTolFrames && c.From <= f1+ExplainTolFrames {
			return "at the cut from " + clipName(r.plan.Clips[i-1].ID) + " to " + clipName(c.ID)
		}
	}
	return ""
}

// frozenFrames classifies frozen spans (repeated pictures).
func (r *run) frozenFrames() {
	if !r.mi.HasVideo {
		r.skip("frozen_frames", models.QAObserved, "the render has no picture")
		return
	}
	planned := append(expectedStatic(r.plan), r.convFreezeSpans()...)
	var blacks []fspan
	for _, b := range r.det.Black {
		blacks = append(blacks, fspan{int(math.Round(b.From * r.fps)), int(math.Round(b.To * r.fps)), "black"})
	}
	source := r.sourceSpans("freeze")
	minFrames := int(math.Round(FreezeMinSeconds * r.fps))
	for _, z := range r.det.Freezes {
		f0, f1 := int(math.Round(z.From*r.fps)), int(math.Round(z.To*r.fps))
		why := covering(f0, f1, planned, ExplainTolFrames)
		rest := subtract(f0, f1, append(append([]fspan{}, planned...), blacks...), ExplainTolFrames)
		r.rep.Measurements.Frozen = append(r.rep.Measurements.Frozen, models.QASpan{From: round3(z.From), To: round3(z.To), Explained: strings.Join(why, "; ")})
		for _, g := range rest {
			if g[1]-g[0] < minFrames {
				continue
			}
			from, to := float64(g[0])/r.fps, float64(g[1])/r.fps
			cs := r.clipsAt(g[0], g[1])
			if coveredFraction(g[0], g[1], source, int(r.fps/2)) >= 0.8 {
				msg := fmt.Sprintf("Around %s the picture stays still for %s, as it does in the footage itself.", clock(from), seconds(to-from))
				if len(cs) > 0 {
					msg = strings.TrimSuffix(msg, ".") + " (" + r.footage(cs[len(cs)-1].AssetID) + ")."
				}
				r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "static_source", From: from, To: to, ClipIDs: ids(cs), Message: msg})
				continue
			}
			moving := len(cs) > 0 && len(r.noEvidence(cs)) == 0
			sev := models.QAWarning
			msg := fmt.Sprintf("Around %s the picture freezes for %s", clock(from), seconds(to-from))
			if len(cs) > 0 {
				msg += " in " + clipNames(ids(cs))
			}
			switch {
			case moving && to-from >= FreezeErrSeconds:
				sev = models.QAError
				msg += ", although the footage moves there: the render stalled or repeated frames."
			case !moving:
				msg += ". A still shot can be on purpose, but MetaRush hasn't looked through that footage to compare."
			default:
				msg += "."
			}
			r.add(&models.QAFinding{Severity: sev, Kind: "frozen_frames", From: from, To: to, ClipIDs: ids(cs), Message: msg, Fix: &models.QAFix{Summary: r.rerenderHint(cs, g[0])}})
		}
	}
	r.check("frozen_frames", models.QAObserved, fmt.Sprintf("freezedetect n=%ddB d=%.1fs; planned cards/dips and static source spans excluded; ≥%.1fs where the source moves = error", FreezeNoiseDB, FreezeMinSeconds, FreezeErrSeconds),
		"frozen_frames", "static_source")
}

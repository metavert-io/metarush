package qa

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// Approximate frame geometry, mirroring video/src/core/layout.ts and the
// templates' metrics closely enough for collision heuristics (not pixel-exact).

type box struct{ L, T, R, B float64 }

func (a box) overlaps(b box) bool { return a.L < b.R && b.L < a.R && a.T < b.B && b.T < a.B }

type geom struct {
	W, H, u       float64
	orient        string
	safe, content box
}

func pick(o string, vertical, landscape, square float64) float64 {
	switch o {
	case "vertical":
		return vertical
	case "landscape":
		return landscape
	}
	return square
}

func newGeom(p *editspec.RenderPlan) geom {
	w, h := float64(p.Width), float64(p.Height)
	g := geom{W: w, H: h, u: math.Min(w, h) / 1080}
	switch r := w / h; {
	case r > 1.15:
		g.orient = "landscape"
	case r < 0.87:
		g.orient = "vertical"
	default:
		g.orient = "square"
	}
	clampM := func(v int, total float64) float64 { return math.Max(0, math.Min(float64(v), total*0.3)) }
	g.safe = box{clampM(p.SafeArea.Left, w), clampM(p.SafeArea.Top, h), w - clampM(p.SafeArea.Right, w), h - clampM(p.SafeArea.Bottom, h)}
	gut := math.Round(16 * g.u)
	g.content = box{g.safe.L + gut, g.safe.T + gut, g.safe.R - gut, g.safe.B - gut}
	return g
}

func (g geom) cw() float64 { return g.content.R - g.content.L }
func (g geom) ch() float64 { return g.content.B - g.content.T }

func (g geom) centered(w, h float64) box {
	l := g.content.L + (g.cw()-w)/2
	t := g.content.T + math.Max(0, (g.ch()-h)/2)
	return box{l, t, l + w, t + h}
}

// captionZone is the band two full-size caption lines can occupy (size:
// small or large captions, D199; "" medium), in the look's line height and
// scale (D245, the numbers the renderer reads).
func (g geom) captionZone(position, style, size string) box {
	look := editspec.CaptionLook(style)
	blockH := 2 * pick(g.orient, 64, 52, 58) * g.u * look.LineHeight * look.Scale * editspec.CaptionSizeScale(size)
	var top float64
	switch position {
	case "top":
		top = g.content.T
	case "middle":
		top = g.content.T + g.ch()/2 - blockH/2
	case "bottom":
		top = g.content.B - blockH
	default:
		top = g.content.T + g.ch()*0.8 - blockH/2
	}
	top = math.Max(g.content.T, math.Min(g.content.B-blockH, top))
	w := pick(g.orient, g.cw(), math.Min(g.cw(), 1240*g.u), g.cw()*0.94)
	l := g.content.L + (g.cw()-w)/2
	return box{l, top, l + w, top + blockH}
}

// motionLowerBand is the share of the content box a lower-region project
// motion graphic may use (video/src/templates/reserve.ts MOTION_LOWER_BAND).
const motionLowerBand = 0.26

// boxOf approximates where an overlay draws: a template by its metrics, a
// project motion graphic by the region its check recorded (D68).
func (g geom) boxOf(o editspec.PlanOverlay) box {
	if o.Motion == nil && o.Template == editspec.TplLogoCorner {
		return g.logoCornerBox(o.Params)
	}
	if o.Motion == nil && o.Template == editspec.TplNameBar {
		return g.nameBarBox(o.Params)
	}
	if o.Motion == nil {
		return g.overlayBox(o.Template)
	}
	switch o.Motion.Region {
	case "lower":
		return box{g.content.L, g.content.B - g.ch()*motionLowerBand, g.content.R, g.content.B}
	case "center":
		return g.centered(g.cw(), g.ch()*0.4)
	}
	return box{0, 0, g.W, g.H}
}

// logoCornerBox is where logo.corner draws (video/src/templates/LogoCorner.tsx
// logoCornerBox): a share of the frame's width, at most 60% of that tall, in a
// corner of the content box.
func (g geom) logoCornerBox(params json.RawMessage) box {
	var p struct {
		Corner string `json:"corner"`
		Size   string `json:"size"`
	}
	_ = json.Unmarshal(params, &p)
	share := map[string]float64{"medium": 0.16, "large": 0.24}[p.Size]
	if share == 0 {
		share = 0.1
	}
	w := math.Round(g.W * share)
	h := math.Round(w * 0.6)
	l, t := g.content.R-w, g.content.T
	if strings.HasSuffix(p.Corner, "left") {
		l = g.content.L
	}
	if strings.HasPrefix(p.Corner, "bottom") {
		t = g.content.B - h
	}
	return box{l, t, l + w, t + h}
}

// nameBarBox is where name.bar draws (video/src/templates/NameBar.tsx
// nameBarMetrics): a band at the bottom (or top) of the picture tall enough to
// cover its area, across the picture or, as a tab, as wide as the area; half
// height (D199) about half as tall, still over the whole area.
func (g geom) nameBarBox(params json.RawMessage) box {
	var p struct {
		Cover  *editspec.CoverBox `json:"cover"`
		Style  string             `json:"style"`
		Hide   bool               `json:"hide"`
		Height string             `json:"height"`
	}
	_ = json.Unmarshal(params, &p)
	c := editspec.DefaultNameCover
	if p.Cover != nil {
		c = *p.Cover
	}
	if p.Hide {
		return box{}
	}
	minBand := math.Round(64 * g.u)
	l, r := 0.0, g.W
	if p.Style == "tab" {
		if c.X+c.W/2 > 0.5 {
			l = math.Max(0, math.Floor(c.X*g.W)-0.006*g.W)
		} else {
			r = math.Min(g.W, math.Ceil((c.X+c.W)*g.W)+0.006*g.W)
		}
	}
	padY := math.Round(0.006 * g.H)
	coverTop := math.Max(0, math.Floor(c.Y*g.H)-padY)
	coverBottom := math.Min(g.H, math.Ceil((c.Y+c.H)*g.H)+padY)
	atBottom := c.Y+c.H/2 >= 0.5
	top, height := 0.0, math.Max(coverBottom, minBand)
	if atBottom {
		top = math.Min(coverTop, g.H-minBand)
		height = g.H - top
	}
	if p.Height == editspec.NameBarHalf {
		half := math.Max(coverBottom-coverTop, math.Round(height/2))
		mid := (coverTop + coverBottom) / 2
		switch {
		case atBottom && coverBottom >= g.H-padY:
			top = g.H - half
		case !atBottom && coverTop <= padY:
			top = 0
		default:
			top = math.Round(math.Max(0, math.Min(g.H-half, mid-half/2)))
		}
		top = math.Min(top, coverTop)
		height = math.Max(half, coverBottom-top)
	}
	return box{l, top, r, top + height}
}

// layoutOverlays lists every graphic on screen: this cut's, then those of the
// cuts nested in it (D84) at their time on this timeline, cut to the nested
// clip and named "<clip>/<graphic>"; nested reports which are those.
func layoutOverlays(p *editspec.RenderPlan) ([]editspec.PlanOverlay, map[string]bool) {
	out := append([]editspec.PlanOverlay(nil), p.Overlays...)
	nested := map[string]bool{}
	for _, c := range p.Clips {
		np, ok := p.Nested[c.AssetID]
		if !ok || np == nil || np.Plan == nil {
			continue
		}
		for _, o := range np.Plan.Overlays {
			from := c.From + o.From - c.TrimBefore
			end := from + o.DurationInFrames
			from, end = max(from, c.From), min(end, c.From+c.DurationInFrames)
			if end <= from {
				continue
			}
			o.ID = c.ID + "/" + o.ID
			o.From, o.DurationInFrames = from, end-from
			nested[o.ID] = true
			out = append(out, o)
		}
	}
	return out, nested
}

// regionOfOverlay is an overlay's screen region (template or motion graphic).
func regionOfOverlay(o editspec.PlanOverlay) string {
	if o.Motion != nil {
		if o.Motion.Region != "" {
			return o.Motion.Region
		}
		return "full"
	}
	return regionOf(o.Template)
}

// overlayBox approximates where a template draws.
func (g geom) overlayBox(template string) box {
	u := g.u
	switch template {
	case editspec.TplLowerThirdClean, editspec.TplLowerThirdCinematic:
		name := pick(g.orient, 58, 48, 52) * u
		h := name*1.12 + 10*u + name*0.6*1.3
		if template == editspec.TplLowerThirdCinematic {
			h *= 1.15
		}
		w := pick(g.orient, g.cw(), math.Min(g.cw(), 1100*u), g.cw())
		return box{g.content.L, g.content.B - h, g.content.L + w, g.content.B}
	case editspec.TplQuoteKinetic:
		return g.centered(pick(g.orient, g.cw(), math.Min(g.cw(), 1360*u), g.cw()*0.94), g.ch()*0.45)
	case editspec.TplDataCard:
		return g.centered(pick(g.orient, g.cw(), math.Min(g.cw(), 900*u), g.cw()*0.88), g.ch()*0.3)
	case editspec.TplTitleCard:
		return g.centered(pick(g.orient, g.cw(), math.Min(g.cw(), 1500*u), g.cw()), g.ch()*0.4)
	}
	return box{0, 0, g.W, g.H} // full frame (end card) or unknown
}

func regionOf(template string) string {
	if t, ok := editspec.TemplateByID(template); ok {
		return t.Region
	}
	return "full"
}

// layout runs the plan-geometry heuristics.
func (r *run) layout() {
	p := r.plan
	g := newGeom(p)
	caps := p.Captions
	var lines []editspec.PlanCaptionLine
	if caps != nil {
		lines = caps.Lines
	}
	overlays, nested := layoutOverlays(p)
	nestedFix := func(o editspec.PlanOverlay) *models.QAFix {
		clip, _, _ := strings.Cut(o.ID, "/")
		return &models.QAFix{Summary: fmt.Sprintf("%s belongs to the cut played inside this one (%s): change it in that project's cut, then update this one; or move this cut's captions or graphics.", upperFirst(graphicName(o)), clipName(clip))}
	}
	for _, o := range overlays {
		oEnd := o.From + o.DurationInFrames
		var hit []editspec.PlanCaptionLine
		for _, l := range lines {
			if l.From < oEnd && l.To > o.From {
				hit = append(hit, l)
			}
		}
		if len(hit) == 0 {
			continue
		}
		from, to := max(hit[0].From, o.From), min(hit[len(hit)-1].To, oEnd)
		fs, ts := float64(from)/r.fps, float64(to)/r.fps
		region := regionOfOverlay(o)
		ob := g.boxOf(o)
		cz := g.captionZone(caps.Position, caps.Style, caps.Size)
		switch {
		case region == "full":
			fix := nestedFix(o)
			if !nested[o.ID] {
				fix = r.moveOverlayAfterSpeech(o, hit)
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "captions_hidden", Basis: models.QAHeuristic, From: fs, To: ts, OverlayIDs: []string{o.ID},
				Message: fmt.Sprintf("Around %s %s fills the screen and covers %s, so that speech has no captions.", clock(fs), graphicName(o), count(len(hit), "caption line")),
				Fix:     fix})
		case cz.overlaps(ob):
			what := "MetaRush squeezes the graphic out of the captions' way"
			if region == "lower" {
				what = "MetaRush lifts the captions above it, which can put them over faces"
			}
			fix := nestedFix(o)
			if !nested[o.ID] {
				fix = r.captionCollisionFix(o, lines, region)
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "caption_overlay_stack", Basis: models.QAHeuristic, From: fs, To: ts, OverlayIDs: []string{o.ID},
				Message: fmt.Sprintf("Around %s the captions (%s) and %s use the same part of the screen: %s.", clock(fs), captionsWhere(caps.Position), graphicName(o), what),
				Fix:     fix})
		}
	}
	// Overlays on top of each other (this cut's, and those of cuts played inside it).
	for i := range overlays {
		for j := i + 1; j < len(overlays); j++ {
			a, b := overlays[i], overlays[j]
			from, to := max(a.From, b.From), min(a.From+a.DurationInFrames, b.From+b.DurationInFrames)
			if from >= to || !g.boxOf(a).overlaps(g.boxOf(b)) || (nested[a.ID] && nested[b.ID]) {
				continue // two graphics of a nested cut are that cut's own business
			}
			if nested[b.ID] || (r.spanning(b.ID) && !r.spanning(a.ID)) {
				a, b = b, a // move this cut's graphic (not the nested cut's), and never one that lasts a section (D163)
			}
			fix := &models.QAFix{Summary: fmt.Sprintf("Start %s after %s ends.", graphicName(b), graphicName(a))}
			switch {
			case r.spanning(b.ID):
				fix.Summary = fmt.Sprintf("%s and %s both stay up for a section: take one of them out, or put one at another place on the screen.", upperFirst(graphicName(a)), graphicName(b))
			case !r.overlayLocked(b.ID):
				fix.Ops = []editspec.Op{setOp("updateOverlay", b.ID, map[string]any{"at": round3(float64(a.From+a.DurationInFrames)/r.fps + 0.2)})}
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "overlay_overlap", Basis: models.QAHeuristic, From: float64(from) / r.fps, To: float64(to) / r.fps,
				OverlayIDs: []string{a.ID, b.ID}, Message: fmt.Sprintf("Around %s %s and %s are on screen at the same time, in the same place.", clock(float64(from)/r.fps), graphicName(a), graphicName(b)), Fix: fix})
		}
	}
	r.captionTiming(lines)
	r.endCardGap()
	r.check("layout", models.QAHeuristic, "approximate template boxes and the caption band (2 lines) from plan geometry; overlap in time and space; end cards that stop < 1s before the end", "captions_hidden", "caption_overlay_stack", "overlay_overlap", "caption_flash", "caption_fast", "endcard_gap")
	r.safeArea(g)
}

func (r *run) overlayLocked(id string) bool {
	if r.in.Spec == nil {
		return false
	}
	for _, o := range r.in.Spec.Overlays {
		if o.ID == id {
			return o.Locked
		}
	}
	return false
}

// spanning reports a graphic that lasts a section or the whole video (D163):
// fixes never move it, they move what is in its way.
func (r *run) spanning(id string) bool {
	so := r.specOverlay(id)
	return so != nil && so.Span != nil
}

func (r *run) specOverlay(id string) *editspec.Overlay {
	if r.in.Spec == nil {
		return nil
	}
	for i := range r.in.Spec.Overlays {
		if r.in.Spec.Overlays[i].ID == id {
			return &r.in.Spec.Overlays[i]
		}
	}
	return nil
}

func (r *run) moveOverlayAfterSpeech(o editspec.PlanOverlay, hit []editspec.PlanCaptionLine) *models.QAFix {
	speechEnd := float64(hit[len(hit)-1].To) / r.fps
	end := speechEnd + 0.2
	total := r.total()
	t, _ := editspec.TemplateByID(o.Template)
	dur := total - end
	if dur < t.MinDuration || r.overlayLocked(o.ID) || r.spanning(o.ID) {
		return &models.QAFix{Summary: fmt.Sprintf("Speech with captions goes on until %s: end it earlier (trim the last clip) so %s gets at least %s of its own, or accept speech without captions under it.", clock(speechEnd), graphicName(o), seconds(t.MinDuration))}
	}
	set := map[string]any{"at": round3(end)}
	if so := r.specOverlay(o.ID); so != nil && so.Duration > dur {
		set["duration"] = round3(dur)
	}
	return &models.QAFix{Summary: fmt.Sprintf("Start %s just after the last captioned word (around %s).", graphicName(o), clock(end)), Ops: []editspec.Op{setOp("updateOverlay", o.ID, set)}}
}

// captionsWhere says where the captions sit on screen.
func captionsWhere(position string) string {
	switch position {
	case "top":
		return "at the top"
	case "middle":
		return "in the middle"
	case "bottom":
		return "at the bottom"
	}
	return "low on the screen"
}

// captionCollisionFix moves the overlay into a caption gap when one is long
// enough, otherwise moves the captions to another band.
func (r *run) captionCollisionFix(o editspec.PlanOverlay, lines []editspec.PlanCaptionLine, region string) *models.QAFix {
	need := o.DurationInFrames
	if !r.overlayLocked(o.ID) && !r.spanning(o.ID) {
		prev := 0
		for _, l := range append(lines, editspec.PlanCaptionLine{From: r.plan.DurationInFrames}) {
			if l.From-prev >= need+int(r.fps/2) {
				at := float64(prev)/r.fps + 0.25
				return &models.QAFix{Summary: fmt.Sprintf("Move %s to around %s, where there are no captions (or move the captions; the frames show both).", graphicName(o), clock(at)),
					Ops: []editspec.Op{setOp("updateOverlay", o.ID, map[string]any{"at": round3(at)})}}
			}
			if l.To > prev {
				prev = l.To
			}
		}
	}
	if r.in.Spec == nil || r.in.Spec.Captions == nil || r.in.Spec.Captions.Locked {
		return &models.QAFix{Summary: fmt.Sprintf("Move %s to a moment without captions, or move the captions higher or lower on the screen (they are locked, so no change is suggested).", graphicName(o))}
	}
	pos := "top"
	if region == "center" {
		pos = "lower"
	}
	c := *r.in.Spec.Captions
	c.Position = pos
	where := "to the top of the screen"
	if pos == "lower" {
		where = "lower on the screen"
	}
	return &models.QAFix{Summary: fmt.Sprintf("No moment without captions is long enough for %s: move the captions %s instead, keeping the rest of how they look.", graphicName(o), where),
		Ops: []editspec.Op{{Op: "setCaptions", Captions: &c}}}
}

// endCardGap flags an end card that stops shortly before the video does: the
// last shot flashes back for a moment after the call to action.
func (r *run) endCardGap() {
	total := r.plan.DurationInFrames
	for _, o := range r.plan.Overlays {
		if o.Template != editspec.TplEndCard {
			continue
		}
		end := o.From + o.DurationInFrames
		gap := total - end
		if gap <= 0 || float64(gap) >= r.fps {
			continue
		}
		want := round3(float64(total-o.From) / r.fps)
		fix := &models.QAFix{Summary: fmt.Sprintf("Make %s last until the end of the video (%s in all), or end the last clip with it.", graphicName(o), seconds(want))}
		if t, ok := editspec.TemplateByID(o.Template); ok && want <= t.MaxDuration && !r.overlayLocked(o.ID) {
			fix.Ops = []editspec.Op{setOp("updateOverlay", o.ID, map[string]any{"duration": want})}
		}
		r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "endcard_gap", Basis: models.QAHeuristic, From: float64(end) / r.fps, To: float64(total) / r.fps, OverlayIDs: []string{o.ID},
			Message: fmt.Sprintf("%s stops %s before the video ends, so the last shot flashes back after it.", upperFirst(graphicName(o)), frames(gap)), Fix: fix})
	}
}

// captionTiming flags lines that flash by or read too fast (aggregated).
func (r *run) captionTiming(lines []editspec.PlanCaptionLine) {
	var flash, fast []editspec.PlanCaptionLine
	for _, l := range lines {
		secs := float64(l.To-l.From) / r.fps
		if secs < 0.25 {
			flash = append(flash, l)
		} else if cps := float64(len([]rune(strings.TrimSpace(l.Text)))) / secs; cps > 25 {
			fast = append(fast, l)
		}
	}
	if len(flash) > 0 {
		l := flash[0]
		msg := fmt.Sprintf("Around %s a caption line is on screen for less than a quarter of a second: “%s”.", clock(float64(l.From)/r.fps), trunc(l.Text, 40))
		if len(flash) > 1 {
			msg = fmt.Sprintf("%s are on screen for less than a quarter of a second (the first, around %s: “%s”).", count(len(flash), "caption line"), clock(float64(l.From)/r.fps), trunc(l.Text, 40))
		}
		r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "caption_flash", Basis: models.QAHeuristic, From: float64(l.From) / r.fps, To: float64(l.To) / r.fps,
			Message: msg, Fix: r.captionFlashFix(l)})
	}
	if len(fast) > 0 {
		l := fast[0]
		msg := fmt.Sprintf("Around %s a caption line goes by too fast to read comfortably: “%s”.", clock(float64(l.From)/r.fps), trunc(l.Text, 40))
		if len(fast) > 1 {
			msg = fmt.Sprintf("%s go by too fast to read comfortably (the first, around %s: “%s”).", count(len(fast), "caption line"), clock(float64(l.From)/r.fps), trunc(l.Text, 40))
		}
		r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "caption_fast", Basis: models.QAHeuristic, From: float64(l.From) / r.fps, To: float64(l.To) / r.fps,
			Message: msg + " Captions usually show at most about 17 to 20 letters a second."})
	}
}

// captionFlashFix merges a too-short manual caption segment into its neighbor;
// transcript-timed captions get advice only (their line breaks follow pauses and
// sentence ends).
func (r *run) captionFlashFix(l editspec.PlanCaptionLine) *models.QAFix {
	c := r.in.Spec
	if c == nil || c.Captions == nil || c.Captions.Locked || c.Captions.Source != "manual" {
		return &models.QAFix{Summary: "It's a word on its own between pauses or sentences, which is usually fine. To join it to the next caption, write the captions yourself instead of taking them from the transcript."}
	}
	caps := *c.Captions
	segs := append([]editspec.CaptionSegment(nil), caps.Segments...)
	from, to := float64(l.From)/r.fps, float64(l.To)/r.fps
	for i, sg := range segs {
		if math.Abs(sg.From-from) > 0.05 || math.Abs(sg.To-to) > 0.05 {
			continue
		}
		switch {
		case i > 0:
			segs[i-1].To, segs[i-1].Text = sg.To, strings.TrimSpace(segs[i-1].Text+" "+sg.Text)
		case i+1 < len(segs):
			segs[i+1].From, segs[i+1].Text = sg.From, strings.TrimSpace(sg.Text+" "+segs[i+1].Text)
		default:
			segs[i].To = sg.From + 0.8
			caps.Segments = segs
			return &models.QAFix{Summary: "Keep the only caption on screen for at least 0.8 seconds.", Ops: []editspec.Op{{Op: "setCaptions", Captions: &caps}}}
		}
		caps.Segments = append(segs[:i], segs[i+1:]...)
		return &models.QAFix{Summary: fmt.Sprintf("Join “%s” to the caption next to it.", trunc(sg.Text, 30)), Ops: []editspec.Op{{Op: "setCaptions", Captions: &caps}}}
	}
	return &models.QAFix{Summary: "Make the short caption longer, or join it to the one next to it."}
}

// socialMargins are the vertical short-form UI zones (fractions: top, bottom, right),
// matching editspec's platform safe areas.
var socialMargins = map[string][3]float64{"shorts": {0.10, 0.20, 0.12}, "tiktok": {0.10, 0.22, 0.14}, "reels": {0.12, 0.22, 0.10}}

// safeArea compares the plan's margins with the platforms the video is for.
func (r *run) safeArea(g geom) {
	plat := r.platform()
	var social []string
	for _, p := range r.in.Platforms {
		if _, ok := socialMargins[p]; ok {
			social = append(social, p)
		}
	}
	_, isSocial := socialMargins[plat]
	switch {
	case g.orient == "vertical" && !isSocial:
		target := "shorts"
		sev := models.QAInfo
		if len(social) > 0 {
			target, sev = social[0], models.QAWarning
		}
		m := socialMargins[target]
		uiTop, uiBottom, uiRight := g.H*m[0], g.H*(1-m[1]), g.W*(1-m[2])
		var under []string
		if c := r.plan.Captions; c != nil && len(c.Lines) > 0 {
			if z := g.captionZone(c.Position, c.Style, c.Size); z.B > uiBottom || z.T < uiTop || z.R > uiRight {
				under = append(under, "the captions")
			}
		}
		for _, o := range r.plan.Overlays {
			if b := g.boxOf(o); regionOfOverlay(o) != "full" && (b.B > uiBottom || b.T < uiTop || b.R > uiRight) {
				under = append(under, graphicName(o))
			}
		}
		msg := fmt.Sprintf("This tall video isn't set up for %s, where the app's buttons and text cover about %.0f%% of the screen at the bottom and %.0f%% on the right", platformName(target), m[1]*100, m[2]*100)
		if len(under) > 0 {
			msg += ": " + joinAnd(under) + " would sit under them"
			if sev == models.QAInfo {
				sev = models.QAWarning
			}
		}
		msg += "."
		fix := &models.QAFix{Summary: fmt.Sprintf("Set the video up for %s, so captions and graphics stay clear of the app's buttons and text.", platformName(target))}
		if r.in.Spec != nil {
			out := r.in.Spec.Output
			out.Platform = target
			fix.Ops = []editspec.Op{{Op: "setOutput", Output: &out}}
		}
		r.add(&models.QAFinding{Severity: sev, Kind: "platform_safe_area", Basis: models.QAHeuristic, To: r.total(), Message: msg, Fix: fix})
	case g.orient != "vertical" && isSocial:
		r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "platform_safe_area", Basis: models.QAHeuristic, To: r.total(),
			Message: fmt.Sprintf("The cut is made for %s, but the video is %s (%s), and %s expects a tall 9:16 video.", platformName(plat), g.orient, sizeWords(r.plan.Width, r.plan.Height), platformName(plat))})
	}
	r.check("safe_area", models.QAHeuristic, "plan safe area vs. vertical short-form UI conventions (Shorts/TikTok/Reels bottom 20–22%, right 10–14%)", "platform_safe_area")
}

func trunc(s string, n int) string {
	rs := []rune(s)
	if len(rs) <= n {
		return s
	}
	return string(rs[:n]) + "…"
}

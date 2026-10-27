package qa

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/measures"
	"github.com/metavert-io/metarush/internal/models"
)

// Promises made to the client (D300): rules a person marked as promises
// ("captions on every video", "under 60 seconds for shorts", "logo in the
// last 3 s", "no music louder than speech", "nothing generated without
// disclosure"). Every quality check measures each one and says the result in
// a plain sentence; a broken promise is a problem (an error), so the version
// waits before it goes out (D298), and one MetaRush couldn't measure here is
// worth checking (a warning, never a hold). A rule that is only a rule keeps
// its warnings (projectRules).

// Thresholds of the promises' measures (recorded in the check's threshold).
const (
	CaptionCoverage    = 0.90  // captions show over at least this share of the speech
	CaptionMinLetters  = 0.035 // caption letters at least this share of the picture's shorter side
	CaptionMaxLetters  = 25.0  // letters a second, the fastest caption line (0.25 s or longer)
	SpeechMusicSample  = 0.5   // the speech-over-music gap is averaged over half-second stretches
	PromiseLengthSlack = 1     // frames a length may run over
)

// NotesFingerprint fingerprints a version's delivery notes for a promise that
// reads them ("" none).
func NotesFingerprint(n *models.DeliveryNotes) string {
	if n.Empty() {
		return ""
	}
	h := sha256.New()
	for _, s := range []string{n.Title, strings.Join(n.Titles, "\n"), n.Description, n.ShowNotes} {
		h.Write([]byte(s))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// promiseWords names a promise in a finding.
func promiseWords(rule models.ProjectRule) string {
	s := "The promise “" + strings.TrimSpace(rule.Text) + "”"
	if rule.House && rule.HouseOf != "" {
		s += " (from “" + rule.HouseOf + "”'s settings)"
	}
	return s
}

// promises measures every promise of the project and its Content Settings.
func (r *run) promises() {
	const name = "promises"
	var list []models.ProjectRule
	for _, rule := range r.in.Rules {
		if rule.Promise {
			list = append(list, rule)
		}
	}
	if len(list) == 0 {
		r.skip(name, models.QAObserved, "no promises are made for this project's videos")
		return
	}
	r.rep.Promises = []models.QAPromise{}
	for _, rule := range list {
		p, f := r.measurePromise(rule)
		r.rep.Promises = append(r.rep.Promises, p)
		r.promiseFindings = append(r.promiseFindings, f)
	}
	r.check(name, models.QAObserved, fmt.Sprintf("each promise measured on the render or the cut: a length within %d frame; captions over %.0f%% of the speech, letters %.1f%% of the shorter side, at most %.0f letters a second; a logo through the window; voices over the music in %.1f s stretches; generated clips disclosed on screen or in the description; faces from the on-screen check; size and frame rate as decoded",
		PromiseLengthSlack, CaptionCoverage*100, CaptionMinLetters*100, CaptionMaxLetters, SpeechMusicSample), "promise_broken", "promise_unmeasured")
}

// promiseResult is what one measure found.
type promiseResult struct {
	status   string
	measured string // a sentence
	value    *float64
	limit    *float64
	unit     string
	basis    string
	from, to float64
	fix      *models.QAFix
	clipIDs  []string
	overlays []string
	// adopted: the measure already made the findings (first names): they are the promise's.
	adopted []*models.QAFinding
	// expected is what the version should carry (the disclosure line).
	expected string
}

func num(v float64) *float64 { x := round3(v); return &x }

// measureRule measures a rule (or a promise) with one of measures' kinds:
// the result for the report and what it found (no finding added).
func (r *run) measureRule(rule models.ProjectRule) (models.QAPromise, promiseResult, bool) {
	m, ok := measures.Parse(rule.Measure)
	p := models.QAPromise{RuleID: rule.ID, Text: rule.Text, Measure: rule.Measure, House: rule.House, HouseOf: rule.HouseOf}
	if !ok {
		return p, promiseResult{}, false
	}
	p.Says = upperFirst(measures.Words(rule.Measure)) + "."
	var res promiseResult
	switch m.Kind {
	case measures.MaxLength:
		res = r.promiseLength(m)
	case measures.Shape:
		res = r.promiseShape(m)
	case measures.CaptionsTop, measures.CaptionsBottom:
		res = r.promiseCaptionPlace(m)
	case measures.FirstNames:
		res = r.promiseFirstNames(rule)
	case measures.NoFlashes:
		res = r.promiseNoFlashes()
	case measures.Captions:
		res = r.promiseCaptions(m)
	case measures.LogoEnd, measures.LogoStart:
		res = r.promiseLogo(m)
	case measures.SpeechOverMusic:
		res = r.promiseSpeechOverMusic(m)
	case measures.Disclosed:
		res = r.promiseDisclosed()
	case measures.FacesInFrame:
		res = r.promiseFaces()
	case measures.MinSize:
		res = r.promiseSize(m)
	case measures.MinFPS:
		res = r.promiseFPS(m)
	}
	p.Status, p.Measured, p.Value, p.Limit, p.Unit, p.Basis = res.status, res.measured, res.value, res.limit, res.unit, res.basis
	if p.Basis == "" {
		p.Basis = models.QAHeuristic
	}
	return p, res, true
}

// measurePromise measures a promise: a broken one is a problem (an error),
// one MetaRush couldn't measure here is worth checking (a warning). It returns
// the finding it made (its ID is given when the report is finished).
func (r *run) measurePromise(rule models.ProjectRule) (models.QAPromise, []*models.QAFinding) {
	p, res, ok := r.measureRule(rule)
	if !ok {
		p.Says = "MetaRush can't measure this promise."
		p.Status, p.Basis = models.PromiseUnmeasured, models.QAHeuristic
		p.Measured = "Nothing measures it: keep it as a sign-off instead."
		f := r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "promise_unmeasured", Basis: models.QAHeuristic, From: 0, To: r.total(),
			Message: promiseWords(rule) + " can't be measured, so the quality check can't keep it for you. Keep it as a sign-off instead."})
		return p, []*models.QAFinding{f}
	}
	p.Expected = res.expected
	switch res.status {
	case models.PromiseBroken:
		if len(res.adopted) > 0 {
			return p, res.adopted
		}
		f := r.add(&models.QAFinding{Severity: models.QAError, Kind: "promise_broken", Basis: p.Basis, From: res.from, To: math.Max(res.from, res.to),
			ClipIDs: res.clipIDs, OverlayIDs: res.overlays, Message: promiseWords(rule) + " isn't kept: " + lowerFirst(res.measured), Fix: res.fix})
		return p, []*models.QAFinding{f}
	case models.PromiseUnmeasured:
		f := r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "promise_unmeasured", Basis: p.Basis, From: 0, To: r.total(),
			Message: promiseWords(rule) + " couldn't be measured here. " + res.measured + " Look for it yourself before it goes out."})
		return p, []*models.QAFinding{f}
	}
	return p, nil
}

// linkPromises gives each promise the ID of the finding it made, once the
// report's findings are numbered.
func (r *run) linkPromises() {
	for i, fs := range r.promiseFindings {
		if i >= len(r.rep.Promises) {
			break
		}
		for _, f := range fs {
			r.rep.Promises[i].FindingIDs = append(r.rep.Promises[i].FindingIDs, f.ID)
		}
		if len(fs) > 0 {
			r.rep.Promises[i].FindingID = fs[0].ID
		}
	}
}

// lowerFirst starts a sentence's words after a colon: a word written with
// capitals inside (MetaRush, AI) and quotes stay as they are.
func lowerFirst(s string) string {
	if s == "" || strings.HasPrefix(s, "“") {
		return s
	}
	first, _, _ := strings.Cut(s, " ")
	if strings.ToLower(first[1:]) != first[1:] {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

// decodedDuration is how long the render plays (the plan's length when nothing was decoded).
func (r *run) decodedDuration() float64 {
	if d := r.rep.Measurements.Duration; d > 0 {
		return d
	}
	return r.total()
}

func (r *run) promiseLength(m measures.Measure) promiseResult {
	got := orientationWord(r.plan.Width, r.plan.Height)
	if m.Shape != "" && got != m.Shape {
		return promiseResult{status: models.PromiseNA, basis: models.QAObserved,
			measured: fmt.Sprintf("It is a %s video, so the promise is for other videos.", measures.ShapeWords(got))}
	}
	d := r.decodedDuration()
	res := promiseResult{value: num(d), limit: num(m.Value), unit: "seconds", basis: models.QAObserved}
	if d <= m.Value+PromiseLengthSlack/r.fps+1e-6 {
		res.status, res.measured = models.PromiseKept, fmt.Sprintf("It lasts %s.", lengthOf(d))
		return res
	}
	res.status, res.from, res.to = models.PromiseBroken, m.Value, d
	res.measured = fmt.Sprintf("It lasts %s, %s longer than %s.", lengthOf(d), seconds(d-m.Value), measures.LengthWords(m.Value))
	res.fix = &models.QAFix{Summary: fmt.Sprintf("Shorten it by at least %s.", seconds(d-m.Value))}
	return res
}

// lengthOf says a video's length: "58.2 seconds", "1:12".
func lengthOf(d float64) string {
	if d < 60 {
		return seconds(d)
	}
	return clock(d)
}

func (r *run) promiseShape(m measures.Measure) promiseResult {
	got := orientationWord(r.plan.Width, r.plan.Height)
	res := promiseResult{basis: models.QAObserved, measured: fmt.Sprintf("It is %s.", measures.ShapeWords(got))}
	if got == m.Shape {
		res.status = models.PromiseKept
		return res
	}
	res.status, res.to = models.PromiseBroken, r.total()
	res.fix = &models.QAFix{Summary: "Make it " + measures.ShapeWords(m.Shape) + "."}
	if r.in.Spec != nil {
		preset := map[string]string{"landscape": editspec.PresetLandscape, "vertical": editspec.PresetVertical, "square": editspec.PresetSquare}[m.Shape]
		out := r.in.Spec.Output
		out.Preset, out.Width, out.Height = preset, editspec.Presets[preset][0], editspec.Presets[preset][1]
		res.fix.Ops = []editspec.Op{{Op: "setOutput", Output: &out}}
	}
	return res
}

func (r *run) promiseCaptionPlace(m measures.Measure) promiseResult {
	want := strings.TrimPrefix(m.Kind, "captions-")
	c := r.specCaptions()
	if c == nil || !c.Enabled {
		return promiseResult{status: models.PromiseNA, measured: "It has no captions.", basis: models.QAHeuristic}
	}
	pos := c.Position
	if pos == "" {
		pos = "lower"
	}
	res := promiseResult{basis: models.QAHeuristic, measured: fmt.Sprintf("The captions are %s.", captionsWhere(pos))}
	if (want == "top" && pos == "top") || (want == "bottom" && (pos == "bottom" || pos == "lower")) {
		res.status = models.PromiseKept
		return res
	}
	res.status, res.to = models.PromiseBroken, r.total()
	res.fix = &models.QAFix{Summary: "Move the captions " + map[string]string{"top": "to the top", "bottom": "to the bottom"}[want] + "."}
	if !c.Locked {
		nc := *c
		nc.Position = want
		res.fix.Ops = []editspec.Op{{Op: "setCaptions", Captions: &nc}}
	}
	return res
}

func (r *run) specCaptions() *editspec.Captions {
	if r.in.Spec == nil {
		return nil
	}
	return r.in.Spec.Captions
}

// promiseFirstNames turns the names the first-names check finds into the
// promise's problems.
func (r *run) promiseFirstNames(rule models.ProjectRule) promiseResult {
	res := promiseResult{basis: models.QAHeuristic}
	if r.in.Spec == nil {
		res.status, res.measured = models.PromiseUnmeasured, "MetaRush doesn't have the cut this render was made from."
		return res
	}
	before := len(r.pend)
	r.firstNames(rule)
	found := r.pend[before:]
	if len(found) == 0 {
		res.status, res.measured = models.PromiseKept, "Every name on screen is a first name."
		return res
	}
	for _, f := range found {
		f.Severity, f.Kind = models.QAError, "promise_broken"
	}
	res.status, res.adopted = models.PromiseBroken, found
	res.measured = fmt.Sprintf("%d graphics show more than a first name.", len(found))
	if len(found) == 1 {
		res.measured = "A graphic shows more than a first name."
	}
	return res
}

func (r *run) promiseNoFlashes() promiseResult {
	res := promiseResult{basis: models.QAHeuristic}
	var first *models.QAFinding
	n := 0
	for _, f := range r.pend {
		if f.Kind == "edge_flash" {
			n++
			if first == nil {
				first = f
			}
		}
	}
	if n == 0 {
		res.status, res.measured = models.PromiseKept, "No clip flashes another picture at its start or end."
		return res
	}
	res.status, res.from, res.to, res.fix = models.PromiseBroken, first.From, first.To, first.Fix
	res.measured = fmt.Sprintf("%s flash another picture at an edge (the first around %s).", upperFirst(count(n, "clip")), clock(first.From))
	return res
}

// promiseCaptions: captions on wherever someone speaks, large enough and slow
// enough to read (from the plan the renderer draws).
func (r *run) promiseCaptions(m measures.Measure) promiseResult {
	res := promiseResult{basis: models.QAHeuristic, unit: "percent", limit: num(CaptionCoverage * 100)}
	speech, _ := r.speechSpans()
	total := 0.0
	for _, s := range speech {
		total += s.To - s.From
	}
	if total < 1 {
		res.status, res.measured, res.limit, res.unit = models.PromiseNA, "Nobody speaks in it, so there is nothing to caption.", nil, ""
		return res
	}
	c := r.specCaptions()
	var lines []editspec.PlanCaptionLine
	if r.plan.Captions != nil {
		lines = r.plan.Captions.Lines
	}
	if c == nil || !c.Enabled || len(lines) == 0 {
		// Where the promise says the footage carries its own captions
		// (captions:burned-in), they count when the on-screen index reads them
		// low in the picture while people speak; unread, it couldn't be measured.
		if m.Burned {
			share, read := r.burnedCaptions(speech)
			switch {
			case !read:
				res.status, res.limit, res.unit = models.PromiseUnmeasured, nil, ""
				res.measured = "The cut's own captions are off, and MetaRush couldn't read the captions the footage carries (that needs the footage looked at on a Mac)."
				return res
			case share >= CaptionCoverage:
				res.status, res.value = models.PromiseKept, num(share*100)
				res.measured = fmt.Sprintf("The cut's own captions are off; the footage's own captions show low in the picture over %.0f%% of the speech.", math.Floor(share*100))
				return res
			}
			res.status, res.value, res.to = models.PromiseBroken, num(share*100), r.total()
			res.measured = fmt.Sprintf("The cut's own captions are off, and the footage's own captions show over only %.0f%% of the speech.", math.Floor(share*100))
			res.fix = &models.QAFix{Summary: "Turn the cut's captions on where the footage has none."}
			return res
		}
		res.status, res.value, res.to = models.PromiseBroken, num(0), r.total()
		res.measured = "It has no captions."
		res.fix = &models.QAFix{Summary: "Turn the captions on."}
		if c == nil || !c.Locked {
			nc := editspec.Captions{Enabled: true, Source: "transcript"}
			if c != nil {
				nc = *c
				nc.Enabled = true
			}
			res.fix.Ops = []editspec.Op{{Op: "setCaptions", Captions: &nc}}
		} else {
			res.fix.Summary = "The captions are locked: unlock them to turn them on."
		}
		return res
	}
	// Coverage: the share of the speech with a caption on screen (every tenth of a second).
	covered, all := 0, 0
	gapAt := -1.0
	for _, s := range speech {
		for t := s.From; t < s.To; t += 0.1 {
			all++
			f := int(t * r.fps)
			on := false
			for _, l := range lines {
				if f >= l.From && f < l.To {
					on = true
					break
				}
			}
			if on {
				covered++
			} else if gapAt < 0 {
				gapAt = t
			}
		}
	}
	share := 1.0
	if all > 0 {
		share = float64(covered) / float64(all)
	}
	res.value = num(share * 100)
	// Size: the letters' height against the picture's shorter side (the look's numbers, D245).
	g := newGeom(r.plan)
	style, size := r.plan.Captions.Style, r.plan.Captions.Size
	look := editspec.CaptionLook(style)
	letters := pick(g.orient, 64, 52, 58) * look.Scale * editspec.CaptionSizeScale(size) / 1080
	// Speed: the fastest line on screen long enough to be read as a line.
	fastest, fastAt := 0.0, 0.0
	for _, l := range lines {
		secs := float64(l.To-l.From) / r.fps
		if secs < 0.25 {
			continue
		}
		if cps := float64(len([]rune(strings.TrimSpace(l.Text)))) / secs; cps > fastest {
			fastest, fastAt = cps, float64(l.From)/r.fps
		}
	}
	res.measured = fmt.Sprintf("Captions show over %.0f%% of the speech, their letters are %.1f%% of the picture's shorter side and the fastest line shows %.0f letters a second.",
		math.Floor(share*100), letters*100, fastest)
	var why []string
	if share < CaptionCoverage {
		why = append(why, fmt.Sprintf("only %.0f%% of the speech has captions (the first stretch without them around %s)", math.Floor(share*100), clock(gapAt)))
		res.from = math.Max(0, gapAt)
	}
	if letters < CaptionMinLetters-1e-9 {
		why = append(why, fmt.Sprintf("their letters are %.1f%% of the picture's shorter side, smaller than the %.1f%% that reads well on a phone", letters*100, CaptionMinLetters*100))
	}
	if fastest > CaptionMaxLetters {
		why = append(why, fmt.Sprintf("a line around %s goes by at %.0f letters a second, faster than the %.0f people can read", clock(fastAt), fastest, CaptionMaxLetters))
		if res.from == 0 {
			res.from = fastAt
		}
	}
	if len(why) == 0 {
		res.status = models.PromiseKept
		return res
	}
	res.status, res.to = models.PromiseBroken, r.total()
	res.measured = upperFirst(joinAnd(why)) + "."
	res.fix = &models.QAFix{Summary: "Make the captions cover every part with speech, large enough and slow enough to read."}
	if letters < CaptionMinLetters-1e-9 && !c.Locked {
		nc := *c
		nc.Size = editspec.CaptionLarge
		if editspec.CaptionLook(nc.Style).Scale*editspec.CaptionSizeScale(nc.Size)*pick(g.orient, 64, 52, 58)/1080 < CaptionMinLetters {
			nc.Style = editspec.CaptionClean
		}
		res.fix = &models.QAFix{Summary: "Make the captions larger.", Ops: []editspec.Op{{Op: "setCaptions", Captions: &nc}}}
	}
	return res
}

// burnedCaptions is the share of the speech during which the footage itself
// shows words low in the picture (captions burned into it), from the
// on-screen index; ok false when the speech's footage wasn't indexed.
func (r *run) burnedCaptions(speech []Span) (float64, bool) {
	if len(r.in.OnScreen) == 0 {
		return 0, false
	}
	W, H := float64(r.plan.Width), float64(r.plan.Height)
	covered, all, looked := 0, 0, 0
	for _, s := range speech {
		for t := s.From; t < s.To; t += 0.25 {
			all++
			f := int(t * r.fps)
			cs := r.clipsAt(f, f+1)
			if len(cs) != 1 {
				continue
			}
			c := cs[0]
			idx := r.in.OnScreen[c.AssetID]
			if idx == nil || c.SourceWidth <= 0 || c.SourceHeight <= 0 {
				continue
			}
			looked++
			srcT := r.srcTime(c, f)
			left, top, w, h := editspec.CropRect(c.Framing, float64(c.SourceWidth), float64(c.SourceHeight), W, H, float64(f-c.From))
			for _, tx := range idx.Text {
				if srcT < tx.From || srcT >= tx.To {
					continue
				}
				cy := top + (tx.Box[1]+tx.Box[3]/2)*h
				cx := left + (tx.Box[0]+tx.Box[2]/2)*w
				if cy > H*0.6 && cy < H && cx > 0 && cx < W {
					covered++
					break
				}
			}
		}
	}
	if all == 0 || looked*10 < all*9 {
		return 0, false
	}
	return float64(covered) / float64(all), true
}

// showsLogo reports a graphic that shows a logo: one with a logo among its
// fields (a logo in a corner, an end card or a title with the brand's logo),
// or an end card.
func showsLogo(o editspec.PlanOverlay) bool {
	if o.Motion == nil && (o.Template == editspec.TplEndCard || strings.HasPrefix(o.Template, "end.") || strings.Contains(o.Template, "endcard")) {
		return true
	}
	has := func(raw json.RawMessage) bool {
		var p map[string]any
		if json.Unmarshal(raw, &p) != nil {
			return false
		}
		for k, v := range p {
			if s, ok := v.(string); ok && strings.TrimSpace(s) != "" && (k == "logo" || strings.HasPrefix(k, "logo")) {
				return true
			}
		}
		return false
	}
	if has(o.Params) {
		return true
	}
	for _, sg := range o.Segments {
		if has(sg.Params) {
			return true
		}
	}
	return false
}

func (r *run) promiseLogo(m measures.Measure) promiseResult {
	total := r.plan.DurationInFrames
	win := int(math.Round(m.Value * r.fps))
	if win > total {
		win = total
	}
	from, to := total-win, total
	if m.Kind == measures.LogoStart {
		from, to = 0, win
	}
	res := promiseResult{basis: models.QAHeuristic, unit: "seconds", limit: num(float64(win) / r.fps), from: float64(from) / r.fps, to: float64(to) / r.fps}
	type iv struct{ a, b int }
	var spans []iv
	var names []string
	for _, o := range r.plan.Overlays {
		if !showsLogo(o) {
			continue
		}
		a, b := max(o.From, from), min(o.From+o.DurationInFrames, to)
		if b > a {
			spans = append(spans, iv{a, b})
			names = append(names, graphicName(o))
			res.overlays = append(res.overlays, o.ID)
		}
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].a < spans[j].a })
	on, end := 0, from
	for _, s := range spans {
		if s.b <= end {
			continue
		}
		on += s.b - max(s.a, end)
		end = s.b
	}
	seen := float64(on) / r.fps
	res.value = num(seen)
	window := "the last " + windowLength(m.Value)
	if m.Kind == measures.LogoStart {
		window = "the first " + windowLength(m.Value)
	}
	switch {
	case m.Kind == measures.LogoEnd && on >= win-2:
		res.status, res.measured = models.PromiseKept, fmt.Sprintf("%s is on screen for %s.", upperFirst(names[0]), window)
		return res
	case m.Kind == measures.LogoStart && (on >= int(math.Min(float64(win), r.fps))-2) && on > 0:
		res.status, res.measured = models.PromiseKept, fmt.Sprintf("%s is on screen within %s.", upperFirst(names[0]), window)
		return res
	case on == 0:
		res.measured = fmt.Sprintf("Nothing shows a logo in %s.", window)
	default:
		res.measured = fmt.Sprintf("A logo is on screen for only %s of %s.", seconds(seen), window)
	}
	res.status = models.PromiseBroken
	res.fix = &models.QAFix{Summary: fmt.Sprintf("Show your logo (or an end card) for %s.", window)}
	if name := r.logoName(); name != "" {
		params, _ := json.Marshal(map[string]any{"logo": name})
		o := editspec.Overlay{ID: "promise-logo", Template: editspec.TplLogoCorner, At: round3(float64(from) / r.fps), Duration: round3(float64(win) / r.fps), Params: params}
		res.fix.Ops = []editspec.Op{{Op: "addOverlay", Overlay: &o}}
	}
	return res
}

// windowLength says a window's length after "the last": "3 seconds", "second".
func windowLength(v float64) string {
	if v == 1 {
		return "second"
	}
	return strconv.FormatFloat(v, 'f', -1, 64) + " seconds"
}

// logoName is the cut's main logo ("" when it pins none).
func (r *run) logoName() string {
	if r.in.Spec == nil {
		return ""
	}
	for _, n := range []string{"logo", "logo-wordmark", "logo-icon"} {
		if _, ok := r.in.Spec.Resources[n]; ok {
			return n
		}
	}
	return ""
}

// promiseSpeechOverMusic estimates, wherever someone speaks over music, how
// much louder the voices are than the music (each recording's measured
// loudness and its gain in the mix, the way the narration check does).
func (r *run) promiseSpeechOverMusic(m measures.Measure) promiseResult {
	res := promiseResult{basis: models.QAHeuristic, unit: "steps", limit: num(m.Value)}
	var music []editspec.PlanAudio
	for _, a := range r.plan.Audio {
		if a.Role == editspec.RoleMusic || a.Bus == editspec.BusMusic {
			music = append(music, a)
		}
	}
	if len(music) == 0 {
		res.status, res.measured, res.limit, res.unit = models.PromiseKept, "It has no music.", nil, ""
		return res
	}
	for _, a := range music {
		if _, ok := r.in.Loudness[a.AssetID]; !ok {
			res.status, res.measured, res.limit, res.unit = models.PromiseUnmeasured, "MetaRush hasn't measured how loud the music is yet.", nil, ""
			return res
		}
	}
	speechAt := func(f int) (float64, bool) {
		best, ok := -200.0, false
		for _, a := range r.plan.Audio {
			if !(a.Role == editspec.RoleVoiceover || a.Bus == editspec.BusVoiceover || a.Role == editspec.RoleDialogue) || f < a.From || f >= a.From+a.DurationInFrames {
				continue
			}
			if l, has := r.in.Loudness[a.AssetID]; has {
				if v := l.LUFS + gainDB(a.Gain, a.GainDB, f-a.From); v > best {
					best, ok = v, true
				}
			}
		}
		for _, c := range r.plan.Clips {
			from, dur, gain := c.Sound()
			if c.Muted || !c.HasAudio || f < from || f >= from+dur {
				continue
			}
			l, has := r.in.Loudness[c.AssetID]
			if !has {
				continue
			}
			if c.SpeechLUFS != nil {
				l.LUFS = *c.SpeechLUFS
			}
			if v := l.LUFS + gainDB(gain, 20*math.Log10(math.Max(1e-6, c.Volume)), f-from); v > best {
				best, ok = v, true
			}
		}
		return best, ok
	}
	musicAt := func(f int) (float64, bool) {
		sum := 0.0
		for _, a := range music {
			if f < a.From || f >= a.From+a.DurationInFrames {
				continue
			}
			l := r.in.Loudness[a.AssetID]
			sum += math.Pow(10, (l.LUFS+gainDB(a.Gain, a.GainDB, f-a.From))/10)
		}
		if sum <= 0 {
			return 0, false
		}
		return 10 * math.Log10(sum), true
	}
	speech, _ := r.speechSpans()
	step := max(1, int(r.fps/10))
	bucket := max(1, int(SpeechMusicSample*r.fps))
	worst, worstAt, measured, unknown := math.Inf(1), 0.0, false, false
	for _, s := range speech {
		for b := int(s.From * r.fps); b < int(s.To*r.fps); b += bucket {
			var gaps []float64
			for f := b; f < min(b+bucket, int(s.To*r.fps)); f += step {
				mv, playing := musicAt(f)
				if !playing {
					continue
				}
				sv, ok := speechAt(f)
				if !ok {
					unknown = true
					continue
				}
				gaps = append(gaps, sv-mv)
			}
			if len(gaps) == 0 {
				continue
			}
			mean := 0.0
			for _, g := range gaps {
				mean += g
			}
			mean /= float64(len(gaps))
			measured = true
			if mean < worst {
				worst, worstAt = mean, float64(b)/r.fps
			}
		}
	}
	if !measured {
		if unknown {
			res.status, res.measured, res.limit, res.unit = models.PromiseUnmeasured, "MetaRush hasn't measured how loud the voices are yet.", nil, ""
			return res
		}
		res.status, res.measured, res.limit, res.unit = models.PromiseKept, "Nobody speaks while the music plays.", nil, ""
		return res
	}
	worst = math.Round(worst*10) / 10
	res.value = num(worst)
	where := fmt.Sprintf("around %s", clock(worstAt))
	switch {
	case worst >= 0:
		res.measured = fmt.Sprintf("Where the music comes closest (%s), the voices are %s louder than it.", where, stepsOf(worst))
	default:
		res.measured = fmt.Sprintf("Where the music comes closest (%s), it is %s louder than the voices.", where, stepsOf(-worst))
	}
	if worst >= m.Value-0.5 {
		res.status = models.PromiseKept
		return res
	}
	need := math.Ceil(m.Value - worst)
	res.status, res.from, res.to = models.PromiseBroken, worstAt, worstAt+SpeechMusicSample
	res.fix = r.busFix(fmt.Sprintf("Turn the music down about %s on the Sound tab, or let it dip further while people speak.", stepsOf(need)), editspec.BusMusic, -need)
	return res
}

func stepsOf(v float64) string {
	s := strconv.FormatFloat(math.Round(v*10)/10, 'f', -1, 64)
	if s == "1" {
		return "1 loudness step"
	}
	return s + " loudness steps"
}

// shownClip is a clip as it plays in the finished video: its frames on the
// outer timeline, nested cuts' clips included (D84).
type shownClip struct {
	c        editspec.PlanClip
	from, to int  // outer frames
	nested   bool // inside a cut played in this one
}

// shownClips walks the plan and the cuts it plays (nested, at any depth).
func (r *run) shownClips() []shownClip {
	var out []shownClip
	var walk func(p *editspec.RenderPlan, offset, lo, hi, depth int, nested bool)
	walk = func(p *editspec.RenderPlan, offset, lo, hi, depth int, nested bool) {
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
				if np != nil {
					walk(np.Plan, offset+c.From-c.TrimBefore, from, to, depth+1, true)
					continue
				}
			}
			out = append(out, shownClip{c: c, from: from, to: to, nested: nested})
		}
	}
	walk(r.plan, 0, 0, r.plan.DurationInFrames, 0, false)
	return out
}

// reExplicitAI is a graphic's words that say something was made with AI.
var reExplicitAI = regexp.MustCompile(`(?i)\b(?:made|created|generated|produced|imagined)\s+(?:with|by|using)\s+(?:an?\s+)?(?:ai|a\.i\.|artificial intelligence)\b|\bai[- ]generated\b|\b(?:ai|a\.i\.)[- ](?:made|created|imagery|footage|voice|narration|picture|image)\b`)

// DisclosureLine is the sentence a version's description carries to disclose
// what was made with AI (D300): written from the records, so a description
// that says it is present and up to date ("" nothing was made with AI).
func DisclosureLine(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	return "Made with AI: " + joinAnd(parts) + "."
}

// normText compares a sentence loosely: spaces and quote marks.
func normText(s string) string {
	s = strings.NewReplacer("“", "\"", "”", "\"", "‘", "'", "’", "'").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// promiseDisclosed: every AI-generated clip (nested cuts' too), AI voice and
// AI picture is disclosed — a graphic that says so explicitly while it plays,
// or the version's description carrying MetaRush's disclosure line, written
// from the records and current.
func (r *run) promiseDisclosed() promiseResult {
	res := promiseResult{basis: models.QAHeuristic, unit: "clips"}
	r.rep.NotesSHA = NotesFingerprint(r.in.Notes)
	type item struct {
		what     string // in the findings
		part     string // in the disclosure line
		from, to float64
		clip     string
	}
	var items []item
	seenAsset := map[string]bool{}
	for _, sc := range r.shownClips() {
		g, ok := r.in.Generated[sc.c.AssetID]
		if !ok {
			continue
		}
		name := firstNonEmptyQA(r.in.AssetNames[sc.c.AssetID], sc.c.ID)
		what := "the AI-generated clip “" + name + "”"
		if sc.nested {
			what += " (in a cut played in this one)"
		}
		it := item{what: what, from: float64(sc.from) / r.fps, to: float64(sc.to) / r.fps, clip: sc.c.ID}
		if !seenAsset[sc.c.AssetID] {
			seenAsset[sc.c.AssetID] = true
			it.part = "the shot “" + name + "”"
			if g.Provider != "" {
				it.part += " (" + g.Provider + ")"
			}
		}
		items = append(items, it)
	}
	// AI voices: the narration's (once), and each change of words in its
	// speaker's voice (D311), nested cuts' included.
	voice := false
	facts := map[string]SaidFacts{}
	for _, f := range r.said() {
		facts[f.ChangeID] = f
	}
	changes := map[string]bool{}
	for _, a := range r.plan.Audio {
		if !a.AIVoice {
			continue
		}
		from, to := float64(a.From)/r.fps, float64(a.From+a.DurationInFrames)/r.fps
		if id := editspec.SaidChangeID(a.ID); id != "" {
			if changes[id] {
				continue
			}
			changes[id] = true
			f := facts[id]
			it := item{what: "words changed with AI in the speaker's voice", part: "words changed in a speaker's voice", from: from, to: to}
			if f.Words != "" {
				who := firstNonEmptyQA(f.Who, "the speaker")
				it.what = fmt.Sprintf("the words “%s” changed with AI in %s's voice", f.Words, who)
				it.part = fmt.Sprintf("the words “%s” in %s's voice", f.Words, who)
			}
			if a.Nested != "" {
				it.what += " (in a cut played in this one)"
			}
			items = append(items, it)
			continue
		}
		if !voice {
			voice = true
			items = append(items, item{"the AI voice", "the narration's voice", from, to, ""})
		}
	}
	for _, f := range r.said() { // new words the cut plays some other way (as a track of their own)
		if changes[f.ChangeID] || f.Words == "" {
			continue
		}
		changes[f.ChangeID] = true
		who := firstNonEmptyQA(f.Who, "the speaker")
		items = append(items, item{what: fmt.Sprintf("the words “%s” changed with AI in %s's voice", f.Words, who), part: fmt.Sprintf("the words “%s” in %s's voice", f.Words, who), from: f.From, to: f.To})
	}
	for _, rc := range r.in.Resources {
		if rc.AI {
			n := firstNonEmptyQA(rc.Title, rc.Name)
			items = append(items, item{"the AI picture “" + n + "”", "the picture “" + n + "”", 0, r.total(), ""})
		}
	}
	if len(items) == 0 {
		res.status, res.measured, res.unit = models.PromiseKept, "Nothing in it was made with AI.", ""
		return res
	}
	var parts []string
	for _, it := range items {
		if it.part != "" {
			parts = append(parts, it.part)
		}
	}
	line := DisclosureLine(parts)
	res.expected = line
	desc := ""
	if n := r.in.Notes; n != nil {
		desc = normText(n.Title + "\n" + n.Description + "\n" + n.ShowNotes)
	}
	if strings.Contains(desc, normText(line)) {
		res.status, res.value = models.PromiseKept, num(0)
		res.measured = fmt.Sprintf("The version's description says what was made with AI, as the records have it: %s", line)
		return res
	}
	// Graphics whose words say so explicitly, and when.
	type iv struct{ a, b float64 }
	var says []iv
	for _, o := range r.plan.Overlays {
		if overlaySays(o, reExplicitAI) {
			says = append(says, iv{float64(o.From) / r.fps, float64(o.From+o.DurationInFrames) / r.fps})
		}
	}
	var missing []item
	for _, it := range items {
		ok := false
		for _, s := range says {
			if math.Min(s.b, it.to)-math.Max(s.a, it.from) >= math.Min(0.5, it.to-it.from) {
				ok = true
				break
			}
		}
		if !ok {
			missing = append(missing, it)
		}
	}
	res.value = num(float64(len(missing)))
	if len(missing) == 0 {
		res.status, res.measured = models.PromiseKept, fmt.Sprintf("A graphic says it was made with AI while each AI-made part plays (%s).", count(len(items), "AI-made part"))
		return res
	}
	var what []string
	for i, m := range missing {
		if i == 3 {
			what = append(what, fmt.Sprintf("%d more", len(missing)-3))
			break
		}
		w := m.what
		if m.clip != "" {
			w += fmt.Sprintf(" (%s to %s)", clock(m.from), clock(m.to))
			res.clipIDs = append(res.clipIDs, m.clip)
		}
		what = append(what, w)
	}
	res.status, res.from, res.to = models.PromiseBroken, missing[0].from, missing[0].to
	res.measured = fmt.Sprintf("Neither a graphic saying so while it plays nor the version's description tells viewers about %s.", joinAnd(what))
	if strings.Contains(strings.ToLower(desc), "made with ai:") {
		res.measured += " The description's line about AI is out of date."
	}
	res.fix = &models.QAFix{Summary: "Put this line in the version's description (its delivery notes): “" + line + "” — or a title saying it was made with AI on screen while each part plays."}
	return res
}

func firstNonEmptyQA(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

// overlaySays reports a graphic whose words match.
func overlaySays(o editspec.PlanOverlay, re *regexp.Regexp) bool {
	match := func(raw json.RawMessage) bool {
		var p map[string]any
		if json.Unmarshal(raw, &p) != nil {
			return false
		}
		for _, v := range p {
			if s, ok := v.(string); ok && re.MatchString(s) {
				return true
			}
		}
		return false
	}
	if match(o.Params) {
		return true
	}
	for _, sg := range o.Segments {
		if match(sg.Params) {
			return true
		}
	}
	return false
}

// promiseFaces reads the on-screen check (qa-5, D116).
func (r *run) promiseFaces() promiseResult {
	res := promiseResult{basis: models.QAHeuristic}
	var check *models.QACheck
	for i := range r.rep.Checks {
		if r.rep.Checks[i].Name == "on_screen" {
			check = &r.rep.Checks[i]
		}
	}
	if check == nil || check.Status == "skipped" {
		why := "MetaRush looks at faces only in footage it has indexed on a Mac."
		if check != nil && check.Detail != "" {
			why = sentenceOf(check.Detail)
		}
		res.status, res.measured = models.PromiseUnmeasured, why
		return res
	}
	res.basis = check.Basis
	var cut []*models.QAFinding
	for _, f := range r.pend {
		if f.Kind == "face_cut_off" {
			cut = append(cut, f)
		}
	}
	if len(cut) == 0 {
		// Kept only where every second shown was looked at: the on-screen
		// check leaves out cuts played in this one, calls and footage nobody
		// indexed (D116).
		if unseen, secs := r.facesUnseen(); secs >= 0.5 {
			res.status = models.PromiseUnmeasured
			res.measured = fmt.Sprintf("No face is cut off where MetaRush looked, but it couldn't look at faces in %s (%s of the video).", joinAnd(unseen), seconds(secs))
			return res
		}
		res.status, res.measured = models.PromiseKept, "No face is cut off by the edge of the picture."
		return res
	}
	f := cut[0]
	res.status, res.from, res.to, res.fix, res.clipIDs = models.PromiseBroken, f.From, f.To, f.Fix, f.ClipIDs
	res.measured = fmt.Sprintf("A face is cut off by the edge of the picture around %s.", clock(f.From))
	if len(cut) > 1 {
		res.measured = fmt.Sprintf("Faces are cut off by the edge of the picture %s (the first around %s).", timesWord(len(cut)), clock(f.From))
	}
	return res
}

// facesUnseen names the clips whose faces the on-screen check couldn't look
// at — a cut played in this one, a call, footage nobody indexed — and how
// long they are shown.
func (r *run) facesUnseen() ([]string, float64) {
	step := max(1, int(math.Round(onScreenStep*r.fps)))
	unseen := map[string]bool{}
	var names []string
	frames := 0
	for f := 0; f < r.plan.DurationInFrames; f += step {
		cs := r.clipsAt(f, f+1)
		if len(cs) != 1 {
			continue // nothing shown, or a transition
		}
		c := cs[0]
		why := ""
		switch {
		case editspec.IsNested(c.AssetID):
			why = "the cut played in this one (" + c.ID + ")"
		case c.Conversation != nil:
			why = "the call “" + c.ID + "”"
		case c.SourceWidth <= 0 || c.SourceHeight <= 0 || r.in.OnScreen[c.AssetID] == nil:
			why = "the clip “" + firstNonEmptyQA(r.in.AssetNames[c.AssetID], c.ID) + "”"
		default:
			if idx := r.in.OnScreen[c.AssetID]; idx.Duration > 0 && r.srcTime(c, f) > idx.Duration+math.Max(1, idx.Step) {
				why = "the clip “" + firstNonEmptyQA(r.in.AssetNames[c.AssetID], c.ID) + "”"
			}
		}
		if why == "" {
			continue
		}
		frames += step
		if !unseen[why] {
			unseen[why] = true
			names = append(names, why)
		}
	}
	return names, float64(frames) / r.fps
}

func timesWord(n int) string {
	if n == 2 {
		return "twice"
	}
	return strconv.Itoa(n) + " times"
}

func sentenceOf(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return s
	}
	s = upperFirst(s)
	if !strings.HasSuffix(s, ".") {
		s += "."
	}
	return s
}

func (r *run) promiseSize(m measures.Measure) promiseResult {
	w, h := r.rep.Measurements.Width, r.rep.Measurements.Height
	basis := models.QAObserved
	note := ""
	if w == 0 || h == 0 || r.in.Scale < 1 {
		// A review copy is smaller on purpose: the cut's own size is what goes out.
		w, h, basis = r.plan.Width, r.plan.Height, models.QAHeuristic
		if r.in.Scale < 1 {
			note = " (the cut's own size: this is a smaller review copy)"
		}
	}
	short := min(w, h)
	res := promiseResult{basis: basis, unit: "pixels", value: num(float64(short)), limit: num(m.Value),
		measured: fmt.Sprintf("Its picture is %s, %d pixels on its shorter side%s.", sizeWords(w, h), short, note)}
	if float64(short) >= m.Value {
		res.status = models.PromiseKept
		return res
	}
	res.status, res.to = models.PromiseBroken, r.total()
	res.fix = &models.QAFix{Summary: fmt.Sprintf("Render it at least %d pixels on its shorter side.", int(m.Value))}
	return res
}

func (r *run) promiseFPS(m measures.Measure) promiseResult {
	fps := r.rep.Measurements.FPS
	basis := models.QAObserved
	if fps <= 0 {
		fps, basis = float64(r.plan.FPS), models.QAHeuristic
	}
	res := promiseResult{basis: basis, unit: "fps", value: num(fps), limit: num(m.Value),
		measured: fmt.Sprintf("It plays %s frames a second.", strings.TrimSuffix(strconv.FormatFloat(fps, 'f', 2, 64), ".00"))}
	if fps >= m.Value-0.01 {
		res.status = models.PromiseKept
		return res
	}
	res.status, res.to = models.PromiseBroken, r.total()
	res.fix = &models.QAFix{Summary: fmt.Sprintf("Make the cut %s frames a second and render it again.", strconv.FormatFloat(math.Ceil(m.Value), 'f', -1, 64))}
	for _, f := range []int{24, 25, 30, 50, 60} { // the frame rates a cut takes
		if r.in.Spec != nil && float64(f) >= m.Value-0.01 {
			out := r.in.Spec.Output
			out.FPS = f
			res.fix.Summary = fmt.Sprintf("Make the cut %d frames a second and render it again.", f)
			res.fix.Ops = []editspec.Op{{Op: "setOutput", Output: &out}}
			break
		}
	}
	return res
}

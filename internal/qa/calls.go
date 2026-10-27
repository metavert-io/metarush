package qa

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
)

// Call footage (D161, D162) and the project's rules (D166) in the quality
// check: split-second flashes of another picture at clip edges, names a call
// app printed that nothing covers, a name bar naming someone else than who is
// on screen, and the rules the check can measure.

// CallFacts is what the worker worked out about the cut's call footage.
type CallFacts struct {
	// Flashes and the change that removes each (editspec.EdgeFlashFix).
	Flashes    []editspec.EdgeFlash
	FlashFixes [][]editspec.Op
	// PrintedClips counts clips whose footage shows names a call app printed,
	// Corner where; NameBar is the built-in name bar that covers them (nil when
	// the cut has one).
	PrintedClips int
	Corner       string
	NameBar      *editspec.NameBarPlan
	// Mismatches are where the cut's name bar names someone else than who is on
	// screen; Rename the change that writes its names again.
	Mismatches []editspec.NameMismatch
	Rename     []editspec.Op
}

// NoFlashes reports the project rule that makes flashes errors.
func noFlashes(rules []models.ProjectRule) bool {
	for _, r := range rules {
		if r.Measure == "no-flashes" {
			return true
		}
	}
	return false
}

func (r *run) callFootage() {
	const name = "call_footage"
	cf := r.in.Calls
	if cf == nil || (len(cf.Flashes) == 0 && cf.PrintedClips == 0 && len(cf.Mismatches) == 0) {
		r.skip(name, models.QAHeuristic, "no clip shows footage whose picture switches between people, or MetaRush hasn't found its switches yet")
		return
	}
	sev := models.QAWarning
	if noFlashes(r.in.Rules) {
		sev = models.QAError
	}
	placed, _ := editspec.Timeline(r.in.Spec)
	for i, f := range cf.Flashes {
		at := 0.0
		if f.Index < len(placed) {
			pc := placed[f.Index]
			at = float64(pc.From) / r.fps
			if f.Edge == "end" {
				at = float64(pc.From+pc.DurationInFrames)/r.fps - f.Seconds
			}
		}
		var fix *models.QAFix
		if i < len(cf.FlashFixes) && len(cf.FlashFixes[i]) > 0 {
			summary := "Start the clip at the switch"
			if f.Edge == "end" {
				summary = "End the clip just before the switch"
			}
			if f.Words {
				summary += ", with its sound running on so no words are lost"
			}
			fix = &models.QAFix{Summary: summary + " (Trim the flashes does every one at once).", Ops: cf.FlashFixes[i]}
		} else {
			fix = &models.QAFix{Summary: "The clip is locked: unlock it to trim the flash."}
		}
		r.add(&models.QAFinding{Severity: sev, Kind: "edge_flash", Basis: models.QAHeuristic, From: at, To: at + f.Seconds, ClipIDs: []string{f.ClipID},
			Message: fmt.Sprintf("%s %s.", upperFirst(shotWho(r.in, f.Index, f.ClipID)), editspec.FlashWords(f)), Fix: fix})
	}
	if cf.NameBar != nil && cf.PrintedClips > 0 {
		var ops []editspec.Op
		for _, id := range cf.NameBar.Removed {
			ops = append(ops, editspec.Op{Op: "removeOverlay", ID: id})
		}
		o := cf.NameBar.Overlay
		ops = append(ops, editspec.Op{Op: "addOverlay", Overlay: &o})
		r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "printed_names", Basis: models.QAHeuristic, From: 0, To: r.total(),
			Message: fmt.Sprintf("Names printed by the call app show at the %s in %s.", strings.ReplaceAll(cf.Corner, "-", " "), count(cf.PrintedClips, "clip")),
			Fix:     &models.QAFix{Summary: "Cover them with your own name bar for the whole video (Cover the printed names).", Ops: ops}})
	}
	for _, m := range cf.Mismatches {
		fix := &models.QAFix{Summary: "Write the name bar's names again from who is on screen (Cover the printed names)."}
		if len(cf.Rename) > 0 {
			fix.Ops = cf.Rename
		}
		r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "name_bar_mismatch", Basis: models.QAHeuristic, From: m.At, To: m.To, ClipIDs: []string{m.ClipID},
			Message: fmt.Sprintf("Around %s the name bar says %s while %s is on screen.", clock(m.At), quote([]string{m.Shows}), m.OnScreen), Fix: fix})
	}
	r.check(name, models.QAHeuristic, "picture switches inside clips (found to the frame): a sliver of at most 0.75 s at a clip's start or end; names printed by a call app; the name bar against who is on screen (at least 0.5 s)", "edge_flash", "printed_names", "name_bar_mismatch")
}

// shotWho is a clip in words for QA messages ("Lena's clip").
func shotWho(in Input, i int, id string) string {
	for _, sh := range in.Shots {
		if sh.Index == i && sh.Who != "" {
			return sh.Who
		}
	}
	return "clip " + strconv.Itoa(i+1)
}

// projectRules measures the project's rules where the quality check can.
func (r *run) projectRules() {
	const name = "project_rules"
	var rules []models.ProjectRule
	for _, rule := range r.in.Rules {
		if !rule.Promise { // promises are measured on their own (promises.go, D300)
			rules = append(rules, rule)
		}
	}
	if len(rules) == 0 {
		r.skip(name, models.QAHeuristic, "the project has no rules")
		return
	}
	var byEye []string
	for _, rule := range rules {
		switch m := rule.Measure; {
		case m == "captions-top" || m == "captions-bottom":
			want := strings.TrimPrefix(m, "captions-")
			c := r.in.Spec.Captions
			if c == nil || !c.Enabled {
				continue
			}
			pos := c.Position
			if pos == "" {
				pos = "lower"
			}
			if (want == "top" && pos == "top") || (want == "bottom" && (pos == "bottom" || pos == "lower")) {
				continue
			}
			fix := &models.QAFix{Summary: "Move the captions " + map[string]string{"top": "to the top", "bottom": "to the bottom"}[want] + "."}
			if !c.Locked {
				nc := *c
				nc.Position = want
				fix.Ops = []editspec.Op{{Op: "setCaptions", Captions: &nc}}
			}
			r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "rule_broken", Basis: models.QAHeuristic, From: 0, To: r.total(),
				Message: fmt.Sprintf("%s: the captions are %s.", ruleWords(rule), captionsWhere(pos)), Fix: fix})
		case m == "first-names":
			r.firstNames(rule)
		case m == "no-flashes":
			// The call footage check reports flashes as errors.
		case strings.HasPrefix(m, "max-length:"):
			num, shape, _ := strings.Cut(strings.TrimPrefix(m, "max-length:"), "@")
			v, _ := strconv.ParseFloat(num, 64)
			if shape != "" && orientationWord(r.plan.Width, r.plan.Height) != shape {
				continue // the rule is for videos of another shape
			}
			if v > 0 && r.total() > v+0.5 {
				r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "rule_broken", Basis: models.QAObserved, From: v, To: r.total(),
					Message: fmt.Sprintf("%s: the video is %s long.", ruleWords(rule), clock(r.total())),
					Fix:     &models.QAFix{Summary: fmt.Sprintf("Shorten it by about %s.", seconds(r.total()-v))}})
			}
		case strings.HasPrefix(m, "shape:"):
			want := strings.TrimPrefix(m, "shape:")
			got := orientationWord(r.plan.Width, r.plan.Height)
			if got != want {
				preset := map[string]string{"landscape": editspec.PresetLandscape, "vertical": editspec.PresetVertical, "square": editspec.PresetSquare}[want]
				out := r.in.Spec.Output
				out.Preset, out.Width, out.Height = preset, editspec.Presets[preset][0], editspec.Presets[preset][1]
				r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "rule_broken", Basis: models.QAObserved, From: 0, To: r.total(),
					Message: fmt.Sprintf("%s: the video is %s.", ruleWords(rule), shapeWords(got)),
					Fix:     &models.QAFix{Summary: "Make it " + shapeWords(want) + ".", Ops: []editspec.Op{{Op: "setOutput", Output: &out}}}})
			}
		default:
			// The measures promises use (D300), for a rule that is only a rule:
			// what breaks it is worth checking, not a problem.
			_, res, ok := r.measureRule(rule)
			switch {
			case !ok || res.status == models.PromiseUnmeasured:
				byEye = append(byEye, rule.Text)
			case res.status == models.PromiseBroken:
				r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "rule_broken", Basis: res.basis, From: res.from, To: math.Max(res.from, res.to),
					ClipIDs: res.clipIDs, OverlayIDs: res.overlays, Message: ruleWords(rule) + ": " + lowerFirst(res.measured), Fix: res.fix})
			}
		}
	}
	if len(byEye) > 0 {
		r.add(&models.QAFinding{Severity: models.QAInfo, Kind: "rule_by_eye", Basis: models.QAHeuristic, From: 0, To: r.total(),
			Message: fmt.Sprintf("Look for these rules of the project yourself: %s.", strings.Join(quoteEach(byEye), "; "))})
	}
	r.check(name, models.QAHeuristic, "the project's rules MetaRush can measure: captions at the top or bottom, first names only, no flashes, a longest length, the shape, and the measures promises use (captions on and readable, a logo in a window, voices over the music, AI disclosed, faces in the picture, size, frame rate)", "rule_broken", "rule_by_eye")
}

// ruleWords names a rule in a finding: the project's own, or a house rule of
// the settings it uses (D193).
func ruleWords(rule models.ProjectRule) string {
	if rule.Promise {
		return promiseWords(rule)
	}
	if rule.House {
		if rule.HouseOf != "" {
			return "The house rule " + quote([]string{rule.Text}) + " (from " + quote([]string{rule.HouseOf}) + "'s settings)"
		}
		return "The house rule " + quote([]string{rule.Text})
	}
	return "The project's rule " + quote([]string{rule.Text})
}

func quoteEach(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = quote([]string{x})
	}
	return out
}

func orientationWord(w, h int) string {
	switch r := float64(w) / float64(max(1, h)); {
	case r > 1.15:
		return "landscape"
	case r < 0.87:
		return "vertical"
	}
	return "square"
}

func shapeWords(o string) string {
	return map[string]string{"landscape": "widescreen", "vertical": "vertical", "square": "square"}[o]
}

// firstNames finds names on screen with more than one word (a name title or a
// name bar's names) under the rule "first names only".
func (r *run) firstNames(rule models.ProjectRule) {
	first := func(n string) string {
		f := strings.Fields(n)
		if len(f) == 0 {
			return n
		}
		return strings.TrimRight(f[0], ",.;:")
	}
	for _, o := range r.in.Spec.Overlays {
		var long []string
		var set map[string]any
		switch o.Template {
		case editspec.TplLowerThirdClean, editspec.TplLowerThirdCinematic, editspec.TplNameBar:
		default:
			continue
		}
		var p struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(o.Params, &p)
		params := map[string]any{}
		_ = json.Unmarshal(o.Params, &params)
		if len(strings.Fields(p.Name)) > 1 {
			long = append(long, p.Name)
			params["name"] = first(p.Name)
			set = map[string]any{"params": params}
		}
		var changes []editspec.OverlayChange
		changed := false
		for _, c := range o.Changes {
			var cp map[string]any
			_ = json.Unmarshal(c.Params, &cp)
			if n, ok := cp["name"].(string); ok && len(strings.Fields(n)) > 1 {
				long = append(long, n)
				cp["name"] = first(n)
				changed = true
			}
			b, _ := json.Marshal(cp)
			changes = append(changes, editspec.OverlayChange{Footage: c.Footage, At: c.At, Params: b})
		}
		if changed {
			if set == nil {
				set = map[string]any{}
			}
			set["changes"] = changes
		}
		if len(long) == 0 {
			continue
		}
		fix := &models.QAFix{Summary: "Use first names only."}
		if !o.Locked {
			b, _ := json.Marshal(set)
			fix.Ops = []editspec.Op{{Op: "updateOverlay", ID: o.ID, Set: b}}
		}
		r.add(&models.QAFinding{Severity: models.QAWarning, Kind: "rule_broken", Basis: models.QAHeuristic, From: o.At, To: o.At + o.Duration, OverlayIDs: []string{o.ID},
			Message: fmt.Sprintf("%s: %s shows %s.", ruleWords(rule), graphicNameOf(o), quote(dedupe(long))), Fix: fix})
	}
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// graphicNameOf is a spec graphic in words.
func graphicNameOf(o editspec.Overlay) string {
	if o.Template == editspec.TplNameBar {
		return "the name bar"
	}
	return "the name title " + o.ID
}

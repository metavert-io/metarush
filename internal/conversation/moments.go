package conversation

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// Outputs (D103): the episode is the conversation's automatic cut; best
// moments are found here and made into shorts. The finder only proposes —
// complete stretches that start cleanly, with why each might work (a
// question and its answer, people laughing together, a claim or a number in
// the first sentence) — and Claude Code chooses with the person (skills).
// Each short is a new project: a vertical cut of its own (the conversation's
// stretch cut again for the shape), or the episode's stretch nested as it is.

func init() {
	ops.Def("conversation.moments", "Find a conversation's best moments for shorts: complete stretches that start cleanly, each with why it might work (a question and its answer, laughing together, a strong first line); Claude chooses with the person", ops.Flags{},
		func(ctx context.Context, a *app.App, in *CallMomentsIn) (*CallMomentsOut, error) {
			return moments(ctx, a, in)
		})
	ops.Def("conversation.short", "Make a short from a stretch of a conversation: a new project with its own cut (vertical by default, captions with names, the picture following who talks) or the episode's stretch nested as it is", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CallShortIn) (*CallShortOut, error) {
			return short(ctx, a, in)
		})
}

// CallMomentsIn finds moments.
type CallMomentsIn struct {
	CallRefIn
	// Min and Max are the moments' length (seconds; default 15–60).
	Min   float64 `json:"min,omitempty" jsonschema:"minimum=3,maximum=600"`
	Max   float64 `json:"max,omitempty" jsonschema:"minimum=5,maximum=900"`
	Count int     `json:"count,omitempty" jsonschema:"minimum=1,maximum=30,description=How many (default 6)."`
}

// CallMomentsOut lists moments, best first.
type CallMomentsOut struct {
	Conversation string       `json:"conversation"`
	Moments      []CallMoment `json:"moments"`
	Summary      string       `json:"summary"`
}

// CallMoment is one candidate.
type CallMoment struct {
	From  float64  `json:"from" jsonschema:"description=On the conversation's clock (seconds)."`
	To    float64  `json:"to"`
	When  string   `json:"when"`
	Who   []string `json:"who"`
	First string   `json:"first" jsonschema:"description=Its first sentence (the hook)."`
	Text  string   `json:"text" jsonschema:"description=Everything said in it."`
	Why   []string `json:"why"`
	Score float64  `json:"score"`
}

var (
	claimWords = regexp.MustCompile(`\b(trick|secret|mistake|surprised|surprising|never|always|most people|everyone|nobody|best|worst|first time|the truth|the problem|the key|actually|years|\d+)\b`)
	laughWords = regexp.MustCompile(`\b(ha|haha|hahaha|lol|laughs?|laughing)\b`)
)

func moments(ctx context.Context, a *app.App, in *CallMomentsIn) (*CallMomentsOut, error) {
	c, err := load(ctx, a, in.Conversation, in.ProjectID)
	if err != nil {
		return nil, err
	}
	v, _, err := callView(ctx, a, c)
	if err != nil {
		return nil, err
	}
	lo, hi := in.Min, in.Max
	if lo <= 0 {
		lo = 15
	}
	if hi <= 0 {
		hi = 60
	}
	if hi < lo {
		return nil, invalid("max must be at least min")
	}
	n := in.Count
	if n <= 0 {
		n = 6
	}
	ms := findMoments(v, c.Cleanup, lo, hi)
	if len(ms) > n {
		ms = ms[:n]
	}
	out := &CallMomentsOut{Conversation: c.Name, Moments: ms}
	switch len(ms) {
	case 0:
		out.Summary = fmt.Sprintf("No complete stretch of %s–%s in %s.", secs(lo), secs(hi), c.Name)
	default:
		out.Summary = fmt.Sprintf("%d moments in %s that could stand on their own, best first.", len(ms), c.Name)
	}
	return out, nil
}

// findMoments scores every complete stretch and keeps the best that don't overlap.
func findMoments(v *editspec.ConversationView, cleanup []models.CleanupSuggestion, lo, hi float64) []CallMoment {
	ss := sentences(v.Words)
	if len(ss) == 0 {
		return nil
	}
	// Stretches to stay out of: call talk and long silences.
	var avoid []editspec.Span
	for _, s := range cleanup {
		if (s.Kind == KindCallTalk || s.Kind == KindDeadAir) && s.State != "skipped" {
			avoid = append(avoid, editspec.Span{From: s.From, To: s.To})
		}
	}
	for _, s := range callTalk(v.Words) {
		avoid = append(avoid, editspec.Span{From: s.From, To: s.To})
	}
	// Who talks at once (laughing together, crosstalk).
	var acts [][]editspec.Span
	for _, sp := range v.Activity {
		acts = append(acts, sp)
	}
	overlapAt := func(a, b float64) float64 {
		t := 0.0
		for i := range acts {
			for j := i + 1; j < len(acts); j++ {
				for _, x := range acts[i] {
					for _, y := range acts[j] {
						f, e := math.Max(math.Max(x.From, y.From), a), math.Min(math.Min(x.To, y.To), b)
						if e > f {
							t += e - f
						}
					}
				}
			}
		}
		return t
	}
	var cands []CallMoment
	for i := range ss {
		// A clean start: the first sentence of a turn, or after a pause.
		if i > 0 && ss[i].from-ss[i-1].to < 0.3 && ss[i-1].speaker == ss[i].speaker {
			continue
		}
		if spanNear(avoid, ss[i].from, ss[i].to) {
			continue
		}
		for j := i; j < len(ss); j++ {
			if spanNear(avoid, ss[j].from, ss[j].to) || (j > i && ss[j].from-ss[j-1].to >= 2) {
				break // call talk, or a long pause: the moment is over
			}
			length := ss[j].to - ss[i].from
			if length > hi {
				break
			}
			if length < lo {
				continue
			}
			// A clean end: the turn ends or a pause follows.
			end := j+1 == len(ss) || ss[j+1].speaker != ss[j].speaker || ss[j+1].from-ss[j].to >= 0.6
			if !end {
				continue
			}
			cands = append(cands, scoreMoment(ss[i:j+1], overlapAt, lo, hi))
		}
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].Score != cands[j].Score {
			return cands[i].Score > cands[j].Score
		}
		return cands[i].From < cands[j].From
	})
	var out []CallMoment
	for _, c := range cands {
		ok := true
		for _, o := range out {
			inter := math.Min(c.To, o.To) - math.Max(c.From, o.From)
			if inter > 0.3*math.Min(c.To-c.From, o.To-o.From) {
				ok = false
				break
			}
		}
		if ok {
			out = append(out, c)
		}
	}
	return out
}

func scoreMoment(ss []sentence, overlapAt func(a, b float64) float64, lo, hi float64) CallMoment {
	from, to := math.Max(0, ss[0].from-0.15), ss[len(ss)-1].to+0.3
	m := CallMoment{From: round3(from), To: round3(to), When: editspec.Clock(from) + "–" + editspec.Clock(to), First: ss[0].text}
	var texts []string
	seen := map[string]bool{}
	words := 0
	for _, s := range ss {
		texts = append(texts, s.text)
		words += s.words
		if !seen[s.speaker] && s.speaker != "" {
			seen[s.speaker] = true
			m.Who = append(m.Who, s.speaker)
		}
	}
	m.Text = strings.Join(texts, " ")
	score := 0.0
	first := plainText(ss[0].text)
	if strings.HasSuffix(strings.TrimSpace(ss[0].text), "?") && len(ss) > 1 && ss[1].speaker != ss[0].speaker {
		score += 2
		m.Why = append(m.Why, "a question and its answer")
	}
	if laughWords.MatchString(plainText(m.Text)) || overlapAt(from, to) >= 0.5 {
		score += 1.5
		m.Why = append(m.Why, "they laugh or talk together")
	}
	if claimWords.MatchString(first) {
		score += 1
		m.Why = append(m.Why, "a strong first line")
	}
	if len(m.Who) >= 2 {
		score += 0.5
		m.Why = append(m.Why, "both sides of the conversation")
	}
	rate := float64(words) / math.Max(1, to-from)
	if rate >= 2.2 && rate <= 3.8 {
		score += 0.5
	}
	// Lengths in the middle of the range read best.
	mid := (lo + hi) / 2
	score += 0.5 * (1 - math.Abs((to-from)-mid)/math.Max(1, hi-lo))
	m.Score = math.Round(score*100) / 100
	if len(m.Why) == 0 {
		m.Why = []string{"a complete stretch that starts and ends cleanly"}
	}
	return m
}

// ---- shorts ----

// CallShortIn makes a short.
type CallShortIn struct {
	CallRefIn
	From float64 `json:"from" jsonschema:"minimum=0,description=On the conversation's clock (seconds)."`
	To   float64 `json:"to" jsonschema:"minimum=0"`
	// Name is the new project's name (default: the conversation's name and the first words).
	Name  string `json:"name,omitempty" jsonschema:"maxLength=120"`
	Shape string `json:"shape,omitempty" jsonschema:"enum=vertical,enum=landscape,enum=square,description=Default vertical."`
	Style string `json:"style,omitempty" jsonschema:"enum=podcast,enum=interview,enum=panel,enum=reactions,enum=broll"`
	// Nest uses the episode's own stretch as it is (same shape as the episode)
	// instead of cutting the conversation again.
	Nest     bool  `json:"nest,omitempty"`
	Captions *bool `json:"captions,omitempty" jsonschema:"description=Default on."`
	// Apply makes the project and its cut; without it the answer says what it would be.
	Apply bool `json:"apply,omitempty"`
	// Origin is where the short's version says it came from (versions' origin;
	// default none).
	Origin string `json:"origin,omitempty" jsonschema:"maxLength=200"`
}

// CallShortOut is the short.
type CallShortOut struct {
	CallPlanOut
	ProjectID string `json:"projectId,omitempty"`
	Project   string `json:"project,omitempty" jsonschema:"description=The new project's name."`
}

// PreviewShort is what a short would be (its project's name and stretch), as a
// card shows it before a person confirms it; nothing is made.
func PreviewShort(ctx context.Context, a *app.App, in CallShortIn) (*CallShortOut, error) {
	in.Apply = false
	return short(ctx, a, &in)
}

func short(ctx context.Context, a *app.App, in *CallShortIn) (*CallShortOut, error) {
	c, err := load(ctx, a, in.Conversation, in.ProjectID)
	if err != nil {
		return nil, err
	}
	if in.To-in.From < 2 {
		return nil, invalid("a short needs at least 2 seconds (from and to on the conversation's clock)")
	}
	if in.To > c.Length+0.5 {
		return nil, invalid("the conversation is only %s long", secs(c.Length))
	}
	src := first(c.ProjectIDs)
	shape := in.Shape
	if shape == "" {
		shape = "vertical"
		if in.Nest {
			shape = ""
		}
	}
	v, _, _ := callView(ctx, a, c)
	firstWords := ""
	if v != nil {
		for _, s := range sentences(v.Words) {
			if s.to > in.From+0.2 && s.from < in.To {
				firstWords = s.text
				break
			}
		}
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = c.Name + " — short"
		if firstWords != "" {
			name = c.Name + ": " + strings.TrimRight(quoteWords(firstWords, 6), ".,!?") + "…"
		}
	}
	if len([]rune(name)) > 120 {
		name = string([]rune(name)[:119]) + "…"
	}
	what := fmt.Sprintf("a %s short of %s (%s–%s, %s)", map[string]string{"vertical": "vertical", "landscape": "16:9", "square": "square", "": "16:9"}[shape], c.Name, editspec.Clock(in.From), editspec.Clock(in.To), secs(in.To-in.From))
	if in.Nest {
		what = fmt.Sprintf("a short of %s that plays the episode's own %s–%s", c.Name, editspec.Clock(in.From), editspec.Clock(in.To))
	}
	out := &CallShortOut{Project: name}
	out.Changes = []string{"a new project, " + name, what}
	if !in.Apply {
		out.Summary = "Ready: " + what + ", as a new project called " + name + ". Apply it to make it."
		return out, nil
	}
	// The new project, using the conversation's project (footage, and its cut for nesting).
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: name}, ops.Caller{})
	if err != nil {
		return nil, err
	}
	out.ProjectID = p.Project.ID
	if src != "" {
		if _, err := ops.CallTyped[ops.ProjectLinkOut](ctx, a, "project.link", ops.ProjectLinkIn{ProjectID: p.Project.ID, From: src}, ops.Caller{}); err != nil {
			return nil, err
		}
	}
	if in.Nest {
		po, err := nestShort(ctx, a, c, p.Project.ID, src, in)
		if err != nil {
			return nil, err
		}
		out.CallPlanOut = *po
	} else {
		captions := true
		if in.Captions != nil {
			captions = *in.Captions
		}
		from, to := in.From, in.To
		po, err := autoCut(ctx, a, &CallCutIn{CallRefIn: CallRefIn{Conversation: c.ID}, Project: p.Project.ID, Style: in.Style, Shape: shape, From: &from, To: &to, Captions: &captions, Apply: true, Origin: in.Origin})
		if err != nil {
			return nil, err
		}
		out.CallPlanOut = *po
	}
	if !containsStr(c.ProjectIDs, p.Project.ID) {
		if fresh, err := load(ctx, a, c.ID, ""); err == nil {
			fresh.ProjectIDs = append(fresh.ProjectIDs, p.Project.ID)
			_ = save(ctx, a, fresh)
		}
	}
	out.Changes = []string{"a new project, " + name, what}
	if out.Applied != nil && out.Applied.Problem == "" {
		out.Summary = fmt.Sprintf("Made %s: %s.", name, what)
	} else {
		out.Summary = fmt.Sprintf("Made the project %s, but its cut isn't in yet: %s", name, out.Summary)
	}
	return out, nil
}

// nestShort is a cut with one clip: the episode's own stretch (the
// conversation's [from, to) found in the episode's timeline).
func nestShort(ctx context.Context, a *app.App, c *models.Conversation, pid, src string, in *CallShortIn) (*CallPlanOut, error) {
	rev, err := latest(ctx, a, src, "")
	if err != nil {
		return nil, err
	}
	if rev == nil {
		return nil, invalid("the episode has no cut yet to take the stretch from")
	}
	s := editspec.Clone(&rev.Spec)
	editspec.Normalize(s)
	clips, _ := editspec.Timeline(s)
	fps := float64(s.Output.FPS)
	t0, t1 := -1.0, -1.0
	for i, cl := range s.Clips {
		pin, ok := s.Conversations[editspec.ConvName(cl.AssetID)]
		if !ok || pin.ID != c.ID || i >= len(clips) {
			continue
		}
		pc := clips[i]
		rate := pc.PlaybackRate
		if rate <= 0 {
			rate = 1
		}
		at := func(t float64) float64 { return float64(pc.From)/fps + (t-pc.SourceIn)/rate }
		if t0 < 0 && in.From < cl.Out && in.To > cl.In {
			t0 = at(math.Max(in.From, cl.In))
		}
		if in.From < cl.Out && in.To > cl.In {
			t1 = at(math.Min(in.To, cl.Out))
		}
	}
	if t0 < 0 || t1-t0 < 1 {
		return nil, invalid("the episode doesn't play %s–%s of the conversation", editspec.Clock(in.From), editspec.Clock(in.To))
	}
	spec := &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Title: c.Name + " (short)", Output: s.Output,
		Clips: []editspec.Clip{{ID: "moment", AssetID: editspec.NestedKey(src, rev.ID), In: round3(t0), Out: round3(t1), Rationale: "The episode's own stretch, as it is."}}}
	if in.Captions == nil || *in.Captions {
		spec.Captions = &editspec.Captions{Enabled: true, ShowSpeakers: true, Style: editspec.CaptionClean, Position: "lower"}
	}
	editspec.Normalize(spec)
	plan := editspec.Plan{ProjectID: pid, Instruction: "A short from " + c.Name, Spec: spec, Origin: in.Origin}
	res, err := checkPlan(ctx, a, plan)
	if err != nil {
		return nil, err
	}
	po := &CallPlanOut{Result: res}
	po.Applied = applyResult(ctx, a, res)
	po.Summary = settle("Ready: the episode's stretch.", po.Applied)
	return po, nil
}

// quoteWords is the first n words.
func quoteWords(s string, n int) string {
	f := strings.Fields(s)
	if len(f) > n {
		f = f[:n]
	}
	return strings.Join(f, " ")
}

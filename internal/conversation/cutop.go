package conversation

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/visual"
)

func init() {
	ops.Def("conversation.cut", "Make a cut of a conversation automatically: whoever is talking, switches in pauses (the voice leading the picture), listening reactions and the split layout when people talk together, in a style (podcast, off-camera interviewer, panel, reaction-heavy, talking head and b-roll), each choice with its reason (checks the plan; apply makes it a new version)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CallCutIn) (*CallPlanOut, error) {
			return autoCutWith(ctx, a, in, true)
		})
	ops.Def("conversation.angle", "Change what a conversation clip shows from a moment on: one person, a layout (side by side, stacked, picture in picture, grid), a text card or a cutaway to other footage (checks the plan; apply makes it a new version)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CallAngleIn) (*CallPlanOut, error) { return angle(ctx, a, in) })
	ops.Def("conversation.styles", "List the styles an automatic conversation cut can have and the layouts it can use", ops.Flags{},
		func(ctx context.Context, a *app.App, in *CallStylesIn) (*CallStylesOut, error) { return styles(), nil })
}

// CallCutIn makes an automatic cut of a conversation.
type CallCutIn struct {
	CallRefIn
	Project string `json:"project,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$,description=The project whose cut it becomes (default: the conversation's)."`
	Branch  string `json:"branch,omitempty" jsonschema:"pattern=^[a-z][a-z0-9-]*$"`
	Style   string `json:"style,omitempty" jsonschema:"enum=podcast,enum=interview,enum=panel,enum=reactions,enum=broll,description=Default podcast (or the conversation's last style)."`
	Shape   string `json:"shape,omitempty" jsonschema:"enum=landscape,enum=vertical,enum=square,description=Default landscape (16:9)."`
	// From and To limit it to a stretch of the conversation (its clock, seconds).
	From *float64 `json:"from,omitempty" jsonschema:"minimum=0"`
	To   *float64 `json:"to,omitempty" jsonschema:"minimum=0"`
	// Interviewer is the off-camera interviewer (interview style; default: who asks the questions).
	Interviewer string `json:"interviewer,omitempty"`
	// Questions: audio (heard over the guest, default) or cards (text cards; the interviewer's voice left out).
	Questions string `json:"questions,omitempty" jsonschema:"enum=audio,enum=cards"`
	// BRoll is footage for cutaways (broll style): IDs, titles or file names.
	BRoll    []string `json:"broll,omitempty" jsonschema:"maxItems=50"`
	Layout   string   `json:"layout,omitempty" jsonschema:"enum=side-by-side,enum=stacked,enum=pip,enum=grid,description=The layout when people talk together (default: side by side on landscape\\, stacked on vertical\\, grid for three or more)."`
	Captions *bool    `json:"captions,omitempty" jsonschema:"description=Captions with each person's name (default: on for vertical\\, off otherwise)."`
	NameTags *bool    `json:"nameTags,omitempty" jsonschema:"description=Name tags the first time each person is seen (default on)."`
	Apply    bool     `json:"apply,omitempty"`
	// Origin is where the new version says it came from (versions' origin;
	// default none).
	Origin string `json:"origin,omitempty" jsonschema:"maxLength=200"`
}

// CallStylesIn has no fields.
type CallStylesIn struct{}

// CallStylesOut lists the styles and layouts.
type CallStylesOut struct {
	Styles  []CallStyle  `json:"styles"`
	Layouts []CallLayout `json:"layouts"`
}

// CallLayout describes a layout.
type CallLayout struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Min   int    `json:"min"`
	Max   int    `json:"max"`
	About string `json:"about"`
}

func styles() *CallStylesOut {
	out := &CallStylesOut{}
	for _, n := range []string{"podcast", "interview", "panel", "reactions", "broll"} {
		out.Styles = append(out.Styles, Styles[n])
	}
	about := map[string]string{
		editspec.LayoutSideBySide: "Two or three people in equal columns (landscape).",
		editspec.LayoutStacked:    "Two or three people one above the other (vertical).",
		editspec.LayoutPiP:        "One person full frame, the other in a corner.",
		editspec.LayoutGrid:       "Everyone in the most even grid (panels).",
	}
	for _, l := range editspec.Layouts {
		lo, hi := editspec.LayoutCount(l)
		out.Layouts = append(out.Layouts, CallLayout{ID: l, Label: editspec.LayoutLabel(l), Min: lo, Max: hi, About: about[l]})
	}
	return out
}

// convName is the short name a cut gives the conversation.
const convName = "call"

// shapeOf is the output preset and platform for a shape.
func shapeOf(shape string) (string, string, error) {
	switch shape {
	case "", "landscape":
		return editspec.PresetLandscape, "youtube", nil
	case "vertical":
		return editspec.PresetVertical, "shorts", nil
	case "square":
		return editspec.PresetSquare, "generic", nil
	}
	return "", "", invalid("shape is landscape, vertical or square, not %q", shape)
}

// splitLayout is the layout for people talking together on a shape.
func splitLayout(preset string, people int) string {
	switch {
	case people > 3:
		return editspec.LayoutGrid
	case preset == editspec.PresetVertical:
		return editspec.LayoutStacked
	}
	return editspec.LayoutSideBySide
}

// pinFor is the conversation as a new cut pins it: its alignment and
// members, with the gate on and every recording placed to the sample (D119).
func pinFor(c *models.Conversation) editspec.ConversationPin {
	p := c.Pin()
	p.Gate = editspec.NewGate()
	p.SampleAligned = true
	return p
}

// prepared is a conversation resolved for the automatic cut.
type prepared struct {
	c     *models.Conversation
	pin   editspec.ConversationPin
	spec  *editspec.Spec
	facts editspec.Facts
	view  *editspec.ConversationView
	names map[string]string
}

func prepare(ctx context.Context, a *app.App, c *models.Conversation, projectID string, out editspec.Output) (*prepared, error) {
	pin := pinFor(c)
	spec := &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Output: out, Conversations: map[string]editspec.ConversationPin{convName: pin},
		Clips: []editspec.Clip{{ID: "talk", AssetID: editspec.ConvKey(convName), In: 0, Out: math.Max(1, c.Length)}}}
	editspec.Normalize(spec)
	facts, err := ops.LoadFacts(ctx, a, projectID, spec)
	if err != nil {
		return nil, err
	}
	v := editspec.Conversation(spec, facts, convName)
	if v == nil {
		return nil, invalid("the conversation's recordings couldn't be read")
	}
	names := map[string]string{}
	missing := []string{}
	for _, m := range pin.Members {
		names[m.ID] = m.Name
		if m.HasSound() {
			if af, ok := facts.Asset(m.AssetID); ok && af.HasAudio && af.TranscriptPrecision == "" {
				missing = append(missing, m.Name)
			}
		}
	}
	if len(missing) > 0 {
		return nil, apperr.New(apperr.InvalidInput, "%s recording has no transcript yet, so MetaRush can't tell when they talk.", possessive(joinAnd(missing))).
			WithHint("Wait until its words are written out (the Sources tab shows when), then try again.").WithCommand("metarush footage analyze")
	}
	return &prepared{c: c, pin: pin, spec: spec, facts: facts, view: v, names: names}, nil
}

// PreviewCut is the automatic cut a card shows before a person confirms it
// (the Claude Code panel): the checked plan, the shots and their reasons, with
// nothing saved — not even the style the conversation remembers.
func PreviewCut(ctx context.Context, a *app.App, in CallCutIn) (*CallPlanOut, error) {
	in.Apply = false
	return autoCutWith(ctx, a, &in, false)
}

func autoCut(ctx context.Context, a *app.App, in *CallCutIn) (*CallPlanOut, error) {
	return autoCutWith(ctx, a, in, true)
}

// autoCutWith makes the automatic cut; remember keeps the style and layout on
// the conversation for its next cut.
func autoCutWith(ctx context.Context, a *app.App, in *CallCutIn, remember bool) (*CallPlanOut, error) {
	c, err := load(ctx, a, in.Conversation, in.ProjectID)
	if err != nil {
		return nil, err
	}
	pid := in.Project
	if pid == "" {
		pid = first(c.ProjectIDs)
	}
	styleName := in.Style
	if styleName == "" {
		styleName = c.Style
	}
	if styleName == "" {
		styleName = "podcast"
	}
	st, ok := Styles[styleName]
	if !ok {
		return nil, invalid("style is %s, not %q", strings.Join(styleNames(), ", "), styleName)
	}
	preset, platform, err := shapeOf(in.Shape)
	if err != nil {
		return nil, err
	}
	fps := projectFPS(ctx, a, pid)
	out := editspec.Output{Preset: preset, FPS: fps, Platform: platform, Framing: editspec.FramingFaces}
	pr, err := prepare(ctx, a, c, pid, out)
	if err != nil {
		return nil, err
	}
	from, to := 0.0, pr.view.Length
	if in.From != nil {
		from = math.Max(0, *in.From)
	}
	if in.To != nil {
		to = math.Min(to, *in.To)
	}
	if to-from < 1 {
		return nil, invalid("that stretch is shorter than a second")
	}
	people := len(pr.pin.PictureMembers())
	layout := in.Layout
	if layout == "" {
		layout = splitLayout(preset, people)
	}
	ci := &cutInput{pin: pr.pin, view: pr.view, from: from, to: to, style: st, layout: layout, names: pr.names}
	if st.Interviewer {
		ci.interviewer, err = interviewer(pr, in.Interviewer)
		if err != nil {
			return nil, err
		}
		ci.cards = in.Questions == "cards"
	}
	if st.BRoll {
		if len(in.BRoll) == 0 {
			return nil, invalid("the cutaways style needs footage to cut away to")
		}
		if ci.broll, err = brollSources(ctx, a, pid, in.BRoll); err != nil {
			return nil, err
		}
	}
	var clips []plannedClip
	if ci.cards {
		clips = questionCards(ci, turns(ci))
	} else {
		clips = []plannedClip{{in: from, out: to}}
	}
	guest := ""
	for _, id := range pr.pin.PictureMembers() {
		if id != ci.interviewer {
			guest = id
			break
		}
	}
	spec := &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Title: c.Name, Output: out, Conversations: map[string]editspec.ConversationPin{convName: pr.pin}}
	for i, pc := range clips {
		id := "talk"
		if i > 0 {
			id = fmt.Sprintf("talk-%d", i+1)
		}
		clip := editspec.Clip{ID: id, AssetID: editspec.ConvKey(convName), In: round3(pc.in), Out: round3(pc.out), MutedMembers: pc.muted, Rationale: pc.why}
		if len(pc.shots) > 0 {
			clip.Shots = pc.shots
			for k := range clip.Shots {
				if clip.Shots[k].Card != "" && guest != "" {
					clip.Shots[k].Members = []string{guest}
				}
			}
		} else {
			sub := *ci
			sub.from, sub.to = pc.in, pc.out
			clip.Shots = autoShots(&sub)
		}
		if i > 0 {
			clip.Transition = &editspec.Transition{Type: editspec.TransitionCut, Audio: &editspec.SoundTransition{Mode: editspec.SoundAuto}}
		}
		spec.Clips = append(spec.Clips, clip)
	}
	if len(spec.Clips) == 0 {
		return nil, invalid("nothing is said in that stretch")
	}
	captions := preset == editspec.PresetVertical
	if in.Captions != nil {
		captions = *in.Captions
	}
	if captions {
		spec.Captions = &editspec.Captions{Enabled: true, ShowSpeakers: true, Style: editspec.CaptionClean, Position: "lower"}
	}
	if in.NameTags == nil || *in.NameTags {
		spec.Overlays = nameTags(spec, pr)
	}
	spec.Notes = fmt.Sprintf("An automatic %s cut of %s (%s). Each shot says why it is there; change any of them on the timeline or in the cut sheet.", strings.ToLower(st.Label), c.Name, joinAnd(peopleNames(pr)))
	editspec.Normalize(spec)
	rev, err := latest(ctx, a, pid, in.Branch)
	if err != nil {
		return nil, err
	}
	plan := editspec.Plan{ProjectID: pid, Branch: in.Branch, Instruction: fmt.Sprintf("Cut %s automatically (%s)", c.Name, st.Label), Spec: spec, Origin: in.Origin}
	if rev != nil {
		plan.ProjectID, plan.BaseRevisionID, plan.Branch = "", rev.ID, rev.Branch
	}
	res, err := checkPlan(ctx, a, plan)
	if err != nil {
		return nil, err
	}
	shots := 0
	var why []string
	for _, cl := range spec.Clips {
		for _, sh := range cl.Shots {
			shots++
			if sh.Rationale != "" {
				why = append(why, editspec.Clock(sh.At)+" "+sh.Rationale)
			}
		}
	}
	po := &CallPlanOut{Result: res, Why: why, Changes: []string{fmt.Sprintf("%s: %d clips and %d shots, %s long", st.Label, len(spec.Clips), shots, secs(totalLength(spec)))}}
	po.Summary = fmt.Sprintf("Ready: an automatic %s cut of %s with %d shots, each with its reason.", strings.ToLower(st.Label), c.Name, shots)
	if in.Apply {
		po.Applied = applyResult(ctx, a, res)
		po.Summary = settle(po.Summary, po.Applied)
	}
	if remember && (c.Style != styleName || c.Layout != layout || c.MaxShot != st.MaxShot || c.Interviewer != ci.interviewer) {
		c.Style, c.Layout, c.MaxShot, c.Interviewer = styleName, layout, st.MaxShot, ci.interviewer
		_ = save(ctx, a, c)
	}
	return po, nil
}

func peopleNames(pr *prepared) []string {
	var out []string
	for _, m := range pr.pin.Members {
		if m.UseOf() != editspec.UsePicture {
			out = append(out, m.Name)
		}
	}
	return out
}

func totalLength(s *editspec.Spec) float64 {
	_, total := editspec.Timeline(s)
	return float64(total) / float64(s.Output.FPS)
}

// interviewer is the off-camera interviewer: the member asked for, else the
// one whose turns most often end with a question (else who talks least).
func interviewer(pr *prepared, ref string) (string, error) {
	if ref != "" {
		for _, m := range pr.pin.Members {
			if m.ID == ref || strings.EqualFold(m.Name, ref) {
				return m.ID, nil
			}
		}
		return "", invalid("there's no member %q", ref)
	}
	qs := map[string]int{}
	talk := map[string]float64{}
	for _, m := range pr.pin.Members {
		if !m.HasSound() {
			continue
		}
		for _, sg := range pr.view.Own[m.ID] {
			talk[m.ID] += sg.To - sg.From
			if strings.HasSuffix(strings.TrimSpace(sg.Text), "?") {
				qs[m.ID]++
			}
		}
	}
	best, bq, bt := "", -1, math.Inf(1)
	for id := range talk {
		if qs[id] > bq || (qs[id] == bq && talk[id] < bt) {
			best, bq, bt = id, qs[id], talk[id]
		}
	}
	if best == "" {
		return "", invalid("nobody speaks in the conversation yet")
	}
	return best, nil
}

// brollSources loads the cutaway footage and the words seen or said in it.
func brollSources(ctx context.Context, a *app.App, projectID string, refs []string) ([]brollSource, error) {
	var out []brollSource
	for _, r := range refs {
		as, err := ops.ResolveFootage(ctx, a, r, projectID)
		if err != nil {
			return nil, err
		}
		if !as.Media.HasVideo {
			return nil, invalid("%s has no picture, so it can't be a cutaway", as.DisplayTitle())
		}
		b := brollSource{id: as.ID, title: as.DisplayTitle(), words: map[string]bool{}, duration: as.Media.Duration}
		for _, w := range strings.FieldsFunc(strings.ToLower(as.DisplayTitle()), func(r rune) bool { return !('a' <= r && r <= 'z') }) {
			b.words[w] = true
		}
		if st := visual.Load(ctx, a.DB, a.WorkspaceID(), as.ID); st != nil && st.Index != nil {
			for _, t := range visual.Terms(st.Index) {
				for _, w := range strings.Fields(strings.ToLower(t)) {
					b.words[w] = true
				}
			}
		}
		out = append(out, b)
	}
	return out, nil
}

// nameTags puts a lower third with each person's name (and their tag) the
// first time they are on screen alone for at least 3 s.
func nameTags(s *editspec.Spec, pr *prepared) []editspec.Overlay {
	var out []editspec.Overlay
	done := map[string]bool{}
	pos := 0.0
	for _, c := range s.Clips {
		for k, sh := range c.Shots {
			end := c.Out
			if k+1 < len(c.Shots) {
				end = math.Min(end, c.Shots[k+1].At)
			}
			start := math.Max(sh.At, c.In)
			if sh.Member == "" || done[sh.Member] || end-start < 3 {
				continue
			}
			m, ok := pr.pin.Member(sh.Member)
			if !ok || !m.HasPicture() {
				continue
			}
			done[sh.Member] = true
			p := map[string]string{"name": m.Name}
			if m.Tag != "" {
				p["title"] = m.Tag
			}
			raw, _ := json.Marshal(p)
			at := pos + (start - c.In) + 0.4
			out = append(out, editspec.Overlay{ID: "name-" + m.ID, Template: editspec.TplLowerThirdClean, At: round3(at), Duration: math.Min(3.5, end-start-0.5),
				Params: raw, Rationale: fmt.Sprintf("Introduce %s the first time they are on screen alone.", m.Name)})
		}
		pos += c.Out - c.In
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// ---- one shot by hand ----

// CallAngleIn changes what a conversation clip shows from a moment on.
type CallAngleIn struct {
	Project string `json:"project" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
	Branch  string `json:"branch,omitempty" jsonschema:"pattern=^[a-z][a-z0-9-]*$"`
	// Clip is the conversation clip (its name or number); At the moment on
	// the conversation's clock (seconds; a shot starting there is replaced).
	Clip string  `json:"clip,omitempty" jsonschema:"description=Default: the conversation clip playing that moment."`
	At   float64 `json:"at" jsonschema:"minimum=0"`
	// One of: Member, Layout (with Members), Card, BRoll (+ BRollIn); or Remove.
	Member   string   `json:"member,omitempty"`
	Layout   string   `json:"layout,omitempty" jsonschema:"enum=side-by-side,enum=stacked,enum=pip,enum=grid"`
	Members  []string `json:"members,omitempty"`
	Card     string   `json:"card,omitempty" jsonschema:"maxLength=240"`
	BRoll    string   `json:"broll,omitempty"`
	BRollIn  float64  `json:"brollIn,omitempty" jsonschema:"minimum=0"`
	Dissolve *float64 `json:"dissolve,omitempty" jsonschema:"minimum=0,maximum=2"`
	Remove   bool     `json:"remove,omitempty" jsonschema:"description=Take out the shot that starts there (the one before plays on)."`
	Why      string   `json:"why,omitempty" jsonschema:"maxLength=1000"`
	Apply    bool     `json:"apply,omitempty"`
}

// PreviewAngle is a change of angle as a card shows it before a person
// confirms it: the checked plan; nothing is saved.
func PreviewAngle(ctx context.Context, a *app.App, in CallAngleIn) (*CallPlanOut, error) {
	in.Apply = false
	return angle(ctx, a, &in)
}

func angle(ctx context.Context, a *app.App, in *CallAngleIn) (*CallPlanOut, error) {
	rev, err := latest(ctx, a, in.Project, in.Branch)
	if err != nil {
		return nil, err
	}
	if rev == nil {
		return nil, invalid("the project has no cut yet")
	}
	s := editspec.Clone(&rev.Spec)
	editspec.Normalize(s)
	ci := -1
	for i, c := range s.Clips {
		if !editspec.IsConversation(c.AssetID) {
			continue
		}
		if in.Clip != "" {
			if c.ID == in.Clip || c.Label == in.Clip || fmt.Sprint(i+1) == in.Clip {
				ci = i
				break
			}
			continue
		}
		if in.At >= c.In && in.At < c.Out {
			ci = i
			break
		}
	}
	if ci < 0 {
		return nil, invalid("no conversation clip plays %s of the conversation (or there's no such clip)", editspec.Clock(in.At))
	}
	c := s.Clips[ci]
	pin := s.Conversations[editspec.ConvName(c.AssetID)]
	shots := append([]editspec.Shot(nil), c.Shots...)
	if len(shots) == 0 {
		shots = []editspec.Shot{{At: c.In, Layout: splitLayout(s.Output.Preset, len(pin.PictureMembers()))}}
	}
	at := round3(in.At)
	idx := -1
	for k, sh := range shots {
		if math.Abs(sh.At-at) < 0.0005 {
			idx = k
		}
	}
	var what string
	if in.Remove {
		if idx < 0 {
			return nil, invalid("no shot starts at %s", editspec.Clock(at))
		}
		shots = append(shots[:idx], shots[idx+1:]...)
		what = "took out the shot at " + editspec.Clock(at)
	} else {
		sh := editspec.Shot{At: at, Rationale: in.Why}
		find := func(ref string) (string, error) {
			for _, m := range pin.Members {
				if m.ID == ref || strings.EqualFold(m.Name, ref) {
					return m.ID, nil
				}
			}
			return "", invalid("there's no member %q", ref)
		}
		switch {
		case in.Member != "":
			if sh.Member, err = find(in.Member); err != nil {
				return nil, err
			}
			m, _ := pin.Member(sh.Member)
			what = m.Name
		case in.Layout != "":
			sh.Layout = in.Layout
			for _, r := range in.Members {
				id, err := find(r)
				if err != nil {
					return nil, err
				}
				sh.Members = append(sh.Members, id)
			}
			what = editspec.LayoutLabel(in.Layout)
		case in.Card != "":
			sh.Card = in.Card
			for _, r := range in.Members {
				id, err := find(r)
				if err != nil {
					return nil, err
				}
				sh.Members = append(sh.Members, id)
			}
			what = "a card"
		case in.BRoll != "":
			as, err := ops.ResolveFootage(ctx, a, in.BRoll, rev.ProjectID)
			if err != nil {
				return nil, err
			}
			sh.BRoll = &editspec.ShotBRoll{AssetID: as.ID, In: round3(in.BRollIn)}
			what = "a cutaway to " + as.DisplayTitle()
		default:
			return nil, invalid("say what to show: a person, a layout, a card or a cutaway")
		}
		if in.Dissolve != nil {
			sh.Dissolve = *in.Dissolve
		}
		if sh.Rationale == "" {
			sh.Rationale = "Chosen by hand."
		}
		if idx >= 0 {
			shots[idx] = sh
		} else {
			shots = append(shots, sh)
			sort.SliceStable(shots, func(i, j int) bool { return shots[i].At < shots[j].At })
		}
		what = what + " from " + editspec.Clock(at)
	}
	raw, _ := json.Marshal(map[string]any{"shots": shots})
	eops := []editspec.Op{{Op: "updateClip", ID: c.ID, Set: raw}}
	res, err := planCheck(ctx, a, rev, "Show "+what, eops)
	if err != nil {
		return nil, err
	}
	out := &CallPlanOut{Result: res, Changes: []string{"clip " + c.ID + ": " + what}, Summary: "Ready: " + what + "."}
	if in.Apply {
		out.Applied = applyResult(ctx, a, res)
		out.Summary = settle(out.Summary, out.Applied)
	}
	return out, nil
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("conversation.cut", ops.Offering{ID: "automatic-call-cut", Group: "edit", Title: "Automatic call cuts",
		Does:  "Cuts a call automatically — whoever is talking, reactions, split layouts — and explains each choice.",
		Words: []string{"edit the podcast", "switch to the speaker"}, Needs: ops.Needs{Works: []string{"editing"}}})
}

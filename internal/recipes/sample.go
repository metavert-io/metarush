package recipes

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/sound"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// A sample first (D294): before a run whose estimate has an expensive kind of
// work — the AI voice, generated shots, voices made clearer in the cloud —
// MetaRush makes one priced sample of each kind, and a person approves it
// before the rest of the run is planned. The sample is a short cut of its own
// in the run's place (its project, a cut named after the run's): the shortest
// line that needs the AI voice, whole (the run reuses it), over the start of
// the video, one generated shot from the run's words, a stretch of the new
// footage made clearer in the cloud — each
// through the same operations a run uses, priced by the services' own estimates
// and held to what the person agreed to for the sample. Its review copy is what
// the person watches and listens to. Approving it plans the rest with today's
// prices (the sample's line and shot are used again, not paid twice) and asks
// for the rest's agreement — by the approving person's own estimates
// preference — as a run that wants to spend more does; "not quite" stops the
// run, with the person's words kept. A person can skip the sample with a
// reason, which is recorded. A kind is expensive when it costs at least
// SampleFloorUSD in the whole run: under that its sample would cost about as
// much as the work itself. A run again samples the kinds whose inputs changed
// (not those a person approved before and that didn't). A sample whose run
// stops or fails ends with it, keeping what it spent. Only people start,
// approve or skip a sample (Auto never does, D204, D241).

// The sample's steps (RecipeStep.ID).
const (
	stepSample  = "sample"
	stepApprove = "approve"
)

// SampleFloorUSD is the least a kind of work costs in a whole run for it to be
// expensive (a sample first); cheaper work is made without one.
var SampleFloorUSD = 0.05

// Sample lengths: a stretch of new footage made clearer and the start of the
// video a sample line plays over (seconds).
const (
	sampleCleanSeconds   = 30.0
	samplePictureSeconds = 20.0
)

// sampleKindOf says which kind of expensive work a run step's spending is.
var sampleKindOf = map[string]string{stepVoice: models.SampleVoice, stepShots: models.SampleShots, stepSound: models.SampleCleanUp, stepPack: models.SampleCleanUp}

// sampleKinds are the kinds in the order a sample makes them.
var sampleKinds = []string{models.SampleShots, models.SampleVoice, models.SampleCleanUp}

// RecipeSamplePlan is what a run's plan says about its sample (D294): the
// expensive kinds of work and the sample of each, what the sample costs, and
// what to agree to before it is made.
type RecipeSamplePlan struct {
	// Required: the run starts with the sample unless a person skips it with a reason.
	Required bool                      `json:"required"`
	Kinds    []models.RecipeSampleKind `json:"kinds"`
	// Estimate is what the sample costs: the services' own parts.
	Estimate costs.Estimate `json:"estimate"`
	// Summary says it plainly ("A sample first: …, about $0.06; …").
	Summary string `json:"summary"`
}

// planSample works out the sample a new run makes first (nil: no expensive
// kind). The parts come from the run's estimate, step by step.
func planSample(ctx context.Context, a *app.App, p *planned, lookIn string) {
	if p.into != nil {
		return // the rest after a sample is the run itself
	}
	// A run again samples only the kinds whose inputs changed since the run it
	// makes again, unless a person approved that run's sample of the kind (or the
	// version it made): what was approved needs no new sample.
	approved := approvedKinds(ctx, a, p.Prev)
	runUSD := map[string]float64{}
	for step, usd := range p.stepUSD {
		if k := sampleKindOf[step]; k != "" {
			runUSD[k] += usd
		}
	}
	sp := &RecipeSamplePlan{Required: true, Kinds: []models.RecipeSampleKind{}, Estimate: costs.Of()}
	total := 0.0
	for _, kind := range sampleKinds {
		usd := costs.Round(runUSD[kind])
		if usd < SampleFloorUSD {
			continue
		}
		if p.Prev != nil && !kindChanged(p, kind) && approved[kind] {
			continue
		}
		k, est, why := sampleOf(ctx, a, p, lookIn, kind)
		if why != "" {
			p.Warnings = append(p.Warnings, why)
			continue
		}
		k.RunUSD = usd
		sp.Kinds = append(sp.Kinds, k)
		sp.Estimate.Add(stepLabels[stepSample], est)
		total += k.USD
	}
	if len(sp.Kinds) == 0 {
		return
	}
	sp.Estimate = sp.Estimate.Total(total)
	var what []string
	for _, k := range sp.Kinds {
		what = append(what, k.What)
	}
	sp.Summary = fmt.Sprintf("A sample first: %s, %s. You watch and listen to it and approve it before the rest of the run is planned (the whole run: %s now).",
		joinAnd(what), sp.Estimate.About, p.Estimate.About)
	p.sample = sp
}

// approvedKinds are the kinds of expensive work a person approved in a run:
// its sample's, or every kind when a person approved the version it made.
func approvedKinds(ctx context.Context, a *app.App, run *models.RecipeRun) map[string]bool {
	out := map[string]bool{}
	if run == nil {
		return out
	}
	if sm := run.Sample; sm != nil && sm.State == models.SampleApproved {
		for _, k := range sm.Kinds {
			out[k.Kind] = true
		}
	}
	if run.Revision != "" {
		var rev models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), run.Revision, &rev) == nil && rev.Approval.State == models.ApprovalApproved {
			for _, k := range sampleKinds {
				out[k] = true
			}
		}
	}
	return out
}

// kindChanged reports whether an input that feeds a kind of work changed in a
// run again: the voice or a narration line's words (the AI voice), a shot's
// words (generated shots), footage (its clean-up).
func kindChanged(p *planned, kind string) bool {
	for _, b := range p.Doc.Bindings {
		pb, ok := ParseBinding(b.To)
		if !ok || !p.Changed[b.Input] {
			continue
		}
		switch {
		case kind == models.SampleVoice && (pb.Kind == models.BindVoice || pb.Kind == models.BindLine),
			kind == models.SampleShots && pb.Kind == models.BindPrompt,
			kind == models.SampleCleanUp && pb.Kind == models.BindClip:
			return true
		}
	}
	if kind == models.SampleCleanUp {
		for _, in := range p.Doc.Inputs {
			if in.Kind == models.RecipeFootage && p.Changed[in.Name] {
				return true
			}
		}
	}
	return false
}

// sampleOf is the sample of one kind of work, priced by the service's own
// estimate; why says plainly when none can be made.
func sampleOf(ctx context.Context, a *app.App, p *planned, lookIn, kind string) (models.RecipeSampleKind, costs.Estimate, string) {
	s := p.Assembly.Spec
	switch kind {
	case models.SampleVoice:
		if s.Voiceover == nil {
			break
		}
		// The shortest line that needs the AI voice, whole: the run reuses it from
		// the cache (its words, voice and settings are the run's), so nothing is
		// paid twice.
		type cand struct {
			i int
			b editspec.Beat
		}
		var cands []cand
		for i, b := range s.Voiceover.Beats {
			if (b.Take != nil && b.Take.Source != models.TakeScratch) || strings.TrimSpace(b.Text) == "" {
				continue
			}
			cands = append(cands, cand{i, b})
		}
		sort.SliceStable(cands, func(x, y int) bool {
			return utf8.RuneCountInString(cands[x].b.Text) < utf8.RuneCountInString(cands[y].b.Text)
		})
		for _, c := range cands {
			i, b, line := c.i, c.b, c.b
			est, err := voiceover.PriceLines(ctx, a, lookIn, []editspec.Beat{line}, voiceover.TTSIn{})
			if err != nil || len(est.Lines) == 0 {
				return models.RecipeSampleKind{}, costs.Estimate{}, "The AI voice can't have a sample line now; skip the sample with a reason, or set ElevenLabs up (Settings → Integrations)."
			}
			if est.CachedLines == len(est.Lines) {
				continue // made before: free, and no sample
			}
			what := "a short sample line in the AI voice"
			if len(est.VoiceNames) > 0 {
				what += " (" + est.VoiceNames[0] + ")"
			}
			return models.RecipeSampleKind{Kind: kind, What: what, Source: fmt.Sprintf("line %d: “%s”", i+1, shorten(line.Text, 80)), Line: b.ID, Text: line.Text,
				USD: est.Cost.USD}, est.Cost, ""
		}
	case models.SampleShots:
		for _, b := range p.Doc.Bindings {
			pb, ok := ParseBinding(b.To)
			if !ok || pb.Kind != models.BindPrompt {
				continue
			}
			est, ok := p.shotCost[pb.Where]
			if !ok {
				continue
			}
			words := ""
			if v := p.Values[b.Input]; v != nil {
				words = v.Text
			}
			return models.RecipeSampleKind{Kind: kind, What: "one generated shot", Source: fmt.Sprintf("the shot “%s” from “%s”", pb.Where, shorten(words, 80)), Clip: pb.Where,
				USD: est.USD}, est, ""
		}
	case models.SampleCleanUp:
		k, est := cleanUpSample(p)
		if k.AssetID != "" {
			return k, est, ""
		}
	}
	return models.RecipeSampleKind{}, costs.Estimate{}, fmt.Sprintf("No sample of %s can be made from this run's inputs; it is made with the rest.", sampleKindWords(kind))
}

// cleanUpSample is a stretch of the run's new footage made clearer in the
// cloud: the first clip of the cut that plays it, up to sampleCleanSeconds.
func cleanUpSample(p *planned) (models.RecipeSampleKind, costs.Estimate) {
	s := p.Assembly.Spec
	fresh, durations := map[string]string{}, map[string]float64{}
	for _, in := range p.Doc.Inputs {
		v := p.Values[in.Name]
		if in.Kind != models.RecipeFootage || v == nil || !v.Changed {
			continue
		}
		for _, it := range v.Items {
			fresh[it.ID], durations[it.ID] = it.Title, it.Duration
		}
	}
	cloud := false
	for _, v := range clearerVoices(s.Mix) {
		cloud = cloud || v.Enhance.Mode == editspec.EnhanceCloud
	}
	if !cloud {
		return models.RecipeSampleKind{}, costs.Estimate{}
	}
	for _, c := range s.Clips {
		title, ok := fresh[c.AssetID]
		if !ok || c.Out-c.In < 3 {
			continue
		}
		to := round3(math.Min(c.Out, c.In+sampleCleanSeconds))
		// What the cloud is sent: the stretch, padded as sound pads it, and a
		// second for the sound a new cut carries past its last clip.
		secs := math.Min(sound.WindowSeconds(c.In, to, durations[c.AssetID])+1, math.Max(durations[c.AssetID], to-c.In))
		credits := sound.CloudCredits(secs)
		est := costs.Of(costs.Part{Service: providers.ElevenLabsTitle, What: "a clearer voice", Model: providers.IsolationModel,
			Source: fmt.Sprintf("%s–%s of “%s”", editspec.Clock(c.In), editspec.Clock(to), title), Amount: credits, Unit: "credits",
			Units: fmt.Sprintf("%s of sound (%s)", seconds(math.Ceil(secs)), costs.Count(credits, "credit", "credits")),
			Rate:  providers.IsolationRate(), USD: providers.IsolationUSD(credits)})
		est.Note(providers.PlanCreditsNote)
		return models.RecipeSampleKind{Kind: models.SampleCleanUp, What: "one clip's clean-up in the cloud", Source: fmt.Sprintf("%s–%s of “%s”", editspec.Clock(c.In), editspec.Clock(to), title),
			AssetID: c.AssetID, From: round3(c.In), To: to, USD: est.USD}, est
	}
	return models.RecipeSampleKind{}, costs.Estimate{}
}

// sampleKindWords names a kind of work for people.
func sampleKindWords(kind string) string {
	switch kind {
	case models.SampleVoice:
		return "the AI voice"
	case models.SampleShots:
		return "the generated shots"
	case models.SampleCleanUp:
		return "the voices made clearer in the cloud"
	}
	return kind
}

// sampleOfKind finds a sample's kind (nil: it doesn't sample that kind).
func sampleOfKind(sm *models.RecipeSample, kind string) *models.RecipeSampleKind {
	if sm == nil {
		return nil
	}
	for i := range sm.Kinds {
		if sm.Kinds[i].Kind == kind {
			return &sm.Kinds[i]
		}
	}
	return nil
}

// sampleBranch is the sample's cut in the run's project: the run's cut's name
// and "sample" ("sample" for a new project's main cut).
func sampleBranch(run *models.RecipeRun) string {
	if run.Branch == "" || run.Branch == "main" {
		return "sample"
	}
	b := run.Branch
	if len(b) > 40-len("-sample") {
		b = strings.TrimRight(b[:40-len("-sample")], "-")
	}
	return b + "-sample"
}

// ---- the sample's steps ----

// sampleStep makes the sample: one generated shot, the sample's own short
// cut, its line in the AI voice, its stretch made clearer in the cloud, and
// its review copy — each within what the person agreed to for the sample.
func (e *engine) sampleStep(ctx context.Context, st *models.RecipeStep) (bool, error) {
	sm := e.run.Sample
	if sm == nil || sm.State == models.SampleSkipped {
		st.State, st.Detail = models.StepSkipped, "No sample: a person skipped it."
		return true, nil
	}
	if st.Work == nil {
		st.Work = map[string]string{}
	}
	// What the sample spent so far is the sample's, on every look (a run that
	// stops partway keeps it on its record and its receipt).
	defer func() { sm.Spent = st.Spent }()
	if sm.Branch == "" {
		sm.Branch = sampleBranch(e.run)
	}
	// One generated shot (the run uses it again: it is the shot).
	if k := sampleOfKind(sm, models.SampleShots); k != nil && st.Work["shot"] != "done" {
		prompt := ""
		for _, b := range e.doc.Bindings {
			if pb, ok := ParseBinding(b.To); ok && pb.Kind == models.BindPrompt && pb.Where == k.Clip && e.values[b.Input] != nil {
				prompt = e.values[b.Input].Text
			}
		}
		if _, made := e.run.Made[k.Clip]; !made {
			ok, err := e.makeShot(ctx, st, k.Clip, prompt)
			if err != nil || !ok {
				if err == nil && e.run.State != models.RunWaiting {
					st.Detail = "Making the sample's generated shot."
				}
				return false, err
			}
		}
		st.Work["shot"] = "done"
	}
	// The sample's own cut.
	if st.Work["cut"] == "" {
		rev, err := e.saveSampleCut(ctx)
		if err != nil {
			return false, err
		}
		st.Work["cut"] = rev.ID
		sm.Revision, sm.RevisionNumber = rev.ID, rev.Number
		st.Made = append(st.Made, rev.ID)
		if err := save(ctx, e.a, e.run); err != nil { // never two sample cuts
			return false, err
		}
	}
	// Its line in the AI voice.
	if sampleOfKind(sm, models.SampleVoice) != nil && st.Work["voice"] != "done" {
		rev, err := e.sampleHead(ctx)
		if err != nil {
			return false, err
		}
		out, done, err := e.makeVoice(ctx, st, rev)
		if err != nil || !done {
			return false, err
		}
		if out != nil {
			sm.Revision, sm.RevisionNumber = out.Applied.RevisionID, out.Applied.Number
			st.Made = append(st.Made, out.Applied.RevisionID)
		}
		st.Work["voice"] = "done"
	}
	// Its stretch of footage made clearer in the cloud.
	if k := sampleOfKind(sm, models.SampleCleanUp); k != nil && st.Work["clean"] != "done" {
		var strength *float64
		for _, v := range clearerVoices(e.doc.Cut.Mix) {
			if v.Enhance.Mode == editspec.EnhanceCloud {
				strength = v.Enhance.Strength
				break
			}
		}
		voice := editspec.VoiceSound{AssetID: k.AssetID, Enhance: &editspec.Enhance{Mode: editspec.EnhanceCloud, Strength: strength}}
		cv, err := e.clearer(ctx, st, sm.Revision, []editspec.VoiceSound{voice}, "sample")
		if cv.revision != "" {
			sm.Revision, sm.RevisionNumber = cv.revision, cv.number
		}
		if err != nil || !cv.done {
			return false, err
		}
		st.Work["clean"] = "done"
	}
	// Its review copy: what the person watches and listens to.
	ready, err := e.reviewCopy(ctx, st, "sample", e.run.ProjectID, sm.Branch, &sm.Render, &sm.QA, &sm.Revision, &sm.RevisionNumber)
	if err != nil || !ready {
		if err == nil {
			st.Detail = "Making the sample's review copy."
		}
		return false, err
	}
	sm.Spent, sm.State = st.Spent, models.SampleWaiting
	st.Detail = fmt.Sprintf("The sample is made: %s.", spentWords(st.Spent))
	return true, nil
}

// endSample ends a sample nobody decided on when its run stops or fails: what
// it spent so far stays on it (the receipt says it), and it can't be approved.
func endSample(run *models.RecipeRun, now time.Time) {
	sm := run.Sample
	if sm == nil || (sm.State != models.SampleMaking && sm.State != models.SampleWaiting) {
		return
	}
	for _, s := range run.Steps {
		if s.ID == stepSample {
			sm.Spent = s.Spent
		}
	}
	sm.State, sm.At = models.SampleStopped, &now
}

// approveStep waits for a person to approve the sample (recipe.sample); the
// rest of the run is planned then.
func (e *engine) approveStep(_ context.Context, st *models.RecipeStep) (bool, error) {
	sm := e.run.Sample
	if sm == nil || sm.State == models.SampleSkipped || sm.State == models.SampleApproved {
		return true, nil
	}
	e.waitFor(st, models.RunWaitsForPerson, "Waiting for you: watch and listen to the sample, then approve it — the rest of the run is planned then — or say what isn't right.")
	return false, nil
}

// sampleHead is the newest version of the sample's cut.
func (e *engine) sampleHead(ctx context.Context) (*models.Revision, error) {
	var p models.Project
	if err := e.a.DB.GetByID(ctx, store.CProjects, e.a.WorkspaceID(), e.run.ProjectID, &p); err != nil {
		return nil, apperr.New(apperr.NotFound, "The project this run makes its video in is gone.")
	}
	head := p.Branches[e.run.Sample.Branch].Head
	if head == "" {
		return nil, apperr.New(apperr.NotFound, "The sample's cut is gone.")
	}
	var rev models.Revision
	if err := e.a.DB.GetByID(ctx, store.CRevisions, e.a.WorkspaceID(), head, &rev); err != nil {
		return nil, err
	}
	return &rev, nil
}

// saveSampleCut saves the sample's own short cut in the run's project.
func (e *engine) saveSampleCut(ctx context.Context) (*ops.RevisionSummary, error) {
	run, sm := e.run, e.run.Sample
	in := assemblyIn{Doc: e.doc, Values: e.values, Changed: e.changed, Made: run.Made, Voice: e.voice, DefaultVoice: e.defaultVoice, Run: runName(run)}
	full := assemble(in).Spec
	spec := sampleSpec(full, sm, run.Made, e.recipe.Title)
	if len(spec.Clips) == 0 {
		return nil, apperr.New(apperr.Conflict, "There is nothing to make a sample of in this run's video.")
	}
	if run.NewProject {
		if err := e.joinFootage(ctx, spec); err != nil {
			return nil, err
		}
	}
	plan := editspec.Plan{ProjectID: run.ProjectID, Branch: sm.Branch, Instruction: "Make the sample for " + runName(run), Origin: "recipe:" + run.ID,
		Note: shorten(fmt.Sprintf("The sample for run %d of “%s”", run.Number, e.recipe.Title), 80), Spec: spec}
	var p models.Project
	if err := e.a.DB.GetByID(ctx, store.CProjects, e.a.WorkspaceID(), run.ProjectID, &p); err != nil {
		return nil, apperr.New(apperr.NotFound, "The project this run makes its video in is gone.")
	}
	if head := p.Branches[sm.Branch].Head; head != "" {
		plan.ProjectID, plan.BaseRevisionID = "", head // made by this run before it was saved: build on it
	}
	saved, err := saveCut(ctx, e.a, plan)
	if err != nil {
		return nil, err
	}
	_, _ = ops.Call(ctx, e.a, "cut.rename", mustJSON(ops.CutRenameIn{ProjectID: run.ProjectID, Branch: sm.Branch, Title: shorten(fmt.Sprintf("Sample — %s, run %d", e.recipe.Title, run.Number), 120)}), ops.Caller{})
	return &saved.Revision, nil
}

// sampleSpec is the sample's cut: the stretch of new footage to make clearer,
// the generated shot, and — for the AI voice — the start of the video (its
// graphics that fit) with the sample line over it.
func sampleSpec(full *editspec.Spec, sm *models.RecipeSample, made map[string]models.RecipeMadeClip, title string) *editspec.Spec {
	s := &editspec.Spec{SchemaVersion: editspec.SchemaVersion, Title: shorten("Sample: "+title, 200), Output: full.Output, Brand: cloneBrand(full.Brand)}
	if k := sampleOfKind(sm, models.SampleCleanUp); k != nil {
		s.Clips = append(s.Clips, editspec.Clip{ID: "sample-sound", AssetID: k.AssetID, In: k.From, Out: k.To, Label: "The sample: a stretch made clearer in the cloud"})
	}
	if k := sampleOfKind(sm, models.SampleShots); k != nil {
		if m, ok := made[k.Clip]; ok && m.AssetID != "" {
			s.Clips = append(s.Clips, editspec.Clip{ID: "sample-shot", AssetID: m.AssetID, In: 0, Out: round3(m.Duration), Label: "The sample: one generated shot"})
		}
	}
	k := sampleOfKind(sm, models.SampleVoice)
	if k == nil || full.Voiceover == nil {
		return s
	}
	var line *editspec.Beat
	for i := range full.Voiceover.Beats {
		if full.Voiceover.Beats[i].ID == k.Line {
			b := full.Voiceover.Beats[i]
			line = &b
		}
	}
	if line == nil {
		return s
	}
	if len(s.Clips) == 0 {
		// The start of the video, its graphics that fit, under the line.
		total := 0.0
		for _, c := range full.Clips {
			if total >= samplePictureSeconds {
				break
			}
			c = cloneClip(c)
			c.Locked = false
			if l := clipSeconds(c); total+l > samplePictureSeconds {
				sp := c.Speed
				if sp <= 0 {
					sp = 1
				}
				c.Out = round3(c.In + (samplePictureSeconds-total)*sp)
			}
			total += clipSeconds(c)
			s.Clips = append(s.Clips, c)
		}
		kept := map[string]bool{}
		for _, c := range s.Clips {
			kept[c.ID] = true
		}
		for _, o := range full.Overlays {
			switch {
			case o.Span != nil && (o.Span.From == "" || kept[o.Span.From]) && (o.Span.To != "" && kept[o.Span.To]):
			case o.Span == nil && o.At+o.Duration <= total+0.001:
			default:
				continue
			}
			o.Locked = false
			s.Overlays = append(s.Overlays, o)
		}
		if len(s.Overlays) > 0 {
			s.Resources = map[string]editspec.ResourcePin{}
			for k, v := range full.Resources {
				s.Resources[k] = v
			}
		}
	}
	line.Text = k.Text
	line.Anchor = editspec.Anchor{Kind: editspec.AnchorClip, ClipID: s.Clips[0].ID, Offset: 0.5}
	line.Take, line.State, line.Locked = nil, "draft", false
	vo := cloneVoiceover(full.Voiceover)
	vo.Beats, vo.Parked, vo.Continuous, vo.Layout, vo.Fitted, vo.Locked = []editspec.Beat{*line}, nil, nil, "", nil, false
	s.Voiceover = vo
	return s
}

// ---- deciding ----

// RecipeSampleIn approves a run's sample, or says it isn't right (people only).
type RecipeSampleIn struct {
	Run string `json:"run" jsonschema:"minLength=1,description=The run (its ID or recipe#number)."`
	// Decision is approve (the rest of the run is planned) or not-quite (the run stops).
	Decision string `json:"decision" jsonschema:"enum=approve,enum=not-quite,description=approve: the rest of the run is planned and asks for its agreement; not-quite: the run stops\\, with your note."`
	Note     string `json:"note,omitempty" jsonschema:"maxLength=500,description=What you thought of it (needed for not-quite)."`
	// MoreUSD agrees to what the rest costs at once, when it is at least that.
	MoreUSD *float64 `json:"moreUsd,omitempty" jsonschema:"minimum=0,description=Agree to spend this much on the rest of the run (US dollars) at once; it goes on when the rest costs no more."`
}

// RecipeSampleOut is the run after the decision.
type RecipeSampleOut struct {
	Run     RecipeRunView `json:"run"`
	Message string        `json:"message"`
	Next    string        `json:"next,omitempty"`
}

func init() {
	ops.Def("recipe.sample", "Approve a run's sample (the rest of the run is planned then and asks for its agreement) or say it isn't right (the run stops, with your note) — people only", ops.Flags{Mutating: true, HumanOnly: true}, decideSample)
}

func decideSample(ctx context.Context, a *app.App, in *RecipeSampleIn) (*RecipeSampleOut, error) {
	decision := strings.TrimSpace(in.Decision)
	note := strings.TrimSpace(in.Note)
	switch decision {
	case "approve":
	case "not-quite":
		if note == "" {
			return nil, apperr.New(apperr.InvalidInput, "Say what isn't right about the sample (it is kept with the run).")
		}
	default:
		return nil, apperr.New(apperr.InvalidInput, "Approve the sample, or say it isn't quite right.")
	}
	ref, err := loadRun(ctx, a, in.Run)
	if err != nil {
		return nil, err
	}
	switch ref.State {
	case models.RunStopped, models.RunFailed, models.RunDone:
		return nil, apperr.New(apperr.Conflict, "%s already ended (%s); its sample can't be decided any more.", upperFirst(runName(ref)), strings.ToLower(stateWords(ref))).
			WithHint("Plan a new run to make a sample again.")
	}
	if ref.Sample == nil || ref.Sample.State != models.SampleWaiting {
		what := "has no sample waiting for you"
		if ref.Sample != nil && ref.Sample.State == models.SampleMaking {
			what = "is still making its sample"
		}
		return nil, apperr.New(apperr.Conflict, "%s %s.", upperFirst(runName(ref)), what)
	}
	var run *models.RecipeRun
	for i := 0; i < 20; i++ {
		if run, err = claim(ctx, a, ref.ID); err != nil {
			return nil, err
		}
		if run != nil {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if run == nil {
		return nil, apperr.New(apperr.Conflict, "%s is busy right now; try again in a moment.", upperFirst(runName(ref)))
	}
	defer release(ctx, a, run)
	if run.Sample == nil || run.Sample.State != models.SampleWaiting || run.State != models.RunWaiting {
		return nil, apperr.New(apperr.Conflict, "%s changed meanwhile; look at it again.", upperFirst(runName(run)))
	}
	now := time.Now().UTC()
	sm := run.Sample
	sm.By, sm.At, sm.Note = &a.Actor, &now, shorten(note, 500)
	out := &RecipeSampleOut{}
	if decision == "not-quite" {
		sm.State = models.SampleNotQuite
		for i := range run.Steps {
			if s := &run.Steps[i]; s.State == models.StepWaiting || s.State == models.StepRunning || s.State == models.StepPending {
				s.State, s.Ended = models.StepSkipped, &now
			}
		}
		run.State, run.Problem, run.Waiting, run.WaitingFor, run.WaitingUSD, run.FinishedAt = models.RunStopped,
			"The sample wasn't right: “"+shorten(note, 200)+"”. What it made stays; plan the run again with other inputs.", "", "", 0, &now
		if err := save(ctx, a, run); err != nil {
			return nil, err
		}
		a.Audit(ctx, "recipe.sample", "recipe", run.RecipeID, run.ProjectID, map[string]any{"run": run.Number, "decision": decision, "spentUsd": sm.Spent.USD})
		closeSampleTodos(ctx, a, run)
		out.Run, out.Message = *runView(ctx, a, run), fmt.Sprintf("%s stopped: the sample wasn't right. What it made stays.", upperFirst(runName(run)))
		out.Next = "Plan the run again with other inputs (another voice, other words)."
		return out, nil
	}
	sm.State = models.SampleApproved
	if err := planRest(ctx, a, run, in.MoreUSD); err != nil {
		return nil, err
	}
	if err := save(ctx, a, run); err != nil {
		return nil, err
	}
	a.Audit(ctx, "recipe.sample", "recipe", run.RecipeID, run.ProjectID, map[string]any{"run": run.Number, "decision": decision, "spentUsd": sm.Spent.USD,
		"restUsd": run.Estimate.USD, "waitingUsd": run.WaitingUSD, "estimateFirst": run.Agreed != nil && !run.Agreed.Skipped})
	closeSampleTodos(ctx, a, run)
	release(ctx, a, run)
	run.Lease = nil
	fresh := advanceNow(ctx, a, run)
	v := runView(ctx, a, fresh)
	out.Run = *v
	switch {
	case fresh.State == models.RunWaiting && fresh.WaitingUSD > 0:
		out.Message = fmt.Sprintf("The sample is approved. The rest of %s is planned: %s — agree to it to go on.", runName(fresh), costs.Money(fresh.WaitingUSD))
		out.Next = "Let the run go on with that amount (Go on on the run's page, or recipe continue)."
	case fresh.State == models.RunWaiting:
		out.Message = "The sample is approved. " + upperFirst(fresh.Waiting)
	default:
		out.Message = fmt.Sprintf("The sample is approved: the rest of %s goes on (%s).", runName(fresh), strings.ToLower(v.StateLabel[:1])+v.StateLabel[1:])
	}
	return out, nil
}

// closeSampleTodos closes the sample's to-do once a person decided.
func closeSampleTodos(ctx context.Context, a *app.App, run *models.RecipeRun) {
	e := &engine{a: a, run: run}
	e.tell(ctx)
}

// planRest plans the rest of a run whose sample a person approved: with the
// inputs the run recorded, in its place, at today's prices (the sample's line
// and shot are used again); its steps follow the sample's, its estimate is the
// run's from now on, and unless the person agreed to at least that much
// already (or their estimates are off, D208) the run waits for their OK to
// spend it — as a run that asks to spend more does (recipe.continue).
func planRest(ctx context.Context, a *app.App, run *models.RecipeRun, moreUSD *float64) error {
	var given []RecipeInputValue
	for _, in := range run.Inputs {
		g := RecipeInputValue{Name: in.Name, Text: in.Text}
		for _, it := range in.Items {
			if it.ID != "" {
				g.Items = append(g.Items, it.ID)
			}
		}
		if in.Kind == models.RecipeVoice && len(in.Items) > 0 {
			g.Text = in.Items[0].ID
		}
		given = append(given, g)
	}
	p, err := makePlan(ctx, a, planIn{Into: run, Repeats: run.Repeats, Inputs: given, Note: run.Note})
	if err != nil {
		return err
	}
	keep := 0
	for i, s := range run.Steps {
		if s.ID == stepApprove {
			keep = i + 1
			run.Steps[i].State = models.StepDone
			now := time.Now().UTC()
			run.Steps[i].Ended, run.Steps[i].Detail = &now, "Approved by "+actorName(a.Actor)+"."
		}
	}
	steps := append([]models.RecipeStep{}, run.Steps[:keep]...)
	for _, s := range p.Steps {
		switch s.ID {
		case stepInputs, stepPlace, stepSample, stepApprove:
			continue
		}
		steps = append(steps, s)
	}
	run.Steps = steps
	rest := p.Estimate
	run.Sample.Rest = &rest
	run.Estimate, run.Fingerprint, run.Outputs = p.Estimate, p.Fingerprint, p.Outputs
	run.State, run.Waiting, run.WaitingFor, run.WaitingUSD = models.RunRunning, "", "", 0
	first := -1
	for i := keep; i < len(run.Steps); i++ {
		first = i
		break
	}
	if len(p.Problems) > 0 {
		words := "Waiting for you: the rest of the run can't be planned yet — " + strings.TrimSuffix(p.Problems[0], ".") + ". Fix it, or stop the run."
		run.State, run.WaitingFor, run.Waiting = models.RunWaiting, models.RunWaitsForPerson, words
		if first >= 0 {
			run.Steps[first].State, run.Steps[first].Detail = models.StepWaiting, words
		}
		return nil
	}
	// Who agrees to the rest is the person approving the sample, by their own
	// "Estimate costs … before edits" (D208), whoever started the run.
	now := time.Now().UTC()
	if run.Agreed == nil {
		run.Agreed = &models.RecipeAgreement{By: a.Actor, At: now, Skipped: true}
	}
	if !prefs.EstimatesFirst(ctx, a, prefs.Asker(a)) {
		run.Agreed.Skipped = true // their estimates are off: the rest goes on within budgets and limits
		return nil
	}
	if run.Agreed.Skipped {
		// The run's starter had estimates off; the approver hasn't: from here on
		// the run is held to what the approver agrees to.
		run.Agreed.Skipped, run.Agreed.UpTo = false, costs.Round(run.Spent.USD)
	}
	if p.Estimate.USD <= 0 {
		return nil // nothing to pay: it goes on
	}
	// What the person agreed to that the sample didn't spend counts toward the rest.
	need := costs.Round(math.Max(0, p.Estimate.USD-math.Max(0, run.Agreed.UpTo-run.Spent.USD)))
	if need <= 0 {
		return nil
	}
	if moreUSD != nil && *moreUSD+0.0005 >= need {
		more := costs.Round(*moreUSD)
		run.Agreed.UpTo = costs.Round(run.Agreed.UpTo + more)
		run.Agreed.More = append(run.Agreed.More, models.RecipeMore{USD: more, By: a.Actor, At: now})
		return nil
	}
	words := fmt.Sprintf("Waiting for your OK: the sample is approved, and the rest of the run costs %s — agree to it to go on.", costs.Money(need))
	run.State, run.WaitingFor, run.Waiting, run.WaitingUSD = models.RunWaiting, models.RunWaitsForPerson, words, need
	if first >= 0 {
		run.Steps[first].State, run.Steps[first].Detail = models.StepWaiting, words
	}
	return nil
}

// actorName is a person's name for a step's words.
func actorName(x models.Actor) string {
	if x.UserName != "" {
		return x.UserName
	}
	return "someone"
}

// sampleWords says where a run's sample stands, for its page and the CLI.
func sampleWords(sm *models.RecipeSample) string {
	if sm == nil {
		return ""
	}
	var what []string
	for _, k := range sm.Kinds {
		what = append(what, k.What)
	}
	switch sm.State {
	case models.SampleSkipped:
		return "No sample: " + actorName(derefActor(sm.By)) + " skipped it — “" + shorten(sm.Skipped, 200) + "”."
	case models.SampleMaking:
		return "Making the sample: " + joinAnd(what) + " (" + sm.Estimate.About + ")."
	case models.SampleWaiting:
		return "The sample is ready: " + joinAnd(what) + ", " + spentWords(sm.Spent) + ". Watch and listen to it, then approve it."
	case models.SampleApproved:
		return "The sample was approved by " + actorName(derefActor(sm.By)) + ": " + joinAnd(what) + ", " + spentWords(sm.Spent) + "."
	case models.SampleNotQuite:
		return "The sample wasn't right (" + actorName(derefActor(sm.By)) + ": “" + shorten(sm.Note, 200) + "”); the run stopped."
	case models.SampleStopped:
		return "The run stopped before anyone decided on the sample: " + joinAnd(what) + ", " + spentWords(sm.Spent) + "."
	}
	return ""
}

func derefActor(x *models.Actor) models.Actor {
	if x == nil {
		return models.Actor{}
	}
	return *x
}

package recipes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/remotion"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// Planning a run (recipe.plan, read-only; D204): the inputs checked and
// recorded, where the video goes, each step with what it reuses and whether
// it asks a person or needs Claude, and one estimate — D208's costs.Estimate:
// a total in dollars that opens to each priced step's parts, the outside
// service, model, source, units and price, each service's own (generated
// shots, the AI voice, clearer voices) added under the step's label — with a
// fingerprint, so what starts is exactly what the person saw.

// Run steps (RecipeStep.ID).
const (
	stepInputs   = "inputs"
	stepPlace    = "place"
	stepCode     = "code"
	stepShots    = "shots"
	stepAssemble = "assemble"
	stepVoice    = "voice"
	stepClaude   = "claude"
	stepReview   = "review"
	stepFinish   = "finish"
)

// planned is a run worked out from a recipe and what it is given.
type planned struct {
	Recipe  *models.Recipe
	Version *models.RecipeVersion
	Doc     *models.RecipeDoc
	Prev    *models.RecipeRun
	Inputs  []models.RecipeRunInput
	Values  map[string]*models.RecipeRunInput
	Changed map[string]bool
	// Project is where the video goes (nil: a new project, ProjectName).
	Project     *models.Project
	ProjectName string
	Branch      string
	Base        *models.Revision // the cut a run again builds on
	Number      int              // the run's number, as planned
	Settings    *models.RecipeRunSettings
	Voice       *editspec.VoiceRef
	// DefaultVoice is the voice the lines that follow the voice input read in now.
	DefaultVoice string
	// Code are Remotion clips to draw again (by the recipe's clip names);
	// Shots generated shots to make from new words (clip → the request).
	Code     map[string]*codeClip
	Shots    map[string]generate.GenShotIn
	Assembly *assembly
	// Outputs are the pack's shorts (D214) with their first picks (D215);
	// packKept: a run again keeps the shorts of the run it repeats.
	Outputs  []models.RecipeRunOutput
	packKept bool
	Steps    []models.RecipeStep
	// Estimate is the run's (D208); stepUSD each priced step's total, the
	// services' own (what each step is held to while the run goes).
	Estimate costs.Estimate
	stepUSD  map[string]float64
	// voiceWords says what the AI voice reads ("2 lines, 25 characters in the
	// voice “Bram”"), voiceFree that every line was made before.
	voiceWords string
	voiceFree  bool
	// Budget says plainly what the budgets and limits allow.
	Budget   []string
	Changes  []string
	Reused   []string
	Problems []string
	Warnings []string
	Note     string
	// Ask: the person agrees to the estimate before it starts — their
	// "Estimate costs of outside AI services before edits" (prefs.EstimatesFirst).
	Ask         bool
	Fingerprint string
	// sample is the sample a new run makes first (D294; nil none); into the run
	// whose rest is planned after its sample was approved; shotCost each
	// generated shot's estimate, by the recipe's clip.
	sample *RecipeSamplePlan
	// sound: the run makes voices clearer (soundDetail says how), priced first.
	sound       bool
	soundDetail string
	into        *models.RecipeRun
	shotCost    map[string]costs.Estimate
}

// planIn is what a plan is made from.
type planIn struct {
	Recipe  string
	Version string
	Inputs  []RecipeInputValue
	Repeats string
	Note    string
	// Into plans the rest of a run whose sample a person approved (D294): its
	// recipe version, place and number (and, for a run again, Repeats is the run
	// it makes again).
	Into *models.RecipeRun
}

// makePlan works a run out. A plan that can't start says why in Problems.
func makePlan(ctx context.Context, a *app.App, in planIn) (*planned, error) {
	p := &planned{Values: map[string]*models.RecipeRunInput{}, Changed: map[string]bool{}, Note: strings.TrimSpace(in.Note), Estimate: costs.Of(), stepUSD: map[string]float64{},
		shotCost: map[string]costs.Estimate{}, into: in.Into}
	if in.Into != nil {
		in.Recipe, in.Version = in.Into.RecipeID, in.Into.Version
	}
	if in.Repeats != "" {
		prev, err := loadRun(ctx, a, in.Repeats)
		if err != nil {
			return nil, err
		}
		p.Prev = prev
		if in.Recipe == "" {
			in.Recipe = prev.RecipeID
		}
	}
	r, err := loadRecipe(ctx, a, in.Recipe)
	if err != nil {
		return nil, err
	}
	p.Recipe = r
	if p.Prev != nil && p.Prev.RecipeID != r.ID {
		return nil, apperr.New(apperr.InvalidInput, "That run was made with another recipe.")
	}
	if r.Archived {
		p.Problems = append(p.Problems, fmt.Sprintf("“%s” is archived; restore it to run it.", r.Title))
	}
	v, err := loadVersion(ctx, a, r, in.Version)
	if err != nil {
		return nil, err
	}
	if in.Version == "" && r.Current == "" && p.into == nil {
		p.Problems = append(p.Problems, fmt.Sprintf("“%s” has no current version yet: a person makes a version current first.", r.Title))
	}
	p.Version, p.Doc = v, Clone(&v.Doc)
	d := p.Doc
	p.Number = r.Runs + 1
	if p.into != nil {
		p.Number = p.into.Number
	}
	// Where the video goes.
	lookIn := d.Settings
	switch {
	case p.Prev != nil:
		if p.Prev.ProjectID == "" || p.Prev.Branch == "" {
			return nil, apperr.New(apperr.Conflict, "That run never made a cut, so there is nothing to make again; start a new run instead.")
		}
		proj, err := requireProject(ctx, a, p.Prev.ProjectID, "of that run")
		if err != nil {
			p.Problems = append(p.Problems, apperr.From(err).Message)
		} else {
			p.Project, p.Branch, lookIn = proj, p.Prev.Branch, proj.ID
			if head := proj.Branches[p.Branch].Head; head != "" {
				var rev models.Revision
				if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &rev); err == nil {
					p.Base = &rev
				}
			}
			if p.Base == nil {
				if sm := p.Prev.Sample; sm != nil && sm.State != models.SampleApproved && sm.State != models.SampleSkipped {
					p.Problems = append(p.Problems, "That run stopped at its sample, before it made its cut: start a new run instead.")
				} else {
					p.Problems = append(p.Problems, "The cut that run made is gone.")
				}
			}
		}
	case p.into != nil:
		// The rest of a run after its sample (D294): where the run makes its video.
		proj, err := requireProject(ctx, a, p.into.ProjectID, "of this run")
		if err != nil {
			p.Problems = append(p.Problems, apperr.From(err).Message)
		} else {
			p.Project, p.Branch, lookIn = proj, p.into.Branch, proj.ID
		}
	case d.Lands.Kind == models.RecipeLandsProject:
		proj, err := requireProject(ctx, a, d.Lands.ProjectID, "this recipe adds its cuts to")
		if err != nil {
			p.Problems = append(p.Problems, apperr.From(err).Message)
		} else {
			p.Project, lookIn = proj, proj.ID
			p.Branch = freeBranch(proj, r.Name, p.Number)
		}
	default:
		p.ProjectName = newProjectName(d, p.Number)
		if lookIn == "" && d.From != nil {
			lookIn = d.From.ProjectID
		}
	}
	if d.Settings != "" && p.Project == nil {
		if _, err := requireProject(ctx, a, d.Settings, "whose Content Settings the recipe follows"); err != nil {
			p.Warnings = append(p.Warnings, apperr.From(err).Message+" New projects use the default Content Settings.")
		}
	}
	// The inputs.
	rs, err := newResolver(ctx, a, d, lookIn, p.Prev)
	if err != nil {
		return nil, err
	}
	inputs, problems := rs.resolveAll(in.Inputs)
	p.Problems = append(p.Problems, problems...)
	p.Inputs = inputs
	for i := range p.Inputs {
		x := &p.Inputs[i]
		p.Values[x.Name] = x
		p.Changed[x.Name] = x.Changed
	}
	// The voice.
	for _, x := range d.Inputs {
		if x.Kind != models.RecipeVoice {
			continue
		}
		if v := p.Values[x.Name]; v != nil && len(v.Items) > 0 {
			p.Voice = &editspec.VoiceRef{Provider: providers.ElevenLabsName, VoiceID: v.Items[0].ID, Name: v.Items[0].Title}
		}
		if len(x.Default.Items) > 0 {
			p.DefaultVoice = x.Default.Items[0]
		}
		if p.Prev != nil {
			for _, pv := range p.Prev.Inputs {
				if pv.Name == x.Name && len(pv.Items) > 0 {
					p.DefaultVoice = pv.Items[0].ID
				}
			}
		}
	}
	// The check, as on every save and plan.
	chk := checkDoc(ctx, a, d)
	for _, is := range chk.Problems {
		p.Problems = append(p.Problems, is.Message)
	}
	p.Settings = runSettings(ctx, a, p, lookIn)
	planCode(ctx, a, p)
	planShots(ctx, a, p, lookIn)
	// What the cut will be (with stand-ins where Claude chooses).
	ain := assemblyIn{Doc: d, Values: p.Values, Changed: p.Changed, Voice: p.Voice, DefaultVoice: p.DefaultVoice, Run: fmt.Sprintf("run %d of “%s”", p.Number, r.Title),
		Moments: momentsOf(ctx, a, d, p.Values, p.Changed)}
	if p.Base != nil {
		ain.Base = &p.Base.Spec
	}
	p.Assembly = assemble(ain)
	p.Warnings = append(p.Warnings, p.Assembly.Notes...)
	planVoice(ctx, a, p, lookIn)
	planPack(ctx, a, p)
	p.sound, p.soundDetail = planSound(p)
	planSample(ctx, a, p, lookIn) // D294: after every priced step, from their parts
	p.Steps = planSteps(p)
	// The total is the services' own totals (what each step is held to); the
	// parts can differ from it by a fraction of a cent.
	total := 0.0
	for _, usd := range p.stepUSD {
		total += usd
	}
	p.Estimate = p.Estimate.Total(total)
	p.Changes, p.Reused = planWords(p)
	p.Ask = prefs.EstimatesFirst(ctx, a, prefs.Asker(a))
	p.Fingerprint = fingerprint(p)
	return p, nil
}

// freeBranch is a new cut's name in a project: the recipe's short name and the run's number.
func freeBranch(p *models.Project, name string, n int) string {
	base := strings.Trim(branchCleaner.ReplaceAllString(name, "-"), "-")
	if base == "" || base[0] < 'a' || base[0] > 'z' {
		base = "recipe"
	}
	for i := 0; ; i++ {
		suffix := fmt.Sprintf("-%d", n+i)
		b := base
		if len(b)+len(suffix) > 40 {
			b = strings.TrimRight(b[:40-len(suffix)], "-")
		}
		if _, taken := p.Branches[b+suffix]; !taken {
			return b + suffix
		}
	}
}

var branchCleaner = regexp.MustCompile(`[^a-z0-9-]+`)

// newProjectName names a run's new project: the recipe's own pattern (where
// {title}, {n}, {date} and {input} — an input's words — may appear), else the
// title and the run's number.
func newProjectName(d *models.RecipeDoc, n int) string {
	name := strings.TrimSpace(d.Lands.Name)
	if name == "" {
		return shorten(fmt.Sprintf("%s %d", d.Title, n), 120)
	}
	name = strings.ReplaceAll(name, "{title}", d.Title)
	name = strings.ReplaceAll(name, "{n}", fmt.Sprint(n))
	name = strings.ReplaceAll(name, "{date}", time.Now().Format("2 Jan 2006"))
	return shorten(name, 120)
}

// nameWithInputs fills a new project's name with its inputs' words.
func nameWithInputs(name string, values map[string]*models.RecipeRunInput) string {
	for k, v := range values {
		if v.Text != "" {
			name = strings.ReplaceAll(name, "{"+k+"}", v.Text)
		}
	}
	return shorten(name, 120)
}

// runSettings are the Content Settings a run will follow, as they are now.
func runSettings(ctx context.Context, a *app.App, p *planned, lookIn string) *models.RecipeRunSettings {
	owner := lookIn
	if p.Project == nil {
		owner = p.Doc.Settings
	}
	rec, who, err := content.For(ctx, a, owner)
	if err != nil {
		return nil
	}
	out := &models.RecipeRunSettings{Owner: who}
	if who != "" {
		var op models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), who, &op) == nil {
			out.OwnerName = op.Name
		}
	}
	if vc, ok := rec.Voice[providers.ElevenLabsName]; ok {
		out.VoiceID, out.VoiceName = vc.VoiceID, vc.VoiceName
	}
	if k := rec.BrandKit; k != nil {
		var parts []string
		if n := content.LogoCount(k); n > 0 {
			parts = append(parts, plural(n, "logo"))
		}
		if k.Primary != "" {
			parts = append(parts, "colours")
		}
		if k.Font != "" || k.FontResource != "" {
			parts = append(parts, "a font")
		}
		out.Brand = joinAnd(parts)
	}
	return out
}

// codeClip is a Remotion clip a run draws again with new words and pictures
// (D168, D209): the words by setting key, the pictures by name (the clip's own,
// remotion.use's `pictures`).
type codeClip struct {
	Words    map[string]string               `json:"words,omitempty"`
	Pictures map[string]editspec.ResourcePin `json:"pictures,omitempty"`
}

// codeWhat says what a run gives clips made from code: "the new words",
// "the new pictures", "the new words and pictures".
func codeWhat(todo map[string]*codeClip) string {
	words, pics := false, false
	for _, cc := range todo {
		words = words || len(cc.Words) > 0
		pics = pics || len(cc.Pictures) > 0
	}
	switch {
	case words && pics:
		return "the new words and pictures"
	case pics:
		return "the new pictures"
	}
	return "the new words"
}

// codeClips are the Remotion clips whose words or pictures a run changes. A
// pictures input fills the picture names bound to it in the order of the
// bindings (graphics' pictures and clips' alike); names left over keep the
// picture the clip draws.
func codeClips(d *models.RecipeDoc, values map[string]*models.RecipeRunInput, changed map[string]bool) map[string]*codeClip {
	out := map[string]*codeClip{}
	slots := pictureSlots(d)
	for i, b := range d.Bindings {
		pb, ok := ParseBinding(b.To)
		if !ok || (pb.Kind != models.BindWords && pb.Kind != models.BindPictures) || !changed[b.Input] || values[b.Input] == nil {
			continue
		}
		v := values[b.Input]
		var pin *editspec.ResourcePin
		if pb.Kind == models.BindPictures {
			k := slots[i]
			if k >= len(v.Items) || (v.Items[k].ID == "" && !pendingDrive(v.Items[k])) {
				continue
			}
			pin = &editspec.ResourcePin{ID: v.Items[k].ID, SHA256: v.Items[k].SHA256}
		}
		cc := out[pb.Where]
		if cc == nil {
			cc = &codeClip{Words: map[string]string{}, Pictures: map[string]editspec.ResourcePin{}}
			out[pb.Where] = cc
		}
		if pin != nil {
			cc.Pictures[pb.Field] = *pin
		} else {
			cc.Words[pb.Field] = v.Text
		}
	}
	return out
}

// planCode works out the Remotion clips to draw with new words and pictures:
// the same approved version, no new code to approve. A clip's pictures need
// its version's pictures to be settings, and each picture must be one the
// Remotion project's own project can use.
func planCode(ctx context.Context, a *app.App, p *planned) {
	p.Code = codeClips(p.Doc, p.Values, p.Changed)
	for _, id := range sortedKeys(p.Code) {
		cc := p.Code[id]
		cl, _ := clipByID(p.Doc, id)
		if cl == nil {
			delete(p.Code, id)
			continue
		}
		st, err := remotion.SettingsOfClip(ctx, a, cl.AssetID)
		switch {
		case err != nil || st == nil:
			p.Problems = append(p.Problems, fmt.Sprintf("The clip “%s” isn't made from a Remotion project any more, so its words and pictures can't change.", id))
			continue
		case !st.Approved:
			p.Problems = append(p.Problems, fmt.Sprintf("Version %d of “%s”, which draws the clip “%s”, isn't approved any more.", st.VersionNumber, st.SourceTitle, id))
			continue
		}
		if len(cc.Pictures) == 0 {
			continue
		}
		if !st.PicturesAreSettings {
			p.Problems = append(p.Problems, picturesNotSettings(st, id))
			continue
		}
		view, err := projectlinks.Load(ctx, a, st.ProjectID)
		if err != nil {
			p.Problems = append(p.Problems, "MetaRush couldn't read what “"+st.SourceTitle+"”'s project can use.")
			continue
		}
		for _, name := range sortedKeys(cc.Pictures) {
			if !drawsPicture(st, name) {
				p.Problems = append(p.Problems, fmt.Sprintf("“%s” in the clip “%s” doesn't draw a picture called %s.", st.Composition, id, name))
				continue
			}
			if cc.Pictures[name].ID == "" {
				continue // still in Google Drive: the run keeps it in the workspace library, which every project sees (D220)
			}
			if _, err := resources.Find(ctx, a, st.ProjectID, view, cc.Pictures[name].ID); err != nil {
				where := "its project"
				var sp models.Project
				if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), st.ProjectID, &sp) == nil {
					where = "its project, “" + sp.Name + "”,"
				}
				p.Problems = append(p.Problems, fmt.Sprintf("The new picture for %s in the clip “%s” isn't one “%s” can use: %s can't see it. Put the picture in the workspace library or in that project.", name, id, st.SourceTitle, where))
			}
		}
	}
}

// picturesNotSettings says a Remotion project's pictures can't change yet.
func picturesNotSettings(st *remotion.ClipSettings, clip string) string {
	return fmt.Sprintf("The pictures of “%s”, which draws the clip “%s”, aren't settings yet: a person makes them settings once (Sources → “%s” → Pictures), with no change to what it shows.", st.SourceTitle, clip, st.SourceTitle)
}

// drawsPicture reports a clip's composition drawing a picture setting called name.
func drawsPicture(st *remotion.ClipSettings, name string) bool {
	for _, pic := range st.Pictures {
		if pic.Name == name {
			return true
		}
	}
	return false
}

// planShots works out the generated shots to make again from new words, and prices them.
func planShots(ctx context.Context, a *app.App, p *planned, lookIn string) {
	p.Shots = map[string]generate.GenShotIn{}
	for _, b := range p.Doc.Bindings {
		pb, ok := ParseBinding(b.To)
		if !ok || pb.Kind != models.BindPrompt || !p.Changed[b.Input] {
			continue
		}
		cl, _ := clipByID(p.Doc, pb.Where)
		if cl == nil {
			continue
		}
		if p.into != nil {
			if _, made := p.into.Made[cl.ID]; made {
				continue // the sample made it (D294): it is used as it is
			}
		}
		gv, err := ops.CallTyped[generate.GenView](ctx, a, "generate.show", generate.GenIDIn{ID: cl.AssetID}, ops.Caller{})
		if err != nil {
			p.Problems = append(p.Problems, fmt.Sprintf("The clip “%s” wasn't generated by MetaRush, so it can't be made again from new words.", cl.ID))
			continue
		}
		g := gv.Generation
		req := generate.GenShotIn{ProjectID: lookIn, Prompt: p.Values[b.Input].Text, Seconds: g.Params.Seconds, AspectRatio: g.Params.AspectRatio,
			Quality: g.Params.Quality, Resolution: g.Params.Resolution, Model: g.Model, NoPeople: g.NoPeople}
		if g.Look != nil {
			req.Look = g.Look.Name
		}
		p.Shots[cl.ID] = req
		if lookIn == "" {
			p.Warnings = append(p.Warnings, "Generated shots are priced once the run has a project.")
			continue
		}
		est, err := ops.CallTyped[generate.GenEstimate](ctx, a, "generate.estimate", req, ops.Caller{})
		if err != nil {
			p.Problems = append(p.Problems, "The generated shot “"+cl.ID+"” can't be made: "+strings.TrimSuffix(apperr.From(err).Message, ".")+".")
			continue
		}
		if est.Consent.Needed && !est.Consent.OK {
			msg := est.Consent.Message
			if msg == "" {
				msg = "A generated shot shows someone who hasn't given their consent for this project."
			}
			p.Problems = append(p.Problems, msg)
		}
		for _, pr := range est.Problems {
			p.Problems = append(p.Problems, pr)
		}
		p.addEstimate(stepShots, est.Cost)
		p.shotCost[cl.ID] = est.Cost
		if t := est.Budget.Today; t.LeftUSD != nil {
			p.Budget = append(p.Budget, fmt.Sprintf("Generated video: %s left of today's limit.", costs.Money(*t.LeftUSD)))
		}
		if pj := est.Budget.Project; pj != nil && pj.LeftUSD != nil && p.Project != nil {
			p.Budget = append(p.Budget, fmt.Sprintf("Generated video: %s left in this project's budget.", costs.Money(*pj.LeftUSD)))
		}
	}
}

// planVoice prices the narration lines that need an AI voice.
func planVoice(ctx context.Context, a *app.App, p *planned, lookIn string) {
	s := p.Assembly.Spec
	if s.Voiceover == nil {
		return
	}
	need := false
	for _, b := range s.Voiceover.Beats {
		if b.Take == nil || b.Take.Source == models.TakeScratch {
			need = true
		}
	}
	if !need {
		return
	}
	est, err := voiceover.PriceLines(ctx, a, lookIn, s.Voiceover.Beats, voiceover.TTSIn{})
	if err != nil {
		ae := apperr.From(err)
		if ae.Code == apperr.NotConfigured {
			p.Problems = append(p.Problems, "The AI voice needs ElevenLabs, which isn't set up on this computer (Settings → Integrations).")
		} else {
			p.Problems = append(p.Problems, "The AI voice can't be made: "+strings.TrimSuffix(ae.Message, ".")+".")
		}
		return
	}
	if len(est.Lines) == 0 {
		return
	}
	src := fmt.Sprintf("%s, %d characters", plural(len(est.Lines)-est.CachedLines, "line"), est.Characters)
	if est.CachedLines > 0 {
		src += fmt.Sprintf(" (%s made before: free)", plural(est.CachedLines, "line"))
	}
	if len(est.VoiceNames) > 0 {
		src += " in the voice " + joinAnd(quoteAll(est.VoiceNames))
	}
	p.voiceWords, p.voiceFree = src, est.Credits == 0
	p.addEstimate(stepVoice, est.Cost)
	if !est.Allowed {
		p.Problems = append(p.Problems, est.Warnings...)
	} else {
		p.Warnings = append(p.Warnings, est.Warnings...)
	}
	if b := est.Budget; b != nil && b.RemainingCredits != nil {
		p.Budget = append(p.Budget, fmt.Sprintf("AI voice: %s left in the project's budget.", creditWords(*b.RemainingCredits)))
	}
	if est.AccountRemaining != nil {
		p.Budget = append(p.Budget, fmt.Sprintf("Your ElevenLabs plan has %s characters left this period.", providers.Commas(*est.AccountRemaining)))
	}
}

// planSteps lists the run's steps.
func planSteps(p *planned) []models.RecipeStep {
	step := func(id, detail string) models.RecipeStep {
		return models.RecipeStep{ID: id, Label: stepLabels[id], Who: stepWho[id], State: models.StepPending, Detail: detail}
	}
	var out []models.RecipeStep
	inputs := "Records exactly what every input is: each clip as it is now, each picture's version."
	if n := pendingDriveFiles(p.Inputs); n > 0 {
		inputs = fmt.Sprintf("Fetches %s from %s (only what the run uses, through your rclone), then records exactly what every input is.", plural(n, "file"), pendingServices(p.Inputs))
	}
	out = append(out, step(stepInputs, inputs))
	switch {
	case p.into != nil:
		out = append(out, step(stepPlace, fmt.Sprintf("The cut in “%s”.", p.Project.Name)))
	case p.Prev != nil:
		out = append(out, step(stepPlace, fmt.Sprintf("A new version of the cut %s made, in “%s”.", runName(p.Prev), p.Project.Name)))
	case p.Project != nil:
		out = append(out, step(stepPlace, fmt.Sprintf("A new cut in “%s”.", p.Project.Name)))
	default:
		out = append(out, step(stepPlace, fmt.Sprintf("A new project, “%s”.", nameWithInputs(p.ProjectName, p.Values))))
	}
	if sp := p.sample; sp != nil {
		var what []string
		for _, k := range sp.Kinds {
			what = append(what, k.What)
		}
		s := step(stepSample, upperFirst(joinAnd(what))+", in a short cut of its own, with its review copy.")
		s.Estimate = sp.Estimate.USD
		out = append(out, s, step(stepApprove, "You watch and listen to the sample and approve it; then the rest of the run is planned and you agree to its price."))
	}
	if len(p.Code) > 0 {
		out = append(out, step(stepCode, fmt.Sprintf("%s drawn again with %s (no new code to approve).", plural(len(p.Code), "clip"), codeWhat(p.Code))))
	}
	if len(p.Shots) > 0 {
		s := step(stepShots, plural(len(p.Shots), "generated shot")+" from your words.")
		s.Estimate = p.stepUSD[stepShots]
		out = append(out, s)
	}
	out = append(out, step(stepAssemble, "MetaRush builds the sections that stay the same and locks them."))
	if p.voiceWords != "" {
		s := step(stepVoice, p.voiceWords+".")
		s.Estimate = p.stepUSD[stepVoice]
		out = append(out, s)
	}
	if len(p.Assembly.StandIns) > 0 || len(p.Outputs) > 0 {
		var choose, look []string
		seen := map[string]bool{}
		for _, si := range p.Assembly.StandIns {
			if seen[si.Section] {
				continue
			}
			seen[si.Section] = true
			if si.First {
				look = append(look, "“"+si.Section+"”")
			} else {
				choose = append(choose, "“"+si.Section+"”")
			}
		}
		var parts []string
		if len(choose) > 0 {
			parts = append(parts, "chooses the moments of "+joinAnd(choose))
		}
		if len(look) > 0 {
			parts = append(parts, "keeps or changes MetaRush's first picks for "+joinAnd(look))
		}
		if len(p.Outputs) > 0 {
			parts = append(parts, "keeps or changes the shorts' first picks")
		}
		out = append(out, step(stepClaude, "Claude "+joinAnd(parts)+"; the run waits for it."))
	}
	if p.sound {
		s := step(stepSound, p.soundDetail)
		s.Estimate = p.stepUSD[stepSound]
		out = append(out, s)
	}
	out = append(out, step(stepReview, "A review copy of the new version, then the quality check (the recipe's rules measured where they can be)."))
	if len(p.Outputs) > 0 {
		s := step(stepPack, packWords(p.Outputs)+" Each gets a review copy and the quality check.")
		s.Estimate = p.stepUSD[stepPack]
		out = append(out, s)
	}
	finish := "The version waits in Awaiting your review; sign-offs remind before it goes out."
	if len(p.Outputs) > 0 {
		finish = "The version and the shorts wait in Awaiting your review; sign-offs remind before they go out."
	}
	out = append(out, step(stepFinish, finish))
	return out
}

// addEstimate puts a priced step's estimate (a service's own) into the run's,
// under the step's label (D208: "a recipe run adds its steps' estimates").
func (p *planned) addEstimate(step string, e costs.Estimate) {
	if e.Empty() {
		return
	}
	p.Estimate.Add(stepLabels[step], e)
	p.stepUSD[step] = costs.Round(p.stepUSD[step] + e.USD)
}

// planWords says what changes and what is reused, plainly.
func planWords(p *planned) (changes, reused []string) {
	for _, in := range p.Doc.Inputs {
		v := p.Values[in.Name]
		if v == nil {
			continue
		}
		label := upperFirst(inputLabel(in))
		if v.Changed {
			changes = append(changes, label+": "+v.Words)
		} else if p.Prev != nil {
			reused = append(reused, label+" as before")
		}
	}
	if p.voiceWords != "" && !p.voiceFree {
		changes = append(changes, "AI voice: "+p.voiceWords+", "+costs.About(p.stepUSD[stepVoice]))
	}
	for _, sec := range p.Doc.Sections {
		if sec.Kind == models.RecipeChosen && !p.Changed[sec.From] && len(sec.Clips) > 0 {
			if p.Prev != nil {
				reused = append(reused, fmt.Sprintf("The moments chosen for “%s”", sec.Name))
			} else {
				reused = append(reused, fmt.Sprintf("The moments chosen for “%s” when the recipe was saved (the same footage)", sec.Name))
			}
		}
	}
	if len(p.Doc.Cut.Audio) > 0 {
		reused = append(reused, "The music and sounds")
	}
	if len(p.Doc.Cut.Overlays) > 0 {
		reused = append(reused, "The graphics (their words where inputs say)")
	}
	if p.packKept {
		reused = append(reused, "The shorts "+runName(p.Prev)+" made (made again only by a new run)")
	}
	return changes, reused
}

// fingerprint is what the person saw: the version, the inputs as resolved,
// where the video goes, the steps and the estimate.
func fingerprint(p *planned) string {
	type fpInput struct {
		Name  string   `json:"n"`
		Text  string   `json:"t,omitempty"`
		Items []string `json:"i,omitempty"`
	}
	var ins []fpInput
	for _, in := range p.Inputs {
		f := fpInput{Name: in.Name, Text: in.Text}
		for _, it := range in.Items {
			f.Items = append(f.Items, itemKey(it))
		}
		ins = append(ins, f)
	}
	var steps []string
	for _, s := range p.Steps {
		steps = append(steps, s.ID)
	}
	project := p.ProjectName
	if p.Project != nil {
		project = p.Project.ID
	}
	prev := ""
	if p.Prev != nil {
		prev = p.Prev.ID
	}
	fp := map[string]any{"v": p.Version.Hash, "inputs": ins, "project": project, "branch": p.Branch, "steps": steps,
		"usd": p.Estimate.USD, "repeats": prev, "note": p.Note}
	if sp := p.sample; sp != nil {
		var kinds []string
		for _, k := range sp.Kinds {
			kinds = append(kinds, fmt.Sprintf("%s:%s@%.4f", k.Kind, k.Line+k.Clip+k.AssetID, k.USD))
		}
		fp["sample"] = kinds // the sample the person saw (D294)
	}
	if len(p.Outputs) > 0 {
		var pack []string
		for _, o := range p.Outputs {
			pack = append(pack, fmt.Sprintf("%s#%d:%s@%.3f-%.3f", o.Output, o.N, o.Conversation+o.AssetID, o.From, o.To))
		}
		fp["pack"] = pack // the shorts the person saw
	}
	b, _ := json.Marshal(fp)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:32]
}

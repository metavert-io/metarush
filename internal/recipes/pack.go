package recipes

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/conversation"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/sound"
	"github.com/metavert-io/metarush/internal/store"
)

// Packs (D214): besides its main video, a run makes the recipe's pack — shorts
// of a conversation the video plays, each its own project made through
// conversation.short — under the same plan, the same estimate and the same
// agreement. The shorts' stretches are first picks from the conversation's best
// moments (D215) that Claude keeps or changes before they are made; each short
// gets the voices made clearer the way the main video has them (priced first
// when that is in the cloud), a review copy and the quality check, and waits in
// Awaiting your review with the main video. Every short is recorded on the run
// (RecipeRun.Outputs) and its versions carry the run's origin (recipe:<run>).

const stepPack = "pack"

// outputCount is how many videos a pack output makes: at least lo, up to hi
// (default 1–3).
func outputCount(o models.RecipeOutput) (lo, hi int) {
	lo, hi = 1, 3
	if c := o.Count; c != nil {
		if c.Max > 0 {
			hi = int(math.Round(c.Max))
		}
		if c.Min > 0 {
			lo = int(math.Round(c.Min))
		} else {
			lo = 0
		}
		if c.Max <= 0 && lo > hi {
			hi = lo
		}
	}
	if hi < 1 {
		hi = 1
	}
	if hi > 30 {
		hi = 30
	}
	if lo > hi {
		lo = hi
	}
	return lo, hi
}

// outputLength is how long each video of a pack output is (seconds; default 15–60).
func outputLength(o models.RecipeOutput) (lo, hi float64) {
	lo, hi = 15, 60
	if l := o.Length; l != nil {
		if l.Min > 0 {
			lo = l.Min
		}
		if l.Max > 0 {
			hi = l.Max
		}
	}
	if hi < lo {
		hi = lo
	}
	return lo, hi
}

// outputOf finds a pack output of a document by name.
func outputOf(d *models.RecipeDoc, name string) *models.RecipeOutput {
	for i := range d.Pack {
		if d.Pack[i].Name == name {
			return &d.Pack[i]
		}
	}
	return nil
}

// conversationOf is the conversation a pack output takes its shorts from: the
// one it names, else the first one the cut plays.
func conversationOf(s *editspec.Spec, name string) (string, *editspec.ConversationPin) {
	if name != "" {
		if p, ok := s.Conversations[name]; ok {
			return name, &p
		}
		for k, p := range s.Conversations {
			if p.ID == name {
				return k, &p
			}
		}
		return "", nil
	}
	for _, c := range s.Clips {
		if editspec.IsConversation(c.AssetID) {
			if p, ok := s.Conversations[editspec.ConvName(c.AssetID)]; ok {
				return editspec.ConvName(c.AssetID), &p
			}
		}
	}
	return "", nil
}

// planPack works out a run's pack: each output's first picks from the
// conversation's best moments, and what making them costs (clearer voices
// in the cloud, as the main video has them). A run again keeps the shorts the
// run it repeats made.
func planPack(ctx context.Context, a *app.App, p *planned) {
	if len(p.Doc.Pack) == 0 {
		return
	}
	if p.Prev != nil {
		p.packKept = true
		return
	}
	s := p.Assembly.Spec
	for _, o := range p.Doc.Pack {
		if o.Kind != models.RecipeShorts {
			p.Problems = append(p.Problems, fmt.Sprintf("The pack's “%s” is something MetaRush can't make (%q); a pack makes shorts.", o.Name, o.Kind))
			continue
		}
		if o.Footage != "" {
			planFootageShorts(ctx, a, p, o) // D295
			continue
		}
		name, pin := conversationOf(s, o.Conversation)
		if pin == nil {
			if o.Conversation != "" {
				p.Problems = append(p.Problems, fmt.Sprintf("The pack's “%s” makes shorts of the conversation “%s”, which the video doesn't play.", o.Name, o.Conversation))
			} else {
				p.Problems = append(p.Problems, fmt.Sprintf("The pack's “%s” makes shorts of the conversation the video plays, but it plays none.", o.Name))
			}
			continue
		}
		lo, hi := outputCount(o)
		minL, maxL := outputLength(o)
		minL, maxL = math.Max(3, math.Min(minL, 600)), math.Max(5, math.Min(maxL, 900))
		if maxL < minL {
			maxL = minL
		}
		mo, err := ops.CallTyped[conversation.CallMomentsOut](ctx, a, "conversation.moments",
			conversation.CallMomentsIn{CallRefIn: conversation.CallRefIn{Conversation: pin.ID}, Min: minL, Max: maxL, Count: hi}, ops.Caller{})
		if err != nil {
			p.Problems = append(p.Problems, fmt.Sprintf("The shorts of the pack's “%s” can't be picked: %s", o.Name, strings.TrimSuffix(apperr.From(err).Message, ".")+"."))
			continue
		}
		convName := mo.Conversation
		if convName == "" {
			convName = name
		}
		if len(mo.Moments) < lo {
			p.Warnings = append(p.Warnings, fmt.Sprintf("Only %s of “%s” can stand on their own as shorts of %s–%s s; the pack's “%s” wants at least %d. Claude can add others.",
				plural(len(mo.Moments), "moment"), convName, trimSeconds(minL), trimSeconds(maxL), o.Name, lo))
		}
		for k, m := range mo.Moments {
			if k == hi {
				break
			}
			p.Outputs = append(p.Outputs, models.RecipeRunOutput{Output: o.Name, N: k + 1, Kind: o.Kind, Conversation: pin.ID, ConversationName: convName,
				From: m.From, To: m.To, Why: m.Why, First: m.First, Pick: models.PickFirst, State: models.OutputPlanned})
		}
	}
	planPackSound(p)
}

// planFootageShorts picks the shorts of a footage input (D295): the best
// stretches of the clips the run gives it that can stand on their own — whole
// stretches of speech of the shorts' length, scored as first picks are (D215) —
// each made into a new cut of the run's project by footage.short (D246).
func planFootageShorts(ctx context.Context, a *app.App, p *planned, o models.RecipeOutput) {
	in := inputByName(p.Doc)[o.Footage]
	v := p.Values[o.Footage]
	switch {
	case in == nil || in.Kind != models.RecipeFootage:
		p.Problems = append(p.Problems, fmt.Sprintf("The pack's “%s” makes shorts of %q, which isn't one of the recipe's footage inputs.", o.Name, o.Footage))
		return
	case v == nil || len(v.Items) == 0:
		p.Problems = append(p.Problems, fmt.Sprintf("The pack's “%s” makes shorts of %s, which has no clips.", o.Name, inputLabel(*in)))
		return
	}
	lo, hi := outputCount(o)
	minL, maxL := outputLength(o)
	minL, maxL = math.Max(3, math.Min(minL, ops.ShortMaxSeconds)), math.Max(5, math.Min(maxL, ops.ShortMaxSeconds))
	if maxL < minL {
		maxL = minL
	}
	var all []footMoment
	order := map[string]int{}
	titles := map[string]string{}
	for i, it := range v.Items {
		if _, seen := order[it.ID]; seen || it.ID == "" {
			continue
		}
		order[it.ID], titles[it.ID] = i, it.Title
		mo, err := ops.CallTyped[ops.MomentsOut](ctx, a, "footage.moments", ops.MomentsIn{AssetID: it.ID}, ops.Caller{})
		if err != nil {
			continue
		}
		all = append(all, speechOnly(candidates(it.ID, it.Duration, sentencesOf(ctx, a, it.ID, mo.Moments), minL, maxL, (minL+maxL)/2))...)
	}
	picks := pickMoments(all, hi, order)
	if len(picks) < lo {
		p.Warnings = append(p.Warnings, fmt.Sprintf("Only %s of %s can stand on their own as shorts of %s–%s s; the pack's “%s” wants at least %d. Claude can add others.",
			plural(len(picks), "stretch"), inputLabel(*in), trimSeconds(minL), trimSeconds(maxL), o.Name, lo))
	}
	for k, m := range picks {
		p.Outputs = append(p.Outputs, models.RecipeRunOutput{Output: o.Name, N: k + 1, Kind: o.Kind, AssetID: m.AssetID, FootageName: titles[m.AssetID],
			From: m.From, To: m.To, Why: m.Why, First: m.First, Pick: models.PickFirst, State: models.OutputPlanned})
	}
}

// speechOnly keeps the stretches of speech among a clip's candidates (a short
// stands on what is said; movement alone doesn't make one).
func speechOnly(ms []footMoment) []footMoment {
	var out []footMoment
	for _, m := range ms {
		if m.First != "" || m.Text != "" {
			out = append(out, m)
		}
	}
	return out
}

// planPackSound prices the shorts' clearer voices: the voices the main video
// makes clearer, over the shorts' length (an upper bound; on this computer free).
func planPackSound(p *planned) {
	voices := clearerVoices(p.Assembly.Spec.Mix)
	secs := 0.0
	for _, o := range p.Outputs {
		// What making a short's voices clearer processes: its stretch padded the
		// way sound pads it, and a second for the sound at its end (an upper bound).
		secs += sound.WindowSeconds(o.From, o.To, 0) + 1
	}
	if len(voices) == 0 || secs <= 0 {
		return
	}
	cloud := costs.Of()
	for _, v := range voices {
		if v.Enhance.Mode != editspec.EnhanceCloud {
			continue
		}
		who := v.Name
		if who == "" {
			who = "a recording"
		}
		credits := sound.CloudCredits(secs)
		cloud.Append(costs.Part{Service: providers.ElevenLabsTitle, What: "a clearer voice", Model: providers.IsolationModel,
			Source: fmt.Sprintf("%s's voice in up to %s of shorts", who, seconds(math.Ceil(secs))), Amount: credits, Unit: "credits",
			Units: fmt.Sprintf("up to %s of sound (%s)", seconds(math.Ceil(secs)), costs.Count(credits, "credit", "credits")),
			Rate:  providers.IsolationRate(), USD: providers.IsolationUSD(credits)})
	}
	if !cloud.Empty() {
		cloud.Note(providers.PlanCreditsNote)
		p.addEstimate(stepPack, cloud)
	}
}

// packWords says what a run's pack makes, for its step.
func packWords(outs []models.RecipeRunOutput) string {
	byName := map[string]int{}
	var names []string
	ownCuts := 0
	for _, o := range outs {
		n := outputSourceName(o)
		if byName[n] == 0 {
			names = append(names, "“"+n+"”")
		}
		byName[n]++
		if o.AssetID != "" {
			ownCuts++
		}
	}
	var picks []string
	for i, o := range outs {
		if i == 3 {
			picks = append(picks, fmt.Sprintf("%d more", len(outs)-3))
			break
		}
		picks = append(picks, fmt.Sprintf("%s–%s “%s”", editspec.Clock(o.From), editspec.Clock(o.To), shorten(o.First, 40)))
	}
	each := "each its own project"
	switch {
	case ownCuts == len(outs):
		each = "each a new cut of the run's project"
	case ownCuts > 0:
		each = "each its own project or cut"
	}
	return fmt.Sprintf("%s of %s, %s: %s.", plural(len(outs), "short"), joinAnd(names), each, joinAnd(picks))
}

// outputSourceName names what a short comes from: its conversation, or the
// clip of footage it plays (D295).
func outputSourceName(o models.RecipeRunOutput) string {
	if o.AssetID != "" {
		return o.FootageName
	}
	return o.ConversationName
}

// ---- the pack step ----

// shortProjectName names a short's project: the output's pattern, else the
// conversation's name and the short's first words.
func shortProjectName(d *models.RecipeDoc, run *models.RecipeRun, o *models.RecipeRunOutput, out *models.RecipeOutput) string {
	if out == nil || strings.TrimSpace(out.Named) == "" {
		return "" // conversation.short's own
	}
	r := strings.NewReplacer("{title}", d.Title, "{n}", fmt.Sprint(run.Number), "{k}", fmt.Sprint(o.N), "{first}", shorten(o.First, 40))
	return shorten(r.Replace(out.Named), 120)
}

// packStep makes the pack's shorts: each its own project and cut
// (conversation.short), its voices made clearer like the main video's, a review
// copy and the quality check.
func (e *engine) packStep(ctx context.Context, st *models.RecipeStep) (bool, error) {
	if st.Work == nil {
		st.Work = map[string]string{}
	}
	main, err := e.headOf(ctx)
	if err != nil {
		return false, err
	}
	voices := clearerVoices(main.Spec.Mix)
	waiting, made, failed := 0, 0, 0
	for i := range e.run.Outputs {
		o := &e.run.Outputs[i]
		switch {
		case o.Pick == models.PickDropped || o.State == models.OutputDropped:
			o.State = models.OutputDropped
			continue
		case o.State == models.OutputFailed:
			failed++
			continue
		case o.State == models.OutputReady:
			made++
			continue
		}
		out := outputOf(e.doc, o.Output)
		if o.ProjectID == "" && o.AssetID != "" {
			// A short of footage (D295): a new cut of the run's project.
			ok, err := e.footageShort(ctx, st, o, out)
			if err != nil {
				return false, err
			}
			if !ok {
				failed++
				continue
			}
		}
		if o.ProjectID == "" {
			in := conversation.CallShortIn{CallRefIn: conversation.CallRefIn{Conversation: o.Conversation}, From: o.From, To: o.To,
				Name: shortProjectName(e.doc, e.run, o, out), Apply: true, Origin: "recipe:" + e.run.ID}
			if out != nil {
				in.Shape, in.Style, in.Captions = out.Shape, out.Style, out.Captions
			}
			sh, err := ops.CallTyped[conversation.CallShortOut](ctx, e.a, "conversation.short", in, ops.Caller{})
			if err != nil {
				o.State, o.Problem = models.OutputFailed, plainFailure(err)
				failed++
				continue
			}
			o.ProjectID, o.ProjectName, o.Branch = sh.ProjectID, sh.Project, "main"
			if sh.Applied == nil || sh.Applied.RevisionID == "" || sh.Applied.Problem != "" {
				why := "its cut couldn't be made"
				if sh.Applied != nil && sh.Applied.Problem != "" {
					why = strings.TrimSuffix(sh.Applied.Problem, ".")
				}
				o.State, o.Problem = models.OutputFailed, upperFirst(why)+"."
				failed++
				if err := save(ctx, e.a, e.run); err != nil {
					return false, err
				}
				continue
			}
			o.Revision, o.RevisionNumber, o.State = sh.Applied.RevisionID, sh.Applied.Number, models.OutputMaking
			st.Made = append(st.Made, sh.Applied.RevisionID)
			if err := save(ctx, e.a, e.run); err != nil { // never two projects for one short
				return false, err
			}
		}
		// The voices made clearer, as the main video has them.
		key := fmt.Sprintf("short-%d", i)
		if len(voices) > 0 && st.Work[key+":voices"] != "done" {
			cv, err := e.clearer(ctx, st, o.Revision, voices, key)
			if err != nil {
				return false, err
			}
			if cv.revision != "" {
				o.Revision, o.RevisionNumber = cv.revision, cv.number
			}
			if !cv.done {
				return false, nil // waits for a person
			}
			st.Work[key+":voices"] = "done"
		}
		// Its review copy and the quality check.
		ready, err := e.reviewCopy(ctx, st, key, o.ProjectID, o.Branch, &o.Render, &o.QA, &o.Revision, &o.RevisionNumber)
		if err != nil {
			o.State, o.Problem = models.OutputFailed, plainFailure(err)
			failed++
			continue
		}
		if !ready {
			waiting++
			continue
		}
		o.State = models.OutputReady
		made++
	}
	if waiting > 0 {
		st.Detail = fmt.Sprintf("Making %s: %d ready.", plural(made+waiting, "short"), made)
		return false, nil
	}
	switch {
	case made == 0 && failed == 0:
		st.Detail = "No short was made: every one was dropped."
	case made == 0 && failed > 0:
		st.Detail = "No short could be made: " + firstProblem(e.run.Outputs)
	case failed > 0:
		st.Detail = fmt.Sprintf("%s made, each its own project; %d couldn't be: %s", plural(made, "short"), failed, firstProblem(e.run.Outputs))
	default:
		st.Detail = fmt.Sprintf("%s made, each its own project with its review copy: %s.", plural(made, "short"), spentWords(st.Spent))
	}
	return true, nil
}

// footageShort makes a short of footage (D295) as a new cut of the run's
// project through footage.short (D246), with the run's origin. Its cut's
// name is fixed by the output and the short's number, so a look that stopped
// after the cut was saved finds it again instead of making a second one. It
// reports false when the short can't be made (said on the output).
func (e *engine) footageShort(ctx context.Context, st *models.RecipeStep, o *models.RecipeRunOutput, out *models.RecipeOutput) (bool, error) {
	branch := shortBranch(o)
	var p models.Project
	if err := e.a.DB.GetByID(ctx, store.CProjects, e.a.WorkspaceID(), e.run.ProjectID, &p); err != nil {
		return false, apperr.New(apperr.NotFound, "The project this run makes its video in is gone.")
	}
	if head := p.Branches[branch].Head; head != "" {
		var rev models.Revision
		if e.a.DB.GetByID(ctx, store.CRevisions, e.a.WorkspaceID(), head, &rev) == nil && rev.Origin == "recipe:"+e.run.ID {
			o.ProjectID, o.ProjectName, o.Branch = p.ID, p.Name, branch
			o.Revision, o.RevisionNumber, o.State = rev.ID, rev.Number, models.OutputMaking
			return true, save(ctx, e.a, e.run)
		}
		o.State, o.Problem = models.OutputFailed, fmt.Sprintf("The project already has a cut called %q that this run didn't make.", branch)
		return false, nil
	}
	in := ops.FootageShortIn{ProjectID: e.run.ProjectID, Pieces: []ops.ShortPiece{{AssetID: o.AssetID, From: o.From, To: o.To}}, Branch: branch,
		Name: shortProjectName(e.doc, e.run, o, out), Why: shorten(joinAnd(o.Why), 2000), Origin: "recipe:" + e.run.ID}
	if out != nil {
		in.Captions = out.Captions
		if out.Shape == "square" {
			in.Shape = "square"
		}
	}
	chk, err := ops.CallTyped[ops.FootageShortOut](ctx, e.a, "footage.short", in, ops.Caller{})
	if err == nil && (chk.Check == nil || !chk.Check.Valid || chk.Check.ConfirmToken == "") {
		why := "its cut didn't pass its check"
		if chk.Check != nil && len(chk.Check.Errors) > 0 {
			why = "its cut didn't pass its check: " + strings.TrimSuffix(chk.Check.Errors[0].Message, ".")
		}
		err = apperr.New(apperr.Validation, "%s", upperFirst(why)+".")
	}
	if err != nil {
		o.State, o.Problem = models.OutputFailed, plainFailure(err)
		return false, nil
	}
	in.ConfirmToken = chk.Check.ConfirmToken
	sh, err := ops.CallTyped[ops.FootageShortOut](ctx, e.a, "footage.short", in, ops.Caller{})
	if err != nil || sh.Applied == nil || sh.Applied.Revision.ID == "" {
		if err == nil {
			err = apperr.New(apperr.Conflict, "Its cut couldn't be saved.")
		}
		o.State, o.Problem = models.OutputFailed, plainFailure(err)
		return false, nil
	}
	o.ProjectID, o.ProjectName, o.Branch = p.ID, p.Name, sh.Branch
	o.Revision, o.RevisionNumber, o.State = sh.Applied.Revision.ID, sh.Applied.Revision.Number, models.OutputMaking
	st.Made = append(st.Made, sh.Applied.Revision.ID)
	return true, save(ctx, e.a, e.run) // never two cuts for one short
}

// shortBranch is a short of footage's cut name: the output's name and its number.
func shortBranch(o *models.RecipeRunOutput) string {
	b := strings.Trim(branchCleaner.ReplaceAllString(o.Output, "-"), "-")
	if b == "" || b[0] < 'a' || b[0] > 'z' {
		b = "short"
	}
	suffix := fmt.Sprintf("-%d", o.N)
	if len(b)+len(suffix) > 40 {
		b = strings.TrimRight(b[:40-len(suffix)], "-")
	}
	return b + suffix
}

func firstProblem(outs []models.RecipeRunOutput) string {
	for _, o := range outs {
		if o.State == models.OutputFailed && o.Problem != "" {
			return o.Problem
		}
	}
	return "something went wrong."
}

// reviewCopy asks for a review copy of a cut's newest version and waits for it
// and its quality check (the main video's review step, for one more cut);
// key names its work on the step.
func (e *engine) reviewCopy(ctx context.Context, st *models.RecipeStep, key, projectID, branch string, render, qa, revision *string, number *int) (bool, error) {
	if st.Work[key+":render"] == "" {
		var p models.Project
		if err := e.a.DB.GetByID(ctx, store.CProjects, e.a.WorkspaceID(), projectID, &p); err != nil {
			return false, apperr.New(apperr.NotFound, "The short's project is gone.")
		}
		head := p.Branches[branch].Head
		if head == "" {
			return false, apperr.New(apperr.NotFound, "The short's cut is gone.")
		}
		var rev models.Revision
		if err := e.a.DB.GetByID(ctx, store.CRevisions, e.a.WorkspaceID(), head, &rev); err != nil {
			return false, err
		}
		*revision, *number = rev.ID, rev.Number
		out, err := ops.CallTyped[ops.RenderOut](ctx, e.a, "render.submit", ops.RenderSubmitIn{RevisionID: rev.ID, Quality: "review"}, ops.Caller{})
		if err != nil {
			return false, err
		}
		st.Work[key+":render"] = out.Render.ID
		*render = out.Render.ID
		if out.Job != nil {
			st.Jobs = append(st.Jobs, out.Job.ID)
		} else if out.Render.JobID != "" {
			st.Jobs = append(st.Jobs, out.Render.JobID)
		}
	}
	rc, err := e.renderChecked(ctx, st, st.Work[key+":render"])
	if err != nil || !rc.done {
		return false, err
	}
	*qa = rc.qa
	return true, nil
}

// clearerDone is what making voices clearer did: done (false while it waits
// for a person), the newest version it saved ("" none) and the voices nobody
// by that name speaks in.
type clearerDone struct {
	done     bool
	revision string
	number   int
	skipped  []string
}

// clearer makes a version's voices clearer as voices say (the recipe's
// settings), within what the person agreed to; key names its work on the step.
func (e *engine) clearer(ctx context.Context, st *models.RecipeStep, revID string, voices []editspec.VoiceSound, key string) (clearerDone, error) {
	var res clearerDone
	for _, v := range voices {
		target := v.Name
		if target == "" {
			target = v.AssetID
		}
		wk := key + ":" + target
		if key == "" {
			wk = target
		}
		if st.Work[wk] == "done" {
			continue
		}
		in := sound.EnhanceIn{RevisionID: revID, Target: target, Mode: v.Enhance.Mode, Strength: v.Enhance.Strength, Estimate: true}
		est, err := ops.CallTyped[sound.EnhanceOut](ctx, e.a, "sound.enhance", in, ops.Caller{})
		if err != nil {
			if w := personFix(err); w != "" {
				e.waitFor(st, models.RunWaitsForPerson, w)
				return res, nil
			}
			if c := apperr.From(err).Code; c == apperr.NotFound || c == apperr.InvalidInput {
				// Nobody by that name speaks in it (yet): the setting stays for when they do.
				res.skipped = append(res.skipped, "“"+target+"”")
				st.Work[wk] = "done"
				continue
			}
			return res, err
		}
		if !est.Estimate.Allowed {
			why := est.Estimate.Problem
			if why == "" {
				why = "the clearer voice can't be made now"
			}
			e.waitFor(st, models.RunWaitsForPerson, "Waiting for you: "+strings.TrimSuffix(why, ".")+". Then let the run go on.")
			return res, nil
		}
		usd := 0.0
		if est.Estimate.Cost != nil {
			usd = est.Estimate.Cost.USD
		}
		if left, limited := e.leftFor(st); limited && usd > left+0.0005 {
			e.waitForMore(st, "the clearer voices cost", usd-left)
			return res, nil
		}
		in.Estimate, in.Apply = false, true
		out, err := ops.CallTyped[sound.EnhanceOut](ctx, e.a, "sound.enhance", in, ops.Caller{})
		if err != nil {
			if w := personFix(err); w != "" {
				e.waitFor(st, models.RunWaitsForPerson, w)
				return res, nil
			}
			return res, err
		}
		if est.Estimate.Credits > 0 {
			e.spend(st, models.RecipeSpent{USD: providers.IsolationUSD(est.Estimate.Credits), Credits: roundCredits(est.Estimate.Credits)})
		}
		if out.JobID != "" {
			st.Jobs = append(st.Jobs, out.JobID)
		}
		if out.Applied != nil && out.Applied.RevisionID != "" {
			res.revision, res.number, revID = out.Applied.RevisionID, out.Applied.Number, out.Applied.RevisionID
			st.Made = append(st.Made, out.Applied.RevisionID)
		} else if out.Applied != nil && out.Applied.Problem != "" {
			return res, apperr.New(apperr.Conflict, "The clearer voice was made, but %s.", strings.TrimSuffix(out.Applied.Problem, "."))
		}
		st.Work[wk] = "done"
	}
	res.done = true
	return res, nil
}

// RecipeOutputBrief is one video of a run's pack as a list shows it: where it
// is (its own project and cut) and where it stands.
type RecipeOutputBrief struct {
	Output    string `json:"output"`
	N         int    `json:"n"`
	State     string `json:"state" jsonschema:"enum=planned,enum=making,enum=ready,enum=dropped,enum=failed"`
	ProjectID string `json:"projectId,omitempty"`
	Project   string `json:"project,omitempty"`
	Branch    string `json:"branch,omitempty"`
	Version   int    `json:"version,omitempty" jsonschema:"description=The number of the version of its cut the run made."`
	URL       string `json:"url,omitempty"`
}

// sortOutputs keeps a run's shorts in order (output, then number).
func sortOutputs(outs []models.RecipeRunOutput) {
	sort.SliceStable(outs, func(i, j int) bool {
		if outs[i].Output != outs[j].Output {
			return outs[i].Output < outs[j].Output
		}
		return outs[i].N < outs[j].N
	})
}

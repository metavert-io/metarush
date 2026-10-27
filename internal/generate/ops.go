package generate

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/prefs"
	"github.com/metavert-io/metarush/internal/store"
)

func init() {
	ops.Def("generate.estimate", "Price a generated shot and check it may be made (consent, budget, this machine's key); nothing is sent or spent", ops.Flags{},
		func(ctx context.Context, a *app.App, in *GenShotIn) (*GenEstimate, error) {
			p, err := planShot(ctx, a, in)
			if err != nil {
				return nil, err
			}
			e := p.estimate(ctx, a)
			return &e, nil
		})
	ops.Def("generate.shot", "Make a new shot with generative video (Google Gemini) from a prompt, a look, a frame to start from or two frames, and pictures; priced first: needs maxUsd, the agreed estimate, unless your estimates are off", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *GenShotIn) (*GenRunOut, error) {
			p, err := planShot(ctx, a, in)
			if err != nil {
				return nil, err
			}
			return start(ctx, a, p, in.MaxUSD, in.DryRun, in.Origin)
		})
	ops.Def("generate.again", "Make new takes of a generated shot, made the same way (Generate again, Variations); never replaces a clip", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *GenAgainIn) (*GenRunOut, error) {
			p, err := planAgain(ctx, a, in)
			if err != nil {
				return nil, err
			}
			return start(ctx, a, p, in.MaxUSD, in.DryRun, in.Origin)
		})
	ops.Def("generate.extend", "Continue a clip (generated or your footage) with generative video; a new clip, priced first", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *GenExtendIn) (*GenRunOut, error) {
			p, err := planWork(ctx, a, models.GenExtend, in.ID, in.Prompt, in.ProjectID, in.From, nil, in.Seconds, in.Quality, in.Resolution, in.NoPeople)
			if err != nil {
				return nil, err
			}
			return start(ctx, a, p, in.MaxUSD, in.DryRun, in.Origin)
		})
	ops.Def("generate.edit", "Change a clip by an instruction with generative video (make it night); a new clip, priced first", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *GenEditIn) (*GenRunOut, error) {
			p, err := planWork(ctx, a, models.GenEdit, in.ID, in.Instruction, in.ProjectID, in.From, in.To, 0, in.Quality, in.Resolution, in.NoPeople)
			if err != nil {
				return nil, err
			}
			return start(ctx, a, p, in.MaxUSD, in.DryRun, in.Origin)
		})
	ops.Def("generate.list", "List generated videos (a project's, a look's, or a clip's takes) with what they cost", ops.Flags{},
		func(ctx context.Context, a *app.App, in *GenListIn) (*GenListOut, error) { return listGens(ctx, a, in) })
	ops.Def("generate.show", "Show how a generated video was made: prompt, look, pictures, settings, cost, safety, lineage and where it is used", ops.Flags{},
		func(ctx context.Context, a *app.App, in *GenIDIn) (*GenView, error) { return showGen(ctx, a, in.ID) })
	ops.Def("generate.cancel", "Stop a generated video that is waiting or being made", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *GenIDIn) (*GenCancelOut, error) {
			return cancelGen(ctx, a, in.ID)
		})
	ops.Def("generate.settings", "Show the generative-video settings: models and prices, draft and final quality, the daily limit and a project's budget", ops.Flags{},
		func(ctx context.Context, a *app.App, in *GenSettingsIn) (*GenSettingsOut, error) {
			return settingsOut(ctx, a, in.ProjectID)
		})
	ops.Def("generate.configure", "Change the generative-video model, draft and final quality, the daily limit or a project's budget (people only)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *GenConfigureIn) (*GenSettingsOut, error) {
			return configure(ctx, a, in)
		})

	ops.Def("looks.list", "List looks: how people and characters appear in generated video", ops.Flags{},
		func(ctx context.Context, a *app.App, in *LookListIn) (*LookListOut, error) {
			return listLooks(ctx, a, in)
		})
	ops.Def("looks.show", "Show a look: its stills and clips, description, consent and test shots", ops.Flags{},
		func(ctx context.Context, a *app.App, in *LookRefIn) (*LookView, error) {
			l, err := findLook(ctx, a, in.Look)
			if err != nil {
				return nil, err
			}
			v := lookView(ctx, a, l, true)
			return &v, nil
		})
	ops.Def("looks.create", "Make a look for a person (with their consent) or a fictional character from stills and short clips (people only)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *LookCreateIn) (*LookOut, error) {
			return createLook(ctx, a, in)
		})
	ops.Def("looks.update", "Change a look; a change to what it shows needs approving again (people only)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *LookUpdateIn) (*LookOut, error) {
			return updateLook(ctx, a, in)
		})
	ops.Def("looks.approve", "Approve a look so new shots can use it (people only)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *LookDecideIn) (*LookOut, error) {
			return decideLook(ctx, a, in, true)
		})
	ops.Def("looks.revoke", "Withdraw a look: nothing new is made with it (people only)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *LookDecideIn) (*LookOut, error) {
			return decideLook(ctx, a, in, false)
		})
	ops.Def("looks.test", "Make a look's test sheet: a few short draft shots to check it before approving (people only; priced first)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *LookTestIn) (*GenRunOut, error) { return testLook(ctx, a, in) })

	ops.Def("consent.list", "List consent records for making people's likeness in generated video", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ConsentListIn) (*ConsentListOut, error) {
			return listConsents(ctx, a, in)
		})
	ops.Def("consent.show", "Show a consent record", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ConsentIDIn) (*ConsentView, error) {
			c, err := loadConsent(ctx, a, in.ID)
			if err != nil {
				return nil, err
			}
			v := consentView(ctx, a, *c)
			return &v, nil
		})
	ops.Def("consent.record", "Record consent to make a person's likeness: your own statement, or their recorded consent with what it covers and until when (people only; never a child or a public figure)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *ConsentRecordIn) (*ConsentOut, error) {
			return recordConsent(ctx, a, in)
		})
	ops.Def("consent.revoke", "Withdraw a consent: nothing new is made with that likeness, and looks that rely on it are withdrawn (people only)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *ConsentRevokeIn) (*ConsentOut, error) {
			return revokeConsent(ctx, a, in)
		})
}

// start turns a plan into generations: the price must have been agreed, the
// consent must be there, the money is set aside, and each take is recorded
// and queued on this machine (the one with the key).
func start(ctx context.Context, a *app.App, p *plan, maxUSD *float64, dry bool, origin string) (*GenRunOut, error) {
	est := p.estimate(ctx, a)
	out := &GenRunOut{Estimate: est, Generations: []GenView{}, JobIDs: []string{}, DryRun: dry}
	if dry {
		out.Summary = est.Summary
		return out, nil
	}
	if _, err := keyFor(a, p.provider); err != nil {
		return nil, err
	}
	// A client's Content Settings may keep its work away from AI services
	// (D225): the project it is for and every project whose clips or pictures
	// it sends.
	if err := RulesAllow(ctx, a, p.provider, TouchedBy(ctx, a, p.projectID, p.refs)); err != nil {
		return nil, err
	}
	if err := mayLeave(ctx, a, p); err != nil { // What may leave (D290)
		return nil, err
	}
	if len(p.consent) > 0 {
		return nil, consentRequired("%s", p.consent[0]).WithDetails(map[string]any{"problems": p.consent, "people": p.people})
	}
	if len(p.problems) > 0 {
		return nil, apperr.New(apperr.Conflict, "%s", p.problems[0]).WithDetails(map[string]any{"problems": p.problems})
	}
	// The estimate step (D208): a person who turned estimates off (and whom no
	// owner asks for them) agrees to the estimate by asking; the project's
	// budget and today's limit below still decide what may be spent.
	estimateFirst := true
	if maxUSD == nil && !prefs.EstimatesFirstForAsker(ctx, a) {
		v := est.USD
		maxUSD, estimateFirst = &v, false
	}
	if maxUSD == nil || est.USD > *maxUSD+0.0005 {
		msg := fmt.Sprintf("This costs about %s. Confirm the price to make it.", money(est.USD))
		if maxUSD != nil {
			msg = fmt.Sprintf("This costs about %s, more than the %s you agreed to.", money(est.USD), money(*maxUSD))
		}
		return nil, apperr.New(apperr.ConfirmRequired, "%s", msg).WithHint("Nothing was sent or spent.").
			WithDetails(map[string]any{"estimateUsd": est.USD, "estimate": est})
	}
	// From here on the takes are made as a unit: a browser that goes away in the middle must not leave
	// money set aside without a generation, or a generation without its job.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
	defer cancel()
	now := time.Now().UTC()
	group, take := "", 1
	if p.parent != nil && (p.op == models.GenAgain || p.op == models.GenVariation) {
		group = p.parent.GroupID
		take = nextTake(ctx, a, group)
	}
	var made []models.Generation
	undo := func() {
		for i := range made {
			g := &made[i]
			_, _ = a.Queue.RequestCancel(context.WithoutCancel(ctx), g.JobID)
			_ = setOnce(context.WithoutCancel(ctx), a, g.ID, "outcome", models.GenOutcome{State: models.GenCanceled, At: time.Now().UTC(), Message: "Stopped: the other takes didn't fit the budget.",
				Actual: &models.GenCost{Basis: "none"}})
			_ = Settle(context.WithoutCancel(ctx), a, g, 0, "")
		}
	}
	for i := 0; i < p.takes; i++ {
		g := models.Generation{ID: ids.New(ids.Generation), WorkspaceID: a.WorkspaceID(), ProjectID: p.projectID, Kind: models.GenKindVideo, Operation: p.op, Take: take + i,
			Provider: p.provider, ProviderTitle: p.pinfo.Title, Model: p.caps.ID, ModelTitle: p.caps.Title, Stage: p.caps.Stage, PriceTable: p.est.PriceTable,
			Prompt: p.prompt, Template: p.tmpl, Look: p.lookRef, References: p.refs, Params: p.params, RequestHash: p.req.Hash(p.provider),
			ConsentIDs: p.consentIDs, NoPeople: p.noPeople, SourceAssets: p.sources, CreatedBy: a.Actor, Machine: a.Cfg.Installation.Name,
			Channel: channelOf(a.Actor, origin), JobID: ids.New(ids.Job), CreatedAt: now,
			Estimate: models.GenCost{USD: p.est.USD, VideoTokens: p.est.VideoTokens, InputTokens: p.est.InputTokens, ThinkingTokens: p.est.ThinkingTokens, Basis: "estimate"}}
		if p.sent != p.prompt {
			g.SentPrompt = p.sent
		}
		if g.Template != nil && p.title != "" && (p.op == models.GenExtend || p.op == models.GenEdit) {
			vars := map[string]string{}
			for k, v := range g.Template.Vars {
				vars[k] = v
			}
			vars["title"] = p.title
			g.Template = &models.GenTemplate{Name: g.Template.Name, Vars: vars}
		}
		if p.parent != nil {
			g.ParentID = p.parent.ID
		}
		g.GroupID = group
		if g.GroupID == "" {
			if len(made) > 0 {
				g.GroupID = made[0].GroupID
			} else {
				g.GroupID = g.ID
			}
		}
		b, err := reserveWith(ctx, a, p.st, p.projectID, g.ID, p.est.USD, now)
		if err != nil {
			undo()
			return nil, err
		}
		g.Budget = b
		if err := a.DB.Insert(ctx, store.CGenerations, g); err != nil {
			_ = Settle(context.WithoutCancel(ctx), a, &g, 0, "")
			undo()
			return nil, err
		}
		_, _, err = a.Queue.Enqueue(ctx, jobs.EnqueueOptions{ID: g.JobID, Type: jobs.TypeGenerate, IdempotencyKey: "generate:" + g.ID, Priority: 9, MaxAttempts: 6,
			ProjectID: p.projectID, Actor: a.Actor, Installation: a.Cfg.Installation.ID,
			Payload: map[string]any{"generationId": g.ID, "what": what(&g), "seconds": g.Params.Seconds, "resolution": g.Params.Resolution, "provider": g.ProviderTitle, "costUsd": g.Estimate.USD, "take": g.Take}})
		if err != nil {
			_ = setOnce(context.WithoutCancel(ctx), a, g.ID, "outcome", models.GenOutcome{State: models.GenFailed, At: time.Now().UTC(), Message: "MetaRush couldn't queue it.", Actual: &models.GenCost{Basis: "none"}})
			_ = Settle(context.WithoutCancel(ctx), a, &g, 0, "")
			undo()
			return nil, err
		}
		made = append(made, g)
		out.JobIDs = append(out.JobIDs, g.JobID)
	}
	var genIDs []string
	for _, g := range made {
		genIDs = append(genIDs, g.ID)
	}
	a.Audit(ctx, "generate."+map[string]string{models.GenShot: "shot", models.GenAgain: "again", models.GenVariation: "again", models.GenExtend: "extend", models.GenEdit: "edit", models.GenLookTest: "test"}[p.op],
		"generation", made[0].ID, p.projectID, map[string]any{"generations": genIDs, "provider": p.provider, "model": p.caps.ID, "estimateUsd": est.USD, "takes": p.takes,
			"look": lookName2(p.lookRef), "consents": p.consentIDs, "likeness": p.likeness, "noPeople": p.noPeople, "aiGenerated": true, "channel": channelOf(a.Actor, origin),
			"estimateFirst": estimateFirst})
	out.Generations = views(ctx, a, made)
	kind := what(&made[0])
	if p.takes > 1 {
		kind = fmt.Sprintf("%d takes of %s", p.takes, kind)
	}
	out.Summary = fmt.Sprintf("Asked %s for %s (about %s). It lands in Sources, marked as AI-generated, in a minute or two.", p.pinfo.Title, kind, money(est.USD))
	return out, nil
}

func lookName2(l *models.GenLookRef) string {
	if l == nil {
		return ""
	}
	return l.Name + "@" + l.Version
}

func cancelGen(ctx context.Context, a *app.App, id string) (*GenCancelOut, error) {
	g, err := resolveGen(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if g.Outcome != nil {
		v := views(ctx, a, []models.Generation{*g})[0]
		return &GenCancelOut{Generation: v, Summary: "It has already ended: " + strings.ToLower(v.Summary[:1]) + v.Summary[1:]}, nil
	}
	j, err := a.Queue.RequestCancel(ctx, g.JobID)
	if err != nil {
		return nil, err
	}
	summary := "Asked it to stop. MetaRush tells Google Gemini to stop too."
	if j.State == models.JobCanceled && g.Ticket == nil {
		// It hadn't started: nothing was sent or spent.
		r := &runner{a: a, g: g, job: j}
		r.end(ctx, &models.GenOutcome{State: models.GenCanceled, Message: "It was stopped before it was sent. Nothing was spent."}, &models.GenCost{Basis: "none"})
		_ = Settle(ctx, a, r.g, 0, "")
		summary = "Stopped before it was sent. Nothing was spent."
	}
	a.Audit(ctx, "generate.cancel", "generation", g.ID, g.ProjectID, map[string]any{"job": g.JobID})
	g, _ = loadGen(ctx, a, g.ID)
	return &GenCancelOut{Generation: views(ctx, a, []models.Generation{*g})[0], Summary: summary}, nil
}

// LookTestIn makes test shots of a look. People only.
type LookTestIn struct {
	Look    string   `json:"look" jsonschema:"minLength=1,maxLength=80"`
	Prompts []string `json:"prompts,omitempty" jsonschema:"maxItems=3,description=What the test shots show (default: two standard shots)."`
	MaxUSD  *float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0"`
	DryRun  bool     `json:"dryRun,omitempty"`
}

// testPrompts are a look's standard test shots.
var testPrompts = []string{"looks at the camera, then smiles. A plain, softly lit background. Medium close-up.",
	"walks slowly across a sunlit room and turns to the camera. Wide shot."}

func testLook(ctx context.Context, a *app.App, in *LookTestIn) (*GenRunOut, error) {
	l, err := findLook(ctx, a, in.Look)
	if err != nil {
		return nil, err
	}
	if l.State == models.LookRevoked {
		return nil, apperr.New(apperr.Conflict, "The look “%s” was withdrawn.", l.Title)
	}
	prompts := in.Prompts
	if len(prompts) == 0 {
		for _, tp := range testPrompts {
			prompts = append(prompts, l.Title+" "+tp)
		}
	}
	st, err := loadSettings(ctx, a, "")
	if err != nil {
		return nil, err
	}
	now := nowFn()
	var plans []*plan
	total := 0.0
	for _, pr := range prompts {
		p := &plan{op: models.GenLookTest, prompt: strings.TrimSpace(pr), st: st, takes: 1, now: now, look: l,
			lookRef: &models.GenLookRef{ID: l.ID, Name: l.Name, Title: l.Title, Version: l.Version, Subject: l.Subject, PersonID: l.PersonID}}
		if l.Subject == models.LookPerson {
			c, err := activeConsent(ctx, a, l.PersonID, "", now)
			if err != nil {
				return nil, consentRequired("%s's consent isn't in force, so their look can't be tried out.", l.PersonName).WithDetails(map[string]any{"reason": "consent"})
			}
			p.lookRef.ConsentID = c.ID
			p.addConsent(c)
		}
		// Test shots are short drafts at the lowest quality: they show the likeness, cheaply.
		if err := p.resolveSettings("", 3, firstNonEmpty(l.Defaults.AspectRatio, st.aspect), "draft", "360p"); err != nil {
			return nil, err
		}
		var tags []string
		for _, s := range l.Stills {
			path, err := local(ctx, a, s.StorageKey, s.SHA256, s.Size)
			if err != nil {
				return nil, err
			}
			tags = append(tags, fmt.Sprintf("<IMAGE_REF_%d>", len(p.req.Images)))
			r, m := p.mediaRef(&stored{SHA256: s.SHA256, Key: s.StorageKey, Path: path, MIME: "image/jpeg", Size: s.Size}, models.RefLookStill, l.Name+"/"+s.Ref+"@"+l.Version)
			r.AssetID, r.At = s.AssetID, s.At
			p.refs, p.req.Images = append(p.refs, r), append(p.req.Images, m)
		}
		for _, c := range l.Clips {
			path, err := local(ctx, a, c.StorageKey, c.SHA256, c.Size)
			if err != nil {
				return nil, err
			}
			r, m := p.mediaRef(&stored{SHA256: c.SHA256, Key: c.StorageKey, Path: path, MIME: "video/mp4", Size: c.Size, Seconds: c.Seconds}, models.RefLookClip, l.Name+"/"+c.Ref+"@"+l.Version)
			r.AssetID, r.From, r.To = c.AssetID, c.From, c.To
			p.refs, p.req.Videos = append(p.refs, r), append(p.req.Videos, m)
		}
		p.req.Task = "reference_to_video"
		p.sent = fmt.Sprintf("%s\n\n%s appears in this shot, exactly as in %s. %s", p.prompt, l.Title, strings.Join(tags, " and "), l.Description)
		if l.Style == models.LookStylized {
			p.sent = fmt.Sprintf("%s\n\n%s appears in this shot as in %s, drawn as %s. %s", p.prompt, l.Title, strings.Join(tags, " and "), l.StyleNote, l.Description)
		}
		p.tmpl = &models.GenTemplate{Name: "look-test", Vars: map[string]string{"prompt": p.prompt, "who": l.Title, "description": l.Description}}
		p.title = "Test of " + l.Title
		p.noPeople = true
		if err := p.finish(ctx, a); err != nil {
			return nil, err
		}
		plans = append(plans, p)
		total += p.est.USD
	}
	total = roundMoney(total)
	// Every test shot is a part of the estimate (D208).
	cost := costs.Of()
	for _, p := range plans {
		cost.Add("", p.cost(p.est.USD))
	}
	cost = cost.Total(total)
	if in.DryRun || in.MaxUSD == nil || total > *in.MaxUSD+0.0005 {
		e := plans[0].estimate(ctx, a)
		e.Takes, e.USD, e.Cost = len(plans), total, cost
		e.Summary = fmt.Sprintf("%s for the look “%s” (draft, 360p) cost about %s.", upperFirst(plural(len(plans), "test shot")), l.Title, money(total))
		if in.DryRun {
			return &GenRunOut{DryRun: true, Estimate: e, Generations: []GenView{}, JobIDs: []string{}, Summary: e.Summary}, nil
		}
		return nil, apperr.New(apperr.ConfirmRequired, "The test shots cost about %s. Confirm the price to make them.", money(total)).WithHint("Nothing was sent or spent.").
			WithDetails(map[string]any{"estimateUsd": total, "estimate": e})
	}
	out := &GenRunOut{Generations: []GenView{}, JobIDs: []string{}}
	for i, p := range plans {
		one := p.est.USD
		res, err := start(ctx, a, p, &one, false, "")
		if err != nil {
			if i == 0 {
				return nil, err
			}
			break
		}
		for _, g := range res.Generations {
			_ = a.DB.Update(ctx, store.CLooks, a.WorkspaceID(), l.ID, map[string]any{"$push": map[string]any{"tests": models.LookTest{GenerationID: g.ID, Prompt: p.prompt, At: time.Now().UTC()}}})
		}
		out.Generations = append(out.Generations, res.Generations...)
		out.JobIDs = append(out.JobIDs, res.JobIDs...)
		if i == 0 {
			out.Estimate = res.Estimate
		}
	}
	out.Estimate.Takes, out.Estimate.USD, out.Estimate.Cost = len(out.Generations), total, cost
	out.Summary = fmt.Sprintf("Asked for %s of the look “%s” (about %s). They show on the look's test sheet when they're ready.", plural(len(out.Generations), "test shot"), l.Title, money(total))
	return out, nil
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("generate.shot", ops.Offering{ID: "generated-shots", Group: "make", Title: "Generated shots",
		Does:  "Makes a new video shot from a description; priced first and paid to the service directly.",
		Words: []string{"ai video", "extra shots", "make a clip from a prompt"}, Needs: ops.Needs{Keys: []string{"gemini"}, Priced: true, Leaves: "your words and any pictures you give it leave this computer"}})
}

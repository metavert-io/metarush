package generate

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/costs"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/videogen"
)

// plan is a generation request worked out before anything is sent: the
// prompt as sent, the exact pictures and clips, the settings, the price, and
// whether it may go ahead (consent, budget, the key).
type plan struct {
	op         string
	projectID  string
	prompt     string // verbatim
	sent       string // as sent
	tmpl       *models.GenTemplate
	look       *models.GenLook
	lookRef    *models.GenLookRef
	refs       []models.GenReference
	req        videogen.Request
	params     models.GenParams
	caps       *videogen.Capabilities
	est        *videogen.Estimate
	takes      int
	consentIDs []string
	people     []string
	likeness   bool
	noPeople   bool
	parent     *models.Generation
	sources    []string
	extendFrom float64
	title      string   // the new clips' title
	consent    []string // why consent is missing (plain)
	problems   []string // other reasons it can't go ahead (plain)
	keyMissing bool     // this machine has no key for the provider
	notes      []string
	refWords   []string
	st         *settings
	now        time.Time
	// provider is the model's provider (D282: Veo on Vertex AI beside
	// Gemini); budgets and the daily limit stay the team's AI work ones.
	provider string
	pinfo    *videogen.ProviderInfo
}

// addConsent records that the request relies on a consent record.
func (p *plan) addConsent(c *models.Consent) {
	for _, id := range p.consentIDs {
		if id == c.ID {
			return
		}
	}
	p.consentIDs = append(p.consentIDs, c.ID)
	p.people = append(p.people, c.PersonName)
	p.likeness = true
}

// mediaRef turns stored bytes into a reference and the media to send.
func (p *plan) mediaRef(s *stored, role, ref string) (models.GenReference, videogen.Media) {
	r := models.GenReference{Role: role, Ref: ref, SHA256: s.SHA256, MIME: s.MIME, Size: s.Size, StorageKey: s.Key}
	m := videogen.Media{Role: role, MIME: s.MIME, Path: s.Path, SHA256: s.SHA256, Size: s.Size, Seconds: s.Seconds}
	return r, m
}

// whoIsIn checks who a moment of a clip shows (the faces index, or the look
// of a generated clip) and that each of them has consent for this project.
func (p *plan) whoIsIn(ctx context.Context, a *app.App, as *models.Asset, from, to float64, what string) []string {
	if as.Source.IsGenerated() {
		if g, err := resolveGen(ctx, a, as.ID); err == nil {
			if g.Look != nil && g.Look.Subject == models.LookPerson {
				p.needConsent(ctx, a, g.Look.PersonID, personName(ctx, a, g.Look.PersonID), what)
				return []string{personName(ctx, a, g.Look.PersonID)}
			}
			for _, id := range g.ConsentIDs {
				if c, err := loadConsent(ctx, a, id); err == nil {
					p.needConsent(ctx, a, c.PersonID, c.PersonName, what)
				}
			}
			return nil
		}
	}
	named, unnamed, resembles, known := facesIn(ctx, a, as, from, to)
	var names []string
	for _, n := range named {
		p.needConsent(ctx, a, n.PersonID, n.Name, what)
		names = append(names, n.Name)
	}
	appNamed := FacesSeen(ctx, a, as, from, to).AppNamed
	switch {
	case len(appNamed) > 0:
		// Only a name a person gave counts (D274).
		p.consent = append(p.consent, fmt.Sprintf("%s shows someone named as %s by %s, not by a person. A person names them on the clip and records their consent, or pick a moment without people.",
			upperFirst(what), appNamed[0].Name, appNamed[0].By))
	case unnamed > 0 && len(resembles) > 0:
		p.consent = append(p.consent, fmt.Sprintf("%s shows someone who looks like %s. MetaRush only goes by looks there: check that it's them on their page, then record their consent, or pick a moment without people.",
			upperFirst(what), strings.Join(resembles, " or ")))
	case unnamed > 0:
		p.consent = append(p.consent, fmt.Sprintf("%s shows someone nobody has named. Name them on the People page and record their consent, or pick a moment without people.", upperFirst(what)))
	case !known && !p.noPeople:
		p.consent = append(p.consent, fmt.Sprintf("MetaRush can't tell whether %s shows people: “%s” hasn't been looked at for what's on screen. Say that it shows no one, or use a look.", what, as.DisplayTitle()))
	}
	return names
}

func personName(ctx context.Context, a *app.App, personID string) string {
	var pp models.Person
	if err := a.DB.GetByID(ctx, store.CPeople, a.WorkspaceID(), personID, &pp); err == nil {
		return pp.Name
	}
	return "someone"
}

// needConsent checks a person's consent for this project.
func (p *plan) needConsent(ctx context.Context, a *app.App, personID, name, what string) {
	c, err := activeConsent(ctx, a, personID, p.projectID, p.now)
	if err != nil {
		p.consent = append(p.consent, fmt.Sprintf("%s shows %s, and MetaRush has no consent from %s for this project. Record it on the People page first.", upperFirst(what), name, name))
		return
	}
	p.addConsent(c)
}

var wordRe = regexp.MustCompile(`[\p{L}\p{N}'’-]+`)

// namesInPrompt finds people the prompt names (as they are written on the
// People page): making someone's likeness takes their look, with consent.
func (p *plan) namesInPrompt(ctx context.Context, a *app.App) {
	ps, err := speakers.People(ctx, a, false)
	if err != nil || len(ps) == 0 {
		return
	}
	words := map[string]bool{}
	for _, w := range wordRe.FindAllString(p.prompt, -1) {
		words[w] = true
	}
	for _, pp := range ps {
		first := strings.Fields(pp.Name)
		if len(first) == 0 || len([]rune(first[0])) < 3 || !words[first[0]] {
			continue
		}
		if p.look != nil && p.look.PersonID == pp.ID {
			continue
		}
		p.consent = append(p.consent, fmt.Sprintf("The prompt names %s. MetaRush makes someone's likeness only from their look, with their consent: choose %s's look, or leave the name out.", pp.Name, pp.Name))
	}
}

// resolveSettings fills in the length, the shape and the quality.
func (p *plan) resolveSettings(model string, seconds int, aspect, quality, resolution string) error {
	st := p.st
	provider, id := videoModel(firstNonEmpty(model, st.model))
	caps, err := videogen.ModelInfo(provider, id)
	if err != nil {
		return err
	}
	if p.pinfo, err = videogen.Info(provider); err != nil {
		return err
	}
	p.caps, p.provider = caps, provider
	if p.look != nil {
		d := p.look.Defaults
		seconds = firstInt(seconds, d.Seconds)
		aspect = firstNonEmpty(aspect, d.AspectRatio)
		quality = firstNonEmpty(quality, d.Quality)
	}
	if aspect == "" && st.project != nil {
		aspect = projectAspect(st.project)
	}
	p.params = models.GenParams{Seconds: firstInt(seconds, st.seconds), AspectRatio: firstNonEmpty(aspect, st.aspect), Quality: firstNonEmpty(quality, "draft")}
	switch {
	case resolution != "":
		p.params.Resolution = resolution
		p.params.Quality = map[bool]string{true: "final", false: "draft"}[resolution == "1080p" || resolution == "4k"]
	case p.params.Quality == "final":
		p.params.Resolution = st.final
	default:
		p.params.Resolution = st.draft
	}
	return nil
}

func firstInt(vs ...int) int {
	for _, v := range vs {
		if v > 0 {
			return v
		}
	}
	return 0
}

// projectAspect is the shape of the project's cut: tall presets make 9:16.
func projectAspect(pr *models.Project) string {
	for _, p := range pr.Brief.Platforms {
		switch strings.ToLower(p) {
		case "shorts", "reels", "tiktok":
			return "9:16"
		}
	}
	return ""
}

// finish composes the prompt, builds the request, checks the model can do
// it and prices it.
func (p *plan) finish(ctx context.Context, a *app.App) error {
	p.req.Model, p.req.Seconds, p.req.AspectRatio, p.req.Resolution = p.caps.ID, p.params.Seconds, p.params.AspectRatio, p.params.Resolution
	if p.sent == "" {
		p.sent = p.prompt
	}
	p.req.Prompt = p.sent
	p.params.Task = p.req.Task
	if err := p.caps.Check(&p.req, p.extendFrom, p.now); err != nil {
		return err
	}
	pt, err := videogen.PriceAt(p.provider, p.caps.ID, p.now)
	if err != nil {
		return err
	}
	if p.est, err = pt.Estimate(&p.req); err != nil {
		return err
	}
	if !p.noPeople && !p.likeness {
		for _, r := range p.refs {
			if r.Role == models.RefImage {
				p.consent = append(p.consent, "MetaRush can't tell whether the pictures show real people. Say that they show no one, or use a look.")
				break
			}
		}
	}
	p.keyMissing = !keyReady(a, p.provider)
	if containsUpscaled(p.caps, p.params.Resolution) {
		p.notes = append(p.notes, fmt.Sprintf("%s video is made at a lower resolution and upscaled by %s.", videogen.ResolutionLabel(p.params.Resolution), p.pinfo.Title))
	}
	return nil
}

func containsUpscaled(c *videogen.Capabilities, res string) bool {
	for _, r := range c.Upscaled {
		if r == res {
			return true
		}
	}
	return false
}

// estimate is the plan's price with every check, for people and for cards.
func (p *plan) estimate(ctx context.Context, a *app.App) GenEstimate {
	e := GenEstimate{ProjectID: p.projectID, Provider: p.provider, ProviderTitle: p.pinfo.Title, Model: p.caps.ID, ModelTitle: p.caps.Title, Stage: p.caps.Stage, Operation: p.op, Task: p.req.Task,
		Takes: p.takes, Seconds: p.params.Seconds, Resolution: p.params.Resolution, AspectRatio: p.params.AspectRatio, Quality: p.params.Quality,
		Upscaled: containsUpscaled(p.caps, p.params.Resolution), PerTakeUSD: p.est.USD, USD: videogen.RoundUSD(p.est.USD * float64(p.takes)),
		PerSecondUSD: p.est.PerSecondUSD, PriceTable: p.est.PriceTable, VideoTokens: p.est.VideoTokens, InputTokens: p.est.InputTokens, ThinkingTokens: p.est.ThinkingTokens,
		References: p.refWords, Likeness: p.likeness, KeyReady: keyReady(a, p.provider), Notes: p.notes}
	if p.look != nil {
		e.Look = p.look.Title
	}
	e.Consent = GenConsentView{Needed: p.likeness || len(p.consent) > 0, OK: len(p.consent) == 0, People: p.people, ConsentIDs: p.consentIDs}
	if len(p.consent) > 0 {
		e.Consent.Message = p.consent[0]
	} else if p.likeness {
		e.Consent.Message = fmt.Sprintf("Consent recorded for %s.", strings.Join(p.people, " and "))
	}
	e.Budget = budgetView(ctx, a, p.st, p.projectID, p.now)
	e.Problems = append(append([]string{}, p.consent...), p.problems...)
	// Every project whose clips or pictures it sends, not only the one it is for (D225).
	if err := RulesAllow(ctx, a, p.provider, TouchedBy(ctx, a, p.projectID, p.refs)); err != nil {
		e.Problems = append(e.Problems, apperr.From(err).Message)
	} else if err := mayLeave(leave.Quiet(ctx), a, p); err != nil { // What may leave (D290)
		e.Problems = append(e.Problems, apperr.From(err).Message)
	}
	if p.keyMissing {
		e.Problems = append(e.Problems, fmt.Sprintf("%s isn't set up on this computer yet. Add the key in Settings, Integrations.", p.pinfo.Title))
	}
	if left := e.Budget.Today.LeftUSD; left != nil && e.USD > *left+0.0005 {
		e.Problems = append(e.Problems, fmt.Sprintf("Today's limit has %s left of %s, and this costs about %s.", money(*left), money(*e.Budget.Today.LimitUSD), money(e.USD)))
	}
	if pb := e.Budget.Project; pb != nil && pb.LeftUSD != nil && e.USD > *pb.LeftUSD+0.0005 {
		e.Problems = append(e.Problems, fmt.Sprintf("The project's budget has %s left of %s, and this costs about %s.", money(*pb.LeftUSD), money(*pb.LimitUSD), money(e.USD)))
	}
	e.Allowed = len(e.Problems) == 0
	kind := what(&models.Generation{Operation: p.op, Params: p.params})
	if p.takes > 1 {
		kind = fmt.Sprintf("%d takes of %s", p.takes, kind)
	}
	e.Summary = fmt.Sprintf("%s (%s, %s) costs about %s with %s.", upperFirst(kind), qualityWords(p.params), shapeWords(p.params.AspectRatio), money(e.USD), p.pinfo.Title)
	if !e.Allowed {
		e.Summary += " " + e.Problems[0]
	}
	e.Cost = p.cost(e.USD)
	return e
}

// costWhat is what a request makes, for an estimate's parts.
func costWhat(op string, takes int) string {
	switch op {
	case models.GenAgain, models.GenVariation:
		if takes > 1 {
			return "new takes of a generated shot"
		}
		return "a new take of a generated shot"
	case models.GenExtend:
		return "more of the clip"
	case models.GenEdit:
		return "a changed copy of the clip"
	case models.GenLookTest:
		return "a look's test shot"
	}
	if takes > 1 {
		return "generated shots"
	}
	return "a generated shot"
}

// cost is the estimate part by part (D208): the video made, what is sent with
// the prompt and the model's thinking allowance, each for every take, priced
// from the dated table. total is the request's own estimate (what a person
// agrees to); the parts may differ from it by a fraction of a cent.
func (p *plan) cost(total float64) costs.Estimate {
	takes := float64(max(1, p.takes))
	service, model, whatOf := p.pinfo.Title, p.caps.Title, costWhat(p.op, p.takes)
	day := costs.Day(p.est.PriceTable)
	source := "“" + clip(p.prompt, 80) + "”"
	switch {
	case p.op == models.GenExtend || p.op == models.GenEdit:
		if len(p.refWords) > 0 {
			source = p.refWords[0]
		}
		if p.prompt != "" {
			source += ": “" + clip(p.prompt, 60) + "”"
		}
	case p.op == models.GenAgain || p.op == models.GenVariation:
		source = "the same prompt and pictures as before (“" + clip(p.prompt, 60) + "”)"
	case len(p.refWords) > 0:
		source += "; " + strings.Join(p.refWords, "; ")
	}
	video := fmt.Sprintf("%s at %s", costs.Count(float64(p.params.Seconds), "second of video", "seconds of video"), videogen.ResolutionLabel(p.params.Resolution))
	if p.takes > 1 {
		video += fmt.Sprintf(", %d takes", p.takes)
	}
	if p.est.VideoTokens == 0 && p.est.InputTokens == 0 {
		// Priced by the second (Veo, D282): one part, the seconds at the table's rate.
		c := costs.Of(costs.Part{Service: service, What: whatOf, Model: model, Source: source, Amount: float64(p.params.Seconds) * takes, Unit: "seconds of video",
			Units: video, Rate: fmt.Sprintf("%s a second at %s (%s's prices of %s)", money(p.est.PerSecondUSD), videogen.ResolutionLabel(p.params.Resolution), service, day),
			USD: p.est.VideoUSD * takes}).Total(total)
		c.Note(fmt.Sprintf("From %s's prices of %s, by the second of video made, with sound.", service, day))
		return c
	}
	video += " (" + costs.Count(float64(p.est.VideoTokens)*takes, "token", "tokens") + ")"
	sent := "the prompt"
	switch n, v := len(p.req.Images), len(p.req.Videos); {
	case n > 0 && v > 0:
		sent += fmt.Sprintf(", %s and %s", plural(n, "picture"), plural(v, "clip"))
	case n > 0:
		sent += " and " + plural(n, "picture")
	case v > 0:
		sent += " and " + plural(v, "clip")
	}
	rate := func(perMillion float64, of string) string {
		return fmt.Sprintf("$%.2f per million %s (%s's prices of %s)", perMillion, of, service, day)
	}
	c := costs.Of(
		costs.Part{Service: service, What: whatOf, Model: model, Source: source, Amount: float64(p.params.Seconds) * takes, Unit: "seconds of video",
			Units: video, Rate: rate(p.est.VideoPerMillion, "tokens of video"), USD: p.est.VideoUSD * takes},
		costs.Part{Service: service, What: whatOf, Model: model, Source: "What is sent: " + sent, Amount: float64(p.est.InputTokens) * takes, Unit: "tokens",
			Units: costs.Count(float64(p.est.InputTokens)*takes, "token", "tokens"), Rate: rate(p.est.InputPerMillion, "tokens sent"), USD: p.est.InputUSD * takes},
		costs.Part{Service: service, What: whatOf, Model: model, Source: "An allowance for the model's thinking", Amount: float64(p.est.ThinkingTokens) * takes, Unit: "tokens",
			Units: costs.Count(float64(p.est.ThinkingTokens)*takes, "token", "tokens"), Rate: rate(p.est.TextPerMillion, "tokens of thinking"), USD: p.est.ThinkingUSD * takes},
	).Total(total)
	c.Note(fmt.Sprintf("From %s's prices of %s. The charge is usually a little less: the estimate allows for the model's thinking.", service, day))
	return c
}

func qualityWords(pr models.GenParams) string {
	q := "draft"
	if pr.Quality == "final" {
		q = "final quality"
	}
	return q + ", " + videogen.ResolutionLabel(pr.Resolution)
}

func shapeWords(ar string) string {
	if ar == "9:16" {
		return "tall"
	}
	return "wide"
}

// planShot works out a new shot.
func planShot(ctx context.Context, a *app.App, in *GenShotIn) (*plan, error) {
	if in.ProjectID == "" {
		return nil, apperr.New(apperr.InvalidInput, "Choose the project the shot is for.")
	}
	st, err := loadSettings(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	now, err := Now(ctx, a)
	if err != nil {
		return nil, err
	}
	p := &plan{op: models.GenShot, projectID: in.ProjectID, prompt: strings.TrimSpace(in.Prompt), st: st, noPeople: in.NoPeople, takes: max(1, in.Takes), now: now}
	if p.prompt == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say what the shot should show.")
	}
	if p.takes > 4 {
		return nil, apperr.New(apperr.InvalidInput, "Ask for at most 4 takes at once.")
	}
	if in.Look != "" {
		if in.From != nil || in.To != nil {
			return nil, apperr.New(apperr.InvalidInput, "Choose either a look or frames to start from, not both.")
		}
		l, err := findLook(ctx, a, in.Look)
		if err != nil {
			return nil, err
		}
		p.look = l
		c, why := usable(ctx, a, l, in.ProjectID, p.now)
		for _, w := range why {
			if strings.Contains(w, "consent") {
				p.consent = append(p.consent, w)
			} else {
				p.problems = append(p.problems, w)
			}
		}
		p.lookRef = &models.GenLookRef{ID: l.ID, Name: l.Name, Title: l.Title, Version: l.Version, Subject: l.Subject, PersonID: l.PersonID}
		if c != nil {
			p.lookRef.ConsentID = c.ID
			p.addConsent(c)
		}
	}
	if err := p.resolveSettings(in.Model, in.Seconds, in.AspectRatio, in.Quality, in.Resolution); err != nil {
		return nil, err
	}
	p.title = clip(p.prompt, 60)
	// Frames of real footage: the shot starts on one, or goes from one to another.
	if in.To != nil && in.From == nil {
		return nil, apperr.New(apperr.InvalidInput, "A shot that ends on a frame also needs the frame it starts from.")
	}
	for _, fr := range []struct {
		f    *FrameRef
		role string
	}{{in.From, models.RefStartFrame}, {in.To, models.RefEndFrame}} {
		if fr.f == nil {
			continue
		}
		if (fr.f.Picture == "") == (fr.f.AssetID == "") {
			return nil, apperr.New(apperr.InvalidInput, "A frame to start or end on is a clip and a time, or a picture.")
		}
		if fr.f.Picture != "" {
			// A picture the person added (D154): the shot starts (or ends) on it.
			s, r, pinned, err := picture(ctx, a, in.ProjectID, fr.f.Picture)
			if err != nil {
				return nil, err
			}
			what := fmt.Sprintf("the picture “%s”", r.Title)
			if !p.noPeople {
				p.consent = append(p.consent, fmt.Sprintf("MetaRush can't tell whether %s shows real people. Say that it shows no one, or use a look.", what))
			}
			gr, m := p.mediaRef(s, fr.role, pinned)
			gr.ResourceID = r.ID
			p.refs, p.req.Images = append(p.refs, gr), append(p.req.Images, m)
			if fr.role == models.RefStartFrame {
				p.refWords = append(p.refWords, "Starts from "+what)
			} else {
				p.refWords = append(p.refWords, "Ends on "+what)
			}
			continue
		}
		as, err := loadVideo(ctx, a, fr.f.AssetID)
		if err != nil {
			return nil, err
		}
		what := fmt.Sprintf("the frame at %s of “%s”", clock(fr.f.At), as.DisplayTitle())
		people := p.whoIsIn(ctx, a, as, fr.f.At, fr.f.At, what)
		s, err := still(ctx, a, as, fr.f.At)
		if err != nil {
			return nil, err
		}
		r, m := p.mediaRef(s, fr.role, fmt.Sprintf("%s@%.2f", as.ID, fr.f.At))
		r.AssetID, r.At, r.People = as.ID, fr.f.At, people
		p.refs, p.req.Images = append(p.refs, r), append(p.req.Images, m)
		p.sources = append(p.sources, as.ID)
		if fr.role == models.RefStartFrame {
			p.refWords = append(p.refWords, "Starts from "+what)
		} else {
			p.refWords = append(p.refWords, "Ends on "+what)
		}
	}
	// The look: its stills and clips, exactly as stored.
	var tags []string
	if l := p.look; l != nil {
		for _, s := range l.Stills {
			path, err := local(ctx, a, s.StorageKey, s.SHA256, s.Size)
			if err != nil {
				return nil, err
			}
			tags = append(tags, fmt.Sprintf("<IMAGE_REF_%d>", len(p.req.Images)))
			r, m := p.mediaRef(&stored{SHA256: s.SHA256, Key: s.StorageKey, Path: path, MIME: "image/jpeg", Size: s.Size}, models.RefLookStill, l.Name+"/"+s.Ref+"@"+l.Version)
			r.AssetID, r.At, r.ResourceID = s.AssetID, s.At, s.ResourceID
			if l.Subject == models.LookPerson {
				r.People = []string{l.PersonName}
			}
			p.refs, p.req.Images = append(p.refs, r), append(p.req.Images, m)
		}
		for _, c := range l.Clips {
			path, err := local(ctx, a, c.StorageKey, c.SHA256, c.Size)
			if err != nil {
				return nil, err
			}
			r, m := p.mediaRef(&stored{SHA256: c.SHA256, Key: c.StorageKey, Path: path, MIME: "video/mp4", Size: c.Size, Seconds: c.Seconds}, models.RefLookClip, l.Name+"/"+c.Ref+"@"+l.Version)
			r.AssetID, r.From, r.To = c.AssetID, c.From, c.To
			if l.Subject == models.LookPerson {
				r.People = []string{l.PersonName}
			}
			p.refs, p.req.Videos = append(p.refs, r), append(p.req.Videos, m)
		}
		if len(l.Clips) > 0 {
			p.refWords = append(p.refWords, fmt.Sprintf("The look “%s”: %s and %s", l.Title, plural(len(l.Stills), "still"), plural(len(l.Clips), "short clip")))
		} else {
			p.refWords = append(p.refWords, fmt.Sprintf("The look “%s”: %s", l.Title, plural(len(l.Stills), "still")))
		}
	}
	// Pictures from the resources library.
	var pics []string
	for _, ref := range in.Images {
		s, r, pinned, err := picture(ctx, a, in.ProjectID, ref)
		if err != nil {
			return nil, err
		}
		tag := fmt.Sprintf("<IMAGE_REF_%d>", len(p.req.Images))
		pics = append(pics, fmt.Sprintf("%s is the picture “%s”", tag, r.Title))
		gr, m := p.mediaRef(s, models.RefImage, pinned)
		gr.ResourceID = r.ID
		p.refs, p.req.Images = append(p.refs, gr), append(p.req.Images, m)
		p.refWords = append(p.refWords, fmt.Sprintf("The picture “%s”", r.Title))
	}
	// The task, and the prompt as sent.
	vars := map[string]string{"prompt": p.prompt}
	name := "plain"
	switch {
	case in.From != nil && in.To != nil:
		p.req.Task, name = videogen.TaskInterpolate, "between"
		p.sent = "The first picture is the first frame of this shot and the second picture is its last frame. " + p.prompt
	case in.From != nil:
		p.req.Task, name = videogen.TaskImageToVideo, "continue"
		p.sent = "The picture is the first frame of this shot; continue it. " + p.prompt
	case len(p.req.Images) > 0 || len(p.req.Videos) > 0:
		p.req.Task = videogen.TaskReferenceToVideo
		p.sent = p.prompt
	default:
		p.req.Task = videogen.TaskTextToVideo
	}
	if l := p.look; l != nil {
		name = "look"
		vars["who"], vars["refs"], vars["description"] = l.Title, strings.Join(tags, " and "), l.Description
		look := fmt.Sprintf("\n\n%s appears in this shot, exactly as in %s", l.Title, vars["refs"])
		if len(l.Clips) > 0 {
			look += " and the reference clips"
		}
		if l.Style == models.LookStylized {
			name, vars["style"] = "look-stylized", l.StyleNote
			look += ", drawn as " + l.StyleNote
		}
		p.sent = p.prompt + look + ". " + l.Description
	}
	if len(pics) > 0 && !strings.Contains(p.prompt, "<IMAGE_REF_") {
		vars["pictures"] = strings.Join(pics, "; ")
		p.sent = strings.TrimSpace(p.sent) + "\n\nPictures: " + vars["pictures"] + "."
	}
	p.tmpl = &models.GenTemplate{Name: name, Vars: vars}
	p.namesInPrompt(ctx, a)
	if err := p.finish(ctx, a); err != nil {
		return nil, err
	}
	return p, nil
}

func clock(t float64) string {
	m := int(t) / 60
	s := t - float64(m*60)
	return fmt.Sprintf("%d:%04.1f", m, s)
}

// planAgain makes new takes of an earlier generation the same way: the same
// prompt as sent and the same pictures and clips (their exact bytes), with
// consent checked again.
func planAgain(ctx context.Context, a *app.App, in *GenAgainIn) (*plan, error) {
	parent, err := resolveGen(ctx, a, in.ID)
	if err != nil {
		return nil, err
	}
	if parent.Operation == models.GenLookTest {
		return nil, apperr.New(apperr.InvalidInput, "A look's test shots are made again from the look itself.")
	}
	st, err := loadSettings(ctx, a, parent.ProjectID)
	if err != nil {
		return nil, err
	}
	now, err := Now(ctx, a)
	if err != nil {
		return nil, err
	}
	p := &plan{op: models.GenAgain, projectID: parent.ProjectID, prompt: parent.Prompt, sent: parent.SentPrompt, tmpl: parent.Template, st: st,
		noPeople: parent.NoPeople, takes: max(1, in.Takes), parent: parent, sources: parent.SourceAssets, now: now, refWords: []string{"The same prompt and pictures as before"}}
	if p.takes > 1 {
		p.op = models.GenVariation
	}
	if p.takes > 4 {
		return nil, apperr.New(apperr.InvalidInput, "Ask for at most 4 takes at once.")
	}
	if parent.Look != nil {
		l, err := findLook(ctx, a, parent.Look.ID)
		if err != nil {
			return nil, err
		}
		p.look, p.lookRef = l, parent.Look
		if l.State == models.LookRevoked {
			p.problems = append(p.problems, fmt.Sprintf("The look “%s” was withdrawn, so no new takes are made with it.", l.Title))
		}
	}
	// Consent again, for every person the earlier take shows.
	for _, id := range parent.ConsentIDs {
		c, err := loadConsent(ctx, a, id)
		if err != nil {
			continue
		}
		p.needConsent(ctx, a, c.PersonID, c.PersonName, "This shot")
	}
	// The same resolution as before, unless another quality or resolution is asked for.
	quality, resolution := parent.Params.Quality, parent.Params.Resolution
	if in.Quality != "" {
		quality, resolution = in.Quality, ""
	}
	if in.Resolution != "" {
		resolution = in.Resolution
	}
	if err := p.resolveSettings(parent.Model, parent.Params.Seconds, parent.Params.AspectRatio, quality, resolution); err != nil {
		return nil, err
	}
	p.req.Task = parent.Params.Task
	for _, r := range parent.References {
		path, err := local(ctx, a, r.StorageKey, r.SHA256, r.Size)
		if err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get a picture this shot was made with from its storage.")
		}
		m := videogen.Media{Role: r.Role, MIME: r.MIME, Path: path, SHA256: r.SHA256, Size: r.Size}
		if strings.HasPrefix(r.MIME, "video/") {
			m.Seconds = math.Max(0.1, r.To-r.From)
			p.req.Videos = append(p.req.Videos, m)
		} else {
			p.req.Images = append(p.req.Images, m)
		}
		p.refs = append(p.refs, r)
	}
	if p.sent == "" {
		p.sent = parent.Prompt
	}
	p.title = clip(parent.Prompt, 60)
	if err := p.finish(ctx, a); err != nil {
		return nil, err
	}
	return p, nil
}

// sourceClip is the clip an extension or an edit works on.
func sourceClip(ctx context.Context, a *app.App, id string) (*models.Asset, *models.Generation, error) {
	var gen *models.Generation
	assetID := id
	if strings.HasPrefix(id, "gen_") {
		g, err := loadGen(ctx, a, id)
		if err != nil {
			return nil, nil, err
		}
		if g, err = videoOnly(g); err != nil { // other AI work isn't a clip to extend or edit
			return nil, nil, err
		}
		if g.Outcome == nil || len(g.Outcome.Outputs) == 0 {
			return nil, nil, apperr.New(apperr.Conflict, "That generated video isn't ready, so there's nothing to work on yet.")
		}
		gen, assetID = g, g.Outcome.Outputs[0].AssetID
	}
	as, err := loadVideo(ctx, a, assetID)
	if err != nil {
		return nil, nil, err
	}
	if gen == nil && as.Source.IsGenerated() {
		gen, _ = resolveGen(ctx, a, as.ID)
	}
	return as, gen, nil
}

// planWork works out an extension or an edit of a clip.
func planWork(ctx context.Context, a *app.App, op, id, prompt, projectID string, from, to *float64, seconds int, quality, resolution string, noPeople bool) (*plan, error) {
	as, gen, err := sourceClip(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if projectID == "" {
		switch {
		case gen != nil && gen.ProjectID != "":
			projectID = gen.ProjectID
		case len(as.ProjectIDs) > 0:
			projectID = as.ProjectIDs[0]
		default:
			return nil, apperr.New(apperr.InvalidInput, "Choose the project the new clip goes to.")
		}
	}
	st, err := loadSettings(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	now, err := Now(ctx, a)
	if err != nil {
		return nil, err
	}
	p := &plan{op: op, projectID: projectID, prompt: strings.TrimSpace(prompt), st: st, noPeople: noPeople, takes: 1, now: now, sources: []string{as.ID}}
	if gen != nil {
		p.parent = gen
		if gen.Look != nil {
			p.lookRef = gen.Look
		}
	}
	params, model := models.GenParams{}, ""
	if gen != nil {
		params, model = gen.Params, gen.Model
	}
	if op == models.GenExtend && seconds == 0 {
		seconds = 6
	}
	if err := p.resolveSettings(model, firstInt(seconds, params.Seconds, int(math.Round(math.Min(as.Media.Duration, 10)))),
		aspectOf(as, params.AspectRatio), firstNonEmpty(quality, params.Quality), resolution); err != nil {
		return nil, err
	}
	maxIn := float64(p.caps.InputVideoMaxSeconds)
	dur := as.Media.Duration
	var a0, a1 float64
	switch op {
	case models.GenExtend:
		a1 = dur
		a0 = math.Max(0, dur-maxIn)
		if from != nil {
			a0 = *from
		}
		p.extendFrom = dur
		p.req.Task = videogen.TaskExtend
		p.sent = "Continue the scene from the end of this clip. " + p.prompt
		if p.prompt == "" {
			p.sent = "Continue the scene from the end of this clip."
		}
		p.title = clip(as.DisplayTitle(), 50) + " (continued)"
		p.refWords = []string{fmt.Sprintf("Continues “%s” from %s", as.DisplayTitle(), clock(a1))}
	case models.GenEdit:
		if p.prompt == "" {
			return nil, apperr.New(apperr.InvalidInput, "Say what to change.")
		}
		if from != nil {
			a0 = *from
		}
		a1 = math.Min(dur, a0+maxIn)
		if to != nil {
			a1 = *to
		}
		if a1-a0 > maxIn+0.05 {
			return nil, apperr.New(apperr.InvalidInput, "%s changes at most %.0f seconds at a time. Pick a part of “%s” that long.", p.caps.Title, maxIn, as.DisplayTitle())
		}
		p.req.Task = videogen.TaskEdit
		p.sent = p.prompt
		p.title = clip(as.DisplayTitle(), 40) + " (changed: " + clip(p.prompt, 30) + ")"
		p.refWords = []string{fmt.Sprintf("Changes %s to %s of “%s”", clock(a0), clock(a1), as.DisplayTitle())}
		p.params.Seconds = int(math.Max(float64(p.caps.MinSeconds), math.Min(float64(p.caps.MaxSeconds), math.Round(a1-a0))))
	}
	if a1-a0 > maxIn+0.05 {
		return nil, apperr.New(apperr.InvalidInput, "%s works on at most %.0f seconds of a clip at a time.", p.caps.Title, maxIn)
	}
	what := fmt.Sprintf("“%s” (%s to %s)", as.DisplayTitle(), clock(a0), clock(a1))
	people := p.whoIsIn(ctx, a, as, a0, a1, what)
	s, err := part(ctx, a, as, a0, a1, false)
	if err != nil {
		return nil, err
	}
	r, m := p.mediaRef(s, models.RefInput, fmt.Sprintf("%s@%.2f-%.2f", as.ID, a0, a1))
	r.AssetID, r.From, r.To, r.People = as.ID, a0, a1, people
	p.refs, p.req.Videos = append(p.refs, r), append(p.req.Videos, m)
	p.tmpl = &models.GenTemplate{Name: op, Vars: map[string]string{"prompt": p.prompt}}
	p.namesInPrompt(ctx, a)
	if err := p.finish(ctx, a); err != nil {
		return nil, err
	}
	return p, nil
}

// aspectOf is the shape of a clip (the generation's, or its picture's).
func aspectOf(as *models.Asset, gen string) string {
	if gen != "" {
		return gen
	}
	if as.Media.Height > as.Media.Width {
		return "9:16"
	}
	return "16:9"
}

// keyFor is this machine's key for the provider, or a plain "not set up".
func keyFor(a *app.App, provider string) (string, error) { return providers.MustKey(a.Cfg, provider) }

// videoModel is a model's provider and ID: "provider/model", a model only
// one provider has (Veo is Vertex AI's, D282), else the usual provider's.
func videoModel(ref string) (provider, model string) {
	ref = strings.TrimSpace(ref)
	if p, m, ok := strings.Cut(ref, "/"); ok {
		return strings.ToLower(strings.TrimSpace(p)), strings.TrimSpace(m)
	}
	for _, pi := range videogen.LoadCatalog().Providers {
		for _, m := range pi.Models {
			if m.ID == ref {
				return pi.Name, ref
			}
		}
	}
	return Provider, ref
}

// nextTake is the next take number in a group of takes made the same way.
func nextTake(ctx context.Context, a *app.App, groupID string) int {
	n, err := a.DB.C(store.CGenerations).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "groupId": groupID})
	if err != nil {
		return 1
	}
	return int(n) + 1
}

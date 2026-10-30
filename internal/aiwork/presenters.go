package aiwork

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// A person's own presenter (D273): begun by a person (the service asked for
// its consent words), made from a training take and the person reading those
// words (priced first, a generation of kind presenter-make), tested with a
// priced clip, approved by a person, and retired by a person or when the
// consent it relies on is withdrawn (D277: deleted at the service).

func init() {
	ops.Def("presenter.list", "List people's own presenters — a person's double at an AI service that reads lines on camera — with whose they are, where each stands and what it needs next", ops.Flags{},
		func(ctx context.Context, a *app.App, in *PresenterListIn) (*PresenterListOut, error) {
			return listPresenters(ctx, a, in)
		})
	ops.Def("presenter.show", "Show a presenter: whose it is, the look and consent it relies on, its training take, its test clips, the clips it read and the cuts that use them", ops.Flags{},
		func(ctx context.Context, a *app.App, in *PresenterIDIn) (*PresenterView, error) {
			pr, err := findPresenter(ctx, a, in.ID)
			if err != nil {
				return nil, err
			}
			v := presenterView(ctx, a, pr, true)
			return &v, nil
		})
	ops.Def("presenter.start", "Begin a person's own presenter from their approved look at an AI service: asks the service for the words the person reads on camera for its consent check (people only)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *PresenterStartIn) (*PresenterOut, error) {
			return startPresenter(ctx, a, in)
		})
	ops.Def("presenter.make", "Make a person's presenter at the service from a training take of them and their reading of its consent words, with their recorded consent naming that service — people only, priced first", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *PresenterMakeIn) (*PresenterOut, error) {
			if in.DryRun {
				ctx = leave.Quiet(ctx) // a dry run: a refusal is said, not recorded (D290)
			}
			return makePresenter(ctx, a, in)
		})
	ops.Def("presenter.test", "Make a test clip of a presenter not yet approved, reading a clip's sound — people only, priced first", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *PresenterTestIn) (*MakeOut, error) {
			if in.DryRun {
				ctx = leave.Quiet(ctx) // a dry run: a refusal is said, not recorded (D290)
			}
			return testPresenter(ctx, a, in)
		})
	ops.Def("presenter.approve", "Approve a presenter after its test clips, so clips can be made with it (people only)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *PresenterDecideIn) (*PresenterOut, error) {
			return approvePresenter(ctx, a, in)
		})
	ops.Def("presenter.retire", "Retire a presenter: nothing more is made with it, it is deleted at the service, clips waiting are stopped, and the cuts using its clips are listed (people only)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *PresenterDecideIn) (*PresenterOut, error) {
			pr, err := findPresenter(ctx, a, in.ID)
			if err != nil {
				return nil, err
			}
			return retirePresenter(ctx, a, pr, firstNonEmpty(in.Note, "A person retired it."))
		})
}

var presenterNameRe = regexp.MustCompile(`[^a-z0-9]+`)

func presenterName(s string) string {
	n := strings.Trim(presenterNameRe.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-"), "-")
	if len(n) > 40 {
		n = strings.Trim(n[:40], "-")
	}
	if n == "" {
		n = "presenter"
	}
	return n
}

// findPresenter finds a presenter by its ID or short name.
func findPresenter(ctx context.Context, a *app.App, ref string) (*models.Presenter, error) {
	ref = strings.TrimSpace(ref)
	var pr models.Presenter
	filter := bson.M{"workspaceId": a.WorkspaceID(), "name": presenterName(ref)}
	if ids.Valid(ids.Presenter, ref) {
		filter = bson.M{"workspaceId": a.WorkspaceID(), "_id": ref}
	}
	if err := a.DB.C(store.CPresenters).FindOne(ctx, filter).Decode(&pr); err != nil {
		return nil, apperr.New(apperr.NotFound, "There's no presenter called “%s”.", ref).WithCommand("metarush presenter list")
	}
	return &pr, nil
}

// freeName is a short name no other presenter has (ada, ada-2…).
func freeName(ctx context.Context, a *app.App, base string) string {
	base = presenterName(base)
	for i := 1; i < 100; i++ {
		n := base
		if i > 1 {
			n = fmt.Sprintf("%s-%d", base, i)
		}
		if c, err := a.DB.C(store.CPresenters).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "name": n}); err == nil && c == 0 {
			return n
		}
	}
	return base + "-" + strings.ToLower(ids.New(ids.Presenter)[4:10])
}

// presenterTerms are a provider's presenter terms, or a plain refusal.
func presenterTerms(provider string) (*aiservices.ProviderInfo, *aiservices.PresenterTerms, error) {
	p, err := aiservices.ProviderOf(provider)
	if err != nil {
		return nil, nil, err
	}
	if p.Presenters == nil {
		return nil, nil, apperr.New(apperr.InvalidInput, "%s doesn't make presenters.", p.Title)
	}
	return p, p.Presenters, nil
}

// personLook is the person's approved look a presenter is made from.
func personLook(ctx context.Context, a *app.App, personID, ref string) (*models.GenLook, error) {
	var l *models.GenLook
	if strings.TrimSpace(ref) != "" {
		found, err := generate.FindLook(ctx, a, ref)
		if err != nil {
			return nil, err
		}
		l = found
	} else {
		var ls []models.GenLook
		_ = a.DB.Find(ctx, store.CLooks, a.WorkspaceID(), bson.M{"personId": personID, "state": models.LookApproved}, &ls,
			options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(1))
		if len(ls) == 0 {
			return nil, apperr.New(apperr.ConsentRequired, "A presenter is made from the person's approved look, and they have none yet.").
				WithHint("Make their look on the People page (with their consent), test it and approve it first.").WithCommand("metarush looks create")
		}
		l = &ls[0]
	}
	switch {
	case l.Subject != models.LookPerson || l.PersonID != personID:
		return nil, apperr.New(apperr.InvalidInput, "The look “%s” isn't this person's.", l.Title)
	case l.State != models.LookApproved:
		return nil, apperr.New(apperr.ConsentRequired, "The look “%s” isn't approved, so no presenter is made from it.", l.Title).WithCommand("metarush looks approve " + l.Name)
	}
	return l, nil
}

func startPresenter(ctx context.Context, a *app.App, in *PresenterStartIn) (*PresenterOut, error) {
	if strings.TrimSpace(in.Again) != "" {
		pr, err := findPresenter(ctx, a, in.Again)
		if err != nil {
			return nil, err
		}
		return presenterWords(ctx, a, pr, in.Language)
	}
	if strings.TrimSpace(in.Person) == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say whose presenter it is: a name from People.")
	}
	person, err := ops.ResolvePerson(ctx, a, in.Person)
	if err != nil {
		return nil, err
	}
	look, err := personLook(ctx, a, person.ID, in.Look)
	if err != nil {
		return nil, err
	}
	now, err := planNow(ctx, a)
	if err != nil {
		return nil, err
	}
	c, err := choose(ctx, a, in.ProjectID, aiservices.KindPresenterMake, in.Model, now)
	if err != nil {
		return nil, err
	}
	m := c.model
	p, terms, err := presenterTerms(m.Provider)
	if err != nil {
		return nil, err
	}
	ad, err := aiservices.Get(m.Provider, aiservices.KindPresenterMake)
	if err != nil {
		return nil, err
	}
	if terms.ConsentMode == aiservices.ConsentHostedPage {
		return nil, apperr.New(apperr.NotConfigured, "%s checks consent on its own page, which MetaRush can't take people through yet, so it doesn't make presenters there.", p.Title)
	}
	key, err := keyFor(a, m.Provider)
	if err != nil {
		return nil, apperr.New(apperr.NotConfigured, "%s isn't set up on this computer yet. Add the key in Settings → Integrations, then begin the presenter here.", p.Title).
			WithCommand("metarush providers set " + p.Name)
	}
	if err := trainingAllowed(ctx, a, p, key, person.Name+"'s face and voice"); err != nil {
		return nil, err
	}
	pr := &models.Presenter{ID: ids.New(ids.Presenter), WorkspaceID: a.WorkspaceID(), Name: freeName(ctx, a, firstNonEmpty(in.Name, person.Name)),
		Title: firstNonEmpty(in.Title, person.Name), PersonID: person.ID, PersonName: person.Name, LookID: look.ID, LookName: look.Name, LookVersion: look.Version,
		Provider: m.Provider, ProviderTitle: m.ProviderTitle, Model: m.ID, GreenScreen: in.GreenScreen, State: models.PresenterWaiting,
		CreatedBy: a.Actor, CreatedAt: now, UpdatedAt: now}
	if terms.ConsentMode == aiservices.ConsentAPIUpload {
		ch, err := askWords(ctx, a, ad, key, person.Name, in.Language, now)
		if err != nil {
			return nil, err
		}
		pr.Challenge = ch
	}
	if err := a.DB.Insert(ctx, store.CPresenters, pr); err != nil {
		return nil, err
	}
	a.Audit(ctx, "presenter.start", "presenter", pr.ID, in.ProjectID, map[string]any{"person": person.Name, "personId": person.ID, "look": look.Name,
		"lookVersion": look.Version, "provider": pr.Provider, "model": pr.Model, "greenScreen": pr.GreenScreen, "consentWords": pr.Challenge != nil})
	v := presenterView(ctx, a, pr, false)
	summary := fmt.Sprintf("Began %s's presenter at %s. %s", person.Name, p.Title, v.Next)
	if v.Words != "" {
		summary += fmt.Sprintf(" %s's words for %s to read: “%s”", p.Title, person.Name, v.Words)
	}
	return &PresenterOut{Presenter: v, Summary: summary}, nil
}

// askWords asks the service for its consent words, with the person's name put in.
func askWords(ctx context.Context, a *app.App, ad aiservices.Adapter, key, name, language string, now time.Time) (*models.PresenterChallenge, error) {
	pm, ok := ad.(aiservices.PresenterMaker)
	if !ok {
		return nil, apperr.New(apperr.NotConfigured, "MetaRush can't ask %s for its consent words.", ad.Provider())
	}
	ch, err := pm.AskConsent(ctx, key, firstNonEmpty(language, "en"))
	if err != nil {
		return nil, providerErr(err)
	}
	// The words' end is kept in the database server's time (D360): the
	// adapter says it by this computer's clock, and whichever computer makes
	// the presenter or shows the words compares it with the server's.
	exp := ch.ExpiresAt
	if exp != nil {
		srv, err := generate.Now(ctx, a)
		if err != nil {
			return nil, err
		}
		e := srv.Add(time.Until(*exp))
		exp = &e
	}
	return &models.PresenterChallenge{ID: ch.ID, Words: ch.Words(name), Language: ch.Language, AskedAt: now, ExpiresAt: exp, Machine: a.Cfg.Installation.Name}, nil
}

// providerErr says a provider's error in MetaRush's terms.
func providerErr(err error) error {
	var ve interface{ AppError() *apperr.Error }
	if errors.As(err, &ve) {
		return ve.AppError()
	}
	return err
}

// presenterWords asks the service for fresh consent words for a presenter
// still waiting (the old ones expired). People only, through presenter.start's ID.
func presenterWords(ctx context.Context, a *app.App, pr *models.Presenter, language string) (*PresenterOut, error) {
	if pr.State != models.PresenterWaiting && pr.State != models.PresenterFailed {
		return nil, apperr.New(apperr.Conflict, "“%s” isn't waiting for its consent reading.", pr.Title)
	}
	_, terms, err := presenterTerms(pr.Provider)
	if err != nil {
		return nil, err
	}
	if terms.ConsentMode != aiservices.ConsentAPIUpload {
		return nil, apperr.New(apperr.InvalidInput, "%s has no consent words to read.", pr.ProviderTitle)
	}
	ad, err := aiservices.Get(pr.Provider, aiservices.KindPresenterMake)
	if err != nil {
		return nil, err
	}
	key, err := keyFor(a, pr.Provider)
	if err != nil {
		return nil, err
	}
	now, err := planNow(ctx, a)
	if err != nil {
		return nil, err
	}
	ch, err := askWords(ctx, a, ad, key, pr.PersonName, firstNonEmpty(language, langOf(pr.Challenge)), now)
	if err != nil {
		return nil, err
	}
	if old := pr.Challenge; old != nil && old.ID != "" {
		if pm, ok := ad.(aiservices.PresenterMaker); ok {
			_ = pm.DropConsent(context.WithoutCancel(ctx), key, old.ID) // never answered: nothing of the person's there
		}
	}
	if err := a.DB.Update(ctx, store.CPresenters, a.WorkspaceID(), pr.ID, bson.M{"$set": bson.M{"challenge": ch, "updatedAt": now}}); err != nil {
		return nil, err
	}
	pr.Challenge = ch
	a.Audit(ctx, "presenter.start", "presenter", pr.ID, "", map[string]any{"newWords": true})
	v := presenterView(ctx, a, pr, false)
	return &PresenterOut{Presenter: v, Summary: fmt.Sprintf("New consent words from %s for %s to read: “%s” %s", pr.ProviderTitle, pr.PersonName, v.Words, v.Next)}, nil
}

func langOf(c *models.PresenterChallenge) string {
	if c == nil {
		return ""
	}
	for code, name := range map[string]string{"en": "english", "es": "spanish", "fr": "french", "de": "german", "it": "italian", "pt": "portuguese", "nl": "dutch"} {
		if c.Language == name {
			return code
		}
	}
	return ""
}

// presenterConsent is the consent a presenter relies on: in force for the
// project, covering presenters, naming the service, with a recording (D274).
func presenterConsent(ctx context.Context, a *app.App, pr *models.Presenter, consentID, projectID string, now time.Time) (*models.Consent, string) {
	var c *models.Consent
	if consentID != "" {
		got, err := generate.LoadConsent(ctx, a, consentID)
		if err != nil {
			return nil, apperr.From(err).Message
		}
		c = got
	} else {
		var cs []models.Consent
		_ = a.DB.Find(ctx, store.CConsents, a.WorkspaceID(), bson.M{"personId": pr.PersonID, "revokedAt": bson.M{"$exists": false}}, &cs,
			options.Find().SetSort(bson.D{{Key: "recordedAt", Value: -1}}))
		for i := range cs {
			if cs[i].CoversUse(models.ConsentPresenters) && cs[i].NamesProvider(pr.Provider) {
				c = &cs[i]
				break
			}
		}
	}
	need := fmt.Sprintf("A presenter of %s at %s needs %s's recorded consent that covers a presenter and names %s. Record it on the People page: a recording of %s saying so, in their own words.",
		pr.PersonName, pr.ProviderTitle, pr.PersonName, pr.ProviderTitle, pr.PersonName)
	switch {
	case c == nil:
		return nil, need
	case c.PersonID != pr.PersonID:
		return nil, fmt.Sprintf("That consent is %s's, not %s's.", c.PersonName, pr.PersonName)
	case !c.Active(now):
		return nil, fmt.Sprintf("%s's consent was withdrawn or has ended.", c.PersonName)
	case projectID != "" && !c.Covers(projectID, now):
		return nil, fmt.Sprintf("%s's consent doesn't cover this project.", c.PersonName)
	case !c.CoversUse(models.ConsentPresenters):
		return nil, fmt.Sprintf("%s's consent doesn't cover a presenter. ", c.PersonName) + need
	case !c.NamesProvider(pr.Provider):
		return nil, fmt.Sprintf("%s's consent doesn't name %s as a service that may keep their recording. ", c.PersonName, pr.ProviderTitle) + need
	case c.RecordingID == "":
		return nil, fmt.Sprintf("A presenter needs %s's consent recorded on camera, not a typed statement. ", pr.PersonName) + need
	}
	var rec models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), c.RecordingID, &rec); err != nil || !rec.Media.HasVideo {
		return nil, fmt.Sprintf("A presenter needs %s's consent filmed on camera; that consent's recording is sound only. ", pr.PersonName) + need
	}
	return c, ""
}

func makePresenter(ctx context.Context, a *app.App, in *PresenterMakeIn) (*PresenterOut, error) {
	pr, err := findPresenter(ctx, a, in.ID)
	if err != nil {
		return nil, err
	}
	switch pr.State {
	case models.PresenterWaiting, models.PresenterFailed:
	case models.PresenterRetired:
		return nil, apperr.New(apperr.Conflict, "“%s” was retired; begin a new presenter instead.", pr.Title)
	default:
		return nil, apperr.New(apperr.Conflict, "“%s” is made already (%s).", pr.Title, pr.State)
	}
	k, _ := aiservices.KindOf(aiservices.KindPresenterMake)
	now, err := planNow(ctx, a)
	if err != nil {
		return nil, err
	}
	p := &plan{kind: k, projectID: in.ProjectID, now: now, personID: pr.PersonID}
	if err := p.base(ctx, a, pr.Provider+"/"+pr.Model); err != nil {
		return nil, err
	}
	prov, terms, err := presenterTerms(pr.Provider)
	if err != nil {
		return nil, err
	}
	if key, err := keyFor(a, pr.Provider); err == nil {
		if err := trainingAllowed(ctx, a, prov, key, pr.PersonName+"'s face and voice"); err != nil {
			return nil, err
		}
	}
	// The look it was begun with, still approved and unchanged.
	var look models.GenLook
	if err := a.DB.GetByID(ctx, store.CLooks, a.WorkspaceID(), pr.LookID, &look); err != nil || look.State != models.LookApproved {
		p.consent = append(p.consent, fmt.Sprintf("The look “%s” this presenter is made from was withdrawn; begin a new presenter from an approved look.", pr.LookName))
	} else if look.Version != pr.LookVersion {
		p.consent = append(p.consent, fmt.Sprintf("The look “%s” changed since this presenter was begun; begin a new presenter from it.", pr.LookName))
	}
	// Consent (D274).
	c, why := presenterConsent(ctx, a, pr, in.Consent, in.ProjectID, p.now)
	if c != nil {
		p.addConsent(c)
	} else {
		p.consent = append(p.consent, why)
	}
	// The consent recording is the person: their named voice, and faces named
	// as them (D274).
	if c != nil {
		var rec models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), c.RecordingID, &rec); err == nil {
			p.speaksAlone(ctx, a, &rec, 0, rec.Media.Duration, pr.PersonID, pr.PersonName, "the consent recording")
			p.facesArePerson(ctx, a, &rec, 0, rec.Media.Duration, pr.PersonID, pr.PersonName, "the consent recording", false)
		}
	}
	// The person reading the service's consent words (D274, D227's check).
	words := ""
	if terms.ConsentMode == aiservices.ConsentAPIUpload {
		ch := pr.Challenge
		switch {
		case ch == nil:
			return nil, apperr.New(apperr.Conflict, "MetaRush hasn't asked %s for its consent words yet.", pr.ProviderTitle).WithCommand("metarush presenter start --again " + pr.Name)
		case ch.ExpiresAt != nil && !p.now.Add(2*time.Minute).Before(*ch.ExpiresAt):
			return nil, apperr.New(apperr.Conflict, "%s's consent words from %s have expired (they last %d minutes). Ask for new ones, record the reading again, then make the presenter.",
				pr.ProviderTitle, ch.AskedAt.Local().Format("15:04"), max(terms.ConsentMinutes, 1)).WithCommand("metarush presenter start --again " + pr.Name)
		}
		words = ch.Words
		reading := strings.TrimSpace(in.Reading)
		if reading == "" && c != nil {
			reading = c.RecordingID // the reading is part of the consent recording (D274)
		}
		if reading != "" && c != nil && reading != c.RecordingID {
			return nil, apperr.New(apperr.ConsentRequired, "%s's words must be read in the same recording as %s's consent, so it is plain the person who agreed is the one reading them. Choose the part of the consent recording where they read them.",
				pr.ProviderTitle, pr.PersonName)
		}
		if reading == "" {
			p.consent = append(p.consent, fmt.Sprintf("%s wants a recording of %s reading its consent words: “%s” Choose the part of the consent recording where they read them.",
				pr.ProviderTitle, pr.PersonName, words))
		} else {
			rs, rfrom, rto, err := clipPart(ctx, a, reading, in.ReadingFrom, in.ReadingTo)
			if err != nil {
				return nil, err
			}
			if rs.Source.IsGenerated() {
				return nil, apperr.New(apperr.ConsentRequired, "A generated clip can't be someone's consent. Use a recording of the person themselves.")
			}
			if !rs.Media.HasVideo {
				p.consent = append(p.consent, fmt.Sprintf("%s wants to see %s reading its words: choose a video clip, not sound alone.", pr.ProviderTitle, pr.PersonName))
			}
			p.inputs = append(p.inputs, input{role: aiservices.RoleConsent, assetID: rs.ID, title: rs.DisplayTitle(), from: rfrom, to: rto, clip: true})
			p.touch(rs)
			p.req.Inputs = append(p.req.Inputs, aiservices.Media{Role: aiservices.RoleConsent, MIME: "video/mp4", Seconds: rto - rfrom})
			p.sources = append(p.sources, rs.ID)
			p.words = append(p.words, "the reading of "+pr.ProviderTitle+"'s consent words in "+partWords(rs, rfrom, rto))
			p.speaksAlone(ctx, a, rs, rfrom, rto, pr.PersonID, pr.PersonName, "the reading")
			p.saysScript(ctx, a, rs, rfrom, rto, pr.PersonName, words)
			pr.Reading = &models.PresenterTake{AssetID: rs.ID, Title: rs.DisplayTitle(), From: rfrom, To: rto, Seconds: rto - rfrom}
		}
	}
	// The training take: the person, on their own, speaking to the camera.
	ts, tfrom, tto, err := clipPart(ctx, a, in.Take, in.TakeFrom, in.TakeTo)
	if err != nil {
		return nil, err
	}
	if ts.Source.IsGenerated() {
		return nil, apperr.New(apperr.ConsentRequired, "A generated clip can't be a presenter's training take. Use a recording of the person themselves.")
	}
	if !ts.Media.HasVideo {
		return nil, apperr.New(apperr.InvalidInput, "The training take must be a video of the person, not sound alone.")
	}
	p.inputs = append(p.inputs, input{role: aiservices.RoleFootage, assetID: ts.ID, title: ts.DisplayTitle(), from: tfrom, to: tto, clip: true})
	p.touch(ts)
	p.req.Inputs = append(p.req.Inputs, aiservices.Media{Role: aiservices.RoleFootage, MIME: "video/mp4", Seconds: tto - tfrom})
	p.sources = append(p.sources, ts.ID)
	p.words = append(p.words, "the training take "+partWords(ts, tfrom, tto))
	p.speaksAlone(ctx, a, ts, tfrom, tto, pr.PersonID, pr.PersonName, "the training take")
	p.facesArePerson(ctx, a, ts, tfrom, tto, pr.PersonID, pr.PersonName, "the training take", in.ShowsPerson)
	pr.Take = &models.PresenterTake{AssetID: ts.ID, Title: ts.DisplayTitle(), From: tfrom, To: tto, Seconds: tto - tfrom}
	if p.adapter != nil {
		if err := linksPossible(a, p.adapter); err != nil {
			p.problems = append(p.problems, apperr.From(err).Message)
		}
	}
	// Presenter names the record, so the job finds it before trainingId is set.
	p.req.Options = models.GenOptions{Name: pr.PersonName, GreenScreen: pr.GreenScreen, Presenter: pr.ID}
	if pr.Challenge != nil {
		p.req.Options.ConsentID = pr.Challenge.ID
	}
	if err := generate.RulesAllow(ctx, a, p.model.Provider, append([]string{p.projectID}, p.touched...)); err != nil {
		return nil, err
	}
	if err := kindsAllow(ctx, a, aiservices.KindPresenterMake, append([]string{p.projectID}, p.touched...)); err != nil {
		return nil, err
	}
	if err := p.mayLeave(ctx, a); err != nil { // What may leave (D290): the person's face and voice, on video
		return nil, err
	}
	if err := p.finish(ctx, a); err != nil {
		return nil, err
	}
	p.alts = nil // a person's presenter is made at the service the consent names
	p.notes = append(p.notes, terms.TakeAdvice)
	// Claimed before anything is sent (check-and-set): two makes of one
	// presenter never both go, and the job — which may end before this
	// returns — finds it training and writes how it went.
	if !in.DryRun {
		res, err := a.DB.C(store.CPresenters).UpdateOne(ctx, bson.M{"_id": pr.ID, "workspaceId": a.WorkspaceID(), "state": pr.State},
			bson.M{"$set": bson.M{"state": models.PresenterTraining, "updatedAt": time.Now().UTC()}, "$unset": bson.M{"problem": ""}})
		if err != nil {
			return nil, err
		}
		if res.MatchedCount == 0 {
			return nil, apperr.New(apperr.Conflict, "“%s” changed meanwhile (it is being made, or was retired); look again.", pr.Title).WithCommand("metarush presenter show " + pr.Name)
		}
	}
	run, err := start(ctx, a, p, in.MaxUSD, in.DryRun, "presenter:"+pr.ID)
	if err != nil {
		if !in.DryRun { // nothing was sent: as it was
			back := bson.M{"state": pr.State, "updatedAt": time.Now().UTC()}
			if pr.Problem != "" {
				back["problem"] = pr.Problem
			}
			_, _ = a.DB.C(store.CPresenters).UpdateOne(context.WithoutCancel(ctx), bson.M{"_id": pr.ID, "workspaceId": a.WorkspaceID(), "state": models.PresenterTraining},
				bson.M{"$set": back})
		}
		return nil, err
	}
	if makeStarted != nil && run.JobID != "" {
		makeStarted(run.JobID)
	}
	est := run.Estimate
	out := &PresenterOut{Estimate: &est, DryRun: run.DryRun, JobID: run.JobID}
	if run.DryRun {
		out.Presenter = presenterView(ctx, a, pr, false)
		out.Summary = est.Summary
		return out, nil
	}
	// What it was made from; the state stays as the job left it.
	set := bson.M{"consentId": p.consentIDs[0], "take": pr.Take, "trainingId": run.Work.ID, "updatedAt": time.Now().UTC()}
	if pr.Reading != nil {
		set["reading"] = pr.Reading
	}
	if _, err := a.DB.C(store.CPresenters).UpdateOne(ctx, bson.M{"_id": pr.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": set}); err != nil {
		return nil, err
	}
	fresh, _ := findPresenter(ctx, a, pr.ID)
	if fresh != nil {
		pr = fresh
	}
	out.Presenter = presenterView(ctx, a, pr, false)
	out.Summary = fmt.Sprintf("Sent %s's training take to %s (about %s). %s makes the presenter in a few minutes; then test it and approve it.",
		pr.PersonName, pr.ProviderTitle, generate.Money(est.USD), pr.ProviderTitle)
	return out, nil
}

// makeStarted, when set (tests), runs right after a presenter's making is
// queued and before makePresenter records it: the job may end first.
var makeStarted func(jobID string)

func testPresenter(ctx context.Context, a *app.App, in *PresenterTestIn) (*MakeOut, error) {
	pr, err := findPresenter(ctx, a, in.ID)
	if err != nil {
		return nil, err
	}
	if pr.State != models.PresenterDraft && pr.State != models.PresenterApproved {
		return nil, apperr.New(apperr.Conflict, "“%s” isn't ready to test (%s).", pr.Title, pr.State)
	}
	mk := &MakeIn{Kind: aiservices.KindPresenter, ProjectID: in.ProjectID, Presenter: pr.ID, Clip: in.Clip, From: in.From, To: in.To, MaxUSD: in.MaxUSD, DryRun: in.DryRun}
	p, err := planPresenter(ctx, a, mk, true)
	if err != nil {
		return nil, err
	}
	out, err := start(ctx, a, p, in.MaxUSD, in.DryRun, "presenter-test:"+pr.ID)
	if err != nil || out.DryRun || out.Work == nil {
		return out, err
	}
	_ = a.DB.Update(ctx, store.CPresenters, a.WorkspaceID(), pr.ID, bson.M{"$push": bson.M{"tests": models.PresenterTest{GenerationID: out.Work.ID, At: time.Now().UTC()}},
		"$set": bson.M{"updatedAt": time.Now().UTC()}})
	out.Summary = fmt.Sprintf("Asked %s for a test clip of “%s” (about %s). Watch it, then approve the presenter.", pr.ProviderTitle, pr.Title, generate.Money(out.Estimate.USD))
	return out, nil
}

func approvePresenter(ctx context.Context, a *app.App, in *PresenterDecideIn) (*PresenterOut, error) {
	pr, err := findPresenter(ctx, a, in.ID)
	if err != nil {
		return nil, err
	}
	now, err := planNow(ctx, a)
	if err != nil {
		return nil, err
	}
	switch pr.State {
	case models.PresenterApproved:
		return &PresenterOut{Presenter: presenterView(ctx, a, pr, false), Summary: "It was approved already."}, nil
	case models.PresenterDraft:
	default:
		return nil, apperr.New(apperr.Conflict, "“%s” can't be approved: %s", pr.Title, lowerFirst(stateWords(pr, now)))
	}
	if c, why := presenterConsent(ctx, a, pr, pr.ConsentID, "", now); c == nil {
		return nil, apperr.New(apperr.ConsentRequired, "%s", why)
	}
	tested := false
	for _, t := range pr.Tests {
		if g, err := loadGen(ctx, a, t.GenerationID); err == nil && g.Outcome != nil && g.Outcome.State == models.GenDone {
			tested = true
		}
	}
	if !tested {
		return nil, apperr.New(apperr.Conflict, "Test “%s” first: make a test clip of it reading a line (priced first) and watch it.", pr.Title).WithCommand("metarush presenter test " + pr.Name + " --clip <clip>")
	}
	actor := a.Actor
	res, err := a.DB.C(store.CPresenters).UpdateOne(ctx, bson.M{"_id": pr.ID, "workspaceId": a.WorkspaceID(), "state": models.PresenterDraft},
		bson.M{"$set": bson.M{"state": models.PresenterApproved, "approvedAt": now, "approvedBy": actor, "updatedAt": now}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, apperr.New(apperr.Conflict, "“%s” changed meanwhile; look again.", pr.Title)
	}
	pr.State, pr.ApprovedAt, pr.ApprovedBy = models.PresenterApproved, &now, &actor
	a.Audit(ctx, "presenter.approve", "presenter", pr.ID, "", map[string]any{"name": pr.Name, "person": pr.PersonName, "provider": pr.Provider, "note": in.Note})
	return &PresenterOut{Presenter: presenterView(ctx, a, pr, false), Summary: fmt.Sprintf("Approved “%s”. It can read lines in clips now, priced first like any AI work.", pr.Title)}, nil
}

// stateWords says where a presenter stands; now is the database server's
// time, as the consent words' end is (askWords).
func stateWords(pr *models.Presenter, now time.Time) string {
	switch pr.State {
	case models.PresenterWaiting:
		if pr.Challenge != nil && pr.Challenge.ExpiresAt != nil && now.After(*pr.Challenge.ExpiresAt) {
			return fmt.Sprintf("Waiting for %s's training take; %s's consent words have expired, so ask for new ones first.", pr.PersonName, pr.ProviderTitle)
		}
		return fmt.Sprintf("Waiting for %s's training take.", pr.PersonName)
	case models.PresenterTraining:
		return pr.ProviderTitle + " is making it."
	case models.PresenterDraft:
		return "Made; test it, then approve it."
	case models.PresenterApproved:
		return fmt.Sprintf("Approved: it reads lines as %s.", pr.PersonName)
	case models.PresenterFailed:
		return firstNonEmpty(pr.Problem, pr.ProviderTitle+" couldn't make it.")
	case models.PresenterRetired:
		at := ""
		if pr.RetiredAt != nil {
			at = " on " + pr.RetiredAt.Local().Format("2 Jan 2006")
		}
		if pr.Removed != nil {
			return fmt.Sprintf("Retired%s and deleted at %s.", at, pr.ProviderTitle)
		}
		return fmt.Sprintf("Retired%s; being deleted at %s by a computer with its key.", at, pr.ProviderTitle)
	}
	return pr.State
}

// presenterView is a presenter with what people read about it.
func presenterView(ctx context.Context, a *app.App, pr *models.Presenter, detail bool) PresenterView {
	now := a.DB.NowOrLocal(ctx) // the consent words end by the server's clock (askWords)
	v := PresenterView{Presenter: *pr, Summary: fmt.Sprintf("“%s”, %s's presenter at %s. %s", pr.Title, pr.PersonName, pr.ProviderTitle, stateWords(pr, now)), Link: links.Person(pr.PersonID)}
	_, terms, _ := presenterTerms(pr.Provider)
	switch pr.State {
	case models.PresenterWaiting, models.PresenterFailed:
		if terms != nil {
			v.TakeAdvice = terms.TakeAdvice
		}
		ch := pr.Challenge
		fresh := ch != nil && (ch.ExpiresAt == nil || now.Before(*ch.ExpiresAt))
		if fresh {
			v.Words, v.WordsUntil = ch.Words, ch.ExpiresAt
		}
		switch {
		case ch != nil && !fresh:
			v.Next = fmt.Sprintf("%s's consent words have expired: ask for new ones, then record the reading within %d minutes.", pr.ProviderTitle, max(1, termsMinutes(terms)))
		case ch != nil:
			until := ""
			if ch.ExpiresAt != nil {
				until = ", before " + ch.ExpiresAt.Local().Format("15:04")
			}
			v.Next = fmt.Sprintf("Film %s on camera saying, in their own words, that they agree to a presenter of them kept at %s, then reading %s's words%s. Record that as their consent (covering a presenter, naming %s), film a training take, then make the presenter.",
				pr.PersonName, pr.ProviderTitle, pr.ProviderTitle, until, pr.ProviderTitle)
		default:
			v.Next = fmt.Sprintf("Record %s's consent on camera (covering a presenter, naming %s), film a training take, then make the presenter.", pr.PersonName, pr.ProviderTitle)
		}
	case models.PresenterDraft:
		v.Next = "Make a test clip (priced first), watch it, then approve it."
	case models.PresenterRetired:
		if terms != nil {
			v.Erasure = terms.Erasure
		}
	}
	if c, err := generate.LoadConsent(ctx, a, pr.ConsentID); err == nil && pr.ConsentID != "" {
		if c.RevokedAt != nil {
			v.ConsentSummary = "The consent it relied on was withdrawn."
		} else {
			v.ConsentSummary = "Relies on " + c.PersonName + "'s recorded consent."
		}
	}
	if pr.ProviderID != "" {
		filter := bson.M{"kind": aiservices.KindPresenter, "options.presenter": pr.ProviderID, "outcome.state": models.GenDone}
		if n, err := a.DB.C(store.CGenerations).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "kind": aiservices.KindPresenter, "options.presenter": pr.ProviderID,
			"outcome.state": models.GenDone}); err == nil {
			v.Clips = int(n)
		}
		if detail {
			var gs []models.Generation
			_ = a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), filter, &gs, options.Find().SetLimit(200))
			v.UsedIn = cutsOf(ctx, a, gs)
		}
	}
	if detail && pr.TrainingID != "" {
		if g, err := loadGen(ctx, a, pr.TrainingID); err == nil {
			w := views(ctx, a, []models.Generation{*g})[0]
			v.Training = &w
		}
	}
	return v
}

func termsMinutes(t *aiservices.PresenterTerms) int {
	if t == nil {
		return 30
	}
	return t.ConsentMinutes
}

// cutsOf are the cuts that use clips some AI work made (asset.where).
func cutsOf(ctx context.Context, a *app.App, gs []models.Generation) []ops.UseRow {
	var out []ops.UseRow
	seen := map[string]bool{}
	for _, g := range gs {
		if g.Outcome == nil {
			continue
		}
		for _, o := range g.Outcome.Outputs {
			if o.AssetID == "" {
				continue
			}
			res, err := ops.CallTyped[ops.AssetWhereOut](ctx, a, "asset.where", ops.AssetRefIn{AssetID: o.AssetID}, ops.CallerFrom(ctx))
			if err != nil {
				continue
			}
			for _, u := range res.Uses {
				if (u.Kind == "cut" || u.Kind == "nested") && !seen[u.ProjectID+"/"+u.Branch+"/"+u.Kind] {
					seen[u.ProjectID+"/"+u.Branch+"/"+u.Kind] = true
					out = append(out, u)
				}
			}
		}
	}
	return out
}

func listPresenters(ctx context.Context, a *app.App, in *PresenterListIn) (*PresenterListOut, error) {
	filter := bson.M{}
	if strings.TrimSpace(in.Person) != "" {
		p, err := ops.ResolvePerson(ctx, a, in.Person)
		if err != nil {
			return nil, err
		}
		filter["personId"] = p.ID
	}
	if !in.Retired {
		filter["state"] = bson.M{"$ne": models.PresenterRetired}
	}
	var ps []models.Presenter
	if err := a.DB.Find(ctx, store.CPresenters, a.WorkspaceID(), filter, &ps, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(200)); err != nil {
		return nil, err
	}
	out := &PresenterListOut{Presenters: []PresenterView{}}
	approved := 0
	for i := range ps {
		v := presenterView(ctx, a, &ps[i], false)
		if ps[i].State == models.PresenterApproved {
			approved++
		}
		out.Presenters = append(out.Presenters, v)
	}
	switch len(ps) {
	case 0:
		out.Summary = "No presenters yet."
	default:
		out.Summary = fmt.Sprintf("%s, %d approved.", count(len(ps), "presenter", "presenters"), approved)
	}
	return out, nil
}

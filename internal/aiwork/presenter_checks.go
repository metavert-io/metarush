package aiwork

import (
	"context"
	"fmt"
	"log"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
)

// What a presenter needs, checked when it is asked for and again just before
// anything is sent (D273, D274, D276, D277).

// --- whose face, whose voice ---

// sentPerson is a named person whose face or voice a request sends.
type sentPerson struct {
	id, name    string
	face, voice bool
	minor       bool // a child's voice nobody may consent for
}

func (s sentPerson) words() string {
	if s.minor {
		return "a child's voice"
	}
	switch {
	case s.face && s.voice:
		return s.name + "'s face and voice"
	case s.face:
		return s.name + "'s face"
	case s.voice:
		return s.name + "'s voice"
	}
	return s.name + "'s face or voice"
}

func sentWords(ss []sentPerson) string {
	var ws []string
	for _, s := range ss {
		ws = append(ws, s.words())
	}
	switch len(ws) {
	case 0:
		return "a person's face or voice"
	case 1:
		return ws[0]
	}
	return strings.Join(ws[:len(ws)-1], ", ") + " and " + ws[len(ws)-1]
}

// sends notes a named person whose face or voice the request sends.
func (p *plan) sends(s sentPerson) {
	for i := range p.sent {
		if s.id != "" && p.sent[i].id == s.id {
			p.sent[i].face = p.sent[i].face || s.face
			p.sent[i].voice = p.sent[i].voice || s.voice
			return
		}
	}
	p.sent = append(p.sent, s)
}

// trainingGate (D276): a named person's face or voice goes to a service that
// trains its models on what it is sent only when the account can opt out,
// the opt-out is recorded for the key this computer uses (checked again on
// the computer that sends it), and the person's consent names the service.
func (p *plan) trainingGate(ctx context.Context, a *app.App) error {
	if p.provider == nil || len(p.sent) == 0 {
		return nil
	}
	trains := p.provider.Data.TrainsOnUploads
	if trains != aiservices.TrainsYes && trains != aiservices.TrainsUnlessOptedOut {
		return nil
	}
	who := sentWords(p.sent)
	if trains == aiservices.TrainsYes {
		return trainingAllowed(ctx, a, p.provider, "", who)
	}
	for _, s := range slices.Clone(p.sent) {
		switch c := consentNaming(ctx, a, s, p.projectID, p.provider.Name, p.now); {
		case s.minor:
			p.consent = append(p.consent, fmt.Sprintf("A child speaks in it. MetaRush doesn't send a child's voice to %s, which trains its models on what it is sent.", p.provider.Title))
		case c == nil:
			p.consent = append(p.consent, fmt.Sprintf("%s would go to %s, which trains its models on what it is sent unless the account opts out: that takes %s's recorded consent naming %s.",
				upperFirst(s.words()), p.provider.Title, s.name, p.provider.Title))
		default:
			p.addConsent(c)
		}
	}
	if key, err := keyFor(a, p.provider.Name); err == nil {
		return trainingAllowed(ctx, a, p.provider, key, who)
	}
	return nil
}

// consentNaming is a person's consent in force that names a service and
// covers what is sent of them (nil: none).
func consentNaming(ctx context.Context, a *app.App, s sentPerson, projectID, provider string, now time.Time) *models.Consent {
	if s.id == "" {
		return nil
	}
	var cs []models.Consent
	_ = a.DB.Find(ctx, store.CConsents, a.WorkspaceID(), bson.M{"personId": s.id, "revokedAt": bson.M{"$exists": false}}, &cs,
		options.Find().SetSort(bson.D{{Key: "recordedAt", Value: -1}}))
	for i := range cs {
		c := &cs[i]
		switch {
		case !c.Active(now), projectID != "" && !c.Covers(projectID, now), !c.NamesProvider(provider):
		case s.voice && !c.CoversUse(models.ConsentVoice) && !c.CoversUse(models.ConsentPresenters):
		case s.face && !c.CoversUse(models.ConsentPresenters) && !c.CoversUse(models.ConsentShots):
		default:
			return c
		}
	}
	return nil
}

// heardVoice is a person whose voice a sound carries.
type heardVoice struct {
	personID, name string
	minor          bool
	copy           bool // AI speech in a copy of their voice
}

// whoseVoice says whose voices a stretch of sound carries (D273): AI speech
// in a voice copy is its person's and in a library voice no one's; a dub or
// a presenter clip carries the voices of the sound it was made from; a
// recording, the voices heard in it as people named them. unsure says why
// MetaRush can't tell whose voice it is ("" when it can).
func whoseVoice(ctx context.Context, a *app.App, as *models.Asset, from, to float64, voice string, depth int) (who []heardVoice, unsure string) {
	if voice != "" {
		return voiceCopyPeople(ctx, a, voice), ""
	}
	if as.Source.IsGenerated() {
		cant := fmt.Sprintf("MetaRush can't tell whose voice speaks in “%s”, a generated clip.", as.DisplayTitle())
		gen := as.Source.Generated
		if depth > 3 || gen == nil || gen.GenerationID == "" {
			return nil, cant
		}
		g, err := loadGen(ctx, a, gen.GenerationID)
		if err != nil {
			return nil, cant
		}
		if g.Options != nil && strings.TrimSpace(g.Options.Voice) != "" {
			return voiceCopyPeople(ctx, a, g.Options.Voice), ""
		}
		switch g.Kind {
		case aiservices.KindDubbing, aiservices.KindPresenter:
			// The voices of the sound it was made from (a dub keeps them).
			found := false
			for _, ref := range g.References {
				if ref.AssetID == "" {
					continue
				}
				var src models.Asset
				if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), ref.AssetID, &src) != nil {
					return nil, cant
				}
				found = true
				w, u := whoseVoice(ctx, a, &src, ref.From, max(ref.To, ref.From+0.1), "", depth+1)
				if u != "" {
					unsure = u
				}
				who = append(who, w...)
			}
			if !found {
				return nil, cant
			}
			return who, unsure
		}
		return nil, "" // music, sound effects, a generated shot: no one's voice
	}
	vs, err := speakers.Voices(ctx, a, as.ID, false)
	switch {
	case err == nil && len(vs) == 0 && as.Analysis["transcribe"] == "available":
		return nil, "" // listened to: nobody speaks in it
	case err != nil || len(vs) == 0:
		return nil, fmt.Sprintf("MetaRush hasn't told apart the voices in “%s” yet.", as.DisplayTitle())
	}
	heard := heardLabels(ctx, a, as.ID, from, to)
	seen := map[string]bool{}
	for _, v := range vs {
		if heard != nil && !heard[v.Label] {
			continue
		}
		switch {
		case v.Minor:
			who = append(who, heardVoice{name: "a child", minor: true})
		case v.PersonID == "":
			unsure = fmt.Sprintf("Nobody has named a voice in “%s” yet.", as.DisplayTitle())
		case v.Source != models.SpeakerByPerson:
			unsure = fmt.Sprintf("MetaRush only recognized a voice in “%s” as %s's; name it there to confirm.", as.DisplayTitle(), firstNonEmpty(v.Name, "someone"))
		case !v.NamedByPerson():
			// Named, but not by a person (D274): it doesn't count.
			unsure = fmt.Sprintf("A voice in “%s” was named as %s by %s, not by a person; a person names it there to confirm.", as.DisplayTitle(), firstNonEmpty(v.Name, "someone"), v.Naming.Words())
		case !seen[v.PersonID]:
			seen[v.PersonID] = true
			who = append(who, heardVoice{personID: v.PersonID, name: v.Name})
		}
	}
	return who, unsure
}

// voiceCopyPeople are the people a voice copy MetaRush made is of (none: a
// voice from a library, no one's).
func voiceCopyPeople(ctx context.Context, a *app.App, voice string) []heardVoice {
	var vc models.Generation
	if err := a.DB.C(store.CGenerations).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "kind": aiservices.KindVoiceCopy, "outcome.outputs.voiceId": voice}).Decode(&vc); err != nil {
		return nil
	}
	var out []heardVoice
	for _, id := range vc.ConsentIDs {
		if c, err := generate.LoadConsent(ctx, a, id); err == nil {
			out = append(out, heardVoice{personID: c.PersonID, name: c.PersonName, copy: true})
		}
	}
	if len(out) == 0 {
		out = append(out, heardVoice{name: "someone", copy: true}) // a copy of a voice whose person MetaRush can't name
	}
	return out
}

// presenterVoice checks the voice a person's presenter speaks with (D273):
// only their own — a recording in which every voice heard is named as
// them, or AI speech in a copy of their voice — or AI speech in a voice that
// is no one's. A stock presenter may read anyone's; the people whose voices
// it carries are noted (a service that trains on it needs their consent).
func (p *plan) presenterVoice(ctx context.Context, a *app.App, as *models.Asset, from, to float64, pr *models.Presenter, voice string) {
	if voice == "" && as.Source != nil && as.Source.Generated != nil && as.Source.Generated.GenerationID != "" {
		if g, err := loadGen(ctx, a, as.Source.Generated.GenerationID); err == nil && g.Options != nil {
			voice = strings.TrimSpace(g.Options.Voice)
		}
	}
	if voice != "" {
		p.voiceCopyInUse(ctx, a, voice) // its consent, when it is a copy of someone's voice
	}
	who, unsure := whoseVoice(ctx, a, as, from, to, voice, 0)
	if pr != nil {
		p.sends(sentPerson{id: pr.PersonID, name: pr.PersonName, face: true})
		rule := fmt.Sprintf("“%s” speaks only %s's own voice or AI speech in a voice that is no one's", pr.Title, pr.PersonName)
		for _, w := range who {
			if w.personID != pr.PersonID {
				whose := w.name + "'s voice"
				switch {
				case w.minor:
					whose = "a child's voice"
				case w.copy:
					whose = "a copy of " + w.name + "'s voice"
				}
				p.consent = append(p.consent, fmt.Sprintf("%s; this is %s.", rule, whose))
				return
			}
		}
		if unsure != "" {
			p.consent = append(p.consent, fmt.Sprintf("%s. %s Name %s's voice there first.", rule, unsure, pr.PersonName))
			return
		}
	}
	for _, w := range who {
		p.sends(sentPerson{id: w.personID, name: w.name, voice: true, minor: w.minor})
	}
}

// --- faces ---

// facesArePerson checks a stretch of video shows the person and no one else
// (D274): every face the on-screen index found is named as them — a face
// MetaRush only recognized must be named first, one that looks like someone
// else refuses. said (a person saying it shows them) counts only where
// MetaRush hasn't looked for faces.
func (p *plan) facesArePerson(ctx context.Context, a *app.App, as *models.Asset, from, to float64, personID, name, what string, said bool) {
	f := generate.FacesSeen(ctx, a, as, from, to)
	if !f.Known {
		if !said {
			p.consent = append(p.consent, fmt.Sprintf("MetaRush hasn't looked for faces in %s (“%s”) yet, so it can't tell it shows %s.", what, as.DisplayTitle(), name))
		}
		return
	}
	for _, n := range f.Named {
		if n.PersonID != personID {
			p.consent = append(p.consent, fmt.Sprintf("%s shows %s, not only %s. Pick one where %s is on screen alone.", upperFirst(what), n.Name, name, name))
			return
		}
	}
	for _, r := range f.Resemble {
		if r.PersonID != personID {
			p.consent = append(p.consent, fmt.Sprintf("A face in %s looks like %s, not %s. Pick one where %s is on screen alone.", what, r.Name, name, name))
			return
		}
	}
	switch {
	case len(f.AppNamed) > 0:
		// Named, but not by a person (D274): an AI app, Auto or Claude can't vouch for whose face it is.
		p.consent = append(p.consent, fmt.Sprintf("A face in %s (“%s”) was named as %s by %s, not by a person. A person names it there to confirm it.",
			what, as.DisplayTitle(), f.AppNamed[0].Name, f.AppNamed[0].By))
	case f.Unnamed > 0:
		p.consent = append(p.consent, fmt.Sprintf("MetaRush can't tell that every face in %s (“%s”) is %s's: name them there first.", what, as.DisplayTitle(), name))
	case len(f.Named) == 0:
		p.consent = append(p.consent, fmt.Sprintf("MetaRush found no face in %s (“%s”).", what, as.DisplayTitle()))
	default:
		p.notes = append(p.notes, fmt.Sprintf("%s's face in %s was named by %s.", name, what, faceNamer(ctx, a, f.Named[0].FaceID)))
	}
}

// faceNamer says who named a face, for the notes.
func faceNamer(ctx context.Context, a *app.App, faceID string) string {
	var f models.FaceTrack
	if err := a.DB.GetByID(ctx, store.CFaces, a.WorkspaceID(), faceID, &f); err != nil {
		return "a person"
	}
	if f.Naming == nil && f.ConfirmedBy != nil {
		return firstNonEmpty(f.ConfirmedBy.UserName, "a person") + " in the dashboard"
	}
	return f.Naming.Words()
}

// --- just before sending, and when it comes back ---

// presenterOf is the person's presenter the work makes or reads with. own
// is false when it involves none (other work, a stock presenter).
func (r *runner) presenterOf(ctx context.Context) (pr *models.Presenter, own bool) {
	g := r.g
	f := bson.M{"workspaceId": r.a.WorkspaceID()}
	switch g.Kind {
	case aiservices.KindPresenterMake:
		or := bson.A{bson.M{"trainingId": g.ID}}
		if g.Options != nil && g.Options.Presenter != "" {
			or = append(or, bson.M{"_id": g.Options.Presenter})
		}
		f["$or"] = or
	case aiservices.KindPresenter:
		if g.Options == nil || g.Options.Stock || g.Options.Presenter == "" {
			return nil, false
		}
		f["providerId"], f["provider"] = g.Options.Presenter, g.Provider
	default:
		return nil, false
	}
	var got models.Presenter
	if err := r.a.DB.C(store.CPresenters).FindOne(ctx, f).Decode(&got); err != nil {
		return nil, true
	}
	return &got, true
}

// presenterHolds checks the person's presenter the work makes or reads with
// is still there to use: not retired, its look not withdrawn (D277).
func (r *runner) presenterHolds(ctx context.Context) error {
	pr, own := r.presenterOf(ctx)
	switch {
	case !own:
		return nil
	case pr == nil && r.g.Kind == aiservices.KindPresenter:
		return apperr.New(apperr.ConsentRequired, "The presenter it reads with isn't in this workspace any more.")
	case pr == nil:
		return nil
	case pr.State == models.PresenterRetired:
		return apperr.New(apperr.ConsentRequired, "“%s” was retired, so nothing more is made with it.", pr.Title)
	}
	var look models.GenLook
	if err := r.a.DB.GetByID(ctx, store.CLooks, r.a.WorkspaceID(), pr.LookID, &look); err != nil || look.State == models.LookRevoked {
		return apperr.New(apperr.ConsentRequired, "The look “%s” behind “%s” was withdrawn.", pr.LookName, pr.Title)
	}
	return nil
}

// trainingHolds checks again, on the computer with the key, that a named
// person's face or voice goes to a service that trains on what it is sent
// only with its opt-out recorded for this key (D276).
func (r *runner) trainingHolds(ctx context.Context) error {
	g := r.g
	p, err := aiservices.ProviderOf(g.Provider)
	if err != nil {
		return nil
	}
	if t := p.Data.TrainsOnUploads; t != aiservices.TrainsYes && t != aiservices.TrainsUnlessOptedOut {
		return nil
	}
	var who []sentPerson
	for _, id := range g.ConsentIDs {
		if c, err := generate.LoadConsent(ctx, r.a, id); err == nil && !slices.ContainsFunc(who, func(s sentPerson) bool { return s.id == c.PersonID }) {
			who = append(who, sentPerson{id: c.PersonID, name: c.PersonName})
		}
	}
	if len(who) == 0 && g.Kind != aiservices.KindPresenterMake {
		return nil
	}
	key, err := keyFor(r.a, g.Provider)
	if err != nil {
		return nil // run ends it: no key here
	}
	return trainingAllowed(ctx, r.a, p, key, sentWords(who))
}

// lostClip: a presenter clip whose answer was lost may have been made all the
// same, and the sound sent for it kept. The clip is looked up at the service
// by the tag in its name and deleted, with the files the request uploaded
// (D277) — again for a while (LostVoiceLookFor) while nothing is found, as a
// lost presenter is: errLookAgain says not found yet. A clip with a ticket is
// deleted by it (finish).
func (r *runner) lostClip(ctx context.Context, key string) error {
	g := r.g
	if g.Kind != aiservices.KindPresenter || g.Forgotten != nil || g.Ticket != nil || key == "" || r.ad == nil || g.Outcome == nil ||
		!(lostAnswer(g) || len(g.Sending) > len(g.Sends)) {
		return nil
	}
	bg, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Minute)
	defer cancel()
	var files []string
	for _, s := range g.Sends {
		files = append(files, s.Files...)
	}
	f := models.GenForgotten{At: time.Now().UTC(), OK: true}
	var problems []string
	if len(files) > 0 { // the sound it uploaded: deleted now, whatever is found
		if err := r.ad.Forget(bg, key, &aiservices.Ticket{Files: files}, nil); err != nil {
			problems = append(problems, firstLine(err.Error()))
		}
	}
	tc, ok := r.ad.(aiservices.TagCleaner)
	looked := time.Since(g.Outcome.At)
	switch {
	case !ok:
		f.OK, f.Kept = false, true
		f.Detail = fmt.Sprintf("No answer had come back, and MetaRush can't look for the clip at %s by its name: if it was made, it stays there.", g.ProviderTitle)
	default:
		n, err := tc.ForgetTagged(bg, key, voiceTag(g))
		switch {
		case err != nil && looked < LostVoiceLookFor:
			return errLookAgain // couldn't look just now
		case err != nil:
			problems = append(problems, firstLine(err.Error()))
		case n > 0:
			f.Detail = fmt.Sprintf("No answer had come back; %s found at %s under its name and deleted.", count(n, "clip was", "clips were"), g.ProviderTitle)
		case looked < LostVoiceLookFor:
			return errLookAgain // not found yet: it may show up late
		default:
			f.OK = false
			f.Detail = fmt.Sprintf("No answer had come back; MetaRush looked for the clip at %s under its name for %d minutes and found none, so it was most likely never made.",
				g.ProviderTitle, int(LostVoiceLookFor.Minutes()))
		}
	}
	if len(problems) > 0 {
		f.OK, f.Detail = false, strings.TrimSpace(f.Detail+" "+strings.Join(problems, "; "))
	}
	if err := generate.SetOnce(bg, r.a, g.ID, "forgotten", f); err == nil {
		g.Forgotten = &f
	}
	return nil
}

// retireLapsed retires the presenters whose consent has ended — its last day
// passed, withdrawn while nothing was listening, or its record gone (the
// daemon's sweep; a withdrawal retires them at once, D277). A consent that
// couldn't be read just now (the database didn't answer, MetaRush is
// stopping) retires nothing: it is skipped, said in the log, and looked at
// again on the next sweep.
func retireLapsed(ctx context.Context, a *app.App) int {
	var ps []models.Presenter
	if err := a.DB.Find(ctx, store.CPresenters, a.WorkspaceID(), bson.M{"state": bson.M{"$ne": models.PresenterRetired}, "consentId": bson.M{"$nin": bson.A{nil, ""}}}, &ps); err != nil {
		return 0
	}
	now := time.Now().UTC()
	type loaded struct {
		c   *models.Consent
		err error
	}
	consents := map[string]loaded{}
	n := 0
	for i := range ps {
		if ctx.Err() != nil {
			return n // stopping: nothing more is decided now
		}
		l, ok := consents[ps[i].ConsentID]
		if !ok {
			l.c, l.err = generate.LoadConsent(ctx, a, ps[i].ConsentID)
			consents[ps[i].ConsentID] = l
		}
		reason := ""
		switch {
		case l.err != nil && !generate.ConsentGone(l.err):
			log.Printf("aiwork: presenter %s kept for now: its consent %s couldn't be read: %v", ps[i].ID, ps[i].ConsentID, l.err)
			continue
		case l.err != nil:
			reason = "The consent record it relied on is gone."
		case l.c.RevokedAt != nil:
			reason = "The consent it relied on was withdrawn."
		case !l.c.Active(now):
			reason = "The consent it relied on has ended."
		default:
			continue
		}
		if _, err := retirePresenter(ctx, a, &ps[i], reason); err == nil {
			n++
		}
	}
	return n
}

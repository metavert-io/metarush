package generate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
)

// Consent extends the voice-copy rule (D44) to a person's likeness in
// generated video (D126): your own needs a statement only you can make (a
// person, never Claude or a script); anyone else's needs their own recorded
// consent — a sound or video clip of them saying so — with what it covers and
// until when; either can be revoked at any time. MetaRush never records
// consent for a child or a public figure, and without consent a request that
// shows someone fails with E_CONSENT_REQUIRED.

// ConsentRecordIn records a person's consent. People only.
type ConsentRecordIn struct {
	Person    string   `json:"person" jsonschema:"minLength=1,maxLength=120,description=Whose likeness: a name or ID from People (a new name adds them)."`
	Self      bool     `json:"self" jsonschema:"description=It is your own likeness."`
	Statement string   `json:"statement" jsonschema:"minLength=20,maxLength=2000,description=The consent in the person's own words\\, e.g. 'I\\, Ada\\, agree to MetaRush making videos with my likeness for my own channel.'"`
	Recording string   `json:"recording,omitempty" jsonschema:"pattern=^(ast_[0-9a-z]{26})?$,description=Anyone else's likeness: a sound or video clip of them giving their consent."`
	Projects  []string `json:"projects,omitempty" jsonschema:"maxItems=50,description=Only these projects (default: every project)."`
	Note      string   `json:"note,omitempty" jsonschema:"maxLength=500,description=Anything they limited it to\\, in their words."`
	ExpiresOn string   `json:"expiresOn,omitempty" jsonschema:"pattern=^(\\d{4}-\\d{2}-\\d{2})?$,description=The last day it covers (YYYY-MM-DD)\\, to its end in TimeZone. Someone else's consent ends after a year unless you say."`
	// TimeZone is where the last day ends (an IANA name such as
	// America/Los_Angeles; the dashboard sends the browser's): default, this
	// computer's.
	TimeZone string `json:"timeZone,omitempty" jsonschema:"maxLength=64,description=The time zone whose end of the last day it covers (e.g. America/Los_Angeles; default: this computer's)."`
	Adult    bool   `json:"adult" jsonschema:"description=You confirm the person is 18 or older."`
	// PublicFigure: MetaRush refuses the likeness of public figures.
	PublicFigure bool `json:"publicFigure,omitempty"`
	// Uses are what it covers (D274, D279, D309): shots, voice, presenters,
	// lip-sync, new-words (default: shots and voice). A presenter needs a
	// filmed recording, the person's own too; a presenter, lip sync and new
	// words each need a service named.
	Uses []string `json:"uses,omitempty" jsonschema:"maxItems=5,description=What it covers: shots\\, voice\\, presenters\\, lip-sync (their lips matched to new sound)\\, new-words (a word they said changed in their own voice copy) (default: shots and voice)."`
	// Providers are the AI services that may keep the recording and the
	// footage, or match their lips (D274, D279), as the recording says.
	Providers []string `json:"providers,omitempty" jsonschema:"maxItems=10,description=The AI services that may keep the recording and footage of the person\\, match their lips or say new words in their voice copy (d-id\\, sync\\, elevenlabs…)\\, as the recording says."`
	// Services is Providers by another name (lip sync's first spelling, D279).
	Services []string `json:"services,omitempty" jsonschema:"maxItems=10,description=The same as providers."`
}

// ConsentView is a consent record with its status in plain words.
type ConsentView struct {
	models.Consent
	Status  string   `json:"status" jsonschema:"enum=active,enum=expired,enum=revoked"`
	Summary string   `json:"summary"`
	Looks   []string `json:"looks,omitempty" jsonschema:"description=Looks that rely on it."`
	// VoiceCopies are the voice copies made with it and where each stands
	// (D227): in use, withdrawn and being deleted at the provider, or deleted.
	VoiceCopies []string `json:"voiceCopies,omitempty"`
	// Presenters are the presenters made with it and where each stands (D273).
	Presenters []string `json:"presenters,omitempty"`
	Link       string   `json:"link"`
}

// ConsentListIn lists consent records.
type ConsentListIn struct {
	Person string `json:"person,omitempty" jsonschema:"maxLength=120,description=One person's (name or ID)."`
}

// ConsentListOut lists consent records, newest first.
type ConsentListOut struct {
	Consents []ConsentView `json:"consents"`
	Summary  string        `json:"summary"`
}

// ConsentIDIn names a consent record.
type ConsentIDIn struct {
	ID string `json:"id" jsonschema:"pattern=^cst_[0-9a-z]{26}$"`
}

// ConsentRevokeIn withdraws a consent. People only.
type ConsentRevokeIn struct {
	ID   string `json:"id" jsonschema:"pattern=^cst_[0-9a-z]{26}$"`
	Note string `json:"note,omitempty" jsonschema:"maxLength=500"`
}

// ConsentOut is one consent record after a change.
type ConsentOut struct {
	Consent ConsentView `json:"consent"`
	// LooksRevoked are looks withdrawn because they relied on it.
	LooksRevoked []string `json:"looksRevoked,omitempty"`
	// AlsoWithdrawn are other things that relied on it and were withdrawn
	// (voice copies, deleted at the provider), in plain words.
	AlsoWithdrawn []string `json:"alsoWithdrawn,omitempty"`
	Summary       string   `json:"summary"`
}

func consentRequired(f string, a ...any) *apperr.Error {
	return apperr.New(apperr.ConsentRequired, f, a...)
}

// personFor finds a person by name or ID, adding someone new by name.
func personFor(ctx context.Context, a *app.App, ref string, create bool) (*models.Person, error) {
	ref = strings.TrimSpace(ref)
	p, err := ops.ResolvePerson(ctx, a, ref)
	if err == nil {
		mp := p.Person
		return &mp, nil
	}
	if !create || ids.Valid(ids.Person, ref) || apperr.From(err).Code != apperr.NotFound {
		return nil, err
	}
	// A name close to someone's ("did you mean Maya?") is not someone new.
	if d, ok := apperr.From(err).Details.(map[string]any); ok {
		if s, ok := d["suggestions"].([]string); ok && len(s) > 0 {
			return nil, err
		}
	}
	now := time.Now().UTC()
	np := models.Person{ID: ids.New(ids.Person), WorkspaceID: a.WorkspaceID(), Name: ref, NameKey: speakers.NameKey(ref), CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CPeople, np); err != nil {
		return nil, err
	}
	return &np, nil
}

func recordConsent(ctx context.Context, a *app.App, in *ConsentRecordIn) (*ConsentOut, error) {
	statement := strings.TrimSpace(in.Statement)
	switch {
	case in.PublicFigure:
		return nil, consentRequired("MetaRush doesn't make the likeness of public figures, even with their consent.").
			WithDetails(map[string]any{"reason": "public_figure"})
	case !in.Adult:
		return nil, consentRequired("MetaRush never makes the likeness of a child. Confirm the person is 18 or older to record their consent.").
			WithDetails(map[string]any{"reason": "minor"})
	case len([]rune(statement)) < 20:
		who := strings.TrimSpace(in.Person)
		return nil, consentRequired("Consent needs a statement in the person's own words.").
			WithHint(`For example: "I, ` + who + `, agree to MetaRush making videos with my likeness for my own projects."`).WithDetails(map[string]any{"reason": "statement"})
	case !in.Self && in.Recording == "":
		return nil, consentRequired("This is someone else's likeness: MetaRush only uses it with their own recorded consent.").
			WithHint("Add a sound or video clip of them saying they agree, then choose it as their consent recording.").WithDetails(map[string]any{"reason": "recording"})
	}
	uses, providers, err := consentUses(in)
	if err != nil {
		return nil, err
	}
	p, err := personFor(ctx, a, in.Person, true)
	if err != nil {
		return nil, err
	}
	if in.Recording != "" {
		var rec models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.Recording, &rec); err != nil || !rec.Media.HasAudio {
			return nil, consentRequired("The consent recording must be a sound or video clip in this workspace, with the person speaking.").WithDetails(map[string]any{"reason": "recording"})
		}
		if rec.Source.IsGenerated() {
			return nil, consentRequired("A generated clip can't be someone's consent. Use a recording of the person themselves.").WithDetails(map[string]any{"reason": "recording"})
		}
		if slices.Contains(uses, models.ConsentPresenters) && !rec.Media.HasVideo {
			return nil, consentRequired("A presenter's consent must be filmed: the recording shows the person agreeing, and this one is sound only.").
				WithHint("Film them saying, in their own words, that they agree to a presenter of them, then choose that video as the consent recording.").
				WithDetails(map[string]any{"reason": "recording_video"})
		}
	}
	// The database server's time (D360): a consent's year runs from it, and
	// every computer says whether it is in force by the same clock.
	now, err := Now(ctx, a)
	if err != nil {
		return nil, err
	}
	c := models.Consent{ID: ids.New(ids.Consent), WorkspaceID: a.WorkspaceID(), PersonID: p.ID, PersonName: p.Name, Self: in.Self, Statement: statement,
		RecordingID: in.Recording, Scope: models.ConsentScope{AllProjects: len(in.Projects) == 0, Projects: in.Projects, Note: strings.TrimSpace(in.Note)},
		Uses: uses, Providers: providers, Adult: true, RecordedBy: a.Actor, RecordedAt: now, Machine: a.Cfg.Installation.Name}
	for _, pid := range in.Projects {
		var pr models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), pid, &pr); err != nil {
			return nil, apperr.New(apperr.NotFound, "One of the projects it should cover isn't in this workspace.").WithDetails(map[string]any{"projectId": pid})
		}
	}
	switch {
	case in.ExpiresOn != "":
		// The last day ends at midnight where the person recording it is, not in
		// UTC on whichever computer looks (D274): the zone is kept with it.
		loc, zone, err := consentZone(in.TimeZone)
		if err != nil {
			return nil, err
		}
		d, err := time.ParseInLocation("2006-01-02", in.ExpiresOn, loc)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "Give the last day it covers as a date like 2027-10-20.")
		}
		end := d.AddDate(0, 0, 1).Add(-time.Second).UTC()
		if !end.After(now) {
			return nil, apperr.New(apperr.InvalidInput, "That day has passed.")
		}
		c.ExpiresAt, c.ExpiresZone = &end, zone
	case !in.Self:
		end := now.AddDate(1, 0, 0)
		c.ExpiresAt = &end
	}
	if err := a.DB.Insert(ctx, store.CConsents, c); err != nil {
		return nil, err
	}
	a.Audit(ctx, "consent.record", "consent", c.ID, "", map[string]any{"person": p.Name, "personId": p.ID, "self": in.Self, "statement": statement,
		"recording": in.Recording, "projects": in.Projects, "expiresAt": c.ExpiresAt, "adult": true, "uses": c.UsesOf(), "providers": providers})
	v := consentView(ctx, a, c)
	return &ConsentOut{Consent: v, Summary: fmt.Sprintf("Recorded %s consent. %s", possessive(p.Name, in.Self), v.Summary)}, nil
}

// consentUses checks what a consent covers and the services it names
// (D274, D279): shots, voice, presenters, lip-sync. A presenter needs a
// recording of the person, their own likeness too, and a service; lip sync
// needs a service; services are named only with what they may do.
func consentUses(in *ConsentRecordIn) ([]string, []string, error) {
	var uses []string
	presenters, lipSync, newWords := false, false, false
	for _, u := range in.Uses {
		u = strings.ToLower(strings.TrimSpace(u))
		switch u {
		case "presenter":
			u = models.ConsentPresenters
		case "lipsync", "lip sync":
			u = models.ConsentUseLipSync
		case "new words", "newwords", "words":
			u = models.ConsentUseNewWords
		}
		if !slices.Contains(models.ConsentUses, u) {
			return nil, nil, apperr.New(apperr.InvalidInput, "A consent covers shots, voice, presenters, lip-sync (their lips matched to new sound) or new-words (a word they said changed in their own voice copy); “%s” isn't something it covers.", u)
		}
		if !slices.Contains(uses, u) {
			uses = append(uses, u)
		}
		presenters = presenters || u == models.ConsentPresenters
		lipSync = lipSync || u == models.ConsentUseLipSync
		newWords = newWords || u == models.ConsentUseNewWords
	}
	var providers []string
	for _, name := range append(append([]string{}, in.Providers...), in.Services...) {
		if strings.TrimSpace(name) == "" {
			continue
		}
		pi, err := aiservices.ProviderOf(name)
		if err != nil {
			return nil, nil, apperr.New(apperr.InvalidInput, "MetaRush knows no AI service called “%s”.", strings.TrimSpace(name))
		}
		if !slices.Contains(providers, pi.Name) {
			providers = append(providers, pi.Name)
		}
	}
	switch {
	case presenters && in.Recording == "":
		return nil, nil, consentRequired("A presenter needs a recording of the person giving their consent on camera — their own likeness too, not a typed statement.").
			WithHint("Record them saying, in their own words, that they agree to a presenter of them, and which services may keep the recording; then choose it as the consent recording.").
			WithDetails(map[string]any{"reason": "recording"})
	case presenters && len(providers) == 0:
		return nil, nil, consentRequired("Say which AI services may keep the recording and the footage of the person, as the recording says (for example D-ID).").
			WithDetails(map[string]any{"reason": "providers"})
	case lipSync && len(providers) == 0:
		return nil, nil, apperr.New(apperr.InvalidInput, "Name the AI service it may go to for that (for lip sync: sync).")
	case newWords && len(providers) == 0:
		return nil, nil, apperr.New(apperr.InvalidInput, "Name the AI service that keeps their voice copy, for new words in it (such as elevenlabs).")
	case newWords && !slices.Contains(uses, models.ConsentVoice):
		// New words are said in a copy of their voice (D309): the consent covers their voice too, or it covers nothing.
		return nil, nil, apperr.New(apperr.InvalidInput, "New words are said in a copy of their voice, so a consent for new words covers their voice too: add voice.")
	case len(uses) == 0 && len(providers) > 0:
		return nil, nil, apperr.New(apperr.InvalidInput, "Say what the services may do with it (presenters, lip-sync, new-words).")
	}
	return uses, providers, nil
}

func possessive(name string, self bool) string {
	if self {
		return "your"
	}
	if strings.HasSuffix(name, "s") {
		return name + "'"
	}
	return name + "'s"
}

func consentStatus(c *models.Consent, now time.Time) string {
	switch {
	case c.RevokedAt != nil:
		return "revoked"
	case c.ExpiresAt != nil && !now.Before(*c.ExpiresAt):
		return "expired"
	}
	return "active"
}

func consentView(ctx context.Context, a *app.App, c models.Consent) ConsentView {
	now := a.DB.NowOrLocal(ctx) // a consent ends by the database server's time (D360, D368)
	v := ConsentView{Consent: c, Status: consentStatus(&c, now), Link: links.Person(c.PersonID)}
	var ls []models.GenLook
	_ = a.DB.Find(ctx, store.CLooks, a.WorkspaceID(), bson.M{"consentId": c.ID}, &ls)
	for _, l := range ls {
		v.Looks = append(v.Looks, l.Title)
	}
	var vcs []models.Generation
	_ = a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"kind": "voice-copy", "consentIds": c.ID,
		"$or": bson.A{bson.M{"outcome.state": models.GenDone}, bson.M{"withdrawn": bson.M{"$exists": true}}}}, &vcs)
	for _, g := range vcs {
		name := "A voice copy"
		if g.Options != nil && g.Options.Name != "" {
			name = "“" + g.Options.Name + "”"
		}
		switch {
		case g.Removed != nil:
			v.VoiceCopies = append(v.VoiceCopies, fmt.Sprintf("%s: withdrawn and deleted at %s.", name, g.ProviderTitle))
		case g.Withdrawn != nil:
			v.VoiceCopies = append(v.VoiceCopies, fmt.Sprintf("%s: withdrawn; being deleted at %s by a computer with its key.", name, g.ProviderTitle))
		default:
			v.VoiceCopies = append(v.VoiceCopies, fmt.Sprintf("%s: a copy of the voice at %s.", name, g.ProviderTitle))
		}
	}
	var ps []models.Presenter
	_ = a.DB.Find(ctx, store.CPresenters, a.WorkspaceID(), bson.M{"consentId": c.ID}, &ps)
	for _, pr := range ps {
		switch {
		case pr.State == models.PresenterRetired && pr.Removed != nil:
			v.Presenters = append(v.Presenters, fmt.Sprintf("“%s”: retired and deleted at %s.", pr.Title, pr.ProviderTitle))
		case pr.State == models.PresenterRetired:
			v.Presenters = append(v.Presenters, fmt.Sprintf("“%s”: retired; being deleted at %s by a computer with its key.", pr.Title, pr.ProviderTitle))
		default:
			v.Presenters = append(v.Presenters, fmt.Sprintf("“%s”: a presenter at %s (%s).", pr.Title, pr.ProviderTitle, pr.State))
		}
	}
	whose := c.PersonName + "'s likeness"
	if c.Self {
		whose = c.PersonName + "'s own likeness (recorded by them)"
	}
	scope := "every project"
	if !c.Scope.AllProjects {
		scope = plural(len(c.Scope.Projects), "project")
	}
	switch v.Status {
	case "revoked":
		v.Summary = fmt.Sprintf("Consent for %s was withdrawn on %s.", whose, c.RevokedAt.Format("2 Jan 2006"))
	case "expired":
		v.Summary = fmt.Sprintf("Consent for %s ended on %s.", whose, lastDay(&c))
	default:
		v.Summary = fmt.Sprintf("Consent for %s covers %s", whose, scope)
		if c.ExpiresAt != nil {
			v.Summary += " until " + lastDay(&c)
		}
		v.Summary += ", for " + usesWords(c.UsesOf()) + "."
		if len(c.Providers) > 0 {
			var names []string
			for _, n := range c.Providers {
				if pi, err := aiservices.ProviderOf(n); err == nil {
					names = append(names, pi.Title)
				} else {
					names = append(names, n)
				}
			}
			v.Summary += " " + strings.Join(names, " and ") + " may keep the recording."
		}
	}
	return v
}

func listConsents(ctx context.Context, a *app.App, in *ConsentListIn) (*ConsentListOut, error) {
	filter := bson.M{}
	if in.Person != "" {
		p, err := personFor(ctx, a, in.Person, false)
		if err != nil {
			return nil, err
		}
		filter["personId"] = p.ID
	}
	var cs []models.Consent
	if err := a.DB.Find(ctx, store.CConsents, a.WorkspaceID(), filter, &cs, options.Find().SetSort(bson.D{{Key: "recordedAt", Value: -1}})); err != nil {
		return nil, err
	}
	out := &ConsentListOut{Consents: []ConsentView{}}
	active := 0
	for _, c := range cs {
		v := consentView(ctx, a, c)
		if v.Status == "active" {
			active++
		}
		out.Consents = append(out.Consents, v)
	}
	out.Summary = fmt.Sprintf("%s, %d in force.", plural(len(cs), "consent record"), active)
	if len(cs) == 0 {
		out.Summary = "No consent recorded yet."
	}
	return out, nil
}

// loadConsent loads a consent record. A record that isn't there is
// E_NOT_FOUND; one MetaRush couldn't read just now (the database didn't
// answer, MetaRush is stopping) is E_DATABASE, never taken for a missing or
// ended consent (ConsentGone).
func loadConsent(ctx context.Context, a *app.App, id string) (*models.Consent, error) {
	var c models.Consent
	err := a.DB.GetByID(ctx, store.CConsents, a.WorkspaceID(), id, &c)
	switch {
	case errors.Is(err, store.ErrNotFound):
		return nil, apperr.New(apperr.NotFound, "That consent record isn't in this workspace.").WithDetails(map[string]any{"id": id})
	case err != nil:
		return nil, apperr.Wrap(apperr.Database, err, "MetaRush couldn't read a consent record just now. It tries again by itself.").WithDetails(map[string]any{"id": id})
	}
	return &c, nil
}

// lastDay is the last day a consent covers, in the zone it ends in.
func lastDay(c *models.Consent) string {
	t := c.ExpiresAt.UTC()
	if c.ExpiresZone != "" && c.ExpiresZone != "Local" {
		if loc, err := time.LoadLocation(c.ExpiresZone); err == nil {
			return t.In(loc).Format("2 Jan 2006") + " (" + c.ExpiresZone + ")"
		}
	}
	if c.ExpiresZone == "Local" {
		t = t.Local()
	}
	return t.Format("2 Jan 2006")
}

// consentZone is the time zone a consent's last day ends in: the one asked
// for, else this computer's (its name when the system says it, else "Local").
func consentZone(name string) (*time.Location, string, error) {
	name = strings.TrimSpace(name)
	if name != "" {
		loc, err := time.LoadLocation(name)
		if err != nil {
			return nil, "", apperr.New(apperr.InvalidInput, "MetaRush doesn't know the time zone “%s”. Give one like America/Los_Angeles or Europe/Berlin.", name)
		}
		return loc, loc.String(), nil
	}
	if tz := strings.TrimPrefix(os.Getenv("TZ"), ":"); tz != "" {
		if loc, err := time.LoadLocation(tz); err == nil {
			return loc, loc.String(), nil
		}
	}
	if target, err := os.Readlink("/etc/localtime"); err == nil {
		if i := strings.Index(target, "zoneinfo/"); i >= 0 {
			if loc, err := time.LoadLocation(target[i+len("zoneinfo/"):]); err == nil {
				return loc, loc.String(), nil
			}
		}
	}
	return time.Local, "Local", nil
}

// ConsentGone reports an error that says a consent record isn't there (not
// one MetaRush couldn't read just now).
func ConsentGone(err error) bool { return apperr.Is(err, apperr.NotFound) }

// Unreadable reports an error from not being able to read the database just
// now: whatever relied on it is checked again later, never taken as a refusal.
func Unreadable(err error) bool {
	return apperr.Is(err, apperr.Database) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

func revokeConsent(ctx context.Context, a *app.App, in *ConsentRevokeIn) (*ConsentOut, error) {
	c, err := loadConsent(ctx, a, in.ID)
	if err != nil {
		return nil, err
	}
	if c.RevokedAt != nil {
		return &ConsentOut{Consent: consentView(ctx, a, *c), Summary: "It was withdrawn already."}, nil
	}
	now := time.Now().UTC()
	actor := a.Actor
	_, err = a.DB.C(store.CConsents).UpdateOne(ctx, bson.M{"_id": c.ID, "workspaceId": a.WorkspaceID(), "revokedAt": bson.M{"$exists": false}},
		bson.M{"$set": bson.M{"revokedAt": now, "revokedBy": actor, "revokeNote": strings.TrimSpace(in.Note)}})
	if err != nil {
		return nil, err
	}
	c.RevokedAt, c.RevokedBy, c.RevokeNote = &now, &actor, strings.TrimSpace(in.Note)
	// Looks that rely on it can't be used any more.
	var ls []models.GenLook
	_ = a.DB.Find(ctx, store.CLooks, a.WorkspaceID(), bson.M{"consentId": c.ID, "state": bson.M{"$ne": models.LookRevoked}}, &ls)
	out := &ConsentOut{}
	for _, l := range ls {
		if err := a.DB.Update(ctx, store.CLooks, a.WorkspaceID(), l.ID, bson.M{"$set": bson.M{"state": models.LookRevoked, "revokedAt": now, "revokedBy": actor,
			"revokeNote": "The consent it relied on was withdrawn.", "updatedAt": now}}); err == nil {
			out.LooksRevoked = append(out.LooksRevoked, l.Title)
		}
	}
	out.AlsoWithdrawn = consentRevoked(ctx, a, c)
	a.Audit(ctx, "consent.revoke", "consent", c.ID, "", map[string]any{"person": c.PersonName, "personId": c.PersonID, "note": in.Note, "looksRevoked": out.LooksRevoked,
		"alsoWithdrawn": out.AlsoWithdrawn})
	out.Consent = consentView(ctx, a, *c)
	out.Summary = fmt.Sprintf("Withdrew the consent for %s's likeness. Nothing new will be made with it; clips already made stay, marked as made with consent that was withdrawn.", c.PersonName)
	if n := len(out.LooksRevoked); n > 0 {
		out.Summary += fmt.Sprintf(" %s withdrawn too.", upperFirst(plural(n, "look")))
	}
	for _, w := range out.AlsoWithdrawn {
		out.Summary += " " + w
	}
	return out, nil
}

// usesWords says what a consent covers: "generated shots and their voice".
func usesWords(uses []string) string {
	words := map[string]string{models.ConsentShots: "generated shots", models.ConsentVoice: "their voice", models.ConsentPresenters: "a presenter"}
	var out []string
	for _, u := range uses {
		out = append(out, words[u])
	}
	switch len(out) {
	case 0:
		return "nothing"
	case 1:
		return out[0]
	}
	return strings.Join(out[:len(out)-1], ", ") + " and " + out[len(out)-1]
}

// activeConsent is the consent for generated shots in force for a person in a project ("" = no project).
func activeConsent(ctx context.Context, a *app.App, personID, projectID string, now time.Time) (*models.Consent, error) {
	return activeConsentFor(ctx, a, personID, projectID, models.ConsentShots, now)
}

// activeConsentFor is the newest consent in force for a person in a project
// ("" = no project) that covers a use ("" = any).
func activeConsentFor(ctx context.Context, a *app.App, personID, projectID, use string, now time.Time) (*models.Consent, error) {
	var cs []models.Consent
	if err := a.DB.Find(ctx, store.CConsents, a.WorkspaceID(), bson.M{"personId": personID, "revokedAt": bson.M{"$exists": false}}, &cs,
		options.Find().SetSort(bson.D{{Key: "recordedAt", Value: -1}})); err != nil {
		return nil, err
	}
	for i := range cs {
		if !cs[i].CoversUse(use) {
			continue
		}
		if projectID == "" && cs[i].Active(now) {
			return &cs[i], nil
		}
		if cs[i].Covers(projectID, now) {
			return &cs[i], nil
		}
	}
	return nil, errNoConsent
}

var errNoConsent = errors.New("no consent")

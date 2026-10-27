package aiservices

import (
	"context"
	"strings"
	"time"
)

// Presenters (D273): two kinds of work share a provider's presenter
// adapter. KindPresenterMake makes a person's own presenter at the provider
// from a training take (and, where the provider checks consent itself, the
// person reading its consent words); KindPresenter has a presenter — a
// person's own, or a stock one the provider licenses — read sound MetaRush
// supplies, so the voice is the one the person consented to. What a provider
// asks for and what its terms say are catalogue data (PresenterTerms).

// Consent modes: how a provider checks that the person agreed to a presenter.
const (
	// ConsentAPIUpload: MetaRush asks the provider for its consent words and
	// sends the recording of the person reading them (D-ID).
	ConsentAPIUpload = "api-upload"
	// ConsentHostedPage: the person records the provider's statement on the
	// provider's own page.
	ConsentHostedPage = "hosted-page"
	// ConsentNone: the provider has no check of its own; MetaRush's is the only one.
	ConsentNone = "none"
)

// Training on uploads, as a provider's terms say (D276).
const (
	TrainsNo             = "no"
	TrainsUnlessOptedOut = "unless-opted-out"
	TrainsYes            = "yes"
)

// ConsentChallenge is a provider's consent request: the words the person
// reads on camera (with its code), until when.
type ConsentChallenge struct {
	ID        string     `json:"id"`
	Text      string     `json:"text"`
	Language  string     `json:"language,omitempty"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`
}

// Words are the challenge's words with the person's name put in where the
// provider leaves a place for it.
func (c *ConsentChallenge) Words(name string) string {
	t := c.Text
	for _, ph := range []string{"[user name]", "[USER NAME]", "[User Name]", "[name]", "{name}"} {
		t = strings.ReplaceAll(t, ph, name)
	}
	return strings.TrimSpace(t)
}

// PresenterMaker is a presenter-make adapter's steps outside one request:
// the provider's consent request, deleting a presenter (withdrawn by a
// person, or because the consent it relied on was withdrawn), and finding
// presenters by MetaRush's tag in their names when an answer was lost (D277).
type PresenterMaker interface {
	// AskConsent asks the provider for its consent words, in a language
	// ("en", "en-US"…), where it checks a recording of them (ConsentAPIUpload).
	AskConsent(ctx context.Context, key, language string) (*ConsentChallenge, error)
	// DropConsent deletes the provider's consent record.
	DropConsent(ctx context.Context, key, id string) error
	// RemovePresenter deletes a presenter the provider made.
	RemovePresenter(ctx context.Context, key, presenterID string) error
	// FindPresenters lists the presenters whose names carry the tag.
	FindPresenters(ctx context.Context, key, tag string) ([]string, error)
}

// TagCleaner is a presenter adapter that finds what a request left at the
// provider by MetaRush's tag (Request.Tag) and deletes it: a clip whose answer
// was lost may have been made, and charged, all the same (D277). It says how
// many it deleted.
type TagCleaner interface {
	ForgetTagged(ctx context.Context, key, tag string) (int, error)
}

// StockPresenter is a provider's ready-made presenter.
type StockPresenter struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Gender      string `json:"gender,omitempty"`
	Preview     string `json:"preview,omitempty" jsonschema:"description=A picture or short video of the presenter at the provider."`
	GreenScreen bool   `json:"greenScreen,omitempty"`
}

// StockLister is a presenter adapter that lists the provider's stock presenters.
type StockLister interface {
	StockPresenters(ctx context.Context, key string) ([]StockPresenter, error)
}

// LinkTaker is an adapter that has the provider download some inputs from
// a link instead of receiving the bytes (D-ID fetches a training take and a
// consent recording itself): it names the roles. MetaRush gives each such
// input a short-lived link to the stored bytes (Media.URL).
type LinkTaker interface {
	LinkRoles() []string
	// LocalProvider: the provider is a test server on this computer, so a
	// link on this computer reaches it (tests and browser runs).
	LocalProvider() bool
}

// StockLicence is what a provider's stock presenters may be used for, as
// its terms say (D275): commercial use, client work and ads, each yes, no,
// plan (it depends on the provider's plan) or unstated.
type StockLicence struct {
	Summary    string `json:"summary"`
	Commercial string `json:"commercial" jsonschema:"enum=yes,enum=no,enum=plan,enum=unstated"`
	ClientWork string `json:"clientWork" jsonschema:"enum=yes,enum=no,enum=plan,enum=unstated"`
	Ads        string `json:"ads" jsonschema:"enum=yes,enum=no,enum=plan,enum=unstated"`
	Source     string `json:"source"`
	Checked    string `json:"checked"`
}

// AdsWords says what the licence says about ads, in plain words ("" when it
// covers them outright).
func (l StockLicence) AdsWords(service string) string {
	switch l.Ads {
	case "yes":
		return ""
	case "plan":
		return service + "'s licence for its stock presenters covers ads only on its paid plans."
	}
	return service + "'s licence for its stock presenters doesn't cover ads."
}

// Offered reports whether the stock presenters may be offered: their licence
// covers commercial use and client work (outright or on paid plans).
func (l StockLicence) Offered() bool {
	ok := func(v string) bool { return v == "yes" || v == "plan" }
	return ok(l.Commercial) && ok(l.ClientWork)
}

// PresenterTerms are what a provider asks of a person's presenter and what
// its terms say (D273–D277), with the day they were read.
type PresenterTerms struct {
	// ConsentMode: api-upload, hosted-page or none.
	ConsentMode string `json:"consentMode" jsonschema:"enum=api-upload,enum=hosted-page,enum=none"`
	// ConsentLanguages maps a language code (en, es…) to the provider's name
	// for it, for its consent words.
	ConsentLanguages map[string]string `json:"consentLanguages,omitempty"`
	// ConsentMinutes is how long the provider's consent words stay valid.
	ConsentMinutes int `json:"consentMinutes,omitempty"`
	// TakeAdvice says how to film the training take, in plain words.
	TakeAdvice string `json:"takeAdvice"`
	// Backgrounds: what can be put behind a presenter (a colour only when
	// its take was filmed against a green screen).
	Backgrounds string `json:"backgrounds"`
	// Deletes says what deleting a presenter there removes.
	Deletes string `json:"deletes"`
	// Erasure is the provider's own route for erasing what it keeps about a
	// person's face and voice — a step only the person can take.
	Erasure string `json:"erasure"`
	// Watermark is what the provider marks clips with ("" none it documents).
	Watermark string       `json:"watermark,omitempty"`
	Stock     StockLicence `json:"stock"`
	Source    string       `json:"source"`
	Checked   string       `json:"checked"`
}

// ConsentLanguage is the provider's name for a language ("" when it has none).
func (t *PresenterTerms) ConsentLanguage(lang string) string {
	if t == nil {
		return ""
	}
	base := strings.ToLower(strings.SplitN(strings.SplitN(strings.TrimSpace(lang), "-", 2)[0], "_", 2)[0])
	if base == "" {
		base = "en"
	}
	return t.ConsentLanguages[base]
}

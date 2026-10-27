// Package reviewlinks is "Share for review": a person sends someone outside
// the workspace — a client, a colleague without an account — an expiring link
// to one version's review copy, with its words (the Script view) if the person
// allows, and a place to leave comments pinned to times (D313–D317).
//
// Reachability (D313): the page is a private, signed, expiring static page
// in the workspace's own media store (S3-compatible or GCS). The link is a
// signed GET of the page; everything inside it is signed for the same time;
// reviewers' browsers write their comments into fixed-size slots through
// signed PUTs, and MetaRush reads the slots back with its own key and keeps
// what they hold as ordinary comments on the version, marked as from
// "<name> (outside)". Nothing reaches this computer: MetaRush only reaches
// out, and the page holds no key and no address of MetaRush.
//
// Links are people's (D314): only a person makes one, owners decide who may
// and for how long, What may leave decides whose things may go (D317), and
// the receipt lists every link. Ending a link — taken back, or past its time —
// deletes its page, its copy of the video and its slots.
package reviewlinks

import (
	"context"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

const (
	// DefaultDays is how long a link lasts unless the person says otherwise;
	// MaxDays the longest a store signs a link (owners may lower it).
	DefaultDays = 7
	MaxDays     = 7
	// Seats is how many reviewers' browsers can comment through one link;
	// SeatSize is each one's slot, written whole every time (padded).
	Seats    = 8
	SeatSize = 128 << 10
	// PerReviewer is the most comments one reviewer's browser leaves;
	// PerHour the most a link takes in an hour (the rest wait in their slots
	// for the next hour); MaxText and MaxName are their lengths (characters).
	PerReviewer = 100
	PerHour     = 60
	MaxText     = 2000
	MaxName     = 60
	// SyncEvery is how often a daemon looks for new comments on live links.
	SyncEvery = 2 * time.Minute
	// lookTimeout is the longest one look at a link's slots runs; syncLease
	// keeps two computers from reading one link's slots at once, and outlasts
	// a look (a look checks it still holds the lease before it writes).
	lookTimeout = 2 * time.Minute
	syncLease   = 3 * time.Minute
	// moveLease holds every link a move deals with while it ends them.
	moveLease = 10 * time.Minute
)

// Gate is the ship gate's word on a version (D298; shipGate, a variable so
// tests can stand in for it): whether it holds the version from going out,
// and why, in plain words. A held version is shared only marked as a draft,
// or by a person sending it anyway with a note.
var Gate func(ctx context.Context, a *app.App, p *models.Project, rev *models.Revision) (*GateWord, error)

// GateWord is what holds a version.
type GateWord struct {
	Held bool
	// Holds are the gate's keys for what holds it; Words say each plainly.
	Holds []string
	Words []string
	// Consent says what it plays whose consent no longer holds (changed
	// words, D309): never shared, draft or not.
	Consent []string
}

// ShareCreateIn makes a review link.
type ShareCreateIn struct {
	ProjectID string `json:"projectId"`
	// Revision is the version (rN or its ID); default the cut's latest.
	Revision string `json:"revision,omitempty"`
	Branch   string `json:"branch,omitempty"`
	// Days is how long the link works (default 7; at most what owners allow).
	Days int `json:"days,omitempty" jsonschema:"minimum=0,maximum=7"`
	// Words shows what is said (the Script view); Speakers how it names who
	// speaks (names, labels "Speaker 1", or none; default labels).
	Words    bool   `json:"words,omitempty"`
	Speakers string `json:"speakers,omitempty" jsonschema:"enum=,enum=names,enum=labels,enum=none"`
	// Download offers the video to download.
	Download bool `json:"download,omitempty"`
	// Title is what the page shows (default the cut's title, else the project's name).
	Title string `json:"title,omitempty" jsonschema:"maxLength=120"`
	// For says who it's for (prefilled as their name on the page).
	For string `json:"for,omitempty" jsonschema:"maxLength=80"`
	// Message is a note the page shows them.
	Message string `json:"message,omitempty" jsonschema:"maxLength=1000"`
	// AsDraft marks the page as a draft for review (not approved yet).
	AsDraft bool `json:"asDraft,omitempty"`
	// Anyway is a person's note sending a version the ship gate holds.
	Anyway string `json:"anyway,omitempty" jsonschema:"maxLength=500"`
}

// SharePlanIn asks what a link would show, and whether it may be made.
type SharePlanIn = ShareCreateIn

// SharePlanOut says what a link would show and whether it may be made: the
// version, its review copy, what leaves, who may share and for how long,
// and anything that stops it, in plain words.
type SharePlanOut struct {
	ProjectID      string `json:"projectId"`
	ProjectName    string `json:"projectName"`
	RevisionID     string `json:"revisionId,omitempty"`
	RevisionNumber int    `json:"revisionNumber,omitempty"`
	// ReviewCopy is the render the link would play ("" none ready).
	ReviewCopy string `json:"reviewCopy,omitempty"`
	Title      string `json:"title"`
	Days       int    `json:"days"`
	MaxDays    int    `json:"maxDays"`
	Ends       string `json:"ends"`
	// Exposes says what the page would show; Kinds as What may leave names them.
	Exposes []string `json:"exposes"`
	Kinds   []string `json:"kinds"`
	// HasWords: the version has words to show.
	HasWords bool `json:"hasWords"`
	// Approved: the version is approved; Held: the ship gate holds it.
	Approved bool     `json:"approved"`
	Held     bool     `json:"held"`
	Holds    []string `json:"holds"`
	// Store says where the page would be kept, in words.
	Store string `json:"store"`
	// May: it may be made now; else Why says what stops it, Code its error code.
	May      bool     `json:"may"`
	Why      string   `json:"why,omitempty"`
	Code     string   `json:"code,omitempty"`
	Hint     string   `json:"hint,omitempty"`
	Warnings []string `json:"warnings"`
	Summary  string   `json:"summary"`
}

// ShareView is a link as people see it.
type ShareView struct {
	Link        models.ReviewLink `json:"link"`
	ProjectName string            `json:"projectName"`
	// Live: it works now. StateWords and Ends say its state and time plainly.
	Live       bool   `json:"live"`
	StateWords string `json:"stateWords"`
	Ends       string `json:"ends"`
	Summary    string `json:"summary"`
}

// ShareOut is one link, and the link to send when it was just made or
// signed again (only people get it).
type ShareOut struct {
	Link        models.ReviewLink `json:"link"`
	ProjectName string            `json:"projectName"`
	Live        bool              `json:"live"`
	StateWords  string            `json:"stateWords"`
	Ends        string            `json:"ends"`
	Summary     string            `json:"summary"`
	// URL is the link to send: the page, signed until the link ends.
	URL      string   `json:"url,omitempty"`
	Warnings []string `json:"warnings"`
}

// ShareListIn lists links: a project's, a version's, or the workspace's.
type ShareListIn struct {
	ProjectID string `json:"projectId,omitempty"`
	Revision  string `json:"revision,omitempty"`
	// All includes links that ended.
	All bool `json:"all,omitempty"`
}

// ShareListOut is the links, newest first.
type ShareListOut struct {
	Links   []ShareView `json:"links"`
	Live    int         `json:"live"`
	Summary string      `json:"summary"`
}

// ShareIDIn names a link.
type ShareIDIn struct {
	ID string `json:"id"`
}

// ShareSyncIn looks for new comments now: one link's, or every live one's.
type ShareSyncIn struct {
	ID string `json:"id,omitempty"`
}

// ShareSyncOut says what came in and what ended.
type ShareSyncOut struct {
	Links    int      `json:"links"`
	Comments int      `json:"comments"`
	Waiting  int      `json:"waiting"`
	Ended    int      `json:"ended"`
	Lines    []string `json:"lines"`
	Summary  string   `json:"summary"`
}

// ShareSettingsOut is the owners' word on review links, and whether this
// workspace's store can hold them.
type ShareSettingsOut struct {
	On         bool          `json:"on"`
	OwnersOnly bool          `json:"ownersOnly"`
	MaxDays    int           `json:"maxDays"`
	UpdatedAt  *time.Time    `json:"updatedAt,omitempty"`
	UpdatedBy  *models.Actor `json:"updatedBy,omitempty"`
	// CanShare: the media store can hold review pages; else Why.
	CanShare bool   `json:"canShare"`
	Why      string `json:"why,omitempty"`
	Store    string `json:"store"`
	// Ended are the links that ended because owners turned links off.
	Ended   []string `json:"ended"`
	Summary string   `json:"summary"`
}

// ShareSettingsSetIn changes them (owners).
type ShareSettingsSetIn struct {
	On         *bool `json:"on,omitempty"`
	OwnersOnly *bool `json:"ownersOnly,omitempty"`
	MaxDays    *int  `json:"maxDays,omitempty" jsonschema:"minimum=1,maximum=7"`
}

// The ship gate (D298) decides for review links too: a version it holds
// from going out (its approval, its review copy's quality check, open
// sign-offs) is shared only marked as a draft, or by a person with a note.
func init() {
	Gate = shipGate
}

func shipGate(ctx context.Context, a *app.App, p *models.Project, rev *models.Revision) (*GateWord, error) {
	g, err := ops.ShipGate(ctx, a, p, rev, "review", nil)
	if err != nil {
		return nil, err
	}
	w := &GateWord{Held: g.On && g.State == ops.GateHeld}
	for _, h := range g.Holds {
		if h.Covered {
			continue
		}
		w.Holds = append(w.Holds, h.Key)
		w.Words = append(w.Words, strings.TrimSuffix(lowerFirst(h.Says), "."))
		if h.Kind == models.HoldConsent {
			w.Consent = append(w.Consent, strings.TrimSuffix(lowerFirst(h.Says), "."))
		}
	}
	return w, nil
}

func lowerFirst(s string) string {
	r := []rune(s)
	if len(r) > 1 && r[0] >= 'A' && r[0] <= 'Z' && !(r[1] >= 'A' && r[1] <= 'Z') {
		r[0] += 'a' - 'A'
	}
	return string(r)
}

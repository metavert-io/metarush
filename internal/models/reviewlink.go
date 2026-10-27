package models

import "time"

// Review links for people outside the workspace (D313–D317): a person sends
// someone without an account an expiring link to one version's review copy,
// its words if the person allows, and a place to leave comments pinned to
// times. The page is kept in the workspace's own media store as a private,
// signed page; MetaRush reads the comments back and keeps them as ordinary
// comments on the version.

// Review link states.
const (
	ReviewLinkLive    = "live"
	ReviewLinkRevoked = "revoked"
	ReviewLinkExpired = "expired"
)

// Speakers on a review link's words: their names as the workspace knows
// them, "Speaker 1, 2…" in the order they first speak, or none.
const (
	SpeakersNames  = "names"
	SpeakersLabels = "labels"
	SpeakersNone   = "none"
)

// ViaOutside marks what someone outside the workspace did through a review
// link: their comments, brought in by MetaRush.
const ViaOutside = "outside"

// ReviewLink is a link a person sent to someone outside the workspace.
type ReviewLink struct {
	ID             string `bson:"_id" json:"id"`
	WorkspaceID    string `bson:"workspaceId" json:"workspaceId"`
	ProjectID      string `bson:"projectId" json:"projectId"`
	RevisionID     string `bson:"revisionId" json:"revisionId"`
	RevisionNumber int    `bson:"revisionNumber" json:"revisionNumber"`
	// RenderID is the review copy it plays (copied into the link's place).
	RenderID string `bson:"renderId" json:"renderId"`
	// Title is what the page shows at the top.
	Title string `bson:"title" json:"title" jsonschema:"maxLength=120"`
	// For says who the person sent it to, in their words.
	For string `bson:"for,omitempty" json:"for,omitempty" jsonschema:"maxLength=80"`
	// Message is a note for the reviewer the page shows.
	Message string `bson:"message,omitempty" json:"message,omitempty" jsonschema:"maxLength=1000"`
	// Words: the page shows what is said (the Script view).
	Words bool `bson:"words" json:"words"`
	// Speakers says how the words name who speaks.
	Speakers string `bson:"speakers" json:"speakers" jsonschema:"enum=names,enum=labels,enum=none"`
	// Download: the page offers the video to download.
	Download bool `bson:"download" json:"download"`
	// Draft: shared as a draft for review (the page says it isn't approved).
	Draft bool `bson:"draft,omitempty" json:"draft,omitempty"`
	// Anyway: a person sent a version the ship gate holds, with a note.
	Anyway *ReviewLinkAnyway `bson:"anyway,omitempty" json:"anyway,omitempty"`
	State  string            `bson:"state" json:"state" jsonschema:"enum=live,enum=revoked,enum=expired"`
	// Key is where its objects are kept: review/<key>/ in the media store
	// (128 random bits). Never shown.
	Key string `bson:"key" json:"-"`
	// Store is the media store it was kept in (a locator, no secrets).
	Store string `bson:"store" json:"-"`
	// Seats is how many reviewers' comment slots the page has, each SeatSize bytes.
	Seats    int `bson:"seats" json:"seats"`
	SeatSize int `bson:"seatSize" json:"-"`
	// Taken is, per reviewer's browser, what MetaRush brought in from it;
	// Seen, per slot, the store's tag of what MetaRush last read there.
	Taken []ReviewSeat `bson:"taken,omitempty" json:"-"`
	Seen  []string     `bson:"seen,omitempty" json:"-"`
	// Owners is, per slot, the browser whose slot it is: the first one
	// MetaRush read there. Another's writing over it is never taken in.
	Owners []string `bson:"owners,omitempty" json:"-"`
	// Hour and InHour count the comments brought in this hour (a link takes
	// in at most so many an hour; the rest wait in their slots).
	Hour   *time.Time `bson:"hour,omitempty" json:"-"`
	InHour int        `bson:"inHour,omitempty" json:"-"`
	// SyncUntil keeps two computers from reading its slots at once.
	SyncUntil *time.Time `bson:"syncUntil,omitempty" json:"-"`
	// SyncBy is the token of the look holding that lease: only it writes the
	// link's comments or gives the lease back.
	SyncBy string `bson:"syncBy,omitempty" json:"-"`
	// Projects are every project whose things the version shows: each
	// one's receipt lists the link.
	Projects []string `bson:"projects" json:"projects"`
	// Exposes says what the page shows, in plain words; Kinds the same as
	// What may leave's kinds.
	Exposes []string `bson:"exposes" json:"exposes"`
	Kinds   []string `bson:"kinds" json:"kinds"`
	// Comments is how many comments came back; Waiting how many wait for
	// the next hour (more than the link takes in an hour).
	Comments      int        `bson:"comments" json:"comments"`
	Waiting       int        `bson:"waiting,omitempty" json:"waiting,omitempty"`
	LastCommentAt *time.Time `bson:"lastCommentAt,omitempty" json:"lastCommentAt,omitempty"`
	// Reviewers are the names reviewers gave, as they typed them.
	Reviewers []string   `bson:"reviewers,omitempty" json:"reviewers,omitempty"`
	CreatedBy Actor      `bson:"createdBy" json:"createdBy"`
	CreatedAt time.Time  `bson:"createdAt" json:"createdAt"`
	ExpiresAt time.Time  `bson:"expiresAt" json:"expiresAt"`
	SyncedAt  *time.Time `bson:"syncedAt,omitempty" json:"syncedAt,omitempty"`
	EndedAt   *time.Time `bson:"endedAt,omitempty" json:"endedAt,omitempty"`
	EndedBy   *Actor     `bson:"endedBy,omitempty" json:"endedBy,omitempty"`
	// EndReason says why it ended by itself (D318): the media moved, its
	// client's rules or someone's consent changed, owners turned links off,
	// its project was cleared out. Empty when a person took it back or its
	// time was up.
	EndReason string `bson:"endReason,omitempty" json:"endReason,omitempty"`
	// LastRead: once it ended, its slots were read one last time; only then
	// are they deleted (D318).
	LastRead bool `bson:"lastRead,omitempty" json:"-"`
	// Removed: everything of it is gone from the store and its signed time
	// is over (nothing can write it again).
	Removed bool `bson:"removed,omitempty" json:"removed,omitempty"`
}

// ReviewLinkAnyway is a person's note sending a version the ship gate holds.
type ReviewLinkAnyway struct {
	Note  string    `bson:"note" json:"note" jsonschema:"maxLength=500"`
	Holds []string  `bson:"holds,omitempty" json:"holds,omitempty"`
	By    Actor     `bson:"by" json:"by"`
	At    time.Time `bson:"at" json:"at"`
}

// ReviewSeat is what MetaRush brought in from one reviewer's browser: the
// slot it writes, its random name for itself, the name its reviewer gave,
// and the number of the last comment taken in.
type ReviewSeat struct {
	Seat   int    `bson:"seat" json:"seat"`
	Writer string `bson:"writer" json:"writer"`
	Name   string `bson:"name,omitempty" json:"name,omitempty"`
	Taken  int    `bson:"taken" json:"taken"`
}

// CommentOutside marks a comment someone outside the workspace left through
// a review link: their name as they typed it, and where it came from. Their
// words are plain text MetaRush cleaned, and an AI reads them only quoted.
type CommentOutside struct {
	Name   string `bson:"name" json:"name"`
	LinkID string `bson:"linkId" json:"linkId"`
	Seat   int    `bson:"seat" json:"seat"`
	// Writer is the reviewer's browser's random name for itself; N the
	// comment's number from that browser.
	Writer string `bson:"writer" json:"-"`
	N      int    `bson:"n" json:"n"`
	// Made is when their browser says they wrote it.
	Made *time.Time `bson:"made,omitempty" json:"made,omitempty"`
}

// ReviewLinkSettings are the owners' word on review links: off for everyone,
// owners only, and the longest a link may last (days, at most 7).
type ReviewLinkSettings struct {
	Off        bool      `bson:"off,omitempty" json:"off,omitempty"`
	OwnersOnly bool      `bson:"ownersOnly,omitempty" json:"ownersOnly,omitempty"`
	MaxDays    int       `bson:"maxDays,omitempty" json:"maxDays,omitempty"`
	UpdatedAt  time.Time `bson:"updatedAt" json:"updatedAt"`
	UpdatedBy  Actor     `bson:"updatedBy" json:"updatedBy"`
}

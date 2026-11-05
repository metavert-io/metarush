package models

import "time"

// Guests (D490–D504): people outside the workspace an owner shares a project,
// or some of its versions, with. A guest is not a member: they have no
// account, no settings and no footage library — only grants, each naming a
// project or versions of it, a role and an end. Guests reach MetaRush only
// through its guest listener (its own address, its own pages and its own
// operations), never the dashboard; they watch the versions' review copies
// and, as commenters, leave comments that come back to the version.

// Guest states.
const (
	GuestActive  = "active"
	GuestRevoked = "revoked"
)

// Roles a grant gives a guest.
const (
	// GuestViewer watches the versions' review copies.
	GuestViewer = "viewer"
	// GuestCommenter watches and leaves comments pinned to times.
	GuestCommenter = "commenter"
	// GuestApprover is kept for approvals by guests, which come later: it is
	// refused for now.
	GuestApprover = "approver"
)

// ViaGuest marks what a guest did through the guest listener: their comments,
// the activity log's entries for their requests.
const ViaGuest = "guest"

// Guest is someone outside the workspace an owner shared with, known by their
// email.
type Guest struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	// Email is where the owner reaches them, as the owner typed it; EmailKey
	// finds them (one guest per email in a workspace).
	Email    string `bson:"email" json:"email"`
	EmailKey string `bson:"emailKey" json:"-"`
	// Name is what the owner calls them ("Dana at the client").
	Name  string `bson:"name,omitempty" json:"name,omitempty" jsonschema:"maxLength=80"`
	State string `bson:"state" json:"state" jsonschema:"enum=active,enum=revoked"`
	// Account is the organization account they signed in with first (D496):
	// another account of the same provider showing their email is refused.
	Account   *GuestAccount `bson:"account,omitempty" json:"-"`
	CreatedBy Actor         `bson:"createdBy" json:"createdBy"`
	CreatedAt time.Time     `bson:"createdAt" json:"createdAt"`
	RevokedAt *time.Time    `bson:"revokedAt,omitempty" json:"revokedAt,omitempty"`
	RevokedBy *Actor        `bson:"revokedBy,omitempty" json:"revokedBy,omitempty"`
	// LastSeenAt is their last signed-in request (the database server's time).
	LastSeenAt *time.Time `bson:"lastSeenAt,omitempty" json:"lastSeenAt,omitempty"`
	// SignedInWith is how they signed in last: link or sso.
	SignedInWith string `bson:"signedInWith,omitempty" json:"signedInWith,omitempty" jsonschema:"enum=,enum=link,enum=sso"`
}

// GuestAccount is the provider account a guest signs in with: its stable key
// (hashed, as on a person's SSO link) and the issuer it belongs to.
type GuestAccount struct {
	Key      string    `bson:"key"`
	Scope    string    `bson:"scope"`
	Provider string    `bson:"provider"`
	LinkedAt time.Time `bson:"linkedAt"`
}

// GuestGrant is what a guest may see and do: a project's versions (all of
// them, or the ones named), as a viewer or a commenter, until it ends.
type GuestGrant struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	GuestID     string `bson:"guestId" json:"guestId"`
	ProjectID   string `bson:"projectId" json:"projectId"`
	// RevisionIDs are the versions it names; empty means every version of the
	// project that has a review copy.
	RevisionIDs []string `bson:"revisionIds,omitempty" json:"revisionIds"`
	Role        string   `bson:"role" json:"role" jsonschema:"enum=viewer,enum=commenter"`
	// EndsAt is when it stops working (the database server's time).
	EndsAt time.Time `bson:"endsAt" json:"endsAt"`
	// Message is a note from the owner the guest's page shows.
	Message string `bson:"message,omitempty" json:"message,omitempty" jsonschema:"maxLength=500"`
	// Kinds are what of a client's things it lets leave (What may leave's
	// kinds); Projects every project whose things its versions showed when it
	// was made (each one's receipt lists it).
	Kinds    []string `bson:"kinds" json:"kinds"`
	Projects []string `bson:"projects" json:"projects"`
	// Exposes says what the guest sees, in plain words.
	Exposes   []string   `bson:"exposes" json:"exposes"`
	CreatedBy Actor      `bson:"createdBy" json:"createdBy"`
	CreatedAt time.Time  `bson:"createdAt" json:"createdAt"`
	RevokedAt *time.Time `bson:"revokedAt,omitempty" json:"revokedAt,omitempty"`
	RevokedBy *Actor     `bson:"revokedBy,omitempty" json:"revokedBy,omitempty"`
	// EndReason says why it ended by itself (its project was cleared out).
	EndReason string `bson:"endReason,omitempty" json:"endReason,omitempty"`
}

// GuestLink is a one-time sign-in link an owner sends a guest (D495), found by
// its token's SHA-256; the token itself is never kept.
type GuestLink struct {
	ID          string     `bson:"_id" json:"id"` // SHA-256 of the token
	WorkspaceID string     `bson:"workspaceId" json:"workspaceId"`
	GuestID     string     `bson:"guestId" json:"guestId"`
	CreatedBy   Actor      `bson:"createdBy" json:"createdBy"`
	CreatedAt   time.Time  `bson:"createdAt" json:"createdAt"`
	ExpiresAt   time.Time  `bson:"expiresAt" json:"expiresAt"`
	UsedAt      *time.Time `bson:"usedAt,omitempty" json:"usedAt,omitempty"`
	RevokedAt   *time.Time `bson:"revokedAt,omitempty" json:"revokedAt,omitempty"`
}

// GuestSession is a guest's signed-in browser (D494), found by its cookie's
// SHA-256; the cookie itself is never kept. Its times are the database
// server's.
type GuestSession struct {
	ID          string `bson:"_id" json:"id"` // SHA-256 of the cookie
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	GuestID     string `bson:"guestId" json:"guestId"`
	// Via is how it signed in: link (a one-time link) or sso (the
	// organization's account), with the provider.
	Via      string `bson:"via" json:"via" jsonschema:"enum=link,enum=sso"`
	Provider string `bson:"provider,omitempty" json:"provider,omitempty"`
	// From is the address it signed in from; Computer the MetaRush that
	// answered (its installation).
	From       string    `bson:"from,omitempty" json:"from,omitempty"`
	Computer   string    `bson:"computer,omitempty" json:"computer,omitempty"`
	CreatedAt  time.Time `bson:"createdAt" json:"createdAt"`
	ExpiresAt  time.Time `bson:"expiresAt" json:"expiresAt"`
	LastSeenAt time.Time `bson:"lastSeenAt" json:"lastSeenAt"`
}

// GuestActivity is one guest request, as owners see it (D497): who, from
// which address, what, when, and whether it was turned away.
type GuestActivity struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	// GuestID is who asked ("" when nobody was signed in: a sign-in turned away).
	GuestID string `bson:"guestId,omitempty" json:"guestId,omitempty"`
	// What is what they did (guest.signin, guest.watch, guest.media,
	// guest.comment…): the activity log's phrase names it.
	What       string    `bson:"what" json:"what"`
	ProjectID  string    `bson:"projectId,omitempty" json:"projectId,omitempty"`
	RevisionID string    `bson:"revisionId,omitempty" json:"revisionId,omitempty"`
	From       string    `bson:"from,omitempty" json:"from,omitempty"`
	At         time.Time `bson:"at" json:"at"`
	// Status is the answer's HTTP status; Refused and Reason say why it was
	// turned away.
	Status  int    `bson:"status,omitempty" json:"status,omitempty"`
	Refused bool   `bson:"refused,omitempty" json:"refused,omitempty"`
	Reason  string `bson:"reason,omitempty" json:"reason,omitempty"`
	// Computer is the MetaRush that answered (its installation's ID).
	Computer string `bson:"computer,omitempty" json:"computer,omitempty"`
}

// GuestSettings are the owners' word on guests for the workspace: whether
// guests are let in at all (D501). Off ends every guest's sign-in at once.
type GuestSettings struct {
	On        bool      `bson:"on" json:"on"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
	UpdatedBy Actor     `bson:"updatedBy" json:"updatedBy"`
}

// CommentGuest marks a comment a guest left through the guest page (D492):
// who, through which grant. Their words are plain text MetaRush cleaned, and
// an AI reads them only quoted.
type CommentGuest struct {
	ID      string `bson:"id" json:"id"`
	GrantID string `bson:"grantId" json:"grantId"`
	Name    string `bson:"name,omitempty" json:"name,omitempty"`
	Email   string `bson:"email" json:"email"`
}

package models

import "time"

// Organizing footage (D96–D98). The Inbox is a triage pile: footage in no
// project, no collection, not archived and not in the Trash. Each clip
// leaves it by being added to a project, put on a collection (a named shelf
// such as "B-roll" or "Family"), archived (the one unnamed "keep for later"
// shelf) or moved to the Trash. Nothing in the Trash is deleted until a
// person deletes it forever or empties the Trash (or turns on emptying it
// after a number of days).

// Collection is a named shelf of footage that people create (D96). A clip can
// be on several collections and in projects too. A project can use
// everything on a collection through a project link, like another project
// that has footage but no resources, motion graphics or cut.
type Collection struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	Name        string `bson:"name" json:"name"`
	// Key is the name folded for uniqueness: one collection per name, in any case.
	Key       string    `bson:"key" json:"-"`
	CreatedAt time.Time `bson:"createdAt" json:"createdAt"`
	CreatedBy Actor     `bson:"createdBy" json:"createdBy"`
	UpdatedAt time.Time `bson:"updatedAt" json:"updatedAt"`
}

// AssetTrash says when footage went to the Trash, who moved it, and where it
// was then (its project, collections and the Archive), so Restore puts it
// back. While it is in the Trash it is in none of those.
type AssetTrash struct {
	At            time.Time `bson:"at" json:"at"`
	By            Actor     `bson:"by" json:"by"`
	ProjectIDs    []string  `bson:"projectIds,omitempty" json:"projectIds,omitempty"`
	CollectionIDs []string  `bson:"collectionIds,omitempty" json:"collectionIds,omitempty"`
	Archived      bool      `bson:"archived,omitempty" json:"archived,omitempty"`
	// Claim marks an automatic emptying in progress: the machine deleting it
	// and since when (another machine takes over a claim older than an hour).
	Claim *TrashClaim `bson:"claim,omitempty" json:"-"`
}

// TrashClaim is a machine deleting a clip from the Trash on its own.
type TrashClaim struct {
	By string    `bson:"by"`
	At time.Time `bson:"at"`
}

// TrashSettings is the workspace's "Empty the Trash after N days": off (0)
// unless a person turns it on.
type TrashSettings struct {
	EmptyAfterDays int       `bson:"emptyAfterDays,omitempty" json:"emptyAfterDays,omitempty"`
	UpdatedAt      time.Time `bson:"updatedAt" json:"updatedAt"`
	UpdatedBy      Actor     `bson:"updatedBy" json:"updatedBy"`
}

// InInbox reports footage nobody has filed yet (D97): in no project, on no
// collection, not archived and not in the Trash. A clearer copy MetaRush made
// of a recording (D88) is never footage.
func (a *Asset) InInbox() bool {
	return len(a.ProjectIDs) == 0 && len(a.CollectionIDs) == 0 && a.ArchivedAt == nil && a.Trash == nil && !a.HasTag(TagEnhanced)
}

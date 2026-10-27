package models

import "time"

// AutoSpend is a change that cost money and that Auto ran by itself (D241):
// for whom, in which project, what it was and what it was expected to cost.
// The receipt lists them, and the bell says each day what Auto spent.
type AutoSpend struct {
	ID             string  `bson:"_id" json:"id"`
	WorkspaceID    string  `bson:"workspaceId" json:"workspaceId"`
	ProjectID      string  `bson:"projectId,omitempty" json:"projectId,omitempty"`
	ForID          string  `bson:"forId" json:"forId" jsonschema:"description=The person whose Auto ran it."`
	ForName        string  `bson:"forName,omitempty" json:"forName,omitempty"`
	ConversationID string  `bson:"conversationId" json:"conversationId"`
	ActionID       string  `bson:"actionId" json:"actionId"`
	Op             string  `bson:"op" json:"op"`
	What           string  `bson:"what" json:"what" jsonschema:"description=The card's title."`
	USD            float64 `bson:"usd" json:"usd" jsonschema:"description=Its estimate (the most it was let spend); what it actually cost comes from what it made."`
	// JobIDs are the work it started (a generated shot's job): what it
	// actually cost is what those settled at (D241).
	JobIDs   []string  `bson:"jobIds,omitempty" json:"jobIds,omitempty"`
	Services []string  `bson:"services,omitempty" json:"services,omitempty"`
	Day      string    `bson:"day" json:"day" jsonschema:"description=The UTC day (YYYY-MM-DD)."`
	At       time.Time `bson:"at" json:"at"`
}

package models

import "time"

// Publication is one time a version was posted to a video platform by a
// person (D305): kept on the version, outside its spec and its hash.
type Publication struct {
	ID       string `bson:"id" json:"id"`
	Platform string `bson:"platform" json:"platform" jsonschema:"enum=youtube"`
	// State: sending, posted or failed.
	State string `bson:"state" json:"state" jsonschema:"enum=sending,enum=posted,enum=failed"`
	// Channel is who it was posted as (the channel's name and ID).
	Channel   string `bson:"channel" json:"channel"`
	ChannelID string `bson:"channelId" json:"channelId"`
	VideoID   string `bson:"videoId,omitempty" json:"videoId,omitempty"`
	URL       string `bson:"url,omitempty" json:"url,omitempty"`
	Privacy   string `bson:"privacy" json:"privacy" jsonschema:"enum=private,enum=unlisted,enum=public"`
	Title     string `bson:"title" json:"title"`
	// RenderID and SHA256 are the file that was sent.
	RenderID string `bson:"renderId" json:"renderId"`
	SHA256   string `bson:"sha256" json:"sha256"`
	Preset   string `bson:"preset" json:"preset"`
	// Captions are the languages of the captions files sent.
	Captions []string `bson:"captions,omitempty" json:"captions,omitempty"`
	Chapters int      `bson:"chapters,omitempty" json:"chapters,omitempty"`
	// Disclosed: the platform was told it contains AI-made content.
	Disclosed bool     `bson:"disclosed,omitempty" json:"disclosed,omitempty"`
	Notes     []string `bson:"notes,omitempty" json:"notes,omitempty"`
	Error     string   `bson:"error,omitempty" json:"error,omitempty"`
	// Sent and Size are how much of the file has gone, while it is sending.
	Sent int64 `bson:"sent,omitempty" json:"sent,omitempty"`
	Size int64 `bson:"size,omitempty" json:"size,omitempty"`
	// Installation is the computer sending it (only it can go on with it).
	Installation string `bson:"installation,omitempty" json:"installation,omitempty"`
	// Beat is the last time its sender said it was still sending.
	Beat *time.Time `bson:"beat,omitempty" json:"beat,omitempty"`
	// Claim names the sender that owns it now (each new sender claims it);
	// only the owner's heartbeat and final word count.
	Claim string `bson:"claim,omitempty" json:"-"`
	// Tries is how many times MetaRush went on with it after it stopped part-way.
	Tries int `bson:"tries,omitempty" json:"tries,omitempty"`
	// Overrides are the ship-gate notes that let it out past what held it
	// (D299): once it is posted, the version says it went out.
	Overrides []string `bson:"overrides,omitempty" json:"overrides,omitempty"`
	// LateURL: the video reached the platform after the post had been marked
	// as not posted (it stays marked so; the notes say what happened).
	LateURL string     `bson:"lateUrl,omitempty" json:"lateUrl,omitempty"`
	By      Actor      `bson:"by" json:"by"`
	At      time.Time  `bson:"at" json:"at"`
	DoneAt  *time.Time `bson:"doneAt,omitempty" json:"doneAt,omitempty"`
}

// Publication states.
const (
	PublicationSending = "sending"
	PublicationPosted  = "posted"
	PublicationFailed  = "failed"
)

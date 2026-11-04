package models

import "time"

// A clip's cut-out (D505): the people in a clip, or in a part of it, told
// from their background frame by frame — a matte at the clip's own rate,
// smoothed over time — kept with the clip's picture as one file whose
// background is see-through, which cuts play over something else (a colour,
// a blur of the clip itself, a picture or another clip). It is derived media
// like a fixed copy: made once on any computer that can, by its fingerprint
// (the media it was made from, the stretch, the model and how it was
// smoothed), never changed once ready, and pinned by its file's SHA-256 in
// the versions that play it (collection mattes).

// States of a cut-out.
const (
	MatteWaiting = "waiting" // queued: no computer has started on it yet
	MatteMaking  = "making"
	MatteReady   = "ready"
	MatteFailed  = "failed"
	MatteStopped = "stopped" // a person stopped it
)

// Matte is one clip's cut-out.
type Matte struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	AssetID     string `bson:"assetId" json:"assetId"`
	// ProjectID is the project it was asked for (whose Content Settings
	// chose the model and decide where its footage may go).
	ProjectID string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	// Media is the file it was made from (its SHA-256): the file renders play
	// for the clip — the original, a fixed copy, or their steady copy where
	// the frame rate varies. Fixed is the fixed copy a cut pins for the clip
	// ("" for the original), what a cut checks it against.
	Media string `bson:"media" json:"media"`
	Fixed string `bson:"fixed,omitempty" json:"fixed,omitempty"`
	// MediaKey and MediaSize locate the media in the media store.
	MediaKey  string `bson:"mediaKey" json:"-"`
	MediaSize int64  `bson:"mediaSize" json:"mediaSize,omitempty"`
	// From and To are the stretch of the clip it covers, in seconds of the
	// clip, from the frame showing at From to the one showing just before
	// To; First is that first frame's number (0 the clip's first) and Frames
	// how many it holds; Rate is the clip's frame rate (a fraction:
	// 30000/1001).
	From   float64 `bson:"from" json:"from"`
	To     float64 `bson:"to" json:"to"`
	First  int     `bson:"first" json:"first"`
	Frames int     `bson:"frames,omitempty" json:"frames,omitempty"`
	Rate   string  `bson:"rate" json:"rate"`
	// Width and Height are the cut-out's size.
	Width  int `bson:"width,omitempty" json:"width,omitempty"`
	Height int `bson:"height,omitempty" json:"height,omitempty"`
	// The model that tells the people from their background (an AI service
	// of kind video-background; local: on a computer of the workspace).
	Provider      string `bson:"provider" json:"provider"`
	ProviderTitle string `bson:"providerTitle" json:"providerTitle"`
	Model         string `bson:"model" json:"model"`
	ModelTitle    string `bson:"modelTitle" json:"modelTitle"`
	// ModelVersion names the exact model: the pinned file's SHA-256, or the
	// helper's protocol and the system's version for Apple Vision.
	ModelVersion string `bson:"modelVersion,omitempty" json:"modelVersion,omitempty"`
	// Choice says why that model: default | asked | content-settings.
	Choice string `bson:"choice,omitempty" json:"choice,omitempty" jsonschema:"enum=,enum=default,enum=asked,enum=content-settings"`
	// Fingerprint names what it is made from and how: two requests with the
	// same fingerprint are the same cut-out, made once.
	Fingerprint string `bson:"fingerprint" json:"fingerprint"`
	State       string `bson:"state" json:"state" jsonschema:"enum=waiting,enum=making,enum=ready,enum=failed,enum=stopped"`
	// Error says why it failed or stopped, in plain words.
	Error string `bson:"error,omitempty" json:"error,omitempty"`
	JobID string `bson:"jobId,omitempty" json:"jobId,omitempty"`
	// GenerationID is the AI work record of a cut-out a service elsewhere
	// made (what was sent, the cost); none for a model on a computer.
	GenerationID string `bson:"generationId,omitempty" json:"generationId,omitempty"`
	// File is the cut-out: the clip's picture with its background
	// see-through (WebM, VP9 with an alpha channel).
	File *MatteFile `bson:"file,omitempty" json:"file,omitempty"`
	// Measures say how it went.
	Measures *MatteMeasures `bson:"measures,omitempty" json:"measures,omitempty"`
	// MadeOn is the installation that made it, MadeOnName its name.
	MadeOn     string     `bson:"madeOn,omitempty" json:"madeOn,omitempty"`
	MadeOnName string     `bson:"madeOnName,omitempty" json:"madeOnName,omitempty"`
	CreatedAt  time.Time  `bson:"createdAt" json:"createdAt"`
	CreatedBy  Actor      `bson:"createdBy" json:"createdBy"`
	UpdatedAt  time.Time  `bson:"updatedAt" json:"updatedAt"`
	ReadyAt    *time.Time `bson:"readyAt,omitempty" json:"readyAt,omitempty"`
}

// MatteFile is a cut-out's file in the media store.
type MatteFile struct {
	SHA256 string `bson:"sha256" json:"sha256"`
	Key    string `bson:"key" json:"key"`
	Size   int64  `bson:"size" json:"size"`
	MIME   string `bson:"mime" json:"mime"`
}

// MatteMeasures are what making a cut-out measured.
type MatteMeasures struct {
	// Seconds is how long it took, MsPerFrame the time per frame.
	Seconds    float64 `bson:"seconds" json:"seconds"`
	MsPerFrame float64 `bson:"msPerFrame" json:"msPerFrame"`
	// People is the share of frames where someone was found (0–1).
	People float64 `bson:"people" json:"people"`
	// Flicker is how much the matte's edge changed from frame to frame where
	// the picture didn't (mean, 0–255) before and after smoothing.
	FlickerBefore float64 `bson:"flickerBefore" json:"flickerBefore"`
	FlickerAfter  float64 `bson:"flickerAfter" json:"flickerAfter"`
}

// Covers reports whether a ready cut-out covers a stretch of its clip (to
// the frame).
func (m *Matte) Covers(from, to float64) bool {
	if m == nil {
		return false
	}
	const slack = 0.001
	return from >= m.From-slack && to <= m.To+slack
}

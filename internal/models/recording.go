package models

import "time"

// Recording states.
const (
	RecordingRecording = "recording" // pieces are arriving from the browser
	RecordingAdding    = "adding"    // the files are being made into footage
	RecordingReady     = "ready"     // every track is footage
	RecordingFailed    = "failed"    // making footage failed; what arrived is kept (finish again)
	RecordingDiscarded = "discarded" // the person threw it away before it became footage
)

// Recording track kinds (and their IDs: one of each at most).
const (
	TrackScreen = "screen"
	TrackCamera = "camera"
	TrackVoice  = "voice"
)

// Recording is a screen recording made in the dashboard (D258–D260): the
// screen, the camera and the voice recorded in the person's browser as
// separate files on one clock, sent to the computer that runs MetaRush while
// they record. Each track becomes footage; linked into a project the
// recording is a conversation there (Links), so its tracks stay together and
// the vetted layouts lay it out.
type Recording struct {
	ID          string `bson:"_id" json:"id"`
	WorkspaceID string `bson:"workspaceId" json:"workspaceId"`
	// ProjectID is the project it was recorded for (its footage goes there;
	// without one, to the Inbox).
	ProjectID string `bson:"projectId,omitempty" json:"projectId,omitempty"`
	Name      string `bson:"name" json:"name"`
	State     string `bson:"state" json:"state" jsonschema:"enum=recording,enum=adding,enum=ready,enum=failed,enum=discarded"`
	// Tracks are the files, in order: screen, camera, voice (each optional,
	// at least one).
	Tracks []RecordingTrack `bson:"tracks" json:"tracks"`
	// Clock says how the tracks were lined up while recording.
	Clock *RecordingClock `bson:"clock,omitempty" json:"clock,omitempty"`
	// Pauses are where the person paused (on the recording's clock, which
	// leaves them out: nothing was recorded meanwhile).
	Pauses []RecordingPause `bson:"pauses,omitempty" json:"pauses,omitempty"`
	// Length is how long it is (seconds, pauses left out), as last measured.
	Length float64 `bson:"length" json:"length"`
	// Layout is how the person wanted the screen and camera shown: the first
	// cut's layout.
	Layout string `bson:"layout,omitempty" json:"layout,omitempty" jsonschema:"enum=pip,enum=side-by-side,enum=screen,enum=camera"`
	// Script is what the teleprompter showed.
	Script *RecordingScript `bson:"script,omitempty" json:"script,omitempty"`
	// Links are the projects it is in, each with its conversation there.
	Links []RecordingLink `bson:"links,omitempty" json:"links,omitempty"`
	// InstallationID and MachineName are the computer that received the
	// pieces (where they wait until they become footage).
	InstallationID string `bson:"installationId" json:"installationId"`
	MachineName    string `bson:"machineName,omitempty" json:"machineName,omitempty"`
	// Remote: it was recorded in a browser on another computer (D128).
	Remote bool   `bson:"remote,omitempty" json:"remote,omitempty"`
	Error  string `bson:"error,omitempty" json:"error,omitempty"`
	// Ended says why recording ended: stopped (the person), track (a shared
	// window or device went away), kept (what arrived, after a closed tab),
	// expired (its pieces were removed after days without being kept), or
	// empty while recording.
	Ended string `bson:"ended,omitempty" json:"ended,omitempty" jsonschema:"enum=stopped,enum=track,enum=kept,enum=expired"`
	// AddingAt and AddingBy are the lease of the process making it footage:
	// renewed while it works, so another finish can take over one that died.
	AddingAt *time.Time `bson:"addingAt,omitempty" json:"addingAt,omitempty"`
	AddingBy string     `bson:"addingBy,omitempty" json:"addingBy,omitempty"`
	// ExpiryNoticeAt is when its maker was told its pieces will be removed.
	ExpiryNoticeAt *time.Time `bson:"expiryNoticeAt,omitempty" json:"expiryNoticeAt,omitempty"`
	// Lined says how its tracks were last lined up in a project: by their
	// sound, or by the clock and why.
	Lined       string     `bson:"lined,omitempty" json:"lined,omitempty"`
	CreatedAt   time.Time  `bson:"createdAt" json:"createdAt"`
	CreatedBy   Actor      `bson:"createdBy" json:"createdBy"`
	UpdatedAt   time.Time  `bson:"updatedAt" json:"updatedAt"`
	LastPieceAt time.Time  `bson:"lastPieceAt" json:"lastPieceAt" jsonschema:"description=When the latest piece arrived."`
	FinishedAt  *time.Time `bson:"finishedAt,omitempty" json:"finishedAt,omitempty"`
}

// RecordingTrack is one file of a recording.
type RecordingTrack struct {
	// ID is the track's kind: screen, camera or voice.
	ID string `bson:"id" json:"id" jsonschema:"enum=screen,enum=camera,enum=voice"`
	// Label is what the browser called the source: a window's or tab's
	// title, a camera's or microphone's name.
	Label string `bson:"label,omitempty" json:"label,omitempty" jsonschema:"maxLength=200"`
	// Surface is what was shared (the screen track): monitor (a whole
	// screen), window or browser (a tab).
	Surface string `bson:"surface,omitempty" json:"surface,omitempty" jsonschema:"enum=monitor,enum=window,enum=browser"`
	// Format is the file's media type as the browser recorded it (the voice:
	// audio/pcm, 16-bit mono).
	Format string `bson:"format" json:"format" jsonschema:"maxLength=120"`
	// Sound is what the file's sound is: own (the shared screen's sound),
	// mic (a copy of the microphone, for listening and lining up), none.
	Sound  string `bson:"sound,omitempty" json:"sound,omitempty" jsonschema:"enum=own,enum=mic,enum=none"`
	Rate   int    `bson:"rate,omitempty" json:"rate,omitempty" jsonschema:"description=The voice's samples per second."`
	Width  int    `bson:"width,omitempty" json:"width,omitempty"`
	Height int    `bson:"height,omitempty" json:"height,omitempty"`
	// Bytes is how much of the file has arrived.
	Bytes int64 `bson:"bytes" json:"bytes"`
	// Start is where the file starts on the recording's clock (seconds;
	// negative when it started a moment earlier), and Drift how much faster
	// its clock runs (parts per million).
	Start float64 `bson:"start" json:"start"`
	Drift float64 `bson:"drift,omitempty" json:"drift,omitempty"`
	// AssetID is the footage it became.
	AssetID string `bson:"assetId,omitempty" json:"assetId,omitempty"`
	State   string `bson:"state" json:"state" jsonschema:"enum=receiving,enum=adding,enum=ready,enum=failed"`
	Error   string `bson:"error,omitempty" json:"error,omitempty"`
}

// RecordingClock is how a recording's tracks were lined up (D259).
type RecordingClock struct {
	// Method: clock (measured on the page's clock while recording).
	Method string `bson:"method" json:"method" jsonschema:"enum=clock"`
	// Spread is how far apart the measurements of the microphone's start
	// were (milliseconds): how sure the voice's place is.
	Spread float64   `bson:"spread" json:"spread"`
	At     time.Time `bson:"at" json:"at"`
}

// RecordingPause is a pause: where on the recording's clock it happened and
// how long it lasted (seconds).
type RecordingPause struct {
	At     float64 `bson:"at" json:"at"`
	Length float64 `bson:"length" json:"length"`
}

// RecordingScript is the script the teleprompter showed.
type RecordingScript struct {
	// Source: project (a cut's narration script) or pasted.
	Source string `bson:"source" json:"source" jsonschema:"enum=project,enum=pasted"`
	// Lines are the lines as shown.
	Lines []string `bson:"lines" json:"lines" jsonschema:"maxItems=2000"`
	// RevisionID is the version whose script it was (source project).
	RevisionID string `bson:"revisionId,omitempty" json:"revisionId,omitempty"`
}

// RecordingLink is a project a recording is in, with its conversation there.
type RecordingLink struct {
	ProjectID      string    `bson:"projectId" json:"projectId"`
	ConversationID string    `bson:"conversationId" json:"conversationId"`
	At             time.Time `bson:"at" json:"at"`
}

// Track finds a track by its ID.
func (r *Recording) Track(id string) *RecordingTrack {
	for i := range r.Tracks {
		if r.Tracks[i].ID == id {
			return &r.Tracks[i]
		}
	}
	return nil
}

// LinkIn is the recording's link into a project (nil when it isn't in it).
func (r *Recording) LinkIn(projectID string) *RecordingLink {
	for i := range r.Links {
		if r.Links[i].ProjectID == projectID {
			return &r.Links[i]
		}
	}
	return nil
}

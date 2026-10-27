package models

import (
	"time"

	"github.com/metavert-io/metarush/internal/editspec"
)

// QAReportPrefix is the ID prefix of QA reports (qar_…).
const QAReportPrefix = "qar"

// QA finding severities and bases.
const (
	QAError     = "error"
	QAWarning   = "warning"
	QAInfo      = "info"
	QAObserved  = "observed"  // measured on the decoded render
	QAHeuristic = "heuristic" // inferred from plan geometry or conventions
)

// QA verdicts.
const (
	QAPass     = "pass"
	QAWarnings = "warnings"
	QAFail     = "fail"
)

// QAFix is a suggested repair in edit-plan terms. Ops are hints written against
// the rendered revision: they are never applied automatically and must go through
// `edit check` like any other plan.
type QAFix struct {
	Summary string        `bson:"summary" json:"summary"`
	Ops     []editspec.Op `bson:"ops,omitempty" json:"ops,omitempty"`
}

// QAFinding is one observed defect or heuristic concern in a render.
type QAFinding struct {
	ID         string   `bson:"id" json:"id"`             // f1, f2… (stable within a report)
	Severity   string   `bson:"severity" json:"severity"` // error | warning | info
	Kind       string   `bson:"kind" json:"kind"`
	Basis      string   `bson:"basis" json:"basis"` // observed | heuristic
	From       float64  `bson:"from" json:"from"`   // timeline seconds
	To         float64  `bson:"to" json:"to"`
	Timecode   string   `bson:"timecode" json:"timecode"`
	Message    string   `bson:"message" json:"message"`
	ClipIDs    []string `bson:"clipIds,omitempty" json:"clipIds,omitempty"`
	OverlayIDs []string `bson:"overlayIds,omitempty" json:"overlayIds,omitempty"`
	Fix        *QAFix   `bson:"fix,omitempty" json:"fix,omitempty"`
	Frames     []string `bson:"frames,omitempty" json:"frames,omitempty"` // QAFrame IDs showing this finding
}

// QACheck summarizes one check and the thresholds it used.
type QACheck struct {
	Name      string `bson:"name" json:"name"`
	Status    string `bson:"status" json:"status"` // pass | warning | fail | skipped
	Basis     string `bson:"basis" json:"basis"`
	Detail    string `bson:"detail" json:"detail"`
	Threshold string `bson:"threshold,omitempty" json:"threshold,omitempty"`
}

// QASpan is a detected interval in timeline seconds, with what (if anything) in
// the plan or the source explains it.
type QASpan struct {
	From      float64 `bson:"from" json:"from"`
	To        float64 `bson:"to" json:"to"`
	Explained string  `bson:"explained,omitempty" json:"explained,omitempty"`
}

// QACutLoudness is how loud the speech is on each side of a cut in the render
// (gated loudness of the few seconds of each clip's sound next to the cut).
type QACutLoudness struct {
	At     float64 `bson:"at" json:"at"` // timeline seconds of the cut
	ClipID string  `bson:"clipId" json:"clipId"`
	Before float64 `bson:"before" json:"before"` // LUFS
	After  float64 `bson:"after" json:"after"`
	Jump   float64 `bson:"jump" json:"jump"` // after − before (LU)
}

// QALoudnessTarget is the delivery convention a render is compared against.
type QALoudnessTarget struct {
	Platform        string  `bson:"platform" json:"platform"`
	IntegratedLUFS  float64 `bson:"integratedLufs" json:"integratedLufs"`
	ToleranceLU     float64 `bson:"toleranceLu" json:"toleranceLu"`
	MaxTruePeakDBTP float64 `bson:"maxTruePeakDbtp" json:"maxTruePeakDbtp"`
	Note            string  `bson:"note" json:"note"`
}

// QAMeasurements are the raw numbers behind the findings.
type QAMeasurements struct {
	Duration         float64          `bson:"duration" json:"duration"`
	ExpectedDuration float64          `bson:"expectedDuration" json:"expectedDuration"`
	Frames           int              `bson:"frames" json:"frames"`
	ExpectedFrames   int              `bson:"expectedFrames" json:"expectedFrames"`
	Width            int              `bson:"width" json:"width"`
	Height           int              `bson:"height" json:"height"`
	ExpectedWidth    int              `bson:"expectedWidth" json:"expectedWidth"`
	ExpectedHeight   int              `bson:"expectedHeight" json:"expectedHeight"`
	FPS              float64          `bson:"fps" json:"fps"`
	ExpectedFPS      int              `bson:"expectedFps" json:"expectedFps"`
	VideoCodec       string           `bson:"videoCodec" json:"videoCodec"`
	PixelFormat      string           `bson:"pixelFormat" json:"pixelFormat"`
	HasAudio         bool             `bson:"hasAudio" json:"hasAudio"`
	ExpectsAudio     bool             `bson:"expectsAudio" json:"expectsAudio"`
	AudioCodec       string           `bson:"audioCodec,omitempty" json:"audioCodec,omitempty"`
	AudioDuration    float64          `bson:"audioDuration,omitempty" json:"audioDuration,omitempty"`
	IntegratedLUFS   *float64         `bson:"integratedLufs,omitempty" json:"integratedLufs,omitempty"`
	TruePeakDBTP     *float64         `bson:"truePeakDbtp,omitempty" json:"truePeakDbtp,omitempty"`
	LoudnessRangeLU  *float64         `bson:"loudnessRangeLu,omitempty" json:"loudnessRangeLu,omitempty"`
	LoudnessTarget   QALoudnessTarget `bson:"loudnessTarget" json:"loudnessTarget"`
	Black            []QASpan         `bson:"black" json:"black"`
	Frozen           []QASpan         `bson:"frozen" json:"frozen"`
	Silences         []QASpan         `bson:"silences" json:"silences"`
	// Speech continuity: planned words, words heard in the render, and how many matched.
	SpeechWordsExpected int `bson:"speechWordsExpected,omitempty" json:"speechWordsExpected,omitempty"`
	SpeechWordsHeard    int `bson:"speechWordsHeard,omitempty" json:"speechWordsHeard,omitempty"`
	SpeechWordsMatched  int `bson:"speechWordsMatched,omitempty" json:"speechWordsMatched,omitempty"`
	// CutLoudness: speech loudness on both sides of each cut between two speaking clips.
	CutLoudness []QACutLoudness `bson:"cutLoudness,omitempty" json:"cutLoudness,omitempty"`
	// Conversations: per conversation clip, how long each person talks, is on
	// screen and how loud they are in the render (D99).
	Conversations []QAConversation `bson:"conversations,omitempty" json:"conversations,omitempty"`
}

// QAConversation is what QA measured for one conversation clip.
type QAConversation struct {
	ClipID string                 `bson:"clipId" json:"clipId"`
	Name   string                 `bson:"name" json:"name"`
	Style  string                 `bson:"style,omitempty" json:"style,omitempty"`
	People []QAConversationPerson `bson:"people" json:"people"`
	// Longest is the longest stretch the picture stays on one person (seconds),
	// against the style's limit.
	Longest float64 `bson:"longest" json:"longest"`
	Limit   float64 `bson:"limit" json:"limit"`
}

// QAConversationPerson is one person in a conversation clip, as measured.
type QAConversationPerson struct {
	Name     string  `bson:"name" json:"name"`
	Speaking float64 `bson:"speaking" json:"speaking"` // seconds heard speaking
	OnScreen float64 `bson:"onScreen" json:"onScreen"` // seconds on screen (alone or in a layout)
	// Loudness is the render's loudness over the words only they say (LUFS;
	// nil when they don't speak alone long enough to measure).
	Loudness *float64 `bson:"loudness,omitempty" json:"loudness,omitempty"`
	Muted    bool     `bson:"muted,omitempty" json:"muted,omitempty"`
}

// QAFrame is a still from the render (or a contact sheet) kept as visual
// evidence. Images live in the media store (content-addressed) so any machine
// can show them; Path is the local copy on the machine that answered.
type QAFrame struct {
	ID     string  `bson:"id" json:"id"`
	At     float64 `bson:"at" json:"at"` // timeline seconds (0 for sheets)
	Label  string  `bson:"label" json:"label"`
	Reason string  `bson:"reason" json:"reason"` // sheet | cut | overlay | finding | start | end
	File   string  `bson:"file" json:"file"`     // path inside the report's folder, e.g. frames/03-cut-0m03.067.jpg
	Key    string  `bson:"key,omitempty" json:"key,omitempty"`
	SHA256 string  `bson:"sha256,omitempty" json:"sha256,omitempty"`
	Size   int64   `bson:"size,omitempty" json:"size,omitempty"`
	Path   string  `bson:"-" json:"path,omitempty"`
}

// QACounts tallies findings by severity.
type QACounts struct {
	Errors   int `bson:"errors" json:"errors"`
	Warnings int `bson:"warnings" json:"warnings"`
	Info     int `bson:"info" json:"info"`
}

// QAReport is the stored result of decoding and checking one render.
type QAReport struct {
	ID             string         `bson:"_id" json:"id"`
	WorkspaceID    string         `bson:"workspaceId" json:"workspaceId"`
	ProjectID      string         `bson:"projectId" json:"projectId"`
	RevisionID     string         `bson:"revisionId" json:"revisionId"`
	RevisionNumber int            `bson:"revisionNumber" json:"revisionNumber"`
	RenderID       string         `bson:"renderId" json:"renderId"`
	RenderSHA256   string         `bson:"renderSha256" json:"renderSha256"`
	SpecHash       string         `bson:"specHash" json:"specHash"`
	Preset         string         `bson:"preset" json:"preset"`
	Quality        string         `bson:"quality" json:"quality"`
	Version        string         `bson:"version" json:"version"`
	Verdict        string         `bson:"verdict" json:"verdict"` // pass | warnings | fail
	Counts         QACounts       `bson:"counts" json:"counts"`
	Findings       []QAFinding    `bson:"findings" json:"findings"`
	Checks         []QACheck      `bson:"checks" json:"checks"`
	Measurements   QAMeasurements `bson:"measurements" json:"measurements"`
	ContactSheet   *QAFrame       `bson:"contactSheet,omitempty" json:"contactSheet,omitempty"`
	MomentsSheet   *QAFrame       `bson:"momentsSheet,omitempty" json:"momentsSheet,omitempty"`
	Frames         []QAFrame      `bson:"frames" json:"frames"`
	Limitations    []string       `bson:"limitations" json:"limitations"`
	// Promises are the promises made to the client (D300) as measured: one
	// per promise of the project and its Content Settings.
	Promises []QAPromise `bson:"promises,omitempty" json:"promises,omitempty"`
	// NotesSHA fingerprints the version's delivery notes when a promise read
	// them (every generated clip disclosed): newer notes make the check old.
	NotesSHA  string            `bson:"notesSha,omitempty" json:"notesSha,omitempty"`
	Tools     map[string]string `bson:"tools,omitempty" json:"tools,omitempty"`
	JobID     string            `bson:"jobId,omitempty" json:"jobId,omitempty"`
	RunBy     string            `bson:"runBy,omitempty" json:"runBy,omitempty"` // installation that decoded the render
	Seconds   float64           `bson:"seconds" json:"seconds"`
	CreatedBy Actor             `bson:"createdBy" json:"createdBy"`
	CreatedAt time.Time         `bson:"createdAt" json:"createdAt"`
}

// RenderQA is the QA summary kept on a render document (additive field).
type RenderQA struct {
	ReportID string    `bson:"reportId" json:"reportId"`
	Verdict  string    `bson:"verdict" json:"verdict"`
	Errors   int       `bson:"errors" json:"errors"`
	Warnings int       `bson:"warnings" json:"warnings"`
	At       time.Time `bson:"at" json:"at"`
}

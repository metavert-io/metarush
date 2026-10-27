// Package recorder keeps screen recordings made in the dashboard (D258–D260):
// the person's browser records the screen, the camera and the microphone as
// separate files and sends each in pieces while recording; the pieces wait on
// the computer that received them (<home>/state/recordings/<id>/) until
// recording.finish makes each track footage. The record (collection
// recordings) says where each file starts on the recording's clock, so linked
// into a project the recording becomes a conversation whose vetted layouts
// show the screen and the camera together.
package recorder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// MaxPiece is the largest piece one request carries.
const MaxPiece = 64 << 20

// QuietAfter is how long a recording may go without a piece before it is
// shown as interrupted (the tab closed, the connection dropped).
const QuietAfter = 60 * time.Second

// FormatPCM is the voice's format: 16-bit little-endian mono samples.
const FormatPCM = "audio/pcm"

// RecordingCreateIn starts a recording (POST /api/recordings): what each track is.
type RecordingCreateIn struct {
	ProjectID string             `json:"projectId,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$,description=The project it is for (its footage goes there); without one, the Inbox."`
	Name      string             `json:"name,omitempty" jsonschema:"maxLength=120"`
	Layout    string             `json:"layout,omitempty" jsonschema:"enum=pip,enum=side-by-side,enum=screen,enum=camera,description=How the first cut shows the screen and camera."`
	Tracks    []RecordingTrackIn `json:"tracks" jsonschema:"minItems=1,maxItems=3"`
	// Script is what the teleprompter shows.
	Script *models.RecordingScript `json:"script,omitempty"`
}

// RecordingTrackIn is one file the browser records.
type RecordingTrackIn struct {
	ID      string `json:"id" jsonschema:"enum=screen,enum=camera,enum=voice"`
	Label   string `json:"label,omitempty" jsonschema:"maxLength=200"`
	Surface string `json:"surface,omitempty" jsonschema:"enum=monitor,enum=window,enum=browser"`
	Format  string `json:"format" jsonschema:"minLength=1,maxLength=120"`
	Sound   string `json:"sound,omitempty" jsonschema:"enum=own,enum=mic,enum=none"`
	Rate    int    `json:"rate,omitempty" jsonschema:"minimum=0,maximum=384000"`
	Width   int    `json:"width,omitempty" jsonschema:"minimum=0,maximum=16384"`
	Height  int    `json:"height,omitempty" jsonschema:"minimum=0,maximum=16384"`
}

// RecordingClockIn is the recording's clock as the browser measured it (POST
// /api/recordings/{id}/clock while recording, and with recording.finish).
type RecordingClockIn struct {
	Tracks []RecordingTrackClock   `json:"tracks" jsonschema:"maxItems=3"`
	Pauses []models.RecordingPause `json:"pauses,omitempty" jsonschema:"maxItems=1000"`
	// Length is how long it has recorded (seconds, pauses left out).
	Length float64 `json:"length" jsonschema:"minimum=0"`
	// Spread is how far apart the microphone's start measurements were (ms).
	Spread float64 `json:"spread,omitempty" jsonschema:"minimum=0"`
	// Ended says why recording ended (with the finish): stopped or track.
	Ended string `json:"ended,omitempty" jsonschema:"enum=stopped,enum=track"`
}

// RecordingTrackClock is one track on the recording's clock.
type RecordingTrackClock struct {
	ID    string  `json:"id" jsonschema:"enum=screen,enum=camera,enum=voice"`
	Start float64 `json:"start" jsonschema:"minimum=-60,maximum=60,description=Where the file starts on the recording's clock (seconds)."`
	Drift float64 `json:"drift,omitempty" jsonschema:"minimum=-5000,maximum=5000,description=How much faster its clock runs (parts per million)."`
	// Bytes is how much the browser has recorded of it (finish checks the
	// host has it all).
	Bytes int64 `json:"bytes,omitempty" jsonschema:"minimum=0"`
}

// RecordingPieceOut answers a piece: how much of the track the host has.
type RecordingPieceOut struct {
	Offset int64 `json:"offset"`
}

func invalid(f string, a ...any) error { return apperr.New(apperr.InvalidInput, f, a...) }

// dir is where a recording's pieces wait on this computer.
func dir(a *app.App, id string) string { return filepath.Join(a.Cfg.StateDir(), "recordings", id) }

func partPath(a *app.App, id, track string) string { return filepath.Join(dir(a, id), track+".part") }

// trackOrder is the order tracks are kept in.
var trackOrder = map[string]int{models.TrackScreen: 0, models.TrackCamera: 1, models.TrackVoice: 2}

// Create starts a recording on this computer: the record and an empty piece
// file per track. remote: the browser is on another computer (D128).
func Create(ctx context.Context, a *app.App, in *RecordingCreateIn, remote bool) (*models.Recording, error) {
	if len(in.Tracks) == 0 || len(in.Tracks) > 3 {
		return nil, invalid("A recording has one to three tracks: the screen, the camera and the voice.")
	}
	now := time.Now().UTC()
	r := &models.Recording{ID: ids.New(ids.Recording), WorkspaceID: a.WorkspaceID(), State: models.RecordingRecording, Layout: in.Layout,
		InstallationID: a.Cfg.Installation.ID, MachineName: a.Cfg.Installation.Name, Remote: remote, Script: in.Script,
		CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now, LastPieceAt: now}
	if in.ProjectID != "" {
		var p models.Project
		if !ids.Valid(ids.Project, in.ProjectID) || a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), in.ProjectID, &p) != nil {
			return nil, apperr.New(apperr.NotFound, "There's no such project in this workspace.")
		}
		if p.Archived {
			return nil, invalid("%s is archived: restore it first, or record into the Inbox.", p.Name)
		}
		r.ProjectID = p.ID
	}
	switch in.Layout {
	case "", "pip", "side-by-side", "screen", "camera":
	default:
		return nil, invalid("The layout is pip, side-by-side, screen or camera, not %q.", in.Layout)
	}
	seen := map[string]bool{}
	for _, t := range in.Tracks {
		if _, ok := trackOrder[t.ID]; !ok {
			return nil, invalid("A track is the screen, the camera or the voice, not %q.", t.ID)
		}
		if seen[t.ID] {
			return nil, invalid("The recording has two %s tracks.", t.ID)
		}
		seen[t.ID] = true
		format := strings.ToLower(strings.TrimSpace(t.Format))
		switch {
		case t.ID == models.TrackVoice:
			if format != FormatPCM || t.Rate < 8000 || t.Rate > 192000 {
				return nil, invalid("The voice is recorded as plain samples (audio/pcm) at 8,000–192,000 a second.")
			}
		case !strings.HasPrefix(format, "video/"):
			return nil, invalid("The %s is recorded as a video, not %q.", t.ID, t.Format)
		}
		switch t.Sound {
		case "", "own", "mic", "none":
		default:
			return nil, invalid("A track's sound is own, mic or none, not %q.", t.Sound)
		}
		label := strings.TrimSpace(t.Label)
		if len([]rune(label)) > 200 {
			label = string([]rune(label)[:200])
		}
		r.Tracks = append(r.Tracks, models.RecordingTrack{ID: t.ID, Label: label, Surface: t.Surface, Format: format, Sound: t.Sound, Rate: t.Rate,
			Width: t.Width, Height: t.Height, State: "receiving"})
	}
	sortTracks(r.Tracks)
	r.Name = strings.TrimSpace(in.Name)
	if r.Name == "" {
		r.Name = "Screen recording, " + now.Local().Format("2 Jan 15:04")
	}
	if len([]rune(r.Name)) > 120 {
		r.Name = string([]rune(r.Name)[:120])
	}
	if err := os.MkdirAll(dir(a, r.ID), 0o700); err != nil {
		return nil, diskErr(err)
	}
	for _, t := range r.Tracks {
		f, err := os.OpenFile(partPath(a, r.ID, t.ID), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if err != nil {
			return nil, diskErr(err)
		}
		f.Close()
	}
	if err := a.DB.Insert(ctx, store.CRecordings, r); err != nil {
		_ = os.RemoveAll(dir(a, r.ID))
		return nil, err
	}
	a.Audit(ctx, "recording.start", "recording", r.ID, r.ProjectID, map[string]any{"tracks": len(r.Tracks)})
	return r, nil
}

func sortTracks(ts []models.RecordingTrack) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && trackOrder[ts[j].ID] < trackOrder[ts[j-1].ID]; j-- {
			ts[j], ts[j-1] = ts[j-1], ts[j]
		}
	}
}

// get loads a recording by ID.
func get(ctx context.Context, a *app.App, id string) (*models.Recording, error) {
	if !ids.Valid(ids.Recording, id) {
		return nil, apperr.New(apperr.NotFound, "There's no recording %q.", id)
	}
	var r models.Recording
	if err := a.DB.GetByID(ctx, store.CRecordings, a.WorkspaceID(), id, &r); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.New(apperr.NotFound, "There's no recording %s in this workspace.", id)
		}
		return nil, err
	}
	return &r, nil
}

// Get loads a recording by ID (for the HTTP handlers).
func Get(ctx context.Context, a *app.App, id string) (*models.Recording, error) {
	return get(ctx, a, id)
}

// mayWrite checks a piece may be added: the recording is still recording, on
// this computer, by this person.
func mayWrite(a *app.App, r *models.Recording, track string) error {
	if r.State != models.RecordingRecording {
		return apperr.New(apperr.Conflict, "This recording has ended (%s), so it takes no more pieces.", stateWords(r.State)).WithDetails(map[string]any{"state": r.State})
	}
	if r.InstallationID != a.Cfg.Installation.ID {
		return apperr.New(apperr.Conflict, "This recording is being received on %s, not here.", machine(r))
	}
	if r.CreatedBy.UserID != "" && a.Actor.UserID != "" && r.CreatedBy.UserID != a.Actor.UserID {
		return apperr.New(apperr.Unauthorized, "Only the person recording it can send its pieces.")
	}
	if r.Track(track) == nil {
		return apperr.New(apperr.NotFound, "This recording has no %s track.", track)
	}
	return nil
}

var (
	locks   sync.Map // recording/track → *sync.Mutex
	touched sync.Map // recording → time of the last write of its progress
)

// freeSpaceHint says who can free space: the person when they own this
// MetaRush, else its owners by name.
func freeSpaceHint(ctx context.Context, a *app.App, r *models.Recording) string {
	const keeps = "; the browser keeps the pieces and sends them again."
	if ops.IsOwnerCaller(ctx) {
		return "Free some space on " + machine(r) + keeps
	}
	var names []string
	if ops.OwnerNames != nil {
		names = ops.OwnerNames(ctx, a)
	}
	who := "an owner of this MetaRush"
	switch len(names) {
	case 0:
	case 1:
		who = names[0]
	default:
		who = strings.Join(names[:len(names)-1], ", ") + " or " + names[len(names)-1]
	}
	return "Ask " + who + " to free some space on " + machine(r) + keeps
}

// The limits, swappable in tests.
var (
	maxBytes  = func(a *app.App) int64 { return a.Cfg.Recordings.MaxBytes() }
	freeSpace = freeBytes
)

func lockFor(key string) *sync.Mutex {
	l, _ := locks.LoadOrStore(key, &sync.Mutex{})
	return l.(*sync.Mutex)
}

// Offset is how much of a track this computer has.
func Offset(a *app.App, id, track string) int64 {
	st, err := os.Stat(partPath(a, id, track))
	if err != nil {
		return 0
	}
	return st.Size()
}

// Append adds a piece to a track at offset (the bytes the host has, as HEAD
// says): a piece for another offset is refused with the host's offset, so
// the browser carries on from there. size is the piece's length when known
// (-1 otherwise). A recording holds at most recordings.maxGB in all, and
// pieces never take the disk below recordings.minFreeGB.
func Append(ctx context.Context, a *app.App, id, track string, offset, size int64, body io.Reader) (int64, error) {
	r, err := get(ctx, a, id)
	if err != nil {
		return 0, err
	}
	if err := mayWrite(a, r, track); err != nil {
		return Offset(a, id, track), err
	}
	l := lockFor(id + "/" + track)
	l.Lock()
	defer l.Unlock()
	cur := Offset(a, id, track)
	if offset != cur {
		return cur, apperr.New(apperr.Conflict, "The recording got out of step; it carries on from the part MetaRush already has.").WithDetails(map[string]any{"offset": cur})
	}
	limits := a.Cfg.Recordings
	held := int64(0)
	for _, t := range r.Tracks {
		held += Offset(a, id, t.ID)
	}
	most := maxBytes(a)
	room := most - held
	if room <= 0 || (size > 0 && size > room) {
		return cur, apperr.New(apperr.Conflict, "This recording reached %s, the most one recording keeps here, so it stops; what arrived is kept.", sizeWords(most)).
			WithHint("Start another recording to go on (recordings.maxGB in config.yaml sets the limit).").WithDetails(map[string]any{"reason": "full", "limitBytes": most})
	}
	need := max(size, 0)
	if free := freeSpace(dir(a, id)); free >= 0 && free-need < limits.MinFreeBytes() {
		return cur, apperr.New(apperr.Storage, "The computer that runs MetaRush is nearly out of disk space (%s free), so it keeps no more of the recording for now.", sizeWords(free)).
			WithHint(freeSpaceHint(ctx, a, r)).WithDetails(map[string]any{"reason": "disk", "freeBytes": free})
	}
	f, err := os.OpenFile(partPath(a, id, track), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return cur, diskErr(err)
	}
	n, err := io.Copy(f, io.LimitReader(body, min(MaxPiece, room)))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	cur += n
	if err != nil {
		if isDiskFull(err) {
			// Take back the half-written piece: the browser keeps it and sends it again.
			_ = os.Truncate(partPath(a, id, track), offset)
			return offset, diskErr(err)
		}
		return cur, apperr.Wrap(apperr.InvalidInput, err, "A piece of the recording was cut off on its way; it is sent again.").WithDetails(map[string]any{"offset": cur})
	}
	progress(ctx, a, id, track, cur, false)
	return cur, nil
}

// progress writes how much of a track arrived (at most every 2 s per
// recording unless forced): the dashboard and other computers see it.
func progress(ctx context.Context, a *app.App, id, track string, n int64, force bool) {
	now := time.Now()
	if last, ok := touched.Load(id); ok && !force && now.Sub(last.(time.Time)) < 2*time.Second {
		return
	}
	touched.Store(id, now)
	_, _ = a.DB.C(store.CRecordings).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID(), "tracks.id": track},
		bson.M{"$set": bson.M{"tracks.$.bytes": n, "lastPieceAt": now.UTC(), "updatedAt": now.UTC()}})
}

// SetClock records the clock as the browser measured it so far (a closed tab
// keeps the last one sent).
func SetClock(ctx context.Context, a *app.App, id string, in *RecordingClockIn) (*models.Recording, error) {
	r, err := get(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if r.State != models.RecordingRecording {
		return r, nil // finished meanwhile: its clock was taken then
	}
	if r.CreatedBy.UserID != "" && a.Actor.UserID != "" && r.CreatedBy.UserID != a.Actor.UserID {
		return nil, apperr.New(apperr.Unauthorized, "Only the person recording it can set its clock.")
	}
	if err := applyClock(r, in); err != nil {
		return nil, err
	}
	set := bson.M{"tracks": r.Tracks, "pauses": r.Pauses, "length": r.Length, "clock": r.Clock, "updatedAt": time.Now().UTC()}
	if _, err := a.DB.C(store.CRecordings).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID(), "state": models.RecordingRecording}, bson.M{"$set": set}); err != nil {
		return nil, err
	}
	return r, nil
}

// applyClock puts the browser's measurements on the record.
func applyClock(r *models.Recording, in *RecordingClockIn) error {
	if in == nil {
		return nil
	}
	for _, tc := range in.Tracks {
		t := r.Track(tc.ID)
		if t == nil {
			return invalid("The recording has no %s track.", tc.ID)
		}
		if tc.Start < -60 || tc.Start > 60 || tc.Drift < -5000 || tc.Drift > 5000 {
			return invalid("The %s's place on the clock (%.3f s, %+.0f ppm) is out of range.", tc.ID, tc.Start, tc.Drift)
		}
		t.Start, t.Drift = round3(tc.Start), round1(tc.Drift)
	}
	if len(in.Pauses) > 1000 {
		return invalid("Too many pauses.")
	}
	r.Pauses = nil
	for _, p := range in.Pauses {
		if p.At < 0 || p.Length < 0 {
			return invalid("A pause can't start before the recording or last less than nothing.")
		}
		r.Pauses = append(r.Pauses, models.RecordingPause{At: round3(p.At), Length: round3(p.Length)})
	}
	if in.Length > 0 {
		r.Length = round3(in.Length)
	}
	r.Clock = &models.RecordingClock{Method: "clock", Spread: round1(in.Spread), At: time.Now().UTC()}
	return nil
}

// diskErr says plainly that the computer couldn't keep the recording.
func diskErr(err error) error {
	if isDiskFull(err) {
		return apperr.Wrap(apperr.Storage, err, "The computer that runs MetaRush is out of disk space, so it can't keep the recording.").
			WithHint("Free some space there; the browser keeps the pieces and sends them again.")
	}
	return apperr.Wrap(apperr.Internal, err, "MetaRush couldn't keep the recording on this computer.")
}

func isDiskFull(err error) bool {
	return errors.Is(err, syscall.ENOSPC) || errors.Is(err, syscall.EDQUOT)
}

func machine(r *models.Recording) string {
	if r.MachineName != "" {
		return r.MachineName
	}
	return "another computer"
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }
func round1(v float64) float64 { return math.Round(v*10) / 10 }

// stateWords says a recording's state plainly.
func stateWords(state string) string {
	switch state {
	case models.RecordingRecording:
		return "being recorded"
	case models.RecordingAdding:
		return "being added to the Library"
	case models.RecordingReady:
		return "in the Library"
	case models.RecordingFailed:
		return "couldn't be added to the Library"
	case models.RecordingDiscarded:
		return "thrown away"
	}
	return fmt.Sprint(state)
}

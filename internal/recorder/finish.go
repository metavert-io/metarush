package recorder

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// ActionFinish names the tracked work of making a recording footage (the
// activity bar's "Adding a screen recording").
const ActionFinish = "recording-finish"

func init() {
	voiceover.ActionTitles[ActionFinish] = "Adding a screen recording"
}

// RecordingFinishIn makes a recording footage.
type RecordingFinishIn struct {
	Recording string `json:"recording" jsonschema:"minLength=1,description=The recording (its ID)."`
	// Clock is the browser's last measurement (with the pieces it recorded).
	Clock *RecordingClockIn `json:"clock,omitempty"`
	// Keep makes footage of what arrived, even when the browser said more
	// was coming or pieces arrived a moment ago (a closed tab, a lost
	// connection).
	Keep bool `json:"keep,omitempty"`
}

// Finish makes each track footage (the voice a WAV, each video copied into
// a clean file) and links the recording into its project. It runs on the
// computer holding the pieces, once: the first finish claims it (a lease it
// renews while it works); a second finish, at once or from another tab,
// answers with the work in progress, and takes over only a lease nobody has
// renewed for LeaseStale (the process finishing it died). Only the person who
// recorded it, or an owner, ends a recording; its footage is theirs.
func Finish(ctx context.Context, a *app.App, in *RecordingFinishIn) (*RecordingInfo, error) {
	r, err := get(ctx, a, in.Recording)
	if err != nil {
		return nil, err
	}
	if err := mayEnd(ctx, a, r, "finish"); err != nil {
		return nil, err
	}
	switch {
	case r.State == models.RecordingReady:
		v := view(ctx, a, r)
		return &v, nil
	case r.State == models.RecordingDiscarded:
		return nil, apperr.New(apperr.Conflict, "This recording was thrown away.")
	case r.State == models.RecordingAdding && !leaseStale(r, time.Now()):
		// Already being added (a second click, another tab): the work in progress answers.
		v := view(ctx, a, r)
		return &v, nil
	}
	if r.InstallationID != a.Cfg.Installation.ID {
		return nil, apperr.New(apperr.Conflict, "The recording's pieces are on %s, so it can be finished only there.", machine(r)).
			WithHint("Run it on that computer, or open the dashboard there.").WithCommand("metarush recording finish " + r.ID)
	}
	if r.State == models.RecordingRecording {
		if in.Clock != nil {
			if err := applyClock(r, in.Clock); err != nil {
				return nil, err
			}
			r.Ended = in.Clock.Ended
		}
		if err := complete(a, r, in); err != nil {
			return nil, err
		}
		if r.Ended == "" {
			r.Ended = "kept"
		}
	}
	for i := range r.Tracks {
		r.Tracks[i].Bytes = Offset(a, r.ID, r.Tracks[i].ID)
	}
	r.Error = ""
	ok, err := claim(ctx, a, r)
	if err != nil {
		return nil, err
	}
	if !ok {
		// Another finish claimed it a moment ago: it answers.
		cur, err := get(ctx, a, r.ID)
		if err != nil {
			return nil, err
		}
		v := view(ctx, a, cur)
		return &v, nil
	}
	a.Audit(ctx, "recording.finish", "recording", r.ID, r.ProjectID, map[string]any{"tracks": len(r.Tracks), "ended": r.Ended})
	maker := actingAs(a, r.CreatedBy)
	// The work goes on when the browser that asked goes away (a closed tab).
	work := context.WithoutCancel(ctx)
	return voiceover.TrackWork(work, maker, ActionFinish, "", r.ProjectID, func(ctx context.Context) (*RecordingInfo, error) {
		makeFootage(ctx, maker, r)
		v := view(ctx, a, r)
		return &v, nil
	})
}

// mayEnd checks the caller may end a recording that isn't footage yet
// (finish or throw it away): the person who recorded it, or an owner.
func mayEnd(ctx context.Context, a *app.App, r *models.Recording, what string) error {
	switch r.State {
	case models.RecordingReady, models.RecordingDiscarded:
		return nil
	}
	if r.CreatedBy.UserID == "" || r.CreatedBy.UserID == a.Actor.UserID || ops.IsOwnerCaller(ctx) {
		return nil
	}
	return apperr.New(apperr.Unauthorized, "Only %s, who recorded it, or an owner can %s this recording.", presenter(r), what).
		WithDetails(map[string]any{"recordedBy": r.CreatedBy.UserID})
}

// actingAs is the app acting as a person (the recording's footage is theirs).
func actingAs(a *app.App, who models.Actor) *app.App {
	if who.UserID == "" || who.UserID == a.Actor.UserID {
		return a
	}
	x := *a
	x.Actor.UserID, x.Actor.UserName = who.UserID, who.UserName
	return &x
}

// workDir is a claim's own folder inside the recording's.
func workDir(a *app.App, r *models.Recording) string {
	sum := sha256.Sum256([]byte(r.AddingBy))
	return filepath.Join(dir(a, r.ID), "work-"+hex.EncodeToString(sum[:6]))
}

// LeaseStale is how long a finishing process may go without renewing its
// lease before another finish takes over; LeaseRenew is how often it renews.
var (
	LeaseStale = 2 * time.Minute
	LeaseRenew = 30 * time.Second
)

func leaseStale(r *models.Recording, now time.Time) bool {
	return r.State == models.RecordingAdding && (r.AddingAt == nil || now.Sub(*r.AddingAt) > LeaseStale)
}

// claim makes r the work of this process: its state becomes adding with a new
// lease, only if it is still recording or failed, or its lease went stale.
// False: someone else claimed it first.
func claim(ctx context.Context, a *app.App, r *models.Recording) (bool, error) {
	now := time.Now().UTC()
	filter := bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID(), "$or": bson.A{
		bson.M{"state": bson.M{"$in": bson.A{models.RecordingRecording, models.RecordingFailed}}},
		bson.M{"state": models.RecordingAdding, "addingAt": bson.M{"$lt": now.Add(-LeaseStale)}},
		bson.M{"state": models.RecordingAdding, "addingAt": bson.M{"$exists": false}},
	}}
	tok := fmt.Sprintf("%s/%d/%d", a.Cfg.Installation.ID, os.Getpid(), now.UnixNano())
	r.State, r.AddingAt, r.AddingBy, r.UpdatedAt = models.RecordingAdding, &now, tok, now
	res, err := a.DB.C(store.CRecordings).ReplaceOne(ctx, filter, r)
	if err != nil {
		return false, err
	}
	return res.MatchedCount == 1, nil
}

// errLeaseLost: another process took the work over (this one was too slow).
var errLeaseLost = errors.New("another process took over adding this recording")

// complete checks the host has every piece the browser recorded; a recording
// still receiving pieces waits unless the person keeps what arrived.
func complete(a *app.App, r *models.Recording, in *RecordingFinishIn) error {
	if in.Keep {
		return nil
	}
	missing := map[string]any{}
	if in.Clock != nil {
		for _, tc := range in.Clock.Tracks {
			if have := Offset(a, r.ID, tc.ID); tc.Bytes > have {
				missing[tc.ID] = map[string]int64{"have": have, "want": tc.Bytes}
			}
		}
		if len(missing) > 0 {
			return apperr.New(apperr.Conflict, "Part of the recording hasn't arrived yet; it is still being sent.").WithDetails(map[string]any{"missing": missing})
		}
		return nil
	}
	if quiet := time.Since(r.LastPieceAt); quiet < QuietAfter {
		return apperr.New(apperr.Conflict, "It is still being recorded: a piece arrived %d seconds ago.", int(quiet.Seconds())).
			WithHint("Stop it in the browser that records it, or keep what arrived so far.").WithCommand("metarush recording finish " + r.ID + " --keep")
	}
	return nil
}

// put writes the record back; while it holds a lease (AddingBy), only if the
// lease is still its own.
func put(ctx context.Context, a *app.App, r *models.Recording) error {
	r.UpdatedAt = time.Now().UTC()
	filter := bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()}
	if r.AddingBy != "" {
		filter["addingBy"] = r.AddingBy
	}
	res, err := a.DB.C(store.CRecordings).ReplaceOne(ctx, filter, r)
	if err == nil && res.MatchedCount == 0 && r.AddingBy != "" {
		return errLeaseLost
	}
	return err
}

// renew keeps the lease fresh while the work goes on (stop() ends it).
func renew(a *app.App, id, tok string) (stop func()) {
	done := make(chan struct{})
	go func() {
		t := time.NewTicker(LeaseRenew)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				now := time.Now().UTC()
				_, _ = a.DB.C(store.CRecordings).UpdateOne(context.Background(), bson.M{"_id": id, "workspaceId": a.WorkspaceID(), "addingBy": tok},
					bson.M{"$set": bson.M{"addingAt": now}})
			}
		}
	}()
	return func() { close(done) }
}

// makeFootage turns each track that isn't footage yet into footage, then
// links the recording into its project. A track that fails says why; what
// arrived stays on this computer for another try.
func makeFootage(ctx context.Context, a *app.App, r *models.Recording) {
	stop := renew(a, r.ID, r.AddingBy)
	defer stop()
	// This claim's own folder: a process that lost its lease (a laptop waking
	// from sleep) never writes or removes what the one that took over uses.
	work := workDir(a, r)
	if err := os.MkdirAll(work, 0o700); err != nil {
		return
	}
	done := 0
	hopeless := map[string]bool{}
	for i := range r.Tracks {
		t := &r.Tracks[i]
		if t.State == "ready" && t.AssetID != "" {
			done++
			continue
		}
		_ = voiceover.Step(ctx, i, len(r.Tracks), "Adding the "+trackWords(t.ID))
		t.State, t.Error = "adding", ""
		if errors.Is(put(ctx, a, r), errLeaseLost) {
			return // another process has it now
		}
		id, err := trackFootage(ctx, a, r, t, work)
		if err != nil {
			t.State, t.Error = "failed", plain(err)
			// Nothing arrived, or what arrived isn't media: no retry can fix it.
			hopeless[t.ID] = apperr.Is(err, apperr.NotFound) || apperr.Is(err, apperr.UnsupportedMedia)
			continue
		}
		t.State, t.AssetID, t.Error = "ready", id, ""
		done++
	}
	now := time.Now().UTC()
	var why []string
	retry := false
	for _, t := range r.Tracks {
		if t.State == "failed" {
			why = append(why, fmt.Sprintf("the %s: %s", trackWords(t.ID), strings.TrimRight(t.Error, ".")))
			retry = retry || !hopeless[t.ID]
		}
	}
	switch {
	case done == len(r.Tracks):
		r.State, r.FinishedAt = models.RecordingReady, &now
	case done > 0 && !retry:
		// A track with nothing readable (a tab closed before its first full
		// piece) can't get better: the rest is the recording.
		r.State, r.FinishedAt = models.RecordingReady, &now
		r.Error = "Part of it had nothing that could be played (" + strings.Join(why, "; ") + "); the rest is in the Library."
	default:
		r.State = models.RecordingFailed
		r.Error = "Some of it couldn't be added to the Library (" + strings.Join(why, "; ") + "). What arrived is kept on " + machine(r) + "; finish it again to retry."
	}
	// A tab that closed may have sent its last clock seconds before its last
	// piece: the recording is as long as its longest file.
	r.Length = max(r.Length, footageLength(ctx, a, r))
	// Done: the lease ends with the state it leaves.
	tok := r.AddingBy
	r.AddingAt, r.AddingBy = nil, ""
	r.UpdatedAt = time.Now().UTC()
	if res, err := a.DB.C(store.CRecordings).ReplaceOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID(), "addingBy": tok}, r); err != nil || res.MatchedCount == 0 {
		return // another process took over: its outcome stands, and its files are left alone
	}
	// The record says what happened; only now do the pieces go (all of them,
	// with every claim's folder, once it is footage; else only this claim's).
	if r.State == models.RecordingReady {
		_ = os.RemoveAll(dir(a, r.ID))
	} else {
		_ = os.RemoveAll(work)
	}
	if r.State == models.RecordingReady && r.ProjectID != "" {
		if _, err := link(ctx, a, r, r.ProjectID); err != nil {
			r.Error = "It is in the Library, but couldn't be linked into its project: " + plain(err)
			_ = put(ctx, a, r)
		}
	}
}

// footageLength is the longest track's footage (seconds).
func footageLength(ctx context.Context, a *app.App, r *models.Recording) float64 {
	best := 0.0
	for _, t := range r.Tracks {
		var as models.Asset
		if t.AssetID != "" && a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), t.AssetID, &as) == nil && as.Media.Duration > best {
			best = as.Media.Duration
		}
	}
	return round3(best)
}

// trackFootage makes one track's file and adds it to the Library.
func trackFootage(ctx context.Context, a *app.App, r *models.Recording, t *models.RecordingTrack, work string) (string, error) {
	part := partPath(a, r.ID, t.ID)
	st, err := os.Stat(part)
	if err != nil || st.Size() == 0 {
		return "", apperr.New(apperr.NotFound, "nothing of it arrived")
	}
	base := fileBase(r.Name) + " - " + t.ID
	var out string
	switch t.ID {
	case models.TrackVoice:
		out = filepath.Join(work, base+".wav")
		if err := writeWAV(part, out, t.Rate); err != nil {
			return "", diskErr(err)
		}
	default:
		out = filepath.Join(work, base+extFor(t.Format))
		if err := remux(ctx, a.Tools, part, out); err != nil {
			// A file the copy can't read is added as it came: ingest says
			// plainly if it can't be played.
			if cerr := copyFile(part, out); cerr != nil {
				return "", diskErr(cerr)
			}
		}
	}
	defer os.Remove(out)
	// A finish that died after adding this track (and before the record said
	// so) made the very same file (the copy is bit-exact): use its clip.
	if id := clipOf(ctx, a, out, r.ProjectID); id != "" {
		return id, nil
	}
	transcribe := t.ID == models.TrackVoice || (t.ID == models.TrackScreen && t.Sound == "own")
	res := ingest.File(ctx, a, out, ingest.Options{ProjectID: r.ProjectID, DisplayName: filepath.Base(out), Title: r.Name + " (" + trackWords(t.ID) + ")",
		Via: viaOf(a), Transcribe: transcribe, Duplicates: ingest.DuplicatesKeep, FootageOnly: true, Tags: []string{"screen-recording", t.ID}})
	if res.AssetID == "" {
		msg := res.Error
		if msg == "" {
			msg = "MetaRush couldn't add it (" + res.Status + ")"
		}
		return "", apperr.New(apperr.UnsupportedMedia, "%s", msg)
	}
	return res.AssetID, nil
}

// clipOf is the clip already made of a file with these bytes ("" when none),
// linked into the project when it isn't yet.
func clipOf(ctx context.Context, a *app.App, path, projectID string) string {
	sum, err := sha256File(path)
	if err != nil {
		return ""
	}
	var as models.Asset
	if err := a.DB.C(store.CAssets).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "sha256": sum, "state": models.AssetAvailable}).Decode(&as); err != nil {
		return ""
	}
	if projectID != "" && !contains(as.ProjectIDs, projectID) {
		if _, err := ops.CallTyped[ops.AssetLinkOut](ctx, a, "asset.link", ops.AssetLinkIn{AssetID: as.ID, ProjectID: projectID}, ops.Caller{}); err != nil {
			return ""
		}
	}
	return as.ID
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// plain is an error's message for people.
func plain(err error) string { return apperr.From(err).Message }

func viaOf(a *app.App) string {
	if a.Actor.Via != "" {
		return a.Actor.Via
	}
	return "dashboard"
}

// trackWords names a track.
func trackWords(id string) string {
	switch id {
	case models.TrackScreen:
		return "screen"
	case models.TrackCamera:
		return "camera"
	case models.TrackVoice:
		return "voice"
	}
	return id
}

var unsafeName = regexp.MustCompile(`[^\p{L}\p{N} ._,()'-]+`)

// fileBase is a file name from the recording's name.
func fileBase(name string) string {
	n := strings.TrimSpace(unsafeName.ReplaceAllString(name, " "))
	n = strings.Join(strings.Fields(n), " ")
	if n == "" {
		n = "Screen recording"
	}
	if r := []rune(n); len(r) > 80 {
		n = strings.TrimSpace(string(r[:80]))
	}
	return n
}

// extFor is the file extension for a browser's video format.
func extFor(format string) string {
	switch {
	case strings.HasPrefix(format, "video/mp4"):
		return ".mp4"
	case strings.HasPrefix(format, "video/x-matroska"):
		return ".mkv"
	}
	return ".webm"
}

// writeWAV wraps 16-bit mono samples in a WAV header.
func writeWAV(part, out string, rate int) error {
	in, err := os.Open(part)
	if err != nil {
		return err
	}
	defer in.Close()
	st, err := in.Stat()
	if err != nil {
		return err
	}
	n := st.Size() &^ 1 // whole samples only
	if n > 0xFFFFFFFF-36 {
		n = (0xFFFFFFFF - 36) &^ 1
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	h := make([]byte, 44)
	copy(h[0:], "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+n))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	binary.LittleEndian.PutUint16(h[20:], 1) // PCM
	binary.LittleEndian.PutUint16(h[22:], 1) // mono
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(h[32:], 2)
	binary.LittleEndian.PutUint16(h[34:], 16)
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(n))
	if _, err := f.Write(h); err != nil {
		f.Close()
		return err
	}
	if _, err := io.Copy(f, io.LimitReader(in, n)); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// remux copies a browser's recording into a clean file without re-encoding:
// the browser's pieces carry no length and few seek points, and a file cut
// off mid-piece (a closed tab) still plays up to there.
func remux(ctx context.Context, ts *tools.Set, in, out string) error {
	args := []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-y", "-fflags", "+genpts+discardcorrupt", "-i", in, "-map", "0", "-c", "copy",
		// The same pieces always make the same bytes (no muxer version or time
		// written), so a finish taken over mid-way makes the same clip, not a second.
		"-fflags", "+bitexact"}
	if strings.HasSuffix(out, ".mp4") {
		args = append(args, "-movflags", "+faststart")
	}
	args = append(args, out)
	if _, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args}); err != nil {
		_ = os.Remove(out)
		return err
	}
	if st, err := os.Stat(out); err != nil || st.Size() == 0 {
		return fmt.Errorf("the copy is empty")
	}
	return nil
}

func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	f, err := os.OpenFile(to, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, in); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// Recover finishes recordings this computer was adding when it stopped
// (their pieces are complete; adding footage is safe to run again): each once
// its lease goes stale (a finish still running elsewhere keeps it fresh). as
// gives the app acting as a recording's person.
func Recover(ctx context.Context, a *app.App, as func(userID, userName string) *app.App) {
	var list []models.Recording
	if err := a.DB.Find(ctx, store.CRecordings, a.WorkspaceID(), bson.M{"state": models.RecordingAdding, "installationId": a.Cfg.Installation.ID}, &list); err != nil {
		return
	}
	for i := range list {
		id := list[i].ID
		wait := time.Duration(0)
		if at := list[i].AddingAt; at != nil {
			wait = max(0, LeaseStale-time.Since(*at)+time.Second)
		}
		go func() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			takeOver(ctx, a, id, as)
		}()
	}
}

// takeOver claims a recording whose finishing process died and finishes it.
func takeOver(ctx context.Context, a *app.App, id string, as func(userID, userName string) *app.App) bool {
	r, err := get(ctx, a, id)
	if err != nil || !leaseStale(r, time.Now()) {
		return false
	}
	if ok, err := claim(ctx, a, r); err != nil || !ok {
		return false
	}
	pa := as(r.CreatedBy.UserID, r.CreatedBy.UserName)
	_, _ = voiceover.TrackWork(ctx, pa, ActionFinish, "", r.ProjectID, func(ctx context.Context) (*RecordingInfo, error) {
		makeFootage(ctx, pa, r)
		v := view(ctx, pa, r)
		return &v, nil
	})
	return true
}

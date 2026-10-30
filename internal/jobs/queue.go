// Package jobs is the durable job queue: MongoDB documents leased atomically with
// find-and-modify, heartbeats that extend leases, retries with backoff, idempotency
// keys and recovery of jobs whose worker died. No Redis, no SKIP LOCKED.
package jobs

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Job types.
const (
	TypeProxy        = "proxy"
	TypeThumbnail    = "thumbnail"
	TypeContactSheet = "contact_sheet"
	TypeWaveform     = "waveform"
	TypeMezzanine    = "mezzanine"
	TypeEvidence     = "evidence"
	TypeTranscribe   = "transcribe"
	TypeRender       = "render"
	TypeQA           = "qa"      // decode a finished render and check it against its plan
	TypeDiarize      = "diarize" // re-detect who speaks when (e.g. after a speaker-count hint), keeping the words
	// TypeVoiceover is narration work (scratch voice, AI voice, cleanup, matching
	// a recording, a voice copy). It runs where it was asked for — the machine
	// with the provider key and the recording — and is recorded as a job so it
	// shows in activity, job details and notifications (see Queue.Start).
	TypeVoiceover = "voiceover"
	// TypeImport downloads a video from a link (YouTube, TikTok, X) and adds it
	// through the ingest pipeline. It is pinned to the machine that asked for it
	// (EnqueueOptions.Installation): its network, and its browser sign-in when the
	// person allowed that for this import.
	TypeImport = "import"
	// TypeStorageMove records moving the workspace's media to another store
	// (`metarush storage migrate`). It runs in the process that asked, pinned
	// to its machine; a worker only closes one whose process stopped.
	TypeStorageMove = "storage_move"
	// TypeResourceUpload copies a resource version that a linked folder kept on
	// one machine into the media store, the first time a cut uses it (D81). It
	// is pinned to a machine that has the file.
	TypeResourceUpload = "resource-upload"
	// TypeVisual looks at what's on screen in a clip with Apple Vision (macOS):
	// labels, people and faces, on-screen text, shot types (internal/visual).
	// Heavy: it waits on battery like transcription.
	TypeVisual = "visual"
	// TypeDescribe has the person's own Claude Code write a sentence or two per
	// scene (off unless turned on for the project or workspace). Pinned to the
	// machine that asked: it runs that machine's Claude Code.
	TypeDescribe = "describe"
	// TypeFetch brings a GitHub repository (pinned at a commit, D104) or a
	// web page (a dated snapshot, D105) into the resources library. It is
	// pinned to the machine that asked: its network, and for repositories its
	// git and gh sign-in. Web pages are taken by a headless browser that has
	// network access, outside the render jail.
	TypeFetch = "fetch"
	// TypeGenerate makes a clip with a generative-video provider (Google
	// Gemini; D123, D125). It is pinned to the machine that asked (its key):
	// the request is saved before it is sent and the provider's ticket before
	// MetaRush waits for it, so a restart resumes instead of sending it again.
	TypeGenerate = "generate"
	// TypeInspect measures a source for its fixes (D153): shake, colour,
	// grain, blockiness, voices, loudness, hum, silent ends. Light.
	TypeInspect = "inspect"
	// TypePrepare makes a source's fixed copy (D152): heavy, like transcripts.
	TypePrepare = "prepare"
	// TypeRemotionImport brings a Remotion project in as a source (D131): a
	// folder on the machine that asked, or a GitHub repository fetched with its
	// sign-in, then its packages, fonts and compositions read in the jail, and
	// the videos it already had brought in as footage. Pinned to that machine.
	TypeRemotionImport = "remotion-import"
	// TypeRemotionRender renders one composition of an approved version of a
	// Remotion source in the jail and adds it as a clip (D135). Heavy; any
	// machine with Node.js renders it (everything it needs is in the media
	// store).
	TypeRemotionRender = "remotion-render"
	// TypeSwitches finds a footage's picture switches to the frame (D160): a
	// few seconds of ffmpeg around each cut, any machine.
	TypeSwitches = "switches"
	// TypeRemotionTask is other work on a Remotion source that runs its code in
	// the jail (D167–D171): making the project map, preparing and checking word
	// settings, lifting a scene into a motion graphic. Payload "task" says
	// which, "title" says it in plain words. Heavy; any machine.
	TypeRemotionTask = "remotion-task"
	// TypeRemotionRefresh runs a Remotion source's own steps that a person
	// started (D173–D175): outside the render jail with the network on, in a
	// separate, minimal environment, then their outputs become a new version
	// that waits for a person. Pinned to the machine that asked (its keys);
	// never retried by itself.
	TypeRemotionRefresh = "remotion-refresh"
	// TypeRemotionPush sends an approved version's changes back to the
	// source's repository as a new branch (and a pull request), as a person
	// asked (D176). Pinned to the machine that asked (its git and gh sign-in).
	TypeRemotionPush = "remotion-push"
	// TypeAbilityRun runs an approved ability (D234): in the sandbox, its
	// network only through MetaRush, what it brings back added with how it
	// was made. Pinned to the machine that asked (its programs and keys);
	// never retried by itself.
	TypeAbilityRun = "ability-run"
)

// LeaseDuration is how long a lease lasts without a heartbeat.
var LeaseDuration = 45 * time.Second

// Clock is this computer's clock, for what a job records of when (created,
// started, finished). A lease's end and when a job may run (nextRunAt) are the
// database server's time instead (other computers compare them: D335, D360),
// never this. Tests skew it with store.Clock.
var Clock = func() time.Time { return store.Clock() }

// RetryBase is the wait before a failed job's second attempt; each later
// attempt waits twice as long (RetryBackoff). Tests shorten it.
var RetryBase = 10 * time.Second

// RetryBackoff is how long a job waits after its attempt number attempt
// (from 1) failed before it may run again: 10 s, 20 s, 40 s…
func RetryBackoff(attempt int) time.Duration {
	return time.Duration(math.Pow(2, float64(max(attempt, 1)-1))) * RetryBase
}

// ErrLostLease means another worker now owns the job.
var ErrLostLease = errors.New("jobs: lease lost")

// Queue is a workspace-scoped job queue.
type Queue struct {
	DB          *store.DB
	WorkspaceID string
	// OnEvent, when set, is told about every change this queue makes to a job
	// (the activity log). It must be fast and must not call back into the queue.
	OnEvent func(ctx context.Context, ev Event)
}

// Job lifecycle event kinds (Event.Kind).
const (
	EventQueued          = "queued"
	EventLeased          = "leased"
	EventProgress        = "progress"
	EventSucceeded       = "succeeded"
	EventFailed          = "failed"
	EventRetry           = "retry" // failed, or postponed (Postpone); it will be tried again after a backoff
	EventCanceled        = "canceled"
	EventCancelRequested = "cancel_requested"
	EventRequeued        = "requeued" // a person asked to try a failed or canceled job again
	EventReleased        = "released" // the worker running it stopped; it waits for the next one (the attempt does not count)
	EventExpedited       = "expedited"
)

// Event is one change to a job.
type Event struct {
	Kind       string
	Job        models.Job // the job after the change (best effort)
	WorkerID   string
	WorkerName string
	Error      string
	NextRunAt  time.Time
	Progress   models.Progress
}

func (q *Queue) emit(ctx context.Context, ev Event) {
	if q.OnEvent != nil {
		q.OnEvent(ctx, ev)
	}
}

// EnqueueOptions configures a new job.
type EnqueueOptions struct {
	// ID, when set, is the new job's ID. A caller that saves the record a job
	// works on before queueing it (render.submit saves the render, then queues
	// its job) chooses the ID first, so that record is complete before any
	// worker can lease the job.
	ID             string
	Type           string
	Payload        map[string]any
	IdempotencyKey string // same key => same job (no duplicate work)
	Priority       int
	Heavy          bool
	MaxAttempts    int
	ProjectID      string
	AssetID        string
	RenderID       string
	Actor          models.Actor
	// Installation pins the job to one machine (only its worker leases it).
	Installation string
}

func (o EnqueueOptions) jobID() string {
	if o.ID != "" {
		return o.ID
	}
	return ids.New(ids.Job)
}

// Enqueue inserts a job, or returns the existing one for the idempotency key.
// Retrying a failed/canceled job with the same key re-queues it.
func (q *Queue) Enqueue(ctx context.Context, o EnqueueOptions) (*models.Job, bool, error) {
	if o.IdempotencyKey == "" {
		o.IdempotencyKey = ids.New("idk")
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 3
	}
	if o.Payload == nil {
		o.Payload = map[string]any{}
	}
	now := Clock().UTC()
	due, err := q.DB.Now(ctx) // other computers compare it: the server's time
	if err != nil {
		return nil, false, err
	}
	j := &models.Job{
		ID: o.jobID(), WorkspaceID: q.WorkspaceID, Type: o.Type, SchemaVersion: 1, State: models.JobQueued,
		Priority: o.Priority, Heavy: o.Heavy, ProjectID: o.ProjectID, AssetID: o.AssetID, RenderID: o.RenderID, Installation: o.Installation,
		Payload: o.Payload, IdempotencyKey: o.IdempotencyKey, MaxAttempts: o.MaxAttempts, Attempts: []models.Attempt{},
		NextRunAt: due, CreatedBy: o.Actor, CreatedAt: now, UpdatedAt: now,
	}
	err = q.DB.Insert(ctx, store.CJobs, j)
	if err == nil {
		q.emit(ctx, Event{Kind: EventQueued, Job: *j})
		return j, true, nil
	}
	if !store.IsDuplicateKey(err) {
		return nil, false, err
	}
	var existing models.Job
	if err := q.DB.C(store.CJobs).FindOne(ctx, bson.M{"workspaceId": q.WorkspaceID, "idempotencyKey": o.IdempotencyKey}).Decode(&existing); err != nil {
		return nil, false, err
	}
	return &existing, false, nil
}

// Worker identifies a leasing worker.
type Worker struct {
	ID           string // installation ID
	Name         string
	JobTypes     []string
	AcceptsHeavy bool // configuration allows heavy jobs on this machine
	// DeferHeavy is the battery policy's verdict right now: heavy jobs wait unless
	// a person allowed them on battery (Job.AllowOnBattery).
	DeferHeavy bool
}

// pinnedTo matches jobs any machine may run (no installation) and jobs pinned
// to this installation.
func pinnedTo(installationID string) bson.M {
	return bson.M{"$in": bson.A{nil, "", installationID}}
}

// heavyFilter restricts what a worker may lease by weight.
func heavyFilter(w Worker) bson.M {
	switch {
	case !w.AcceptsHeavy:
		return bson.M{"heavy": false}
	case w.DeferHeavy:
		return bson.M{"$or": bson.A{bson.M{"heavy": false}, bson.M{"allowOnBattery": true}}}
	}
	return nil
}

// Lease atomically claims the next runnable job this worker can do: a queued job
// whose nextRunAt has passed, or a running job whose lease expired (dead worker),
// both by the database server's time.
func (q *Queue) Lease(ctx context.Context, w Worker) (*models.Job, error) {
	return q.leaseWhere(ctx, w, nil)
}

// LeaseAmong is Lease limited to some jobs. A terminal that waits for jobs and
// runs them itself (`--wait` with no capable worker online) leases only those:
// work it would abandon when the wait ends (a render's automatic quality check)
// stays queued for a worker.
func (q *Queue) LeaseAmong(ctx context.Context, w Worker, ids []string) (*models.Job, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	return q.leaseWhere(ctx, w, bson.M{"_id": bson.M{"$in": ids}})
}

func (q *Queue) leaseWhere(ctx context.Context, w Worker, only bson.M) (*models.Job, error) {
	now := Clock().UTC()
	runnable := bson.M{"workspaceId": q.WorkspaceID, "type": bson.M{"$in": w.JobTypes}, "cancelRequested": false,
		"installation": pinnedTo(w.ID),
		"$or": bson.A{
			due(),
			leaseOver(),
		},
	}
	for k, v := range only {
		runnable[k] = v
	}
	if hf := heavyFilter(w); hf != nil {
		runnable = bson.M{"$and": bson.A{runnable, hf}}
	}
	update := takeLease(w, now, models.Progress{Pct: 0, Message: "starting"})
	opts := options.FindOneAndUpdate().SetSort(bson.D{{Key: "priority", Value: -1}, {Key: "createdAt", Value: 1}}).SetReturnDocument(options.After)
	var j models.Job
	err := q.DB.C(store.CJobs).FindOneAndUpdate(ctx, runnable, update, opts).Decode(&j)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if j.AttemptCount > j.MaxAttempts {
		// An expired lease pushed this job past its budget: fail it, don't run it.
		_ = q.finish(ctx, &j, w.ID, models.JobFailed, nil, fmt.Sprintf("gave up after %d attempts (last worker stopped responding)", j.MaxAttempts))
		return q.leaseWhere(ctx, w, only)
	}
	q.emit(ctx, Event{Kind: EventLeased, Job: j, WorkerID: w.ID, WorkerName: w.Name})
	return &j, nil
}

// Leases end by the database server's time (D335): computers' clocks differ,
// and others compare a lease with now (a running job, a move of the media, the
// database's move waiting for work to stop). Written as $$NOW + LeaseDuration
// in a pipeline update, and compared with $$NOW.

// leaseEnd is when a lease taken or renewed now ends, in the server's time.
func leaseEnd() bson.D {
	return bson.D{{Key: "$add", Value: bson.A{"$$NOW", LeaseDuration.Milliseconds()}}}
}

// due matches a queued job whose wait is over (nextRunAt, written in the
// server's time), by the server's time: a computer whose clock is fast never
// skips a retry's wait, nor one whose clock is slow leaves a job waiting.
func due() bson.M {
	return bson.M{"state": models.JobQueued, "$expr": bson.M{"$lte": bson.A{"$nextRunAt", "$$NOW"}}}
}

// leaseOver matches a running job whose lease ended (a worker that stopped
// answering), by the server's time.
func leaseOver() bson.M {
	return bson.M{"state": models.JobRunning, "$expr": bson.M{"$lt": bson.A{"$lease.expiresAt", "$$NOW"}}}
}

// literal is a value a pipeline takes as it is (a name starting with $ is
// never read as a field).
func literal(v any) bson.D { return bson.D{{Key: "$literal", Value: v}} }

// takeLease is the update that leases a job to a worker: running, its lease
// (ending by the server's time), progress, one more attempt.
func takeLease(w Worker, now time.Time, p models.Progress) mongo.Pipeline {
	return mongo.Pipeline{{{Key: "$set", Value: bson.D{
		{Key: "state", Value: models.JobRunning},
		{Key: "lease", Value: bson.D{{Key: "$mergeObjects", Value: bson.A{
			literal(bson.D{{Key: "owner", Value: w.ID}, {Key: "ownerName", Value: w.Name}}),
			bson.D{{Key: "expiresAt", Value: leaseEnd()}},
		}}}},
		{Key: "updatedAt", Value: literal(now)},
		{Key: "progress", Value: literal(p)},
		{Key: "attemptCount", Value: bson.D{{Key: "$add", Value: bson.A{bson.D{{Key: "$ifNull", Value: bson.A{"$attemptCount", 0}}}, 1}}}},
		{Key: "attempts", Value: bson.D{{Key: "$concatArrays", Value: bson.A{
			bson.D{{Key: "$ifNull", Value: bson.A{"$attempts", bson.A{}}}},
			bson.A{literal(models.Attempt{Worker: w.ID, WorkerName: w.Name, StartedAt: now})},
		}}}},
	}}}}
}

// Heartbeat extends the lease and records progress. It reports whether
// cancellation was requested, and ErrLostLease if another worker took over.
func (q *Queue) Heartbeat(ctx context.Context, j *models.Job, owner string, p models.Progress) (cancel bool, err error) {
	now := Clock().UTC()
	var out models.Job
	err = q.DB.C(store.CJobs).FindOneAndUpdate(ctx,
		bson.M{"_id": j.ID, "workspaceId": q.WorkspaceID, "state": models.JobRunning, "lease.owner": owner},
		mongo.Pipeline{{{Key: "$set", Value: bson.D{{Key: "lease.expiresAt", Value: leaseEnd()},
			{Key: "progress", Value: literal(p)}, {Key: "updatedAt", Value: literal(now)}}}}},
		options.FindOneAndUpdate().SetReturnDocument(options.After),
	).Decode(&out)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return false, ErrLostLease
	}
	if err != nil {
		return false, err
	}
	q.emit(ctx, Event{Kind: EventProgress, Job: out, WorkerID: owner, Progress: p})
	return out.CancelRequested, nil
}

// Succeed completes a job. Only the current lease owner can complete it.
func (q *Queue) Succeed(ctx context.Context, j *models.Job, owner string, result map[string]any) error {
	return q.finish(ctx, j, owner, models.JobSucceeded, result, "")
}

// Fail records a failure: retry with exponential backoff, or fail permanently.
func (q *Queue) Fail(ctx context.Context, j *models.Job, owner string, cause error, retryable bool) error {
	msg := cause.Error()
	if !retryable || j.AttemptCount >= j.MaxAttempts {
		return q.finish(ctx, j, owner, models.JobFailed, nil, msg)
	}
	now := Clock().UTC()
	backoff := RetryBackoff(j.AttemptCount)
	server, err := q.DB.Now(ctx)
	if err != nil {
		return err
	}
	next := server.Add(backoff) // compared by every worker: the server's time
	res, err := q.DB.C(store.CJobs).UpdateOne(ctx,
		bson.M{"_id": j.ID, "workspaceId": q.WorkspaceID, "lease.owner": owner, "state": models.JobRunning},
		bson.M{"$set": bson.M{"state": models.JobQueued, "nextRunAt": next, "error": msg, "updatedAt": now,
			"progress": models.Progress{Message: "retrying after error"}, "attempts.$[last].endedAt": now, "attempts.$[last].outcome": "retry", "attempts.$[last].error": msg},
			"$unset": bson.M{"lease": ""}},
		options.UpdateOne().SetArrayFilters([]any{bson.M{"last.worker": owner, "last.endedAt": bson.M{"$exists": false}}}),
	)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrLostLease
	}
	ev := *j
	ev.State, ev.Error, ev.NextRunAt = models.JobQueued, msg, next
	q.emit(ctx, Event{Kind: EventRetry, Job: ev, WorkerID: owner, Error: msg, NextRunAt: next})
	return nil
}

// Release puts a running job back in the queue because the worker running it
// is stopping (a terminal's wait ended, the daemon shuts down): the lease is
// dropped, the attempt does not count and the next worker starts it afresh.
// It is never a failure.
func (q *Queue) Release(ctx context.Context, j *models.Job, owner string) error {
	// Handing a job back goes through a move's freeze: it puts the database
	// at rest (the move waits for running work, D335).
	ctx = store.WriteThroughFreeze(ctx)
	now := Clock().UTC()
	res, err := q.DB.C(store.CJobs).UpdateOne(ctx,
		bson.M{"_id": j.ID, "workspaceId": q.WorkspaceID, "lease.owner": owner, "state": models.JobRunning},
		bson.M{"$set": bson.M{"state": models.JobQueued, "updatedAt": now, "progress": models.Progress{Message: "waiting (the worker running it stopped)"},
			"attempts.$[last].endedAt": now, "attempts.$[last].outcome": "released"},
			"$currentDate": bson.M{"nextRunAt": true}, // due now by the server's time
			"$inc":         bson.M{"attemptCount": -1},
			"$unset":       bson.M{"lease": ""}},
		options.UpdateOne().SetArrayFilters([]any{bson.M{"last.worker": owner, "last.endedAt": bson.M{"$exists": false}}}),
	)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrLostLease
	}
	ev := *j
	ev.State, ev.Lease = models.JobQueued, nil
	if ev.AttemptCount > 0 {
		ev.AttemptCount--
	}
	q.emit(ctx, Event{Kind: EventReleased, Job: ev, WorkerID: owner})
	return nil
}

// OutcomePostponed is the outcome of an attempt that put its job back in the
// queue for a moment because something it needs wasn't there yet (Postpone).
const OutcomePostponed = "postponed"

// Postpone puts a running job back in the queue to start again after a short
// wait, because something it needs isn't there yet (a render job that finds no
// render). Like Release it is never a failure and the attempt does not count;
// the attempt and the job's progress keep the reason, in plain words, so people
// see why it waits. Postponed counts the waits, so a handler can stop waiting.
func (q *Queue) Postpone(ctx context.Context, j *models.Job, owner string, after time.Duration, reason string) error {
	now := Clock().UTC()
	server, err := q.DB.Now(ctx)
	if err != nil {
		return err
	}
	next := server.Add(after) // compared by every worker: the server's time
	res, err := q.DB.C(store.CJobs).UpdateOne(ctx,
		bson.M{"_id": j.ID, "workspaceId": q.WorkspaceID, "lease.owner": owner, "state": models.JobRunning},
		bson.M{"$set": bson.M{"state": models.JobQueued, "nextRunAt": next, "updatedAt": now, "progress": models.Progress{Message: reason},
			"attempts.$[last].endedAt": now, "attempts.$[last].outcome": OutcomePostponed, "attempts.$[last].error": reason},
			"$inc":   bson.M{"attemptCount": -1},
			"$unset": bson.M{"lease": ""}},
		options.UpdateOne().SetArrayFilters([]any{bson.M{"last.worker": owner, "last.endedAt": bson.M{"$exists": false}}}),
	)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrLostLease
	}
	ev := *j
	ev.State, ev.Lease, ev.NextRunAt, ev.Progress = models.JobQueued, nil, next, models.Progress{Message: reason}
	if ev.AttemptCount > 0 {
		ev.AttemptCount--
	}
	q.emit(ctx, Event{Kind: EventRetry, Job: ev, WorkerID: owner, Error: reason, NextRunAt: next})
	return nil
}

// Postponed counts the times in a row the job was postponed (Postpone) just
// before the attempt running now.
func Postponed(j *models.Job) int {
	n := 0
	for i := len(j.Attempts) - 1; i >= 0; i-- {
		at := j.Attempts[i]
		if i == len(j.Attempts)-1 && at.EndedAt == nil {
			continue // the attempt running now
		}
		if at.Outcome != OutcomePostponed {
			break
		}
		n++
	}
	return n
}

// Canceled marks a running job canceled at the worker's request.
func (q *Queue) Canceled(ctx context.Context, j *models.Job, owner string) error {
	return q.finish(ctx, j, owner, models.JobCanceled, nil, "canceled")
}

func (q *Queue) finish(ctx context.Context, j *models.Job, owner, state string, result map[string]any, msg string) error {
	now := Clock().UTC()
	set := bson.M{"state": state, "updatedAt": now, "finishedAt": now,
		"attempts.$[last].endedAt": now, "attempts.$[last].outcome": state}
	if result != nil {
		set["result"] = result
	}
	if msg != "" {
		set["error"] = msg
		set["attempts.$[last].error"] = msg
	}
	if state == models.JobSucceeded {
		set["progress"] = models.Progress{Pct: 1, Message: "done"}
	}
	res, err := q.DB.C(store.CJobs).UpdateOne(ctx,
		bson.M{"_id": j.ID, "workspaceId": q.WorkspaceID, "lease.owner": owner, "state": models.JobRunning},
		bson.M{"$set": set, "$unset": bson.M{"lease": ""}},
		options.UpdateOne().SetArrayFilters([]any{bson.M{"last.worker": owner, "last.endedAt": bson.M{"$exists": false}}}),
	)
	if err != nil {
		return err
	}
	if res.MatchedCount == 0 {
		return ErrLostLease
	}
	ev := *j
	ev.State, ev.UpdatedAt, ev.FinishedAt = state, now, &now
	if msg != "" {
		ev.Error = msg
	}
	if result != nil {
		ev.Result = result
	}
	kind := map[string]string{models.JobSucceeded: EventSucceeded, models.JobFailed: EventFailed, models.JobCanceled: EventCanceled}[state]
	q.emit(ctx, Event{Kind: kind, Job: ev, WorkerID: owner, Error: msg})
	return nil
}

// RequestCancel asks for a job to stop. Queued jobs cancel immediately; running
// jobs stop at the worker's next heartbeat.
func (q *Queue) RequestCancel(ctx context.Context, id string) (*models.Job, error) {
	now := Clock().UTC()
	coll := q.DB.C(store.CJobs)
	_, err := coll.UpdateOne(ctx, bson.M{"_id": id, "workspaceId": q.WorkspaceID, "state": models.JobQueued},
		bson.M{"$set": bson.M{"state": models.JobCanceled, "cancelRequested": true, "updatedAt": now, "finishedAt": now}})
	if err != nil {
		return nil, err
	}
	_, err = coll.UpdateOne(ctx, bson.M{"_id": id, "workspaceId": q.WorkspaceID, "state": models.JobRunning},
		bson.M{"$set": bson.M{"cancelRequested": true, "updatedAt": now}})
	if err != nil {
		return nil, err
	}
	j, err := q.Get(ctx, id)
	if err == nil {
		kind := EventCancelRequested
		if j.State == models.JobCanceled {
			kind = EventCanceled
		}
		q.emit(ctx, Event{Kind: kind, Job: *j})
	}
	return j, err
}

// CancelQueued cancels a job only while it is still queued: a job a worker
// already leased is left alone (the follow-the-latest-version review copy,
// D187, replaces an older version's copy that hasn't started, never one
// that is rendering). It reports whether it canceled the job.
func (q *Queue) CancelQueued(ctx context.Context, id string) (bool, error) {
	now := Clock().UTC()
	res, err := q.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": q.WorkspaceID, "state": models.JobQueued},
		bson.M{"$set": bson.M{"state": models.JobCanceled, "cancelRequested": true, "updatedAt": now, "finishedAt": now}})
	if err != nil || res.ModifiedCount == 0 {
		return false, err
	}
	if j, err := q.Get(ctx, id); err == nil {
		q.emit(ctx, Event{Kind: EventCanceled, Job: *j})
	}
	return true, nil
}

// Expedite lets queued jobs run now: heavy jobs may run on a machine that is
// deferring them for battery, and any retry backoff is skipped. It returns the
// IDs of the jobs it changed (jobs that are not queued are left alone).
func (q *Queue) Expedite(ctx context.Context, ids []string) ([]string, error) {
	if len(ids) == 0 {
		return []string{}, nil
	}
	now := Clock().UTC()
	var match []models.Job
	if err := q.DB.Find(ctx, store.CJobs, q.WorkspaceID, bson.M{"_id": bson.M{"$in": ids}, "state": models.JobQueued}, &match); err != nil {
		return nil, err
	}
	out := []string{}
	for _, j := range match {
		// A retry's wait is skipped by the server's time: nextRunAt becomes
		// now unless it has come already.
		set := bson.D{{Key: "allowOnBattery", Value: true}, {Key: "updatedAt", Value: literal(now)},
			{Key: "nextRunAt", Value: bson.D{{Key: "$min", Value: bson.A{"$nextRunAt", "$$NOW"}}}}}
		res, err := q.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": j.ID, "workspaceId": q.WorkspaceID, "state": models.JobQueued}, mongo.Pipeline{{{Key: "$set", Value: set}}})
		if err != nil {
			return out, err
		}
		if res.MatchedCount > 0 {
			out = append(out, j.ID)
			j.AllowOnBattery = true
			q.emit(ctx, Event{Kind: EventExpedited, Job: j})
		}
	}
	return out, nil
}

// Retry re-queues a failed or canceled job.
func (q *Queue) Retry(ctx context.Context, id string) (*models.Job, error) {
	now := Clock().UTC()
	res, err := q.DB.C(store.CJobs).UpdateOne(ctx,
		bson.M{"_id": id, "workspaceId": q.WorkspaceID, "state": bson.M{"$in": bson.A{models.JobFailed, models.JobCanceled}}},
		bson.M{"$set": bson.M{"state": models.JobQueued, "cancelRequested": false, "updatedAt": now, "attemptCount": 0, "progress": models.Progress{Message: "re-queued"}},
			"$currentDate": bson.M{"nextRunAt": true}, // due now by the server's time
			"$unset":       bson.M{"error": "", "finishedAt": "", "lease": ""}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, fmt.Errorf("job %s is not failed or canceled", id)
	}
	j, err := q.Get(ctx, id)
	if err == nil {
		q.emit(ctx, Event{Kind: EventRequeued, Job: *j})
	}
	return j, err
}

// Get loads a job.
func (q *Queue) Get(ctx context.Context, id string) (*models.Job, error) {
	var j models.Job
	if err := q.DB.GetByID(ctx, store.CJobs, q.WorkspaceID, id, &j); err != nil {
		return nil, err
	}
	return &j, nil
}

// List returns recent jobs, optionally filtered.
func (q *Queue) List(ctx context.Context, filter bson.M, limit int64) ([]models.Job, error) {
	var out []models.Job
	if limit <= 0 {
		limit = 100
	}
	err := q.DB.Find(ctx, store.CJobs, q.WorkspaceID, filter, &out, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(limit))
	if out == nil {
		out = []models.Job{}
	}
	return out, err
}

// Wait polls a job until it reaches a terminal state or ctx ends.
func (q *Queue) Wait(ctx context.Context, id string, every time.Duration, onUpdate func(*models.Job)) (*models.Job, error) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		j, err := q.Get(ctx, id)
		if err != nil {
			return nil, err
		}
		if onUpdate != nil {
			onUpdate(j)
		}
		switch j.State {
		case models.JobSucceeded, models.JobFailed, models.JobCanceled:
			return j, nil
		}
		select {
		case <-ctx.Done():
			return j, ctx.Err()
		case <-t.C:
		}
	}
}

// Start records work that runs right here, in the calling process, as a job
// that is already running and leased to w: it shows in activity, job details
// and notifications like any other job, and no other worker can take it while
// the caller keeps the lease alive with Heartbeat. Finish it with Succeed, Fail
// or Canceled. If the caller dies, the lease expires and a worker handling the
// type recovers it (for narration: marks it interrupted).
func (q *Queue) Start(ctx context.Context, o EnqueueOptions, w Worker) (*models.Job, error) {
	if o.IdempotencyKey == "" {
		o.IdempotencyKey = ids.New("idk")
	}
	if o.MaxAttempts <= 0 {
		o.MaxAttempts = 1
	}
	if o.Payload == nil {
		o.Payload = map[string]any{}
	}
	now := Clock().UTC()
	j := &models.Job{
		ID: o.jobID(), WorkspaceID: q.WorkspaceID, Type: o.Type, SchemaVersion: 1, State: models.JobRunning,
		Priority: o.Priority, Heavy: o.Heavy, ProjectID: o.ProjectID, AssetID: o.AssetID, RenderID: o.RenderID, Installation: o.Installation,
		Payload: o.Payload, IdempotencyKey: o.IdempotencyKey, MaxAttempts: o.MaxAttempts, AttemptCount: 1,
		Attempts: []models.Attempt{{Worker: w.ID, WorkerName: w.Name, StartedAt: now}},
		Lease:    &models.Lease{Owner: w.ID, OwnerName: w.Name},
		Progress: models.Progress{Message: "starting"}, NextRunAt: now, CreatedBy: o.Actor, CreatedAt: now, UpdatedAt: now,
	}
	// Inserted with the lease's end and nextRunAt in the database server's
	// time: an upsert that matches nothing (a job with that ID already there
	// is refused, as an insert would be).
	doc, err := bson.Marshal(j)
	if err != nil {
		return nil, err
	}
	var raw bson.D
	if err := bson.Unmarshal(doc, &raw); err != nil {
		return nil, err
	}
	if _, err := q.DB.C(store.CJobs).UpdateOne(ctx, bson.M{"_id": j.ID, "state": "__starting"},
		mongo.Pipeline{
			{{Key: "$replaceWith", Value: literal(raw)}},
			{{Key: "$set", Value: bson.D{{Key: "lease.expiresAt", Value: leaseEnd()}, {Key: "nextRunAt", Value: "$$NOW"}}}},
		}, options.UpdateOne().SetUpsert(true)); err != nil {
		return nil, err
	}
	if got, err := q.Get(ctx, j.ID); err == nil {
		j = got
	}
	q.emit(ctx, Event{Kind: EventLeased, Job: *j, WorkerID: w.ID, WorkerName: w.Name})
	return j, nil
}

// LeaseByID claims one specific job (inline `--local` execution).
func (q *Queue) LeaseByID(ctx context.Context, w Worker, id string) (*models.Job, error) {
	now := Clock().UTC()
	filter := bson.M{"_id": id, "workspaceId": q.WorkspaceID, "cancelRequested": false, "installation": pinnedTo(w.ID),
		"$or": bson.A{
			bson.M{"state": models.JobQueued},
			leaseOver(),
		}}
	update := takeLease(w, now, models.Progress{Message: "starting"})
	var j models.Job
	err := q.DB.C(store.CJobs).FindOneAndUpdate(ctx, filter, update, options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&j)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err == nil {
		q.emit(ctx, Event{Kind: EventLeased, Job: j, WorkerID: w.ID, WorkerName: w.Name})
	}
	return &j, err
}

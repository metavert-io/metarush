package ops

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/store"
)

func strconvF(v float64) string { return strconv.FormatFloat(v, 'f', 2, 64) }
func nowStamp() string          { return time.Now().UTC().Format("20060102T150405.000") }

// takeBackRender removes a render that render.submit saved but whose job was
// never queued. If it can't be removed it is closed instead (state, with the
// plain words people read), so it never waits for a job that doesn't exist. It
// still runs when the request that asked is gone.
func takeBackRender(ctx context.Context, a *app.App, id, state, plain string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 15*time.Second)
	defer cancel()
	if _, err := a.DB.C(store.CRenders).DeleteOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()}); err == nil {
		return
	}
	_ = a.DB.Update(ctx, store.CRenders, a.WorkspaceID(), id, bson.M{"$set": bson.M{"state": state, "error": plain}})
}

// RenderSubmitIn requests a render.
type RenderSubmitIn struct {
	RevisionID     string `json:"revisionId"`
	Preset         string `json:"preset,omitempty" jsonschema:"enum=vertical-1080p,enum=landscape-1080p,enum=square-1080,description=Override the revision's output preset for this render."`
	Quality        string `json:"quality,omitempty" jsonschema:"enum=review,enum=final,description=review = half resolution and fast; final = full quality from originals."`
	IdempotencyKey string `json:"idempotencyKey,omitempty"`
	// NoQA skips the automatic quality check of the finished render (it runs by default).
	NoQA bool `json:"noQA,omitempty" jsonschema:"description=Do not check the finished render automatically (by default every render gets a quality check that also listens to its speech)."`
}

// RenderOut is a render with its job.
type RenderOut struct {
	Render  models.Render `json:"render"`
	Job     *models.Job   `json:"job,omitempty"`
	Created bool          `json:"created"`
	Workers int           `json:"onlineWorkers"`
	Note    string        `json:"note,omitempty"`
	URL     string        `json:"url"`
}

// RenderIDIn selects a render.
type RenderIDIn struct {
	ID string `json:"id"`
}

// RenderListIn filters renders.
type RenderListIn struct {
	ProjectID  string `json:"projectId,omitempty"`
	RevisionID string `json:"revisionId,omitempty"`
	Limit      int64  `json:"limit,omitempty"`
}

// RenderListOut lists renders.
type RenderListOut struct {
	Renders []models.Render `json:"renders"`
}

// RenderFetchIn copies a finished render to a local path.
type RenderFetchIn struct {
	ID  string `json:"id"`
	Out string `json:"out,omitempty" jsonschema:"description=Destination file or directory (default: print the cached path)."`
}

// RenderFetchOut is where the file landed.
type RenderFetchOut struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// JobListIn filters jobs.
type JobListIn struct {
	State     string `json:"state,omitempty" jsonschema:"enum=queued,enum=running,enum=succeeded,enum=failed,enum=canceled"`
	ProjectID string `json:"projectId,omitempty"`
	AssetID   string `json:"assetId,omitempty"`
	Type      string `json:"type,omitempty"`
	Limit     int64  `json:"limit,omitempty"`
}

// JobListOut lists jobs.
type JobListOut struct {
	Jobs []models.Job `json:"jobs"`
}

// JobIDIn selects a job.
type JobIDIn struct {
	ID string `json:"id"`
}

// CommentAddIn adds a review comment.
type CommentAddIn struct {
	ProjectID  string   `json:"projectId"`
	RevisionID string   `json:"revisionId,omitempty"`
	AssetID    string   `json:"assetId,omitempty"`
	At         *float64 `json:"at,omitempty"`
	Body       string   `json:"body" jsonschema:"minLength=1,maxLength=4000"`
}

// CommentListIn filters comments.
type CommentListIn struct {
	ProjectID  string `json:"projectId"`
	RevisionID string `json:"revisionId,omitempty"`
}

// CommentListOut lists comments.
type CommentListOut struct {
	Comments []models.Comment `json:"comments"`
}

// AuditListIn filters the audit log.
type AuditListIn struct {
	ProjectID string `json:"projectId,omitempty"`
	TargetID  string `json:"targetId,omitempty"`
	Limit     int64  `json:"limit,omitempty"`
}

// AuditListOut lists audit events.
type AuditListOut struct {
	Events []models.AuditEvent `json:"events"`
}

// renderQuality is the quality a request asks for (final when it doesn't say).
func renderQuality(in *RenderSubmitIn) string {
	if in.Quality == "" {
		return "final"
	}
	return in.Quality
}

// renderKey is the job key of a render of a version in a shape and quality:
// asking again gives the same render, on any machine (D187 uses it too).
func renderKey(revisionID, preset, quality string) string {
	return fmt.Sprintf("render:%s:%s:%s", revisionID, preset, quality)
}

// submitRender saves a render of r and queues its job. follow marks a review
// copy MetaRush makes by itself to follow the cut's latest version (D187): a
// heavy job (it waits on battery like other big jobs, never silently), made
// as MetaRush for the person who saved the version.
func submitRender(ctx context.Context, a *app.App, r *models.Revision, in *RenderSubmitIn, follow bool) (*RenderOut, error) {
	quality := renderQuality(in)
	preset := in.Preset
	if preset == "" {
		preset = r.Spec.Output.Preset
	}
	if preset != "" {
		if _, ok := editspec.Presets[preset]; !ok {
			return nil, invalid("“%s” isn't a video shape MetaRush renders: choose vertical-1080p, landscape-1080p or square-1080.", preset)
		}
	}
	// A cut using a motion graphic no person has approved (or whose approval
	// was withdrawn) is refused here, plainly, instead of queued to fail in the
	// worker as "the render didn't finish".
	if _, err := motion.RequireApproved(ctx, a.DB, a.WorkspaceID(), &r.Spec); err != nil {
		return nil, err
	}
	key := in.IdempotencyKey
	if key == "" {
		key = renderKey(r.ID, preset, quality)
	}
	// reuse answers with the render that an existing job for the same key makes.
	reuse := func(j *models.Job) (*RenderOut, bool) {
		var rd models.Render
		if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), j.RenderID, &rd); err != nil {
			return nil, false
		}
		w, _ := OnlineWorkers(ctx, a, jobs.TypeRender)
		return &RenderOut{Render: rd, Job: j, Created: false, Workers: len(w), Note: "This version was already rendered this way, so here is that render.", URL: dashboardURL(a, "/renders/"+rd.ID)}, true
	}
	// Reuse an existing render for the same key unless it failed/canceled.
	var existing models.Job
	if err := a.DB.C(store.CJobs).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "idempotencyKey": key}).Decode(&existing); err == nil {
		if existing.State != models.JobFailed && existing.State != models.JobCanceled {
			if out, ok := reuse(&existing); ok {
				return out, nil
			}
		}
		if follow {
			// MetaRush never makes a version's review copy again by itself:
			// one that failed or was stopped waits for a person.
			return nil, apperr.New(apperr.Conflict, "This version's review copy was already asked for once.")
		}
		key = key + ":retry:" + nowStamp()
	}
	// A new render of a cut that plays a Remotion clip nobody approved is refused now (D136).
	if RemotionRenderCheck != nil {
		if err := RemotionRenderCheck(ctx, a, r); err != nil {
			return nil, err
		}
	}
	// The render is saved before its job is queued: a worker may lease the job
	// the moment it exists, and it must find its render (queueing first let a
	// fast worker fail the job with "render not found"). The job's ID is chosen
	// up front so the saved render already points at it.
	now := time.Now().UTC()
	jobID := ids.New(ids.Job)
	rd := models.Render{ID: ids.New(ids.Render), WorkspaceID: a.WorkspaceID(), ProjectID: r.ProjectID, RevisionID: r.ID, RevisionNumber: r.Number, SpecHash: r.SpecHash,
		Preset: preset, Quality: quality, JobID: jobID, State: models.RenderQueued, CreatedBy: a.Actor, CreatedAt: now, Follow: follow}
	if err := a.DB.Insert(ctx, store.CRenders, rd); err != nil {
		return nil, err
	}
	payload := map[string]any{"renderId": rd.ID, "revisionId": r.ID, "noQA": in.NoQA}
	if follow {
		payload["follow"] = true
	}
	j, created, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{ID: jobID, Type: jobs.TypeRender, IdempotencyKey: key, Priority: 15, Heavy: quality == "final" || follow, ProjectID: r.ProjectID, RenderID: rd.ID,
		Payload: payload, MaxAttempts: 2, Actor: a.Actor})
	if err != nil {
		// No job will ever make this render: take it back, so nothing waits for it.
		takeBackRender(ctx, a, rd.ID, models.RenderFailed, "MetaRush couldn't start this render, so it was never made. Render it again.")
		return nil, err
	}
	if !created && j.ID != jobID {
		// Another request with the same key queued its render at the same
		// moment: that render is the one, and this one was never queued.
		takeBackRender(ctx, a, rd.ID, models.RenderCanceled, "This render was asked for twice at the same moment, so MetaRush made the other one.")
		if out, ok := reuse(j); ok {
			return out, nil
		}
		return nil, apperr.New(apperr.Conflict, "This render was asked for twice at the same moment. Ask for it again to see it.")
	}
	action := "render.submit"
	if follow {
		action = "render.follow"
	}
	a.Audit(ctx, action, "render", rd.ID, rd.ProjectID, map[string]any{"revisionId": r.ID, "preset": preset, "quality": quality})
	w, _ := OnlineWorkers(ctx, a, jobs.TypeRender)
	out := &RenderOut{Render: rd, Job: j, Created: true, Workers: len(w), URL: dashboardURL(a, "/renders/"+rd.ID)}
	if len(w) == 0 {
		out.Note = "It's waiting to start: no machine running MetaRush can render right now. Start MetaRush on a computer that renders, or render it here in the terminal."
	}
	return out, nil
}

func init() {
	Def("render.submit", "Queue a render of a revision; returns the render and job (it is NOT done until state is available)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *RenderSubmitIn) (*RenderOut, error) {
		r, _, err := getRevision(ctx, a, in.RevisionID)
		if err != nil {
			return nil, err
		}
		out, err := submitRender(ctx, a, r, in, false)
		if err == nil && renderQuality(in) == "review" && askedByPerson(a.Actor) {
			// Someone asked for a review copy of this version: it waits for
			// their review (D188), unlike a hand edit saved in the background.
			markReviewAsked(ctx, a, r, models.ReviewAskCopy)
		}
		return out, err
	})
	Def("render.status", "Show a render and its job progress", Flags{}, func(ctx context.Context, a *app.App, in *RenderIDIn) (*RenderOut, error) {
		var rd models.Render
		if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), in.ID, &rd); err != nil {
			return nil, notFound("render", in.ID)
		}
		j, _ := a.Queue.Get(ctx, rd.JobID)
		w, _ := OnlineWorkers(ctx, a, jobs.TypeRender)
		return &RenderOut{Render: rd, Job: j, Workers: len(w), URL: dashboardURL(a, "/renders/"+rd.ID)}, nil
	})
	Def("render.list", "List renders", Flags{}, func(ctx context.Context, a *app.App, in *RenderListIn) (*RenderListOut, error) {
		filter := bson.M{}
		if in.ProjectID != "" {
			filter["projectId"] = in.ProjectID
		}
		if in.RevisionID != "" {
			filter["revisionId"] = in.RevisionID
		}
		if in.Limit <= 0 {
			in.Limit = 50
		}
		out := &RenderListOut{}
		if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), filter, &out.Renders, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(in.Limit)); err != nil {
			return nil, err
		}
		if out.Renders == nil {
			out.Renders = []models.Render{}
		}
		return out, nil
	})
	Def("render.cancel", "Cancel a queued or running render", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *RenderIDIn) (*RenderOut, error) {
		var rd models.Render
		if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), in.ID, &rd); err != nil {
			return nil, notFound("render", in.ID)
		}
		j, err := a.Queue.RequestCancel(ctx, rd.JobID)
		if err != nil {
			return nil, err
		}
		if j.State == models.JobCanceled {
			_ = a.DB.Update(ctx, store.CRenders, a.WorkspaceID(), rd.ID, bson.M{"$set": bson.M{"state": models.RenderCanceled}})
			rd.State = models.RenderCanceled
		}
		a.Audit(ctx, "render.cancel", "render", rd.ID, rd.ProjectID, nil)
		return &RenderOut{Render: rd, Job: j, URL: dashboardURL(a, "/renders/"+rd.ID)}, nil
	})
	Def("render.fetch", "Download a finished render to a local file (verified by SHA-256)", Flags{LocalOnly: true}, func(ctx context.Context, a *app.App, in *RenderFetchIn) (*RenderFetchOut, error) {
		var rd models.Render
		if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), in.ID, &rd); err != nil {
			return nil, notFound("render", in.ID)
		}
		if rd.State != models.RenderAvailable || rd.Output == nil {
			return nil, apperr.New(apperr.Conflict, "The %s isn't finished (%s), so there's no video to copy yet.", strings.ToLower(models.RenderLabel(rd.Quality, rd.RevisionNumber)), renderStateWords(rd.State)).
				WithDetails(map[string]any{"renderId": rd.ID, "state": rd.State})
		}
		p, err := a.Cache.Ensure(ctx, a.Media, rd.Output.StorageKey, rd.Output.SHA256, rd.Output.Size, nil)
		if err != nil {
			return nil, apperr.Wrap(apperr.Storage, err, "The %s couldn't be copied to this machine.", strings.ToLower(models.RenderLabel(rd.Quality, rd.RevisionNumber))).
				WithHint("Check the internet connection and try again.").WithDetails(map[string]any{"renderId": rd.ID})
		}
		if in.Out == "" {
			return &RenderFetchOut{Path: p, Size: rd.Output.Size, SHA256: rd.Output.SHA256}, nil
		}
		dst := in.Out
		if st, err := os.Stat(dst); err == nil && st.IsDir() {
			dst = filepath.Join(dst, fmt.Sprintf("%s-r%d-%s.mp4", rd.ProjectID, rd.RevisionNumber, rd.Preset))
		}
		if _, err := os.Stat(dst); err == nil {
			return nil, apperr.New(apperr.Conflict, "There's already a file named %s, and MetaRush never overwrites files.", filepath.Base(dst)).
				WithHint("Choose another place, or move that file first.").WithDetails(map[string]any{"existing": dst})
		}
		if err := copyOut(p, dst); err != nil {
			return nil, err
		}
		return &RenderFetchOut{Path: dst, Size: rd.Output.Size, SHA256: rd.Output.SHA256}, nil
	})
	Def("job.list", "List jobs (newest first)", Flags{}, func(ctx context.Context, a *app.App, in *JobListIn) (*JobListOut, error) {
		filter := bson.M{}
		for k, v := range map[string]string{"state": in.State, "projectId": in.ProjectID, "assetId": in.AssetID, "type": in.Type} {
			if v != "" {
				filter[k] = v
			}
		}
		js, err := a.Queue.List(ctx, filter, in.Limit)
		if err != nil {
			return nil, err
		}
		return &JobListOut{Jobs: js}, nil
	})
	Def("job.show", "Show one job in plain words: what it is, who started it, where it ran, why it waits, what it produced, and related project/clip/version/render/QA report", Flags{}, func(ctx context.Context, a *app.App, in *JobIDIn) (*JobShowOut, error) {
		return JobDetail(ctx, a, in.ID)
	})
	Def("job.cancel", "Cancel a job", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *JobIDIn) (*models.Job, error) {
		j, err := a.Queue.RequestCancel(ctx, in.ID)
		if err != nil {
			return nil, notFound("job", in.ID)
		}
		a.Audit(ctx, "job.cancel", "job", in.ID, j.ProjectID, nil)
		return j, nil
	})
	Def("job.retry", "Re-queue a failed or canceled job", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *JobIDIn) (*models.Job, error) {
		if cur, err := a.Queue.Get(ctx, in.ID); err == nil && cur.Type == jobs.TypeVoiceover {
			return nil, apperr.New(apperr.Conflict, "narration is started again where it was asked for, because it needs that computer's key and files").
				WithHint("run the same narration command again (lines already made are reused at no cost)")
		}
		j, err := a.Queue.Retry(ctx, in.ID)
		if err != nil {
			return nil, apperr.Wrap(apperr.Conflict, err, "Only a job that failed or was stopped can be started again.").WithDetails(map[string]any{"jobId": in.ID})
		}
		if j.RenderID != "" {
			_ = a.DB.Update(ctx, store.CRenders, a.WorkspaceID(), j.RenderID, bson.M{"$set": bson.M{"state": models.RenderQueued}, "$unset": bson.M{"error": ""}})
		}
		a.Audit(ctx, "job.retry", "job", in.ID, j.ProjectID, nil)
		return j, nil
	})
	Def("comment.add", "Add a review comment (optionally pinned to a time)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CommentAddIn) (*models.Comment, error) {
		body := strings.TrimSpace(in.Body)
		if body == "" {
			return nil, invalid("body is required")
		}
		c := models.Comment{ID: ids.New(ids.Comment), WorkspaceID: a.WorkspaceID(), ProjectID: in.ProjectID, RevisionID: in.RevisionID, AssetID: in.AssetID, At: in.At, Body: body, Author: a.Actor, CreatedAt: time.Now().UTC()}
		if err := a.DB.Insert(ctx, store.CComments, c); err != nil {
			return nil, err
		}
		return &c, nil
	})
	Def("comment.list", "List review comments for a project or revision", Flags{}, func(ctx context.Context, a *app.App, in *CommentListIn) (*CommentListOut, error) {
		filter := bson.M{"projectId": in.ProjectID}
		if in.RevisionID != "" {
			filter["revisionId"] = in.RevisionID
		}
		out := &CommentListOut{}
		if err := a.DB.Find(ctx, store.CComments, a.WorkspaceID(), filter, &out.Comments, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}})); err != nil {
			return nil, err
		}
		if out.Comments == nil {
			out.Comments = []models.Comment{}
		}
		QuoteOutside(a, out.Comments)
		return out, nil
	})
	Def("audit.list", "Read the audit trail (who did what, from which machine)", Flags{}, func(ctx context.Context, a *app.App, in *AuditListIn) (*AuditListOut, error) {
		filter := bson.M{}
		if in.ProjectID != "" {
			filter["projectId"] = in.ProjectID
		}
		if in.TargetID != "" {
			filter["targetId"] = in.TargetID
		}
		if in.Limit <= 0 {
			in.Limit = 100
		}
		out := &AuditListOut{}
		if err := a.DB.Find(ctx, store.CAudit, a.WorkspaceID(), filter, &out.Events, options.Find().SetSort(bson.D{{Key: "at", Value: -1}}).SetLimit(in.Limit)); err != nil {
			return nil, err
		}
		if out.Events == nil {
			out.Events = []models.AuditEvent{}
		}
		return out, nil
	})
}

func copyOut(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	tmp := dst + ".part"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(tmp)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, dst)
}

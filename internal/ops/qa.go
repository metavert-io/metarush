package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/qa"
	"github.com/metavert-io/metarush/internal/store"
)

// QARunIn requests QA of a finished render.
type QARunIn struct {
	ID    string `json:"id" jsonschema:"description=Render ID (rnd_…), or a revision ID (rev_…) to check its newest available render."`
	Force bool   `json:"force,omitempty" jsonschema:"description=Run again even if this render already has a report for the current check version."`
	// NoSpeechCheck skips transcribing the render (the speech continuity check).
	NoSpeechCheck bool `json:"noSpeechCheck,omitempty" jsonschema:"description=Skip listening to the render's speech word by word (faster)."`
}

// QARunOut is the queued QA job (or the existing report).
type QARunOut struct {
	RenderID string           `json:"renderId"`
	Job      *models.Job      `json:"job,omitempty"`
	Created  bool             `json:"created"`
	Report   *models.QAReport `json:"report,omitempty"` // set when an identical QA already finished
	Workers  int              `json:"onlineWorkers"`
	Note     string           `json:"note,omitempty"`
}

// QAShowIn selects a report.
type QAShowIn struct {
	ID         string `json:"id" jsonschema:"description=Report (qar_…), render (rnd_…: its latest report) or revision (rev_…: the latest report of any of its renders)."`
	LocalFiles bool   `json:"localFiles,omitempty" jsonschema:"description=Copy the sheets and frames into this machine's scratch folder and return their paths (the CLI does this)."`
}

// QAShowOut is a report.
type QAShowOut struct {
	Report models.QAReport `json:"report"`
	Dir    string          `json:"dir,omitempty"`
	URL    string          `json:"url"`
	Note   string          `json:"note"`
}

// resolveQARender finds the render a QA request is about.
func resolveQARender(ctx context.Context, a *app.App, id string) (*models.Render, error) {
	var rd models.Render
	switch ids.Prefix(id) {
	case ids.Render:
		if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), id, &rd); err != nil {
			return nil, notFound("render", id)
		}
	case ids.Revision:
		err := a.DB.C(store.CRenders).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "revisionId": id, "state": models.RenderAvailable},
			options.FindOne().SetSort(bson.D{{Key: "completedAt", Value: -1}})).Decode(&rd)
		if errors.Is(err, mongo.ErrNoDocuments) {
			return nil, apperr.New(apperr.NotFound, "%s hasn't been rendered yet, so there's nothing to check.", numberedVersion(ctx, a, id)).
				WithHint("Render it first (on the Renders page, or ask Claude Code), then check it.").
				WithCommand("metarush render submit --revision " + id + " --quality review --wait")
		}
		if err != nil {
			return nil, err
		}
	default:
		return nil, apperr.New(apperr.InvalidInput, "That isn't a render or a version MetaRush knows.").
			WithDetails(map[string]any{"id": id, "expected": "a render (rnd_…) or revision (rev_…) ID"})
	}
	if rd.State != models.RenderAvailable || rd.Output == nil {
		return nil, apperr.New(apperr.Conflict, "The %s isn't finished (%s), so it can't be checked yet.", strings.ToLower(models.RenderLabel(rd.Quality, rd.RevisionNumber)), renderStateWords(rd.State)).
			WithHint("Wait until it's finished: the Renders page shows how far it is.").
			WithCommand("metarush render status " + rd.ID)
	}
	return &rd, nil
}

// numberedVersion names a revision for people: "Version 3" (or "That version").
func numberedVersion(ctx context.Context, a *app.App, id string) string {
	var r models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), id, &r); err == nil && r.Number > 0 {
		return fmt.Sprintf("Version %d", r.Number)
	}
	return "That version"
}

// qaMissing says that what was asked about has no quality check yet.
func qaMissing(ctx context.Context, a *app.App, id string) error {
	what := "That render"
	switch ids.Prefix(id) {
	case models.QAReportPrefix:
		return apperr.New(apperr.NotFound, "That quality check isn't in this workspace.").WithDetails(map[string]any{"reportId": id})
	case ids.Render:
		var rd models.Render
		if err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), id, &rd); err != nil {
			return notFound("render", id)
		}
		what = "The " + strings.ToLower(models.RenderLabel(rd.Quality, rd.RevisionNumber))
	case ids.Revision:
		what = numberedVersion(ctx, a, id)
	}
	return apperr.New(apperr.NotFound, "%s hasn't had a quality check yet.", what).
		WithHint("Use “Check quality” in the render's details on the Renders page, or ask Claude Code to check it.").
		WithDetails(map[string]any{"id": id}).WithCommand("metarush qa run " + id + " --wait")
}

// renderStateWords says where an unfinished render is.
func renderStateWords(state string) string {
	switch state {
	case models.RenderQueued:
		return "it's waiting to start"
	case models.RenderRendering:
		return "it's still rendering"
	case models.RenderUploading:
		return "it's being saved"
	case models.RenderFailed:
		return "it failed"
	case models.RenderCanceled:
		return "it was canceled"
	}
	return "it has no video yet"
}

func latestQA(ctx context.Context, a *app.App, filter bson.M) (*models.QAReport, error) {
	filter["workspaceId"] = a.WorkspaceID()
	var rep models.QAReport
	err := a.DB.C(store.CQAReports).FindOne(ctx, filter, options.FindOne().SetSort(bson.D{{Key: "createdAt", Value: -1}})).Decode(&rep)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, store.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	return &rep, nil
}

// materializeQA copies a report's images from the media store (via the verified
// cache) into this machine's QA folder and fills in their local paths.
func materializeQA(ctx context.Context, a *app.App, rep *models.QAReport) (string, error) {
	dir := qa.Dir(a.Cfg.Scratch.Dir, rep.ID)
	one := func(f *models.QAFrame) error {
		if f == nil || f.Key == "" || f.File == "" || strings.Contains(f.File, "..") {
			return nil
		}
		dst := filepath.Join(dir, filepath.FromSlash(f.File))
		if st, err := os.Stat(dst); err == nil && st.Size() == f.Size {
			f.Path = dst
			return nil
		}
		src, err := a.Cache.Ensure(ctx, a.Media, f.Key, f.SHA256, f.Size, nil)
		if err != nil {
			return apperr.Wrap(apperr.Storage, err, "The quality check's frames couldn't be copied to this machine.").WithDetails(map[string]any{"file": f.File, "reportId": rep.ID})
		}
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			return err
		}
		_ = os.Remove(dst + ".part")
		if err := copyOut(src, dst); err != nil {
			return err
		}
		f.Path = dst
		return nil
	}
	if err := one(rep.ContactSheet); err != nil {
		return "", err
	}
	if err := one(rep.MomentsSheet); err != nil {
		return "", err
	}
	for i := range rep.Frames {
		if err := one(&rep.Frames[i]); err != nil {
			return "", err
		}
	}
	return dir, nil
}

const qaNote = "Findings: severity error/warning/info; basis observed (measured on the decoded render) or heuristic (plan geometry, conventions). " +
	"Fix ops are suggestions against the rendered revision — write them into a plan and run `metarush edit check`; never apply them blindly. " +
	"Read the contact sheet and frames before making visual judgments."

func init() {
	Def("qa.run", "Queue QA of a finished render: decode it, check conformance, black/frozen frames, loudness, silence vs. dialogue and layout, and extract frames", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *QARunIn) (*QARunOut, error) {
		rd, err := resolveQARender(ctx, a, in.ID)
		if err != nil {
			return nil, err
		}
		w, _ := OnlineWorkers(ctx, a, jobs.TypeQA)
		out := &QARunOut{RenderID: rd.ID, Workers: len(w)}
		key := "qa:" + rd.ID + ":" + qa.Version
		if in.Force {
			key += ":" + nowStamp()
		}
		if in.NoSpeechCheck {
			key += ":nospeech"
		}
		var existing models.Job
		if err := a.DB.C(store.CJobs).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "idempotencyKey": key}).Decode(&existing); err == nil {
			switch existing.State {
			case models.JobSucceeded:
				if rep, err := latestQA(ctx, a, bson.M{"renderId": rd.ID, "version": qa.Version}); err == nil {
					out.Job, out.Report = &existing, rep
					out.Note = "This render was already checked with today's checks, so here is that report (ask to check it again to run it once more)."
					return out, nil
				}
				key += ":rerun:" + nowStamp()
			case models.JobQueued, models.JobRunning:
				out.Job, out.Note = &existing, "This render's quality check is already waiting or running."
				return out, nil
			default:
				key += ":retry:" + nowStamp()
			}
		}
		// The job deliberately carries the render only in its payload: job.RenderID is
		// reserved for render jobs (a failed QA must never mark the render failed).
		j, created, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeQA, IdempotencyKey: key, Priority: 12, ProjectID: rd.ProjectID,
			Payload: map[string]any{"renderId": rd.ID, "revisionId": rd.RevisionID, "noSpeechCheck": in.NoSpeechCheck}, MaxAttempts: 2, Actor: a.Actor})
		if err != nil {
			return nil, err
		}
		out.Job, out.Created = j, created
		a.Audit(ctx, "qa.run", "render", rd.ID, rd.ProjectID, map[string]any{"jobId": j.ID})
		if len(w) == 0 {
			out.Note = "It's waiting to start: no machine running MetaRush can check renders right now. Start MetaRush, or wait for the check in this terminal."
		}
		return out, nil
	})
	Def("qa.show", "Show a render's QA report: findings with timecodes, basis and suggested edit-plan fixes, measurements, and contact sheet/frame images", Flags{}, func(ctx context.Context, a *app.App, in *QAShowIn) (*QAShowOut, error) {
		var rep *models.QAReport
		var err error
		switch ids.Prefix(in.ID) {
		case models.QAReportPrefix:
			var r models.QAReport
			if err = a.DB.GetByID(ctx, store.CQAReports, a.WorkspaceID(), in.ID, &r); err == nil {
				rep = &r
			}
		case ids.Render:
			rep, err = latestQA(ctx, a, bson.M{"renderId": in.ID})
		case ids.Revision:
			rep, err = latestQA(ctx, a, bson.M{"revisionId": in.ID})
		default:
			return nil, apperr.New(apperr.InvalidInput, "That isn't a quality check, a render or a version MetaRush knows.").
				WithDetails(map[string]any{"id": in.ID, "expected": "a QA report (qar_…), render (rnd_…) or revision (rev_…) ID"})
		}
		if errors.Is(err, store.ErrNotFound) {
			return nil, qaMissing(ctx, a, in.ID)
		}
		if err != nil {
			return nil, err
		}
		out := &QAShowOut{Report: *rep, URL: dashboardURL(a, "/renders/"+rep.RenderID), Note: qaNote}
		if in.LocalFiles {
			dir, err := materializeQA(ctx, a, &out.Report)
			if err != nil {
				return nil, err
			}
			out.Dir = dir
		}
		return out, nil
	})
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("qa.run", Offering{ID: "quality-check", Group: "deliver", Title: "Quality check",
		Does:  "Watches and listens to a finished video before it goes out: words cut off, captions over faces, sound too quiet.",
		Words: []string{"is the finished video ok", "problems before it goes out"}, Needs: Needs{Works: []string{"checks"}}})
}

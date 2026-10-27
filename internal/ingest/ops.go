package ingest

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// FilesIn ingests local files or folders.
type FilesIn struct {
	Paths      []string `json:"paths" jsonschema:"minItems=1"`
	ProjectID  string   `json:"projectId,omitempty"`
	Recursive  bool     `json:"recursive,omitempty"`
	Tags       []string `json:"tags,omitempty"`
	Transcribe *bool    `json:"transcribe,omitempty" jsonschema:"description=Schedule local transcription (default true)."`
	DryRun     bool     `json:"dryRun,omitempty" jsonschema:"description=List what would be ingested without uploading."`
	Duplicates string   `json:"duplicates,omitempty" jsonschema:"enum=skip,enum=keep,description=A file identical to a stored asset under another name: skip it (default) or keep both names (an alias; the bytes are stored once)."`
}

// FilesOut is a digest.
type FilesOut struct {
	Results    []Result `json:"results"`
	Ingested   int      `json:"ingested"`
	Deduped    int      `json:"deduped"`
	Duplicates int      `json:"duplicates" jsonschema:"description=Files identical to a stored asset under another name that were skipped."`
	Resumed    int      `json:"resumed"`
	Skipped    int      `json:"skipped"`
	Failed     int      `json:"failed"`
	DryRun     bool     `json:"dryRun"`
	Note       string   `json:"note,omitempty"`
}

// SessionsIn filters ingest sessions.
type SessionsIn struct {
	State string `json:"state,omitempty"`
	Limit int64  `json:"limit,omitempty"`
}

// SessionsOut lists sessions.
type SessionsOut struct {
	Sessions []models.IngestSession `json:"sessions"`
}

// ReconcileIn cleans up abandoned uploads.
type ReconcileIn struct {
	OlderThanHours float64 `json:"olderThanHours,omitempty"`
	DryRun         bool    `json:"dryRun,omitempty"`
}

// ReconcileOut reports cleanup.
type ReconcileOut struct {
	AbortedUploads []string `json:"abortedUploads"`
	StaleSessions  []string `json:"staleSessions"`
	DryRun         bool     `json:"dryRun"`
}

// Progress is set by the CLI to render progress bars.
var Progress func(path, phase string, done, total int64)

func init() {
	ops.Def("ingest.files", "Ingest local files/folders: probe, hash, dedupe, upload (resumable), verify, then process", ops.Flags{Mutating: true, LocalOnly: true}, func(ctx context.Context, a *app.App, in *FilesIn) (*FilesOut, error) {
		out := &FilesOut{DryRun: in.DryRun}
		if in.DryRun {
			files, skipped := Collect(in.Paths, in.Recursive)
			out.Results = skipped
			for _, f := range files {
				out.Results = append(out.Results, Result{Path: f, Status: "would-ingest"})
			}
			if out.Results == nil {
				out.Results = []Result{}
			}
			return out, nil
		}
		tr := in.Transcribe == nil || *in.Transcribe
		if in.Duplicates != "" && in.Duplicates != DuplicatesSkip && in.Duplicates != DuplicatesKeep {
			return nil, apperr.New(apperr.InvalidInput, "For a file that's already in MetaRush under another name, choose skip or keep (both names).")
		}
		if in.ProjectID != "" {
			// A project's ID or its name ("fernbrook"); never an unknown project.
			p, err := ops.ResolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			in.ProjectID = p.ID
		}
		out.Results = Paths(ctx, a, in.Paths, Options{ProjectID: in.ProjectID, Tags: in.Tags, Recursive: in.Recursive, Via: a.Actor.Via, Transcribe: tr, Duplicates: in.Duplicates, Progress: Progress})
		for _, r := range out.Results {
			switch r.Status {
			case "ingested":
				out.Ingested++
			case "deduped":
				out.Deduped++
			case "duplicate":
				out.Duplicates++
			case "resumed":
				out.Resumed++
			case "skipped":
				out.Skipped++
			default:
				out.Failed++
			}
		}
		if out.Results == nil {
			out.Results = []Result{}
		}
		if out.Ingested+out.Resumed > 0 {
			out.Note = "The originals are safely stored. Preview copies, transcripts and the rest are being prepared."
		}
		return out, nil
	})
	ops.Def("ingest.sessions", "List ingest sessions (uploads in progress, interrupted or finished)", ops.Flags{}, func(ctx context.Context, a *app.App, in *SessionsIn) (*SessionsOut, error) {
		filter := bson.M{}
		if in.State != "" {
			filter["state"] = in.State
		}
		if in.Limit <= 0 {
			in.Limit = 100
		}
		out := &SessionsOut{}
		if err := a.DB.Find(ctx, store.CIngest, a.WorkspaceID(), filter, &out.Sessions, options.Find().SetSort(bson.D{{Key: "updatedAt", Value: -1}}).SetLimit(in.Limit)); err != nil {
			return nil, err
		}
		if out.Sessions == nil {
			out.Sessions = []models.IngestSession{}
		}
		return out, nil
	})
	ops.Def("ingest.reconcile", "Clean up abandoned uploads and stale ingest sessions (never touches available media)", ops.Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *ReconcileIn) (*ReconcileOut, error) {
		age := in.OlderThanHours
		if age <= 0 {
			age = 24
		}
		cutoff := time.Now().Add(-time.Duration(age * float64(time.Hour)))
		out := &ReconcileOut{AbortedUploads: []string{}, StaleSessions: []string{}, DryRun: in.DryRun}
		inc, err := a.Media.ListIncomplete(ctx)
		if err != nil {
			return nil, err
		}
		for _, u := range inc {
			if u.Initiated.After(cutoff) {
				continue
			}
			out.AbortedUploads = append(out.AbortedUploads, u.Key)
			if !in.DryRun {
				_ = a.Media.AbortIncomplete(ctx, u)
			}
		}
		var stale []models.IngestSession
		if err := a.DB.Find(ctx, store.CIngest, a.WorkspaceID(), bson.M{"state": bson.M{"$in": bson.A{"uploading", "verifying"}}, "updatedAt": bson.M{"$lt": cutoff}}, &stale); err != nil {
			return nil, err
		}
		for _, s := range stale {
			out.StaleSessions = append(out.StaleSessions, s.ID)
			if s.Upload.SessionURI != "" { // GCS sessions are not listable; cancel the one we recorded
				out.AbortedUploads = append(out.AbortedUploads, s.Upload.Key)
				if !in.DryRun {
					_ = a.Media.AbortIncomplete(ctx, media.Incomplete{Key: s.Upload.Key, UploadID: s.Upload.SessionURI})
				}
			}
			if !in.DryRun {
				_ = a.DB.Update(ctx, store.CIngest, a.WorkspaceID(), s.ID, bson.M{"$set": bson.M{"state": "failed", "error": "abandoned (reconciled)", "updatedAt": time.Now().UTC()}})
				_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), s.AssetID, bson.M{"$set": bson.M{"state": models.AssetFailed, "stateReason": "The upload was left unfinished. Add the file again to finish it."}})
			}
		}
		if !in.DryRun {
			a.Audit(ctx, "ingest.reconcile", "workspace", a.WorkspaceID(), "", map[string]any{"aborted": len(out.AbortedUploads), "stale": len(out.StaleSessions)})
		}
		return out, nil
	})
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("ingest.files", ops.Offering{ID: "footage-files", Group: "bring", Title: "Footage from files",
		Does:  "Adds video and sound files: each copied into MetaRush's storage once, then written down and looked at.",
		Words: []string{"upload", "import my recordings", "add clips"}, Needs: ops.Needs{Works: []string{"editing"}}})
}

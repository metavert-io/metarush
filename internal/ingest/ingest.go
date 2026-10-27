// Package ingest is the single pipeline behind CLI ingest, browser drops and the
// watched inbox: probe (by content) → hash → dedupe → resumable upload to the
// canonical store → verify → publish → schedule derivatives. Asset state moves
// staging → verified → available; an interrupted ingest resumes from its session.
package ingest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/notify"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// Result describes one file's outcome.
type Result struct {
	Path    string `json:"path"`
	Name    string `json:"name"`
	Status  string `json:"status" jsonschema:"enum=ingested,enum=deduped,enum=duplicate,enum=resumed,enum=skipped,enum=failed,enum=would-ingest,description=deduped = the same bytes were already stored (same file again or kept as an alias); duplicate = identical to an existing asset under another name and skipped."`
	AssetID string `json:"assetId,omitempty"`
	Title   string `json:"title,omitempty" jsonschema:"description=The asset's display title."`
	// Duplicate is set when the bytes were already stored: which asset they
	// match and what happened (skipped, kept-both, same-file).
	Duplicate *models.DuplicateOf `json:"duplicate,omitempty"`
	SessionID string              `json:"sessionId,omitempty"`
	// Resource is set when the file was a picture, SVG, PDF or font: it went to
	// the resources library instead of footage (D80).
	Resource *ResourceResult `json:"resource,omitempty"`
	Size     int64           `json:"size"`
	SHA256   string          `json:"sha256,omitempty"`
	Kind     string          `json:"kind,omitempty"`
	Duration float64         `json:"duration,omitempty"`
	Error    string          `json:"error,omitempty"`
	JobIDs   []string        `json:"jobIds,omitempty"`
}

// ResourceResult is what a resource file became.
type ResourceResult struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Title   string `json:"title"`
	Kind    string `json:"kind" jsonschema:"enum=image,enum=svg,enum=pdf,enum=font"`
	Scope   string `json:"scope" jsonschema:"enum=project,enum=workspace"`
	Status  string `json:"status" jsonschema:"enum=added,enum=version,enum=same"`
	Message string `json:"message"`
}

// Options configure an ingest.
type Options struct {
	// FootageOnly is for callers that bring in footage they made or chose
	// (generated shots, Remotion renders, call recordings): a picture, SVG,
	// PDF or font is skipped with a plain reason — never sent to the
	// resources library, and never made a clip of a still picture either
	// (the Library and a project's sources hide those since D79, so it would
	// be a clip nobody sees).
	FootageOnly bool
	ProjectID   string
	Tags        []string
	Via         string // cli | dashboard | watch
	Recursive   bool
	Transcribe  bool
	DisplayName string // for browser uploads (temp file name differs)
	// Duplicates decides what happens when the bytes are already stored under
	// another filename: "skip" (default: nothing changes, the digest and a
	// notification say which asset it matches) or "keep" (record the filename as
	// an alias and link the asset to ProjectID; the bytes are never stored twice).
	Duplicates string
	Progress   func(path, phase string, done, total int64)
	// Title is the new asset's display title (default: the file name without
	// its extension). Link imports use the original post's title.
	Title string
	// Source records where an imported file came from (a YouTube, TikTok or X
	// post). It is set on a new asset, and on an existing one that has none.
	Source *models.AssetSource
	// JobID is the link import doing this ingest (recorded on the ingest
	// session so activity shows one row for the import).
	JobID string
}

// Duplicate handling modes.
const (
	DuplicatesSkip = "skip"
	DuplicatesKeep = "keep"
)

var skipNames = map[string]bool{".DS_Store": true, "Thumbs.db": true, "desktop.ini": true}

// Collect expands paths into candidate files (hidden files and folders skipped).
func Collect(paths []string, recursive bool) ([]string, []Result) {
	var files []string
	var skipped []Result
	for _, p := range paths {
		st, err := os.Stat(p)
		if err != nil {
			skipped = append(skipped, Result{Path: p, Name: filepath.Base(p), Status: "failed", Error: FailText(err)})
			continue
		}
		if !st.IsDir() {
			files = append(files, p)
			continue
		}
		_ = filepath.WalkDir(p, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return nil
			}
			name := d.Name()
			if d.IsDir() {
				if path != p && (strings.HasPrefix(name, ".") || !recursive) {
					return filepath.SkipDir
				}
				return nil
			}
			if strings.HasPrefix(name, ".") || skipNames[name] || !d.Type().IsRegular() {
				return nil
			}
			files = append(files, path)
			return nil
		})
	}
	sort.Strings(files)
	return files, skipped
}

// Paths ingests files and directories.
func Paths(ctx context.Context, a *app.App, paths []string, o Options) []Result {
	files, results := Collect(paths, o.Recursive)
	for _, f := range files {
		if ctx.Err() != nil {
			results = append(results, Result{Path: f, Name: filepath.Base(f), Status: "skipped", Error: "Stopped before it was added."})
			continue
		}
		results = append(results, File(ctx, a, f, o))
	}
	return results
}

// File ingests one local file.
func File(ctx context.Context, a *app.App, path string, o Options) Result {
	name := o.DisplayName
	if name == "" {
		name = filepath.Base(path)
	}
	res := Result{Path: path, Name: name}
	clean := func(msg string) string { return strings.ReplaceAll(msg, path, name) } // never leak temp paths
	fail := func(err error) Result {
		res.Status, res.Error = "failed", clean(FailText(err))
		return res
	}
	st, err := os.Stat(path)
	if err != nil {
		return fail(err)
	}
	if !st.Mode().IsRegular() || st.Size() == 0 {
		res.Status, res.Error = "skipped", "It's empty, or not a file MetaRush can add."
		return res
	}
	res.Size = st.Size()
	if o.ProjectID != "" {
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), o.ProjectID, &p); err != nil {
			return fail(apperr.New(apperr.NotFound, "The project it was meant for was not found. It may have been deleted."))
		}
	}
	report := func(phase string, done, total int64) {
		if o.Progress != nil {
			o.Progress(path, phase, done, total)
		}
	}
	// One classifier for everything people add (D80): pictures, SVGs, PDFs and
	// fonts go to the resources library, by what the file is.
	if sn, err := resources.SniffFile(path); err == nil && sn.Route == resources.RouteResource {
		if o.FootageOnly {
			res.Status, res.Error = "skipped", "It's "+resourceWords(sn.Kind)+", not a video or sound file."
			return res
		}
		if err := leave.MayStore(ctx, a, o.ProjectID, []string{models.LeavePictures}, "“"+name+"”"); err != nil { // D290
			return fail(err)
		}
		report("hash", 0, res.Size)
		return asResource(ctx, a, path, name, o, res)
	}
	// What may leave (D290): a client whose footage may not be in cloud
	// storage can't add it to a workspace whose media is.
	if err := leave.MayStore(ctx, a, o.ProjectID, leave.FootageKinds, "“"+name+"”"); err != nil {
		return fail(err)
	}
	report("probe", 0, res.Size)
	mi, err := probe.Probe(ctx, a.Tools, path)
	if err != nil {
		var nm *probe.ErrNotMedia
		if errors.As(err, &nm) {
			res.Status, res.Error = "skipped", FailText(err)
			return res
		}
		return fail(err)
	}
	res.Kind, res.Duration = probe.Kind(mi), mi.Duration
	fp, _, fpErr := media.FingerprintFile(path) // cheap identity (first + last MiB) for pre-upload duplicate checks
	sample, sampleErr := media.SampleFile(path) // confirms fingerprint matches
	sha, err := hashWithProgress(path, res.Size, func(d int64) { report("hash", d, res.Size) })
	if err != nil {
		return fail(err)
	}
	res.SHA256 = sha
	now := time.Now().UTC()
	via := o.Via
	if via == "" {
		via = "cli"
	}
	actor := a.Actor
	actor.Via = via
	key := media.ObjectKey(a.WorkspaceID(), sha)
	var as models.Asset
	err = a.DB.C(store.CAssets).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "sha256": sha}).Decode(&as)
	switch {
	case err == nil && as.State == models.AssetAvailable:
		// The full SHA-256 is authoritative: these bytes are already stored.
		res.AssetID, res.Title = as.ID, as.DisplayTitle()
		backfill := bson.M{} // assets ingested before fingerprints existed
		if as.Fingerprint == "" && fpErr == nil {
			backfill["fingerprint"] = fp
		}
		if as.Sample == "" && sampleErr == nil {
			backfill["sample"] = sample
		}
		if as.Source == nil && o.Source != nil {
			backfill["source"] = o.Source // now we know where these bytes came from
			as.Source = o.Source
		}
		if len(backfill) > 0 {
			_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": backfill})
		}
		// Adding a clip that is in the Trash again brings it back (D98), unless the copy is skipped.
		if as.Trash != nil && (as.HasName(name) || o.Duplicates == DuplicatesKeep) {
			if err := ops.RestoreFromTrash(ctx, a, &as); err != nil {
				return fail(err)
			}
		}
		dup := ops.DescribeDuplicate(ctx, a, &as)
		switch {
		case as.HasName(name):
			dup.Action, res.Status = models.DuplicateSameFile, "deduped"
			if o.ProjectID != "" {
				_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$addToSet": bson.M{"projectIds": o.ProjectID}, "$set": bson.M{"updatedAt": now}})
				refreshCounts(ctx, a, o.ProjectID)
			}
		case o.Duplicates == DuplicatesKeep:
			if err := ops.AddAlias(ctx, a, &as, name, o.ProjectID, via); err != nil {
				return fail(err)
			}
			dup.Action, res.Status = models.DuplicateKeptBoth, "deduped"
		default:
			dup.Action, res.Status = models.DuplicateSkipped, "duplicate"
			notify.DuplicateSkipped(context.WithoutCancel(ctx), a, &as, name, via)
			a.Audit(ctx, "asset.duplicate", "asset", as.ID, o.ProjectID, map[string]any{"name": name, "action": models.DuplicateSkipped, "via": via})
		}
		res.Duplicate = &dup
		_, _ = a.Cache.Adopt(path, sha) // keep a local copy for fast work here
		return res
	case err == nil:
		res.Status = "resumed" // an earlier ingest of these bytes was interrupted
	default:
		projectIDs := []string{}
		if o.ProjectID != "" {
			projectIDs = append(projectIDs, o.ProjectID)
		}
		tags := o.Tags
		if tags == nil {
			tags = []string{}
		}
		// The file name until MetaRush's first look names it (D150); a title an
		// import brings (a link's post, a generated shot's prompt) is MetaRush's.
		title, titleBy := models.DefaultTitle(name), models.TitleByFile
		if t := strings.Join(strings.Fields(o.Title), " "); t != "" {
			title, titleBy = t, models.TitleByMetaRush
		}
		as = models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: projectIDs, Kind: res.Kind, Name: name, Title: title, TitleBy: titleBy, NameAuto: true,
			SHA256: sha, Size: res.Size, MIME: probe.MIME(mi, name), StorageKey: key, State: models.AssetStaging, Media: *mi, Variants: map[string]models.Variant{}, Tags: tags, Via: via,
			Analysis: map[string]string{}, Source: o.Source, CreatedAt: now, CreatedBy: actor, UpdatedAt: now}
		if fpErr == nil {
			as.Fingerprint = fp
		}
		if sampleErr == nil {
			as.Sample = sample
		}
		if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
			if !store.IsDuplicateKey(err) {
				return fail(err)
			}
			// Another machine is ingesting the same bytes right now.
			if err := a.DB.C(store.CAssets).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "sha256": sha}).Decode(&as); err != nil {
				return fail(err)
			}
			res.Status = "resumed"
		} else {
			res.Status = "ingested"
		}
	}
	res.AssetID, res.Title = as.ID, as.DisplayTitle()
	// Find or create the ingest session holding resume state.
	var sess models.IngestSession
	err = a.DB.C(store.CIngest).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": as.ID, "state": bson.M{"$in": bson.A{"uploading", "verifying", "failed"}}}).Decode(&sess)
	if err != nil {
		sess = models.IngestSession{ID: ids.New(ids.Ingest), WorkspaceID: a.WorkspaceID(), InstallationID: a.Cfg.Installation.ID, ProjectID: o.ProjectID, SourceName: name,
			Size: res.Size, SHA256: sha, AssetID: as.ID, State: "uploading", Upload: models.UploadState{Key: key}, Via: via, JobID: o.JobID, CreatedAt: now, UpdatedAt: now}
		if err := a.DB.Insert(ctx, store.CIngest, sess); err != nil {
			return fail(err)
		}
	}
	res.SessionID = sess.ID
	setSession := func(set bson.M) {
		set["updatedAt"] = time.Now().UTC()
		_ = a.DB.Update(context.WithoutCancel(ctx), store.CIngest, a.WorkspaceID(), sess.ID, bson.M{"$set": set})
	}
	setSession(bson.M{"state": "uploading", "installationId": a.Cfg.Installation.ID, "error": ""})
	resume := media.Resume{UploadID: sess.Upload.UploadID, PartSize: sess.Upload.PartSize, PartsDone: sess.Upload.PartsDone, SessionURI: sess.Upload.SessionURI, Offset: sess.Upload.Offset}
	var lastSave time.Time
	info, err := a.Media.PutFile(ctx, key, path, media.PutOptions{ContentType: as.MIME, SHA256: sha}, resume, func(done, total int64, r media.Resume) {
		report("upload", done, total)
		if time.Since(lastSave) > 2*time.Second || done == total || r.UploadID != resume.UploadID || r.SessionURI != resume.SessionURI {
			lastSave = time.Now()
			resume = r
			setSession(bson.M{"bytesDone": done, "upload": models.UploadState{Key: key, UploadID: r.UploadID, PartSize: r.PartSize, PartsDone: r.PartsDone, SessionURI: r.SessionURI, Offset: r.Offset}})
		}
	})
	if err != nil {
		state := "uploading" // keep resumable
		if !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
			state = "failed"
		}
		setSession(bson.M{"state": state, "error": err.Error()})
		_ = a.DB.Update(context.WithoutCancel(ctx), store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"stateReason": "upload interrupted: " + err.Error(), "updatedAt": time.Now().UTC()}})
		// The exact error stays on the ingest session; people read what to do.
		res.Status, res.Error = "failed", "The upload stopped before it finished. Add the file again and it picks up where it stopped."
		return res
	}
	if info.Size != res.Size {
		setSession(bson.M{"state": "failed", "error": "size mismatch after upload"})
		return fail(apperr.New(apperr.Integrity, "The stored copy didn't match the file, so it wasn't saved. Add the file again."))
	}
	setSession(bson.M{"state": "verifying", "bytesDone": res.Size})
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"state": models.AssetVerified, "updatedAt": time.Now().UTC()}})
	if _, err := a.Cache.Adopt(path, sha); err != nil {
		report("cache", 0, 0) // non-fatal: the canonical copy is safe
	}
	set := bson.M{"state": models.AssetAvailable, "stateReason": "", "updatedAt": time.Now().UTC()}
	if as.Source == nil && o.Source != nil { // an interrupted earlier ingest of these bytes
		set["source"] = o.Source
		as.Source = o.Source
	}
	upd := bson.M{"$set": set}
	if o.ProjectID != "" {
		upd["$addToSet"] = bson.M{"projectIds": o.ProjectID}
	}
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, upd); err != nil {
		return fail(err)
	}
	setSession(bson.M{"state": "available"})
	as.State = models.AssetAvailable
	res.JobIDs = ops.EnqueueDerivatives(ctx, a, &as, o.Transcribe)
	if o.ProjectID != "" {
		refreshCounts(ctx, a, o.ProjectID)
	}
	a.Audit(ctx, "asset.ingest", "asset", as.ID, o.ProjectID, map[string]any{"name": name, "sha256": sha, "size": res.Size, "via": via, "status": res.Status})
	// MetaRush's first look starts: a link's title, the file's own tags and
	// the day it was recorded may already name it (D150).
	ops.SourceFactsChanged(ctx, a, as.ID, ops.FactsIngested)
	if t := currentTitle(ctx, a, as.ID); t != "" {
		res.Title = t
	}
	report("done", res.Size, res.Size)
	return res
}

// currentTitle is an asset's title as stored now ("" when it can't be read).
func currentTitle(ctx context.Context, a *app.App, id string) string {
	var cur models.Asset
	if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &cur) != nil {
		return ""
	}
	return cur.DisplayTitle()
}

// resourceWords names a kind of resource file for people: "a picture", "an SVG".
func resourceWords(kind string) string {
	switch kind {
	case models.ResourceSVG:
		return "an SVG"
	case models.ResourcePDF:
		return "a PDF"
	case models.ResourceFont:
		return "a font"
	}
	return "a picture"
}

// asResource adds a picture, SVG, PDF or font to the project's resources (or
// the workspace library when there is no project).
func asResource(ctx context.Context, a *app.App, path, name string, o Options, res Result) Result {
	res.Kind = "resource"
	via := o.Via
	if via == "" {
		via = "cli"
	}
	r, err := resources.AddFile(ctx, a, path, resources.AddOptions{ProjectID: o.ProjectID, FileName: name, Via: via, Tags: o.Tags})
	if err != nil {
		res.Status, res.Error = "failed", strings.ReplaceAll(FailText(err), path, name)
		return res
	}
	res.SHA256 = r.SHA256
	if r.Resource != nil {
		res.Title = r.Resource.Title
		res.Resource = &ResourceResult{ID: r.Resource.ID, Name: r.Resource.Name, Title: r.Resource.Title, Kind: r.Resource.Kind, Scope: r.Resource.Scope, Status: r.Status, Message: r.Message}
	}
	switch r.Status {
	case resources.StatusAdded, resources.StatusVersion:
		res.Status = "ingested"
	case resources.StatusSame:
		res.Status = "deduped"
	default:
		res.Status, res.Error = "skipped", r.Message
	}
	if o.Progress != nil {
		o.Progress(path, "done", res.Size, res.Size)
	}
	return res
}

// FailText says why a file couldn't be added, in plain words (the upload
// tray, the watched folder, the Add box, linked folders). MetaRush's own
// errors keep their message; an unexpected one keeps its text, since it is
// the only record of what went wrong.
func FailText(err error) string {
	var nm *probe.ErrNotMedia
	var ae *apperr.Error
	var missing *tools.ErrMissing
	switch {
	case err == nil:
		return ""
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "It stopped before it finished. Add it again and it picks up where it stopped."
	case errors.Is(err, fs.ErrNotExist):
		return "MetaRush can't find this file."
	case errors.Is(err, fs.ErrPermission):
		return "MetaRush isn't allowed to read this file."
	case errors.As(err, &nm):
		if nm.Reason == "zero duration" {
			return "It has no length, so there's nothing to add."
		}
		return "It isn't a video or sound file MetaRush can read."
	case errors.As(err, &ae) && ae.Code != apperr.Internal:
		return ae.Message
	case errors.As(err, &missing):
		// A program a component brings (FFmpeg): its plain words and where to add it (D326).
		return missing.Error()
	}
	return "Something went wrong while adding it: " + err.Error()
}

func refreshCounts(ctx context.Context, a *app.App, projectID string) {
	n, err := a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectIds": projectID})
	if err == nil {
		_ = a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), projectID, bson.M{"$set": bson.M{"assetCount": n, "updatedAt": time.Now().UTC()}})
	}
}

func hashWithProgress(path string, size int64, progress func(int64)) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 4<<20)
	var done int64
	last := time.Now()
	for {
		n, err := f.Read(buf)
		if n > 0 {
			h.Write(buf[:n])
			done += int64(n)
			if time.Since(last) > 500*time.Millisecond {
				progress(done)
				last = time.Now()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	progress(size)
	return hex.EncodeToString(h.Sum(nil)), nil
}

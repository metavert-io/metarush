package linkimport

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// Progress stages of an import job (models.Progress.Stage).
const (
	StageLookup   = "lookup"   // Looking up the video
	StageDownload = "download" // Downloading (percent and rate)
	StageAdding   = "adding"   // Adding to MetaRush (the ingest pipeline: hash, store)
)

// StageLabels are the stages as people read them.
var StageLabels = map[string]string{StageLookup: "Looking up the video", StageDownload: "Downloading", StageAdding: "Adding to MetaRush"}

// Report hands a progress report to the worker (sent with its heartbeat).
type Report func(p models.Progress)

// Payload is an import job's input (models.Job.Payload).
type Payload struct {
	URL      string `json:"url" bson:"url"`
	Input    string `json:"input,omitempty" bson:"input,omitempty"`
	Platform string `json:"platform" bson:"platform"`
	Kind     string `json:"kind" bson:"kind"`
	VideoID  string `json:"videoId,omitempty" bson:"videoId,omitempty"`
	ListID   string `json:"listId,omitempty" bson:"listId,omitempty"`
	// Browser is the browser whose sign-in this import may use (opt-in, per import).
	Browser string `json:"browser,omitempty" bson:"browser,omitempty"`
	Via     string `json:"via,omitempty" bson:"via,omitempty"`
	// Limit is how many videos of a playlist or channel to import (0: none).
	Limit  int    `json:"limit,omitempty" bson:"limit,omitempty"`
	Parent string `json:"parent,omitempty" bson:"parent,omitempty" jsonschema:"description=The playlist import this video belongs to."`
	// Title and Who are filled in after the lookup (activity, job details).
	Title string `json:"title,omitempty" bson:"title,omitempty"`
	Who   string `json:"who,omitempty" bson:"who,omitempty"`
}

// payloadOf reads an import job's payload (BSON round trip: nested documents
// decode as bson.D from the database and as maps in memory).
func payloadOf(j *models.Job) Payload {
	var p Payload
	if b, err := bson.Marshal(j.Payload); err == nil {
		_ = bson.Unmarshal(b, &p)
	}
	return p
}

// AdviceOf is the advice a failed import recorded (nil when none).
func AdviceOf(j *models.Job) *Advice {
	var p struct {
		Advice *Advice `bson:"advice"`
		Detail string  `bson:"detail"`
	}
	if b, err := bson.Marshal(j.Payload); err == nil && bson.Unmarshal(b, &p) == nil {
		return p.Advice
	}
	return nil
}

// PayloadOf exposes an import job's payload (activity, job details, the CLI).
func PayloadOf(j *models.Job) Payload { return payloadOf(j) }

func (p Payload) asMap() map[string]any {
	b, _ := bson.Marshal(p)
	m := map[string]any{}
	_ = bson.Unmarshal(b, &m)
	return m
}

// Failure is an import that could not be done, with advice.
type Failure struct {
	Advice Advice
	Detail string // technical detail (yt-dlp's error lines)
}

func (f *Failure) Error() string {
	if f.Detail != "" {
		return f.Advice.Message + " " + f.Advice.Next + " (" + firstLine(f.Detail, 400) + ")"
	}
	return f.Advice.Message + " " + f.Advice.Next
}

// Retryable reports whether the worker should try again by itself after a
// backoff: network trouble and unexplained failures. Everything else waits for
// the person (Retry, or the browser sign-in), so a bot check or a rate limit
// is never hammered; DRM, private and removed videos are never retried.
func Retryable(err error) bool {
	var f *Failure
	if errors.As(err, &f) {
		return f.Advice.Code == AdviceNetwork || f.Advice.Code == AdviceFailed || f.Advice.Code == AdviceNoFormats
	}
	return !errors.Is(err, context.Canceled)
}

// Dir is where an import job downloads (kept between attempts so a retry
// continues a partial download; removed once the clip is added).
func Dir(a *app.App, jobID string) string { return filepath.Join(a.Cfg.Scratch.Dir, "imports", jobID) }

// Run performs one import job: look up the video, download it, add it through
// the ingest pipeline with its provenance. A playlist or channel job looks up
// the first videos and queues one import per video instead.
func Run(ctx context.Context, a *app.App, j *models.Job, report Report) (map[string]any, error) {
	p := payloadOf(j)
	if p.URL == "" {
		return nil, &Failure{Advice: AdviceFor(AdviceUnsupported, p.Platform, "")}
	}
	report(models.Progress{Stage: StageLookup, Message: StageLabels[StageLookup]})
	if !a.Tools.Has(tools.YtDlp) {
		return nil, fail(ctx, a, j, &Failure{Advice: AdviceFor(AdviceMissing, p.Platform, ""), Detail: "yt-dlp is not installed or not on PATH"})
	}
	// The person who asked is the one adding the clip (notifications, job details).
	ia := *a
	ia.Actor = j.CreatedBy
	ia.Actor.Via = p.Via
	if ia.Actor.Via == "" {
		ia.Actor.Via = "cli"
	}
	if ia.Actor.UserID == "" {
		ia.Actor = a.Actor
	}
	r := &runner{a: a, browser: p.Browser}
	if p.Kind == KindPlaylist || p.Kind == KindChannel {
		return expand(ctx, &ia, j, p, r)
	}
	if as := findImported(ctx, a, p.Platform, p.VideoID); as != nil {
		return already(ctx, &ia, j, p, as)
	}
	dir := Dir(a, j.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	info, raw, err := r.lookupRaw(ctx, r.lookupArgs(p.URL), p.Platform)
	if err != nil {
		return nil, fail(ctx, a, j, asFailure(err))
	}
	entries := []*Info{info}
	raws := [][]byte{raw}
	if info.Type == "playlist" || len(info.Entries) > 0 {
		// One post with several videos (an X post): each becomes its own clip.
		entries, raws = entries[:0], raws[:0]
		re := rawEntries(raw)
		for i := range info.Entries {
			if len(entries) == maxPostVideos || i >= len(re) {
				break
			}
			e := info.Entries[i]
			entries = append(entries, &e)
			raws = append(raws, re[i])
		}
		if len(entries) == 0 {
			return nil, fail(ctx, a, j, &Failure{Advice: AdviceFor(AdviceNoVideo, p.Platform, p.Browser)})
		}
	}
	version := a.Tools.Version(ctx, tools.YtDlp)
	var out []map[string]any
	first := ""
	for i, e := range entries {
		if e.Live() {
			return nil, fail(ctx, a, j, &Failure{Advice: AdviceFor(AdviceLive, p.Platform, p.Browser)})
		}
		if e.DRM() {
			return nil, fail(ctx, a, j, &Failure{Advice: AdviceFor(AdviceDRM, p.Platform, p.Browser), Detail: "yt-dlp marked the video as DRM protected"})
		}
		src := Provenance(p.Platform, e, "yt-dlp", version, p.Browser != "", time.Now())
		if i == 0 {
			p.Title, p.Who = src.Title, src.Who()
			setPayload(ctx, a, j, bson.M{"payload.title": p.Title, "payload.who": p.Who})
		}
		if as := findImported(ctx, a, p.Platform, src.VideoID); as != nil {
			res, err := already(ctx, &ia, j, p, as)
			if err != nil {
				return nil, err
			}
			out = append(out, res)
			if first == "" {
				first = as.ID
			}
			continue
		}
		label := StageLabels[StageDownload]
		if len(entries) > 1 {
			label = fmt.Sprintf("Downloading video %d of %d", i+1, len(entries))
		}
		total := e.ExpectedSize()
		report(models.Progress{Stage: StageDownload, Message: label, Total: total})
		// yt-dlp's own metadata for this video: the download needs all of it (formats, addresses).
		infoFile := filepath.Join(dir, fmt.Sprintf("info-%d.json", i))
		if err := os.WriteFile(infoFile, raws[i], 0o600); err != nil {
			return nil, err
		}
		pathFile := filepath.Join(dir, fmt.Sprintf("path-%d.txt", i))
		onProgress := func(done, total int64, rate float64, finishing bool) {
			pct := 0.0
			if total > 0 {
				pct = float64(done) / float64(total)
			}
			msg := label
			if finishing {
				msg, rate = "Finishing the download", 0
			}
			report(models.Progress{Stage: StageDownload, Message: msg, Pct: (float64(i) + min(pct, 1)) / float64(len(entries)), Done: done, Total: total, Rate: rate})
		}
		path, err := r.download(ctx, r.downloadArgs(infoFile, dir, pathFile), p.Platform, e, pathFile, onProgress)
		var re *RunError
		if err != nil && errors.As(err, &re) && (re.Advice.Code == AdviceForbidden || re.Advice.Code == AdviceFailed || re.Advice.Code == AdviceNoFormats) {
			// Saved addresses expire (YouTube's last about six hours): look it up again.
			item := 0
			if len(entries) > 1 {
				item = i + 1
			}
			path, err = r.download(ctx, r.downloadURLArgs(p.URL, dir, pathFile, item), p.Platform, e, pathFile, onProgress)
		}
		if err != nil {
			return nil, fail(ctx, a, j, asFailure(err))
		}
		report(models.Progress{Stage: StageAdding, Message: StageLabels[StageAdding], Pct: 0})
		ext := strings.TrimPrefix(filepath.Ext(path), ".")
		clipTitle := src.Title // the source keeps the original title; several clips from one post are told apart
		if len(entries) > 1 {
			clipTitle = fmt.Sprintf("%s (video %d of %d)", src.Title, i+1, len(entries))
		}
		res := ingest.File(ctx, &ia, path, ingest.Options{ProjectID: j.ProjectID, DisplayName: FileName(clipTitle, ext), Title: clipTitle, Source: &src, JobID: j.ID,
			Via: ia.Actor.Via, Transcribe: true, Duplicates: ingest.DuplicatesSkip, Progress: func(_, phase string, done, total int64) {
				pct := 0.0
				if total > 0 {
					pct = float64(done) / float64(total)
				}
				switch phase {
				case "hash":
					pct *= 0.3
				case "upload":
					pct = 0.3 + 0.7*pct
				case "done":
					pct = 1
				default:
					pct = 0
				}
				report(models.Progress{Stage: StageAdding, Message: StageLabels[StageAdding], Pct: pct, Done: done, Total: total})
			}})
		if res.Status == "failed" || res.Status == "skipped" {
			msg := res.Error
			if msg == "" {
				msg = "the downloaded file isn't a video MetaRush can read"
			}
			return nil, fail(ctx, a, j, &Failure{Advice: Advice{Code: AdviceFailed, Message: "The video was downloaded, but adding it to MetaRush didn't work.", Next: "Retry.", Retry: true}, Detail: msg})
		}
		if res.Duplicate != nil && res.Duplicate.AssetID != "" && res.AssetID == "" {
			res.AssetID = res.Duplicate.AssetID
		}
		// "duplicate": the same bytes are already a clip under another name; it
		// now carries this source too (ingest.File set it when missing).
		if res.Status == "duplicate" && j.ProjectID != "" {
			linkProject(ctx, &ia, res.AssetID, j.ProjectID)
		}
		_ = os.Remove(path) // the canonical store and the cache hold the bytes now
		row := map[string]any{"assetId": res.AssetID, "status": res.Status, "title": clipTitle, "videoId": src.VideoID, "url": src.URL}
		if res.Duplicate != nil {
			row["duplicateOf"] = res.Duplicate.Title
		}
		out = append(out, row)
		if first == "" {
			first = res.AssetID
		}
	}
	_ = os.RemoveAll(dir)
	setPayload(ctx, a, j, bson.M{"assetId": first})
	result := map[string]any{"assetId": first, "assets": out, "title": p.Title, "who": p.Who}
	if len(out) > 0 {
		result["status"] = out[0]["status"]
	}
	return result, nil
}

// asFailure turns a run error into a Failure with advice.
func asFailure(err error) *Failure {
	var f *Failure
	if errors.As(err, &f) {
		return f
	}
	var re *RunError
	if errors.As(err, &re) {
		return &Failure{Advice: re.Advice, Detail: re.Output}
	}
	if errors.Is(err, context.Canceled) {
		return &Failure{Advice: Advice{Code: "canceled", Message: "The import was stopped.", Next: "Paste the link again to start over."}}
	}
	return &Failure{Advice: AdviceFor(AdviceFailed, "", ""), Detail: err.Error()}
}

// fail records the advice on the job (activity, job details, the CLI and the
// notification read it there) and returns the failure.
func fail(ctx context.Context, a *app.App, j *models.Job, f *Failure) error {
	setPayload(ctx, a, j, bson.M{"payload.advice": f.Advice, "payload.detail": firstLine(f.Detail, 600)})
	return f
}

func setPayload(ctx context.Context, a *app.App, j *models.Job, set bson.M) {
	set["updatedAt"] = time.Now().UTC()
	_ = a.DB.Update(context.WithoutCancel(ctx), store.CJobs, a.WorkspaceID(), j.ID, bson.M{"$set": set})
}

// findImported is the clip already imported from this video, if any.
func findImported(ctx context.Context, a *app.App, platform, videoID string) *models.Asset {
	if platform == "" || videoID == "" {
		return nil
	}
	var as models.Asset
	err := a.DB.C(store.CAssets).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "source.platform": platform, "source.videoId": videoID, "state": models.AssetAvailable}).Decode(&as)
	if err != nil {
		return nil
	}
	return &as
}

// already reports a video that is already a clip: it is linked to the project
// (when one was given) and nothing is downloaded.
func already(ctx context.Context, a *app.App, j *models.Job, p Payload, as *models.Asset) (map[string]any, error) {
	if j.ProjectID != "" {
		linkProject(ctx, a, as.ID, j.ProjectID)
	}
	title := as.DisplayTitle()
	if p.Title == "" {
		setPayload(ctx, a, j, bson.M{"payload.title": title, "payload.who": as.Source.Who(), "assetId": as.ID})
	} else {
		setPayload(ctx, a, j, bson.M{"assetId": as.ID})
	}
	return map[string]any{"assetId": as.ID, "status": "already", "title": title, "who": as.Source.Who(),
		"assets": []map[string]any{{"assetId": as.ID, "status": "already", "title": title}}}, nil
}

func linkProject(ctx context.Context, a *app.App, assetID, projectID string) {
	if assetID == "" || projectID == "" {
		return
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), assetID, bson.M{"$addToSet": bson.M{"projectIds": projectID}, "$set": bson.M{"updatedAt": time.Now().UTC()}})
	n, err := a.DB.C(store.CAssets).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectIds": projectID})
	if err == nil {
		_ = a.DB.Update(ctx, store.CProjects, a.WorkspaceID(), projectID, bson.M{"$set": bson.M{"assetCount": n, "updatedAt": time.Now().UTC()}})
	}
}

// expand looks up the first videos of a playlist or channel and queues one
// import for each (on this machine, with the same sign-in choice).
func expand(ctx context.Context, a *app.App, j *models.Job, p Payload, r *runner) (map[string]any, error) {
	limit := p.Limit
	if limit <= 0 {
		return nil, fail(ctx, a, j, &Failure{Advice: Advice{Code: "playlist", Message: "This link is a playlist or a channel, so nothing was imported.", Next: "Paste it again and choose how many of its videos to import."}})
	}
	info, err := r.lookup(ctx, r.listArgs(p.URL, limit), p.Platform)
	if err != nil {
		return nil, fail(ctx, a, j, asFailure(err))
	}
	setPayload(ctx, a, j, bson.M{"payload.title": firstNonEmpty(info.Title, "Playlist"), "payload.who": firstNonEmpty(info.Uploader, info.Channel)})
	var queued []string
	for i, e := range info.Entries {
		if i == limit {
			break
		}
		link := firstNonEmpty(e.URL, e.WebpageURL)
		if link == "" && p.Platform == models.PlatformYouTube && ytID.MatchString(e.ID) {
			link = "https://www.youtube.com/watch?v=" + e.ID
		}
		l, err := Classify(link)
		if err != nil || l.Many() {
			continue
		}
		child := Payload{URL: l.URL, Input: link, Platform: l.Platform, Kind: l.Kind, VideoID: firstNonEmpty(l.VideoID, e.ID), Browser: p.Browser, Via: p.Via, Parent: j.ID, Title: e.Title}
		cj, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeImport, Payload: child.asMap(), IdempotencyKey: IdempotencyKey(a.Cfg.Installation.ID, l, j.ProjectID),
			Priority: 8, MaxAttempts: 3, ProjectID: j.ProjectID, Actor: a.Actor, Installation: j.Installation})
		if err != nil {
			return nil, err
		}
		queued = append(queued, cj.ID)
	}
	if len(queued) == 0 {
		return nil, fail(ctx, a, j, &Failure{Advice: AdviceFor(AdviceNoVideo, p.Platform, p.Browser)})
	}
	return map[string]any{"queued": len(queued), "jobIds": queued, "title": info.Title, "status": "expanded"}, nil
}

// IdempotencyKey identifies one import of one video into one place on one
// machine: pasting the same link again joins the import already running.
func IdempotencyKey(installationID string, l Link, projectID string) string {
	id := l.VideoID
	if id == "" || l.Kind == KindPost {
		id = l.URL
	}
	if l.Many() {
		id = "list:" + l.URL
	}
	return "import:" + installationID + ":" + l.Platform + ":" + id + ":" + projectID
}

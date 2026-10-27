package remotion

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/store"
)

// A composition becomes a clip by rendering it (D135): in the jail, with the
// source's own packages and MetaRush's encoding, then ingested as footage
// with provenance. Renders are cached per (source, version, composition,
// props, frames, render settings, word settings, pictures): the same key never
// renders twice.

// Limits.
const (
	// MaxSeconds is the longest composition MetaRush renders (15 minutes).
	MaxSeconds = 15 * 60
	// renderBase and renderPerSecond give the time limit: 10 minutes plus 6×
	// the composition's length.
	renderBase      = 600
	renderPerSecond = 6
)

// renderSettings names how renders are made (part of the key): the
// composition's own sound mixed in, or (sound in layers, D170) none.
func renderSettings(v *models.RemotionVersion, sound string) string {
	f, q := renderFormat(v)
	audio := "mixed"
	if sound == models.RemotionSoundLayers {
		audio = "none"
	}
	return fmt.Sprintf("remotion-render/1;h264+aac;crf=18;frames=%s;q=%d;audio=%s", f, q, audio)
}

// variant is what else makes a render of a composition: its sound (mixed or
// in layers, D170), the word settings its composition uses (D169) and the
// pictures it draws in place of the project's own (D209).
type variant struct {
	Sound       string
	Words       string // canonical JSON of the settings that differ from the code
	WordsNumber int
	Pictures    string // canonical JSON of name → served picture ("<sha256>.<ext>")
}

// canonicalProps reads props: a JSON object, written back with sorted keys
// ("" when there are none).
func canonicalProps(raw json.RawMessage) (string, map[string]any, error) {
	t := bytes.TrimSpace(raw)
	if len(t) == 0 || string(t) == "null" || string(t) == "{}" {
		return "", nil, nil
	}
	// The same canonical form as a cut sheet's `with …` (sorted keys, numbers as
	// JSON writes them), so a sheet line finds the clip a command made.
	var m map[string]any
	if err := json.Unmarshal(t, &m); err != nil || m == nil {
		return "", nil, apperr.New(apperr.InvalidInput, "A composition's props are named values (for example a title in quotes).")
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", nil, err
	}
	if len(b) > 64<<10 {
		return "", nil, apperr.New(apperr.InvalidInput, "Those props are too long (more than 64 KB).")
	}
	var plain map[string]any
	_ = json.Unmarshal(b, &plain)
	return string(b), plain, nil
}

// renderKey is the cache key of one render. A render with the composition's
// own sound, the code's own words and the project's own pictures keeps the key
// it always had.
func renderKey(s *models.RemotionSource, v *models.RemotionVersion, comp, props string, frames []int, vr variant) string {
	fr := ""
	if len(frames) == 2 {
		fr = fmt.Sprintf("%d-%d", frames[0], frames[1])
	}
	parts := []string{"remotion-render-key/1", s.ID, v.Hash, comp, props, fr, renderSettings(v, vr.Sound)}
	if vr.Words != "" {
		parts = append(parts, "words="+vr.Words)
	}
	if vr.Pictures != "" {
		parts = append(parts, "pictures="+vr.Pictures)
	}
	return sha256Hex([]byte(strings.Join(parts, "\n")))
}

// renderRequest is what makes a render.
type renderRequest struct {
	Source      *models.RemotionSource
	Version     *models.RemotionVersion
	Composition string
	Props       string
	Frames      []int
	AddTo       *models.RemotionAddTo
	// Sound "layers": the picture alone, with its sounds for the cut (D170).
	Sound string
	// Words: the word settings to render with (D169), and Pictures the
	// pictures in place of the project's own (D209); set by requestRender
	// from the source's settings (and the clip's own pictures, OwnPictures)
	// unless KeepSettings (the caller worked them out).
	Words          string
	WordsNumber    int
	Pictures       string
	PictureValues  []models.RemotionPictureValue
	PicturesNumber int
	OwnPictures    []models.RemotionPictureValue
	KeepSettings   bool
}

// requireRenderable refuses a version or composition that can't be rendered
// into a cut (with the plain reason and what to do).
func requireRenderable(s *models.RemotionSource, v *models.RemotionVersion, comp string) (*models.RemotionComposition, error) {
	if v.State != models.RemotionApproved {
		why := "no person has approved it yet"
		switch v.State {
		case models.RemotionRevoked:
			why = "its approval was withdrawn"
		case models.RemotionRejected:
			why = "a person turned it down"
		}
		return nil, apperr.New(apperr.Validation, "Version %d of “%s” can't be made into clips: %s.", v.Number, s.Title, why).
			WithHint("A person can look at it and approve it under Sources in the dashboard.").
			WithDetails(map[string]any{"reason": "remotion_not_approved", "version": v.Hash, "state": v.State}).
			WithCommand("metarush remotion approve " + s.Name + "@" + v.Short())
	}
	c := v.Composition(comp)
	if c == nil {
		var ids []string
		for _, x := range v.Compositions {
			ids = append(ids, x.ID)
		}
		sort.Strings(ids)
		return nil, apperr.New(apperr.NotFound, "Version %d of “%s” has no composition called “%s”.", v.Number, s.Title, comp).
			WithHint("It has: " + strings.Join(ids, ", ") + ".").WithCommand("metarush remotion show " + s.Name)
	}
	if c.Still || c.DurationInFrames <= 1 {
		return nil, apperr.New(apperr.InvalidInput, "“%s” is a still picture, not a video.", comp).WithHint("Draw it with stills instead.").WithCommand("metarush remotion stills " + s.Name + " " + comp)
	}
	if c.Duration > MaxSeconds {
		return nil, apperr.New(apperr.InvalidInput, "“%s” is %s long; MetaRush makes clips of compositions up to 15 minutes.", comp, clock(c.Duration)).WithHint("Render a part of it (a frame range).")
	}
	return c, nil
}

// requestRender returns the cached render for a request, or queues it.
func requestRender(ctx context.Context, a *app.App, rq renderRequest) (*models.RemotionRender, error) {
	c, err := requireRenderable(rq.Source, rq.Version, rq.Composition)
	if err != nil {
		return nil, err
	}
	if len(rq.Frames) == 2 {
		if rq.Frames[0] < 0 || rq.Frames[1] < rq.Frames[0] || rq.Frames[1] >= c.DurationInFrames {
			return nil, apperr.New(apperr.InvalidInput, "“%s” has frames 0 to %d; that range is outside it.", c.ID, c.DurationInFrames-1)
		}
		if rq.Frames[0] == 0 && rq.Frames[1] == c.DurationInFrames-1 {
			rq.Frames = nil
		}
	} else {
		rq.Frames = nil
	}
	if !rq.KeepSettings {
		rq.Words, rq.WordsNumber = wordsFor(rq.Source, rq.Version, c.ID)
		rq.Pictures, rq.PictureValues, rq.PicturesNumber = picturesFor(rq.Source, rq.Version, c.ID, rq.OwnPictures)
	}
	vr := variant{Sound: rq.Sound, Words: rq.Words, WordsNumber: rq.WordsNumber, Pictures: rq.Pictures}
	key := renderKey(rq.Source, rq.Version, c.ID, rq.Props, rq.Frames, vr)
	now := time.Now().UTC()
	id := docID(a.WorkspaceID(), key)
	r, err := getRender(ctx, a, key)
	if err == nil && r.State == models.RemotionRenderDone && r.AssetID != "" {
		var as models.Asset
		if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), r.AssetID, &as) == nil && as.State == models.AssetAvailable {
			if rq.AddTo != nil {
				if err := settleAddTo(ctx, a, rq.Source, r, []models.RemotionAddTo{*rq.AddTo}); err != nil {
					return r, err
				}
			}
			return r, nil
		}
	}
	if err == nil && (r.State == models.RemotionRenderQueued || r.State == models.RemotionRenderRendering) {
		if rq.AddTo != nil {
			_, _ = a.DB.C(store.CRemotionRenders).UpdateOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()}, bson.M{"$push": bson.M{"addTo": rq.AddTo}})
		}
		if j, jerr := a.Queue.Get(ctx, r.JobID); jerr == nil && (j.State == models.JobQueued || j.State == models.JobRunning) {
			return r, nil
		}
	}
	// New (or failed, or its job is gone): queue it.
	var add []models.RemotionAddTo
	if r != nil {
		add = r.AddTo
	}
	if rq.AddTo != nil {
		add = append(add, *rq.AddTo)
	}
	frames := rq.Frames
	rec := models.RemotionRender{ID: id, WorkspaceID: a.WorkspaceID(), Key: key, SourceID: rq.Source.ID, ProjectID: rq.Source.ProjectID, Version: rq.Version.Hash,
		VersionNumber: rq.Version.Number, Composition: c.ID, Props: rq.Props, Frames: frames, Settings: renderSettings(rq.Version, rq.Sound), State: models.RemotionRenderQueued,
		Words: rq.Words, WordsNumber: rq.WordsNumber, Pictures: rq.Pictures, PictureValues: rq.PictureValues, PicturesNumber: rq.PicturesNumber, OwnPictures: ownDrawn(rq.Version, c.ID, rq.OwnPictures),
		Sound: rq.Sound, AddTo: add, CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
	if _, err := a.DB.C(store.CRemotionRenders).ReplaceOne(ctx, bson.M{"_id": id, "workspaceId": a.WorkspaceID()}, rec, options.Replace().SetUpsert(true)); err != nil {
		return nil, err
	}
	idem := "remotion-render:" + key
	if r != nil {
		idem += ":retry:" + now.Format("20060102T150405.000")
	}
	seconds := c.Duration
	if len(frames) == 2 {
		seconds = float64(frames[1]-frames[0]+1) / c.FPS
	}
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeRemotionRender, Payload: map[string]any{"key": key, "seconds": seconds, "composition": c.ID, "source": rq.Source.Name}, IdempotencyKey: idem,
		Priority: 15, Heavy: true, MaxAttempts: 2, ProjectID: rq.Source.ProjectID, Actor: a.Actor})
	if err != nil {
		return nil, err
	}
	rec.JobID = j.ID
	if err := setRender(ctx, a, key, bson.M{"jobId": j.ID}); err != nil {
		return nil, err
	}
	return &rec, nil
}

// RunRender runs a remotion-render job.
func RunRender(ctx context.Context, a *app.App, j *models.Job, report func(pct float64, msg string)) (map[string]any, error) {
	if report == nil {
		report = func(float64, string) {}
	}
	key, _ := j.Payload["key"].(string)
	r, err := getRender(ctx, a, key)
	if err != nil {
		return nil, finalf("The clip this render was for is no longer asked for.")
	}
	ra := *a
	ra.Actor = r.CreatedBy
	a = &ra
	s, err := getSource(ctx, a, r.SourceID)
	if err != nil {
		return nil, &Final{Err: err}
	}
	v, err := getVersion(ctx, a, r.Version)
	if err != nil {
		return nil, &Final{Err: err}
	}
	c, err := requireRenderable(s, v, r.Composition)
	if err != nil {
		_ = setRender(ctx, a, key, bson.M{"state": models.RemotionRenderFailed, "error": apperrMessage(err)})
		return nil, &Final{Err: err}
	}
	if !v.PackagesAllowed {
		return nil, finalf("Version %d of “%s” uses packages nobody allowed.", v.Number, s.Title)
	}
	now := time.Now().UTC()
	_ = setRender(ctx, a, key, bson.M{"state": models.RemotionRenderRendering, "machine": a.Cfg.Installation.Name, "startedAt": now, "error": ""})
	fail := func(err error) (map[string]any, error) {
		_ = setRender(ctx, a, key, bson.M{"state": models.RemotionRenderFailed, "error": apperrMessage(err)})
		return nil, err
	}
	videoDir, release, err := rendererDir(a)
	if err != nil {
		return fail(err)
	}
	defer release()
	report(0.02, "getting the project's files")
	m, err := loadManifest(ctx, a, v)
	if err != nil {
		return fail(err)
	}
	pi, err := versionPackages(ctx, a, m)
	if err != nil {
		return fail(&Final{Err: err})
	}
	env, err := ensureEnv(ctx, a, pi, videoDir, func(msg string) { report(0.05, msg) })
	if err != nil {
		return fail(err)
	}
	work, done, err := workTree(ctx, a, v, m, env)
	if err != nil {
		return fail(err)
	}
	defer done()
	ff, err := fontFiles(ctx, a, v.Fonts)
	if err != nil {
		return fail(err)
	}
	pics, err := pictureFiles(ctx, a, r.PictureValues)
	if err != nil {
		return fail(err)
	}
	dir, err := tempDir(a, "remotion-render-")
	if err != nil {
		return fail(err)
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, r.Composition+".mp4")
	props := renderProps(r.Props, r.Words, r.PictureValues)
	seconds := c.Duration
	if len(r.Frames) == 2 {
		seconds = float64(r.Frames[1]-r.Frames[0]+1) / c.FPS
	}
	format, quality := renderFormat(v)
	report(0.08, "rendering "+r.Composition)
	layers := r.Sound == models.RemotionSoundLayers
	tasks := []runTask{{Type: "render", Composition: r.Composition, Props: props, Frames: r.Frames, Out: out, CRF: 18, MaxLength: MaxSeconds, Muted: layers}}
	if layers {
		// Where its sounds play, from Remotion's own record of every frame (D170).
		tasks = append(tasks, runTask{Type: "scenes", Composition: r.Composition, Props: props})
	}
	res, err := jailRun(ctx, a, videoDir, runSpec{Env: env, MaxSeconds: renderBase + int(seconds*renderPerSecond), FrameTimeoutMs: frameTimeout(v),
		Concurrency: max(1, runtime.NumCPU()/2), ImageFormat: format, JPEGQuality: quality,
		Bundles: []runBundle{{Name: "main", Work: work, Entry: v.Entry, Bundle: bundleDir(a, v), Fonts: ff, Pictures: pics}},
		Tasks:   tasks}, func(ev runEvent) {
		switch {
		case ev.Type == "progress" && ev.Stage == "bundling":
			report(0.1, "bundling the project's code")
		case ev.Type == "progress" && ev.Stage == "rendering":
			report(0.1+0.75*ev.Progress, fmt.Sprintf("rendering %s (%.0f%%)", r.Composition, ev.Progress*100))
		}
	})
	if err != nil {
		if res != nil && len(res.Blocked) > 0 {
			_ = setRender(ctx, a, key, bson.M{"blocked": res.Blocked})
		}
		rerr := fmt.Errorf("MetaRush couldn't render “%s”: %s", r.Composition, err.Error())
		if er, ok := err.(*errRun); ok && er.slow() {
			return fail(rerr) // a busy computer can be slow once: the job tries again
		}
		return fail(&Final{Err: rerr})
	}
	// The file is what was asked for: the size, rate and length the jailed run
	// worked out for these props (calculateMetadata can change them).
	width, height, fps := c.Width, c.Height, c.FPS
	if ev := res.Rendered; ev != nil && ev.Width > 0 && ev.Height > 0 && ev.FPS > 0 && ev.Duration > 0 {
		width, height, fps, seconds = ev.Width, ev.Height, ev.FPS, float64(ev.Duration)/ev.FPS
	}
	mi, err := probe.Probe(ctx, a.Tools, out)
	if err != nil {
		return fail(fmt.Errorf("the render can't be read: %w", err))
	}
	if !mi.HasVideo || mi.Width != width || mi.Height != height {
		return fail(&Final{Err: fmt.Errorf("the render is %d×%d, not %d×%d", mi.Width, mi.Height, width, height)})
	}
	if math.Abs(mi.Duration-seconds) > 2/fps+0.1 {
		return fail(&Final{Err: fmt.Errorf("the render is %.2f s long, not %.2f s", mi.Duration, seconds)})
	}
	report(0.9, "adding it to Sources")
	title := fmt.Sprintf("%s (%s v%d)", r.Composition, s.Title, v.Number)
	if r.Props != "" {
		title += " · own settings"
	}
	if r.Words != "" {
		title += fmt.Sprintf(" · words changed (%d)", r.WordsNumber)
	}
	switch {
	case len(r.OwnPictures) > 0:
		title += " · pictures of its own"
	case r.Pictures != "":
		title += fmt.Sprintf(" · pictures changed (%d)", r.PicturesNumber)
	}
	if layers {
		title += " · picture only"
	}
	if len(r.Frames) == 2 {
		title += fmt.Sprintf(" · frames %d–%d", r.Frames[0], r.Frames[1])
	}
	src := &models.AssetSource{Platform: models.PlatformRemotion, Method: models.SourceRemotionRender, Title: r.Composition, Duration: mi.Duration,
		DownloadedAt: time.Now().UTC(), Tool: "Remotion", ToolVersion: v.Remotion,
		Remotion: &models.RemotionClipSource{SourceID: s.ID, SourceName: s.Name, SourceTitle: s.Title, Version: v.Hash, VersionNumber: v.Number, Kind: models.SourceRemotionRender,
			Composition: r.Composition, Props: r.Props, Frames: r.Frames, RenderKey: key, Origin: s.Origin.Where(), Words: r.Words, WordsNumber: r.WordsNumber,
			Pictures: r.Pictures, PicturesNumber: r.PicturesNumber, OwnPictures: r.OwnPictures, Sound: r.Sound}}
	// A picture without its sound has nothing to transcribe (its voices come in as their own files).
	ing := ingest.File(ctx, a, out, ingest.Options{ProjectID: s.ProjectID, FootageOnly: true, Title: title, DisplayName: r.Composition + ".mp4", Source: src,
		Duplicates: ingest.DuplicatesKeep, Transcribe: !layers, JobID: j.ID, Via: a.Actor.Via})
	if ing.AssetID == "" || ing.Status == "failed" {
		return fail(fmt.Errorf("adding the render as a clip didn't work: %s", ing.Error))
	}
	fin := time.Now().UTC()
	set := bson.M{"state": models.RemotionRenderDone, "assetId": ing.AssetID, "sha256": ing.SHA256, "duration": mi.Duration, "width": mi.Width, "height": mi.Height,
		"fps": mi.FPS, "finishedAt": fin, "blocked": res.Blocked}
	if layers {
		set["sounds"] = placementsOf(res.Scenes[r.Composition], fps, r.Frames)
	}
	if err := setRender(ctx, a, key, set); err != nil {
		return nil, err
	}
	a.Audit(ctx, "remotion.render", "remotion", s.ID, s.ProjectID, map[string]any{"composition": r.Composition, "version": v.Hash, "assetId": ing.AssetID, "key": key})
	fresh, _ := getRender(ctx, a, key)
	if fresh != nil && len(fresh.AddTo) > 0 {
		report(0.97, "putting it in the cut")
		if err := settleAddTo(ctx, a, s, fresh, fresh.AddTo); err != nil {
			report(0.99, "it is in Sources; putting it in the cut didn't work: "+apperrMessage(err))
		}
	}
	msg := fmt.Sprintf("Made “%s” of “%s” (version %d) into a clip: %s.", r.Composition, s.Title, v.Number, clock(mi.Duration))
	report(1, msg)
	return map[string]any{"assetId": ing.AssetID, "key": key, "composition": r.Composition, "version": v.Hash, "duration": mi.Duration, "message": msg, "projectId": s.ProjectID}, nil
}

// apperrMessage is an error's plain message.
func apperrMessage(err error) string {
	if ae := apperr.From(err); ae != nil && ae.Code != apperr.Internal {
		return ae.Message
	}
	return firstLine(err.Error())
}

// clock writes seconds as m:ss.
func clock(s float64) string {
	t := int(math.Round(s))
	if t >= 3600 {
		return fmt.Sprintf("%d:%02d:%02d", t/3600, t/60%60, t%60)
	}
	return fmt.Sprintf("%d:%02d", t/60, t%60)
}

// ownDrawn keeps the clip's own pictures that are part of its render: names
// the version reads as settings and the composition draws.
func ownDrawn(v *models.RemotionVersion, comp string, own []models.RemotionPictureValue) []models.RemotionPictureValue {
	have := pictureSettings(v)
	c := v.Composition(comp)
	var out []models.RemotionPictureValue
	for _, o := range own {
		if have[o.Name] != nil && drawsPicture(c, o.Name) {
			out = append(out, o)
		}
	}
	return out
}

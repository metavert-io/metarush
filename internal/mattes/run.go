package mattes

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiservices/local"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/matting"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/videogen"
)

func init() {
	aiwork.RegisterKeeper(aiwork.MatteFeature, keepElsewhere, endedElsewhere)
}

// Final ends a cut-out's work for good: it is never tried again by itself.
type Final struct{ Err error }

func (f *Final) Error() string { return f.Err.Error() }
func (f *Final) Unwrap() error { return f.Err }

// Wait puts the work back for another computer: this one can't make it
// with the model it was asked of, and another one can.
type Wait struct {
	After time.Duration
	Why   string
}

func (w *Wait) Error() string { return w.Why }

// waitFirst is how long a cut-out waits for another computer the first
// time; each wait after is twice as long, at most waitMost.
const (
	waitFirst = 2 * time.Minute
	waitMost  = 30 * time.Minute
)

// Run makes a cut-out (the matte job, on any computer that can): the
// people told from their background by the model chosen, then the clip's
// picture kept with them, its background see-through, in the media store.
// A stop asked for ends it as stopped; this computer stopping leaves it
// waiting for the next one.
func Run(ctx context.Context, a *app.App, j *models.Job, report func(pct float64, msg string)) (res map[string]any, err error) {
	id, _ := j.Payload["matteId"].(string)
	m, err := Load(ctx, a, id)
	if err != nil {
		return nil, &Final{Err: err}
	}
	switch m.State {
	case models.MatteReady:
		return map[string]any{"matteId": m.ID, "reused": true}, nil
	case models.MatteStopped:
		return nil, &Final{Err: errors.New("the cut-out was stopped")}
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), m.AssetID, &as); err != nil {
		setState(context.WithoutCancel(ctx), a, m, models.MatteFailed, "the clip isn't in MetaRush any more.")
		return nil, &Final{Err: apperr.New(apperr.NotFound, "The clip isn't in MetaRush any more.")}
	}
	if as.IsReference() {
		setState(context.WithoutCancel(ctx), a, m, models.MatteFailed, "the clip is marked as someone else's video, which MetaRush never processes.")
		return nil, &Final{Err: apperr.New(apperr.Conflict, "“%s” is marked as someone else's video, which MetaRush never processes.", as.DisplayTitle())}
	}
	ref := m.Provider + "/" + m.Model
	here := aiservices.Env{Tools: a.Tools, Cfg: a.Cfg, Home: a.Cfg.Home(), App: a}
	if err := aiservices.ReadyHere(here, m.Provider, aiservices.KindVideoBackground, m.Model); err != nil {
		if madeElsewhere(ctx, a, ref) {
			after := min(waitMost, waitFirst<<min(jobs.Postponed(j), 4))
			return nil, &Wait{After: after, Why: fmt.Sprintf("Waiting for a computer of this workspace that makes cut-outs with %s; this one can't.", m.ModelTitle)}
		}
		ae := apperr.From(err)
		why := strings.TrimSuffix(ae.Message, ".") + "."
		setState(context.WithoutCancel(ctx), a, m, models.MatteFailed, why)
		return nil, &Final{Err: apperr.New(apperr.ToolMissing, "No computer of this workspace can make it with %s now: %s", m.ModelTitle, lowerFirst(why)).
			WithHint(ae.Hint).WithDetails(map[string]any{"component": matting.Component, "model": ref})}
	}
	defer func() {
		if err == nil {
			return
		}
		c := context.WithoutCancel(ctx)
		var fin *Final
		switch {
		case ctx.Err() != nil:
			// A person stopped it, or this computer is stopping (the job waits).
			if jj, e := a.Queue.Get(c, j.ID); e == nil && jj.CancelRequested {
				setState(c, a, m, models.MatteStopped, "")
			} else {
				setState(c, a, m, models.MatteWaiting, "")
			}
		case errors.As(err, &fin):
			setState(c, a, m, models.MatteFailed, plainWords(fin.Err))
		case len(j.Attempts) >= j.MaxAttempts:
			setState(c, a, m, models.MatteFailed, plainWords(err))
		default:
			setState(c, a, m, models.MatteWaiting, "")
		}
	}()
	if err := a.DB.Update(ctx, store.CMattes, a.WorkspaceID(), m.ID, bson.M{"$set": bson.M{"state": models.MatteMaking, "jobId": j.ID,
		"madeOn": a.Cfg.Installation.ID, "madeOnName": a.Cfg.Installation.Name}, "$unset": bson.M{"error": ""}, "$currentDate": bson.M{"updatedAt": true}}); err != nil {
		return nil, err
	}
	report(0.01, "Getting the clip")
	src, err := a.Cache.Ensure(ctx, a.Media, m.MediaKey, m.Media, m.MediaSize, nil)
	if err != nil {
		return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get “%s” from its storage.", as.DisplayTitle())
	}
	dir, err := os.MkdirTemp(a.Cfg.Scratch.Dir, "matte-")
	if err != nil {
		if err := os.MkdirAll(a.Cfg.Scratch.Dir, 0o755); err != nil {
			return nil, err
		}
		if dir, err = os.MkdirTemp(a.Cfg.Scratch.Dir, "matte-"); err != nil {
			return nil, err
		}
	}
	defer os.RemoveAll(dir)
	ad, err := aiservices.Get(m.Provider, aiservices.KindVideoBackground)
	if err != nil {
		return nil, &Final{Err: err}
	}
	env := here
	env.Scratch = dir
	env.Progress = func(done, total int) {
		report(0.02+0.78*float64(done)/float64(max(1, total)), fmt.Sprintf("Found the people in %d of %d frames", done, total))
	}
	req := &aiservices.Request{Kind: aiservices.KindVideoBackground, Task: aiservices.TaskMatte, Model: m.Model,
		Inputs: []aiservices.Media{{Role: aiservices.RoleClip, MIME: "video/mp4", Path: src, SHA256: m.Media, Size: m.MediaSize, Seconds: m.To - m.From, From: m.From, To: m.To}}}
	tk, err := ad.Submit(aiservices.WithEnv(ctx, env), "", req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		var ve *videogen.Error
		if errors.As(err, &ve) && (ve.Kind == videogen.KindUnusable || ve.Kind == videogen.KindInvalid) {
			return nil, &Final{Err: apperr.New(apperr.Provider, "%s", ve.Message).WithHint(ve.Hint)}
		}
		return nil, err
	}
	if tk.Ready == nil || len(tk.Ready.Outputs) == 0 {
		return nil, &Final{Err: errors.New("no matte came back")}
	}
	out := tk.Ready.Outputs[0]
	defer func() { _ = ad.Forget(context.WithoutCancel(ctx), "", tk, tk.Ready.Outputs) }()
	matte := out.File
	if matte == "" {
		matte = filepath.Join(dir, "matte"+aiservices.ExtFor(out.MIME))
		f, err := os.Create(matte)
		if err != nil {
			return nil, err
		}
		_, err = ad.Fetch(ctx, "", tk, &out, f)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, err
		}
	}
	report(0.8, "Keeping the people with their background see-through")
	cut, err := makeCutOut(ctx, a.Tools, src, m.From, m.To, matte, filepath.Join(dir, "cut-out.webm"), func(p float64) {
		report(0.8+0.17*p, "Keeping the people with their background see-through")
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, err
	}
	report(0.97, "Saving the cut-out")
	key, sha, size, err := publish(ctx, a, cut.Path, "video/webm")
	if err != nil {
		return nil, err
	}
	meas := &models.MatteMeasures{Seconds: out.Measures["seconds"], MsPerFrame: out.Measures["msPerFrame"], People: out.Measures["people"],
		FlickerBefore: out.Measures["flickerBefore"], FlickerAfter: out.Measures["flickerAfter"]}
	if err := markReady(ctx, a, m, cut, key, sha, size, local.MatteVersion(ctx, env, m.Model), meas); err != nil {
		return nil, err
	}
	return map[string]any{"matteId": m.ID, "sha256": sha, "frames": cut.Frames}, nil
}

// markReady records a made cut-out.
func markReady(ctx context.Context, a *app.App, m *models.Matte, cut *cutOut, key, sha string, size int64, version string, meas *models.MatteMeasures) error {
	set := bson.M{"state": models.MatteReady, "file": models.MatteFile{SHA256: sha, Key: key, Size: size, MIME: "video/webm"},
		"from": cut.From, "to": cut.To, "first": cut.First, "frames": cut.Frames, "rate": cut.Rate, "width": cut.Width, "height": cut.Height}
	if version != "" {
		set["modelVersion"] = version
	}
	if meas != nil {
		set["measures"] = meas
	}
	if m.MadeOn == "" {
		set["madeOn"], set["madeOnName"] = a.Cfg.Installation.ID, a.Cfg.Installation.Name
	}
	return a.DB.Update(ctx, store.CMattes, a.WorkspaceID(), m.ID, bson.M{"$set": set, "$unset": bson.M{"error": ""},
		"$currentDate": bson.M{"updatedAt": true, "readyAt": true}})
}

// publish keeps a file in the media store by its content.
func publish(ctx context.Context, a *app.App, path, mime string) (key, sha string, size int64, err error) {
	if sha, err = media.HashFile(path); err != nil {
		return
	}
	key = media.ObjectKey(a.WorkspaceID(), sha)
	info, err := a.Media.PutFile(ctx, key, path, media.PutOptions{ContentType: mime, SHA256: sha}, media.Resume{}, nil)
	if err != nil {
		err = apperr.Wrap(apperr.Storage, err, "MetaRush couldn't save the cut-out in its storage.")
		return
	}
	size = info.Size
	_, _ = a.Cache.Adopt(path, sha)
	return
}

// cutOut is a made cut-out file.
type cutOut struct {
	Path          string
	From, To      float64
	First, Frames int
	Rate          string
	Width, Height int
}

// makeCutOut keeps the people of a stretch of a clip with their background
// see-through (D509): the clip's own picture, at most CutOutLong on its long
// side, and the matte (scaled to it) as its alpha, frame for frame, in a
// WebM file with VP9's alpha channel — what Chrome's video plays in the
// preview and Remotion's compositor reads in renders. Its colours are
// described as the clip's, so the people match the clip around them.
func makeCutOut(ctx context.Context, ts *tools.Set, src string, from, to float64, matte, out string, progress func(float64)) (*cutOut, error) {
	v, err := matting.Probe(ctx, ts, src)
	if err != nil {
		return nil, err
	}
	first, count, err := matting.Frames(v, from, to)
	if err != nil {
		return nil, err
	}
	w, h := matting.CutOutSize(v.Width, v.Height, CutOutLong)
	args := matting.CutOutArgs(v, first, count, src, matte, w, h)
	args = append(args, "-progress", "pipe:1", "-nostats", out)
	_, err = ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args, Background: true, OnLine: func(line string) {
		if n, ok := strings.CutPrefix(line, "frame="); ok && progress != nil {
			if k, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
				progress(math.Min(1, float64(k)/float64(count)))
			}
		}
	}})
	if err != nil {
		return nil, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't keep the people with their background see-through.")
	}
	got, err := framesIn(ctx, ts, out)
	if err != nil {
		return nil, err
	}
	if got != count {
		return nil, fmt.Errorf("the cut-out has %d frames where the clip has %d", got, count)
	}
	return &cutOut{Path: out, From: v.FrameTime(first), To: v.FrameTime(first + count), First: first, Frames: count, Rate: v.Rate, Width: w, Height: h}, nil
}

// framesIn counts a video's frames.
func framesIn(ctx context.Context, ts *tools.Set, path string) (int, error) {
	res, err := ts.Run(ctx, tools.Spec{Tool: tools.FFprobe, Args: []string{"-v", "error", "-count_packets", "-select_streams", "v:0",
		"-show_entries", "stream=nb_read_packets", "-of", "csv=p=0", path}})
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(strings.Split(strings.TrimSpace(string(res.Stdout)), "\n")[0]))
}

func plainWords(err error) string {
	ae := apperr.From(err)
	return strings.TrimSuffix(firstNonEmpty(ae.Message, err.Error()), ".") + "."
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if len(r) > 1 && r[1] >= 'A' && r[1] <= 'Z' {
		return s // an initialism
	}
	return strings.ToLower(string(r[0])) + string(r[1:])
}

// keepElsewhere keeps the matte a service elsewhere made (AI work's keeper,
// in the generate job on the computer that asked): the clip's picture kept
// with it as the cut-out, as one made on a computer is.
func keepElsewhere(ctx context.Context, a *app.App, g *models.Generation, path, mime string) (*models.GenOutput, error) {
	id := strings.TrimPrefix(g.Origin, aiwork.MatteFeature)
	m, err := Load(ctx, a, id)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(mime, "video/") {
		return nil, apperr.New(apperr.Provider, "%s sent something that isn't a matte MetaRush can read.", g.ProviderTitle)
	}
	_ = a.DB.Update(ctx, store.CMattes, a.WorkspaceID(), m.ID, bson.M{"$set": bson.M{"state": models.MatteMaking}, "$currentDate": bson.M{"updatedAt": true}})
	src, err := a.Cache.Ensure(ctx, a.Media, m.MediaKey, m.Media, m.MediaSize, nil)
	if err != nil {
		return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get the clip from its storage.")
	}
	dir, err := os.MkdirTemp(a.Cfg.Scratch.Dir, "matte-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	cut, err := makeCutOut(ctx, a.Tools, src, m.From, m.To, path, filepath.Join(dir, "cut-out.webm"), nil)
	if err != nil {
		return nil, apperr.Wrap(apperr.Provider, err, "%s's matte doesn't match the footage sent.", g.ProviderTitle)
	}
	key, sha, size, err := publish(ctx, a, cut.Path, "video/webm")
	if err != nil {
		return nil, err
	}
	if err := markReady(ctx, a, m, cut, key, sha, size, g.ModelVersion, nil); err != nil {
		return nil, err
	}
	return &models.GenOutput{SHA256: sha, Size: size, MIME: "video/webm", StorageKey: key, Width: cut.Width, Height: cut.Height,
		Duration: cut.To - cut.From, FPS: RateOf(cut.Rate), Status: "cut-out"}, nil
}

// endedElsewhere hears how a service elsewhere's work ended: a cut-out that
// didn't come says why.
func endedElsewhere(ctx context.Context, a *app.App, g *models.Generation) {
	id := strings.TrimPrefix(g.Origin, aiwork.MatteFeature)
	m, err := Load(ctx, a, id)
	if err != nil || m.State == models.MatteReady || g.Outcome == nil {
		return
	}
	switch g.Outcome.State {
	case models.GenDone:
	case models.GenCanceled:
		setState(ctx, a, m, models.MatteStopped, "")
	default:
		setState(ctx, a, m, models.MatteFailed, firstNonEmpty(g.Outcome.Message, "the service couldn't make it."))
	}
}

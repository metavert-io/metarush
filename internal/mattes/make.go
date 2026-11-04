package mattes

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/aiwork"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/matting"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// CutoutIn asks for a clip's cut-out.
type CutoutIn struct {
	AssetID   string   `json:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$,description=The clip."`
	ProjectID string   `json:"projectId,omitempty" jsonschema:"pattern=^(prj_[0-9a-z]{26})?$,description=The project it is for: its Content Settings choose the model and say where footage may go (default: the clip's first project)."`
	From      *float64 `json:"from,omitempty" jsonschema:"minimum=0,description=Where the part starts\\, in seconds of the clip (default: its start)."`
	To        *float64 `json:"to,omitempty" jsonschema:"minimum=0,description=Where it ends (default: the clip's end)."`
	// Fixed is the media it follows: "" what the clip plays now, "original"
	// the original, or a fixed copy's SHA-256 (what a cut pins).
	Fixed string `json:"fixed,omitempty" jsonschema:"maxLength=64,description=The clip's media to make it from: original\\, or a fixed copy's SHA-256 (default: what the clip plays now)."`
	Model string `json:"model,omitempty" jsonschema:"maxLength=160,description=provider/model (local/apple-vision\\, local/modnet); default: the usual one where it can run."`
	// MaxUSD is the price agreed for a service elsewhere (none needed on a computer).
	MaxUSD *float64 `json:"maxUsd,omitempty" jsonschema:"minimum=0,description=For a service elsewhere: the most you agree to pay\\, in US dollars."`
	DryRun bool     `json:"dryRun,omitempty" jsonschema:"description=Say what it would use and how long it would take; nothing is made."`
}

// CutoutOut is a clip's cut-out asked for.
type CutoutOut struct {
	Matte CutoutView `json:"matte"`
	// Reused: a cut-out that covers it was made before (nothing new is made).
	Reused bool   `json:"reused"`
	JobID  string `json:"jobId,omitempty"`
	DryRun bool   `json:"dryRun,omitempty"`
	// Seconds is about how long making it takes.
	Seconds float64 `json:"seconds,omitempty" jsonschema:"description=About how long making it takes\\, in seconds."`
	// Here: the model runs on a computer of the workspace (free; nothing
	// leaves it). Estimate is a service elsewhere's price and checks.
	Here     bool               `json:"here"`
	Estimate *aiwork.AIEstimate `json:"estimate,omitempty"`
	Summary  string             `json:"summary"`
}

// plan is a cut-out worked out before anything is made.
type plan struct {
	as        *models.Asset
	projectID string
	fixed     string // the fixed copy it follows ("" the original)
	role      string
	key, sha  string
	size      int64
	from, to  float64
	rate      string
	choice    *aiwork.Choice
	fp        string
}

// clipFor loads a clip a cut-out can be made of.
func clipFor(ctx context.Context, a *app.App, id string) (*models.Asset, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), strings.TrimSpace(id), &as); err != nil {
		return nil, apperr.New(apperr.NotFound, "That clip isn't in this workspace.").WithDetails(map[string]any{"assetId": id})
	}
	if as.IsReference() {
		// Someone else's video is never processed (D441).
		return nil, ops.RefuseReference(&as, "cut the people out of it")
	}
	if !as.Media.HasVideo {
		return nil, apperr.New(apperr.UnsupportedMedia, "“%s” has no picture to cut the people out of.", as.DisplayTitle())
	}
	if as.State != models.AssetAvailable {
		return nil, apperr.New(apperr.Conflict, "“%s” is still being added. Try again once it's ready.", as.DisplayTitle())
	}
	return &as, nil
}

// mediaOf is the file renders play for a clip following a fixed copy ("" the
// original, "now" whatever the clip plays now): its fixed copy's SHA-256,
// where it is, and its frame rate.
func mediaOf(ctx context.Context, a *app.App, as *models.Asset, fixed string) (string, string, string, string, int64, string, error) {
	switch fixed {
	case "now":
		fixed = as.MediaSHA()
	case "original":
		fixed = ""
	}
	if fixed == "" {
		role, key, sha, size := as.RenderSource()
		rate := rateOfMedia(&as.Media)
		if role == models.VariantMezzanine {
			if v, ok := as.OriginalVariant(models.VariantMezzanine); ok && v.Params["fps"] != "" {
				rate = v.Params["fps"] + "/1"
			}
		}
		return "", role, key, sha, size, rate, nil
	}
	var c models.SourceCopy
	err := a.DB.C(store.CSourceCopies).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": as.ID, "sha256": fixed}).Decode(&c)
	if errors.Is(err, mongo.ErrNoDocuments) || (err == nil && c.State != models.FixReady) {
		return "", "", "", "", 0, "", apperr.New(apperr.NotFound, "MetaRush doesn't have that fixed copy of “%s” ready.", as.DisplayTitle()).WithDetails(map[string]any{"fixed": fixed})
	}
	if err != nil {
		return "", "", "", "", 0, "", err
	}
	role, key, sha, size := as.RenderSourceFrom(&c)
	rate := ""
	if c.Media != nil {
		rate = rateOfMedia(c.Media)
	}
	if role == models.VariantMezzanine {
		if v, ok := c.Variants[models.VariantMezzanine]; ok && v.Params["fps"] != "" {
			rate = v.Params["fps"] + "/1"
		}
	}
	return fixed, role, key, sha, size, firstNonEmpty(rate, rateOfMedia(&as.Media)), nil
}

// rateOfMedia is a file's frame rate as a fraction, from its probe.
func rateOfMedia(mi *models.MediaInfo) string {
	for _, s := range mi.Streams {
		if s.Type == "video" && RateOf(s.RFrameRate) > 0 && RateOf(s.RFrameRate) <= 240 {
			return s.RFrameRate
		}
	}
	if mi.FPS > 0 {
		return fmt.Sprintf("%g", mi.FPS)
	}
	return "30"
}

// readyAnywhere says whether a model that runs on a computer can run on one
// of the workspace's: this one, or another that makes cut-outs with it and
// was seen in the last day.
func readyAnywhere(ctx context.Context, a *app.App) func(provider, kind, model string) error {
	here := aiservices.Env{Tools: a.Tools, Cfg: a.Cfg, Home: a.Cfg.Home(), App: a}
	return func(provider, kind, model string) error {
		if !aiservices.RunsHere(provider) {
			return nil
		}
		err := aiservices.ReadyHere(here, provider, kind, model)
		if err == nil || madeElsewhere(ctx, a, provider+"/"+model) {
			return nil
		}
		return err
	}
}

// elsewhereSeen is how recently another computer that makes cut-outs must
// have been seen for work to wait for it (a Mac asleep for the night counts).
const elsewhereSeen = 24 * time.Hour

// madeElsewhere reports another computer of the workspace that makes
// cut-outs with a model (provider/model) and was seen within elsewhereSeen.
func madeElsewhere(ctx context.Context, a *app.App, ref string) bool {
	f := store.SeenWithin("lastHeartbeat", elsewhereSeen)
	f["workspaceId"], f["mattes"], f["jobTypes"] = a.WorkspaceID(), ref, jobs.TypeMatte
	f["_id"] = bson.M{"$ne": a.Cfg.Installation.ID}
	n, err := a.DB.C(store.CWorkers).CountDocuments(ctx, f)
	return err == nil && n > 0
}

// planFor works out a cut-out: the clip, the media renders play for it, the
// stretch, the model by the usual rules (asked, else Content Settings' pin,
// else the usual one where it can run; never a paid service by itself, D225,
// D278) and its fingerprint.
func planFor(ctx context.Context, a *app.App, in *CutoutIn) (*plan, error) {
	as, err := clipFor(ctx, a, in.AssetID)
	if err != nil {
		return nil, err
	}
	p := &plan{as: as, projectID: strings.TrimSpace(in.ProjectID)}
	if p.projectID == "" && len(as.ProjectIDs) > 0 {
		p.projectID = as.ProjectIDs[0]
	}
	fixed := strings.TrimSpace(in.Fixed)
	if fixed == "" {
		fixed = "now"
	}
	if p.fixed, p.role, p.key, p.sha, p.size, p.rate, err = mediaOf(ctx, a, as, fixed); err != nil {
		return nil, err
	}
	dur := as.Media.Duration
	p.from, p.to = 0, dur
	if in.From != nil {
		p.from = *in.From
	}
	if in.To != nil && *in.To > 0 {
		p.to = math.Min(*in.To, dur)
	}
	if p.from < 0 || p.from >= dur || p.to-p.from < 1/math.Max(1, RateOf(p.rate)) {
		return nil, apperr.New(apperr.InvalidInput, "That part of “%s” is too short, or outside it (it is %s long).", as.DisplayTitle(), clock(dur))
	}
	if p.choice, err = aiwork.ChooseWhere(ctx, a, p.projectID, aiservices.KindVideoBackground, in.Model, readyAnywhere(ctx, a)); err != nil {
		return nil, err
	}
	m := p.choice.Model
	if err := rulesAllow(ctx, a, as, p.projectID, m.Provider); err != nil {
		return nil, err
	}
	if aiservices.RunsHere(m.Provider) {
		if err := readyAnywhere(ctx, a)(m.Provider, m.Kind, m.ID); err != nil {
			ae := apperr.From(err)
			return nil, apperr.New(apperr.ToolMissing, "No computer in this workspace can cut the people out of footage yet. %s", ae.Message).
				WithHint(ae.Hint).WithDetails(map[string]any{"model": m.Ref(), "component": matting.Component}).WithCommand("metarush components add " + matting.Component)
		}
	}
	p.fp = Fingerprint(p.sha, p.from, p.to, m.Provider, m.ID)
	return p, nil
}

// rulesAllow checks Content Settings' AI services part (D225) for finding
// the people in a clip with a provider's model, over every project the work
// touches: the one it is for and every project holding the clip (the
// default's settings when it is in none).
func rulesAllow(ctx context.Context, a *app.App, as *models.Asset, projectID, provider string) error {
	projects := append([]string{}, as.ProjectIDs...)
	if len(projects) == 0 {
		projects = []string{""}
	}
	if projectID != "" {
		projects = append([]string{projectID}, projects...)
	}
	seen := map[string]bool{}
	for _, pid := range projects {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		if err := aiwork.Allows(ctx, a, pid, aiservices.KindVideoBackground, provider); err != nil {
			return err
		}
	}
	return nil
}

// seconds is about how long making a cut-out takes: its frames at the time a
// frame took this model in this workspace lately (else a first guess), and
// the file's own making.
func seconds(ctx context.Context, a *app.App, p *plan) float64 {
	m := p.choice.Model
	perFrame := map[string]float64{"apple-vision": 45, "modnet": 180}[m.ID]
	if perFrame == 0 {
		perFrame = 200
	}
	var done []models.Matte
	_ = a.DB.Find(ctx, store.CMattes, a.WorkspaceID(), bson.M{"provider": m.Provider, "model": m.ID, "state": models.MatteReady, "measures.msPerFrame": bson.M{"$gt": 0}}, &done)
	if len(done) > 0 {
		var xs []float64
		for _, d := range done {
			xs = append(xs, d.Measures.MsPerFrame)
		}
		sort.Float64s(xs)
		perFrame = xs[len(xs)/2]
	}
	frames := (p.to - p.from) * math.Max(1, RateOf(p.rate))
	return math.Max(1, math.Round(frames*(perFrame+12)/1000))
}

// Make asks for a clip's cut-out: one that covers it is used as it is; one
// being made is followed; else it is made — on a computer of the workspace
// that can (a heavy matte job), or by a service elsewhere through AI work.
func Make(ctx context.Context, a *app.App, in *CutoutIn) (*CutoutOut, error) {
	p, err := planFor(ctx, a, in)
	if err != nil {
		return nil, err
	}
	m := p.choice.Model
	asked := strings.TrimSpace(in.Model) != ""
	title := p.as.DisplayTitle()
	// One made before that covers it.
	var have *models.Matte
	if asked {
		have, err = Covering(ctx, a, p.as.ID, p.sha, p.from, p.to, m.Provider, m.ID)
	} else {
		have, err = Covering(ctx, a, p.as.ID, p.sha, p.from, p.to, "", "")
	}
	if err != nil {
		return nil, err
	}
	if have != nil {
		out := &CutoutOut{Matte: ViewOf(have, title, p.as.Media.Duration, nil), Reused: true, DryRun: in.DryRun, Here: aiservices.IsLocal(have.Provider)}
		out.Summary = fmt.Sprintf("The cut-out of “%s” (%s) is ready: it was made before, so nothing new is made.", title, out.Matte.Part)
		return out, nil
	}
	// The same one asked for before.
	var same models.Matte
	err = a.DB.C(store.CMattes).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "fingerprint": p.fp}).Decode(&same)
	switch {
	case err == nil:
		job := Refresh(ctx, a, &same)
		switch same.State {
		case models.MatteWaiting, models.MatteMaking:
			out := &CutoutOut{Matte: ViewOf(&same, title, p.as.Media.Duration, job), JobID: same.JobID, DryRun: in.DryRun, Here: aiservices.IsLocal(same.Provider)}
			out.Summary = fmt.Sprintf("The cut-out of “%s” is already being made. %s", title, out.Matte.Words)
			return out, nil
		case models.MatteReady:
			out := &CutoutOut{Matte: ViewOf(&same, title, p.as.Media.Duration, nil), Reused: true, DryRun: in.DryRun, Here: aiservices.IsLocal(same.Provider)}
			out.Summary = fmt.Sprintf("The cut-out of “%s” is ready.", title)
			return out, nil
		}
	case !errors.Is(err, mongo.ErrNoDocuments):
		return nil, err
	}
	if !aiservices.IsLocal(m.Provider) {
		return makeElsewhere(ctx, a, p, in, &same)
	}
	out := &CutoutOut{DryRun: in.DryRun, Here: true, Seconds: seconds(ctx, a, p)}
	draft := models.Matte{ID: same.ID, WorkspaceID: a.WorkspaceID(), AssetID: p.as.ID, ProjectID: p.projectID,
		Media: p.sha, Fixed: p.fixed, MediaKey: p.key, MediaSize: p.size, From: p.from, To: p.to, Rate: p.rate, Provider: m.Provider,
		ProviderTitle: m.ProviderTitle, Model: m.ID, ModelTitle: m.Title, Choice: p.choice.By, Fingerprint: p.fp, State: models.MatteWaiting,
		CreatedBy: a.Actor}
	if in.DryRun {
		out.Matte = ViewOf(&draft, title, p.as.Media.Duration, nil)
		out.Summary = fmt.Sprintf("A cut-out of “%s” (%s) would be made with %s on a computer of this workspace: free, and nothing leaves it; it takes about %s.",
			title, out.Matte.Part, m.Title, durationWords(out.Seconds))
		return out, nil
	}
	if draft.ID == "" {
		draft.ID = ids.New(ids.Matte)
	}
	jobID := ids.New(ids.Job)
	draft.JobID = jobID
	now := a.DB.NowOrLocal(ctx)
	draft.CreatedAt, draft.UpdatedAt = now, now
	if same.ID != "" {
		// Asked for before and failed or stopped: made again, the same record.
		if err := a.DB.Update(ctx, store.CMattes, a.WorkspaceID(), same.ID, bson.M{"$set": bson.M{"state": models.MatteWaiting, "jobId": jobID},
			"$unset": bson.M{"error": ""}, "$currentDate": bson.M{"updatedAt": true}}); err != nil {
			return nil, err
		}
	} else if err := a.DB.Insert(ctx, store.CMattes, draft); err != nil {
		if store.IsDuplicateKey(err) {
			return Make(ctx, a, in) // asked for at the same moment elsewhere: follow that one
		}
		return nil, err
	}
	doing := fmt.Sprintf("Cutting the people out of “%s”", title)
	_, _, err = a.Queue.Enqueue(ctx, jobs.EnqueueOptions{ID: jobID, Type: jobs.TypeMatte, IdempotencyKey: "matte:" + draft.ID + ":" + jobID, Priority: 5, Heavy: true,
		MaxAttempts: 2, ProjectID: p.projectID, AssetID: p.as.ID, Actor: a.Actor,
		Payload: map[string]any{"matteId": draft.ID, "assetId": p.as.ID, "doing": doing, "model": m.Ref()}})
	if err != nil {
		setState(context.WithoutCancel(ctx), a, &draft, models.MatteFailed, "MetaRush couldn't queue it.")
		return nil, err
	}
	a.Audit(ctx, "footage.cutout", "matte", draft.ID, p.projectID, map[string]any{"assetId": p.as.ID, "from": p.from, "to": p.to, "model": m.Ref(),
		"choice": p.choice.By, "media": p.sha, "fixed": p.fixed})
	out.Matte, out.JobID = ViewOf(&draft, title, p.as.Media.Duration, nil), jobID
	out.Summary = fmt.Sprintf("Cutting the people out of “%s” (%s) with %s, on a computer of this workspace: free, and nothing leaves it. It takes about %s.",
		title, out.Matte.Part, m.Title, durationWords(out.Seconds))
	return out, nil
}

// makeElsewhere asks a service elsewhere for the matte through AI work
// (D505): priced first, What may leave asked, the cost recorded with the
// work; the picture of the part is sent without its sound. What comes back
// is kept by keepElsewhere.
func makeElsewhere(ctx context.Context, a *app.App, p *plan, in *CutoutIn, same *models.Matte) (*CutoutOut, error) {
	m := p.choice.Model
	from, to := p.from, p.to
	mi := &aiwork.MakeIn{Kind: aiservices.KindVideoBackground, ProjectID: p.projectID, Model: m.Ref(), Clip: p.as.ID, From: &from, To: &to,
		MaxUSD: in.MaxUSD, DryRun: in.DryRun}
	title := p.as.DisplayTitle()
	if in.DryRun {
		mi.Feature = aiwork.MatteFeature
		est, err := aiwork.EstimateFor(ctx, a, mi)
		if err != nil {
			return nil, err
		}
		draft := models.Matte{AssetID: p.as.ID, From: p.from, To: p.to, Rate: p.rate, Provider: m.Provider, ProviderTitle: m.ProviderTitle, Model: m.ID,
			ModelTitle: m.Title, State: models.MatteWaiting}
		return &CutoutOut{Matte: ViewOf(&draft, title, p.as.Media.Duration, nil), DryRun: true, Estimate: est, Summary: est.Summary}, nil
	}
	// The record first, so what comes back finds it, whenever it comes.
	id := firstNonEmpty(same.ID, ids.New(ids.Matte))
	genID := ids.New(ids.Generation)
	now := a.DB.NowOrLocal(ctx)
	rec := models.Matte{ID: id, WorkspaceID: a.WorkspaceID(), AssetID: p.as.ID, ProjectID: p.projectID, Media: p.sha, Fixed: p.fixed, MediaKey: p.key,
		MediaSize: p.size, From: p.from, To: p.to, Rate: p.rate, Provider: m.Provider, ProviderTitle: m.ProviderTitle, Model: m.ID, ModelTitle: m.Title,
		Choice: p.choice.By, Fingerprint: p.fp, State: models.MatteWaiting, GenerationID: genID, CreatedBy: a.Actor, CreatedAt: now, UpdatedAt: now}
	part, err := pictureOfPart(ctx, a, p)
	if err != nil {
		return nil, err
	}
	if same.ID != "" {
		if err := a.DB.Update(ctx, store.CMattes, a.WorkspaceID(), same.ID, bson.M{"$set": bson.M{"state": models.MatteWaiting, "generationId": genID},
			"$unset": bson.M{"error": "", "jobId": ""}, "$currentDate": bson.M{"updatedAt": true}}); err != nil {
			return nil, err
		}
	} else if err := a.DB.Insert(ctx, store.CMattes, rec); err != nil {
		return nil, err
	}
	mi.Feature, mi.ID, mi.Footage = aiwork.MatteFeature+id, genID, part
	made, err := aiwork.MakeFor(ctx, a, mi)
	if err != nil {
		// Nothing was sent: the record goes with it (or back to how it was).
		if same.ID == "" {
			_, _ = a.DB.C(store.CMattes).DeleteOne(context.WithoutCancel(ctx), bson.M{"_id": id, "workspaceId": a.WorkspaceID(), "state": models.MatteWaiting})
		} else {
			setState(context.WithoutCancel(ctx), a, same, models.MatteFailed, apperr.From(err).Message)
		}
		return nil, err
	}
	_ = a.DB.Update(ctx, store.CMattes, a.WorkspaceID(), id, bson.M{"$set": bson.M{"jobId": made.JobID}})
	rec.JobID = made.JobID
	out := &CutoutOut{Matte: ViewOf(&rec, title, p.as.Media.Duration, nil), JobID: made.JobID, Estimate: &made.Estimate}
	out.Summary = fmt.Sprintf("Asked %s to cut the people out of “%s” (%s), about %s; only its picture is sent.", m.ProviderTitle, title, out.Matte.Part,
		strings.TrimPrefix(made.Estimate.Cost.About, "about "))
	return out, nil
}

// pictureOfPart cuts the picture of a part of a clip to the frame (no sound),
// at most 1080 on its short side, and keeps it by its content: what a
// service elsewhere is sent.
func pictureOfPart(ctx context.Context, a *app.App, p *plan) (*aiwork.FootagePart, error) {
	src, err := a.Cache.Ensure(ctx, a.Media, p.key, p.sha, p.size, nil)
	if err != nil {
		return nil, apperr.Wrap(apperr.Storage, err, "MetaRush couldn't get “%s” from its storage.", p.as.DisplayTitle())
	}
	v, err := matting.Probe(ctx, a.Tools, src)
	if err != nil {
		return nil, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't read “%s”.", p.as.DisplayTitle())
	}
	first, count, err := matting.Frames(v, p.from, p.to)
	if err != nil {
		return nil, apperr.New(apperr.InvalidInput, "That part of “%s” is empty.", p.as.DisplayTitle())
	}
	dir, err := os.MkdirTemp(a.Cfg.Scratch.Dir, "matte-part-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	out := filepath.Join(dir, "part.mp4")
	args := append([]string{"-hide_banner", "-nostdin", "-y", "-loglevel", "error"}, matting.SeekArgs(v, first)...)
	args = append(args, "-i", src, "-map", "0:v:0", "-an", "-frames:v", fmt.Sprint(count),
		"-vf", "scale='if(gt(iw,ih),-2,min(1080,iw))':'if(gt(iw,ih),min(1080,ih),-2)',format=yuv420p", "-c:v", "libx264", "-preset", "veryfast",
		"-crf", "18", "-movflags", "+faststart", out)
	if _, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args}); err != nil {
		return nil, apperr.Wrap(apperr.ToolFailed, err, "MetaRush couldn't cut that part of “%s”.", p.as.DisplayTitle())
	}
	s, err := generate.Keep(ctx, a, out, "video/mp4")
	if err != nil {
		return nil, err
	}
	return &aiwork.FootagePart{Key: s.Key, SHA256: s.SHA256, MIME: "video/mp4", Size: s.Size, Seconds: float64(count) / v.FPS}, nil
}

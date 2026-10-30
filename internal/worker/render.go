package worker

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/probe"
	"github.com/metavert-io/metarush/internal/remotion"
	"github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/transcripts"
	"github.com/metavert-io/metarush/internal/visual"
)

// renderFailure is a render error with the words people read about it: the
// render's own error (the Renders page, notifications) is the plain words, the
// job keeps the technical error (its Details, the activity log, the advice
// job details derive from it).
type renderFailure struct {
	plain string
	err   error
}

func (f *renderFailure) Error() string { return f.err.Error() }
func (f *renderFailure) Unwrap() error { return f.err }

// Plain is what went wrong, in plain words.
func (f *renderFailure) Plain() string { return f.plain }

// failed gives a render error the plain words people read.
func failed(plain string, err error) error { return &renderFailure{plain: plain, err: err} }

// plainFailure is what people read about a failed render.
func plainFailure(err error) string {
	var p interface{ Plain() string }
	if errors.As(err, &p) {
		return p.Plain()
	}
	return "The render stopped with an error. Render it again; if it fails again, its details say what went wrong."
}

// RenderReady reports whether this machine can render now: Node.js and this
// version's own renderer (internal/renderer), or a developer checkout. The
// error says plainly what to do (it is a *renderer.NotReady).
func RenderReady(a *app.App) error {
	_, _, err := renderer.New(a.Cfg, a.Tools).Ready(false)
	return err
}

// rendererFor waits for a renderer setup in progress (the daemon sets this
// version's renderer up when it starts) and returns the folder to run node in,
// marked in use until release is called. A renderer that is missing, removed
// or damaged fails the render with what to do: a person runs `metarush
// renderer install` (renders never silently fall back to another copy).
func rendererFor(ctx context.Context, a *app.App, report Report) (renderer.Status, func(), error) {
	env := renderer.New(a.Cfg, a.Tools)
	deadline := time.Now().Add(30 * time.Minute)
	for {
		st := env.Check(true)
		switch {
		case st.Ready:
			release, err := renderer.Use(st)
			if err != nil {
				return st, nil, err
			}
			return st, release, nil
		case st.State == renderer.StateInstalling && time.Now().Before(deadline):
			report(0.02, "waiting for the renderer to finish setting up")
			select {
			case <-ctx.Done():
				return st, nil, ctx.Err()
			case <-time.After(3 * time.Second):
			}
		default:
			return st, nil, &Permanent{Err: &renderer.NotReady{Status: st}}
		}
	}
}

func remotionVersion(a *app.App) string {
	if st := renderer.New(a.Cfg, a.Tools).Check(false); st.Remotion != "" {
		return st.Remotion
	}
	return "unknown"
}

// facts loader shared with ops (re-implemented here to avoid an import cycle).
type renderFacts struct {
	facts  map[string]editspec.AssetFacts
	tx     map[string][]editspec.TranscriptSegment
	loud   map[string]evidence.StoredLoudness
	series map[string]*evidence.Series
	ch     map[string]int
	// The project motion graphics the cut uses — every one approved by a
	// person (resolveForRender refuses anything else).
	motion map[string]*models.MotionComponent
	beats  map[string]evidence.BeatsOf // music timed to the video, exactly like the preview's facts
	grids  map[string]evidence.GridOf  // its whole-track analysis (tracks with an ending), exactly like the preview's facts
	// extras are the resources the cut pins and the cuts it nests (D82, D84).
	extras *ops.RenderExtras
	// faces are the on-screen indexes (where the faces are), like the preview's facts.
	faces map[string]*visual.Index
	// switches are the footage's picture switches and who is on screen (D160),
	// loaded only for the quality check (renders never read them).
	switches map[string]*editspec.FootageShots
	// copies are the fixed copies the version pins, by asset (D152).
	copies map[string]*models.SourceCopy
}

// Footage implements editspec.SwitchFacts for the quality check.
func (f *renderFacts) Footage(id string) (*editspec.FootageShots, bool) {
	fs, ok := f.switches[id]
	return fs, ok && fs != nil
}

// FixedCopy implements editspec.FixedFacts like the preview's facts.
func (f *renderFacts) FixedCopy(assetID, sha string) (editspec.FixedInfo, bool) {
	c, ok := f.copies[assetID]
	if !ok || c.SHA256 != sha {
		return editspec.FixedInfo{}, false
	}
	return editspec.FixedInfo{Words: ops.CopyWords(c), Ready: c.State == models.FixReady}, true
}

// FaceFocus implements editspec.FaceFacts exactly like the preview's facts
// (output.framing faces, D73).
func (f *renderFacts) FaceFocus(id string, from, to float64) (float64, float64, bool) {
	idx := f.faces[id]
	if idx == nil {
		return 0, 0, false
	}
	x, y, n := visual.FaceFocus(idx, from, to)
	return x, y, n > 0
}

// Subject implements editspec.FollowFacts exactly like the preview's facts
// (output.framing follow, D115).
func (f *renderFacts) Subject(id string, from, to float64) ([]editspec.SubjectSample, []float64, bool) {
	return ops.SubjectOf(f.faces[id], from, to)
}

// FaceBox implements editspec.FaceBoxFacts exactly like the preview's facts
// (conversation layouts, D102).
func (f *renderFacts) FaceBox(id string, from, to float64) ([4]float64, bool) {
	idx := f.faces[id]
	if idx == nil {
		return [4]float64{}, false
	}
	return visual.FaceBox(idx, from, to)
}

// LoudnessSeries implements editspec.LoudnessSeriesFacts exactly like the
// preview's facts (who is speaking in a conversation, D101).
func (f *renderFacts) LoudnessSeries(id string) (float64, []float64, bool) {
	s := f.series[id]
	if s == nil || s.Step <= 0 || len(s.LUFS) == 0 {
		return 0, nil, false
	}
	return s.Step, s.LUFS, true
}

// Beats implements editspec.MusicFacts exactly like the preview's facts.
func (f *renderFacts) Beats(id string) ([]float64, bool) {
	b, ok := f.beats[id]
	return b.Times, ok
}

// MusicGrid implements editspec.MusicGridFacts exactly like the preview's facts.
func (f *renderFacts) MusicGrid(id string) (*editspec.MusicGrid, bool) {
	g, ok := f.grids[id]
	if !ok {
		return nil, false
	}
	return ops.MusicGridOf(g), true
}

// Resource implements editspec.ResourceFacts.
func (f *renderFacts) Resource(id, sha string) (editspec.ResourceInfo, bool) {
	return f.extras.Resource(id, sha)
}

// Nested implements editspec.NestedFacts.
func (f *renderFacts) Nested(projectID, revisionID string) (*editspec.NestedSource, bool) {
	return f.extras.Nested(projectID, revisionID)
}

// Motion implements editspec.MotionFacts for approved versions.
func (f *renderFacts) Motion(hash string) (editspec.MotionInfo, bool) {
	c, ok := f.motion[hash]
	if !ok {
		return editspec.MotionInfo{}, false
	}
	return motion.InfoOf(c), true
}

// RangeLoudness implements editspec.RangeLoudnessFacts exactly like the preview's facts.
func (f *renderFacts) RangeLoudness(id string, from, to float64) (float64, bool) {
	return f.series[id].Integrated(from, to)
}

// AudioChannels implements editspec.ChannelFacts exactly like the preview's facts.
func (f *renderFacts) AudioChannels(id string) int { return f.ch[id] }

// audioChannels is the channel count of an asset's first audio stream (0 when unknown).
func audioChannels(a *models.Asset) int {
	for _, s := range a.Media.Streams {
		if s.Type == "audio" {
			return s.Channels
		}
	}
	return 0
}

// Loudness implements editspec.LoudnessFacts exactly like the preview's facts.
func (f *renderFacts) Loudness(id string) (float64, float64, bool) {
	l, ok := f.loud[id]
	if !ok || l.IntegratedLUFS == nil {
		return 0, 0, false
	}
	tp := 0.0
	if l.TruePeakDBFS != nil {
		tp = *l.TruePeakDBFS
	}
	return *l.IntegratedLUFS, tp, true
}

func (f *renderFacts) Asset(id string) (editspec.AssetFacts, bool) {
	x, ok := f.facts[id]
	return x, ok
}
func (f *renderFacts) Transcript(id string) []editspec.TranscriptSegment { return f.tx[id] }

// A render job that finds no render waits for it before it fails: back in the
// queue for renderWait, then twice as long each time, renderWaits times
// (jobs.Queue.Postpone; the attempts don't count). render.submit saves the
// render before it queues the job, so this only covers a render that is still
// being saved, or one that is gone.
var (
	renderWaits = 3
	renderWait  = time.Second
)

// renderRecord loads the render a render job makes, or postpones the job while
// the render isn't there yet, and fails it, saying so, once waiting is over.
func renderRecord(ctx context.Context, a *app.App, j *models.Job) (models.Render, error) {
	rid, _ := j.Payload["renderId"].(string)
	var rd models.Render
	err := a.DB.GetByID(ctx, store.CRenders, a.WorkspaceID(), rid, &rd)
	if err == nil || !errors.Is(err, store.ErrNotFound) {
		return rd, err // found, or the database didn't answer (an ordinary error, tried again as usual)
	}
	if n := jobs.Postponed(j); n < renderWaits {
		return rd, &postponed{after: renderWait << n,
			reason: fmt.Sprintf("This render isn't saved yet, so MetaRush waits a moment and looks again (%d of %d).", n+1, renderWaits)}
	}
	return rd, failed("MetaRush couldn't find this render, even after waiting for it, so it didn't start. Render the version again.",
		permanent("render %s not found (looked for it %d times)", rid, renderWaits+1))
}

func renderHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	a := w.App
	rd, err := renderRecord(ctx, a, j)
	if err != nil {
		return nil, err
	}
	var rev models.Revision
	if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), rd.RevisionID, &rev); err != nil {
		return nil, failed("The version this render is for isn't there any more.", permanent("revision %s not found", rd.RevisionID))
	}
	setRender := func(set bson.M) {
		_ = a.DB.Update(context.WithoutCancel(ctx), store.CRenders, a.WorkspaceID(), rd.ID, bson.M{"$set": set})
	}
	setRender(bson.M{"state": models.RenderRendering, "renderedBy": a.Cfg.Installation.ID, "renderedByName": a.Cfg.Installation.Name, "error": ""})
	plan, _, assets, facts, err := resolveForRender(ctx, a, &rd, &rev)
	if err != nil {
		return nil, err
	}
	// This version's own renderer (or a developer checkout), never another copy.
	rs, releaseRenderer, err := rendererFor(ctx, a, report)
	if err != nil {
		return nil, err
	}
	defer releaseRenderer()
	rendererID := rs.ID
	if rs.Source == renderer.SourceCheckout {
		rendererID = renderer.SourceCheckout
	}
	plan.Media = "original"
	// Fetch sources into the cache (renders can run on any machine — originals are
	// pulled from the canonical store and verified by hash).
	local := map[string]string{}
	for i, as := range assets {
		_, key, sha, size := as.RenderSourceFrom(facts.copies[as.ID]) // same choice `cache prefetch` makes; a pinned fixed copy (D152)
		p, err := a.Cache.Ensure(ctx, a.Media, key, sha, size, func(done, total int64) {
			report(0.05+0.15*(float64(i)+float64(done)/float64(max(total, 1)))/float64(len(assets)), "getting “"+as.DisplayTitle()+"”")
		})
		if err != nil {
			return nil, failed("MetaRush couldn't get “"+as.DisplayTitle()+"” onto this machine to render it. Check the internet connection and render again.", err)
		}
		local[as.ID] = p
	}
	dir, err := scratchDir(a, j)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	// Recordings placed to the sample whose clocks drift play from copies
	// resampled to the conversation's clock (D119), served with the footage.
	if err := makeDriftCopies(ctx, a, plan, local, dir); err != nil {
		return nil, err
	}
	// Project motion graphics: the approved archives, verified by hash, for
	// the renderer to scan and bundle itself (D68–D70).
	var motionArgs []string
	tv := map[string]string{}
	for h, c := range facts.motion {
		p, err := motion.ArchivePath(ctx, a, c)
		if err != nil {
			return nil, err
		}
		motionArgs = append(motionArgs, "--motion", h+"="+p)
		tv["motion:"+c.Name] = h
	}
	wantDur := float64(plan.DurationInFrames) / float64(plan.FPS)
	if len(motionArgs) > 0 {
		motionArgs = append(motionArgs, motion.RenderLimits(wantDur)...)
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, motion.RenderGrace(wantDur))
		defer cancel()
	}
	// Resource files (logos, pictures, PDF pages, fonts) the plan draws, pinned
	// by hash, served by the same loopback server (allowed under the jail).
	rfiles, err := renderResources(ctx, a, plan, facts.extras)
	if err != nil {
		return nil, err
	}
	types := map[string]string{}
	for sha, f := range rfiles {
		local["r-"+sha] = f.Path
		types["r-"+sha] = f.MIME
	}
	srv, base, err := serveFiles(local, types)
	if err != nil {
		return nil, err
	}
	defer srv.Close()
	for id := range local {
		if sha, ok := strings.CutPrefix(id, "r-"); ok {
			if plan.ResourceSources == nil {
				plan.ResourceSources = map[string]string{}
			}
			plan.ResourceSources[sha] = base + "/" + id
			continue
		}
		plan.Sources[id] = base + "/" + id
	}
	editspec.FillNested(plan)
	props := filepath.Join(dir, "props.json")
	pb, _ := json.Marshal(map[string]any{"plan": plan})
	if err := os.WriteFile(props, pb, 0o600); err != nil {
		return nil, err
	}
	out := filepath.Join(dir, "render.mp4")
	// Final: software x264 at CRF 18. Review: half resolution through the hardware
	// encoder when available (hardware encoders cannot honor CRF).
	scale, encArgs := "1", []string{"--crf", "18", "--hwaccel", "disable"}
	if rd.Quality == "review" {
		scale, encArgs = "0.5", []string{"--hwaccel", "if-possible"}
	}
	conc := runtime.NumCPU() / 2
	if conc < 1 {
		conc = 1
	}
	bundleCache := filepath.Join(a.Cfg.Cache.Dir, "remotion-bundle")
	var mu sync.Mutex
	var versions map[string]any
	var renderErr string
	var blocked []string
	report(0.2, "getting ready to draw the video")
	args := append([]string{"render/render.mjs", "--props", props, "--out", out, "--scale", scale, "--concurrency", fmt.Sprint(conc), "--bundle-cache", bundleCache}, encArgs...)
	_, err = a.Tools.Run(ctx, tools.Spec{Tool: tools.Node, Dir: rs.VideoDir, Background: true,
		Args: append(args, motionArgs...),
		OnLine: func(line string) {
			var ev map[string]any
			if json.Unmarshal([]byte(line), &ev) != nil {
				return
			}
			switch ev["type"] {
			case "versions":
				mu.Lock()
				versions = ev
				mu.Unlock()
			case "progress":
				p, _ := ev["progress"].(float64)
				report(0.2+0.65*p, fmt.Sprintf("rendering %.0f%%", p*100))
			case "blocked":
				mu.Lock()
				for _, h := range anyStrings(ev["hosts"]) {
					blocked = append(blocked, h)
				}
				mu.Unlock()
			case "error":
				msg, _ := ev["message"].(string)
				mu.Lock()
				renderErr = msg
				mu.Unlock()
			}
		}})
	if err != nil {
		const stopped = "The video couldn't be drawn: the renderer stopped with an error. Render it again; if it fails again, its details say what went wrong."
		if renderErr != "" {
			return nil, failed(stopped, fmt.Errorf("remotion: %s", renderErr))
		}
		return nil, failed(stopped, err)
	}
	// Verify the artifact by decoding its metadata — never trust exit codes alone.
	report(0.87, "checking the finished video")
	mi, err := probe.Probe(ctx, a.Tools, out)
	if err != nil {
		return nil, failed("The finished video couldn't be read, so it wasn't kept. Render it again.", fmt.Errorf("render output unreadable: %w", err))
	}
	wantW := int(math.Round(float64(plan.Width) * map[string]float64{"1": 1, "0.5": 0.5}[scale]))
	if math.Abs(mi.Duration-wantDur) > 2/float64(plan.FPS)+0.05 {
		return nil, failed("The finished video came out a different length than the cut, so it wasn't kept. Render it again.",
			permanent("render duration %.3fs does not match plan %.3fs", mi.Duration, wantDur))
	}
	if !mi.HasVideo || absInt(mi.Width-wantW) > 2 {
		return nil, failed("The finished video came out the wrong size, so it wasn't kept. Render it again.", permanent("render is %dx%d, expected width %d", mi.Width, mi.Height, wantW))
	}
	report(0.9, "saving the video")
	setRender(bson.M{"state": models.RenderUploading})
	thumb := filepath.Join(dir, "thumb.jpg")
	_ = (&derive.Deriver{T: a.Tools}).Frame(ctx, out, thumb, math.Min(1.5, wantDur/2), 640)
	key, sha, size, err := publish(ctx, a, out, "video/mp4")
	if err != nil {
		return nil, failed("The finished video couldn't be saved to your storage. Render it again; it's often a brief network problem.", err)
	}
	output := models.RenderOutput{StorageKey: key, SHA256: sha, Size: size, Duration: mi.Duration, Width: mi.Width, Height: mi.Height, FPS: mi.FPS}
	if _, err := os.Stat(thumb); err == nil {
		if tk, _, _, err := publish(ctx, a, thumb, "image/jpeg"); err == nil {
			output.ThumbnailKey = tk
		}
	}
	rv := ""
	mu.Lock()
	if versions != nil {
		rv, _ = versions["remotion"].(string)
		if t, ok := versions["templates"].(map[string]any); ok {
			for k, v := range t {
				tv[k] = fmt.Sprint(v)
			}
		}
	}
	mu.Unlock()
	// Other computers wait a while after it for the quality check (a run's
	// step, the panel's card): the database server's time (D360).
	now := w.serverNow(ctx)
	setRender(bson.M{"state": models.RenderAvailable, "output": output, "rendererVersion": "remotion " + rv, "rendererId": rendererID, "templateVersions": tv, "completedAt": now})
	details := map[string]any{"sha256": sha, "size": size, "duration": mi.Duration, "worker": a.Cfg.Installation.Name, "renderer": rendererID}
	if len(blocked) > 0 {
		// An approved motion graphic reached for the network; the jail blocked it.
		details["motionBlocked"] = blocked
	}
	a.Audit(ctx, "render.complete", "render", rd.ID, rd.ProjectID, details)
	return map[string]any{"renderId": rd.ID, "sha256": sha, "size": size, "duration": mi.Duration, "width": mi.Width, "height": mi.Height}, nil
}

// resolveForRender resolves the frame-accurate plan a render uses: the revision's
// spec with the render's preset override, plus the referenced assets and their
// transcripts. Render and QA share it so QA checks exactly what was rendered.
func resolveForRender(ctx context.Context, a *app.App, rd *models.Render, rev *models.Revision) (*editspec.RenderPlan, *editspec.Spec, []models.Asset, *renderFacts, error) {
	spec := editspec.Clone(&rev.Spec)
	if rd.Preset != "" && rd.Preset != spec.Output.Preset {
		d := editspec.Presets[rd.Preset]
		spec.Output.Preset, spec.Output.Width, spec.Output.Height = rd.Preset, d[0], d[1]
	}
	// Project motion graphics first: only versions a person approved render or
	// get checked (D68). A refusal is final (the job does not retry).
	comps, err := motion.RequireApproved(ctx, a.DB, a.WorkspaceID(), spec)
	if err != nil {
		if ae := apperr.From(err); ae.Code == apperr.Validation {
			return nil, nil, nil, nil, failed(ae.Message, permanent("%s", ae.Message))
		}
		return nil, nil, nil, nil, err
	}
	// Gather assets (clips, audio and narration takes) and transcripts.
	list := editspec.AssetIDs(spec)
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": list}}, &assets); err != nil {
		return nil, nil, nil, nil, err
	}
	if len(assets) != len(list) {
		return nil, nil, nil, nil, failed("Some footage this version uses isn't in MetaRush any more, so it can't be rendered. Put the clip back, or take it out of the cut.",
			permanent("revision references %d asset(s) but only %d exist (missing source media)", len(list), len(assets)))
	}
	// The version plays the media it pins and reads that media's analysis (D152).
	sel := ops.PinnedMedia(spec, list)
	copies, err := ops.LoadPinnedCopies(ctx, a, spec)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	for id := range spec.Fixed {
		if c := copies[id]; c == nil || c.State != models.FixReady {
			return nil, nil, nil, nil, failed("A fixed copy this version plays isn't in MetaRush, so it can't be rendered. Play the original in a new version, or tick the fixes again.",
				permanent("fixed copy %s of %s not found or not ready", spec.FixedSHA(id), id))
		}
	}
	facts := &renderFacts{facts: map[string]editspec.AssetFacts{}, tx: map[string][]editspec.TranscriptSegment{}, loud: evidence.LoadLoudness(ctx, a.DB, a.WorkspaceID(), list, sel), ch: map[string]int{}, motion: comps,
		faces: visual.LoadFaces(ctx, a.DB, a.WorkspaceID(), list, sel), copies: copies}
	if spec.LevelsSpeech() || editspec.HasConversations(spec) {
		facts.series = evidence.LoadSeries(ctx, a.DB, a.WorkspaceID(), list, sel)
	}
	facts.beats = evidence.LoadBeats(ctx, a.DB, a.WorkspaceID(), fittedMusic(spec), sel)
	facts.grids = evidence.LoadGrids(ctx, a.DB, a.WorkspaceID(), fittedMusic(spec), sel)
	pins := transcripts.PinsOf(rev)
	for _, as := range assets {
		facts.ch[as.ID] = audioChannels(&as)
		af := editspec.AssetFacts{ID: as.ID, Name: as.DisplayTitle(), Kind: as.Kind, State: as.State, InProject: true, Duration: as.Media.Duration, HasVideo: as.Media.HasVideo,
			HasAudio: as.Media.HasAudio, Width: as.Media.Width, Height: as.Media.Height, FPS: as.Media.FPS, VFR: as.Media.VFR, HDR: as.Media.HDR}
		// The words the version was made with (D146), as the preview's facts give them.
		segs, prec, ok, err := transcripts.For(ctx, a, &as, pins)
		if err != nil {
			return nil, nil, nil, nil, err
		}
		if ok {
			af.TranscriptPrecision = prec
			facts.tx[as.ID] = segs
		}
		facts.facts[as.ID] = af
	}
	if facts.extras, err = ops.LoadRenderExtras(ctx, a, spec); err != nil {
		return nil, nil, nil, nil, err
	}
	plan := editspec.Resolve(spec, facts, rev.ID, rev.ProjectID, rev.SpecHash)
	// Cuts nested inside this one: their motion graphics must be approved
	// too, and their footage is fetched like the cut's own.
	if facts.motion, err = nestedApprovals(ctx, a, plan, facts.motion); err != nil {
		return nil, nil, nil, nil, err
	}
	have := map[string]bool{}
	for _, as := range assets {
		have[as.ID] = true
	}
	for _, id := range editspec.PlanSourceIDs(plan) {
		if have[id] {
			continue
		}
		as, ok := facts.extras.Asset(id)
		if !ok {
			return nil, nil, nil, nil, failed("A cut played inside this one uses footage that isn't in MetaRush any more, so it can't be rendered.",
				permanent("a cut played inside this one uses footage (%s) that no longer exists", id))
		}
		have[id] = true
		assets = append(assets, *as)
		facts.ch[as.ID] = audioChannels(as)
	}
	// Compositions rendered from Remotion sources: only from versions a
	// person approved (D136). A refusal is final.
	if err := remotion.RequireApproved(ctx, a, assets); err != nil {
		if ae := apperr.From(err); ae.Code == apperr.Validation {
			return nil, nil, nil, nil, failed(ae.Message, permanent("%s", ae.Message))
		}
		return nil, nil, nil, nil, err
	}
	return plan, spec, assets, facts, nil
}

// makeDriftCopies makes the drift copies a plan plays (editspec.DriftCopies
// rewrites the plan's elements to them) from the local files of the footage,
// into dir, and adds them to local under their keys.
func makeDriftCopies(ctx context.Context, a *app.App, plan *editspec.RenderPlan, local map[string]string, dir string) error {
	for _, c := range editspec.DriftCopies(plan) {
		in := local[c.AssetID]
		if in == "" {
			return failed("A recording in a conversation isn't on this machine, so the video can't be drawn. Render it again.", permanent("drift copy: no local file for %s", c.AssetID))
		}
		out := filepath.Join(dir, c.Key+".wav")
		if _, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: c.FFmpegArgs(in, out), Background: true}); err != nil {
			return failed("MetaRush couldn't line a conversation's recordings up for the render. Render it again.", err)
		}
		local[c.Key] = out
	}
	return nil
}

func absInt(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// serveFiles exposes exactly the given files on an ephemeral loopback port under a
// random path prefix, with Range support (needed for seeking). types gives a
// file's Content-Type when its name can't (cache files have no extension; an
// SVG only draws as image/svg+xml).
func serveFiles(files map[string]string, types map[string]string) (*http.Server, string, error) {
	tok := make([]byte, 16)
	_, _ = rand.Read(tok)
	prefix := "/" + hex.EncodeToString(tok)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, "", err
	}
	mux := http.NewServeMux()
	mux.HandleFunc(prefix+"/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, prefix+"/")
		p, ok := files[id]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Access-Control-Allow-Origin", "*")
		if t := types[id]; t != "" {
			w.Header().Set("Content-Type", t)
			// A served SVG never runs script or loads anything.
			w.Header().Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; sandbox")
			w.Header().Set("X-Content-Type-Options", "nosniff")
		}
		http.ServeFile(w, r, p)
	})
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	return srv, "http://" + ln.Addr().String() + prefix, nil
}

func anyStrings(v any) []string {
	xs, _ := v.([]any)
	var out []string
	for _, x := range xs {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// fittedMusic lists the files of tracks timed to the video (as ops does for the preview).
func fittedMusic(s *editspec.Spec) []string {
	var out []string
	for _, t := range s.Audio {
		if t.Fit == editspec.TrackFitAuto {
			out = append(out, t.AssetID)
		}
	}
	return out
}

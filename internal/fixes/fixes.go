// Package fixes is pre-processing for sources (D152, D153): per kind of
// source a few fixes ("Steady the camera", "Remove hum") that MetaRush
// measures and marks when they fit, and that a person ticks. Ticked fixes
// make one fixed copy of the original, stored by its content with its fix
// steps; the source then uses that copy everywhere (previews, analysis,
// Claude, new cuts), while saved versions keep playing what they pinned.
// Unticking goes back to the original. The original is never changed.
package fixes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/oldnames"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sources"
	"github.com/metavert-io/metarush/internal/store"
)

// Job types (internal/worker runs them through Inspect and Prepare).
const (
	// TypeInspect measures a source for its fixes (light).
	TypeInspect = jobs.TypeInspect
	// TypePrepare makes a fixed copy (heavy: it waits on battery like transcripts).
	TypePrepare = jobs.TypePrepare
)

// StepsVersion goes into every fingerprint: changing how a fix works makes
// new copies instead of reusing old ones.
const StepsVersion = 1

// makesCopy reports whether a fix changes the file (music's silent ends only
// change where the music plays from).
func makesCopy(id string) bool {
	f, ok := models.Fix(id)
	return ok && f.Affects != models.AffectsTiming
}

// copyFixes are the ticked fixes that make the copy, in the order they run.
func copyFixes(ticked []string) []string {
	var out []string
	for _, id := range models.RunOrder(ticked) {
		if makesCopy(id) {
			out = append(out, id)
		}
	}
	return out
}

// fixVersions are fixes whose way of working changed after StepsVersion: fix
// steps with one of them hash differently, so copies made the old way are
// not reused while the other fixes' copies still are. Colour 2: its
// brightness step brightens dark pictures (before, it darkened them).
var fixVersions = map[string]int{models.FixColour: 2}

// Fingerprint names the copy of a set of fixes ("" when none makes a copy):
// the same fixes on the same original are made once, on any machine. Its
// input never changes for fixes that work as before — copies already made
// stay in use (D210 renamed it from its old name and kept every value).
func Fingerprint(ticked []string, take int) string {
	fs := copyFixes(ticked)
	if len(fs) == 0 {
		return ""
	}
	m := map[string]any{"v": StepsVersion, "fixes": fs, "take": take}
	for _, f := range fs {
		if v := fixVersions[f]; v > 0 {
			m["v:"+f] = v
		}
	}
	b, _ := json.Marshal(m)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// kindOf is the kind of a source: a person's choice, else MetaRush's.
func kindOf(ctx context.Context, a *app.App, as *models.Asset) string {
	if f := as.Fixes; f != nil && f.KindBy == models.TitleByPerson && f.Kind != "" {
		return f.Kind
	}
	k, _ := sources.Kind(ctx, a, as)
	return k
}

// offered reports whether a fix is one a kind of source is offered.
func offered(kind, id string) bool {
	for _, f := range models.FixesFor(kind) {
		if f.ID == id {
			return true
		}
	}
	return false
}

// affects says what changing from one set of copy fixes to another changes:
// the picture, the sound.
func affects(from, to []string) (picture, sound bool) {
	for _, id := range append(append([]string{}, from...), to...) {
		in := slices.Contains(from, id) != slices.Contains(to, id)
		if !in {
			continue
		}
		f, _ := models.Fix(id)
		switch f.Affects {
		case models.AffectsPicture:
			picture = true
		case models.AffectsSound:
			sound = true
		}
	}
	return
}

func usingFixes(as *models.Asset) []string {
	if as.Fixes != nil && as.Fixes.Copy != nil {
		return as.Fixes.Copy.Fixes
	}
	return nil
}

// SetIn ticks and unticks fixes of a source.
type SetIn struct {
	ID     string   `json:"id" jsonschema:"description=The clip (ast_…) or the picture (res_…)."`
	Tick   []string `json:"tick,omitempty" jsonschema:"description=Fixes to turn on (steady\\, colour\\, grain\\, volumes\\, voices\\, blocks\\, loudness\\, hum\\, ends\\, background\\, edges\\, larger)."`
	Untick []string `json:"untick,omitempty" jsonschema:"description=Fixes to turn off."`
	// Only replaces the ticked set (an empty list with only=true goes back to the original).
	Only *[]string `json:"only,omitempty" jsonschema:"description=Exactly these fixes (an empty list: none\\, the original again)."`
	Kind string    `json:"kind,omitempty" jsonschema:"enum=,enum=camera,enum=call,enum=voice,enum=music,description=Say what kind of source it is (changes the fixes offered)."`
	// Again makes the copy afresh (a better tool is available now).
	Again bool `json:"again,omitempty" jsonschema:"description=Make the fixed copy again (for example once the better steadying is set up)."`
}

// set changes a clip's ticked fixes and starts what that needs: nothing (the
// copy exists: switch now), a prepare job, or going back to the original.
func set(ctx context.Context, a *app.App, in *SetIn) (*View, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.ID, &as); err != nil {
		return nil, apperr.New(apperr.NotFound, "MetaRush can't find that clip.").WithDetails(map[string]any{"id": in.ID})
	}
	if as.State != models.AssetAvailable {
		return nil, apperr.New(apperr.Conflict, "“%s” is still being stored; fix it once it's ready.", as.DisplayTitle())
	}
	fx := models.SourceFixes{}
	if as.Fixes != nil {
		fx = *as.Fixes
	}
	if in.Kind != "" {
		switch {
		case as.Media.HasVideo && (in.Kind == models.SourceCamera || in.Kind == models.SourceCall):
		case !as.Media.HasVideo && (in.Kind == models.SourceVoice || in.Kind == models.SourceMusic):
		case as.Media.HasVideo:
			return nil, apperr.New(apperr.InvalidInput, "“%s” has a picture: it is a camera video or a call or screen recording.", as.DisplayTitle())
		default:
			return nil, apperr.New(apperr.InvalidInput, "“%s” is sound only: it is a voice recording or music.", as.DisplayTitle())
		}
		fx.Kind, fx.KindBy = in.Kind, models.TitleByPerson
	}
	kind := fx.Kind
	if kind == "" || fx.KindBy != models.TitleByPerson {
		kind = kindOf(ctx, a, &as)
		fx.Kind = kind
	}
	ticked := append([]string{}, fx.Ticked...)
	if in.Only != nil {
		ticked = append([]string{}, (*in.Only)...)
	}
	for _, id := range in.Tick {
		if !slices.Contains(ticked, id) {
			ticked = append(ticked, id)
		}
	}
	ticked = slices.DeleteFunc(ticked, func(id string) bool { return slices.Contains(in.Untick, id) })
	for _, id := range ticked {
		if _, ok := models.Fix(id); !ok {
			return nil, apperr.New(apperr.InvalidInput, "MetaRush has no fix called %q.", id)
		}
		if !offered(kind, id) {
			f, _ := models.Fix(id)
			return nil, apperr.New(apperr.InvalidInput, "%s isn't offered for a %s.", f.Label, models.SourceKindWords(kind)).
				WithHint("Say what kind of source it is first, if MetaRush has it wrong.")
		}
		if id == models.FixSteady && !as.Media.HasVideo || (id == models.FixVoices || id == models.FixVolumes || id == models.FixHum || id == models.FixLoudness) && !as.Media.HasAudio {
			f, _ := models.Fix(id)
			return nil, apperr.New(apperr.InvalidInput, "%s needs %s, and “%s” has none.", f.Label, map[bool]string{true: "a picture", false: "sound"}[id == models.FixSteady], as.DisplayTitle())
		}
	}
	ticked = models.RunOrder(ticked)
	take := 0
	if in.Again {
		take = int(time.Now().Unix())
	}
	fp := Fingerprint(ticked, take)
	if !in.Again && fp != "" && fx.Copy != nil && slices.Equal(copyFixes(ticked), fx.Copy.Fixes) {
		fp = fx.Copy.Fingerprint // the copy in use already has these fixes
	}
	now := time.Now().UTC()
	actor := a.Actor
	fx.Ticked, fx.Fingerprint, fx.UpdatedAt, fx.UpdatedBy = ticked, fp, &now, &actor
	fx.Error = ""
	// Music's silent ends: where the music starts and stops (no copy).
	if slices.Contains(ticked, models.FixEnds) {
		if fx.Trim == nil {
			tr, err := measureEnds(ctx, a, &as)
			if err != nil {
				return nil, err
			}
			fx.Trim = tr
		}
	} else {
		fx.Trim = nil
	}
	var job *models.Job
	switch {
	case fp == "":
		fx.State, fx.JobID = "", ""
	case fx.Copy != nil && fx.Copy.Fingerprint == fp:
		fx.State = models.FixReady
	default:
		c, err := ensureCopy(ctx, a, &as, fp, ticked, take)
		if err != nil {
			return nil, err
		}
		if c.State == models.FixReady {
			fx.State = models.FixReady
		} else {
			fx.State = models.FixPreparing
			j, err := queuePrepare(ctx, a, &as, c)
			if err != nil {
				return nil, err
			}
			job, fx.JobID = j, j.ID
		}
	}
	if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"fixes.kind": fx.Kind, "fixes.kindBy": fx.KindBy, "fixes.ticked": fx.Ticked,
		"fixes.fingerprint": fx.Fingerprint, "fixes.state": fx.State, "fixes.error": fx.Error, "fixes.jobId": fx.JobID, "fixes.trim": fx.Trim,
		"fixes.updatedAt": fx.UpdatedAt, "fixes.updatedBy": fx.UpdatedBy, "updatedAt": now}, "$unset": bson.M{oldnames.FixesFingerprint: ""}}); err != nil {
		return nil, err
	}
	a.Audit(ctx, "asset.fixes.set", "asset", as.ID, firstOf(as.ProjectIDs), map[string]any{"ticked": ticked, "fingerprint": fp, "kind": fx.Kind})
	// Nothing to make, or made already: the media switches now.
	switch {
	case fp == "" && as.Fixed():
		if err := switchTo(ctx, a, as.ID, nil); err != nil {
			return nil, err
		}
	case fx.State == models.FixReady && (fx.Copy == nil || fx.Copy.Fingerprint != fp):
		c := FindCopyByFingerprint(ctx, a, as.SHA256, fp)
		if c != nil {
			if err := switchTo(ctx, a, as.ID, c); err != nil {
				return nil, err
			}
		}
	}
	v, err := view(ctx, a, as.ID)
	if err != nil {
		return nil, err
	}
	if job != nil {
		v.JobIDs = []string{job.ID}
	}
	return v, nil
}

// ensureCopy finds or starts the record of a fixed copy.
func ensureCopy(ctx context.Context, a *app.App, as *models.Asset, fp string, ticked []string, take int) (*models.SourceCopy, error) {
	id := ops.CopyID(a.WorkspaceID(), as.SHA256, fp)
	var c models.SourceCopy
	if err := a.DB.GetByID(ctx, store.CSourceCopies, a.WorkspaceID(), id, &c); err == nil {
		if c.State == models.FixFailed {
			_ = a.DB.Update(ctx, store.CSourceCopies, a.WorkspaceID(), id, bson.M{"$set": bson.M{"state": models.FixPreparing, "error": "", "updatedAt": time.Now().UTC()}})
			c.State = models.FixPreparing
		}
		return &c, nil
	}
	now := time.Now().UTC()
	steps := models.FixSteps{Version: StepsVersion, Kind: kindOf(ctx, a, as), Take: take}
	for _, f := range copyFixes(ticked) {
		steps.Steps = append(steps.Steps, models.FixStep{Fix: f})
	}
	c = models.SourceCopy{ID: id, WorkspaceID: a.WorkspaceID(), AssetID: as.ID, SourceSHA256: as.SHA256, Fingerprint: fp, FixSteps: steps, State: models.FixPreparing,
		CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CSourceCopies, c); err != nil && !store.IsDuplicateKey(err) {
		return nil, err
	}
	return &c, nil
}

// FindCopyByFingerprint reads the fixed copy of an original and fingerprint
// (nil when there is none, or it isn't ready).
func FindCopyByFingerprint(ctx context.Context, a *app.App, sourceSHA, fp string) *models.SourceCopy {
	var c models.SourceCopy
	if err := a.DB.GetByID(ctx, store.CSourceCopies, a.WorkspaceID(), ops.CopyID(a.WorkspaceID(), sourceSHA, fp), &c); err != nil || c.State != models.FixReady {
		return nil
	}
	return &c
}

func queuePrepare(ctx context.Context, a *app.App, as *models.Asset, c *models.SourceCopy) (*models.Job, error) {
	var ids []string
	for _, st := range c.FixSteps.Steps {
		ids = append(ids, st.Fix)
	}
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: TypePrepare, IdempotencyKey: TypePrepare + ":" + c.ID, Priority: 4, Heavy: true, AssetID: as.ID,
		ProjectID: firstOf(as.ProjectIDs), MaxAttempts: 2, Actor: a.Actor,
		Payload: map[string]any{"assetId": as.ID, "copyId": c.ID, "doing": Doing(ids)}})
	if err != nil {
		return nil, err
	}
	if j.State == models.JobFailed || j.State == models.JobCanceled {
		// Ticked again after a failure: try again.
		if again, err := a.Queue.Retry(ctx, j.ID); err == nil {
			j = again
		}
	}
	return j, nil
}

// Doing says what a prepare job does, for the activity bar ("Steadying the
// camera and balancing the colour").
func Doing(fixIDs []string) string {
	words := map[string]string{models.FixSteady: "steadying the camera", models.FixColour: "balancing the colour", models.FixGrain: "reducing the grain",
		models.FixVolumes: "evening out the voices", models.FixVoices: "cleaning up the voices", models.FixBlocks: "smoothing the blocky picture",
		models.FixLoudness: "evening out the loudness", models.FixHum: "removing the hum", models.FixBackground: "removing the background",
		models.FixEdges: "trimming the empty edges", models.FixLarger: "making it larger and sharper", models.FixEnds: "trimming the silence at the ends"}
	var parts []string
	for _, id := range models.RunOrder(fixIDs) {
		if w := words[id]; w != "" {
			parts = append(parts, w)
		}
	}
	s := joinAnd(parts)
	if s == "" {
		return "Fixing"
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// switchTo makes a fixed copy (or the original, c nil) the media a clip uses
// now, if the ticked fixes still ask for it, and starts again what the change
// affects (D152): the transcript and voices for the sound, what's on screen
// for the picture. Analysis made before is kept per media, so going back is
// cheap where nothing changed.
func switchTo(ctx context.Context, a *app.App, assetID string, c *models.SourceCopy) error {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as); err != nil {
		return err
	}
	before := usingFixes(&as)
	now := time.Now().UTC()
	var filter, upd bson.M
	if c == nil {
		if !as.Fixed() {
			return nil
		}
		filter = oldnames.FingerprintIs("")
		upd = bson.M{"$set": bson.M{"variants": orEmpty(as.Fixes.OriginalVariants), "fixes.fingerprint": "", "updatedAt": now},
			"$unset": bson.M{"fixes.copy": "", "fixes.originalVariants": "", oldnames.FixesFingerprint: ""}}
	} else {
		if as.Fixed() && as.Fixes.Copy.SHA256 == c.SHA256 {
			return nil
		}
		filter = oldnames.FingerprintIs(c.Fingerprint)
		ref := models.FixedCopyRef{ID: c.ID, SHA256: c.SHA256, StorageKey: c.StorageKey, Size: c.Size, MIME: c.MIME, Fingerprint: c.Fingerprint, Fixes: stepsOf(c), At: now}
		set := bson.M{"fixes.copy": ref, "fixes.fingerprint": c.Fingerprint, "fixes.state": models.FixReady, "fixes.error": "", "variants": orEmpty(c.Variants), "updatedAt": now}
		if !as.Fixed() {
			set["fixes.originalVariants"] = orEmpty(as.Variants)
		}
		upd = bson.M{"$set": set, "$unset": bson.M{oldnames.FixesFingerprint: ""}}
	}
	filter["_id"], filter["workspaceId"] = as.ID, a.WorkspaceID()
	res, err := a.DB.C(store.CAssets).UpdateOne(ctx, filter, upd)
	if err != nil || res.MatchedCount == 0 {
		return err // someone ticked something else meanwhile: theirs wins
	}
	var after []string
	if c != nil {
		after = stepsOf(c)
	}
	a.Audit(ctx, "asset.fixes.switch", "asset", as.ID, firstOf(as.ProjectIDs), map[string]any{"from": before, "to": after})
	picture, sound := affects(before, after)
	reanalyze(ctx, a, &as, mediaKey(c), picture, sound)
	ops.SourceFactsChanged(ctx, a, as.ID, ops.FactsFixed)
	return nil
}

func stepsOf(c *models.SourceCopy) []string {
	var out []string
	for _, st := range c.FixSteps.Steps {
		out = append(out, st.Fix)
	}
	return out
}

func mediaKey(c *models.SourceCopy) string {
	if c == nil {
		return "original"
	}
	return c.SHA256[:16]
}

func orEmpty(m map[string]models.Variant) map[string]models.Variant {
	if m == nil {
		return map[string]models.Variant{}
	}
	return m
}

// reanalyze starts again what a switch of media affects. Each switch gets
// its own jobs (a switch back is a new transcript of the original).
func reanalyze(ctx context.Context, a *app.App, as *models.Asset, media string, picture, sound bool) {
	stamp := fmt.Sprintf("%s@%s:%d", as.ID, media, time.Now().UnixNano())
	add := func(typ string, prio int, heavy bool) {
		_, _, _ = a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: typ, IdempotencyKey: typ + ":" + stamp, Priority: prio, Heavy: heavy, AssetID: as.ID,
			ProjectID: firstOf(as.ProjectIDs), Payload: map[string]any{"assetId": as.ID, "reason": "fixed"}, Actor: a.Actor})
	}
	if sound && as.Media.HasAudio && as.Transcript != nil && as.Transcript.State != "" {
		// The transcribe job hears the media the clip uses now (the fixed
		// copy), keeps the names people gave the voices
		// (speakers.CapturePrevious) and carries people's word fixes over to
		// the new words under the clip's lease, telling the person about any
		// it couldn't place (transcripts.BeforeTranscribing/AfterTranscribing,
		// D149; TestSoundFixRetranscriptionKeepsWordFixes in internal/worker).
		add(jobs.TypeTranscribe, 6, true)
	}
	if (picture || sound) && as.Media.HasVideo {
		add(jobs.TypeEvidence, 8, false)
	}
	if !as.Media.HasVideo && sound {
		add(jobs.TypeEvidence, 8, false)
	}
	if picture && as.Media.HasVideo && as.Analysis["visual"] != "" {
		add(jobs.TypeVisual, 3, true)
	}
}

func firstOf(xs []string) string {
	if len(xs) > 0 {
		return xs[0]
	}
	return ""
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

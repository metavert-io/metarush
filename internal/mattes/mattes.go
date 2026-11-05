// Package mattes keeps clips' cut-outs for background removal in video
// (D505): for a clip, or a stretch of it, the people told from their
// background frame by frame (internal/matting, through an AI services
// adapter of kind video-background), smoothed over time, and kept with the
// clip's picture as one file whose background is see-through (WebM, VP9 with
// an alpha channel, D509), which cuts play over a colour, a blur of the clip
// itself, a picture or another clip (editspec.ClipBackground, D510).
//
// A cut-out is derived media like a fixed copy: made once per fingerprint —
// the media renders play for the clip, the stretch, the model and MetaRush's
// way of making it (Version) — on any computer of the workspace that can (a
// heavy `matte` job no computer is pinned to, D505), reused by every cut that
// plays that stretch of that media, and never changed once ready. A model
// elsewhere (none is built yet) would go through AI work's lifecycle, priced
// first, asked of What may leave, recorded with its cost, its answer kept
// here by AI work's keeper (origin "matte:").
package mattes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Version is MetaRush's way of making a cut-out: the smoothing, the sizes
// and the encoding. A new version makes new cut-outs; those made before stay
// as they are, pinned by the versions that play them.
const Version = "mte-1"

// CutOutLong is the cut-out file's long side at most: footage larger is
// made at this size (renders are 1080p).
const CutOutLong = 1920

// Fingerprint names a cut-out by what it is made from and how: the media's
// SHA-256, the stretch (to the millisecond), the model, and Version.
func Fingerprint(media string, from, to float64, provider, model string) string {
	canon := struct {
		V        string  `json:"v"`
		Media    string  `json:"media"`
		From     float64 `json:"from"`
		To       float64 `json:"to"`
		Provider string  `json:"provider"`
		Model    string  `json:"model"`
	}{Version, media, ms(from), ms(to), provider, model}
	b, _ := json.Marshal(canon)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

func ms(v float64) float64 { return math.Round(v*1000) / 1000 }

// Load reads a cut-out by its ID.
func Load(ctx context.Context, a *app.App, id string) (*models.Matte, error) {
	var m models.Matte
	if err := a.DB.GetByID(ctx, store.CMattes, a.WorkspaceID(), id, &m); err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.New(apperr.NotFound, "That cut-out isn't in this workspace.").WithDetails(map[string]any{"matteId": id})
		}
		return nil, err
	}
	return &m, nil
}

// BySHA reads the cut-out whose file has this SHA-256 (nil when none).
func BySHA(ctx context.Context, a *app.App, sha string) (*models.Matte, error) {
	if sha == "" {
		return nil, nil
	}
	var m models.Matte
	err := a.DB.C(store.CMattes).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "file.sha256": sha}).Decode(&m)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// BySHAs reads the cut-outs whose files have these SHA-256s, by SHA-256.
func BySHAs(ctx context.Context, a *app.App, shas []string) (map[string]*models.Matte, error) {
	out := map[string]*models.Matte{}
	if len(shas) == 0 {
		return out, nil
	}
	var docs []models.Matte
	if err := a.DB.Find(ctx, store.CMattes, a.WorkspaceID(), bson.M{"file.sha256": bson.M{"$in": shas}}, &docs); err != nil {
		return nil, err
	}
	for i := range docs {
		if docs[i].File != nil {
			out[docs[i].File.SHA256] = &docs[i]
		}
	}
	return out, nil
}

// OfClip lists a clip's cut-outs, newest first.
func OfClip(ctx context.Context, a *app.App, assetID string) ([]models.Matte, error) {
	var docs []models.Matte
	err := a.DB.Find(ctx, store.CMattes, a.WorkspaceID(), bson.M{"assetId": assetID}, &docs, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}))
	return docs, err
}

// Covering is the best ready cut-out of a clip's media that covers a
// stretch (made with the model asked for, when one was): the newest, else
// nil. A frame's slack is allowed at each end.
func Covering(ctx context.Context, a *app.App, assetID, media string, from, to float64, provider, model string) (*models.Matte, error) {
	f := bson.M{"assetId": assetID, "media": media, "state": models.MatteReady}
	if model != "" {
		f["provider"], f["model"] = provider, model
	}
	var docs []models.Matte
	if err := a.DB.Find(ctx, store.CMattes, a.WorkspaceID(), f, &docs, options.Find().SetSort(bson.D{{Key: "readyAt", Value: -1}})); err != nil {
		return nil, err
	}
	for i := range docs {
		if covers(&docs[i], from, to) {
			return &docs[i], nil
		}
	}
	return nil, nil
}

// covers reports whether a cut-out covers a stretch, with a frame's slack.
func covers(m *models.Matte, from, to float64) bool {
	slack := 0.001
	if fps := RateOf(m.Rate); fps > 0 {
		slack = 0.5 / fps
	}
	return from >= m.From-slack && to <= m.To+slack
}

// RateOf reads a frame rate written as a fraction (30000/1001) or a number.
func RateOf(r string) float64 {
	n, d, ok := strings.Cut(r, "/")
	var a, b float64
	if _, err := fmt.Sscan(n, &a); err != nil {
		return 0
	}
	if !ok {
		return a
	}
	if _, err := fmt.Sscan(d, &b); err != nil || b == 0 {
		return 0
	}
	return a / b
}

// CutoutView is a cut-out as people and programs see it.
type CutoutView struct {
	ID        string `json:"id"`
	AssetID   string `json:"assetId"`
	Clip      string `json:"clip" jsonschema:"description=The clip's name."`
	ProjectID string `json:"projectId,omitempty"`
	State     string `json:"state" jsonschema:"enum=waiting,enum=making,enum=ready,enum=failed,enum=stopped"`
	// Words say where it is in plain words.
	Words string `json:"words"`
	// Part says the stretch it covers ("the whole clip", "0:12 to 1:24").
	Part   string  `json:"part"`
	From   float64 `json:"from"`
	To     float64 `json:"to"`
	Frames int     `json:"frames,omitempty"`
	Rate   string  `json:"rate,omitempty"`
	// Fixed is the fixed copy it follows ("" the original).
	Fixed         string `json:"fixed,omitempty"`
	Provider      string `json:"provider"`
	ProviderTitle string `json:"providerTitle"`
	Model         string `json:"model"`
	ModelTitle    string `json:"modelTitle"`
	Choice        string `json:"choice,omitempty"`
	// SHA256 is the cut-out file's, once ready: what a cut pins.
	SHA256   string                `json:"sha256,omitempty"`
	Size     int64                 `json:"size,omitempty"`
	Measures *models.MatteMeasures `json:"measures,omitempty"`
	MadeOn   string                `json:"madeOn,omitempty" jsonschema:"description=The computer that made it."`
	JobID    string                `json:"jobId,omitempty"`
	// Pct is how far along it is (0–1) while it is made.
	Pct          float64    `json:"pct,omitempty"`
	Error        string     `json:"error,omitempty"`
	GenerationID string     `json:"generationId,omitempty"`
	CreatedAt    time.Time  `json:"createdAt"`
	ReadyAt      *time.Time `json:"readyAt,omitempty"`
}

// ViewOf is a cut-out's view; job is its job when known (for how far along it is).
func ViewOf(m *models.Matte, clip string, duration float64, job *models.Job) CutoutView {
	v := CutoutView{ID: m.ID, AssetID: m.AssetID, Clip: clip, ProjectID: m.ProjectID, State: m.State, From: m.From, To: m.To, Frames: m.Frames, Rate: m.Rate,
		Fixed: m.Fixed, Provider: m.Provider, ProviderTitle: m.ProviderTitle, Model: m.Model, ModelTitle: m.ModelTitle, Choice: m.Choice,
		Measures: m.Measures, MadeOn: m.MadeOnName, JobID: m.JobID, Error: m.Error, GenerationID: m.GenerationID, CreatedAt: m.CreatedAt, ReadyAt: m.ReadyAt}
	if m.File != nil {
		v.SHA256, v.Size = m.File.SHA256, m.File.Size
	}
	v.Part = PartWords(m.From, m.To, duration)
	if job != nil && (m.State == models.MatteWaiting || m.State == models.MatteMaking) {
		v.Pct = job.Progress.Pct
	}
	v.Words = stateWords(m, v.Pct)
	return v
}

// stateWords says where a cut-out is.
func stateWords(m *models.Matte, pct float64) string {
	with := m.ModelTitle
	switch m.State {
	case models.MatteReady:
		s := "Ready"
		if m.MadeOnName != "" {
			s += ": made on " + m.MadeOnName
		}
		if with != "" {
			if m.MadeOnName != "" {
				s += " with " + with
			} else {
				s += ": made with " + with
			}
		}
		if m.Measures != nil && m.Measures.Seconds > 0 {
			s += " in " + durationWords(m.Measures.Seconds)
		}
		return s + "."
	case models.MatteMaking:
		if pct > 0 {
			return fmt.Sprintf("Finding the people in the footage: %d%% done.", int(math.Round(pct*100)))
		}
		return "Finding the people in the footage."
	case models.MatteFailed:
		return "It couldn't be made: " + firstNonEmpty(m.Error, "something went wrong.")
	case models.MatteStopped:
		return "Stopped before it was finished."
	}
	return "Waiting for a computer that can make it."
}

// PartWords says a stretch of a clip: the whole clip, or from … to ….
func PartWords(from, to, duration float64) string {
	if from <= 0.05 && (duration <= 0 || to >= duration-0.05) {
		return "the whole clip"
	}
	return "from " + clock(from) + " to " + clock(to)
}

func clock(s float64) string {
	s = math.Max(0, s)
	m := int(s) / 60
	return fmt.Sprintf("%d:%02d", m, int(s)-m*60)
}

func durationWords(s float64) string {
	switch {
	case s < 1:
		return "under a second"
	case s < 90:
		return fmt.Sprintf("%d seconds", int(math.Round(s)))
	case s < 5400:
		return fmt.Sprintf("%d minutes", int(math.Round(s/60)))
	}
	return fmt.Sprintf("%.1f hours", s/3600)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

// Refresh brings a cut-out that waits or is being made in step with its job:
// a job that failed, was stopped or is gone leaves it failed or stopped (a
// worker that stopped mid-way releases the job, so that one keeps waiting).
func Refresh(ctx context.Context, a *app.App, m *models.Matte) *models.Job {
	if m.State != models.MatteWaiting && m.State != models.MatteMaking {
		return nil
	}
	if m.GenerationID != "" {
		return nil // AI work's own lifecycle settles it (its keeper and ended hook)
	}
	if m.JobID == "" {
		return nil
	}
	j, err := a.Queue.Get(ctx, m.JobID)
	switch {
	case errors.Is(err, store.ErrNotFound):
		setState(ctx, a, m, models.MatteFailed, "the work to make it was lost; ask again.")
		return nil
	case err != nil:
		return nil
	}
	switch j.State {
	case models.JobFailed:
		setState(ctx, a, m, models.MatteFailed, firstNonEmpty(plainJobError(j), "something went wrong."))
	case models.JobCanceled:
		setState(ctx, a, m, models.MatteStopped, "")
	}
	return j
}

// plainJobError is a failed job's last error, in the words it carries.
func plainJobError(j *models.Job) string { return strings.TrimSpace(j.Error) }

// setState moves a cut-out that isn't ready to another state.
func setState(ctx context.Context, a *app.App, m *models.Matte, state, why string) {
	set := bson.M{"state": state}
	unset := bson.M{}
	if why != "" {
		set["error"] = why
	} else {
		unset["error"] = ""
	}
	upd := bson.M{"$set": set, "$currentDate": bson.M{"updatedAt": true}}
	if len(unset) > 0 {
		upd["$unset"] = unset
	}
	res, err := a.DB.C(store.CMattes).UpdateOne(ctx, bson.M{"_id": m.ID, "workspaceId": a.WorkspaceID(), "state": bson.M{"$ne": models.MatteReady}}, upd)
	if err == nil && res.MatchedCount > 0 {
		m.State, m.Error = state, why
	}
}

// LocalModels are the models this computer makes cut-outs with now, best
// first, as provider/model.
func LocalModels(a *app.App) []string {
	e := aiservices.Env{Tools: a.Tools, Cfg: a.Cfg, Home: a.Cfg.Home(), App: a}
	ms := aiservices.ModelsOf(aiservices.KindVideoBackground)
	sort.SliceStable(ms, func(i, j int) bool { return ms[i].Rank > ms[j].Rank })
	var out []string
	for _, m := range ms {
		if !aiservices.IsLocal(m.Provider) {
			continue
		}
		if aiservices.ReadyHere(e, m.Provider, m.Kind, m.ID) == nil {
			out = append(out, m.Ref())
		}
	}
	return out
}

package ops

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
	"github.com/metavert-io/metarush/internal/visual"
)

// dbFacts adapts stored assets and transcripts to editspec.Facts (and
// editspec.LoudnessFacts, from technical evidence).
type dbFacts struct {
	assets map[string]*models.Asset
	facts  map[string]editspec.AssetFacts
	tx     map[string][]editspec.TranscriptSegment
	loud   map[string]evidence.StoredLoudness
	series map[string]*evidence.Series
	motion map[string]editspec.MotionInfo // project motion graphics the spec uses, by hash
	beats  map[string]evidence.BeatsOf    // music timed to the video (audio fit auto)
	grids  map[string]evidence.GridOf     // … and its whole-track analysis (tracks with an ending, D120)
	// res are the resources the spec pins; nested the cuts it nests (D82, D84).
	res    *resourceFacts
	nested *nestedFacts
	// view is what the spec's project can use through its links (D83).
	view  *projectlinks.View
	faces map[string]*visual.Index // where the faces are (on-screen index), by asset
	// Picture switches, who is on screen and printed names (D160), read the
	// first time something asks (Footage, calls.go).
	loadSwitches func() map[string]*editspec.FootageShots
	switchesOnce sync.Once
	switches     map[string]*editspec.FootageShots
	// fixed are the fixed copies the spec pins, by asset (D152).
	fixed map[string]*models.SourceCopy
}

// Beats implements editspec.MusicFacts: the beats found when music was added.
func (f *dbFacts) Beats(id string) ([]float64, bool) {
	b, ok := f.beats[id]
	return b.Times, ok
}

// MusicGrid implements editspec.MusicGridFacts: a music file's whole-track analysis.
func (f *dbFacts) MusicGrid(id string) (*editspec.MusicGrid, bool) {
	g, ok := f.grids[id]
	if !ok {
		return nil, false
	}
	return MusicGridOf(g), true
}

// MusicGridOf is the stored analysis as the engine reads it.
func MusicGridOf(g evidence.GridOf) *editspec.MusicGrid {
	return &editspec.MusicGrid{Beats: g.Beats, Downbeats: g.Downbeats, Phrases: g.Phrases}
}

// FaceFocus implements editspec.FaceFacts: the main face of a source range.
func (f *dbFacts) FaceFocus(id string, from, to float64) (float64, float64, bool) {
	idx := f.faces[id]
	if idx == nil {
		return 0, 0, false
	}
	x, y, n := visual.FaceFocus(idx, from, to)
	return x, y, n > 0
}

// Subject implements editspec.FollowFacts: where the subject is over a source
// range (the main face of each shot, else what draws the eye) and the
// footage's cuts, from the on-screen index (D115).
func (f *dbFacts) Subject(id string, from, to float64) ([]editspec.SubjectSample, []float64, bool) {
	return subjectOf(f.faces[id], from, to)
}

// subjectOf reads the subject from an index (the preview's facts and the
// render's use it alike, so they agree).
func subjectOf(idx *visual.Index, from, to float64) ([]editspec.SubjectSample, []float64, bool) {
	if idx == nil {
		return nil, nil, false
	}
	s, cuts, ok := visual.Subject(idx, from, to)
	if !ok {
		return nil, nil, false
	}
	out := make([]editspec.SubjectSample, len(s))
	for i, p := range s {
		out[i] = editspec.SubjectSample{At: p.At, X: p.X, Y: p.Y, Face: p.Face}
	}
	return out, cuts, true
}

// SubjectOf is subjectOf for the worker's render facts.
func SubjectOf(idx *visual.Index, from, to float64) ([]editspec.SubjectSample, []float64, bool) {
	return subjectOf(idx, from, to)
}

// FaceBox implements editspec.FaceBoxFacts: the main face's box over a source
// range (the median of the biggest face in each frame that shows one).
func (f *dbFacts) FaceBox(id string, from, to float64) ([4]float64, bool) {
	idx := f.faces[id]
	if idx == nil {
		return [4]float64{}, false
	}
	return visual.FaceBox(idx, from, to)
}

// LoudnessSeries implements editspec.LoudnessSeriesFacts: the momentary
// loudness kept with the asset's evidence (loaded for conversations' recordings).
func (f *dbFacts) LoudnessSeries(id string) (float64, []float64, bool) {
	s := f.series[id]
	if s == nil || s.Step <= 0 || len(s.LUFS) == 0 {
		return 0, nil, false
	}
	return s.Step, s.LUFS, true
}

// Motion implements editspec.MotionFacts: what the workspace's check of a
// motion graphic version recorded, and whether a person approved it.
func (f *dbFacts) Motion(hash string) (editspec.MotionInfo, bool) {
	m, ok := f.motion[hash]
	return m, ok
}

// RangeLoudness implements editspec.RangeLoudnessFacts: the gated loudness of a
// source range, from the momentary loudness kept with the asset's evidence.
func (f *dbFacts) RangeLoudness(id string, from, to float64) (float64, bool) {
	return f.series[id].Integrated(from, to)
}

func (f *dbFacts) Asset(id string) (editspec.AssetFacts, bool)       { a, ok := f.facts[id]; return a, ok }
func (f *dbFacts) Transcript(id string) []editspec.TranscriptSegment { return f.tx[id] }

// Loudness implements editspec.LoudnessFacts.
func (f *dbFacts) Loudness(id string) (float64, float64, bool) {
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

// AudioChannels implements editspec.ChannelFacts (0 when unknown).
func (f *dbFacts) AudioChannels(id string) int { return AudioChannelsOf(f.assets[id]) }

// AudioChannelsOf returns the channel count of an asset's first audio stream (0 when unknown).
func AudioChannelsOf(a *models.Asset) int {
	if a == nil {
		return 0
	}
	for _, s := range a.Media.Streams {
		if s.Type == "audio" {
			return s.Channels
		}
	}
	return 0
}

// fittedMusic lists the files of tracks timed to the video (their beats matter).
func fittedMusic(s *editspec.Spec) []string {
	var out []string
	for _, t := range s.Audio {
		if t.Fit == editspec.TrackFitAuto {
			out = append(out, t.AssetID)
		}
	}
	return out
}

// specAssetIDs lists every asset a spec plays (clips, audio, narration).
func specAssetIDs(s *editspec.Spec) []string { return editspec.AssetIDs(s) }

// LoadFacts gathers asset, transcript and loudness facts for a spec (for
// packages that validate or resolve specs outside this one).
func LoadFacts(ctx context.Context, a *app.App, projectID string, s *editspec.Spec) (editspec.Facts, error) {
	return loadFacts(ctx, a, projectID, s)
}

// AssetOf returns a loaded asset document from facts built by LoadFacts.
func AssetOf(f editspec.Facts, id string) *models.Asset {
	if d, ok := f.(*dbFacts); ok {
		return d.assets[id]
	}
	return nil
}

// loadFacts gathers everything validation and planning need for a spec, with
// the transcripts as they are now (a change about to be saved uses them).
func loadFacts(ctx context.Context, a *app.App, projectID string, s *editspec.Spec) (*dbFacts, error) {
	return loadFactsPinned(ctx, a, projectID, s, nil)
}

// loadFactsPinned is loadFacts for a saved version: each clip's words are the
// ones it was made with (pins, D146); nil pins read the transcripts as they are.
func loadFactsPinned(ctx context.Context, a *app.App, projectID string, s *editspec.Spec, pins *transcripts.Pins) (*dbFacts, error) {
	view, err := projectlinks.Load(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	return loadFactsWith(ctx, a, projectID, view, s, 0, []string{projectID}, pins)
}

// LoadRevisionFacts gathers the facts of a saved version, with the words it
// was made with (for packages that resolve saved versions outside this one).
func LoadRevisionFacts(ctx context.Context, a *app.App, r *models.Revision) (editspec.Facts, error) {
	spec := r.Spec
	return loadFactsPinned(ctx, a, r.ProjectID, &spec, transcripts.PinsOf(r))
}

// pinTranscripts keeps the words a version is being saved with (D146): the
// transcripts its check read, one pin per clip that has one.
func pinTranscripts(ctx context.Context, a *app.App, f *dbFacts) ([]models.TranscriptPin, error) {
	if f == nil || len(f.tx) == 0 {
		return nil, nil
	}
	prec, revs := map[string]string{}, map[string]int{}
	for id := range f.tx {
		prec[id] = f.facts[id].TranscriptPrecision
		if as := f.assets[id]; as != nil && as.TextFixes != nil {
			revs[id] = as.TextFixes.Rev
		}
	}
	return transcripts.Pin(ctx, a, f.tx, prec, revs)
}

// loadFactsWith is loadFacts with the project's links already read, for a
// spec nested depth deep inside the projects in chain.
func loadFactsWith(ctx context.Context, a *app.App, projectID string, view *projectlinks.View, s *editspec.Spec, depth int, chain []string, pins *transcripts.Pins) (*dbFacts, error) {
	f := &dbFacts{assets: map[string]*models.Asset{}, facts: map[string]editspec.AssetFacts{}, tx: map[string][]editspec.TranscriptSegment{}, loud: map[string]evidence.StoredLoudness{},
		faces: map[string]*visual.Index{}, view: view}
	m, err := motion.Infos(ctx, a.DB, a.WorkspaceID(), editspec.MotionHashes(s))
	if err != nil {
		return nil, err
	}
	f.motion = m
	if f.res, err = loadResourceFacts(ctx, a, projectID, view, s); err != nil {
		return nil, err
	}
	if f.nested, err = loadNested(ctx, a, projectID, view, s, depth, chain); err != nil {
		return nil, err
	}
	ids := specAssetIDs(s)
	if len(ids) == 0 {
		return f, nil
	}
	// The analysis of the media the spec plays: its pinned fixed copies' (D152).
	sel := PinnedMedia(s, ids)
	if f.fixed, err = LoadPinnedCopies(ctx, a, s); err != nil {
		return nil, err
	}
	f.loud = evidence.LoadLoudness(ctx, a.DB, a.WorkspaceID(), ids, sel)
	f.faces = visual.LoadFaces(ctx, a.DB, a.WorkspaceID(), ids, sel)
	f.loadSwitches = switchLoader(ctx, a, f, ids)
	if s.LevelsSpeech() || editspec.HasConversations(s) {
		// Conversations also read who is speaking from their recordings' loudness (D101).
		f.series = evidence.LoadSeries(ctx, a.DB, a.WorkspaceID(), ids, sel)
	}
	f.beats = evidence.LoadBeats(ctx, a.DB, a.WorkspaceID(), fittedMusic(s), sel)
	f.grids = evidence.LoadGrids(ctx, a.DB, a.WorkspaceID(), fittedMusic(s), sel)
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": ids}}, &assets); err != nil {
		return nil, err
	}
	for i := range assets {
		as := &assets[i]
		f.assets[as.ID] = as
		af := editspec.AssetFacts{ID: as.ID, Name: as.DisplayTitle(), Kind: as.Kind, State: as.State, InProject: slices.Contains(as.ProjectIDs, projectID) || (projectID != "" && view.SeesAsset(ctx, a, as)) || assumed(ctx, as.ID),
			Duration: as.Media.Duration, HasVideo: as.Media.HasVideo, HasAudio: as.Media.HasAudio, Width: as.Media.Width, Height: as.Media.Height,
			FPS: as.Media.FPS, VFR: as.Media.VFR, HDR: as.Media.HDR}
		_, af.HasMezzanine = as.Variants[models.VariantMezzanine]
		af.RightsRecorded = as.Rights != nil && as.Rights.License != ""
		// The words: as they are now, or the ones a saved version was made with.
		segs, prec, ok, err := transcripts.For(ctx, a, as, pins)
		if err != nil {
			return nil, err
		}
		if ok {
			af.TranscriptPrecision = prec
			f.tx[as.ID] = segs
		}
		f.facts[as.ID] = af
	}
	remotionFacts(ctx, a, assets, f.facts)
	return f, nil
}

// remotionFacts says where the versions of rendered Remotion compositions
// stand (D136): validation refuses clips from versions no person approved.
func remotionFacts(ctx context.Context, a *app.App, assets []models.Asset, facts map[string]editspec.AssetFacts) {
	var hashes []string
	for i := range assets {
		if s := assets[i].Source; s.IsRemotion() && s.Remotion.Kind == models.SourceRemotionRender {
			hashes = append(hashes, a.WorkspaceID()+"/"+s.Remotion.Version)
		}
	}
	if len(hashes) == 0 {
		return
	}
	var vs []models.RemotionVersion
	if err := a.DB.Find(ctx, store.CRemotionVersions, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": hashes}}, &vs); err != nil {
		return
	}
	state := map[string]string{}
	for _, v := range vs {
		state[v.Hash] = v.State
	}
	for i := range assets {
		s := assets[i].Source
		if !s.IsRemotion() || s.Remotion.Kind != models.SourceRemotionRender {
			continue
		}
		af := facts[assets[i].ID]
		st := state[s.Remotion.Version]
		if st == "" {
			st = models.RemotionRevoked // the version is gone: treat it as not usable
		}
		af.Remotion = &editspec.RemotionFacts{SourceTitle: s.Remotion.SourceTitle, Composition: s.Remotion.Composition, VersionNumber: s.Remotion.VersionNumber, State: st}
		facts[assets[i].ID] = af
	}
}

// loadTranscript reads a clip's transcript as captions and cuts read it
// (captions show the name someone gave the voice; an unnamed voice is
// "Speaker N" only when there is more than one voice to tell apart).
func loadTranscript(ctx context.Context, a *app.App, assetID string) ([]editspec.TranscriptSegment, error) {
	segs, err := transcripts.LoadSegments(ctx, a, assetID)
	if err != nil {
		return nil, err
	}
	return transcripts.Engine(segs), nil
}

// dashboardURL builds a deep link into this installation's dashboard: at the
// public address when remote access is on (D128), so a link printed here
// opens on any of the person's computers.
func dashboardURL(a *app.App, path string) string {
	return DashboardURL(a, path)
}

// DashboardURL is dashboardURL for other packages (printed and stored links).
func DashboardURL(a *app.App, path string) string {
	if u := a.Cfg.Remote().PublicURL; u != "" {
		return u + path
	}
	return fmt.Sprintf("http://%s:%d%s", a.Cfg.Dashboard.Host, a.Cfg.Dashboard.Port, path)
}

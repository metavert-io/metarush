package sound

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// MusicAddIn puts music under the cut.
type MusicAddIn struct {
	RevisionID string   `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Asset      string   `json:"asset" jsonschema:"minLength=1,maxLength=300,description=The music: an audio file in the workspace (its ID or title). It is added to the project when it is not in it yet."`
	Fit        string   `json:"fit,omitempty" jsonschema:"enum=auto,enum=manual,description=auto (default): starts with the video and ends with it; manual: from → to of the track\\, from at."`
	From       *float64 `json:"from,omitempty" jsonschema:"minimum=0,description=Where to start in the track (seconds)."`
	To         *float64 `json:"to,omitempty" jsonschema:"minimum=0,description=Where to stop in the track (seconds; default its end)."`
	At         *float64 `json:"at,omitempty" jsonschema:"minimum=0,description=Where it starts in the video (seconds; default the start)."`
	GainDB     *float64 `json:"gainDb,omitempty" jsonschema:"minimum=-60,maximum=12,description=Its level in dB (default -12: a bed under voices)."`
	Duck       *bool    `json:"duck,omitempty" jsonschema:"description=Gets quieter while someone talks (default on)."`
	DuckDB     *float64 `json:"duckDb,omitempty" jsonschema:"minimum=-30,maximum=0,description=How much quieter while someone talks (dB; default: the mix's depth)."`
	Loop       bool     `json:"loop,omitempty" jsonschema:"description=Repeat a shorter track until the video ends (fit auto)."`
	Ending     string   `json:"ending,omitempty" jsonschema:"enum=phrase,enum=beat,description=fit auto: end where the music starts its next phrase (default) or on a beat."`
	ID         string   `json:"id,omitempty" jsonschema:"pattern=^[a-z][a-z0-9_-]*$,maxLength=64,description=A name for the track (default music\\, music-2…)."`
	Apply      bool     `json:"apply,omitempty"`
}

// MusicSetIn changes a music track (the music lane's drags, the Sound tab, the CLI).
type MusicSetIn struct {
	RevisionID string   `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Track      string   `json:"track" jsonschema:"minLength=1,maxLength=200,description=The track's name (id) or its file's title."`
	Fit        string   `json:"fit,omitempty" jsonschema:"enum=auto,enum=manual"`
	From       *float64 `json:"from,omitempty" jsonschema:"minimum=0"`
	To         *float64 `json:"to,omitempty" jsonschema:"minimum=0"`
	At         *float64 `json:"at,omitempty" jsonschema:"minimum=0"`
	GainDB     *float64 `json:"gainDb,omitempty" jsonschema:"minimum=-60,maximum=12"`
	Duck       string   `json:"duck,omitempty" jsonschema:"enum=on,enum=off,enum=auto,description=on: quieter while someone talks; off: never; auto: as the mix decides."`
	DuckDB     *float64 `json:"duckDb,omitempty" jsonschema:"minimum=-30,maximum=0"`
	Loop       *bool    `json:"loop,omitempty"`
	Ending     string   `json:"ending,omitempty" jsonschema:"enum=phrase,enum=beat,description=fit auto: end where the music starts its next phrase or on a beat."`
	Remove     bool     `json:"remove,omitempty" jsonschema:"description=Take the music out of the cut."`
	Apply      bool     `json:"apply,omitempty"`
}

// MusicOut is the change and where the music plays.
type MusicOut struct {
	Track     editspec.AudioTrack `json:"track"`
	Placement string              `json:"placement" jsonschema:"description=Where it plays\\, in words."`
	Tempo     float64             `json:"tempo,omitempty" jsonschema:"description=Beats per minute found in the music (0: no clear beat)."`
	Beats     int                 `json:"beats" jsonschema:"description=How many beats (or strong notes) were found."`
	// The whole track's grid (D120), for music with an ending.
	Tempos  []evidence.TempoSpan  `json:"tempos,omitempty" jsonschema:"description=The tempo over the track: steady stretches and smooth changes."`
	Meter   int                   `json:"meter,omitempty" jsonschema:"description=Beats in a bar (0: the bars aren't clear)."`
	Phrases int                   `json:"phrases,omitempty" jsonschema:"description=How many phrases were found."`
	Music   string                `json:"music,omitempty" jsonschema:"description=What was found in the music\\, in words."`
	Rights  string                `json:"rights,omitempty" jsonschema:"description=A reminder when nobody has said where the music comes from."`
	Result  *voiceover.PlanResult `json:"result,omitempty"`
	Applied *voiceover.Applied    `json:"applied,omitempty"`
	Summary string                `json:"summary"`
}

// DefaultMusicGain is a music bed's level under voices.
const DefaultMusicGain = -12.0

// findAudioAsset finds a workspace audio file by ID or title (the project's first).
func findAudioAsset(ctx context.Context, a *app.App, projectID, ref string) (*models.Asset, error) {
	ref = strings.TrimSpace(ref)
	var as models.Asset
	if strings.HasPrefix(ref, "ast_") {
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), ref, &as); err != nil {
			return nil, apperr.New(apperr.NotFound, "That music file isn't in this workspace.").WithDetails(map[string]any{"asset": ref})
		}
		return &as, nil
	}
	re := bson.M{"$regex": "^" + regexp.QuoteMeta(ref) + "$", "$options": "i"}
	for _, filter := range []bson.M{
		{"projectIds": projectID, "$or": bson.A{bson.M{"title": re}, bson.M{"name": re}}},
		{"$or": bson.A{bson.M{"title": re}, bson.M{"name": re}}, "tags": bson.M{"$ne": TagEnhanced}},
	} {
		var list []models.Asset
		if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), filter, &list); err == nil && len(list) > 0 {
			return &list[0], nil
		}
	}
	return nil, apperr.New(apperr.NotFound, "There is no music called “%s” in this workspace.", ref).WithHint("Add the file first: drop it on the Sound tab.").
		WithCommand("metarush sound music add <file>")
}

// musicGrid returns the file's whole-track analysis (D120), making it the
// first time: every beat at the music's own tempo, which may change, its bars
// and its phrases, from start to end (streamed: any length).
func musicGrid(ctx context.Context, a *app.App, as *models.Asset) (evidence.GridOf, error) {
	if g, ok := evidence.LoadGrids(ctx, a.DB, a.WorkspaceID(), []string{as.ID})[as.ID]; ok {
		return g, nil
	}
	src, err := a.Cache.Ensure(ctx, a.Media, as.StorageKey, as.SHA256, as.Size, nil)
	if err != nil {
		return evidence.GridOf{}, err
	}
	eng := &Engine{Tools: a.Tools, Scratch: soundScratch(a)}
	g, err := eng.MusicGrid(ctx, src)
	if err != nil {
		return evidence.GridOf{}, err
	}
	og := GridEvidence(g)
	if err := evidence.SaveGrid(ctx, a.DB, a.WorkspaceID(), as.ID, as.SHA256, og); err != nil {
		return evidence.GridOf{}, err
	}
	return og, nil
}

// GridEvidence is a grid as it is stored.
func GridEvidence(g Grid) evidence.GridOf {
	og := evidence.GridOf{Tempo: g.Tempo, Confidence: g.Confidence, Beats: g.Beats, Onsets: g.Onsets, Meter: g.Meter, Downbeats: g.Downbeats,
		Bars: g.Bars, PhraseBars: g.PhraseBars, Phrases: g.Phrases, PhraseConf: g.PhraseConf, Seconds: g.Seconds}
	for _, s := range g.Tempos {
		og.Tempos = append(og.Tempos, evidence.TempoSpan(s))
	}
	return og
}

// gridWords says what was found in the music: its tempo (or how it changes),
// its bars and its phrases.
func gridWords(g evidence.GridOf) string {
	if g.Onsets || g.Tempo == 0 {
		return "No steady beat: it can end on one of its strong notes."
	}
	var parts []string
	switch {
	case len(g.Tempos) > 1 || len(g.Tempos) == 1 && g.Tempos[0].Start != g.Tempos[0].End:
		var steps []string
		for _, s := range g.Tempos {
			if s.Start == s.End {
				steps = append(steps, fmt.Sprintf("%.0f", s.Start))
			} else {
				steps = append(steps, fmt.Sprintf("%.0f to %.0f", s.Start, s.End))
			}
		}
		parts = append(parts, "Its tempo changes: "+strings.Join(steps, ", then ")+" beats a minute")
	default:
		parts = append(parts, fmt.Sprintf("About %.0f beats a minute", g.Tempo))
	}
	if g.Meter > 0 {
		parts = append(parts, fmt.Sprintf("%d beats to the bar", g.Meter))
	}
	if len(g.Phrases) > 0 {
		parts = append(parts, fmt.Sprintf("phrases of %d bars", g.PhraseBars))
	}
	return strings.Join(parts, ", ") + "."
}

func musicAdd(ctx context.Context, a *app.App, in *MusicAddIn) (*MusicOut, error) {
	c, err := loadCut(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	as, err := findAudioAsset(ctx, a, c.rev.ProjectID, in.Asset)
	if err != nil {
		return nil, err
	}
	if !as.Media.HasAudio {
		return nil, apperr.New(apperr.InvalidInput, "%s has no sound.", as.DisplayTitle())
	}
	if as.State != models.AssetAvailable {
		return nil, apperr.New(apperr.InvalidInput, "%s is still being stored. Try again in a moment.", as.DisplayTitle())
	}
	if !contains(as.ProjectIDs, c.rev.ProjectID) {
		if err := a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$addToSet": bson.M{"projectIds": c.rev.ProjectID}}); err != nil {
			return nil, err
		}
		a.Audit(ctx, "asset.link", "asset", as.ID, c.rev.ProjectID, map[string]any{"for": "music"})
	}
	grid, err := musicGrid(ctx, a, as)
	if err != nil {
		return nil, err
	}
	fit := in.Fit
	if fit == "" {
		fit = editspec.TrackFitAuto
		if in.From != nil || in.To != nil || in.At != nil {
			fit = editspec.TrackFitManual
		}
	}
	t := editspec.AudioTrack{ID: in.ID, AssetID: as.ID, Role: editspec.RoleMusic, In: 0, Out: round3(as.Media.Duration), Fit: fit, Loop: in.Loop}
	// "Trim silence at the ends" (D152): from its first sound to its last.
	if f := as.Fixes; f != nil && f.Trim != nil && slices.Contains(f.Ticked, models.FixEnds) && f.Trim.End > f.Trim.Start {
		t.In, t.Out = round3(f.Trim.Start), round3(math.Min(f.Trim.End, as.Media.Duration))
	}
	switch {
	case fit == editspec.TrackFitManual && in.Ending != "":
		return nil, apperr.New(apperr.InvalidInput, "Only music fitted to the video ends by itself: leave out where it ends, or fit it to the video.")
	case fit == editspec.TrackFitManual:
		t.Fit = ""
	case in.Ending == "" || in.Ending == editspec.EndingPhrase:
		t.Ending = editspec.EndingPhrase // ends where the music starts its next phrase (D120)
	case in.Ending == editspec.EndingBeat:
		t.Ending = editspec.EndingBeat
	default:
		return nil, apperr.New(apperr.InvalidInput, "Music ends at a phrase or on a beat, not %q.", in.Ending)
	}
	if in.From != nil {
		t.In = round3(*in.From)
	}
	if in.To != nil {
		t.Out = round3(math.Min(*in.To, as.Media.Duration))
	}
	if in.At != nil {
		t.At = round3(*in.At)
	}
	if t.Out <= t.In {
		return nil, apperr.New(apperr.InvalidInput, "The music has to stop after it starts (it starts at %s of the track and stops at %s).", editspec.Clock(t.In), editspec.Clock(t.Out))
	}
	g := DefaultMusicGain
	if in.GainDB != nil {
		g = *in.GainDB
	}
	t.GainDB = &g
	duck := true
	if in.Duck != nil {
		duck = *in.Duck
	}
	t.Duck = &duck
	if in.DuckDB != nil {
		d := -math.Abs(*in.DuckDB)
		t.DuckDB = &d
	}
	if fit == editspec.TrackFitManual && t.Loop {
		return nil, errLoopNeedsFit()
	}
	if t.ID == "" {
		t.ID = freeTrackID(c.spec, "music")
	}
	out := gridOut(t, grid)
	spec := editspec.Clone(c.spec)
	spec.Audio = append(spec.Audio, t)
	out.Placement = placementWords(spec, t, withBeats{Facts: c.facts, id: as.ID, grid: ops.MusicGridOf(grid)}, len(grid.Beats) > 0)
	if as.Rights == nil || as.Rights.License == "" {
		out.Rights = fmt.Sprintf("Nobody has said where %s comes from yet: To-dos asks once it is in the cut (platforms can mute videos with unlicensed music).", quote(as.DisplayTitle()))
	}
	res, err := planCheck(ctx, a, c.rev, fmt.Sprintf("Music: %s", as.DisplayTitle()), []editspec.Op{{Op: "addAudio", Track: &t}})
	if err != nil {
		return nil, err
	}
	out.Result = res
	out.Summary = fmt.Sprintf("Music %s: %s", quote(as.DisplayTitle()), out.Placement)
	if in.Apply {
		out.Applied = applyResult(ctx, a, res)
		out.Summary += appliedNote(out.Applied)
	} else {
		out.Summary += " Apply the change to put it in the cut."
	}
	return out, nil
}

func round3(v float64) float64 { return math.Round(v*1000) / 1000 }

// errLoopNeedsFit is the refusal to loop music placed by hand.
func errLoopNeedsFit() *apperr.Error {
	return apperr.New(apperr.InvalidInput, "Music loops only when it's fitted to the video. Fit it to the video, or let it play once.").WithCommand("metarush sound music set <track> --fit auto --loop")
}

// withBeats adds a track's analysis to a cut's facts (just found, or for a
// track newly fitted to the video) so the placement words use it: its grid
// (tracks with an ending) or its beats-1 beats (tracks without).
type withBeats struct {
	editspec.Facts
	id    string
	times []float64
	grid  *editspec.MusicGrid
}

func (f withBeats) Beats(id string) ([]float64, bool) {
	if id == f.id && f.times != nil {
		return f.times, true
	}
	if mf, ok := f.Facts.(editspec.MusicFacts); ok {
		return mf.Beats(id)
	}
	return nil, false
}

func (f withBeats) MusicGrid(id string) (*editspec.MusicGrid, bool) {
	if id == f.id && f.grid != nil {
		return f.grid, true
	}
	if gf, ok := f.Facts.(editspec.MusicGridFacts); ok {
		return gf.MusicGrid(id)
	}
	return nil, false
}

// gridOut is a music change's result with what was found in the music.
func gridOut(t editspec.AudioTrack, g evidence.GridOf) *MusicOut {
	return &MusicOut{Track: t, Tempo: g.Tempo, Beats: len(g.Beats), Tempos: g.Tempos, Meter: g.Meter, Phrases: len(g.Phrases), Music: gridWords(g)}
}

func freeTrackID(s *editspec.Spec, base string) string {
	used := map[string]bool{"captions": true, "voiceover": true, "mix": true}
	for _, c := range s.Clips {
		used[c.ID] = true
	}
	for _, t := range s.Audio {
		used[t.ID] = true
	}
	for _, o := range s.Overlays {
		used[o.ID] = true
	}
	if s.Voiceover != nil {
		for _, b := range s.Voiceover.Beats {
			used[b.ID] = true
		}
	}
	if !used[base] {
		return base
	}
	for n := 2; ; n++ {
		if id := fmt.Sprintf("%s-%d", base, n); !used[id] {
			return id
		}
	}
}

// endingWords says what a fitted track ends on.
func endingWords(fi editspec.FitInfo) string {
	switch fi.EndsOn {
	case editspec.EndsOnPhrase:
		return "where the music starts its next phrase"
	case editspec.EndsOnBar:
		return "at the end of a bar"
	}
	return "on a beat"
}

// placementWords says where a track plays in the cut.
func placementWords(s *editspec.Spec, t editspec.AudioTrack, facts editspec.Facts, _ bool) string {
	fi, ok := editspec.MusicFit(s, t, facts)
	if !ok {
		return "it would not be heard (it starts after the video ends)."
	}
	clock := editspec.Clock
	if t.Fit != editspec.TrackFitAuto {
		return fmt.Sprintf("plays %s–%s of the track from %s in the video.", clock(t.In), clock(t.Out), clock(fi.From))
	}
	switch {
	case fi.Repeats > 1:
		s := fmt.Sprintf("plays %d times in a row to fill the video, from %s to %s", fi.Repeats, clock(fi.From), clock(fi.To))
		if fi.EndsOnBeat {
			s += ", ending " + endingWords(fi)
		}
		return s + "."
	case fi.EndsEarly:
		return fmt.Sprintf("starts at %s and fades out at %s, before the video ends (the track is shorter; loop it to fill the video).", clock(fi.From), clock(fi.To))
	case fi.EndsOnBeat:
		return fmt.Sprintf("starts with the video and ends with it at %s, %s, fading out smoothly.", clock(fi.To), endingWords(fi))
	}
	return fmt.Sprintf("starts with the video and fades out with it at %s.", clock(fi.To))
}

func musicSet(ctx context.Context, a *app.App, in *MusicSetIn) (*MusicOut, error) {
	return musicSetWith(ctx, a, in, true)
}

// musicSetWith changes a track; analyse makes the whole-track analysis of
// music fitted with an ending the first time (D120; a card's preview leaves
// that to the change itself).
func musicSetWith(ctx context.Context, a *app.App, in *MusicSetIn, analyse bool) (*MusicOut, error) {
	c, err := loadCut(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	idx := -1
	for i, t := range c.spec.Audio {
		if t.ID == in.Track {
			idx = i
		}
	}
	if idx < 0 {
		for i, t := range c.spec.Audio {
			if as := c.asset(t.AssetID); as != nil && strings.EqualFold(as.DisplayTitle(), in.Track) {
				idx = i
			}
		}
	}
	if idx < 0 {
		return nil, apperr.New(apperr.NotFound, "There is no music or sound called “%s” in this cut.", in.Track)
	}
	t := c.spec.Audio[idx]
	name := t.ID
	if as := c.asset(t.AssetID); as != nil {
		name = as.DisplayTitle()
	}
	if in.Remove {
		res, err := planCheck(ctx, a, c.rev, "Take out the music "+name, []editspec.Op{{Op: "removeAudio", ID: t.ID}})
		if err != nil {
			return nil, err
		}
		out := &MusicOut{Track: t, Result: res, Placement: "taken out", Summary: fmt.Sprintf("Take %s out of the cut.", quote(name))}
		if in.Apply {
			out.Applied = applyResult(ctx, a, res)
			out.Summary += appliedNote(out.Applied)
		}
		return out, nil
	}
	set := map[string]any{}
	var what []string
	fit := in.Fit
	if fit == "" && (in.From != nil || in.To != nil || in.At != nil) {
		fit = editspec.TrackFitManual // a hand-made timing
	}
	switch fit {
	case editspec.TrackFitAuto:
		set["fit"] = editspec.TrackFitAuto
		t.Fit = editspec.TrackFitAuto
		what = append(what, "fit it to the video")
		if t.Ending == "" && in.Ending == "" {
			// Fitted now: it ends where the music starts its next phrase (D120).
			set["ending"], t.Ending = editspec.EndingPhrase, editspec.EndingPhrase
		}
	case editspec.TrackFitManual:
		set["fit"] = nil
		t.Fit = ""
		if c.spec.Audio[idx].Fit == editspec.TrackFitAuto {
			what = append(what, "time it by hand")
		}
		if t.Ending != "" {
			set["ending"], t.Ending = nil, ""
		}
	}
	switch in.Ending {
	case "":
	case editspec.EndingPhrase, editspec.EndingBeat:
		if t.Fit != editspec.TrackFitAuto {
			return nil, apperr.New(apperr.InvalidInput, "Only music fitted to the video ends by itself: fit it to the video first.").WithCommand("metarush sound music set <track> --fit auto")
		}
		set["ending"], t.Ending = in.Ending, in.Ending
		what = append(what, map[string]string{editspec.EndingPhrase: "end it where the music starts its next phrase", editspec.EndingBeat: "end it on a beat"}[in.Ending])
	default:
		return nil, apperr.New(apperr.InvalidInput, "Music ends at a phrase or on a beat, not %q.", in.Ending)
	}
	num := func(v *float64, key, words string, dst *float64) {
		if v != nil {
			x := round3(math.Max(0, *v))
			set[key], *dst = x, x
			what = append(what, fmt.Sprintf(words, editspec.Clock(x)))
		}
	}
	num(in.From, "in", "start at %s of the track", &t.In)
	num(in.To, "out", "stop at %s of the track", &t.Out)
	num(in.At, "at", "start at %s in the video", &t.At)
	if t.Out <= t.In {
		return nil, apperr.New(apperr.InvalidInput, "The music has to stop after it starts.")
	}
	if in.GainDB != nil {
		g := math.Round(math.Max(-60, math.Min(12, *in.GainDB))*10) / 10
		set["gainDb"], t.GainDB = g, &g
		what = append(what, fmt.Sprintf("set its level to %s dB", strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", g), "0"), ".")))
	}
	switch in.Duck {
	case "on":
		on := true
		set["duck"], t.Duck = true, &on
		what = append(what, "make it quieter while someone talks")
	case "off":
		off := false
		set["duck"], t.Duck = false, &off
		what = append(what, "keep it at its level while people talk")
	case "auto":
		set["duck"], t.Duck = nil, nil
		what = append(what, "let the mix decide when it gets quieter")
	}
	if in.DuckDB != nil {
		d := -math.Abs(*in.DuckDB)
		set["duckDb"], t.DuckDB = d, &d
		what = append(what, fmt.Sprintf("dip it by %.0f dB while someone talks", math.Abs(d)))
	}
	if in.Loop != nil {
		set["loop"], t.Loop = *in.Loop, *in.Loop
		if !*in.Loop {
			set["loop"] = nil
		}
		what = append(what, map[bool]string{true: "loop it to fill the video", false: "play it once"}[*in.Loop])
	}
	if t.Fit != editspec.TrackFitAuto && t.Loop {
		if in.Loop != nil && *in.Loop {
			return nil, errLoopNeedsFit()
		}
		set["loop"], t.Loop = nil, false // placed by hand: it plays once
		what = append(what, "play it once")
	}
	if len(set) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "Nothing to change for this music.").
			WithHint("Choose what to change on the Sound tab: when it plays, how loud it is, or whether it dips under voices.").WithCommand("metarush sound music set --help")
	}
	spec := editspec.Clone(c.spec)
	spec.Audio[idx] = t
	var out *MusicOut
	g, haveGrid := evidence.GridOf{}, false
	if as := c.asset(t.AssetID); as != nil && t.Ending != "" {
		if analyse {
			// Fitted with an ending: the whole track's analysis (made now the first time).
			var gerr error
			if g, gerr = musicGrid(ctx, a, as); gerr != nil {
				return nil, gerr
			}
			haveGrid = true
		} else {
			// A card's preview makes nothing: the analysis only if it is there already.
			g, haveGrid = evidence.LoadGrids(ctx, a.DB, a.WorkspaceID(), []string{as.ID})[as.ID]
		}
	}
	if haveGrid {
		out = gridOut(t, g)
		out.Placement = placementWords(spec, t, withBeats{Facts: c.facts, id: t.AssetID, grid: ops.MusicGridOf(g)}, len(g.Beats) > 0)
	} else {
		// Music fitted before endings existed keeps the beats it was fitted with.
		bt := evidence.LoadBeats(ctx, a.DB, a.WorkspaceID(), []string{t.AssetID})[t.AssetID]
		out = &MusicOut{Track: t, Tempo: bt.Tempo, Beats: len(bt.Times), Placement: placementWords(spec, t, withBeats{Facts: c.facts, id: t.AssetID, times: bt.Times}, len(bt.Times) > 0)}
	}
	res, err := planCheck(ctx, a, c.rev, "Music: "+strings.Join(what, ", "), []editspec.Op{{Op: "updateAudio", ID: t.ID, Set: jsonSet(set)}})
	if err != nil {
		return nil, err
	}
	out.Result = res
	out.Summary = upper(strings.Join(what, ", ")) + ": it " + out.Placement
	if in.Apply {
		out.Applied = applyResult(ctx, a, res)
		out.Summary += appliedNote(out.Applied)
	}
	return out, nil
}

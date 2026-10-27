package sound

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/components/choice"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// CompareIn asks how much clearer the voices of a cut are.
type CompareIn struct {
	RevisionID string `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Target     string `json:"target,omitempty" jsonschema:"maxLength=200,description=Only this person (or narration); default everyone with a clearer voice."`
}

// CompareOut is each clearer voice before and after.
type CompareOut struct {
	Scores  []PersonScore `json:"scores"`
	Summary string        `json:"summary"`
}

func soundCompare(ctx context.Context, a *app.App, in *CompareIn) (*CompareOut, error) {
	c, err := loadCut(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	var q *Quality
	quality := func() *Quality {
		if q == nil {
			comp := &Components{Home: a.Cfg.Home(), Tools: a.Tools}
			comp.Status()
			q = newQuality(a, comp)
		}
		return q
	}
	eng := &Engine{Tools: a.Tools, Scratch: soundScratch(a)}
	var views []CopyView
	m := c.spec.Mix
	if m != nil {
		for _, e := range m.Enhanced {
			v := CopyView{AssetID: e.AssetID, Variant: e.Variant, From: e.Start}
			if r := copyRecordByVariant(ctx, a, e.Variant); r != nil && len(r.Scores) > 0 && hasClarity(r.Scores) {
				v.Scores = r.Scores
			} else {
				scores, err := measureCopy(ctx, a, eng, quality(), c, e.AssetID, e.Variant, e.Start, false)
				if err != nil {
					return nil, err
				}
				v.Scores = scores
			}
			views = append(views, v)
		}
	}
	var narration []CopyView
	if v := c.spec.Voiceover; v != nil {
		seen := map[string]bool{}
		for _, b := range v.Beats {
			t := b.Take
			if t == nil || t.Processed == nil || seen[t.Processed.AssetID] {
				continue
			}
			if _, _, ok := ParsePreset(t.Processed.Preset); !ok {
				continue
			}
			seen[t.Processed.AssetID] = true
			cv := CopyView{AssetID: t.AssetID, Variant: t.Processed.AssetID}
			if r := copyRecordByVariant(ctx, a, t.Processed.AssetID); r != nil && hasClarity(r.Scores) {
				cv.Scores = r.Scores
			} else {
				scores, err := measureCopy(ctx, a, eng, quality(), c, t.AssetID, t.Processed.AssetID, 0, true)
				if err != nil {
					return nil, err
				}
				cv.Scores = scores
			}
			narration = append(narration, cv)
		}
	}
	out := &CompareOut{Scores: []PersonScore{}}
	want := strings.ToLower(strings.TrimSpace(in.Target))
	for _, s := range append(mergeScores(views), mergeScores(narration)...) {
		if want != "" && strings.ToLower(s.Speaker) != want && !(want == "narration" && s.Speaker == "Narration") {
			continue
		}
		out.Scores = append(out.Scores, s)
	}
	if len(out.Scores) == 0 {
		out.Summary = "No voice in this cut has a clearer version yet. Make one on the Sound tab."
		if want != "" {
			out.Summary = fmt.Sprintf("%s has no clearer version in this cut.", upper(in.Target))
		}
		return out, nil
	}
	var lines []string
	for _, s := range out.Scores {
		name := s.Speaker
		if name == "" {
			name = "The voice"
		}
		lines = append(lines, name+": "+strings.ToLower(s.Summary[:1])+s.Summary[1:])
	}
	out.Summary = strings.Join(lines, " ")
	return out, nil
}

func hasClarity(ss []PersonScore) bool {
	for _, s := range ss {
		if s.Before.Clarity != nil && s.After.Clarity != nil {
			return true
		}
	}
	return false
}

// measureCopy scores a copy made earlier (or on a computer without the score).
func measureCopy(ctx context.Context, a *app.App, eng *Engine, q *Quality, c *cut, sourceID, variantID string, start float64, narration bool) ([]PersonScore, error) {
	var src, cp models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), sourceID, &src); err != nil {
		return nil, apperr.New(apperr.NotFound, "A recording this cut uses is missing.").WithDetails(map[string]any{"assetId": sourceID})
	}
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), variantID, &cp); err != nil {
		return nil, apperr.New(apperr.NotFound, "A clearer voice this cut uses is missing. Make it again on the Sound tab.").WithDetails(map[string]any{"assetId": variantID})
	}
	sp, err := a.Cache.Ensure(ctx, a.Media, src.StorageKey, src.SHA256, src.Size, nil)
	if err != nil {
		return nil, err
	}
	vp, err := a.Cache.Ensure(ctx, a.Media, cp.StorageKey, cp.SHA256, cp.Size, nil)
	if err != nil {
		return nil, err
	}
	end := start + cp.Media.Duration
	dry, err := eng.Extract(ctx, sp, start, end, WetRate, 1)
	if err != nil {
		return nil, err
	}
	wet, err := eng.Extract(ctx, vp, 0, 0, WetRate, 1)
	if err != nil {
		return nil, err
	}
	m := c.spec.Mix
	segs := c.transcript(sourceID)
	if narration || len(segs) == 0 {
		spans := [][2]float64{{start, end}}
		name := ""
		if narration {
			name = "Narration"
		}
		return []PersonScore{q.ComparePerson(name, dry.Data, wet.Data, start, spans, nil, "", 0, 0)}, nil
	}
	by := map[string][][2]float64{}
	whole := map[string][][2]float64{} // each person's stretches, pauses included
	for _, sp := range editspec.SpeakerSpans(segs, start, end) {
		whole[sp.Speaker] = append(whole[sp.Speaker], [2]float64{sp.From, sp.To})
	}
	var order []string
	for _, sg := range segs {
		a0, b0 := math.Max(sg.From, start), math.Min(sg.To, end)
		if b0 <= a0 {
			continue
		}
		who := strings.TrimSpace(sg.Speaker)
		v := m.VoiceFor(who)
		active := v != nil && v.Enhance.Active()
		if !active {
			if rv := m.RecordingVoice(sourceID); rv == nil || !rv.Enhance.Active() || (v != nil && v.Enhance != nil) {
				continue
			}
		}
		if _, ok := by[who]; !ok {
			order = append(order, who)
		}
		by[who] = append(by[who], [2]float64{a0, b0})
	}
	var out []PersonScore
	for _, who := range order {
		mode, strength := "", 0.0
		if v := m.VoiceFor(who); v != nil && v.Enhance != nil {
			mode, strength = v.Enhance.Mode, v.Enhance.StrengthOf()
		} else if rv := m.RecordingVoice(sourceID); rv != nil && rv.Enhance != nil {
			mode, strength = rv.Enhance.Mode, rv.Enhance.StrengthOf()
		}
		out = append(out, q.ComparePerson(who, dry.Data, wet.Data, start, by[who], whole[who], mode, strength, 0))
	}
	return out, nil
}

// SetupIn sets up what clearer voices need on this computer.
type SetupIn struct {
	Components []string `json:"components,omitempty" jsonschema:"description=clean (the voice cleaner and the clarity score\\, about 30 MB; the default) and/or studio (about 1.8 GB: Python packages and a voice model)."`
	// Background (the dashboard) starts the work and answers at once; it shows
	// in the activity bar and job details.
	Background bool `json:"background,omitempty"`
}

// SetupOut is the state of each component.
type SetupOut struct {
	Components []ComponentStatus `json:"components"`
	Started    bool              `json:"started,omitempty" jsonschema:"description=background: the setup is running (see Activity)."`
	Summary    string            `json:"summary"`
	JobID      string            `json:"jobId,omitempty"`
}

// SetJobID and JobResult make setup tracked work.
func (o *SetupOut) SetJobID(id string) { o.JobID = id }

// JobResult is what the job records.
func (o *SetupOut) JobResult() map[string]any {
	return map[string]any{"summary": o.Summary, "lines": 0}
}

func soundSetup(ctx context.Context, a *app.App, in *SetupIn) (*SetupOut, error) {
	want := in.Components
	if len(want) == 0 {
		want = []string{ComponentClean}
	}
	for _, w := range want {
		if w != ComponentClean && w != ComponentStudio {
			return nil, apperr.New(apperr.InvalidInput, "There's nothing called “%s” to set up. Choose clean or studio.", w)
		}
	}
	// Studio brings the voice cleaner and the clarity score too: its results are
	// scored with them.
	if pre := (&Components{Home: a.Cfg.Home(), Tools: a.Tools}); contains(want, ComponentStudio) && !contains(want, ComponentClean) && !pre.CleanInstalled() && pre.CleanAvailable() {
		want = append([]string{ComponentClean}, want...)
	}
	// A person asked for it: the choice is theirs (D325), so what was left
	// out or removed is set up now.
	chosen := map[string]bool{}
	for _, w := range want {
		chosen[map[string]string{ComponentClean: "clean-voice", ComponentStudio: "studio-voice"}[w]] = true
	}
	_ = choice.Set(a.Cfg.Home(), chosen, a.Actor.UserName)
	run := func(ctx context.Context) (*SetupOut, error) {
		comp := &Components{Home: a.Cfg.Home(), Tools: a.Tools}
		for _, w := range want {
			var err error
			switch w {
			case ComponentClean:
				err = comp.InstallClean(ctx, func(d, t int, msg string) error { return stepTracked(ctx, d, t, msg) })
			case ComponentStudio:
				_, err = comp.InstallStudio(ctx, func(d, t int, msg string) error { return stepTracked(ctx, d, t, msg) })
			}
			if err != nil {
				return nil, plainFailure(err, "Setting up clearer voices didn't work")
			}
			a.Audit(ctx, "sound.setup", "installation", a.Cfg.Installation.ID, "", map[string]any{"component": w})
		}
		out := &SetupOut{Components: comp.Status()}
		var ready []string
		for _, st := range out.Components {
			if st.Installed {
				ready = append(ready, strings.ToLower(strings.SplitN(st.Title, " ", 2)[0]))
			}
		}
		out.Summary = "Clearer voices are set up on this computer: " + strings.Join(ready, " and ") + "."
		return out, nil
	}
	if in.Background {
		go func() {
			bg := context.WithoutCancel(ctx)
			_, _ = trackSetup(bg, a, run)
		}()
		comp := &Components{Home: a.Cfg.Home(), Tools: a.Tools}
		return &SetupOut{Components: comp.Status(), Started: true, Summary: "Setting it up now; Activity shows how far along it is."}, nil
	}
	return trackSetup(ctx, a, run)
}

func trackSetup(ctx context.Context, a *app.App, run func(context.Context) (*SetupOut, error)) (*SetupOut, error) {
	return trackSetupWork(ctx, a, run)
}

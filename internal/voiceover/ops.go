package voiceover

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

func opsCall[O any](ctx context.Context, a *app.App, name string, in any) (*O, error) {
	return ops.CallTyped[O](ctx, a, name, in, ops.Caller{Human: true})
}

// RevisionIn selects a revision.
type RevisionIn struct {
	RevisionID string `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
}

// ParseIn turns a plain-text script into narration lines.
type ParseIn struct {
	RevisionID string `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Text       string `json:"text" jsonschema:"minLength=1,maxLength=200000,description=Paragraphs become lines; [@clip+0.5] [0:12.4] [after] (notes) place them."`
	Append     bool   `json:"append,omitempty" jsonschema:"description=Add the lines after the existing script instead of replacing it."`
	// Clip makes the lines the narration of one clip: the first starts with it
	// (0.3 s in) unless it has its own start point, the rest follow it; they go
	// into the script where the clip plays, replacing lines that start over it,
	// and every other line stays.
	Clip string `json:"clip,omitempty" jsonschema:"maxLength=64,description=Clip ID or label: the lines are the narration of this clip only."`
	// Spread places lines that have no start point of their own over the shots,
	// one per shot in order (how earlier versions read a script). By default a
	// script is used as-is: no timing is assumed, each line follows the one
	// before, and a recording sets the timing (D86).
	Spread bool `json:"spread,omitempty" jsonschema:"description=Spread lines without a start point over the shots in order (default: as-is\\, each line follows the one before until a recording sets the timing)."`
	// FromRecording says the text is what a recording says (no script was
	// given), so it is not kept as the original script.
	FromRecording bool `json:"fromRecording,omitempty" jsonschema:"description=The text is what a recording says; it is not kept as the original script."`
}

// ParseOut is the parsed script and the plan that sets it.
type ParseOut struct {
	Beats   []editspec.Beat  `json:"beats"`
	Issues  []editspec.Issue `json:"issues"`
	Result  *PlanResult      `json:"result,omitempty"`
	Summary string           `json:"summary"`
}

// FitIn checks the lines against their time and (with strategy) builds the fix.
type FitIn struct {
	RevisionID string   `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Strategy   string   `json:"strategy,omitempty" jsonschema:"enum=preferred,enum=extend-shot,enum=speed-up,enum=start-earlier,enum=move-next,description=Build an edit plan with this fix for every line that runs over (preferred = what the fit policy picks)."`
	Beats      []string `json:"beats,omitempty"`
}

// FitOut is the fit report (and the fix plan).
type FitOut struct {
	Policy  string             `json:"policy"`
	Lines   []editspec.BeatFit `json:"lines"`
	Over    int                `json:"over"`
	Rewrite []editspec.BeatFit `json:"rewrite,omitempty"` // lines whose fix is a shorter text (Claude or you rewrite them)
	Result  *PlanResult        `json:"result,omitempty"`
	Summary string             `json:"summary"`
}

// TakesIn lists takes.
type TakesIn struct {
	RevisionID string `json:"revisionId,omitempty" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	ProjectID  string `json:"projectId,omitempty" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
	Beat       string `json:"beat,omitempty" jsonschema:"description=Line ID or number."`
}

// TakesOut lists takes, newest first.
type TakesOut struct {
	Takes []TakeView `json:"takes"`
}

// ChooseIn puts a take (or the original/cleaned-up version) on a line.
type ChooseIn struct {
	RevisionID string `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Beat       string `json:"beat" jsonschema:"description=Line ID or number."`
	TakeID     string `json:"takeId,omitempty" jsonschema:"pattern=^tk_[0-9a-z]{26}$"`
	Version    string `json:"version,omitempty" jsonschema:"enum=original,enum=processed,description=A/B: play the original or the cleaned-up version."`
}

// AddTakeIn registers a range of an audio asset as a take for a line.
type AddTakeIn struct {
	ProjectID string  `json:"projectId" jsonschema:"pattern=^prj_[0-9a-z]{26}$"`
	BeatID    string  `json:"beatId" jsonschema:"maxLength=64"`
	Text      string  `json:"text,omitempty" jsonschema:"maxLength=2000"`
	AssetID   string  `json:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	In        float64 `json:"in" jsonschema:"minimum=0"`
	Out       float64 `json:"out" jsonschema:"minimum=0"`
	Source    string  `json:"source" jsonschema:"enum=recorded,enum=imported"`
}

func init() {
	ops.Def("voiceover.show", "Show a cut's narration: each line, where it plays, whether it fits, its audio and takes", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RevisionIn) (*ShowOut, error) {
			return show(ctx, a, in.RevisionID)
		})
	ops.Def("voiceover.parse", "Turn a plain-text script into narration lines (paragraphs; [@clip+0.5], [0:12.4], [after], (notes)) and check the plan that sets it", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ParseIn) (*ParseOut, error) { return parse(ctx, a, in) })
	ops.Def("voiceover.fit", "Check whether every narration line fits before the next one, and build the fix (extend the shot, speed up AI speech ≤ 8%, start earlier, move the next line)", ops.Flags{},
		func(ctx context.Context, a *app.App, in *FitIn) (*FitOut, error) { return fit(ctx, a, in) })
	ops.Def("voiceover.scratch", "Make free scratch narration with a system voice (macOS say or espeak-ng) for timing review, and check the plan that adds it", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *ScratchIn) (*GenerateOut, error) {
			return track(ctx, a, ActionScratch, in.RevisionID, "", func(ctx context.Context) (*GenerateOut, error) {
				out, err := scratch(ctx, a, in)
				if err == nil && out != nil && in.Apply {
					out.apply(ctx, a)
				}
				return out, err
			})
		})
	ops.Def("voiceover.estimate", "Price ElevenLabs narration before generating: characters and credits per line, free cached lines, budget and plan allowance", ops.Flags{},
		func(ctx context.Context, a *app.App, in *TTSIn) (*EstimateOut, error) { return estimate(ctx, a, in) })
	ops.Def("voiceover.tts", "Generate ElevenLabs narration with word timing (unchanged lines are reused for free; respects the project budget), and check the plan that adds it", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *TTSIn) (*GenerateOut, error) {
			return track(ctx, a, ActionTTS, in.RevisionID, "", func(ctx context.Context) (*GenerateOut, error) {
				out, err := generateTTS(ctx, a, in)
				if err == nil && out != nil && in.Apply {
					out.apply(ctx, a)
				}
				return out, err
			})
		})
	ops.Def("voiceover.voices", "List voices: ElevenLabs (your account or the public library) or the free local scratch voices", ops.Flags{},
		func(ctx context.Context, a *app.App, in *VoicesIn) (*VoicesOut, error) { return listVoices(ctx, a, in) })
	ops.Def("voiceover.models", "List the ElevenLabs models this account can use, with languages, text limits and cost per character", ops.Flags{},
		func(ctx context.Context, a *app.App, _ *ops.EmptyIn) (*ModelsOut, error) { return listModels(ctx, a) })
	ops.Def("voiceover.preview", "Download a voice's sample to a local file (the dashboard streams samples instead)", ops.Flags{LocalOnly: true},
		func(ctx context.Context, a *app.App, in *PreviewIn) (*PreviewOut, error) { return preview(ctx, a, in) })
	ops.Def("voiceover.takes", "List the audio takes made or found for narration lines", ops.Flags{},
		func(ctx context.Context, a *app.App, in *TakesIn) (*TakesOut, error) { return takes(ctx, a, in) })
	ops.Def("voiceover.choose", "Put a take — or the original or cleaned-up version of the current one — on a narration line (checks the plan)", ops.Flags{},
		func(ctx context.Context, a *app.App, in *ChooseIn) (*PlanResult, error) { return choose(ctx, a, in) })
	ops.Def("voiceover.align", "Match a transcribed recording to the script and place it (split at each line, or as one piece); measures the narrator's pace", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *AlignIn) (*AlignOut, error) {
			return track(ctx, a, ActionAlign, in.RevisionID, "", func(ctx context.Context) (*AlignOut, error) {
				out, err := align(ctx, a, in)
				if err == nil && out != nil && in.Apply {
					out.apply(ctx, a)
				}
				return out, err
			})
		})
	ops.Def("voiceover.addTake", "Register part of an audio asset as a take for a narration line (used by the recorder)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *AddTakeIn) (*TakeView, error) { return addTake(ctx, a, in) })
	ops.Def("voiceover.place", "Put a live take (recorded while the video played) where it was spoken: each line gets its piece of the recording, cut only in pauses, and a start point where it was said; or put an earlier take back, each line where it was spoken or at its planned spot", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *PlaceIn) (*PlaceOut, error) {
			return track(ctx, a, ActionPlace, in.RevisionID, "", func(ctx context.Context) (*PlaceOut, error) {
				out, err := place(ctx, a, in)
				if err == nil && out != nil && in.Apply && out.Result != nil {
					out.apply(ctx, a)
				}
				return out, err
			})
		})
	ops.Def("voiceover.fitted", "Use Claude's version of the script fitted to the video (the original is kept), or choose line by line which version to keep; shows both side by side and checks the plan", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *FittedIn) (*FittedOut, error) { return fitted(ctx, a, in) })
	ops.Def("voiceover.use", "Switch the narration between your original script and Claude's fitted version (the other one is kept with its sound and places)", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *UseIn) (*UseOut, error) { return use(ctx, a, in) })
	ops.Def("voiceover.cleanup", "Clean up recorded narration with a preset (noise, rumble, harsh s, level, leading silence) as a second version the line can switch to", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *CleanupIn) (*CleanupOut, error) {
			return track(ctx, a, ActionCleanup, in.RevisionID, "", func(ctx context.Context) (*CleanupOut, error) {
				out, err := cleanup(ctx, a, in)
				if err == nil && out != nil && in.Apply {
					out.apply(ctx, a)
				}
				return out, err
			})
		})
	ops.Def("voiceover.clone", "Create an ElevenLabs voice from recordings of your own voice (anyone else's needs their recorded consent) — people only", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *CloneIn) (*CloneOut, error) {
			return track(ctx, a, ActionClone, "", in.ProjectID, func(ctx context.Context) (*CloneOut, error) { return clone(ctx, a, in) })
		})
	ops.Def("mix.show", "Show a cut's sound mix in plain words: levels of the clips' sound, narration, music and effects, what is muted, ducking and loudness", ops.Flags{},
		func(ctx context.Context, a *app.App, in *RevisionIn) (*MixOut, error) {
			return mixShow(ctx, a, in.RevisionID)
		})
}

func parse(ctx context.Context, a *app.App, in *ParseIn) (*ParseOut, error) {
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	text := in.Text
	clipIdx := -1
	if in.Clip != "" {
		for i, c := range rc.spec.Clips {
			if c.ID == in.Clip || (c.Label != "" && strings.EqualFold(c.Label, in.Clip)) {
				clipIdx = i
			}
		}
		if clipIdx < 0 {
			return nil, apperr.New(apperr.NotFound, "There is no clip “%s” in this cut.", in.Clip)
		}
		text = startWithClip(text, rc.spec.Clips[clipIdx].ID)
	}
	beats, issues := editspec.ParseScriptAsIs(text, rc.spec)
	if in.Spread {
		beats, issues = editspec.ParseScript(text, rc.spec)
	}
	out := &ParseOut{Beats: beats, Issues: issues}
	if out.Issues == nil {
		out.Issues = []editspec.Issue{}
	}
	if len(beats) == 0 {
		out.Summary = "The script has no lines."
		return out, nil
	}
	if editspec.HasErrors(issues) {
		out.Summary = "Some start points could not be read; fix them and try again."
		return out, nil
	}
	if clipIdx >= 0 {
		return parseForClip(ctx, a, rc, clipIdx, out)
	}
	v := editspec.Voiceover{Beats: beats}
	if rc.spec.Voiceover != nil {
		v = *editspec.Clone(&editspec.Spec{Voiceover: rc.spec.Voiceover}).Voiceover
		if in.Append {
			used := map[string]bool{}
			for _, b := range v.Beats {
				used[b.ID] = true
			}
			n := len(v.Beats)
			for _, b := range beats {
				n++
				b.ID = fmt.Sprintf("line-%d", n)
				for used[b.ID] {
					b.ID += "x"
				}
				used[b.ID] = true
				v.Beats = append(v.Beats, b)
			}
		} else {
			v.Beats = beats
			v.Continuous, v.Layout = nil, ""
			// A new script: Claude's fitted version and the parked lines of the
			// old one are gone (earlier versions of the cut keep them).
			v.Fitted, v.Parked, v.Active = nil, nil, ""
		}
	}
	if !in.Append && !in.FromRecording {
		v.Original = in.Text // exactly as given (D86)
	}
	res, err := planCheck(ctx, a, rc.rev, fmt.Sprintf("Narration script: %s", plural(len(beats), "line")), []editspec.Op{{Op: "setScript", Voiceover: &v}})
	if err != nil {
		return nil, err
	}
	out.Result = res
	out.Summary = plural(len(beats), "narration line") + " read from the script."
	return out, nil
}

func fit(ctx context.Context, a *app.App, in *FitIn) (*FitOut, error) {
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	if rc.spec.Voiceover == nil {
		return &FitOut{Policy: editspec.FitAuto, Lines: []editspec.BeatFit{}, Summary: "No narration yet."}, nil
	}
	rep := editspec.FitReport(editspec.Clone(rc.spec), rc.facts)
	out := &FitOut{Policy: rc.spec.FitPolicy(), Lines: rep}
	want := map[string]bool{}
	for _, r := range in.Beats {
		if i := beatIndex(rc.spec.Voiceover.Beats, r); i >= 0 {
			want[rc.spec.Voiceover.Beats[i].ID] = true
		}
	}
	var eops []editspec.Op
	shots := 0 // lines fixed for running into the next shot
	for _, f := range rep {
		// A line that runs over, or (asked for by name) one that runs into the next shot.
		if f.Status != "over" && !(f.IntoNextShot > 0 && want[f.BeatID] && in.Strategy != "") {
			continue
		}
		if f.Status == "over" {
			out.Over++
		}
		if len(want) > 0 && !want[f.BeatID] {
			continue
		}
		st := in.Strategy
		if st == "preferred" || st == "" {
			st = f.Preferred
		}
		if st == editspec.FixShorten || st == "" {
			out.Rewrite = append(out.Rewrite, f)
			continue
		}
		for _, fx := range f.Fixes {
			if fx.Strategy == st {
				eops = append(eops, fx.Ops...)
				if f.Status != "over" {
					shots++
				}
			}
		}
	}
	if in.Strategy != "" && len(eops) > 0 {
		if out.Result, err = planCheck(ctx, a, rc.rev, "Make the narration fit: "+fixWords(in.Strategy), dedupeOps(eops)); err != nil {
			return nil, err
		}
	}
	switch {
	case shots > 0 && out.Over == 0:
		out.Summary = fmt.Sprintf("Built a fix for %s that ran into the next shot.", plural(shots, "line"))
	case out.Over == 0:
		out.Summary = fmt.Sprintf("All %s fit.", plural(len(rep), "narration line"))
		if len(rep) == 1 {
			out.Summary = "The narration line fits."
		}
	case in.Strategy == "":
		verb := "run"
		if out.Over == 1 {
			verb = "runs"
		}
		out.Summary = fmt.Sprintf("%d of %s %s over; each has suggested fixes.", out.Over, plural(len(rep), "line"), verb)
	default:
		out.Summary = "Built a fix for " + plural(out.Over-len(out.Rewrite), "line")
		if len(out.Rewrite) > 0 {
			out.Summary += fmt.Sprintf("; %d need a shorter text", len(out.Rewrite))
		}
		out.Summary += "."
	}
	return out, nil
}

// fixWords says what a fit strategy does to the video.
func fixWords(strategy string) string {
	switch strategy {
	case editspec.FixExtendShot:
		return "hold the shots a little longer"
	case editspec.FixSpeedUp:
		return "play the AI voice a little faster"
	case editspec.FixStartEarlier:
		return "start the lines a little earlier"
	case editspec.FixMoveNext:
		return "move the next line later"
	}
	return "the suggested fix for each line"
}

// dedupeOps keeps the last op per (op, id) for clip updates so two lines
// extending the same shot do not stack.
func dedupeOps(in []editspec.Op) []editspec.Op {
	seen := map[string]int{}
	var out []editspec.Op
	for _, o := range in {
		k := o.Op + "\x00" + o.ID
		if i, ok := seen[k]; ok && o.Op == "updateClip" {
			out[i] = o
			continue
		}
		seen[k] = len(out)
		out = append(out, o)
	}
	return out
}

func takes(ctx context.Context, a *app.App, in *TakesIn) (*TakesOut, error) {
	project := in.ProjectID
	chosen := map[string]string{}
	beatID := in.Beat
	if in.RevisionID != "" {
		rc, err := load(ctx, a, in.RevisionID)
		if err != nil {
			return nil, err
		}
		project = rc.rev.ProjectID
		if rc.spec.Voiceover != nil {
			for _, b := range rc.spec.Voiceover.Beats {
				if b.Take != nil && b.Take.TakeID != "" {
					chosen[b.ID] = b.Take.TakeID
				}
			}
			if beatID != "" {
				if i := beatIndex(rc.spec.Voiceover.Beats, beatID); i >= 0 {
					beatID = rc.spec.Voiceover.Beats[i].ID
				}
			}
		}
	}
	if project == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say which project, or which version of the cut, the takes belong to.")
	}
	filter := bson.M{"projectId": project}
	if beatID != "" {
		filter["beatId"] = beatID
	}
	var ts []models.VoiceTake
	if err := a.DB.Find(ctx, store.CVoiceTakes, a.WorkspaceID(), filter, &ts, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(500)); err != nil {
		return nil, err
	}
	out := &TakesOut{Takes: []TakeView{}}
	for _, t := range ts {
		out.Takes = append(out.Takes, TakeView{VoiceTake: t, Chosen: chosen[t.BeatID] == t.ID, Summary: takeSummary(t)})
	}
	sort.SliceStable(out.Takes, func(i, j int) bool { return out.Takes[i].BeatID < out.Takes[j].BeatID })
	return out, nil
}

func choose(ctx context.Context, a *app.App, in *ChooseIn) (*PlanResult, error) {
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	idx, err := rc.selectBeats([]string{in.Beat}, nil)
	if err != nil {
		return nil, err
	}
	b := rc.spec.Voiceover.Beats[idx[0]]
	var ref *editspec.TakeRef
	instruction := ""
	switch {
	case in.TakeID != "":
		var t models.VoiceTake
		if err := a.DB.GetByID(ctx, store.CVoiceTakes, a.WorkspaceID(), in.TakeID, &t); err != nil {
			return nil, apperr.New(apperr.NotFound, "That take isn't in this workspace any more.").WithDetails(map[string]any{"takeId": in.TakeID})
		}
		if t.ProjectID != rc.rev.ProjectID {
			return nil, apperr.New(apperr.InvalidInput, "That take belongs to another project.")
		}
		ref = takeRef(&t)
		if len(t.Cleanups) > 0 && in.Version == "processed" {
			c := t.Cleanups[len(t.Cleanups)-1]
			ref.Processed, ref.UseProcessed = &editspec.ProcessedRef{AssetID: c.AssetID, Preset: c.Preset, Offset: c.Offset}, true
		}
		instruction = fmt.Sprintf("Use another take for %s", lineRef(idx[0], b))
	case in.Version != "":
		if b.Take == nil || b.Take.Processed == nil {
			return nil, apperr.New(apperr.InvalidInput, "%s has no cleaned-up version yet.", beatName(idx[0], b)).
				WithHint("Clean it up on the Voiceover tab first.").WithCommand(fmt.Sprintf("metarush voiceover cleanup --revision %s --beat %d", rc.rev.ID, idx[0]+1))
		}
		t := *b.Take
		t.UseProcessed = in.Version == "processed"
		ref = &t
		instruction = fmt.Sprintf("Play the %s version of %s", map[bool]string{true: "cleaned-up", false: "original"}[t.UseProcessed], lineRef(idx[0], b))
	default:
		return nil, apperr.New(apperr.InvalidInput, "Choose a take, or the original or cleaned-up version.")
	}
	state := map[string]string{"scratch": "scratch", "tts": "generated", "recorded": "recorded", "imported": "imported"}[ref.Source]
	set := map[string]any{"take": ref}
	if state != "" {
		set["state"] = state
	}
	// A piece of a live take plays where it was spoken (D85).
	if in.TakeID != "" {
		var t models.VoiceTake
		if a.DB.GetByID(ctx, store.CVoiceTakes, a.WorkspaceID(), in.TakeID, &t) == nil && t.ReadID != "" && t.Pin != nil {
			set["anchor"] = *t.Pin
		}
	}
	return planCheck(ctx, a, rc.rev, instruction, []editspec.Op{setOp(b.ID, set)})
}

func addTake(ctx context.Context, a *app.App, in *AddTakeIn) (*TakeView, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
		return nil, apperr.New(apperr.NotFound, "That recording isn't in this workspace.").WithDetails(map[string]any{"assetId": in.AssetID})
	}
	if !as.Media.HasAudio {
		return nil, apperr.New(apperr.UnsupportedMedia, "%s has no sound.", as.Name)
	}
	if in.Out <= in.In || (as.Media.Duration > 0 && in.Out > as.Media.Duration+0.05) {
		return nil, apperr.New(apperr.InvalidInput, "The take has to start and end inside %s, which is %.1f seconds long.", as.Name, as.Media.Duration)
	}
	t := &models.VoiceTake{ProjectID: in.ProjectID, BeatID: in.BeatID, Text: in.Text, Source: in.Source, AssetID: as.ID, In: round3(in.In), Out: round3(in.Out),
		Duration: round3(in.Out - in.In), Words: editspec.WordCount(in.Text), Timing: "none"}
	if as.Transcript != nil && as.Transcript.State == "available" {
		t.Timing = "word"
	}
	if t.Duration > 0 && t.Words > 0 {
		t.WPM = float64(int(float64(t.Words)/t.Duration*60 + 0.5))
	}
	if err := insertTake(ctx, a, t); err != nil {
		return nil, err
	}
	// A recording uploaded from the dashboard's recorder arrives as an ordinary
	// file: mark it as narration (and keep it in the project) like the CLI recorder's.
	upd := bson.M{"$addToSet": bson.M{"tags": bson.M{"$each": []string{"voiceover", in.Source}}}}
	if in.ProjectID != "" {
		upd["$addToSet"].(bson.M)["projectIds"] = in.ProjectID
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, upd)
	a.Audit(ctx, "voiceover.take", "take", t.ID, in.ProjectID, map[string]any{"beat": in.BeatID, "asset": as.ID, "source": in.Source})
	return &TakeView{VoiceTake: *t, Summary: takeSummary(*t)}, nil
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	ops.Offer("voiceover.tts", ops.Offering{ID: "ai-narration", Group: "make", Title: "AI narration",
		Does:  "Reads your script in a natural AI voice with ElevenLabs; lines that didn't change are reused for free. Priced first.",
		Words: []string{"text to speech", "voiceover", "narrator"}, Needs: ops.Needs{Keys: []string{"elevenlabs"}, Priced: true, Leaves: "the script's words leave this computer"}})
	ops.Offer("voiceover.scratch", ops.Offering{ID: "stand-in-narration", Group: "make", Title: "Stand-in narration",
		Does:  "A free robotic voice to time narration before you record it or choose an AI voice.",
		Words: []string{"scratch voice", "temp voiceover"}, Needs: ops.Needs{Works: []string{"scratch-voice"}}})
}

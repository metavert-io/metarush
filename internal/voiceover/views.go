package voiceover

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// BeatView is one narration line as the dashboard and CLI show it.
type BeatView struct {
	ID        string             `json:"id"`
	Line      int                `json:"line"`
	Text      string             `json:"text"`
	Delivery  string             `json:"delivery,omitempty"`
	Anchor    editspec.Anchor    `json:"anchor"`
	Placement string             `json:"placement"` // plain words: "0.5 s after the shot “broll” starts"
	Start     float64            `json:"start"`     // timeline seconds
	End       float64            `json:"end"`
	Window    float64            `json:"window"`
	Fit       string             `json:"fit" jsonschema:"enum=ok,enum=tight,enum=over,enum=unresolved"`
	Problem   string             `json:"problem,omitempty"`
	Words     int                `json:"words"`
	Estimated bool               `json:"estimated"`
	Take      *editspec.TakeRef  `json:"take,omitempty"`
	TakeLabel string             `json:"takeLabel"` // plain words
	TakeCount int                `json:"takeCount"`
	State     string             `json:"state,omitempty"`
	Voice     *editspec.VoiceRef `json:"voice,omitempty"`
	Locked    bool               `json:"locked,omitempty"`
	AIVoice   bool               `json:"aiVoice,omitempty"`
	// A line played by a live take (D85): the take, and where the line was
	// planned to start before it (in words and seconds on this cut);
	// AtPlanned says it sits there now (snapped), not where it was spoken.
	ReadID    string   `json:"readId,omitempty"`
	Planned   string   `json:"planned,omitempty"`
	PlannedAt *float64 `json:"plannedAt,omitempty"`
	AtPlanned bool     `json:"atPlanned,omitempty"`
	// Pinned: the line has a start point of its own (not "after the line before").
	Pinned bool `json:"pinned"`
}

// ScriptView says which versions of the script the cut keeps (D86).
type ScriptView struct {
	// HasOriginal: the script as the person gave it is kept (Original).
	HasOriginal bool   `json:"hasOriginal"`
	Original    string `json:"original,omitempty"`
	// Active is the version in use: original or fitted.
	Active string `json:"active" jsonschema:"enum=original,enum=fitted"`
	// Fitted is set when Claude fitted the script: how many lines it changed and
	// how many of those the person kept as they wrote them.
	Fitted *FittedSummary `json:"fitted,omitempty"`
	// Timed: at least one line has a start point of its own; an as-is script
	// has none until a recording (or a fit) sets them.
	Timed bool `json:"timed"`
}

// FittedSummary is the fitted version in numbers.
type FittedSummary struct {
	Lines   int    `json:"lines"`
	Changed int    `json:"changed"`
	Kept    int    `json:"kept"`
	Note    string `json:"note,omitempty"`
}

// ShowOut is the narration of one revision.
type ShowOut struct {
	RevisionID      string             `json:"revisionId"`
	ProjectID       string             `json:"projectId"`
	HasScript       bool               `json:"hasScript"`
	Layout          string             `json:"layout"`
	SpeakingRateWPM float64            `json:"speakingRateWpm"`
	RateSource      string             `json:"rateSource"`
	FitPolicy       string             `json:"fitPolicy"`
	Duration        float64            `json:"duration"`
	Beats           []BeatView         `json:"beats"`
	Fit             []editspec.BeatFit `json:"fit"`
	Issues          []editspec.Issue   `json:"issues"`
	Script          ScriptView         `json:"script"`
	// Reads are the project's live takes, newest first (A/B between them).
	Reads   []ReadView `json:"reads"`
	Summary string     `json:"summary"`
}

// Placement describes an anchor in plain words.
func Placement(s *editspec.Spec, a editspec.Anchor) string {
	off := ""
	switch {
	case a.Offset > 0.0005:
		off = fmt.Sprintf("%.1f s after ", a.Offset)
	case a.Offset < -0.0005:
		off = fmt.Sprintf("%.1f s before ", -a.Offset)
	}
	shot := func(id string) string {
		for i, c := range s.Clips {
			if c.ID == id {
				if c.Label != "" {
					return fmt.Sprintf("shot %d (“%s”)", i+1, c.Label)
				}
				return fmt.Sprintf("shot %d", i+1)
			}
		}
		return "a shot that is no longer in the video"
	}
	switch a.Kind {
	case editspec.AnchorClip:
		edge := "starts"
		if a.Edge == "end" {
			edge = "ends"
		}
		if off == "" {
			return "when " + shot(a.ClipID) + " " + edge
		}
		return off + shot(a.ClipID) + " " + edge
	case editspec.AnchorMoment:
		if strings.ContainsAny(a.Quote, "“\"") {
			// A spoken moment, already in words: "when Sam says “we built it…”".
			if off == "" {
				return "when " + a.Quote
			}
			return off + a.Quote
		}
		what := "the chosen moment"
		if a.Quote != "" {
			what = "“" + a.Quote + "”"
		}
		if off == "" {
			return "right at " + what
		}
		return off + what
	case editspec.AnchorAfter:
		g := 0.3
		if s.Voiceover != nil && s.Voiceover.Settings != nil && s.Voiceover.Settings.Gap > 0 {
			g = s.Voiceover.Settings.Gap
		}
		if a.Gap != nil {
			g = *a.Gap
		}
		return fmt.Sprintf("%.1f s after the previous line", g+a.Offset)
	case editspec.AnchorTime:
		if a.At != nil {
			return "at " + editspec.Clock(*a.At+a.Offset)
		}
	}
	return "not placed"
}

func show(ctx context.Context, a *app.App, revisionID string) (*ShowOut, error) {
	rc, err := load(ctx, a, revisionID)
	if err != nil {
		return nil, err
	}
	s := rc.spec
	fps := rc.fps()
	out := &ShowOut{RevisionID: rc.rev.ID, ProjectID: rc.rev.ProjectID, Layout: editspec.LayoutBeats, SpeakingRateWPM: s.SpeakingRate(), RateSource: "default",
		FitPolicy: s.FitPolicy(), Duration: round3(float64(rc.total) / fps), Beats: []BeatView{}, Fit: []editspec.BeatFit{}, Issues: []editspec.Issue{},
		Script: ScriptView{Active: editspec.VersionOriginal}, Reads: projectReads(ctx, a, rc.rev.ProjectID, s)}
	if s.Voiceover == nil {
		out.Summary = "No narration yet."
		return out, nil
	}
	out.Script = scriptView(s.Voiceover)
	liveTakes := liveTakesOf(ctx, a, s.Voiceover.Beats)
	out.HasScript = len(s.Voiceover.Beats) > 0
	if s.Continuous() {
		out.Layout = editspec.LayoutContinuous
	}
	if st := s.Voiceover.Settings; st != nil && st.RateSource != "" {
		out.RateSource = st.RateSource
	}
	counts := takeCounts(ctx, a, rc.rev.ProjectID)
	planFit := map[string]string{}
	for _, f := range editspec.FitReport(editspec.Clone(s), rc.facts) {
		out.Fit = append(out.Fit, f)
		planFit[f.BeatID] = f.Status
	}
	recorded, missing := 0, 0
	for _, rb := range rc.beats {
		b := s.Voiceover.Beats[rb.Index]
		bv := BeatView{ID: b.ID, Line: rb.Index + 1, Text: b.Text, Delivery: b.Delivery, Anchor: b.Anchor, Placement: Placement(s, b.Anchor),
			Start: round3(float64(rb.Start) / fps), End: round3(float64(rb.End()) / fps), Window: round3(float64(rb.Window()) / fps),
			Fit: planFit[b.ID], Problem: rb.Problem, Words: rb.Words, Estimated: rb.Estimated, Take: b.Take, State: b.State, Voice: b.Voice,
			Locked: b.Locked, TakeCount: counts[b.ID], Pinned: b.Anchor.Kind != editspec.AnchorAfter}
		if b.Take != nil {
			if t, ok := liveTakes[b.Take.TakeID]; ok {
				bv.ReadID = t.ReadID
				if t.Planned != nil {
					bv.Planned = Placement(s, *t.Planned)
					bv.AtPlanned = sameAnchor(*t.Planned, b.Anchor) && (t.Pin == nil || !sameAnchor(*t.Pin, b.Anchor))
					pe := -1
					if rb.Index > 0 && rc.beats[rb.Index-1].Problem == "" {
						pe = rc.beats[rb.Index-1].End()
					}
					if f, prob := editspec.AnchorFrame(s, rc.clips, *t.Planned, pe); prob == "" {
						pa := round3(float64(f) / fps)
						bv.PlannedAt = &pa
					}
				}
			}
		}
		if b.Take != nil {
			recorded++
			bv.AIVoice = b.Take.AIVoice
			bv.TakeLabel = map[string]string{"scratch": "scratch voice", "tts": "AI voice", "recorded": "your recording", "imported": "imported recording"}[b.Take.Source]
			if bv.TakeLabel == "" {
				bv.TakeLabel = "audio"
			}
			if b.Take.UseProcessed && b.Take.Processed != nil {
				bv.TakeLabel += " (cleaned up)"
			}
		} else {
			missing++
			bv.TakeLabel = "no audio yet"
		}
		out.Beats = append(out.Beats, bv)
	}
	issues := editspec.Validate(editspec.Clone(s), rc.facts, nil, false)
	for _, is := range issues {
		if strings.HasPrefix(is.Path, "/voiceover") || strings.HasPrefix(is.Path, "/mix") || strings.HasPrefix(is.Code, "beat") || strings.HasPrefix(is.Code, "captions_") {
			out.Issues = append(out.Issues, is)
		}
	}
	over := 0
	for _, f := range out.Fit {
		if f.Status == "over" || f.Status == "unresolved" {
			over++
		}
	}
	out.Summary = fmt.Sprintf("%s: %d with audio, %d without", plural(len(out.Beats), "narration line"), recorded, missing)
	switch {
	case over == 1:
		out.Summary += "; 1 doesn't fit yet"
	case over > 1:
		out.Summary += fmt.Sprintf("; %d don't fit yet", over)
	}
	return out, nil
}

// scriptView says which script versions a narration keeps.
func scriptView(v *editspec.Voiceover) ScriptView {
	sv := ScriptView{HasOriginal: strings.TrimSpace(v.Original) != "", Original: v.Original, Active: v.ActiveVersion()}
	if f := v.Fitted; f != nil {
		fs := &FittedSummary{Lines: len(f.Lines), Note: f.Note}
		for _, l := range f.Lines {
			if !editspec.SameWords(l.Original, l.Text) || l.Anchor != nil {
				fs.Changed++
				if l.Kept {
					fs.Kept++
				}
			}
		}
		sv.Fitted = fs
	}
	for _, b := range v.Beats {
		if b.Anchor.Kind != editspec.AnchorAfter {
			sv.Timed = true
		}
	}
	return sv
}

// liveTakesOf loads the take records of lines played by a live take.
func liveTakesOf(ctx context.Context, a *app.App, beats []editspec.Beat) map[string]models.VoiceTake {
	var idsList []string
	for _, b := range beats {
		if b.Take != nil && b.Take.TakeID != "" {
			idsList = append(idsList, b.Take.TakeID)
		}
	}
	out := map[string]models.VoiceTake{}
	if len(idsList) == 0 {
		return out
	}
	var ts []models.VoiceTake
	_ = a.DB.Find(ctx, store.CVoiceTakes, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idsList}, "readId": bson.M{"$exists": true, "$ne": ""}}, &ts)
	for _, t := range ts {
		out[t.ID] = t
	}
	return out
}

func takeCounts(ctx context.Context, a *app.App, projectID string) map[string]int {
	out := map[string]int{}
	var ts []models.VoiceTake
	_ = a.DB.Find(ctx, store.CVoiceTakes, a.WorkspaceID(), bson.M{"projectId": projectID}, &ts, options.Find().SetProjection(bson.M{"beatId": 1}))
	for _, t := range ts {
		out[t.BeatID]++
	}
	return out
}

// BusView is one mixer bus in plain words.
type BusView struct {
	Name   string  `json:"name"`
	Label  string  `json:"label"`
	GainDB float64 `json:"gainDb"`
	Mute   bool    `json:"mute"`
	Solo   bool    `json:"solo"`
	Muted  bool    `json:"muted"` // effective (muted or silenced by another solo)
	Level  string  `json:"level"` // "normal", "3 dB quieter", "muted"
}

// MixOut is the resolved mix of a revision.
type MixOut struct {
	RevisionID   string            `json:"revisionId"`
	Mix          *editspec.Mix     `json:"mix,omitempty"`
	Effective    *editspec.PlanMix `json:"effective"`
	Buses        []BusView         `json:"buses"`
	MutedClips   []string          `json:"mutedClips"`
	Explanations []string          `json:"explanations"` // plain sentences about what the mix does
}

func mixShow(ctx context.Context, a *app.App, revisionID string) (*MixOut, error) {
	rc, err := load(ctx, a, revisionID)
	if err != nil {
		return nil, err
	}
	plan := editspec.Resolve(editspec.Clone(rc.spec), rc.facts, rc.rev.ID, rc.rev.ProjectID, rc.rev.SpecHash)
	return mixView(rc.rev.ID, rc.spec, plan), nil
}

// mixView is a cut's mix in plain words, from its spec and resolved plan (a
// saved version, or a change previewed before it is saved).
func mixView(revisionID string, s *editspec.Spec, plan *editspec.RenderPlan) *MixOut {
	out := &MixOut{RevisionID: revisionID, Mix: s.Mix, Effective: plan.Mix, Buses: []BusView{}, MutedClips: []string{}, Explanations: []string{}}
	labels := map[string]string{editspec.BusSource: "Camera sound", editspec.BusVoiceover: "Narration", editspec.BusMusic: "Music", editspec.BusSFX: "Effects"}
	specBus := map[string]*editspec.Bus{}
	if s.Mix != nil {
		specBus = map[string]*editspec.Bus{editspec.BusSource: s.Mix.Source, editspec.BusVoiceover: s.Mix.Voiceover, editspec.BusMusic: s.Mix.Music, editspec.BusSFX: s.Mix.SFX}
	}
	for _, name := range editspec.Buses {
		pb := plan.Mix.Buses[name]
		bv := BusView{Name: name, Label: labels[name], GainDB: pb.GainDB, Muted: pb.Muted, Level: levelWords(pb.GainDB, pb.Muted)}
		if b := specBus[name]; b != nil {
			bv.Mute, bv.Solo = b.Mute, b.Solo
		}
		out.Buses = append(out.Buses, bv)
	}
	for i, c := range s.Clips {
		if c.Muted || plan.Clips[i].Muted {
			out.MutedClips = append(out.MutedClips, c.ID)
		}
	}
	d := plan.Mix.Ducking
	if d.Auto {
		if len(plan.Mix.VoiceRanges) > 0 {
			sourceAudible := false
			for _, c := range plan.Clips {
				if c.HasAudio && !c.Muted {
					sourceAudible = true
				}
			}
			if sourceAudible {
				out.Explanations = append(out.Explanations,
					fmt.Sprintf("Music gets quieter whenever the narration plays (%s), and the camera sound dips further (%s).", softer(d.MusicUnderVoiceoverDB), softer(d.SourceUnderVoiceoverDB)))
			} else {
				out.Explanations = append(out.Explanations, fmt.Sprintf("Music gets quieter whenever the narration plays (%s).", softer(d.MusicUnderVoiceoverDB)))
			}
		}
		if len(plan.DialogueRanges) > 0 {
			out.Explanations = append(out.Explanations, fmt.Sprintf("Music gets quieter whenever someone talks on camera (%s).", softer(d.MusicUnderDialogueDB)))
		}
	}
	if s.Mix != nil && s.Mix.MuteSource {
		out.Explanations = append(out.Explanations, "The camera sound is off for the whole video.")
	} else if n := len(out.MutedClips); n > 0 {
		if n == 1 {
			out.Explanations = append(out.Explanations, "1 clip plays without its camera sound.")
		} else {
			out.Explanations = append(out.Explanations, fmt.Sprintf("%d clips play without their camera sound.", n))
		}
	}
	if plan.Mix.LevelSpeech {
		turned := 0
		for _, c := range plan.Clips {
			if c.LevelDB != nil && math.Abs(*c.LevelDB) >= 0.5 {
				turned++
			}
		}
		switch {
		case turned == 1:
			out.Explanations = append(out.Explanations, "Voices are evened out from clip to clip: 1 clip is turned up or down to match the others.")
		case turned > 1:
			out.Explanations = append(out.Explanations, fmt.Sprintf("Voices are evened out from clip to clip: %d clips are turned up or down to match the others.", turned))
		default:
			out.Explanations = append(out.Explanations, "Voices are evened out from clip to clip (they already match).")
		}
	}
	if plan.Mix.CutBlendFrames > 0 {
		out.Explanations = append(out.Explanations, "At straight cuts the sound blends for a moment, so cuts never click.")
	}
	if est := plan.Mix.EstimatedLUFS; est != nil {
		goal := "about as loud as most videos on this platform"
		switch {
		case plan.Mix.MasterGainDB > 3:
			out.Explanations = append(out.Explanations, fmt.Sprintf("The whole mix is turned up (%s) so it is %s.", louder(plan.Mix.MasterGainDB), goal))
		case plan.Mix.MasterGainDB > 0.5:
			out.Explanations = append(out.Explanations, fmt.Sprintf("The whole mix is turned up a little (%s) so it is %s.", louder(plan.Mix.MasterGainDB), goal))
		case plan.Mix.MasterGainDB < -3:
			out.Explanations = append(out.Explanations, fmt.Sprintf("The whole mix is turned down (%s) so it is %s.", softer(plan.Mix.MasterGainDB), goal))
		case plan.Mix.MasterGainDB < -0.5:
			out.Explanations = append(out.Explanations, fmt.Sprintf("The whole mix is turned down a little (%s) so it is %s.", softer(plan.Mix.MasterGainDB), goal))
		}
		if plan.Mix.PeakLimited {
			out.Explanations = append(out.Explanations, "It stops short of the usual loudness so the loudest moments don't distort.")
		}
	}
	sort.Strings(out.MutedClips)
	return out
}

func levelWords(db float64, muted bool) string {
	switch {
	case muted:
		return "muted"
	case math.Abs(db) < 0.05:
		return "normal"
	case db < 0:
		return fmt.Sprintf("%.0f dB quieter", -db)
	}
	return fmt.Sprintf("%.0f dB louder", db)
}

func softer(db float64) string { return fmt.Sprintf("%.0f dB quieter", math.Abs(db)) }
func louder(db float64) string { return fmt.Sprintf("%.0f dB louder", math.Abs(db)) }

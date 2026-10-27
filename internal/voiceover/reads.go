package voiceover

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// Live reads (D85): the person watches the cut and speaks; the recording lands
// exactly where it was spoken. Placing a read keeps the whole recording, cuts it
// into one piece per script line only in the pauses between lines, and sets
// each line's start point to where it was said (tied to the shot playing there,
// to the millisecond). A read covers the stretch of the video it was spoken
// over: lines heard there take the new pieces, lines not heard lose their old
// sound and follow the line before (flagged), lines elsewhere stay as they
// were, and a line that ran into the stretch stops where the read begins with a
// short crossfade (a re-recorded stretch, a punch-in). Every read stays in the
// project; putting an earlier one back (or each line at its planned spot) is
// another placement of the same pieces.

// ActionPlace is the narration job for placing a live read.
const ActionPlace = "place"

func init() {
	ActionTitles[ActionPlace] = "Placing your take"
}

// PlaceIn puts a live read in the cut, or puts an earlier read back.
type PlaceIn struct {
	RevisionID string `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	// A new read: the recording and where it started on the timeline.
	AssetID string   `json:"assetId,omitempty" jsonschema:"pattern=^ast_[0-9a-z]{26}$,description=A new read: the recording (transcribed)."`
	From    *float64 `json:"from,omitempty" jsonschema:"minimum=0,maximum=86400,description=A new read: the point of the video (timeline seconds) where the recording starts (where the playhead was when recording began)."`
	// Until ends the stretch the read replaces (a re-recorded range); by default
	// it reaches as far as the read was spoken.
	Until *float64 `json:"until,omitempty" jsonschema:"minimum=0,maximum=86400,description=Replace only up to this point of the video (a re-recorded range)."`
	// Read puts an earlier read back: its ID (rd_…) or one of its takes (tk_…).
	Read  string `json:"read,omitempty" jsonschema:"maxLength=40,description=An earlier live read to put back: its ID (rd_…) or one of its takes (tk_…)."`
	Where string `json:"where,omitempty" jsonschema:"enum=recorded,enum=planned,description=recorded (default): each line where it was spoken; planned: each line at the spot it was planned for."`
	// Lines limits the read to some lines (IDs or numbers; default every line).
	Lines  []string `json:"lines,omitempty" jsonschema:"maxItems=300"`
	Source string   `json:"source,omitempty" jsonschema:"enum=recorded,enum=imported"`
	Apply  bool     `json:"apply,omitempty" jsonschema:"description=Also put it in the cut as a new version (applies the plan it checked)."`
}

// Placed line states.
const (
	PlacedLineIn   = "placed"    // this read plays it (where it was spoken, or at its planned spot)
	PlacedNotHeard = "not-heard" // inside the read but not heard: no sound of its own, follows the line before
	PlacedKept     = "kept"      // outside the read: unchanged
	PlacedTrimmed  = "trimmed"   // outside the read, but its sound now stops where the read begins
)

// PlacedLine is one line after a read was placed.
type PlacedLine struct {
	BeatID string `json:"beatId"`
	Line   int    `json:"line"`
	Text   string `json:"text"`
	State  string `json:"state" jsonschema:"enum=placed,enum=not-heard,enum=kept,enum=trimmed"`
	// At is where the line starts now (timeline seconds) and Placement says it in words.
	At        float64 `json:"at"`
	Placement string  `json:"placement"`
	// Planned is where the line was meant to start before the read, in words
	// and in seconds; Moved is how far it landed from there (+ later).
	Planned   string   `json:"planned,omitempty"`
	PlannedAt *float64 `json:"plannedAt,omitempty"`
	Moved     *float64 `json:"moved,omitempty"`
	// In and Out are its piece of the recording.
	In      float64 `json:"in,omitempty"`
	Out     float64 `json:"out,omitempty"`
	Match   float64 `json:"match,omitempty"`
	TakeID  string  `json:"takeId,omitempty"`
	Message string  `json:"message,omitempty"`
}

// PlaceOut is the placement and the plan that makes it.
type PlaceOut struct {
	ReadID string `json:"readId"`
	Where  string `json:"where" jsonschema:"enum=recorded,enum=planned"`
	// From and To are the stretch of the video the read covers (timeline seconds).
	From        float64      `json:"from"`
	To          float64      `json:"to"`
	Lines       []PlacedLine `json:"lines"`
	Placed      int          `json:"placed"`
	NotHeard    int          `json:"notHeard"`
	Coverage    float64      `json:"coverage,omitempty"`
	MeasuredWPM float64      `json:"measuredWpm,omitempty"`
	Warnings    []string     `json:"warnings,omitempty"`
	Result      *PlanResult  `json:"result,omitempty"`
	Applied     *Applied     `json:"applied,omitempty"`
	Summary     string       `json:"summary"`
	JobID       string       `json:"jobId,omitempty"`
}

func (o *PlaceOut) setJobID(id string) { o.JobID = id }
func (o *PlaceOut) jobResult() map[string]any {
	return withApplied(map[string]any{"summary": o.Summary, "lines": o.Placed, "notHeard": o.NotHeard, "coverage": o.Coverage}, o.Applied)
}

func (o *PlaceOut) apply(ctx context.Context, a *app.App) {
	o.Applied = applyResult(ctx, a, o.Result)
	o.Summary = settle(o.Summary, o.Applied)
}

// place runs voiceover.place.
func place(ctx context.Context, a *app.App, in *PlaceIn) (*PlaceOut, error) {
	switch in.Where {
	case "", "recorded", "planned":
	default:
		return nil, apperr.New(apperr.InvalidInput, "Choose where the lines go: where they were spoken, or at their planned spots.").WithDetails(map[string]any{"where": []string{"recorded", "planned"}})
	}
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	if in.Read != "" {
		if in.AssetID != "" {
			return nil, apperr.New(apperr.InvalidInput, "Give a new recording or an earlier take, not both.")
		}
		return placeAgain(ctx, a, rc, in)
	}
	if in.AssetID == "" || in.From == nil {
		return nil, apperr.New(apperr.InvalidInput, "Say which recording to place and where on the video it starts.").WithDetails(map[string]any{"fields": []string{"assetId", "from"}})
	}
	if in.Where == "planned" {
		return nil, apperr.New(apperr.InvalidInput, "A new take goes where it was spoken. Once it's in, you can put its lines at their planned spots.")
	}
	return placeNew(ctx, a, rc, in)
}

// placeNew places a new live read.
func placeNew(ctx context.Context, a *app.App, rc *revCtx, in *PlaceIn) (*PlaceOut, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
		return nil, apperr.New(apperr.NotFound, "That recording isn't in this workspace.").WithDetails(map[string]any{"assetId": in.AssetID})
	}
	if !as.Media.HasAudio {
		return nil, apperr.New(apperr.UnsupportedMedia, "%s has no sound.", as.Name)
	}
	source := in.Source
	if source == "" {
		source = models.TakeRecorded
	}
	// It is narration from now on (even while MetaRush is still listening to it):
	// the take is reported by the recorder, not announced as an ordinary file.
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$addToSet": bson.M{"tags": bson.M{"$each": []string{"voiceover", source}}, "projectIds": rc.rev.ProjectID}})
	if as.Transcript == nil || as.Transcript.State != "available" {
		return nil, apperr.New(apperr.Conflict, "MetaRush hasn't finished listening to the take yet, so it can't find its lines.").
			WithHint("Wait a moment until its words are ready, then place it again.").WithCommand(fmt.Sprintf("metarush footage analyze --asset %s --wait", as.ID))
	}
	segs, err := transcriptSegments(ctx, a, as.ID)
	if err != nil {
		return nil, err
	}
	var words []editspec.TranscriptWord
	for _, s := range segs {
		for _, w := range s.Words {
			words = append(words, editspec.TranscriptWord{Text: w.Text, From: w.From, To: w.To})
		}
	}
	if len(words) == 0 {
		return nil, apperr.New(apperr.Validation, "No speech was heard in the take.")
	}
	if err := step(ctx, 0, 2, "Matching your take to the script"); err != nil {
		return nil, err
	}
	env := readEnvelope(ctx, a, &as)
	p := round3(*in.From)
	fps := rc.fps()
	readID := ids.New(models.ReadPrefix)
	out := &PlaceOut{ReadID: readID, Where: "recorded", Lines: []PlacedLine{}}

	// No script yet: the read writes it (one line per sentence group it heard).
	fresh := rc.spec.Voiceover == nil || len(rc.spec.Voiceover.Beats) == 0
	var scope []int
	var beats []editspec.Beat
	if fresh {
		for i, s := range segs {
			t := strings.Join(strings.Fields(s.Text), " ")
			if t == "" {
				continue
			}
			beats = append(beats, editspec.Beat{ID: fmt.Sprintf("line-%d", len(beats)+1), Text: clipText(t, 2000), Anchor: editspec.Anchor{Kind: editspec.AnchorAfter}, State: "draft"})
			scope = append(scope, i)
		}
		if len(beats) == 0 {
			return nil, apperr.New(apperr.Validation, "No speech was heard in the take.")
		}
	} else {
		if scope, err = rc.selectBeats(in.Lines, nil); err != nil {
			return nil, err
		}
		for _, i := range scope {
			beats = append(beats, rc.spec.Voiceover.Beats[i])
		}
	}
	sp := editspec.SplitRead(beats, words, env, as.Media.Duration)
	out.Coverage, out.MeasuredWPM = sp.Coverage, sp.WPM
	first, last := -1, -1
	for k, rl := range sp.Lines {
		if rl.Found {
			if first < 0 {
				first = k
			}
			last = k
		}
	}
	if first < 0 {
		return nil, apperr.New(apperr.Validation, "None of the script's lines were heard in the take.").
			WithHint("Check that the right microphone is on and read the lines on the teleprompter. The take is kept in the project.")
	}
	// The stretch of the video the read covers: from its first piece to where
	// it stops (or to the end of a chosen range).
	from := p + sp.Lines[first].In
	to := p + sp.Lines[last].Out
	if in.Until != nil {
		if *in.Until <= p {
			return nil, apperr.New(apperr.InvalidInput, "The stretch to record again ends before the recording starts.")
		}
		from, to = p, *in.Until
	}
	out.From, out.To = round3(from), round3(to)

	type change struct {
		set map[string]any
	}
	changes := map[string]change{}
	order := []string{}
	put := func(id string, set map[string]any) {
		if _, ok := changes[id]; !ok {
			order = append(order, id)
		}
		changes[id] = change{set}
	}
	placedAt := map[int]bool{}
	for k, rl := range sp.Lines {
		if !rl.Found {
			continue
		}
		pin := p + rl.In
		if pin < from-0.001 || pin >= to {
			continue // spoken outside the chosen range
		}
		placedAt[k] = true
	}
	fpsOf := func(f int) float64 { return round3(float64(f) / fps) }
	var takes []*models.VoiceTake
	for k, i := range scope {
		b := beats[k]
		rl := sp.Lines[k]
		pl := PlacedLine{BeatID: b.ID, Line: i + 1, Text: b.Text, State: PlacedKept, Match: rl.Match}
		if fresh {
			pl.Line = k + 1
		}
		var old *editspec.ResolvedBeat
		if !fresh && i < len(rc.beats) && rc.beats[i].Problem == "" {
			old = &rc.beats[i]
		}
		switch {
		case placedAt[k]:
			pinT := p + rl.In
			pin := editspec.PinAnchor(rc.spec, rc.clips, pinT)
			planned := plannedFor(ctx, a, b)
			if fresh {
				planned = nil
			}
			match := rl.Match
			t := &models.VoiceTake{ProjectID: rc.rev.ProjectID, BeatID: b.ID, Text: b.Text, Source: source, AssetID: as.ID, In: rl.In, Out: rl.Out,
				Duration: round3(rl.Out - rl.In), Words: rl.Matched, Timing: "word", Match: &match, ReadID: readID, Pin: &pin, PinAt: ptr(round3(pinT)),
				Planned: planned, RecordedOn: rc.rev.ID}
			if t.Duration > 0 && rl.Matched > 0 {
				t.WPM = math.Round(float64(rl.Matched) / math.Max(0.1, rl.SpokenTo-rl.SpokenFrom) * 60)
			}
			takes = append(takes, t)
			pl.State, pl.At, pl.In, pl.Out = PlacedLineIn, round3(pinT), rl.In, rl.Out
			pl.Placement = Placement(rc.spec, pin)
			if planned != nil {
				pl.Planned = Placement(rc.spec, *planned)
				if old != nil {
					// The planned spot on this cut: the anchor resolved after the line before.
					pe := -1
					if i > 0 && rc.beats[i-1].Problem == "" {
						pe = rc.beats[i-1].End()
					}
					if f, prob := editspec.AnchorFrame(rc.spec, rc.clips, *planned, pe); prob == "" {
						pa := fpsOf(f)
						pl.PlannedAt = &pa
						mv := round3(pl.At - pa)
						pl.Moved = &mv
					}
				}
			}
			if rl.Match < 0.85 || len(rl.Missing) > 2 || len(rl.Extra) > 3 {
				pl.Message = mismatchWords(beatName(i, b), rl.Missing, rl.Extra)
				out.Warnings = append(out.Warnings, pl.Message)
			}
			out.Placed++
		case !fresh:
			// Not heard here: inside the read (between lines heard, or where the
			// read was spoken over) it loses its old sound and follows the line before.
			between := false
			for j := k - 1; j >= 0 && !between; j-- {
				if placedAt[j] {
					for jj := k + 1; jj < len(sp.Lines); jj++ {
						if placedAt[jj] {
							between = true
							break
						}
					}
					break
				}
			}
			inside := old != nil && float64(old.Start)/fps >= from-0.001 && float64(old.Start)/fps < to
			if between || inside {
				pl.State = PlacedNotHeard
				pl.Message = fmt.Sprintf("%s wasn't heard in this take; if you said it, it plays as part of the line before", beatName(i, b))
				out.Warnings = append(out.Warnings, pl.Message)
				out.NotHeard++
				put(b.ID, map[string]any{"take": nil, "anchor": editspec.Anchor{Kind: editspec.AnchorAfter}, "state": "draft"})
			} else if old != nil {
				pl.At = fpsOf(old.Start)
				pl.Placement = Placement(rc.spec, b.Anchor)
			}
		}
		out.Lines = append(out.Lines, pl)
	}
	// Lines outside the read that ran into it now stop where its sound begins
	// (with a short crossfade): the re-recorded stretch plays only the new take.
	// Not earlier: the lead-in before the first line (a re-recorded line starts
	// playing a moment early) is silence in the new take, so the line before
	// keeps its last words.
	trimAt := math.Max(from, p+sp.Lines[first].In)
	if !fresh {
		for _, rb := range rc.beats {
			b := rc.spec.Voiceover.Beats[rb.Index]
			if rb.Problem != "" || b.Take == nil || b.Locked {
				continue
			}
			if _, done := changes[b.ID]; done || inScope(scope, rb.Index) && lineState(out.Lines, b.ID) != PlacedKept {
				continue
			}
			start, end := float64(rb.Start)/fps, float64(rb.End())/fps
			if start >= trimAt || end <= trimAt+readCrossfade {
				continue
			}
			t := *b.Take
			keep := (trimAt + readCrossfade - start) * rb.Rate
			if keep < 0.2 {
				continue
			}
			t.Out = round3(t.In + keep)
			put(b.ID, map[string]any{"take": t})
			msg := fmt.Sprintf("%s now stops where your take begins", beatName(rb.Index, b))
			found := false
			for j := range out.Lines {
				if out.Lines[j].BeatID == b.ID {
					out.Lines[j].State, out.Lines[j].Message, found = PlacedTrimmed, msg, true
				}
			}
			if !found {
				out.Lines = append(out.Lines, PlacedLine{BeatID: b.ID, Line: rb.Index + 1, Text: b.Text, State: PlacedTrimmed, At: round3(start),
					Placement: Placement(rc.spec, b.Anchor), Message: msg})
			}
		}
	}
	for _, t := range takes {
		if err := insertTake(ctx, a, t); err != nil {
			return nil, err
		}
		for j := range out.Lines {
			if out.Lines[j].BeatID == t.BeatID && out.Lines[j].State == PlacedLineIn {
				out.Lines[j].TakeID = t.ID
			}
		}
		put(t.BeatID, map[string]any{"take": takeRef(t), "anchor": *t.Pin, "state": "recorded"})
	}

	// The lines this read was not about are listed too, unchanged, so a review
	// shows the whole script (a re-recorded line between the ones around it).
	if !fresh {
		listed := map[string]bool{}
		for _, l := range out.Lines {
			listed[l.BeatID] = true
		}
		for i, b := range rc.spec.Voiceover.Beats {
			if listed[b.ID] {
				continue
			}
			pl := PlacedLine{BeatID: b.ID, Line: i + 1, Text: b.Text, State: PlacedKept, Placement: Placement(rc.spec, b.Anchor)}
			if i < len(rc.beats) && rc.beats[i].Problem == "" {
				pl.At = fpsOf(rc.beats[i].Start)
			}
			out.Lines = append(out.Lines, pl)
		}
		sort.SliceStable(out.Lines, func(x, y int) bool { return out.Lines[x].Line < out.Lines[y].Line })
	}

	var eops []editspec.Op
	shell := map[string]any{}
	if sp.WPM >= 60 && sp.WPM <= 320 {
		shell["settings"] = map[string]any{"speakingRateWpm": sp.WPM, "rateSource": "measured"}
	}
	if fresh {
		v := editspec.Voiceover{Beats: beats}
		if rc.spec.Voiceover != nil {
			v = *editspec.Clone(&editspec.Spec{Voiceover: rc.spec.Voiceover}).Voiceover
			v.Beats = beats
		}
		v.Layout, v.Continuous = "", nil
		for k := range v.Beats {
			if c, ok := changes[v.Beats[k].ID]; ok {
				setBeat(&v.Beats[k], c.set)
			}
		}
		if s, ok := shell["settings"].(map[string]any); ok {
			st := editspec.VoiceoverSettings{}
			if v.Settings != nil {
				st = *v.Settings
			}
			st.SpeakingRateWPM, st.RateSource = s["speakingRateWpm"].(float64), "measured"
			v.Settings = &st
		}
		eops = []editspec.Op{{Op: "setScript", Voiceover: &v}}
	} else {
		if rc.spec.Continuous() {
			// One continuous recording becomes lines that stay where they play now.
			shell["layout"], shell["continuous"] = nil, nil
			for _, op := range continuousToLines(rc) {
				if _, done := changes[op.ID]; !done {
					eops = append(eops, op)
				}
			}
		}
		if len(shell) > 0 {
			raw, _ := jsonRaw(shell)
			eops = append([]editspec.Op{{Op: "updateVoiceover", Set: raw}}, eops...)
		}
		for _, id := range order {
			eops = append(eops, setOp(id, changes[id].set))
		}
	}
	res, note, err := planCheckAfterWork(ctx, a, rc.rev, fmt.Sprintf("Place your take where you said it (%s)", plural(out.Placed, "line")), eops)
	if err != nil {
		return nil, err
	}
	out.Result = res
	if note != "" {
		out.Warnings = append(out.Warnings, note)
	}
	a.Audit(ctx, "voiceover.place", "asset", as.ID, rc.rev.ProjectID, map[string]any{"read": readID, "from": p, "lines": out.Placed, "notHeard": out.NotHeard, "coverage": sp.Coverage})
	out.Summary = placeSummary(out, fresh)
	return out, nil
}

// readCrossfade is how far a line that runs into a re-recorded stretch keeps
// playing past its start, under the new take's fade-in.
const readCrossfade = 0.05

func inScope(scope []int, i int) bool {
	for _, x := range scope {
		if x == i {
			return true
		}
	}
	return false
}

func lineState(ls []PlacedLine, id string) string {
	for _, l := range ls {
		if l.BeatID == id {
			return l.State
		}
	}
	return PlacedKept
}

// placeSummary says what a placement did, in plain words.
func placeSummary(o *PlaceOut, fresh bool) string {
	var b strings.Builder
	switch {
	case fresh:
		fmt.Fprintf(&b, "Your take is in place as %s, each where you said it.", plural(o.Placed, "line"))
	case o.Where == "planned":
		fmt.Fprintf(&b, "%s back at their planned spots.", upper(plural(o.Placed, "line")))
	default:
		fmt.Fprintf(&b, "Your take is in place: %s where you said %s.", plural(o.Placed, "line"), map[bool]string{true: "it", false: "them"}[o.Placed == 1])
	}
	moved := 0
	for _, l := range o.Lines {
		if l.State == PlacedLineIn && l.Moved != nil && math.Abs(*l.Moved) >= 0.3 && o.Where != "planned" {
			moved++
			if moved <= 2 {
				dir := "later"
				if *l.Moved < 0 {
					dir = "earlier"
				}
				fmt.Fprintf(&b, " %s starts %s %s than planned.", editspec.LineName(l.Line-1, l.Text), seconds(math.Abs(*l.Moved)), dir)
			}
		}
	}
	if moved == 3 {
		b.WriteString(" 1 more line moved from its planned spot.")
	} else if moved > 3 {
		fmt.Fprintf(&b, " %d more lines moved from their planned spots.", moved-2)
	}
	if o.NotHeard == 1 {
		b.WriteString(" 1 line wasn't heard: it follows the line before.")
	} else if o.NotHeard > 1 {
		fmt.Fprintf(&b, " %d lines weren't heard: each follows the line before.", o.NotHeard)
	}
	if o.Result != nil {
		b.WriteString(hintApply)
	}
	return b.String()
}

// placeAgain puts an earlier read back: each of its lines where it was spoken,
// or at the spot it was planned for.
func placeAgain(ctx context.Context, a *app.App, rc *revCtx, in *PlaceIn) (*PlaceOut, error) {
	readID := in.Read
	if ids.Valid(models.VoiceTakePrefix, readID) {
		var t models.VoiceTake
		if err := a.DB.GetByID(ctx, store.CVoiceTakes, a.WorkspaceID(), readID, &t); err != nil || t.ReadID == "" {
			return nil, apperr.New(apperr.NotFound, "That take wasn't recorded while the video played, so it can't be put back where it was spoken.").WithDetails(map[string]any{"take": readID})
		}
		readID = t.ReadID
	}
	var ts []models.VoiceTake
	if err := a.DB.Find(ctx, store.CVoiceTakes, a.WorkspaceID(), bson.M{"projectId": rc.rev.ProjectID, "readId": readID}, &ts, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}})); err != nil {
		return nil, err
	}
	if len(ts) == 0 {
		return nil, apperr.New(apperr.NotFound, "That live take isn't in this project.").WithDetails(map[string]any{"read": readID})
	}
	if rc.spec.Voiceover == nil {
		return nil, apperr.New(apperr.InvalidInput, "This cut has no narration.")
	}
	where := in.Where
	if where == "" {
		where = "recorded"
	}
	var only map[string]bool
	if len(in.Lines) > 0 {
		idx, err := rc.selectBeats(in.Lines, nil)
		if err != nil {
			return nil, err
		}
		only = map[string]bool{}
		for _, i := range idx {
			only[rc.spec.Voiceover.Beats[i].ID] = true
		}
	}
	out := &PlaceOut{ReadID: readID, Where: where, Lines: []PlacedLine{}}
	var eops []editspec.Op
	fps := rc.fps()
	first := true
	for _, t := range ts {
		i := beatIndex(rc.spec.Voiceover.Beats, t.BeatID)
		if i < 0 {
			out.Warnings = append(out.Warnings, fmt.Sprintf("A line of that take (“%s”) is no longer in the script", clipText(t.Text, 40)))
			continue
		}
		b := rc.spec.Voiceover.Beats[i]
		if only != nil && !only[b.ID] {
			continue
		}
		if b.Locked {
			out.Warnings = append(out.Warnings, beatName(i, b)+" is locked, so it stays where it is")
			continue
		}
		an := t.Pin
		if where == "planned" {
			an = t.Planned
		}
		if an == nil {
			continue
		}
		set := map[string]any{"anchor": *an, "state": "recorded"}
		if b.Take == nil || b.Take.TakeID != t.ID {
			set["take"] = takeRef(&t)
		}
		pl := PlacedLine{BeatID: b.ID, Line: i + 1, Text: b.Text, State: PlacedLineIn, In: t.In, Out: t.Out, TakeID: t.ID, Placement: Placement(rc.spec, *an)}
		pe := -1
		if i > 0 && rc.beats[i-1].Problem == "" {
			pe = rc.beats[i-1].End()
		}
		if f, prob := editspec.AnchorFrame(rc.spec, rc.clips, *an, pe); prob == "" {
			pl.At = round3(float64(f) / fps)
		}
		if t.Planned != nil {
			pl.Planned = Placement(rc.spec, *t.Planned)
		}
		if first || pl.At < out.From {
			out.From = pl.At
		}
		first = false
		out.To = math.Max(out.To, pl.At+(t.Out-t.In))
		out.Lines = append(out.Lines, pl)
		out.Placed++
		eops = append(eops, setOp(b.ID, set))
	}
	if len(eops) == 0 {
		out.Summary = "Nothing to change: no line of that take can be placed."
		return out, nil
	}
	instruction := "Put the lines of a live take where you said them"
	if where == "planned" {
		instruction = "Put the lines of a live take at their planned spots"
	}
	res, err := planCheck(ctx, a, rc.rev, instruction, eops)
	if err != nil {
		return nil, err
	}
	out.Result = res
	out.Summary = placeSummary(out, false)
	return out, nil
}

// plannedFor is where a line was meant to start before a read placed it: its
// start point now, unless it still sits where an earlier read put it (then that
// read's plan).
func plannedFor(ctx context.Context, a *app.App, b editspec.Beat) *editspec.Anchor {
	an := b.Anchor
	if b.Take != nil && b.Take.TakeID != "" {
		var t models.VoiceTake
		if a.DB.GetByID(ctx, store.CVoiceTakes, a.WorkspaceID(), b.Take.TakeID, &t) == nil && t.ReadID != "" && t.Planned != nil && t.Pin != nil && sameAnchor(*t.Pin, b.Anchor) {
			p := *t.Planned
			return &p
		}
	}
	return &an
}

func sameAnchor(x, y editspec.Anchor) bool {
	a, _ := jsonRaw(x)
	b, _ := jsonRaw(y)
	return string(a) == string(b)
}

// continuousToLines turns one continuous recording into lines that each keep
// the place they play at now (tied to the shot there), so a read can replace
// part of it.
func continuousToLines(rc *revCtx) []editspec.Op {
	c := rc.spec.Voiceover.Continuous
	var out []editspec.Op
	for _, rb := range rc.beats {
		b := rc.spec.Voiceover.Beats[rb.Index]
		if rb.Problem != "" || b.Take == nil || b.Take.AssetID != c.AssetID || b.Locked {
			continue
		}
		pin := editspec.PinAnchor(rc.spec, rc.clips, float64(rb.Start)/rc.fps())
		out = append(out, setOp(b.ID, map[string]any{"anchor": pin}))
	}
	return out
}

// setBeat applies an updateBeat-style change to a beat in place (for a whole
// new script).
func setBeat(b *editspec.Beat, set map[string]any) {
	for k, v := range set {
		switch k {
		case "take":
			switch t := v.(type) {
			case *editspec.TakeRef:
				b.Take = t
			case editspec.TakeRef:
				b.Take = &t
			default:
				b.Take = nil
			}
		case "anchor":
			if an, ok := v.(editspec.Anchor); ok {
				b.Anchor = an
			}
		case "state":
			if s, ok := v.(string); ok {
				b.State = s
			}
		}
	}
}

// transcriptSegments loads an asset's transcript segments in order.
func transcriptSegments(ctx context.Context, a *app.App, assetID string) ([]models.TranscriptSegment, error) {
	var segs []models.TranscriptSegment
	err := a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": assetID}, &segs, options.Find().SetSort(bson.D{{Key: "idx", Value: 1}}))
	return segs, err
}

// readEnvelope measures a recording's loudness every 10 ms (decoded by ffmpeg
// at 8 kHz), to put the cuts between lines in real pauses. nil when it cannot.
func readEnvelope(ctx context.Context, a *app.App, as *models.Asset) *editspec.Envelope {
	if !a.Tools.Has(tools.FFmpeg) {
		return nil
	}
	path, err := a.Cache.Ensure(ctx, a.Media, as.StorageKey, as.SHA256, as.Size, nil)
	if err != nil {
		return nil
	}
	const rate, per = 8000, 80
	pr, pw := io.Pipe()
	errc := make(chan error, 1)
	go func() {
		_, err := a.Tools.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: []string{"-hide_banner", "-nostdin", "-loglevel", "error", "-i", path, "-map", "0:a:0", "-ac", "1", "-ar", fmt.Sprint(rate), "-f", "s16le", "-"}, Stdout: pw, Background: true})
		pw.CloseWithError(err)
		errc <- err
	}()
	env := &editspec.Envelope{Step: float64(per) / rate}
	buf := make([]byte, 16384)
	var carry []byte
	var sum float64
	n := 0
	for {
		k, rerr := pr.Read(buf)
		data := append(carry, buf[:k]...)
		usable := len(data) - len(data)%2
		for i := 0; i < usable; i += 2 {
			v := float64(int16(binary.LittleEndian.Uint16(data[i:]))) / 32768
			sum += v * v
			n++
			if n == per {
				env.RMS = append(env.RMS, math.Sqrt(sum/per))
				sum, n = 0, 0
			}
		}
		carry = append([]byte(nil), data[usable:]...)
		if rerr != nil {
			break
		}
	}
	if err := <-errc; err != nil || len(env.RMS) == 0 {
		return nil
	}
	return env
}

func clipText(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

// ReadView is a live read of the project, for A/B between reads.
type ReadView struct {
	ReadID string `json:"readId"`
	// Lines it covers (numbers in the script now) and how many play it now.
	Lines     []int   `json:"lines"`
	InUse     int     `json:"inUse"`
	From      float64 `json:"from"` // where it was spoken on the timeline it was recorded on
	CreatedAt string  `json:"createdAt"`
	Summary   string  `json:"summary"` // plain words: "Lines 1–3, recorded 14:02"
}

// projectReads lists the live reads of a project, newest first, with what
// they cover in the cut s.
func projectReads(ctx context.Context, a *app.App, projectID string, s *editspec.Spec) []ReadView {
	var ts []models.VoiceTake
	_ = a.DB.Find(ctx, store.CVoiceTakes, a.WorkspaceID(), bson.M{"projectId": projectID, "readId": bson.M{"$exists": true, "$ne": ""}}, &ts,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(400))
	byRead := map[string]*ReadView{}
	var order []string
	playing := map[string]bool{}
	lineOf := map[string]int{}
	if s != nil && s.Voiceover != nil {
		for i, b := range s.Voiceover.Beats {
			lineOf[b.ID] = i + 1
			if b.Take != nil && b.Take.TakeID != "" {
				playing[b.Take.TakeID] = true
			}
		}
	}
	for _, t := range ts {
		r := byRead[t.ReadID]
		if r == nil {
			r = &ReadView{ReadID: t.ReadID, Lines: []int{}, From: math.Inf(1), CreatedAt: t.CreatedAt.UTC().Format("2006-01-02T15:04:05Z")}
			byRead[t.ReadID] = r
			order = append(order, t.ReadID)
		}
		if n := lineOf[t.BeatID]; n > 0 {
			r.Lines = append(r.Lines, n)
		}
		if playing[t.ID] {
			r.InUse++
		}
		if t.PinAt != nil && *t.PinAt < r.From {
			r.From = *t.PinAt
		}
	}
	out := []ReadView{}
	for _, id := range order {
		r := byRead[id]
		sort.Ints(r.Lines)
		if math.IsInf(r.From, 1) {
			r.From = 0
		}
		r.Summary = linesWords(r.Lines)
		out = append(out, *r)
		if len(out) == 12 {
			break
		}
	}
	return out
}

// linesWords says which lines, in words: "Line 3", "Lines 1–3", "Lines 1, 3 and 5".
func linesWords(ls []int) string {
	switch len(ls) {
	case 0:
		return "No line of the script now"
	case 1:
		return fmt.Sprintf("Line %d", ls[0])
	}
	run := true
	for i := 1; i < len(ls); i++ {
		if ls[i] != ls[i-1]+1 {
			run = false
		}
	}
	if run {
		return fmt.Sprintf("Lines %d–%d", ls[0], ls[len(ls)-1])
	}
	parts := make([]string, len(ls))
	for i, n := range ls {
		parts[i] = fmt.Sprint(n)
	}
	return "Lines " + strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

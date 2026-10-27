package voiceover

import (
	"context"
	"fmt"
	"math"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// AlignIn fits a recording (an ingested, transcribed audio asset) to the script.
type AlignIn struct {
	RevisionID string `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	AssetID    string `json:"assetId" jsonschema:"pattern=^ast_[0-9a-z]{26}$"`
	// Layout: beats (default) splits the recording at each line and places every
	// piece at its line's spot; continuous plays it as one piece from the first line.
	Layout string `json:"layout,omitempty" jsonschema:"enum=beats,enum=continuous"`
	// Source: imported (default) or recorded.
	Source string `json:"source,omitempty" jsonschema:"enum=imported,enum=recorded"`
	// Beats limits the alignment to these lines (IDs or numbers); default all.
	Beats []string `json:"beats,omitempty"`
	// Apply also puts the placed recording in the cut as a new version.
	Apply bool `json:"apply,omitempty" jsonschema:"description=Also put the result in the cut as a new version (applies the plan it checked)."`
}

// LineMatch is how one line was found in the recording.
type LineMatch struct {
	BeatID  string   `json:"beatId"`
	Line    int      `json:"line"`
	Found   bool     `json:"found"`
	From    float64  `json:"from"`
	To      float64  `json:"to"`
	Match   float64  `json:"match"` // share of the line's words heard
	Missing []string `json:"missing,omitempty"`
	Extra   []string `json:"extra,omitempty"`
	Message string   `json:"message,omitempty"` // plain words when it does not match the script
}

// AlignOut is the alignment and the plan that places the recording.
type AlignOut struct {
	Lines       []LineMatch `json:"lines"`
	Coverage    float64     `json:"coverage"`
	MeasuredWPM float64     `json:"measuredWpm"`
	Layout      string      `json:"layout"`
	Takes       []TakeView  `json:"takes"`
	Warnings    []string    `json:"warnings,omitempty"`
	Result      *PlanResult `json:"result,omitempty"`
	Applied     *Applied    `json:"applied,omitempty"`
	Summary     string      `json:"summary"`
	JobID       string      `json:"jobId,omitempty"`
}

// transcriptWords loads an asset's word-timed transcript.
func transcriptWords(ctx context.Context, a *app.App, assetID string) ([]editspec.TranscriptWord, error) {
	var segs []models.TranscriptSegment
	if err := a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": assetID}, &segs, options.Find().SetSort(bson.D{{Key: "idx", Value: 1}})); err != nil {
		return nil, err
	}
	var out []editspec.TranscriptWord
	for _, s := range segs {
		for _, w := range s.Words {
			out = append(out, editspec.TranscriptWord{Text: w.Text, From: w.From, To: w.To})
		}
	}
	return out, nil
}

func align(ctx context.Context, a *app.App, in *AlignIn) (*AlignOut, error) {
	rc, err := load(ctx, a, in.RevisionID)
	if err != nil {
		return nil, err
	}
	idx, err := rc.selectBeats(in.Beats, nil)
	if err != nil {
		return nil, err
	}
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
		return nil, apperr.New(apperr.NotFound, "That recording isn't in this workspace.").WithDetails(map[string]any{"assetId": in.AssetID})
	}
	if !as.Media.HasAudio {
		return nil, apperr.New(apperr.UnsupportedMedia, "%s has no sound.", as.Name)
	}
	if as.Transcript == nil || as.Transcript.State != "available" {
		state := "not transcribed"
		if as.Transcript != nil {
			state = as.Transcript.State
		}
		msg := fmt.Sprintf("MetaRush is still writing down the words of %s, so it can't match them to the script yet.", as.Name)
		hint := "Wait until its words are ready, then try again."
		if state == "failed" {
			msg = fmt.Sprintf("MetaRush couldn't write down the words of %s, so it can't match them to the script.", as.Name)
			hint = "See what went wrong in Activity, then try again."
		}
		return nil, apperr.New(apperr.Conflict, "%s", msg).WithHint(hint).
			WithDetails(map[string]any{"transcript": state}).WithCommand(fmt.Sprintf("metarush footage analyze --asset %s --wait", as.ID))
	}
	words, err := transcriptWords(ctx, a, as.ID)
	if err != nil {
		return nil, err
	}
	if len(words) == 0 {
		return nil, apperr.New(apperr.Validation, "No speech was found in %s.", as.Name)
	}
	beats := make([]editspec.Beat, len(idx))
	for k, i := range idx {
		beats[k] = rc.spec.Voiceover.Beats[i]
	}
	if err := step(ctx, 0, 2, "Matching your recording to the script"); err != nil {
		return nil, err
	}
	al := editspec.AlignScript(beats, words, as.Media.Duration)
	source := in.Source
	if source == "" {
		source = models.TakeImported
	}
	layout := in.Layout
	if layout == "" {
		layout = editspec.LayoutBeats
	}
	out := &AlignOut{Lines: []LineMatch{}, Coverage: al.Coverage, MeasuredWPM: al.WPM, Layout: layout, Takes: []TakeView{}}
	var eops []editspec.Op
	var firstFound *editspec.BeatAlignment
	firstIdx := -1
	for k, ba := range al.Beats {
		i := idx[k]
		b := beats[k]
		lm := LineMatch{BeatID: b.ID, Line: i + 1, Found: ba.Found, From: ba.From, To: ba.To, Missing: ba.Missing, Extra: ba.Extra}
		if ba.Total > 0 {
			lm.Match = round3(float64(ba.Matched) / float64(ba.Total))
		}
		switch {
		case !ba.Found:
			lm.Message = fmt.Sprintf("%s wasn't found in the recording", beatName(i, b))
			out.Warnings = append(out.Warnings, lm.Message)
		case lm.Match < 0.85 || len(ba.Missing) > 2 || len(ba.Extra) > 3:
			lm.Message = mismatchWords(beatName(i, b), ba.Missing, ba.Extra)
			out.Warnings = append(out.Warnings, lm.Message)
		}
		out.Lines = append(out.Lines, lm)
		if !ba.Found {
			continue
		}
		if firstFound == nil {
			firstFound, firstIdx = &al.Beats[k], i
		}
		match := lm.Match
		take := &models.VoiceTake{ProjectID: rc.rev.ProjectID, BeatID: b.ID, Text: b.Text, Source: source, AssetID: as.ID, In: ba.From, Out: ba.To,
			Duration: round3(ba.To - ba.From), Words: ba.Matched, Timing: "word", Match: &match}
		if take.Duration > 0 {
			take.WPM = math.Round(float64(ba.Matched) / take.Duration * 60)
		}
		if err := insertTake(ctx, a, take); err != nil {
			return nil, err
		}
		out.Takes = append(out.Takes, TakeView{VoiceTake: *take, Chosen: true, Summary: takeSummary(*take)})
		eops = append(eops, setOp(b.ID, map[string]any{"take": takeRef(take), "state": source}))
	}
	if firstFound == nil {
		return nil, apperr.New(apperr.Validation, "None of the script's lines were found in %s.", as.Name).WithDetails(out)
	}
	// A recording added from the dashboard arrives as an ordinary file: mark it
	// as narration, as the CLI's import does.
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$addToSet": bson.M{"tags": bson.M{"$each": []string{"voiceover", source}}}})
	settings := map[string]any{"speakingRateWpm": al.WPM, "rateSource": "measured"}
	if rc.spec.Voiceover.Settings != nil {
		if p := rc.spec.Voiceover.Settings.FitPolicy; p != "" {
			settings["fitPolicy"] = p
		}
		if g := rc.spec.Voiceover.Settings.Gap; g > 0 {
			settings["gap"] = g
		}
	}
	if al.WPM < 60 || al.WPM > 320 {
		delete(settings, "speakingRateWpm")
		delete(settings, "rateSource")
	}
	shell := map[string]any{"settings": settings}
	if layout == editspec.LayoutContinuous {
		// Start the recording so its first line lands exactly on that line's spot.
		an := rc.spec.Voiceover.Beats[firstIdx].Anchor
		if an.Kind == editspec.AnchorAfter {
			an = editspec.Anchor{Kind: editspec.AnchorTime, At: ptr(math.Max(0, float64(rc.beats[firstIdx].Start)/rc.fps()))}
		}
		an.Offset = round3(an.Offset - firstFound.From)
		shell["layout"] = editspec.LayoutContinuous
		shell["continuous"] = editspec.ContinuousVO{AssetID: as.ID, In: 0, Out: round3(as.Media.Duration), Anchor: an, Source: source}
	} else {
		shell["layout"] = nil
		shell["continuous"] = nil
	}
	setShell, _ := jsonRaw(shell)
	eops = append([]editspec.Op{{Op: "updateVoiceover", Set: setShell}}, eops...)
	res, note, err := planCheckAfterWork(ctx, a, rc.rev, fmt.Sprintf("Place the recording %s on %s (%s)", as.Name, plural(len(out.Takes), "narration line"), layoutWords(layout)), eops)
	if err != nil {
		return nil, err
	}
	out.Result = res
	if note != "" {
		out.Warnings = append(out.Warnings, note)
	}
	a.Audit(ctx, "voiceover.align", "asset", as.ID, rc.rev.ProjectID, map[string]any{"lines": len(out.Takes), "coverage": al.Coverage, "wpm": al.WPM, "layout": layout})
	out.Summary = fmt.Sprintf("Matched %d of %s in the recording (%.0f%% of the script's words); the narrator speaks about %.0f words a minute.",
		len(out.Takes), plural(len(idx), "line"), al.Coverage*100, al.WPM)
	return out, nil
}

func layoutWords(l string) string {
	if l == editspec.LayoutContinuous {
		return "played as one piece"
	}
	return "split at each line"
}

func mismatchWords(who string, missing, extra []string) string {
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, "it leaves out “"+strings.Join(limit(missing, 6), " ")+"”")
	}
	if len(extra) > 0 {
		parts = append(parts, "it adds “"+strings.Join(limit(extra, 6), " ")+"”")
	}
	return who + " doesn't match the script: " + strings.Join(parts, " and ")
}

func limit(xs []string, n int) []string {
	if len(xs) <= n {
		return xs
	}
	return append(append([]string{}, xs[:n]...), "…")
}

func ptr(v float64) *float64 { return &v }

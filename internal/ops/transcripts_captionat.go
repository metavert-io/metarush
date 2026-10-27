package ops

import (
	"context"
	"math"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// Fixing a caption from the review player (D145): a click on a caption asks
// which words of which footage it shows; the fix goes to that stretch of the
// transcript (the version keeps its words until a new version).

// CaptionsAtIn names a caption: the one on screen at a frame of a version's video.
type CaptionsAtIn struct {
	RevisionID string `json:"revisionId"`
	Frame      int    `json:"frame,omitempty" jsonschema:"minimum=0,description=A frame of the video; the caption on screen then."`
	// At is the same moment in seconds into the video (instead of a frame).
	At *float64 `json:"at,omitempty" jsonschema:"minimum=0"`
}

// CaptionsAtOut is the caption and where its words are said.
type CaptionsAtOut struct {
	Found   bool   `json:"found"`
	Line    int    `json:"line"`
	Text    string `json:"text" jsonschema:"description=What the caption says (the version's words)."`
	Speaker string `json:"speaker,omitempty"`
	From    int    `json:"from" jsonschema:"description=First frame it shows."`
	To      int    `json:"to"`
	// AssetID, SourceFrom and SourceTo: the stretch of footage (or narration) it captions.
	AssetID    string  `json:"assetId,omitempty"`
	Clip       string  `json:"clip,omitempty"`
	SourceFrom float64 `json:"sourceFrom"`
	SourceTo   float64 `json:"sourceTo"`
	// Now is what the transcript says there now (it differs once fixed).
	Now string `json:"now"`
	// Fixable: the words come from one transcript (not typed captions or a call's recordings).
	Fixable bool   `json:"fixable"`
	Why     string `json:"why,omitempty"`
}

func init() {
	Def("captions.at", "Which words a caption shows: the caption on screen at a frame of a version, the footage it comes from and what its transcript says there now", Flags{},
		func(ctx context.Context, a *app.App, in *CaptionsAtIn) (*CaptionsAtOut, error) {
			r, _, err := getRevision(ctx, a, in.RevisionID)
			if err != nil {
				return nil, err
			}
			out := &CaptionsAtOut{}
			if r.Spec.Captions == nil || !r.Spec.Captions.Enabled {
				out.Why = "This version shows no captions."
				return out, nil
			}
			if r.Spec.Captions.Source == "manual" {
				out.Why = "These captions were typed into the cut: change them in the cut's captions."
				return out, nil
			}
			spec := r.Spec
			facts, err := loadFactsPinned(ctx, a, r.ProjectID, &spec, transcripts.PinsOf(r))
			if err != nil {
				return nil, err
			}
			plan := editspec.Resolve(&spec, facts, r.ID, r.ProjectID, r.SpecHash)
			if plan.Captions == nil {
				out.Why = "This version shows no captions."
				return out, nil
			}
			if in.At != nil {
				in.Frame = int(math.Round(*in.At * float64(plan.FPS)))
			}
			best, dist := -1, math.MaxInt
			for i, l := range plan.Captions.Lines {
				d := 0
				switch {
				case in.Frame < l.From:
					d = l.From - in.Frame
				case in.Frame >= l.To:
					d = in.Frame - l.To + 1
				}
				if d < dist {
					best, dist = i, d
				}
			}
			if best < 0 || dist > plan.FPS {
				out.Why = "No caption shows there."
				return out, nil
			}
			l := plan.Captions.Lines[best]
			out.Found, out.Line, out.Text, out.Speaker, out.From, out.To = true, best, l.Text, l.Speaker, l.From, l.To
			first := true
			for _, w := range l.Words {
				if w.Src == nil || w.Src.AssetID == "" {
					out.Why = "These captions come from a call's recordings: fix the words in each recording's transcript."
					return out, nil
				}
				if first {
					out.AssetID, out.SourceFrom, out.SourceTo, first = w.Src.AssetID, w.Src.From, w.Src.To, false
				}
				if w.Src.AssetID != out.AssetID {
					out.Why = "This caption shows words from two clips: fix each clip's transcript."
					return out, nil
				}
				out.SourceFrom, out.SourceTo = math.Min(out.SourceFrom, w.Src.From), math.Max(out.SourceTo, w.Src.To)
			}
			if out.AssetID == "" {
				out.Why = "This caption has no timed words to fix."
				return out, nil
			}
			if as := AssetOf(facts, out.AssetID); as != nil {
				out.Clip = as.DisplayTitle()
			}
			segs, err := transcripts.LoadSegments(ctx, a, out.AssetID)
			if err != nil {
				return nil, err
			}
			var now []string
			for _, u := range transcripts.Units(segs) {
				if mid := (u.From + u.To) / 2; mid >= out.SourceFrom-0.001 && mid <= out.SourceTo+0.001 {
					now = append(now, u.Text)
				}
			}
			out.Now, out.Fixable = strings.Join(now, " "), len(now) > 0
			if !out.Fixable {
				out.Why = "The transcript has no words there any more."
			}
			return out, nil
		})
}

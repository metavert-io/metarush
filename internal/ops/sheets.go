package ops

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/derive"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Sheet modes.
const (
	SheetSmart = "smart"
	SheetEven  = "even"
)

// KeyframesIn chooses the frames of a contact sheet without making the image.
type KeyframesIn struct {
	AssetID string  `json:"assetId"`
	From    float64 `json:"from,omitempty"`
	To      float64 `json:"to,omitempty" jsonschema:"description=End of the range (default: the end of the clip)."`
	// Tiles caps the frames (smart: default 20; even: the number of frames, default 12).
	Tiles int    `json:"tiles,omitempty" jsonschema:"minimum=1,maximum=48"`
	Mode  string `json:"mode,omitempty" jsonschema:"enum=smart,enum=even,description=smart (default): one frame per shot, then visual changes, new speakers and coverage. even: evenly spaced."`
}

// SheetTile is one frame of a sheet and why it was chosen.
type SheetTile struct {
	N        int     `json:"n" jsonschema:"description=The tile's number on the sheet (#n)."`
	At       float64 `json:"at" jsonschema:"description=Source seconds."`
	Timecode string  `json:"timecode" jsonschema:"description=M:SS.s as printed on the sheet."`
	Reason   string  `json:"reason" jsonschema:"enum=shot,enum=change,enum=movement,enum=speaker,enum=coverage,enum=even"`
	Why      string  `json:"why" jsonschema:"description=Why this frame, in plain words."`
	Shot     int     `json:"shot,omitempty" jsonschema:"description=Which shot of the range it belongs to (1-based; smart sheets)."`
	Speaker  string  `json:"speaker,omitempty" jsonschema:"description=Who is speaking then (a name, or Speaker N when several voices are unnamed)."`
	Words    string  `json:"words,omitempty" jsonschema:"description=A few words being said then (machine transcription)."`
	Caption  string  `json:"caption" jsonschema:"description=The label under the frame."`
}

// KeyframesOut is the chosen frames.
type KeyframesOut struct {
	AssetID string      `json:"assetId"`
	Title   string      `json:"title"`
	From    float64     `json:"from"`
	To      float64     `json:"to"`
	Mode    string      `json:"mode"`
	Shots   int         `json:"shots" jsonschema:"description=Shots in the range (hard cuts + 1)."`
	Tiles   []SheetTile `json:"tiles"`
	// Gap is the longest stretch without a frame (seconds).
	Gap  float64 `json:"gap"`
	Note string  `json:"note"`
}

// SheetIn builds a contact sheet, optionally for a time range.
type SheetIn struct {
	AssetID string  `json:"assetId"`
	From    float64 `json:"from,omitempty"`
	To      float64 `json:"to,omitempty"`
	// Frames caps the tiles (smart: default 20; even: the number of frames, default 12).
	Frames int    `json:"frames,omitempty" jsonschema:"maximum=48"`
	Mode   string `json:"mode,omitempty" jsonschema:"enum=smart,enum=even,description=smart (default) or even."`
	OutDir string `json:"outDir,omitempty"`
}

// SheetOut is a local image path.
type SheetOut struct {
	Path  string      `json:"path"`
	Mode  string      `json:"mode"`
	Times []float64   `json:"times"`
	Tiles []SheetTile `json:"tiles"`
	Note  string      `json:"note"`
}

var reasonWords = map[string]string{derive.ReasonShot: "new shot", derive.ReasonChange: "the picture is changing", derive.ReasonMovement: "movement",
	derive.ReasonSpeaker: "someone starts speaking", derive.ReasonCoverage: "fills a gap", derive.ReasonEven: "evenly spaced"}

// sheetTimecode is M:SS.s.
func sheetTimecode(t float64) string {
	t = math.Round(t*10) / 10
	m := int(t) / 60
	return fmt.Sprintf("%d:%04.1f", m, t-float64(m*60))
}

// segmentsWithWords reads an asset's transcript with word timings.
func segmentsWithWords(ctx context.Context, a *app.App, assetID string) []models.TranscriptSegment {
	var segs []models.TranscriptSegment
	_ = a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": assetID}, &segs, options.Find().SetSort(bson.D{{Key: "idx", Value: 1}}))
	return segs
}

// speechAt says who is speaking at t and a few words they say then.
func speechAt(segs []models.TranscriptSegment, multiVoice bool, t float64) (who, words string) {
	best, bestD := -1, math.Inf(1)
	for i, s := range segs {
		d := 0.0
		switch {
		case t < s.From:
			d = s.From - t
		case t > s.To:
			d = t - s.To
		}
		if d < bestD {
			best, bestD = i, d
		}
	}
	if best < 0 || bestD > 0.4 {
		return "", ""
	}
	s := segs[best]
	who = s.SpeakerName
	if who == "" && multiVoice {
		who = models.SpeakerDisplayLabel(s.Speaker)
	}
	const n = 5
	if len(s.Words) > 0 {
		i := 0
		for i < len(s.Words)-1 && s.Words[i].To < t-0.1 {
			i++
		}
		var ws []string
		for k := i; k < len(s.Words) && len(ws) < n; k++ {
			ws = append(ws, strings.TrimSpace(s.Words[k].Text))
		}
		words = strings.Join(ws, " ")
		if i+n < len(s.Words) {
			words += "…"
		}
		return who, words
	}
	f := strings.Fields(s.Text)
	if len(f) > n {
		return who, strings.Join(f[:n], " ") + "…"
	}
	return who, strings.Join(f, " ")
}

// keyframes chooses a sheet's frames and labels them.
func keyframes(ctx context.Context, a *app.App, as *models.Asset, in KeyframesIn) (*KeyframesOut, error) {
	mode := in.Mode
	if mode == "" {
		mode = SheetSmart
	}
	if mode != SheetSmart && mode != SheetEven {
		return nil, invalid("Choose smart or even frames for the contact sheet.")
	}
	if as.Kind != "video" || !as.Media.HasVideo {
		return nil, invalid("“%s” has no picture", as.DisplayTitle())
	}
	from, to := in.From, in.To
	if to <= 0 || to > as.Media.Duration {
		to = as.Media.Duration
	}
	if from < 0 || from >= to {
		return nil, apperr.New(apperr.InvalidInput, "Choose a start time before the end time, inside “%s” (it's %.1f seconds long).", as.DisplayTitle(), as.Media.Duration).
			WithDetails(map[string]any{"from": from, "to": to})
	}
	out := &KeyframesOut{AssetID: as.ID, Title: as.DisplayTitle(), From: from, To: to, Mode: mode, Tiles: []SheetTile{}}
	ev := evidence.LoadStored(ctx, a.DB, a.WorkspaceID(), []string{as.ID})[as.ID]
	var cuts []float64
	var changes []derive.KeyframeChange
	if ev != nil {
		if ev.VisualChanges != nil {
			for _, c := range ev.VisualChanges {
				if c.Kind == evidence.ChangeCut {
					cuts = append(cuts, c.At)
				} else {
					changes = append(changes, derive.KeyframeChange{Kind: c.Kind, At: c.At, Score: c.Score, Frames: c.Frames})
				}
			}
		} else {
			for _, sc := range ev.Scenes {
				cuts = append(cuts, sc.At)
			}
		}
	}
	segs := segmentsWithWords(ctx, a, as.ID)
	multi := as.Transcript != nil && as.Transcript.Speakers > 1
	var turns []float64
	prev := ""
	for _, s := range segs {
		who := s.Speaker
		if s.SpeakerName != "" {
			who = s.SpeakerName
		}
		if who != "" && who != prev && prev != "" {
			turns = append(turns, s.From+0.4)
		}
		if who != "" {
			prev = who
		}
	}
	var kfs []derive.Keyframe
	if mode == SheetSmart {
		kfs = derive.PickKeyframes(derive.KeyframeInput{From: from, To: to, Cuts: cuts, Changes: changes, Turns: turns, Cap: in.Tiles})
	} else {
		kfs = derive.EvenKeyframes(from, to, in.Tiles)
	}
	out.Shots = 1
	for _, c := range cuts {
		if c > from+0.05 && c < to-0.05 {
			out.Shots++
		}
	}
	last := from
	for i, k := range kfs {
		who, words := speechAt(segs, multi, k.At)
		t := SheetTile{N: i + 1, At: k.At, Timecode: sheetTimecode(k.At), Reason: k.Reason, Why: reasonWords[k.Reason], Shot: k.Shot, Speaker: who, Words: words}
		switch {
		case who != "" && words != "":
			t.Caption = fmt.Sprintf("%s · %s: “%s”", t.Timecode, who, words)
		case words != "":
			t.Caption = fmt.Sprintf("%s · “%s”", t.Timecode, words)
		case k.Reason != derive.ReasonEven && k.Reason != derive.ReasonCoverage:
			t.Caption = t.Timecode + " · " + t.Why
		default:
			t.Caption = t.Timecode
		}
		out.Tiles = append(out.Tiles, t)
		out.Gap = math.Max(out.Gap, k.At-last)
		last = k.At
	}
	out.Gap = math.Round(math.Max(out.Gap, to-last)*10) / 10
	switch {
	case mode == SheetEven:
		out.Note = "Evenly spaced frames. Labels: #n, M:SS.s source time, who is speaking and a few of their words (machine transcription)."
	case ev == nil || ev.VisualChanges == nil:
		out.Note = "Frames chosen per shot, new speaker and to cover the clip; MetaRush hasn't looked for slow picture changes in this clip yet (analyzing the footage does), so they may be missed. " +
			"Labels: #n, M:SS.s source time, who is speaking and a few of their words."
	default:
		out.Note = "Frames chosen by priority: one per shot, then slow changes and movement, then new speakers, then coverage so no stretch goes unseen too long. " +
			"Labels: #n, M:SS.s source time, who is speaking and a few of their words (machine transcription)."
	}
	return out, nil
}

func init() {
	Def("footage.keyframes", "Choose the frames of a contact sheet (one per shot, visual changes, new speakers, coverage) with their labels, without making the image", Flags{}, func(ctx context.Context, a *app.App, in *KeyframesIn) (*KeyframesOut, error) {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
			return nil, notFound("asset", in.AssetID)
		}
		return keyframes(ctx, a, &as, *in)
	})
	Def("footage.sheet", "Build a labeled contact sheet (whole asset or a time range) as a local image: smart (one frame per shot, visual changes, new speakers, coverage) or even", Flags{LocalOnly: true}, func(ctx context.Context, a *app.App, in *SheetIn) (*SheetOut, error) {
		as, src, _, err := localMedia(ctx, a, in.AssetID, "auto")
		if err != nil {
			return nil, err
		}
		mode := in.Mode
		if mode == "" {
			mode = SheetSmart
		}
		kf, err := keyframes(ctx, a, as, KeyframesIn{AssetID: as.ID, From: in.From, To: in.To, Tiles: in.Frames, Mode: mode})
		if err != nil {
			return nil, err
		}
		dir := in.OutDir
		if dir == "" {
			dir = filepath.Join(a.Cfg.Scratch.Dir, "sheets")
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
		tiles := make([]derive.Tile, len(kf.Tiles))
		times := make([]float64, len(kf.Tiles))
		for i, t := range kf.Tiles {
			tiles[i] = derive.Tile{At: t.At, Badge: fmt.Sprintf("#%d", t.N), Caption: t.Caption}
			times[i] = t.At
		}
		name := fmt.Sprintf("%s-%s-%s-%s.jpg", as.ID, mode, strings.ReplaceAll(sheetTimecode(kf.From), ":", "m"), strings.ReplaceAll(sheetTimecode(kf.To), ":", "m"))
		p := filepath.Join(dir, name)
		if _, err := (&derive.Deriver{T: a.Tools}).LabeledSheet(ctx, src, p, a.Cfg.Scratch.Dir, &as.Media, tiles); err != nil {
			return nil, err
		}
		return &SheetOut{Path: p, Mode: mode, Times: times, Tiles: kf.Tiles, Note: kf.Note}, nil
	})
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("footage.sheet", Offering{ID: "contact-sheets", Group: "understand", Title: "Contact sheets",
		Does:  "A labeled grid of frames from a clip: one per shot, then picture changes and new speakers.",
		Words: []string{"frame grid", "thumbnails", "every shot at a glance"}, Needs: Needs{Works: []string{"editing"}}})
}

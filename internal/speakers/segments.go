package speakers

import (
	"context"
	"math"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voice"
)

// run is a stretch of speech by one voice: consecutive words of one segment
// (gaps under runGap), with Orig set when the words came from a voice that was
// merged into Label.
type run struct {
	From, To float64
	Label    string
	Orig     string
	Words    int
}

const runGap = 0.4

func loadSegments(ctx context.Context, a *app.App, assetID string) ([]models.TranscriptSegment, error) {
	var segs []models.TranscriptSegment
	err := a.DB.Find(ctx, store.CSegments, a.WorkspaceID(), bson.M{"assetId": assetID}, &segs, options.Find().SetSort(bson.D{{Key: "idx", Value: 1}}))
	return segs, err
}

// runsOf turns segments into runs of speech per voice.
func runsOf(segs []models.TranscriptSegment) []run {
	var out []run
	for _, s := range segs {
		if s.Speaker == "" {
			continue
		}
		if len(s.Words) == 0 {
			if s.To > s.From {
				out = append(out, run{From: s.From, To: s.To, Label: s.Speaker, Orig: s.SpeakerFrom, Words: max(1, len(strings.Fields(s.Text)))})
			}
			continue
		}
		cur := run{From: s.Words[0].From, To: s.Words[0].To, Label: s.Speaker, Orig: s.SpeakerFrom, Words: 1}
		for _, w := range s.Words[1:] {
			if w.From-cur.To > runGap {
				out = append(out, cur)
				cur = run{From: w.From, To: w.To, Label: s.Speaker, Orig: s.SpeakerFrom}
			}
			cur.To = math.Max(cur.To, w.To)
			cur.Words++
		}
		out = append(out, cur)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

// spansFor splits runs into a voice's own speech and everyone else's. A voice
// that was merged into another owns the words that came from it; a voice owns
// its words except those that came from voices merged into it.
func spansFor(runs []run, v models.AssetSpeaker) (own, others []voice.Span) {
	for _, r := range runs {
		mine := false
		if v.MergedInto != "" {
			mine = r.Orig == v.Label
		} else {
			mine = r.Label == v.Label && r.Orig == ""
		}
		if mine {
			own = append(own, voice.Span{From: r.From, To: r.To})
		} else {
			others = append(others, voice.Span{From: r.From, To: r.To})
		}
	}
	return own, others
}

// voiceStats recounts a voice's speech from runs.
func voiceStats(runs []run, label string) (seconds float64, words, turns int, first float64) {
	first = -1
	last := ""
	for _, r := range runs {
		if r.Label != label {
			last = r.Label
			continue
		}
		seconds += r.To - r.From
		words += r.Words
		if last != label {
			turns++
		}
		last = label
		if first < 0 || r.From < first {
			first = r.From
		}
	}
	if first < 0 {
		first = 0
	}
	return round3(seconds), words, turns, round3(first)
}

// relabel gives every word in [from, to) to label, splitting segments where
// the speaker changes. Segments without word timing move when their middle is
// inside. It returns the new segments (re-indexed), how many words moved and
// the labels they came from.
func relabel(segs []models.TranscriptSegment, from, to float64, label string) ([]models.TranscriptSegment, int, []string) {
	inside := func(f, t float64) bool { m := (f + t) / 2; return m >= from && m < to }
	var out []models.TranscriptSegment
	moved := 0
	fromSet := map[string]bool{}
	for _, s := range segs {
		if len(s.Words) == 0 {
			if inside(s.From, s.To) && s.Speaker != label {
				fromSet[s.Speaker] = true
				moved += max(1, len(strings.Fields(s.Text)))
				s.Speaker, s.SpeakerName, s.SpeakerFrom = label, "", ""
			}
			out = append(out, s)
			continue
		}
		// Split the segment into pieces of consecutive words with the same new label.
		type piece struct {
			label, orig string
			words       []models.Word
		}
		var pieces []piece
		for _, w := range s.Words {
			l, o := s.Speaker, s.SpeakerFrom
			if inside(w.From, w.To) {
				if l != label {
					fromSet[l] = true
					moved++
				}
				l, o = label, ""
			}
			if n := len(pieces); n > 0 && pieces[n-1].label == l && pieces[n-1].orig == o {
				pieces[n-1].words = append(pieces[n-1].words, w)
			} else {
				pieces = append(pieces, piece{label: l, orig: o, words: []models.Word{w}})
			}
		}
		for i, p := range pieces {
			ns := s
			if i > 0 {
				ns.ID = ids.New(ids.Segment)
			}
			ns.Words = p.words
			ns.From, ns.To = p.words[0].From, p.words[len(p.words)-1].To
			if len(pieces) == 1 {
				ns.From, ns.To = s.From, s.To
			}
			ns.Text = joinWords(p.words)
			if len(pieces) == 1 {
				ns.Text = s.Text
			}
			if ns.Speaker != p.label {
				ns.SpeakerName = ""
			}
			ns.Speaker, ns.SpeakerFrom = p.label, p.orig
			out = append(out, ns)
		}
	}
	for i := range out {
		out[i].Index = i
	}
	var labels []string
	for l := range fromSet {
		if l != "" {
			labels = append(labels, l)
		}
	}
	sort.Strings(labels)
	return out, moved, labels
}

func joinWords(ws []models.Word) string {
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		if t := strings.TrimSpace(w.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

// replaceSegments writes an asset's segments as given (same IDs where kept).
func replaceSegments(ctx context.Context, a *app.App, assetID string, segs []models.TranscriptSegment) error {
	if _, err := a.DB.C(store.CSegments).DeleteMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": assetID}); err != nil {
		return err
	}
	if len(segs) == 0 {
		return nil
	}
	docs := make([]any, 0, len(segs))
	for _, s := range segs {
		docs = append(docs, s)
	}
	_, err := a.DB.C(store.CSegments).InsertMany(ctx, docs)
	return err
}

// addSpan adds s to spans (merging overlaps), sorted.
func addSpan(spans []models.TimeSpan, s models.TimeSpan) []models.TimeSpan {
	all := append(append([]models.TimeSpan(nil), spans...), s)
	sort.Slice(all, func(i, j int) bool { return all[i].From < all[j].From })
	var out []models.TimeSpan
	for _, x := range all {
		if n := len(out); n > 0 && x.From <= out[n-1].To {
			out[n-1].To = math.Max(out[n-1].To, x.To)
			continue
		}
		out = append(out, x)
	}
	return out
}

// cutSpan removes s from spans.
func cutSpan(spans []models.TimeSpan, s models.TimeSpan) []models.TimeSpan {
	var out []models.TimeSpan
	for _, x := range spans {
		if s.To <= x.From || s.From >= x.To {
			out = append(out, x)
			continue
		}
		if s.From > x.From {
			out = append(out, models.TimeSpan{From: x.From, To: s.From})
		}
		if s.To < x.To {
			out = append(out, models.TimeSpan{From: s.To, To: x.To})
		}
	}
	return out
}

// nextLabel is the first speaker label not used in a clip (merged voices count).
func nextLabel(vs []models.AssetSpeaker) (string, int) {
	used := map[string]bool{}
	color := 0
	for _, v := range vs {
		used[v.Label] = true
		color = max(color, v.Color+1)
	}
	for n := 0; ; n++ {
		l := models.SpeakerLabel(n)
		if !used[l] {
			return l, color
		}
	}
}

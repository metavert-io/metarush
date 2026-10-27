package transcribe

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/voice"
)

// Diarizer runs sherpa-onnx offline speaker diarization (pyannote segmentation
// 3.0 + NeMo TitaNet-Small), independently of the speech recognizer, so
// whisper.cpp and Parakeet transcripts get speakers the same way.
type Diarizer struct {
	t        *tools.Set
	seg, emb string
}

// NewDiarizer returns nil when the CLI or the models are not installed.
func NewDiarizer(t *tools.Set) *Diarizer {
	if t == nil || !t.Has(tools.SherpaDiar) {
		return nil
	}
	d := filepath.Join(t.ModelsDir(), "diarization")
	seg, emb := filepath.Join(d, "sherpa-onnx-pyannote-segmentation-3-0", "model.onnx"), filepath.Join(d, voice.ModelFile)
	for _, f := range []string{seg, emb} {
		if _, err := os.Stat(f); err != nil {
			return nil
		}
	}
	return &Diarizer{t: t, seg: seg, emb: emb}
}

// Name describes the diarization stack for status and audit records.
func (d *Diarizer) Name() string { return "sherpa-onnx pyannote-3.0 + titanet-small" }

// EmbeddingModel is the TitaNet model path (also used for voice fingerprints).
func (d *Diarizer) EmbeddingModel() string { return d.emb }

// Library is the sherpa-onnx C library next to the diarization CLI ("" if absent).
func (d *Diarizer) Library() string { return voice.Library(d.t.Path(tools.SherpaDiar)) }

// DiarizeOptions tune one diarization run.
type DiarizeOptions struct {
	// Speakers fixes the number of clusters (--clustering.num-clusters); 0 = automatic.
	Speakers int
	// Threshold is the clustering distance threshold used when Speakers is 0
	// (sherpa-onnx default 0.5; larger means fewer speakers).
	Threshold float64
	// MinOn drops speech segments shorter than this (sherpa-onnx default 0.3 s).
	MinOn float64
	// MinOff merges same-speaker segments separated by less than this (default 0.5 s).
	MinOff float64
}

// DefaultThreshold is MetaRush's clustering threshold. sherpa-onnx's default
// (0.5) split one real speaker into several clusters on real recordings; 0.7
// kept every real speaker separate in our tests and over-split far less
// (docs/speakers.md). Voice.Refine then repairs what is left.
const DefaultThreshold = 0.7

// Run diarizes a 16 kHz mono WAV into speaker turns (absolute seconds).
func (d *Diarizer) Run(ctx context.Context, wav string, o DiarizeOptions) ([]voice.Turn, error) {
	args := []string{"--print-args=false", "--segmentation.pyannote-model=" + d.seg, "--embedding.model=" + d.emb,
		"--segmentation.num-threads=4", "--embedding.num-threads=4"}
	if o.Speakers > 0 {
		args = append(args, fmt.Sprintf("--clustering.num-clusters=%d", o.Speakers))
	} else {
		th := o.Threshold
		if th <= 0 {
			th = DefaultThreshold
		}
		args = append(args, "--clustering.cluster-threshold="+strconv.FormatFloat(th, 'f', 3, 64))
	}
	if o.MinOn > 0 {
		args = append(args, "--min-duration-on="+strconv.FormatFloat(o.MinOn, 'f', 3, 64))
	}
	if o.MinOff > 0 {
		args = append(args, "--min-duration-off="+strconv.FormatFloat(o.MinOff, 'f', 3, 64))
	}
	res, err := d.t.Run(ctx, tools.Spec{Tool: tools.SherpaDiar, Background: true, Args: append(args, wav)})
	if err != nil {
		return nil, err
	}
	return parseDiarization(string(res.Stdout)), nil
}

func parseDiarization(stdout string) []voice.Turn {
	var out []voice.Turn
	for _, l := range strings.Split(stdout, "\n") {
		if m := reDiar.FindStringSubmatch(strings.TrimSpace(l)); m != nil {
			f, _ := strconv.ParseFloat(m[1], 64)
			t, _ := strconv.ParseFloat(m[2], 64)
			out = append(out, voice.Turn{From: f, To: t, Label: m[3]})
		}
	}
	return out
}

// SpeakerFor gives a word the speaker whose turn overlaps it most, or the
// nearest turn edge when none overlaps (the measured bake-off rule).
func SpeakerFor(from, to float64, turns []voice.Turn) string {
	best, bestOv := "", 0.0
	nearest, nd := "", math.MaxFloat64
	for _, t := range turns {
		ov := math.Min(to, t.To) - math.Max(from, t.From)
		if ov > bestOv {
			best, bestOv = t.Label, ov
		}
		dist := math.Min(math.Abs(from-t.To), math.Abs(t.From-to))
		if dist < nd {
			nearest, nd = t.Label, dist
		}
	}
	if best != "" {
		return best
	}
	return nearest
}

// AssignSpeakers labels every segment with speakers from diarization turns and
// splits segments where the speaker changes mid-segment. Word-timed segments
// are labeled per word; segment-timed ones by their largest overlap. It works
// the same for every provider's output. With no turns, speakers are cleared.
func AssignSpeakers(segs []Segment, turns []voice.Turn, dur float64) []Segment {
	if len(turns) == 0 {
		for i := range segs {
			segs[i].Speaker = ""
		}
		return segs
	}
	var out []Segment
	for _, s := range segs {
		if len(s.Words) == 0 {
			s.Speaker = SpeakerFor(s.From, s.To, turns)
			out = append(out, s)
			continue
		}
		var cur *Segment
		flush := func() {
			if cur == nil || len(cur.Words) == 0 {
				cur = nil
				return
			}
			texts := make([]string, len(cur.Words))
			for i, w := range cur.Words {
				texts[i] = w.Text
			}
			cur.Text = strings.Join(texts, " ")
			cur.From, cur.To = cur.Words[0].From, cur.Words[len(cur.Words)-1].To
			if dur > 0 {
				cur.To = math.Min(cur.To, dur)
			}
			out = append(out, *cur)
			cur = nil
		}
		for _, w := range s.Words {
			sp := SpeakerFor(w.From, w.To, turns)
			if cur != nil && cur.Speaker != sp {
				flush()
			}
			if cur == nil {
				cur = &Segment{Speaker: sp}
			}
			cur.Words = append(cur.Words, w)
		}
		flush()
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

// Words flattens stored segments back into time-ordered words (for re-running
// diarization without re-transcribing).
func Words(segs []models.TranscriptSegment) []models.Word {
	var out []models.Word
	for _, s := range segs {
		out = append(out, s.Words...)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].From < out[j].From })
	return out
}

// Package calls is what MetaRush knows about call recordings' pictures
// (D160–D162, docs/call-recordings.md): the footage's picture switches found
// to the frame (the `switches` insight, made by the `switches` job), who is on
// screen between them, and the names a call app printed in the picture. The
// edit check, the quality check and the built-in name bar read it through
// editspec.SwitchFacts.
package calls

import (
	"context"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/visual"
)

// Version of the switches insight; InsightKind its kind.
const (
	Version     = "switches-1"
	InsightKind = "switches"
)

// Detection settings (D160).
const (
	window     = 0.6  // seconds decoded on each side of a switch to refine
	maxWindow  = 6.0  // longest stretch decoded at once
	peakScore  = 0.12 // a frame change at least this big may be a switch
	peakRatio  = 3.0  // … and this many times the window's typical change
	strongPeak = 0.30 // a change this big is a switch even where nothing pointed
	near       = 0.25 // candidates closer than this are one
	exactScene = 0.30 // the evidence's full-rate scene changes this strong are exact switches
)

// Switch is one picture switch of a footage, found to the frame.
type Switch struct {
	At    float64 `json:"at" bson:"at" jsonschema:"description=The first frame of the new picture (seconds in the footage)."`
	Last  float64 `json:"last" bson:"last" jsonschema:"description=The last frame of the old picture."`
	Score float64 `json:"score" bson:"score" jsonschema:"description=How much the picture changed (0–1)."`
	// Was is where the earlier detector had it, when that differed.
	Was *float64 `json:"was,omitempty" bson:"was,omitempty"`
	// By: scene (the evidence's full-rate scene change), refined (decoded
	// around a candidate), found (a strong change nothing pointed at).
	By string `json:"by" bson:"by" jsonschema:"enum=scene,enum=refined,enum=found"`
}

// Stored is the switches insight of one footage.
type Stored struct {
	Version string `json:"version" bson:"version"`
	// Source is the file the frames were read from (the preview copy, or the
	// original when it has none), FPS its frame rate.
	Source string  `json:"source" bson:"source" jsonschema:"enum=proxy,enum=original"`
	FPS    float64 `json:"fps" bson:"fps"`
	// Precision is how far the original's switch can be from At: 0 when the
	// file read has the original's own frames.
	Precision float64  `json:"precision" bson:"precision"`
	Duration  float64  `json:"duration" bson:"duration"`
	Switches  []Switch `json:"switches" bson:"switches"`
	// Index is the on-screen index version whose candidates were used ("" none).
	Index string `json:"index,omitempty" bson:"index,omitempty"`
	// Refined counts the stretches decoded.
	Refined int `json:"refined" bson:"refined"`
}

type candidate struct {
	at    float64
	exact bool
	score float64
}

// Candidates are the moments worth a look: the evidence's cuts (exact when
// they snapped to a full-rate scene change) and strong scene changes, plus the
// on-screen index's text and face boundaries that no cut explains.
func Candidates(ev *evidence.Stored, idx *visual.Index) []candidate {
	var out []candidate
	scenes := []evidence.Scene{}
	if ev != nil {
		scenes = ev.Scenes
	}
	exactAt := func(t float64) (float64, bool) {
		best, ok := 0.0, false
		for _, sc := range scenes {
			if math.Abs(sc.At-t) <= 0.021 && sc.Score >= exactScene {
				best, ok = sc.At, true
			}
		}
		return best, ok
	}
	if ev != nil {
		for _, vc := range ev.VisualChanges {
			if vc.Kind != "cut" {
				continue
			}
			if t, ok := exactAt(vc.At); ok {
				out = append(out, candidate{at: t, exact: true, score: vc.Score})
			} else {
				out = append(out, candidate{at: vc.At, score: vc.Score})
			}
		}
		for _, sc := range scenes {
			// Every strong full-rate change is a switch of its own, even a frame
			// after another (a split-second flash has two).
			if sc.Score >= exactScene && !nearAny(out, sc.At, 0.01) {
				out = append(out, candidate{at: sc.At, exact: true, score: sc.Score})
			}
		}
	}
	if idx != nil {
		var extra []float64
		for _, c := range idx.Cuts {
			extra = append(extra, c)
		}
		for _, t := range idx.Text {
			extra = append(extra, t.From, t.To)
		}
		for _, tr := range idx.Tracks {
			extra = append(extra, tr.From, tr.To)
		}
		for _, t := range extra {
			if t <= 0.1 || (idx.Duration > 0 && t >= idx.Duration-0.1) {
				continue
			}
			if !nearAny(out, t, 1.0) {
				out = append(out, candidate{at: t})
			}
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].at < out[j].at })
	return out
}

func nearAny(cs []candidate, t, d float64) bool {
	for _, c := range cs {
		if math.Abs(c.at-t) < d {
			return true
		}
	}
	return false
}

// frameScore is one decoded frame: its time and how much it changed from the
// frame before (ffmpeg's scene score).
type frameScore struct {
	t, score float64
}

var (
	rePTS   = regexp.MustCompile(`pts_time:([0-9.eE+-]+)`)
	reScore = regexp.MustCompile(`lavfi\.scene_score=([0-9.eE+-]+)`)
)

// parseScores reads ffmpeg's metadata=print output.
func parseScores(b []byte) []frameScore {
	var out []frameScore
	for _, line := range strings.Split(string(b), "\n") {
		if m := rePTS.FindStringSubmatch(line); m != nil {
			t, err := strconv.ParseFloat(m[1], 64)
			if err == nil {
				out = append(out, frameScore{t: t})
			}
		} else if m := reScore.FindStringSubmatch(line); m != nil && len(out) > 0 {
			v, _ := strconv.ParseFloat(m[1], 64)
			out[len(out)-1].score = v
		}
	}
	return out
}

// peaks finds the switches in one decoded window: frames that change much
// more than the window's typical frame and at least as much as their neighbours.
func peaks(fs []frameScore) []int {
	if len(fs) < 3 {
		return nil
	}
	scores := make([]float64, 0, len(fs))
	for _, f := range fs[1:] { // the first frame after a seek has nothing before it
		scores = append(scores, f.score)
	}
	sorted := append([]float64(nil), scores...)
	sort.Float64s(sorted)
	typical := sorted[len(sorted)/2]
	var out []int
	for i := 1; i < len(fs); i++ {
		s := fs[i].score
		if s < peakScore || s < peakRatio*typical+0.02 {
			continue
		}
		// About as big as its neighbours' change, or bigger: a one-frame flash
		// has two switches on neighbouring frames, both of them peaks.
		local := (i == 0 || fs[i-1].score*0.7 <= s) && (i+1 >= len(fs) || fs[i+1].score*0.7 <= s)
		if local {
			out = append(out, i)
		}
	}
	return out
}

// windows groups the candidates that need refining into stretches to decode.
func windows(cs []candidate, duration float64) [][2]float64 {
	var out [][2]float64
	for _, c := range cs {
		if c.exact {
			continue
		}
		lo, hi := math.Max(0, c.at-window), c.at+window
		if duration > 0 {
			hi = math.Min(duration, hi)
		}
		if n := len(out); n > 0 && lo <= out[n-1][1] && hi-out[n-1][0] <= maxWindow {
			out[n-1][1] = math.Max(out[n-1][1], hi)
			continue
		}
		out = append(out, [2]float64{lo, hi})
	}
	return out
}

// Decoder reads the frame changes of one stretch of a file at its full
// frame rate.
type Decoder func(ctx context.Context, from, to float64) ([]frameScore, error)

// FFmpegDecoder decodes stretches of path with ffmpeg: an accurate seek to
// the stretch's start (the frames before it are decoded and dropped), its
// frames' times put back on the footage's clock.
func FFmpegDecoder(ts *tools.Set, path, scratch string) Decoder {
	return func(ctx context.Context, from, to float64) ([]frameScore, error) {
		fs, err := decodeWith(ctx, ts, scratch, []string{"-ss", ff(from), "-t", ff(to - from), "-i", path})
		if err != nil {
			return nil, err
		}
		for i := range fs {
			fs[i].t = r3(fs[i].t + from)
		}
		return fs, nil
	}
}

// decodeWith runs ffmpeg's scene score over every frame of the input given.
func decodeWith(ctx context.Context, ts *tools.Set, scratch string, input []string) ([]frameScore, error) {
	if err := os.MkdirAll(scratch, 0o755); err != nil {
		return nil, err
	}
	tmp, err := os.CreateTemp(scratch, "switches-*.txt")
	if err != nil {
		return nil, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	filter := fmt.Sprintf("select='gte(scene\\,0)',metadata=mode=print:file=%s", escapeFilterPath(tmp.Name()))
	args := append([]string{"-hide_banner", "-nostdin", "-nostats", "-loglevel", "error"}, input...)
	args = append(args, "-map", "0:v:0", "-vf", filter, "-an", "-f", "null", "-")
	if _, err := ts.Run(ctx, tools.Spec{Tool: tools.FFmpeg, Args: args, Background: true}); err != nil {
		return nil, err
	}
	b, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, err
	}
	return parseScores(b), nil
}

func ff(v float64) string { return strconv.FormatFloat(math.Max(0, v), 'f', 3, 64) }

func escapeFilterPath(p string) string {
	p = filepath.ToSlash(p)
	r := strings.NewReplacer(`\`, `\\`, `:`, `\:`, `'`, `\'`, `,`, `\,`, `[`, `\[`, `]`, `\]`, `;`, `\;`)
	return r.Replace(p)
}

// Detect finds a footage's switches (D160): exact candidates as they are, the
// others refined by decoding the stretch around them. fps is the decoded
// file's frame rate (for the last frame before an exact switch).
func Detect(ctx context.Context, cs []candidate, duration, fps float64, decode Decoder) ([]Switch, int, error) {
	frame := 1.0 / math.Max(1, fps)
	var out []Switch
	for _, c := range cs {
		if c.exact {
			out = append(out, Switch{At: r3(c.at), Last: r3(math.Max(0, c.at-frame)), Score: r3(c.score), By: "scene"})
		}
	}
	ws := windows(cs, duration)
	for _, w := range ws {
		fs, err := decode(ctx, w[0], w[1])
		if err != nil {
			return nil, 0, err
		}
		for _, i := range peaks(fs) {
			t := fs[i].t
			var was *float64
			explained := false
			for _, c := range cs {
				if !c.exact && math.Abs(c.at-t) <= window {
					explained = true
					if math.Abs(c.at-t) > 0.5*frame {
						v := r3(c.at)
						was = &v
					}
					break
				}
			}
			by := "refined"
			if !explained {
				if fs[i].score < strongPeak {
					continue
				}
				by = "found"
			}
			out = append(out, Switch{At: r3(t), Last: r3(fs[i-1].t), Score: r3(fs[i].score), Was: was, By: by})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].At < out[j].At })
	// One switch per frame: keep the exact (scene) one, else the stronger.
	var merged []Switch
	for _, s := range out {
		if n := len(merged); n > 0 && s.At-merged[n-1].At < 0.5*frame {
			if merged[n-1].By != "scene" && (s.By == "scene" || s.Score > merged[n-1].Score) {
				merged[n-1] = s
			}
			continue
		}
		merged = append(merged, s)
	}
	if merged == nil {
		merged = []Switch{}
	}
	return merged, len(ws), nil
}

func r3(v float64) float64 { return math.Round(v*1000) / 1000 }

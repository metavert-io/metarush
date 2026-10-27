//go:build voiceeval

package voice

// Evaluation harness for diarization repair and speaker matching on real
// recordings (run with -tags voiceeval; inputs come from the environment so no
// recording or transcript is ever committed):
//
//	EVAL_WAV      16 kHz mono WAV
//	EVAL_DIAR     file with sherpa-onnx diarization lines ("0.5 -- 3.1 speaker_00")
//	EVAL_TRUTH    optional reference: {"turns":[{"speaker":"A","from":0.5,"to":3.1}, …]};
//	              turns without times mark unscored stretches
//	EVAL_SPLIT    optional seconds: enroll people on [0, split), test matching on [split, end)
//	EVAL_MERGE, EVAL_MINCLUSTER, EVAL_ABSORB override DefaultParams

import (
	"bufio"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type evalTruth struct {
	Turns []struct {
		Speaker string       `json:"speaker"`
		From    *float64     `json:"from"`
		To      *float64     `json:"to"`
		Spans   [][2]float64 `json:"spans"` // speech inside the turn (word-level), optional
	} `json:"turns"`
}

var diarLine = regexp.MustCompile(`^(\d+\.\d+) -- (\d+\.\d+) (speaker_\d+)$`)

func readDiar(t *testing.T, path string) []Turn {
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var out []Turn
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if m := diarLine.FindStringSubmatch(strings.TrimSpace(sc.Text())); m != nil {
			a, _ := strconv.ParseFloat(m[1], 64)
			b, _ := strconv.ParseFloat(m[2], 64)
			out = append(out, Turn{From: a, To: b, Label: m[3]})
		}
	}
	return out
}

func envS(name, def string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return def
}

func envF(name string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(name), 64); err == nil {
		return v
	}
	return def
}

// reference turns plus unscored gaps (where the transcript has turns we could not time).
// fullTurns are the reference turns as wholes (for per-turn accuracy).
var fullTurns []refTurn

func loadTruth(t *testing.T, path string) ([]refTurn, []Span) {
	if path == "" {
		return nil, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var tr evalTruth
	if err := json.Unmarshal(b, &tr); err != nil {
		t.Fatal(err)
	}
	var ref []refTurn
	var skip []Span
	lastTo := 0.0
	pendingGap := false
	for _, x := range tr.Turns {
		if x.From == nil || x.To == nil {
			pendingGap = true
			continue
		}
		if pendingGap {
			skip = append(skip, Span{lastTo, *x.From})
			pendingGap = false
		}
		fullTurns = append(fullTurns, refTurn{From: *x.From, To: *x.To, Speaker: x.Speaker})
		if len(x.Spans) > 0 {
			for _, s := range x.Spans {
				ref = append(ref, refTurn{From: s[0], To: s[1], Speaker: x.Speaker})
			}
		} else {
			ref = append(ref, refTurn{From: *x.From, To: *x.To, Speaker: x.Speaker})
		}
		lastTo = *x.To
	}
	return ref, skip
}

func clip(ref []refTurn, hyp []Turn, from, to float64) ([]refTurn, []Turn) {
	var r []refTurn
	for _, x := range ref {
		if x.To > from && x.From < to {
			r = append(r, refTurn{math.Max(x.From, from), math.Min(x.To, to), x.Speaker})
		}
	}
	var h []Turn
	for _, x := range hyp {
		if x.To > from && x.From < to {
			h = append(h, Turn{math.Max(x.From, from), math.Min(x.To, to), x.Label})
		}
	}
	return r, h
}

// dropSkipped removes hypothesis speech inside unscored gaps (DER counts no
// false alarm where the reference is unknown).
func dropSkipped(h []Turn, skip []Span) []Turn {
	var out []Turn
	for _, x := range h {
		pieces := []Span{{x.From, x.To}}
		for _, s := range skip {
			pieces = subtract(pieces, s)
		}
		for _, p := range pieces {
			if p.Len() > 0.01 {
				out = append(out, Turn{p.From, p.To, x.Label})
			}
		}
	}
	return out
}

func report(t *testing.T, name string, ref []refTurn, skip []Span, hyp []Turn, from, to float64) {
	labels := map[string]float64{}
	for _, h := range hyp {
		labels[h.Label] += h.To - h.From
	}
	line := fmt.Sprintf("%-22s clusters=%d", name, len(labels))
	if len(ref) > 0 {
		r, h := clip(ref, dropSkipped(hyp, skip), from, to)
		d := scoreDER(r, h, 0.25, from, to)
		// turn accuracy: majority label of each reference turn maps to its speaker
		var shortOK, shortN, longOK, longN int
		turnsForAcc, _ := clip(fullTurns, nil, from, to)
		for _, x := range turnsForAcc {
			got := d.Mapping[majority(h, x.From, x.To)]
			if x.To-x.From < 1.5 {
				shortN++
				if got == x.Speaker {
					shortOK++
				}
				if os.Getenv("EVAL_SHORT") != "" {
					t.Logf("   short %.2f–%.2f ref=%s got=%s (%s)", x.From, x.To, x.Speaker, got, majority(h, x.From, x.To))
				}
			} else {
				longN++
				if got == x.Speaker {
					longOK++
				}
			}
		}
		line += fmt.Sprintf(" DER=%.1f%% (miss %.1f%%, FA %.1f%%, confusion %.1f%%) turns≥1.5s %d/%d, short %d/%d",
			100*d.DER(), 100*d.Miss/d.Total, 100*d.FalseAlarm/d.Total, 100*d.Confusion/d.Total, longOK, longN, shortOK, shortN)
	}
	t.Log(line)
	var ls []string
	for l, s := range labels {
		ls = append(ls, fmt.Sprintf("%s=%.1fs", l, s))
	}
	sort.Strings(ls)
	t.Log("   ", strings.Join(ls, " "))
}

// TestEvalMatch enrolls the speakers of recording A (labeled with the
// reference, as a person would by listening to one sample each) and matches
// the speakers of recording B against them:
//
//	EVAL_A_WAV, EVAL_A_DIAR, EVAL_A_TRUTH and EVAL_B_WAV, EVAL_B_DIAR, EVAL_B_TRUTH
func TestEvalMatch(t *testing.T) {
	if os.Getenv("EVAL_A_WAV") == "" {
		t.Skip("EVAL_A_* not set")
	}
	home, _ := os.UserHomeDir()
	ex, err := Open(envS("EVAL_LIB", home+"/.local/opt/sherpa-onnx/current/lib/libsherpa-onnx-c-api.dylib"), envS("EVAL_MODEL", home+"/.local/share/metarush-models/diarization/"+ModelFile), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	run := func(prefix string, known []Profile) (*Result, []refTurn) {
		pcm, err := ReadWAV(os.Getenv(prefix + "WAV"))
		if err != nil {
			t.Fatal(err)
		}
		fullTurns = nil
		ref, _ := loadTruth(t, os.Getenv(prefix+"TRUTH"))
		res, err := Refine(readDiar(t, os.Getenv(prefix+"DIAR")), pcm, ex, DefaultParams, known...)
		if err != nil {
			t.Fatal(err)
		}
		return res, ref
	}
	truthOf := func(res *Result, ref []refTurn, label string) string {
		cov := map[string]float64{}
		for _, h := range res.Turns {
			if h.Label != label {
				continue
			}
			for _, r := range ref {
				if ov := math.Min(h.To, r.To) - math.Max(h.From, r.From); ov > 0 {
					cov[r.Speaker] += ov
				}
			}
		}
		best, bv := "?", 0.0
		for s, v := range cov {
			if v > bv {
				best, bv = s, v
			}
		}
		return best
	}
	a, refA := run("EVAL_A_", nil)
	var people []Profile
	for _, c := range a.Clusters {
		who := truthOf(a, refA, c.Label)
		if os.Getenv("EVAL_ENROLL_ALL") == "" && c.Minor {
			continue // a person labels the speakers they are asked about
		}
		if who == "?" || c.Centroid == nil {
			continue
		}
		found := false
		for i := range people {
			if people[i].Name == who { // "labeling two clusters with the same name merges them"
				people[i] = people[i].Enroll(c, "A/"+c.Label)
				found = true
			}
		}
		if !found {
			people = append(people, Profile{ID: who, Name: who}.Enroll(c, "A/"+c.Label))
		}
		t.Logf("A %s → labeled %s (%.1fs, fingerprinted %.1fs)", c.Label, who, c.Seconds, c.Embedded)
	}
	b, refB := run("EVAL_B_", people)
	before := len(b.Clusters)
	ds := Consolidate(b, people, DefaultThresholds)
	var auto, autoOK, suggested, unknown int
	for _, c := range b.Clusters {
		who := truthOf(b, refB, c.Label)
		d := ds[c.Label]
		line := fmt.Sprintf("B %s truth=%s %.1fs (fp %.1fs, minor=%v):", c.Label, who, c.Seconds, c.Embedded, c.Minor)
		switch {
		case d.Auto != nil:
			auto++
			if d.Auto.Name == who {
				autoOK++
			}
			line += fmt.Sprintf(" AUTO %s %.3f", d.Auto.Name, d.Auto.Score)
		case len(d.Suggestions) > 0:
			suggested++
			line += fmt.Sprintf(" SUGGEST %s %.3f", d.Suggestions[0].Name, d.Suggestions[0].Score)
		default:
			unknown++
			line += " unknown"
		}
		for _, m := range Rank(c, people) {
			line += fmt.Sprintf(" | %s=%.3f", m.Name, m.Score)
		}
		t.Log(line)
	}
	t.Logf("B clusters %d → %d after consolidation; auto %d (correct %d), suggested %d, unknown %d", before, len(b.Clusters), auto, autoOK, suggested, unknown)
	if refB != nil {
		pcm, _ := ReadWAV(os.Getenv("EVAL_B_WAV"))
		fullTurns = nil
		ref, skip := loadTruth(t, os.Getenv("EVAL_B_TRUTH"))
		report(t, "B consolidated", ref, skip, b.Turns, 0, float64(len(pcm))/SampleRate)
	}
}

// TestEvalShortWindows measures how short pieces of speech score: profiles are
// enrolled from recording A (by reference label), then every EVAL_WIN-second
// window inside B's reference turns is scored against them. Prints the
// same-person and different-person score distributions.
func TestEvalShortWindows(t *testing.T) {
	if os.Getenv("EVAL_A_WAV") == "" || os.Getenv("EVAL_WIN") == "" {
		t.Skip("EVAL_A_*/EVAL_WIN not set")
	}
	win := envF("EVAL_WIN", 2.5)
	home, _ := os.UserHomeDir()
	ex, err := Open(envS("EVAL_LIB", home+"/.local/opt/sherpa-onnx/current/lib/libsherpa-onnx-c-api.dylib"), envS("EVAL_MODEL", home+"/.local/share/metarush-models/diarization/"+ModelFile), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	pcmA, _ := ReadWAV(os.Getenv("EVAL_A_WAV"))
	fullTurns = nil
	refA, _ := loadTruth(t, os.Getenv("EVAL_A_TRUTH"))
	resA, err := Refine(readDiar(t, os.Getenv("EVAL_A_DIAR")), pcmA, ex, DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	people := map[string]Profile{}
	for _, c := range resA.Clusters {
		if c.Minor || c.Centroid == nil {
			continue
		}
		cov := map[string]float64{}
		for _, h := range resA.Turns {
			if h.Label == c.Label {
				for _, r := range refA {
					if ov := math.Min(h.To, r.To) - math.Max(h.From, r.From); ov > 0 {
						cov[r.Speaker] += ov
					}
				}
			}
		}
		who, bv := "", 0.0
		for s, v := range cov {
			if v > bv {
				who, bv = s, v
			}
		}
		if who != "" {
			people[who] = people[who].Enroll(c, c.Label)
			p := people[who]
			p.ID, p.Name = who, who
			people[who] = p
		}
	}
	pcmB, _ := ReadWAV(os.Getenv("EVAL_B_WAV"))
	fullTurns = nil
	loadTruth(t, os.Getenv("EVAL_B_TRUTH"))
	var same, diff []float64
	var diffWho []string
	for _, r := range fullTurns {
		for from := r.From + 0.2; from+win <= r.To-0.2; from += win {
			v, err := ex.Embed(slice(pcmB, Span{from, from + win}))
			if err != nil {
				continue
			}
			for name, p := range people {
				s := Score(Cluster{Centroid: v}, p).Score
				if name == r.Speaker {
					same = append(same, s)
				} else {
					diff = append(diff, s)
					diffWho = append(diffWho, r.Speaker+"→"+name)
				}
			}
		}
	}
	pct := func(xs []float64, q float64) float64 {
		if len(xs) == 0 {
			return math.NaN()
		}
		c := append([]float64(nil), xs...)
		sort.Float64s(c)
		return c[int(q*float64(len(c)-1))]
	}
	t.Logf("window %.1fs: same-person n=%d min=%.3f p05=%.3f p50=%.3f; different-person n=%d p50=%.3f p95=%.3f p99=%.3f max=%.3f",
		win, len(same), pct(same, 0), pct(same, 0.05), pct(same, 0.5), len(diff), pct(diff, 0.5), pct(diff, 0.95), pct(diff, 0.99), pct(diff, 1))
	for _, th := range []float64{0.40, 0.45, 0.50, 0.55, 0.60, 0.65} {
		var tp, fp int
		for _, s := range same {
			if s >= th {
				tp++
			}
		}
		for _, s := range diff {
			if s >= th {
				fp++
			}
		}
		t.Logf("  threshold %.2f: same-person accepted %d/%d (%.0f%%), different-person accepted %d/%d", th, tp, len(same), 100*float64(tp)/float64(max(1, len(same))), fp, len(diff))
	}
	_ = diffWho
}

func TestEvalRefine(t *testing.T) {
	wav, diar := os.Getenv("EVAL_WAV"), os.Getenv("EVAL_DIAR")
	if wav == "" || diar == "" {
		t.Skip("EVAL_WAV/EVAL_DIAR not set")
	}
	pcm, err := ReadWAV(wav)
	if err != nil {
		t.Fatal(err)
	}
	home, _ := os.UserHomeDir()
	ex, err := Open(envS("EVAL_LIB", home+"/.local/opt/sherpa-onnx/current/lib/libsherpa-onnx-c-api.dylib"), envS("EVAL_MODEL", home+"/.local/share/metarush-models/diarization/"+ModelFile), 4)
	if err != nil {
		t.Fatal(err)
	}
	defer ex.Close()
	raw := readDiar(t, diar)
	ref, skip := loadTruth(t, os.Getenv("EVAL_TRUTH"))
	dur := float64(len(pcm)) / SampleRate
	p := DefaultParams
	p.Merge = envF("EVAL_MERGE", p.Merge)
	p.MinCluster = envF("EVAL_MINCLUSTER", p.MinCluster)
	p.Absorb = envF("EVAL_ABSORB", p.Absorb)
	report(t, "raw", ref, skip, raw, 0, dur)
	res, err := Refine(raw, pcm, ex, p)
	if err != nil {
		t.Fatal(err)
	}
	report(t, "refined", ref, skip, res.Turns, 0, dur)
	t.Logf("refined speakers=%d minor=%d (raw %d, merged %d, absorbed %d turns)", res.Speakers(), len(res.Clusters)-res.Speakers(), res.Raw, res.Merged, res.Absorbed)
	for _, n := range res.Notes {
		t.Log("   note:", n)
	}
	for _, c := range res.Clusters {
		t.Logf("   %s %.1fs (fingerprinted %.1fs, %d exemplars) samples=%v", c.Label, c.Seconds, c.Embedded, len(c.Exemplars), c.Samples)
	}
	// Pairwise cluster similarity (diagnostic for thresholds).
	for i := range res.Clusters {
		for j := i + 1; j < len(res.Clusters); j++ {
			t.Logf("   sim %s~%s = %.3f", res.Clusters[i].Label, res.Clusters[j].Label, Cosine(res.Clusters[i].Centroid, res.Clusters[j].Centroid))
		}
	}
}

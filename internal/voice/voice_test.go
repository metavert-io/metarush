package voice

import (
	"math"
	"testing"
)

// fakeVoices encodes which person speaks in the PCM itself (sample value =
// person id / 100) and returns that person's voice vector, blended by how much
// of the window each person fills, with a little deterministic jitter.
type fakeVoices struct{ vec map[int][]float32 }

// unit is a person's voice: mostly one direction, a little shared texture, so
// different people score about 0.1 and the same person about 0.98.
func unit(seed int, dim int) []float32 {
	v := make([]float32, dim)
	for i := range v {
		v[i] = float32(0.08 * math.Sin(float64(seed*131+i*17)))
	}
	v[(seed*7)%dim] += 1
	Normalize(v)
	return v
}

func newFake(ids ...int) *fakeVoices {
	f := &fakeVoices{vec: map[int][]float32{}}
	for _, id := range ids {
		f.vec[id] = unit(id, 64)
	}
	return f
}

func (f *fakeVoices) Embed(x []float32) ([]float32, error) {
	if len(x) < MinSamples {
		return nil, ErrTooShort
	}
	count := map[int]int{}
	for _, s := range x {
		count[int(math.Round(float64(s)*100))]++
	}
	out := make([]float32, 64)
	for id, n := range count {
		v, ok := f.vec[id]
		if !ok {
			continue
		}
		w := float32(n) / float32(len(x))
		for i := range out {
			out[i] += w * v[i]
		}
	}
	// jitter: same person, slightly different every window
	out[len(x)%64] += 0.15
	Normalize(out)
	return out, nil
}

// pcm builds audio where each span is spoken by a person id (0 = silence).
func pcm(dur float64, spans ...[3]float64) []float32 {
	x := make([]float32, int(dur*SampleRate))
	for _, s := range spans {
		for i := int(s[0] * SampleRate); i < int(s[1]*SampleRate) && i < len(x); i++ {
			x[i] = float32(s[2] / 100)
		}
	}
	return x
}

func labels(res *Result) map[string]float64 {
	out := map[string]float64{}
	for _, t := range res.Turns {
		out[t.Label] += t.To - t.From
	}
	return out
}

func TestRefineFoldsShortSplitOffTurn(t *testing.T) {
	// Two people: the diarizer gives the last short line of
	// person 1 its own label.
	audio := pcm(23, [3]float64{0, 5.3, 1}, [3]float64{6.5, 7.3, 1}, [3]float64{7.9, 20.1, 2}, [3]float64{21.0, 22.7, 1})
	raw := []Turn{{0, 5.3, "speaker_00"}, {6.5, 7.3, "speaker_00"}, {7.9, 20.1, "speaker_01"}, {21.0, 22.7, "speaker_04"}}
	res, err := Refine(raw, audio, newFake(1, 2), DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	if res.Raw != 3 || res.Speakers() != 2 || len(res.Clusters) != 2 {
		t.Fatalf("want 3 raw → 2 speakers, got raw %d, %d speakers, %d clusters (%v)", res.Raw, res.Speakers(), len(res.Clusters), res.Notes)
	}
	last := res.Turns[len(res.Turns)-1]
	if last.Label != res.Turns[0].Label {
		t.Fatalf("short last line should belong to the first speaker: %+v", res.Turns)
	}
}

func TestRefineMergesOnePersonSplitInTwo(t *testing.T) {
	audio := pcm(30, [3]float64{0, 8, 1}, [3]float64{8.5, 15, 2}, [3]float64{15.5, 25, 1}, [3]float64{25.5, 30, 2})
	raw := []Turn{{0, 8, "speaker_00"}, {8.5, 15, "speaker_01"}, {15.5, 25, "speaker_02"}, {25.5, 30, "speaker_01"}}
	res, err := Refine(raw, audio, newFake(1, 2), DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	if res.Speakers() != 2 || res.Merged != 1 {
		t.Fatalf("want 2 speakers after one merge, got %d (merged %d): %v", res.Speakers(), res.Merged, res.Notes)
	}
	if res.Turns[2].Label != res.Turns[0].Label || res.Turns[1].Label == res.Turns[0].Label {
		t.Fatalf("labels %+v", res.Turns)
	}
	if res.Turns[0].Label != "speaker_00" || res.Turns[1].Label != "speaker_01" {
		t.Fatalf("relabel by first appearance: %+v", res.Turns)
	}
}

func TestRefineKeepsDistinctPeopleAndBriefVoices(t *testing.T) {
	// Three people; the third says one 1.8 s line. Nobody is merged, and the
	// brief voice is kept (flagged minor) instead of being given to someone else.
	audio := pcm(40, [3]float64{0, 12, 1}, [3]float64{12.5, 30, 2}, [3]float64{30.5, 32.3, 3}, [3]float64{32.8, 40, 1})
	raw := []Turn{{0, 12, "speaker_00"}, {12.5, 30, "speaker_01"}, {30.5, 32.3, "speaker_02"}, {32.8, 40, "speaker_00"}}
	res, err := Refine(raw, audio, newFake(1, 2, 3), DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Clusters) != 3 || res.Merged != 0 {
		t.Fatalf("want 3 clusters, no merges: %d %v", len(res.Clusters), res.Notes)
	}
	minor := 0
	for _, c := range res.Clusters {
		if c.Minor {
			minor++
			if c.Seconds > 2 {
				t.Fatalf("only the brief voice is minor: %+v", c)
			}
		}
	}
	if minor != 1 || res.Speakers() != 2 {
		t.Fatalf("want 2 speakers + 1 brief voice, got %d speakers, %d minor", res.Speakers(), minor)
	}
}

func TestRefineShortClipKeepsEveryoneAsMainVoices(t *testing.T) {
	// 11.5 s clips of two people: 3.6 s (or 1.7 s) is little speech, but a big
	// part of the clip, so that person is one of the voices to name, not a
	// "brief voice" that is never asked about.
	for _, tc := range []struct {
		name  string
		audio []float32
		raw   []Turn
	}{
		{"second person 3.6 s of 9.7 s", pcm(11.5, [3]float64{0.03, 5.31, 1}, [3]float64{6.49, 7.29, 1}, [3]float64{7.88, 11.5, 2}),
			[]Turn{{0.03, 5.31, "speaker_00"}, {6.49, 7.29, "speaker_00"}, {7.88, 11.5, "speaker_01"}}},
		{"second person 1.7 s of 10.3 s", pcm(11.5, [3]float64{0, 8.6, 1}, [3]float64{9.52, 11.26, 2}),
			[]Turn{{0, 8.6, "speaker_00"}, {9.52, 11.26, "speaker_01"}}},
	} {
		res, err := Refine(tc.raw, tc.audio, newFake(1, 2), DefaultParams)
		if err != nil {
			t.Fatal(err)
		}
		if len(res.Clusters) != 2 || res.Speakers() != 2 {
			t.Fatalf("%s: want 2 main voices, got %d clusters, %d speakers (%v)", tc.name, len(res.Clusters), res.Speakers(), res.Notes)
		}
	}
	// The same 1.7 s line in a 40 s recording is a brief voice.
	audio := pcm(40, [3]float64{0, 20, 1}, [3]float64{20.5, 38, 3}, [3]float64{38.26, 40, 2})
	res, err := Refine([]Turn{{0, 20, "speaker_00"}, {20.5, 38, "speaker_01"}, {38.26, 40, "speaker_02"}}, audio, newFake(1, 2, 3), DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	if res.Speakers() != 2 || len(res.Clusters) != 3 {
		t.Fatalf("want 2 main voices + 1 brief voice, got %d of %d (%v)", res.Speakers(), len(res.Clusters), res.Notes)
	}
}

func TestRefineSamplesAreCleanAndListenable(t *testing.T) {
	audio := pcm(60, [3]float64{0, 20, 1}, [3]float64{19, 40, 2}, [3]float64{40.5, 60, 1})
	raw := []Turn{{0, 20, "speaker_00"}, {19, 40, "speaker_01"}, {40.5, 60, "speaker_00"}} // 1 s of overlap at 19–20
	res, err := Refine(raw, audio, newFake(1, 2), DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range res.Clusters {
		if len(c.Samples) < 2 || len(c.Samples) > 3 {
			t.Fatalf("%s: want 2–3 samples, got %v", c.Label, c.Samples)
		}
		for i, s := range c.Samples {
			if s.Len() < 2 || s.Len() > 5 {
				t.Fatalf("%s sample %v is not 2–5 s", c.Label, s)
			}
			if s.From < 20 && s.To > 19 {
				t.Fatalf("%s sample %v includes overlapping speech", c.Label, s)
			}
			if i > 0 && s.From < c.Samples[i-1].To {
				t.Fatalf("samples overlap or are unordered: %v", c.Samples)
			}
		}
	}
}

func TestRefineWithoutEmbedderTrustsLabels(t *testing.T) {
	raw := []Turn{{5, 6, "speaker_03"}, {0, 4, "speaker_01"}}
	res, err := Refine(raw, pcm(7), nil, DefaultParams)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Clusters) != 2 || res.Turns[0].Label != "speaker_00" || res.Turns[1].Label != "speaker_01" {
		t.Fatalf("want relabeled pass-through: %+v", res.Turns)
	}
}

func TestMatchingThresholdsAndEnrollment(t *testing.T) {
	f := newFake(1, 2, 3)
	enrollFrom := func(person int, from float64) Cluster {
		audio := pcm(from+20, [3]float64{from, from + 20, float64(person)})
		res, err := Refine([]Turn{{from, from + 20, "speaker_00"}}, audio, f, DefaultParams)
		if err != nil {
			t.Fatal(err)
		}
		return res.Clusters[0]
	}
	ada := Profile{ID: "per_ada", Name: "Ada"}.Enroll(enrollFrom(1, 0), "ast_1/speaker_00")
	ben := Profile{ID: "per_ben", Name: "Ben"}.Enroll(enrollFrom(2, 0), "ast_1/speaker_01")
	if len(ada.Exemplars) == 0 || len(ada.Exemplars) > MaxExemplarsPerSource {
		t.Fatalf("enrollment keeps up to %d exemplars per recording, got %d", MaxExemplarsPerSource, len(ada.Exemplars))
	}
	people := []Profile{ada, ben}
	// The same person in a new recording is labeled automatically.
	d := Decide(enrollFrom(1, 3), people, DefaultThresholds)
	if d.Auto == nil || d.Auto.Name != "Ada" {
		t.Fatalf("Ada should be recognized: %+v", d)
	}
	// Someone new is not labeled.
	d = Decide(enrollFrom(3, 0), people, DefaultThresholds)
	if d.Auto != nil {
		t.Fatalf("a stranger was labeled %s", d.Auto.Name)
	}
	// Re-enrolling a source replaces it; forgetting removes it.
	again := ada.Enroll(enrollFrom(1, 5), "ast_1/speaker_00")
	if len(again.Exemplars) != len(ada.Exemplars) {
		t.Fatalf("re-enrolling the same source should replace it: %d vs %d", len(again.Exemplars), len(ada.Exemplars))
	}
	if gone := again.Forget("ast_1/speaker_00"); len(gone.Exemplars) != 0 {
		t.Fatalf("forget left %d exemplars", len(gone.Exemplars))
	}
}

func TestConsolidateMergesTwoVoicesOfOnePerson(t *testing.T) {
	f := newFake(1, 2)
	audio := pcm(40, [3]float64{0, 15, 1}, [3]float64{15.5, 25, 2}, [3]float64{25.5, 40, 1})
	// Pretend the diarizer split person 1 and Refine was told not to merge (high threshold).
	p := DefaultParams
	p.Merge = 2
	res, err := Refine([]Turn{{0, 15, "speaker_00"}, {15.5, 25, "speaker_01"}, {25.5, 40, "speaker_02"}}, audio, f, p)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Clusters) != 3 {
		t.Fatalf("setup: want 3 clusters, got %d", len(res.Clusters))
	}
	ada := Profile{ID: "per_ada", Name: "Ada"}.Enroll(res.Clusters[0], "x")
	dec := Consolidate(res, []Profile{ada}, DefaultThresholds)
	if len(res.Clusters) != 2 {
		t.Fatalf("two voices that are both Ada should be one: %d clusters (%v)", len(res.Clusters), res.Notes)
	}
	if dec["speaker_00"].Auto == nil || res.Turns[2].Label != "speaker_00" {
		t.Fatalf("decisions %+v turns %+v", dec, res.Turns)
	}
}

func TestCosineAndMean(t *testing.T) {
	a, b := []float32{1, 0}, []float32{0, 1}
	if Cosine(a, b) != 0 || math.Abs(Cosine(a, a)-1) > 1e-9 || Cosine(a, nil) != 0 {
		t.Fatal("cosine")
	}
	m := Mean([][]float32{a, b}, []float64{3, 1})
	if m[0] <= m[1] || math.Abs(Cosine(m, m)-1) > 1e-9 {
		t.Fatalf("weighted mean %v", m)
	}
}

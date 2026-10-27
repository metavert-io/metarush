package editspec

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateGolden = flag.Bool("update", false, "rewrite golden files in testdata/")

// legacySpecs are cuts written before split edits, the transition library,
// cut blends and speech leveling existed. Their hashes and resolved plans are
// frozen in testdata/legacy-plans.json (captured from the engine as it was):
// old revisions must keep resolving to exactly the same RenderPlan. One
// deliberate change since: the narration's fit bookkeeping (voiceover.beats
// windowEnd and fit) follows time order, so the voSpec line placed before the
// line above it no longer reports a negative window (D62); sound and timing are
// unchanged.
func legacySpecs() map[string]*Spec {
	out := map[string]*Spec{}
	out["cuts-crossfade-dip"] = baseSpec()

	fade := baseSpec()
	fade.Clips[0].Transition = &Transition{Type: TransitionFade, Duration: 0.6}
	fade.Clips[2].Transition = &Transition{Type: TransitionDip, Duration: 0.8}
	fade.Clips[2].FadeOut = 1.2
	fade.Clips[1].Speed = 1.5
	vol := 0.6
	fade.Clips[0].Volume = &vol
	out["fade-dip-fadeout-speed"] = fade

	caps := baseSpec()
	caps.Captions = &Captions{Enabled: true, Style: CaptionEmphasis, ShowSpeakers: true}
	caps.Audio = []AudioTrack{{ID: "bed", AssetID: astMusic, Role: RoleMusic, In: 0, Out: 20, At: 0, GainDB: ptr(-8), FadeIn: 0.5, FadeOut: 1.5, DuckUnderDialogue: true, DuckDB: ptr(-12), Rights: &Rights{License: "CC0"}}}
	out["captions-music-ducking"] = caps

	vo := voSpec()
	vo.Captions = &Captions{Enabled: true, Source: "both", ShowSpeakers: true}
	vo.Audio = []AudioTrack{{ID: "bed", AssetID: astMusic, Role: RoleMusic, In: 0, Out: 30, At: 0, GainDB: ptr(-6), Rights: &Rights{License: "CC0"}}}
	out["voiceover-captions-music"] = vo

	mix := voSpec()
	mix.Mix = &Mix{Music: &Bus{GainDB: ptr(-3)}, Ducking: &Ducking{MusicUnderVoiceoverDB: ptr(-16)}, Loudness: &MasterLoudness{TargetLUFS: ptr(-14)}}
	mix.Audio = []AudioTrack{{ID: "bed", AssetID: astMusic, Role: RoleMusic, In: 0, Out: 30, At: 0, Rights: &Rights{License: "CC0"}}}
	out["mix-loudness"] = mix

	mute := baseSpec()
	mute.Mix = &Mix{MuteSource: true}
	out["mix-mute-source"] = mute

	emptyMix := baseSpec()
	emptyMix.Mix = &Mix{}
	out["mix-empty-section"] = emptyMix

	for _, s := range out {
		Normalize(s)
	}
	return out
}

type legacyGolden struct {
	Hash string          `json:"hash"`
	Plan json.RawMessage `json:"plan"`
}

// indexedFacts is the same footage after its clips were looked at: a face in
// every clip (D73). Old cuts never asked to follow faces, so nothing may change.
type indexedFacts struct{ loudFacts }

func (indexedFacts) FaceFocus(string, float64, float64) (float64, float64, bool) {
	return 0.8, 0.2, true
}

// Subject: the subject also moves across every clip, with a cut in it (D115).
func (indexedFacts) Subject(string, float64, float64) ([]SubjectSample, []float64, bool) {
	return []SubjectSample{{At: 0.5, X: 0.1, Y: 0.1, Face: true}, {At: 5.5, X: 0.9, Y: 0.9, Face: true}, {At: 12.5, X: 0.2, Y: 0.7}}, []float64{3, 11}, true
}

func TestLegacySpecsResolveIdentically(t *testing.T) {
	checkLegacyPlans(t, voFacts())
}

// The same frozen plans, byte for byte, once the footage has been indexed.
func TestLegacySpecsResolveIdenticallyOnceIndexed(t *testing.T) {
	if *updateGolden {
		t.Skip("the golden file is written from the unindexed facts")
	}
	checkLegacyPlans(t, indexedFacts{voFacts()})
}

func checkLegacyPlans(t *testing.T, f Facts) {
	t.Helper()
	path := filepath.Join("testdata", "legacy-plans.json")
	got := map[string]legacyGolden{}
	for name, s := range legacySpecs() {
		h := Hash(s)
		plan := Resolve(Clone(s), f, "rev_legacy", "prj_legacy", h)
		b, err := json.Marshal(plan)
		if err != nil {
			t.Fatal(err)
		}
		got[name] = legacyGolden{Hash: h, Plan: b}
	}
	if *updateGolden {
		b, _ := json.MarshalIndent(got, "", "  ")
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", path)
		return
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%s missing: %v", path, err)
	}
	var want map[string]legacyGolden
	if err := json.Unmarshal(raw, &want); err != nil {
		t.Fatal(err)
	}
	if len(want) != len(got) {
		t.Fatalf("golden has %d specs, test builds %d", len(want), len(got))
	}
	for name, w := range want {
		g, ok := got[name]
		if !ok {
			t.Errorf("%s: no longer built", name)
			continue
		}
		if g.Hash != w.Hash {
			t.Errorf("%s: spec hash changed: %s → %s (old revisions must keep their hash)", name, w.Hash, g.Hash)
		}
		var a, b any
		_ = json.Unmarshal(w.Plan, &a)
		_ = json.Unmarshal(g.Plan, &b)
		if !equalJSON(a, b) {
			t.Errorf("%s: resolved plan changed for an old spec\n want %s\n  got %s", name, w.Plan, g.Plan)
		}
	}
}

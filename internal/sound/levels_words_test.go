package sound

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/textalign"
	"github.com/metavert-io/metarush/internal/voiceover"
)

func gainOf(m *editspec.Mix, name string) *float64 {
	if v := m.VoiceFor(name); v != nil {
		return v.GainDB
	}
	return nil
}

func fl(v float64) *float64 { return &v }
func yes(b bool) *bool      { return &b }

// levelsRev is a saved cut with one person's level set and a music track.
func levelsRev() *models.Revision {
	g := 3.0
	return &models.Revision{ID: "rev_01levels000000000000000000", Branch: "main", Spec: editspec.Spec{SchemaVersion: 1,
		Output: editspec.Output{Preset: editspec.PresetLandscape, FPS: 30},
		Clips:  []editspec.Clip{{ID: "talk", AssetID: "ast_01talk0000000000000000000", In: 0, Out: 8}},
		Audio:  []editspec.AudioTrack{{ID: "theme", AssetID: "ast_01theme000000000000000000", Role: editspec.RoleMusic}},
		Mix:    &editspec.Mix{Voices: []editspec.VoiceSound{{Name: "Ben", GainDB: &g}}}}}
}

// A sound change is planned as the ops that make it and said in words people
// use: people louder or quieter (within what's sensible), back as recorded,
// a music track's level and dip; what can't be done is refused plainly.
func TestLevelsArePlannedAndSaidPlainly(t *testing.T) {
	title := func(string) string { return "Morning theme" }
	rev := levelsRev()

	eops, what, err := levelsPlan(rev, &LevelsIn{People: []PersonLevel{{Name: "Ada", GainDB: -2.04}, {Name: "Cy", GainDB: 40}}}, title)
	if err != nil {
		t.Fatal(err)
	}
	if len(eops) != 1 || eops[0].Op != "setMix" || eops[0].Mix == nil {
		t.Fatalf("ops %+v", eops)
	}
	if g := gainOf(eops[0].Mix, "Ada"); g == nil || *g != -2 {
		t.Fatalf("Ada %v", g)
	}
	if g := gainOf(eops[0].Mix, "Cy"); g == nil || *g != 12 {
		t.Fatalf("louder than 12 dB is kept at 12: %v", g)
	}
	if g := gainOf(eops[0].Mix, "Ben"); g == nil || *g != 3 {
		t.Fatalf("Ben's level is kept: %v", g)
	}
	if strings.Join(what, "; ") != "make Ada quieter (-2 dB); make Cy louder (+12 dB)" {
		t.Fatalf("words %q", what)
	}
	// Back as recorded: nothing left for Ben, so the mix is gone.
	eops, what, err = levelsPlan(rev, &LevelsIn{People: []PersonLevel{{Name: "Ben", GainDB: 0}}}, title)
	if err != nil || len(eops) != 1 || eops[0].Op != "setMix" || eops[0].Mix != nil || what[0] != "put Ben back at their recorded level" {
		t.Fatalf("back as recorded: %+v %q %v", eops, what, err)
	}
	// A music track's own level and dip.
	eops, what, err = levelsPlan(rev, &LevelsIn{Music: []MusicLevel{{Track: "theme", GainDB: fl(-6.04), Duck: "on", DuckDB: fl(9)}}}, title)
	if err != nil || len(eops) != 1 || eops[0].Op != "updateAudio" || eops[0].ID != "theme" {
		t.Fatalf("music ops %+v %v", eops, err)
	}
	var set map[string]any
	if err := json.Unmarshal(eops[0].Set, &set); err != nil || set["gainDb"] != -6.0 || set["duck"] != true || set["duckDb"] != -9.0 {
		t.Fatalf("music set %s", eops[0].Set)
	}
	if strings.Join(what, "; ") != "set the music “Morning theme” to -6 dB; make “Morning theme” quieter while someone talks; dip “Morning theme” by 9 dB while someone talks" {
		t.Fatalf("music words %q", what)
	}
	_, what, _ = levelsPlan(rev, &LevelsIn{Music: []MusicLevel{{Track: "theme", Duck: "off"}}}, title)
	if len(what) != 1 || what[0] != "keep “Morning theme” at its level while people talk" {
		t.Fatalf("duck off %q", what)
	}
	eops, what, _ = levelsPlan(rev, &LevelsIn{Music: []MusicLevel{{Track: "theme", Duck: "auto"}}}, title)
	if len(eops) != 1 || len(what) != 0 || !strings.Contains(string(eops[0].Set), `"duck":null`) {
		t.Fatalf("duck back to automatic: %+v %q", eops, what)
	}
	// The whole mix: depth, smooth cuts, loudness, the camera sound.
	eops, what, err = levelsPlan(rev, &LevelsIn{Depth: fl(10), SmoothCuts: yes(true), Loudness: "-14", MuteSource: yes(true), LevelSpeech: yes(true)}, title)
	if err != nil || len(eops) == 0 || len(what) == 0 {
		t.Fatalf("mix change: %+v %q %v", eops, what, err)
	}
	var mix *editspec.Mix
	for _, op := range eops {
		if op.Op == "setMix" {
			mix = op.Mix
		}
	}
	if mix == nil || mix.CutBlend == nil || *mix.CutBlend != editspec.DefaultCutBlend || mix.Loudness == nil || mix.Loudness.TargetLUFS == nil || *mix.Loudness.TargetLUFS != -14 {
		t.Fatalf("mix %+v", mix)
	}
	if _, _, err := levelsPlan(rev, &LevelsIn{Loudness: "auto", SmoothCuts: yes(false)}, title); err != nil {
		t.Fatalf("automatic loudness: %v", err)
	}

	for _, c := range []struct {
		name string
		in   LevelsIn
		code string
		says string
	}{
		{"nothing", LevelsIn{}, apperr.InvalidInput, "Nothing to change in the sound."},
		{"no name", LevelsIn{People: []PersonLevel{{Name: "  ", GainDB: 2}}}, apperr.InvalidInput, "Say whose voice level to change."},
		{"too quiet a video", LevelsIn{Loudness: "-40"}, apperr.InvalidInput, "between -31 and -8"},
		{"not a level", LevelsIn{Loudness: "loud"}, apperr.InvalidInput, "between -31 and -8"},
		{"no such music", LevelsIn{Music: []MusicLevel{{Track: "nope", GainDB: fl(-3)}}}, apperr.NotFound, "There is no music or sound called “nope” in this cut."},
	} {
		_, _, err := levelsPlan(rev, &c.in, title)
		if ae := apperr.From(err); err == nil || ae.Code != c.code || !strings.Contains(ae.Message, c.says) {
			t.Errorf("%s: %v", c.name, err)
		}
	}
	if !hasLevelChange(&LevelsIn{Loudness: "-16"}) || hasLevelChange(&LevelsIn{Original: true}) {
		t.Fatal("hasLevelChange")
	}
}

// Levels are said the way people hear them, never in loudness jargon.
func TestLevelWords(t *testing.T) {
	for _, c := range []struct {
		db   float64
		want string
	}{{0, "As recorded"}, {0.2, "As recorded"}, {-0.2, "As recorded"}, {-20, "Much quieter (20 dB)"}, {-18, "Much quieter (18 dB)"}, {-7.5, "Quieter (7.5 dB)"},
		{-6, "A little quieter (6 dB)"}, {-1.5, "A little quieter (1.5 dB)"}, {3, "A little louder (3 dB)"}, {4, "A little louder (4 dB)"}, {6, "Louder (6 dB)"}} {
		got := levelWords(c.db)
		if got != c.want || len(plainwords.Check(got)) > 0 {
			t.Errorf("levelWords(%v) = %q, want %q", c.db, got, c.want)
		}
	}
	for _, c := range []struct {
		g    float64
		want string
	}{{3, "+3 dB"}, {-6.5, "-6.5 dB"}, {0, "0 dB"}} {
		if got := signedDB(c.g); got != c.want {
			t.Errorf("signedDB(%v) = %q", c.g, got)
		}
	}
	if personLevelWords("Ada", 2) != "make Ada louder (+2 dB)" || personLevelWords("Ada", -2) != "make Ada quieter (-2 dB)" || personLevelWords("Ada", 0) != "put Ada back at their recorded level" {
		t.Fatal("person level words")
	}
	if lowerFirst("") != "" || lowerFirst("Louder (6 dB)") != "louder (6 dB)" || upper("") != "" || upper("music") != "Music" {
		t.Fatal("first letters")
	}
}

// The Sound tab's sentences: the automatic help, each clearer voice, and the
// whole cut in one line.
func TestSoundIsSummedUpPlainly(t *testing.T) {
	if got := autoWords(AutoView{}); got != "No automatic help is on." {
		t.Fatalf("no help: %q", got)
	}
	if got := autoWords(AutoView{Ducking: true, Depth: 12, LevelSpeech: true, SmoothCuts: true}); got != "Music gets quieter when someone talks (by 12 dB), voices are evened out from clip to clip, cuts blend smoothly." {
		t.Fatalf("all help: %q", got)
	}
	for _, c := range []struct {
		ev   EnhanceView
		want string
	}{
		{EnhanceView{Mode: "clean", Strength: 1, State: "ready"}, "Ada sounds clearer (clean, 100%)."},
		{EnhanceView{Mode: "cloud", Strength: 0.8, State: "outdated"}, "Ada sounds clearer (in the cloud, 80%), but the settings changed since it was made: make it again to hear them."},
		{EnhanceView{Mode: "studio", Strength: 1, State: "missing"}, "Ada sounds clearer (studio quality, 100%), but some clips still play the original: make it again to cover them."},
		{EnhanceView{Mode: "clean", Strength: 0.5, Off: true}, "Ada plays as recorded for now (a clearer version is kept: clean, 50%)."},
	} {
		ev := c.ev
		if got := enhanceViewWords("Ada", &ev); got != c.want || len(plainwords.Check(got)) > 0 {
			t.Errorf("enhanceViewWords(%+v) = %q", c.ev, got)
		}
	}
	if got := showSummary(&ShowOut{}); got != "Everything plays as it was recorded." {
		t.Fatalf("nothing changed: %q", got)
	}
	o := &ShowOut{People: []PersonView{{Name: "Ada", Enhance: &EnhanceView{Mode: "clean"}}, {Name: "Ben", GainDB: -3, Level: "A little quieter (3 dB)"},
		{Name: "Cy", Enhance: &EnhanceView{Mode: "clean", Off: true}}},
		Narration: &NarrationView{Enhance: &EnhanceView{Mode: "clean"}}, Music: []MusicView{{ID: "theme"}}}
	if got := showSummary(o); got != "Ada sounds clearer; Ben a little quieter (3 dB); the narration sounds clearer; music under the video." {
		t.Fatalf("summary %q", got)
	}
	o.Music = append(o.Music, MusicView{ID: "sting"})
	if got := showSummary(o); !strings.HasSuffix(got, "; 2 music tracks.") {
		t.Fatalf("two tracks %q", got)
	}
}

// Presets round-trip; a failed step is said plainly with the first part of why.
func TestPresetsAndPlainFailures(t *testing.T) {
	for _, c := range []struct {
		mode     string
		strength float64
	}{{"clean", 1}, {"studio", 0.35}, {"cloud", 0.8}} {
		p := PresetFor(c.mode, c.strength)
		m, s, ok := ParsePreset(p)
		if !ok || m != c.mode || s != c.strength {
			t.Errorf("%s: %s %v %v", p, m, s, ok)
		}
	}
	if _, _, ok := ParsePreset("de-noise"); ok {
		t.Fatal("another clean-up read as a preset")
	}
	if got := firstClause("could not download the voice cleaner: Get https://example.invalid/x: dial tcp\nmore"); got != "could not download the voice cleaner" {
		t.Fatalf("first clause %q", got)
	}
	if got := firstClause("the model failed (exit status 3)"); got != "the model failed" {
		t.Fatalf("first clause %q", got)
	}
	e := plainFailure(errors.New("could not download the voice cleaner: Get https://example.invalid/x"), "Setting up clearer voices didn't work")
	if e.Code != apperr.ToolFailed || e.Message != "Setting up clearer voices didn't work: could not download the voice cleaner." {
		t.Fatalf("plain failure %+v", e)
	}
	e = plainFailure(apperr.New(apperr.NotConfigured, "Studio quality is not set up on this computer."), "Making the voice clearer didn't work")
	if e.Message != "Making the voice clearer didn't work: Studio quality is not set up on this computer." {
		t.Fatalf("plain failure of an apperr: %q", e.Message)
	}
	if e := plainFailure(errors.New(""), "It didn't work"); e.Message != "It didn't work." {
		t.Fatalf("no reason: %q", e.Message)
	}
	for _, c := range []struct{ in, want string }{{"camera", "source"}, {"Dialogue", "source"}, {"narration", "voiceover"}, {"VO", "voiceover"}, {"music", "music"}, {"fx", "sfx"}} {
		if got, ok := Part(c.in); !ok || got != c.want {
			t.Errorf("Part(%q) = %q %v", c.in, got, ok)
		}
	}
	if _, ok := Part("drums"); ok {
		t.Fatal("an unknown part")
	}
}

// The preview's A/B switches every clearer voice off (narration takes too)
// without touching anything else.
func TestWithoutClearer(t *testing.T) {
	s := &editspec.Spec{Mix: &editspec.Mix{Enhanced: []editspec.EnhancedSound{{AssetID: "ast_1", Variant: "ast_2"}}},
		Voiceover: &editspec.Voiceover{Beats: []editspec.Beat{
			{ID: "b1", Take: &editspec.TakeRef{AssetID: "ast_3", Processed: &editspec.ProcessedRef{AssetID: "ast_4", Preset: PresetFor("clean", 1)}, UseProcessed: true}},
			{ID: "b2", Take: &editspec.TakeRef{AssetID: "ast_5", Processed: &editspec.ProcessedRef{AssetID: "ast_6", Preset: "de-hum"}, UseProcessed: true}},
			{ID: "b3"}},
			Continuous: &editspec.ContinuousVO{AssetID: "ast_7", Processed: &editspec.ProcessedRef{AssetID: "ast_8", Preset: PresetFor("studio", 1)}, UseProcessed: true}}}
	withoutClearer(s)
	if s.Mix.Enhanced != nil || s.Voiceover.Beats[0].Take.UseProcessed || !s.Voiceover.Beats[1].Take.UseProcessed || s.Voiceover.Continuous.UseProcessed {
		t.Fatalf("A/B %+v %+v %+v", s.Mix, s.Voiceover.Beats, s.Voiceover.Continuous)
	}
	withoutClearer(&editspec.Spec{}) // nothing to switch off
}

func heard(words string, start float64) []textalign.Word {
	var out []textalign.Word
	for i, w := range strings.Fields(words) {
		from := start + float64(i)*0.5
		out = append(out, textalign.Word{Text: w, Norm: textalign.Normalize(w), From: from, To: from + 0.4})
	}
	return out
}

// Studio's word check (D90): where the rebuilt voice says another word, the
// cleaned voice plays — the stretch padded, neighbours merged; filler words
// don't count; the words are listed (at most eight, then how many more).
func TestStudioWordCheckFindsChangedWords(t *testing.T) {
	same := heard("we planted the seedlings today", 0)
	if spans, words := differingWords(same, heard("we planted the seedlings today", 0)); len(spans) != 0 || len(words) != 0 {
		t.Fatalf("the same words: %v %q", spans, words)
	}
	spans, words := differingWords(same, heard("we planted the seedlinks today", 0))
	if len(spans) != 1 || len(words) != 1 || words[0] != "“seedlings” → “seedlinks”" || spans[0][0] != 1.5-guardPad || spans[0][1] != 1.9+guardPad {
		t.Fatalf("one changed word: %v %q", spans, words)
	}
	// A filler the rebuilt voice adds is free; a word it drops is listed.
	if spans, words := differingWords(same, heard("we um planted the seedlings today", 0)); len(spans) != 0 || len(words) != 0 {
		t.Fatalf("a filler: %v %q", spans, words)
	}
	spans, words = differingWords(same, heard("we planted the today", 0))
	if len(words) == 0 || !strings.Contains(strings.Join(words, " "), "lost") || len(spans) == 0 {
		t.Fatalf("a word lost: %v %q", spans, words)
	}
	// Many differences: eight listed, then how many more.
	before := heard("one two three four five six seven eight nine ten eleven twelve", 0)
	after := heard("uno dos tres cuatro cinco seis siete ocho nueve diez once doce", 0)
	_, words = differingWords(before, after)
	if len(words) != 9 || !strings.HasPrefix(words[8], "and ") || !strings.HasSuffix(words[8], " more") {
		t.Fatalf("many: %q", words)
	}
}

// A processed voice is lined up with what it was made from before anything
// compares them.
func TestLineUpFindsTheShift(t *testing.T) {
	const rate = 8000
	ref := make([]float32, rate*2)
	for i := range ref {
		if (i/400)%3 == 0 { // bursts of sound, like syllables
			ref[i] = float32(((i*7919)%200)-100) / 100
		}
	}
	shifted := make([]float32, len(ref))
	copy(shifted[240:], ref) // 30 ms late
	got := lineUp(ref, shifted, rate)
	var diff float64
	for i := 1000; i < len(ref)-1000; i++ {
		d := float64(got[i] - ref[i])
		diff += d * d
	}
	if diff > 1e-3 {
		t.Fatalf("not lined up (error %g)", diff)
	}
	if same := lineUp(ref, ref, rate); &same[0] != &ref[0] {
		t.Fatal("an aligned voice is returned as it is")
	}
}

// What a sound change touches is kept to the clip's own bus names.
func TestBusChangesNameTheParts(t *testing.T) {
	_, what, err := levelsPlan(levelsRev(), &LevelsIn{Parts: []voiceover.BusChange{{Bus: "music", GainDB: fl(-6)}}}, func(string) string { return "" })
	if err != nil || len(what) != 1 || len(plainwords.Check(what[0])) > 0 {
		t.Fatalf("a part's level: %q %v", what, err)
	}
}

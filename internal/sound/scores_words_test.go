package sound

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/plainwords"
)

func clar(o, s, b float64) *Clarity { return &Clarity{Overall: o, Speech: s, Background: b} }
func snr(db float64) *Noise         { return &Noise{SNR: db} }

// What a clearer voice did is said plainly (D91): the clarity score out of 5
// in words, the background against the voice, cautions when it went too far
// — never jargon.
func TestScoresAreSaidPlainly(t *testing.T) {
	like := 0.62
	for _, c := range []struct {
		name     string
		ps       PersonScore
		summary  string
		warnings []string
	}{
		{"much clearer, less background", PersonScore{Speaker: "Ada", Seconds: 12, Mode: "clean", Strength: 1,
			Before: Score{Clarity: clar(2.4, 3.1, 2.0), Noise: snr(18)}, After: Score{Clarity: clar(3.2, 3.3, 3.9), Noise: snr(45)}},
			"Voice clarity score 2.4 → 3.2 out of 5 (much clearer); less background noise: it is now 45 dB below the voice (was 18 dB).", nil},
		{"a little clearer, about the same background", PersonScore{Speaker: "Ada", Seconds: 12, Mode: "clean", Strength: 1,
			Before: Score{Clarity: clar(3.0, 3.2, 3.0), Noise: snr(30)}, After: Score{Clarity: clar(3.2, 3.3, 3.1), Noise: snr(31)}},
			"Voice clarity score 3.0 → 3.2 out of 5 (clearer); about as much background noise as before (31 dB below the voice).", nil},
		{"worse", PersonScore{Speaker: "Ben", Seconds: 12, Mode: "clean", Strength: 1,
			Before: Score{Clarity: clar(3.4, 3.5, 3.5)}, After: Score{Clarity: clar(3.1, 3.4, 3.0)}},
			"Voice clarity score 3.4 → 3.1 out of 5 (less clear); more background noise than before.",
			[]string{"Processing makes Ben sound a little worse (voice clarity score 3.4 → 3.1): try a lower strength, or leave it as recorded."}},
		{"the voice itself processed", PersonScore{Seconds: 12, Mode: "cloud", Strength: 1,
			Before: Score{Clarity: clar(3.0, 3.6, 2.5)}, After: Score{Clarity: clar(3.05, 3.3, 3.6)}},
			"Voice clarity score 3.0 → 3.0 out of 5 (about the same); less background noise.",
			[]string{"The voice itself may sound processed now (thinner, a little robotic): try a lower strength for this voice."}},
		{"only the level", PersonScore{Speaker: "Cy", Seconds: 2, Mode: "clean", Strength: 0.5, Before: Score{Noise: snr(20)}, After: Score{Noise: snr(12)}, Likeness: &like},
			"More background noise than before: it is now 12 dB below the voice (was 20 dB).",
			[]string{"It may not sound quite like Cy any more (voice likeness 62%): listen before you keep it.", "There are only 2.0 seconds of speech to measure, so the scores are a rough guide."}},
		{"nothing measured", PersonScore{Speaker: "Cy", Mode: "clean", Strength: 1},
			"Not measured (the voice clarity score is not set up on this computer).", nil},
		{"studio blended", PersonScore{Speaker: "Di", Seconds: 10, Mode: "studio", Strength: 0.5, Before: Score{Noise: snr(20)}, After: Score{Noise: snr(40)}},
			"Less background noise: it is now 40 dB below the voice (was 20 dB).",
			[]string{"Studio rebuilds the voice, so blending it with the original can sound hollow; it works best at full strength."}},
	} {
		if got := summarize(c.ps); got != c.summary {
			t.Errorf("%s: summary %q\nwant %q", c.name, got, c.summary)
		}
		got := warnings(c.ps, 0)
		if strings.Join(got, "|") != strings.Join(c.warnings, "|") {
			t.Errorf("%s: warnings %q\nwant %q", c.name, got, c.warnings)
		}
		for _, s := range append(got, summarize(c.ps)) {
			if fs := plainwords.Check(s); len(fs) > 0 {
				t.Errorf("%s: not plain %q %v", c.name, s, fs)
			}
		}
	}
	// A blend of two that don't line up says so (not studio, which says its own).
	if w := warnings(PersonScore{Mode: "clean", Strength: 0.5, Seconds: 10}, 0.3); len(w) != 1 || !strings.Contains(w[0], "does not line up exactly") {
		t.Fatalf("blend warning %q", w)
	}
}

// Studio's word check is added to a score: moments kept, the words, and when
// no recognizer could check, that the cleaned voice played throughout.
func TestStudioWordChecksAreAddedToTheScore(t *testing.T) {
	ps := PersonScore{Speaker: "Ada", Mode: "studio", Strength: 1, Before: Score{Noise: snr(20)}, After: Score{Noise: snr(40)}, KeptWords: []string{"“a” → “b”"}}
	ps.guarded([]*Guard{{Checked: true, Replaced: 1, Words: []string{"“a” → “b”"}}, {Checked: true, Replaced: 2, Words: []string{"“seedlings” → “sea legs”", "“pepper” lost"}}})
	if ps.Kept != 3 || len(ps.KeptWords) != 3 || !strings.HasSuffix(ps.Summary, "Every word is kept: at 3 moments studio would have changed a word, so the cleaned voice plays there.") {
		t.Fatalf("checked: %+v", ps)
	}
	one := PersonScore{Mode: "studio", Strength: 1}
	one.guarded([]*Guard{{Checked: true, Replaced: 1}})
	if !strings.HasSuffix(one.Summary, "at 1 moment studio would have changed a word, so the cleaned voice plays there.") {
		t.Fatalf("one moment: %q", one.Summary)
	}
	safe := PersonScore{Mode: "studio", Strength: 1}
	safe.guarded([]*Guard{{Checked: false}})
	if !safe.SafeOnly || !strings.Contains(safe.Summary, "Studio played the cleaned voice throughout") {
		t.Fatalf("no recognizer: %+v", safe)
	}
	many := PersonScore{}
	var ws []string
	for i := 0; i < 12; i++ {
		ws = append(ws, string(rune('a'+i)))
	}
	many.guarded([]*Guard{{Checked: true, Replaced: 12, Words: ws}})
	if len(many.KeptWords) != 8 {
		t.Fatalf("at most eight words kept: %q", many.KeptWords)
	}
}

// wavBytes is a WAV file of the given format (1 PCM, 3 float) and samples.
func wavBytes(format, bits, channels, rate int, samples []byte) []byte {
	var b bytes.Buffer
	le := binary.LittleEndian
	b.WriteString("RIFF")
	_ = binary.Write(&b, le, uint32(36+8+len(samples)+12))
	b.WriteString("WAVE")
	b.WriteString("LIST") // a chunk readers must skip
	_ = binary.Write(&b, le, uint32(4))
	b.WriteString("INFO")
	b.WriteString("fmt ")
	_ = binary.Write(&b, le, uint32(16))
	_ = binary.Write(&b, le, uint16(format))
	_ = binary.Write(&b, le, uint16(channels))
	_ = binary.Write(&b, le, uint32(rate))
	_ = binary.Write(&b, le, uint32(rate*channels*bits/8))
	_ = binary.Write(&b, le, uint16(channels*bits/8))
	_ = binary.Write(&b, le, uint16(bits))
	b.WriteString("data")
	_ = binary.Write(&b, le, uint32(len(samples)))
	b.Write(samples)
	return b.Bytes()
}

// WAV files of every common sample format read as the same sound; one MetaRush
// can't read is refused with what it is.
func TestWAVsOfEveryCommonFormatRead(t *testing.T) {
	dir := t.TempDir()
	want := []float64{0, 0.5, -0.5, 0.25}
	le := binary.LittleEndian
	enc := map[string]func() []byte{
		"pcm24": func() []byte {
			var out []byte
			for _, v := range want {
				x := int32(math.Round(v * 8388607))
				out = append(out, byte(x), byte(x>>8), byte(x>>16))
			}
			return out
		},
		"pcm32": func() []byte {
			out := make([]byte, 4*len(want))
			for i, v := range want {
				le.PutUint32(out[4*i:], uint32(int32(math.Round(v*2147483647))))
			}
			return out
		},
		"float64": func() []byte {
			out := make([]byte, 8*len(want))
			for i, v := range want {
				le.PutUint64(out[8*i:], math.Float64bits(v))
			}
			return out
		},
	}
	for name, f := range map[string]struct{ format, bits int }{"pcm24": {1, 24}, "pcm32": {1, 32}, "float64": {3, 64}} {
		p := filepath.Join(dir, name+".wav")
		if err := os.WriteFile(p, wavBytes(f.format, f.bits, 1, 8000, enc[name]()), 0o600); err != nil {
			t.Fatal(err)
		}
		a, err := ReadWAV(p)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if a.Rate != 8000 || a.Channels != 1 || a.Frames() != len(want) || a.Seconds() != float64(len(want))/8000 {
			t.Fatalf("%s: %d Hz, %d channels, %d frames", name, a.Rate, a.Channels, a.Frames())
		}
		for i, v := range want {
			if math.Abs(float64(a.Data[i])-v) > 1e-4 {
				t.Fatalf("%s sample %d: %v, want %v", name, i, a.Data[i], v)
			}
		}
	}
	p := filepath.Join(dir, "u8.wav")
	if err := os.WriteFile(p, wavBytes(1, 8, 1, 8000, []byte{128, 200, 50}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadWAV(p); err == nil || !strings.Contains(err.Error(), "unsupported WAV encoding (format 1, 8 bits)") {
		t.Fatalf("8-bit: %v", err)
	}
	var none *Audio
	if none.Frames() != 0 || none.Seconds() != 0 || (&Audio{Channels: 2}).Seconds() != 0 {
		t.Fatal("empty audio")
	}
}

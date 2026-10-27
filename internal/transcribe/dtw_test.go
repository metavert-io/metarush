package transcribe

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/models"
)

// Models with an alignment-heads preset get DTW token times; others keep the
// decoder's (D244).
func TestDTWPresets(t *testing.T) {
	for model, want := range map[string]string{"/m/ggml-large-v3-turbo.bin": "large.v3.turbo", "ggml-large-v3.bin": "large.v3",
		"ggml-medium.en.bin": "medium.en", "ggml-base.en.bin": "base.en", "ggml-large-v3-turbo-q5_0.bin": "", "custom.bin": ""} {
		if got := dtwPreset(model); got != want {
			t.Errorf("%s: %q, want %q", model, got, want)
		}
	}
	if a := strings.Join(whisperServerArgs("/m/ggml-large-v3-turbo.bin", 1, "/p", "/q", 2), " "); !strings.HasSuffix(a, "-dtw large.v3.turbo -nfa") {
		t.Errorf("the loaded model gets DTW times like whisper-cli: %s", a)
	}
	if a := strings.Join(whisperServerArgs("/m/custom.bin", 1, "/p", "/q", 2), " "); strings.Contains(a, "-dtw") {
		t.Errorf("no preset, no DTW: %s", a)
	}
}

// A word starts at its first token's DTW time and ends where the next word
// starts when that is sooner than its last token's end.
func TestParseWhisperDTW(t *testing.T) {
	js := `{"result":{"language":"es"},"transcription":[
	 {"offsets":{"from":0,"to":3000},"text":" Hola amigos.","tokens":[
	  {"text":"[_BEG_]","offsets":{"from":0,"to":0},"p":0.9,"t_dtw":-1},
	  {"text":" Hola","offsets":{"from":0,"to":900},"p":0.9,"t_dtw":20},
	  {"text":" amig","offsets":{"from":900,"to":1500},"p":0.8,"t_dtw":110},
	  {"text":"os","offsets":{"from":1500,"to":2600},"p":0.9,"t_dtw":130},
	  {"text":".","offsets":{"from":2600,"to":2700},"p":0.9,"t_dtw":150}]},
	 {"offsets":{"from":2700,"to":5000},"text":" Vamos.","tokens":[
	  {"text":" Vamos","offsets":{"from":2700,"to":4800},"p":0.9,"t_dtw":410},
	  {"text":".","offsets":{"from":4800,"to":4900},"p":0.9,"t_dtw":470}]}]}`
	tr, err := parseWhisperJSON([]byte(js), true)
	if err != nil {
		t.Fatal(err)
	}
	var got []models.Word
	for _, s := range tr.Segments {
		got = append(got, s.Words...)
	}
	want := [][2]float64{{0.2, 0.9}, {1.1, 2.6}, {4.1, 4.8}}
	if len(got) != len(want) {
		t.Fatalf("words %+v", got)
	}
	for i, w := range want {
		if got[i].From != w[0] || got[i].To != w[1] {
			t.Errorf("word %d %q: %.2f–%.2f, want %.2f–%.2f", i, got[i].Text, got[i].From, got[i].To, w[0], w[1])
		}
	}
	if got[1].Text != "amigos." {
		t.Errorf("text %q", got[1].Text)
	}
	// Without DTW the decoder's times stay.
	tr, _ = parseWhisperJSON([]byte(js), false)
	if w := tr.Segments[1].Words[0]; w.From != 2.7 {
		t.Errorf("decoder times: %+v", w)
	}
	// The next word starting sooner than a word's end ends it there.
	js2 := `{"result":{"language":"de"},"transcription":[{"offsets":{"from":0,"to":3000},"text":" ja nein","tokens":[
	  {"text":" ja","offsets":{"from":0,"to":2000},"p":0.9,"t_dtw":10},
	  {"text":" nein","offsets":{"from":2000,"to":2900},"p":0.9,"t_dtw":150}]}]}`
	tr, _ = parseWhisperJSON([]byte(js2), true)
	if w := tr.Segments[0].Words[0]; w.To != 1.5 {
		t.Errorf("ja ends where nein starts: %+v", w)
	}
}

// whisper-server's DTW times reach the parser as whisper-cli's do; a reply
// without them reads as whisper-cli's -1.
func TestCLIFromVerboseCarriesDTW(t *testing.T) {
	b, err := cliFromVerbose([]byte(`{"language":"spanish","segments":[{"text":" de la","start":0,"end":1,"words":[
	  {"word":" de","start":0,"end":0.22,"t_dtw":46,"probability":0.9},{"word":" la","start":0.22,"end":0.44,"probability":0.9}]}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(b), `"t_dtw":46`) || !strings.Contains(string(b), `"t_dtw":-1`) || !strings.Contains(string(b), `"language":"es"`) {
		t.Fatalf("%s", b)
	}
}

// Pause edges come from the silence: the last word before a pause ends where
// it starts (trimmed when it ran into it, stretched when it ended up to a
// second early), the first word after it starts where it ends (moved when it
// started inside it or up to a second late); which side a word is on is
// where it starts. A word inside a pause, or a pause too short, is left alone.
func TestSnapToSilences(t *testing.T) {
	segs := []Segment{{Words: []models.Word{
		{Text: "a", From: 0, To: 2.4},   // runs 0.4 into the pause at 2–3
		{Text: "b", From: 2.6, To: 3.5}, // starts inside it
		{Text: "c", From: 4.5, To: 6.5}, // starts before the pause at 5–5.5 and runs across it
		{Text: "d", From: 7.1, To: 7.2}, // inside the pause at 7–8, in its first half
		{Text: "e", From: 9, To: 9.1},   // pause too short to count
	}}, {Words: []models.Word{
		{Text: "f", From: 10, To: 10.5}, // ends 0.5 before the pause at 11–12
		{Text: "g", From: 12.4, To: 13}, // starts 0.4 after it
		{Text: "h", From: 13.1, To: 13.5},
		{Text: "i", From: 16, To: 17}, // starts more than a second after the pause at 14–14.5
	}}}
	sil := []span{{2, 3}, {5, 5.5}, {7, 8}, {9.05, 9.08}, {11, 12}, {14, 14.5}}
	got := snapToSilences(segs, sil, 0.25)
	want := [][2]float64{{0, 2}, {3, 3.5}, {4.5, 5}, {7.1, 7.2}, {9, 9.1}, {10, 11}, {12, 13}, {13.1, 14}, {16, 17}}
	k := 0
	for _, s := range got {
		for _, w := range s.Words {
			if w.From != want[k][0] || w.To != want[k][1] {
				t.Errorf("%s: %.2f–%.2f, want %.2f–%.2f", w.Text, w.From, w.To, want[k][0], want[k][1])
			}
			k++
		}
	}
	if got[0].From != 0 || got[0].To != 9.1 || got[1].From != 10 || got[1].To != 17 {
		t.Errorf("segments follow their words: %+v %+v", got[0], got[1])
	}
}

package cutsheet

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

// The dashboard edits by transcript in the browser (web/src/lib/words.ts): it
// works out which words a clip plays, the stretch that removes a selection and
// the removeWords ops for a set of removals, before the server's preview
// answers. This golden file is what Go makes of a set of cuts, transcripts and
// selections; web/scripts/words-parity.mjs checks that the browser makes the
// same. Regenerate after changing the word rules:
//
//	UPDATE_GOLDEN=1 go test ./internal/cutsheet -run TestWordRemovalsGolden
const wordsGoldenPath = "testdata/word-removals.golden.json"

// goldenSeg is a transcript segment as the dashboard gets it (footage.transcript).
type goldenSeg struct {
	From    float64      `json:"from"`
	To      float64      `json:"to"`
	Text    string       `json:"text"`
	Speaker string       `json:"speaker,omitempty"`
	Words   []goldenWord `json:"words,omitempty"`
}

type goldenWord struct {
	W string  `json:"w"`
	F float64 `json:"f"`
	T float64 `json:"t"`
}

type goldenSel struct {
	ClipID string  `json:"clipId"`
	From   float64 `json:"from"`
	To     float64 `json:"to"`
}

type wordsCase struct {
	Name        string                 `json:"name"`
	Spec        *editspec.Spec         `json:"spec"`
	Transcripts map[string][]goldenSeg `json:"transcripts"`
	Selections  []goldenSel            `json:"selections"`
	// Removals[i] is RemovalFor(Selections[i]); null when no word is there.
	Removals []*WordRemoval `json:"removals"`
	// Ops are RemovalOps of the removals found (Error: refused).
	Ops      []editspec.Op `json:"ops"`
	Error    bool          `json:"error,omitempty"`
	Cleanups []WordRemoval `json:"cleanups"`
	// CleanupOps are RemovalOps of every cleanup suggestion.
	CleanupOps   []editspec.Op `json:"cleanupOps"`
	CleanupError bool          `json:"cleanupError,omitempty"`
}

const (
	astOther   = "ast_01jabcdefghjkmnpqrstvwxyz1"
	astNoWords = "ast_01jabcdefghjkmnpqrstvwxyz2"
)

func toGolden(segs []editspec.TranscriptSegment) []goldenSeg {
	out := make([]goldenSeg, 0, len(segs))
	for _, s := range segs {
		g := goldenSeg{From: s.From, To: s.To, Text: s.Text, Speaker: s.Speaker}
		for i, w := range s.Words {
			g.Words = append(g.Words, goldenWord{W: w.Text, F: w.From, T: w.To})
			if s.Text == "" {
				if i > 0 {
					g.Text += " "
				}
				g.Text += w.Text
			}
		}
		out = append(out, g)
	}
	return out
}

func wordsGoldenCases() []wordsCase {
	vertical := editspec.Output{Preset: editspec.PresetVertical, FPS: 30}
	talks := map[string][]editspec.TranscriptSegment{
		astTalk: talkTranscript(),
		// No word times: words are spread evenly over each segment.
		astNoWords: {{From: 0.5, To: 3.5, Text: "Hello there, my old friend.", Speaker: "Speaker 2"}, {From: 4, To: 6, Text: "Um, it's been a while."}},
	}
	spec := func(clips ...editspec.Clip) *editspec.Spec {
		s := &editspec.Spec{Output: vertical, Clips: clips}
		editspec.Normalize(s)
		return s
	}
	cross := &editspec.Transition{Type: editspec.TransitionCrossfade, Duration: 0.5}
	sel := func(id string, from, to float64) goldenSel { return goldenSel{ClipID: id, From: from, To: to} }
	type named struct {
		name string
		spec *editspec.Spec
		tx   map[string][]editspec.TranscriptSegment
		sels []goldenSel
	}
	cases := []named{
		{"one clip", spec(editspec.Clip{ID: "talk", AssetID: astTalk, In: 0.8, Out: 10.4}), talks,
			[]goldenSel{sel("talk", 1.4, 1.9), sel("talk", 3.4, 4.5), sel("talk", 0.9, 1.25), sel("talk", 9.0, 10.1), sel("talk", 7.0, 7.9)}},
		{"whole cut", spec(editspec.Clip{ID: "talk", AssetID: astTalk, In: 0.8, Out: 10.4}), talks, []goldenSel{sel("talk", 0, 11)}},
		{"two parts and b-roll", spec(
			editspec.Clip{ID: "intro", AssetID: astTalk, In: 0.8, Out: 5.35},
			editspec.Clip{ID: "broll", AssetID: astOther, In: 0, Out: 2, Muted: true},
			editspec.Clip{ID: "outro", AssetID: astTalk, In: 5.4, Out: 10.4, Speed: 1.25, Transition: cross},
		), talks, []goldenSel{sel("intro", 1.4, 1.9), sel("intro", 4.5, 5.35), sel("outro", 5.45, 5.97), sel("broll", 0, 2)}},
		{"same footage twice", spec(
			editspec.Clip{ID: "a", AssetID: astTalk, In: 0.8, Out: 4.5},
			editspec.Clip{ID: "b", AssetID: astTalk, In: 3.0, Out: 7.0},
		), talks, []goldenSel{sel("a", 3.3, 4.0), sel("b", 3.3, 4.0)}},
		{"locked", spec(editspec.Clip{ID: "talk", AssetID: astTalk, In: 0.8, Out: 10.4, Locked: true}, editspec.Clip{ID: "end", AssetID: astOther, In: 0, Out: 2}), talks,
			[]goldenSel{sel("talk", 1.4, 1.9)}},
		{"no word times", spec(editspec.Clip{ID: "hello", AssetID: astNoWords, In: 0, Out: 6.5}, editspec.Clip{ID: "end", AssetID: astOther, In: 0, Out: 2}), talks,
			[]goldenSel{sel("hello", 1.0, 2.0), sel("hello", 4.0, 4.6)}},
		{"slow with a fade", spec(editspec.Clip{ID: "talk", AssetID: astTalk, In: 0.8, Out: 10.4, Speed: 0.8, FadeOut: 1}, editspec.Clip{ID: "end", AssetID: astOther, In: 0, Out: 2}), talks,
			[]goldenSel{sel("talk", 2.2, 3.0), sel("talk", 8.0, 10.4)}},
	}
	// Random transcripts, cuts and selections.
	vocab := []string{"so", "we", "built", "it", "because", "the", "robot", "kept", "falling", "over", "and", "I", "wanted", "to", "quit", "um", "uh", "really", "fast", "then"}
	for seed := int64(1); seed <= 80; seed++ {
		r := rand.New(rand.NewSource(seed))
		round := func(v float64) float64 { return math.Round(v*100) / 100 }
		gen := func() ([]editspec.TranscriptSegment, float64) {
			t := 0.3 + r.Float64()
			var segs []editspec.TranscriptSegment
			for s := 0; s < 1+r.Intn(3); s++ {
				seg := editspec.TranscriptSegment{From: round(t), Speaker: []string{"Ada", "Bo", ""}[r.Intn(3)]}
				prev := ""
				for w := 0; w < 3+r.Intn(10); w++ {
					text := vocab[r.Intn(len(vocab))]
					switch r.Intn(9) {
					case 0:
						if prev != "" {
							text = prev // said twice
						}
					case 1:
						text = text[:1] + "-" // cut off
					case 2:
						text += ","
					}
					d := 0.08 + r.Float64()*0.4
					seg.Words = append(seg.Words, editspec.TranscriptWord{Text: text, From: round(t), To: round(t + d)})
					prev = text
					t += d + []float64{0, 0.02, 0.1, 0.25, 0.6}[r.Intn(5)]
				}
				seg.To = round(t)
				t += 0.4 + r.Float64()
				segs = append(segs, seg)
			}
			return segs, t
		}
		segsA, lenA := gen()
		tx := map[string][]editspec.TranscriptSegment{astTalk: segsA}
		lens := map[string]float64{astTalk: lenA}
		if r.Intn(2) == 0 {
			segsB, lenB := gen()
			if r.Intn(3) == 0 {
				for i := range segsB { // no word times
					var words []string
					for _, w := range segsB[i].Words {
						words = append(words, w.Text)
					}
					segsB[i].Text, segsB[i].Words = strings.Join(words, " "), nil
				}
			}
			tx[astOther], lens[astOther] = segsB, lenB
		}
		ids := []string{astTalk}
		if _, ok := tx[astOther]; ok {
			ids = append(ids, astOther)
		}
		var clips []editspec.Clip
		for i := 0; i < 1+r.Intn(4); i++ {
			id := ids[r.Intn(len(ids))]
			in := round(r.Float64() * math.Max(0, lens[id]-1))
			out := round(math.Min(lens[id], in+0.5+r.Float64()*3.5))
			c := editspec.Clip{ID: fmt.Sprintf("c%d", i+1), AssetID: id, In: in, Out: out, Speed: []float64{1, 1, 1, 1.25, 0.8}[r.Intn(5)], Muted: r.Intn(10) == 0}
			if i > 0 && r.Intn(3) == 0 {
				c.Transition = &editspec.Transition{Type: editspec.TransitionCrossfade, Duration: 0.3}
			}
			clips = append(clips, c)
		}
		var sels []goldenSel
		for k := 0; k < 2; k++ {
			c := clips[r.Intn(len(clips))]
			a, b := c.In-0.2+r.Float64()*(c.Out-c.In+0.4), c.In-0.2+r.Float64()*(c.Out-c.In+0.4)
			if a > b {
				a, b = b, a
			}
			sels = append(sels, sel(c.ID, round(a), round(b)))
		}
		cases = append(cases, named{fmt.Sprintf("random %d", seed), spec(clips...), tx, sels})
	}

	var out []wordsCase
	for _, n := range cases {
		tx := func(id string) []editspec.TranscriptSegment { return n.tx[id] }
		wc := wordsCase{Name: n.name, Spec: n.spec, Transcripts: map[string][]goldenSeg{}, Selections: n.sels, Removals: []*WordRemoval{}, Ops: []editspec.Op{}, Cleanups: []WordRemoval{}, CleanupOps: []editspec.Op{}}
		for _, c := range n.spec.Clips {
			if segs, ok := n.tx[c.AssetID]; ok {
				wc.Transcripts[c.AssetID] = toGolden(segs)
			}
		}
		var found []WordRemoval
		for _, s := range n.sels {
			if r, ok := RemovalFor(n.spec, tx, s.ClipID, s.From, s.To); ok {
				found = append(found, r)
				wc.Removals = append(wc.Removals, &r)
			} else {
				wc.Removals = append(wc.Removals, nil)
			}
		}
		if ops, err := RemovalOps(n.spec, found, false); err != nil {
			wc.Error = true
		} else if ops != nil {
			wc.Ops = ops
		}
		wc.Cleanups = append(wc.Cleanups, Cleanups(n.spec, tx)...)
		if ops, err := RemovalOps(n.spec, wc.Cleanups, false); err != nil {
			wc.CleanupError = true
		} else if ops != nil {
			wc.CleanupOps = ops
		}
		out = append(out, wc)
	}
	return out
}

func TestWordRemovalsGolden(t *testing.T) {
	cases := wordsGoldenCases()
	// The rules are exercised: words are found, some ops are refused, cleanups are suggested.
	var found, refused, cleanups int
	for _, c := range cases {
		for _, r := range c.Removals {
			if r != nil {
				found++
			}
		}
		if c.Error {
			refused++
		}
		cleanups += len(c.Cleanups)
	}
	if found < 60 || refused < 2 || cleanups < 40 {
		t.Fatalf("the golden cases don't exercise the rules: %d found, %d refused, %d cleanups", found, refused, cleanups)
	}
	// One compact line per case (a changed case is a one-line diff).
	var buf bytes.Buffer
	buf.WriteString(`{"generatedBy": "go test ./internal/cutsheet -run TestWordRemovalsGolden (UPDATE_GOLDEN=1)",` + "\n\"cases\": [\n")
	for i, c := range cases {
		line, err := json.Marshal(c)
		if err != nil {
			t.Fatal(err)
		}
		buf.Write(line)
		if i < len(cases)-1 {
			buf.WriteByte(',')
		}
		buf.WriteByte('\n')
	}
	buf.WriteString("]}\n")
	b := buf.Bytes()
	if os.Getenv("UPDATE_GOLDEN") == "1" {
		if err := os.MkdirAll(filepath.Dir(wordsGoldenPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(wordsGoldenPath, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cur, err := os.ReadFile(wordsGoldenPath)
	if err != nil || !bytes.Equal(cur, b) {
		t.Fatalf("%s is stale; run UPDATE_GOLDEN=1 go test ./internal/cutsheet -run TestWordRemovalsGolden (%v)", wordsGoldenPath, err)
	}
	// The browser's word rules agree (when node and the web dependencies are
	// installed; CI and the build checks have both).
	node, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed")
	}
	if _, err := os.Stat("../../node_modules/esbuild"); err != nil {
		t.Skip("web dependencies not installed (npm install)")
	}
	out, err := exec.Command(node, "../../web/scripts/words-parity.mjs", wordsGoldenPath).CombinedOutput()
	if err != nil {
		t.Fatalf("the dashboard's word rules differ from Go's:\n%s", out)
	}
}

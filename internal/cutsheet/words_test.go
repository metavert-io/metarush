package cutsheet

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/editspec"
)

const astTalk = "ast_01jabcdefghjkmnpqrstvwxyz0"

func tw(args ...any) []editspec.TranscriptWord {
	var out []editspec.TranscriptWord
	for i := 0; i+2 < len(args); i += 3 {
		out = append(out, editspec.TranscriptWord{Text: args[i].(string), From: args[i+1].(float64), To: args[i+2].(float64)})
	}
	return out
}

// "So um we built it, we built it twice because because I wa- I wanted to. We were, we went fast."
func talkTranscript() []editspec.TranscriptSegment {
	return []editspec.TranscriptSegment{
		{From: 1, To: 7.4, Speaker: "Ada", Words: tw(
			"So", 1.0, 1.2, "um", 1.5, 1.8, "we", 2.2, 2.35, "built", 2.4, 2.7, "it,", 2.75, 2.9,
			"we", 3.3, 3.45, "built", 3.5, 3.8, "it", 3.85, 3.95, "twice", 4.0, 4.4,
			"because", 4.6, 4.9, "because", 5.0, 5.3, "I", 5.4, 5.45, "wa-", 5.5, 5.6, "I", 5.9, 5.95, "wanted", 6.0, 6.3, "to.", 6.35, 6.6)},
		{From: 8, To: 10.5, Speaker: "Ada", Words: tw("We", 8.0, 8.15, "were,", 8.2, 8.5, "we", 8.9, 9.05, "went", 9.1, 9.4, "fast.", 9.45, 10.0)},
	}
}

func talkSpec() *editspec.Spec {
	s := &editspec.Spec{Output: editspec.Output{Preset: editspec.PresetVertical, FPS: 30}, Clips: []editspec.Clip{
		{ID: "talk", AssetID: astTalk, In: 0.8, Out: 10.4},
	}}
	editspec.Normalize(s)
	return s
}

func txOf(id string) []editspec.TranscriptSegment {
	if id == astTalk {
		return talkTranscript()
	}
	return nil
}

func TestCleanupsFindUmsRepeatsAndFalseStarts(t *testing.T) {
	got := Cleanups(talkSpec(), txOf)
	var kinds []string
	for _, r := range got {
		kinds = append(kinds, r.Kind+":"+r.Words)
	}
	want := []string{"filler:um", "repeat:we built it,", "repeat:because", "false-start:I wa-", "false-start:We were,"}
	if strings.Join(kinds, " | ") != strings.Join(want, " | ") {
		t.Fatalf("got %v", kinds)
	}
	um := got[0]
	// The cut lands in the pauses: 0.15 s after "So", 0.1 s before "we".
	if um.From != 1.35 || um.To != 2.1 || um.Before != "So" || !strings.HasPrefix(um.After, "we built") || um.At != 0.7 {
		t.Fatalf("um: %+v", um)
	}
	ops, err := RemovalOps(talkSpec(), got, false)
	if err != nil || len(ops) != 5 {
		t.Fatalf("ops %v %v", ops, err)
	}
	// Each removal continues in the part the previous one made ("we built it," starts
	// right where the um's stretch ended, so it only moves that part's start).
	if ops[0].ID != "talk" || ops[0].NewID != "talk-2" || ops[1].ID != "talk-2" || ops[1].NewID != "" || ops[2].ID != "talk-2" {
		t.Fatalf("targets: %+v", ops)
	}
	out, _, err := editspec.ApplyOps(talkSpec(), ops, false)
	if err != nil || len(out.Clips) != 5 {
		t.Fatalf("applied: %v %+v", err, out.Clips)
	}
	var heard []string
	for _, c := range out.Clips {
		for _, w := range wordsIn(flattenWords(talkTranscript()), c.In, c.Out) {
			heard = append(heard, w.text)
		}
	}
	if got := strings.Join(heard, " "); got != "So we built it twice because I wanted to. we went fast." {
		t.Fatalf("what is left: %q", got)
	}
}

func TestFindWordsAndSelections(t *testing.T) {
	s := talkSpec()
	hits, _ := FindWords(s, txOf, "because because", "")
	if len(hits) != 1 || hits[0].Words != "because because" || hits[0].Kind != RemovePhrase {
		t.Fatalf("hits %+v", hits)
	}
	twice, _ := FindWords(s, txOf, "we built it", "")
	if len(twice) != 2 {
		t.Fatalf("said twice: %+v", twice)
	}
	// Small slips still find the words; something never said gives the closest thing that is.
	if fuzzy, _ := FindWords(s, txOf, "we bilt it twise", ""); len(fuzzy) != 1 || fuzzy[0].Words != "we built it twice" {
		t.Fatalf("fuzzy %+v", fuzzy)
	}
	none, closest := FindWords(s, txOf, "we went slowly", "")
	if len(none) != 0 || closest != "we went fast." {
		t.Fatalf("no match should give a hint: %+v %q", none, closest)
	}
	// A selection by times picks the words whose middles are inside.
	sel, ok := RemovalFor(s, txOf, "talk", 8.0, 8.6)
	if !ok || sel.Words != "We were," || sel.To != 8.8 {
		t.Fatalf("selection %+v", sel)
	}
	// Words at the very start of a clip: the stretch starts at the clip's start.
	first, ok := RemovalFor(s, txOf, "talk", 0.9, 1.3)
	if !ok || first.From != 0.8 || first.Words != "So" {
		t.Fatalf("first words %+v", first)
	}
	if _, ok := RemovalFor(s, txOf, "talk", 7.0, 7.5); ok {
		t.Fatal("no words in a pause")
	}
	// Outside the clip's range nothing is heard.
	short := talkSpec()
	short.Clips[0].Out = 5
	if hits, _ := FindWords(short, txOf, "went fast", ""); len(hits) != 0 {
		t.Fatalf("words the clip doesn't play: %+v", hits)
	}
}

// A clip cut in two reads as one split, not an added clip and a shorter one.
func TestDescribeSplit(t *testing.T) {
	a := talkSpec()
	at := 4.5
	b, _, err := editspec.ApplyOps(a, []editspec.Op{{Op: "splitClip", ID: "talk", At: &at}}, false)
	if err != nil {
		t.Fatal(err)
	}
	got := Describe(a, b, nil)
	if len(got) != 1 || got[0] != "Split talk in two at 0:04.5; the second part is talk-2." {
		t.Fatalf("describe: %v", got)
	}
	b.Clips[1].Muted = true
	if got := Describe(a, b, nil); len(got) != 2 || got[1] != "talk-2 is now muted." {
		t.Fatalf("describe with a change: %v", got)
	}
}

// A quote in Japanese is found across any number of whisper's pieces (D244),
// for cut sheet anchors and for taking words out.
func TestJapaneseQuotesFindPieces(t *testing.T) {
	const ast = "ast_01jabcdefghjkmnpqrstvwxyz7"
	segs := []editspec.TranscriptSegment{{From: 0, To: 3, Words: tw(
		"人魚", 0.0, 0.4, "は、", 0.4, 0.6, "南", 0.8, 1.0, "の", 1.0, 1.1, "方", 1.1, 1.3,
		"の", 1.3, 1.4, "海", 1.4, 1.6, "に", 1.6, 1.7, "住", 1.7, 1.9, "んで", 1.9, 2.2, "いる。", 2.2, 2.6)}}
	ws := flattenWords(segs)
	for phrase, want := range map[string][2]int{"南の方の海に住んで": {2, 9}, "南の方": {2, 4}, "人魚は南": {0, 2}, "海に住んでいる": {6, 10}} {
		ms := findPhrase(ws, phrase)
		if len(ms) != 1 || ms[0].first != want[0] || ms[0].last != want[1] {
			t.Errorf("%q: %+v, want %d–%d", phrase, ms, want[0], want[1])
		}
	}
	if ms := findPhrase(ws, "北の海"); len(ms) != 0 {
		t.Errorf("not said: %+v", ms)
	}
	s := &editspec.Spec{Output: editspec.Output{Preset: editspec.PresetVertical, FPS: 30}, Clips: []editspec.Clip{{ID: "mermaid", AssetID: ast, In: 0, Out: 3}}}
	editspec.Normalize(s)
	hits, _ := FindWords(s, func(id string) []editspec.TranscriptSegment {
		if id == ast {
			return segs
		}
		return nil
	}, "南の方の海に", "")
	if len(hits) != 1 || hits[0].Words != "南の方の海に" {
		t.Fatalf("remove-words finds the pieces: %+v", hits)
	}
}

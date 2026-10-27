package transcripts

import (
	"context"
	"testing"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Japanese words are whisper's pieces (D244). Search finds a phrase across
// pieces however they were cut, with or without punctuation or spaces in the
// query; the text reads without spaces; retyping keeps the pieces that didn't
// change (and their times) and writes only the changed ones again.
func TestJapanesePiecesSearchJoinAndRetype(t *testing.T) {
	pieces := []models.Word{w("人魚", 0, 0.4), w("は、", 0.4, 0.6), w("南", 0.8, 1.0), w("の", 1.0, 1.1), w("方", 1.1, 1.3),
		w("の", 1.3, 1.4), w("海", 1.4, 1.6), w("に", 1.6, 1.7), w("住", 1.7, 1.9), w("んで", 1.9, 2.2)}
	segs := []models.TranscriptSegment{seg(pieces...)}
	us := Units(segs)
	if segs[0].Text != "人魚は、南の方の海に住んで" {
		t.Fatalf("the line reads without spaces: %q", segs[0].Text)
	}
	for q, want := range map[string][2]int{"南の方": {2, 4}, "海に住んで": {6, 9}, "海に住ん": {6, 9}, "は南": {1, 2}, "南の 方": {2, 4}, "人魚は、南": {0, 2}} {
		sp := Match(us, q, MatchOptions{})
		if len(sp) != 1 || sp[0].I != want[0] || sp[0].J != want[1] {
			t.Errorf("%q: %+v, want units %d–%d", q, sp, want[0], want[1])
		}
	}
	if sp := Match(us, "海に住ん", MatchOptions{}); len(sp) == 1 && sp[0].Post != "で" {
		t.Errorf("the rest of the last piece is kept: %+v", sp[0])
	}
	if sp := Match(us, "北の方", MatchOptions{}); len(sp) != 0 {
		t.Errorf("not said: %+v", sp)
	}
	if got := Text(us, 2, 6); got != "南の方の海" {
		t.Errorf("Text: %q", got)
	}
	// Retyping one kanji changes only that piece, at its time.
	bs := Retype(pieces, "人魚は、南の方の川に住んで")
	if len(bs) != 1 || bs[0].I != 6 || bs[0].J != 6 || len(bs[0].Words) != 1 || bs[0].Words[0].Text != "川" || bs[0].Words[0].From != 1.4 || bs[0].Words[0].To != 1.6 {
		t.Fatalf("one piece changed: %+v", bs)
	}
	if bs := Retype(pieces, "人魚は、南の方の海に住んで"); len(bs) != 0 {
		t.Fatalf("nothing changed: %+v", bs)
	}
	// A word added between pieces joins the piece after it; the rest stay.
	bs = Retype(pieces, "人魚は、遠い南の方の海に住んで")
	if len(bs) != 1 || bs[0].I != 2 || bs[0].J != 2 || JoinWords(bs[0].Words) != "遠い南" {
		t.Fatalf("an added word: %+v", bs)
	}
	// English words in the same line keep their spaces, and are never cut apart.
	mixed := []models.Word{w("LibriVox", 0, 0.5), w("の", 0.5, 0.6), w("録音", 0.6, 1.0)}
	if got := JoinWords(mixed); got != "LibriVoxの録音" {
		t.Errorf("mixed: %q", got)
	}
	bs = Retype([]models.Word{w("Fern", 0, 0.2), w("Brook", 0.2, 0.5), w("の", 0.5, 0.6)}, "FernBrookの")
	if len(bs) != 1 || len(bs[0].Words) != 1 || bs[0].Words[0].Text != "FernBrook" {
		t.Fatalf("a Latin join stays joined: %+v", bs)
	}
}

// Search through the real path (D244): Find narrows clips by their stored
// lines before matching, and a Japanese query finds its clip across the
// punctuation and spaces between pieces — also in a line stored with spaces
// between them, as lines were before pieces joined without — but not a clip
// that doesn't say it.
func TestFindJapaneseAcrossPunctuation(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	prj := ids.New(ids.Project)
	mermaid := clip(t, a, prj, []models.Word{w("人魚", 0, 0.4), w("は、", 0.4, 0.6), w("南", 0.8, 1.0), w("の", 1.0, 1.1), w("方", 1.1, 1.3)})
	old := clip(t, a, prj, []models.Word{w("人魚", 5, 5.4), w("は、", 5.4, 5.6), w("南", 5.8, 6.0)})
	if _, err := a.DB.C("transcript_segments").UpdateMany(ctx, map[string]any{"assetId": old.ID}, map[string]any{"$set": map[string]any{"text": "人魚 は、 南"}}); err != nil {
		t.Fatal(err)
	}
	other := clip(t, a, prj, []models.Word{w("北", 0, 0.3), w("の", 0.3, 0.4), w("海", 0.4, 0.8)})
	for _, q := range []string{"は南", "は、南", "人魚 は 南"} {
		f, err := Find(ctx, a, FindInput{Query: q, Scope: Scope{ProjectID: prj}})
		if err != nil {
			t.Fatal(err)
		}
		got := map[string]bool{}
		for _, h := range f.Hits {
			got[h.AssetID] = true
		}
		if !got[mermaid.ID] || !got[old.ID] || got[other.ID] || len(f.Hits) != 2 {
			t.Errorf("%q: %+v", q, f.Hits)
		}
	}
}

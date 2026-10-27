package transcripts

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// clip makes a transcribed clip: lines of words "text@from-to".
func clip(t *testing.T, a *app.App, project string, lines ...[]models.Word) *models.Asset {
	t.Helper()
	ctx := context.Background()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: "interview.mov", Title: "Interview", SHA256: ids.New("sha"),
		State: models.AssetAvailable, Media: models.MediaInfo{Duration: 60, HasAudio: true, HasVideo: true}, CreatedAt: time.Now().UTC(),
		Transcript: &models.TranscriptStatus{State: "available", Provider: "parakeet", Model: "test", Precision: "word", Segments: len(lines)}}
	if project != "" {
		as.ProjectIDs = []string{project}
	}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	for i, ws := range lines {
		s := models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, Index: i, From: ws[0].From, To: ws[len(ws)-1].To,
			Text: JoinWords(ws), Words: ws, Speaker: "speaker_00", Provider: "parakeet", Model: "test", CreatedAt: time.Now().UTC()}
		if err := a.DB.Insert(ctx, store.CSegments, s); err != nil {
			t.Fatal(err)
		}
	}
	return &as
}

func words(spec string) []models.Word {
	// "We@0-0.2 met@0.25-0.4"
	var out []models.Word
	for _, f := range strings.Fields(spec) {
		at := strings.LastIndex(f, "@")
		var a, b float64
		parseRange(f[at+1:], &a, &b)
		out = append(out, models.Word{Text: f[:at], From: a, To: b, Score: 0.42})
	}
	return out
}

func parseRange(s string, a, b *float64) {
	i := strings.Index(s, "-")
	*a, *b = atof(s[:i]), atof(s[i+1:])
}

func atof(s string) float64 {
	var v float64
	var frac float64 = 0
	div := 1.0
	for _, r := range s {
		switch {
		case r == '.':
			frac = 1
		case frac > 0:
			div *= 10
			v += float64(r-'0') / div
		default:
			v = v*10 + float64(r-'0')
		}
	}
	return v
}

func textOf(t *testing.T, a *app.App, assetID string) string {
	t.Helper()
	segs, err := LoadSegments(context.Background(), a, assetID)
	if err != nil {
		t.Fatal(err)
	}
	var parts []string
	for _, s := range segs {
		parts = append(parts, s.Text)
	}
	return strings.Join(parts, " | ")
}

func TestFixUndoAndHistory(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	as := clip(t, a, "", words("We@0-0.2 met@0.25-0.4 Fern@0.5-0.7 Brook@0.75-1 today.@1.1-1.4"), words("Then@2-2.2 elmwood@2.3-2.9 road.@3-3.3"))

	// Retype: only "Fern Brook" changes, into one word over both times.
	ch, err := FixWords(ctx, a, FixInput{AssetID: as.ID, From: 0, To: 1.4, Text: "We met FernBrook today.", Expect: "We met Fern Brook today."})
	if err != nil {
		t.Fatal(err)
	}
	if len(ch.Fixes) != 1 || ch.Fixes[0].BeforeText != "Fern Brook" || ch.Fixes[0].AfterText != "FernBrook" || ch.Rev != 1 {
		t.Fatalf("fix %+v", ch)
	}
	if got := textOf(t, a, as.ID); got != "We met FernBrook today. | Then elmwood road." {
		t.Fatalf("text %q", got)
	}
	segs, _ := LoadSegments(ctx, a, as.ID)
	if w := segs[0].Words[2]; w.Text != "FernBrook" || w.From != 0.5 || w.To != 1 || !w.Fixed || w.Score != 0 {
		t.Fatalf("joined word %+v", w)
	}
	// The machine's transcript is kept.
	o, err := LoadOriginal(ctx, a, as.ID, 1)
	if err != nil || len(o.Segments) != 2 || o.Segments[0].Text != "We met Fern Brook today." {
		t.Fatalf("original %+v %v", o, err)
	}
	// A stale fix is refused.
	if _, err := FixWords(ctx, a, FixInput{AssetID: as.ID, From: 0.5, To: 1, Text: "Fern-Brook", Expect: "Fern Brook"}); err == nil || !strings.Contains(err.Error(), "changed since") {
		t.Fatalf("stale: %v", err)
	}
	// A second fix, then history and undo in order.
	if _, err := FixWords(ctx, a, FixInput{AssetID: as.ID, From: 2.3, To: 2.9, Text: "Elmwood"}); err != nil {
		t.Fatal(err)
	}
	var reloaded models.Asset
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, &reloaded)
	if reloaded.TextFixes == nil || reloaded.TextFixes.Rev != 2 || reloaded.TextFixes.Fixes != 2 {
		t.Fatalf("state %+v", reloaded.TextFixes)
	}
	h, err := LoadHistory(ctx, a, &reloaded, 0)
	if err != nil || len(h.Entries) != 2 || !h.Entries[0].CanUndo || h.Entries[0].AfterText != "Elmwood" || h.Original == nil {
		t.Fatalf("history %+v %v", h, err)
	}
	// Fixing a fixed word again, then undoing the first fix is refused until the later one goes.
	ch2, err := FixWords(ctx, a, FixInput{AssetID: as.ID, From: 0.5, To: 1, Text: "Fern-Brook"})
	if err != nil {
		t.Fatal(err)
	}
	res, err := Undo(ctx, a, ch.Fixes[0].ID, "")
	if err != nil || len(res.Undone) != 0 || len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0].Why, "changed again") {
		t.Fatalf("undo refused %+v %v", res, err)
	}
	if res, err := Undo(ctx, a, ch2.Fixes[0].ID, ""); err != nil || len(res.Undone) != 1 {
		t.Fatalf("undo later %+v %v", res, err)
	}
	if res, err := Undo(ctx, a, ch.Fixes[0].ID, ""); err != nil || len(res.Undone) != 1 {
		t.Fatalf("undo first %+v %v", res, err)
	}
	if got := textOf(t, a, as.ID); got != "We met Fern Brook today. | Then Elmwood road." {
		t.Fatalf("after undo %q", got)
	}
	segs, _ = LoadSegments(ctx, a, as.ID)
	if w := segs[0].Words[2]; w.Text != "Fern" || w.Score != 0.42 || w.Fixed || w.From != 0.5 || w.To != 0.7 {
		t.Fatalf("restored %+v", w)
	}
}

// Clips added in the same millisecond (a fast machine, a bulk import) are
// listed in one order every time: by when they were added, then by ID.
func TestFindKeepsOneOrderForClipsAddedTogether(t *testing.T) {
	a := testapp.New(t)
	for _, backward := range []bool{false, true} {
		findKeepsOneOrder(t, a, backward)
	}
}

// findKeepsOneOrder stores four clips at the same moment (first ID first, or
// last ID first) and one an hour earlier, and finds them three times.
func findKeepsOneOrder(t *testing.T, a *app.App, backward bool) {
	t.Helper()
	ctx := context.Background()
	prj := ids.New(ids.Project)
	same := time.Now().UTC().Truncate(time.Millisecond)
	var made []string
	for range 4 {
		made = append(made, ids.New(ids.Asset))
	}
	slices.Sort(made)
	stored := slices.Clone(made)
	if backward {
		slices.Reverse(stored)
	}
	for _, id := range stored {
		as := clip(t, a, prj, words("Ask@0-0.3 Fern@0.4-0.5 Brook@0.55-0.9"))
		old := as.ID
		if _, err := a.DB.C(store.CAssets).DeleteOne(ctx, bson.M{"_id": old}); err != nil {
			t.Fatal(err)
		}
		as.ID, as.CreatedAt = id, same
		if err := a.DB.Insert(ctx, store.CAssets, *as); err != nil {
			t.Fatal(err)
		}
		if _, err := a.DB.C(store.CSegments).UpdateMany(ctx, bson.M{"assetId": old}, bson.M{"$set": bson.M{"assetId": id}}); err != nil {
			t.Fatal(err)
		}
	}
	later := clip(t, a, prj, words("Fern@1-1.1 Brook@1.2-1.5"))
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": later.ID}, bson.M{"$set": bson.M{"createdAt": same.Add(-time.Hour)}}); err != nil {
		t.Fatal(err)
	}
	want := append([]string{later.ID}, made...)
	for range 3 {
		f, err := Find(ctx, a, FindInput{Query: "Fern Brook", Scope: Scope{ProjectID: prj}})
		if err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, h := range f.Hits {
			got = append(got, h.AssetID)
		}
		if !slices.Equal(got, want) {
			t.Fatalf("places in clips %v, want %v (earliest first, then by ID)", got, want)
		}
	}
}

func TestReplaceAcrossClipsJoinsAndSplits(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	prj := ids.New(ids.Project)
	one := clip(t, a, prj, words("Ask@0-0.3 Fern@0.4-0.5 Brook,@0.55-0.9 please.@1-1.3"))
	two := clip(t, a, prj, words("fern@5-5.1 brook@5.15-5.5 again@5.6-6"), words("at@7-7.2 the@7.3-7.4 Maplegrove@7.5-8.3"))
	other := clip(t, a, "", words("Fern@1-1.1 Brook@1.2-1.5"))
	f, err := Find(ctx, a, FindInput{Query: "Fern Brook", Scope: Scope{ProjectID: prj}, With: ptr("FernBrook")})
	// The clips may have been added in the same millisecond: find clip one's
	// place by its clip, not by where it is in the list.
	at := slices.IndexFunc(f.Hits, func(h Hit) bool { return h.AssetID == one.ID })
	if err != nil || f.Total != 2 || f.Clips != 2 || at < 0 || f.Hits[at].New != "FernBrook," || f.Hits[at].Before != "Ask" {
		t.Fatalf("find %+v %v", f, err)
	}
	// Tick only the place in clip one.
	r, err := Replace(ctx, a, ReplaceInput{Query: "Fern Brook", With: "FernBrook", Scope: Scope{ProjectID: prj}, Only: []string{f.Hits[at].ID}})
	if err != nil || len(r.Fixes) != 1 || r.Clips != 1 {
		t.Fatalf("replace %+v %v", r, err)
	}
	if got := textOf(t, a, one.ID); got != "Ask FernBrook, please." {
		t.Fatalf("one %q", got)
	}
	if got := textOf(t, a, two.ID); !strings.HasPrefix(got, "fern brook") {
		t.Fatalf("two changed %q", got)
	}
	// Everything: the rest, and a split.
	if _, err := Replace(ctx, a, ReplaceInput{Query: "fern brook", With: "FernBrook", Scope: Scope{All: true}}); err != nil {
		t.Fatal(err)
	}
	if got := textOf(t, a, other.ID); got != "FernBrook" {
		t.Fatalf("other %q", got)
	}
	r2, err := Replace(ctx, a, ReplaceInput{Query: "Maplegrove", With: "Maple Grove", Scope: Scope{AssetID: two.ID}})
	if err != nil || len(r2.Fixes) != 1 {
		t.Fatal(err)
	}
	segs, _ := LoadSegments(ctx, a, two.ID)
	ws := segs[1].Words
	if len(ws) != 4 || ws[2].Text != "Maple" || ws[3].Text != "Grove" || ws[2].From != 7.5 || ws[3].To != 8.3 || ws[2].To != ws[3].From {
		t.Fatalf("split %+v", ws)
	}
	// A whole replace goes back in one undo.
	res, err := Undo(ctx, a, "", r2.Batch)
	if err != nil || len(res.Undone) != 1 {
		t.Fatalf("batch undo %+v %v", res, err)
	}
}

func ptr(s string) *string { return &s }

// settingsProject is a project whose Content Settings are its own and the
// default (D192), like a workspace's first project: its spelling list is the
// one footage in no project uses.
func settingsProject(t *testing.T, a *app.App) string {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	p := models.Project{ID: ids.New(ids.Project), WorkspaceID: a.WorkspaceID(), Name: "Spelled", Branches: map[string]models.BranchHead{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CProjects, p); err != nil {
		t.Fatal(err)
	}
	if _, err := store.MoveContentSettings(ctx, a.DB, a.WorkspaceID()); err != nil {
		t.Fatal(err)
	}
	return p.ID
}

func TestSpellingListCorrectsAndSuggests(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	prj := settingsProject(t, a)
	as := clip(t, a, prj, words("We@0-0.2 met@0.25-0.4 fern-brook@0.5-1 at@1.1-1.2 maple@1.3-1.6 grove@1.65-2 on@2.1-2.2 elmwood@2.3-2.9 and@3-3.1 rose@3.2-3.5 Fernbrooke's@3.6-4.2 thing.@4.3-4.6"))
	if _, _, err := AddEntry(ctx, a, AddInput{ProjectID: prj, Word: "FernBrook", HeardAs: []string{"Fernbrooke"}}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AddEntry(ctx, a, AddInput{Word: "Nobody's"}); err == nil {
		t.Fatal("a list is a project's settings' list")
	}
	for _, w := range []string{"Maple Grove", "Elmwood", "Rose"} {
		if _, _, err := AddEntry(ctx, a, AddInput{ProjectID: prj, Word: w}); err != nil {
			t.Fatal(err)
		}
	}
	entries, err := ListsFor(ctx, a, []string{prj})
	if err != nil || len(entries) != 4 {
		t.Fatalf("lists %+v %v", entries, err)
	}
	ch, err := Edit(ctx, a, as.ID, EditOptions{}, func(c *Clip) error {
		c.ApplySpelling(entries, nil)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// "elmwood" and "rose" differ only in capitals that could start a sentence: left alone.
	if got := textOf(t, a, as.ID); got != "We met FernBrook at Maple Grove on elmwood and rose FernBrook's thing." {
		t.Fatalf("spelled %q (%d fixes)", got, len(ch.Fixes))
	}
	if HintWords(ctx, a, []string{prj}, 10) == nil {
		t.Fatal("no hints")
	}
	// Repeated fixes suggest an entry.
	other := clip(t, a, prj, words("Lena@0-0.4 and@0.5-0.6 Kestrel@0.7-1 by@1.05-1.4"), words("Kestrel@3-3.3 by@3.35-3.7"), words("kestrelby@5-5.6"))
	for _, at := range [][2]float64{{0.7, 1.4}, {3, 3.7}, {5, 5.6}} {
		if _, err := FixWords(ctx, a, FixInput{AssetID: other.ID, From: at[0], To: at[1], Text: "Kestrelby", ProjectID: prj}); err != nil {
			t.Fatal(err)
		}
	}
	sug, err := Suggestions(ctx, a, prj)
	if err != nil || len(sug) != 1 || sug[0].Word != "Kestrelby" || sug[0].Times != 3 {
		t.Fatalf("suggestions %+v %v", sug, err)
	}
	if _, _, err := AddEntry(ctx, a, AddInput{ProjectID: prj, Word: "Kestrelby"}); err != nil {
		t.Fatal(err)
	}
	if sug, _ := Suggestions(ctx, a, prj); len(sug) != 0 {
		t.Fatalf("listed words aren't suggested %+v", sug)
	}
}

func TestPinsKeepTheWordsAVersionWasMadeWith(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	as := clip(t, a, "", words("We@0-0.2 met@0.25-0.4 Fern@0.5-0.7 Brook@0.75-1"))
	segs, _ := LoadSegments(ctx, a, as.ID)
	live := Engine(segs)
	pins, err := Pin(ctx, a, map[string][]editspec.TranscriptSegment{as.ID: live}, map[string]string{as.ID: "word"}, map[string]int{as.ID: 0})
	if err != nil || len(pins) != 1 || pins[0].Hash != Hash(live, "word") {
		t.Fatalf("pins %+v %v", pins, err)
	}
	r := &models.Revision{Transcripts: pins}
	if _, err := FixWords(ctx, a, FixInput{AssetID: as.ID, From: 0.5, To: 1, Text: "FernBrook"}); err != nil {
		t.Fatal(err)
	}
	var cur models.Asset
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, &cur)
	got, prec, ok, err := For(ctx, a, &cur, PinsOf(r))
	if err != nil || !ok || prec != "word" || got[0].Text != "We met Fern Brook" {
		t.Fatalf("pinned %+v %s %v", got, prec, err)
	}
	now, _, _, _ := For(ctx, a, &cur, nil)
	if now[0].Text != "We met FernBrook" {
		t.Fatalf("live %+v", now)
	}
	// A version saved before pins: the machine's words.
	old, _, _, _ := For(ctx, a, &cur, PinsOf(&models.Revision{}))
	if old[0].Text != "We met Fern Brook" {
		t.Fatalf("legacy %+v", old)
	}
	// Pending for the pinned version: the fix; taken back since: nothing.
	spec := editspec.Spec{Output: editspec.Output{FPS: 30}, Clips: []editspec.Clip{{ID: "a", AssetID: as.ID, In: 0, Out: 1.2, Speed: 1}}}
	r.Spec = spec
	p, err := PendingFor(ctx, a, r)
	if err != nil || len(p.Fixes) != 1 || p.Fixes[0].AfterText != "FernBrook" || p.Fixes[0].At != 0.5 {
		t.Fatalf("pending %+v %v", p, err)
	}
	var fx models.TranscriptFix
	_ = a.DB.C(store.CTranscriptFixes).FindOne(ctx, bson.M{"assetId": as.ID}).Decode(&fx)
	if _, err := Undo(ctx, a, fx.ID, ""); err != nil {
		t.Fatal(err)
	}
	if p, _ := PendingFor(ctx, a, r); len(p.Fixes) != 0 {
		t.Fatalf("undone fix still pending %+v", p.Fixes)
	}
	// A version made after the fix has it; undoing it then is pending for that version.
	_, _ = FixWords(ctx, a, FixInput{AssetID: as.ID, From: 0.5, To: 1, Text: "FernBrook"})
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, &cur)
	r2 := &models.Revision{Spec: spec, Transcripts: []models.TranscriptPin{{AssetID: as.ID, Rev: cur.TextFixes.Rev, Hash: "x"}}}
	var last models.TranscriptFix
	_ = a.DB.C(store.CTranscriptFixes).FindOne(ctx, bson.M{"assetId": as.ID, "undone": nil}).Decode(&last)
	_, _ = Undo(ctx, a, last.ID, "")
	if p, _ := PendingFor(ctx, a, r2); len(p.Fixes) != 1 || !p.Fixes[0].TakenBack {
		t.Fatalf("taken back %+v", p.Fixes)
	}
}

// A fix writes its lines before it moves the clip's revision: a version saved
// in between read the new words with the old number, and must not offer that
// fix as made since (D145).
func TestPinSettlesTheRevisionOfWordsReadWhileAFixLands(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	as := clip(t, a, "", words("We@0-0.2 met@0.25-0.4 Fern@0.5-0.7 Brook@0.75-1"))
	before, _ := LoadSegments(ctx, a, as.ID)
	old := Engine(before)
	if _, err := FixWords(ctx, a, FixInput{AssetID: as.ID, From: 0.5, To: 1, Text: "FernBrook"}); err != nil {
		t.Fatal(err)
	}
	after, _ := LoadSegments(ctx, a, as.ID)
	fixed := Engine(after)
	prec := map[string]string{as.ID: "word"}
	read0 := map[string]int{as.ID: 0}

	// New words read with revision 0: they are revision 1's.
	pins, err := Pin(ctx, a, map[string][]editspec.TranscriptSegment{as.ID: fixed}, prec, read0)
	if err != nil || len(pins) != 1 || pins[0].Rev != 1 {
		t.Fatalf("new words %+v %v", pins, err)
	}
	spec := editspec.Spec{Output: editspec.Output{FPS: 30}, Clips: []editspec.Clip{{ID: "a", AssetID: as.ID, In: 0, Out: 1.2, Speed: 1}}}
	if p, _ := PendingFor(ctx, a, &models.Revision{Spec: spec, Transcripts: pins}); len(p.Fixes) != 0 {
		t.Fatalf("a fix the version has is offered %+v", p.Fixes)
	}
	// Words read before the fix keep revision 0 (the fix stays offered).
	if pins, err = Pin(ctx, a, map[string][]editspec.TranscriptSegment{as.ID: old}, prec, read0); err != nil || pins[0].Rev != 0 {
		t.Fatalf("old words %+v %v", pins, err)
	}
	// A fix under way: the save waits for it, then settles.
	until := time.Now().Add(time.Minute)
	_, _ = a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": as.ID}, bson.M{"$set": bson.M{"textFixes.lock": models.TranscriptLock{Token: "t", Until: until}}})
	go func() {
		time.Sleep(300 * time.Millisecond)
		_, _ = a.DB.C(store.CAssets).UpdateOne(context.Background(), bson.M{"_id": as.ID}, bson.M{"$unset": bson.M{"textFixes.lock": ""}})
	}()
	if pins, err = Pin(ctx, a, map[string][]editspec.TranscriptSegment{as.ID: fixed}, prec, map[string]int{as.ID: 1}); err != nil || pins[0].Rev != 1 {
		t.Fatalf("under way %+v %v", pins, err)
	}
	// One that never finishes in time: the number read is kept.
	defer func(w time.Duration) { settleWait = w }(settleWait)
	settleWait = 200 * time.Millisecond
	_, _ = a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": as.ID}, bson.M{"$set": bson.M{"textFixes.lock": models.TranscriptLock{Token: "t", Until: until}}})
	if pins, err = Pin(ctx, a, map[string][]editspec.TranscriptSegment{as.ID: fixed}, prec, read0); err != nil || pins[0].Rev != 0 {
		t.Fatalf("stuck %+v %v", pins, err)
	}
}

func TestFixesCarryOverToANewTranscript(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	as := clip(t, a, "", words("We@0-0.2 met@0.25-0.4 Fern@0.5-0.7 Brook@0.75-1 at@1.1-1.2 Elmhurst@1.3-1.8"))
	if _, err := FixWords(ctx, a, FixInput{AssetID: as.ID, From: 0.5, To: 1, Text: "FernBrook"}); err != nil {
		t.Fatal(err)
	}
	if _, err := FixWords(ctx, a, FixInput{AssetID: as.ID, From: 1.3, To: 1.8, Text: "Elmwood"}); err != nil {
		t.Fatal(err)
	}
	var cur models.Asset
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, &cur)
	prev, err := BeforeTranscribing(ctx, a, &cur)
	if err != nil {
		t.Fatal(err)
	}
	// The new machine transcript: slightly different times; it hears "Furbuk" and "Elm Hurst".
	_, _ = a.DB.C(store.CSegments).DeleteMany(ctx, bson.M{"assetId": as.ID})
	ws := words("We@0.01-0.21 met@0.26-0.41 Furbuk@0.52-1.01 at@1.1-1.2 Elm@1.3-1.5 Hurst@1.55-1.8")
	_ = a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, From: 0.01, To: 1.8,
		Text: JoinWords(ws), Words: ws, Provider: "parakeet", Model: "test"})
	res, err := AfterTranscribing(ctx, a, &cur, prev, nil)
	if err != nil {
		t.Fatal(err)
	}
	// "Elm Hurst" is close to what the machine heard before ("Elmhurst"): carried.
	// "Furbuk" isn't close enough to "Fern Brook" to be sure: reported, not guessed.
	if res.Carry == nil || res.Carry.Carried != 1 || len(res.Carry.Missed) != 1 || res.Carry.Missed[0].AfterText != "FernBrook" || res.Carry.Missed[0].Heard != "met Furbuk at" {
		t.Fatalf("carry %+v", res.Carry)
	}
	if got := textOf(t, a, as.ID); got != "We met Furbuk at Elmwood" {
		t.Fatalf("carried %q", got)
	}
	_ = a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, &cur)
	if cur.TextFixes.Base != 2 || cur.TextFixes.Fixes != 1 {
		t.Fatalf("state %+v", cur.TextFixes)
	}
	// Both machine transcripts are kept.
	for _, b := range []int{1, 2} {
		if _, err := LoadOriginal(ctx, a, as.ID, b); err != nil {
			t.Fatalf("original %d: %v", b, err)
		}
	}
	// Transcribed a third time: the same words as the machine had (carried),
	// and the spelling list catches "Furbuk" now that it says how it's heard.
	if _, _, err := AddEntry(ctx, a, AddInput{ProjectID: settingsProject(t, a), Word: "FernBrook", HeardAs: []string{"Furbuk"}}); err != nil {
		t.Fatal(err)
	}
	prev, _ = BeforeTranscribing(ctx, a, &cur)
	_, _ = a.DB.C(store.CSegments).DeleteMany(ctx, bson.M{"assetId": as.ID})
	ws = words("We@0-0.2 met@0.25-0.4 Furbuk@0.5-1 at@1.1-1.2 Elm@1.3-1.5 Hurst@1.55-1.8")
	_ = a.DB.Insert(ctx, store.CSegments, models.TranscriptSegment{ID: ids.New(ids.Segment), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, From: 0, To: 1.8,
		Text: JoinWords(ws), Words: ws, Provider: "parakeet", Model: "test"})
	entries, _ := ListsFor(ctx, a, nil)
	res, err = AfterTranscribing(ctx, a, &cur, prev, entries)
	if err != nil || res.Carry.Carried != 1 || len(res.Carry.Missed) != 0 || res.Spelled != 1 {
		t.Fatalf("third %+v %+v %v", res, res.Carry, err)
	}
	if got := textOf(t, a, as.ID); got != "We met FernBrook at Elmwood" {
		t.Fatalf("third %q", got)
	}
}

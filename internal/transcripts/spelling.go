package transcripts

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Spelling lists (D147): how names and terms are always written. A spelling
// list is part of a project's Content Settings (D193): entries carry the
// project whose own settings hold them, and a clip uses the lists of the
// settings its projects use (a clip in several projects all of them; footage
// in no project the default's). A transcript is corrected by them when it is
// made (and when a person asks); the transcriber gets the words as hints where
// it can take them; repeated fixes suggest new entries.
//
// What a list changes: the same letters written differently ("Fern Brook",
// "Fern-Brook", "fernbrook" → "FernBrook"; "Maplegrove" → "Maple Grove"), and the other
// ways the entry says a transcript writes it (HeardAs). A difference in
// capitals alone is changed only for entries whose capitals can't be the start
// of a sentence ("FernBrook", "NASA", "Maple Grove"): "rose" never becomes "Rose".

const (
	maxSpellingWords = 6
	maxSpellingLen   = 80
)

// SpellingKey is how entries compare: the word's letters and digits, lowercase.
func SpellingKey(word string) string { return Norm(word) }

// LoadList returns the spelling list of a project's own Content Settings, in
// word order ("" : none).
func LoadList(ctx context.Context, a *app.App, projectID string) ([]models.SpellingEntry, error) {
	if projectID == "" {
		return nil, nil
	}
	filter := bson.M{"projectId": projectID}
	var out []models.SpellingEntry
	if err := a.DB.Find(ctx, store.CSpelling, a.WorkspaceID(), filter, &out); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool { return strings.ToLower(out[i].Word) < strings.ToLower(out[j].Word) })
	return out, nil
}

// ListsFor returns the entries that apply to a clip in these projects: the
// spelling lists of the Content Settings each project uses (D193) — a clip in
// several projects uses all of them; a clip in none, the default's. For the
// same letters the most recently added entry wins.
func ListsFor(ctx context.Context, a *app.App, projectIDs []string) ([]models.SpellingEntry, error) {
	owners, err := content.Owners(ctx, a, projectIDs)
	if err != nil {
		return nil, err
	}
	if len(owners) == 0 {
		return []models.SpellingEntry{}, nil
	}
	var all []models.SpellingEntry
	if err := a.DB.Find(ctx, store.CSpelling, a.WorkspaceID(), bson.M{"projectId": bson.M{"$in": owners}}, &all, options.Find().SetSort(bson.D{{Key: "addedAt", Value: 1}})); err != nil {
		return nil, err
	}
	byKey := map[string]models.SpellingEntry{}
	for _, e := range all {
		byKey[e.Key] = e
	}
	out := make([]models.SpellingEntry, 0, len(byKey))
	for _, e := range byKey {
		out = append(out, e)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// CheckWord says what is wrong with a word for a spelling list ("" when nothing is).
func CheckWord(word string) string {
	w := Space(word)
	switch {
	case w == "":
		return "Say how the word is written."
	case utf8.RuneCountInString(w) > maxSpellingLen:
		return "That's longer than a name or term; spelling lists hold words like names, places and brands."
	case len(strings.Fields(w)) > maxSpellingWords:
		return "Spelling lists hold names and short terms of up to six words."
	case SpellingKey(w) == "":
		return "That has no letters or digits to spell."
	}
	return ""
}

// AddInput puts a word on a list.
type AddInput struct {
	ProjectID string // the project whose own Content Settings hold the list
	Word      string
	HeardAs   []string
	Note      string
	FromFixes int
}

// AddEntry puts a word on a list (or updates the entry with the same letters:
// its spelling and the ways it is heard). created is false for an update.
func AddEntry(ctx context.Context, a *app.App, in AddInput) (*models.SpellingEntry, bool, error) {
	w := Space(in.Word)
	if why := CheckWord(w); why != "" {
		return nil, false, apperr.New(apperr.InvalidInput, "%s", why)
	}
	if in.ProjectID == "" {
		return nil, false, apperr.New(apperr.InvalidInput, "Say whose spelling list: a project's settings.")
	}
	key := SpellingKey(w)
	var heard []string
	for _, h := range in.HeardAs {
		h = Space(h)
		if h != "" && SpellingKey(h) != key && CheckWord(h) == "" && !containsFold(heard, h) {
			heard = append(heard, h)
		}
	}
	filter := bson.M{"workspaceId": a.WorkspaceID(), "key": key, "projectId": in.ProjectID}
	var cur models.SpellingEntry
	err := a.DB.C(store.CSpelling).FindOne(ctx, filter).Decode(&cur)
	if err == nil {
		for _, h := range cur.HeardAs {
			if !containsFold(heard, h) {
				heard = append(heard, h)
			}
		}
		set := bson.M{"word": w, "heardAs": heard, "updatedAt": time.Now().UTC()}
		if in.Note != "" {
			set["note"] = in.Note
		}
		if _, err := a.DB.C(store.CSpelling).UpdateOne(ctx, bson.M{"_id": cur.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": set}); err != nil {
			return nil, false, err
		}
		cur.Word, cur.HeardAs = w, heard
		if in.Note != "" {
			cur.Note = in.Note
		}
		return &cur, false, nil
	}
	if !errors.Is(notFound(err), store.ErrNotFound) {
		return nil, false, err
	}
	e := models.SpellingEntry{ID: ids.New(ids.Spelling), WorkspaceID: a.WorkspaceID(), ProjectID: in.ProjectID, Word: w, Key: key, HeardAs: heard, Note: Space(in.Note),
		AddedBy: a.Actor, AddedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(), FromFixes: in.FromFixes}
	if err := a.DB.Insert(ctx, store.CSpelling, e); err != nil {
		if store.IsDuplicateKey(err) {
			return AddEntry(ctx, a, in) // added at the same moment elsewhere: update it
		}
		return nil, false, err
	}
	return &e, true, nil
}

// RemoveEntry takes a word off a list (by its letters). It reports whether it was there.
func RemoveEntry(ctx context.Context, a *app.App, projectID, word string) (*models.SpellingEntry, error) {
	if projectID == "" {
		return nil, store.ErrNotFound
	}
	filter := bson.M{"workspaceId": a.WorkspaceID(), "key": SpellingKey(word), "projectId": projectID}
	var cur models.SpellingEntry
	if err := a.DB.C(store.CSpelling).FindOneAndDelete(ctx, filter).Decode(&cur); err != nil {
		return nil, notFound(err)
	}
	return &cur, nil
}

func containsFold(xs []string, s string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, s) {
			return true
		}
	}
	return false
}

// SpellSpan is a place a spelling list would change.
type SpellSpan struct {
	Span
	Entry models.SpellingEntry
}

// capsMatter reports whether an entry's capitals are its own (not just a
// capital at the start of a word that could be the start of a sentence).
func capsMatter(word string) bool {
	if len(strings.Fields(word)) > 1 {
		return true // "Maple Grove"
	}
	_, core, _ := Split(word)
	first := true
	for _, r := range core {
		if !unicode.IsLetter(r) {
			continue
		}
		if unicode.IsUpper(r) && !first {
			return true // a capital inside the word: "FernBrook", "McKay", "iPhone", "NASA"
		}
		first = false
	}
	return false
}

// SpellingSpans finds the places the entries would change in a clip's words,
// left to right, longest first, without overlaps.
func SpellingSpans(us []Unit, entries []models.SpellingEntry) []SpellSpan {
	if len(entries) == 0 || len(us) == 0 {
		return nil
	}
	type target struct {
		e     models.SpellingEntry
		heard bool
	}
	keys := map[string]target{}
	maxToks := 1
	for _, e := range entries {
		keys[e.Key] = target{e: e}
		maxToks = max(maxToks, len(strings.Fields(e.Word)))
		for _, h := range e.HeardAs {
			if k := SpellingKey(h); k != "" {
				if _, taken := keys[k]; !taken {
					keys[k] = target{e: e, heard: true}
				}
				maxToks = max(maxToks, len(strings.Fields(h)))
			}
		}
	}
	maxToks = min(maxToks+2, maxSpellingWords+2)
	cores := make([]string, len(us))
	norms := make([]string, len(us))
	for i, u := range us {
		_, cores[i], _ = Split(u.Text)
		norms[i] = Norm(cores[i])
	}
	var out []SpellSpan
	for i := 0; i < len(us); {
		if norms[i] == "" {
			i++
			continue
		}
		found := false
		for n := min(maxToks, len(us)-i); n >= 1 && !found; n-- {
			j := i + n - 1
			ok := true
			var joined strings.Builder
			for k := i; k <= j; k++ {
				if norms[k] == "" || (k > i && !joinable(us[k-1], us[k])) {
					ok = false
					break
				}
				joined.WriteString(norms[k])
			}
			if !ok {
				continue
			}
			lead, _, trail := Split(us[i].Text)
			if j != i {
				_, _, trail = Split(us[j].Text)
			}
			key := joined.String()
			t, hit := keys[key]
			// A possessive keeps its ending: "Fern Brook's" → "FernBrook's".
			possessive := ""
			if !hit {
				for _, suf := range []string{"'s", "’s"} {
					if strings.HasSuffix(cores[j], suf) {
						if t2, ok2 := keys[strings.TrimSuffix(key, "s")]; ok2 {
							t, hit, possessive = t2, true, suf
						}
					}
				}
			}
			if !hit {
				continue
			}
			written := strings.Join(cores[i:j+1], " ")
			if possessive != "" {
				written = strings.TrimSuffix(written, possessive)
				trail = possessive + trail
			}
			want := Space(t.e.Word)
			switch {
			case written == want:
				// already written that way
			case !t.heard && strings.EqualFold(written, want) && !capsMatter(want):
				// only capitals differ and they could be the start of a sentence
			default:
				out = append(out, SpellSpan{Span: Span{I: i, J: j, Pre: lead, Post: trail}, Entry: t.e})
			}
			found = true
			i = j + 1
		}
		if !found {
			i++
		}
	}
	return out
}

// ApplySpelling corrects the working copy by the entries: one fix per place.
func (c *Clip) ApplySpelling(entries []models.SpellingEntry, only map[string]bool) []models.TranscriptFix {
	us := c.Units()
	spans := SpellingSpans(us, entries)
	var out []models.TranscriptFix
	for k := len(spans) - 1; k >= 0; k-- {
		sp := spans[k]
		if only != nil && !only[HitID(c.Asset.ID, us[sp.I].From, us[sp.J].To)] {
			continue
		}
		newText := Space(sp.Pre + Space(sp.Entry.Word) + sp.Post)
		words := RetypeWords(WordsOf(us, sp.I, sp.J), newText)
		out = append(out, c.Replace(sp.I, sp.J, words, models.TranscriptFix{Source: models.FixBySpelling, Query: sp.Entry.Word}))
		us = c.Units()
	}
	return out
}

// SpellingHits previews what the entries would change in a clip.
func SpellingHits(as *models.Asset, segs []models.TranscriptSegment, entries []models.SpellingEntry) []Hit {
	us := Units(segs)
	var out []Hit
	for _, sp := range SpellingSpans(us, entries) {
		h := Hit{ID: HitID(as.ID, us[sp.I].From, us[sp.J].To), AssetID: as.ID, Clip: as.DisplayTitle(), From: round3(us[sp.I].From), To: round3(us[sp.J].To),
			Text: Text(us, sp.I, sp.J), Before: Text(us, max(0, sp.I-8), sp.I-1), After: Text(us, sp.J+1, min(len(us)-1, sp.J+8)),
			New: Space(sp.Pre + Space(sp.Entry.Word) + sp.Post), Speaker: segs[us[sp.I].Seg].SpeakerName}
		for k := sp.I; k <= sp.J; k++ {
			h.Fixed = h.Fixed || us[k].Fixed
		}
		out = append(out, h)
	}
	return out
}

// HintWords are the words a transcriber is told to expect for a clip in these
// projects: the spelling lists' words, most recent first, at most limit.
func HintWords(ctx context.Context, a *app.App, projectIDs []string, limit int) []string {
	es, err := ListsFor(ctx, a, projectIDs)
	if err != nil || len(es) == 0 {
		return nil
	}
	sort.SliceStable(es, func(i, j int) bool { return es[i].AddedAt.After(es[j].AddedAt) })
	var out []string
	for _, e := range es {
		if limit > 0 && len(out) >= limit {
			break
		}
		out = append(out, Space(e.Word))
	}
	return out
}

// Suggestion is a word people keep fixing the same way that isn't on a
// spelling list yet ("You fixed “Fern Brook” 3 times").
type Suggestion struct {
	Word string `json:"word"`
	Key  string `json:"key"`
	// Heard are the ways the transcripts had it.
	Heard []string `json:"heard"`
	// Times counts the separate fixes (a find and replace is one); Places
	// every place they changed.
	Times  int `json:"times"`
	Places int `json:"places"`
}

// suggestAt: separate fixes, or places changed, that make a suggestion.
const (
	suggestTimes  = 3
	suggestPlaces = 5
)

// Suggestions lists words people fixed the same way again and again in a
// project (or anywhere, projectID ""), that the spelling list it uses (the
// default's for "") doesn't have.
func Suggestions(ctx context.Context, a *app.App, projectID string) ([]Suggestion, error) {
	filter := bson.M{"kind": models.FixWords, "source": bson.M{"$in": bson.A{models.FixByHand, models.FixByReplace}}, "undone": nil}
	if projectID != "" {
		filter["projectId"] = projectID
	}
	var fixes []models.TranscriptFix
	if err := a.DB.Find(ctx, store.CTranscriptFixes, a.WorkspaceID(), filter, &fixes,
		options.Find().SetSort(bson.D{{Key: "at", Value: -1}}).SetLimit(3000).SetProjection(bson.M{"before": 0, "after": 0})); err != nil {
		return nil, err
	}
	var ps []string
	if projectID != "" {
		ps = []string{projectID}
	}
	listed, err := ListsFor(ctx, a, ps)
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, e := range listed {
		have[e.Key] = true
	}
	type acc struct {
		word    string
		heard   []string
		batches map[string]bool
		places  int
	}
	by := map[string]*acc{}
	var order []string
	for _, f := range fixes {
		word := termOf(f.AfterText)
		key := SpellingKey(word)
		if word == "" || have[key] || SpellingKey(f.BeforeText) == "" {
			continue
		}
		before := termOf(f.BeforeText)
		if before == word {
			continue
		}
		x := by[key]
		if x == nil {
			x = &acc{word: word, batches: map[string]bool{}}
			by[key] = x
			order = append(order, key)
		}
		x.batches[f.BatchID] = true
		x.places++
		if before != "" && !containsFold(x.heard, before) && len(x.heard) < 5 {
			x.heard = append(x.heard, before)
		}
	}
	var out []Suggestion
	for _, k := range order {
		x := by[k]
		if len(x.batches) >= suggestTimes || x.places >= suggestPlaces {
			out = append(out, Suggestion{Word: x.word, Key: k, Heard: x.heard, Times: len(x.batches), Places: x.places})
		}
	}
	return out, nil
}

// termOf is the words of a fix as a term: punctuation around them dropped;
// "" when it is more than a short name or term.
func termOf(text string) string {
	toks := strings.Fields(text)
	if len(toks) == 0 || len(toks) > 4 {
		return ""
	}
	cores := make([]string, 0, len(toks))
	for _, t := range toks {
		if _, c, _ := Split(t); c != "" {
			cores = append(cores, c)
		}
	}
	return strings.Join(cores, " ")
}

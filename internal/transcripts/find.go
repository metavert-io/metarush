package transcripts

import (
	"context"
	"errors"
	"fmt"
	"math"
	"regexp"
	"slices"
	"sort"
	"strings"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Scope is where to look: one clip, a project's clips, or every clip.
type Scope struct {
	AssetID   string
	ProjectID string
	All       bool
	// Projects: clips in any of these projects; with NoProject also clips in
	// no project (the clips a Content Settings' spelling list corrects, D193).
	Projects  []string
	NoProject bool
}

// Hit is one place the words are said.
type Hit struct {
	// ID names the place for replace ("<clip>@<start ms>-<end ms>").
	ID      string  `json:"id"`
	AssetID string  `json:"assetId"`
	Clip    string  `json:"clip" jsonschema:"description=The clip's title."`
	From    float64 `json:"from" jsonschema:"description=Where the words start (source seconds)."`
	To      float64 `json:"to" jsonschema:"description=Where they end (source seconds)."`
	Text    string  `json:"text" jsonschema:"description=The words as the transcript has them."`
	// Before and After are up to eight words on each side.
	Before string `json:"before"`
	After  string `json:"after"`
	// New is what the words would become (replace previews).
	New     string `json:"new,omitempty"`
	Speaker string `json:"speaker,omitempty"`
	// Fixed: a person already wrote some of these words.
	Fixed bool `json:"fixed,omitempty"`
	// Same: the words already read like that (nothing would change).
	Same bool `json:"same,omitempty"`
}

// HitID names a place.
func HitID(assetID string, from, to float64) string {
	return fmt.Sprintf("%s@%d-%d", assetID, int64(math.Round(from*1000)), int64(math.Round(to*1000)))
}

// FindInput is what to look for and where.
type FindInput struct {
	Query string
	// With, when set, previews a replace: each hit says what it would become.
	With    *string
	Scope   Scope
	Options MatchOptions
	Limit   int
}

// FindResult lists the places.
type FindResult struct {
	Hits  []Hit
	Total int
	Clips int
	// More: there are more places than Hits holds.
	More bool
}

// clipsIn lists the clips of a scope that may say the query (a quick text
// search narrows them), with a transcript ready, in the order people added them
// (clips added in the same millisecond by their IDs, so the list never
// reorders itself).
func clipsIn(ctx context.Context, a *app.App, sc Scope, probe string) ([]models.Asset, error) {
	filter := bson.M{"transcript.state": "available", "trash": bson.M{"$exists": false}}
	switch {
	case sc.AssetID != "":
		filter["_id"] = sc.AssetID
		delete(filter, "trash")
	case sc.ProjectID != "":
		filter["projectIds"] = sc.ProjectID
	case len(sc.Projects) > 0 || sc.NoProject:
		var or bson.A
		if len(sc.Projects) > 0 {
			or = append(or, bson.M{"projectIds": bson.M{"$in": sc.Projects}})
		}
		if sc.NoProject {
			or = append(or, bson.M{"projectIds": bson.M{"$exists": false}}, bson.M{"projectIds": bson.M{"$size": 0}})
		}
		filter["$or"] = or
	case sc.All:
	default:
		return nil, apperr.New(apperr.InvalidInput, "Say where to look: one clip, a project, or everywhere.")
	}
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), filter, &assets,
		options.Find().SetProjection(bson.M{"name": 1, "title": 1, "projectIds": 1, "transcript": 1, "textFixes": 1, "createdAt": 1}).SetSort(bson.D{{Key: "createdAt", Value: 1}, {Key: "_id", Value: 1}})); err != nil {
		return nil, err
	}
	if probe == "" || len(assets) == 0 {
		return assets, nil
	}
	segFilter := bson.M{"text": bson.M{"$regex": probeRegex(probe), "$options": "i"}}
	if !sc.All {
		in := make([]string, len(assets))
		for i, as := range assets {
			in[i] = as.ID
		}
		segFilter["assetId"] = bson.M{"$in": in}
	}
	segFilter["workspaceId"] = a.WorkspaceID()
	var have []string
	if err := a.DB.C(store.CSegments).Distinct(ctx, "assetId", segFilter).Decode(&have); err != nil {
		return nil, err
	}
	out := assets[:0]
	for _, as := range assets {
		if slices.Contains(have, as.ID) {
			out = append(out, as)
		}
	}
	return out, nil
}

// Clips lists the clips of a scope that have a transcript ready.
func Clips(ctx context.Context, a *app.App, sc Scope) ([]models.Asset, error) {
	return clipsIn(ctx, a, sc, "")
}

// probe is the longest word of a query: every clip that says the query has
// it in some line. A query in a script written without spaces (D244) is its
// letters and digits, which the clip's line has with punctuation or spaces
// between them perhaps (probeRegex allows them).
func probe(q string) string {
	if hasUnspaced(q) {
		var b strings.Builder
		for _, r := range q {
			if isWordRune(r) || r == 'ー' || r == '々' {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	best := ""
	for _, f := range strings.Fields(q) {
		_, core, _ := Split(f)
		if utf8.RuneCountInString(core) > utf8.RuneCountInString(best) {
			best = core
		}
	}
	return best
}

// unspacedGap is what may come between two letters of a line in a script
// written without spaces: spaces (lines stored before D244 joined pieces
// with them), ASCII punctuation, CJK punctuation and fullwidth punctuation.
const unspacedGap = `[\s[:punct:]\x{3000}-\x{303F}\x{FF01}-\x{FF0F}\x{FF1A}-\x{FF20}\x{FF3B}-\x{FF40}\x{FF5B}-\x{FF65}]*`

func probeRegex(word string) string {
	var b strings.Builder
	gap := hasUnspaced(word)
	for i, r := range []rune(word) {
		if gap && i > 0 {
			b.WriteString(unspacedGap)
		}
		switch r {
		case '\'', '’', '‘', 'ʼ':
			b.WriteString("['’‘ʼ]")
		default:
			b.WriteString(regexp.QuoteMeta(string(r)))
		}
	}
	return b.String()
}

// Find lists every place the words are said in a scope, with the words around them.
func Find(ctx context.Context, a *app.App, in FindInput) (*FindResult, error) {
	q := Space(in.Query)
	if q == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say what to look for.")
	}
	if !in.Options.Parts && len(QueryTokens(q)) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "Look for words: “%s” has none.", q)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 200
	}
	clips, err := clipsIn(ctx, a, in.Scope, probe(q))
	if err != nil {
		return nil, err
	}
	res := &FindResult{Hits: []Hit{}}
	for i := range clips {
		as := &clips[i]
		segs, err := LoadSegments(ctx, a, as.ID)
		if err != nil {
			return nil, err
		}
		hits := hitsIn(as, segs, q, in.With, in.Options)
		if len(hits) == 0 {
			continue
		}
		res.Clips++
		res.Total += len(hits)
		for _, h := range hits {
			if len(res.Hits) < limit {
				res.Hits = append(res.Hits, h)
			} else {
				res.More = true
			}
		}
	}
	return res, nil
}

// hitsIn finds the places in one clip.
func hitsIn(as *models.Asset, segs []models.TranscriptSegment, q string, with *string, o MatchOptions) []Hit {
	us := Units(segs)
	spans := Match(us, q, o)
	out := make([]Hit, 0, len(spans))
	for _, sp := range spans {
		h := Hit{ID: HitID(as.ID, us[sp.I].From, us[sp.J].To), AssetID: as.ID, Clip: as.DisplayTitle(), From: round3(us[sp.I].From), To: round3(us[sp.J].To),
			Text: Text(us, sp.I, sp.J), Before: Text(us, max(0, sp.I-8), sp.I-1), After: Text(us, sp.J+1, min(len(us)-1, sp.J+8))}
		if sp.I == 0 {
			h.Before = ""
		}
		if sp.J == len(us)-1 {
			h.After = ""
		}
		for k := sp.I; k <= sp.J; k++ {
			h.Fixed = h.Fixed || us[k].Fixed
		}
		s := segs[us[sp.I].Seg]
		h.Speaker = s.SpeakerName
		if with != nil {
			h.New = Space(sp.Pre + *with + sp.Post)
			h.Same = h.New == h.Text
		}
		out = append(out, h)
	}
	return out
}

// ReplaceInput says what to replace with what, where, and which places.
type ReplaceInput struct {
	Query   string
	With    string
	Scope   Scope
	Options MatchOptions
	// Only lists the places to change (Hit.ID); empty changes every place.
	Only      []string
	ProjectID string
	Source    string
	Batch     string
}

// ReplaceResult says what changed.
type ReplaceResult struct {
	Batch   string
	Fixes   []models.TranscriptFix
	Clips   int
	Changes []Change
	// Gone are ticked places that aren't said that way any more.
	Gone []string
}

// Replace changes every place the words are said in a scope (or only the
// ticked ones) to the new words, one fix per place in one batch. Joining or
// splitting words keeps the time they were said in.
func Replace(ctx context.Context, a *app.App, in ReplaceInput) (*ReplaceResult, error) {
	q := Space(in.Query)
	if q == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say what to replace.")
	}
	with := Space(in.With)
	src := in.Source
	if src == "" {
		src = models.FixByReplace
	}
	clips, err := clipsIn(ctx, a, in.Scope, probe(q))
	if err != nil {
		return nil, err
	}
	only := map[string]bool{}
	for _, id := range in.Only {
		only[id] = true
	}
	res := &ReplaceResult{Batch: in.Batch}
	if res.Batch == "" {
		res.Batch = ids.New(ids.FixBatch)
	}
	seen := map[string]bool{}
	for i := range clips {
		as := clips[i]
		ch, err := Edit(ctx, a, as.ID, EditOptions{Batch: res.Batch, ProjectID: in.ProjectID}, func(c *Clip) error {
			us := c.Units()
			spans := Match(us, q, in.Options)
			for k := len(spans) - 1; k >= 0; k-- {
				sp := spans[k]
				id := HitID(as.ID, us[sp.I].From, us[sp.J].To)
				seen[id] = true
				if len(only) > 0 && !only[id] {
					continue
				}
				newText := Space(sp.Pre + with + sp.Post)
				if newText == Text(us, sp.I, sp.J) {
					continue
				}
				words := RetypeWords(WordsOf(us, sp.I, sp.J), newText)
				c.Replace(sp.I, sp.J, words, models.TranscriptFix{Source: src, Query: q})
			}
			return nil
		})
		if err != nil {
			if errors.Is(err, ErrNoTranscript) || errors.Is(err, store.ErrNotFound) {
				continue
			}
			return nil, err
		}
		if len(ch.Fixes) > 0 {
			res.Clips++
			res.Fixes = append(res.Fixes, ch.Fixes...)
			res.Changes = append(res.Changes, *ch)
		}
	}
	for id := range only {
		if !seen[id] {
			res.Gone = append(res.Gone, id)
		}
	}
	sort.Strings(res.Gone)
	sortFixes(res.Fixes)
	return res, nil
}

// RetypeWords is the whole stretch after retyping it: words that stay keep
// their times and confidence, changed ones take the time of what they replace.
func RetypeWords(old []models.Word, text string) []models.Word {
	blocks := Retype(old, text)
	var out []models.Word
	at := 0
	for _, b := range blocks {
		out = append(out, old[at:max(at, b.I)]...)
		out = append(out, b.Words...)
		at = b.J + 1
	}
	if at < len(old) {
		out = append(out, old[at:]...)
	}
	return out
}

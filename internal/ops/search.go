package ops

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Search ranks, best first.
const (
	RankExact   = "exact"   // the whole query appears as a phrase
	RankAll     = "all"     // every word appears as a whole word
	RankForms   = "forms"   // word forms (the text index matched a stem, e.g. bunnies for bunny)
	RankPartial = "partial" // every word appears as a prefix or substring
)

var rankScore = map[string]int{RankExact: 4, RankAll: 3, RankForms: 2, RankPartial: 1}

// SearchIn is a full-text search across the workspace.
type SearchIn struct {
	Query     string `json:"query" jsonschema:"minLength=1,maxLength=200"`
	ProjectID string `json:"projectId,omitempty"`
	Limit     int    `json:"limit,omitempty" jsonschema:"minimum=1,maximum=100,description=Maximum results per group (default 10)."`
}

// SnippetPart is a piece of a result snippet; Hit parts matched the query.
type SnippetPart struct {
	Text string `json:"text"`
	Hit  bool   `json:"hit,omitempty"`
}

// SearchMoment is a transcript hit: what was said, where and when.
type SearchMoment struct {
	AssetID     string        `json:"assetId"`
	AssetTitle  string        `json:"assetTitle"`
	AssetName   string        `json:"assetName"`
	ProjectID   string        `json:"projectId,omitempty"`
	ProjectName string        `json:"projectName,omitempty"`
	Filed       string        `json:"filed,omitempty" jsonschema:"description=Where a clip in no project is filed: the Inbox\\, a collection or the Archive."`
	SegmentID   string        `json:"segmentId"`
	From        float64       `json:"from"`
	To          float64       `json:"to"`
	At          float64       `json:"at" jsonschema:"description=Source time of the first matching word (word-level timing when available)."`
	Timecode    string        `json:"timecode"`
	Speaker     string        `json:"speaker,omitempty" jsonschema:"description=Voice label in the clip (speaker_00…)."`
	SpeakerName string        `json:"speakerName,omitempty" jsonschema:"description=Who said it, once someone named the voice (or it was recognized)."`
	Text        string        `json:"text"`
	Snippet     []SnippetPart `json:"snippet"`
	Terms       []string      `json:"terms" jsonschema:"description=The matched words as they appear in the transcript (for highlighting)."`
	Rank        string        `json:"rank" jsonschema:"enum=exact,enum=all,enum=forms,enum=partial"`
	Score       float64       `json:"score"`
}

// SearchAsset is a video or audio source whose title, filename or alias
// matched, or in which a person with a matching name speaks.
type SearchAsset struct {
	AssetID     string        `json:"assetId"`
	Title       string        `json:"title"`
	Name        string        `json:"name"`
	Kind        string        `json:"kind"`
	Duration    float64       `json:"duration"`
	ProjectID   string        `json:"projectId,omitempty"`
	ProjectName string        `json:"projectName,omitempty"`
	Filed       string        `json:"filed,omitempty" jsonschema:"description=Where a clip in no project is filed: the Inbox\\, a collection or the Archive."`
	HasThumb    bool          `json:"hasThumb"`
	Snippet     []SnippetPart `json:"snippet"`
	MatchedIn   string        `json:"matchedIn" jsonschema:"enum=title,enum=filename,enum=alias,enum=speaker"`
	// Speaker is the person whose name matched (matchedIn speaker).
	Speaker string `json:"speaker,omitempty"`
	// From names the linked project it comes from (a project's search sees
	// what its project links bring in).
	From string `json:"from,omitempty"`
	Rank string `json:"rank"`
}

// SearchResource is a logo, picture, PDF or font whose name, title, tags,
// description or text matched.
type SearchResource struct {
	ResourceID  string `json:"resourceId"`
	Name        string `json:"name"`
	Title       string `json:"title"`
	Kind        string `json:"kind"`
	ThumbURL    string `json:"thumbUrl"`
	Scope       string `json:"scope" jsonschema:"enum=project,enum=workspace"`
	ProjectID   string `json:"projectId,omitempty"`
	ProjectName string `json:"projectName,omitempty"`
	// From names the linked project it comes from (a project's search).
	From      string        `json:"from,omitempty"`
	Snippet   []SnippetPart `json:"snippet"`
	MatchedIn string        `json:"matchedIn" jsonschema:"enum=name,enum=title,enum=tag,enum=description,enum=text"`
	Rank      string        `json:"rank"`
}

// SearchProject is a project whose name or brief matched.
type SearchProject struct {
	ProjectID    string        `json:"projectId"`
	Name         string        `json:"name"`
	Snippet      []SnippetPart `json:"snippet"`
	MatchedIn    string        `json:"matchedIn" jsonschema:"enum=name,enum=goal,enum=audience,enum=tone"`
	ThumbAssetID string        `json:"thumbAssetId,omitempty"`
	Rank         string        `json:"rank"`
}

// SearchRevision is a revision whose title, instruction or rationale matched.
type SearchRevision struct {
	RevisionID  string        `json:"revisionId"`
	ProjectID   string        `json:"projectId"`
	ProjectName string        `json:"projectName,omitempty"`
	Number      int           `json:"number"`
	Snippet     []SnippetPart `json:"snippet"`
	MatchedIn   string        `json:"matchedIn" jsonschema:"enum=title,enum=instruction,enum=rationale"`
	Rank        string        `json:"rank"`
}

// SearchComment is a review comment that matched.
type SearchComment struct {
	CommentID   string        `json:"commentId"`
	ProjectID   string        `json:"projectId"`
	ProjectName string        `json:"projectName,omitempty"`
	RevisionID  string        `json:"revisionId,omitempty"`
	AssetID     string        `json:"assetId,omitempty"`
	At          *float64      `json:"at,omitempty"`
	Author      string        `json:"author,omitempty"`
	Snippet     []SnippetPart `json:"snippet"`
	Rank        string        `json:"rank"`
	// Outside: someone outside the workspace wrote it (a review link, D315).
	Outside bool `json:"outside,omitempty"`
}

// SearchOut groups results by type, each ranked best first.
type SearchOut struct {
	Query   string         `json:"query"`
	Moments []SearchMoment `json:"moments"`
	// Visuals are stretches of clips where the words are on screen (a thing,
	// an animal, a place or text the on-device index saw) or in a written scene description.
	Visuals   []SearchVisual   `json:"visuals"`
	Videos    []SearchAsset    `json:"videos"`
	Projects  []SearchProject  `json:"projects"`
	Revisions []SearchRevision `json:"revisions"`
	Comments  []SearchComment  `json:"comments"`
	Resources []SearchResource `json:"resources"`
	Total     int              `json:"total"`
	Note      string           `json:"note"`
}

// queryWords lowercases and splits a query into words.
func queryWords(q string) []string {
	return strings.FieldsFunc(strings.ToLower(q), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '\'' })
}

type tok struct {
	text       string // lowercased
	start, end int    // byte offsets in the original
}

func tokenize(s string) []tok {
	var out []tok
	start := -1
	for i, r := range s {
		w := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '\''
		switch {
		case w && start < 0:
			start = i
		case !w && start >= 0:
			out = append(out, tok{strings.ToLower(s[start:i]), start, i})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, tok{strings.ToLower(s[start:]), start, len(s)})
	}
	return out
}

// stem is a crude prefix used to recognize word forms (bunny / bunnies).
func stem(w string) string {
	r := []rune(w)
	if len(r) <= 4 {
		return w
	}
	return string(r[:len(r)-2])
}

// match ranks text against the query words and returns the spans to highlight.
func match(text string, words []string, phrase string) (string, [][2]int) {
	toks := tokenize(text)
	if len(toks) == 0 || len(words) == 0 {
		return "", nil
	}
	lower := strings.ToLower(text)
	var spans [][2]int
	rank := ""
	// Exact phrase (word-aligned).
	if phrase != "" {
		for off := 0; ; {
			i := strings.Index(lower[off:], phrase)
			if i < 0 {
				break
			}
			s, e := off+i, off+i+len(phrase)
			if wordBoundary(lower, s, e) {
				spans = append(spans, [2]int{s, e})
				rank = RankExact
			}
			off = s + 1
		}
	}
	whole, prefix, forms := true, true, true
	var wordSpans [][2]int
	for _, w := range words {
		foundWhole, foundPrefix, foundForm := false, false, false
		st := stem(w)
		for _, t := range toks {
			switch {
			case t.text == w:
				foundWhole = true
				wordSpans = append(wordSpans, [2]int{t.start, t.end})
			case strings.HasPrefix(t.text, w) || (len(w) >= 3 && strings.Contains(t.text, w)):
				foundPrefix = true
				i := strings.Index(t.text, w)
				wordSpans = append(wordSpans, [2]int{t.start + i, t.start + i + len(w)})
			case len([]rune(w)) > 4 && strings.HasPrefix(t.text, st):
				foundForm = true
				wordSpans = append(wordSpans, [2]int{t.start, t.end})
			}
		}
		whole = whole && foundWhole
		prefix = prefix && (foundWhole || foundPrefix)
		forms = forms && (foundWhole || foundPrefix || foundForm)
	}
	if rank == "" {
		switch {
		case whole:
			rank = RankAll
		case prefix:
			rank = RankPartial
		case forms:
			rank = RankForms
		default:
			return "", nil
		}
	}
	if rank != RankExact {
		spans = wordSpans
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i][0] < spans[j][0] })
	return rank, mergeSpans(spans)
}

func wordBoundary(s string, start, end int) bool {
	isWord := func(i int) bool {
		if i < 0 || i >= len(s) {
			return false
		}
		r := rune(s[i])
		return unicode.IsLetter(r) || unicode.IsDigit(r)
	}
	return !isWord(start-1) && !isWord(end)
}

func mergeSpans(sp [][2]int) [][2]int {
	var out [][2]int
	for _, s := range sp {
		if n := len(out); n > 0 && s[0] <= out[n-1][1] {
			if s[1] > out[n-1][1] {
				out[n-1][1] = s[1]
			}
			continue
		}
		out = append(out, s)
	}
	return out
}

// snippet cuts text to about max bytes around the first hit and splits it into parts.
func snippet(text string, spans [][2]int, max int) []SnippetPart {
	start, end := 0, len(text)
	if len(text) > max && len(spans) > 0 {
		c := spans[0][0]
		start = c - max/3
		if start < 0 {
			start = 0
		}
		end = start + max
		if end > len(text) {
			end = len(text)
			start = max0(end - max)
		}
		for start > 0 && start < len(text) && !isSpace(text[start-1]) { // whole words
			start++
		}
		for end < len(text) && end > start && !isSpace(text[end]) {
			end--
		}
	} else if len(text) > max {
		end = max
	}
	var parts []SnippetPart
	if start > 0 {
		parts = append(parts, SnippetPart{Text: "…"})
	}
	pos := start
	for _, s := range spans {
		if s[1] <= start || s[0] >= end {
			continue
		}
		a, b := maxInt(s[0], start), minInt(s[1], end)
		if a > pos {
			parts = append(parts, SnippetPart{Text: text[pos:a]})
		}
		parts = append(parts, SnippetPart{Text: text[a:b], Hit: true})
		pos = b
	}
	if pos < end {
		parts = append(parts, SnippetPart{Text: text[pos:end]})
	}
	if end < len(text) {
		parts = append(parts, SnippetPart{Text: "…"})
	}
	return parts
}

func isSpace(b byte) bool { return b == ' ' || b == '\n' || b == '\t' }
func max0(v int) int {
	if v < 0 {
		return 0
	}
	return v
}
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// wordRegex matches any query word as a case-insensitive substring (bounded fallback).
func wordRegex(words []string) bson.M {
	var alts []string
	for _, w := range words {
		alts = append(alts, regexpQuote(w))
	}
	return bson.M{"$regex": strings.Join(alts, "|"), "$options": "i"}
}

func sourceTimecode(s float64) string {
	m := int(s) / 60
	return fmt.Sprintf("%d:%06.3f", m, s-float64(m*60))
}

// Search runs the search op.
func Search(ctx context.Context, a *app.App, in *SearchIn) (*SearchOut, error) {
	q := strings.TrimSpace(in.Query)
	words := queryWords(q)
	if len(words) == 0 {
		return nil, invalid("search for at least one word")
	}
	if len(words) > 12 {
		words = words[:12]
	}
	phrase := ""
	if len(words) > 1 {
		phrase = strings.Join(words, " ")
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}
	out := &SearchOut{Query: q, Moments: []SearchMoment{}, Visuals: []SearchVisual{}, Videos: []SearchAsset{}, Projects: []SearchProject{}, Revisions: []SearchRevision{}, Comments: []SearchComment{},
		Resources: []SearchResource{}}
	ws := a.WorkspaceID()
	qctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	// A project's search also sees what its project links bring in (D83).
	var view *projectlinks.View
	linkedFrom := map[string]string{} // asset → the linked project it comes from
	if in.ProjectID != "" {
		view, _ = projectlinks.Load(qctx, a, in.ProjectID)
		if view != nil && len(view.Links) > 0 {
			if _, from, err := view.LinkedAssets(qctx, a); err == nil {
				linkedFrom = from
			}
		}
	}

	// Projects (names are needed for every group).
	var projects []models.Project
	pf := bson.M{"archived": false}
	if in.ProjectID != "" {
		pf = bson.M{"_id": in.ProjectID}
	}
	_ = a.DB.Find(qctx, store.CProjects, ws, pf, &projects, options.Find().SetProjection(bson.M{"name": 1, "brief": 1, "coverAssetId": 1, "updatedAt": 1}).SetLimit(2000))
	projName := map[string]string{}
	for _, p := range projects {
		projName[p.ID] = p.Name
	}
	// Archived (deleted) projects are out of search (D196): footage in one is
	// named by another project it is in, or where it is filed.
	shown := func(pids []string) string {
		for _, id := range pids {
			if _, ok := projName[id]; ok {
				return id
			}
		}
		return ""
	}
	if view != nil {
		for id, name := range view.Names {
			if _, ok := projName[id]; !ok {
				projName[id] = name
			}
		}
	}
	if in.ProjectID == "" {
		for _, p := range projects {
			best := ""
			var bestSpans [][2]int
			field, text := "", ""
			for _, f := range []struct{ name, text string }{{"name", p.Name}, {"goal", p.Brief.Goal}, {"audience", p.Brief.Audience}, {"tone", p.Brief.Tone}} {
				if r, sp := match(f.text, words, phrase); r != "" && (best == "" || rankScore[r] > rankScore[best]) {
					best, bestSpans, field, text = r, sp, f.name, f.text
				}
			}
			if best != "" {
				out.Projects = append(out.Projects, SearchProject{ProjectID: p.ID, Name: p.Name, Snippet: snippet(text, bestSpans, 140), MatchedIn: field, ThumbAssetID: p.CoverAssetID, Rank: best})
			}
		}
		sort.SliceStable(out.Projects, func(i, j int) bool { return rankScore[out.Projects[i].Rank] > rankScore[out.Projects[j].Rank] })
		if len(out.Projects) > limit {
			out.Projects = out.Projects[:limit]
		}
	}

	// Assets: title, filename, aliases.
	af := bson.M{"$or": bson.A{bson.M{"title": wordRegex(words)}, bson.M{"name": wordRegex(words)}, bson.M{"aliases.name": wordRegex(words)}}, "trash": bson.M{"$exists": false}}
	var scope []string
	if in.ProjectID != "" {
		var ids []struct {
			ID string `bson:"_id"`
		}
		_ = a.DB.Find(qctx, store.CAssets, ws, bson.M{"projectIds": in.ProjectID}, &ids, options.Find().SetProjection(bson.M{"_id": 1}).SetLimit(5000))
		for _, x := range ids {
			scope = append(scope, x.ID)
		}
		for id := range linkedFrom {
			scope = append(scope, id)
		}
		af["_id"] = bson.M{"$in": scope}
	}
	var assets []models.Asset
	_ = a.DB.Find(qctx, store.CAssets, ws, af, &assets, options.Find().SetProjection(bson.M{"media.streams": 0}).SetLimit(300))
	for _, as := range assets {
		best, field, text := "", "", ""
		var spans [][2]int
		cands := []struct{ name, text string }{{"title", as.DisplayTitle()}, {"filename", as.Name}}
		for _, al := range as.Aliases {
			cands = append(cands, struct{ name, text string }{"alias", al.Name})
		}
		for _, c := range cands {
			if r, sp := match(c.text, words, phrase); r != "" && (best == "" || rankScore[r] > rankScore[best]) {
				best, spans, field, text = r, sp, c.name, c.text
			}
		}
		if best == "" {
			continue
		}
		sa := SearchAsset{AssetID: as.ID, Title: as.DisplayTitle(), Name: as.Name, Kind: as.Kind, Duration: as.Media.Duration, HasThumb: as.Variants[models.VariantThumbnail].StorageKey != "",
			Snippet: snippet(text, spans, 120), MatchedIn: field, Rank: best}
		if pid := shown(as.ProjectIDs); pid != "" {
			sa.ProjectID, sa.ProjectName = pid, projName[pid]
		}
		if src, ok := linkedFrom[as.ID]; ok && !slices.Contains(as.ProjectIDs, in.ProjectID) {
			sa.ProjectID, sa.ProjectName, sa.From = in.ProjectID, projName[in.ProjectID], projName[src]
		}
		out.Videos = append(out.Videos, sa)
	}
	out.Resources = searchResources(qctx, a, in.ProjectID, view, words, phrase, projName, limit)
	out.Videos = append(out.Videos, speakerVideos(qctx, a, words, phrase, in.ProjectID, projName, out.Videos)...)
	sort.SliceStable(out.Videos, func(i, j int) bool { return rankScore[out.Videos[i].Rank] > rankScore[out.Videos[j].Rank] })
	if len(out.Videos) > limit {
		out.Videos = out.Videos[:limit]
	}

	// Transcript moments: the text index (word forms, ranked by score), plus a
	// bounded case-insensitive substring pass for partial words and short queries.
	segs := map[string]models.TranscriptSegment{}
	textScore := map[string]float64{}
	base := bson.M{}
	if scope != nil {
		base["assetId"] = bson.M{"$in": scope}
	}
	{
		f := bson.M{"$text": bson.M{"$search": q}}
		for k, v := range base {
			f[k] = v
		}
		var hits []struct {
			models.TranscriptSegment `bson:",inline"`
			Score                    float64 `bson:"score"`
		}
		opts := options.Find().SetProjection(bson.M{"score": bson.M{"$meta": "textScore"}}).SetSort(bson.D{{Key: "score", Value: bson.M{"$meta": "textScore"}}}).SetLimit(200)
		if err := a.DB.Find(qctx, store.CSegments, ws, f, &hits, opts); err == nil {
			for _, h := range hits {
				segs[h.ID] = h.TranscriptSegment
				textScore[h.ID] = h.Score
			}
		}
	}
	{
		f := bson.M{"text": wordRegex(words)}
		for k, v := range base {
			f[k] = v
		}
		var hits []models.TranscriptSegment
		rctx, rcancel := context.WithTimeout(qctx, 3*time.Second) // the substring pass is a bounded scan
		_ = a.DB.Find(rctx, store.CSegments, ws, f, &hits, options.Find().SetLimit(300))
		rcancel()
		for _, h := range hits {
			if _, ok := segs[h.ID]; !ok {
				segs[h.ID] = h
			}
		}
	}
	assetInfo := map[string]models.Asset{}
	if len(segs) > 0 {
		var need []string
		seen := map[string]bool{}
		for _, s := range segs {
			if !seen[s.AssetID] {
				seen[s.AssetID] = true
				need = append(need, s.AssetID)
			}
		}
		var as []models.Asset
		_ = a.DB.Find(qctx, store.CAssets, ws, bson.M{"_id": bson.M{"$in": need}, "trash": bson.M{"$exists": false}}, &as, options.Find().SetProjection(bson.M{"name": 1, "title": 1, "projectIds": 1}))
		for _, x := range as {
			assetInfo[x.ID] = x
		}
	}
	for id, s := range segs {
		r, spans := match(s.Text, words, phrase)
		if r == "" {
			if _, fromIndex := textScore[id]; !fromIndex {
				continue
			}
			r = RankForms // stemmed match the tokenizer did not see
		}
		as, ok := assetInfo[s.AssetID]
		if !ok {
			continue // asset deleted or out of scope
		}
		m := SearchMoment{AssetID: s.AssetID, AssetTitle: as.DisplayTitle(), AssetName: as.Name, SegmentID: s.ID, From: s.From, To: s.To, At: s.From,
			Speaker: s.Speaker, SpeakerName: s.SpeakerName, Text: s.Text, Snippet: snippet(s.Text, spans, 180), Rank: r}
		if pid := shown(as.ProjectIDs); pid != "" {
			m.ProjectID = pid
			if in.ProjectID != "" {
				m.ProjectID = in.ProjectID
			}
			m.ProjectName = projName[m.ProjectID]
		}
		for _, sp := range spans {
			m.Terms = append(m.Terms, s.Text[sp[0]:sp[1]])
		}
		if m.Terms == nil {
			m.Terms = []string{}
		}
		// Word-level timing: the first word that matched.
		if len(spans) > 0 && len(s.Words) > 0 {
			first := strings.ToLower(s.Text[spans[0][0]:spans[0][1]])
			fw := queryWords(first)
			for _, w := range s.Words {
				lw := strings.ToLower(w.Text)
				if len(fw) > 0 && strings.Contains(lw, fw[0]) {
					m.At = w.From
					break
				}
			}
		}
		m.Timecode = sourceTimecode(m.At)
		m.Score = float64(rankScore[r])*100 + textScore[id] + float64(len(spans))
		out.Moments = append(out.Moments, m)
	}
	sort.SliceStable(out.Moments, func(i, j int) bool {
		if out.Moments[i].Score != out.Moments[j].Score {
			return out.Moments[i].Score > out.Moments[j].Score
		}
		if out.Moments[i].AssetID != out.Moments[j].AssetID {
			return out.Moments[i].AssetTitle < out.Moments[j].AssetTitle
		}
		return out.Moments[i].At < out.Moments[j].At
	})
	if len(out.Moments) > limit {
		out.Moments = out.Moments[:limit]
	}

	// What's on screen and written scene descriptions.
	if vs := visualMatches(qctx, a, words, phrase, in.ProjectID, scope, projName, limit); vs != nil {
		out.Visuals = vs
	}

	// Revisions: title, instruction, rationale.
	rf := bson.M{"detached": bson.M{"$ne": true}, "$or": bson.A{bson.M{"spec.title": wordRegex(words)}, bson.M{"instruction": wordRegex(words)}, bson.M{"rationale": wordRegex(words)}}}
	if in.ProjectID != "" {
		rf["projectId"] = in.ProjectID
	}
	var revs []models.Revision
	_ = a.DB.Find(qctx, store.CRevisions, ws, rf, &revs, options.Find().SetProjection(bson.M{"projectId": 1, "number": 1, "spec.title": 1, "instruction": 1, "rationale": 1, "createdAt": 1}).
		SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(200))
	for _, r := range revs {
		best, field, text := "", "", ""
		var spans [][2]int
		for _, c := range []struct{ name, text string }{{"title", r.Spec.Title}, {"instruction", r.Instruction}, {"rationale", r.Rationale}} {
			if rk, sp := match(c.text, words, phrase); rk != "" && (best == "" || rankScore[rk] > rankScore[best]) {
				best, spans, field, text = rk, sp, c.name, c.text
			}
		}
		if _, ok := projName[r.ProjectID]; !ok && in.ProjectID == "" {
			continue // an archived project's version
		}
		if best != "" {
			out.Revisions = append(out.Revisions, SearchRevision{RevisionID: r.ID, ProjectID: r.ProjectID, ProjectName: projName[r.ProjectID], Number: r.Number,
				Snippet: snippet(text, spans, 160), MatchedIn: field, Rank: best})
		}
	}
	sort.SliceStable(out.Revisions, func(i, j int) bool { return rankScore[out.Revisions[i].Rank] > rankScore[out.Revisions[j].Rank] })
	if len(out.Revisions) > limit {
		out.Revisions = out.Revisions[:limit]
	}

	// Comments.
	cf := bson.M{"body": wordRegex(words)}
	if in.ProjectID != "" {
		cf["projectId"] = in.ProjectID
	}
	var comments []models.Comment
	_ = a.DB.Find(qctx, store.CComments, ws, cf, &comments, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(200))
	for _, c := range comments {
		rk, spans := match(c.Body, words, phrase)
		if _, ok := projName[c.ProjectID]; rk == "" || (!ok && in.ProjectID == "") {
			continue // no match, or an archived project's comment
		}
		sc := SearchComment{CommentID: c.ID, ProjectID: c.ProjectID, ProjectName: projName[c.ProjectID], RevisionID: c.RevisionID,
			AssetID: c.AssetID, At: c.At, Author: c.Author.UserName, Snippet: snippet(c.Body, spans, 160), Rank: rk, Outside: c.Outside != nil}
		if sc.Outside && AIReader(a) {
			for i := range sc.Snippet { // their quotes can't close the ones put around them
				sc.Snippet[i].Text = quoteMarks.Replace(sc.Snippet[i].Text)
			}
			sc.Snippet = append(append([]SnippetPart{{Text: "“"}}, sc.Snippet...), SnippetPart{Text: "” (" + OutsideQuoteNote + ")"})
			sc.Author = OutsideName(c.Outside.Name)
		}
		out.Comments = append(out.Comments, sc)
	}
	sort.SliceStable(out.Comments, func(i, j int) bool { return rankScore[out.Comments[i].Rank] > rankScore[out.Comments[j].Rank] })
	if len(out.Comments) > limit {
		out.Comments = out.Comments[:limit]
	}
	fillFiled(qctx, a, out)
	out.Total = len(out.Moments) + len(out.Visuals) + len(out.Videos) + len(out.Projects) + len(out.Revisions) + len(out.Comments) + len(out.Resources)
	out.Note = "Transcript times are source seconds; machine transcription can contain errors, so verify quotes. Ranks: exact phrase, all words, word forms, partial. " +
		"A person's name finds the videos where they speak (matchedIn speaker); moments carry speakerName once a voice is named. " +
		"visuals are what the on-device index saw on screen (labels such as dog or beach, on-screen text such as EXIT) and written scene descriptions, with time ranges: look at frames there before describing them."
	return out, nil
}

// speakerVideos finds the clips where a person whose name matches speaks
// (named by someone or recognized by voice), one result per clip, skipping
// clips already found by title or filename.
func speakerVideos(ctx context.Context, a *app.App, words []string, phrase, projectID string, projName map[string]string, found []SearchAsset) []SearchAsset {
	var voices []models.AssetSpeaker
	f := bson.M{"name": wordRegex(words), "mergedInto": bson.M{"$in": bson.A{nil, ""}}}
	if err := a.DB.Find(ctx, store.CSpeakers, a.WorkspaceID(), f, &voices, options.Find().SetProjection(bson.M{"assetId": 1, "name": 1, "seconds": 1}).SetLimit(500)); err != nil {
		return nil
	}
	have := map[string]bool{}
	for _, v := range found {
		have[v.AssetID] = true
	}
	type hit struct {
		name, rank string
		spans      [][2]int
		seconds    float64
	}
	best := map[string]hit{}
	var order []string
	for _, v := range voices {
		if have[v.AssetID] {
			continue
		}
		r, spans := match(v.Name, words, phrase)
		if r == "" {
			continue
		}
		cur, ok := best[v.AssetID]
		if !ok {
			order = append(order, v.AssetID)
		}
		if !ok || rankScore[r] > rankScore[cur.rank] || (r == cur.rank && v.Seconds > cur.seconds) {
			best[v.AssetID] = hit{name: v.Name, rank: r, spans: spans, seconds: v.Seconds}
		}
	}
	if len(order) == 0 {
		return nil
	}
	af := bson.M{"_id": bson.M{"$in": order}, "trash": bson.M{"$exists": false}}
	if projectID != "" {
		af["projectIds"] = projectID
	}
	var assets []models.Asset
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), af, &assets, options.Find().SetProjection(bson.M{"media.streams": 0})); err != nil {
		return nil
	}
	byID := map[string]models.Asset{}
	for _, as := range assets {
		byID[as.ID] = as
	}
	var out []SearchAsset
	for _, id := range order {
		as, ok := byID[id]
		if !ok {
			continue // deleted, or outside the project
		}
		h := best[id]
		text := h.name + " speaks in it for " + speakingFor(h.seconds)
		sa := SearchAsset{AssetID: as.ID, Title: as.DisplayTitle(), Name: as.Name, Kind: as.Kind, Duration: as.Media.Duration,
			HasThumb: as.Variants[models.VariantThumbnail].StorageKey != "", Snippet: snippet(text, h.spans, 120), MatchedIn: "speaker", Speaker: h.name, Rank: h.rank}
		if len(as.ProjectIDs) > 0 {
			sa.ProjectID, sa.ProjectName = as.ProjectIDs[0], projName[as.ProjectIDs[0]]
			if projectID != "" {
				sa.ProjectID, sa.ProjectName = projectID, projName[projectID]
			}
		}
		out = append(out, sa)
	}
	return out
}

// searchResources finds logos, pictures, PDFs and fonts by name, title, tag,
// description or the text found in them: a project's own, its links' and the
// workspace library's, or (no project) every one in the workspace.
func searchResources(ctx context.Context, a *app.App, projectID string, view *projectlinks.View, words []string, phrase string, projName map[string]string, limit int) []SearchResource {
	var list []resources.Entry
	if projectID != "" {
		list, _ = resources.Visible(ctx, a, projectID, view, resources.ListOptions{})
	} else {
		var all []models.Resource
		_ = a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"removed": bson.M{"$ne": true}}, &all, options.Find().SetLimit(5000))
		for _, r := range all {
			list = append(list, resources.Entry{Resource: r, Origin: r.Scope})
		}
	}
	out := []SearchResource{}
	for _, e := range list {
		r := e.Resource
		best, field, text := "", "", ""
		var spans [][2]int
		cands := []struct{ name, text string }{{"name", r.Name}, {"title", r.Title}, {"description", r.Description}}
		for _, t := range r.AllTags() {
			cands = append(cands, struct{ name, text string }{"tag", t})
		}
		if v := r.Latest(); v != nil && v.Text != "" {
			cands = append(cands, struct{ name, text string }{"text", v.Text})
		}
		for _, c := range cands {
			if rk, sp := match(c.text, words, phrase); rk != "" && (best == "" || rankScore[rk] > rankScore[best]) {
				best, spans, field, text = rk, sp, c.name, c.text
			}
		}
		if best == "" {
			continue
		}
		row := SearchResource{ResourceID: r.ID, Name: r.Name, Title: r.Title, Kind: r.Kind, ThumbURL: "/media/resources/" + r.ID + "/thumb", Scope: r.Scope,
			ProjectID: r.ProjectID, ProjectName: projName[r.ProjectID], Snippet: snippet(text, spans, 120), MatchedIn: field, Rank: best}
		if v := r.Latest(); v != nil {
			row.ThumbURL += "?v=" + v.SHA256[:12]
		}
		if e.Origin == resources.FromLinked {
			row.From = projName[e.SourceID]
		}
		out = append(out, row)
	}
	sort.SliceStable(out, func(i, j int) bool { return rankScore[out[i].Rank] > rankScore[out[j].Rank] })
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// speakingFor says how long someone speaks, in plain words.
func speakingFor(sec float64) string {
	switch {
	case sec < 1.5:
		return "about a second"
	case sec < 90:
		return fmt.Sprintf("%d seconds", int(sec+0.5))
	}
	m := int(sec/60 + 0.5)
	if m == 1 {
		return "about a minute"
	}
	return fmt.Sprintf("about %d minutes", m)
}

func init() {
	Def("search", "Search transcripts (what was said, with timecodes and who said it), what's on screen (things, animals, places and on-screen text the on-device index saw, and written scene descriptions, with time ranges), people's names (the videos they speak in), video titles/filenames, projects, revisions, comments and resources (logos, pictures, PDFs, fonts: name, tags, description); a project's search includes what its project links bring in; ranked exact phrase > all words > partial", Flags{}, Search)
}

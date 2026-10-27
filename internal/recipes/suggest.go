package recipes

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Suggested changes (D217): MetaRush learns from what people changed in runs.
// Each run's final approved version is compared with the version the run made
// — part by part of the recipe it made: a fixed section's clips (shorter,
// longer, taken out), a chosen section's moments in all, the recipe's graphics
// (taken out, other words, shown for longer or less), the captions, its music
// and sounds (taken out, quieter, louder) and its narration lines' words. When a
// change repeats — in more than half of the runs looked at, and at least twice
// ("you shortened the intro in 4 of 5 runs") — it is offered as a draft
// version with plain reasons. It is always a suggestion: saving it makes a
// draft (recipe.suggestions.save), and only a person makes a version current.
// Runs made again from another (recipe again) build on a cut people already
// changed, so they aren't counted; a change the version a draft starts from
// already has isn't offered again.

// suggestRuns is how many of the newest approved runs are looked at.
const suggestRuns = 12

// Kinds of change (the first part of a suggestion's key).
const (
	sgClipLength    = "length"         // a fixed section's clip plays shorter or longer
	sgClipGone      = "gone"           // a fixed section's clip was taken out
	sgMoments       = "moments"        // a chosen section's moments are shorter or longer in all
	sgGraphicGone   = "graphic-gone"   // one of the recipe's graphics was taken out
	sgWords         = "words"          // a graphic's words, changed to the same new words
	sgVaryWords     = "vary"           // a graphic's words, changed to other words each time: something each run gives
	sgGraphicLength = "graphic-length" // a graphic shows for longer or less
	sgCaptions      = "captions"       // captions turned off, on, or moved
	sgSoundGone     = "sound-gone"     // music or a sound was taken out
	sgLevel         = "level"          // music or a sound is quieter or louder
	sgLine          = "line"           // a narration line, rewritten the same way
	sgVaryLine      = "vary-line"      // a narration line, rewritten differently each time: something each run gives
)

// RecipeSuggestionsIn asks for a recipe's suggested changes.
type RecipeSuggestionsIn struct {
	Recipe string `json:"recipe" jsonschema:"minLength=1,description=Its short name\\, title or ID."`
	// Only keeps some of the suggestions in the draft (their keys); default all.
	Only []string `json:"only,omitempty" jsonschema:"maxItems=40,description=Only these suggestions go into the draft (their keys); default all."`
}

// RecipeSuggestion is one change people kept making in runs, with why.
type RecipeSuggestion struct {
	// Key names it (for only): what changed, where and how.
	Key string `json:"key"`
	// Part is the part of the recipe it is about (as recipe diff groups them).
	Part string `json:"part" jsonschema:"enum=sections,enum=graphics,enum=sound"`
	// Reason says what people changed and in how many runs, plainly ("You
	// shortened “intro” in 4 of 5 runs: from 3 s to about 2 s.").
	Reason string `json:"reason"`
	// Change says what the draft does ("“intro” plays about 2 s (was 3 s)").
	Change string `json:"change"`
	// Count is how many of the runs looked at show it, Of how many were looked at.
	Count int `json:"count"`
	Of    int `json:"of"`
	// Runs are the runs that show it (recipe#N).
	Runs []string `json:"runs"`
	// InDraft: it is in the draft (every suggestion unless only leaves it out).
	InDraft bool `json:"inDraft"`
}

// RecipeSuggestionsOut is a recipe's suggested changes and the draft they make.
type RecipeSuggestionsOut struct {
	Recipe string `json:"recipe" jsonschema:"description=The recipe's short name."`
	Title  string `json:"title"`
	// Looked is how many runs were compared (done, made from the recipe and
	// not again from another run, their cut approved by a person); Waiting how
	// many more are done but not approved yet.
	Looked      int                `json:"looked"`
	Waiting     int                `json:"waiting"`
	Suggestions []RecipeSuggestion `json:"suggestions"`
	// Base is the version a draft starts from: the current one, else the newest draft.
	Base *RecipeVersionRow `json:"base,omitempty"`
	// Changes are what the draft changes from Base, part by part (recipe diff's words).
	Changes []RecipeChangePart  `json:"changes"`
	Check   *models.RecipeCheck `json:"check,omitempty"`
	Summary string              `json:"summary"`
	Next    string              `json:"next,omitempty"`
}

// RecipeSuggestionsSaveIn saves the suggested changes as a draft.
type RecipeSuggestionsSaveIn struct {
	RecipeSuggestionsIn
	Note string `json:"note,omitempty" jsonschema:"maxLength=300,description=What the draft changes\\, in a few words (default: the suggestions it has)."`
}

func init() {
	ops.Def("recipe.suggestions", "Suggest changes to a recipe from what people changed in its runs (each run's approved version against the one it made): a change that repeats — you shortened the intro in 4 of 5 runs — with plain reasons and what a draft would change; nothing is saved", ops.Flags{}, suggestRecipe)
	ops.Def("recipe.suggestions.save", "Save a recipe's suggested changes (from what people changed in its runs) as a draft version; only a person makes it current", ops.Flags{Mutating: true}, saveSuggestions)
}

// runEvidence is one run's two versions: what it made and what a person approved.
type runEvidence struct {
	run            *models.RecipeRun
	doc            *models.RecipeDoc
	made, approved *editspec.Spec
}

// observed is one change a person made in one run.
type observed struct {
	kind, target, dir string
	// made and now are seconds (lengths) or dB (levels).
	made, now float64
	// dIn and dOut are how a fixed clip's in and out moved.
	dIn, dOut float64
	// text and was are words, now and before (and a caption position).
	text, was string
	caps      *editspec.Captions
}

// suggestion is a change that repeats, and what the draft does with it.
type suggestion struct {
	RecipeSuggestion
	kind, target, dir string
	value, from       float64
	dIn, dOut         float64
	text, was         string
	caps              *editspec.Captions
}

func suggestRecipe(ctx context.Context, a *app.App, in *RecipeSuggestionsIn) (*RecipeSuggestionsOut, error) {
	out, _, _, _, err := suggestions(ctx, a, in)
	return out, err
}

// suggestions works out a recipe's suggested changes and the draft they make
// (the recipe, the version the draft starts from and the draft's document).
func suggestions(ctx context.Context, a *app.App, in *RecipeSuggestionsIn) (*RecipeSuggestionsOut, *models.Recipe, *models.RecipeVersion, *models.RecipeDoc, error) {
	r, err := loadRecipe(ctx, a, in.Recipe)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	base, err := loadVersion(ctx, a, r, "")
	if err != nil {
		return nil, nil, nil, nil, err
	}
	ev, waiting, err := evidenceOf(ctx, a, r)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	out := &RecipeSuggestionsOut{Recipe: r.Name, Title: r.Title, Looked: len(ev), Waiting: waiting, Suggestions: []RecipeSuggestion{}, Changes: []RecipeChangePart{}}
	row := versionRow(base, nil)
	out.Base = &row
	only := map[string]bool{}
	for _, k := range in.Only {
		if k = strings.TrimSpace(k); k != "" {
			only[k] = true
		}
	}
	draft := Clone(&base.Doc)
	known := map[string]bool{}
	for _, s := range aggregate(ev, newSheetContext(ctx, a).title) {
		if !applies(&base.Doc, s) {
			continue // the version a draft starts from has it already, or no longer has what it changes
		}
		known[s.Key] = true
		s.InDraft = len(only) == 0 || only[s.Key]
		if s.InDraft && !apply(draft, s) {
			continue
		}
		out.Suggestions = append(out.Suggestions, s.RecipeSuggestion)
	}
	for k := range only {
		if !known[k] {
			return nil, nil, nil, nil, apperr.New(apperr.InvalidInput, "There is no suggestion “%s” for “%s” now.", k, r.Title).WithCommand("metarush recipe suggestions " + r.Name + " --details")
		}
	}
	draft.Makes = MakesWords(draft)
	n := 0
	for _, s := range out.Suggestions {
		if s.InDraft {
			n++
		}
	}
	switch {
	case len(ev) == 0 && waiting > 0:
		out.Summary = fmt.Sprintf("No run of “%s” is approved yet (%s waiting): once people approve runs, MetaRush says what they keep changing.", r.Title, plural(waiting, "run"))
	case len(ev) == 0:
		out.Summary = fmt.Sprintf("No approved runs of “%s” yet: once people approve runs, MetaRush says what they keep changing.", r.Title)
	case len(out.Suggestions) == 0:
		out.Summary = fmt.Sprintf("Nothing people changed repeats in the %s looked at.", plural(len(ev), "approved run"))
	default:
		out.Summary = fmt.Sprintf("%s from what people changed in the %s looked at.", upperFirst(plural(len(out.Suggestions), "suggested change")), plural(len(ev), "approved run"))
	}
	if n > 0 {
		chk := checkDoc(ctx, a, draft)
		out.Check = chk
		out.Changes = Differences(ctx, a, &base.Doc, draft)
		out.Next = "Save them as a draft; a person looks at it and makes it current."
	}
	return out, r, base, draft, nil
}

func saveSuggestions(ctx context.Context, a *app.App, in *RecipeSuggestionsSaveIn) (*RecipeUpdateOut, error) {
	sug, r, base, draft, err := suggestions(ctx, a, &in.RecipeSuggestionsIn)
	if err != nil {
		return nil, err
	}
	var reasons []string
	for _, s := range sug.Suggestions {
		if s.InDraft {
			reasons = append(reasons, lowerFirst(strings.TrimSuffix(s.Change, ".")))
		}
	}
	if len(reasons) == 0 {
		return nil, apperr.New(apperr.Conflict, "Nothing people changed in runs of “%s” repeats now, so there is no draft to save.", r.Title).WithCommand("metarush recipe suggestions " + r.Name)
	}
	note := strings.TrimSpace(in.Note)
	if note == "" {
		note = shorten("Suggested from runs: "+strings.Join(reasons, "; "), 300)
	}
	chk := sug.Check
	if chk == nil {
		chk = checkDoc(ctx, a, draft)
	}
	v, made, err := saveVersion(ctx, a, r, draft, base.Hash, note, chk)
	if err != nil {
		return nil, err
	}
	out := &RecipeUpdateOut{Version: versionRow(v, map[string]int{base.Hash: base.Number}), Check: chk, Same: !made, Recipe: r.Name, Title: r.Title}
	if made {
		out.Changes, out.RemovedRules = Differences(ctx, a, &base.Doc, draft), removedRules(&base.Doc, draft)
		a.Audit(ctx, "recipe.suggestions.save", "recipe", r.ID, r.ProjectID, map[string]any{"version": v.Number, "from": base.Number, "suggestions": len(reasons)})
	}
	switch {
	case !made:
		out.Message = fmt.Sprintf("Nothing new: the suggested changes are version %d of “%s”.", v.Number, r.Title)
	case !chk.OK:
		out.Message = fmt.Sprintf("Draft version %d of “%s” has the suggested changes; its check found %s to fix.", v.Number, r.Title, plural(len(chk.Problems), "problem"))
	default:
		out.Message = fmt.Sprintf("Draft version %d of “%s” has the suggested changes; a person makes it current.", v.Number, r.Title)
		out.Next = "A person looks at it and makes it current."
	}
	return out, nil
}

// evidenceOf loads the newest runs that show what people changed: done runs
// made from the recipe (not again from another run), whose cut a person
// approved; waiting counts the done ones not approved yet.
func evidenceOf(ctx context.Context, a *app.App, r *models.Recipe) ([]*runEvidence, int, error) {
	runs, err := runsOf(ctx, a, bson.M{"recipeId": r.ID, "state": models.RunDone, "projectGone": bson.M{"$ne": true}}, 4*suggestRuns)
	if err != nil {
		return nil, 0, err
	}
	docs := map[string]*models.RecipeDoc{}
	var out []*runEvidence
	waiting := 0
	for i := range runs {
		run := &runs[i]
		if run.Repeats != "" || run.Revision == "" || run.Branch == "" || run.ProjectID == "" {
			continue
		}
		if len(out) == suggestRuns {
			break
		}
		approved, err := approvedOf(ctx, a, run)
		if err != nil {
			return nil, 0, err
		}
		if approved == nil {
			waiting++
			continue
		}
		var made models.Revision
		if err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"_id": run.Revision, "workspaceId": a.WorkspaceID()},
			options.FindOne().SetProjection(bson.M{"ops": 0})).Decode(&made); err != nil {
			continue
		}
		d, ok := docs[run.Version]
		if !ok {
			var v models.RecipeVersion
			if a.DB.GetByID(ctx, store.CRecipeVersions, a.WorkspaceID(), versionID(a.WorkspaceID(), run.Version), &v) == nil {
				d = &v.Doc
			}
			docs[run.Version] = d
		}
		if d == nil {
			continue
		}
		out = append(out, &runEvidence{run: run, doc: d, made: &made.Spec, approved: &approved.Spec})
	}
	return out, waiting, nil
}

// approvedOf is the newest version of a run's cut a person approved, at or
// after the version the run made (nil: none yet).
func approvedOf(ctx context.Context, a *app.App, run *models.RecipeRun) (*models.Revision, error) {
	var rev models.Revision
	err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": run.ProjectID, "branch": run.Branch,
		"approval.state": models.ApprovalApproved, "number": bson.M{"$gte": run.RevisionNumber}},
		options.FindOne().SetSort(bson.D{{Key: "number", Value: -1}}).SetProjection(bson.M{"ops": 0})).Decode(&rev)
	if errors.Is(err, mongo.ErrNoDocuments) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &rev, nil
}

// changedLength reports whether a length changed enough to say so: a third
// of a second, and a tenth of what it was.
func changedLength(was, now float64) bool {
	return math.Abs(now-was) >= math.Max(0.3, 0.1*was)
}

func shorterLonger(was, now float64) string {
	if now < was {
		return "shorter"
	}
	return "longer"
}

func clipsByID(s *editspec.Spec) map[string]*editspec.Clip {
	out := map[string]*editspec.Clip{}
	for i := range s.Clips {
		out[s.Clips[i].ID] = &s.Clips[i]
	}
	return out
}

func overlayIn(s *editspec.Spec, id string) *editspec.Overlay {
	for i := range s.Overlays {
		if s.Overlays[i].ID == id {
			return &s.Overlays[i]
		}
	}
	return nil
}

func trackIn(s *editspec.Spec, id string) *editspec.AudioTrack {
	for i := range s.Audio {
		if s.Audio[i].ID == id {
			return &s.Audio[i]
		}
	}
	return nil
}

func beatIn(s *editspec.Spec, id string) *editspec.Beat {
	if s.Voiceover == nil {
		return nil
	}
	for i := range s.Voiceover.Beats {
		if s.Voiceover.Beats[i].ID == id {
			return &s.Voiceover.Beats[i]
		}
	}
	return nil
}

func gainOf(t *editspec.AudioTrack) float64 {
	if t.GainDB == nil {
		return 0
	}
	return *t.GainDB
}

func captionsOn(c *editspec.Captions) bool { return c != nil && c.Enabled }

func captionsAt(c *editspec.Captions) string {
	if c == nil || c.Position == "" {
		return "bottom"
	}
	return c.Position
}

// chosenSeconds is how long a chosen section's moments play in a cut, in all.
func chosenSeconds(s *editspec.Spec, of map[string]string, section string) float64 {
	t := 0.0
	for _, c := range s.Clips {
		if of[c.ID] == section {
			t += clipSeconds(c)
		}
	}
	return t
}

// boundTargets are the places inputs fill (param:<graphic>.<field>, line:<line>…):
// what changes there is a run's own, never the recipe's.
func boundTargets(d *models.RecipeDoc) map[string]bool {
	out := map[string]bool{}
	for _, b := range d.Bindings {
		out[b.To] = true
	}
	return out
}

// compareRun says what a person changed in one run: the version they
// approved against the one the run made, part by part of the recipe it made.
func compareRun(d *models.RecipeDoc, made, now *editspec.Spec) []observed {
	var out []observed
	mc, nc := clipsByID(made), clipsByID(now)
	ofMade, ofNow := sectionsOfClips(d, made), sectionsOfClips(d, now)
	for _, sec := range d.Sections {
		if sec.Kind == models.RecipeFixed {
			for _, c := range sec.Clips {
				m := mc[c.ID]
				if m == nil {
					continue
				}
				n := nc[c.ID]
				if n == nil {
					out = append(out, observed{kind: sgClipGone, target: c.ID})
					continue
				}
				if n.AssetID != m.AssetID {
					continue // other footage: what it shows, not how long it plays
				}
				lm, ln := clipSeconds(*m), clipSeconds(*n)
				if changedLength(lm, ln) {
					out = append(out, observed{kind: sgClipLength, target: c.ID, dir: shorterLonger(lm, ln), made: lm, now: ln, dIn: n.In - m.In, dOut: n.Out - m.Out})
				}
			}
			continue
		}
		// Claude picks moments of other lengths each run: only a change of a
		// fifth (and a second) says the section itself should be shorter or longer.
		lm, ln := chosenSeconds(made, ofMade, sec.Name), chosenSeconds(now, ofNow, sec.Name)
		if lm > 0 && ln > 0 && math.Abs(ln-lm) >= math.Max(1, 0.2*lm) {
			out = append(out, observed{kind: sgMoments, target: sec.Name, dir: shorterLonger(lm, ln), made: lm, now: ln})
		}
	}
	bound := boundTargets(d)
	for i := range d.Cut.Overlays {
		id := d.Cut.Overlays[i].ID
		m, n := overlayIn(made, id), overlayIn(now, id)
		if m == nil {
			continue
		}
		if n == nil {
			out = append(out, observed{kind: sgGraphicGone, target: id})
			continue
		}
		sameKind := m.Template == n.Template && (m.Motion == nil) == (n.Motion == nil) && (m.Motion == nil || m.Motion.Name == n.Motion.Name)
		if sameKind {
			mp, np := paramsOf(m), paramsOf(n)
			for _, f := range sortedKeys(mp) {
				if bound[Binding{Kind: models.BindParam, Where: id, Field: f}.String()] {
					continue
				}
				if _, isRes := resourceField(m.Template, f); isRes {
					continue
				}
				ws, ok1 := mp[f].(string)
				ns, ok2 := np[f].(string)
				ws, ns = strings.TrimSpace(ws), strings.TrimSpace(ns)
				if !ok1 || !ok2 || ns == "" || ws == ns {
					continue
				}
				out = append(out, observed{kind: sgWords, target: id + "." + f, text: ns, was: ws})
			}
		}
		if m.Duration > 0 && n.Duration > 0 && changedLength(m.Duration, n.Duration) {
			out = append(out, observed{kind: sgGraphicLength, target: id, dir: shorterLonger(m.Duration, n.Duration), made: m.Duration, now: n.Duration})
		}
	}
	switch mc, nc := made.Captions, now.Captions; {
	case captionsOn(mc) && !captionsOn(nc):
		out = append(out, observed{kind: sgCaptions, dir: "off"})
	case !captionsOn(mc) && captionsOn(nc):
		out = append(out, observed{kind: sgCaptions, dir: "on", text: captionsAt(nc), caps: cloneCaptions(nc)})
	case captionsOn(mc) && captionsOn(nc) && captionsAt(mc) != captionsAt(nc):
		out = append(out, observed{kind: sgCaptions, dir: captionsAt(nc), text: captionsAt(nc), was: captionsAt(mc)})
	}
	for _, t := range d.Cut.Audio {
		m, n := trackIn(made, t.ID), trackIn(now, t.ID)
		if m == nil {
			continue
		}
		if n == nil {
			out = append(out, observed{kind: sgSoundGone, target: t.ID})
			continue
		}
		if gm, gn := gainOf(m), gainOf(n); math.Abs(gn-gm) >= 2 {
			dir := "louder"
			if gn < gm {
				dir = "quieter"
			}
			out = append(out, observed{kind: sgLevel, target: t.ID, dir: dir, made: gm, now: gn})
		}
	}
	for _, b := range beatsOf(d) {
		if bound[Binding{Kind: models.BindLine, Where: b.ID}.String()] {
			continue
		}
		m, n := beatIn(made, b.ID), beatIn(now, b.ID)
		if m == nil || n == nil {
			continue
		}
		if ws, ns := strings.TrimSpace(m.Text), strings.TrimSpace(n.Text); ns != "" && ws != ns {
			out = append(out, observed{kind: sgLine, target: b.ID, text: ns, was: ws})
		}
	}
	return out
}

// repeats reports whether a change in count of the of runs looked at repeats:
// at least twice, in more than half of them.
func repeats(count, of int) bool { return count >= 2 && 2*count > of }

// aggregate finds the changes that repeat across runs.
func aggregate(ev []*runEvidence, titles func(id string) string) []*suggestion {
	type group struct {
		kind, target, dir string
		obs               []observed
		runs              []*runEvidence
		doc               *models.RecipeDoc
	}
	groups := map[string]*group{}
	var order []string
	for _, e := range ev {
		seen := map[string]bool{}
		for _, o := range compareRun(e.doc, e.made, e.approved) {
			dir := o.dir
			if o.kind == sgWords || o.kind == sgLine {
				dir = "" // the words are sorted out below: the same each time, or not
			}
			k := o.kind + ":" + o.target + ":" + dir
			if seen[k] {
				continue
			}
			seen[k] = true
			g := groups[k]
			if g == nil {
				g = &group{kind: o.kind, target: o.target, dir: dir, doc: e.doc}
				groups[k] = g
				order = append(order, k)
			}
			g.obs = append(g.obs, o)
			g.runs = append(g.runs, e)
		}
	}
	of := len(ev)
	var out []*suggestion
	for _, k := range order {
		g := groups[k]
		refs := func(rs []*runEvidence) []string {
			var xs []string
			for _, e := range rs {
				xs = append(xs, runRef(e.run))
			}
			sort.Strings(xs)
			return xs
		}
		s := &suggestion{kind: g.kind, target: g.target, dir: g.dir}
		switch g.kind {
		case sgWords, sgLine:
			// The most common new words, if they repeat; else other words each
			// time, which make it something each run gives.
			counts := map[string]int{}
			best := ""
			for _, o := range g.obs {
				counts[o.text]++
				if counts[o.text] > counts[best] || (counts[o.text] == counts[best] && o.text < best) {
					best = o.text
				}
			}
			switch {
			case repeats(counts[best], of):
				var rs []*runEvidence
				for i, o := range g.obs {
					if o.text == best {
						rs = append(rs, g.runs[i])
					}
				}
				s.text, s.was = best, g.obs[0].was
				s.Count, s.Runs = len(rs), refs(rs)
			case repeats(len(g.obs), of) && len(counts) > 1:
				if g.kind == sgWords {
					s.kind = sgVaryWords
				} else {
					s.kind = sgVaryLine
				}
				s.was = g.obs[0].was
				s.Count, s.Runs = len(g.obs), refs(g.runs)
			default:
				continue
			}
		default:
			if !repeats(len(g.obs), of) {
				continue
			}
			s.Count, s.Runs = len(g.obs), refs(g.runs)
			var made, now, dIn, dOut []float64
			for _, o := range g.obs {
				made, now, dIn, dOut = append(made, o.made), append(now, o.now), append(dIn, o.dIn), append(dOut, o.dOut)
			}
			s.from, s.value, s.dIn, s.dOut = median(made), median(now), median(dIn), median(dOut)
			if g.kind == sgCaptions {
				s.text, s.was = g.obs[0].text, g.obs[0].was
				for _, o := range g.obs {
					if o.caps != nil {
						s.caps = o.caps
						break
					}
				}
			}
		}
		s.Of = of
		s.Key = suggestionKey(s)
		s.Part = suggestionPart(s.kind)
		s.Reason, s.Change = suggestionWords(g.doc, s, titles)
		out = append(out, s)
	}
	parts := map[string]int{partSections: 0, partGraphics: 1, partSound: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if pi, pj := parts[out[i].Part], parts[out[j].Part]; pi != pj {
			return pi < pj
		}
		if out[i].Count != out[j].Count {
			return out[i].Count > out[j].Count
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func median(xs []float64) float64 {
	if len(xs) == 0 {
		return 0
	}
	s := append([]float64(nil), xs...)
	sort.Float64s(s)
	if len(s)%2 == 1 {
		return s[len(s)/2]
	}
	return (s[len(s)/2-1] + s[len(s)/2]) / 2
}

func suggestionKey(s *suggestion) string {
	switch s.kind {
	case sgCaptions:
		return sgCaptions + ":" + s.dir
	case sgClipLength, sgMoments, sgGraphicLength, sgLevel:
		return s.kind + ":" + s.target + ":" + s.dir
	}
	return s.kind + ":" + s.target
}

func suggestionPart(kind string) string {
	switch kind {
	case sgClipLength, sgClipGone, sgMoments:
		return partSections
	case sgGraphicGone, sgWords, sgVaryWords, sgGraphicLength:
		return partGraphics
	}
	return partSound
}

// inRuns says in how many of the runs looked at a change was made.
func inRuns(count, of int) string {
	switch {
	case count == of && of == 2:
		return "in both runs"
	case count == of:
		return fmt.Sprintf("in all %d runs", of)
	}
	return fmt.Sprintf("in %d of %d runs", count, of)
}

// clipWords names a fixed section's clip for people: the section's name when
// the clip is all of it, else the clip in its section.
func clipWords(d *models.RecipeDoc, id string) string {
	c, sec := clipByID(d, id)
	if c == nil || sec == nil {
		return "“" + id + "”"
	}
	if len(sec.Clips) == 1 {
		return "“" + sec.Name + "”"
	}
	return "“" + id + "” in “" + sec.Name + "”"
}

// fieldOf names a graphic's field ("the end card's call to action").
func fieldOf(d *models.RecipeDoc, target string) string {
	id, f, _ := strings.Cut(target, ".")
	return graphicWords(overlayByID(d, id)) + "'s " + fieldName(f)
}

func lineNumber(d *models.RecipeDoc, id string) int {
	for i, b := range beatsOf(d) {
		if b.ID == id {
			return i + 1
		}
	}
	return 0
}

func trackWords(d *models.RecipeDoc, id string, titles func(id string) string) string {
	for _, t := range d.Cut.Audio {
		if t.ID != id {
			continue
		}
		name := id
		if titles != nil {
			if n := titles(t.AssetID); n != "" {
				name = n
			}
		}
		if t.Role == editspec.RoleMusic {
			return "the music “" + shorten(name, 40) + "”"
		}
		return "the sound “" + shorten(name, 40) + "”"
	}
	return "“" + id + "”"
}

// dbWords says a change of level ("6 dB").
func dbWords(v float64) string {
	return strings.TrimSuffix(strings.TrimSuffix(fmt.Sprintf("%.1f", math.Abs(v)), "0"), ".") + " dB"
}

// suggestionWords says why a suggestion is made and what its draft changes, plainly.
func suggestionWords(d *models.RecipeDoc, s *suggestion, titles func(id string) string) (reason, change string) {
	runs := inRuns(s.Count, s.Of)
	about := func(v float64) string { return "about " + seconds(round1(v)) }
	switch s.kind {
	case sgClipLength:
		name := clipWords(d, s.target)
		if s.dir == "shorter" {
			reason = fmt.Sprintf("You shortened %s %s: from %s to %s.", name, runs, seconds(round1(s.from)), about(s.value))
		} else {
			reason = fmt.Sprintf("You made %s longer %s: from %s to %s.", name, runs, seconds(round1(s.from)), about(s.value))
		}
		change = fmt.Sprintf("%s plays %s (was %s)", upperFirst(name), about(s.value), seconds(round1(s.from)))
	case sgClipGone:
		name := clipWords(d, s.target)
		reason = fmt.Sprintf("You took %s out %s.", name, runs)
		change = "No more " + name
	case sgMoments:
		name := "“" + s.target + "”"
		reason = fmt.Sprintf("You made the moments of %s %s %s: %s in all instead of %s.", name, s.dir, runs, about(s.value), seconds(round1(s.from)))
		change = fmt.Sprintf("The moments of %s last %s in all", name, about(s.value))
	case sgGraphicGone:
		g := graphicWords(overlayByID(d, s.target))
		reason = fmt.Sprintf("You took %s out %s.", g, runs)
		change = "No more " + g
	case sgWords:
		f := fieldOf(d, s.target)
		reason = fmt.Sprintf("You changed %s to “%s” %s (it said “%s”).", f, shorten(s.text, 80), runs, shorten(s.was, 80))
		change = fmt.Sprintf("%s says “%s” (was “%s”)", upperFirst(f), shorten(s.text, 80), shorten(s.was, 80))
	case sgVaryWords:
		f := fieldOf(d, s.target)
		reason = fmt.Sprintf("You changed %s %s, each time to other words.", f, runs)
		change = fmt.Sprintf("%s is something each run gives (unless it gives none: “%s”)", upperFirst(f), shorten(s.was, 80))
	case sgGraphicLength:
		g := graphicWords(overlayByID(d, s.target))
		how := "for less time"
		if s.dir == "longer" {
			how = "for longer"
		}
		reason = fmt.Sprintf("You made %s show %s %s: %s instead of %s.", g, how, runs, about(s.value), seconds(round1(s.from)))
		change = fmt.Sprintf("%s shows for %s (was %s)", upperFirst(g), about(s.value), seconds(round1(s.from)))
	case sgCaptions:
		switch s.dir {
		case "off":
			reason = fmt.Sprintf("You turned the captions off %s.", runs)
			change = "No captions"
		case "on":
			reason = fmt.Sprintf("You turned captions on %s.", runs)
			change = "Captions at the " + s.text
		default:
			reason = fmt.Sprintf("You moved the captions to the %s %s.", s.text, runs)
			change = fmt.Sprintf("Captions at the %s (were at the %s)", s.text, s.was)
		}
	case sgSoundGone:
		t := trackWords(d, s.target, titles)
		reason = fmt.Sprintf("You took %s out %s.", t, runs)
		change = "No more " + t
	case sgLevel:
		t := trackWords(d, s.target, titles)
		reason = fmt.Sprintf("You made %s %s %s (by about %s).", t, s.dir, runs, dbWords(s.value-s.from))
		change = fmt.Sprintf("%s plays %s %s", upperFirst(t), dbWords(s.value-s.from), s.dir)
	case sgLine:
		n := lineNumber(d, s.target)
		reason = fmt.Sprintf("You rewrote narration line %d the same way %s: “%s”.", n, runs, shorten(s.text, 80))
		change = fmt.Sprintf("Narration line %d says “%s” (was “%s”)", n, shorten(s.text, 80), shorten(s.was, 80))
	case sgVaryLine:
		n := lineNumber(d, s.target)
		reason = fmt.Sprintf("You rewrote narration line %d %s, each time differently.", n, runs)
		change = fmt.Sprintf("Narration line %d is something each run gives (unless it gives none: “%s”)", n, shorten(s.was, 80))
	}
	return reason, change
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }

// applies reports whether a suggestion still changes a version: what it
// changes is there, and the version doesn't have it already.
func applies(d *models.RecipeDoc, s *suggestion) bool {
	bound := boundTargets(d)
	switch s.kind {
	case sgClipLength:
		c, sec := clipByID(d, s.target)
		return c != nil && sec.Kind == models.RecipeFixed && changedLength(s.value, clipSeconds(*c))
	case sgClipGone:
		c, sec := clipByID(d, s.target)
		return c != nil && sec.Kind == models.RecipeFixed
	case sgMoments:
		for _, sec := range d.Sections {
			if sec.Name == s.target && sec.Kind == models.RecipeChosen {
				l := sec.Length
				return l == nil || l.Max <= 0 || s.value < l.Min-0.3 || s.value > l.Max+0.3
			}
		}
		return false
	case sgGraphicGone:
		return overlayByID(d, s.target) != nil
	case sgWords, sgVaryWords:
		id, f, _ := strings.Cut(s.target, ".")
		o := overlayByID(d, id)
		if o == nil || bound[Binding{Kind: models.BindParam, Where: id, Field: f}.String()] {
			return false
		}
		if s.kind == sgVaryWords {
			_, text, known := templateField(o.Template, f)
			return o.Motion == nil && known && text
		}
		cur, _ := paramsOf(o)[f].(string)
		return strings.TrimSpace(cur) != s.text
	case sgGraphicLength:
		o := overlayByID(d, s.target)
		return o != nil && o.Duration > 0 && changedLength(s.value, o.Duration)
	case sgCaptions:
		c := d.Cut.Captions
		switch s.dir {
		case "off":
			return captionsOn(c)
		case "on":
			return !captionsOn(c)
		}
		return captionsOn(c) && captionsAt(c) != s.text
	case sgSoundGone:
		for _, t := range d.Cut.Audio {
			if t.ID == s.target {
				return true
			}
		}
		return false
	case sgLevel:
		for i := range d.Cut.Audio {
			if t := &d.Cut.Audio[i]; t.ID == s.target {
				return math.Abs(gainOf(t)-s.value) >= 1
			}
		}
		return false
	case sgLine, sgVaryLine:
		b := beatByID(d, s.target)
		if b == nil || bound[Binding{Kind: models.BindLine, Where: s.target}.String()] {
			return false
		}
		return s.kind == sgVaryLine || strings.TrimSpace(b.Text) != s.text
	}
	return false
}

// apply makes a suggestion's change in a draft document; false when it can't.
func apply(d *models.RecipeDoc, s *suggestion) bool {
	switch s.kind {
	case sgClipLength:
		c, _ := clipByID(d, s.target)
		if c == nil {
			return false
		}
		in, out := round3(math.Max(0, c.In+s.dIn)), round3(c.Out+s.dOut)
		sp := c.Speed
		if sp <= 0 {
			sp = 1
		}
		if math.Abs((out-in)/sp-s.value) > 0.05 {
			// The trims don't add up to the length people kept (another speed,
			// or the start was at 0): keep the start and end it there.
			out = round3(in + s.value*sp)
		}
		if out-in < 0.2 {
			return false
		}
		c.In, c.Out = in, out
		return true
	case sgClipGone:
		return removeClip(d, s.target)
	case sgMoments:
		for i := range d.Sections {
			if sec := &d.Sections[i]; sec.Name == s.target && sec.Kind == models.RecipeChosen {
				v := math.Round(s.value*2) / 2 // to the half second
				lo, hi := math.Max(1, math.Round(v*0.85*2)/2), math.Round(v*1.15*2)/2
				sec.Length = &models.RecipeRange{Min: lo, Max: hi}
				return true
			}
		}
		return false
	case sgGraphicGone:
		return removeOverlay(d, s.target)
	case sgWords:
		id, f, _ := strings.Cut(s.target, ".")
		o := overlayByID(d, id)
		if o == nil {
			return false
		}
		o.Params = setParam(o.Params, f, s.text)
		return true
	case sgVaryWords:
		id, f, _ := strings.Cut(s.target, ".")
		o := overlayByID(d, id)
		if o == nil {
			return false
		}
		limit, _, _ := templateField(o.Template, f)
		cur, _ := paramsOf(o)[f].(string)
		name := freeInputName(d, f)
		d.Inputs = append(d.Inputs, models.RecipeInput{Name: name, Kind: models.RecipeWords, Label: "The " + fieldName(f) + " on " + graphicWords(o),
			Default: models.RecipeValue{Text: cur}, MaxLength: limit})
		d.Bindings = append(d.Bindings, models.RecipeBinding{Input: name, To: Binding{Kind: models.BindParam, Where: id, Field: f}.String()})
		return true
	case sgGraphicLength:
		o := overlayByID(d, s.target)
		if o == nil {
			return false
		}
		o.Duration = math.Max(0.2, round1(s.value))
		return true
	case sgCaptions:
		switch s.dir {
		case "off":
			if d.Cut.Captions == nil {
				return false
			}
			d.Cut.Captions.Enabled = false
		case "on":
			if s.caps != nil {
				d.Cut.Captions = cloneCaptions(s.caps)
			} else if d.Cut.Captions == nil {
				d.Cut.Captions = &editspec.Captions{Enabled: true, Position: s.text}
			}
			d.Cut.Captions.Enabled = true
		default:
			if d.Cut.Captions == nil {
				return false
			}
			d.Cut.Captions.Position = s.text
		}
		return true
	case sgSoundGone:
		for i := range d.Cut.Audio {
			if d.Cut.Audio[i].ID == s.target {
				d.Cut.Audio = append(d.Cut.Audio[:i:i], d.Cut.Audio[i+1:]...)
				if len(d.Cut.Audio) == 0 {
					d.Cut.Audio = nil
				}
				return true
			}
		}
		return false
	case sgLevel:
		for i := range d.Cut.Audio {
			if t := &d.Cut.Audio[i]; t.ID == s.target {
				g := round1(s.value)
				t.GainDB = &g
				return true
			}
		}
		return false
	case sgLine:
		b := beatByID(d, s.target)
		if b == nil {
			return false
		}
		b.Text, b.Take, b.State = s.text, nil, "draft" // read again in the recipe's voice
		return true
	case sgVaryLine:
		b := beatByID(d, s.target)
		if b == nil {
			return false
		}
		name := freeInputName(d, "line-"+fmt.Sprint(lineNumber(d, s.target)))
		d.Inputs = append(d.Inputs, models.RecipeInput{Name: name, Kind: models.RecipeWords, Label: fmt.Sprintf("Narration line %d", lineNumber(d, s.target)),
			Default: models.RecipeValue{Text: b.Text}, MaxLength: 2000})
		d.Bindings = append(d.Bindings, models.RecipeBinding{Input: name, To: Binding{Kind: models.BindLine, Where: s.target}.String()})
		return true
	}
	return false
}

// freeInputName is an input name the document doesn't have yet.
func freeInputName(d *models.RecipeDoc, want string) string {
	want = shortName(want)
	have := map[string]bool{}
	for _, in := range d.Inputs {
		have[in.Name] = true
	}
	n := want
	for i := 2; have[n]; i++ {
		n = fmt.Sprintf("%s-%d", want, i)
	}
	return n
}

// removeClip takes a fixed section's clip out of a document: a section left
// without clips goes; what pointed at the clip follows its neighbours, and
// bindings and inputs that went only there go.
func removeClip(d *models.RecipeDoc, id string) bool {
	var order []string
	for _, sec := range d.Sections {
		for _, c := range sec.Clips {
			order = append(order, c.ID)
		}
	}
	at := -1
	for i, x := range order {
		if x == id {
			at = i
		}
	}
	if at < 0 {
		return false
	}
	next, prev := "", ""
	if at+1 < len(order) {
		next = order[at+1]
	}
	if at > 0 {
		prev = order[at-1]
	}
	if next == "" && prev == "" {
		return false // the only clip: the recipe would make nothing
	}
	for i := range d.Sections {
		sec := &d.Sections[i]
		for j := range sec.Clips {
			if sec.Clips[j].ID == id {
				sec.Clips = append(sec.Clips[:j:j], sec.Clips[j+1:]...)
				break
			}
		}
	}
	var secs []models.RecipeSection
	for _, sec := range d.Sections {
		if sec.Kind == models.RecipeFixed && len(sec.Clips) == 0 {
			continue
		}
		secs = append(secs, sec)
	}
	d.Sections = secs
	for i := range d.Cut.Overlays {
		o := &d.Cut.Overlays[i]
		if o.Span == nil {
			continue
		}
		if o.Span.From == id {
			o.Span.From = next
		}
		if o.Span.To == id {
			o.Span.To = prev
		}
	}
	if v := d.Cut.Voiceover; v != nil {
		to := next
		if to == "" {
			to = prev
		}
		for i := range v.Beats {
			a := &v.Beats[i].Anchor
			if a.ClipID == id {
				a.Kind, a.ClipID, a.AssetID, a.At, a.Quote, a.Ref, a.Offset = editspec.AnchorClip, to, "", nil, "", "", 0
			}
		}
	}
	dropBindings(d, func(b Binding) bool {
		switch b.Kind {
		case models.BindClip, models.BindWords, models.BindPictures, models.BindPrompt:
			return b.Where == id
		}
		return false
	})
	return true
}

// removeOverlay takes a graphic out of a document, with the bindings and
// inputs that went only to it.
func removeOverlay(d *models.RecipeDoc, id string) bool {
	for i := range d.Cut.Overlays {
		if d.Cut.Overlays[i].ID == id {
			d.Cut.Overlays = append(d.Cut.Overlays[:i:i], d.Cut.Overlays[i+1:]...)
			if len(d.Cut.Overlays) == 0 {
				d.Cut.Overlays = nil
			}
			dropBindings(d, func(b Binding) bool { return b.Kind == models.BindParam && b.Where == id })
			return true
		}
	}
	return false
}

// dropBindings takes bindings out of a document, then the inputs that went
// only there (a chosen section's input stays).
func dropBindings(d *models.RecipeDoc, gone func(Binding) bool) {
	var keep []models.RecipeBinding
	lost := map[string]bool{}
	for _, b := range d.Bindings {
		if p, ok := ParseBinding(b.To); ok && gone(p) {
			lost[b.Input] = true
			continue
		}
		keep = append(keep, b)
	}
	d.Bindings = keep
	for _, b := range d.Bindings {
		delete(lost, b.Input)
	}
	for _, sec := range d.Sections {
		delete(lost, sec.From)
	}
	if len(lost) == 0 {
		return
	}
	var ins []models.RecipeInput
	for _, in := range d.Inputs {
		if !lost[in.Name] {
			ins = append(ins, in)
		}
	}
	d.Inputs = ins
}

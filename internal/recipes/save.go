package recipes

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/remotion"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Saving a cut as a recipe (recipe.save, "Save as a recipe…"): MetaRush reads
// the approved version and proposes what varies — the project's own footage
// (chosen again by Claude), words in graphics, word settings, pictures that
// aren't the brand's, the AI voice — and what stays: the brand through
// Content Settings, music, intros and outros, templates, fixed lengths,
// captions, the mix, rules and sign-offs. The person (or Claude with them)
// moves items between the lists; the approved render becomes the example.

// RecipeProposalItem is one thing the proposal varies or keeps.
type RecipeProposalItem struct {
	// Key is what vary, keep, fixed, chosen and pack take.
	Key  string `json:"key"`
	Kind string `json:"kind" jsonschema:"enum=input,enum=section,enum=part,enum=pack"`
	What string `json:"what" jsonschema:"description=What it is\\, plainly."`
	// Varies: an input changes each run; a section is Claude's to choose.
	Varies bool   `json:"varies"`
	Why    string `json:"why,omitempty"`
	// Movable: a person can move it to the other list.
	Movable bool `json:"movable"`
}

// varyFields are a graphic's fields that vary by default; the others
// (a call to action, a handle, an address, a source) stay.
var varyFields = map[string]bool{"headline": true, "title": true, "subtitle": true, "subline": true, "name": true, "text": true, "kicker": true,
	"label": true, "value": true, "context": true, "caption": true, "attribution": true, "role": true}

// brandPins are the brand's logos as cuts pin them (they come with Content Settings).
var brandPins = map[string]bool{ops.BrandLogoPin: true, ops.BrandLogoDarkPin: true, ops.BrandLogoLightPin: true, ops.BrandLogoIconPin: true, ops.BrandLogoWordmarkPin: true}

// saveSource is the cut a recipe is saved from.
type saveSource struct {
	project *models.Project
	branch  string
	rev     *models.Revision
}

// cutToSave finds the version to save: the one asked for, else the cut's
// newest approved version, else its newest.
func cutToSave(ctx context.Context, a *app.App, from, branch, version string) (*saveSource, error) {
	p, err := ops.ResolveProject(ctx, a, from)
	if err != nil {
		return nil, err
	}
	if p.Archived {
		return nil, apperr.New(apperr.Conflict, "“%s” is archived; restore it to save its cut as a recipe.", p.Name)
	}
	if branch == "" {
		branch = "main"
	}
	head := p.Branches[branch].Head
	if head == "" {
		return nil, apperr.New(apperr.NotFound, "“%s” has no cut called %s yet.", p.Name, branch)
	}
	var rev models.Revision
	switch v := strings.TrimPrefix(strings.TrimSpace(version), "r"); {
	case version != "" && ids.Valid(ids.Revision, version):
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), version, &rev); err != nil || rev.ProjectID != p.ID {
			return nil, apperr.New(apperr.NotFound, "That version isn't one of “%s”.", p.Name)
		}
	case version != "":
		n, err := strconv.Atoi(v)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "Say which version by its number (like 4).")
		}
		if err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": p.ID, "number": n}).Decode(&rev); err != nil {
			return nil, apperr.New(apperr.NotFound, "“%s” has no version %d.", p.Name, n)
		}
	default:
		err := a.DB.C(store.CRevisions).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "projectId": p.ID, "branch": branch, "approval.state": models.ApprovalApproved, "detached": bson.M{"$ne": true}},
			options.FindOne().SetSort(bson.D{{Key: "number", Value: -1}})).Decode(&rev)
		if err != nil {
			if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &rev); err != nil {
				return nil, err
			}
		}
	}
	return &saveSource{project: p, branch: branch, rev: &rev}, nil
}

// proposal is a recipe document worked out from a cut, with what it varies
// and keeps.
type proposal struct {
	Doc   *models.RecipeDoc
	Items []RecipeProposalItem
	// optional are inputs a person can add (lines, the title, generated
	// shots, more words), by key.
	optional map[string]optionalInput
	// packs are pack outputs a person can add (D214: shorts of a conversation
	// the cut plays), by key.
	packs map[string]models.RecipeOutput
}

type optionalInput struct {
	input   models.RecipeInput
	binding []models.RecipeBinding
}

// propose works out a recipe from a cut. Overrides move items between the lists.
func propose(ctx context.Context, a *app.App, src *saveSource, in *RecipeSaveIn) (*proposal, error) {
	p, rev := src.project, src.rev
	spec := editspec.Clone(&rev.Spec)
	editspec.Normalize(spec)
	fps := float64(spec.Output.FPS)
	if fps <= 0 {
		fps = 30
	}
	assets := map[string]*models.Asset{}
	var idsList []string
	for _, c := range spec.Clips {
		if strings.HasPrefix(c.AssetID, "ast_") {
			idsList = append(idsList, c.AssetID)
		}
	}
	var found []models.Asset
	if len(idsList) > 0 {
		if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idsList}}, &found); err != nil {
			return nil, err
		}
	}
	for i := range found {
		assets[found[i].ID] = &found[i]
	}
	own := func(c editspec.Clip) bool {
		as := assets[c.AssetID]
		if as == nil || (as.Kind != "video" && as.Kind != "audio") || as.Source.IsRemotion() || as.Source.IsGenerated() {
			return false
		}
		for _, id := range as.ProjectIDs {
			if id == p.ID {
				return true
			}
		}
		return false
	}
	title := strings.TrimSpace(in.Title)
	if h := p.Branches[src.branch]; title == "" && h.TitleBy == "person" {
		title = strings.TrimSpace(h.Title) // the cut's name a person gave it
	}
	if title == "" {
		title = strings.TrimSpace(spec.Title)
	}
	if title == "" {
		title = p.Name
	}
	d := &models.RecipeDoc{Format: models.RecipeFormat, Title: shorten(title, 120)}
	pr := &proposal{Doc: d, optional: map[string]optionalInput{}, packs: map[string]models.RecipeOutput{}}
	// Sections: runs of the project's own footage are Claude's to choose
	// again; everything else is fixed.
	taken := map[string]bool{}
	sectionName := func(want string) string {
		want = shortName(want)
		n := want
		for i := 2; taken[n]; i++ {
			n = fmt.Sprintf("%s-%d", want, i)
		}
		taken[n] = true
		return n
	}
	var groups [][]editspec.Clip
	var ownGroup []bool
	for _, c := range spec.Clips {
		o := own(c)
		if len(groups) > 0 && ownGroup[len(ownGroup)-1] == o {
			groups[len(groups)-1] = append(groups[len(groups)-1], c)
			continue
		}
		groups = append(groups, []editspec.Clip{c})
		ownGroup = append(ownGroup, o)
	}
	var footage []string
	seenAsset := map[string]bool{}
	for gi, g := range groups {
		sec := models.RecipeSection{Clips: cloneJSON(g)}
		if ownGroup[gi] {
			sec.Kind, sec.From = models.RecipeChosen, "footage"
			sec.Name = sectionName("moments")
			sec.Purpose = purposeOf(g)
			total := 0.0
			for _, c := range g {
				total += clipSeconds(c)
				if !seenAsset[c.AssetID] {
					seenAsset[c.AssetID] = true
					footage = append(footage, c.AssetID)
				}
			}
			n := float64(len(g))
			sec.Count = &models.RecipeRange{Min: math.Max(1, n-1), Max: n + 1}
			sec.Length = &models.RecipeRange{Min: math.Floor(total * 0.8), Max: math.Ceil(total * 1.2)}
		} else {
			sec.Kind = models.RecipeFixed
			switch {
			case strings.TrimSpace(g[0].Label) != "":
				sec.Name = sectionName(g[0].Label)
			case gi == 0:
				sec.Name = sectionName("intro")
			case gi == len(groups)-1:
				sec.Name = sectionName("end")
			default:
				sec.Name = sectionName(fmt.Sprintf("part-%d", gi+1))
			}
			sec.Purpose = purposeOf(g)
		}
		d.Sections = append(d.Sections, sec)
	}
	// Everything else the cut has, with graphics and lines following their clips.
	d.Cut = models.RecipeCut{Title: spec.Title, Output: spec.Output, Audio: cloneAudio(spec.Audio), Captions: cloneCaptions(spec.Captions),
		Overlays: spanOverlays(spec, fps), Brand: cloneBrand(spec.Brand), Notes: spec.Notes, Voiceover: clipAnchoredLines(spec, fps), Mix: cloneMix(spec.Mix),
		Resources: spec.Resources, Conversations: spec.Conversations, Fixed: spec.Fixed}
	// The inputs, proposed.
	if len(footage) > 0 {
		d.Inputs = append(d.Inputs, models.RecipeInput{Name: "footage", Kind: models.RecipeFootage, Label: "The footage", Default: models.RecipeValue{Items: footage},
			Count: &models.RecipeRange{Min: 1}})
		pr.Items = append(pr.Items, RecipeProposalItem{Key: "footage", Kind: "input", What: "The project's own footage (" + plural(len(footage), "clip") + "); Claude chooses the moments again from new footage",
			Varies: true, Movable: true})
	}
	names := map[string]bool{"footage": len(footage) > 0}
	inputName := func(want string) string {
		want = shortName(want)
		n := want
		for i := 2; names[n]; i++ {
			n = fmt.Sprintf("%s-%d", want, i)
		}
		names[n] = true
		return n
	}
	// Words in graphics (the same words in several graphics are one input).
	byText := map[string]int{}
	for _, o := range d.Cut.Overlays {
		if o.Template == "" || len(o.Params) == 0 {
			continue
		}
		var m map[string]any
		if json.Unmarshal(o.Params, &m) != nil {
			continue
		}
		for _, f := range sortedKeys(m) {
			text, ok := m[f].(string)
			if !ok || strings.TrimSpace(text) == "" {
				continue
			}
			limit, isText, known := templateField(o.Template, f)
			if !known || !isText {
				continue
			}
			if _, isRes := resourceField(o.Template, f); isRes {
				continue
			}
			b := models.RecipeBinding{To: Binding{Kind: models.BindParam, Where: o.ID, Field: f}.String()}
			if k, ok := byText[text]; ok && d.Inputs[k].Kind == models.RecipeWords {
				b.Input = d.Inputs[k].Name
				d.Bindings = append(d.Bindings, b)
				if limit > 0 && (d.Inputs[k].MaxLength == 0 || limit < d.Inputs[k].MaxLength) {
					d.Inputs[k].MaxLength = limit
				}
				continue
			}
			x := models.RecipeInput{Name: inputName(f), Kind: models.RecipeWords, Label: "The " + fieldName(f) + " on " + graphicWords(&o), Default: models.RecipeValue{Text: text}, MaxLength: limit}
			b.Input = x.Name
			what := fmt.Sprintf("%s: “%s”", upperFirst(x.Label[4:]), shorten(text, 60))
			if varyFields[f] {
				byText[text] = len(d.Inputs)
				d.Inputs = append(d.Inputs, x)
				d.Bindings = append(d.Bindings, b)
				pr.Items = append(pr.Items, RecipeProposalItem{Key: x.Name, Kind: "input", What: what, Varies: true, Movable: true})
			} else {
				pr.optional[x.Name] = optionalInput{input: x, binding: []models.RecipeBinding{b}}
				pr.Items = append(pr.Items, RecipeProposalItem{Key: x.Name, Kind: "input", What: what, Movable: true, Why: "the same every time unless you vary it"})
			}
		}
	}
	// Word and picture settings of clips made from code (D168, D209).
	wordInputs := map[string]string{} // source:key → input
	shownWords := 0
	var codePics []codePicture
	for _, sec := range d.Sections {
		for _, c := range sec.Clips {
			as := assets[c.AssetID]
			if as == nil || !as.Source.IsRemotion() {
				continue
			}
			st, err := remotion.SettingsOfClip(ctx, a, c.AssetID)
			if err != nil || st == nil {
				continue
			}
			for _, pic := range st.Pictures {
				codePics = append(codePics, codePicture{clip: c.ID, st: st, pic: pic})
			}
			for _, w := range st.Words {
				if !w.OnScreen {
					continue
				}
				b := models.RecipeBinding{To: Binding{Kind: models.BindWords, Where: c.ID, Field: w.Key}.String()}
				if name, ok := wordInputs[st.SourceID+":"+w.Key]; ok {
					b.Input = name
					if _, opt := pr.optional[name]; opt {
						o := pr.optional[name]
						o.binding = append(o.binding, b)
						pr.optional[name] = o
					} else {
						d.Bindings = append(d.Bindings, b)
					}
					continue
				}
				x := models.RecipeInput{Name: inputName(w.Key), Kind: models.RecipeWords, Label: fmt.Sprintf("The words %s in “%s”", w.Key, st.Composition), Default: models.RecipeValue{Text: w.Text}}
				b.Input = x.Name
				wordInputs[st.SourceID+":"+w.Key] = x.Name
				what := fmt.Sprintf("Words %s in “%s”: “%s”", w.Key, st.Composition, shorten(w.Text, 60))
				if shownWords < 10 {
					shownWords++
					d.Inputs = append(d.Inputs, x)
					d.Bindings = append(d.Bindings, b)
					pr.Items = append(pr.Items, RecipeProposalItem{Key: x.Name, Kind: "input", What: what, Varies: true, Movable: true, Why: "a word setting: new words need no new code"})
				} else {
					pr.optional[x.Name] = optionalInput{input: x, binding: []models.RecipeBinding{b}}
					pr.Items = append(pr.Items, RecipeProposalItem{Key: x.Name, Kind: "input", What: what, Movable: true})
				}
			}
		}
	}
	// Pictures that aren't the brand's: one input per linked folder, else per
	// picture — graphics' pictures and the picture settings of clips made from code.
	pictureInputs(ctx, a, d, pr, inputName, codePics)
	// The AI voice.
	voiceInput(ctx, a, p, d, pr)
	// Lines, the title and generated shots can vary if a person says so.
	if v := d.Cut.Voiceover; v != nil {
		for i, b := range v.Beats {
			x := models.RecipeInput{Name: inputName("line-" + strconv.Itoa(i+1)), Kind: models.RecipeWords, Label: fmt.Sprintf("Narration line %d", i+1), Default: models.RecipeValue{Text: b.Text}, MaxLength: 2000}
			pr.optional[x.Name] = optionalInput{input: x, binding: []models.RecipeBinding{{Input: x.Name, To: Binding{Kind: models.BindLine, Where: b.ID}.String()}}}
			pr.Items = append(pr.Items, RecipeProposalItem{Key: x.Name, Kind: "input", What: fmt.Sprintf("Narration line %d: “%s”", i+1, shorten(b.Text, 60)), Movable: true, Why: "the same words every time unless you vary it"})
		}
	}
	if t := strings.TrimSpace(d.Cut.Title); t != "" {
		x := models.RecipeInput{Name: inputName("title"), Kind: models.RecipeWords, Label: "The video's title", Default: models.RecipeValue{Text: t}, MaxLength: 200}
		pr.optional[x.Name] = optionalInput{input: x, binding: []models.RecipeBinding{{Input: x.Name, To: models.BindTitle}}}
		pr.Items = append(pr.Items, RecipeProposalItem{Key: x.Name, Kind: "input", What: "The video's title: “" + shorten(t, 60) + "”", Movable: true})
	}
	for _, sec := range d.Sections {
		for _, c := range sec.Clips {
			as := assets[c.AssetID]
			if as == nil || !as.Source.IsGenerated() {
				continue
			}
			gv, err := ops.CallTyped[generate.GenView](ctx, a, "generate.show", generate.GenIDIn{ID: c.AssetID}, ops.Caller{})
			if err != nil {
				continue
			}
			x := models.RecipeInput{Name: inputName(c.ID + "-prompt"), Kind: models.RecipeWords, Label: "The words the shot “" + as.DisplayTitle() + "” is made from", Default: models.RecipeValue{Text: gv.Prompt}, MaxLength: 4000}
			pr.optional[x.Name] = optionalInput{input: x, binding: []models.RecipeBinding{{Input: x.Name, To: Binding{Kind: models.BindPrompt, Where: c.ID}.String()}}}
			pr.Items = append(pr.Items, RecipeProposalItem{Key: x.Name, Kind: "input", What: "The generated shot “" + as.DisplayTitle() + "” (made again from new words each run, priced first, if you vary it)", Movable: true})
		}
	}
	// Shorts of a conversation the cut plays (D214): a pack a person can add.
	if name, pin := conversationOf(spec, ""); pin != nil {
		convName := name
		var c models.Conversation
		if a.DB.GetByID(ctx, store.CConversations, a.WorkspaceID(), pin.ID, &c) == nil && c.Name != "" {
			convName = c.Name
		}
		o := models.RecipeOutput{Name: "shorts", Kind: models.RecipeShorts, Conversation: name, Count: &models.RecipeRange{Min: 1, Max: 3}, Length: &models.RecipeRange{Min: 15, Max: 60}}
		pr.packs[o.Name] = o
		pr.Items = append(pr.Items, RecipeProposalItem{Key: o.Name, Kind: "pack", What: fmt.Sprintf("Shorts of “%s”: %s", convName, strings.Replace(outputWords(o), " from the conversation “"+name+"”", "", 1)),
			Movable: true, Why: "every run makes them too if you add them; Claude keeps or changes MetaRush's first picks"})
	}
	// Sections in the proposal.
	for _, sec := range d.Sections {
		what := fmt.Sprintf("Section “%s”, %s: %s", sec.Name, seconds(sectionSeconds(sec)), sec.Purpose)
		item := RecipeProposalItem{Key: sec.Name, Kind: "section", What: what, Varies: sec.Kind == models.RecipeChosen, Movable: sec.Kind == models.RecipeChosen || allOwn(sec, own)}
		if sec.Kind == models.RecipeChosen {
			item.Why = "Claude chooses its moments (" + sectionNeeds(sec) + ")"
		} else {
			item.Why = "built by MetaRush and locked"
		}
		pr.Items = append(pr.Items, item)
	}
	// What stays.
	stays := []string{}
	if len(d.Cut.Audio) > 0 {
		stays = append(stays, "the music and sounds")
	}
	if d.Cut.Captions != nil && d.Cut.Captions.Enabled {
		stays = append(stays, "the captions")
	}
	if d.Cut.Mix != nil {
		stays = append(stays, "the mix")
	}
	stays = append(stays, "the brand, spelling and house rules (through Content Settings)")
	pr.Items = append(pr.Items, RecipeProposalItem{Key: "stays", Kind: "part", What: "Stays: " + joinAnd(stays) + "."})
	// Rules, sign-offs and hints.
	for _, r := range p.Rules {
		d.Rules = append(d.Rules, models.RecipeRule{Text: r.Text, Measure: r.Measure, Check: r.Check})
	}
	for _, s := range ops.OwnSignoffs(p, src.branch) {
		d.Signoffs = append(d.Signoffs, s.Text)
	}
	for _, h := range in.Hints {
		if h = strings.TrimSpace(h); h != "" {
			d.Hints = append(d.Hints, shorten(h, 300))
		}
	}
	if len(d.Rules) > 0 || len(d.Signoffs) > 0 {
		pr.Items = append(pr.Items, RecipeProposalItem{Key: "rules", Kind: "part", What: fmt.Sprintf("Rules and sign-offs: %s and %s, kept by every run's project.", plural(len(d.Rules), "rule"), plural(len(d.Signoffs), "sign-off"))})
	}
	// Where runs go, whose settings they follow, how long the video is.
	d.Lands = models.RecipeLanding{Kind: models.RecipeLandsProject, ProjectID: p.ID}
	if in.NewProjects {
		d.Lands = models.RecipeLanding{Kind: models.RecipeLandsNewProject, Name: "{title} {n}"}
	}
	if _, owner, err := content.For(ctx, a, p.ID); err == nil {
		d.Settings = owner
	}
	dur := rev.Duration
	if dur > 0 {
		d.Length = &models.RecipeRange{Min: math.Floor(dur * 0.85), Max: math.Ceil(dur * 1.15)}
	}
	d.From = &models.RecipeFrom{ProjectID: p.ID, Branch: src.branch, RevisionID: rev.ID, Number: rev.Number, Approved: rev.Approval.State == models.ApprovalApproved}
	if err := applyOverrides(pr, in); err != nil {
		return nil, err
	}
	d.Makes = MakesWords(d)
	return pr, nil
}

// allOwn reports whether every clip of a section is the project's own footage.
func allOwn(sec models.RecipeSection, own func(editspec.Clip) bool) bool {
	for _, c := range sec.Clips {
		if !own(c) {
			return false
		}
	}
	return len(sec.Clips) > 0
}

// purposeOf says what a section's clips are for, from their labels and reasons.
func purposeOf(cs []editspec.Clip) string {
	var parts []string
	seen := map[string]bool{}
	for _, c := range cs {
		for _, t := range []string{c.Label, c.Rationale} {
			t = strings.TrimSpace(t)
			if t != "" && !seen[t] {
				seen[t] = true
				parts = append(parts, t)
			}
		}
	}
	if len(parts) == 0 {
		return "the strongest moments"
	}
	return shorten(strings.Join(parts, "; "), 300)
}

// spanOverlays makes graphics follow the clips they are on (D163 spans), so
// they stay with their section when chosen moments change its length.
func spanOverlays(s *editspec.Spec, fps float64) []editspec.Overlay {
	out := cloneOverlays(s.Overlays)
	placed, total := editspec.Timeline(s)
	if len(placed) == 0 {
		return out
	}
	at := func(frame int) int {
		for i, c := range placed {
			if frame >= c.From && frame < c.From+c.DurationInFrames {
				return i
			}
		}
		if frame <= 0 {
			return 0
		}
		return len(placed) - 1
	}
	for i := range out {
		o := &out[i]
		if o.Span != nil {
			continue
		}
		start := int(math.Round(o.At * fps))
		end := start + int(math.Round(o.Duration*fps))
		if t, ok := editspec.TemplateByID(o.Template); ok && t.Persistent && start <= 1 && end >= total-1 {
			o.Span, o.At, o.Duration = &editspec.OverlaySpan{}, 0, 0
			continue
		}
		first, last := at(start), at(max(start, end-1))
		o.Span = &editspec.OverlaySpan{From: placed[first].ID, To: placed[last].ID}
		o.At = round3(float64(start-placed[first].From) / fps)
	}
	return out
}

// clipAnchoredLines makes narration lines placed at a clock time follow the
// clip they start in.
func clipAnchoredLines(s *editspec.Spec, fps float64) *editspec.Voiceover {
	v := cloneVoiceover(s.Voiceover)
	if v == nil {
		return nil
	}
	placed, _ := editspec.Timeline(s)
	for i := range v.Beats {
		a := &v.Beats[i].Anchor
		if a.Kind != editspec.AnchorTime || a.At == nil {
			continue
		}
		frame := int(math.Round(*a.At * fps))
		for _, c := range placed {
			if frame >= c.From && frame < c.From+c.DurationInFrames {
				off := round3(float64(frame-c.From) / fps)
				if off <= 60 {
					*a = editspec.Anchor{Kind: editspec.AnchorClip, ClipID: c.ID, Offset: off}
				}
				break
			}
		}
	}
	return v
}

// resourceField reports whether a template's field names a resource.
func resourceField(template, field string) (string, bool) {
	t, ok := editspec.TemplateByID(template)
	if !ok {
		return "", false
	}
	for _, p := range t.ResourceParams {
		if p == field {
			return p, true
		}
	}
	return "", false
}

// codePicture is a picture setting of a clip made from code (D209).
type codePicture struct {
	clip string
	st   *remotion.ClipSettings
	pic  remotion.ClipPicture
}

// pictureInputs proposes the pictures graphics draw that aren't the brand's,
// and the picture settings of clips made from code: one input per linked
// folder (its pictures fill the names bound to it in order), else one per
// picture (the same setting of one Remotion project in several clips is one
// input). A picture setting that shows the project's own picture has no
// default: a run without a new one keeps it.
func pictureInputs(ctx context.Context, a *app.App, d *models.RecipeDoc, pr *proposal, inputName func(string) string, codePics []codePicture) {
	s := &editspec.Spec{Overlays: d.Cut.Overlays, Brand: d.Cut.Brand, Resources: d.Cut.Resources}
	var names []string
	seen := map[string]bool{}
	for _, u := range editspec.ResourceUses(s, nil) {
		if u.Param == "" || u.BrandLogo || u.Font || brandPins[u.Name] || seen[u.Name] {
			continue
		}
		if _, ok := d.Cut.Resources[u.Name]; !ok {
			continue
		}
		seen[u.Name] = true
		names = append(names, u.Name)
	}
	type group struct {
		folder, label, name string
		repo, path          string   // pictures kept from a repository path (D216)
		targets             []string // where its pictures go (bindings)
		ids                 []string
		count               models.RecipeRange
		code                bool // only picture settings of clips made from code
	}
	var groups []*group
	byFolder := map[string]*group{}
	folderGroup := func(folderID string) *group {
		if g := byFolder[folderID]; g != nil {
			return g
		}
		var f models.LinkedFolder
		label := "the linked folder"
		if a.DB.GetByID(ctx, store.CLinkedFolders, a.WorkspaceID(), folderID, &f) == nil {
			label = "“" + f.Name + "”"
		}
		g := &group{folder: folderID, label: label, name: strings.Trim(label, "“”")}
		byFolder[folderID] = g
		groups = append(groups, g)
		return g
	}
	// A picture kept from a repository joins the input of the repository path
	// it was kept from (nil: not from one the repository still keeps).
	byRepoPath := map[string]*group{}
	repoGroup := func(r *models.Resource) *group {
		if r.RepoFile == nil || r.RepoFile.RepoID == "" {
			return nil
		}
		var repo models.Resource
		if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), r.RepoFile.RepoID, &repo) != nil || repo.Removed {
			return nil
		}
		dir := resources.PicturePathOf(r.RepoFile.Path, repo.PicturePaths)
		if dir == "" {
			return nil
		}
		key := repo.ID + "\x00" + dir
		if g := byRepoPath[key]; g != nil {
			return g
		}
		name := path.Base(dir)
		if dir == resources.WholeRepo {
			name = repo.Name
		}
		g := &group{repo: repo.ID, path: dir, label: repoPlace(&repo, dir), name: name}
		byRepoPath[key] = g
		groups = append(groups, g)
		return g
	}
	for _, n := range names {
		pin := d.Cut.Resources[n]
		var r models.Resource
		if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), pin.ID, &r); err != nil {
			continue
		}
		if r.Kind != editspec.ResourceKindImage && r.Kind != editspec.ResourceKindSVG {
			continue
		}
		to := Binding{Kind: models.BindResource, Where: n}.String()
		if r.Folder != nil && r.Folder.FolderID != "" {
			g := folderGroup(r.Folder.FolderID)
			g.targets, g.ids = append(g.targets, to), append(g.ids, r.ID)
			continue
		}
		if g := repoGroup(&r); g != nil {
			g.targets, g.ids = append(g.targets, to), append(g.ids, r.ID)
			continue
		}
		groups = append(groups, &group{label: "the picture “" + n + "”", name: n, targets: []string{to}, ids: []string{r.ID}, count: models.RecipeRange{Min: 1, Max: 1}})
	}
	bySetting := map[string]*group{} // source:name → its input
	for _, cp := range codePics {
		to := Binding{Kind: models.BindPictures, Where: cp.clip, Field: cp.pic.Name}.String()
		if sh := cp.pic.Shown; sh != nil && sh.ResourceID != "" {
			var r models.Resource
			if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), sh.ResourceID, &r) == nil && r.Folder != nil && r.Folder.FolderID != "" {
				g := folderGroup(r.Folder.FolderID)
				g.targets, g.ids = append(g.targets, to), append(g.ids, r.ID)
				continue
			}
			if r.ID != "" {
				if g := repoGroup(&r); g != nil {
					g.targets, g.ids = append(g.targets, to), append(g.ids, r.ID)
					continue
				}
			}
		}
		key := cp.st.SourceID + ":" + cp.pic.Name
		if g := bySetting[key]; g != nil {
			g.targets = append(g.targets, to)
			continue
		}
		g := &group{label: fmt.Sprintf("the picture %s in “%s”", cp.pic.Name, cp.st.Composition), name: strings.ToLower(cp.pic.Name), targets: []string{to},
			count: models.RecipeRange{Min: 0, Max: 1}, code: true}
		if sh := cp.pic.Shown; sh != nil && sh.ResourceID != "" {
			g.ids, g.count.Min = []string{sh.ResourceID}, 1
		}
		bySetting[key] = g
		groups = append(groups, g)
	}
	for _, g := range groups {
		x := models.RecipeInput{Kind: models.RecipePictures, Name: inputName(g.name), Label: "The " + strings.TrimPrefix(g.label, "the "), Default: models.RecipeValue{Items: g.ids}}
		switch {
		case g.folder != "":
			x.Label = "The pictures from " + g.label
			x.From = &models.RecipeSource{Folder: g.folder}
			x.Count = &models.RecipeRange{Min: 1, Max: float64(len(g.targets))}
		case g.repo != "":
			x.Label = "The pictures from " + g.label
			x.From = &models.RecipeSource{Repo: g.repo, Path: g.path}
			x.Count = &models.RecipeRange{Min: 1, Max: float64(len(g.targets))}
		default:
			c := g.count
			x.Count = &c
		}
		d.Inputs = append(d.Inputs, x)
		for _, to := range g.targets {
			d.Bindings = append(d.Bindings, models.RecipeBinding{Input: x.Name, To: to})
		}
		what := fmt.Sprintf("%s (%s)", upperFirst(x.Label[4:]), plural(len(g.targets), "picture"))
		why := "not the brand's"
		switch {
		case g.folder != "":
			why = "a run takes the folder's newest"
		case g.repo != "":
			why = "a run takes the pictures the repository keeps there at the commit it shows"
		case g.code:
			why = "a picture setting: new art needs no new code"
		}
		pr.Items = append(pr.Items, RecipeProposalItem{Key: x.Name, Kind: "input", What: what, Varies: true, Movable: true, Why: why})
	}
}

// voiceInput proposes the AI voice the narration reads in.
func voiceInput(ctx context.Context, a *app.App, p *models.Project, d *models.RecipeDoc, pr *proposal) {
	v := d.Cut.Voiceover
	if v == nil {
		return
	}
	counts := map[string]int{}
	names := map[string]string{}
	ai := false
	for _, b := range v.Beats {
		if b.Take != nil && (b.Take.AIVoice || b.Take.Source == models.TakeTTS) {
			ai = true
		}
		if b.Voice != nil && b.Voice.VoiceID != "" {
			counts[b.Voice.VoiceID]++
			if b.Voice.Name != "" {
				names[b.Voice.VoiceID] = b.Voice.Name
			}
		}
	}
	if !ai {
		return
	}
	id, name := "", ""
	best := 0
	for k, n := range counts {
		if n > best || (n == best && k < id) {
			id, best = k, n
		}
	}
	name = names[id]
	if id == "" {
		if rec, _, err := content.For(ctx, a, p.ID); err == nil {
			if vc, ok := rec.Voice["elevenlabs"]; ok {
				id, name = vc.VoiceID, vc.VoiceName
			}
		}
	}
	if id == "" {
		return
	}
	if name == "" {
		name = "the narration's voice"
	}
	x := models.RecipeInput{Name: "voice", Kind: models.RecipeVoice, Label: "The AI voice", Default: models.RecipeValue{Text: name, Items: []string{id}}}
	for _, in := range d.Inputs {
		if in.Name == "voice" {
			x.Name = "ai-voice"
		}
	}
	d.Inputs = append(d.Inputs, x)
	d.Bindings = append(d.Bindings, models.RecipeBinding{Input: x.Name, To: models.BindVoice})
	pr.Items = append(pr.Items, RecipeProposalItem{Key: x.Name, Kind: "input", What: "The AI voice: “" + name + "” (lines read again in another voice are priced first)", Varies: true, Movable: true})
}

// applyOverrides moves items between the lists as the person said.
func applyOverrides(pr *proposal, in *RecipeSaveIn) error {
	d := pr.Doc
	known := map[string]*RecipeProposalItem{}
	for i := range pr.Items {
		known[pr.Items[i].Key] = &pr.Items[i]
	}
	var unknown []string
	check := func(keys []string, kind string) {
		for _, k := range keys {
			it := known[k]
			if it == nil || (kind != "" && it.Kind != kind) || !it.Movable {
				unknown = append(unknown, "“"+k+"”")
			}
		}
	}
	check(in.Vary, "input")
	check(in.Keep, "input")
	check(in.Fixed, "section")
	check(in.Chosen, "section")
	check(in.Pack, "pack")
	if len(unknown) > 0 {
		var keys []string
		for _, it := range pr.Items {
			if it.Movable {
				keys = append(keys, it.Key)
			}
		}
		sort.Strings(keys)
		return apperr.New(apperr.InvalidInput, "The proposal has nothing called %s that can move.", joinAnd(unknown)).
			WithHint("What can move: " + strings.Join(keys, ", ") + ".")
	}
	for _, k := range in.Pack {
		if o, ok := pr.packs[k]; ok {
			d.Pack = append(d.Pack, o)
			delete(pr.packs, k)
			known[k].Varies = true
		}
	}
	for _, k := range in.Vary {
		if o, ok := pr.optional[k]; ok {
			d.Inputs = append(d.Inputs, o.input)
			d.Bindings = append(d.Bindings, o.binding...)
			delete(pr.optional, k)
		}
		known[k].Varies = true
	}
	for _, k := range in.Keep {
		removeInput(d, k)
		known[k].Varies = false
		if k == "footage" {
			// The same footage every time: its sections keep their moments.
			for i := range d.Sections {
				if d.Sections[i].Kind == models.RecipeChosen && d.Sections[i].From == k {
					d.Sections[i].Kind, d.Sections[i].From, d.Sections[i].Count, d.Sections[i].Length = models.RecipeFixed, "", nil, nil
					known[d.Sections[i].Name].Varies = false
				}
			}
		}
	}
	for _, k := range in.Fixed {
		for i := range d.Sections {
			s := &d.Sections[i]
			if s.Name != k || s.Kind != models.RecipeChosen {
				continue
			}
			// Fixed, but still showing the footage input when it varies.
			if hasInput(d, s.From) {
				for _, c := range s.Clips {
					d.Bindings = append(d.Bindings, models.RecipeBinding{Input: s.From, To: Binding{Kind: models.BindClip, Where: c.ID}.String()})
				}
			}
			s.Kind, s.From, s.Count, s.Length = models.RecipeFixed, "", nil, nil
			known[k].Varies = false
		}
	}
	for _, k := range in.Chosen {
		for i := range d.Sections {
			s := &d.Sections[i]
			if s.Name != k || s.Kind != models.RecipeFixed {
				continue
			}
			if !hasInput(d, "footage") {
				var items []string
				for _, c := range s.Clips {
					items = append(items, c.AssetID)
				}
				d.Inputs = append(d.Inputs, models.RecipeInput{Name: "footage", Kind: models.RecipeFootage, Label: "The footage", Default: models.RecipeValue{Items: items}, Count: &models.RecipeRange{Min: 1}})
			}
			total := sectionSeconds(*s)
			n := float64(len(s.Clips))
			s.Kind, s.From = models.RecipeChosen, "footage"
			s.Count = &models.RecipeRange{Min: math.Max(1, n-1), Max: n + 1}
			s.Length = &models.RecipeRange{Min: math.Floor(total * 0.8), Max: math.Ceil(total * 1.2)}
			known[k].Varies = true
		}
	}
	return nil
}

func hasInput(d *models.RecipeDoc, name string) bool {
	for _, in := range d.Inputs {
		if in.Name == name {
			return true
		}
	}
	return false
}

// removeInput takes an input and its bindings out (their places keep what the cut had).
func removeInput(d *models.RecipeDoc, name string) {
	var ins []models.RecipeInput
	for _, in := range d.Inputs {
		if in.Name != name {
			ins = append(ins, in)
		}
	}
	d.Inputs = ins
	var bs []models.RecipeBinding
	for _, b := range d.Bindings {
		if b.Input != name {
			bs = append(bs, b)
		}
	}
	d.Bindings = bs
}

// exampleOf is the render that shows what a version of a cut makes: a final
// render, else a review copy, that is ready.
func exampleOf(ctx context.Context, a *app.App, rev *models.Revision) *models.RecipeExample {
	var rs []models.Render
	if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), bson.M{"revisionId": rev.ID, "state": models.RenderAvailable}, &rs,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(20)); err != nil || len(rs) == 0 {
		return nil
	}
	best := rs[0]
	for _, r := range rs {
		if r.Quality == "final" {
			best = r
			break
		}
	}
	return &models.RecipeExample{RenderID: best.ID, ProjectID: best.ProjectID, RevisionID: rev.ID}
}

// newRecipe makes a recipe's record.
func newRecipe(ctx context.Context, a *app.App, name, title, projectID string) (*models.Recipe, error) {
	if name == "" {
		name = shortName(title)
	} else if !namePattern.MatchString(name) {
		return nil, apperr.New(apperr.InvalidInput, "A recipe's short name is small letters, digits and dashes (like app-teaser).")
	}
	name, err := freeName(ctx, a, name)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	r := &models.Recipe{ID: ids.New(ids.Recipe), WorkspaceID: a.WorkspaceID(), Name: name, Title: title, ProjectID: projectID, CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CRecipes, r); err != nil {
		if store.IsDuplicateKey(err) {
			return nil, apperr.New(apperr.Conflict, "Another recipe was just saved as “%s”; try again.", name)
		}
		return nil, err
	}
	return r, nil
}

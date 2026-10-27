package recipes

import (
	"context"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// The recipe sheet as operations (D213): recipe.checkout writes a version as
// its sheet; recipe.check reads a sheet and says what is wrong with it and
// what it changes (nothing is saved); recipe.write saves it as a draft — a new
// version of the recipe it names, or a new recipe. Only a person makes a
// version current (recipe.publish), whoever wrote the sheet.

// RecipeCheckoutOut is a recipe version as its sheet.
type RecipeCheckoutOut struct {
	// Sheet is the recipe sheet (Markdown): edit it, then check it and save it
	// as a draft (recipe check --save).
	Sheet   string           `json:"sheet"`
	Name    string           `json:"name"`
	File    string           `json:"file" jsonschema:"description=A file name for it: <name>.recipe.md."`
	Version RecipeVersionRow `json:"version"`
	Message string           `json:"message"`
}

// RecipeWriteIn saves a recipe sheet as a draft.
type RecipeWriteIn struct {
	Sheet string `json:"sheet" jsonschema:"minLength=1,maxLength=4000000,description=A recipe sheet (recipe checkout writes one)."`
	Note  string `json:"note,omitempty" jsonschema:"maxLength=300,description=What the draft changes\\, in a few words."`
}

// RecipeWriteOut is the draft a sheet became.
type RecipeWriteOut struct {
	Recipe  RecipeRow        `json:"recipe"`
	Version RecipeVersionRow `json:"version"`
	// New: the sheet made a new recipe; Same: nothing changed (the version it
	// already is comes back).
	New      bool                `json:"new"`
	Same     bool                `json:"same"`
	Changes  []string            `json:"changes"`
	Problems []cutsheet.Problem  `json:"problems"`
	Check    *models.RecipeCheck `json:"check,omitempty"`
	// Sheet is the saved version's sheet (its base is the new version).
	Sheet   string `json:"sheet"`
	Message string `json:"message"`
	Next    string `json:"next,omitempty"`
}

// SheetFile is the file name a recipe's sheet is written to.
func SheetFile(name string) string { return name + ".recipe.md" }

func init() {
	ops.Def("recipe.checkout", "Write a recipe version as its sheet: the recipe in Markdown — inputs, sections, the pack, bindings, rules, hints and its cut as a cut sheet — to read, edit and keep in git; check it and save it as a draft afterwards", ops.Flags{}, checkoutRecipe)
	ops.Def("recipe.write", "Save a recipe sheet as a draft: a new version of the recipe it names (built on the version it was written from), or a new recipe; a person makes it current", ops.Flags{Mutating: true}, writeRecipe)
}

// sheetContext is what printing and reading a sheet needs from the workspace.
type sheetContext struct {
	ctx    context.Context
	a      *app.App
	titles map[string]string
}

func newSheetContext(ctx context.Context, a *app.App) *sheetContext {
	return &sheetContext{ctx: ctx, a: a, titles: map[string]string{}}
}

// title names a project, folder, collection, clip or picture by its ID ("" unknown).
func (s *sheetContext) title(id string) string {
	if t, ok := s.titles[id]; ok {
		return t
	}
	t := ""
	ws := s.a.WorkspaceID()
	switch {
	case ids.Valid(ids.Project, id):
		var p models.Project
		if s.a.DB.GetByID(s.ctx, store.CProjects, ws, id, &p) == nil {
			t = p.Name
		}
	case ids.Valid(ids.Asset, id):
		var as models.Asset
		if s.a.DB.GetByID(s.ctx, store.CAssets, ws, id, &as) == nil {
			t = as.DisplayTitle()
		}
	case ids.Valid(ids.Resource, id):
		var r models.Resource
		if s.a.DB.GetByID(s.ctx, store.CResources, ws, id, &r) == nil {
			t = r.Title
			if t == "" {
				t = r.Name
			}
		}
	case strings.HasPrefix(id, "fld_"):
		var f models.LinkedFolder
		if s.a.DB.GetByID(s.ctx, store.CLinkedFolders, ws, id, &f) == nil {
			t = f.Name
		}
	case strings.HasPrefix(id, "col_"):
		var c models.Collection
		if s.a.DB.GetByID(s.ctx, store.CCollections, ws, id, &c) == nil {
			t = c.Name
		}
	}
	s.titles[id] = t
	return t
}

// footage are the clips the inputs give, for the cut's footage list.
func sheetFootage(d *models.RecipeDoc) []string {
	var out []string
	for _, in := range d.Inputs {
		if in.Kind != models.RecipeFootage {
			continue
		}
		for _, id := range in.Default.Items {
			if ids.Valid(ids.Asset, id) {
				out = append(out, id)
			}
		}
	}
	return out
}

// versionNote says which version a sheet was written from ("version 2, current").
func versionNote(v *models.RecipeVersion) string {
	state := "earlier"
	switch v.State {
	case models.RecipeCurrent:
		state = "current"
	case models.RecipeDraft:
		state = "a draft"
	}
	return fmt.Sprintf("version %d, %s", v.Number, state)
}

// printVersion writes a version as its sheet.
func printVersion(ctx context.Context, a *app.App, r *models.Recipe, v *models.RecipeVersion) string {
	sc := newSheetContext(ctx, a)
	return PrintSheet(&v.Doc, SheetOptions{Name: r.Name, Base: v.Hash, BaseNote: versionNote(v), Info: ops.SheetInfo(ctx, a, homeProject(&v.Doc)),
		Footage: sheetFootage(&v.Doc), Title: sc.title})
}

func checkoutRecipe(ctx context.Context, a *app.App, in *RecipeRefIn) (*RecipeCheckoutOut, error) {
	r, err := loadRecipe(ctx, a, in.Recipe)
	if err != nil {
		return nil, err
	}
	v, err := loadVersion(ctx, a, r, in.Version)
	if err != nil {
		return nil, err
	}
	out := &RecipeCheckoutOut{Sheet: printVersion(ctx, a, r, v), Name: r.Name, File: SheetFile(r.Name), Version: versionRow(v, nil),
		Message: fmt.Sprintf("Version %d of “%s” as its sheet: edit it, check it, and save it as a draft a person makes current.", v.Number, r.Title)}
	return out, nil
}

// readSheet reads a sheet with the workspace's footage, pictures and Remotion
// clips (looked up in the recipe's own project).
func readSheet(ctx context.Context, a *app.App, text string) (*Sheet, []cutsheet.Problem) {
	sheet, probs := ParseSheet(text, SheetParseOptions{Info: ops.SheetInfo(ctx, a, "")})
	if sheet.Doc != nil {
		if home := homeProject(sheet.Doc); home != "" {
			sheet, probs = ParseSheet(text, SheetParseOptions{Info: ops.SheetInfo(ctx, a, home)})
		}
	}
	if probs == nil {
		probs = []cutsheet.Problem{}
	}
	return sheet, probs
}

// sheetTarget is the recipe and the version a sheet builds on (nil: a new recipe).
type sheetTarget struct {
	recipe *models.Recipe
	base   *models.RecipeVersion
}

// targetOf finds what a sheet names: an existing recipe and the version it was
// written from, or a new recipe's free name.
func targetOf(ctx context.Context, a *app.App, sheet *Sheet) (*sheetTarget, error) {
	name := strings.TrimSpace(sheet.Name)
	if name == "" {
		return nil, apperr.New(apperr.InvalidInput, "The sheet doesn't say which recipe it is: it needs name: with the recipe's short name.").
			WithHint("Add `name: <short name>` under `recipe: 1` (a new name makes a new recipe).")
	}
	r, err := loadRecipe(ctx, a, name)
	if err != nil {
		if apperr.From(err).Code != apperr.NotFound {
			return nil, err
		}
		if sheet.Base != "" {
			return nil, apperr.New(apperr.NotFound, "The sheet says it was written from a version of “%s”, but there is no such recipe here.", name).
				WithHint("Take out base: to save it as a new recipe.")
		}
		if !namePattern.MatchString(name) {
			return nil, apperr.New(apperr.InvalidInput, "A recipe's short name is small letters, digits and dashes (like app-teaser), not “%s”.", name)
		}
		return &sheetTarget{}, nil
	}
	if sheet.Base == "" {
		return nil, apperr.New(apperr.Conflict, "There is already a recipe called “%s”: check it out to change it (its sheet says which version it builds on), or give this one another name.", r.Name).
			WithCommand("metarush recipe checkout " + r.Name)
	}
	var v models.RecipeVersion
	if err := a.DB.GetByID(ctx, store.CRecipeVersions, a.WorkspaceID(), versionID(a.WorkspaceID(), sheet.Base), &v); err != nil || v.RecipeID != r.ID {
		return nil, apperr.New(apperr.NotFound, "The sheet says it was written from a version “%s” doesn't have.", r.Title).
			WithHint("Check out the recipe again and make the change there.").WithCommand("metarush recipe checkout " + r.Name)
	}
	return &sheetTarget{recipe: r, base: &v}, nil
}

// checkSheet reads a sheet and checks the recipe in it (nothing is saved).
func checkSheet(ctx context.Context, a *app.App, text string) (*RecipeCheckOut, error) {
	sheet, probs := readSheet(ctx, a, text)
	out := &RecipeCheckOut{Problems: probs, Changes: []string{}, Name: sheet.Name}
	if cutsheet.HasErrors(probs) {
		n := 0
		for _, p := range probs {
			if p.Severity == "error" {
				n++
			}
		}
		out.Message = fmt.Sprintf("The sheet has %s to fix before it can be checked as a recipe.", plural(n, "problem"))
		return out, nil
	}
	out.Valid = true
	doc := sheetDoc(sheet)
	t, err := targetOf(ctx, a, sheet)
	if err != nil {
		ae := apperr.From(err)
		out.Problems = append(out.Problems, cutsheet.Problem{Severity: "error", Code: "recipe_" + strings.ToLower(ae.Code), Line: 1, Col: 1, EndCol: 2, Message: ae.Message, Hint: ae.Hint})
		out.Valid = false
	}
	chk := checkDoc(ctx, a, doc)
	out.Check = *chk
	out.Title = doc.Title
	if t != nil && t.base != nil {
		row := versionRow(t.base, nil)
		out.Base = &row
		out.Same = sameAsVersion(doc, t.base)
		out.Changes = DocChanges(ctx, a, &t.base.Doc, doc)
		if !out.Same {
			out.Parts, out.RemovedRules = Differences(ctx, a, &t.base.Doc, doc), removedRules(&t.base.Doc, doc)
		}
	} else if t != nil {
		out.New = true
	}
	switch {
	case !out.Valid:
		out.Message = out.Problems[len(out.Problems)-1].Message
	case out.Same:
		out.Message = fmt.Sprintf("Nothing changed: the sheet is version %d of “%s”.", t.base.Number, t.recipe.Title)
	case !chk.OK:
		out.Message = fmt.Sprintf("The sheet reads, but the recipe has %s to fix: %s", plural(len(chk.Problems), "problem"), chk.Problems[0].Message)
	case out.New:
		out.Message = fmt.Sprintf("The sheet is a new recipe, “%s”, that checks out: with its own inputs it makes %s. Saving it makes a draft a person makes current.", doc.Title, seconds(chk.Duration))
	default:
		out.Message = fmt.Sprintf("The sheet checks out: %s to version %d; with its own inputs it makes %s. Saving it makes a draft a person makes current.", plural(len(out.Changes), "change"), t.base.Number, seconds(chk.Duration))
	}
	return out, nil
}

// sameAsVersion reports whether a document (as saved: normalized) is a
// version's, normalized the same way (versions saved before D213 weren't).
func sameAsVersion(doc *models.RecipeDoc, v *models.RecipeVersion) bool {
	if _, h := Canonical(doc); h == v.Hash {
		return true
	}
	base := Clone(&v.Doc)
	NormalizeDoc(base)
	_, a := Canonical(doc)
	_, b := Canonical(base)
	return a == b
}

// sheetDoc is a sheet's document as MetaRush saves it: its format, what it
// makes (when the sheet doesn't say) and its cut's defaults.
func sheetDoc(sheet *Sheet) *models.RecipeDoc {
	doc := Clone(sheet.Doc)
	if doc.Format == "" {
		doc.Format = models.RecipeFormat
	}
	if strings.TrimSpace(doc.Makes) == "" {
		doc.Makes = MakesWords(doc)
	}
	NormalizeDoc(doc)
	return doc
}

func writeRecipe(ctx context.Context, a *app.App, in *RecipeWriteIn) (*RecipeWriteOut, error) {
	sheet, probs := readSheet(ctx, a, in.Sheet)
	if cutsheet.HasErrors(probs) {
		n := 0
		for _, p := range probs {
			if p.Severity == "error" {
				n++
			}
		}
		return nil, apperr.New(apperr.Validation, "The recipe sheet has %s to fix.", plural(n, "problem")).WithDetails(map[string]any{"problems": probs}).
			WithHint("Check the sheet to see each problem with its line.").WithCommand("metarush recipe check <file>")
	}
	t, err := targetOf(ctx, a, sheet)
	if err != nil {
		return nil, err
	}
	doc := sheetDoc(sheet)
	chk := checkDoc(ctx, a, doc)
	out := &RecipeWriteOut{Problems: probs, Check: chk, Changes: []string{}}
	r := t.recipe
	parent := ""
	if r == nil {
		project := homeProject(doc)
		if r, err = newRecipe(ctx, a, sheet.Name, doc.Title, project); err != nil {
			return nil, err
		}
		if r.Name != sheet.Name {
			_, _ = a.DB.C(store.CRecipes).DeleteOne(ctx, map[string]any{"_id": r.ID, "workspaceId": a.WorkspaceID()})
			return nil, apperr.New(apperr.Conflict, "Another recipe was just saved as “%s”; give this one another name.", sheet.Name)
		}
		out.New = true
	} else {
		parent = t.base.Hash
		out.Changes = DocChanges(ctx, a, &t.base.Doc, doc)
		if sameAsVersion(doc, t.base) {
			// The version it was written from, unchanged (one saved before
			// versions were normalized reads back normalized): nothing to save.
			out.Same, out.Recipe, out.Version = true, recipeRow(ctx, a, r, t.base), versionRow(t.base, nil)
			out.Sheet = printVersion(ctx, a, r, t.base)
			out.Message = fmt.Sprintf("Nothing changed: that is version %d of “%s”.", t.base.Number, r.Title)
			return out, nil
		}
	}
	v, made, err := saveVersion(ctx, a, r, doc, parent, in.Note, chk)
	if err != nil {
		if out.New {
			_, _ = a.DB.C(store.CRecipes).DeleteOne(ctx, map[string]any{"_id": r.ID, "workspaceId": a.WorkspaceID()}) // never a recipe without a version
		}
		return nil, err
	}
	out.Same = !made
	numbers := map[string]int{}
	if t.base != nil {
		numbers[t.base.Hash] = t.base.Number
	}
	out.Recipe, out.Version = recipeRow(ctx, a, r, v), versionRow(v, numbers)
	out.Sheet = printVersion(ctx, a, r, v)
	if made {
		a.Audit(ctx, "recipe.write", "recipe", r.ID, r.ProjectID, map[string]any{"version": v.Number, "new": out.New, "changes": len(out.Changes)})
	}
	switch {
	case out.Same:
		out.Message = fmt.Sprintf("Nothing changed: that is version %d of “%s”.", v.Number, r.Title)
	case out.New && !chk.OK:
		out.Message = fmt.Sprintf("Saved “%s” as a new recipe, a draft; its check found %s to fix before a person makes it current.", r.Title, plural(len(chk.Problems), "problem"))
	case out.New:
		out.Message = fmt.Sprintf("Saved “%s” as a new recipe, a draft: a person makes it current before it runs.", r.Title)
	case !chk.OK:
		out.Message = fmt.Sprintf("Draft version %d of “%s” is saved; its check found %s to fix.", v.Number, r.Title, plural(len(chk.Problems), "problem"))
	default:
		out.Message = fmt.Sprintf("Draft version %d of “%s” is saved (%s); a person makes it current.", v.Number, r.Title, plural(len(out.Changes), "change"))
	}
	if !out.Same {
		out.Next = "A person looks at the draft and makes it current (the recipe's page, or metarush recipe publish in their terminal)."
	}
	return out, nil
}

// ---- what changed ----

// DocChanges says in plain words what changed between two versions of a
// recipe: its names, inputs, sections, pack, bindings, rules, sign-offs and
// hints, then its cut (the cut sheet's own words).
func DocChanges(ctx context.Context, a *app.App, from, to *models.RecipeDoc) []string {
	out := []string{}
	add := func(f string, x ...any) { out = append(out, fmt.Sprintf(f, x...)) }
	if from.Title != to.Title {
		add("The title is now “%s”.", to.Title)
	}
	if from.Makes != to.Makes {
		add("It makes %s.", to.Makes)
	}
	if !sameJSON(from.Length, to.Length) {
		if to.Length == nil {
			add("It has no length to keep to any more.")
		} else {
			add("The video should be %s long.", sheetRange(*to.Length, " s"))
		}
	}
	if !sameJSON(from.Lands, to.Lands) {
		add("Runs now make %s.", landsWords(ctx, a, to))
	}
	if from.Settings != to.Settings {
		add("Runs follow other Content Settings.")
	}
	// Inputs.
	was := map[string]models.RecipeInput{}
	for _, in := range from.Inputs {
		was[in.Name] = in
	}
	now := map[string]bool{}
	for _, in := range to.Inputs {
		now[in.Name] = true
		old, ok := was[in.Name]
		switch {
		case !ok:
			add("Added the input “%s” (%s).", in.Name, kindWords(in.Kind))
		case !sameJSON(old, in):
			add("The input “%s” changed%s.", in.Name, fieldsChanged(old, in))
		}
	}
	for _, in := range from.Inputs {
		if !now[in.Name] {
			add("Took out the input “%s”.", in.Name)
		}
	}
	// Sections (their settings; their clips are the cut's).
	wasSec := map[string]models.RecipeSection{}
	for _, s := range from.Sections {
		s.Clips = nil
		wasSec[s.Name] = s
	}
	nowSec := map[string]bool{}
	for _, s := range to.Sections {
		nowSec[s.Name] = true
		s2 := s
		s2.Clips = nil
		old, ok := wasSec[s.Name]
		switch {
		case !ok:
			add("Added the section “%s” (%s).", s.Name, map[string]string{models.RecipeFixed: "the same every run", models.RecipeChosen: "Claude chooses its moments"}[s.Kind])
		case !sameJSON(old, s2):
			add("The section “%s” changed%s.", s.Name, fieldsChanged(old, s2))
		}
	}
	for _, s := range from.Sections {
		if !nowSec[s.Name] {
			add("Took out the section “%s”.", s.Name)
		}
	}
	if order := func(d *models.RecipeDoc) string {
		var ns []string
		for _, s := range d.Sections {
			ns = append(ns, s.Name)
		}
		return strings.Join(ns, "\x00")
	}; order(from) != order(to) && len(from.Sections) == len(to.Sections) {
		add("The sections are in another order.")
	}
	// The pack.
	wasOut := map[string]models.RecipeOutput{}
	for _, o := range from.Pack {
		wasOut[o.Name] = o
	}
	nowOut := map[string]bool{}
	for _, o := range to.Pack {
		nowOut[o.Name] = true
		old, ok := wasOut[o.Name]
		switch {
		case !ok:
			add("Each run also makes %s.", outputWords(o))
		case !sameJSON(old, o):
			add("The pack's “%s” changed: %s.", o.Name, outputWords(o))
		}
	}
	for _, o := range from.Pack {
		if !nowOut[o.Name] {
			add("Runs no longer make the pack's “%s”.", o.Name)
		}
	}
	// Bindings, rules, sign-offs, hints.
	listChanges := func(what string, a, b []string) {
		have := map[string]bool{}
		for _, x := range a {
			have[x] = true
		}
		keep := map[string]bool{}
		for _, x := range b {
			keep[x] = true
			if !have[x] {
				add("Added the %s “%s”.", what, shorten(x, 80))
			}
		}
		for _, x := range a {
			if !keep[x] {
				add("Took out the %s “%s”.", what, shorten(x, 80))
			}
		}
	}
	bindings := func(d *models.RecipeDoc) []string {
		var out []string
		for _, b := range d.Bindings {
			out = append(out, b.Input+" → "+b.To)
		}
		return out
	}
	listChanges("binding", bindings(from), bindings(to))
	rules := func(d *models.RecipeDoc) []string {
		var out []string
		for _, r := range d.Rules {
			out = append(out, r.Text)
		}
		return out
	}
	listChanges("rule", rules(from), rules(to))
	listChanges("sign-off", from.Signoffs, to.Signoffs)
	listChanges("hint", from.Hints, to.Hints)
	// The cut, in the cut sheet's words.
	fs, ts := cutSpec(from), cutSpec(to)
	editspec.Normalize(fs)
	editspec.Normalize(ts)
	if editspec.Hash(fs) != editspec.Hash(ts) {
		names := cutsheet.NameFunc(ts, ops.SheetInfo(ctx, a, homeProject(to)), nil)
		for _, c := range cutsheet.Describe(fs, ts, names) {
			out = append(out, "The cut: "+c)
		}
	}
	return out
}

// fieldsChanged names the fields that differ between two items (": its label, count").
func fieldsChanged(a, b any) string {
	ma, mb := membersOf(a), membersOf(b)
	var names []string
	seen := map[string]bool{}
	for _, k := range append(append([]string{}, ma.keys...), mb.keys...) {
		if seen[k] || k == "name" {
			continue
		}
		seen[k] = true
		if string(ma.vals[k]) != string(mb.vals[k]) {
			n := fieldNames[k]
			if n == "" {
				n = k
			}
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return ""
	}
	return ": its " + joinAnd(names)
}

// fieldNames say a recipe item's fields plainly.
var fieldNames = map[string]string{"kind": "kind", "label": "label", "required": "being required", "default": "default", "from": "where it comes from",
	"maxLength": "longest words", "choices": "choices", "count": "count", "purpose": "purpose", "length": "length", "clips": "clips"}

func sameJSON(a, b any) bool {
	ma, mb := mustRaw(a), mustRaw(b)
	return string(ma) == string(mb)
}

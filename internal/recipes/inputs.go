package recipes

import (
	"context"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/drive"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Inputs (D202): what a run gives each input — words, or things in MetaRush
// by name or ID — resolved to what they are (each file's SHA-256, a
// picture's version, a voice by its ID) and recorded on the run.

// RecipeInputValue is what a run gives one input.
type RecipeInputValue struct {
	Name string `json:"name" jsonschema:"minLength=1,maxLength=40"`
	Text string `json:"text,omitempty" jsonschema:"maxLength=2000,description=Words\\, a choice or a voice (its name or ID)."`
	// Items are clips or pictures by name or ID.
	Items []string `json:"items,omitempty" jsonschema:"maxItems=100,description=Clips or pictures (their names or IDs)."`
}

// resolver turns what a run gives into what its inputs are.
type resolver struct {
	ctx context.Context
	a   *app.App
	doc *models.RecipeDoc
	// project is where names are looked up (the run's project, else the
	// recipe's own); view its links.
	project string
	view    *projectlinks.View
	// prev are the inputs of the run this one repeats.
	prev   map[string]*models.RecipeRunInput
	eleven *providers.ElevenClient
	// offline: a check, which never lists a Google Drive folder (D220): an
	// input from one is checked with the recipe's own items.
	offline bool
	// drives are the Drive folders listed for this plan, by remote:path.
	drives map[string][]drive.File
}

func newResolver(ctx context.Context, a *app.App, d *models.RecipeDoc, project string, prev *models.RecipeRun) (*resolver, error) {
	view, err := projectlinks.Load(ctx, a, project)
	if err != nil {
		return nil, err
	}
	rs := &resolver{ctx: ctx, a: a, doc: d, project: project, view: view, prev: map[string]*models.RecipeRunInput{}}
	if prev != nil {
		for i := range prev.Inputs {
			rs.prev[prev.Inputs[i].Name] = &prev.Inputs[i]
		}
	}
	return rs, nil
}

// resolveAll resolves every input; problems say plainly what is wrong.
func (rs *resolver) resolveAll(given []RecipeInputValue) ([]models.RecipeRunInput, []string) {
	byName := map[string]*RecipeInputValue{}
	var problems []string
	for i := range given {
		g := &given[i]
		name := strings.TrimSpace(g.Name)
		found := false
		for _, in := range rs.doc.Inputs {
			if in.Name == name {
				found = true
			}
		}
		if !found {
			problems = append(problems, fmt.Sprintf("The recipe has no input called “%s”.", name))
			continue
		}
		byName[name] = g
	}
	var out []models.RecipeRunInput
	for _, in := range rs.doc.Inputs {
		v, ps := rs.resolve(in, byName[in.Name])
		problems = append(problems, ps...)
		if v != nil {
			out = append(out, *v)
		}
	}
	return out, problems
}

// resolve resolves one input.
func (rs *resolver) resolve(in models.RecipeInput, given *RecipeInputValue) (*models.RecipeRunInput, []string) {
	label := inputLabel(in)
	prev := rs.prev[in.Name]
	fromDriveFolder := hasCloudFolder(in) && !rs.offline
	fromSource := in.From != nil && (in.From.Folder != "" || in.From.Collection != "" || in.From.Project != "" || in.From.Repo != "") || fromDriveFolder
	v := &models.RecipeRunInput{Name: in.Name, Kind: in.Kind}
	var problems []string
	switch {
	case given != nil:
	case prev != nil && !fromSource:
		cp := *prev
		cp.Changed, cp.Items = false, append([]models.RecipeRunItem(nil), prev.Items...)
		return &cp, nil
	}
	switch in.Kind {
	case models.RecipeWords, models.RecipeChoice:
		v.Text = in.Default.Text
		if given != nil {
			v.Text = strings.TrimSpace(given.Text)
		}
		switch {
		case v.Text == "" && (in.Required || given != nil):
			problems = append(problems, fmt.Sprintf("%s needs words.", upperFirst(label)))
		case in.MaxLength > 0 && len([]rune(v.Text)) > in.MaxLength:
			problems = append(problems, fmt.Sprintf("%s takes at most %d characters; “%s” has %d.", upperFirst(label), in.MaxLength, shorten(v.Text, 60), len([]rune(v.Text))))
		case in.Kind == models.RecipeChoice && len(in.Choices) > 0 && !containsFold(in.Choices, v.Text):
			problems = append(problems, fmt.Sprintf("%s is one of %s.", upperFirst(label), joinAnd(quoteAll(in.Choices))))
		}
		v.Words = "“" + shorten(v.Text, 80) + "”"
	case models.RecipeVoice:
		item, ps := rs.voice(in, given)
		problems = append(problems, ps...)
		if item != nil {
			v.Items, v.Text = []models.RecipeRunItem{*item}, item.Title
			v.Words = "the voice “" + item.Title + "”"
		}
	case models.RecipeFootage, models.RecipePictures:
		if given != nil || !fromDriveFolder {
			if in.Kind == models.RecipeFootage {
				items, ps := rs.footage(in, given, fromSource)
				problems = append(problems, ps...)
				v.Items = items
				v.Words = itemsWords(items, "clip")
				break
			}
			v, problems = rs.pictureInput(in, given, fromSource, v, problems)
			break
		}
		// A Google Drive folder (D220): the newest files, fetched by the run.
		got, ps := rs.fromDrive(in)
		problems = append(problems, ps...)
		if got != nil {
			what := "picture"
			if in.Kind == models.RecipeFootage {
				what = "clip"
			}
			v.Items, v.Drive, v.DriveKind = got.items, got.folder, got.kind
			v.Words = itemsWords(got.items, what) + " (" + got.words + ")"
		}
	}
	return rs.finishInput(in, v, prev, problems)
}

// pictureInput resolves a pictures input that doesn't come from a Google Drive folder.
func (rs *resolver) pictureInput(in models.RecipeInput, given *RecipeInputValue, fromSource bool, v *models.RecipeRunInput, problems []string) (*models.RecipeRunInput, []string) {
	if given == nil && fromSource && in.From.Repo != "" {
		// Pictures from a repository path (D216): its commit and each picture's hash.
		got, ps := rs.repoPictures(in)
		problems = append(problems, ps...)
		if got != nil {
			v.Items, v.Repo, v.Commit = got.items, got.repo, got.commit
			v.Words = itemsWords(got.items, "picture") + " (" + got.words + ")"
		}
		return v, problems
	}
	items, ps := rs.pictures(in, given, fromSource)
	problems = append(problems, ps...)
	v.Items = items
	v.Words = itemsWords(items, "picture")
	return v, problems
}

// finishInput checks how many clips or pictures an input has, and says
// whether it is the recipe's own and whether it changed.
func (rs *resolver) finishInput(in models.RecipeInput, v *models.RecipeRunInput, prev *models.RecipeRunInput, problems []string) (*models.RecipeRunInput, []string) {
	label := inputLabel(in)
	if in.Count != nil && (in.Kind == models.RecipeFootage || in.Kind == models.RecipePictures) && len(problems) == 0 {
		n := float64(len(v.Items))
		what := "clip"
		if in.Kind == models.RecipePictures {
			what = "picture"
		}
		switch {
		case in.Count.Min > 0 && n < in.Count.Min:
			problems = append(problems, fmt.Sprintf("%s needs at least %s; it has %d.", upperFirst(label), plural(int(in.Count.Min), what), len(v.Items)))
		case in.Count.Max > 0 && n > in.Count.Max:
			problems = append(problems, fmt.Sprintf("%s takes at most %s; it has %d.", upperFirst(label), plural(int(in.Count.Max), what), len(v.Items)))
		}
	}
	v.Default = sameAsDefault(in, v)
	if prev != nil {
		v.Changed = !sameValue(prev, v)
	} else {
		v.Changed = !v.Default
	}
	return v, problems
}

// voice resolves a voice input: the recipe's voice, or one of the account's
// voices by name or ID.
func (rs *resolver) voice(in models.RecipeInput, given *RecipeInputValue) (*models.RecipeRunItem, []string) {
	def := models.RecipeRunItem{Title: in.Default.Text}
	if len(in.Default.Items) > 0 {
		def.ID = in.Default.Items[0]
	}
	if given == nil || strings.TrimSpace(given.Text) == "" {
		if def.ID == "" {
			// No voice of its own (a starter recipe, D295): the narration voice the
			// Content Settings name, as every line without a voice reads in.
			if it := rs.settingsVoice(); it != nil {
				return it, nil
			}
			if rs.offline {
				return nil, nil // a check: the run gives one, or the Content Settings will
			}
			return nil, []string{"The recipe's narration has no voice: give the run one, or choose a narration voice in Content Settings."}
		}
		return &def, nil
	}
	ref := strings.TrimSpace(given.Text)
	if ref == def.ID || strings.EqualFold(ref, def.Title) {
		return &def, nil
	}
	if p := rs.prev[in.Name]; p != nil && len(p.Items) > 0 && (ref == p.Items[0].ID || strings.EqualFold(ref, p.Items[0].Title)) {
		it := p.Items[0]
		return &it, nil
	}
	if rs.eleven == nil {
		c, err := providers.ElevenClientForProject(rs.ctx, rs.a, rs.project)
		if err != nil {
			return nil, []string{"Another AI voice needs ElevenLabs, which isn't set up on this computer (Settings → Integrations)."}
		}
		rs.eleven = c
	}
	v, err := providers.FindVoice(rs.ctx, rs.eleven, ref)
	if err != nil {
		return nil, []string{fmt.Sprintf("Your ElevenLabs account has no voice called “%s”.", ref)}
	}
	return &models.RecipeRunItem{ID: v.VoiceID, Title: v.Name}, nil
}

// settingsVoice is the ElevenLabs narration voice the Content Settings of the
// project names are looked up in name (D193), as a voice input's value; nil
// when they name none.
func (rs *resolver) settingsVoice() *models.RecipeRunItem {
	rec, _, err := content.For(rs.ctx, rs.a, rs.project)
	if err != nil || rec == nil {
		return nil
	}
	vc, ok := rec.Voice[providers.ElevenLabsName]
	if !ok || vc.VoiceID == "" {
		return nil
	}
	title := vc.VoiceName
	if title == "" {
		title = vc.VoiceID
	}
	return &models.RecipeRunItem{ID: vc.VoiceID, Title: title}
}

// footage resolves clips: given by name or ID, else from the input's
// collection or project (newest first), else the recipe's.
func (rs *resolver) footage(in models.RecipeInput, given *RecipeInputValue, fromSource bool) ([]models.RecipeRunItem, []string) {
	var refs []string
	switch {
	case given != nil:
		refs = given.Items
	case fromSource && (in.From.Collection != "" || in.From.Project != ""):
		src := in.From.Collection
		if src == "" {
			src = in.From.Project
		}
		var as []models.Asset
		n := int64(countWanted(in))
		if err := rs.a.DB.Find(rs.ctx, store.CAssets, rs.a.WorkspaceID(), sourceFootage(src), &as, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(n)); err != nil {
			return nil, []string{"MetaRush couldn't read the footage it takes."}
		}
		var out []models.RecipeRunItem
		for i := range as {
			out = append(out, clipItem(&as[i]))
		}
		if len(out) == 0 {
			return nil, []string{fmt.Sprintf("%s has no footage to take yet.", upperFirst(inputLabel(in)))}
		}
		return out, nil
	default:
		refs = in.Default.Items
	}
	var out []models.RecipeRunItem
	var problems []string
	for _, ref := range refs {
		as, err := ops.ResolveFootage(rs.ctx, rs.a, ref, "")
		if err != nil {
			problems = append(problems, fmt.Sprintf("There's no clip “%s”.", ref))
			continue
		}
		switch {
		case as.Trash != nil:
			problems = append(problems, fmt.Sprintf("The clip “%s” is in the Trash.", as.DisplayTitle()))
		case as.State != models.AssetAvailable:
			problems = append(problems, fmt.Sprintf("The clip “%s” isn't ready yet.", as.DisplayTitle()))
		case as.Kind == "image":
			problems = append(problems, fmt.Sprintf("“%s” is a picture, not footage.", as.DisplayTitle()))
		default:
			out = append(out, clipItem(as))
		}
	}
	return out, problems
}

// sourceFootage finds the footage a collection or a project gives an input:
// clips with a picture, ready — never the narration made for its cuts (AI
// voice, scratch and recorded takes) or a clearer copy, which play as sound and
// can't be a clip.
func sourceFootage(src string) bson.M {
	filter := projectlinks.SourceFootage(src)
	filter["kind"] = "video"
	filter["state"] = models.AssetAvailable
	filter["$and"] = bson.A{bson.M{"tags": bson.M{"$ne": "voiceover"}}}
	return filter
}

func clipItem(as *models.Asset) models.RecipeRunItem {
	return models.RecipeRunItem{ID: as.ID, Title: as.DisplayTitle(), SHA256: as.SHA256, Duration: as.Media.Duration}
}

// pictures resolves pictures: given by name or ID (their newest version),
// else the input's linked folder's newest, else the versions the recipe pins.
func (rs *resolver) pictures(in models.RecipeInput, given *RecipeInputValue, fromSource bool) ([]models.RecipeRunItem, []string) {
	switch {
	case given != nil:
		var out []models.RecipeRunItem
		var problems []string
		for _, ref := range given.Items {
			e, err := resources.Find(rs.ctx, rs.a, rs.project, rs.view, ref)
			if err != nil {
				problems = append(problems, fmt.Sprintf("There's no picture “%s” this project can use.", ref))
				continue
			}
			it, why := pictureItem(&e.Resource, "")
			if why != "" {
				problems = append(problems, why)
				continue
			}
			out = append(out, it)
		}
		return out, problems
	case fromSource && in.From.Folder != "":
		var f models.LinkedFolder
		if err := rs.a.DB.GetByID(rs.ctx, store.CLinkedFolders, rs.a.WorkspaceID(), in.From.Folder, &f); err != nil {
			return nil, []string{fmt.Sprintf("The linked folder %s takes its pictures from is gone.", inputLabel(in))}
		}
		view := rs.view
		if f.ProjectID != "" && f.ProjectID != rs.project {
			if v, err := projectlinks.Load(rs.ctx, rs.a, f.ProjectID); err == nil {
				view = v
			}
		}
		es, err := resources.Visible(rs.ctx, rs.a, f.ProjectID, view, resources.ListOptions{Folder: f.ID})
		if err != nil {
			return nil, []string{"MetaRush couldn't read the linked folder “" + f.Name + "”."}
		}
		want := countWanted(in)
		var out []models.RecipeRunItem
		for i := range es {
			if len(out) >= want {
				break
			}
			if it, why := pictureItem(&es[i].Resource, ""); why == "" {
				out = append(out, it)
			}
		}
		if len(out) == 0 {
			return nil, []string{fmt.Sprintf("The linked folder “%s” has no pictures yet.", f.Name)}
		}
		return out, nil
	}
	// The recipe's own pictures, at the versions its cut pins.
	var out []models.RecipeRunItem
	var problems []string
	for _, b := range bindingsOf(rs.doc, in.Name) {
		if b.Kind != models.BindResource {
			continue
		}
		pin, ok := rs.doc.Cut.Resources[b.Where]
		if !ok {
			continue
		}
		var r models.Resource
		if err := rs.a.DB.GetByID(rs.ctx, store.CResources, rs.a.WorkspaceID(), pin.ID, &r); err != nil {
			problems = append(problems, fmt.Sprintf("The picture “%s” the recipe uses is gone.", b.Where))
			continue
		}
		it, why := pictureItem(&r, pin.SHA256)
		if why != "" {
			problems = append(problems, why)
			continue
		}
		out = append(out, it)
	}
	return out, problems
}

// pictureItem is a picture at a version ("" its newest), or why it can't be one.
func pictureItem(r *models.Resource, sha string) (models.RecipeRunItem, string) {
	title := r.Title
	if title == "" {
		title = r.Name
	}
	if r.Kind != editspec.ResourceKindImage && r.Kind != editspec.ResourceKindSVG {
		return models.RecipeRunItem{}, fmt.Sprintf("“%s” isn't a picture.", title)
	}
	if r.Removed && sha == "" {
		return models.RecipeRunItem{}, fmt.Sprintf("The picture “%s” was removed from the library.", title)
	}
	v := r.Latest()
	if sha != "" {
		v = r.Version(sha)
	}
	if v == nil {
		return models.RecipeRunItem{}, fmt.Sprintf("The picture “%s” has no file yet.", title)
	}
	n := 0
	for i := range r.Versions {
		if r.Versions[i].SHA256 == v.SHA256 {
			n = i + 1
		}
	}
	return models.RecipeRunItem{ID: r.ID, Title: title, SHA256: v.SHA256, Version: n}, ""
}

// countWanted is how many clips or pictures an input takes from a source.
func countWanted(in models.RecipeInput) int {
	if in.Count != nil && in.Count.Max > 0 {
		return int(in.Count.Max)
	}
	if n := len(in.Default.Items); n > 0 {
		return n
	}
	return 1
}

// sameAsDefault reports whether a value is the recipe's default.
func sameAsDefault(in models.RecipeInput, v *models.RecipeRunInput) bool {
	switch in.Kind {
	case models.RecipeWords, models.RecipeChoice:
		return v.Text == in.Default.Text
	case models.RecipeVoice:
		return len(v.Items) == 1 && len(in.Default.Items) == 1 && v.Items[0].ID == in.Default.Items[0]
	}
	if len(v.Items) != len(in.Default.Items) {
		return false
	}
	for i := range v.Items {
		if v.Items[i].ID != in.Default.Items[i] {
			return false
		}
	}
	return true
}

// sameValue reports whether two runs gave an input the same value.
func sameValue(a, b *models.RecipeRunInput) bool {
	if a.Text != b.Text || len(a.Items) != len(b.Items) {
		return false
	}
	for i := range a.Items {
		if itemKey(a.Items[i]) != itemKey(b.Items[i]) {
			return false
		}
	}
	return true
}

// inputLabel names an input for people ("the headline").
func inputLabel(in models.RecipeInput) string {
	if l := strings.TrimSpace(in.Label); l != "" {
		if strings.HasPrefix(l, "The ") {
			return "the " + l[4:]
		}
		return l
	}
	return "the input “" + in.Name + "”"
}

func itemsWords(items []models.RecipeRunItem, what string) string {
	if len(items) == 0 {
		return "none"
	}
	var names []string
	for i, it := range items {
		if i == 3 {
			names = append(names, fmt.Sprintf("%d more", len(items)-3))
			break
		}
		names = append(names, "“"+shorten(it.Title, 40)+"”")
	}
	return plural(len(items), what) + ": " + joinAnd(names)
}

func quoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "“" + x + "”"
	}
	return out
}

// requireProject loads a project a recipe uses, refusing one that is gone
// or archived.
func requireProject(ctx context.Context, a *app.App, id, what string) (*models.Project, error) {
	var p models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), id, &p); err != nil {
		return nil, apperr.New(apperr.NotFound, "The project %s was cleared out.", what).WithHint("Save the recipe again from another project.")
	}
	if p.Archived {
		return nil, apperr.New(apperr.Conflict, "The project %s, “%s”, is archived.", what, p.Name).WithHint("Restore it first (Archived projects).").
			WithCommand("metarush project restore " + p.ID)
	}
	return &p, nil
}

package recipes

import (
	"context"
	"fmt"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Pictures from a repository path (D216): a pictures input whose values come
// from a folder (or file) a GitHub repository keeps pictures from — art kept
// in git. A run takes the pictures the repository kept there at the commit it
// shows, as many as the input takes, each file in the place it had before (a
// file's newer art takes its own place; a place whose file went away takes
// the next picture in path order), and records that commit and each picture's
// version (its file's hash). The bindings put them where any pictures input
// goes — a template's picture parameter (resource:<name>) or a Remotion
// picture setting (pictures:<clip>.<NAME>, D209). A newer commit reaches runs
// once the repository is refreshed (`metarush resources refresh`), and running
// again rebinds only the pictures that changed.

// repoPicked is what a repository path gave a pictures input.
type repoPicked struct {
	items        []models.RecipeRunItem
	repo, commit string
	// words say where they came from ("from “art/characters” in acme/widget,
	// as it was on 2 Oct 2026").
	words string
}

// repoOfInput loads the repository a pictures input takes its pictures from,
// as a project sees it; why says plainly what is wrong.
func repoOfInput(ctx context.Context, a *app.App, project string, view *projectlinks.View, src *models.RecipeSource) (*models.Resource, string) {
	var r models.Resource
	if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), src.Repo, &r); err != nil || r.Kind != models.ResourceRepo {
		return nil, "The repository a pictures input takes its pictures from is gone."
	}
	switch {
	case r.Removed:
		return nil, fmt.Sprintf("The repository “%s” was removed from the library; bring it back to take its pictures.", r.Title)
	case r.Scope == models.ScopeWorkspace, r.ProjectID == project:
	case view != nil:
		if from, ok := view.From(projectlinks.ResourceItem(&r)); !ok || from == "" {
			return nil, fmt.Sprintf("The repository “%s” isn't one the run's project can use: link its project first.", r.Title)
		}
	default:
		return nil, fmt.Sprintf("The repository “%s” isn't one the run's project can use: link its project first.", r.Title)
	}
	return &r, ""
}

// repoPlace says a path in a repository in a few words: “art/characters” in
// acme/widget, or acme/widget for the whole of it.
func repoPlace(r *models.Resource, dir string) string {
	if dir == "" || dir == resources.WholeRepo {
		return r.Title
	}
	return "“" + dir + "” in " + r.Title
}

// repoPictures resolves a pictures input from a repository path: the pictures
// the repository kept there at the commit it shows, as many as the input
// takes, each file in its place (pictures a person removed from the library
// left out).
func (rs *resolver) repoPictures(in models.RecipeInput) (*repoPicked, []string) {
	label := upperFirst(inputLabel(in))
	r, why := repoOfInput(rs.ctx, rs.a, rs.project, rs.view, in.From)
	if why != "" {
		return nil, []string{why}
	}
	dir, err := resources.CleanPicturePath(in.From.Path)
	if err != nil {
		return nil, []string{fmt.Sprintf("%s takes pictures from a path that isn't one a repository has.", label)}
	}
	v := r.Latest()
	if v == nil || v.Repo == nil {
		return nil, []string{fmt.Sprintf("The repository “%s” has no commit yet.", r.Title)}
	}
	if resources.PicturePathOf(dirOrAll(dir), r.PicturePaths) == "" && !coversAny(dir, r.PicturePaths) {
		what := "the pictures in “" + dir + "”"
		if dir == resources.WholeRepo {
			what = "its pictures"
		}
		return nil, []string{fmt.Sprintf("The repository “%s” doesn't keep %s: keep them first, in the repository's details on the Resources tab.", r.Title, what)}
	}
	pics := resources.KeptPictures(r, v, dir)
	when := v.Repo.CommittedAt.UTC().Format("2 Jan 2006")
	if len(pics) == 0 {
		return nil, []string{fmt.Sprintf("%s has no pictures from %s as it was on %s.", label, repoPlace(r, dir), when)}
	}
	idsList := make([]string, 0, len(pics))
	for _, p := range pics {
		idsList = append(idsList, p.ResourceID)
	}
	var list []models.Resource
	if err := rs.a.DB.Find(rs.ctx, store.CResources, rs.a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idsList}}, &list); err != nil {
		return nil, []string{"MetaRush couldn't read the repository's pictures."}
	}
	byID := map[string]*models.Resource{}
	for i := range list {
		byID[list[i].ID] = &list[i]
	}
	// Each file keeps the place it had (the run this one repeats, else the
	// saved video): a file's newer art takes its own place; places whose file
	// went away take the other pictures in path order.
	usable := map[string]models.RecipeRunItem{}
	var order []string
	for _, p := range pics {
		pr := byID[p.ResourceID]
		if pr == nil || pr.Removed {
			continue
		}
		it, why := pictureItem(pr, p.SHA256)
		if why != "" {
			continue
		}
		it.Path = p.Path
		usable[pr.ID] = it
		order = append(order, pr.ID)
	}
	if len(order) == 0 {
		return nil, []string{fmt.Sprintf("%s has no pictures from %s that are still in the library.", label, repoPlace(r, dir))}
	}
	prefer := in.Default.Items
	if p := rs.prev[in.Name]; p != nil {
		prefer = nil
		for _, it := range p.Items {
			prefer = append(prefer, it.ID)
		}
	}
	out := &repoPicked{repo: r.ID, commit: v.Repo.Commit, words: "from " + repoPlace(r, dir) + ", as it was on " + when}
	out.items = placePictures(order, usable, prefer, countWanted(in))
	return out, nil
}

// placePictures fills an input's places: place i keeps the picture prefer[i]
// had when it is still there; the empty places take the others in order.
func placePictures(order []string, usable map[string]models.RecipeRunItem, prefer []string, want int) []models.RecipeRunItem {
	if want > len(order) {
		want = len(order)
	}
	slots := make([]string, want)
	used := map[string]bool{}
	for i := 0; i < want && i < len(prefer); i++ {
		if _, ok := usable[prefer[i]]; ok && !used[prefer[i]] {
			slots[i], used[prefer[i]] = prefer[i], true
		}
	}
	next := 0
	for i := range slots {
		if slots[i] != "" {
			continue
		}
		for next < len(order) && used[order[next]] {
			next++
		}
		if next < len(order) {
			slots[i], used[order[next]] = order[next], true
		}
	}
	out := make([]models.RecipeRunItem, 0, want)
	for _, id := range slots {
		if id != "" {
			out = append(out, usable[id])
		}
	}
	return out
}

// dirOrAll is a picture path to look a kept path up with ("" is the whole repository).
func dirOrAll(dir string) string {
	if dir == "" {
		return resources.WholeRepo
	}
	return dir
}

// coversAny reports whether a path holds one the repository keeps pictures
// from (an input may take a folder that holds several of them).
func coversAny(dir string, paths []string) bool {
	for _, p := range paths {
		if resources.UnderPicturePath(p, dir) {
			return true
		}
	}
	return false
}

// repoSourceWords says where a repository input takes its pictures from, for
// the recipe's page and its readable form.
func repoSourceWords(ctx context.Context, a *app.App, src *models.RecipeSource) string {
	var r models.Resource
	if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), src.Repo, &r) != nil {
		return "the pictures of a repository that is gone"
	}
	dir, err := resources.CleanPicturePath(src.Path)
	if err != nil {
		dir = strings.TrimSpace(src.Path)
	}
	return "the pictures from " + repoPlace(&r, dir) + " at the commit it shows"
}

// repoPicturesShape is what a document's own shape says about inputs taking
// pictures from a repository (no database).
func repoPicturesShape(in models.RecipeInput) []string {
	f := in.From
	if f == nil || f.Repo == "" {
		return nil
	}
	var out []string
	if in.Kind != models.RecipePictures {
		out = append(out, fmt.Sprintf("The input %q takes pictures from a repository, but it is %s.", in.Name, kindWords(in.Kind)))
	}
	if f.Folder != "" || f.Collection != "" || f.Project != "" {
		out = append(out, fmt.Sprintf("The input %q takes its values from a repository and from somewhere else: say one.", in.Name))
	}
	if _, err := resources.CleanPicturePath(f.Path); err != nil {
		out = append(out, fmt.Sprintf("The input %q takes pictures from a path that isn't one a repository has.", in.Name))
	}
	return out
}

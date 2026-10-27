package recipes

import (
	"archive/zip"
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// Starter recipes (D293): MetaRush ships recipes people can start from — "a
// call becomes an episode with shorts" and "a release tag becomes a release
// video" — as recipe files (metarush.recipe-file/1, D219) kept in the
// repository under starters/<name>/ (recipe.json, the sheet, files/<sha256>,
// and starter.json saying what it is for people) and embedded in the binary.
// Adding them packs each into a recipe file and brings it in through
// recipe.import's strict reader, so a starter comes in exactly as a file from
// another workspace does: a draft — never current by itself; a person makes it
// current — with its example footage and pictures in one project ("Starter
// recipes"). A newer MetaRush's starter shares its lineage and comes in as a
// new draft of the same recipe; the version already here brings nothing.

//go:embed starters
var startersFS embed.FS

// startersDir is where the starters are in the embedded files.
const startersDir = "starters"

// StarterProject is the project that keeps what the starters come with.
const StarterProject = "Starter recipes"

// StarterInfo is a starter's starter.json: what it is, in people's words.
type StarterInfo struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Makes   string `json:"makes"`
	Summary string `json:"summary"`
	// Gives says what a run is given; Uses names the first-party abilities
	// (metarush/<id>, D269) its runs use.
	Gives []string `json:"gives"`
	Uses  []string `json:"uses"`
	Order int      `json:"order"`
}

// RecipeStarterRow is one starter recipe and whether this workspace has it.
type RecipeStarterRow struct {
	StarterInfo
	// Version is the starter's version in this MetaRush; Here the recipe it
	// came in as (nil: not added yet); Newer: this MetaRush has a newer version
	// than the newest one here.
	Version int        `json:"version"`
	Here    *RecipeRow `json:"here,omitempty"`
	Newer   bool       `json:"newer,omitempty"`
	// State: new (not here), here, or newer.
	State string `json:"state" jsonschema:"enum=new,enum=here,enum=newer"`
	Words string `json:"words"`
}

// RecipeStartersIn lists the starter recipes.
type RecipeStartersIn struct{}

// RecipeStartersOut lists the starter recipes.
type RecipeStartersOut struct {
	Starters []RecipeStarterRow `json:"starters"`
	Summary  string             `json:"summary"`
}

// RecipeStartersAddIn adds starter recipes (all, or those named) as drafts.
type RecipeStartersAddIn struct {
	Only []string `json:"only,omitempty" jsonschema:"maxItems=10,description=Only these starters (their short names; default: all of them)."`
}

// RecipeStarterAdded is what adding one starter did.
type RecipeStarterAdded struct {
	Name string `json:"name"`
	// New: a new recipe; Same: that version was here already (nothing came in);
	// otherwise a new draft of the recipe it came in as before.
	New     bool              `json:"new"`
	Same    bool              `json:"same"`
	Recipe  *RecipeRow        `json:"recipe,omitempty"`
	Version *RecipeVersionRow `json:"version,omitempty"`
	URL     string            `json:"url,omitempty"`
	Message string            `json:"message"`
}

// RecipeStartersAddOut says what came in.
type RecipeStartersAddOut struct {
	Added   []RecipeStarterAdded `json:"added"`
	Project string               `json:"project,omitempty"`
	Message string               `json:"message"`
	Next    string               `json:"next,omitempty"`
}

func init() {
	ops.Def("recipe.starters", "List the starter recipes MetaRush comes with (a call becomes an episode with shorts; a release tag becomes a release video): what each makes, what a run gives it, the abilities it uses, and whether this workspace has it", ops.Flags{}, listStarters)
	ops.Def("recipe.starters.add", "Add MetaRush's starter recipes (all, or those named) to the workspace as drafts, with their example footage and pictures in the project “Starter recipes”; a person makes each current before it runs", ops.Flags{Mutating: true}, addStarters)
}

// starter is one embedded starter: its info and its recipe file's description.
type starter struct {
	info StarterInfo
	file RecipeFile
}

// starters reads the embedded starters, in their order.
func starters() ([]starter, error) {
	ents, err := fs.ReadDir(startersFS, startersDir)
	if err != nil {
		return nil, err
	}
	var out []starter
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		var s starter
		if err := readEmbeddedJSON(path.Join(startersDir, e.Name(), "starter.json"), &s.info); err != nil {
			return nil, err
		}
		if err := readEmbeddedJSON(path.Join(startersDir, e.Name(), recipeJSON), &s.file); err != nil {
			return nil, err
		}
		if s.info.Name != e.Name() || s.file.Format != RecipeFileFormat {
			return nil, fmt.Errorf("recipes: the starter %q is malformed", e.Name())
		}
		out = append(out, s)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].info.Order < out[j].info.Order })
	return out, nil
}

func readEmbeddedJSON(name string, v any) error {
	b, err := startersFS.ReadFile(name)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

// starterHere finds the recipe a starter came in as: one whose versions share
// its lineage. newer: none of them is the starter's own version.
func starterHere(ctx context.Context, a *app.App, f *RecipeFile) (r *models.Recipe, newer bool, err error) {
	lineage := append([]string{}, f.Lineage...)
	if len(lineage) == 0 || lineage[len(lineage)-1] != f.Version.Hash {
		lineage = append(lineage, f.Version.Hash)
	}
	var vs []models.RecipeVersion
	if err := a.DB.Find(ctx, store.CRecipeVersions, a.WorkspaceID(), bson.M{"$or": bson.A{bson.M{"hash": bson.M{"$in": lineage}},
		bson.M{"imported.from": bson.M{"$in": lineage}}, bson.M{"imported.lineage": bson.M{"$in": lineage}}}}, &vs); err != nil {
		return nil, false, err
	}
	if len(vs) == 0 {
		return nil, false, nil
	}
	newer = true
	pick := ""
	for _, v := range vs {
		if v.Hash == f.Version.Hash || (v.Imported != nil && v.Imported.From == f.Version.Hash) {
			newer, pick = false, v.RecipeID
		}
	}
	if pick == "" {
		pick = vs[len(vs)-1].RecipeID
	}
	var rec models.Recipe
	if err := a.DB.GetByID(ctx, store.CRecipes, a.WorkspaceID(), pick, &rec); err != nil {
		return nil, false, nil
	}
	return &rec, newer, nil
}

func listStarters(ctx context.Context, a *app.App, _ *RecipeStartersIn) (*RecipeStartersOut, error) {
	ss, err := starters()
	if err != nil {
		return nil, err
	}
	out := &RecipeStartersOut{Starters: []RecipeStarterRow{}}
	missing := 0
	for i := range ss {
		s := &ss[i]
		row := RecipeStarterRow{StarterInfo: s.info, Version: s.file.Version.Number, State: "new"}
		if row.Gives == nil {
			row.Gives = []string{}
		}
		if row.Uses == nil {
			row.Uses = []string{}
		}
		r, newer, err := starterHere(ctx, a, &s.file)
		if err != nil {
			return nil, err
		}
		switch {
		case r == nil:
			missing++
			row.Words = "Not added yet: adding it makes a draft a person makes current."
		case newer:
			v, _ := loadVersion(ctx, a, r, "")
			rr := recipeRow(ctx, a, r, v)
			row.Here, row.Newer, row.State = &rr, true, "newer"
			row.Words = fmt.Sprintf("Here as “%s”; this MetaRush has a newer version of it (adding it makes a draft).", r.Title)
		default:
			v, _ := loadVersion(ctx, a, r, "")
			rr := recipeRow(ctx, a, r, v)
			row.Here, row.State = &rr, "here"
			row.Words = fmt.Sprintf("Here as “%s”.", r.Title)
			if r.Current == "" {
				row.Words += " A person makes it current before it runs."
			}
		}
		out.Starters = append(out.Starters, row)
	}
	switch {
	case missing == len(out.Starters):
		out.Summary = fmt.Sprintf("MetaRush comes with %s; add them to start from one (each comes in as a draft).", plural(len(out.Starters), "starter recipe"))
	case missing > 0:
		out.Summary = fmt.Sprintf("%s of %d isn't added yet.", upperFirst(plural(missing, "starter recipe")), len(out.Starters))
	default:
		out.Summary = "Every starter recipe is here."
	}
	return out, nil
}

func addStarters(ctx context.Context, a *app.App, in *RecipeStartersAddIn) (*RecipeStartersAddOut, error) {
	ss, err := starters()
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, n := range in.Only {
		if n = strings.TrimSpace(n); n != "" {
			want[n] = true
		}
	}
	for n := range want {
		found := false
		for _, s := range ss {
			found = found || s.info.Name == n
		}
		if !found {
			return nil, apperr.New(apperr.NotFound, "MetaRush has no starter recipe called “%s”.", n).WithCommand("metarush recipe starters")
		}
	}
	home, err := starterHome(ctx, a)
	if err != nil {
		return nil, err
	}
	out := &RecipeStartersAddOut{Added: []RecipeStarterAdded{}, Project: home.Name}
	came := 0
	for _, s := range ss {
		if len(want) > 0 && !want[s.info.Name] {
			continue
		}
		res, err := addStarter(ctx, a, s, home.ID)
		if err != nil {
			return nil, err
		}
		if !res.Same {
			came++
		}
		out.Added = append(out.Added, *res)
	}
	switch {
	case came == 0:
		out.Message = "The starter recipes are here already; nothing new came in."
	default:
		out.Message = fmt.Sprintf("Added %s as drafts, with their examples in the project “%s”.", plural(came, "starter recipe"), home.Name)
		out.Next = "Open each one, look at it, and make it current to run it."
	}
	a.Audit(ctx, "recipe.starters.add", "recipe", "", home.ID, map[string]any{"added": came, "only": in.Only})
	return out, nil
}

// starterHome is the project that keeps what the starters come with (made the first time).
func starterHome(ctx context.Context, a *app.App) (*models.Project, error) {
	var ps []models.Project
	if err := a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), bson.M{"name": StarterProject, "archived": bson.M{"$ne": true}}, &ps); err != nil {
		return nil, err
	}
	if len(ps) > 0 {
		return &ps[0], nil
	}
	po, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: StarterProject}, ops.Caller{})
	if err != nil {
		return nil, err
	}
	return &po.Project, nil
}

// addStarter packs one starter into a recipe file and brings it in (a draft).
func addStarter(ctx context.Context, a *app.App, s starter, projectID string) (*RecipeStarterAdded, error) {
	tmp, err := os.CreateTemp(a.Cfg.Scratch.Dir, "starter-*.recipe.zip")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if err := packStarter(tmp, s.info.Name); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	imp, err := importRecipe(ctx, a, &RecipeImportIn{File: tmp.Name(), FileName: ExportFile(s.info.Name), ProjectID: projectID,
		Note: shorten(fmt.Sprintf("MetaRush's starter recipe “%s”, version %d", s.info.Title, s.file.Version.Number), 300)})
	if err != nil {
		return nil, err
	}
	res := &RecipeStarterAdded{Name: s.info.Name, New: imp.New, Same: imp.Same, Recipe: imp.Recipe, Version: imp.Version, URL: imp.URL}
	switch {
	case imp.Same:
		res.Message = fmt.Sprintf("“%s” is here already.", s.info.Title)
	case imp.New:
		res.Message = fmt.Sprintf("“%s” came in as a draft.", s.info.Title)
	default:
		res.Message = fmt.Sprintf("A newer “%s” came in as a draft of the recipe it came in as before.", s.info.Title)
	}
	return res, nil
}

// packStarter writes a starter's recipe file (a zip of its recipe.json, its
// sheet and its files) to w.
func packStarter(w io.Writer, name string) error {
	root := path.Join(startersDir, name)
	zw := zip.NewWriter(w)
	put := func(entry, src string) error {
		b, err := startersFS.ReadFile(src)
		if err != nil {
			return err
		}
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: entry, Method: zip.Deflate})
		if err != nil {
			return err
		}
		_, err = fw.Write(b)
		return err
	}
	if err := put(recipeJSON, path.Join(root, recipeJSON)); err != nil {
		return err
	}
	if _, err := fs.Stat(startersFS, path.Join(root, SheetFile(name))); err == nil {
		if err := put(SheetFile(name), path.Join(root, SheetFile(name))); err != nil {
			return err
		}
	}
	files, err := fs.ReadDir(startersFS, path.Join(root, "files"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	for _, f := range files {
		if f.IsDir() || !hexSHA(f.Name()) {
			continue
		}
		if err := put("files/"+f.Name(), path.Join(root, "files", f.Name())); err != nil {
			return err
		}
	}
	return zw.Close()
}

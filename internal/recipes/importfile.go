package recipes

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/remotion"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Bringing a recipe file in (D219): the file another workspace exported is
// read strictly — only recipe.json, a sheet and files/<sha256>, each file the
// size its description says and, when it is used, the SHA-256 it is named by
// — and nothing in it is trusted: its words are data (a person here makes the
// version current, D206), and its code waits for a person here. It comes in as
// a draft: a new version of the recipe whose lineage it shares (a version of
// it that came in before, or that it came from), else a new recipe under its
// own name, else the next free one. What it uses comes into one project here
// (named after the recipe, or the one an earlier import of it used):
// pictures as pictures of their own, footage as clips (the provenance it came
// with kept), motion graphics and their libraries kept by hash, unchecked until
// a person checks them here, Remotion versions as new sources whose packages,
// check and approval wait for people. The document is rewritten to what it
// has here; what didn't come along is said plainly.

// RecipeImportIn brings a recipe file in.
type RecipeImportIn struct {
	File string `json:"file" jsonschema:"minLength=1,description=The recipe file (a .recipe.zip another workspace exported)."`
	// ProjectID is the project that keeps what comes with it.
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=The project that keeps what comes with it (default: a new project named after the recipe\\, or the one an earlier import of it used)."`
	// Name brings it in as a new recipe with this short name.
	Name   string `json:"name,omitempty" jsonschema:"pattern=^[a-z][a-z0-9-]{0\\,39}$,description=Bring it in as a new recipe with this short name (default: a new version of the recipe it shares its history with\\, else its own name or the next free one)."`
	Note   string `json:"note,omitempty" jsonschema:"maxLength=300"`
	DryRun bool   `json:"dryRun,omitempty" jsonschema:"description=Say what would come in and what it needs; change nothing."`
	// FileName is the name the file had (the dashboard's upload; default: the file's own).
	FileName string `json:"fileName,omitempty" jsonschema:"maxLength=200"`
}

// RecipeImportOut says what came in and what it waits for.
type RecipeImportOut struct {
	DryRun bool `json:"dryRun"`
	// New: a new recipe; Same: that version was here already.
	New     bool              `json:"new"`
	Same    bool              `json:"same"`
	Recipe  *RecipeRow        `json:"recipe,omitempty"`
	Version *RecipeVersionRow `json:"version,omitempty"`
	// Name and Title are the recipe's here (or would be); From says where it came from.
	Name  string `json:"name"`
	Title string `json:"title"`
	From  string `json:"from"`
	// ProjectID and Project are where what it uses is kept (NewProject: made for it).
	ProjectID  string                 `json:"projectId,omitempty"`
	Project    string                 `json:"project"`
	NewProject bool                   `json:"newProject,omitempty"`
	Pictures   int                    `json:"pictures"`
	Footage    int                    `json:"footage"`
	Code       []RecipeImportCodeView `json:"code"`
	Notes      []string               `json:"notes"`
	Check      *models.RecipeCheck    `json:"check,omitempty"`
	URL        string                 `json:"url,omitempty"`
	Message    string                 `json:"message"`
	Next       string                 `json:"next,omitempty"`
}

// RecipeImportCodeView is one piece of code an imported version came with,
// and what it waits for here.
type RecipeImportCodeView struct {
	Kind  string `json:"kind" jsonschema:"enum=motion,enum=library,enum=remotion"`
	Name  string `json:"name"`
	Hash  string `json:"hash"`
	Short string `json:"short"`
	// Ready: a person here approved these exact bytes.
	Ready bool `json:"ready"`
	// State says where it stands, Next what a person does next, in words;
	// Command is the command that does it (for Details).
	State   string `json:"state"`
	Next    string `json:"next,omitempty"`
	Command string `json:"command,omitempty"`
	// URL is where a person looks at it and approves it in the dashboard.
	URL string `json:"url,omitempty"`
	// Libraries are the shared libraries a graphic is drawn with.
	Libraries map[string]string `json:"libraries,omitempty"`
	// Check is what checks a kept graphic or library here (motion.check's
	// input), while it hasn't been checked in this workspace.
	Check *RecipeImportCheck `json:"check,omitempty"`
}

// RecipeImportCheck is motion.check's input for a graphic a recipe file brought.
type RecipeImportCheck struct {
	Version   string            `json:"version"`
	Libraries map[string]string `json:"libraries,omitempty"`
	ProjectID string            `json:"projectId,omitempty"`
}

func init() {
	ops.Def("recipe.import", "Bring in a recipe file another workspace exported: a draft — a new version of the recipe it shares its history with, else a new recipe — with its pictures, footage and code in one project here; its words are data and its code waits for a person's approval here", ops.Flags{Mutating: true, LocalOnly: true}, importRecipe)
}

// Limits on what a recipe file may hold.
const (
	maxDescription = 32 << 20
	maxRecipeFile  = 256 << 30
	maxEntries     = 200000
)

var blobName = regexp.MustCompile(`^files/[0-9a-f]{64}$`)

// recipeFile is a recipe file being read.
type recipeFile struct {
	zr     *zip.ReadCloser
	file   *RecipeFile
	blobs  map[string]*zip.File
	name   string
	digest string
}

func (rf *recipeFile) Close() {
	if rf != nil && rf.zr != nil {
		_ = rf.zr.Close()
	}
}

// notRecipeFile is the error for a file that isn't a recipe file.
func notRecipeFile(why string) error {
	return apperr.New(apperr.InvalidInput, "That isn't a recipe file MetaRush can bring in: %s.", why).
		WithHint("Export the recipe again in the workspace it comes from (its page, or the Library's Recipes tab).")
}

// openRecipeFile reads a recipe file's description and checks what it holds
// (nothing is unpacked yet).
func openRecipeFile(path string) (*recipeFile, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return nil, notRecipeFile("it isn't a zip file")
	}
	rf := &recipeFile{zr: zr, blobs: map[string]*zip.File{}, name: filepath.Base(path)}
	fail := func(err error) (*recipeFile, error) {
		rf.Close()
		return nil, err
	}
	if len(zr.File) > maxEntries {
		return fail(notRecipeFile("it holds too many files"))
	}
	var desc *zip.File
	sheets := 0
	var total uint64
	for _, f := range zr.File {
		if f.Method != zip.Store && f.Method != zip.Deflate {
			return fail(notRecipeFile("a file in it is packed in a way MetaRush doesn't read"))
		}
		total += f.UncompressedSize64
		switch name := f.Name; {
		case name == "files/" && f.UncompressedSize64 == 0:
			// The folder itself, as a zip tool may write it: nothing in it.
		case name == recipeJSON && desc == nil:
			desc = f
		case strings.HasSuffix(name, ".recipe.md") && !strings.ContainsAny(name, `/\`) && sheets == 0:
			sheets++ // for people to read; the description is what comes in
		case blobName.MatchString(name):
			sha := strings.TrimPrefix(name, "files/")
			if _, dup := rf.blobs[sha]; dup {
				return fail(notRecipeFile("it holds a file twice"))
			}
			rf.blobs[sha] = f
		default:
			return fail(notRecipeFile(fmt.Sprintf("it holds something a recipe file never does (%q)", shorten(name, 60))))
		}
	}
	if desc == nil {
		return fail(notRecipeFile("it has no recipe.json"))
	}
	if total > maxRecipeFile {
		return fail(notRecipeFile("it is larger than MetaRush brings in"))
	}
	if desc.UncompressedSize64 > maxDescription {
		return fail(notRecipeFile("its description is too large"))
	}
	r, err := desc.Open()
	if err != nil {
		return fail(notRecipeFile("its description can't be read"))
	}
	raw, err := io.ReadAll(io.LimitReader(r, maxDescription+1))
	_ = r.Close()
	if err != nil || len(raw) > maxDescription {
		return fail(notRecipeFile("its description can't be read"))
	}
	sum := sha256.Sum256(raw)
	rf.digest = hex.EncodeToString(sum[:])
	var head struct {
		Format string `json:"format"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return fail(notRecipeFile("its description isn't readable"))
	}
	if head.Format != RecipeFileFormat {
		return fail(apperr.New(apperr.InvalidInput, "This recipe file was made by another MetaRush (its format is %q; this one reads %q).", shorten(head.Format, 40), RecipeFileFormat).
			WithHint("Bring it in with the MetaRush that matches, or export it again from there."))
	}
	var file RecipeFile
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&file); err != nil {
		return fail(apperr.Wrap(apperr.InvalidInput, err, "This recipe file's description doesn't read as one (a newer MetaRush may have made it).").WithDetails(map[string]any{"problem": err.Error()}))
	}
	rf.file = &file
	// Every file it lists is there, the size it says; nothing else is.
	listed := map[string]int64{}
	for _, b := range file.Files {
		if !hexSHA(b.SHA256) || b.Size < 0 {
			return fail(notRecipeFile("its list of files is broken"))
		}
		f, ok := rf.blobs[b.SHA256]
		if !ok {
			return fail(notRecipeFile("a file it lists is missing"))
		}
		if f.UncompressedSize64 != uint64(b.Size) {
			return fail(notRecipeFile("a file in it isn't the size its list says"))
		}
		listed[b.SHA256] = b.Size
	}
	if len(listed) != len(rf.blobs) {
		return fail(notRecipeFile("it holds files its list doesn't name"))
	}
	need := func(sha string) bool { _, ok := listed[sha]; return ok }
	for _, p := range file.Pictures {
		if !need(p.SHA256) {
			return fail(notRecipeFile("a picture it describes isn't in it"))
		}
	}
	for _, f := range file.Footage {
		if !need(f.SHA256) {
			return fail(notRecipeFile("a clip it describes isn't in it"))
		}
	}
	for _, c := range file.Code {
		if !need(c.Hash) {
			return fail(notRecipeFile("code it describes isn't in it"))
		}
		for _, f := range c.Files {
			if !need(f) {
				return fail(notRecipeFile("a file of its code isn't in it"))
			}
		}
	}
	if !hexSHA(file.Version.Hash) {
		return fail(notRecipeFile("it doesn't say which version it is"))
	}
	return rf, nil
}

var hexSHARe = regexp.MustCompile(`^[0-9a-f]{64}$`)

func hexSHA(s string) bool { return hexSHARe.MatchString(s) }

// extract unpacks every listed file into dir, checking each one's SHA-256 as
// it comes out; it returns each file's path by SHA-256.
func (rf *recipeFile) extract(dir string) (map[string]string, error) {
	out := map[string]string{}
	for _, b := range rf.file.Files {
		f := rf.blobs[b.SHA256]
		r, err := f.Open()
		if err != nil {
			return nil, notRecipeFile("a file in it can't be read")
		}
		p := filepath.Join(dir, b.SHA256)
		w, err := os.Create(p)
		if err != nil {
			_ = r.Close()
			return nil, err
		}
		h := sha256.New()
		n, err := io.Copy(io.MultiWriter(w, h), io.LimitReader(r, b.Size+1))
		_ = r.Close()
		if cerr := w.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return nil, notRecipeFile("a file in it can't be read")
		}
		if n != b.Size || hex.EncodeToString(h.Sum(nil)) != b.SHA256 {
			return nil, apperr.New(apperr.Integrity, "A file in the recipe file isn't what its name says (it was damaged or changed), so nothing came in.")
		}
		out[b.SHA256] = p
	}
	return out, nil
}

// importTarget is where a file's version goes: a new version of a recipe here
// (its parent the newest version of its history here), the version it is
// already, or a new recipe with a free name.
type importTarget struct {
	recipe  *models.Recipe
	parent  *models.RecipeVersion
	same    *models.RecipeVersion
	project string // where an earlier import of it kept its things
	name    string // a new recipe's short name
}

// findTarget works out where a file's version goes.
func findTarget(ctx context.Context, a *app.App, f *RecipeFile, want string) (*importTarget, error) {
	if want = strings.TrimSpace(want); want != "" {
		if !namePattern.MatchString(want) {
			return nil, apperr.New(apperr.InvalidInput, "A recipe's short name is small letters, digits and dashes (like app-teaser).")
		}
		if _, err := loadRecipe(ctx, a, want); err == nil {
			return nil, apperr.New(apperr.Conflict, "There is already a recipe called “%s” here.", want).
				WithHint("Give another name, or leave the name out to bring it in as a new version of the recipe it shares its history with.")
		}
		return &importTarget{name: want}, nil
	}
	lineage := append([]string{}, f.Lineage...)
	if len(lineage) == 0 || lineage[len(lineage)-1] != f.Version.Hash {
		lineage = append(lineage, f.Version.Hash)
	}
	var vs []models.RecipeVersion
	if err := a.DB.Find(ctx, store.CRecipeVersions, a.WorkspaceID(), bson.M{"$or": bson.A{bson.M{"hash": bson.M{"$in": lineage}},
		bson.M{"imported.from": bson.M{"$in": lineage}}, bson.M{"imported.lineage": bson.M{"$in": lineage}}}}, &vs); err != nil {
		return nil, err
	}
	if len(vs) > 0 {
		// The recipe that shares it: the one with the file's name, else the one most of it matches.
		count := map[string]int{}
		for _, v := range vs {
			count[v.RecipeID]++
		}
		ids := sortedKeys(count)
		best := ""
		for _, id := range ids {
			var r models.Recipe
			if a.DB.GetByID(ctx, store.CRecipes, a.WorkspaceID(), id, &r) == nil && r.Name == f.Recipe.Name {
				best = id
			}
		}
		if best == "" {
			for _, id := range ids {
				if best == "" || count[id] > count[best] {
					best = id
				}
			}
		}
		var r models.Recipe
		if err := a.DB.GetByID(ctx, store.CRecipes, a.WorkspaceID(), best, &r); err != nil {
			return nil, err
		}
		versions, err := listVersions(ctx, a, r.ID)
		if err != nil || len(versions) == 0 {
			return nil, err
		}
		t := &importTarget{recipe: &r}
		is := func(v *models.RecipeVersion, h string) bool {
			return v.Hash == h || (v.Imported != nil && v.Imported.From == h)
		}
		for i := range versions {
			if is(&versions[i], f.Version.Hash) {
				t.same = &versions[i]
				return t, nil
			}
		}
		for i := len(lineage) - 1; i >= 0 && t.parent == nil; i-- {
			for j := range versions {
				if is(&versions[j], lineage[i]) {
					t.parent = &versions[j]
					break
				}
			}
		}
		if t.parent == nil {
			t.parent = &versions[0]
		}
		for _, v := range versions { // newest first
			if v.Imported != nil && v.Imported.ProjectID != "" {
				t.project = v.Imported.ProjectID
				break
			}
		}
		return t, nil
	}
	name := f.Recipe.Name
	if !namePattern.MatchString(name) {
		name = shortName(f.Doc.Title)
	}
	name, err := freeName(ctx, a, name)
	if err != nil {
		return nil, err
	}
	return &importTarget{name: name}, nil
}

// importMaps say what each thing of the file is here.
type importMaps struct {
	pictures map[string]editspec.ResourcePin // ID there → pin here
	shas     map[string]string               // a picture's SHA-256 there → here (an SVG cleaned again)
	assets   map[string]string               // clip there → clip here
	fixed    map[string]bool                 // clips whose file was the fixed copy their cut played
	sources  map[string]*models.RemotionSource
}

func importRecipe(ctx context.Context, a *app.App, in *RecipeImportIn) (*RecipeImportOut, error) {
	path := strings.TrimSpace(in.File)
	if path == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say which recipe file to bring in.")
	}
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() {
		return nil, apperr.New(apperr.NotFound, "There's no recipe file there.")
	}
	rf, err := openRecipeFile(path)
	if err != nil {
		return nil, err
	}
	defer rf.Close()
	if n := strings.TrimSpace(in.FileName); n != "" {
		rf.name = filepath.Base(n)
	}
	f := rf.file
	t, err := findTarget(ctx, a, f, in.Name)
	if err != nil {
		return nil, err
	}
	out := &RecipeImportOut{DryRun: in.DryRun, Title: f.Doc.Title, From: fromWords(f), Code: []RecipeImportCodeView{}, Notes: []string{}}
	if t.recipe != nil {
		out.Name = t.recipe.Name
	} else {
		out.Name, out.New = t.name, true
	}
	if t.same != nil {
		row, vrow := recipeRow(ctx, a, t.recipe, t.same), versionRow(t.same, nil)
		out.Same, out.Recipe, out.Version = true, &row, &vrow
		out.Code = importCodeViews(ctx, a, t.same.Imported)
		out.URL = ops.DashboardURL(a, links.Recipe(t.recipe.Name))
		out.Message = fmt.Sprintf("That version of “%s” is here already: version %d of “%s”. Nothing came in.", f.Recipe.Title, t.same.Number, t.recipe.Title)
		return out, nil
	}
	// Where what it uses is kept.
	var home *models.Project
	switch {
	case strings.TrimSpace(in.ProjectID) != "":
		p, err := ops.ResolveProject(ctx, a, in.ProjectID)
		if err != nil {
			return nil, err
		}
		if p.Archived {
			return nil, apperr.New(apperr.Conflict, "“%s” is archived; restore it first, or bring the recipe in elsewhere.", p.Name)
		}
		home = p
	case t.project != "":
		var p models.Project
		if a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), t.project, &p) == nil && !p.Archived {
			home = &p
		}
	}
	if home != nil {
		out.ProjectID, out.Project = home.ID, home.Name
	} else {
		out.Project, out.NewProject = shorten(f.Doc.Title, 120), true
	}
	out.Pictures, out.Footage = len(f.Pictures), len(f.Footage)
	if in.DryRun {
		for _, c := range f.Code {
			out.Code = append(out.Code, dryCodeView(ctx, a, c))
		}
		out.Notes = append(out.Notes, docNotes(f)...)
		out.Message = dryWords(out, t)
		out.Next = "Bring it in; then a person checks and approves its code here and makes the version current."
		return out, nil
	}
	// Unpack it, checking every file.
	dir, err := os.MkdirTemp(a.Cfg.Scratch.Dir, "recipe-import-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	paths, err := rf.extract(dir)
	if err != nil {
		return nil, err
	}
	if home == nil {
		po, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: out.Project}, ops.Caller{})
		if err != nil {
			return nil, err
		}
		home = &po.Project
		out.ProjectID, out.Project = home.ID, home.Name
	}
	m := &importMaps{pictures: map[string]editspec.ResourcePin{}, shas: map[string]string{}, assets: map[string]string{}, fixed: map[string]bool{}, sources: map[string]*models.RemotionSource{}}
	var notes []string
	imp := &models.RecipeImported{From: f.Version.Hash, Lineage: f.Lineage, Recipe: f.Recipe.Name, Title: shorten(f.Recipe.Title, 120), Number: f.Version.Number, Workspace: shorten(f.Workspace, 120),
		File: rf.name, Digest: rf.digest, ProjectID: home.ID, By: a.Actor, At: time.Now().UTC()}
	if !f.Made.IsZero() {
		made := f.Made.UTC()
		imp.Made = &made
	}
	// Code: kept, never run; each waits for a person here.
	for _, c := range f.Code {
		switch c.Kind {
		case models.ImportedMotion, models.ImportedLibrary:
			b, err := os.ReadFile(paths[c.Hash])
			if err != nil {
				return nil, err
			}
			_, known, err := motion.Keep(ctx, a, b, c.Hash)
			if err != nil {
				notes = append(notes, fmt.Sprintf("The motion graphic %s didn't come in: %s", c.Name, strings.TrimSuffix(apperr.From(err).Message, ".")+"."))
				continue
			}
			imp.Code = append(imp.Code, models.RecipeImportedCode{Kind: c.Kind, Name: c.Name, Hash: c.Hash, Known: known})
		case models.ImportedRemotion:
			manifest, err := os.ReadFile(paths[c.Hash])
			if err != nil {
				return nil, err
			}
			files := map[string]string{}
			for _, sha := range c.Files {
				files[sha] = paths[sha]
			}
			src, v, known, err := remotion.ImportFromFile(ctx, a, remotion.FromFileIn{ProjectID: home.ID, Name: c.Name, Title: c.Title, File: rf.name, Manifest: manifest, Hash: c.Hash, Files: files})
			if err != nil {
				notes = append(notes, fmt.Sprintf("The Remotion project “%s” didn't come in: %s Its clips come in as ordinary footage.", c.Title, strings.TrimSuffix(apperr.From(err).Message, ".")+"."))
				continue
			}
			m.sources[c.Hash] = src
			imp.Code = append(imp.Code, models.RecipeImportedCode{Kind: c.Kind, Name: src.Name, Hash: v.Hash, SourceID: src.ID, Known: known})
		}
	}
	// Pictures: each a picture of its own in the project (the same bytes there stay one).
	for _, p := range f.Pictures {
		src := cleanSource(p.Source)
		res, err := resources.AddFile(ctx, a, paths[p.SHA256], resources.AddOptions{ProjectID: home.ID, Name: resourceName(p.Name), Title: shorten(p.Title, 120), FileName: safeFileName(p.FileName, p.SHA256),
			License: shorten(p.License, 300), Source: src, Fresh: true, Via: a.Actor.Via})
		if err != nil || res.Resource == nil {
			why := "it isn't a picture, SVG, PDF or font MetaRush takes"
			if err != nil {
				why = strings.TrimSuffix(apperr.From(err).Message, ".")
			}
			notes = append(notes, fmt.Sprintf("The picture “%s” didn't come in: %s.", shorten(p.Title, 60), why))
			continue
		}
		m.pictures[p.ID] = editspec.ResourcePin{ID: res.Resource.ID, SHA256: res.SHA256}
		m.shas[p.SHA256] = res.SHA256
	}
	// Footage: clips of the project, keeping where they came from.
	for _, ft := range f.Footage {
		src := importedSource(ft.Source, m)
		r := ingest.File(ctx, a, paths[ft.SHA256], ingest.Options{FootageOnly: true, ProjectID: home.ID, DisplayName: safeFileName(ft.FileName, ft.SHA256), Title: shorten(ft.Title, 200),
			Tags: ft.Tags, Source: src, Duplicates: ingest.DuplicatesKeep, Via: a.Actor.Via})
		if r.AssetID == "" || r.Status == "failed" || r.Status == "skipped" {
			why := strings.TrimSuffix(strings.TrimSpace(r.Error), ".")
			if why == "" {
				why = "it isn't video or sound MetaRush can read"
			}
			notes = append(notes, fmt.Sprintf("The clip “%s” didn't come in: %s.", shorten(ft.Title, 60), why))
			continue
		}
		m.assets[ft.ID] = r.AssetID
		if ft.Fixed {
			m.fixed[ft.ID] = true
		}
	}
	// The document, as it is here.
	doc := Clone(&f.Doc)
	notes = append(notes, rewriteDoc(doc, f, m, home.ID)...)
	for _, lo := range f.LeftOut {
		// The file's own words (where it was made), quoted as theirs.
		notes = append(notes, fmt.Sprintf("The file says it left out “%s”: “%s”.", strings.Trim(shorten(lo.What, 80), "“”"), strings.TrimSuffix(shorten(lo.Why, 200), ".")))
	}
	imp.Notes = notes
	r := t.recipe
	parent := ""
	if r == nil {
		if r, err = newRecipe(ctx, a, t.name, doc.Title, home.ID); err != nil {
			return nil, err
		}
	} else {
		parent = t.parent.Hash
	}
	chk := checkDoc(ctx, a, doc)
	note := strings.TrimSpace(in.Note)
	if note == "" {
		note = shorten("Brought in from "+out.From, 300)
	}
	v, made, err := saveVersionWith(ctx, a, r, doc, parent, note, chk, imp)
	if err != nil {
		if out.New {
			_, _ = a.DB.C(store.CRecipes).DeleteOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()}) // never a recipe without a version
		}
		return nil, err
	}
	if !made {
		out.Same = true
	}
	a.Audit(ctx, "recipe.import", "recipe", r.ID, home.ID, map[string]any{"version": v.Number, "new": out.New, "from": f.Version.Hash, "file": rf.name, "pictures": len(m.pictures),
		"footage": len(m.assets), "code": len(imp.Code), "notes": len(notes)})
	row, vrow := recipeRow(ctx, a, r, v), versionRow(v, nil)
	out.Recipe, out.Version, out.Name, out.Check = &row, &vrow, r.Name, chk
	out.Pictures, out.Footage = len(m.pictures), len(m.assets)
	out.Code = importCodeViews(ctx, a, imp)
	out.Notes = notes
	out.URL = ops.DashboardURL(a, links.Recipe(r.Name))
	out.Message = importWords(out, r, v)
	out.Next = "A person checks and approves its code here (the recipe's page lists each piece), then makes the version current."
	if waiting(out.Code) == 0 {
		out.Next = "A person looks at the draft and makes it current."
	}
	return out, nil
}

// RecipeImportView is where a version brought in from a recipe file came
// from, and what its code waits for here (D219).
type RecipeImportView struct {
	// From says where it came from, in words; File the file's name.
	From      string                 `json:"from"`
	File      string                 `json:"file,omitempty"`
	At        time.Time              `json:"at"`
	By        string                 `json:"by"`
	ProjectID string                 `json:"projectId,omitempty"`
	Project   string                 `json:"project,omitempty"`
	Code      []RecipeImportCodeView `json:"code"`
	// Waiting counts the pieces of code still waiting for a person here.
	Waiting int      `json:"waiting"`
	Notes   []string `json:"notes"`
	Summary string   `json:"summary"`
}

// importView is an imported version's view (nil for one made here).
func importView(ctx context.Context, a *app.App, v *models.RecipeVersion) *RecipeImportView {
	im := v.Imported
	if im == nil {
		return nil
	}
	name := im.Title // people know a recipe by its title
	if name == "" {
		name = im.Recipe
	}
	from := fmt.Sprintf("version %d of “%s”", im.Number, name)
	if im.Workspace != "" {
		from += " in the workspace “" + im.Workspace + "”"
	} else {
		from += " in another workspace"
	}
	by := im.By.UserName
	if by == "" {
		by = "someone"
	}
	out := &RecipeImportView{From: from, File: im.File, At: im.At, By: by, ProjectID: im.ProjectID, Code: importCodeViews(ctx, a, im), Notes: append([]string{}, im.Notes...)}
	var p models.Project
	if im.ProjectID != "" && a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), im.ProjectID, &p) == nil {
		out.Project = p.Name
	}
	out.Waiting = waiting(out.Code)
	out.Summary = "Brought in from " + from + "."
	if out.Waiting > 0 {
		out.Summary += fmt.Sprintf(" %s waits for a person's approval here before it runs; its words are another workspace's until a person here makes it current.", upperFirst(plural(out.Waiting, "piece")+" of code"))
	} else {
		out.Summary += " Its words are another workspace's until a person here makes it current."
	}
	return out
}

// fromWords says where a file's version came from: “version 3 of “App teaser” in Acme”.
func fromWords(f *RecipeFile) string {
	s := fmt.Sprintf("version %d of “%s”", f.Version.Number, shorten(f.Recipe.Title, 80))
	if w := strings.TrimSpace(f.Workspace); w != "" {
		s += " in the workspace “" + shorten(w, 60) + "”"
	} else {
		s += " in another workspace"
	}
	return s
}

func waiting(cs []RecipeImportCodeView) int {
	n := 0
	for _, c := range cs {
		if !c.Ready {
			n++
		}
	}
	return n
}

// importWords says what came in.
func importWords(out *RecipeImportOut, r *models.Recipe, v *models.RecipeVersion) string {
	what := fmt.Sprintf("%s, %s and %s", plural(out.Pictures, "picture"), plural(out.Footage, "clip"), plural(len(out.Code), "piece")+" of code")
	var s string
	switch {
	case out.Same:
		s = fmt.Sprintf("Brought in %s: it is version %d of “%s” already.", out.From, v.Number, r.Title)
	case out.New:
		s = fmt.Sprintf("Brought in %s as a new recipe, “%s”, a draft (version %d), with %s, kept in the project “%s”.", out.From, r.Title, v.Number, what, out.Project)
	default:
		s = fmt.Sprintf("Brought in %s as draft version %d of “%s” (they share their history), with %s, kept in the project “%s”.", out.From, v.Number, r.Title, what, out.Project)
	}
	if n := waiting(out.Code); n > 0 {
		s += fmt.Sprintf(" %s waits for a person's approval here before it runs.", upperFirst(plural(n, "piece")+" of code"))
	}
	if n := len(out.Notes); n > 0 {
		s += fmt.Sprintf(" %s on the way in.", upperFirst(plural(n, "note")))
	}
	return s
}

// dryWords says what a file would bring.
func dryWords(out *RecipeImportOut, t *importTarget) string {
	where := "a new recipe, “" + out.Title + "” (" + out.Name + ")"
	if t.recipe != nil {
		where = "a new draft of “" + t.recipe.Title + "” (they share their history)"
	}
	proj := "a new project, “" + out.Project + "”"
	if !out.NewProject {
		proj = "the project “" + out.Project + "”"
	}
	return fmt.Sprintf("The file holds %s: it would come in as %s with %s, %s and %s, kept in %s. Nothing came in yet.", out.From, where,
		plural(out.Pictures, "picture"), plural(out.Footage, "clip"), plural(len(out.Code), "piece")+" of code", proj)
}

// resourceName is a picture's short name suggestion (taken: the next free one).
func resourceName(n string) string {
	if resources.NamePattern.MatchString(n) {
		return n
	}
	return ""
}

// safeFileName is a file's name from a file's description: its last part,
// plain characters only (else named by its hash).
func safeFileName(n, sha string) string {
	n = filepath.Base(strings.TrimSpace(n))
	if n == "." || n == "/" || n == "" || len(n) > 200 || strings.ContainsAny(n, "\x00/\\") {
		return sha[:12]
	}
	for _, r := range n {
		if r < 0x20 || r == 0x7f {
			return sha[:12]
		}
	}
	return n
}

// importedSource is where a clip came from, as it is here: a generated shot
// stays flagged as generated (its record stays where it was made); a clip made
// from a Remotion project points at the source it came in as (when it came in).
func importedSource(s *models.AssetSource, m *importMaps) *models.AssetSource {
	if s == nil {
		return nil
	}
	cp := *s
	if cp.Generated != nil {
		g := *cp.Generated
		g.GenerationID = ""
		cp.Generated = &g
	}
	if cp.Remotion != nil {
		rc := *cp.Remotion
		src := m.sources[rc.Version]
		if src == nil {
			return nil // its Remotion project didn't come in: plain footage (a note says so)
		}
		rc.SourceID, rc.SourceName, rc.SourceTitle, rc.RenderKey, rc.SoundOf = src.ID, src.Name, src.Title, "", ""
		var own []models.RemotionPictureValue
		for _, p := range rc.OwnPictures {
			if pin, ok := m.pictures[p.ResourceID]; ok {
				p.ResourceID, p.SHA256 = pin.ID, pin.SHA256
				own = append(own, p)
			}
		}
		rc.OwnPictures = own
		cp.Remotion = &rc
	}
	return &cp
}

// docNotes says, before anything comes in, what a file's document needs here.
func docNotes(f *RecipeFile) []string {
	var out []string
	if f.Doc.Settings != "" {
		out = append(out, "Its runs follow this workspace's default Content Settings (the brand kit, narration voice and house rules of the workspace it came from stay there); its cut keeps its own look.")
	}
	for _, in := range f.Doc.Inputs {
		if in.From != nil && (in.From.Folder != "" || in.From.Collection != "" || in.From.Project != "" || in.From.Repo != "") {
			out = append(out, fmt.Sprintf("%s took its values from a folder, collection, project or repository there; a run here gives them.", upperFirst(inputLabel(in))))
		}
	}
	if len(f.Doc.Cut.Conversations) > 0 {
		out = append(out, "It plays a conversation from calls, which doesn't come along: the check says what is missing.")
	}
	for _, b := range f.Doc.Bindings {
		if p, ok := ParseBinding(b.To); ok && p.Kind == models.BindPrompt {
			out = append(out, fmt.Sprintf("The generated shot “%s” comes in as footage; it is made again from new words only where it was generated.", p.Where))
		}
	}
	return out
}

// rewriteDoc makes a file's document what it is here: the pictures, clips and
// sources that came in, its runs' place, and nothing that names what stayed
// in the other workspace. It returns what it had to change, plainly.
func rewriteDoc(d *models.RecipeDoc, f *RecipeFile, m *importMaps, home string) []string {
	notes := docNotes(f)
	// Where runs go and whose settings they follow.
	if d.Lands.Kind == models.RecipeLandsProject {
		d.Lands.ProjectID = home
	}
	d.Settings = ""
	d.From = &models.RecipeFrom{ProjectID: home}
	// Pictures the cut pins.
	for name, pin := range d.Cut.Resources {
		if np, ok := m.pictures[pin.ID]; ok {
			sha := pin.SHA256
			if s, ok := m.shas[sha]; ok {
				sha = s
			}
			if sha == "" {
				sha = np.SHA256
			}
			d.Cut.Resources[name] = editspec.ResourcePin{ID: np.ID, SHA256: sha}
		} else {
			notes = append(notes, fmt.Sprintf("The picture “%s” the cut shows didn't come along.", name))
		}
	}
	// Inputs: their own items here; sources elsewhere taken away.
	for i := range d.Inputs {
		in := &d.Inputs[i]
		var items []string
		lost := 0
		for _, id := range in.Default.Items {
			switch {
			case strings.HasPrefix(id, "res_"):
				if p, ok := m.pictures[id]; ok {
					items = append(items, p.ID)
				} else {
					lost++
				}
			case strings.HasPrefix(id, "ast_"):
				if c, ok := m.assets[id]; ok {
					items = append(items, c)
				} else {
					lost++
				}
			default:
				items = append(items, id) // a voice
			}
		}
		in.Default.Items = items
		if lost > 0 && len(items) == 0 && (in.Kind == models.RecipeFootage || in.Kind == models.RecipePictures) {
			in.Required = true
			notes = append(notes, fmt.Sprintf("%s has none of its own here: every run gives it.", upperFirst(inputLabel(*in))))
		}
		if fr := in.From; fr != nil {
			drive, dropbox := fr.Drive, fr.Dropbox
			if fr.Folder != "" || fr.Collection != "" || fr.Project != "" || fr.Repo != "" || fr.Path != "" {
				if drive == "" && dropbox == "" && len(in.Default.Items) == 0 {
					in.Required = true
				}
			}
			in.From = nil
			switch {
			case drive != "":
				in.From = &models.RecipeSource{Drive: drive} // a Google Drive folder: the same address, through this computer's rclone (D220)
			case dropbox != "":
				in.From = &models.RecipeSource{Dropbox: dropbox} // a Dropbox folder, the same way (D306)
			}
		}
	}
	// Clips, sounds and narration: the ones that came in.
	for si := range d.Sections {
		s := &d.Sections[si]
		var keep []editspec.Clip
		for _, c := range s.Clips {
			if id, ok := m.assets[c.AssetID]; ok {
				c.AssetID = id
				keep = append(keep, c)
				continue
			}
			if s.Kind == models.RecipeChosen && strings.HasPrefix(c.AssetID, "ast_") {
				continue // a moment of footage that stayed there: runs choose again
			}
			if strings.HasPrefix(c.AssetID, "ast_") {
				notes = append(notes, fmt.Sprintf("The clip “%s” of the section “%s” didn't come along.", c.ID, s.Name))
			}
			keep = append(keep, c)
		}
		s.Clips = keep
	}
	var audio []editspec.AudioTrack
	for _, tr := range d.Cut.Audio {
		if id, ok := m.assets[tr.AssetID]; ok {
			tr.AssetID = id
			audio = append(audio, tr)
		} else {
			notes = append(notes, fmt.Sprintf("The sound “%s” didn't come along.", tr.ID))
		}
	}
	d.Cut.Audio = audio
	if vo := d.Cut.Voiceover; vo != nil {
		for i := range vo.Beats {
			b := &vo.Beats[i]
			if b.Take == nil {
				continue
			}
			id, ok := m.assets[b.Take.AssetID]
			if !ok {
				b.Take, b.State = nil, "draft"
				continue
			}
			b.Take.AssetID, b.Take.TakeID = id, ""
			if p := b.Take.Processed; p != nil {
				if pid, ok := m.assets[p.AssetID]; ok {
					p.AssetID = pid
				} else {
					b.Take.Processed, b.Take.UseProcessed = nil, false
				}
			}
		}
		if c := vo.Continuous; c != nil {
			if id, ok := m.assets[c.AssetID]; ok {
				c.AssetID = id
				if c.Processed != nil {
					if pid, ok := m.assets[c.Processed.AssetID]; ok {
						c.Processed.AssetID = pid
					} else {
						c.Processed = nil
					}
				}
			} else {
				vo.Continuous, vo.Layout = nil, ""
				notes = append(notes, "The narration's one long recording didn't come along: the lines are read again.")
			}
		}
	}
	if mx := d.Cut.Mix; mx != nil {
		// A clearer voice set for a whole recording follows the clip it came in as.
		var voices []editspec.VoiceSound
		for _, v := range mx.Voices {
			if v.AssetID != "" {
				id, ok := m.assets[v.AssetID]
				if !ok {
					continue
				}
				v.AssetID = id
			}
			voices = append(voices, v)
		}
		mx.Voices = voices
		var keep []editspec.EnhancedSound
		for _, e := range mx.Enhanced {
			if id, ok := m.assets[e.AssetID]; ok {
				if v, ok := m.assets[e.Variant]; ok {
					e.AssetID, e.Variant = id, v
					keep = append(keep, e)
				}
			}
		}
		mx.Enhanced = keep
	}
	// A fixed copy came in as the clip itself.
	if len(d.Cut.Fixed) > 0 {
		fixed := map[string]editspec.FixedPin{}
		for id, pin := range d.Cut.Fixed {
			if nid, ok := m.assets[id]; ok && !m.fixed[id] {
				fixed[nid] = pin
			}
		}
		d.Cut.Fixed = fixed
		if len(fixed) == 0 {
			d.Cut.Fixed = nil
		}
	}
	// Generated shots aren't made again here.
	var bindings []models.RecipeBinding
	for _, b := range d.Bindings {
		if p, ok := ParseBinding(b.To); ok && p.Kind == models.BindPrompt {
			continue
		}
		bindings = append(bindings, b)
	}
	d.Bindings = bindings
	sort.Strings(notes)
	return dedupe(notes)
}

func dedupe(xs []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

// dryCodeView is what a piece of code would be here, before it comes in.
func dryCodeView(ctx context.Context, a *app.App, c RecipeFileCode) RecipeImportCodeView {
	imp := &models.RecipeImported{Code: []models.RecipeImportedCode{{Kind: c.Kind, Name: c.Name, Hash: c.Hash}}}
	views := importCodeViews(ctx, a, imp)
	v := views[0]
	if c.Kind == models.ImportedRemotion && !v.Ready && v.State == "" {
		v.State = "comes in as a new Remotion project here, waiting for people"
	}
	return v
}

// importCodeViews says where each piece of code an imported version came with
// stands here, and what a person does next.
func importCodeViews(ctx context.Context, a *app.App, imp *models.RecipeImported) []RecipeImportCodeView {
	out := []RecipeImportCodeView{}
	if imp == nil {
		return out
	}
	libs := map[string]string{}
	for _, c := range imp.Code {
		if c.Kind == models.ImportedLibrary {
			libs[c.Name] = c.Hash
		}
	}
	for _, c := range imp.Code {
		v := RecipeImportCodeView{Kind: c.Kind, Name: c.Name, Hash: c.Hash, Short: models.ShortHash(c.Hash)}
		switch c.Kind {
		case models.ImportedMotion, models.ImportedLibrary:
			what := "The motion graphic " + c.Name
			if c.Kind == models.ImportedLibrary {
				what = "The shared library " + c.Name
			}
			ref := c.Name + "@" + motion.Short(c.Hash)
			comp, err := motion.Get(ctx, a.DB, a.WorkspaceID(), c.Hash)
			switch {
			case errors.Is(err, store.ErrNotFound) || err != nil:
				v.State = "kept, not checked here yet"
				v.Next = what + " is checked here first (its test frames drawn in the jailed browser), then a person approves it."
				v.Command = "metarush motion check --version " + c.Name + "@" + c.Hash + " --project " + imp.ProjectID
				v.Check = &RecipeImportCheck{Version: c.Name + "@" + c.Hash, ProjectID: imp.ProjectID}
				if c.Kind == models.ImportedMotion {
					for _, l := range sortedKeys(libs) {
						v.Command += " --library " + l + "@" + libs[l]
					}
					if len(libs) > 0 {
						v.Check.Libraries = libs
					}
				}
			case comp.State == models.MotionApproved:
				v.Ready, v.State = true, "approved here"
			case comp.State == models.MotionWaiting:
				v.State = "checked here, waiting for a person's approval"
				v.Next = "A person looks at its test frames and approves it."
				v.Command = "metarush motion approve " + ref
			case comp.State == models.MotionRejected:
				v.State = "turned down here"
			case comp.State == models.MotionRevoked:
				v.State = "its approval was withdrawn here"
			}
			v.URL = ops.DashboardURL(a, links.Motion(c.Hash))
			if c.Kind == models.ImportedMotion {
				v.Libraries = libs
			}
		case models.ImportedRemotion:
			var rv models.RemotionVersion
			if err := a.DB.GetByID(ctx, store.CRemotionVersions, a.WorkspaceID(), a.WorkspaceID()+"/"+c.Hash, &rv); err != nil {
				break
			}
			ref := c.Name + "@" + models.ShortHash(c.Hash)
			switch {
			case rv.State == models.RemotionApproved:
				v.Ready, v.State = true, "approved here"
			case !rv.PackagesAllowed:
				v.State = "its packages wait for a person"
				v.Next = "A person allows the packages it uses; then it is checked here and a person approves it."
				v.Command = "metarush remotion allow " + ref
			case rv.Read == nil || !rv.Read.OK:
				v.State = "waiting for its check here"
				v.Next = "It is checked here (read and drawn in the jailed browser); then a person approves it."
				v.Command = "metarush remotion check --version " + ref
			case rv.State == models.RemotionWaiting:
				v.State = "checked here, waiting for a person's approval"
				v.Next = "A person looks at its stills and approves it."
				v.Command = "metarush remotion approve " + ref
			case rv.State == models.RemotionRejected:
				v.State = "turned down here"
			case rv.State == models.RemotionRevoked:
				v.State = "its approval was withdrawn here"
			}
			if rv.SourceID != "" {
				v.URL = ops.DashboardURL(a, links.Remotion(rv.ProjectID, rv.SourceID, rv.Hash))
			}
		}
		out = append(out, v)
	}
	return out
}

package recipes

import (
	"archive/zip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/remotion"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/version"
)

// Exporting a recipe (D219): one version of a recipe as a file another
// workspace can bring in — a zip holding recipe.json (the format, the
// version's lineage, its document and what each file is), the version's sheet
// for people to read, and files/<sha256>: the bytes of the pictures, fonts and
// other resources it pins, the footage its cut plays (the fixed copy where the
// cut pins one) and the approved code it uses by hash — motion graphics and
// their shared libraries (their canonical archives), Remotion versions (their
// manifests and files). Nothing in the file is trusted where it comes in: its
// words are data and its code waits for a person there (import.go).

// RecipeFileFormat is the format of a recipe file.
const RecipeFileFormat = "metarush.recipe-file/1"

// RecipeFile is recipe.json, the description of a recipe file.
type RecipeFile struct {
	Format string    `json:"format" jsonschema:"enum=metarush.recipe-file/1"`
	Made   time.Time `json:"made"`
	// MetaRush is the version of MetaRush that made it; Workspace the name of
	// the workspace it comes from.
	MetaRush  string `json:"metarush"`
	Workspace string `json:"workspace,omitempty"`
	Recipe    struct {
		Name  string `json:"name"`
		Title string `json:"title"`
		Makes string `json:"makes,omitempty"`
	} `json:"recipe"`
	Version struct {
		Number int    `json:"number"`
		Hash   string `json:"hash"`
		Note   string `json:"note,omitempty"`
		State  string `json:"state"`
	} `json:"version"`
	// Lineage is every version this one descends from, oldest first, this one
	// last (versions a workspace imported carry where they came from).
	Lineage  []string            `json:"lineage"`
	Doc      models.RecipeDoc    `json:"doc"`
	Pictures []RecipeFilePicture `json:"pictures"`
	Footage  []RecipeFileFootage `json:"footage"`
	Code     []RecipeFileCode    `json:"code"`
	LeftOut  []RecipeFileLeftOut `json:"leftOut"`
	// Files are every file under files/, by SHA-256.
	Files []RecipeFileBlob `json:"files"`
}

// RecipeFilePicture is a resource the recipe pins or takes by default.
type RecipeFilePicture struct {
	// ID is its ID where the file was made (the document names it).
	ID       string `json:"id"`
	Name     string `json:"name"`
	Title    string `json:"title"`
	Kind     string `json:"kind"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	FileName string `json:"fileName"`
	License  string `json:"license,omitempty"`
	// Source is where it came from there (its words).
	Source models.ResourceSource `json:"source"`
}

// RecipeFileFootage is a clip or sound the recipe's cut plays.
type RecipeFileFootage struct {
	ID       string  `json:"id"`
	Title    string  `json:"title"`
	FileName string  `json:"fileName"`
	Kind     string  `json:"kind"`
	SHA256   string  `json:"sha256"`
	Size     int64   `json:"size"`
	Duration float64 `json:"duration,omitempty"`
	// Fixed: the file is the fixed copy the cut plays (fixes applied).
	Fixed bool     `json:"fixed,omitempty"`
	Tags  []string `json:"tags,omitempty"`
	// Source is where it came from there (a site, a generated shot, a Remotion project).
	Source *models.AssetSource `json:"source,omitempty"`
	// Input: footage only an input's default plays (it varies each run).
	Input bool `json:"input,omitempty"`
}

// RecipeFileCode is approved code the recipe uses, by hash, with its files.
type RecipeFileCode struct {
	Kind string `json:"kind" jsonschema:"enum=motion,enum=library,enum=remotion"`
	Name string `json:"name"`
	// Title is a Remotion project's; Number the version's number there.
	Title  string `json:"title,omitempty"`
	Number int    `json:"number,omitempty"`
	Hash   string `json:"hash"`
	// Libraries are the shared libraries a graphic is drawn with (name → version).
	Libraries map[string]string `json:"libraries,omitempty"`
	// Files are a Remotion version's files (their SHA-256s): its manifest is files/<hash>.
	Files []string `json:"files,omitempty"`
}

// RecipeFileLeftOut is something the recipe uses that the file doesn't hold, and why.
type RecipeFileLeftOut struct {
	What string `json:"what"`
	Why  string `json:"why"`
}

// RecipeFileBlob is one file under files/.
type RecipeFileBlob struct {
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// RecipeExportIn exports a version of a recipe.
type RecipeExportIn struct {
	Recipe  string `json:"recipe" jsonschema:"minLength=1,description=Its short name\\, title or ID."`
	Version string `json:"version,omitempty" jsonschema:"description=A version (default: the current one\\, else the newest draft)."`
	// Out is the file to write (default <name>.recipe.zip in this folder).
	Out string `json:"out,omitempty" jsonschema:"description=The file to write (default: <name>.recipe.zip here)."`
	// MaxMB is the most the file may hold (default 2048): footage an input
	// gives by default is left out first, the largest first.
	MaxMB int `json:"maxMb,omitempty" jsonschema:"minimum=0,maximum=65536,description=The most the file may hold in MB (default 2048); footage inputs give by default is left out first."`
	// Force writes over a file that is already there.
	Force bool `json:"force,omitempty"`
}

// RecipeExportOut says what the file holds.
type RecipeExportOut struct {
	File     string              `json:"file"`
	Bytes    int64               `json:"bytes"`
	Recipe   string              `json:"recipe"`
	Title    string              `json:"title"`
	Version  int                 `json:"version"`
	Pictures int                 `json:"pictures"`
	Footage  int                 `json:"footage"`
	Code     []RecipeFileCode    `json:"code"`
	LeftOut  []RecipeFileLeftOut `json:"leftOut"`
	Message  string              `json:"message"`
	Next     string              `json:"next,omitempty"`
}

// ExportFile is the file name a recipe is exported to.
func ExportFile(name string) string { return name + ".recipe.zip" }

func init() {
	ops.Def("recipe.export", "Export a version of a recipe to a file another workspace can bring in: its document and sheet, the pictures it pins, the footage its cut plays and the approved code it uses by hash (motion graphics, shared libraries, Remotion versions)", ops.Flags{LocalOnly: true},
		func(ctx context.Context, a *app.App, in *RecipeExportIn) (*RecipeExportOut, error) {
			out := strings.TrimSpace(in.Out)
			r, err := loadRecipe(ctx, a, in.Recipe)
			if err != nil {
				return nil, err
			}
			if out == "" {
				out = ExportFile(r.Name)
			}
			if _, err := os.Stat(out); err == nil && !in.Force {
				return nil, apperr.New(apperr.Conflict, "%s is already there; nothing was written.", filepath.Base(out)).WithHint("Write over it with force, or give another file.")
			}
			dir := filepath.Dir(out)
			tmp, err := os.CreateTemp(dir, ".recipe-export-")
			if err != nil {
				return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush can't write a file there.")
			}
			defer os.Remove(tmp.Name())
			res, err := Export(ctx, a, in, tmp)
			if cerr := tmp.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				return nil, err
			}
			if err := os.Rename(tmp.Name(), out); err != nil {
				return nil, err
			}
			if st, err := os.Stat(out); err == nil {
				res.Bytes = st.Size()
			}
			res.File = out
			return res, nil
		})
}

// exportItem is one file the zip holds.
type exportItem struct {
	path string
	size int64
	data []byte // small files made here (a manifest)
}

// exporter gathers what a recipe file holds.
type exporter struct {
	ctx   context.Context
	a     *app.App
	file  *RecipeFile
	items map[string]exportItem
	total int64
	max   int64
}

// Export writes a version of a recipe as a recipe file (a zip) to w.
func Export(ctx context.Context, a *app.App, in *RecipeExportIn, w io.Writer) (*RecipeExportOut, error) {
	r, err := loadRecipe(ctx, a, in.Recipe)
	if err != nil {
		return nil, err
	}
	v, err := loadVersion(ctx, a, r, in.Version)
	if err != nil {
		return nil, err
	}
	max := int64(in.MaxMB) << 20
	if max <= 0 {
		max = 2048 << 20
	}
	e := &exporter{ctx: ctx, a: a, items: map[string]exportItem{}, max: max, file: &RecipeFile{Format: RecipeFileFormat, Made: time.Now().UTC(), MetaRush: version.Current().Version,
		Lineage: lineageOf(ctx, a, v), Doc: *Clone(&v.Doc), Pictures: []RecipeFilePicture{}, Footage: []RecipeFileFootage{}, Code: []RecipeFileCode{}, LeftOut: []RecipeFileLeftOut{}, Files: []RecipeFileBlob{}}}
	var ws models.Workspace
	if a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws) == nil {
		e.file.Workspace = ws.Name
	}
	e.file.Recipe.Name, e.file.Recipe.Title, e.file.Recipe.Makes = r.Name, r.Title, r.Makes
	e.file.Version.Number, e.file.Version.Hash, e.file.Version.Note, e.file.Version.State = v.Number, v.Hash, v.Note, v.State
	d := &e.file.Doc
	if err := e.pictures(d); err != nil {
		return nil, err
	}
	fixed, input := footageOf(d)
	if err := e.code(d, append(append([]string{}, fixed...), input...)); err != nil {
		return nil, err
	}
	if err := e.footage(d, fixed, false); err != nil {
		return nil, err
	}
	if err := e.footage(d, input, true); err != nil {
		return nil, err
	}
	if e.total > e.max {
		return nil, apperr.New(apperr.InvalidInput, "What “%s” always uses is %s, more than the %s a recipe file may hold.", r.Title, sizeWords(e.total), sizeWords(e.max)).
			WithHint("Give it more room (the most it may hold, in MB).")
	}
	sheet := printVersion(ctx, a, r, v)
	if err := e.write(w, r.Name, sheet); err != nil {
		return nil, err
	}
	out := &RecipeExportOut{Recipe: r.Name, Title: r.Title, Version: v.Number, Pictures: len(e.file.Pictures), Footage: len(e.file.Footage), Code: e.file.Code, LeftOut: e.file.LeftOut}
	out.Message = fmt.Sprintf("Exported version %d of “%s”: %s, %s and %s.", v.Number, r.Title, plural(out.Pictures, "picture"), plural(out.Footage, "clip"), codeWords(e.file.Code))
	if n := len(out.LeftOut); n > 0 {
		out.Message += fmt.Sprintf(" %s didn't come along (see why).", upperFirst(plural(n, "thing")))
	}
	out.Next = "Bring it into another workspace there (Import a recipe… on the Library's Recipes tab): its code waits for a person's approval there."
	a.Audit(ctx, "recipe.export", "recipe", r.ID, r.ProjectID, map[string]any{"version": v.Number, "pictures": out.Pictures, "footage": out.Footage, "code": len(out.Code), "leftOut": len(out.LeftOut)})
	return out, nil
}

// codeWords says what code a file holds ("2 pieces of code that wait for a person's OK there", "no code").
func codeWords(cs []RecipeFileCode) string {
	if len(cs) == 0 {
		return "no code"
	}
	return plural(len(cs), "piece") + " of code (each waits for a person's approval where it is brought in)"
}

func sizeWords(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/float64(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%d MB", n>>20)
	}
	return fmt.Sprintf("%d KB", (n+1023)>>10)
}

// lineageOf is every version a version descends from, oldest first, itself
// last; a version imported from elsewhere brings the lineage it came with.
func lineageOf(ctx context.Context, a *app.App, v *models.RecipeVersion) []string {
	var chain []models.RecipeVersion
	seen := map[string]bool{}
	for x := v; x != nil && !seen[x.Hash] && len(chain) < 1000; {
		seen[x.Hash] = true
		chain = append(chain, *x)
		if x.Parent == "" {
			break
		}
		var p models.RecipeVersion
		if a.DB.GetByID(ctx, store.CRecipeVersions, a.WorkspaceID(), versionID(a.WorkspaceID(), x.Parent), &p) != nil {
			break
		}
		x = &p
	}
	var out []string
	added := map[string]bool{}
	add := func(h string) {
		if h != "" && !added[h] {
			added[h] = true
			out = append(out, h)
		}
	}
	for i := len(chain) - 1; i >= 0; i-- {
		if im := chain[i].Imported; im != nil {
			for _, h := range im.Lineage {
				add(h)
			}
			add(im.From)
		}
		add(chain[i].Hash)
	}
	return out
}

// add puts a file in the zip (once per SHA-256).
func (e *exporter) add(sha, path string, size int64, data []byte) {
	if _, ok := e.items[sha]; ok {
		return
	}
	e.items[sha] = exportItem{path: path, size: size, data: data}
	e.total += size
	e.file.Files = append(e.file.Files, RecipeFileBlob{SHA256: sha, Size: size})
}

// kept says why a client's Content Settings keep a file out of a recipe
// file, which is made to leave for another workspace (What may leave,
// delivery, D290); the refusal is recorded. "" when it may go.
func (e *exporter) kept(projects, kinds []string, what string) string {
	s := leave.Send{Place: models.PlaceDelivery, To: "file", ToWords: "a recipe file", Kinds: kinds, Projects: projects, What: what + " in a recipe file"}
	r, err := leave.Problem(e.ctx, e.a, s)
	if err != nil || r == nil {
		return ""
	}
	leave.Record(e.ctx, e.a, r, s)
	return strings.TrimSuffix(lowerFirst(r.Message), ".")
}

func (e *exporter) leave(what, why string) {
	e.file.LeftOut = append(e.file.LeftOut, RecipeFileLeftOut{What: what, Why: why})
}

// pictures adds the resources the recipe pins, and those its pictures inputs take by default.
func (e *exporter) pictures(d *models.RecipeDoc) error {
	type want struct{ id, sha string }
	var wants []want
	pinned := map[string]string{}
	for _, name := range sortedKeys(d.Cut.Resources) {
		p := d.Cut.Resources[name]
		wants = append(wants, want{p.ID, p.SHA256})
		pinned[p.ID] = p.SHA256
	}
	for _, in := range d.Inputs {
		if in.Kind != models.RecipePictures {
			continue
		}
		for _, id := range in.Default.Items {
			wants = append(wants, want{id, pinned[id]})
		}
	}
	done := map[string]bool{}
	for _, w := range wants {
		var r models.Resource
		if err := e.a.DB.GetByID(e.ctx, store.CResources, e.a.WorkspaceID(), w.id, &r); err != nil {
			e.leave("a picture the recipe uses", "it is no longer in this workspace")
			continue
		}
		title := r.Title
		if title == "" {
			title = r.Name
		}
		if r.Kind == models.ResourceRepo {
			continue // a repository is never drawn itself
		}
		if why := e.kept([]string{r.ProjectID}, []string{models.LeavePictures}, "“"+title+"”"); why != "" {
			e.leave("“"+title+"”", why)
			continue
		}
		p, ver, err := resources.LocalFile(e.ctx, e.a, &r, w.sha, 0)
		if err != nil || ver == nil {
			why := "MetaRush couldn't read its file here"
			if err != nil {
				why = strings.TrimSuffix(apperr.From(err).Message, ".")
			}
			e.leave("“"+title+"”", why)
			continue
		}
		if done[r.ID+"@"+ver.SHA256] {
			continue
		}
		done[r.ID+"@"+ver.SHA256] = true
		fileName := ver.Source.Name
		if fileName == "" {
			fileName = r.Name + "." + ver.Ext
		}
		e.file.Pictures = append(e.file.Pictures, RecipeFilePicture{ID: r.ID, Name: r.Name, Title: title, Kind: r.Kind, SHA256: ver.SHA256, Size: ver.Size,
			FileName: filepath.Base(fileName), License: r.License, Source: cleanSource(ver.Source)})
		e.add(ver.SHA256, p, ver.Size, nil)
	}
	return nil
}

// cleanSource keeps where a picture came from without what only its own
// workspace knows (folders, projects, machines, records).
func cleanSource(s models.ResourceSource) models.ResourceSource {
	s.Path, s.Machine, s.FolderID, s.RelPath, s.ProjectID, s.ResourceID, s.AssetID = "", "", "", "", "", "", ""
	for i := range s.Lineage {
		s.Lineage[i].ResourceID = ""
	}
	return s
}

// footageOf lists the footage a recipe's cut plays: the parts that stay the
// same every run (fixed sections, music and sounds, narration, clearer
// voices), then what only inputs give by default (moments of chosen sections,
// footage inputs' own clips). Conversations from calls and other projects'
// cuts are left out (they aren't files).
func footageOf(d *models.RecipeDoc) (fixed, input []string) {
	seen := map[string]bool{}
	add := func(list *[]string, id string) {
		if strings.HasPrefix(id, "ast_") && !seen[id] {
			seen[id] = true
			*list = append(*list, id)
		}
	}
	conv := map[string]bool{}
	for _, c := range d.Cut.Conversations {
		for _, m := range c.Members {
			conv[m.AssetID] = true
		}
	}
	for _, s := range d.Sections {
		for _, c := range s.Clips {
			if s.Kind == models.RecipeFixed {
				add(&fixed, c.AssetID)
			}
		}
	}
	s := cutSpec(d)
	s.Clips = nil
	for _, id := range editspec.AssetIDs(s) {
		if !conv[id] {
			add(&fixed, id)
		}
	}
	for _, sec := range d.Sections {
		if sec.Kind == models.RecipeChosen {
			for _, c := range sec.Clips {
				add(&input, c.AssetID)
			}
		}
	}
	for _, in := range d.Inputs {
		if in.Kind == models.RecipeFootage {
			for _, id := range in.Default.Items {
				add(&input, id)
			}
		}
	}
	return fixed, input
}

// footage adds clips the cut plays; input footage is left out, the largest
// first, when it doesn't fit.
func (e *exporter) footage(d *models.RecipeDoc, list []string, input bool) error {
	type clip struct {
		f    RecipeFileFootage
		path string
	}
	var clips []clip
	for _, id := range list {
		var as models.Asset
		if err := e.a.DB.GetByID(e.ctx, store.CAssets, e.a.WorkspaceID(), id, &as); err != nil {
			e.leave("a clip the recipe uses", "it is no longer in this workspace")
			continue
		}
		title := as.DisplayTitle()
		if as.Source != nil && as.Source.Generated != nil && as.Source.Generated.Likeness {
			e.leave("“"+title+"”", "it is a generated shot that shows a real person, whose consent covers projects in this workspace only")
			continue
		}
		if as.State != models.AssetAvailable {
			e.leave("“"+title+"”", "it isn't ready yet")
			continue
		}
		whose := as.ProjectIDs
		if len(whose) == 0 {
			whose = []string{""}
		}
		if why := e.kept(whose, leave.FootageKinds, "“"+title+"”"); why != "" {
			e.leave("“"+title+"”", why)
			continue
		}
		key, sha, size, fixedCopy := as.StorageKey, as.SHA256, as.Size, false
		if fp := d.Cut.Fixed[id]; fp.SHA256 != "" {
			var cp models.SourceCopy
			if e.a.DB.C(store.CSourceCopies).FindOne(e.ctx, map[string]any{"workspaceId": e.a.WorkspaceID(), "assetId": id, "sha256": fp.SHA256, "state": "ready"}).Decode(&cp) == nil && cp.StorageKey != "" {
				key, sha, size, fixedCopy = cp.StorageKey, cp.SHA256, cp.Size, true
			}
		}
		p, err := e.a.Cache.Ensure(e.ctx, e.a.Media, key, sha, size, nil)
		if err != nil {
			e.leave("“"+title+"”", "MetaRush couldn't read its file here")
			continue
		}
		src := as.Source
		if src != nil {
			cp := *src
			if cp.Generated != nil {
				g := *cp.Generated
				g.GenerationID = "" // the record stays where it was made
				cp.Generated = &g
			}
			src = &cp
		}
		clips = append(clips, clip{path: p, f: RecipeFileFootage{ID: as.ID, Title: title, FileName: filepath.Base(as.Name), Kind: as.Kind, SHA256: sha, Size: size,
			Duration: as.Media.Duration, Fixed: fixedCopy, Tags: as.Tags, Source: src, Input: input}})
	}
	if input {
		sort.SliceStable(clips, func(i, j int) bool { return clips[i].f.Size < clips[j].f.Size })
	}
	for _, c := range clips {
		if input && e.total+c.f.Size > e.max {
			e.leave("“"+c.f.Title+"”", fmt.Sprintf("it is footage an input gives by default, and the file would be larger than %s with it (a run there gives footage of its own)", sizeWords(e.max)))
			continue
		}
		e.file.Footage = append(e.file.Footage, c.f)
		e.add(c.f.SHA256, c.path, c.f.Size, nil)
	}
	return nil
}

// code adds the approved code the recipe uses by hash: its graphics' archives
// and their libraries, and the Remotion versions its clips are made from.
func (e *exporter) code(d *models.RecipeDoc, assets []string) error {
	ws := e.a.WorkspaceID()
	addMotion := func(kind, name, hash string, libs map[string]string) {
		for _, c := range e.file.Code {
			if c.Hash == hash {
				return
			}
		}
		c, err := motion.Get(e.ctx, e.a.DB, ws, hash)
		if err != nil {
			e.leave("the motion graphic "+name, "this workspace has no check of that version")
			return
		}
		if c.State != models.MotionApproved {
			e.leave("the motion graphic "+name, "no person here approved that version")
			return
		}
		p, err := motion.ArchivePath(e.ctx, e.a, c)
		if err != nil {
			e.leave("the motion graphic "+name, "MetaRush couldn't read its code here")
			return
		}
		e.file.Code = append(e.file.Code, RecipeFileCode{Kind: kind, Name: name, Hash: hash, Libraries: libs})
		e.add(hash, p, c.Archive.Size, nil)
	}
	for _, o := range d.Cut.Overlays {
		if o.Motion == nil {
			continue
		}
		for _, l := range sortedKeys(o.Motion.Libs) {
			addMotion(models.ImportedLibrary, l, o.Motion.Libs[l], nil)
		}
		addMotion(models.ImportedMotion, o.Motion.Name, o.Motion.Hash, o.Motion.Libs)
	}
	done := map[string]bool{}
	for _, id := range assets {
		var as models.Asset
		if e.a.DB.GetByID(e.ctx, store.CAssets, ws, id, &as) != nil || !as.Source.IsRemotion() {
			continue
		}
		hash := as.Source.Remotion.Version
		if done[hash] {
			continue
		}
		done[hash] = true
		fv, err := remotion.ExportVersion(e.ctx, e.a, hash)
		if err != nil {
			e.leave("the Remotion project “"+as.Source.Remotion.SourceTitle+"”", "MetaRush couldn't read that version here")
			continue
		}
		c := RecipeFileCode{Kind: models.ImportedRemotion, Name: fv.SourceName, Title: fv.SourceTitle, Number: fv.Number, Hash: fv.Hash}
		for _, f := range fv.Files {
			p, err := remotion.ObjectPath(e.ctx, e.a, f.SHA256, f.Size)
			if err != nil {
				return err
			}
			e.add(f.SHA256, p, f.Size, nil)
			c.Files = append(c.Files, f.SHA256)
		}
		e.add(fv.Hash, "", int64(len(fv.Manifest)), fv.Manifest)
		e.file.Code = append(e.file.Code, c)
	}
	return nil
}

// write writes the zip: recipe.json, the sheet, then every file by its SHA-256.
func (e *exporter) write(w io.Writer, name, sheet string) error {
	zw := zip.NewWriter(w)
	sort.Slice(e.file.Files, func(i, j int) bool { return e.file.Files[i].SHA256 < e.file.Files[j].SHA256 })
	manifest, err := json.MarshalIndent(e.file, "", "  ")
	if err != nil {
		return err
	}
	now := e.file.Made
	put := func(entry string, method uint16, body func(io.Writer) error) error {
		fw, err := zw.CreateHeader(&zip.FileHeader{Name: entry, Method: method, Modified: now})
		if err != nil {
			return err
		}
		return body(fw)
	}
	if err := put(recipeJSON, zip.Deflate, func(fw io.Writer) error { _, err := fw.Write(manifest); return err }); err != nil {
		return err
	}
	if err := put(SheetFile(name), zip.Deflate, func(fw io.Writer) error { _, err := io.WriteString(fw, sheet); return err }); err != nil {
		return err
	}
	for _, b := range e.file.Files {
		it := e.items[b.SHA256]
		method := zip.Store
		if b.Size < 1<<20 {
			method = zip.Deflate
		}
		if err := put("files/"+b.SHA256, method, func(fw io.Writer) error {
			if it.data != nil {
				_, err := fw.Write(it.data)
				return err
			}
			f, err := os.Open(it.path)
			if err != nil {
				return err
			}
			defer f.Close()
			n, err := io.Copy(fw, f)
			if err == nil && n != b.Size {
				err = fmt.Errorf("a file changed while it was exported")
			}
			return err
		}); err != nil {
			return err
		}
	}
	return zw.Close()
}

// recipeJSON is the description's name in a recipe file.
const recipeJSON = "recipe.json"

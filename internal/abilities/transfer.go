package abilities

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/version"
)

// Ability files (D292): a version of an ability as a file another workspace
// can bring in, like Recipes' files (D219). The zip holds ability.json
// (metarush.ability-file/1: the version, its lineage and its approval record
// there — information only), archive.json (the canonical archive whose
// SHA-256 is the version) and, for people to read, ability.yaml and notes.md.
// Bringing one in reads it strictly and records a draft: imported, its test
// not run here, never approved and never set up — its code waits for a
// person to check it here and for an owner to approve it.

// FileFormat is the format of an ability file.
const FileFormat = "metarush.ability-file/1"

// The files an ability file holds.
const (
	fileDoc     = "ability.json"
	fileArchive = "archive.json"
)

// maxArchiveFile caps archive.json: as large as the biggest archive export
// writes (text is kept as JSON text, where a control character takes six
// bytes; other files as base64, a third larger).
const maxArchiveFile = MaxTotalBytes*6 + 1<<20

// AbilityFileDoc is ability.json, the description of an ability file.
type AbilityFileDoc struct {
	Format   string    `json:"format" jsonschema:"enum=metarush.ability-file/1"`
	Made     time.Time `json:"made"`
	MetaRush string    `json:"metarush"`
	// Workspace is the name of the workspace it comes from.
	Workspace string `json:"workspace,omitempty"`
	Ability   struct {
		Name  string `json:"name"`
		Title string `json:"title"`
		Does  string `json:"does"`
	} `json:"ability"`
	Version struct {
		Number int    `json:"number"`
		Hash   string `json:"hash"`
		State  string `json:"state"`
		Size   int64  `json:"size"`
	} `json:"version"`
	// Lineage is every version this one descends from, oldest first, this
	// one last (versions a workspace brought in carry where they came from).
	Lineage []string `json:"lineage"`
	// Approval is the approval record where it was made: information only.
	Approval AbilityFileApproval `json:"approval"`
	// Files are the version's files (paths, sizes, SHA-256s).
	Files []models.AbilityFile `json:"files"`
}

// AbilityFileApproval is a version's approval record as a file carries it.
type AbilityFileApproval struct {
	State     string                       `json:"state"`
	CheckedOK bool                         `json:"checkedOk"`
	CheckedAt time.Time                    `json:"checkedAt"`
	Decisions []models.AbilityFileDecision `json:"decisions"`
	Note      string                       `json:"note"`
}

// AbilityExportIn exports a version of an ability.
type AbilityExportIn struct {
	Ref string `json:"ref" jsonschema:"minLength=1,description=name (its newest approved version\\, else its newest) or name@version."`
	// Out is the file to write (default <name>.ability.zip in this folder).
	Out   string `json:"out,omitempty"`
	Force bool   `json:"force,omitempty"`
}

// AbilityExportOut says what was written.
type AbilityExportOut struct {
	File    string   `json:"file,omitempty"`
	Bytes   int64    `json:"bytes"`
	Name    string   `json:"name"`
	Title   string   `json:"title"`
	Version string   `json:"version"`
	Number  int      `json:"number"`
	Lineage []string `json:"lineage"`
	Message string   `json:"message"`
}

// AbilityImportIn brings in an ability file.
type AbilityImportIn struct {
	File string `json:"file" jsonschema:"minLength=1,description=The ability file (.ability.zip) on this computer."`
	// AsVersion brings it in as a new version of an ability of the same name
	// here that shares no version with it (otherwise refused).
	AsVersion bool `json:"asVersion,omitempty"`
	// DryRun reads and checks the file and says what bringing it in would
	// do; nothing is kept.
	DryRun bool `json:"dryRun,omitempty"`
}

// AbilityImportOut says what came in.
type AbilityImportOut struct {
	Name    string `json:"name"`
	Title   string `json:"title"`
	Version string `json:"version"`
	// Number is its number here (0 on a dry run of a new version).
	Number int    `json:"number"`
	State  string `json:"state"`
	// New: a version this workspace didn't have; Of: a new version of an
	// ability already here (else a new ability).
	New     bool                  `json:"new"`
	Of      bool                  `json:"of"`
	DryRun  bool                  `json:"dryRun"`
	From    *models.AbilityImport `json:"from,omitempty"`
	Facts   models.AbilityFacts   `json:"facts"`
	Link    string                `json:"link,omitempty"`
	Message string                `json:"message"`
	Next    string                `json:"next,omitempty"`
	Leaves  string                `json:"leaves" jsonschema:"description=What it says leaves this computer when it runs."`
	// Lineage says what the file claims about its history here (never verified).
	Lineage string               `json:"lineage,omitempty"`
	Hosts   []string             `json:"hosts"`
	Files   []models.AbilityFile `json:"files"`
}

func init() {
	ops.Def("ability.export", "Export a version of an ability to a file another workspace can bring in: its manifest and files (its version, by SHA-256), its lineage, and its approval here as information only; writing over a file takes a person", ops.Flags{Mutating: true, LocalOnly: true},
		func(ctx context.Context, a *app.App, in *AbilityExportIn) (*AbilityExportOut, error) {
			return exportFile(ctx, a, in)
		})
	ops.Def("ability.import", "Bring in an ability file from another workspace as a draft: never approved and never set up here — check it on this computer, then an owner approves it", ops.Flags{Mutating: true, LocalOnly: true},
		func(ctx context.Context, a *app.App, in *AbilityImportIn) (*AbilityImportOut, error) {
			f, err := os.Open(strings.TrimSpace(in.File))
			if err != nil {
				return nil, apperr.New(apperr.InvalidInput, "There is no ability file at %s.", in.File)
			}
			defer f.Close()
			st, err := f.Stat()
			if err != nil {
				return nil, err
			}
			return Import(ctx, a, f, st.Size(), in.AsVersion, in.DryRun)
		})
}

// ExportName is the file an ability's export is called.
func ExportName(name string) string { return name + ".ability.zip" }

func exportFile(ctx context.Context, a *app.App, in *AbilityExportIn) (*AbilityExportOut, error) {
	v, err := exportVersion(ctx, a, in.Ref)
	if err != nil {
		return nil, err
	}
	out := strings.TrimSpace(in.Out)
	if out == "" {
		out = ExportName(v.Name)
	}
	if _, err := os.Lstat(out); err == nil {
		switch {
		case !in.Force:
			return nil, apperr.New(apperr.Conflict, "%s is already there; nothing was written.", filepath.Base(out)).WithHint("Write over it with force, or give another file.")
		case !ops.CallerFrom(ctx).Human:
			return nil, apperr.New(apperr.HumanOnly, "Only a person writes over a file that is already there.").WithHint("Give another file, or write over it yourself in a terminal.")
		}
	}
	tmp, err := os.CreateTemp(filepath.Dir(out), ".ability-export-")
	if err != nil {
		return nil, apperr.Wrap(apperr.InvalidInput, err, "MetaRush can't write a file there.")
	}
	defer os.Remove(tmp.Name())
	res, err := Export(ctx, a, v, tmp)
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
}

// ExportRef writes the version a ref names as an ability file to w.
func ExportRef(ctx context.Context, a *app.App, ref string, w io.Writer) (*AbilityExportOut, error) {
	v, err := exportVersion(ctx, a, ref)
	if err != nil {
		return nil, err
	}
	return Export(ctx, a, v, w)
}

// exportVersion is the version a ref names: name@version, else the newest
// approved version, else the newest.
func exportVersion(ctx context.Context, a *app.App, ref string) (*models.AbilityVersion, error) {
	r, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	if r.Hash == "" {
		if v, err := resolve(ctx, a, ref, true); err == nil {
			return v, nil
		}
	}
	return resolve(ctx, a, ref, false)
}

// Export writes a version as an ability file (a zip) to w.
func Export(ctx context.Context, a *app.App, v *models.AbilityVersion, w io.Writer) (*AbilityExportOut, error) {
	arc, err := loadArchive(ctx, a, v)
	if err != nil {
		return nil, err
	}
	doc := AbilityFileDoc{Format: FileFormat, Made: now(), MetaRush: version.Current().Version, Lineage: lineageOf(ctx, a, v), Files: v.Files}
	var ws models.Workspace
	if a.DB.GetByID(ctx, store.CWorkspaces, a.WorkspaceID(), a.WorkspaceID(), &ws) == nil {
		doc.Workspace = ws.Name
	}
	doc.Ability.Name, doc.Ability.Title, doc.Ability.Does = v.Name, v.Title, v.Does
	doc.Version.Number, doc.Version.Hash, doc.Version.State, doc.Version.Size = v.Number, v.Hash, v.State, int64(len(arc.Bytes))
	doc.Approval = AbilityFileApproval{State: v.State, CheckedOK: v.Check.OK, CheckedAt: v.Check.At, Decisions: []models.AbilityFileDecision{},
		Note: "Information only: a workspace that brings this file in checks the ability itself and an owner there approves it."}
	for _, d := range v.Decisions {
		doc.Approval.Decisions = append(doc.Approval.Decisions, models.AbilityFileDecision{State: d.State, By: firstNonEmpty(d.By.UserName, "an owner"), At: d.At, Note: d.Note, OpenNetwork: d.OpenNetwork})
	}
	if doc.Files == nil {
		doc.Files = []models.AbilityFile{}
	}
	zw := zip.NewWriter(w)
	put := func(name string, b []byte) error {
		f, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate, Modified: doc.Made})
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		return err
	}
	db, _ := json.MarshalIndent(doc, "", "  ")
	if err := put(fileDoc, db); err != nil {
		return nil, err
	}
	if err := put(fileArchive, arc.Bytes); err != nil {
		return nil, err
	}
	if m, ok := arc.File(ManifestFile); ok {
		if err := put(ManifestFile, m); err != nil {
			return nil, err
		}
	}
	if n, ok := arc.File(NotesFile); ok {
		if err := put(NotesFile, n); err != nil {
			return nil, err
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	a.Audit(ctx, "ability.export", "ability", v.ID, "", map[string]any{"name": v.Name, "version": v.Hash, "number": v.Number})
	return &AbilityExportOut{Name: v.Name, Title: v.Title, Version: v.Hash, Number: v.Number, Lineage: doc.Lineage,
		Message: fmt.Sprintf("Version %d of “%s” is in an ability file: another workspace brings it in as a draft that its owners check and approve there.", v.Number, v.Title)}, nil
}

// lineageOf is every version a version descends from, oldest first, this
// one last: the earlier versions of its name here (with where each that came
// in from a file came from).
func lineageOf(ctx context.Context, a *app.App, v *models.AbilityVersion) []string {
	vs, _ := versionsOf(ctx, a, v.Name)
	slices.SortFunc(vs, func(x, y models.AbilityVersion) int { return x.Number - y.Number })
	var out []string
	add := func(h string) {
		if !slices.Contains(out, h) {
			out = append(out, h)
		}
	}
	for _, x := range vs {
		if x.Number > v.Number {
			break
		}
		if x.Imported != nil {
			for _, h := range x.Imported.Lineage {
				add(h)
			}
		}
		add(x.Hash)
	}
	if len(out) == 0 || out[len(out)-1] != v.Hash {
		out = slices.DeleteFunc(out, func(h string) bool { return h == v.Hash })
		out = append(out, v.Hash)
	}
	return out
}

// Import reads an ability file strictly and records it as a draft.
func Import(ctx context.Context, a *app.App, r io.ReaderAt, size int64, asVersion, dryRun bool) (*AbilityImportOut, error) {
	doc, arc, err := readFile(r, size)
	if err != nil {
		return nil, err
	}
	m, err := arc.Manifest()
	if err != nil {
		return nil, notAbilityFile("its manifest can't be read: %s", err.Error())
	}
	if m.Name != doc.Ability.Name {
		return nil, notAbilityFile("it says it is “%s”, but its manifest names “%s”", doc.Ability.Name, m.Name)
	}
	if probs := m.Validate(arc.Programs()); len(probs) > 0 {
		var words []string
		for _, p := range probs {
			words = append(words, p.String())
		}
		return nil, apperr.New(apperr.Validation, "The ability in that file has problems MetaRush won't bring in: %s", strings.Join(words, "; ")).WithDetails(map[string]any{"problems": words})
	}
	f := factsOf(m)
	out := &AbilityImportOut{Name: m.Name, Title: m.Title, Version: arc.Hash, Facts: f, DryRun: dryRun, Hosts: f.Hosts, Leaves: sendsWords(f), Files: []models.AbilityFile{}}
	if out.Hosts == nil {
		out.Hosts = []string{}
	}
	for _, x := range arc.Files {
		b := x.Bytes()
		out.Files = append(out.Files, models.AbilityFile{Path: x.Path, Size: int64(len(b)), SHA256: sum(b), Program: x.Exec})
	}
	from := &models.AbilityImport{Workspace: clean(doc.Workspace, 120), Made: doc.Made, MetaRush: clean(doc.MetaRush, 40), Number: doc.Version.Number, State: clean(doc.Version.State, 20),
		Decisions: []models.AbilityFileDecision{}, Lineage: doc.Lineage, At: now(), By: a.Actor}
	for _, d := range doc.Approval.Decisions {
		d.By, d.Note, d.State = clean(d.By, 120), clean(d.Note, 500), clean(d.State, 20)
		from.Decisions = append(from.Decisions, d)
	}
	out.From = from
	// Already here: nothing new.
	if v, err := getVersion(ctx, a, arc.Hash); err == nil {
		out.Number, out.State, out.Link = v.Number, v.State, links.Ability(v.Name, v.Hash)
		out.Message = fmt.Sprintf("This workspace already has that version: version %d of “%s” (%s). Nothing new came in.", v.Number, v.Title, StateWords(v.State))
		return out, nil
	} else if !errors.Is(err, store.ErrNotFound) {
		return nil, err
	}
	out.New = true
	// A version of an ability already here, or a new ability.
	here, err := versionsOf(ctx, a, m.Name)
	if err != nil {
		return nil, err
	}
	if len(here) > 0 {
		// The file's lineage is its own word: it can't be checked, so a
		// version of an ability already here comes in only when a person says
		// it is the same one.
		var claimed *models.AbilityVersion
		for i := range here {
			if slices.Contains(doc.Lineage, here[i].Hash) {
				claimed = &here[i]
				break
			}
		}
		if claimed != nil {
			out.Lineage = fmt.Sprintf("It says it shares history with version %d of “%s” here; MetaRush can't verify that.", claimed.Number, claimed.Title)
		}
		if !asVersion {
			msg := fmt.Sprintf("This workspace already has an ability called “%s” (“%s”), and the file says nothing MetaRush can verify about it.", m.Name, here[0].Title)
			if claimed != nil {
				msg = fmt.Sprintf("This workspace already has an ability called “%s”. %s", m.Name, out.Lineage)
			}
			return nil, apperr.New(apperr.Conflict, "%s", msg).
				WithHint("Bring it in as a new version of that ability only if it is the same one; otherwise rename one of them.").
				WithCommand("metarush ability import <file> --as-version")
		}
		out.Of = true
	}
	if dryRun {
		out.State = models.AbilityImported
		out.Message = fmt.Sprintf("“%s” would come in as %s, never approved here: check it on this computer, then an owner approves it.", m.Title, map[bool]string{true: "a new version of the ability here", false: "a new ability"}[out.Of])
		return out, nil
	}
	v, err := recordImported(ctx, a, arc, m, from)
	if err != nil {
		return nil, err
	}
	out.Number, out.State, out.Link = v.Number, v.State, links.Ability(v.Name, v.Hash)
	a.Audit(context.WithoutCancel(ctx), "ability.import", "ability", v.ID, "", map[string]any{"name": v.Name, "version": v.Hash, "number": v.Number, "from": from.Workspace,
		"theirNumber": from.Number, "theirState": from.State, "of": out.Of})
	out.Message = fmt.Sprintf("Version %d of “%s” came in from %s as a draft: it isn't approved or set up here.", v.Number, v.Title, firstNonEmpty(quoted(from.Workspace), "another workspace"))
	out.Next = "Check it on this computer (metarush ability check --version " + v.Name + "@" + Short(v.Hash) + "); then an owner reads what it may do under Library → Abilities and approves it."
	return out, nil
}

func quoted(s string) string {
	if s == "" {
		return ""
	}
	return "“" + s + "”"
}

// clean keeps imported words as plain data: control characters out, and
// at most n characters.
func clean(s string, n int) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if r := []rune(s); len(r) > n {
		s = string(r[:n])
	}
	return strings.TrimSpace(s)
}

func notAbilityFile(f string, args ...any) error {
	return apperr.New(apperr.InvalidInput, "That isn't an ability file MetaRush can bring in: "+f+".", args...)
}

// readFile reads an ability file strictly: only its own files, each once
// and within its size, ability.json decoded with no unknown fields and its
// format named, the archive canonical and its SHA-256 the version it names.
func readFile(r io.ReaderAt, size int64) (*AbilityFileDoc, *Archive, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, nil, notAbilityFile("it isn't a zip file")
	}
	allowed := map[string]int64{fileDoc: 1 << 20, fileArchive: maxArchiveFile, ManifestFile: MaxFileBytes, NotesFile: MaxFileBytes}
	got := map[string][]byte{}
	for _, f := range zr.File {
		max, ok := allowed[f.Name]
		if !ok {
			return nil, nil, notAbilityFile("it holds %s, which an ability file doesn't", f.Name)
		}
		if _, dup := got[f.Name]; dup {
			return nil, nil, notAbilityFile("it holds %s twice", f.Name)
		}
		if f.Method != zip.Store && f.Method != zip.Deflate {
			return nil, nil, notAbilityFile("%s is packed in a way MetaRush doesn't read", f.Name)
		}
		if f.UncompressedSize64 > uint64(max) {
			return nil, nil, notAbilityFile("%s is larger than an ability file allows", f.Name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, nil, notAbilityFile("%s can't be read", f.Name)
		}
		b, err := io.ReadAll(io.LimitReader(rc, max+1))
		rc.Close()
		if err != nil || int64(len(b)) > max {
			return nil, nil, notAbilityFile("%s can't be read", f.Name)
		}
		got[f.Name] = b
	}
	db, ok := got[fileDoc]
	if !ok {
		return nil, nil, notAbilityFile("it has no %s", fileDoc)
	}
	var doc AbilityFileDoc
	dec := json.NewDecoder(bytes.NewReader(db))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&doc); err != nil {
		return nil, nil, notAbilityFile("%s isn't in the form MetaRush writes", fileDoc)
	}
	if doc.Format != FileFormat {
		return nil, nil, notAbilityFile("it is in the format %q, and this MetaRush reads %q", clean(doc.Format, 60), FileFormat)
	}
	ab, ok := got[fileArchive]
	if !ok {
		return nil, nil, notAbilityFile("it has no %s", fileArchive)
	}
	arc, err := Parse(ab)
	if err != nil {
		var ae *apperr.Error
		if errors.As(err, &ae) {
			return nil, nil, notAbilityFile("%s", strings.TrimSuffix(lowerFirst(ae.Message), "."))
		}
		return nil, nil, notAbilityFile("its archive isn't one MetaRush writes")
	}
	if arc.Hash != doc.Version.Hash || !ValidHash(doc.Version.Hash) {
		return nil, nil, notAbilityFile("its files aren't the version it names")
	}
	// The copies for people to read must be the archive's own.
	for _, name := range []string{ManifestFile, NotesFile} {
		copyB, inZip := got[name]
		own, inArc := arc.File(name)
		if inZip && (!inArc || !bytes.Equal(copyB, own)) {
			return nil, nil, notAbilityFile("its %s isn't the one in its archive", name)
		}
	}
	for _, h := range doc.Lineage {
		if !ValidHash(h) {
			return nil, nil, notAbilityFile("its lineage names something that isn't a version")
		}
	}
	if len(doc.Lineage) == 0 || doc.Lineage[len(doc.Lineage)-1] != arc.Hash {
		doc.Lineage = append(slices.DeleteFunc(doc.Lineage, func(h string) bool { return h == arc.Hash }), arc.Hash)
	}
	if len(doc.Lineage) > 500 {
		doc.Lineage = doc.Lineage[len(doc.Lineage)-500:]
	}
	return &doc, arc, nil
}

// recordImported keeps an imported version: its archive in the media store
// and its record, imported (its test not run here, nobody approved it here).
func recordImported(ctx context.Context, a *app.App, arc *Archive, m *Manifest, from *models.AbilityImport) (*models.AbilityVersion, error) {
	tmp, err := os.CreateTemp("", "metarush-ability-archive-")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(arc.Bytes); err != nil {
		tmp.Close()
		return nil, err
	}
	if err := tmp.Close(); err != nil {
		return nil, err
	}
	key := media.ObjectKey(a.WorkspaceID(), arc.Hash)
	info, err := a.Media.PutFile(ctx, key, tmp.Name(), media.PutOptions{ContentType: "application/json", SHA256: arc.Hash}, media.Resume{}, nil)
	if err != nil {
		return nil, apperr.Wrap(apperr.Storage, err, "save the ability in the media store")
	}
	files := make([]models.AbilityFile, 0, len(arc.Files))
	for _, f := range arc.Files {
		b := f.Bytes()
		files = append(files, models.AbilityFile{Path: f.Path, Size: int64(len(b)), SHA256: sum(b), Program: f.Exec})
	}
	manifest, _ := arc.File(ManifestFile)
	search := m.Search
	if search == nil {
		search = []string{}
	}
	ctx = context.WithoutCancel(ctx)
	for attempt := 0; attempt < 5; attempt++ {
		n, err := nextNumber(ctx, a, m.Name)
		if err != nil {
			return nil, err
		}
		doc := bson.M{"_id": docID(a.WorkspaceID(), arc.Hash), "workspaceId": a.WorkspaceID(), "name": m.Name, "hash": arc.Hash, "number": n, "state": models.AbilityImported,
			"decisions": []models.AbilityDecision{}, "by": a.Actor, "createdAt": now(), "updatedAt": now(), "title": m.Title, "does": m.Does, "search": search,
			"facts": factsOf(m), "manifest": string(manifest), "notes": arc.Notes(), "files": files, "ignored": arc.Ignored,
			"archive": models.AbilityObject{Key: key, SHA256: arc.Hash, Size: info.Size}, "imported": from,
			"check": models.AbilityCheck{Problems: []string{"It came from another workspace and hasn't been checked on a computer here yet."}, Warnings: []string{}, By: a.Actor, Machine: a.Cfg.Installation.Name}}
		_, err = a.DB.C(store.CAbilities).InsertOne(ctx, doc)
		if mongo.IsDuplicateKeyError(err) {
			if got, gerr := getVersion(ctx, a, arc.Hash); gerr == nil {
				return got, nil // brought in at the same time elsewhere
			}
			continue // another version took the number
		}
		if err != nil {
			return nil, err
		}
		return getVersion(ctx, a, arc.Hash)
	}
	return nil, apperr.New(apperr.Conflict, "MetaRush couldn't number this version; bring it in again.")
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

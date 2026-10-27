package remotion

import (
	"context"
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Versions that travel in a file another workspace exported (D219). A version
// is its manifest and its files, all content-addressed, so it travels as
// bytes and comes in as the same version (the same hash). What comes in never
// runs and is never trusted: it is checked against the import's rules like any
// folder brought in (a version holding secrets, other tools' settings,
// installed packages or git refuses), kept as a new source of the project the
// file's things go to, and its version waits — its packages for a person to
// allow, then a check in the render jail, then a person's approval — exactly
// like a version Claude edited. What MetaRush read of it elsewhere (its
// compositions, settings and pictures) is not taken from the file: the check
// here reads them again. A version this workspace already has (the same
// hash) stays as it is, approvals included.

// FileVersion is a version as a file carries it.
type FileVersion struct {
	SourceName  string
	SourceTitle string
	Number      int
	Hash        string
	// Manifest is the canonical manifest (its SHA-256 is Hash); Files its files.
	Manifest []byte
	Files    []ManifestFile
}

// ExportVersion reads a version for a file: its manifest and the list of its
// files (each read with ObjectPath).
func ExportVersion(ctx context.Context, a *app.App, hash string) (*FileVersion, error) {
	v, err := getVersion(ctx, a, hash)
	if err != nil {
		return nil, err
	}
	s, err := getSource(ctx, a, v.SourceID)
	if err != nil {
		return nil, err
	}
	b, err := readObject(ctx, a, v.Manifest.SHA256, v.Manifest.Size)
	if err != nil {
		return nil, err
	}
	m, err := ParseManifest(b, v.Hash)
	if err != nil {
		return nil, apperr.Wrap(apperr.Integrity, err, "Version %d of “%s” isn't what MetaRush stored.", v.Number, s.Title)
	}
	return &FileVersion{SourceName: s.Name, SourceTitle: s.Title, Number: v.Number, Hash: v.Hash, Manifest: b, Files: m.Files}, nil
}

// ObjectPath is a verified copy, on this computer, of a file of a version.
func ObjectPath(ctx context.Context, a *app.App, sha string, size int64) (string, error) {
	return localObject(ctx, a, sha, size)
}

// FromFileIn is a version a file brought in.
type FromFileIn struct {
	// ProjectID is the project its source goes into; Name and Title the
	// source's names where the file was made (a free name here is taken).
	ProjectID string
	Name      string
	Title     string
	// File names the file it came in (words for people: where it came from).
	File string
	// Manifest is the version's canonical manifest and Hash the version;
	// Files the local copy of each of its files, by SHA-256.
	Manifest []byte
	Hash     string
	Files    map[string]string
}

// ImportFromFile brings a version in from a file: a new source of the project
// whose version waits for a person (known reports a version this workspace
// had already, which stays as it is).
func ImportFromFile(ctx context.Context, a *app.App, in FromFileIn) (s *models.RemotionSource, v *models.RemotionVersion, known bool, err error) {
	m, err := ParseManifest(in.Manifest, in.Hash)
	if err != nil {
		return nil, nil, false, apperr.Wrap(apperr.Integrity, err, "The Remotion project “%s” in the file isn't a version MetaRush can read.", in.Title)
	}
	if old, err := getVersion(ctx, a, in.Hash); err == nil {
		src, err := getSource(ctx, a, old.SourceID)
		if err != nil {
			return nil, nil, false, err
		}
		return src, old, true, nil
	}
	// The import's rules, as for any folder brought in: nothing it would leave out.
	for _, f := range m.Files {
		p, ok := in.Files[f.SHA256]
		if !ok {
			return nil, nil, false, apperr.New(apperr.Integrity, "The file lacks a file of the Remotion project “%s”.", in.Title)
		}
		if why := leftOutByImport(f.Path); why != "" {
			return nil, nil, false, apperr.New(apperr.Validation, "The Remotion project “%s” in the file holds what MetaRush never brings in (%s: %s), so it didn't come along.", in.Title, f.Path, why)
		}
		if !isMedia(f.Path) && f.Size <= 8<<20 {
			data, err := os.ReadFile(p)
			if err != nil {
				return nil, nil, false, err
			}
			if what := secretIn(f.Path, data); what != "" {
				return nil, nil, false, apperr.New(apperr.Validation, "A file of the Remotion project “%s” in the file looks like it holds %s (%s), so it didn't come along.", in.Title, what, f.Path)
			}
		}
	}
	if problem := projectFiles(m); problem != "" {
		return nil, nil, false, apperr.New(apperr.Validation, "The Remotion project “%s” in the file %s.", in.Title, problem)
	}
	var prj models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), in.ProjectID, &prj); err != nil {
		return nil, nil, false, apperr.New(apperr.NotFound, "The project the Remotion project was meant for is gone.")
	}
	// Its files and its manifest, by their contents.
	for _, f := range m.Files {
		if _, err := putFile(ctx, a, in.Files[f.SHA256], f.SHA256, f.Size, ""); err != nil {
			return nil, nil, false, err
		}
	}
	mobj, err := putBytes(ctx, a, in.Manifest, "application/json")
	if err != nil {
		return nil, nil, false, err
	}
	if mobj.SHA256 != in.Hash {
		return nil, nil, false, apperr.New(apperr.Integrity, "The Remotion project “%s” in the file isn't the version it says it is.", in.Title)
	}
	src, err := fileSource(ctx, a, &prj, in)
	if err != nil {
		return nil, nil, false, err
	}
	files, bytes, pf, pb := totals(m)
	nv := &models.RemotionVersion{Hash: in.Hash, Kind: models.RemotionKindImport, Message: "Brought in with " + src.Origin.Where(),
		By: a.Actor, At: time.Now().UTC(), Machine: a.Cfg.Installation.Name, Manifest: mobj, Files: files, Bytes: bytes, PublicFiles: pf, PublicBytes: pb,
		State: models.RemotionWaiting}
	if pi, err := versionPackages(ctx, a, m); err == nil {
		nv.Lock, nv.Remotion, nv.Packages = pi.lockHash, pi.remotion, pi.packages
	}
	nv.PackagesAllowed = false // a person allows them here before anything is installed
	nv, _, err = insertVersion(ctx, a, src, nv)
	if err != nil {
		return nil, nil, false, err
	}
	a.Audit(ctx, "remotion.import", "remotion", src.ID, src.ProjectID, map[string]any{"name": src.Name, "version": nv.Hash, "files": len(m.Files), "from": "file", "via": a.Actor.Via})
	return src, nv, false, nil
}

// leftOutByImport says why a folder brought in would leave a file out ("" it keeps it).
func leftOutByImport(rel string) string {
	if !safeRel(rel) {
		return whyName
	}
	dirs := strings.Split(rel, "/")
	for i := 1; i < len(dirs); i++ {
		if why := skipDir(path.Join(dirs[:i]...)); why != "" {
			return why
		}
	}
	return skipFile(rel)
}

// projectFiles says what a version lacks to be a Remotion project ("" nothing).
func projectFiles(m *Manifest) string {
	if _, ok := m.File("package.json"); !ok {
		return "has no package.json"
	}
	if _, ok := m.File("package-lock.json"); !ok {
		return "has no package-lock.json (its packages can't be checked)"
	}
	return ""
}

// fileSource makes the source a file's version comes in as: a free name in
// the project, its title from the file, nothing allowed yet.
func fileSource(ctx context.Context, a *app.App, prj *models.Project, in FromFileIn) (*models.RemotionSource, error) {
	all, err := listSources(ctx, a, prj.ID)
	if err != nil {
		return nil, err
	}
	base := in.Name
	if !NamePattern.MatchString(base) {
		base = slug(firstNonEmpty(in.Title, "remotion"))
	}
	taken := map[string]bool{}
	for _, s := range all {
		taken[s.Name] = true
	}
	name := base
	for i := 2; taken[name]; i++ {
		name = fmt.Sprintf("%s-%d", strings.TrimRight(base[:min(len(base), 36)], "-"), i)
	}
	title := strings.TrimSpace(in.Title)
	if title == "" {
		title = name
	}
	now := time.Now().UTC()
	s := &models.RemotionSource{ID: ids.New(ids.RemotionSource), WorkspaceID: a.WorkspaceID(), ProjectID: prj.ID, Name: name, Title: shortTitle(title),
		Origin:  models.RemotionOrigin{Kind: models.RemotionFromFile, Folder: in.File, Machine: a.Cfg.Installation.Name},
		Allowed: []models.RemotionAllowed{}, CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CRemotionSources, s); err != nil {
		return nil, err
	}
	return s, nil
}

func shortTitle(s string) string {
	r := []rune(s)
	if len(r) > 120 {
		return string(r[:120])
	}
	return s
}

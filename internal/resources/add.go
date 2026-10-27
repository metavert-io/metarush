package resources

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Add statuses.
const (
	StatusAdded   = "added"   // a new resource
	StatusVersion = "version" // a new version of an existing resource
	StatusSame    = "same"    // these exact bytes are already there (nothing new)
	StatusSkipped = "skipped" // not a resource (footage, or nothing MetaRush can use)
	StatusFailed  = "failed"
)

// ErrNotResource means the file is footage or something MetaRush can't use as a resource.
var ErrNotResource = errors.New("not a picture, SVG, PDF or font")

// MaxFileBytes is the largest resource file (a big PDF or a poster-sized photo).
const MaxFileBytes = 512 << 20

// AddOptions configure adding one file.
type AddOptions struct {
	// Scope: project or workspace (default: project when ProjectID is set).
	Scope     string
	ProjectID string
	// Name is the short name wanted (default: from the file name); Title the
	// display name (default: the file name without its extension).
	Name        string
	Title       string
	Tags        []string
	Description string
	License     string
	// ResourceID adds the file as a new version of that resource.
	ResourceID string
	// Folder ties the resource to a file in a linked folder (a changed file
	// there becomes a new version of the same resource).
	Folder *models.ResourceFolderRef
	// RepoFile ties a picture to the repository file it was kept from (D216):
	// the same file at a newer commit becomes a new version of it, and it is
	// a picture of its own even when the same bytes are elsewhere.
	RepoFile *models.ResourceRepoFile
	// DriveFile ties a picture to the Google Drive file it was fetched from
	// (D220): the same file with new contents becomes a new version of it, and
	// it is a picture of its own even when the same bytes are elsewhere.
	DriveFile *models.ResourceDriveFile
	// Fresh makes the file a resource of its own, never a new version of
	// another found by its name (a picture brought in with a file from another
	// workspace, D219); the same bytes already there stay one resource.
	Fresh bool
	// Source is the provenance (Kind file when empty).
	Source models.ResourceSource
	// Defer keeps the bytes on this machine (its cache) instead of uploading
	// them now; the version is recorded with this machine as a holder and is
	// uploaded when a cut first uses it (UploadVersion).
	Defer bool
	// FileName is the name the file arrived under (browser uploads land in a
	// temporary file); default: the path's base name.
	FileName string
	Via      string
}

// AddResult is one file's outcome.
type AddResult struct {
	Status   string           `json:"status" jsonschema:"enum=added,enum=version,enum=same,enum=skipped,enum=failed"`
	Resource *models.Resource `json:"resource,omitempty"`
	SHA256   string           `json:"sha256,omitempty"`
	// Message says what happened, in plain words ("Added Acme logo to Resources").
	Message string `json:"message"`
	// Changes are what MetaRush changed on the way in (converted, cleaned).
	Changes []string `json:"changes,omitempty"`
	Notes   []string `json:"notes,omitempty"`
	// Sniffed is what the file is.
	Sniffed Sniffed `json:"sniffed"`
	// Error is set when it failed.
	Error string `json:"error,omitempty"`
}

// AddFile adds one local file to the library: recognized by content,
// converted or cleaned when needed, deduplicated by SHA-256, described,
// stored and tagged.
// MayStore refuses keeping a picture for a client whose rules keep pictures
// from cloud storage while the media is there (set by internal/leave).
var MayStore func(ctx context.Context, a *app.App, projectID, what string) error

func AddFile(ctx context.Context, a *app.App, path string, o AddOptions) (*AddResult, error) {
	name := o.FileName
	if name == "" {
		name = filepath.Base(path)
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, apperr.Wrap(apperr.Internal, err, "MetaRush can't read %s.", name)
	}
	if !st.Mode().IsRegular() || st.Size() == 0 {
		return &AddResult{Status: StatusSkipped, Message: name + " is empty or not a file."}, nil
	}
	if st.Size() > MaxFileBytes {
		return nil, apperr.New(apperr.InvalidInput, "%s is larger than %d MB, the most a resource can be.", name, MaxFileBytes>>20)
	}
	sn, err := SniffFile(path)
	if err != nil {
		return nil, err
	}
	res := &AddResult{Sniffed: sn}
	if sn.Route != RouteResource {
		res.Status, res.Message = StatusSkipped, name+" isn't a picture, SVG, PDF or font."
		return res, ErrNotResource
	}
	o.FileName = name
	scope, err := scopeOf(ctx, a, &o)
	if err != nil {
		return nil, err
	}
	if MayStore != nil { // What may leave (D290): a client's pictures and cloud storage
		pid := o.ProjectID
		if o.ResourceID != "" {
			var r models.Resource
			if a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), o.ResourceID, &r) == nil {
				pid = r.ProjectID
			}
		}
		if err := MayStore(ctx, a, pid, "“"+name+"”"); err != nil {
			return nil, err
		}
	}
	work, err := os.MkdirTemp(a.Cfg.Scratch.Dir, "resource-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	prepared := path
	var originalSHA string
	// HEIC and AVIF photos become JPEG (browsers and the renderer draw JPEG everywhere).
	if sn.Convert {
		out := filepath.Join(work, "photo.jpg")
		tool, err := ConvertPhoto(ctx, a.Tools, path, out)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "%s", err.Error())
		}
		originalSHA, _ = media.HashFile(path)
		prepared = out
		res.Changes = append(res.Changes, fmt.Sprintf("converted from %s to JPEG (%s)", strings.ToUpper(sn.Format), tool))
		sn = Sniffed{Route: RouteResource, Kind: models.ResourceImage, Format: "jpeg", MIME: "image/jpeg", Ext: "jpg"}
		res.Sniffed = sn
	}
	// SVGs are cleaned: no scripts, no outside references.
	if sn.Kind == models.ResourceSVG {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		clean, changes, err := SanitizeSVG(raw)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "%s: %s", name, err.Error())
		}
		if len(changes) > 0 || string(clean) != string(raw) {
			originalSHA, _ = media.HashFile(path)
			out := filepath.Join(work, "clean.svg")
			if err := os.WriteFile(out, clean, 0o644); err != nil {
				return nil, err
			}
			prepared = out
		}
		res.Changes = append(res.Changes, changes...)
	}
	sha, err := media.HashFile(prepared)
	if err != nil {
		return nil, err
	}
	pst, _ := os.Stat(prepared)
	res.SHA256 = sha
	actor := a.Actor
	if o.Via != "" {
		actor.Via = o.Via
	}
	now := time.Now().UTC()

	// Where it goes: a new version of a known resource, the same bytes again,
	// or a new resource.
	var target *models.Resource
	switch {
	case o.ResourceID != "":
		var r models.Resource
		if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), o.ResourceID, &r); err != nil {
			return nil, apperr.New(apperr.NotFound, "The resource this was meant to be a new version of was not found. It may have been removed.").WithDetails(map[string]any{"resource": o.ResourceID})
		}
		target = &r
	case o.RepoFile != nil:
		target = repoFileResource(ctx, a, o.RepoFile.RepoID, o.RepoFile.Path)
	case o.DriveFile != nil:
		target = DriveFileResource(ctx, a, o.DriveFile.ID)
	case o.Fresh:
		// Never another resource's new version by its name.
	case o.Folder != nil:
		var r models.Resource
		err := a.DB.C(store.CResources).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "folder.folderId": o.Folder.FolderID, "folder.relPath": o.Folder.RelPath}).Decode(&r)
		if err == nil {
			target = &r
		}
	default:
		// The same name asked for (--as logo), or a file with the same name
		// as one added before, in the same place: a new version of it (a
		// refreshed logo keeps its name, and cuts can update to it).
		target = sameName(ctx, a, scope, o.ProjectID, sn.Kind, o.Name, name)
	}
	if target != nil {
		if v := target.Version(sha); v != nil {
			if (o.Folder != nil || o.RepoFile != nil) && target.Missing {
				_ = a.DB.Update(ctx, store.CResources, a.WorkspaceID(), target.ID, bson.M{"$set": bson.M{"missing": false, "updatedAt": now}, "$unset": bson.M{"missingSince": ""}})
				target.Missing = false
			}
			if o.DriveFile != nil && (target.DriveFile == nil || *target.DriveFile != *o.DriveFile) {
				// The same bytes: what Google says about the file now (its name, its checksum).
				_ = a.DB.Update(ctx, store.CResources, a.WorkspaceID(), target.ID, bson.M{"$set": bson.M{"driveFile": o.DriveFile, "updatedAt": now}})
				target.DriveFile = o.DriveFile
			}
			res.Status, res.Resource = StatusSame, target
			res.Message = fmt.Sprintf("%s is already this version of %s.", name, target.Title)
			return res, nil
		}
	} else if o.RepoFile == nil && o.DriveFile == nil { // a repository's or a Drive's file is a picture of its own, even with bytes seen before
		if same := sameBytes(ctx, a, sha, scope, o.ProjectID); same != nil {
			res.Status, res.Resource = StatusSame, same
			where := "Resources"
			if same.Scope == models.ScopeWorkspace && scope == models.ScopeProject {
				where = "the workspace library"
			}
			res.Message = fmt.Sprintf("%s is already in %s as %s.", name, where, same.Title)
			return res, nil
		}
	}

	// Describe it.
	v := models.ResourceVersion{SHA256: sha, Size: pst.Size(), MIME: sn.MIME, Ext: sn.Ext, StorageKey: media.ObjectKey(a.WorkspaceID(), sha),
		OriginalSHA256: originalSHA, Changes: res.Changes, CreatedAt: now, CreatedBy: actor}
	v.Source = o.Source
	if v.Source.Kind == "" {
		v.Source.Kind = models.FromFile
	}
	if v.Source.Name == "" {
		v.Source.Name = name
	}
	if v.Source.Machine == "" {
		v.Source.Machine = a.Cfg.Installation.Name
	}
	v.Source.At = now
	palette := 0
	var pagePaths []string
	switch sn.Kind {
	case models.ResourceImage:
		pi, err := ReadPicture(prepared)
		if err != nil {
			return nil, apperr.New(apperr.InvalidInput, "%s: %s", name, err.Error())
		}
		v.Width, v.Height, v.HasAlpha, v.Colors, palette = pi.Width, pi.Height, pi.HasAlpha, pi.Colors, pi.Palette
		if pi.Thumb != nil {
			obj, err := putBytes(ctx, a, work, pi.Thumb, "image/png")
			if err == nil {
				obj.Width, obj.Height = pi.ThumbW, pi.ThumbH
				v.Thumbnail = obj
			}
		}
	case models.ResourceSVG:
		b, _ := os.ReadFile(prepared)
		si := ReadSVG(b)
		v.Width, v.Height, v.Colors, palette = si.Width, si.Height, si.Colors, si.Palette
		v.HasAlpha = true // a vector picture has no background of its own
	case models.ResourcePDF:
		pages, count, err := PDFPages(ctx, a.Tools, prepared, filepath.Join(work, "pages"))
		pagePaths = pages
		v.PageCount = count
		switch {
		case errors.Is(err, ErrNoPoppler):
			res.Notes = append(res.Notes, PopplerNote)
		case err != nil:
			res.Notes = append(res.Notes, "Its pages couldn't be made into pictures; the PDF itself is kept.")
		}
		for i, p := range pages {
			obj, err := putFile(ctx, a, p, "image/png")
			if err != nil {
				return nil, err
			}
			w, h := pngSize(p)
			v.Pages = append(v.Pages, models.ResourcePage{Number: i + 1, SHA256: obj.SHA256, Size: obj.Size, StorageKey: obj.StorageKey, Width: w, Height: h})
			if i == 0 {
				v.Width, v.Height = w, h
				if thumb, tw, th, err := ThumbFromPNG(p); err == nil {
					if obj, err := putBytes(ctx, a, work, thumb, "image/png"); err == nil {
						obj.Width, obj.Height = tw, th
						v.Thumbnail = obj
					}
				}
			}
		}
		if count > MaxPDFPages {
			res.Notes = append(res.Notes, fmt.Sprintf("The first %d of its %d pages are pictures you can use in graphics.", MaxPDFPages, count))
		}
	case models.ResourceFont:
		fi := ReadFont(prepared)
		v.Family, v.Style = fi.Family, fi.Style
		if v.Family == "" {
			v.Family = models.DefaultTitle(name)
		}
	}
	// The bytes: stored now (every machine can render them), or kept on this
	// machine until a cut first uses them.
	if o.Defer {
		v.Holders = []string{a.Cfg.Installation.ID}
	} else {
		if err := putPrepared(ctx, a, prepared, v.StorageKey, sha, sn.MIME); err != nil {
			return nil, err
		}
		v.Stored = true
	}
	if _, err := a.Cache.Adopt(prepared, sha); err != nil && o.Defer {
		return nil, fmt.Errorf("keep a local copy of %s: %w", name, err)
	}
	look := TagInput{Name: name, Kind: sn.Kind, Version: &v, Path: prepared, Pages: pagePaths, Palette: palette, Tools: a.Tools}
	tags := AutoTags(ctx, look)
	if t := FindText(ctx, look); t != "" {
		v.Text = t
	}
	if target != nil {
		set := bson.M{"current": sha, "autoTags": mergeAuto(target.AutoTags, tags), "updatedAt": now, "missing": false}
		if o.DriveFile != nil {
			set["driveFile"] = o.DriveFile
		}
		upd := bson.M{"$push": bson.M{"versions": v}, "$set": set, "$unset": bson.M{"missingSince": ""}}
		if _, err := a.DB.C(store.CResources).UpdateOne(ctx, bson.M{"_id": target.ID, "workspaceId": a.WorkspaceID(), "versions.sha256": bson.M{"$ne": sha}}, upd); err != nil {
			return nil, err
		}
		_ = a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), target.ID, target)
		res.Status, res.Resource = StatusVersion, target
		res.Message = fmt.Sprintf("Added a new version of %s.", target.Title)
		a.Audit(ctx, "resource.version", "resource", target.ID, target.ProjectID, map[string]any{"sha256": sha, "name": name, "via": actor.Via})
		return res, nil
	}
	title := strings.Join(strings.Fields(o.Title), " ")
	if title == "" {
		title = models.DefaultTitle(name)
		if sn.Kind == models.ResourceFont && v.Family != "" {
			title = strings.TrimSpace(v.Family + " " + strings.TrimPrefix(v.Style, "Regular"))
		}
	}
	want := strings.TrimSpace(o.Name)
	if want == "" {
		want = Slug(title)
	} else if !NamePattern.MatchString(want) {
		return nil, apperr.New(apperr.InvalidInput, "%q can't be a resource's short name: use lowercase letters, digits, - and _, starting with a letter.", want)
	}
	r := models.Resource{ID: ids.New(ids.Resource), WorkspaceID: a.WorkspaceID(), Name: want, Title: title, Kind: sn.Kind, Scope: scope,
		Versions: []models.ResourceVersion{v}, Current: sha, Tags: cleanTags(o.Tags), AutoTags: tags, Description: strings.TrimSpace(o.Description),
		License: strings.TrimSpace(o.License), Folder: o.Folder, RepoFile: o.RepoFile, DriveFile: o.DriveFile, CreatedAt: now, CreatedBy: actor, UpdatedAt: now}
	if scope == models.ScopeProject {
		r.ProjectID = o.ProjectID
	}
	// A repository's, a Drive's or an imported picture takes the next free
	// name: its name is a suggestion.
	var kept func() bool
	switch {
	case o.RepoFile != nil:
		kept = func() bool { return repoFileResource(ctx, a, o.RepoFile.RepoID, o.RepoFile.Path) != nil }
	case o.DriveFile != nil:
		kept = func() bool { return DriveFileResource(ctx, a, o.DriveFile.ID) != nil }
	}
	if err := insertUniqueOr(ctx, a, &r, o.Name != "" && o.RepoFile == nil && o.DriveFile == nil && !o.Fresh, kept); err != nil {
		if errors.Is(err, errFileKept) {
			// Another fetch kept the same file meanwhile (D216, D220): a version of that picture.
			if o.RepoFile != nil {
				o.ResourceID = repoFileResource(ctx, a, o.RepoFile.RepoID, o.RepoFile.Path).ID
			} else {
				o.ResourceID = DriveFileResource(ctx, a, o.DriveFile.ID).ID
			}
			return AddFile(ctx, a, path, o)
		}
		return nil, err
	}
	res.Status, res.Resource = StatusAdded, &r
	res.Message = fmt.Sprintf("Added %s to %s.", r.Title, scopeWords(scope))
	a.Audit(ctx, "resource.add", "resource", r.ID, r.ProjectID, map[string]any{"name": r.Name, "kind": r.Kind, "sha256": sha, "scope": scope, "via": actor.Via})
	return res, nil
}

// sameName finds the resource a manually added file updates: one with the
// short name asked for, else one whose newest version came from a file of
// the same name (same kind, same project or library, not removed).
func sameName(ctx context.Context, a *app.App, scope, projectID, kind, want, fileName string) *models.Resource {
	filter := bson.M{"scope": scope, "kind": kind, "removed": bson.M{"$ne": true}, "folder": bson.M{"$exists": false}}
	if scope == models.ScopeProject {
		filter["projectId"] = projectID
	}
	var list []models.Resource
	if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), filter, &list); err != nil {
		return nil
	}
	if want != "" {
		for i := range list {
			if list[i].Name == want {
				return &list[i]
			}
		}
		return nil
	}
	for i := range list {
		if v := list[i].Latest(); v != nil && strings.EqualFold(v.Source.Name, fileName) && (v.Source.Kind == "" || v.Source.Kind == models.FromFile) {
			return &list[i]
		}
	}
	return nil
}

func scopeWords(scope string) string {
	if scope == models.ScopeWorkspace {
		return "the workspace library"
	}
	return "this project's resources"
}

// scopeOf settles the scope and checks the project exists.
func scopeOf(ctx context.Context, a *app.App, o *AddOptions) (string, error) {
	if o.ResourceID != "" {
		return "", nil // a new version keeps its resource's scope
	}
	scope := o.Scope
	if scope == "" {
		scope = models.ScopeWorkspace
		if o.ProjectID != "" {
			scope = models.ScopeProject
		}
	}
	switch scope {
	case models.ScopeWorkspace:
		o.ProjectID = ""
	case models.ScopeProject:
		if o.ProjectID == "" && o.ResourceID == "" {
			return "", apperr.New(apperr.InvalidInput, "Say which project it's for.")
		}
		if o.ProjectID != "" {
			var p models.Project
			if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), o.ProjectID, &p); err != nil {
				return "", apperr.New(apperr.NotFound, "That project was not found. It may have been deleted.").WithDetails(map[string]any{"project": o.ProjectID})
			}
		}
	default:
		return "", apperr.New(apperr.InvalidInput, "Choose where it goes: a project, or the workspace library.")
	}
	return scope, nil
}

// sameBytes finds a resource that already holds these bytes where the target
// can see it: the same project, or the workspace library.
func sameBytes(ctx context.Context, a *app.App, sha, scope, projectID string) *models.Resource {
	var list []models.Resource
	if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"versions.sha256": sha, "removed": bson.M{"$ne": true}}, &list); err != nil {
		return nil
	}
	for i := range list {
		r := &list[i]
		if r.Scope == models.ScopeWorkspace {
			return r
		}
		if scope == models.ScopeProject && r.ProjectID == projectID {
			return r
		}
	}
	return nil
}

// insertUnique inserts r, numbering its name (logo-2…) when the name is taken
// in its scope, unless the name was asked for explicitly.
func insertUnique(ctx context.Context, a *app.App, r *models.Resource, exact bool) error {
	return insertUniqueOr(ctx, a, r, exact, nil)
}

// errFileKept: the repository file a picture is for already has its picture
// (one per file, D216: migration 40's unique index).
var errFileKept = errors.New("that repository file is already a picture")

// insertUniqueOr is insertUnique that stops with errFileKept when a duplicate
// is the file's picture rather than the name (kept says so; nil never).
func insertUniqueOr(ctx context.Context, a *app.App, r *models.Resource, exact bool, kept func() bool) error {
	base := r.Name
	for n := 1; n <= 99; n++ {
		if n > 1 {
			if exact {
				return apperr.New(apperr.Conflict, "There's already a resource called %s here.", base).WithHint("Choose another name, or add the file as a new version of it.")
			}
			r.Name = numbered(base, n)
		}
		err := a.DB.Insert(ctx, store.CResources, *r)
		if err == nil {
			return nil
		}
		if !store.IsDuplicateKey(err) {
			return err
		}
		if kept != nil && kept() {
			return errFileKept
		}
	}
	return apperr.New(apperr.Conflict, "Too many resources are called %s; choose another name.", base)
}

func cleanTags(tags []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, t := range tags {
		t = strings.ToLower(strings.Join(strings.Fields(t), " "))
		if t == "" || len(t) > 40 || seen[t] {
			continue
		}
		seen[t] = true
		out = append(out, t)
	}
	return out
}

// CleanTags normalizes people's tags (lowercase, trimmed, unique).
func CleanTags(tags []string) []string { return cleanTags(tags) }

// mergeAuto keeps suggestions from earlier versions that the new one does not
// contradict (filename tags stay; picture tags follow the latest).
func mergeAuto(old, fresh []models.ResourceTag) []models.ResourceTag {
	byTag := map[string]models.ResourceTag{}
	for _, t := range old {
		if t.Source != "picture" {
			byTag[t.Tag] = t
		}
	}
	for _, t := range fresh {
		if cur, ok := byTag[t.Tag]; !ok || t.Confidence >= cur.Confidence {
			byTag[t.Tag] = t
		}
	}
	out := make([]models.ResourceTag, 0, len(byTag))
	for _, t := range byTag {
		out = append(out, t)
	}
	sortTags(out)
	return out
}

func sortTags(ts []models.ResourceTag) {
	for i := 1; i < len(ts); i++ {
		for j := i; j > 0 && ts[j].Tag < ts[j-1].Tag; j-- {
			ts[j], ts[j-1] = ts[j-1], ts[j]
		}
	}
}

// putPrepared uploads a file under its content key unless the store has it.
func putPrepared(ctx context.Context, a *app.App, path, key, sha, mime string) error {
	if info, err := a.Media.Stat(ctx, key); err == nil && info.Size > 0 {
		return nil
	}
	_, err := a.Media.PutFile(ctx, key, path, media.PutOptions{ContentType: mime, SHA256: sha}, media.Resume{}, nil)
	if err != nil {
		return fmt.Errorf("store the file: %w", err)
	}
	return nil
}

// putFile stores a derived file (a PDF page) content-addressed.
func putFile(ctx context.Context, a *app.App, path, mime string) (*models.ResourceObject, error) {
	sha, err := media.HashFile(path)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if err := putPrepared(ctx, a, path, key, sha, mime); err != nil {
		return nil, err
	}
	_, _ = a.Cache.Adopt(path, sha)
	return &models.ResourceObject{SHA256: sha, Size: st.Size(), MIME: mime, StorageKey: key}, nil
}

// putBytes stores generated bytes (a thumbnail).
func putBytes(ctx context.Context, a *app.App, dir string, b []byte, mime string) (*models.ResourceObject, error) {
	tok := make([]byte, 6)
	_, _ = rand.Read(tok)
	p := filepath.Join(dir, "obj-"+hex.EncodeToString(tok))
	if err := os.WriteFile(p, b, 0o644); err != nil {
		return nil, err
	}
	return putFile(ctx, a, p, mime)
}

func pngSize(path string) (int, int) {
	f, err := os.Open(path)
	if err != nil {
		return 0, 0
	}
	defer f.Close()
	cfg, _, err := image.DecodeConfig(f)
	if err != nil {
		return 0, 0
	}
	return cfg.Width, cfg.Height
}

package resources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/store"
)

// Where a visible resource comes from.
const (
	FromHere      = "project"   // the project's own
	FromWorkspace = "workspace" // the workspace library
	FromLinked    = "linked"    // a linked project (Entry.SourceID says which)
)

// Entry is a resource as one project sees it.
type Entry struct {
	Resource models.Resource
	// Origin is project, workspace or linked; SourceID the linked project.
	Origin   string
	SourceID string
}

// ListOptions narrow a listing.
type ListOptions struct {
	Kind  string // image, svg, pdf, font
	Tag   string // a person's tag or a suggestion
	Query string // words in the name, title, tags or description
	// Only: project (its own), workspace (the library), linked (from linked
	// projects); empty = all a project sees.
	Only string
	// Removed includes removed resources (they still render where pinned).
	Removed bool
	// Folder: only resources from this linked folder.
	Folder string
}

// Visible lists what a project can use: its own resources, the workspace
// library and what its links bring in. projectID "" lists the workspace
// library. Newest first within each origin (own, then linked, then workspace).
func Visible(ctx context.Context, a *app.App, projectID string, view *projectlinks.View, o ListOptions) ([]Entry, error) {
	or := bson.A{bson.M{"scope": models.ScopeWorkspace}}
	if projectID != "" {
		or = append(or, bson.M{"scope": models.ScopeProject, "projectId": projectID})
		if src := view.SourceIDs(); len(src) > 0 {
			or = append(or, bson.M{"scope": models.ScopeProject, "projectId": bson.M{"$in": src}})
		}
	}
	filter := bson.M{"$or": or}
	if !o.Removed {
		filter["removed"] = bson.M{"$ne": true}
	}
	if o.Kind != "" {
		filter["kind"] = o.Kind
	}
	if o.Folder != "" {
		filter["folder.folderId"] = o.Folder
	}
	var list []models.Resource
	if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), filter, &list); err != nil {
		return nil, err
	}
	var out []Entry
	for i := range list {
		r := list[i]
		e := Entry{Resource: r}
		switch {
		case r.Scope == models.ScopeWorkspace:
			e.Origin = FromWorkspace
		case r.ProjectID == projectID:
			e.Origin = FromHere
		default:
			src, ok := view.From(projectlinks.ResourceItem(&r))
			if !ok || src == "" {
				continue
			}
			e.Origin, e.SourceID = FromLinked, src
		}
		if o.Only != "" && o.Only != e.Origin && !(o.Only == "project" && e.Origin == FromHere) {
			continue
		}
		if o.Tag != "" && !hasTag(&r, o.Tag) {
			continue
		}
		if q := strings.ToLower(strings.TrimSpace(o.Query)); q != "" && !matches(&r, q) {
			continue
		}
		out = append(out, e)
	}
	rank := map[string]int{FromHere: 0, FromLinked: 1, FromWorkspace: 2}
	sort.SliceStable(out, func(i, j int) bool {
		if rank[out[i].Origin] != rank[out[j].Origin] {
			return rank[out[i].Origin] < rank[out[j].Origin]
		}
		return out[i].Resource.UpdatedAt.After(out[j].Resource.UpdatedAt)
	})
	return out, nil
}

func hasTag(r *models.Resource, tag string) bool {
	tag = strings.ToLower(strings.TrimSpace(tag))
	for _, t := range r.AllTags() {
		if t == tag {
			return true
		}
	}
	return false
}

// matches: every word of q is in the name, title, a tag, the description or
// the text found in the picture.
func matches(r *models.Resource, q string) bool {
	hay := strings.ToLower(strings.Join([]string{r.Name, r.Title, strings.Join(r.AllTags(), " "), r.Description, latestText(r)}, " "))
	for _, w := range strings.Fields(q) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

func latestText(r *models.Resource) string {
	if v := r.Latest(); v != nil {
		return v.Text
	}
	return ""
}

// Find resolves a resource reference as a project sees it: an ID, or a name
// (the project's own first, then the workspace library, then linked projects
// in link order). Removed resources resolve by ID only.
func Find(ctx context.Context, a *app.App, projectID string, view *projectlinks.View, ref string) (*Entry, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say which resource: its short name or its title.")
	}
	if ids.Valid(ids.Resource, ref) {
		var r models.Resource
		if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), ref, &r); err != nil {
			return nil, apperr.New(apperr.NotFound, "That resource was not found. It may have been removed.").WithDetails(map[string]any{"resource": ref})
		}
		e := &Entry{Resource: r, Origin: FromWorkspace}
		switch {
		case r.Scope == models.ScopeProject && r.ProjectID == projectID:
			e.Origin = FromHere
		case r.Scope == models.ScopeProject:
			e.Origin = FromLinked
			e.SourceID, _ = view.From(projectlinks.ResourceItem(&r))
			if e.SourceID == "" {
				e.SourceID = r.ProjectID
			}
		}
		return e, nil
	}
	list, err := Visible(ctx, a, projectID, view, ListOptions{})
	if err != nil {
		return nil, err
	}
	key := strings.ToLower(ref)
	var best *Entry
	for i := range list {
		e := &list[i]
		if e.Resource.Name == key || strings.EqualFold(e.Resource.Title, ref) {
			if best == nil || originRank(e.Origin) < originRank(best.Origin) {
				best = e
			}
		}
	}
	if best == nil {
		if projectID == "" {
			return nil, apperr.New(apperr.NotFound, "There's no resource called %q in the workspace library.", ref).
				WithHint("A project's Resources tab lists what it can use, the workspace library included.").WithCommand("metarush resources list")
		}
		return nil, apperr.New(apperr.NotFound, "There's no resource called %q here.", ref).
			WithHint("The project's Resources tab lists what it can use.").WithCommand("metarush resources list --project " + projectID)
	}
	return best, nil
}

func originRank(o string) int {
	switch o {
	case FromHere:
		return 0
	case FromWorkspace:
		return 1
	}
	return 2
}

// LocalFile returns a version's file on this machine (fetched into the cache
// from the media store when needed). page > 0 picks a PDF page picture.
func LocalFile(ctx context.Context, a *app.App, r *models.Resource, sha string, page int) (string, *models.ResourceVersion, error) {
	v := r.Latest()
	if sha != "" {
		v = r.Version(sha)
	}
	if v == nil {
		return "", nil, missingVersion(r, sha)
	}
	if page > 0 {
		if page > len(v.Pages) {
			if len(v.Pages) == 0 {
				return "", v, apperr.New(apperr.NotConfigured, "%s's pages aren't pictures yet. %s", r.Title, PopplerNote).WithCommand(PopplerCommand)
			}
			return "", v, apperr.New(apperr.InvalidInput, "%s has %d pages as pictures, so there's no page %d.", r.Title, len(v.Pages), page)
		}
		pg := v.Pages[page-1]
		p, err := a.Cache.Ensure(ctx, a.Media, pg.StorageKey, pg.SHA256, pg.Size, nil)
		return p, v, err
	}
	if a.Cache.Has(v.SHA256, v.Size) {
		return a.Cache.Path(v.SHA256), v, nil
	}
	if !v.Stored {
		// Kept back in a linked folder: this machine may be the one that has it.
		if err := AdoptFromFolder(a, r, v.SHA256); err == nil && a.Cache.Has(v.SHA256, v.Size) {
			return a.Cache.Path(v.SHA256), v, nil
		}
		return "", v, apperr.New(apperr.NotConfigured, "%s is still only on %s; it's uploaded when a cut first uses it.", r.Title, holdersText(ctx, a, v.Holders)).
			WithDetails(map[string]any{"reason": "not_uploaded", "holders": v.Holders})
	}
	p, err := a.Cache.Ensure(ctx, a.Media, v.StorageKey, v.SHA256, v.Size, nil)
	return p, v, err
}

// AdoptFromFolder puts a version back in the cache from its linked folder on
// this machine (a large file kept back until a cut uses it, or one the cache
// let go), checking it is still the same file.
func AdoptFromFolder(a *app.App, r *models.Resource, sha string) error {
	if r.Folder == nil {
		return fmt.Errorf("this machine no longer has %s", r.Title)
	}
	for _, fp := range a.Cfg.Resources.Folders {
		if fp.ID != r.Folder.FolderID {
			continue
		}
		p := filepath.Join(fp.Path, filepath.FromSlash(r.Folder.RelPath))
		if _, err := os.Stat(p); err != nil {
			return fmt.Errorf("%s is no longer in its folder on this machine", r.Title)
		}
		got, err := media.HashFile(p)
		if err != nil {
			return err
		}
		if got != sha {
			return fmt.Errorf("%s changed in its folder; the version a cut uses isn't there any more", r.Title)
		}
		_, err = a.Cache.Adopt(p, sha)
		return err
	}
	return fmt.Errorf("this machine doesn't have %s's folder", r.Title)
}

// holdersText names the machines that have a file ("Ada's laptop").
func holdersText(ctx context.Context, a *app.App, holders []string) string {
	if len(holders) == 0 {
		return "another machine"
	}
	var ins []models.Installation
	_ = a.DB.Find(ctx, store.CInstallations, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": holders}}, &ins)
	var names []string
	for _, i := range ins {
		names = append(names, i.Name)
	}
	if len(names) == 0 {
		return "another machine"
	}
	return strings.Join(names, " and ")
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// UploadVersion copies a version that was kept on this machine to the media
// store (a linked folder's large file, the first time a cut uses it).
func UploadVersion(ctx context.Context, a *app.App, r *models.Resource, sha string) error {
	v := r.Version(sha)
	if v == nil {
		return missingVersion(r, sha)
	}
	if v.Stored {
		return nil
	}
	if !a.Cache.Has(v.SHA256, v.Size) {
		return apperr.New(apperr.NotConfigured, "This machine doesn't have %s any more.", r.Title)
	}
	if err := putPrepared(ctx, a, a.Cache.Path(v.SHA256), v.StorageKey, v.SHA256, v.MIME); err != nil {
		return err
	}
	_, err := a.DB.C(store.CResources).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID(), "versions.sha256": sha},
		bson.M{"$set": bson.M{"versions.$.stored": true, "updatedAt": time.Now().UTC()}})
	if err == nil {
		v.Stored = true
		a.Audit(ctx, "resource.upload", "resource", r.ID, r.ProjectID, map[string]any{"sha256": sha, "size": v.Size})
	}
	return err
}

// UpdateFields are the edits people make to a resource.
type UpdateFields struct {
	Title       *string
	Name        *string
	Tags        *[]string
	AddTags     []string
	RemoveTags  []string
	Description *string
	License     *string
}

// Update edits a resource's words (title, name, tags, description, licence).
func Update(ctx context.Context, a *app.App, r *models.Resource, f UpdateFields) error {
	set := bson.M{"updatedAt": time.Now().UTC()}
	if f.Title != nil {
		t := strings.Join(strings.Fields(*f.Title), " ")
		if t == "" || len([]rune(t)) > 200 {
			return apperr.New(apperr.InvalidInput, "A title needs 1 to 200 characters.")
		}
		set["title"] = t
		r.Title = t
	}
	if f.Name != nil {
		n := strings.TrimSpace(*f.Name)
		if !NamePattern.MatchString(n) {
			return apperr.New(apperr.InvalidInput, "%q can't be a resource's short name: use lowercase letters, digits, - and _, starting with a letter.", n)
		}
		set["name"] = n
	}
	tags := r.Tags
	if f.Tags != nil {
		tags = cleanTags(*f.Tags)
	}
	if len(f.AddTags) > 0 {
		tags = cleanTags(append(append([]string{}, tags...), f.AddTags...))
	}
	if len(f.RemoveTags) > 0 {
		drop := map[string]bool{}
		for _, t := range cleanTags(f.RemoveTags) {
			drop[t] = true
		}
		var kept []string
		for _, t := range tags {
			if !drop[t] {
				kept = append(kept, t)
			}
		}
		tags = kept
		// Removing a suggestion is a person saying "not this": drop it from the suggestions too.
		var auto []models.ResourceTag
		for _, t := range r.AutoTags {
			if !drop[t.Tag] {
				auto = append(auto, t)
			}
		}
		set["autoTags"] = auto
		r.AutoTags = auto
	}
	if f.Tags != nil || len(f.AddTags)+len(f.RemoveTags) > 0 {
		if tags == nil {
			tags = []string{}
		}
		set["tags"] = tags
		r.Tags = tags
	}
	if f.Description != nil {
		d := strings.TrimSpace(*f.Description)
		if len([]rune(d)) > 2000 {
			return apperr.New(apperr.InvalidInput, "A description can be up to 2,000 characters.")
		}
		set["description"] = d
		r.Description = d
	}
	if f.License != nil {
		l := strings.TrimSpace(*f.License)
		if len([]rune(l)) > 500 {
			return apperr.New(apperr.InvalidInput, "A licence note can be up to 500 characters.")
		}
		set["license"] = l
		r.License = l
	}
	err := a.DB.Update(ctx, store.CResources, a.WorkspaceID(), r.ID, bson.M{"$set": set})
	if store.IsDuplicateKey(err) {
		return apperr.New(apperr.Conflict, "There's already a resource called %s here.", *f.Name)
	}
	if err != nil {
		return err
	}
	if f.Name != nil {
		r.Name = strings.TrimSpace(*f.Name)
	}
	return nil
}

// Promote makes a project resource part of the workspace library (every
// project sees it). Its versions, and the cuts that pin them, don't change.
func Promote(ctx context.Context, a *app.App, r *models.Resource) error {
	if r.Scope == models.ScopeWorkspace {
		return apperr.New(apperr.InvalidInput, "%s is already in the workspace library.", r.Title)
	}
	from := r.ProjectID
	name := r.Name
	for n := 1; n <= 99; n++ {
		if n > 1 {
			name = numbered(r.Name, n)
		}
		_, err := a.DB.C(store.CResources).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()},
			bson.M{"$set": bson.M{"scope": models.ScopeWorkspace, "name": name, "promotedFrom": from, "updatedAt": time.Now().UTC()}, "$unset": bson.M{"projectId": ""}})
		if err == nil {
			r.Scope, r.Name, r.ProjectID, r.PromotedFrom = models.ScopeWorkspace, name, "", from
			a.Audit(ctx, "resource.promote", "resource", r.ID, from, map[string]any{"name": name})
			if r.Kind == models.ResourceRepo {
				// The pictures a repository keeps go with it (D216).
				var pics []models.Resource
				_ = a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"repoFile.repoId": r.ID, "scope": models.ScopeProject}, &pics)
				for i := range pics {
					_ = Promote(ctx, a, &pics[i])
				}
			}
			return nil
		}
		if !store.IsDuplicateKey(err) {
			return err
		}
	}
	return apperr.New(apperr.Conflict, "The workspace library already has too many resources called %s; rename this one first.", r.Name)
}

var removedSuffix = regexp.MustCompile(`-removed-[0-9a-z]{4}$`)

// Remove hides a resource from lists and pickers. Nothing is deleted: cuts
// that pin one of its versions keep rendering. Its name is freed for a new
// resource. restore brings it back.
func Remove(ctx context.Context, a *app.App, r *models.Resource, restore bool) error {
	now := time.Now().UTC()
	if restore {
		name := removedSuffix.ReplaceAllString(r.Name, "")
		for n := 1; n <= 99; n++ {
			if n > 1 {
				name = numbered(removedSuffix.ReplaceAllString(r.Name, ""), n)
			}
			_, err := a.DB.C(store.CResources).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID()},
				bson.M{"$set": bson.M{"removed": false, "name": name, "updatedAt": now}, "$unset": bson.M{"removedAt": "", "removedBy": ""}})
			if err == nil {
				r.Removed, r.Name = false, name
				a.Audit(ctx, "resource.restore", "resource", r.ID, r.ProjectID, nil)
				return nil
			}
			if !store.IsDuplicateKey(err) {
				return err
			}
		}
		return apperr.New(apperr.Conflict, "Too many resources are called %s; rename one first.", name)
	}
	if r.Removed {
		return nil
	}
	actor := a.Actor
	freed := r.Name
	if !removedSuffix.MatchString(freed) {
		tail := r.ID[len(r.ID)-4:]
		freed = r.Name
		if len(freed) > 48-13 {
			freed = strings.TrimRight(freed[:48-13], "-")
		}
		freed += "-removed-" + tail
	}
	if err := a.DB.Update(ctx, store.CResources, a.WorkspaceID(), r.ID, bson.M{"$set": bson.M{"removed": true, "removedAt": now, "removedBy": actor, "name": freed, "updatedAt": now}}); err != nil {
		return err
	}
	r.Removed, r.RemovedAt, r.RemovedBy, r.Name = true, &now, &actor, freed
	a.Audit(ctx, "resource.remove", "resource", r.ID, r.ProjectID, map[string]any{"title": r.Title})
	return nil
}

// Refresh describes a resource's latest version again (after installing
// poppler, or when MetaRush learned new things to notice) and re-runs the
// taggers. It never changes the file.
func Refresh(ctx context.Context, a *app.App, r *models.Resource) ([]string, error) {
	v := r.Latest()
	if v == nil {
		return nil, apperr.New(apperr.NotFound, "%s has no versions.", r.Title)
	}
	path, _, err := LocalFile(ctx, a, r, v.SHA256, 0)
	if err != nil {
		return nil, err
	}
	var notes []string
	palette := 0
	set := bson.M{}
	switch r.Kind {
	case models.ResourceImage:
		pi, err := ReadPicture(path)
		if err != nil {
			return nil, err
		}
		palette = pi.Palette
		set["versions.$.width"], set["versions.$.height"], set["versions.$.hasAlpha"], set["versions.$.colors"] = pi.Width, pi.Height, pi.HasAlpha, pi.Colors
		v.Width, v.Height, v.HasAlpha, v.Colors = pi.Width, pi.Height, pi.HasAlpha, pi.Colors
	case models.ResourceSVG:
		b, err := readSmall(path)
		if err != nil {
			return nil, err
		}
		si := ReadSVG(b)
		palette = si.Palette
		set["versions.$.width"], set["versions.$.height"], set["versions.$.colors"] = si.Width, si.Height, si.Colors
		v.Width, v.Height, v.Colors = si.Width, si.Height, si.Colors
	case models.ResourcePDF:
		if len(v.Pages) == 0 {
			work, err := makeTemp(a)
			if err != nil {
				return nil, err
			}
			defer removeAll(work)
			pages, count, err := PDFPages(ctx, a.Tools, path, work)
			if errors.Is(err, ErrNoPoppler) {
				return nil, apperr.New(apperr.NotConfigured, "%s", PopplerNote).WithCommand(PopplerCommand)
			}
			if err != nil {
				return nil, apperr.Wrap(apperr.NotConfigured, err, "Its pages couldn't be made into pictures; the PDF itself is kept.")
			}
			var ps []models.ResourcePage
			for i, p := range pages {
				obj, err := putFile(ctx, a, p, "image/png")
				if err != nil {
					return nil, err
				}
				w, h := pngSize(p)
				ps = append(ps, models.ResourcePage{Number: i + 1, SHA256: obj.SHA256, Size: obj.Size, StorageKey: obj.StorageKey, Width: w, Height: h})
			}
			v.Pages, v.PageCount = ps, count
			set["versions.$.pages"], set["versions.$.pageCount"] = ps, count
			if len(ps) > 0 {
				set["versions.$.width"], set["versions.$.height"] = ps[0].Width, ps[0].Height
				v.Width, v.Height = ps[0].Width, ps[0].Height
				if thumb, tw, th, err := ThumbFromPNG(pages[0]); err == nil {
					if obj, err := putBytes(ctx, a, work, thumb, "image/png"); err == nil {
						obj.Width, obj.Height = tw, th
						set["versions.$.thumbnail"] = obj
					}
				}
				notes = append(notes, fmt.Sprintf("Made %d page pictures.", len(ps)))
			}
		}
	}
	// Page pictures (a PDF's first pages) for the visual index to look at.
	var pagePaths []string
	for i, pg := range v.Pages {
		if i >= 3 {
			break
		}
		if p, err := a.Cache.Ensure(ctx, a.Media, pg.StorageKey, pg.SHA256, pg.Size, nil); err == nil {
			pagePaths = append(pagePaths, p)
		}
	}
	look := TagInput{Name: r.Title + "." + v.Ext, Kind: r.Kind, Version: v, Path: path, Pages: pagePaths, Palette: palette, Tools: a.Tools}
	tags := AutoTags(ctx, look)
	set["autoTags"] = mergeAuto(nil, append(filenameKept(r.AutoTags), tags...))
	if t := FindText(ctx, look); t != "" {
		set["versions.$.text"] = t
	}
	set["updatedAt"] = time.Now().UTC()
	if _, err := a.DB.C(store.CResources).UpdateOne(ctx, bson.M{"_id": r.ID, "workspaceId": a.WorkspaceID(), "versions.sha256": v.SHA256}, bson.M{"$set": set}); err != nil {
		return nil, err
	}
	_ = a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), r.ID, r)
	return notes, nil
}

func filenameKept(ts []models.ResourceTag) []models.ResourceTag {
	var out []models.ResourceTag
	for _, t := range ts {
		if t.Source == "filename" {
			out = append(out, t)
		}
	}
	return out
}

// missingVersion is the error for a version a resource doesn't have (the
// version's fingerprint stays in Details).
func missingVersion(r *models.Resource, sha string) error {
	return apperr.New(apperr.NotFound, "%s doesn't have that version any more.", r.Title).WithDetails(map[string]any{"version": short(sha)})
}

// ObjectKey is where a resource file lives in the media store.
func ObjectKey(workspaceID, sha string) string { return media.ObjectKey(workspaceID, sha) }

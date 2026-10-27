package ops

import (
	"context"
	"fmt"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/projectlinks"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Pictures from a repository (D216): a repository keeps the pictures under
// some of its folders or files as pictures of their own, pinned to the
// commit (repo@commit:path) and refreshed with it. resources.pictures lists
// what a version kept; resources.pictures.set changes the paths and brings
// the new ones' pictures in at the commit the repository shows (a fetch job
// on this machine).

// RepoPicturesIn names a repository, a version of it and a path in it.
type RepoPicturesIn struct {
	ResourceRefIn
	Version string `json:"version,omitempty" jsonschema:"description=A version of the repository (a commit or version hash; first characters are enough); default the current one."`
	Path    string `json:"path,omitempty" jsonschema:"maxLength=512,description=Only the pictures under this folder or file."`
}

// RepoPicturesSetIn changes the folders and files a repository keeps pictures from.
type RepoPicturesSetIn struct {
	ResourceRefIn
	Keep []string `json:"keep,omitempty" jsonschema:"maxItems=20,description=Folders or files whose pictures to keep (like art/characters; . is the whole repository). They come in at the commit the repository shows."`
	Drop []string `json:"drop,omitempty" jsonschema:"maxItems=20,description=Paths to stop keeping in step: the pictures kept from them stay\\, and cuts that show them keep them."`
}

// RepoPictureRow is one picture a repository version kept.
type RepoPictureRow struct {
	Path       string `json:"path" jsonschema:"description=The file in the repository."`
	ResourceID string `json:"resourceId"`
	Name       string `json:"name" jsonschema:"description=The short name graphics use."`
	Title      string `json:"title"`
	Kind       string `json:"kind" jsonschema:"enum=image,enum=svg"`
	// SHA256 is the picture's version from this commit; Newer says it has a newer one (a later commit changed it).
	SHA256   string `json:"sha256"`
	Newer    bool   `json:"newer,omitempty"`
	Bytes    int64  `json:"bytes"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
	ThumbURL string `json:"thumbUrl"`
	FileURL  string `json:"fileUrl"`
	// Removed: a person took it out of the library (cuts that show it keep it).
	Removed bool `json:"removed,omitempty"`
}

// RepoPictures is what a repository keeps from its picture paths at one version.
type RepoPictures struct {
	// Paths are the folders or files it keeps pictures from (. is the whole repository).
	Paths       []string  `json:"paths"`
	Commit      string    `json:"commit"`
	CommittedAt time.Time `json:"committedAt"`
	// UpToDate: this version kept the pictures of every path it keeps now
	// (false: the new paths' pictures come in with the fetch on its way).
	UpToDate bool `json:"upToDate"`
	// Kept counts the pictures; Pictures lists them (all, or the first ones
	// in the repository's overview).
	Kept     int              `json:"kept"`
	Pictures []RepoPictureRow `json:"pictures"`
	// Skipped counts the files under the paths left out; SkippedWhy says why
	// in words, LeftOut lists them (the first 100).
	Skipped    int                  `json:"skipped,omitempty"`
	SkippedWhy []string             `json:"skippedWhy,omitempty"`
	LeftOut    []models.RepoLeftOut `json:"leftOut,omitempty"`
	// Summary says it in a sentence.
	Summary string `json:"summary"`
}

// RepoPicturesOut is a repository's pictures, and what a change did.
type RepoPicturesOut struct {
	Repo ResourceRow `json:"repo"`
	RepoPictures
	// Message says what changed; JobID is the fetch that brings new paths' pictures in.
	Message string   `json:"message,omitempty"`
	Notes   []string `json:"notes,omitempty"`
	JobID   string   `json:"jobId,omitempty"`
	// Note says when the fetch starts if MetaRush isn't running on this machine now.
	Note string `json:"note,omitempty"`
}

// repoOf finds a repository resource as a project sees it.
func repoOf(ctx context.Context, a *app.App, in ResourceRefIn) (*resources.Entry, *projectlinks.View, error) {
	e, view, err := findResource(ctx, a, in)
	if err != nil {
		return nil, nil, err
	}
	if e.Resource.Kind != models.ResourceRepo {
		return nil, nil, apperr.New(apperr.InvalidInput, "“%s” is a %s, not a repository: only a repository keeps the pictures in its folders.", e.Resource.Title, resources.KindWord(e.Resource.Kind))
	}
	return e, view, nil
}

// repoVersion is a repository's version by commit or hash ("" the current one).
func repoVersion(r *models.Resource, ref string) (*models.ResourceVersion, error) {
	ref = strings.ToLower(strings.TrimSpace(ref))
	if ref == "" {
		if v := r.Latest(); v != nil && v.Repo != nil {
			return v, nil
		}
		return nil, apperr.New(apperr.NotFound, "%s has no commit yet.", r.Title)
	}
	for i := range r.Versions {
		x := &r.Versions[i]
		if x.Repo != nil && (strings.HasPrefix(x.SHA256, ref) || strings.HasPrefix(x.Repo.Commit, ref)) {
			return x, nil
		}
	}
	return nil, apperr.New(apperr.NotFound, "%s has no version %s.", r.Title, ref)
}

// RepoPicturesOf says what a repository version kept from its picture paths
// (under a path when one is given), with up to limit pictures listed (0 all).
func RepoPicturesOf(ctx context.Context, a *app.App, r *models.Resource, v *models.ResourceVersion, under string, limit int) (*RepoPictures, error) {
	out := &RepoPictures{Paths: r.PicturePaths, Pictures: []RepoPictureRow{}}
	if out.Paths == nil {
		out.Paths = []string{}
	}
	if v == nil || v.Repo == nil {
		out.Summary = "It has no commit yet."
		return out, nil
	}
	rs := v.Repo
	out.Commit, out.CommittedAt = rs.Commit, rs.CommittedAt
	out.UpToDate = v.SHA256 == r.Current && resources.SamePicturePaths(rs.PicturePaths, r.PicturePaths)
	pics := resources.KeptPictures(r, v, under)
	out.Kept = len(pics)
	var idsList []string
	for _, p := range pics {
		idsList = append(idsList, p.ResourceID)
	}
	byID := map[string]*models.Resource{}
	if len(idsList) > 0 {
		var list []models.Resource
		if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"_id": bson.M{"$in": idsList}}, &list); err != nil {
			return nil, err
		}
		for i := range list {
			byID[list[i].ID] = &list[i]
		}
	}
	for _, p := range pics {
		if limit > 0 && len(out.Pictures) >= limit {
			break
		}
		pr := byID[p.ResourceID]
		if pr == nil {
			continue
		}
		row := RepoPictureRow{Path: p.Path, ResourceID: pr.ID, Name: pr.Name, Title: pr.Title, Kind: pr.Kind, SHA256: p.SHA256, Newer: pr.Current != p.SHA256, Bytes: p.Bytes,
			ThumbURL: "/media/resources/" + pr.ID + "/thumb?v=" + shortHash(p.SHA256), FileURL: "/media/resources/" + pr.ID + "/file?v=" + shortHash(p.SHA256), Removed: pr.Removed}
		if pv := pr.Version(p.SHA256); pv != nil {
			row.Width, row.Height = pv.Width, pv.Height
		}
		out.Pictures = append(out.Pictures, row)
	}
	for _, l := range rs.PicturesLeftOut {
		if resources.UnderPicturePath(l.Path, under) && resources.PicturePathOf(l.Path, r.PicturePaths) != "" {
			out.LeftOut = append(out.LeftOut, l)
		}
	}
	if (under == "" || under == resources.WholeRepo) && resources.SamePicturePaths(rs.PicturePaths, r.PicturePaths) {
		out.Skipped, out.SkippedWhy = rs.PicturesSkipped, rs.PicturesSkippedWhy // all of them, beyond the ones listed
	} else {
		out.Skipped, out.SkippedWhy = len(out.LeftOut), resources.LeftOutWords(out.LeftOut)
	}
	out.Summary = picturesSummary(out, len(rs.Pictures), v.SHA256 == r.Current)
	return out, nil
}

func shortHash(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// picturesSummary says what a repository keeps, in a sentence (indexed: the
// pictures the version kept, its paths since dropped included; current: the
// version is the one the repository shows).
func picturesSummary(p *RepoPictures, indexed int, current bool) string {
	if len(p.Paths) == 0 {
		switch {
		case indexed == 1:
			return "It no longer keeps pictures in step; the picture it kept stays in the library."
		case indexed > 1:
			return fmt.Sprintf("It no longer keeps pictures in step; the %s it kept stay in the library.", plural(indexed, "picture"))
		}
		return "It keeps no pictures: only its text files."
	}
	where := "“" + strings.Join(p.Paths, "”, “") + "”"
	if len(p.Paths) == 1 && p.Paths[0] == resources.WholeRepo {
		where = "the whole repository"
	}
	s := fmt.Sprintf("%s kept from %s, as it was on %s", upperFirstWord(plural(p.Kept, "picture")), where, p.CommittedAt.UTC().Format("2 Jan 2006"))
	if p.Skipped > 0 {
		s += fmt.Sprintf("; %s left out", plural(p.Skipped, "file"))
	}
	s += "."
	if !p.UpToDate && current {
		s += " The pictures of the folders just added come in with the fetch on its way."
	}
	return s
}

func upperFirstWord(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

// SetRepoPictures changes the paths a repository keeps pictures from and
// brings the new ones' pictures in (resources.pictures.set).
func SetRepoPictures(ctx context.Context, a *app.App, in RepoPicturesSetIn) (*RepoPicturesOut, error) {
	e, view, err := repoOf(ctx, a, in.ResourceRefIn)
	if err != nil {
		return nil, err
	}
	r := &e.Resource
	if r.Removed {
		return nil, apperr.New(apperr.Conflict, "“%s” was removed from the library; bring it back first.", r.Title)
	}
	if len(in.Keep)+len(in.Drop) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "Say which folder of the repository to keep the pictures of, or which to stop keeping.")
	}
	v, err := repoVersion(r, "")
	if err != nil {
		return nil, err
	}
	ch, err := resources.ChangePicturePaths(r.PicturePaths, in.Keep, in.Drop)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	upd := bson.M{"$set": bson.M{"picturePaths": ch.Paths, "updatedAt": now}}
	if len(ch.Paths) == 0 {
		upd = bson.M{"$set": bson.M{"updatedAt": now}, "$unset": bson.M{"picturePaths": ""}}
	}
	if len(ch.Added)+len(ch.Dropped) > 0 {
		if err := a.DB.Update(ctx, store.CResources, a.WorkspaceID(), r.ID, upd); err != nil {
			return nil, err
		}
		r.PicturePaths = ch.Paths
		a.Audit(ctx, "resource.pictures.set", "resource", r.ID, r.ProjectID, map[string]any{"paths": ch.Paths, "added": ch.Added, "dropped": ch.Dropped})
	}
	out := &RepoPicturesOut{Notes: ch.Notes}
	var msgs []string
	if len(ch.Added) > 0 {
		q, err := resources.QueueFetch(ctx, a, resources.FetchRequest{Kind: resources.FetchRepo, URL: v.Source.URL, Ref: v.Repo.Commit, ResourceID: r.ID,
			ProjectID: r.ProjectID, Workspace: r.Scope == models.ScopeWorkspace, PicturesOnly: true})
		if err != nil {
			return nil, err
		}
		out.JobID = q.JobID
		msgs = append(msgs, fmt.Sprintf("Keeping the pictures in “%s” of %s as it was on %s: MetaRush is getting them.", strings.Join(ch.Added, "”, “"), r.Title,
			v.Repo.CommittedAt.UTC().Format("2 Jan 2006")))
		ws, _ := OnlineWorkers(ctx, a, jobs.TypeFetch)
		running := false
		for _, w := range ws {
			running = running || w.ID == a.Cfg.Installation.ID
		}
		if !running {
			out.Note = "It starts as soon as MetaRush is running on " + a.Cfg.Installation.Name + "."
		}
	}
	if len(ch.Dropped) > 0 {
		msgs = append(msgs, fmt.Sprintf("No longer keeps “%s” in step: the pictures kept from it stay, and cuts that show them keep them.", strings.Join(ch.Dropped, "”, “")))
	}
	if len(msgs) == 0 {
		msgs = append(msgs, "Nothing changed.")
	}
	out.Message = strings.Join(msgs, " ")
	pics, err := RepoPicturesOf(ctx, a, r, v, "", 0)
	if err != nil {
		return nil, err
	}
	out.RepoPictures = *pics
	out.Repo = ResourceRowOf(*e, view, folderNames(ctx, a))
	return out, nil
}

func init() {
	Def("resources.pictures", "List the pictures a GitHub repository keeps from its folders: each a picture of its own pinned to the commit, with what was left out and why", Flags{}, func(ctx context.Context, a *app.App, in *RepoPicturesIn) (*RepoPicturesOut, error) {
		e, view, err := repoOf(ctx, a, in.ResourceRefIn)
		if err != nil {
			return nil, err
		}
		v, err := repoVersion(&e.Resource, in.Version)
		if err != nil {
			return nil, err
		}
		under := ""
		if strings.TrimSpace(in.Path) != "" {
			if under, err = resources.CleanPicturePath(in.Path); err != nil {
				return nil, err
			}
		}
		pics, err := RepoPicturesOf(ctx, a, &e.Resource, v, under, 0)
		if err != nil {
			return nil, err
		}
		return &RepoPicturesOut{Repo: ResourceRowOf(*e, view, folderNames(ctx, a)), RepoPictures: *pics}, nil
	})
	Def("resources.pictures.set", "Keep the pictures (PNG, JPEG, WebP, GIF, SVG) in a GitHub repository's folders as pictures of their own, pinned to the commit and refreshed with it, or stop keeping a folder in step; nothing from a repository ever runs", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *RepoPicturesSetIn) (*RepoPicturesOut, error) {
		return SetRepoPictures(ctx, a, *in)
	})
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	Offer("resources.pictures.set", Offering{ID: "art-in-git", Group: "bring", Title: "Art kept in git",
		Does:  "Keeps the pictures in a repository's folders as pictures of their own, each pinned to the commit.",
		Words: []string{"pictures from a repository", "design files in git"}, Needs: Needs{Programs: []string{"git"}}})
}

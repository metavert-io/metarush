package resources

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Pictures from a repository (D216). A repository resource can also keep the
// pictures under some of its paths — a folder of character art, the logos in
// brand/ — each PNG, JPEG, WebP, GIF or SVG file there a picture resource of
// its own, next to the repository (the same project, or the workspace
// library). A file is told by its bytes, never by its name; an SVG is cleaned
// like any SVG; Git LFS pointers, links, submodules, other kinds of files and
// files past the size caps (per picture, per fetch) are left out with a
// reason. Each picture version says exactly where it came from —
// repo@commit:path — and a newer commit makes a new version of the same
// picture (the same file), adds new files and marks the ones that went away;
// cuts keep the versions they pin. The blobs are read raw (git cat-file), like
// the snapshot's: nothing from a repository is ever run.

// WholeRepo is the picture path that means the whole repository.
const WholeRepo = "."

// MaxPicturePaths is how many paths one repository keeps pictures from.
const MaxPicturePaths = 20

// maxLeftOutKept is how many left-out files a version lists by name.
const maxLeftOutKept = 100

// pictureExts are the file names that say "picture" (a file named like one
// that isn't one is said to be so; other text files are the snapshot's).
var pictureExts = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".svg": true, ".heic": true, ".avif": true}

// CleanPicturePath turns a folder or file a person named ("art/characters/",
// "./brand") into the form a repository's tree uses; "", "." and "/" are the
// whole repository. Patterns, ".." and the .git folder are refused.
func CleanPicturePath(p string) (string, error) {
	s := strings.TrimSpace(strings.ReplaceAll(p, "\\", "/"))
	for strings.HasPrefix(s, "./") {
		s = s[2:]
	}
	s = strings.Trim(s, "/")
	if s == "" || s == "." {
		return WholeRepo, nil
	}
	bad := len(s) > 512 || !safeRepoPath(s) || strings.ContainsAny(s, "*?[]{}")
	for _, r := range s {
		bad = bad || unicode.IsControl(r)
	}
	if bad {
		return "", apperr.New(apperr.InvalidInput, "“%s” isn't a folder or file of a repository MetaRush can keep pictures from.", shortWords(p, 80)).
			WithHint("Name a folder or a file as the repository has it, like art/characters (. is the whole repository).")
	}
	return s, nil
}

// UnderPicturePath reports whether a repository file is under a picture path
// (the path itself, a file inside it, or anything for the whole repository).
func UnderPicturePath(file, dir string) bool {
	return dir == WholeRepo || dir == "" || file == dir || strings.HasPrefix(file, dir+"/")
}

// PicturePathOf is the picture path a file is under (the narrowest), "" none.
func PicturePathOf(file string, paths []string) string {
	best := ""
	for _, p := range paths {
		if UnderPicturePath(file, p) && (best == "" || best == WholeRepo || len(p) > len(best)) {
			best = p
		}
	}
	return best
}

// PicturePathChange says what keeping and dropping paths did.
type PicturePathChange struct {
	Paths   []string // the paths now kept, sorted
	Added   []string // paths kept now that weren't (new pictures to bring in)
	Dropped []string // paths no longer kept in step
	Notes   []string // keeps that changed nothing, in plain words
}

// ChangePicturePaths keeps and drops paths: a path already inside a kept one
// adds nothing, and a wider path takes the place of the narrower ones it
// holds. Dropping a path that isn't kept is refused.
func ChangePicturePaths(now, keep, drop []string) (*PicturePathChange, error) {
	set := map[string]bool{}
	for _, p := range now {
		set[p] = true
	}
	out := &PicturePathChange{}
	for _, raw := range drop {
		p, err := CleanPicturePath(raw)
		if err != nil {
			return nil, err
		}
		if !set[p] {
			return nil, apperr.New(apperr.InvalidInput, "The repository doesn't keep pictures from “%s”.", p).
				WithHint("Its picture paths: " + orNone(now) + ".")
		}
		delete(set, p)
		out.Dropped = append(out.Dropped, p)
	}
	for _, raw := range keep {
		p, err := CleanPicturePath(raw)
		if err != nil {
			return nil, err
		}
		if set[p] {
			out.Notes = append(out.Notes, upperWord(pathWords(p))+" is already kept.")
			continue
		}
		if wider := PicturePathOf(p, keys(set)); wider != "" {
			out.Notes = append(out.Notes, fmt.Sprintf("%s is already kept: it is in %s.", upperWord(pathWords(p)), pathWords(wider)))
			continue
		}
		for q := range set {
			if UnderPicturePath(q, p) {
				delete(set, q) // the wider path holds it now
			}
		}
		set[p] = true
		out.Added = append(out.Added, p)
	}
	out.Paths = keys(set)
	if len(out.Paths) > MaxPicturePaths {
		return nil, apperr.New(apperr.InvalidInput, "A repository keeps pictures from at most %d folders or files.", MaxPicturePaths).
			WithHint("Keep a folder that holds several of them instead.")
	}
	return out, nil
}

// pathWords names a picture path: “art/characters”, or the whole repository.
func pathWords(p string) string {
	if p == WholeRepo || p == "" {
		return "the whole repository"
	}
	return "“" + p + "”"
}

func keys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func orNone(xs []string) string {
	if len(xs) == 0 {
		return "none yet"
	}
	return "“" + strings.Join(xs, "”, “") + "”"
}

func shortWords(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n-1]) + "…"
}

// SamePicturePaths reports whether two lists hold the same paths.
func SamePicturePaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	x, y := append([]string(nil), a...), append([]string(nil), b...)
	sort.Strings(x)
	sort.Strings(y)
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}

// RepoPicturesUnder are the pictures a repository version kept under a path
// ("" or . all of them), in path order.
func RepoPicturesUnder(v *models.ResourceVersion, dir string) []models.RepoPicture {
	if v == nil || v.Repo == nil {
		return nil
	}
	var out []models.RepoPicture
	for _, p := range v.Repo.Pictures {
		if UnderPicturePath(p.Path, dir) {
			out = append(out, p)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// KeptPictures are the pictures a repository version kept under dir ("" or .
// all of them) that are under a path the repository still keeps pictures
// from, in path order.
func KeptPictures(repo *models.Resource, v *models.ResourceVersion, dir string) []models.RepoPicture {
	var out []models.RepoPicture
	for _, p := range RepoPicturesUnder(v, dir) {
		if PicturePathOf(p.Path, repo.PicturePaths) != "" {
			out = append(out, p)
		}
	}
	return out
}

// keepPicturesOf keeps a fetched repository's pictures (D216): under the
// paths it keeps pictures from and the ones the fetch asks for, at the
// version the fetch stored or found — when that version is the one the
// repository shows (an older commit asked for again keeps what it had), and
// unless it already kept them for these paths.
func keepPicturesOf(ctx context.Context, a *app.App, bare string, r RepoRef, req FetchRequest, res *FetchResult, dir string, progress func(float64, string)) error {
	if len(res.Resources) == 0 {
		return nil
	}
	repo := res.Resources[0]
	ch, err := ChangePicturePaths(repo.PicturePaths, req.Pictures, nil)
	if err != nil {
		return &Permanent{Msg: apperr.From(err).Message}
	}
	paths := ch.Paths
	v := repo.Version(res.version)
	switch {
	case len(paths) == 0 || v == nil || v.Repo == nil || v.SHA256 != repo.Current:
		return nil
	case res.Statuses[0] == StatusSame && SamePicturePaths(v.Repo.PicturePaths, paths):
		return nil
	}
	progress(0.8, "keeping its pictures")
	out, err := keepRepoPictures(ctx, a, bare, r, repo, v, paths, pictureLimitsOf(a), dir)
	if err != nil {
		return err
	}
	if err := recordPictures(ctx, a, repo, v, paths, out); err != nil {
		return err
	}
	res.Pictures = len(out.kept)
	res.Notes = append(res.Notes, picturesWords(paths, out)...)
	a.Audit(ctx, "resource.pictures", "resource", repo.ID, repo.ProjectID, map[string]any{"commit": v.Repo.Commit, "paths": paths, "kept": len(out.kept),
		"added": out.added, "changed": out.changed, "leftOut": len(out.leftOut), "gone": len(out.gone)})
	return nil
}

// keepPicturesAt keeps a repository's pictures at the commit it shows, which
// the bare repository holds (a fetch for its pictures only, after its picture
// paths changed): no new version of its files.
func keepPicturesAt(ctx context.Context, a *app.App, bare string, r RepoRef, req FetchRequest, repo *models.Resource, commit, dir string, progress func(float64, string)) (*FetchResult, error) {
	if repo == nil {
		return nil, permanentf("The repository was removed.")
	}
	v := repo.Latest()
	if v == nil || v.Repo == nil {
		return nil, permanentf("%s has no commit to keep pictures from.", repo.Title)
	}
	res := &FetchResult{Resources: []*models.Resource{repo}, Statuses: []string{StatusSame}, version: v.SHA256}
	if v.Repo.Commit != commit {
		// It moved to another commit meanwhile: that fetch kept its pictures.
		res.Message = fmt.Sprintf("%s shows a newer commit now; its pictures came with it.", repo.Title)
		return res, nil
	}
	ch, err := ChangePicturePaths(repo.PicturePaths, req.Pictures, nil)
	if err != nil {
		return nil, &Permanent{Msg: apperr.From(err).Message}
	}
	paths := ch.Paths
	if len(paths) == 0 {
		res.Message = fmt.Sprintf("%s keeps no pictures now.", repo.Title)
		return res, nil
	}
	progress(0.4, "keeping its pictures")
	out, err := keepRepoPictures(ctx, a, bare, r, repo, v, paths, pictureLimitsOf(a), dir)
	if err != nil {
		return nil, err
	}
	if err := recordPictures(ctx, a, repo, v, paths, out); err != nil {
		return nil, err
	}
	res.Pictures = len(out.kept)
	res.Message = fmt.Sprintf("Kept the pictures of %s as it is at %s (%s).", repo.Title, models.ShortSHA(commit), plainDate(v.Repo.CommittedAt))
	res.Notes = picturesWords(paths, out)
	a.Audit(ctx, "resource.pictures", "resource", repo.ID, repo.ProjectID, map[string]any{"commit": commit, "paths": paths, "kept": len(out.kept),
		"added": out.added, "changed": out.changed, "leftOut": len(out.leftOut), "gone": len(out.gone)})
	progress(1, res.Message)
	return res, nil
}

// pictureLimits bound what one fetch keeps.
type pictureLimits struct {
	fileBytes, totalBytes int64
	count                 int
}

func pictureLimitsOf(a *app.App) pictureLimits {
	r := a.Cfg.Resources.Repos
	return pictureLimits{fileBytes: r.PictureBytes(), totalBytes: r.PicturesBytes(), count: r.MaxPictures()}
}

// classifyPicture tells what a file under a picture path is, by its bytes: a
// picture MetaRush keeps (ext is its extension), or why it isn't one. Both
// are empty for a text file that isn't named like a picture: the snapshot
// keeps those, and the pictures say nothing about them.
func classifyPicture(name string, data []byte) (ext, why string) {
	if isLFSPointer(data) {
		return "", "stored with Git LFS (not downloaded)"
	}
	head := data
	if len(head) > sniffBytes {
		head = head[:sniffBytes]
	}
	sn := Sniff(name, head)
	switch {
	case sn.Route == RouteResource && sn.Kind == models.ResourceImage:
		switch sn.Format {
		case "png", "jpeg", "webp":
			return sn.Ext, ""
		case "gif":
			if animatedGIFData(data) {
				return "", "an animated GIF (it plays like a video; add it to the footage instead)"
			}
			return sn.Ext, ""
		}
		return "", "a HEIC or AVIF photo (from a repository MetaRush keeps PNG, JPEG, WebP, GIF and SVG)"
	case sn.Route == RouteResource && sn.Kind == models.ResourceSVG:
		return "svg", ""
	case sn.Route == RouteResource && sn.Kind == models.ResourcePDF:
		return "", "a PDF, not a picture"
	case sn.Route == RouteResource && sn.Kind == models.ResourceFont:
		return "", "a font, not a picture"
	case sn.Route == RouteFootage && isBinaryData(data):
		return "", "a video or a sound, not a picture"
	case isBinaryData(data):
		return "", "not a PNG, JPEG, WebP, GIF or SVG picture"
	case pictureExts[strings.ToLower(path.Ext(name))]:
		return "", "named like a picture, but it isn't one"
	}
	return "", ""
}

// pictureOutcome is what keeping a commit's pictures did.
type pictureOutcome struct {
	kept    []models.RepoPicture
	leftOut []models.RepoLeftOut
	// added, changed and same count the pictures kept by what they were:
	// new resources, new versions, or the version they already had.
	added, changed, same int
	// gone are pictures of earlier commits whose file this commit doesn't have.
	gone []string
	// empty are paths nothing in the commit is under.
	empty []string
}

// keepRepoPictures keeps the pictures under paths at a repository version's
// commit, which the bare repository in work holds: each file a picture
// resource of its own (new, a new version of the file's picture, or the same),
// and the pictures whose file went away marked missing.
func keepRepoPictures(ctx context.Context, a *app.App, bare string, r RepoRef, repo *models.Resource, v *models.ResourceVersion, paths []string, lim pictureLimits, dir string) (*pictureOutcome, error) {
	if v == nil || v.Repo == nil {
		return nil, errors.New("no repository version to keep pictures from")
	}
	entries, err := listTree(ctx, a.Tools, bare, v.Repo.Commit)
	if err != nil {
		return nil, err
	}
	out := &pictureOutcome{}
	leave := func(file, why string) { out.leftOut = append(out.leftOut, models.RepoLeftOut{Path: file, Why: why}) }
	inTree := map[string]bool{}
	found := map[string]bool{}
	// Every file under the paths is read (up to the per-picture size) and told
	// by its bytes; reading stops at four times what one fetch keeps.
	budget := 4 * lim.totalBytes
	var reading int64
	var want []treeEntry
	for _, e := range entries {
		at := PicturePathOf(e.path, paths)
		if at == "" {
			continue
		}
		found[at] = true
		inTree[e.path] = true
		switch {
		case e.typ == "commit":
			leave(e.path, "a submodule (another repository)")
		case e.typ != "blob":
		case e.mode == "120000":
			leave(e.path, "a link to another file")
		case !safeRepoPath(e.path):
			leave(e.path, "an unusual file name")
		case e.size > lim.fileBytes:
			leave(e.path, "larger than "+humanBytes(lim.fileBytes))
		case reading+e.size > budget:
			leave(e.path, "past what one fetch keeps")
		default:
			reading += e.size
			want = append(want, e)
		}
	}
	for _, p := range paths {
		if !found[p] {
			out.empty = append(out.empty, p)
		}
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	// The blobs, raw: pictures go to files here, the rest is told and dropped.
	byBlob := map[string][]int{}
	var shas []string
	for i, e := range want {
		if _, ok := byBlob[e.sha]; !ok {
			shas = append(shas, e.sha)
		}
		byBlob[e.sha] = append(byBlob[e.sha], i)
	}
	files := map[int]string{}
	why := map[int]string{}
	pastLimit := fmt.Sprintf("past what one fetch keeps (%d pictures, %s in all)", lim.count, humanBytes(lim.totalBytes))
	// Blobs arrive in path order of their first use; each picture's bytes are
	// written once (files with the same bytes share them), and none past the
	// limits, so the scratch folder holds at most what one fetch keeps.
	var written int
	var writtenBytes int64
	if err := readBlobs(ctx, a.Tools, bare, shas, func(sha string, data []byte) error {
		f := ""
		for _, i := range byBlob[sha] {
			ext, no := classifyPicture(path.Base(want[i].path), data)
			switch {
			case no != "":
				why[i] = no
			case ext == "":
			case f != "":
				files[i] = f
			case written >= lim.count || writtenBytes+int64(len(data)) > lim.totalBytes:
				why[i] = pastLimit
			default:
				f = filepath.Join(dir, fmt.Sprintf("picture-%05d.%s", i, ext))
				if err := os.WriteFile(f, data, 0o600); err != nil {
					return err
				}
				files[i] = f
				written++
				writtenBytes += int64(len(data))
			}
		}
		return nil
	}); err != nil {
		return nil, err
	}
	var keptBytes int64
	for i, e := range want {
		if w := why[i]; w != "" {
			leave(e.path, w)
			continue
		}
		f, ok := files[i]
		if !ok {
			continue // a text file: the snapshot's
		}
		if len(out.kept) >= lim.count || keptBytes+e.size > lim.totalBytes {
			leave(e.path, pastLimit)
			continue
		}
		res, err := keepRepoPicture(ctx, a, r, repo, v, e.path, f)
		switch {
		case errors.Is(err, ErrNotResource):
			leave(e.path, "an animated GIF (it plays like a video; add it to the footage instead)")
			continue
		case err != nil && apperr.From(err).Code == apperr.InvalidInput:
			leave(e.path, "a picture MetaRush couldn't read")
			continue
		case err != nil:
			return nil, err
		case res.Resource == nil || res.SHA256 == "":
			leave(e.path, "a picture MetaRush couldn't read")
			continue
		}
		keptBytes += e.size
		switch res.Status {
		case StatusAdded:
			out.added++
		case StatusVersion:
			out.changed++
		default:
			out.same++
		}
		size := e.size
		if pv := res.Resource.Version(res.SHA256); pv != nil {
			size = pv.Size
		}
		out.kept = append(out.kept, models.RepoPicture{Path: e.path, ResourceID: res.Resource.ID, SHA256: res.SHA256, Bytes: size})
	}
	// Pictures of earlier commits whose file this one doesn't have (under a path
	// still kept): marked, never removed — cuts that show them keep them.
	var tied []models.Resource
	if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"repoFile.repoId": repo.ID}, &tied); err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	for _, t := range tied {
		if t.RepoFile == nil || inTree[t.RepoFile.Path] || PicturePathOf(t.RepoFile.Path, paths) == "" || t.Missing {
			continue
		}
		if err := a.DB.Update(ctx, store.CResources, a.WorkspaceID(), t.ID, bson.M{"$set": bson.M{"missing": true, "missingSince": now, "updatedAt": now}}); err != nil {
			return nil, err
		}
		out.gone = append(out.gone, t.Title)
	}
	sort.SliceStable(out.leftOut, func(i, j int) bool { return out.leftOut[i].Path < out.leftOut[j].Path })
	return out, nil
}

// keepRepoPicture adds one picture file of a repository: a new picture
// resource next to the repository, a new version of the one that file is, or
// the same version again.
func keepRepoPicture(ctx context.Context, a *app.App, r RepoRef, repo *models.Resource, v *models.ResourceVersion, file, local string) (*AddResult, error) {
	base := path.Base(file)
	title := cleanTitle(models.DefaultTitle(base))
	src := models.ResourceSource{Kind: models.FromRepo, Name: cleanTitle(base), URL: r.URL(), Site: r.Site(), Commit: v.Repo.Commit, License: v.Repo.License, Author: r.Owner,
		File: file, Lineage: []models.ResourceLineage{{ResourceID: repo.ID, SHA256: v.SHA256, Step: file + " at " + models.ShortSHA(v.Repo.Commit)}}}
	// Named after the repository and the file (widget-alice), numbered when taken.
	prefix := Slug(repo.Name)
	if len(prefix) > 16 {
		prefix = strings.TrimRight(prefix[:16], "-")
	}
	o := AddOptions{Scope: repo.Scope, ProjectID: repo.ProjectID, Name: Slug(prefix + " " + title), Title: title, FileName: cleanTitle(base), Source: src, Via: a.Actor.Via,
		RepoFile: &models.ResourceRepoFile{RepoID: repo.ID, Path: file}}
	if repo.Scope == models.ScopeWorkspace {
		o.ProjectID = ""
	}
	return AddFile(ctx, a, local, o)
}

// cleanTitle keeps a file's name readable: no control characters, at most 120 characters.
func cleanTitle(s string) string {
	s = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return shortWords(s, 120)
}

// repoFileResource is the picture a repository file was kept as (nil none).
func repoFileResource(ctx context.Context, a *app.App, repoID, file string) *models.Resource {
	var r models.Resource
	if err := a.DB.C(store.CResources).FindOne(ctx, bson.M{"workspaceId": a.WorkspaceID(), "repoFile.repoId": repoID, "repoFile.path": file}).Decode(&r); err != nil {
		return nil
	}
	return &r
}

// recordPictures keeps what a commit's pictures were on its repository
// version, and the paths the repository keeps pictures from.
func recordPictures(ctx context.Context, a *app.App, repo *models.Resource, v *models.ResourceVersion, paths []string, out *pictureOutcome) error {
	kept := out.kept
	if kept == nil {
		kept = []models.RepoPicture{}
	}
	listed := out.leftOut
	if len(listed) > maxLeftOutKept {
		listed = listed[:maxLeftOutKept]
	}
	now := time.Now().UTC()
	set := bson.M{"versions.$.repo.picturePaths": paths, "versions.$.repo.pictures": kept, "versions.$.repo.picturesSkipped": len(out.leftOut),
		"versions.$.repo.picturesSkippedWhy": LeftOutWords(out.leftOut), "versions.$.repo.picturesLeftOut": listed, "picturePaths": paths, "updatedAt": now}
	if _, err := a.DB.C(store.CResources).UpdateOne(ctx, bson.M{"_id": repo.ID, "workspaceId": a.WorkspaceID(), "versions.sha256": v.SHA256}, bson.M{"$set": set}); err != nil {
		return err
	}
	return a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), repo.ID, repo)
}

// LeftOutWords sums up why files were left out ("2 files: stored with Git
// LFS (not downloaded)").
func LeftOutWords(list []models.RepoLeftOut) []string {
	sk := make([]SkippedFile, 0, len(list))
	for _, l := range list {
		sk = append(sk, SkippedFile{Path: l.Path, Why: l.Why})
	}
	return skippedWords(sk)
}

// picturesWords says what keeping a commit's pictures did, in plain words.
func picturesWords(paths []string, out *pictureOutcome) []string {
	var notes []string
	where := "“" + strings.Join(paths, "”, “") + "”"
	if len(paths) == 1 && paths[0] == WholeRepo {
		where = "the whole repository"
	}
	switch n := len(out.kept); {
	case n == 0:
		notes = append(notes, "No pictures kept from "+where+".")
	default:
		var parts []string
		if out.added > 0 {
			parts = append(parts, fmt.Sprintf("%d new", out.added))
		}
		if out.changed > 0 {
			parts = append(parts, fmt.Sprintf("%d changed", out.changed))
		}
		if out.same > 0 {
			parts = append(parts, fmt.Sprintf("%d the same", out.same))
		}
		notes = append(notes, fmt.Sprintf("Kept %s from %s: %s.", pluralWord(n, "picture"), where, strings.Join(parts, ", ")))
	}
	for _, p := range out.empty {
		if p != WholeRepo {
			notes = append(notes, fmt.Sprintf("Nothing in this commit is under “%s”.", p))
		}
	}
	if n := len(out.leftOut); n > 0 {
		notes = append(notes, fmt.Sprintf("%s under them left out (%s).", upperWord(pluralWord(n, "file")), strings.Join(LeftOutWords(out.leftOut), "; ")))
	}
	if n := len(out.gone); n > 0 {
		notes = append(notes, fmt.Sprintf("%s no longer in the repository (“%s”): kept, and cuts that show them keep them.", upperWord(pluralWord(n, "picture")), strings.Join(out.gone, "”, “")))
	}
	return notes
}

func pluralWord(n int, w string) string {
	if n == 1 {
		return "1 " + w
	}
	return fmt.Sprintf("%d %ss", n, w)
}

func upperWord(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

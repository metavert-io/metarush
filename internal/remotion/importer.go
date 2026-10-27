package remotion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// Importing a Remotion project (D131): read it (a folder: only read; a GitHub
// repository: fetched into a temporary bare repository), keep its files as
// the first version, read its packages, fonts and compositions, and bring in
// the videos it already had as footage.

// Final marks a failure retrying can't fix (the job fails at once).
type Final struct{ Err error }

func (f *Final) Error() string { return f.Err.Error() }
func (f *Final) Unwrap() error { return f.Err }

func finalf(format string, a ...any) error { return &Final{Err: fmt.Errorf(format, a...)} }

// IsFinal reports a failure retrying can't fix.
func IsFinal(err error) bool {
	var f *Final
	if errors.As(err, &f) {
		return true
	}
	var ae *apperr.Error
	if errors.As(err, &ae) {
		switch ae.Code {
		case apperr.InvalidInput, apperr.Validation, apperr.NotFound, apperr.Conflict, apperr.HumanOnly, apperr.ToolMissing:
			return true
		}
	}
	return false
}

// importPayload is what the import job carries.
type importPayload struct {
	Kind      string // folder | github
	From      string // the folder's full path, or the repository's address
	ProjectID string
	Name      string
	Title     string
	Ref       string
	NoVideos  bool
}

func (p importPayload) toMap() map[string]any {
	return map[string]any{"kind": p.Kind, "from": p.From, "projectId": p.ProjectID, "name": p.Name, "title": p.Title, "ref": p.Ref, "noVideos": p.NoVideos}
}

func payloadOf(j *models.Job) importPayload {
	s := func(k string) string { v, _ := j.Payload[k].(string); return v }
	nv, _ := j.Payload["noVideos"].(bool)
	return importPayload{Kind: s("kind"), From: s("from"), ProjectID: s("projectId"), Name: s("name"), Title: s("title"), Ref: s("ref"), NoVideos: nv}
}

// RunImport runs a remotion-import job.
func RunImport(ctx context.Context, a *app.App, j *models.Job, report func(pct float64, msg string)) (map[string]any, error) {
	if report == nil {
		report = func(float64, string) {}
	}
	p := payloadOf(j)
	ia := *a
	ia.Actor = j.CreatedBy // the person who asked, not the worker
	a = &ia
	var prj models.Project
	if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), p.ProjectID, &prj); err != nil {
		return nil, finalf("The project to bring it into was removed.")
	}
	videoDir, release, err := rendererDir(a)
	if err != nil {
		return nil, err
	}
	defer release()
	work, err := tempDir(a, "remotion-import-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(work)
	say := func(pct float64) func(string) { return func(m string) { report(pct, m) } }

	// 1. Read it.
	var snap *snapshot
	var origin models.RemotionOrigin
	switch p.Kind {
	case models.RemotionFromFolder:
		report(0.02, "reading the folder")
		st, err := os.Stat(p.From)
		if err != nil || !st.IsDir() {
			return nil, finalf("The folder %s isn't there any more.", tildePath(p.From))
		}
		if snap, err = readFolder(ctx, p.From); err != nil {
			return nil, err
		}
		origin = folderOrigin(ctx, a.Tools, p.From, a.Cfg.Installation.Name, snap)
	case models.RemotionFromGitHub:
		r, ok := resources.ParseRepoURL(p.From)
		if !ok {
			return nil, finalf("%s isn't a GitHub repository's address.", p.From)
		}
		if p.Ref != "" {
			r.Ref = p.Ref
		}
		snap, origin, err = readRepo(ctx, a.Tools, work, r, say(0.05))
		if err != nil {
			var perm *resources.Permanent
			if errors.As(err, &perm) {
				return nil, &Final{Err: err}
			}
			return nil, err
		}
	default:
		return nil, finalf("MetaRush can bring in a Remotion project from a folder or from GitHub.")
	}
	if why := projectProblem(snap); why != "" {
		return nil, finalf("%s", why)
	}

	// 2. Keep its files.
	report(0.15, fmt.Sprintf("saving its %s", plural(len(snap.files), "file", "files")))
	if err := uploadFiles(ctx, a, snap.files, func(done, total int) {
		if total > 0 && done%25 == 0 {
			report(0.15+0.25*float64(done)/float64(total), fmt.Sprintf("saving its files (%d of %d)", done, total))
		}
	}); err != nil {
		return nil, err
	}
	m := snap.manifest()
	mobj, err := putBytes(ctx, a, m.Bytes(), "application/json")
	if err != nil {
		return nil, err
	}

	// 3. The source and its first version (or a new version of the same one).
	src, reimport, err := sourceFor(ctx, a, &prj, p, origin, snap)
	if err != nil {
		return nil, err
	}
	files, bytes, pf, pb := totals(m)
	v := &models.RemotionVersion{Hash: m.Hash(), Kind: models.RemotionKindImport, Message: "Brought in from " + origin.Where(),
		By: a.Actor, At: time.Now().UTC(), Machine: a.Cfg.Installation.Name, Manifest: mobj, Files: files, Bytes: bytes, PublicFiles: pf, PublicBytes: pb}
	if reimport {
		v.Parent = src.Head
	}
	v, isNew, err := insertVersion(ctx, a, src, v)
	if err != nil {
		return nil, err
	}

	// 4. Its packages (the import allows the set it came with), fonts and
	// compositions.
	pi, perr := versionPackages(ctx, a, m)
	allowed := false
	if perr == nil {
		allowed = !reimport || packagesAllowed(src, pi.lockHash)
		if allowed && !packagesAllowed(src, pi.lockHash) {
			al := models.RemotionAllowed{Lock: pi.lockHash, Version: v.Hash, By: a.Actor, At: time.Now().UTC(), Import: true}
			if _, err := a.DB.C(store.CRemotionSources).UpdateOne(ctx, bson.M{"_id": src.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$push": bson.M{"allowed": al}}); err != nil {
				return nil, err
			}
			src.Allowed = append(src.Allowed, al)
		}
	}
	if err := setVersion(ctx, a, v.Hash, bson.M{"packagesAllowed": allowed}); err != nil {
		return nil, err
	}
	v.PackagesAllowed = allowed
	var problems []models.RemotionProblem
	if isNew || v.Read == nil || !v.Read.OK {
		rs, ps, err := readVersion(ctx, a, v, m, videoDir, readOptions{Allowed: allowed, Jailed: true, Say: say(0.5)})
		if err != nil {
			return nil, err
		}
		rs.done()
		problems = ps
		if !allowed && perr == nil {
			ch := diffPackages(prevPackages(ctx, a, src, v), v.Packages)
			_ = setVersion(ctx, a, v.Hash, bson.M{"packagesChanged": ch})
		}
	}

	// 5. The videos it already had.
	var existing []models.RemotionExisting
	if !p.NoVideos && len(snap.videos) > 0 {
		report(0.85, fmt.Sprintf("bringing in %s it already had", plural(len(snap.videos), "video", "videos")))
		existing = importVideos(ctx, a, src, v, snap.videos, j.ID)
	}
	set := bson.M{"leftOut": leftOutModels(snap), "updatedAt": time.Now().UTC(), "origin": origin}
	if len(existing) > 0 {
		set["existing"] = mergeExisting(src.Existing, existing)
	}
	if _, err := a.DB.C(store.CRemotionSources).UpdateOne(ctx, bson.M{"_id": src.ID, "workspaceId": a.WorkspaceID()}, bson.M{"$set": set}); err != nil {
		return nil, err
	}
	a.Audit(ctx, "remotion.import", "remotion", src.ID, src.ProjectID, map[string]any{"name": src.Name, "version": v.Hash, "files": len(snap.files), "videos": len(existing), "new": isNew, "via": a.Actor.Via})
	fresh, _ := getVersion(ctx, a, v.Hash)
	if fresh != nil {
		v = fresh
	}
	remindApproval(ctx, a, src, v) // D172
	msg := importMessage(src, v, len(existing), problems, reimport, isNew)
	report(1, msg)
	return map[string]any{"sourceId": src.ID, "name": src.Name, "version": v.Hash, "versionNumber": v.Number, "compositions": len(v.Compositions),
		"videos": len(existing), "message": msg, "projectId": src.ProjectID}, nil
}

// importMessage says what the import did, in plain words.
func importMessage(s *models.RemotionSource, v *models.RemotionVersion, videos int, problems []models.RemotionProblem, reimport, isNew bool) string {
	var b strings.Builder
	switch {
	case reimport && !isNew:
		fmt.Fprintf(&b, "“%s” is unchanged since version %d.", s.Title, v.Number)
	case reimport:
		fmt.Fprintf(&b, "Brought “%s” in again as version %d.", s.Title, v.Number)
	default:
		fmt.Fprintf(&b, "Brought in the Remotion project “%s”", s.Title)
		if n := len(v.Compositions); n > 0 {
			fmt.Fprintf(&b, " with %s", plural(n, "composition", "compositions"))
		}
		b.WriteString(".")
	}
	if videos > 0 {
		if videos == 1 {
			b.WriteString(" The video it already had is in Sources as footage.")
		} else {
			fmt.Fprintf(&b, " The %d videos it already had are in Sources as footage.", videos)
		}
	}
	if v.Read != nil && !v.Read.OK && v.Read.Problem != "" {
		b.WriteString(" " + v.Read.Problem)
	} else if hasErrors(problems) {
		for _, p := range problems {
			if p.Severity == "error" {
				b.WriteString(" " + strings.TrimSpace(p.Message))
				break
			}
		}
	}
	if !v.PackagesAllowed {
		b.WriteString(" It changes the project's packages; a person needs to allow them before MetaRush installs them.")
	} else if v.State == models.RemotionWaiting && v.Read != nil && v.Read.OK {
		b.WriteString(" A person approves this version before its compositions can go into cuts.")
	}
	return b.String()
}

// sourceFor finds the source a re-import updates (the same folder on the
// same computer, or the same repository, in the same project) or creates one.
func sourceFor(ctx context.Context, a *app.App, prj *models.Project, p importPayload, origin models.RemotionOrigin, snap *snapshot) (*models.RemotionSource, bool, error) {
	all, err := listSources(ctx, a, prj.ID)
	if err != nil {
		return nil, false, err
	}
	for i := range all {
		o := all[i].Origin
		same := (o.Kind == models.RemotionFromGitHub && origin.Kind == o.Kind && strings.EqualFold(o.Repo, origin.Repo)) ||
			(o.Kind == models.RemotionFromFolder && origin.Kind == o.Kind && o.Path == origin.Path && o.Machine == origin.Machine)
		if same && (p.Name == "" || p.Name == all[i].Name) {
			return &all[i], true, nil
		}
	}
	base := p.Name
	if base == "" {
		base = slug(firstNonEmpty(strings.TrimSuffix(path.Base(strings.TrimRight(origin.Repo, "/")), ".git"), origin.Folder, "remotion"))
	} else if !NamePattern.MatchString(base) {
		return nil, false, finalf("“%s” can't be a short name: use lowercase letters, digits and dashes, starting with a letter.", base)
	}
	taken := map[string]bool{}
	for _, s := range all {
		taken[s.Name] = true
	}
	name := base
	for i := 2; taken[name]; i++ {
		if p.Name != "" {
			return nil, false, finalf("This project already has a Remotion project called “%s”.", p.Name)
		}
		name = fmt.Sprintf("%s-%d", strings.TrimRight(base[:min(len(base), 36)], "-"), i)
	}
	title := strings.TrimSpace(p.Title)
	if title == "" {
		title = firstNonEmpty(path.Base(origin.Repo), origin.Folder, name)
	}
	now := time.Now().UTC()
	s := &models.RemotionSource{ID: ids.New(ids.RemotionSource), WorkspaceID: a.WorkspaceID(), ProjectID: prj.ID, Name: name, Title: title, Origin: origin,
		Allowed: []models.RemotionAllowed{}, CreatedAt: now, CreatedBy: a.Actor, UpdatedAt: now}
	if pj, ok := snapDescription(snap); ok {
		s.Description = pj
	}
	if err := a.DB.Insert(ctx, store.CRemotionSources, s); err != nil {
		if store.IsDuplicateKey(err) {
			return nil, false, errors.New("another import is bringing in a Remotion project with the same name; try again in a moment")
		}
		return nil, false, err
	}
	return s, false, nil
}

// snapDescription is package.json's description, when there is one.
func snapDescription(s *snapshot) (string, bool) {
	for _, f := range s.files {
		if f.Path == "package.json" {
			b, err := os.ReadFile(f.local)
			if err != nil {
				return "", false
			}
			var pj packageJSON
			if json.Unmarshal(b, &pj) == nil && strings.TrimSpace(pj.Description) != "" && pj.Description != "My Remotion video" {
				d := strings.TrimSpace(pj.Description)
				if len(d) > 300 {
					d = d[:300]
				}
				return d, true
			}
		}
	}
	return "", false
}

// prevPackages is the package list of the version before v.
func prevPackages(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion) []models.RemotionPackage {
	if v.Parent == "" {
		return nil
	}
	pv, err := getVersion(ctx, a, v.Parent)
	if err != nil {
		return nil
	}
	return pv.Packages
}

// importVideos brings the videos in out/ in as footage, with provenance.
func importVideos(ctx context.Context, a *app.App, s *models.RemotionSource, v *models.RemotionVersion, vids []outVideo, jobID string) []models.RemotionExisting {
	var out []models.RemotionExisting
	comps := map[string]string{}
	for _, c := range v.Compositions {
		comps[strings.ToLower(c.ID)] = c.ID
	}
	for _, vid := range vids {
		base := strings.TrimSuffix(path.Base(vid.Path), path.Ext(vid.Path))
		comp := comps[strings.ToLower(base)]
		title := base + " (" + s.Title + ")"
		src := &models.AssetSource{Platform: models.PlatformRemotion, Method: models.SourceRemotionExisting, Title: base, DownloadedAt: time.Now().UTC(), Tool: "MetaRush",
			Remotion: &models.RemotionClipSource{SourceID: s.ID, SourceName: s.Name, SourceTitle: s.Title, Version: v.Hash, VersionNumber: v.Number,
				Kind: models.SourceRemotionExisting, Composition: comp, Path: vid.Path, Origin: s.Origin.Where()}}
		res := ingest.File(ctx, a, vid.local, ingest.Options{ProjectID: s.ProjectID, FootageOnly: true, Title: title, DisplayName: path.Base(vid.Path),
			Source: src, Duplicates: ingest.DuplicatesKeep, Transcribe: true, JobID: jobID, Via: a.Actor.Via})
		if res.AssetID == "" {
			continue
		}
		out = append(out, models.RemotionExisting{Path: vid.Path, AssetID: res.AssetID, Composition: comp, Duration: res.Duration})
	}
	return out
}

func mergeExisting(old, add []models.RemotionExisting) []models.RemotionExisting {
	seen := map[string]bool{}
	var out []models.RemotionExisting
	for _, x := range append(append([]models.RemotionExisting{}, old...), add...) {
		if seen[x.AssetID] {
			continue
		}
		seen[x.AssetID] = true
		out = append(out, x)
	}
	return out
}

func leftOutModels(s *snapshot) []models.RemotionLeftOut {
	out := []models.RemotionLeftOut{}
	for _, g := range s.leftOut() {
		out = append(out, models.RemotionLeftOut{Why: g.why, Count: g.count, Examples: g.examples})
	}
	return out
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

// slug makes a short name from a folder or repository name.
func slug(s string) string {
	s = nonSlug.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-")
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		s = "remotion-" + s
	}
	if len(s) > 40 {
		s = strings.TrimRight(s[:40], "-")
	}
	return strings.Trim(s, "-")
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" && x != "." && x != "/" {
			return x
		}
	}
	return ""
}

// fullPath turns a folder the person typed into a full path.
func fullPath(p string) (string, error) {
	p = expandHome(p)
	if !filepath.IsAbs(p) {
		abs, err := filepath.Abs(p)
		if err != nil {
			return "", err
		}
		p = abs
	}
	return filepath.Clean(p), nil
}

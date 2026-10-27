package ops

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"syscall"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cache"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// ProgressFunc receives byte progress for one labeled item of a long operation.
type ProgressFunc func(label string, done, total int64)

type progressKey struct{}

// WithProgress attaches a progress callback to ctx (the CLI prints it).
func WithProgress(ctx context.Context, fn ProgressFunc) context.Context {
	return context.WithValue(ctx, progressKey{}, fn)
}

func progressFrom(ctx context.Context) ProgressFunc {
	if fn, ok := ctx.Value(progressKey{}).(ProgressFunc); ok && fn != nil {
		return fn
	}
	return func(string, int64, int64) {}
}

// CacheStatusIn has no options.
type CacheStatusIn struct{}

// CachePruneIn evicts least-recently-used local copies.
type CachePruneIn struct {
	ToGB   float64 `json:"toGB,omitempty" jsonschema:"minimum=0,description=Prune down to this size (default: the configured cache.maxGB)."`
	DryRun bool    `json:"dryRun,omitempty"`
}

// PrefetchIn selects what to make available offline.
type PrefetchIn struct {
	RevisionID string `json:"revisionId,omitempty" jsonschema:"description=Everything a render of this revision reads, plus proxies and its latest render."`
	ProjectID  string `json:"projectId,omitempty" jsonschema:"description=Every asset in the project (render sources and proxies) plus the latest render."`
	Pin        bool   `json:"pin,omitempty" jsonschema:"description=Pin the objects so quota pruning never evicts them."`
	DryRun     bool   `json:"dryRun,omitempty" jsonschema:"description=Report what would be downloaded without downloading."`
}

// PrefetchItem is one object considered by a prefetch.
type PrefetchItem struct {
	AssetID  string `json:"assetId,omitempty"`
	RenderID string `json:"renderId,omitempty"`
	Name     string `json:"name"`
	Role     string `json:"role" jsonschema:"enum=original,enum=mezzanine,enum=proxy,enum=thumbnail,enum=waveform,enum=peaks,enum=render,enum=render_thumbnail"`
	SHA256   string `json:"sha256"`
	Size     int64  `json:"size"`
	Status   string `json:"status" jsonschema:"enum=cached,enum=downloaded,enum=resumed,enum=would-download,enum=failed,enum=skipped"`
	Error    string `json:"error,omitempty"`
}

// PrefetchOut summarizes a prefetch.
type PrefetchOut struct {
	Scope           string         `json:"scope"`
	Items           []PrefetchItem `json:"items"`
	Objects         int            `json:"objects"`
	AlreadyCached   int            `json:"alreadyCached"`
	Downloaded      int            `json:"downloaded"`
	Resumed         int            `json:"resumed"`
	Failed          int            `json:"failed"`
	Skipped         int            `json:"skipped"`
	TotalBytes      int64          `json:"totalBytes"`
	DownloadedBytes int64          `json:"downloadedBytes"`
	Pinned          int            `json:"pinned"`
	OfflineReady    bool           `json:"offlineReady"`
	Interrupted     bool           `json:"interrupted,omitempty"`
	DryRun          bool           `json:"dryRun"`
	Note            string         `json:"note,omitempty"`
}

// CacheRepairIn re-verifies the local cache.
type CacheRepairIn struct {
	DryRun bool `json:"dryRun,omitempty" jsonschema:"description=Report problems without changing anything."`
}

// objectRef is a stored object that belongs to an asset or render.
type objectRef struct {
	assetID, renderID, name, role, key, sha string
	size                                    int64
}

var rolePriority = map[string]int{"thumbnail": 0, "render_thumbnail": 0, models.VariantPeaks: 1, models.VariantWaveform: 1, models.VariantProxy: 2, "original": 3, models.VariantMezzanine: 3, "render": 4}

// assetObjects lists what review and rendering on this machine read for an
// asset: small review variants, the proxy, and the render source (RenderSource).
func assetObjects(as *models.Asset, all bool) []objectRef {
	var out []objectRef
	add := func(role, key, sha string, size int64) {
		if key != "" && sha != "" {
			out = append(out, objectRef{assetID: as.ID, name: as.Name, role: role, key: key, sha: sha, size: size})
		}
	}
	kinds := []string{models.VariantThumbnail, models.VariantPeaks, models.VariantWaveform, models.VariantProxy}
	if all {
		kinds = append(kinds, models.VariantContactSheet)
	}
	for _, k := range kinds {
		if v, ok := as.Variants[k]; ok {
			add(k, v.StorageKey, v.SHA256, v.Size)
		}
	}
	role, key, sha, size := as.RenderSource()
	add(role, key, sha, size)
	if all && role != "original" {
		add("original", as.StorageKey, as.SHA256, as.Size)
	}
	// The fixed copy it uses now: what new versions render (D152).
	if as.Fixed() {
		add("fixed", as.Fixes.Copy.StorageKey, as.Fixes.Copy.SHA256, as.Fixes.Copy.Size)
	}
	return out
}

func renderObjects(rd *models.Render) []objectRef {
	if rd.Output == nil {
		return nil
	}
	name := fmt.Sprintf("r%d %s %s", rd.RevisionNumber, rd.Preset, rd.Quality)
	out := []objectRef{{renderID: rd.ID, name: name, role: "render", key: rd.Output.StorageKey, sha: rd.Output.SHA256, size: rd.Output.Size}}
	if k := rd.Output.ThumbnailKey; k != "" {
		out = append(out, objectRef{renderID: rd.ID, name: name, role: "render_thumbnail", key: k, sha: k[strings.LastIndex(k, "/")+1:], size: -1})
	}
	return out
}

// prefetchScope resolves a revision or project into the objects it needs.
func prefetchScope(ctx context.Context, a *app.App, in *PrefetchIn) (string, []objectRef, []models.Asset, error) {
	if (in.RevisionID == "") == (in.ProjectID == "") {
		return "", nil, nil, invalid("Say which version of the cut or which project to download.")
	}
	var ids []string
	renderFilter := bson.M{"state": models.RenderAvailable}
	scope := ""
	if in.RevisionID != "" {
		r, _, err := getRevision(ctx, a, in.RevisionID)
		if err != nil {
			return "", nil, nil, err
		}
		seen := map[string]bool{}
		for _, c := range r.Spec.Clips {
			if !seen[c.AssetID] {
				seen[c.AssetID] = true
				ids = append(ids, c.AssetID)
			}
		}
		for _, t := range r.Spec.Audio {
			if !seen[t.AssetID] {
				seen[t.AssetID] = true
				ids = append(ids, t.AssetID)
			}
		}
		renderFilter["revisionId"] = r.ID
		scope = fmt.Sprintf("version %d of the cut", r.Number)
	} else {
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), in.ProjectID, &p); err != nil {
			return "", nil, nil, notFound("project", in.ProjectID)
		}
		renderFilter["projectId"] = p.ID
		scope = fmt.Sprintf("the project “%s”", p.Name)
	}
	var assets []models.Asset
	filter := bson.M{"projectIds": in.ProjectID}
	if in.RevisionID != "" {
		filter = bson.M{"_id": bson.M{"$in": ids}}
	}
	if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), filter, &assets, options.Find().SetSort(bson.D{{Key: "name", Value: 1}})); err != nil {
		return "", nil, nil, err
	}
	var refs []objectRef
	for i := range assets {
		refs = append(refs, assetObjects(&assets[i], false)...)
	}
	var renders []models.Render
	if err := a.DB.Find(ctx, store.CRenders, a.WorkspaceID(), renderFilter, &renders, options.Find().SetSort(bson.D{{Key: "createdAt", Value: -1}}).SetLimit(1)); err != nil {
		return "", nil, nil, err
	}
	for i := range renders {
		refs = append(refs, renderObjects(&renders[i])...)
	}
	sort.SliceStable(refs, func(i, j int) bool { return rolePriority[refs[i].role] < rolePriority[refs[j].role] })
	return scope, refs, assets, nil
}

func freeBytes(dir string) (int64, bool) {
	var s syscall.Statfs_t
	if err := syscall.Statfs(dir, &s); err != nil {
		return 0, false
	}
	return int64(s.Bavail) * int64(s.Bsize), true
}

// Prefetch downloads (resumably, verified) everything in scope into the cache.
func Prefetch(ctx context.Context, a *app.App, in *PrefetchIn) (*PrefetchOut, error) {
	scope, refs, assets, err := prefetchScope(ctx, a, in)
	if err != nil {
		return nil, err
	}
	out := &PrefetchOut{Scope: scope, Items: []PrefetchItem{}, DryRun: in.DryRun}
	unavailable := map[string]bool{}
	for _, as := range assets {
		if as.State != models.AssetAvailable {
			unavailable[as.ID] = true
		}
	}
	var need int64
	for _, r := range refs {
		if r.size > 0 {
			out.TotalBytes += r.size
		}
		if !a.Cache.Has(r.sha, r.size) && r.size > 0 {
			need += r.size - a.Cache.PartialSize(r.sha)
		}
	}
	if free, ok := freeBytes(a.Cfg.Cache.Dir); ok && !in.DryRun && need > 0 && need > free-(64<<20) {
		return nil, apperr.New(apperr.Storage, "Downloading this needs %s, but only %s is free on the drive that holds this machine's copies.", fmtSize(need), fmtSize(free)).
			WithHint("Free up space, or keep this machine's copies on a bigger drive (cache.dir in config.yaml).")
	}
	report := progressFrom(ctx)
	var pins []string
	for i, r := range refs {
		it := PrefetchItem{AssetID: r.assetID, RenderID: r.renderID, Name: r.name, Role: r.role, SHA256: r.sha, Size: r.size}
		switch {
		case unavailable[r.assetID] && r.role == "original":
			it.Status, it.Error = "skipped", "this clip isn't fully added yet"
		case a.Cache.Has(r.sha, r.size):
			it.Status = "cached"
			pins = append(pins, r.sha)
		case in.DryRun || out.Interrupted:
			it.Status = "would-download"
			if out.Interrupted {
				it.Status = "skipped"
			}
		default:
			resumed := a.Cache.PartialSize(r.sha) > 0
			label := fmt.Sprintf("[%d/%d] %s %s", i+1, len(refs), r.role, r.name)
			_, err := a.Cache.Ensure(ctx, a.Media, r.key, r.sha, r.size, func(done, total int64) { report(label, done, total) })
			switch {
			case err == nil:
				it.Status = map[bool]string{true: "resumed", false: "downloaded"}[resumed]
				if st, err := os.Stat(a.Cache.Path(r.sha)); err == nil {
					it.Size = st.Size()
				}
				out.DownloadedBytes += it.Size
				pins = append(pins, r.sha)
			case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
				it.Status, it.Error = "failed", "interrupted"
				out.Interrupted = true
			default:
				it.Status, it.Error = "failed", err.Error()
			}
		}
		switch it.Status {
		case "cached":
			out.AlreadyCached++
		case "downloaded":
			out.Downloaded++
		case "resumed":
			out.Resumed++
		case "failed":
			out.Failed++
		case "skipped":
			out.Skipped++
		}
		out.Items = append(out.Items, it)
	}
	out.Objects = len(out.Items)
	if in.Pin && !in.DryRun && len(pins) > 0 {
		if err := a.Cache.Pin(pins...); err != nil {
			return nil, err
		}
		out.Pinned = len(pins)
	}
	out.OfflineReady = !in.DryRun && out.Failed == 0 && out.Skipped == 0 && out.Objects > 0
	switch {
	case out.Interrupted:
		out.Note = "Interrupted. Run the same download again: what was already downloaded is kept, and it carries on where it stopped."
	case out.Failed > 0:
		out.Note = "Some files couldn't be downloaded (the list says which); the rest are on this machine, checked. Run it again to retry."
	case in.DryRun:
		out.Note = fmt.Sprintf("%d of %d files would be downloaded (%s).", out.Objects-out.AlreadyCached-out.Skipped, out.Objects, fmtSize(need))
	case out.OfflineReady && !in.Pin && a.Cache.MaxBytes > 0 && out.TotalBytes > a.Cache.MaxBytes*9/10:
		out.Note = "This is more than this machine's copies may take up, so some of it will be cleared again to stay under the limit. Keep it with --pin, or raise cache.maxGB in config.yaml."
	case out.OfflineReady:
		out.Note = "Everything the renders and reviews of " + scope + " need is on this machine, checked."
	}
	return out, nil
}

// Repair re-verifies the cache and re-fetches pinned objects.
func Repair(ctx context.Context, a *app.App, in *CacheRepairIn) (*cache.RepairResult, error) {
	report := progressFrom(ctx)
	var refetch func(ctx context.Context, sha string) error
	if a.Media != nil {
		refetch = func(ctx context.Context, sha string) error {
			_, err := a.Cache.Ensure(ctx, a.Media, media.ObjectKey(a.WorkspaceID(), sha), sha, -1, func(done, total int64) { report("re-fetching "+sha[:12], done, total) })
			return err
		}
	}
	res, err := a.Cache.Repair(ctx, in.DryRun, refetch, func(i, n int, sha string, size int64) {
		report(fmt.Sprintf("verifying %d/%d %s", i, n, sha[:12]), int64(i), int64(n))
	})
	if err != nil {
		return nil, err
	}
	if !in.DryRun && (res.Corrupt > 0 || res.Refetched > 0) {
		a.Audit(ctx, "cache.repair", "installation", a.Cfg.Installation.ID, "", map[string]any{"corrupt": res.Corrupt, "refetched": res.Refetched, "partials": res.Partials})
	}
	return &res, nil
}

func fmtSize(n int64) string {
	const u = 1024
	if n < u {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(u), 0
	for x := n / u; x >= u; x /= u {
		div *= u
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGTPE"[exp])
}

func init() {
	Def("cache.status", "How much room this machine's copies of footage take: files, kept for offline work, in use, part-downloaded, and the size limit", Flags{}, func(ctx context.Context, a *app.App, in *CacheStatusIn) (*cache.Stats, error) {
		st, err := a.Cache.Status()
		if err != nil {
			return nil, err
		}
		return &st, nil
	})
	Def("cache.prune", "Clear the copies on this machine that were used least recently (never ones kept for offline work or in use; MetaRush's storage is untouched)", Flags{LocalOnly: true}, func(ctx context.Context, a *app.App, in *CachePruneIn) (*cache.PruneResult, error) {
		target := int64(-1)
		if in.ToGB > 0 {
			target = int64(in.ToGB * (1 << 30))
		}
		r, err := a.Cache.PruneTo(target, in.DryRun)
		if err != nil {
			return nil, err
		}
		return &r, nil
	})
	Def("cache.prefetch", "Download everything the renders and reviews of a version or project need, for offline work (it carries on after a break, and checks each file)", Flags{LocalOnly: true}, func(ctx context.Context, a *app.App, in *PrefetchIn) (*PrefetchOut, error) {
		return Prefetch(ctx, a, in)
	})
	Def("cache.repair", "Check every copy on this machine, remove damaged or half-downloaded ones, and download again the ones kept for offline work", Flags{LocalOnly: true}, func(ctx context.Context, a *app.App, in *CacheRepairIn) (*cache.RepairResult, error) {
		return Repair(ctx, a, in)
	})
}

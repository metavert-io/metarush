package worker

import (
	"context"
	"errors"
	"fmt"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
)

// resourceUploadHandler copies a resource version a linked folder kept on this
// machine into the media store (the first time a cut uses it, D81).
func resourceUploadHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	a := w.App
	rid, _ := j.Payload["resourceId"].(string)
	sha, _ := j.Payload["sha256"].(string)
	var r models.Resource
	if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), rid, &r); err != nil {
		return nil, permanent("resource %s not found", rid)
	}
	v := r.Version(sha)
	if v == nil {
		return nil, permanent("%s has no version %s", r.Title, sha)
	}
	if v.Stored {
		return map[string]any{"resourceId": rid, "sha256": sha, "already": true}, nil
	}
	report(0.1, "looking for “"+r.Title+"”")
	if !a.Cache.Has(sha, v.Size) {
		if err := resources.AdoptFromFolder(a, &r, sha); err != nil {
			return nil, permanent("%s", err.Error())
		}
	}
	report(0.3, "saving “"+r.Title+"” to your storage")
	if err := resources.UploadVersion(ctx, a, &r, sha); err != nil {
		return nil, err
	}
	report(1, "saved “"+r.Title+"”")
	return map[string]any{"resourceId": rid, "sha256": sha, "size": v.Size}, nil
}

// fetchHandler brings in a GitHub repository or a web page (D104, D105).
// It is pinned to the machine that asked for it.
func fetchHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	res, err := resources.RunFetch(ctx, w.App, j, func(pct float64, msg string) { report(pct, msg) })
	if err != nil {
		var p *resources.Permanent
		if errors.As(err, &p) {
			return nil, &Permanent{Err: err}
		}
		return nil, err
	}
	return res.Map(), nil
}

// resourceFile is one resource file a render serves: where it is in the cache
// and the type it is served with (an SVG must say image/svg+xml to draw).
type resourceFile struct {
	Path string
	MIME string
}

// renderResources fetches every resource file a plan draws into the cache and
// returns them by SHA-256 (for the render's media server). A version only
// another machine has makes the render wait for its upload (retryable).
func renderResources(ctx context.Context, a *app.App, plan *editspec.RenderPlan, extras *ops.RenderExtras) (map[string]resourceFile, error) {
	files := editspec.PlanResourceFiles(plan)
	out := map[string]resourceFile{}
	if len(files) == 0 {
		return out, nil
	}
	docs := extras.Resources()
	for _, sha := range files {
		key, size, stored, holders, title, mime := "", int64(0), true, []string(nil), "", ""
		for _, r := range docs {
			for _, v := range r.Versions {
				if v.SHA256 == sha {
					key, size, stored, holders, title, mime = v.StorageKey, v.Size, v.Stored, v.Holders, r.Title, v.MIME
				}
				for _, pg := range v.Pages {
					if pg.SHA256 == sha {
						key, size, stored, title, mime = pg.StorageKey, pg.Size, true, r.Title, "image/png"
					}
				}
			}
		}
		if key == "" {
			return nil, failed("A picture or font this version shows isn't in MetaRush any more, so it can't be rendered.",
				permanent("the plan draws a resource file (%s) this workspace doesn't have", sha[:8]))
		}
		if !stored && !a.Cache.Has(sha, size) {
			// This machine may be the one with the folder.
			for _, r := range docs {
				if r.Version(sha) != nil {
					_ = resources.AdoptFromFolder(a, r, sha)
				}
			}
		}
		if !stored && !a.Cache.Has(sha, size) {
			return nil, failed("“"+title+"” is still being copied from the computer that has its folder; the render tries again by itself.",
				fmt.Errorf("waiting for %s to be uploaded from the machine that has its folder (%v)", title, holders))
		}
		var p string
		var err error
		if a.Cache.Has(sha, size) {
			p = a.Cache.Path(sha)
		} else {
			p, err = a.Cache.Ensure(ctx, a.Media, key, sha, size, nil)
			if err != nil {
				return nil, err
			}
		}
		out[sha] = resourceFile{Path: p, MIME: mime}
	}
	return out, nil
}

// nestedApprovals refuses a render whose nested cuts use motion graphics a
// person hasn't approved, and returns the approved ones (by hash) for the
// renderer's --motion archives.
func nestedApprovals(ctx context.Context, a *app.App, plan *editspec.RenderPlan, have map[string]*models.MotionComponent) (map[string]*models.MotionComponent, error) {
	out := map[string]*models.MotionComponent{}
	for _, h := range editspec.PlanMotionHashes(plan) {
		if c, ok := have[h]; ok {
			out[h] = c
			continue
		}
		c, err := motion.Get(ctx, a.DB, a.WorkspaceID(), h)
		if err != nil || c.State != models.MotionApproved {
			return nil, failed("A cut played inside this one uses a motion graphic nobody has approved yet: approve it under Motion graphics in the dashboard first.",
				permanent("a cut played inside this one uses a motion graphic (%s) a person hasn't approved; approve it first (Motion graphics in the dashboard)", h[:8]))
		}
		out[h] = c
	}
	return out, nil
}

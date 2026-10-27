package ops

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// PreviewPlan resolves a spec into the render plan the dashboard preview plays:
// proxies for footage, originals for everything else (served local-first, never
// redirected — see api.assetMedia), and each file's audio channel count, so the
// preview can play mono files the way the renderer mixes them (D64). revision.plan
// and edit.preview build theirs with previewRenderPlan; this is for a change not
// saved yet that only has a spec (mix.preview).
func PreviewPlan(ctx context.Context, a *app.App, projectID, revisionID, specHash string, spec *editspec.Spec) (*editspec.RenderPlan, error) {
	facts, err := loadFacts(ctx, a, projectID, spec)
	if err != nil {
		return nil, err
	}
	return previewRenderPlan(spec, facts, revisionID, projectID, specHash), nil
}

// RevisionPlan resolves a saved version into the plan the dashboard preview
// plays, with the words the version was made with (D146): the review player
// shows what its render shows.
func RevisionPlan(ctx context.Context, a *app.App, r *models.Revision) (*editspec.RenderPlan, error) {
	spec := r.Spec
	facts, err := loadFactsPinned(ctx, a, r.ProjectID, &spec, transcripts.PinsOf(r))
	if err != nil {
		return nil, err
	}
	return previewRenderPlan(&spec, facts, r.ID, r.ProjectID, r.SpecHash), nil
}

func previewSources(plan *editspec.RenderPlan, facts *dbFacts, spec *editspec.Spec) {
	plan.Media = "proxy"
	plan.SourceChannels = map[string]int{}
	ids := specAssetIDs(spec)
	for _, id := range editspec.PlanSourceIDs(plan) { // nested cuts' footage too (D84)
		if !slices.Contains(ids, id) {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		kind := "proxy"
		as, ok := facts.anyAsset(id)
		// The media this version plays (D152): its pinned fixed copy, else the
		// original — even while a fixed copy is the footage's current media.
		pinned := facts.anyFixed(id)
		media := ""
		if ok {
			_, hasProxy := as.OriginalVariant(models.VariantProxy)
			switch {
			case pinned != nil:
				_, hasProxy = pinned.Variants[models.VariantProxy]
				media = pinned.SHA256
			case as.Fixed():
				media = "original"
			}
			if !hasProxy {
				kind = "original"
			}
			if !as.Media.HasVideo {
				kind = "original"
			}
		}
		plan.Sources[id] = fmt.Sprintf("/media/assets/%s/%s", id, kind)
		q := ""
		if kind == "original" {
			q = "preview=1" // served local-first, never redirected (see api.assetMedia)
		}
		if media != "" {
			q = strings.TrimPrefix(q+"&media="+media, "&")
		}
		if q != "" {
			plan.Sources[id] += "?" + q
		}
		if !ok {
			continue
		}
		// Proxies always carry stereo sound (internal/derive makes them with
		// -ac 2, which spreads a mono recording with equal power, as the
		// renderer does); an original plays as recorded.
		ch := 2
		if kind == "original" {
			ch = AudioChannelsOf(as)
			if pinned != nil && pinned.Media != nil {
				ch = 0
				for _, st := range pinned.Media.Streams {
					if st.Type == "audio" {
						ch = st.Channels
						break
					}
				}
			}
		}
		if ch > 0 && as.Media.HasAudio {
			plan.SourceChannels[id] = ch
		}
	}
	// Resource files (logos, pictures, PDF pages, fonts): same-origin, local-first.
	for _, sha := range editspec.PlanResourceFiles(plan) {
		if plan.ResourceSources == nil {
			plan.ResourceSources = map[string]string{}
		}
		plan.ResourceSources[sha] = "/media/resources/file/" + sha
	}
	// Motion graphics: the stills their checks drew, shown in their place
	// (D187); same-origin and signed in, never the graphic's code.
	editspec.FillMotionStills(plan, facts.anyMotion, MotionStillURL)
	editspec.FillNested(plan)
}

// MotionStillURL is where the dashboard fetches a checked motion graphic
// version's test frame n as a still for the preview (D187): the frame with
// the check's backdrop taken out (api.motionMedia, still-<n>).
func MotionStillURL(hash string, n int) string {
	return fmt.Sprintf("/media/motion/%s/still-%d", hash, n)
}

// anyMotion finds what a motion graphic version's check recorded among the
// spec's and its nested cuts' facts.
func (f *dbFacts) anyMotion(hash string) (editspec.MotionInfo, bool) {
	if m, ok := f.motion[hash]; ok {
		return m, true
	}
	if f.nested != nil {
		for _, ns := range f.nested.byKey {
			if inner, ok := ns.Facts.(*dbFacts); ok && inner != nil {
				if m, ok := inner.anyMotion(hash); ok {
					return m, true
				}
			}
		}
	}
	return editspec.MotionInfo{}, false
}

// anyFixed finds the fixed copy an asset plays among the spec's and its nested
// cuts' pins (nil: the original).
func (f *dbFacts) anyFixed(id string) *models.SourceCopy {
	if c, ok := f.fixed[id]; ok {
		return c
	}
	if _, own := f.assets[id]; own {
		return nil
	}
	if f.nested != nil {
		for _, ns := range f.nested.byKey {
			if inner, ok := ns.Facts.(*dbFacts); ok && inner != nil {
				if _, has := inner.anyAsset(id); has {
					return inner.anyFixed(id)
				}
			}
		}
	}
	return nil
}

// anyAsset finds an asset among the spec's and its nested cuts' facts.
func (f *dbFacts) anyAsset(id string) (*models.Asset, bool) {
	if as, ok := f.assets[id]; ok {
		return as, true
	}
	if f.nested != nil {
		for _, ns := range f.nested.byKey {
			if inner, ok := ns.Facts.(*dbFacts); ok && inner != nil {
				if as, ok := inner.anyAsset(id); ok {
					return as, true
				}
			}
		}
	}
	return nil, false
}

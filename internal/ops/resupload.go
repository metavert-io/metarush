package ops

import (
	"context"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
)

// queueResourceUploads asks the machine that has a linked folder's large file
// to upload it, the first time a saved cut uses a version that isn't in the
// media store yet (D81), so every machine can render the cut.
func queueResourceUploads(ctx context.Context, a *app.App, rev *models.Revision, facts *dbFacts) {
	if facts == nil || facts.res == nil {
		return
	}
	for _, n := range editspec.ResourceNames(&rev.Spec) {
		pin := rev.Spec.Resources[n]
		r := facts.res.byID[pin.ID]
		if r == nil {
			continue
		}
		v := r.Version(pin.SHA256)
		if v == nil || v.Stored || len(v.Holders) == 0 {
			continue
		}
		_, _, _ = a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeResourceUpload, IdempotencyKey: "resource-upload:" + pin.SHA256, Priority: 25,
			ProjectID: rev.ProjectID, Installation: v.Holders[0], Actor: a.Actor,
			Payload: map[string]any{"resourceId": r.ID, "sha256": pin.SHA256, "title": r.Title}})
	}
}

// LoadRenderExtras reads what a render needs beyond footage: the resources a
// cut pins and the cuts it nests, with their facts (D82, D84). Visibility was
// checked when the cut was saved; cycles are still refused.
func LoadRenderExtras(ctx context.Context, a *app.App, s *editspec.Spec) (*RenderExtras, error) {
	res, err := loadResourceFacts(ctx, a, "", nil, s)
	if err != nil {
		return nil, err
	}
	nested, err := loadNested(ctx, a, "", nil, s, 0, nil)
	if err != nil {
		return nil, err
	}
	return &RenderExtras{res: res, nested: nested}, nil
}

// RenderExtras answers editspec.ResourceFacts and editspec.NestedFacts for renders.
type RenderExtras struct {
	res    *resourceFacts
	nested *nestedFacts
}

// Resource implements editspec.ResourceFacts.
func (x *RenderExtras) Resource(id, sha string) (editspec.ResourceInfo, bool) {
	if x == nil {
		return editspec.ResourceInfo{}, false
	}
	return x.res.info(id, sha)
}

// Nested implements editspec.NestedFacts.
func (x *RenderExtras) Nested(projectID, revisionID string) (*editspec.NestedSource, bool) {
	if x == nil || x.nested == nil {
		return nil, false
	}
	ns, ok := x.nested.byKey[editspec.NestedKey(projectID, revisionID)]
	return ns, ok
}

// Asset finds an asset document among the nested cuts' facts (renders fetch
// every file a plan plays, nested ones included).
func (x *RenderExtras) Asset(id string) (*models.Asset, bool) {
	if x == nil || x.nested == nil {
		return nil, false
	}
	for _, ns := range x.nested.byKey {
		if inner, ok := ns.Facts.(*dbFacts); ok && inner != nil {
			if as, ok := inner.anyAsset(id); ok {
				return as, true
			}
		}
	}
	return nil, false
}

// Resources lists the resource documents the plan pins (by ID), nested cuts' included.
func (x *RenderExtras) Resources() map[string]*models.Resource {
	out := map[string]*models.Resource{}
	if x == nil {
		return out
	}
	var walk func(r *resourceFacts, n *nestedFacts)
	walk = func(r *resourceFacts, n *nestedFacts) {
		if r != nil {
			for id, doc := range r.byID {
				out[id] = doc
			}
		}
		if n != nil {
			for _, ns := range n.byKey {
				if inner, ok := ns.Facts.(*dbFacts); ok && inner != nil {
					walk(inner.res, inner.nested)
				}
			}
		}
	}
	walk(x.res, x.nested)
	return out
}

// NestedCutUse is a cut played inside a delivered or checked cut: the clip that
// plays it, and the project whose cut that clip is in (empty: the cut itself).
type NestedCutUse struct {
	ClipID string
	Source *editspec.NestedSource
	In     string
}

// NestedCuts lists the cuts s plays, at any depth, outer ones first.
func (x *RenderExtras) NestedCuts(s *editspec.Spec) []NestedCutUse {
	var out []NestedCutUse
	var walk func(s *editspec.Spec, f editspec.NestedFacts, in string, depth int)
	walk = func(s *editspec.Spec, f editspec.NestedFacts, in string, depth int) {
		if s == nil || f == nil || depth > editspec.MaxNestDepth {
			return
		}
		for _, c := range s.Clips {
			pid, rid, ok := editspec.NestedRef(c.AssetID)
			if !ok {
				continue
			}
			ns, ok := f.Nested(pid, rid)
			if !ok || ns == nil || ns.Spec == nil || ns.Missing || ns.Cycle {
				continue
			}
			out = append(out, NestedCutUse{ClipID: c.ID, Source: ns, In: in})
			if inner, ok := ns.Facts.(editspec.NestedFacts); ok {
				walk(ns.Spec, inner, ns.ProjectName, depth+1)
			}
		}
	}
	if x != nil {
		walk(s, x, "", 0)
	}
	return out
}

// ShownResource is a resource version a cut shows: pinned by the cut itself
// (In empty) or by a cut played inside it (In: that cut's project).
type ShownResource struct {
	Name string
	Pin  editspec.ResourcePin
	Doc  *models.Resource
	In   string
}

// ShownResources lists every resource version s shows, its nested cuts' pins
// included; each version once, the cut's own first.
func (x *RenderExtras) ShownResources(s *editspec.Spec) []ShownResource {
	docs := x.Resources()
	seen := map[string]bool{}
	var out []ShownResource
	add := func(s *editspec.Spec, in string) {
		for _, name := range editspec.ResourceNames(s) {
			pin := s.Resources[name]
			doc, ok := docs[pin.ID]
			if !ok || seen[pin.ID+"@"+pin.SHA256] {
				continue
			}
			seen[pin.ID+"@"+pin.SHA256] = true
			out = append(out, ShownResource{Name: name, Pin: pin, Doc: doc, In: in})
		}
	}
	add(s, "")
	for _, u := range x.NestedCuts(s) {
		add(u.Source.Spec, u.Source.ProjectName)
	}
	return out
}

// NestedMotion lists the nested cuts' motion graphics (by hash) with their facts.
func (x *RenderExtras) NestedMotion() map[string]editspec.MotionInfo {
	out := map[string]editspec.MotionInfo{}
	if x == nil || x.nested == nil {
		return out
	}
	var walk func(n *nestedFacts)
	walk = func(n *nestedFacts) {
		for _, ns := range n.byKey {
			if inner, ok := ns.Facts.(*dbFacts); ok && inner != nil {
				for h, m := range inner.motion {
					out[h] = m
				}
				if inner.nested != nil {
					walk(inner.nested)
				}
			}
		}
	}
	walk(x.nested)
	return out
}

package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Fixed copies in cuts (D152): a version plays the media it pins (spec.fixed)
// and reads that media's analysis; footage without a pin plays its original.

// CopyID is a fixed copy's record ID: <workspace>/<original sha>:<fingerprint>.
func CopyID(workspaceID, sourceSHA, fingerprint string) string {
	return workspaceID + "/" + sourceSHA + ":" + fingerprint
}

// PinnedMedia is whose analysis a spec reads for each of its assets: the
// pinned fixed copy's, or the original's.
func PinnedMedia(s *editspec.Spec, ids []string) store.InsightMedia {
	out := store.InsightMedia{}
	for _, id := range ids {
		out[id] = s.FixedSHA(id)
	}
	return out
}

// LoadPinnedCopies reads the fixed copies a spec pins, by asset.
func LoadPinnedCopies(ctx context.Context, a *app.App, s *editspec.Spec) (map[string]*models.SourceCopy, error) {
	out := map[string]*models.SourceCopy{}
	if len(s.Fixed) == 0 {
		return out, nil
	}
	var ids, shas []string
	for id, pin := range s.Fixed {
		ids = append(ids, id)
		shas = append(shas, pin.SHA256)
	}
	var docs []models.SourceCopy
	if err := a.DB.Find(ctx, store.CSourceCopies, a.WorkspaceID(), bson.M{"assetId": bson.M{"$in": ids}, "sha256": bson.M{"$in": shas}}, &docs); err != nil {
		return nil, err
	}
	for i := range docs {
		c := &docs[i]
		if s.FixedSHA(c.AssetID) == c.SHA256 {
			out[c.AssetID] = c
		}
	}
	return out, nil
}

// FixedCopy implements editspec.FixedFacts.
func (f *dbFacts) FixedCopy(assetID, sha string) (editspec.FixedInfo, bool) {
	c, ok := f.fixed[assetID]
	if !ok || c.SHA256 != sha {
		return editspec.FixedInfo{}, false
	}
	return editspec.FixedInfo{Words: CopyWords(c), Ready: c.State == models.FixReady}, true
}

// CopyWords says what a fixed copy fixes, in a few words ("steadier picture
// and less grain").
func CopyWords(c *models.SourceCopy) string {
	var ids []string
	for _, st := range c.FixSteps.Steps {
		ids = append(ids, st.Fix)
	}
	return models.FixResults(ids)
}

// currentFixed is the SHA-256 of the fixed copy an asset uses now, for
// pinning footage a change puts into a cut for the first time ("" when it
// uses its original or isn't known).
func currentFixed(f *dbFacts) func(string) string {
	return func(id string) string {
		if f == nil {
			return ""
		}
		if as := f.assets[id]; as != nil && as.Fixed() {
			return as.Fixes.Copy.SHA256
		}
		return ""
	}
}

// pinNewFootage pins the fixed copy of footage next plays for the first time
// (editspec.PinNewFootage), reading which footage has one now.
func pinNewFootage(ctx context.Context, a *app.App, base, next *editspec.Spec, explicit map[string]bool) bool {
	cur := a.DB.CurrentMedia(ctx, a.WorkspaceID(), editspec.AssetIDs(next))
	return editspec.PinNewFootage(base, next, explicit, func(id string) string { return cur[id] })
}

// CutUseFixedIn moves a cut to a source's fixed copy (or back to its original).
type CutUseFixedIn struct {
	CutEditCommon
	Footage string `json:"footage" jsonschema:"minLength=1,description=The clip (its title\\, file name or ID) or a picture (res_…)."`
	// Original plays the original again instead of the fixed copy.
	Original bool `json:"original,omitempty" jsonschema:"description=Play the original again instead of the fixed copy."`
}

func init() {
	Def("cut.useFixed", "Save a new version of a cut that plays a source's fixed copy (the steadier picture, the cleaned-up voices, the trimmed music, the fixed picture), or its original again; saved versions never change. dryRun checks it and gives a confirm token", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *CutUseFixedIn) (*CutEditOut, error) {
		t, err := loadEditTarget(ctx, a, in.CutEditCommon)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(in.Footage, "res_") {
			return useFixedPicture(ctx, a, t, in)
		}
		as, err := t.resolveFootage(ctx, a, in.Footage)
		if err != nil {
			return nil, err
		}
		title := as.DisplayTitle()
		if !slices.Contains(editspec.AssetIDs(t.spec), as.ID) {
			return nil, apperr.New(apperr.NotFound, "%s doesn't play “%s”.", upperFirst(branchWords(t.project.Name, t.branch)), title)
		}
		var ops []editspec.Op
		var summary, instruction string
		pinned := t.spec.FixedSHA(as.ID)
		switch {
		case in.Original:
			if pinned != "" {
				ops = append(ops, editspec.Op{Op: "setFixed", ID: as.ID})
			}
			summary = fmt.Sprintf("“%s” plays its original again", title)
			instruction = fmt.Sprintf("Play the original of “%s” again", title)
		case as.Fixed():
			if pinned != as.Fixes.Copy.SHA256 {
				ops = append(ops, editspec.Op{Op: "setFixed", ID: as.ID, Fixed: &editspec.FixedPin{SHA256: as.Fixes.Copy.SHA256}})
			}
			summary = fmt.Sprintf("“%s” plays its fixed copy (%s)", title, models.FixResults(as.Fixes.Copy.Fixes))
			instruction = fmt.Sprintf("Play the fixed copy of “%s” (%s)", title, models.FixResults(as.Fixes.Copy.Fixes))
		}
		// Music's silent ends: the tracks start at its first sound and stop at its last.
		if f := as.Fixes; f != nil && f.Trim != nil && slices.Contains(f.Ticked, models.FixEnds) && !in.Original {
			for _, tr := range t.spec.Audio {
				if tr.AssetID != as.ID || (tr.In >= f.Trim.Start-0.05 && tr.Out <= f.Trim.End+0.05) {
					continue
				}
				set := map[string]float64{"in": math.Max(tr.In, f.Trim.Start), "out": math.Min(tr.Out, f.Trim.End)}
				raw, _ := json.Marshal(set)
				ops = append(ops, editspec.Op{Op: "updateAudio", ID: tr.ID, Set: raw})
				if summary == "" {
					summary = fmt.Sprintf("“%s” plays from its first sound to its last", title)
					instruction = fmt.Sprintf("Play “%s” from its first sound to its last", title)
				}
			}
		}
		if summary == "" {
			return nil, apperr.New(apperr.Conflict, "“%s” has no fixed copy yet: tick its fixes first.", title)
		}
		return t.finish(ctx, a, in.CutEditCommon, "fixed", ops, summary, instruction, &CutEditOut{})
	})
}

// useFixedPicture moves a cut's pins of a picture to its fixed version (or the original).
func useFixedPicture(ctx context.Context, a *app.App, t *editTarget, in *CutUseFixedIn) (*CutEditOut, error) {
	var r models.Resource
	if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), in.Footage, &r); err != nil {
		return nil, apperr.New(apperr.NotFound, "MetaRush can't find that picture.")
	}
	want := r.Current
	if in.Original {
		if r.Fixes == nil || r.Fixes.From == "" {
			return nil, apperr.New(apperr.Conflict, "“%s” has no fixed version.", r.Title)
		}
		want = r.Fixes.From
	}
	var ops []editspec.Op
	for _, n := range editspec.ResourceNames(t.spec) {
		pin := t.spec.Resources[n]
		if pin.ID == r.ID && pin.SHA256 != want {
			ops = append(ops, editspec.Op{Op: "setResource", ID: n, Resource: &editspec.ResourcePin{ID: r.ID, SHA256: want}})
		}
	}
	summary := fmt.Sprintf("The cut uses the fixed version of “%s”", r.Title)
	instruction := fmt.Sprintf("Use the fixed version of “%s”", r.Title)
	if in.Original {
		summary = fmt.Sprintf("The cut uses the original version of “%s”", r.Title)
		instruction = fmt.Sprintf("Use the original version of “%s”", r.Title)
	}
	return t.finish(ctx, a, in.CutEditCommon, "fixed", ops, summary, instruction, &CutEditOut{})
}

// fixedName is the readable name of footage for messages about its copy.
func fixedName(f *dbFacts, id string) string {
	if as := f.assets[id]; as != nil {
		return fmt.Sprintf("“%s”", as.DisplayTitle())
	}
	return "the footage"
}

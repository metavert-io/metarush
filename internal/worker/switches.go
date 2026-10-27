package worker

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/calls"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/evidence"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/qa"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/visual"
)

// switchesHandler finds a footage's picture switches to the frame (D160):
// the evidence's cuts and the on-screen index's boundaries, refined by
// decoding a few seconds of the preview copy around each.
func switchesHandler(ctx context.Context, w *Worker, j *models.Job, report Report) (map[string]any, error) {
	a := w.App
	as, err := loadAsset(ctx, a, j)
	if err != nil {
		return nil, err
	}
	ev := evidence.LoadStored(ctx, a.DB, a.WorkspaceID(), []string{as.ID})[as.ID]
	var idx *visual.Index
	if st := visual.Load(ctx, a.DB, a.WorkspaceID(), as.ID); st != nil {
		idx = st.Index
	}
	if !calls.Worth(as, ev, idx) {
		return map[string]any{"switches": 0, "note": "no cuts in this footage"}, nil
	}
	in := calls.Input{Asset: as, Evidence: ev, Index: idx}
	if px, ok := as.Variants[models.VariantProxy]; ok {
		if p, err := a.Cache.Ensure(ctx, a.Media, px.StorageKey, px.SHA256, px.Size, nil); err == nil {
			in.Path, in.Source = p, "proxy"
			if f, err := strconv.ParseFloat(px.Params["fps"], 64); err == nil && f > 0 {
				in.FPS = f
			}
		}
	}
	if in.Path == "" {
		if in.Path, err = original(ctx, a, as, report); err != nil {
			return nil, err
		}
		in.Source, in.FPS = "original", as.Media.FPS
	}
	dir, err := scratchDir(a, j)
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	report(0.3, "finding where the picture switches")
	st, err := calls.Compute(ctx, a.Tools, in, dir)
	if err != nil {
		return nil, err
	}
	id, err := calls.Save(ctx, a.DB, a.WorkspaceID(), as.ID, st, a.Tools.Version(ctx, "ffmpeg"))
	if err != nil {
		return nil, err
	}
	_ = a.DB.Update(ctx, store.CAssets, a.WorkspaceID(), as.ID, bson.M{"$set": bson.M{"analysis.switches": "available", "updatedAt": time.Now().UTC()}})
	return map[string]any{"insightId": id, "switches": len(st.Switches), "refined": st.Refined}, nil
}

// callFacts is what the quality check needs about the cut's call footage
// (D161, D162): the flashes at clip edges and their fixes, the clips showing
// names a call app printed (and the name bar that covers them when the cut has
// none), and where the cut's name bar names someone else than who is on screen.
func callFacts(ctx context.Context, a *app.App, spec *editspec.Spec, plan *editspec.RenderPlan, facts *renderFacts, proj *models.Project) *qa.CallFacts {
	// The cut's own footage and its conversations' recordings (a nested cut's
	// footage is read through its own facts, D182).
	ids := editspec.AssetIDs(spec)
	facts.switches = calls.Footage(ctx, a.DB, a.WorkspaceID(), ids, func(id string) []editspec.TranscriptSegment { return facts.tx[id] })
	if !editspec.HasCallFootage(spec, facts) {
		return nil
	}
	cf := &qa.CallFacts{}
	cf.Flashes = editspec.EdgeFlashes(spec, facts)
	for _, f := range cf.Flashes {
		cf.FlashFixes = append(cf.FlashFixes, editspec.EdgeFlashFix(spec, f, facts))
	}
	cf.PrintedClips, cf.Corner = editspec.PrintedNameClips(spec, facts)
	firstNames := false
	for _, r := range content.RulesFor(ctx, a, proj) {
		firstNames = firstNames || r.Measure == "first-names"
	}
	var bar *editspec.Overlay
	for i := range spec.Overlays {
		if spec.Overlays[i].Template == editspec.TplNameBar {
			bar = &spec.Overlays[i]
		}
	}
	if bar == nil {
		if cf.PrintedClips > 0 {
			// The house style's name bar (D199).
			house, _, _ := content.StyleFor(ctx, a, proj.ID)
			look, height, animate := editspec.NameBarStyle(house)
			if nb, ok := editspec.BuildNameBar(spec, facts, editspec.NameBarOptions{FirstNames: firstNames, Style: look, Height: height, Animate: animate}); ok {
				cf.NameBar = &nb
			}
		}
		return cf
	}
	cf.Mismatches = editspec.NameBarMismatches(spec, plan, facts, 0.5)
	if len(cf.Mismatches) > 0 && !bar.Locked {
		// Its names again; its look, height and animation stay as they are.
		look, height, animate := editspec.NameBarLookOf(*bar)
		if nb, ok := editspec.BuildNameBar(spec, facts, editspec.NameBarOptions{ID: bar.ID, FirstNames: firstNames, KeepTitles: true, Style: look, Height: height, Animate: animate}); ok {
			set, _ := json.Marshal(map[string]any{"changes": nb.Overlay.Changes, "params": nb.Overlay.Params})
			cf.Rename = []editspec.Op{{Op: "updateOverlay", ID: bar.ID, Set: set}}
		}
	}
	return cf
}

// queueSwitches asks for a footage's switches once its evidence (or its
// on-screen index, with more to look at) is there.
func queueSwitches(ctx context.Context, w *Worker, j *models.Job) {
	a := w.App
	as, err := loadAsset(ctx, a, j)
	if err != nil || !as.Media.HasVideo {
		return
	}
	ev := evidence.LoadStored(ctx, a.DB, a.WorkspaceID(), []string{as.ID})[as.ID]
	var idx *visual.Index
	ver := ""
	if st := visual.Load(ctx, a.DB, a.WorkspaceID(), as.ID); st != nil && st.Index != nil {
		idx, ver = st.Index, st.Index.Version
	}
	if !calls.Worth(as, ev, idx) {
		return
	}
	if _, _, err := calls.Queue(ctx, a, as, ver, j.CreatedBy); err != nil {
		w.Log("clip %s: could not ask for its picture switches: %v", as.ID, err)
	}
}

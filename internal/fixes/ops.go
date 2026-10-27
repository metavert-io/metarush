package fixes

import (
	"context"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// ShowIn names a source.
type ShowIn struct {
	ID string `json:"id" jsonschema:"minLength=1,description=The clip (ast_…) or the picture (res_…)."`
}

// LookOut is a fresh measurement on its way.
type LookOut struct {
	View  *View  `json:"view"`
	JobID string `json:"jobId,omitempty"`
}

func init() {
	ops.Def("asset.fixes", "Show a source's fixes: its kind, the fixes it is offered, which fit and why (measured), which are ticked, the fixed copy it uses and the cuts that play it", ops.Flags{}, func(ctx context.Context, a *app.App, in *ShowIn) (*View, error) {
		return view(ctx, a, in.ID)
	})
	ops.Def("asset.fixes.set", "Tick or untick a source's fixes (steady, colour, grain, volumes, voices, blocks, loudness, hum, ends, background, edges, larger) or say what kind of source it is; the original is kept and a fixed copy is made on a worker", ops.Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *SetIn) (*View, error) {
		if strings.HasPrefix(in.ID, "res_") {
			return setPicture(ctx, a, in)
		}
		return set(ctx, a, in)
	})
	ops.Def("asset.fixes.look", "Measure a clip again for its fixes (shake, colour, grain, blockiness, voices, loudness, hum, silent ends)", ops.Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *ShowIn) (*LookOut, error) {
		if strings.HasPrefix(in.ID, "res_") {
			v, err := pictureView(ctx, a, in.ID)
			return &LookOut{View: v}, err
		}
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.ID, &as); err != nil {
			return nil, apperr.New(apperr.NotFound, "MetaRush can't find that clip.")
		}
		j, err := QueueInspect(ctx, a, &as, true)
		if err != nil {
			return nil, err
		}
		v, err := view(ctx, a, as.ID)
		if err != nil {
			return nil, err
		}
		return &LookOut{View: v, JobID: j.ID}, nil
	})
}

// First-party abilities (D269): how the menu and find show what these
// operations do, and what each needs on a computer.
func init() {
	fix := func(id, title, does, f string, words []string, needs ops.Needs) ops.Offering {
		return ops.Offering{ID: id, Group: "fix", Title: title, Does: does, Input: map[string]any{"tick": []string{f}}, Words: words, Needs: needs}
	}
	video := ops.Needs{Works: []string{"editing"}}
	ops.Offer("asset.fixes.set",
		fix("steady-footage", "Steady shaky footage", "Steadies a shaky camera; the original is kept.", models.FixSteady, []string{"stabilize", "shaky", "camera shake"}, video),
		fix("balance-colour", "Balance colour", "Balances a clip's colour and brightness; the original is kept.", models.FixColour, []string{"color correct", "too dark", "white balance"}, video),
		fix("reduce-grain", "Reduce grain", "Reduces the grain of a dark or noisy picture; the original is kept.", models.FixGrain, []string{"noisy picture", "denoise video"}, video),
		fix("remove-hum", "Remove hum", "Takes the electrical hum out of a recording's sound; the original is kept.", models.FixHum, []string{"buzz", "mains hum"}, video),
		fix("even-loudness", "Even out loudness", "Makes a recording as loud as the rest, all the way through; the original is kept.", models.FixLoudness, []string{"too quiet", "volume"}, video),
		fix("trim-silent-ends", "Trim silent ends", "Trims the silence at the start and end of a recording; the original is kept.", models.FixEnds, []string{"cut the silence at the start"}, video),
		fix("remove-background", "A photo's subject", "Takes the background out of a photo or a logo, leaving its subject; a background that isn't one plain colour needs a Mac.", models.FixBackground, []string{"cut out", "transparent background"},
			ops.Needs{OS: []string{"mac"}, Partly: "only plain-colour backgrounds"}),
		fix("larger-pictures", "Larger, sharper pictures", "Makes a small picture larger and sharper for a full-screen shot.", models.FixLarger, []string{"upscale", "low resolution"}, ops.Needs{}))
}

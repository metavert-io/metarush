// Package withdrawn answers whether a version plays anything made with a
// consent that was withdrawn (D227): generated shots and pictures of a person,
// their voice copy's speech, their presenter's clips, matched lips — in the
// version itself or in a cut it nests, at any depth. Review links and guests
// ask it before showing a version to anyone outside the workspace, and both
// refuse when it can't answer: a lookup that failed, or more nested cuts than
// MaxNested.
package withdrawn

import (
	"context"
	"errors"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// MaxNested is how many versions Plays looks through for one version.
var MaxNested = 500

// Made is what was made with consents that were withdrawn: each clip, take
// or picture, and whose consent it was.
func Made(ctx context.Context, a *app.App) (map[string]string, error) {
	var cs []models.Consent
	if err := a.DB.Find(ctx, store.CConsents, a.WorkspaceID(), bson.M{"revokedAt": bson.M{"$exists": true, "$ne": nil}}, &cs); err != nil {
		return nil, err
	}
	out := map[string]string{}
	for i := range cs {
		made, err := MadeWith(ctx, a, &cs[i])
		if err != nil {
			return nil, err
		}
		for id := range made {
			if _, ok := out[id]; !ok {
				out[id] = cs[i].PersonName
			}
		}
	}
	return out, nil
}

// In says whether a version plays anything in gone (what was made with
// withdrawn consents) and whose consent it was. A version with more nested
// cuts than MaxNested is bad too, with who "": it can't be said to be clear.
func In(ctx context.Context, a *app.App, revisionID string, gone map[string]string) (who string, bad bool, err error) {
	if len(gone) == 0 {
		return "", false, nil
	}
	ps, whole, err := Plays(ctx, a, revisionID)
	if err != nil {
		return "", false, err
	}
	for _, id := range ps {
		if who, ok := gone[id]; ok {
			if who == "" {
				who = "someone it shows"
			}
			return who, true, nil
		}
	}
	if !whole {
		return "", true, nil
	}
	return "", false, nil
}

// Version is the whole check of one version: Made, then In.
func Version(ctx context.Context, a *app.App, revisionID string) (who string, bad bool, err error) {
	gone, err := Made(ctx, a)
	if err != nil {
		return "", false, err
	}
	return In(ctx, a, revisionID, gone)
}

// Plays is every clip, take and picture a version plays: its own, and (as
// What may leave counts them) those of the cuts it nests, at any depth, and
// the pictures its graphics pin. whole: every nested cut was looked through
// (false past MaxNested versions).
func Plays(ctx context.Context, a *app.App, revisionID string) (_ []string, whole bool, _ error) {
	var out []string
	seen := map[string]bool{}
	whole = true
	var walk func(id string) error
	walk = func(id string) error {
		if seen[id] {
			return nil
		}
		if len(seen) >= MaxNested {
			whole = false
			return nil
		}
		seen[id] = true
		var rev models.Revision
		if err := a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), id, &rev); err != nil {
			if errors.Is(err, store.ErrNotFound) {
				return nil
			}
			return err
		}
		out = append(out, editspec.AssetIDs(&rev.Spec)...)
		for _, pin := range rev.Spec.Resources {
			out = append(out, pin.ID)
		}
		for _, c := range rev.Spec.Clips {
			if _, nested, ok := editspec.NestedRef(c.AssetID); ok {
				if err := walk(nested); err != nil {
					return err
				}
			}
		}
		return nil
	}
	err := walk(revisionID)
	return out, whole, err
}

// MadeWith are the clips, takes and pictures made with a consent:
//   - AI work that relied on it (a generated picture is a resource);
//   - a presenter made with it: every clip it presents;
//   - a voice copy made with it (withdrawn with it, D227): everything said
//     or changed with that voice, whatever consent later work recorded;
//   - pictures made from any of those on this computer (made larger, its
//     background taken away…), followed through their lineage.
func MadeWith(ctx context.Context, a *app.App, c *models.Consent) (map[string]bool, error) {
	out := map[string]bool{}
	var gs []models.Generation
	if err := a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"consentIds": c.ID}, &gs); err != nil {
		return nil, err
	}
	var presenters []models.Presenter
	if err := a.DB.Find(ctx, store.CPresenters, a.WorkspaceID(), bson.M{"consentId": c.ID}, &presenters); err != nil {
		return nil, err
	}
	var presenterIDs, voices bson.A
	for _, p := range presenters {
		if p.ProviderID != "" {
			presenterIDs = append(presenterIDs, p.ProviderID)
		}
	}
	for _, g := range gs {
		if g.Outcome == nil {
			continue
		}
		for _, o := range g.Outcome.Outputs {
			if o.VoiceID != "" {
				voices = append(voices, o.VoiceID)
			}
			if o.PresenterID != "" {
				presenterIDs = append(presenterIDs, o.PresenterID)
			}
		}
	}
	// What was made with its presenters and voice copies.
	var with bson.A
	if len(presenterIDs) > 0 {
		with = append(with, bson.M{"options.presenter": bson.M{"$in": presenterIDs}})
	}
	if len(voices) > 0 {
		with = append(with, bson.M{"options.voice": bson.M{"$in": voices}})
	}
	if len(with) > 0 {
		var more []models.Generation
		if err := a.DB.Find(ctx, store.CGenerations, a.WorkspaceID(), bson.M{"$or": with}, &more); err != nil {
			return nil, err
		}
		gs = append(gs, more...)
	}
	var pictures []string
	for _, g := range gs {
		if g.Outcome == nil {
			continue
		}
		for _, o := range g.Outcome.Outputs {
			if o.AssetID != "" {
				out[o.AssetID] = true
			}
			if o.ResourceID != "" && !out[o.ResourceID] {
				out[o.ResourceID] = true
				pictures = append(pictures, o.ResourceID)
			}
		}
	}
	if len(voices) > 0 {
		var takes []models.VoiceTake
		if err := a.DB.Find(ctx, store.CVoiceTakes, a.WorkspaceID(), bson.M{"voiceId": bson.M{"$in": voices}}, &takes); err != nil {
			return nil, err
		}
		for _, t := range takes {
			if t.AssetID != "" {
				out[t.AssetID] = true
			}
		}
	}
	// Pictures made from them, and from those, on this computer.
	for round := 0; len(pictures) > 0 && round < 20; round++ {
		var rs []models.Resource
		if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), bson.M{"versions.source.lineage.resourceId": bson.M{"$in": pictures}}, &rs); err != nil {
			return nil, err
		}
		pictures = nil
		for _, r := range rs {
			if !out[r.ID] {
				out[r.ID] = true
				pictures = append(pictures, r.ID)
			}
		}
	}
	return out, nil
}

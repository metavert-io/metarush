package motion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// docID is a component version's document ID: one per workspace and hash.
func docID(workspaceID, hash string) string { return workspaceID + "/" + hash }

// Get loads one version by its full hash.
func Get(ctx context.Context, db *store.DB, workspaceID, hash string) (*models.MotionComponent, error) {
	var c models.MotionComponent
	if err := db.GetByID(ctx, store.CMotion, workspaceID, docID(workspaceID, hash), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// List returns the workspace's motion graphics, newest check first. Without
// all, only the latest version of each name.
func List(ctx context.Context, db *store.DB, workspaceID string, all bool) ([]models.MotionComponent, error) {
	var cs []models.MotionComponent
	if err := db.Find(ctx, store.CMotion, workspaceID, bson.M{}, &cs, options.Find().SetSort(bson.D{{Key: "check.at", Value: -1}, {Key: "updatedAt", Value: -1}}).SetLimit(2000)); err != nil {
		return nil, err
	}
	if all {
		return cs, nil
	}
	seen := map[string]bool{}
	out := cs[:0]
	for _, c := range cs {
		if seen[c.Name] {
			continue
		}
		seen[c.Name] = true
		out = append(out, c)
	}
	return out, nil
}

// Find returns the versions matching a reference (name and/or hash prefix),
// newest check first.
func Find(ctx context.Context, db *store.DB, workspaceID string, r Ref) ([]models.MotionComponent, error) {
	filter := bson.M{}
	if r.Name != "" {
		filter["name"] = r.Name
	}
	if r.Hash != "" {
		if ValidHash(r.Hash) {
			filter["hash"] = r.Hash
		} else {
			filter["hash"] = bson.M{"$gte": r.Hash, "$lt": r.Hash + "g"} // hex prefix range
		}
	}
	var cs []models.MotionComponent
	err := db.Find(ctx, store.CMotion, workspaceID, filter, &cs, options.Find().SetSort(bson.D{{Key: "check.at", Value: -1}, {Key: "updatedAt", Value: -1}}).SetLimit(200))
	return cs, err
}

// Resolve finds exactly one version: name@prefix, a bare prefix, or a name
// (its latest check). Errors are plain and say what to do.
func Resolve(ctx context.Context, db *store.DB, workspaceID string, ref string) (*models.MotionComponent, error) {
	r, err := ParseRef(ref)
	if err != nil {
		return nil, err
	}
	cs, err := Find(ctx, db, workspaceID, r)
	if err != nil {
		return nil, err
	}
	if len(cs) == 0 && r.Name != "" && r.Hash != "" {
		// Checked under another folder name: the version is what counts.
		if cs, err = Find(ctx, db, workspaceID, Ref{Hash: r.Hash}); err != nil {
			return nil, err
		}
	}
	switch {
	case len(cs) == 0:
		return nil, apperr.New(apperr.NotFound, "MetaRush doesn't know the motion graphic %s.", r).
			WithHint("It needs a check first; then it shows up under Motion graphics for a person to approve.").
			WithCommand("metarush motion check " + firstNonEmpty(r.Name, "<folder>"))
	case len(cs) > 1 && r.Hash != "":
		var vs []string
		for _, c := range cs {
			vs = append(vs, c.Name+"@"+Short(c.Hash))
		}
		return nil, apperr.New(apperr.Conflict, "More than one version matches %s.", r).WithHint("Give more of the version.").WithDetails(map[string]any{"matches": vs})
	}
	return &cs[0], nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToLower(s[:1]) + s[1:]
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// InfoOf is what validation and planning need to know about a version.
func InfoOf(c *models.MotionComponent) editspec.MotionInfo {
	pictures, fonts := c.PictureAndFontNames()
	return editspec.MotionInfo{Name: c.Name, Hash: c.Hash, State: c.State, Region: c.Region, MinDuration: c.MinDuration, MaxDuration: c.MaxDuration, Schema: json.RawMessage(c.SchemaJSON), Resources: pictures,
		Fonts: fonts, Kind: c.Kind, Libraries: c.Libraries, Persistent: c.Persistent, Stills: Stills(c), CheckSeconds: c.Check.Duration}
}

// Stills are a checked version's test frames with the shape each was drawn
// in (D187): what the dashboard's preview may show in the graphic's place.
// A version that was never drawn has none.
func Stills(c *models.MotionComponent) []editspec.MotionStill {
	var out []editspec.MotionStill
	for i, f := range c.Frames {
		if f.Object.Key == "" {
			continue
		}
		w, h := f.Object.Width, f.Object.Height
		if w == 0 || h == 0 {
			w, h = c.Check.Width, c.Check.Height
			for _, s := range c.Shapes {
				if s.Label == f.Shape {
					w, h = s.Width, s.Height
					break
				}
			}
		}
		out = append(out, editspec.MotionStill{Index: i, Label: f.Label, At: f.At, Width: w, Height: h})
	}
	return out
}

// Infos loads the versions a spec uses (missing ones are simply absent).
func Infos(ctx context.Context, db *store.DB, workspaceID string, hashes []string) (map[string]editspec.MotionInfo, error) {
	out := map[string]editspec.MotionInfo{}
	if len(hashes) == 0 {
		return out, nil
	}
	ids := make([]string, len(hashes))
	for i, h := range hashes {
		ids[i] = docID(workspaceID, h)
	}
	var cs []models.MotionComponent
	if err := db.Find(ctx, store.CMotion, workspaceID, bson.M{"_id": bson.M{"$in": ids}}, &cs); err != nil {
		return nil, err
	}
	for i := range cs {
		out[cs[i].Hash] = InfoOf(&cs[i])
	}
	return out, nil
}

// Versions lists the full hashes of checked versions starting with prefix
// (cut sheets: route-map@3fa9c2d1).
func Versions(ctx context.Context, db *store.DB, workspaceID, prefix string) []string {
	cs, err := Find(ctx, db, workspaceID, Ref{Hash: strings.ToLower(prefix)})
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range cs {
		out = append(out, c.Hash)
	}
	sort.Strings(out)
	return out
}

// SaveCheck records a check: new versions start waiting for a person; a
// version people already decided about keeps its decisions (the code is the
// same bytes).
func SaveCheck(ctx context.Context, db *store.DB, c *models.MotionComponent) (*models.MotionComponent, error) {
	now := time.Now().UTC()
	c.ID, c.UpdatedAt = docID(c.WorkspaceID, c.Hash), now
	set := bson.M{
		"name": c.Name, "summary": c.Summary, "region": c.Region, "minDuration": c.MinDuration, "maxDuration": c.MaxDuration,
		"checkAt": c.CheckAt, "schemaJson": c.SchemaJSON, "previewJson": c.PreviewJSON, "files": c.Files, "imports": c.Imports,
		"warnings": c.Warnings, "archive": c.Archive, "sheet": c.Sheet, "frames": c.Frames, "video": c.Video, "check": c.Check, "updatedAt": now,
		"resources": c.Resources, "persistent": c.Persistent, "shapes": c.Shapes,
		"kind": c.Kind, "libraries": c.Libraries, "checkedWith": c.CheckedWith, "usedBy": c.UsedBy,
	}
	if c.ProjectID != "" {
		set["projectId"] = c.ProjectID
	}
	var out models.MotionComponent
	err := db.C(store.CMotion).FindOneAndUpdate(ctx, bson.M{"_id": c.ID, "workspaceId": c.WorkspaceID},
		bson.M{"$set": set, "$setOnInsert": bson.M{"workspaceId": c.WorkspaceID, "hash": c.Hash, "state": models.MotionWaiting, "createdAt": now}},
		options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

// Decision verbs.
const (
	Approve = "approve"
	Reject  = "reject"
	Revoke  = "revoke"
)

// Decide records a person's decision about a version. Approve works from
// waiting, rejected or revoked; reject from waiting or revoked; revoke only
// from approved.
func Decide(ctx context.Context, db *store.DB, workspaceID, hash, verb string, by models.Actor, note string) (*models.MotionComponent, error) {
	c, err := Get(ctx, db, workspaceID, hash)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.New(apperr.NotFound, "MetaRush doesn't know that motion graphic version.").
				WithHint("It needs a check first; then it shows up under Motion graphics for a person to approve.").WithCommand("metarush motion check <folder>")
		}
		return nil, err
	}
	who := fmt.Sprintf("This version of %s", c.Name)
	state := ""
	switch verb {
	case Approve:
		if c.State == models.MotionApproved {
			return nil, apperr.New(apperr.Conflict, "%s is already approved.", who)
		}
		state = models.MotionApproved
	case Reject:
		if c.State == models.MotionApproved {
			return nil, apperr.New(apperr.Conflict, "%s is approved, so it can't be turned down. Withdraw the approval instead.", who).
				WithHint("Withdraw it under Motion graphics.").WithCommand("metarush motion revoke " + c.Name + "@" + Short(c.Hash))
		}
		if c.State == models.MotionRejected {
			return nil, apperr.New(apperr.Conflict, "%s was already turned down.", who)
		}
		state = models.MotionRejected
	case Revoke:
		if c.State != models.MotionApproved {
			return nil, apperr.New(apperr.Conflict, "%s isn't approved, so there is no approval to withdraw.", who)
		}
		state = models.MotionRevoked
	default:
		return nil, apperr.New(apperr.InvalidInput, "A motion graphic can be approved, turned down or have its approval withdrawn, not “%s”.", verb)
	}
	now := time.Now().UTC()
	d := models.MotionDecision{State: state, By: by, At: now, Note: note}
	set := bson.M{"state": state, "updatedAt": now}
	if state == models.MotionApproved {
		set["approvedBy"], set["approvedAt"] = by, now
	}
	// Compare-and-swap on the state, so two people deciding at once can't both win.
	res, err := db.C(store.CMotion).UpdateOne(ctx, bson.M{"_id": c.ID, "workspaceId": workspaceID, "state": c.State}, bson.M{"$set": set, "$push": bson.M{"decisions": d}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, apperr.New(apperr.Conflict, "Someone else decided about %s at the same moment. Look at it again.", lowerFirst(who))
	}
	return Get(ctx, db, workspaceID, hash)
}

// RequireApproved refuses work on a spec that uses a motion graphic no person
// approved (or whose approval was withdrawn). Workers call it before
// rendering and before QA.
func RequireApproved(ctx context.Context, db *store.DB, workspaceID string, s *editspec.Spec) (map[string]*models.MotionComponent, error) {
	out := map[string]*models.MotionComponent{}
	names := map[string]string{}
	for _, o := range s.Overlays {
		if o.Motion != nil {
			names[o.Motion.Hash] = o.Motion.Name
		}
	}
	for _, h := range editspec.MotionHashes(s) {
		c, err := Get(ctx, db, workspaceID, h)
		if errors.Is(err, store.ErrNotFound) {
			return nil, apperr.New(apperr.Validation, "This cut uses a version of the motion graphic %s that hasn't been checked in this workspace, so it can't be rendered.", names[h]).
				WithDetails(map[string]any{"reason": "motion_unknown", "hash": h, "version": names[h] + "@" + Short(h)}).
				WithHint("It needs a check first; then a person approves it under Motion graphics.").WithCommand("metarush motion check " + firstNonEmpty(names[h], "<folder>"))
		}
		if err != nil {
			return nil, err
		}
		if c.State != models.MotionApproved {
			why := "no person has approved it yet"
			switch c.State {
			case models.MotionRevoked:
				why = "its approval was withdrawn"
			case models.MotionRejected:
				why = "a person turned it down"
			}
			// The name the cut uses (the same version may have been checked under another folder name).
			name := names[h]
			if name == "" {
				name = c.Name
			}
			return nil, apperr.New(apperr.Validation, "This cut uses a version of the motion graphic %s, and %s, so it can't be rendered.", name, why).
				WithDetails(map[string]any{"reason": "motion_not_approved", "hash": h, "state": c.State, "version": name + "@" + Short(h)}).
				WithHint("A person can look at it and approve it under Motion graphics in the dashboard.").WithCommand("metarush motion approve " + name + "@" + Short(h))
		}
		out[h] = c
	}
	return out, nil
}

var _ = mongo.ErrNoDocuments

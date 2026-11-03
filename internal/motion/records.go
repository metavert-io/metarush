package motion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
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
	info := editspec.MotionInfo{Name: c.Name, Hash: c.Hash, State: c.State, Region: c.Region, MinDuration: c.MinDuration, MaxDuration: c.MaxDuration, Schema: json.RawMessage(c.SchemaJSON), Resources: pictures,
		Fonts: fonts, Kind: c.Kind, Libraries: c.Libraries, Persistent: c.Persistent, Stills: Stills(c), CheckSeconds: c.Check.Duration}
	for _, k := range c.ApprovedSizes() {
		var w, h int
		if _, err := fmt.Sscanf(k, "%dx%d", &w, &h); err == nil {
			info.ApprovedShapes = append(info.ApprovedShapes, editspec.ShapeOfSize(w, h))
		}
	}
	return info
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

// SaveCheck records a check: a new version starts waiting for a person. A
// version checked before — the same bytes, so the same code — is only ever
// added to (D381): shapes it wasn't drawn in yet, with their frames and
// previews, a draw record and new notes; the frames, previews, contact sheet
// and check a person saw stay exactly as they were, and so do its decisions.
// An approved version from before approved shapes were recorded gets them
// written down (every shape it was drawn in) before anything is added.
func SaveCheck(ctx context.Context, db *store.DB, c *models.MotionComponent) (*models.MotionComponent, error) {
	now := time.Now().UTC()
	c.ID, c.UpdatedAt = docID(c.WorkspaceID, c.Hash), now
	for attempt := 0; attempt < 6; attempt++ {
		old, err := Get(ctx, db, c.WorkspaceID, c.Hash)
		if errors.Is(err, store.ErrNotFound) {
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
			// Only a first check inserts: one that loses the race is merged next time round.
			var out models.MotionComponent
			err := db.C(store.CMotion).FindOneAndUpdate(ctx, bson.M{"_id": c.ID, "workspaceId": c.WorkspaceID, "createdAt": bson.M{"$exists": false}},
				bson.M{"$set": set, "$setOnInsert": bson.M{"workspaceId": c.WorkspaceID, "hash": c.Hash, "state": models.MotionWaiting, "createdAt": now}},
				options.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(options.After)).Decode(&out)
			if mongo.IsDuplicateKeyError(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			return &out, nil
		}
		if err != nil {
			return nil, err
		}
		set, push, err := mergeCheck(old, c)
		if err != nil {
			return nil, err
		}
		if set == nil {
			return old, nil // nothing new: every shape was drawn before
		}
		set["updatedAt"] = now
		update := bson.M{"$set": set}
		if push != nil {
			update["$push"] = bson.M{"draws": *push}
		}
		var out models.MotionComponent
		err = db.C(store.CMotion).FindOneAndUpdate(ctx, bson.M{"_id": old.ID, "workspaceId": c.WorkspaceID, "updatedAt": old.UpdatedAt}, update,
			options.FindOneAndUpdate().SetReturnDocument(options.After)).Decode(&out)
		if errors.Is(err, mongo.ErrNoDocuments) {
			continue // changed meanwhile (a decision, another draw): merge again
		}
		if err != nil {
			return nil, err
		}
		return &out, nil
	}
	return nil, apperr.New(apperr.Conflict, "The motion graphic %s changed while this check was saved. Check it again.", c.Name)
}

// MaxDrawnShapes bounds the shapes one version is drawn in over all its
// checks (D381); one check adds at most MaxShapes.
const MaxDrawnShapes = 9

// mergeCheck is what a check adds to a version checked before (D381): the
// shapes it drew that the version lacks, their frames and previews, notes not
// there yet, the graphics a library's check drew; nil when it adds nothing.
func mergeCheck(old, c *models.MotionComponent) (bson.M, *models.MotionDraw, error) {
	// A shape is drawn when any size of its ratio was (D379).
	have := map[string]bool{}
	for _, k := range old.DrawnSizes() {
		have[ratioOfKey(k)] = true
	}
	shapes := append([]models.MotionShape{}, old.Shapes...)
	if len(shapes) == 0 && old.Check.Width > 0 && old.Check.Height > 0 && len(old.Frames) > 0 {
		// Checked in one shape before D164: that shape, with its preview.
		shapes = []models.MotionShape{{Label: ShapeLabel(old.Check.Width, old.Check.Height), Width: old.Check.Width, Height: old.Check.Height, Video: old.Video}}
	}
	var fresh []models.MotionShape
	for _, sh := range c.Shapes {
		k := sizeKeyOf(sh.Width, sh.Height)
		if k == "" || have[ratioOfKey(k)] {
			continue
		}
		have[ratioOfKey(k)] = true
		fresh = append(fresh, sh)
	}
	// New shapes are named by their word, with the ratio when another shape
	// (kept or new) has the same word; the shapes kept keep their names.
	words := map[string]int{}
	for _, sh := range shapes {
		words[strings.SplitN(sh.Label, " ", 2)[0]]++
	}
	for _, sh := range fresh {
		words[ShapeLabel(sh.Width, sh.Height)]++
	}
	if len(fresh) > 0 && len(shapes)+len(fresh) > MaxDrawnShapes {
		// Full: refused, never by dropping a shape (least of all an approved one).
		return nil, nil, apperr.New(apperr.Conflict, "%s is already drawn in %d shapes; MetaRush draws one version in at most %d, so it wasn't drawn in more.", old.Name, len(shapes), MaxDrawnShapes).
			WithHint("The shapes it has stay as they are. For more shapes, change the graphic and check the new version.")
	}
	label := map[string]string{}
	var added []string
	for _, sh := range fresh {
		k := sizeKeyOf(sh.Width, sh.Height)
		sh.Label = ShapeLabel(sh.Width, sh.Height)
		if words[sh.Label] > 1 {
			sh.Label += " " + editspec.ShapeOfSize(sh.Width, sh.Height).Ratio
		}
		label[k] = sh.Label
		shapes = append(shapes, sh)
		added = append(added, k)
	}
	frames := append([]models.MotionFrame{}, old.Frames...)
	for _, f := range c.Frames {
		if l, ok := label[sizeKeyOf(f.Object.Width, f.Object.Height)]; ok {
			f.Shape = l
			frames = append(frames, f)
		}
	}
	warnings := append([]models.MotionProblem{}, old.Warnings...)
	seen := map[string]bool{}
	for _, w := range warnings {
		seen[w.Code+"\x00"+w.Message] = true
	}
	moreNotes := false
	for _, w := range c.Warnings {
		if !seen[w.Code+"\x00"+w.Message] {
			seen[w.Code+"\x00"+w.Message] = true
			warnings = append(warnings, w)
			moreNotes = true
		}
	}
	usedBy := append([]string{}, old.UsedBy...)
	moreUses := false
	for _, u := range c.UsedBy {
		if !slices.Contains(usedBy, u) {
			usedBy = append(usedBy, u)
			moreUses = true
		}
	}
	if len(added) == 0 && !moreNotes && !moreUses {
		return nil, nil, nil
	}
	set := bson.M{"warnings": warnings, "usedBy": usedBy}
	var draw *models.MotionDraw
	if len(added) > 0 {
		set["shapes"], set["frames"] = shapes, frames
		draw = &models.MotionDraw{At: c.Check.At, By: c.Check.By, Machine: c.Check.Machine, Renderer: c.Check.Renderer, Shapes: added}
		if covered := old.ApprovedSizes(); old.State == models.MotionApproved && old.ApprovedShapes == nil && len(covered) > 0 {
			// What the approval covered, written down before anything is added.
			set["approvedShapes"] = covered
		}
	}
	if old.ProjectID == "" && c.ProjectID != "" {
		set["projectId"] = c.ProjectID
	}
	return set, draw, nil
}

func sizeKeyOf(w, h int) string {
	return editspec.ShapeOfSize(w, h).Key()
}

// ratioOfKey is a size key's ratio ("1920x1080" → "16:9").
func ratioOfKey(k string) string {
	var w, h int
	if _, err := fmt.Sscanf(k, "%dx%d", &w, &h); err != nil {
		return k
	}
	return editspec.ShapeOfSize(w, h).RatioKey()
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
//
// shapes are the shapes a person looked at (D381: sizes like 1920x1080 or a
// shape's word). Approving a version covers them, or every shape it was drawn
// in when none are given; approving an approved version in shapes it isn't
// approved in yet adds them.
func Decide(ctx context.Context, db *store.DB, workspaceID, hash, verb string, by models.Actor, note string, shapes ...string) (*models.MotionComponent, error) {
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
	seen, err := lookedAt(c, shapes)
	if err != nil {
		return nil, err
	}
	switch verb {
	case Approve:
		if c.State == models.MotionApproved {
			return approveShapes(ctx, db, c, by, note, seen)
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
		if seen == nil {
			// Callers name the shapes the person was shown (the dashboard, the
			// CLI, the panel's card); one that names none gets only the first
			// check's shapes, never a later drawing nobody may have seen (D384).
			seen = c.OriginalSizes()
		}
		set["approvedBy"], set["approvedAt"], set["approvedShapes"] = by, now, seen
		d.Shapes = seen
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

// lookedAt reads the shapes a person says they looked at (D381) as the sizes
// the version was drawn at: a size, or a word, matches a drawn size of the
// same ratio. Nil when none are given.
func lookedAt(c *models.MotionComponent, shapes []string) ([]string, error) {
	if len(shapes) == 0 {
		return nil, nil
	}
	drawn := c.DrawnSizes()
	var out []string
	for _, raw := range shapes {
		want, ok := editspec.ParseShape(strings.ToLower(strings.TrimSpace(raw)))
		if !ok {
			return nil, apperr.New(apperr.InvalidInput, "“%s” isn't a shape: say vertical, landscape, square or a size like 1080x1350.", raw)
		}
		match := ""
		for _, k := range drawn {
			var w, h int
			if _, err := fmt.Sscanf(k, "%dx%d", &w, &h); err == nil && editspec.ShapeOfSize(w, h).RatioKey() == want.RatioKey() {
				match = k
				break
			}
		}
		if match == "" {
			return nil, apperr.New(apperr.InvalidInput, "%s was never drawn %s (%s), so there is nothing to look at in that shape yet.", c.Name, want.Word, want.Ratio).
				WithHint("Draw it in that shape first; then look at its frames and approve it.").WithCommand("metarush motion check --version " + c.Name + "@" + Short(c.Hash) + " --shape " + editspec.TestSize(want.Width, want.Height).Key())
		}
		if !slices.Contains(out, match) {
			out = append(out, match)
		}
	}
	return out, nil
}

// approveShapes adds shapes to an approved version's approval (D381).
func approveShapes(ctx context.Context, db *store.DB, c *models.MotionComponent, by models.Actor, note string, seen []string) (*models.MotionComponent, error) {
	who := fmt.Sprintf("This version of %s", c.Name)
	covered := c.ApprovedSizes()
	var add []string
	for _, k := range seen {
		if !slices.Contains(covered, k) {
			add = append(add, k)
		}
	}
	if len(add) == 0 {
		var waiting []string
		for _, k := range c.DrawnSizes() {
			if !slices.Contains(covered, k) {
				waiting = append(waiting, k)
			}
		}
		e := apperr.New(apperr.Conflict, "%s is already approved%s.", who, map[bool]string{true: " in those shapes", false: ""}[len(seen) > 0])
		if len(waiting) > 0 {
			e = e.WithHint("It also has test frames in shapes nobody approved yet; look at them and approve it in those shapes too.").
				WithCommand("metarush motion approve " + c.Name + "@" + Short(c.Hash) + " --shape " + waiting[0])
		}
		return nil, e
	}
	now := time.Now().UTC()
	d := models.MotionDecision{State: models.MotionApproved, By: by, At: now, Note: note, Shapes: add}
	filter := bson.M{"_id": c.ID, "workspaceId": c.WorkspaceID, "state": models.MotionApproved}
	if c.ApprovedShapes == nil {
		filter["approvedShapes"] = bson.M{"$exists": false}
	} else {
		filter["approvedShapes"] = c.ApprovedShapes
	}
	res, err := db.C(store.CMotion).UpdateOne(ctx, filter, bson.M{"$set": bson.M{"approvedShapes": append(append([]string{}, covered...), add...), "updatedAt": now}, "$push": bson.M{"decisions": d}})
	if err != nil {
		return nil, err
	}
	if res.MatchedCount == 0 {
		return nil, apperr.New(apperr.Conflict, "Someone else decided about %s at the same moment. Look at it again.", lowerFirst(who))
	}
	return Get(ctx, db, c.WorkspaceID, c.Hash)
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

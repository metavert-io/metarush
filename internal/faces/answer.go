package faces

import (
	"context"
	"errors"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/names"
	"github.com/metavert-io/metarush/internal/naming"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
)

// Answers about a group of faces (D155): one answer names the group, says it
// isn't someone, skips it, or joins it with another group of the clip.

// ErrNoGroup means the clip has no unnamed faces in that group any more (it
// was answered meanwhile, or the clip was looked at again).
var ErrNoGroup = errors.New("those faces were named or changed meanwhile")

// unnamed is the filter for a clip's faces of a group nobody named yet.
func unnamed(assetID, groupID string) bson.M {
	return bson.M{"assetId": assetID, "group": groupID, "personId": bson.M{"$in": bson.A{nil, ""}}, "recognized": nil, "skipped": bson.M{"$ne": true}}
}

// GroupMembers lists the unnamed faces of a clip's group, in time order.
func GroupMembers(ctx context.Context, a *app.App, assetID, groupID string) ([]models.FaceTrack, error) {
	var fs []models.FaceTrack
	if err := a.DB.Find(ctx, store.CFaces, a.WorkspaceID(), unnamed(assetID, groupID), &fs,
		options.Find().SetSort(bson.D{{Key: "from", Value: 1}, {Key: "track", Value: 1}})); err != nil {
		return nil, err
	}
	return fs, nil
}

// GroupNamed is what naming a group did.
type GroupNamed struct {
	// Named are the faces confirmed by the answer; Aside the faces left out
	// because they look less like the rest (asked about later on their own).
	Named int `json:"named"`
	Aside int `json:"aside"`
}

// NameGroup records a person's answer that a clip's group of faces is person:
// every face that fits the group (GroupFit) is confirmed through the group,
// the others are set aside, the group's questions close, and the library is
// re-scored once.
func NameGroup(ctx context.Context, a *app.App, assetID, groupID string, person models.Person) (*GroupNamed, error) {
	res, err := nameGroup(ctx, a, assetID, groupID, person)
	if err != nil {
		return nil, err
	}
	if _, err := Refresh(ctx, a, assetID); err != nil {
		return nil, err
	}
	return res, nil
}

// GroupAnswer is an answer about one group of a clip's faces: who they are.
type GroupAnswer struct {
	GroupID string
	Person  models.Person
}

// NameGroups records several answers about one clip's groups together (the
// names a clip shows, accepted in one click): every group is named before the
// library is re-scored once, so naming one person can't take faces from
// another's group first. Each answer has its result or its error.
func NameGroups(ctx context.Context, a *app.App, assetID string, answers []GroupAnswer) ([]*GroupNamed, []error, error) {
	res := make([]*GroupNamed, len(answers))
	errs := make([]error, len(answers))
	named := 0
	for i, x := range answers {
		res[i], errs[i] = nameGroup(ctx, a, assetID, x.GroupID, x.Person)
		if errs[i] == nil {
			named++
		}
	}
	if named > 0 {
		if _, err := Refresh(ctx, a, assetID); err != nil {
			return res, errs, err
		}
	}
	return res, errs, nil
}

// nameGroup is NameGroup without re-scoring the library.
func nameGroup(ctx context.Context, a *app.App, assetID, groupID string, person models.Person) (*GroupNamed, error) {
	fs, err := GroupMembers(ctx, a, assetID, groupID)
	if err != nil {
		return nil, err
	}
	if len(fs) == 0 {
		return nil, ErrNoGroup
	}
	vecs := make([][]float32, len(fs))
	qs := make([]float64, len(fs))
	for i, f := range fs {
		vecs[i], qs[i] = Vector(f), f.Quality
	}
	fit := fits(vecs, qs)
	var named, aside []string
	for i, f := range fs {
		if fit[i] {
			named = append(named, f.ID)
		} else {
			aside = append(aside, f.ID)
		}
	}
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	if len(named) > 0 {
		if _, err := a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": ws, "_id": bson.M{"$in": named}}, bson.M{
			"$set": bson.M{"personId": person.ID, "name": person.Name, "confirmedBy": a.Actor, "confirmedAt": now, "confirmedVia": "group", "confirmedGroup": groupID,
				"naming": naming.Record(ctx, a.Actor), "updatedAt": now},
			"$unset": bson.M{"candidate": "", "recognized": "", "asideFrom": "", "checked": ""},
			"$pull":  bson.M{"rejected": person.ID}}); err != nil {
			return nil, err
		}
	}
	if len(aside) > 0 {
		if _, err := a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": ws, "_id": bson.M{"$in": aside}}, bson.M{
			"$set":   bson.M{"asideFrom": person.ID, "updatedAt": now},
			"$unset": bson.M{"group": "", "candidate": ""}}); err != nil {
			return nil, err
		}
	}
	if _, err := closeGroupQuestions(ctx, a, assetID, groupID, models.ResolvedElsewhere, "Named "+person.Name+"."); err != nil {
		return nil, err
	}
	if len(named) > 0 {
		onScreenAgain(ctx, a, assetID, person.ID)
	}
	a.Audit(ctx, "footage.faces.group", "asset", assetID, "", map[string]any{"group": groupID, "personId": person.ID, "name": person.Name, "named": len(named), "aside": len(aside)})
	return &GroupNamed{Named: len(named), Aside: len(aside)}, nil
}

// RejectGroup records that a clip's group of faces is not someone: a person
// (personID), or a name the video shows or says that nobody has in People yet
// (name). It is never suggested for those faces again.
func RejectGroup(ctx context.Context, a *app.App, assetID, groupID, personID, name string) (int, error) {
	fs, err := GroupMembers(ctx, a, assetID, groupID)
	if err != nil {
		return 0, err
	}
	if len(fs) == 0 {
		return 0, ErrNoGroup
	}
	ids := make([]string, 0, len(fs))
	for _, f := range fs {
		ids = append(ids, f.ID)
	}
	add := bson.M{}
	if personID != "" {
		add["rejected"] = personID
	}
	if k := names.Fold(name); k != "" {
		add["notNames"] = k
	}
	if len(add) == 0 {
		return 0, nil
	}
	if _, err := a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "_id": bson.M{"$in": ids}},
		bson.M{"$addToSet": add, "$unset": bson.M{"candidate": ""}, "$set": bson.M{"updatedAt": time.Now().UTC()}}); err != nil {
		return 0, err
	}
	a.Audit(ctx, "footage.faces.group.reject", "asset", assetID, "", map[string]any{"group": groupID, "personId": personID, "faces": len(ids)})
	_, err = Candidates(ctx, a, assetID)
	return len(ids), err
}

// SkipGroup records that a person doesn't want to be asked about a clip's
// group of faces: they are never asked about again (they can still be named by
// hand, or by their looks).
func SkipGroup(ctx context.Context, a *app.App, assetID, groupID string) (int, error) {
	res, err := a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": assetID, "group": groupID, "personId": bson.M{"$in": bson.A{nil, ""}}},
		bson.M{"$set": bson.M{"skipped": true, "updatedAt": time.Now().UTC()}, "$unset": bson.M{"candidate": ""}})
	if err != nil {
		return 0, err
	}
	a.Audit(ctx, "footage.faces.group.skip", "asset", assetID, "", map[string]any{"group": groupID, "faces": res.ModifiedCount})
	return int(res.ModifiedCount), nil
}

// JoinGroups records that two groups of a clip are one person ("Same as…"):
// from's faces join into, and the clip's questions are worked out again (one
// question for both).
func JoinGroups(ctx context.Context, a *app.App, assetID, into, from string) (int, error) {
	if into == from {
		return 0, errors.New("that is the same question")
	}
	keep, err := GroupMembers(ctx, a, assetID, into)
	if err != nil {
		return 0, err
	}
	if len(keep) == 0 {
		return 0, ErrNoGroup
	}
	res, err := a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": assetID, "group": from, "personId": bson.M{"$in": bson.A{nil, ""}}},
		bson.M{"$set": bson.M{"group": into, "updatedAt": time.Now().UTC()}})
	if err != nil {
		return 0, err
	}
	if res.ModifiedCount == 0 {
		return 0, ErrNoGroup
	}
	if _, err := closeGroupQuestions(ctx, a, assetID, from, models.ResolvedElsewhere, "The same person as another question about this clip."); err != nil {
		return 0, err
	}
	a.Audit(ctx, "footage.faces.group.join", "asset", assetID, "", map[string]any{"into": into, "from": from, "faces": res.ModifiedCount})
	_, err = Candidates(ctx, a, assetID)
	return int(res.ModifiedCount), err
}

// closeGroupQuestions closes the open questions about one group of a clip.
func closeGroupQuestions(ctx context.Context, a *app.App, assetID, groupID, resolution, note string) (int, error) {
	var open []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"state": models.TodoOpen, "kind": models.TodoFaceGroup, "assetId": assetID, "context.groupId": groupID}, &open,
		options.Find().SetProjection(bson.M{"groupKey": 1})); err != nil {
		return 0, err
	}
	n := 0
	for _, t := range open {
		if ok, err := todos.Close(ctx, a, t.GroupKey, resolution, note); err != nil {
			return n, err
		} else if ok {
			n++
		}
	}
	return n, nil
}

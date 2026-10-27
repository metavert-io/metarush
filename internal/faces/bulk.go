package faces

import (
	"context"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/names"
	"github.com/metavert-io/metarush/internal/store"
)

// Answers about many faces at once (D189): "None of these are Lena" (every
// question that suggests her), "Not Lena anywhere in this clip", and
// skipping every question of a clip or a project. Each changes the faces as
// the one-by-one answers do, and the clip's questions are worked out again
// once at the end.

// NotIn records that a clip's groups of faces are not a person (personID), or
// not a name the video shows or says that nobody has in People yet (name): it
// is rejected for every unnamed face of those groups (as "No, someone else"
// does for one), without working the clip's questions out again (Candidates
// does, once, after).
func NotIn(ctx context.Context, a *app.App, assetID string, groups []string, personID, name string) (int, error) {
	add := bson.M{}
	if personID != "" {
		add["rejected"] = personID
	}
	if k := names.Fold(name); k != "" && personID == "" {
		add["notNames"] = k
	}
	if len(groups) == 0 || len(add) == 0 {
		return 0, nil
	}
	res, err := a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "assetId": assetID, "group": bson.M{"$in": groups},
		"personId": bson.M{"$in": bson.A{nil, ""}}, "recognized": nil}, bson.M{"$addToSet": add, "$unset": bson.M{"candidate": ""},
		"$set": bson.M{"updatedAt": time.Now().UTC()}})
	if err != nil {
		return 0, err
	}
	a.Audit(ctx, "footage.faces.group.reject", "asset", assetID, "", map[string]any{"groups": groups, "personId": personID, "faces": res.ModifiedCount})
	return int(res.ModifiedCount), nil
}

// NotHereResult is what "not on screen anywhere in this clip" changed.
type NotHereResult struct {
	// Faces no longer suggested (or named by their looks) as the person.
	Faces int `json:"faces"`
	// Kept are faces a person confirmed as them in the clip (left as they are).
	Kept int `json:"kept"`
}

// NotHere records that a person appears nowhere on screen in a clip: they
// are remembered on the clip (Asset.NotOnScreen, so faces found later aren't
// them either), rejected for every face there a person didn't confirm as
// them (names given by looks go), and their voice is no evidence for its
// faces. The clip's questions are worked out again.
func NotHere(ctx context.Context, a *app.App, assetID, personID string) (*NotHereResult, error) {
	now := time.Now().UTC()
	ws := a.WorkspaceID()
	if err := a.DB.Update(ctx, store.CAssets, ws, assetID, bson.M{"$addToSet": bson.M{"notOnScreen": personID}, "$set": bson.M{"updatedAt": now}}); err != nil {
		return nil, err
	}
	out := &NotHereResult{}
	kept, err := a.DB.C(store.CFaces).CountDocuments(ctx, bson.M{"workspaceId": ws, "assetId": assetID, "personId": personID})
	if err != nil {
		return nil, err
	}
	out.Kept = int(kept)
	filter := bson.M{"workspaceId": ws, "assetId": assetID, "personId": bson.M{"$ne": personID}, "rejected": bson.M{"$ne": personID}}
	res, err := a.DB.C(store.CFaces).UpdateMany(ctx, filter, bson.M{"$addToSet": bson.M{"rejected": personID}, "$set": bson.M{"updatedAt": now}})
	if err != nil {
		return nil, err
	}
	out.Faces = int(res.ModifiedCount)
	for _, field := range []string{"recognized", "candidate"} {
		if _, err := a.DB.C(store.CFaces).UpdateMany(ctx, bson.M{"workspaceId": ws, "assetId": assetID, field + ".personId": personID},
			bson.M{"$unset": bson.M{field: ""}, "$set": bson.M{"updatedAt": now}}); err != nil {
			return nil, err
		}
	}
	a.Audit(ctx, "footage.faces.notHere", "asset", assetID, "", map[string]any{"personId": personID, "faces": out.Faces, "kept": out.Kept})
	if _, err := Candidates(ctx, a, assetID); err != nil {
		return out, err
	}
	return out, nil
}

// onScreenAgain forgets that a person is nowhere on screen in a clip (a person
// just named one of its faces as them).
func onScreenAgain(ctx context.Context, a *app.App, assetID, personID string) {
	_, _ = a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": assetID, "workspaceId": a.WorkspaceID(), "notOnScreen": personID},
		bson.M{"$pull": bson.M{"notOnScreen": personID}})
}

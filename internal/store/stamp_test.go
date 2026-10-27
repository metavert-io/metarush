package store

import (
	"context"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

// A person's change to a project or a footage records them; system work and
// other collections don't (D183).
func TestStampRecordsWhoChanged(t *testing.T) {
	who := map[string]string{"userId": "usr_1"}
	ctx := WithChanger(context.Background(), who)
	u := bson.M{"$set": bson.M{"title": "x", "updatedAt": 1}}
	stamp(ctx, CAssets, u)
	if u["$set"].(bson.M)["updatedBy"] == nil {
		t.Fatal("a person's change isn't stamped")
	}
	for _, c := range []struct {
		ctx  context.Context
		coll string
		u    bson.M
	}{
		{context.Background(), CAssets, bson.M{"$set": bson.M{"updatedAt": 1}}},
		{ctx, CJobs, bson.M{"$set": bson.M{"updatedAt": 1}}},
		{ctx, CProjects, bson.M{"$set": bson.M{"state": "x"}}},
	} {
		stamp(c.ctx, c.coll, c.u)
		if c.u["$set"].(bson.M)["updatedBy"] != nil {
			t.Fatalf("stamped: %s %+v", c.coll, c.u)
		}
	}
}

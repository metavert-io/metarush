package leave_test

import (
	"context"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/receipt"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// The workspace's database keeps words, and the voice and face prints
// MetaRush knows people by: a client whose rules keep their voices or faces
// from cloud storage can't have the database elsewhere on the internet. A
// move says so naming the prints, the receipt names them, and so does a
// restore into such a database — from the archive's own rules, before any
// record goes in.
func TestDatabaseKeepsWordsVoicesAndFaces(t *testing.T) {
	cloud := config.Database{URI: "mongodb+srv://cluster0.example.mongodb.net/", Name: "studio"}
	here := config.Database{URI: "mongodb://127.0.0.1:27018/", Name: "studio"}
	if k := leave.DatabaseKept(cloud); strings.Join(k.Kinds, ",") != "voices,faces,words" {
		t.Fatalf("the database keeps %v", k.Kinds)
	}
	for kind, prints := range map[string]string{models.LeaveVoices: "voice prints", models.LeaveFaces: "face prints"} {
		t.Run(kind, func(t *testing.T) {
			a := testapp.New(t)
			ctx := context.Background()
			acme := do[ops.ProjectOut](t, a, "project.create", ops.ProjectCreateIn{Name: "Acme"}, ops.Caller{}).Project
			do[ops.ContentShowOut](t, a, "content.own", ops.ContentOwnIn{ProjectID: acme.ID}, ops.Caller{})
			do[leave.LeaveOut](t, a, "content.leave.set", leave.LeaveSetIn{ProjectID: acme.ID, Keep: map[string][]string{models.PlaceStorage: {kind}}}, person)

			err := ops.LeaveDatabase(ctx, a, cloud, true)
			if e := apperr.From(err); e.Code != apperr.KeptHere || !strings.Contains(e.Message, prints) {
				t.Fatalf("moving the database to the cloud with a client keeping %s: %v", kind, err)
			}
			if err := ops.LeaveDatabase(ctx, a, here, true); err != nil {
				t.Fatalf("a database on this computer: %v", err)
			}

			// The receipt of a workspace whose database is elsewhere names them.
			inCloud := *a
			cfg := *a.Cfg
			cfg.Database = cloud
			inCloud.Cfg = &cfg
			l, err := ops.CallTyped[receipt.ProjectLeavesOut](ctx, &inCloud, "project.leaves", receipt.ProjectLeavesIn{ProjectID: acme.ID}, ops.Caller{})
			if err != nil {
				t.Fatal(err)
			}
			if stored := strings.Join(l.Left.Stored, " "); !strings.Contains(stored, "voice and face prints") {
				t.Fatalf("the receipt says: %q", stored)
			}
			if p := strings.Join(leave.StorageProblems(&inCloud, &models.LeaveRules{Places: map[string]models.LeavePlace{models.PlaceStorage: {Keep: []string{kind}}}}), " "); !strings.Contains(p, prints) {
				t.Fatalf("what the rules would say: %q", p)
			}

			// A restore checks the archive's own rules before anything goes in.
			each := func(coll string, fn func(bson.Raw) error) error {
				cur, err := a.DB.DB.Collection(coll).Find(ctx, bson.M{})
				if err != nil {
					return err
				}
				defer cur.Close(ctx)
				for cur.Next(ctx) {
					if err := fn(cur.Current); err != nil {
						return err
					}
				}
				return cur.Err()
			}
			if e := apperr.From(leave.ArchiveDatabaseCheck(cloud, each)); e == nil || e.Code != apperr.KeptHere || !strings.Contains(e.Message, prints) || !strings.Contains(e.Hint, "metarush setup --restore") {
				t.Fatalf("restoring into a database in the cloud: %+v", e)
			}
			if err := leave.ArchiveDatabaseCheck(here, each); err != nil {
				t.Fatalf("restoring into a database on this computer: %v", err)
			}
		})
	}
}

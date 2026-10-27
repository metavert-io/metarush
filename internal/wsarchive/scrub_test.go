package wsarchive_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/wsarchive"
)

func scrubbed(t *testing.T, s *wsarchive.Scrubber, doc bson.D) (bson.M, []string) {
	t.Helper()
	raw, err := bson.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	out, changes, err := s.Doc(raw, nil)
	if err != nil {
		t.Fatal(err)
	}
	var m bson.M
	if err := bson.Unmarshal(out, &m); err != nil {
		t.Fatal(err)
	}
	var changed []string
	for _, c := range changes {
		changed = append(changed, c.Field)
	}
	return m, changed
}

// Taking keys out of an archive leaves people's words alone: "the basic
// questions", "token: 3 per person" and a word that happens to be a weak
// password stay as they are, and so do identifiers — a storage key's ID is
// the copy's way to remove that key later. Only credential shapes and the
// exact keys this computer knows (eight characters or more) are taken out.
func TestScrubTakesOutCredentialsNotWords(t *testing.T) {
	known := "Zq8" + "studioSecretValue"
	s := wsarchive.NewScrubber("studio", known)
	keyID := "tid_" + strings.Repeat("Ab3", 8)
	awsID := "AKIA" + strings.Repeat("Q", 16)
	for _, tc := range []struct {
		doc  bson.D
		same []string
	}{
		{bson.D{{Key: "_id", Value: "seg_1"}, {Key: "text", Value: "the basic questions come first"}}, []string{"text"}},
		{bson.D{{Key: "_id", Value: "seg_2"}, {Key: "text", Value: "token: 3 per person, cookie: chocolate"}}, []string{"text"}},
		{bson.D{{Key: "_id", Value: "res_1"}, {Key: "madeWith", Value: "metarush/studio-look"}}, []string{"madeWith"}},
		{bson.D{{Key: "_id", Value: "ins_1"}, {Key: "storageKeyId", Value: keyID}, {Key: "storageKey", Value: bson.D{{Key: "keyId", Value: awsID}, {Key: "label", Value: "mini"}}}}, []string{"storageKeyId"}},
		{bson.D{{Key: "_id", Value: keyID}, {Key: "name", Value: "Basic explainer"}}, []string{"_id", "name"}},
	} {
		got, changed := scrubbed(t, s, tc.doc)
		if len(changed) != 0 {
			t.Errorf("%v: changed %v", tc.doc, changed)
		}
		for _, f := range tc.same {
			if got[f] != asMap(tc.doc)[f] {
				t.Errorf("%s changed: %v → %v", f, asMap(tc.doc)[f], got[f])
			}
		}
	}
	if got, _ := scrubbed(t, s, bson.D{{Key: "_id", Value: "ins_2"}, {Key: "storageKey", Value: bson.D{{Key: "keyId", Value: awsID}}}}); asMap(got["storageKey"])["keyId"] != awsID {
		t.Errorf("a storage key's ID was taken out: %v", got)
	}
	// What is a credential still goes.
	key := "sk-ant-" + strings.Repeat("k3y", 10)
	got, changed := scrubbed(t, s, bson.D{{Key: "_id", Value: "cmt_1"}, {Key: "text", Value: "use " + key + " and " + known + " on mongodb://maya:" + "pa55w0rd" + "@db.example.com/x"}})
	text := got["text"].(string)
	if len(changed) != 1 || strings.Contains(text, key) || strings.Contains(text, known) || strings.Contains(text, "pa55w0rd") || !strings.Contains(text, "mongodb://maya:") {
		t.Fatalf("credentials: %q %v", text, changed)
	}
}

// Two Recipes records whose names a broad scrub would have made equal ("Basic
// explainer", "Basic interviews") export and restore: names are never
// rewritten, and the export checks every changed record against its
// collection's unique indexes before it says it is ready.
func TestScrubNeverBreaksUniqueIndexes(t *testing.T) {
	src := testutil.DB(t)
	st := fsStore(t)
	ctx := context.Background()
	ws := ids.New(ids.Workspace)
	now := time.Now().UTC()
	for coll, docs := range map[string][]bson.M{
		store.CWorkspaces: {{"_id": ws, "workspaceId": ws, "name": "FernBrook Studio"}},
		store.CRecipes: {
			{"_id": ids.New(ids.Recipe), "workspaceId": ws, "name": "Basic explainer", "createdAt": now},
			{"_id": ids.New(ids.Recipe), "workspaceId": ws, "name": "Basic interviews", "createdAt": now},
		},
		store.CComments: {{"_id": ids.New(ids.Comment), "workspaceId": ws, "text": "key " + "sk-ant-" + strings.Repeat("k3y", 10)}},
	} {
		for _, d := range docs {
			if _, err := src.C(coll).InsertOne(ctx, d); err != nil {
				t.Fatal(err)
			}
		}
	}
	res := exportTo(t, src, st, ws, wsarchive.MediaReferenced, false)
	if c := collection(res.Manifest, store.CRecipes); c.Scrubbed != 0 {
		t.Fatalf("names in Recipes were changed: %+v", c)
	}
	if c := collection(res.Manifest, store.CComments); c.Scrubbed != 1 || len(c.ScrubbedAt) != 1 || c.ScrubbedAt[0].Kind == "" {
		t.Fatalf("the comment's key: %+v", c)
	}
	if res.Manifest.Scrubbed.Records != 1 || !strings.Contains(res.Manifest.Scrubbed.Says, "1 comment") {
		t.Fatalf("the archive should say what was changed: %+v", res.Manifest.Scrubbed)
	}
	rep, err := restoreInto(t, res.Path, emptyDB(t), st, false)
	if err != nil || !rep.Verified {
		t.Fatalf("restore: %v", err)
	}
}

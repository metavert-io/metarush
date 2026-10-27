package store_test

import (
	"context"
	"reflect"
	"sort"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/oldnames"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
)

// elems are a document's fields but the ones named, for comparing what a
// rename must leave alone.
func elems(t *testing.T, doc bson.Raw, skip ...string) map[string]bson.RawValue {
	t.Helper()
	all, err := doc.Elements()
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bson.RawValue{}
	for _, e := range all {
		out[e.Key()] = e.Value()
	}
	for _, k := range skip {
		delete(out, k)
	}
	return out
}

func sameElems(t *testing.T, what string, a, b map[string]bson.RawValue) {
	t.Helper()
	if len(a) != len(b) {
		t.Errorf("%s: %d fields, then %d", what, len(a), len(b))
	}
	for k, v := range a {
		if !v.Equal(b[k]) {
			t.Errorf("%s: %s changed from %s to %s", what, k, v, b[k])
		}
	}
}

// D210's migration 37 renames the stored fields in place: every value stays
// byte for byte (the fixed copies' fingerprints and IDs, how resources were
// made, a refresh's steps) and everything else in the documents too; the old
// names go; a document with both names keeps the old value (only older builds
// write it); running it again changes nothing. Readers and filters take
// either name, before and after.
func TestMigration37RenamesInPlace(t *testing.T) {
	db := testutil.DB(t)
	ctx := context.Background()
	const ws = "wsp_d210"
	now := time.Date(2026, 10, 23, 12, 0, 0, 0, time.UTC)
	fp, other := "74e3b9d105f005ce", "c9527fb01b922022"
	copyID := ws + "/" + "ab12:" + fp
	fixedRef := bson.D{{Key: "id", Value: copyID}, {Key: "sha256", Value: "cd34"}, {Key: "storageKey", Value: "k"}, {Key: "size", Value: int64(9)},
		{Key: "mime", Value: "video/mp4"}, {Key: oldnames.Fingerprint, Value: fp}, {Key: "fixes", Value: bson.A{models.FixSteady}}, {Key: "at", Value: now}}
	madeWith := bson.D{{Key: "kind", Value: "code"}, {Key: "repoId", Value: "res_repo"}, {Key: "repoSha", Value: "abc"}, {Key: "file", Value: "main.go"},
		{Key: "from", Value: int32(1)}, {Key: "to", Value: int32(3)}}
	docs := map[string][]any{
		store.CAssets: {
			bson.D{{Key: "_id", Value: "ast_old"}, {Key: "workspaceId", Value: ws}, {Key: "sha256", Value: "ab12"}, {Key: "fixes", Value: bson.D{
				{Key: "kind", Value: models.SourceCamera}, {Key: "ticked", Value: bson.A{models.FixSteady}}, {Key: oldnames.Fingerprint, Value: fp},
				{Key: "state", Value: models.FixReady}, {Key: "copy", Value: fixedRef}}}},
			// An older build ticked something else after the new name was written.
			bson.D{{Key: "_id", Value: "ast_both"}, {Key: "workspaceId", Value: ws}, {Key: "sha256", Value: "ab13"}, {Key: "fixes", Value: bson.D{
				{Key: "fingerprint", Value: fp}, {Key: oldnames.Fingerprint, Value: other}}}},
			bson.D{{Key: "_id", Value: "ast_none"}, {Key: "workspaceId", Value: ws}, {Key: "sha256", Value: "ab14"}, {Key: "fixes", Value: bson.D{{Key: oldnames.Fingerprint, Value: ""}}}},
			bson.D{{Key: "_id", Value: "ast_new"}, {Key: "workspaceId", Value: ws}, {Key: "sha256", Value: "ab15"}, {Key: "fixes", Value: bson.D{{Key: "fingerprint", Value: fp}}}},
		},
		store.CResources: {
			bson.D{{Key: "_id", Value: "res_old"}, {Key: "workspaceId", Value: ws}, {Key: "name", Value: "slide"}, {Key: "fixes", Value: bson.D{{Key: oldnames.Fingerprint, Value: fp}, {Key: "copy", Value: fixedRef}}},
				{Key: "versions", Value: bson.A{
					bson.D{{Key: "sha256", Value: "s1"}, {Key: "source", Value: bson.D{{Key: "kind", Value: models.FromFile}, {Key: "at", Value: now}}}},
					bson.D{{Key: "sha256", Value: "s2"}, {Key: "source", Value: bson.D{{Key: "kind", Value: models.FromGenerated}, {Key: "tool", Value: "code slide"},
						{Key: oldnames.MadeWith, Value: madeWith}, {Key: "at", Value: now}}}},
				}}},
			bson.D{{Key: "_id", Value: "res_both"}, {Key: "workspaceId", Value: ws}, {Key: "name", Value: "card"}, {Key: "versions", Value: bson.A{
				bson.D{{Key: "sha256", Value: "s3"}, {Key: "source", Value: bson.D{{Key: "kind", Value: models.FromGenerated},
					{Key: "madeWith", Value: bson.D{{Key: "kind", Value: "stats"}, {Key: "repoId", Value: "res_stale"}}},
					{Key: oldnames.MadeWith, Value: bson.D{{Key: "kind", Value: "stats"}, {Key: "repoId", Value: "res_repo"}}}}}},
			}}},
		},
		store.CSourceCopies: {
			bson.D{{Key: "_id", Value: copyID}, {Key: "workspaceId", Value: ws}, {Key: "assetId", Value: "ast_old"}, {Key: "sourceSha256", Value: "ab12"},
				{Key: oldnames.Fingerprint, Value: fp}, {Key: oldnames.FixSteps, Value: bson.D{{Key: "version", Value: int32(1)}, {Key: "kind", Value: models.SourceCamera},
					{Key: "steps", Value: bson.A{bson.D{{Key: "fix", Value: models.FixSteady}, {Key: "engine", Value: "vidstab"}, {Key: "params", Value: bson.D{{Key: "shakiness", Value: "8"}}}}}}}},
				{Key: "state", Value: models.FixReady}, {Key: "sha256", Value: "cd34"}, {Key: "createdAt", Value: now}},
		},
		store.CRemotionRefreshes: {
			bson.D{{Key: "_id", Value: "rmf_old"}, {Key: "workspaceId", Value: ws}, {Key: "number", Value: int32(1)}, {Key: oldnames.RefreshSteps, Value: bson.D{
				{Key: "steps", Value: bson.A{bson.D{{Key: "run", Value: "npm run vo"}, {Key: "kind", Value: models.RemotionStepScript}, {Key: "name", Value: "vo"}}}},
				{Key: "keys", Value: bson.A{bson.D{{Key: "provider", Value: "elevenlabs"}, {Key: "env", Value: "ELEVEN_API_KEY"}}}},
				{Key: "outputs", Value: bson.A{"public/vo/"}}}},
				{Key: "fingerprint", Value: "planfp"}, {Key: "state", Value: models.RemotionRunDone}},
		},
	}
	for coll, ds := range docs {
		if _, err := db.C(coll).InsertMany(ctx, ds); err != nil {
			t.Fatal(err)
		}
	}
	raw := func(coll, id string) bson.Raw {
		t.Helper()
		var r bson.Raw
		if err := db.C(coll).FindOne(ctx, bson.M{"_id": id}).Decode(&r); err != nil {
			t.Fatalf("%s %s: %v", coll, id, err)
		}
		return r
	}
	type decoded struct {
		assets    map[string]models.Asset
		resources map[string]models.Resource
		copy      models.SourceCopy
		refresh   models.RemotionRefresh
	}
	read := func() decoded {
		t.Helper()
		d := decoded{assets: map[string]models.Asset{}, resources: map[string]models.Resource{}}
		for _, id := range []string{"ast_old", "ast_both", "ast_none", "ast_new"} {
			var as models.Asset
			if err := db.GetByID(ctx, store.CAssets, ws, id, &as); err != nil {
				t.Fatal(err)
			}
			d.assets[id] = as
		}
		for _, id := range []string{"res_old", "res_both"} {
			var r models.Resource
			if err := db.GetByID(ctx, store.CResources, ws, id, &r); err != nil {
				t.Fatal(err)
			}
			d.resources[id] = r
		}
		if err := db.GetByID(ctx, store.CSourceCopies, ws, copyID, &d.copy); err != nil {
			t.Fatal(err)
		}
		if err := db.GetByID(ctx, store.CRemotionRefreshes, ws, "rmf_old", &d.refresh); err != nil {
			t.Fatal(err)
		}
		return d
	}
	ids := func(coll string, filter bson.M) []string {
		t.Helper()
		var out []struct {
			ID string `bson:"_id"`
		}
		if err := db.Find(ctx, coll, ws, filter, &out); err != nil {
			t.Fatal(err)
		}
		var s []string
		for _, o := range out {
			s = append(s, o.ID)
		}
		sort.Strings(s)
		return s
	}
	// readable checks what the readers and filters see, before and after.
	readable := func(when string) decoded {
		t.Helper()
		d := read()
		if f := d.assets["ast_old"].Fixes; f == nil || f.Fingerprint != fp || f.Copy == nil || f.Copy.Fingerprint != fp || f.Copy.ID != copyID {
			t.Errorf("%s: a clip's fixes: %+v", when, f)
		}
		if f := d.assets["ast_both"].Fixes; f == nil || f.Fingerprint != other {
			t.Errorf("%s: both names, the old one wins: %+v", when, f)
		}
		if f := d.assets["ast_none"].Fixes; f == nil || f.Fingerprint != "" {
			t.Errorf("%s: no copy: %+v", when, f)
		}
		r := d.resources["res_old"]
		if r.Fixes == nil || r.Fixes.Fingerprint != fp || r.Fixes.Copy == nil || r.Fixes.Copy.Fingerprint != fp || len(r.Versions) != 2 || r.Versions[0].Source.MadeWith != nil ||
			r.Versions[1].Source.MadeWith == nil || r.Versions[1].Source.MadeWith.RepoID != "res_repo" || r.Versions[1].Source.MadeWith.To != 3 || r.Versions[1].Source.Tool != "code slide" {
			t.Errorf("%s: a picture and how its versions were made: %+v %+v", when, r.Fixes, r.Versions)
		}
		if mw := d.resources["res_both"].Versions[0].Source.MadeWith; mw == nil || mw.RepoID != "res_repo" {
			t.Errorf("%s: both names, the old one wins: %+v", when, mw)
		}
		if c := d.copy; c.Fingerprint != fp || len(c.FixSteps.Steps) != 1 || c.FixSteps.Steps[0].Params["shakiness"] != "8" || c.FixSteps.Version != 1 {
			t.Errorf("%s: a fixed copy: %+v", when, c)
		}
		if rs := d.refresh.RefreshSteps; len(rs.Steps) != 1 || rs.Steps[0].Run != "npm run vo" || len(rs.Keys) != 1 || rs.Outputs[0] != "public/vo/" || d.refresh.Fingerprint != "planfp" {
			t.Errorf("%s: a refresh's steps: %+v", when, d.refresh)
		}
		for _, c := range []struct {
			filter bson.M
			want   []string
		}{
			{oldnames.FingerprintIs(fp), []string{"ast_new", "ast_old"}},
			{oldnames.FingerprintIs(other), []string{"ast_both"}},
			{oldnames.FingerprintIs(""), []string{"ast_none"}},
		} {
			if got := ids(store.CAssets, c.filter); !reflect.DeepEqual(got, c.want) {
				t.Errorf("%s: %v matches %v, want %v", when, c.filter, got, c.want)
			}
		}
		if got := ids(store.CResources, oldnames.MadeFrom("res_repo")); !reflect.DeepEqual(got, []string{"res_both", "res_old"}) {
			t.Errorf("%s: made from the repository: %v", when, got)
		}
		return d
	}

	before := readable("before the migration")
	was := map[string]bson.Raw{}
	for coll, ds := range docs {
		for _, d := range ds {
			id := d.(bson.D)[0].Value.(string)
			was[coll+" "+id] = raw(coll, id)
		}
	}
	var m37 *store.Migration
	for i := range store.Migrations {
		if store.Migrations[i].Version == 37 {
			m37 = &store.Migrations[i]
		}
	}
	if m37 == nil {
		t.Fatal("migration 37 is listed")
	}
	if err := m37.Up(ctx, db); err != nil {
		t.Fatal(err)
	}

	// The values moved to the new names, untouched; nothing else changed.
	oldA, newA := was[store.CAssets+" ast_old"], raw(store.CAssets, "ast_old")
	sameElems(t, "a clip", elems(t, oldA, "fixes"), elems(t, newA, "fixes"))
	oldF, newF := oldA.Lookup("fixes").Document(), newA.Lookup("fixes").Document()
	sameElems(t, "a clip's fixes", elems(t, oldF, oldnames.Fingerprint, "copy"), elems(t, newF, "fingerprint", "copy"))
	if !oldF.Lookup(oldnames.Fingerprint).Equal(newF.Lookup("fingerprint")) || newF.Lookup(oldnames.Fingerprint).Type != 0 {
		t.Errorf("a clip's fingerprint: %s", newF)
	}
	oldC, newC := oldF.Lookup("copy").Document(), newF.Lookup("copy").Document()
	sameElems(t, "the copy a clip uses", elems(t, oldC, oldnames.Fingerprint), elems(t, newC, "fingerprint"))
	if !oldC.Lookup(oldnames.Fingerprint).Equal(newC.Lookup("fingerprint")) || newC.Lookup(oldnames.Fingerprint).Type != 0 {
		t.Errorf("the copy's fingerprint: %s", newC)
	}
	if v := raw(store.CAssets, "ast_both").Lookup("fixes", "fingerprint"); v.StringValue() != other {
		t.Errorf("both names: the old value stays: %s", v)
	}
	oldS, newS := was[store.CSourceCopies+" "+copyID], raw(store.CSourceCopies, copyID)
	sameElems(t, "a fixed copy", elems(t, oldS, oldnames.Fingerprint, oldnames.FixSteps), elems(t, newS, "fingerprint", "fixSteps"))
	if !oldS.Lookup(oldnames.Fingerprint).Equal(newS.Lookup("fingerprint")) || !oldS.Lookup(oldnames.FixSteps).Equal(newS.Lookup("fixSteps")) ||
		newS.Lookup(oldnames.FixSteps).Type != 0 || newS.Lookup(oldnames.Fingerprint).Type != 0 {
		t.Errorf("a fixed copy's fingerprint and fix steps: %s", newS)
	}
	oldR, newR := was[store.CResources+" res_old"], raw(store.CResources, "res_old")
	sameElems(t, "a picture", elems(t, oldR, "fixes", "versions"), elems(t, newR, "fixes", "versions"))
	oldV, newV := oldR.Lookup("versions").Array(), newR.Lookup("versions").Array()
	if !oldV.Index(0).Equal(newV.Index(0)) {
		t.Errorf("a version made from a file is untouched: %s", newV.Index(0))
	}
	oldSrc, newSrc := oldV.Index(1).Document().Lookup("source").Document(), newV.Index(1).Document().Lookup("source").Document()
	sameElems(t, "a generated version's source", elems(t, oldSrc, oldnames.MadeWith), elems(t, newSrc, "madeWith"))
	if !oldSrc.Lookup(oldnames.MadeWith).Equal(newSrc.Lookup("madeWith")) || newSrc.Lookup(oldnames.MadeWith).Type != 0 {
		t.Errorf("how it was made: %s", newSrc)
	}
	if v := raw(store.CResources, "res_both").Lookup("versions").Array().Index(0).Document().Lookup("source", "madeWith", "repoId"); v.StringValue() != "res_repo" {
		t.Errorf("both names: the old value stays: %s", v)
	}
	oldX, newX := was[store.CRemotionRefreshes+" rmf_old"], raw(store.CRemotionRefreshes, "rmf_old")
	sameElems(t, "a refresh", elems(t, oldX, oldnames.RefreshSteps), elems(t, newX, "refreshSteps"))
	if !oldX.Lookup(oldnames.RefreshSteps).Equal(newX.Lookup("refreshSteps")) || newX.Lookup(oldnames.RefreshSteps).Type != 0 {
		t.Errorf("a refresh's steps: %s", newX)
	}
	if after := readable("after the migration"); !reflect.DeepEqual(before, after) {
		t.Errorf("the readers see something else after the migration:\n%+v\n%+v", before, after)
	}

	// Again: nothing changes.
	now37 := map[string]bson.Raw{}
	for key := range was {
		coll, id := splitKey(key)
		now37[key] = raw(coll, id)
	}
	if err := m37.Up(ctx, db); err != nil {
		t.Fatal(err)
	}
	for key, r := range now37 {
		coll, id := splitKey(key)
		if !reflect.DeepEqual(r, raw(coll, id)) {
			t.Errorf("running it again changed %s", key)
		}
	}
}

func splitKey(key string) (coll, id string) {
	for i := range key {
		if key[i] == ' ' {
			return key[:i], key[i+1:]
		}
	}
	return key, ""
}

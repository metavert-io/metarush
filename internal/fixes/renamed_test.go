package fixes_test

import (
	"context"
	"image/color"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/oldnames"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// Fixed copies made before D210 stay in use: a picture's fixed version whose
// documents an older build wrote (the old names) is finished by this build's
// worker, read, unticked and ticked again — reused, never made again — and the
// same once migration 37 renamed the fields. Every write leaves the new names
// only.
func TestCopiesMadeBeforeTheRenameStayInUse(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	p := newProject(t, a, "Launch")
	logo := addPicture(t, a, p.ID, "badge.png", badge(color.NRGBA{20, 90, 200, 255}))
	original := logo.Current
	edges := []string{models.FixEdges}
	fp := fixes.Fingerprint(edges, 0)
	copyID := ops.CopyID(a.WorkspaceID(), original, fp)
	renames := []struct{ coll, now, old string }{
		{store.CResources, "fixes.fingerprint", oldnames.FixesFingerprint},
		{store.CResources, "fixes.copy.fingerprint", "fixes.copy." + oldnames.Fingerprint},
		{store.CSourceCopies, "fingerprint", oldnames.Fingerprint},
		{store.CSourceCopies, "fixSteps", oldnames.FixSteps},
	}
	// olden writes the documents back the way builds before D210 did.
	olden := func() {
		t.Helper()
		for _, r := range renames {
			if _, err := a.DB.C(r.coll).UpdateMany(ctx, bson.M{r.now: bson.M{"$exists": true}}, bson.M{"$rename": bson.M{r.now: r.old}}); err != nil {
				t.Fatal(err)
			}
		}
	}
	// oldNames counts the old names the picture and its copy still have.
	oldNames := func() int64 {
		t.Helper()
		var n int64
		for _, r := range renames {
			c, err := a.DB.C(r.coll).CountDocuments(ctx, bson.M{"$or": bson.A{bson.M{"_id": logo.ID}, bson.M{"_id": copyID}}, r.old: bson.M{"$exists": true}})
			if err != nil {
				t.Fatal(err)
			}
			n += c
		}
		return n
	}
	var m37 *store.Migration
	for i := range store.Migrations {
		if store.Migrations[i].Version == 37 {
			m37 = &store.Migrations[i]
		}
	}
	if m37 == nil {
		t.Fatal("migration 37 renames the fields")
	}

	// An older build ticked the fix and recorded the copy to make; this
	// build's worker makes it.
	v := setFixes(t, a, fixes.SetIn{ID: logo.ID, Tick: edges})
	if v.State != models.FixPreparing || len(v.JobIDs) != 1 {
		t.Fatalf("ticked: %+v", v)
	}
	olden()
	if n := oldNames(); n != 3 {
		t.Fatalf("the picture's fingerprint and the copy's fingerprint and fix steps have the old names: %d", n)
	}
	res := runPrepare(t, a, v.JobIDs[0])
	fixed, _ := res["sha256"].(string)
	r := getResource(t, a, logo.ID)
	if r.Current != fixed || fixed == original || r.Fixes == nil || r.Fixes.Fingerprint != fp || r.Fixes.Copy == nil || r.Fixes.Copy.ID != copyID || r.Fixes.Copy.Fingerprint != fp {
		t.Fatalf("the fixed version is current: %+v", r.Fixes)
	}
	if n := oldNames(); n != 0 {
		t.Fatalf("%d old names left after the copy was made", n)
	}

	// Made before the rename: read, unticked, ticked again — first with the
	// old names, then after migration 37 renamed them.
	for _, migrated := range []bool{false, true} {
		olden()
		if migrated {
			if err := m37.Up(ctx, a.DB); err != nil {
				t.Fatal(err)
			}
			if n := oldNames(); n != 0 {
				t.Fatalf("migration 37 left %d old names", n)
			}
		}
		v = fixesView(t, a, logo.ID)
		if v.Using != "fixed" || v.Copy == nil || len(v.Copy.Measures) != 1 {
			t.Fatalf("migrated %v: the fixed version in use: %+v", migrated, v)
		}
		var c models.SourceCopy
		if err := a.DB.GetByID(ctx, store.CSourceCopies, a.WorkspaceID(), copyID, &c); err != nil || c.Fingerprint != fp || len(c.FixSteps.Steps) != 1 || c.FixSteps.Steps[0].Fix != models.FixEdges {
			t.Fatalf("migrated %v: the copy's record: %+v %v", migrated, c, err)
		}
		if v = setFixes(t, a, fixes.SetIn{ID: logo.ID, Only: &[]string{}}); v.Using != "original" || getResource(t, a, logo.ID).Current != original {
			t.Fatalf("migrated %v: unticked: %+v", migrated, v)
		}
		olden()
		v = setFixes(t, a, fixes.SetIn{ID: logo.ID, Tick: edges})
		if r := getResource(t, a, logo.ID); r.Current != fixed || len(v.JobIDs) != 0 || v.State != models.FixReady || r.Fixes.Copy == nil || r.Fixes.Copy.ID != copyID {
			t.Fatalf("migrated %v: ticked again, the copy made before is used: current %s, view %+v", migrated, r.Current, v)
		}
		if n, err := a.DB.C(store.CSourceCopies).CountDocuments(ctx, bson.M{"resourceId": logo.ID}); err != nil || n != 1 {
			t.Fatalf("migrated %v: %d copies (%v)", migrated, n, err)
		}
	}
}

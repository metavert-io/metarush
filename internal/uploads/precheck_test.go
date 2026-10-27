package uploads

import (
	"context"
	"crypto/rand"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func writeFile(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func describe(t *testing.T, p string) PrecheckFile {
	t.Helper()
	fp, size, err := media.FingerprintFile(p)
	if err != nil {
		t.Fatal(err)
	}
	sp, err := media.SampleFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return PrecheckFile{Name: filepath.Base(p), Size: size, Fingerprint: fp, Sample: sp}
}

// storeAsset puts the bytes in the media store and records an older asset
// (no fingerprint or sample yet, as before this feature).
func storeAsset(t *testing.T, a *app.App, p, name, title string) models.Asset {
	t.Helper()
	ctx := context.Background()
	sha, err := media.HashFile(p)
	if err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p)
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if _, err := a.Media.PutFile(ctx, key, p, media.PutOptions{SHA256: sha}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: name, Title: title, SHA256: sha, Size: st.Size(),
		StorageKey: key, State: models.AssetAvailable, Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	return as
}

func TestPrecheckFindsDuplicatesLookalikesAndResumes(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	dir := t.TempDir()
	orig := make([]byte, 5<<20+123)
	_, _ = rand.Read(orig)
	// A look-alike: same size, same first and last MiB (so the same fingerprint),
	// different in the middle. The sampled hash must tell them apart.
	look := append([]byte(nil), orig...)
	for i := 2 << 20; i < 3<<20; i++ {
		look[i] ^= 0xff
	}
	pOrig := writeFile(t, dir, "IMG_0001.MOV", orig)
	as := storeAsset(t, a, pOrig, "IMG_0001.MOV", "Garage interview")

	copyA := describe(t, writeFile(t, dir, "copy of interview.mov", orig))
	lookalike := describe(t, writeFile(t, dir, "lookalike.mov", look))
	if copyA.Fingerprint != lookalike.Fingerprint || copyA.Sample == lookalike.Sample {
		t.Fatal("test setup: the look-alike must share the fingerprint but not the sample")
	}
	again := describe(t, writeFile(t, dir, "another copy.mov", orig))
	sameName := describe(t, writeFile(t, dir, "IMG_0001.MOV", orig))
	fresh := make([]byte, 3<<20)
	_, _ = rand.Read(fresh)
	newFile := describe(t, writeFile(t, dir, "new.mov", fresh))

	// An upload of new.mov was interrupted on this machine at 1 MiB.
	ups := For(a.Cfg.StateDir())
	rec, err := ups.Create("new.mov", newFile.Size, "", newFile.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ups.Part(rec.ID), fresh[:1<<20], 0o600); err != nil {
		t.Fatal(err)
	}
	ups.Update(rec.ID, true, func(r *Record) { r.Offset = 1 << 20 })

	out, err := Precheck(ctx, a, &PrecheckIn{Files: []PrecheckFile{copyA, lookalike, again, sameName, newFile}})
	if err != nil {
		t.Fatal(err)
	}
	r := out.Results
	if d := r[0].Duplicate; d == nil || d.AssetID != as.ID || d.Title != "Garage interview" || d.SameName || !d.InTarget || d.Where != "Inbox" {
		t.Fatalf("copy: %+v", r[0].Duplicate)
	}
	if r[1].Duplicate != nil {
		t.Fatalf("a look-alike with a different sampled hash was called a duplicate: %+v", r[1].Duplicate)
	}
	if r[2].BatchDuplicateOf == nil || *r[2].BatchDuplicateOf != 0 || r[2].Duplicate == nil {
		t.Fatalf("second copy in the same drop: %+v", r[2])
	}
	if r[3].Duplicate == nil || !r[3].Duplicate.SameName {
		t.Fatalf("same filename: %+v", r[3].Duplicate)
	}
	if r[4].Duplicate != nil || r[4].Resume == nil || r[4].Resume.UploadID != rec.ID || r[4].Resume.Offset != 1<<20 {
		t.Fatalf("resume: %+v", r[4])
	}
	if out.Duplicates != 3 || out.Resumable != 1 || out.Backfilled != 1 {
		t.Fatalf("counts: dup %d resumable %d backfilled %d", out.Duplicates, out.Resumable, out.Backfilled)
	}
	// The older asset's fingerprint and sample were computed once and saved.
	var saved models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), as.ID, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Fingerprint != copyA.Fingerprint || saved.Sample != copyA.Sample {
		t.Fatalf("backfill not saved: fp %q sample %q", saved.Fingerprint, saved.Sample)
	}
	out2, err := Precheck(ctx, a, &PrecheckIn{Files: []PrecheckFile{copyA}, ProjectID: ids.New(ids.Project)})
	if err != nil {
		t.Fatal(err)
	}
	if out2.Backfilled != 0 || out2.Results[0].Duplicate == nil || out2.Results[0].Duplicate.InTarget {
		t.Fatalf("second precheck (other project): %+v", out2.Results[0])
	}
}

package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// A clip that plays a fixed copy (D152) is served from its playback record
// (D180): the copy it uses now, the original with its own preview copy
// (?media=original, what a version saved before the fix plays) and a copy a
// version pins (?media=<sha>), the same files while the database answers and,
// for what the record holds, while it doesn't.
func TestFixedCopiesPlayFromThePlaybackRecord(t *testing.T) {
	d := newMediaDash(t)
	ctx := context.Background()
	clip := addClip(t, d.a, "Garden walk", 96<<10)
	copyFile, copyProxy := randomBytes(t, 80<<10), randomBytes(t, 48<<10)
	ck, cs := stored(t, d.a, copyFile, "video/mp4")
	pk, ps := stored(t, d.a, copyProxy, "video/mp4")
	now := time.Now().UTC()
	copyVariants := map[string]models.Variant{
		models.VariantProxy: {Kind: models.VariantProxy, StorageKey: pk, SHA256: ps, Size: int64(len(copyProxy)), MIME: "video/mp4", CreatedAt: now},
	}
	sc := models.SourceCopy{ID: d.a.WorkspaceID() + "/" + clip.asset.SHA256 + ":steady", WorkspaceID: d.a.WorkspaceID(), AssetID: clip.asset.ID,
		SourceSHA256: clip.asset.SHA256, Fingerprint: "steady", State: models.FixReady, SHA256: cs, StorageKey: ck, Size: int64(len(copyFile)),
		MIME: "video/mp4", Variants: copyVariants, CreatedAt: now}
	if err := d.a.DB.Insert(ctx, store.CSourceCopies, sc); err != nil {
		t.Fatal(err)
	}
	// The clip uses its copy now: the copy's variants are the clip's, the
	// original's are set aside (as fixes.Switch leaves them).
	fx := models.SourceFixes{Ticked: []string{models.FixSteady}, Fingerprint: "steady", State: models.FixReady, OriginalVariants: clip.asset.Variants,
		Copy: &models.FixedCopyRef{ID: sc.ID, SHA256: cs, StorageKey: ck, Size: int64(len(copyFile)), MIME: "video/mp4", Fingerprint: "steady", Fixes: []string{models.FixSteady}, At: now}}
	if err := d.a.DB.Update(ctx, store.CAssets, d.a.WorkspaceID(), clip.asset.ID, bson.M{"$set": bson.M{"fixes": fx, "variants": copyVariants, "updatedAt": now}}); err != nil {
		t.Fatal(err)
	}
	base := "/media/assets/" + clip.asset.ID
	shaOf := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	check := func(when, path, wantSHA, wantFrom string) {
		t.Helper()
		ans := d.get(path, "")
		if ans.code != http.StatusOK || shaOf(ans.body) != wantSHA || ans.from != wantFrom {
			t.Errorf("%s, %s: %d from %q, sha %s… (want %s… from %q)", when, path, ans.code, ans.from, shaOf(ans.body)[:12], wantSHA[:12], wantFrom)
		}
	}
	for _, c := range []struct{ path, sha string }{
		{base + "/original?preview=1", cs},                               // what the clip plays now: the copy
		{base + "/original?preview=1&media=original", clip.asset.SHA256}, // a version saved before the fix
		{base + "/original?preview=1&media=" + cs, cs},                   // a version that pins the copy
		{base + "/proxy", ps},                                            // the copy's own preview copy
		{base + "/proxy?media=original", shaOf(clip.proxy)},              // the original's, set aside
	} {
		check("the database answers", c.path, c.sha, "")
	}

	d.link.Cut()
	for _, c := range []struct{ path, sha string }{
		{base + "/original?preview=1", cs},
		{base + "/original?preview=1&media=original", clip.asset.SHA256},
		{base + "/proxy?media=original", shaOf(clip.proxy)},
	} {
		check("in the outage", c.path, c.sha, "memory")
	}
	if ans := d.get(base+"/proxy", "bytes=0-1023"); ans.code != http.StatusPartialContent || !bytes.Equal(ans.body, copyProxy[:1024]) || ans.from != "memory" {
		t.Errorf("the copy's preview copy in the outage: %d from %q", ans.code, ans.from)
	}
	d.link.Restore()
	d.databaseBack(base + "/proxy")
}

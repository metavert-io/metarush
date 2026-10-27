package fixes

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/oldnames"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// fingerprintBeforeD210 is the fingerprint's code as it was before D210
// renamed it, word for word but for its names.
func fingerprintBeforeD210(ticked []string, take int) string {
	fs := copyFixes(ticked)
	if len(fs) == 0 {
		return ""
	}
	m := map[string]any{"v": 1, "fixes": fs, "take": take}
	for _, f := range fs {
		if v := fixVersions[f]; v > 0 {
			m["v:"+f] = v
		}
	}
	b, _ := json.Marshal(m)
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])[:16]
}

// Every fixed copy already made is named by its fingerprint (its record is
// <workspace>/<original>:<fingerprint>, D152), so D210's rename may not change
// a single one. The values are what the code gave before the rename, for real
// sets of fixes: clips, calls, sound and pictures, colour's second version, a
// fix that makes no copy, and takes made again.
func TestFingerprintsAreTheSameAfterTheRename(t *testing.T) {
	cases := []struct {
		ticked []string
		take   int
		before string
	}{
		{[]string{models.FixSteady}, 0, "74e3b9d105f005ce"},
		{[]string{models.FixColour}, 0, "67e7370ad764b77a"},
		{[]string{models.FixSteady, models.FixColour}, 0, "c9527fb01b922022"},
		{[]string{models.FixColour, models.FixSteady, models.FixGrain}, 0, "13c47834ea958ed0"},
		{[]string{models.FixHum, models.FixVoices}, 0, "0777c7ba94864e26"},
		{[]string{models.FixVolumes, models.FixVoices, models.FixLoudness}, 0, "ffa0ac976081e435"},
		{[]string{models.FixBlocks, models.FixGrain}, 0, "3ecd4a08ac89839b"},
		{[]string{models.FixLoudness, models.FixEnds}, 0, "3ecbf7a6b9c69c8a"},
		{[]string{models.FixEnds}, 0, ""},
		{[]string{models.FixBackground, models.FixEdges, models.FixLarger}, 0, "76f949f2635fbf08"},
		{[]string{models.FixLarger}, 0, "f3ae0a155ed72a11"},
		{[]string{models.FixSteady}, 1761200000, "aa4d9de7ee944c7b"},
		{[]string{models.FixVoices, models.FixHum}, 1761234567, "5c8ae214cdbd7439"},
	}
	if StepsVersion != 1 {
		t.Fatalf("the version in every fingerprint changed to %d: no copy made before would be used again", StepsVersion)
	}
	for _, c := range cases {
		after := Fingerprint(c.ticked, c.take)
		if after != c.before || fingerprintBeforeD210(c.ticked, c.take) != c.before {
			t.Errorf("%v (take %d): %q after the rename, %q before", c.ticked, c.take, after, c.before)
		}
		if id := ops.CopyID("wsp_x", "ab12", after); id != "wsp_x/ab12:"+c.before {
			t.Errorf("%v (take %d): the copy's record is %q", c.ticked, c.take, id)
		}
	}
}

// A copy finished while an older build wrote the old name (D210): the clip
// switches to it only when its ticked fixes still ask for it, read under the
// old name first (only older builds write it), and the switch leaves the new
// name only. Going back to the original reads it the same way.
func TestSwitchReadsTheOldName(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	now := time.Now().UTC()
	steady := Fingerprint([]string{models.FixSteady}, 0)
	other := Fingerprint([]string{models.FixSteady, models.FixGrain}, 0)
	original := map[string]models.Variant{models.VariantProxy: {Kind: models.VariantProxy, StorageKey: "proxy-original", SHA256: "p1", Size: 10, MIME: "video/mp4", CreatedAt: now}}
	// clip adds a clip whose stored fixes are set as an older or newer build left them.
	clip := func(name string, set bson.M) models.Asset {
		t.Helper()
		as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), Kind: "video", Name: name + ".mov", Title: name, SHA256: strings.Repeat("ab", 31) + name[:2],
			State: models.AssetAvailable, Media: models.MediaInfo{HasVideo: true, HasAudio: true, Duration: 10}, ProjectIDs: []string{}, Variants: original,
			Fixes:     &models.SourceFixes{Kind: models.SourceCamera, KindBy: models.TitleByPerson, Ticked: []string{models.FixSteady}, State: models.FixPreparing},
			CreatedAt: now, UpdatedAt: now}
		if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
			t.Fatal(err)
		}
		if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": as.ID}, bson.M{"$set": set}); err != nil {
			t.Fatal(err)
		}
		return as
	}
	copyOf := func(as models.Asset) *models.SourceCopy {
		t.Helper()
		c := models.SourceCopy{ID: ops.CopyID(a.WorkspaceID(), as.SHA256, steady), WorkspaceID: a.WorkspaceID(), AssetID: as.ID, SourceSHA256: as.SHA256,
			Fingerprint: steady, FixSteps: models.FixSteps{Version: StepsVersion, Kind: models.SourceCamera, Steps: []models.FixStep{{Fix: models.FixSteady, Engine: "vidstab"}}},
			State: models.FixReady, SHA256: "cd" + as.SHA256[2:], StorageKey: "k-" + as.ID, Size: 900, MIME: "video/mp4",
			Variants:  map[string]models.Variant{models.VariantProxy: {Kind: models.VariantProxy, StorageKey: "proxy-fixed", SHA256: "p2", Size: 9, MIME: "video/mp4", CreatedAt: now}},
			CreatedAt: now, UpdatedAt: now}
		if err := a.DB.Insert(ctx, store.CSourceCopies, c); err != nil {
			t.Fatal(err)
		}
		return FindCopyByFingerprint(ctx, a, as.SHA256, steady)
	}
	// stored reads a clip, and its stored fingerprint under the new and the old name.
	stored := func(id string) (as models.Asset, fp, oldFP *string) {
		t.Helper()
		var raw bson.Raw
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), id, &as); err != nil {
			t.Fatal(err)
		}
		if err := a.DB.C(store.CAssets).FindOne(ctx, bson.M{"_id": id}).Decode(&raw); err != nil {
			t.Fatal(err)
		}
		if v, err := raw.LookupErr("fixes", "fingerprint"); err == nil {
			s := v.StringValue()
			fp = &s
		}
		if v, err := raw.LookupErr("fixes", oldnames.Fingerprint); err == nil {
			s, _ := v.StringValueOK()
			oldFP = &s
		}
		return as, fp, oldFP
	}

	// An older build ticked Steady and wrote the old name.
	older := clip("older", bson.M{oldnames.FixesFingerprint: steady})
	c := copyOf(older)
	if c == nil || c.Fingerprint != steady {
		t.Fatalf("the ready copy is found by its fingerprint: %+v", c)
	}
	if err := switchTo(ctx, a, older.ID, c); err != nil {
		t.Fatal(err)
	}
	as, fp, oldFP := stored(older.ID)
	if !as.Fixed() || as.Fixes.Copy.Fingerprint != steady || as.Fixes.Fingerprint != steady || as.Variants[models.VariantProxy].SHA256 != "p2" {
		t.Fatalf("the clip uses its copy: %+v", as.Fixes)
	}
	if oldFP != nil || fp == nil || *fp != steady {
		t.Fatalf("only the new name is left: %v %v", fp, oldFP)
	}

	// Both names: an older build ticked something else after a newer one
	// wrote the new name. The old name is the newer value, so theirs wins.
	both := clip("both", bson.M{"fixes.fingerprint": steady, oldnames.FixesFingerprint: other})
	if err := switchTo(ctx, a, both.ID, copyOf(both)); err != nil {
		t.Fatal(err)
	}
	if as, _, _ := stored(both.ID); as.Fixed() || as.Fixes.Fingerprint != other {
		t.Fatalf("someone ticked something else meanwhile: %+v", as.Fixes)
	}

	// Back to the original: an older build unticked everything (the old name
	// empty, the new one left from before) — and the other way round.
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": older.ID}, bson.M{"$set": bson.M{oldnames.FixesFingerprint: ""}}); err != nil {
		t.Fatal(err)
	}
	if err := switchTo(ctx, a, older.ID, nil); err != nil {
		t.Fatal(err)
	}
	if as, fp, oldFP := stored(older.ID); as.Fixed() || as.Variants[models.VariantProxy].SHA256 != "p1" || as.Fixes.Fingerprint != "" || oldFP != nil || fp == nil || *fp != "" {
		t.Fatalf("back to the original, under the new name only: %+v %v %v", as.Fixes, fp, oldFP)
	}
	again := clip("again", bson.M{"fixes.fingerprint": "", oldnames.FixesFingerprint: steady})
	if err := switchTo(ctx, a, again.ID, copyOf(again)); err != nil {
		t.Fatal(err)
	}
	if _, err := a.DB.C(store.CAssets).UpdateOne(ctx, bson.M{"_id": again.ID}, bson.M{"$set": bson.M{oldnames.FixesFingerprint: steady, "fixes.fingerprint": ""}}); err != nil {
		t.Fatal(err)
	}
	if err := switchTo(ctx, a, again.ID, nil); err != nil {
		t.Fatal(err)
	}
	if as, _, _ := stored(again.ID); !as.Fixed() || as.Fixes.Fingerprint != steady {
		t.Fatalf("Steady is still ticked under the old name: the copy stays: %+v", as.Fixes)
	}
}

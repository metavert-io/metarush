package models

import (
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/oldnames"
)

func marshal(t *testing.T, d bson.D) []byte {
	t.Helper()
	b, err := bson.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Documents written before D210, or by an older build since, read under the
// new names: the old name wins when a document has both (only older builds
// write it), an old value that is null means none, a document with the new
// name only reads as it is — and writing uses the new names only.
func TestReadsTheNamesBeforeD210(t *testing.T) {
	var f SourceFixes
	if err := bson.Unmarshal(marshal(t, bson.D{{Key: "fingerprint", Value: "stale"}, {Key: oldnames.Fingerprint, Value: "newer"},
		{Key: "copy", Value: bson.D{{Key: "id", Value: "c1"}, {Key: oldnames.Fingerprint, Value: "newer"}}}}), &f); err != nil {
		t.Fatal(err)
	}
	if f.Fingerprint != "newer" || f.Copy == nil || f.Copy.ID != "c1" || f.Copy.Fingerprint != "newer" {
		t.Fatalf("both names: %+v %+v", f, f.Copy)
	}
	var none SourceFixes
	if err := bson.Unmarshal(marshal(t, bson.D{{Key: "fingerprint", Value: "stale"}, {Key: oldnames.Fingerprint, Value: nil}}), &none); err != nil || none.Fingerprint != "" {
		t.Fatalf("an old null is none: %+v %v", none, err)
	}
	var today SourceFixes
	if err := bson.Unmarshal(marshal(t, bson.D{{Key: "fingerprint", Value: "fp"}, {Key: "copy", Value: bson.D{{Key: "fingerprint", Value: "fp"}}}}), &today); err != nil ||
		today.Fingerprint != "fp" || today.Copy.Fingerprint != "fp" {
		t.Fatalf("the new names: %+v %v", today, err)
	}

	var c SourceCopy
	if err := bson.Unmarshal(marshal(t, bson.D{{Key: "_id", Value: "w/ab:fp"}, {Key: oldnames.Fingerprint, Value: "fp"},
		{Key: oldnames.FixSteps, Value: bson.D{{Key: "version", Value: 1}, {Key: "steps", Value: bson.A{bson.D{{Key: "fix", Value: FixSteady}, {Key: "engine", Value: "vidstab"}}}}}}}), &c); err != nil {
		t.Fatal(err)
	}
	if c.ID != "w/ab:fp" || c.Fingerprint != "fp" || c.FixSteps.Version != 1 || len(c.FixSteps.Steps) != 1 || c.FixSteps.Steps[0].Engine != "vidstab" {
		t.Fatalf("a fixed copy: %+v", c)
	}
	var cleared SourceCopy
	if err := bson.Unmarshal(marshal(t, bson.D{{Key: "fixSteps", Value: bson.D{{Key: "version", Value: 1}}}, {Key: oldnames.FixSteps, Value: nil}}), &cleared); err != nil || cleared.FixSteps.Version != 0 {
		t.Fatalf("an old null is none: %+v %v", cleared, err)
	}

	var s ResourceSource
	if err := bson.Unmarshal(marshal(t, bson.D{{Key: "kind", Value: FromGenerated}, {Key: oldnames.MadeWith, Value: bson.D{{Key: "kind", Value: "code"}, {Key: "file", Value: "main.go"}}}}), &s); err != nil ||
		s.MadeWith == nil || s.MadeWith.Kind != "code" || s.MadeWith.File != "main.go" || s.Kind != FromGenerated {
		t.Fatalf("how it was made: %+v %v", s, err)
	}
	var plain ResourceSource
	if err := bson.Unmarshal(marshal(t, bson.D{{Key: "kind", Value: FromFile}, {Key: "madeWith", Value: bson.D{{Key: "kind", Value: "stats"}}}, {Key: oldnames.MadeWith, Value: nil}}), &plain); err != nil || plain.MadeWith != nil {
		t.Fatalf("an old null is none: %+v %v", plain, err)
	}

	var r RemotionRefresh
	if err := bson.Unmarshal(marshal(t, bson.D{{Key: "_id", Value: "rmf_1"}, {Key: "fingerprint", Value: "plan"},
		{Key: oldnames.RefreshSteps, Value: bson.D{{Key: "steps", Value: bson.A{bson.D{{Key: "run", Value: "npm run vo"}}}}, {Key: "outputs", Value: bson.A{"public/"}}}}}), &r); err != nil {
		t.Fatal(err)
	}
	if r.ID != "rmf_1" || r.Fingerprint != "plan" || len(r.RefreshSteps.Steps) != 1 || r.RefreshSteps.Steps[0].Run != "npm run vo" || r.RefreshSteps.Outputs[0] != "public/" {
		t.Fatalf("a refresh's steps: %+v", r)
	}

	// A null reads as nothing, as the default decoder reads it.
	var v ResourceVersion
	if err := bson.Unmarshal(marshal(t, bson.D{{Key: "sha256", Value: "s"}, {Key: "source", Value: nil}}), &v); err != nil || v.SHA256 != "s" || v.Source.Kind != "" {
		t.Fatalf("a null source: %+v %v", v, err)
	}
	var as Asset
	if err := bson.Unmarshal(marshal(t, bson.D{{Key: "_id", Value: "ast_1"}, {Key: "fixes", Value: nil}}), &as); err != nil || as.ID != "ast_1" || as.Fixes != nil {
		t.Fatalf("null fixes: %+v %v", as.Fixes, err)
	}

	// Writing uses the new names only.
	for what, v := range map[string]any{"fixes": f, "copy": c, "source": s, "refresh": r} {
		b, err := bson.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		for _, old := range []string{oldnames.Fingerprint, oldnames.FixSteps, oldnames.MadeWith, oldnames.RefreshSteps} {
			if _, err := bson.Raw(b).LookupErr(old); err == nil {
				t.Errorf("%s is written with the old name %q", what, old)
			}
		}
	}
}

package machines

import (
	"context"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/testutil"
)

func envValue(t *testing.T, file, name string) string {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, name+"="); ok {
			return v
		}
	}
	return ""
}

// The live-bucket CI key (D194) can only touch <bucket>/<prefix>/*: it is
// minted with a policy for that prefix, checked with a round trip, and written
// to a new 0600 file, never over one; minting again with replace deletes the
// older CI keys and their policies; on a store that isn't Tigris it says what
// to do by hand.
func TestCIKeysAreLimitedToTheirPrefix(t *testing.T) {
	f := newFixture(t, "tigris")
	ctx := context.Background()
	file := filepath.Join(t.TempDir(), "ci.env")
	k, err := MintCIKey(ctx, f.app, "", file, false, false)
	if err != nil {
		t.Fatal(err)
	}
	if k.Label != CILabel || k.Prefix != DefaultCIPrefix || k.Bucket != "media" || k.Region != "us-east-1" || !strings.Contains(k.KeyID, "…") || k.File != file {
		t.Fatalf("CI key %+v", k)
	}
	if st, err := os.Stat(file); err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("the file: %v %v", st, err)
	}
	first := envValue(t, file, "METARUSH_TEST_S3_ACCESS_KEY_ID")
	if first == "" || envValue(t, file, "METARUSH_TEST_S3_SECRET_ACCESS_KEY") == "" || envValue(t, file, "METARUSH_TEST_S3_BUCKET") != "media" {
		t.Fatalf("the file's variables are incomplete")
	}
	if key := f.iam.Key(first); key == nil || len(key.Policies) != 1 {
		t.Fatalf("the key and its policy: %+v", key)
	}
	limited := false
	for _, p := range f.iam.Policies {
		limited = limited || strings.Contains(p.Document, "media/ci-test/*")
	}
	if !limited {
		t.Fatal("no policy limits the key to media/ci-test/*")
	}
	// The file is never written over; the key minted for it is taken back.
	before := len(f.iam.Live())
	if _, err := MintCIKey(ctx, f.app, "ci-test", file, false, false); err == nil {
		t.Fatal("an existing file was written over")
	}
	if n := len(f.iam.Live()); n != before {
		t.Fatalf("a key was left behind: %d keys, want %d", n, before)
	}
	// Replacing: a new key, the old one and its policy gone.
	file2 := filepath.Join(t.TempDir(), "ci2.env")
	k2, err := MintCIKey(ctx, f.app, "/ci-test/", file2, true, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(k2.Replaced) != 1 || k2.Replaced[0] != MaskKeyID(first) || f.iam.Key(first) != nil || f.iam.PolicyCount() != 1 || k2.Prefix != "ci-test" {
		t.Fatalf("replaced %+v (old key still there: %v, policies %d)", k2, f.iam.Key(first) != nil, f.iam.PolicyCount())
	}
	// Not Tigris: done by hand.
	r2 := newFixture(t, "r2")
	_, err = MintCIKey(ctx, r2.app, "", filepath.Join(t.TempDir(), "ci.env"), false, false)
	if ae := apperr.From(err); ae.Code != apperr.NotConfigured || !strings.Contains(ae.Hint, "by hand") {
		t.Fatalf("not Tigris: %v", err)
	}
}

// Tigris's IAM answers are read whichever way they say what went wrong, and
// an authorization failure is told apart.
func TestTigrisAnswersAreRead(t *testing.T) {
	for _, c := range []struct {
		raw, status, want string
	}{
		{`{"Message":"Access denied"}`, "403 Forbidden", "Access denied"},
		{`{"message":"key not found"}`, "404 Not Found", "key not found"},
		{`{"error":"bad request"}`, "400 Bad Request", "bad request"},
		{`{"Error":{"Message":"NoSuchEntity"}}`, "404 Not Found", "NoSuchEntity"},
		{`<Error><Code>X</Code><Message>The policy is malformed</Message></Error>`, "400 Bad Request", "The policy is malformed"},
		{"", "502 Bad Gateway", "502 Bad Gateway"},
		{strings.Repeat("z", 400), "500", strings.Repeat("z", 300)},
	} {
		if got := errorMessage([]byte(c.raw), c.status); got != c.want {
			t.Errorf("errorMessage(%.30q) = %.40q, want %.40q", c.raw, got, c.want)
		}
	}
	forbidden := &TigrisError{Status: http.StatusForbidden, Message: "Access denied"}
	if forbidden.Error() != "tigris IAM: HTTP 403: Access denied" || !IsForbidden(forbidden) || !IsForbidden(&TigrisError{Status: 401}) ||
		IsForbidden(&TigrisError{Status: 500}) || IsForbidden(errors.New("other")) {
		t.Fatal("forbidden")
	}
	if !isGone(&TigrisError{Status: 404}) || !isGone(&TigrisError{Status: 400, Message: "NoSuchEntity: the key"}) || isGone(&TigrisError{Status: 500, Message: "boom"}) || isGone(errors.New("x")) {
		t.Fatal("gone")
	}
	if err := (statusResp{Status: "error", Message: "bucket missing"}).err("update key roles"); err == nil || !strings.Contains(err.Error(), "update key roles: bucket missing") {
		t.Fatalf("status answer: %v", err)
	}
	if (statusResp{Status: "success"}).err("x") != nil {
		t.Fatal("success is no error")
	}

	iam := testutil.FakeTigrisIAM(t)
	c := &Tigris{Endpoint: iam.URL + "/", AccessKeyID: "tid_admin", SecretAccessKey: "tsec_admin"}
	ctx := context.Background()
	if err := (&Tigris{Endpoint: iam.URL}).DeleteKey(ctx, "tid_x"); err == nil || !strings.Contains(err.Error(), "no credentials") {
		t.Fatalf("no credentials: %v", err)
	}
	k, err := c.CreateKey(ctx, "metarush-policy-test", nil, ScopeNoDefaultAllow)
	if err != nil {
		t.Fatal(err)
	}
	arn, err := c.CreatePolicy(ctx, "metarush-policy-test", "objects under media/x", map[string]any{"Version": "2012-10-17"})
	if err != nil {
		t.Fatal(err)
	}
	if err := c.AttachPolicy(ctx, k.ID, arn); err != nil {
		t.Fatal(err)
	}
	names, err := c.ListKeyPolicies(ctx, k.ID)
	if err != nil || len(names) != 1 || names[0] != "metarush-policy-test" {
		t.Fatalf("the key's policies: %v %v", names, err)
	}
	all, err := c.ListPolicies(ctx)
	if err != nil || len(all) != 1 || all[0].Arn != arn {
		t.Fatalf("policies: %+v %v", all, err)
	}
	if err := (&TigrisProvider{IAM: c}).RevokeByID(ctx, k.ID); err != nil {
		t.Fatal(err)
	}
	if iam.Key(k.ID) != nil || iam.PolicyCount() != 0 {
		t.Fatalf("revoked: key %v, %d policies", iam.Key(k.ID) != nil, iam.PolicyCount())
	}
	if err := c.SetRoles(ctx, "tid_nothing", nil); err == nil {
		t.Fatal("an action the IAM doesn't have")
	}
	if (&TigrisProvider{}).Name() != "tigris" {
		t.Fatal("name")
	}
}

// The manual equivalents MetaRush prints for Atlas, with the profile when one
// is used; a secret never reaches an error message.
func TestAtlasManualCommands(t *testing.T) {
	a := &Atlas{ProjectID: "proj1", Profile: "work"}
	if got := a.CreateCommand("metarush-studio", "metarush", "Cluster0"); !strings.HasPrefix(got, "atlas dbusers create --username metarush-studio --role readWrite@metarush --scope Cluster0 --projectId proj1 -P work") ||
		!strings.Contains(got, "mongodb+srv://metarush-studio:<password>@<host>/") {
		t.Fatalf("create %q", got)
	}
	if got := a.DeleteCommand("metarush-studio"); got != "atlas dbusers delete metarush-studio --projectId proj1 --force -P work" {
		t.Fatalf("delete %q", got)
	}
	none := &Atlas{}
	if got := none.AccessListCommand("metarush-studio"); got != "atlas accessLists create <the new machine's public IP> --type ipAddress --comment metarush-studio --projectId <project-id>" {
		t.Fatalf("access list %q", got)
	}
	if got := none.CreateCommand("u", "db", ""); strings.Contains(got, "--scope") || strings.Contains(got, " -P ") {
		t.Fatalf("no cluster, no profile: %q", got)
	}
	err := scrub(errors.New("dbusers create failed for s3cr3t-pass: denied"), "s3cr3t-pass")
	if err == nil || strings.Contains(err.Error(), "s3cr3t-pass") || !strings.Contains(err.Error(), "REDACTED") {
		t.Fatalf("scrub %v", err)
	}
	if scrub(nil, "x") != nil || scrub(errors.New("plain"), "").Error() != "plain" || scrub(errors.New("plain"), "x").Error() != "plain" {
		t.Fatal("scrub without a secret in it")
	}
	if got := without([]string{"a", "b", "a", "c"}, "a"); strings.Join(got, ",") != "b,c" {
		t.Fatalf("without %v", got)
	}
	if objectsARN(config.S3{Bucket: "media"}) != "arn:aws:s3:::media/*" || objectsARN(config.S3{Bucket: "media", Prefix: "/team/"}) != "arn:aws:s3:::media/team/*" {
		t.Fatal("ARNs")
	}
}

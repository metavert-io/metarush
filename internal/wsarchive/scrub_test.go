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

// A private key goes whole: a pasted service account's JSON (its newlines
// written as \n) loses the key's body and its END line, not just the BEGIN
// line; a key cut short is taken out to the end of the text.
func TestScrubTakesOutAWholePrivateKey(t *testing.T) {
	s := wsarchive.NewScrubber()
	body := strings.Repeat("MIIEvQIBADANBgkqhkiG9w0BAQEFAASC", 3)
	begin, end := "-----BEGIN "+"PRIVATE KEY-----", "-----END "+"PRIVATE KEY-----" // a made-up key: its markers split for the scanners
	pem := begin + `\n` + body + `\n` + body + `\n` + end + `\n`
	sa := `{"type": "service_account", "private_key": "` + pem + `", "client_email": "render@example.com"}`
	got, changed := scrubbed(t, s, bson.D{{Key: "_id", Value: "cmt_sa"}, {Key: "text", Value: "here it is " + sa}})
	text := got["text"].(string)
	if len(changed) != 1 || strings.Contains(text, body) || strings.Contains(text, "END PRIVATE KEY") || !strings.Contains(text, "client_email") {
		t.Fatalf("a service account's key: %q", text)
	}
	cut := "-----BEGIN RSA " + "PRIVATE KEY-----\n" + body + "\n" + body[:20]
	got, _ = scrubbed(t, s, bson.D{{Key: "_id", Value: "cmt_cut"}, {Key: "text", Value: "partial: " + cut}})
	if text := got["text"].(string); strings.Contains(text, body[:20]) || !strings.HasPrefix(text, "partial: ") {
		t.Fatalf("a key cut short: %q", text)
	}
}

// The credential shapes the docs promise go — a password in any address,
// signatures and keys in a query, Google's client secrets and refresh
// tokens — and a short known value goes only where it stands as a word of
// its own: "metarush.yaml" and "~/metarush/" stay.
func TestScrubCoversWhatTheDocsSay(t *testing.T) {
	s := wsarchive.NewScrubber("metarush", "Zq8studioSecretValue")
	for _, secret := range []string{
		"postgres://maya:" + "pgPass42x" + "@db.example.com/x",
		"https://lena:" + "webPass77y" + "@files.example.com/a",
		"https://api.example.com/v1?api_key=" + "abcd1234efgh5678",
		"https://storage.googleapis.com/b/o?X-Goog-Signature=" + strings.Repeat("ab12", 8) + "&X-Goog-Date=1",
		"https://bucket.s3.amazonaws.com/o?X-Amz-Signature=" + strings.Repeat("cd34", 8),
		"https://maps.example.com/?key=" + "AbCdEf123456",
		"https://x.example.com/?token=" + "tok987654321",
		"https://blob.example.net/c?sig=" + "Sig0987654321",
		"GOCSPX-" + strings.Repeat("Ab1_", 7),
		"1//0" + strings.Repeat("gXyZ-", 9),
	} {
		got, changed := scrubbed(t, s, bson.D{{Key: "_id", Value: "cmt_x"}, {Key: "text", Value: "see " + secret + " ok"}})
		text := got["text"].(string)
		var hidden string
		switch {
		case strings.Contains(secret, "://") && strings.Contains(secret, "@"):
			hidden = secret[strings.LastIndex(secret[:strings.Index(secret, "@")], ":")+1 : strings.Index(secret, "@")]
		case strings.Contains(secret, "="):
			hidden = secret[strings.Index(secret, "=")+1:]
			if i := strings.Index(hidden, "&"); i >= 0 {
				hidden = hidden[:i]
			}
		default:
			hidden = secret
		}
		if len(changed) != 1 || strings.Contains(text, hidden) || !strings.HasSuffix(text, " ok") {
			t.Errorf("%s: %q", secret, text)
		}
	}
	got, _ := scrubbed(t, s, bson.D{{Key: "_id", Value: "cmt_y"}, {Key: "text", Value: "open metarush.yaml in ~/metarush/ and metarush-dev; the key Zq8studioSecretValue"}})
	if text := got["text"].(string); text != "open metarush.yaml in ~/metarush/ and metarush-dev; the key [redacted]" {
		t.Fatalf("words around a short known value: %q", text)
	}
	// Standing on its own, a short value goes, even at a sentence's end.
	got, _ = scrubbed(t, s, bson.D{{Key: "_id", Value: "cmt_z"}, {Key: "text", Value: "the password is metarush. Then (metarush) again"}})
	if text := got["text"].(string); text != "the password is [redacted]. Then ([redacted]) again" {
		t.Fatalf("a short known value on its own: %q", text)
	}
}

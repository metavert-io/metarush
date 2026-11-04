package api

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/fakeeleven"
)

// What someone sends an operation in a secret-named field — a key, a password
// — is hidden in that call's own entries, and joins the secrets hidden
// everywhere (every later entry, the workspace's exports) only once the
// operation takes it: a key it keeps or the service accepts, a password it
// sets or that works. Before, a signed-in member could send a made-up value
// equal to an ordinary word — here a person's email — and blank that word out
// of every later entry of the log, in the log and in every export of it.

// logShows reports whether the activity log, written out, says v.
func logShows(t *testing.T, d *dash, v string) bool {
	t.Helper()
	ctx := context.Background()
	d.a.Log.Flush(ctx)
	var entries []bson.M
	cur, err := d.a.DB.C(store.CLogEntries).Find(ctx, bson.M{})
	if err != nil {
		t.Fatal(err)
	}
	if err := cur.All(ctx, &entries); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(entries)
	return strings.Contains(string(b), v)
}

// exportShows makes a workspace export now (the owner, at this computer) and
// reports, per collection asked about, whether its records say v.
func exportShows(t *testing.T, d *dash, v string, colls ...string) map[string]bool {
	t.Helper()
	d.a.Log.Flush(context.Background())
	dir := filepath.Join(t.TempDir(), "archive")
	if _, err := ops.CallTyped[ops.WorkspaceExportOut](context.Background(), d.a, "workspace.export", ops.WorkspaceExportIn{To: dir}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, c := range colls {
		b, err := os.ReadFile(filepath.Join(dir, "collections", c+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		out[c] = strings.Contains(string(b), v)
	}
	return out
}

func TestMadeUpSecretFieldsLeaveTheLogAlone(t *testing.T) {
	keyring.MockInit() // never the real keychain
	// Keys built at run time that match no credential pattern: only the exact
	// values MetaRush was told about can hide them.
	tested := "xk-" + strings.Repeat("Hq5", 9) + "-tested"
	kept := "xk-" + strings.Repeat("Vb8", 9) + "-kept"
	service := fakeeleven.New(tested)
	t.Cleanup(service.Close)
	t.Setenv(providers.EnvElevenLabsBaseURL, service.URL)
	t.Setenv(providers.EnvVar(providers.ElevenLabsName), "")
	d, _, _ := newRemoteDash(t, "maya@example.com")
	maya := d.tailnet("maya@example.com", "Maya") // a member, from another computer
	const word = "sasha.okafor@example.com"       // a person's email, sent as a key
	ctx := context.Background()

	// Maya sends it in secret-named fields: a key to test (the service refuses
	// it), a key for an owners' operation, a field no operation takes.
	var out providers.TestOut
	maya.op("provider.test", map[string]any{"name": providers.ElevenLabsName, "key": word}, &out)
	if out.Result.OK || out.Tested != "candidate" {
		t.Fatalf("the made-up key: %+v", out)
	}
	if res, env, _ := maya.do("POST", "/api/ops/provider.set", map[string]any{"name": providers.ElevenLabsName, "key": word, "skipTest": true}, true, nil); res.StatusCode == 200 || env.Error.Details["reason"] != "owner_only" {
		t.Fatalf("a member saving a key: %d %+v", res.StatusCode, env.Error)
	}
	if res, _, raw := maya.do("POST", "/api/ops/project.create", map[string]any{"name": "Spare", "apiKey": word}, true, nil); res.StatusCode == 200 {
		t.Fatalf("a field the operation doesn't take: %s", raw)
	}
	if logShows(t, d, word) {
		t.Fatal("the calls that were sent it show it in the log")
	}
	// Later the words are a project's name: the log and an export keep them.
	maya.op("project.create", map[string]any{"name": "Interview with " + word}, nil)
	if !logShows(t, d, word) {
		t.Fatal("a key someone made up blanked a person's email out of the log")
	}
	if got := exportShows(t, d, word, store.CLogEntries, store.CProjects); !got[store.CLogEntries] || !got[store.CProjects] {
		t.Fatalf("a key someone made up blanked a person's email out of an export: %v", got)
	}
	if logbook.Remembered(word) {
		t.Fatal("a key the service refused was taken for a secret")
	}

	// Real keys: one the service takes when Maya tests it, one the owner keeps
	// (checked with the service first). Both are hidden from then on — in the
	// log and in exports — wherever they turn up.
	maya.op("provider.test", map[string]any{"name": providers.ElevenLabsName, "key": tested}, &out)
	if !out.Result.OK {
		t.Fatalf("the service's own key: %+v", out)
	}
	other := fakeeleven.New(kept)
	t.Cleanup(other.Close)
	t.Setenv(providers.EnvElevenLabsBaseURL, other.URL)
	if _, err := ops.CallTyped[providers.Status](ctx, d.a, "provider.set", providers.SetIn{Name: providers.ElevenLabsName, Key: kept}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	maya.op("project.create", map[string]any{"name": "Keys " + tested + " and " + kept}, nil)
	for _, k := range []string{tested, kept} {
		if logShows(t, d, k) {
			t.Fatalf("a real key shows in the log: %s…", k[:6])
		}
		if got := exportShows(t, d, k, store.CLogEntries, store.CProjects); got[store.CLogEntries] || got[store.CProjects] {
			t.Fatalf("a real key shows in an export: %s… %v", k[:6], got)
		}
	}
}

// Passwords: hidden by name in the calls that send them (account.password's
// "current" and "new" too, though their names don't say so), and everywhere
// once set or checked; one that never worked is hidden only in its call.
func TestPasswordsAreHiddenOnceTheyWork(t *testing.T) {
	d, _, _ := newRemoteDash(t)
	d.signIn() // the owner, on this computer
	ctx := context.Background()
	const first, typo, second = "tomato garden at dusk", "tomato garden at dawn", "rosemary under the window"
	d.op("auth.method.set", map[string]any{"method": "password", "email": "owner@example.com", "password": first}, nil)
	if code, env := d.do("POST", "/api/ops/account.password", map[string]any{"current": typo, "new": second}, true); code == 200 || env.Error.Details["reason"] != "bad_current" {
		t.Fatalf("a wrong current password: %d %+v", code, env.Error)
	}
	for _, pw := range []string{first, typo, second} {
		if logShows(t, d, pw) {
			t.Fatalf("a password shows in the log: %q", pw)
		}
	}
	// Neither the mistyped password nor the new one that wasn't set is anyone's
	// secret: a project named with those words keeps them.
	if _, err := ops.CallTyped[ops.ProjectOut](ctx, d.a, "project.create", ops.ProjectCreateIn{Name: "Notes: " + typo + " / " + second}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if !logShows(t, d, typo) || !logShows(t, d, second) {
		t.Fatal("words no one's password blanked out of the log")
	}
	d.op("account.password", map[string]any{"current": first, "new": second}, nil)
	d.a.Log.Flush(ctx)
	var entries []logbook.Entry
	cur, err := d.a.DB.C(store.CLogEntries).Find(ctx, bson.M{"op.name": "account.password"})
	if err != nil {
		t.Fatal(err)
	}
	if err := cur.All(ctx, &entries); err != nil || len(entries) != 2 {
		t.Fatalf("both password changes are in the log: %d %v", len(entries), err)
	}
	for _, e := range entries {
		if in := e.Op.Input; in["current"] != logbook.Redacted || in["new"] != logbook.Redacted {
			t.Fatalf("a password change's input: %v", in)
		}
	}
	// Set and checked: hidden wherever they turn up from now on.
	if _, err := ops.CallTyped[ops.ProjectOut](ctx, d.a, "project.create", ops.ProjectCreateIn{Name: "Reminder: " + first + " then " + second}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
	if logShows(t, d, "Reminder: "+first) || logShows(t, d, "then "+second) {
		t.Fatal("a password that works shows in the log")
	}
}

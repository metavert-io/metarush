package api

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

// local is a browser on this computer, at the dashboard's own address.
func (d *dash) local() *browser {
	return &browser{t: d.t, d: d, host: "127.0.0.1:" + strconv.Itoa(d.a.Cfg.Dashboard.Port), hdr: map[string]string{}, cookies: map[string]string{}}
}

// makeOwner makes the person with a Tailscale login (or an email) an owner.
func (d *dash) makeOwner(who string) {
	d.t.Helper()
	res, err := d.a.DB.C(store.CUsers).UpdateMany(context.Background(), bson.M{"workspaceId": d.a.WorkspaceID(),
		"$or": bson.A{bson.M{"logins": who}, bson.M{"emailKey": auth.EmailKey(who)}}}, bson.M{"$set": bson.M{"role": models.RoleOwner, "state": models.UserActive}})
	if err != nil || res.MatchedCount == 0 {
		d.t.Fatalf("make %s an owner: %v %+v", who, err, res)
	}
}

// Owner-only machine settings (D191) through the whole dashboard stack:
// everyone can see this computer's settings, only owners change them — a
// member signed in with a password is refused on another computer and at the
// host's own address alike, for every owner-only op and endpoint; the owner
// (one-time link at the host, or a password from elsewhere) and the host's own
// command line may; a project's own choices stay open; reading stays open.
func TestOwnerOnlyMachineSettings(t *testing.T) {
	d, projectID, _ := newRemoteDash(t)
	d.signIn() // the owner, on this computer, with a one-time link
	ctx := context.Background()
	d.op("auth.method.set", map[string]any{"method": "password", "email": "owner@example.com", "password": "tomato garden at dusk"}, nil)

	// Maya is invited as a member and accepts on another computer…
	var inv auth.UserLinkOut
	d.op("users.invite", map[string]any{"email": "maya@example.com", "name": "Maya"}, &inv)
	token := inv.Link[strings.LastIndex(inv.Link, "/")+1:]
	away := d.tailnet("", "")
	if res, _, raw := away.do("POST", "/api/auth/link/"+token, map[string]any{"password": "sunflowers by the gate"}, true, nil); res.StatusCode != 200 {
		t.Fatalf("accept: %s", raw)
	}
	// …and signs in at the host's own address with her password too.
	here := d.local()
	if res, _, raw := here.do("POST", "/api/auth/login", map[string]any{"email": "maya@example.com", "password": "sunflowers by the gate"}, true, nil); res.StatusCode != 200 {
		t.Fatalf("sign in at the host: %s", raw)
	}
	members := map[string]*browser{"another computer": away, "the host's own address": here}

	// The sessions say who may change settings, and who the owners are.
	session := func(do func() envelope) Session {
		t.Helper()
		var s Session
		_ = json.Unmarshal(do().Data, &s)
		return s
	}
	for where, b := range members {
		s := session(func() envelope { _, env, _ := b.do("GET", "/api/session", nil, false, nil); return env })
		if s.Owner || len(s.Owners) != 1 || s.Owners[0] != "Tester" || s.User.Name != "Maya" {
			t.Fatalf("member's session on %s: owner %v owners %v user %+v", where, s.Owner, s.Owners, s.User)
		}
		var st auth.AuthStatusOut
		b.op("auth.status", map[string]any{}, &st)
		if st.Owner || len(st.Owners) != 1 || st.Owners[0] != "Tester" {
			t.Fatalf("member's auth.status on %s: %+v", where, st)
		}
	}
	if s := session(func() envelope { _, env := d.do("GET", "/api/session", nil, false); return env }); !s.Owner || len(s.Owners) != 1 {
		t.Fatalf("owner's session: owner %v owners %v", s.Owner, s.Owners)
	}

	refused := func(where string, res int, env envelope, what string) {
		t.Helper()
		owners, _ := env.Error.Details["owners"].([]any)
		if res != 401 || env.Error.Details["reason"] != "owner_only" || env.Error.Message != "Only an owner of this MetaRush can change that." || len(owners) != 1 || owners[0] != "Tester" {
			t.Fatalf("%s from %s: %d %+v", what, where, res, env.Error)
		}
	}
	// Every owner-only op, from anywhere: refused before anything runs.
	var owned []string
	for _, op := range ops.All() {
		if op.OwnerOnly {
			owned = append(owned, op.Name)
		}
	}
	// (The whole list is checked where every op is registered: internal/cli TestOwnerOnlyOps.)
	for _, name := range []string{"assistant.settings.set", "watch.set", "watch.disable", "watch.mkdir", "trash.configure", "trash.empty", "provider.set",
		"provider.remove", "remote.enable", "remote.disable", "remote.allow", "remote.signout", "project.purge",
		"machine.invite", "machine.revoke", "machine.rekey", "storage.migrate", "worker.configure", "ingest.url.configure", "ingest.url.update",
		"auth.method.set", "users.invite", "users.role"} {
		if op, ok := ops.Get(name); !ok || !op.OwnerOnly {
			t.Errorf("%s should be owner-only", name)
		}
	}
	for where, b := range members {
		for _, name := range owned {
			res, env, _ := b.do("POST", "/api/ops/"+name, map[string]any{}, true, nil)
			refused(where, res.StatusCode, env, name)
		}
		// The endpoints outside the registry that change this computer.
		for _, path := range []string{"/api/storage/choose", "/api/storage/plan", "/api/storage/move", "/api/watch/choose", "/api/resources/folders/choose"} {
			res, env, _ := b.do("POST", path, map[string]any{}, true, nil)
			refused(where, res.StatusCode, env, path)
		}
		// Ops whose workspace setting is the owners' while a project's own stays open.
		for _, c := range []struct {
			op            string
			team, project map[string]any
		}{
			{"footage.describe.configure", map[string]any{"on": true}, map[string]any{"projectId": projectID, "on": true}},
			{"generate.configure", map[string]any{"dailyCapUsd": 5}, map[string]any{"projectId": projectID, "budgetUsd": 2}},
		} {
			res, env, _ := b.do("POST", "/api/ops/"+c.op, c.team, true, nil)
			refused(where, res.StatusCode, env, c.op+" (the workspace's)")
			if res, _, raw := b.do("POST", "/api/ops/"+c.op, c.project, true, nil); res.StatusCode != 200 {
				t.Fatalf("%s for a project, from %s: %d %s", c.op, where, res.StatusCode, raw)
			}
		}
		// The narration voice is Content Settings (D193): the default's (no
		// project) and a project's own are open to members.
		for _, in := range []map[string]any{{"name": "elevenlabs", "clear": []string{"voice"}}, {"name": "elevenlabs", "projectId": projectID, "clear": []string{"voice"}}} {
			if res, _, raw := b.do("POST", "/api/ops/provider.settings", in, true, nil); res.StatusCode != 200 {
				t.Fatalf("provider.settings %v from %s: %d %s", in, where, res.StatusCode, raw)
			}
		}
		// A link import may lend this computer's browser sign-in only for an owner.
		res, env, _ := b.do("POST", "/api/ops/ingest.url", map[string]any{"urls": []string{"https://www.youtube.com/watch?v=aaaaaaaaaaa"}, "browserSignIn": "chrome", "dryRun": true}, true, nil)
		refused(where, res.StatusCode, env, "ingest.url with the browser's sign-in")
		// Reading stays open to everyone.
		for _, name := range []string{"trash.settings", "worker.settings", "extension.status", "provider.list", "footage.describe.settings",
			"generate.settings", "storage.status", "watch.status", "assistant.settings", "users.list", "auth.status"} {
			if res, _, raw := b.do("POST", "/api/ops/"+name, map[string]any{}, true, nil); res.StatusCode != 200 {
				t.Fatalf("reading %s from %s: %d %s", name, where, res.StatusCode, raw)
			}
		}
	}

	// The owner may: at the host with a one-time link…
	d.op("trash.configure", map[string]any{"emptyAfterDays": 0}, nil)
	d.op("footage.describe.configure", map[string]any{"on": false}, nil)
	d.op("generate.configure", map[string]any{"dailyCapUsd": 5}, nil)
	d.op("provider.settings", map[string]any{"name": "elevenlabs", "clear": []string{"voice"}}, nil)
	d.op("extension.revoke", map[string]any{"all": true}, nil)
	// …and with their password from another computer.
	boss := d.tailnet("", "")
	if res, _, raw := boss.do("POST", "/api/auth/login", map[string]any{"email": "owner@example.com", "password": "tomato garden at dusk"}, true, nil); res.StatusCode != 200 {
		t.Fatalf("owner sign-in from elsewhere: %s", raw)
	}
	boss.op("trash.configure", map[string]any{"emptyAfterDays": 30}, nil)
	if s := session(func() envelope { _, env, _ := boss.do("GET", "/api/session", nil, false, nil); return env }); !s.Owner {
		t.Fatal("the owner from another computer is an owner")
	}
	// Made an owner, Maya may too (the role is read on every check).
	d.makeOwner("maya@example.com")
	here.op("trash.configure", map[string]any{"emptyAfterDays": 0}, nil)
	if _, err := d.a.DB.C(store.CUsers).UpdateOne(ctx, bson.M{"workspaceId": d.a.WorkspaceID(), "emailKey": "maya@example.com"}, bson.M{"$set": bson.M{"role": models.RoleMember}}); err != nil {
		t.Fatal(err)
	}

	// This computer's own command line counts as the owner.
	if _, err := ops.Call(ctx, d.a, "trash.configure", json.RawMessage(`{"emptyAfterDays":7}`), ops.Caller{Human: true}); err != nil {
		t.Fatalf("the host's CLI: %v", err)
	}
	// A call made for a member's request — a handler, or a card she approves —
	// is held to the same rule, and HTTP without a person behind it is refused.
	member := ops.OverHTTP(ctx, func() bool { return false })
	for _, c := range []struct {
		ctx    context.Context
		caller ops.Caller
	}{{member, ops.Caller{Human: true}}, {ctx, ops.Caller{Human: true, Remote: true}}} {
		_, err := ops.Call(c.ctx, d.a, "trash.configure", json.RawMessage(`{"emptyAfterDays":1}`), c.caller)
		if det, _ := apperr.From(err).Details.(map[string]any); det["reason"] != "owner_only" {
			t.Fatalf("%+v: %v", c.caller, err)
		}
	}
	if err := ops.RequireOwner(member, d.a, "provider.settings"); apperr.From(err).Code != apperr.Unauthorized {
		t.Fatalf("RequireOwner for a member: %v", err)
	}
	if err := ops.RequireOwner(ctx, d.a, "provider.settings"); err != nil {
		t.Fatalf("RequireOwner at the host: %v", err)
	}
}

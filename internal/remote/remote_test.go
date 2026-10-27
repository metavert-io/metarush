package remote

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sessions"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/faketailscale"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
	"github.com/metavert-io/metarush/internal/tools"
)

func TestMain(m *testing.M) {
	faketailscale.MaybeRun() // this binary doubles as the fake `tailscale`
	os.Exit(m.Run())
}

// reasonOf is the reason an error gives in its details ("owner_only").
func reasonOf(err error) string {
	d, _ := apperr.From(err).Details.(map[string]any)
	r, _ := d["reason"].(string)
	return r
}

func TestParseStatus(t *testing.T) {
	st := faketailscale.Default()
	b, _ := json.Marshal(statusOf(st))
	ts := parseStatus(b, Tailscale{Installed: true, CertDomains: []string{}})
	if !ts.Running || ts.DNSName != "studio-mac.tail0000.ts.net" || ts.HostName != "Sam's MacBook" || ts.Login != "sam@example.com" || !ts.HTTPSReady || ts.Problem != "" {
		t.Fatalf("%+v", ts)
	}
	st.Certs = false
	b, _ = json.Marshal(statusOf(st))
	ts = parseStatus(b, Tailscale{Installed: true, CertDomains: []string{}})
	if ts.HTTPSReady || !strings.Contains(ts.Problem, "HTTPS Certificates") || !strings.Contains(ts.Problem, "admin console") {
		t.Fatalf("certificates off must be explained: %+v", ts)
	}
	st.BackendState = "Stopped"
	b, _ = json.Marshal(statusOf(st))
	if ts = parseStatus(b, Tailscale{Installed: true}); ts.Running || !strings.Contains(ts.Problem, "isn't connected") {
		t.Fatalf("%+v", ts)
	}
	// A tagged computer belongs to no one: no account to link.
	tagged := `{"BackendState":"Running","Self":{"HostName":"mini","DNSName":"mini.tail0000.ts.net.","UserID":7,"Tags":["tag:server"]},"User":{"7":{"LoginName":"tagged-devices"}},"CertDomains":["mini.tail0000.ts.net"]}`
	if ts = parseStatus([]byte(tagged), Tailscale{Installed: true}); ts.Login != "" || !ts.HTTPSReady {
		t.Fatalf("tagged: %+v", ts)
	}
	// MagicDNS off: no name.
	if ts = parseStatus([]byte(`{"BackendState":"Running","Self":{"HostName":"x","DNSName":""}}`), Tailscale{Installed: true}); !strings.Contains(ts.Problem, "MagicDNS") {
		t.Fatalf("%+v", ts)
	}
}

// statusOf runs the fake's status answer in-process.
func statusOf(st faketailscale.State) json.RawMessage {
	dir, _ := os.MkdirTemp("", "remote-fake")
	defer os.RemoveAll(dir)
	p := filepath.Join(dir, "state.json")
	_ = faketailscale.Save(p, st)
	os.Setenv("METARUSH_FAKE_TAILSCALE_STATE", p)
	defer os.Unsetenv("METARUSH_FAKE_TAILSCALE_STATE")
	var out strings.Builder
	faketailscale.Main([]string{"status", "--json"}, &out, &out)
	return json.RawMessage(out.String())
}

func TestParseServe(t *testing.T) {
	sv := parseServe([]byte(`{}`), 4845, Serve{Elsewhere: []string{}})
	if !sv.Known || sv.Active {
		t.Fatalf("%+v", sv)
	}
	cfg := `{"TCP":{"443":{"HTTPS":true}},"Web":{"studio-mac.tail0000.ts.net:443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:4845"}}},
		"studio-mac.tail0000.ts.net:8443":{"Handlers":{"/":{"Proxy":"http://127.0.0.1:3000"},"/docs":{"Proxy":"http://127.0.0.1:4845"}}}},
		"AllowFunnel":{"studio-mac.tail0000.ts.net:443":true}}`
	sv = parseServe([]byte(cfg), 4845, Serve{Elsewhere: []string{}})
	if !sv.Active || sv.Address != "https://studio-mac.tail0000.ts.net" || !sv.Funnel || len(sv.Elsewhere) != 2 {
		t.Fatalf("%+v", sv)
	}
	fg := `{"Foreground":{"abc":{"Web":{"studio-mac.tail0000.ts.net:443":{"Handlers":{"/":{"Proxy":"localhost:4845"}}}}}}}`
	if sv = parseServe([]byte(fg), 4845, Serve{Elsewhere: []string{}}); !sv.Active {
		t.Fatalf("serve in the foreground counts: %+v", sv)
	}
	if sv = parseServe([]byte(`{"Web":{"x.tail0000.ts.net:443":{"Handlers":{"/":{"Proxy":"http://10.0.0.5:4845"}}}}}`), 4845, Serve{}); sv.Active {
		t.Fatal("another computer's port is not this dashboard")
	}
	for target, want := range map[string]bool{"4845": true, "http://127.0.0.1:4845": true, "http://localhost:4845/": true, "https+insecure://127.0.0.1:4845": false,
		"http://[::1]:4845": true, "http://127.0.0.1:4846": false, "": false, "unix:/tmp/x.sock": false} {
		if proxiesTo(target, 4845) != want {
			t.Errorf("proxiesTo(%q) != %v", target, want)
		}
	}
}

func TestCommands(t *testing.T) {
	rm, _ := config.RemoteFrom(config.Dashboard{PublicURL: "https://studio-mac.tail0000.ts.net"})
	if ServeCommand(rm, 4845) != "tailscale serve --bg 4845" || StopCommand(rm) != "tailscale serve --https=443 off" {
		t.Fatal(ServeCommand(rm, 4845), StopCommand(rm))
	}
	rm, _ = config.RemoteFrom(config.Dashboard{PublicURL: "https://studio-mac.tail0000.ts.net:8443"})
	if ServeCommand(rm, 4845) != "tailscale serve --bg --https=8443 4845" || StopCommand(rm) != "tailscale serve --https=8443 off" {
		t.Fatal(ServeCommand(rm, 4845), StopCommand(rm))
	}
	if strings.Join(serveArgs(rm, 4845), " ") != "serve --bg --https=8443 4845" || strings.Join(stopArgs(rm), " ") != "serve --https=8443 off" {
		t.Fatal("args")
	}
}

func TestDecodeHeader(t *testing.T) {
	for in, want := range map[string]string{
		"Maya Lindqvist":                        "Maya Lindqvist",
		"=?utf-8?q?Zo=C3=AB_Ib=C3=A1=C3=B1ez?=": "Zoë Ibáñez",
		"  Sam\x07 ":                            "Sam",
		"=?utf-8?q?broken":                      "=?utf-8?q?broken",
	} {
		if got := DecodeHeader(in); got != want {
			t.Errorf("DecodeHeader(%q) = %q, want %q", in, got, want)
		}
	}
	if DisplayName("maya@example.com", "") != "maya" || DisplayName("x@y", " Maya ") != "Maya" {
		t.Fatal("DisplayName")
	}
}

// testApp opens a throwaway workspace whose `tailscale` is the fake.
func testApp(t *testing.T, st faketailscale.State) (*app.App, string) {
	t.Helper()
	a := testapp.New(t)
	tool, stateFile := faketailscale.Write(t, st)
	a.Cfg.Tools.Tailscale = tool
	a.Tools = tools.New(a.Cfg.Tools)
	ctx := context.Background()
	if err := a.DB.Insert(ctx, store.CUsers, models.User{ID: a.Cfg.User.ID, WorkspaceID: a.WorkspaceID(), Name: a.Cfg.User.Name, Email: "sam@example.com", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	return a, stateFile
}

func call[O any](t *testing.T, a *app.App, name string, in any, c ops.Caller) (*O, error) {
	t.Helper()
	return ops.CallTyped[O](context.Background(), a, name, in, c)
}

func TestEnableStatusAllowDisable(t *testing.T) {
	a, stateFile := testApp(t, faketailscale.Default())
	human := ops.Caller{Human: true}

	// Status reads Tailscale and changes nothing.
	st, err := call[RemoteStatusOut](t, a, "remote.status", RemoteStatusIn{}, ops.Caller{})
	if err != nil || st.On || !st.Tailscale.HTTPSReady || st.ServeCommand != "tailscale serve --bg 4859" || st.Ready {
		t.Fatalf("%+v %v", st, err)
	}
	// Changes are people-only.
	if _, err := call[RemoteStatusOut](t, a, "remote.enable", RemoteEnableIn{}, ops.Caller{}); apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("enable without a person: %v", err)
	}
	if _, err := call[RemoteStatusOut](t, a, "remote.enable", RemoteEnableIn{Allow: []string{"not an account"}}, human); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("bad account: %v", err)
	}
	out, err := call[RemoteStatusOut](t, a, "remote.enable", RemoteEnableIn{Allow: []string{"Maya@Example.com"}}, human)
	if err != nil {
		t.Fatal(err)
	}
	if !out.On || out.URL != "https://studio-mac.tail0000.ts.net" || out.Machine != "Sam's MacBook" || !out.SignInWithTailscale || out.Ready || !out.Linked {
		t.Fatalf("%+v", out)
	}
	if len(out.People) != 2 || out.People[0].Login != "maya@example.com" || !out.People[1].ThisComputer || out.People[1].Name != a.Cfg.User.Name {
		t.Fatalf("this computer's own account is added and is the person who runs MetaRush here: %+v", out.People)
	}
	if !strings.Contains(strings.Join(out.Steps, " "), "tailscale serve") {
		t.Fatalf("the next step is tailscale serve: %v", out.Steps)
	}
	// Written to config.yaml key by key and in effect at once.
	loaded, err := config.Load(a.Cfg.Home())
	if err != nil || loaded.Remote().PublicURL != out.URL || len(loaded.Remote().Logins) != 2 || loaded.Remote().MachineName != "Sam's MacBook" {
		t.Fatalf("%v %+v", err, loaded.Remote())
	}
	if !a.Cfg.Remote().IdentitySignIn() {
		t.Fatal("live settings")
	}
	var me models.User
	_ = a.DB.GetByID(context.Background(), store.CUsers, a.WorkspaceID(), a.Cfg.User.ID, &me)
	if len(me.Logins) != 1 || me.Logins[0] != "sam@example.com" {
		t.Fatalf("linked: %+v", me)
	}

	// The fake's serve, as a person running the printed command would.
	if _, err := StartServe(context.Background(), a.Tools, a.Cfg.Remote(), a.Cfg.Dashboard.Port); err != nil {
		t.Fatal(err)
	}
	if st, _ = call[RemoteStatusOut](t, a, "remote.status", RemoteStatusIn{}, ops.Caller{}); !st.Ready || !st.Serve.Active {
		t.Fatalf("ready: %+v", st)
	}

	// Adding people only from this computer, even for an owner; removing works from anywhere and signs them out.
	ownerElsewhere := ops.Caller{Human: true, Remote: true, Elsewhere: true, Owner: true}
	if _, err := call[RemoteStatusOut](t, a, "remote.allow", RemoteAllowIn{Add: []string{"bob@example.com"}}, ownerElsewhere); apperr.From(err).Code != apperr.Unauthorized || reasonOf(err) == "owner_only" {
		t.Fatalf("adding from elsewhere: %v", err)
	}
	// Nor through remote.enable's allow list (it widened the list from elsewhere before).
	if _, err := call[RemoteStatusOut](t, a, "remote.enable", RemoteEnableIn{Allow: []string{"bob@example.com"}}, ownerElsewhere); apperr.From(err).Code != apperr.Unauthorized || reasonOf(err) == "owner_only" {
		t.Fatalf("enabling with more people from elsewhere: %v", err)
	}
	// Someone over HTTP who isn't known to be an owner changes nothing (D191).
	if _, err := call[RemoteStatusOut](t, a, "remote.allow", RemoteAllowIn{Remove: []string{"maya@example.com"}}, ops.Caller{Human: true, Remote: true}); reasonOf(err) != "owner_only" {
		t.Fatalf("a member removing: %v", err)
	}
	sessStore := sessions.For(a.Cfg.StateDir())
	tok, _ := sessStore.New(sessions.Session{UserID: "usr_j", Login: "maya@example.com", Public: true}, time.Now())
	local, _ := sessStore.New(sessions.Session{UserID: a.Cfg.User.ID}, time.Now())
	out, err = call[RemoteStatusOut](t, a, "remote.allow", RemoteAllowIn{Remove: []string{"maya@example.com"}}, ownerElsewhere)
	if err != nil || out.SignedOut != 1 || len(out.People) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	if _, ok := sessStore.Get(tok, time.Now()); ok {
		t.Fatal("a removed person is signed out at once")
	}
	if out, err = call[RemoteStatusOut](t, a, "remote.allow", RemoteAllowIn{Add: []string{"maya@example.com"}}, human); err != nil || len(out.People) != 2 {
		t.Fatalf("%+v %v", out, err)
	}

	// Signing out: one person's browsers, or all of them (never this computer's own).
	tok, _ = sessStore.New(sessions.Session{UserID: "usr_j", Login: "maya@example.com", Public: true}, time.Now())
	so, err := call[RemoteSignoutOut](t, a, "remote.signout", RemoteSignoutIn{All: true}, human)
	if err != nil || so.SignedOut != 1 {
		t.Fatalf("%+v %v", so, err)
	}
	if _, ok := sessStore.Get(local, time.Now()); !ok {
		t.Fatal("this computer's own browser stays signed in")
	}

	// Disable clears the address and the list, and says how to stop serve.
	tok, _ = sessStore.New(sessions.Session{UserID: a.Cfg.User.ID, Public: true}, time.Now())
	out, err = call[RemoteStatusOut](t, a, "remote.disable", RemoteDisableIn{}, human)
	if err != nil || out.On || len(out.People) != 0 || out.SignedOut != 1 || out.StopCommand != "tailscale serve --https=443 off" || !out.Serve.Active {
		t.Fatalf("%+v %v", out, err)
	}
	if !strings.Contains(out.Summary, "turns away") {
		t.Fatalf("summary: %q", out.Summary)
	}
	if _, err := StopServe(context.Background(), a.Tools, config.Remote{}); err != nil {
		t.Fatal(err)
	}
	if fake := faketailscale.Load(stateFile); len(fake.Serve) != 0 {
		t.Fatalf("serve off: %+v", fake.Serve)
	}
	var audits []models.AuditEvent
	_ = a.DB.Find(context.Background(), store.CAudit, a.WorkspaceID(), bson.M{"action": bson.M{"$regex": "^remote\\."}}, &audits)
	seen := map[string]bool{}
	for _, e := range audits {
		seen[e.Action] = true
	}
	for _, want := range []string{"remote.enable", "remote.allow", "remote.signout", "remote.disable"} {
		if !seen[want] {
			t.Errorf("not audited: %s (%v)", want, seen)
		}
	}
}

func TestEnableExplainsTailscaleProblems(t *testing.T) {
	st := faketailscale.Default()
	st.Certs = false
	a, _ := testApp(t, st)
	out, err := call[RemoteStatusOut](t, a, "remote.enable", RemoteEnableIn{}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Ready || out.Tailscale.HTTPSReady || !strings.Contains(strings.Join(out.Steps, " "), "HTTPS Certificates") || out.SignInWithTailscale {
		t.Fatalf("certificates off are said plainly: %+v", out)
	}
	// Serve refuses without certificates (the real one waits for a browser).
	if _, err := StartServe(context.Background(), a.Tools, a.Cfg.Remote(), a.Cfg.Dashboard.Port); err == nil {
		t.Fatal("serve without certificates")
	}

	st = faketailscale.Default()
	st.BackendState = "Stopped"
	b, _ := testApp(t, st)
	if _, err := call[RemoteStatusOut](t, b, "remote.enable", RemoteEnableIn{}, ops.Caller{Human: true}); apperr.From(err).Code != apperr.NotConfigured {
		t.Fatalf("not connected: %v", err)
	}
	// No tailscale at all.
	c := testapp.New(t)
	c.Cfg.Tools.Tailscale = filepath.Join(t.TempDir(), "missing")
	c.Tools = tools.New(c.Cfg.Tools)
	if _, err := call[RemoteStatusOut](t, c, "remote.enable", RemoteEnableIn{}, ops.Caller{Human: true}); apperr.From(err).Code != apperr.ToolMissing {
		t.Fatalf("not installed: %v", err)
	}
}

// Something else already served at the address: said plainly, never replaced.
func TestStatusSaysWhenTheAddressIsTaken(t *testing.T) {
	st := faketailscale.Default()
	st.Serve = map[string]any{"TCP": map[string]any{"443": map[string]any{"HTTPS": true}},
		"Web": map[string]any{"studio-mac.tail0000.ts.net:443": map[string]any{"Handlers": map[string]any{"/": map[string]any{"Proxy": "http://127.0.0.1:3000"}}}}}
	a, _ := testApp(t, st)
	out, err := call[RemoteStatusOut](t, a, "remote.enable", RemoteEnableIn{}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	if out.Ready || out.Serve.Active || !strings.Contains(strings.Join(out.Steps, " "), "already serves something else") {
		t.Fatalf("%+v", out)
	}
}

func TestPersonFor(t *testing.T) {
	a, _ := testApp(t, faketailscale.Default())
	ctx := context.Background()
	// The same email: the person who set MetaRush up.
	u, created, err := PersonFor(ctx, a, "Sam@Example.com", "Sam")
	if err != nil || created || u.ID != a.Cfg.User.ID {
		t.Fatalf("%+v %v %v", u, created, err)
	}
	// Someone new: made once, named as Tailscale names them.
	j, created, err := PersonFor(ctx, a, "maya@example.com", "Maya Lindqvist")
	if err != nil || !created || j.Name != "Maya Lindqvist" || j.Email != "maya@example.com" || j.Source != "tailscale" || !ids.Valid(ids.User, j.ID) {
		t.Fatalf("%+v %v %v", j, created, err)
	}
	again, created, err := PersonFor(ctx, a, "maya@example.com", "Someone Else")
	if err != nil || created || again.ID != j.ID || again.Name != "Maya Lindqvist" {
		t.Fatalf("%+v %v %v", again, created, err)
	}
	gh, _, _ := PersonFor(ctx, a, "octocat@github", "")
	if gh.Name != "octocat" || gh.Email != "" {
		t.Fatalf("%+v", gh)
	}
	n, _ := a.DB.C(store.CUsers).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID()})
	if n != 3 {
		t.Fatalf("people: %d", n)
	}
	// This computer's account never takes another person's.
	if linked, err := LinkThisComputer(ctx, a, "maya@example.com"); err != nil || linked {
		t.Fatalf("%v %v", linked, err)
	}
}

// Remote access needs no Tailscale (D183): an HTTPS address from any proxy on
// this computer turns it on; without one, the advice says both ways.
func TestEnableWithoutTailscale(t *testing.T) {
	a, _ := testApp(t, faketailscale.Default())
	a.Cfg.Tools.Tailscale = filepath.Join(t.TempDir(), "no-tailscale-here")
	a.Tools = tools.New(a.Cfg.Tools)
	human := ops.Caller{Human: true}
	if _, err := call[RemoteStatusOut](t, a, "remote.enable", RemoteEnableIn{}, human); apperr.From(err).Code != apperr.ToolMissing || !strings.Contains(apperr.From(err).Command(), "--url") {
		t.Fatalf("no Tailscale, no address: %v", err)
	}
	out, err := call[RemoteStatusOut](t, a, "remote.enable", RemoteEnableIn{URL: "https://studio.example.com"}, human)
	if err != nil || a.Cfg.Remote().PublicURL != "https://studio.example.com" {
		t.Fatalf("own proxy: %+v %v", out, err)
	}
}

package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/zalando/go-keyring"
	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/tools"
)

// Moving the database from the dashboard (D338) through the whole server.

// newDatabaseName is a test database's name nobody made yet.
func newDatabaseName() string {
	return store.TestDBPrefix + strings.ToLower(strings.TrimPrefix(ids.New("t"), "t_"))
}

// emptyTestDB is a new, empty test database (dropped after the test).
func emptyTestDB(t *testing.T, name string) *store.DB {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	db, err := store.Connect(ctx, testutil.URI(), name)
	if err != nil {
		t.Skipf("test MongoDB unavailable: %v", err)
	}
	if err := db.MarkTestRun(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		_ = db.DropTestDatabase(c, testutil.URI())
		_ = db.Close(c)
	})
	return db
}

// secretAddressFor is the test database's address with a made-up token in
// it, so a test can tell it never comes back or reaches the log.
func secretAddressFor() (string, string) {
	token := "mrdash" + strings.ToLower(strings.TrimPrefix(ids.New("t"), "t_"))
	return testutil.URI() + "&appName=" + token, token
}

// leftNoTrace fails when the token is in the activity log or the change
// history of the workspace.
func leftNoTrace(t *testing.T, d *dash, token string) {
	t.Helper()
	ctx := context.Background()
	d.a.Log.Flush(ctx)
	for _, coll := range []string{store.CLogEntries, store.CAudit} {
		cur, err := d.a.DB.C(coll).Find(ctx, bson.M{})
		if err != nil {
			t.Fatal(err)
		}
		var docs []bson.M
		_ = cur.All(ctx, &docs)
		raw, _ := json.Marshal(docs)
		if strings.Contains(string(raw), token) {
			t.Fatalf("the address is in %s", coll)
		}
	}
}

// moveRecord rewrites a dashboard move's record on this computer (when it
// was planned or granted), the way time passing would.
func moveRecord(t *testing.T, d *dash, id string, change func(r map[string]any)) {
	t.Helper()
	logPath, err := ops.DatabaseMoveLog(d.a, id)
	if err != nil {
		t.Fatal(err)
	}
	p := strings.TrimSuffix(logPath, ".log") + ".json"
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	r := map[string]any{}
	if err := json.Unmarshal(b, &r); err != nil {
		t.Fatal(err)
	}
	change(r)
	b, _ = json.Marshal(r)
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The owner plans a move on this computer: a signed-in session with the CSRF
// header only; the typed address goes to the keychain and never comes back
// or into the log; the plan says what moving does. Starting it runs
// `metarush database move --dashboard-move <id>` in a process of its own
// with a one-time grant on its standard input (never in its arguments): the
// grant works once, and not after two minutes; one move at a time; a plan
// more than an hour old must be made again.
func TestDatabaseMoveEndpoints(t *testing.T) {
	keyring.MockInit()
	t.Setenv("CLAUDECODE", "")
	d, _, _ := newDash(t, noClaude)
	ctx := context.Background()
	address, token := secretAddressFor()
	form := DatabaseMovePlanIn{Form: ops.DatabaseMoveForm{To: "elsewhere", Address: address, Name: newDatabaseName()}}
	if code, env := d.do("POST", "/api/database/plan", form, true); code != http.StatusUnauthorized || env.Error.Details["reason"] != "no_session" {
		t.Fatalf("without a session: %d %+v", code, env.Error)
	}
	d.signIn()
	if code, env := d.do("POST", "/api/database/plan", form, false); code != http.StatusUnauthorized || env.Error.Details["reason"] != "csrf" {
		t.Fatalf("without the CSRF header: %d %+v", code, env.Error)
	}
	// Something that isn't a MongoDB address is refused without repeating it.
	bad, _ := json.Marshal(DatabaseMovePlanIn{Form: ops.DatabaseMoveForm{To: "elsewhere", Address: "https://" + token + ".example.com/", Name: "studio"}})
	if code, body := d.raw("POST", "/api/database/plan", bad); code != http.StatusBadRequest || strings.Contains(body, token) || !strings.Contains(body, "isn't a MongoDB address") {
		t.Fatalf("not an address: %d %s", code, body)
	}
	// Somewhere the database can't go, and fields the form doesn't have.
	if code, env := d.do("POST", "/api/database/plan", DatabaseMovePlanIn{Form: ops.DatabaseMoveForm{To: "the moon"}}, true); code != http.StatusBadRequest || env.Error.Code != apperr.InvalidInput {
		t.Fatalf("nowhere: %d %+v", code, env.Error)
	}
	if code, _ := d.do("POST", "/api/database/plan", map[string]any{"form": form.Form, "uri": address}, true); code != http.StatusBadRequest {
		t.Fatalf("an address outside the form: %d", code)
	}

	plan := func() DatabaseMovePlanOut {
		t.Helper()
		b, _ := json.Marshal(form)
		code, body := d.raw("POST", "/api/database/plan", b)
		if strings.Contains(body, token) || strings.Contains(body, "appName") {
			t.Fatalf("the plan sent the address back: %s", body)
		}
		var env envelope
		_ = json.Unmarshal([]byte(body), &env)
		var out DatabaseMovePlanOut
		_ = json.Unmarshal(env.Data, &out)
		if code != 200 || !regexp.MustCompile(`^[0-9a-f]{24}$`).MatchString(out.MoveID) {
			t.Fatalf("plan: %d %s", code, body)
		}
		return out
	}
	first := plan()
	if !first.Plan.Ready || first.Plan.To.Kind != "server" || first.Plan.To.Words != "a MongoDB server on this computer" || first.Plan.Summary == "" ||
		first.Plan.Documents == 0 || !first.Plan.Target.Empty || !first.Plan.Target.Writable {
		t.Fatalf("what moving would do: %+v", first.Plan)
	}
	if kept, _ := ops.LoadMoveAddress(d.a.Cfg); kept != address {
		t.Fatal("the plan didn't keep the address for the move")
	}

	// Starting a move: one this computer never planned is refused.
	if code, env := d.do("POST", "/api/database/move", DatabaseMoveStartIn{MoveID: strings.Repeat("0", 24)}, true); code != http.StatusNotFound {
		t.Fatalf("unknown move: %d %+v", code, env.Error)
	}
	if code, env := d.do("POST", "/api/database/move", DatabaseMoveStartIn{MoveID: "../../etc/passwd"}, true); code != http.StatusBadRequest {
		t.Fatalf("a path for a move: %d %+v", code, env.Error)
	}
	// The process it starts: MetaRush itself (a stand-in here), the move in
	// its arguments and the grant on its standard input.
	seen := t.TempDir()
	t.Setenv("MR_TEST_MOVE_SEEN", seen)
	fake := filepath.Join(t.TempDir(), "metarush")
	_ = os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$MR_TEST_MOVE_SEEN/args.tmp\"\ncat > \"$MR_TEST_MOVE_SEEN/stdin\"\nmv \"$MR_TEST_MOVE_SEEN/args.tmp\" \"$MR_TEST_MOVE_SEEN/args\"\necho moving\n"), 0o755)
	if !d.a.Tools.Use(tools.Self, fake) {
		t.Fatal("stand-in")
	}
	start := func(id string) string {
		t.Helper()
		_ = os.Remove(filepath.Join(seen, "args"))
		code, env := d.do("POST", "/api/database/move", DatabaseMoveStartIn{MoveID: id}, true)
		var started DatabaseMoveStartOut
		_ = json.Unmarshal(env.Data, &started)
		if code != 200 || !started.Started || !strings.HasPrefix(started.Summary, "Moving the database to a MongoDB server on this computer.") {
			t.Fatalf("start: %d %+v %+v", code, started, env.Error)
		}
		var args []byte
		for i := 0; i < 100 && len(args) == 0; i++ {
			time.Sleep(50 * time.Millisecond)
			args, _ = os.ReadFile(filepath.Join(seen, "args"))
		}
		want := strings.Join([]string{"--home", d.a.Cfg.Home(), "database", "move", "--dashboard-move", id}, "\n") + "\n"
		if string(args) != want {
			t.Fatalf("arguments:\n%s\nwant:\n%s", args, want)
		}
		stdin, _ := os.ReadFile(filepath.Join(seen, "stdin"))
		grant := strings.TrimSpace(string(stdin))
		if len(grant) != 64 || strings.Contains(string(args), grant) || strings.Contains(string(args), token) {
			t.Fatalf("the grant: %q", stdin)
		}
		return grant
	}
	grant := start(first.MoveID)
	logPath, _ := ops.DatabaseMoveLog(d.a, first.MoveID)
	var logText []byte
	for i := 0; i < 100 && !strings.Contains(string(logText), "moving"); i++ {
		time.Sleep(50 * time.Millisecond)
		logText, _ = os.ReadFile(logPath)
	}
	if st, err := os.Stat(logPath); err != nil || st.Mode().Perm() != 0o600 || !strings.Contains(string(logText), "moving") {
		t.Fatalf("the move's log: %v %v %q", st, err, logText)
	}
	// The same move can't start twice, and while it starts no other does.
	if code, env := d.do("POST", "/api/database/move", DatabaseMoveStartIn{MoveID: first.MoveID}, true); code != http.StatusConflict {
		t.Fatalf("the same move again: %d %+v", code, env.Error)
	}
	second := plan()
	if code, env := d.do("POST", "/api/database/move", DatabaseMoveStartIn{MoveID: second.MoveID}, true); code != http.StatusConflict ||
		!strings.Contains(env.Error.Message, "already being moved from this computer") {
		t.Fatalf("a second move while one starts: %d %+v", code, env.Error)
	}
	// The grant on its stdin is taken once, by the process it started.
	if _, _, err := ops.ClaimDashboardDatabaseMove(ctx, d.a, first.MoveID, grant); err != nil {
		t.Fatalf("the grant: %v", err)
	}
	if _, _, err := ops.ClaimDashboardDatabaseMove(ctx, d.a, first.MoveID, grant); err == nil {
		t.Fatal("the grant was taken twice")
	}
	// A wrong grant uses up the move's only try.
	other := start(second.MoveID)
	if _, _, err := ops.ClaimDashboardDatabaseMove(ctx, d.a, second.MoveID, "0"+other[1:]); err == nil {
		t.Fatal("a wrong grant was taken")
	}
	if _, _, err := ops.ClaimDashboardDatabaseMove(ctx, d.a, second.MoveID, other); err == nil {
		t.Fatal("a grant was taken after a wrong one used the move's only try")
	}
	moveRecord(t, d, second.MoveID, func(r map[string]any) { r["granted"] = time.Now().UTC().Add(-3 * time.Minute) })
	// A grant more than two minutes old isn't taken.
	third := plan()
	late := start(third.MoveID)
	moveRecord(t, d, third.MoveID, func(r map[string]any) { r["granted"] = time.Now().UTC().Add(-3 * time.Minute) })
	if _, _, err := ops.ClaimDashboardDatabaseMove(ctx, d.a, third.MoveID, late); apperr.From(err) == nil || apperr.From(err).Code != apperr.HumanOnly {
		t.Fatalf("a grant past its two minutes: %v", err)
	}
	// A plan an hour old is made again before anything starts.
	fourth := plan()
	moveRecord(t, d, fourth.MoveID, func(r map[string]any) { r["planned"] = time.Now().UTC().Add(-2 * time.Hour) })
	if code, env := d.do("POST", "/api/database/move", DatabaseMoveStartIn{MoveID: fourth.MoveID}, true); code != http.StatusConflict ||
		!strings.Contains(env.Error.Message, "more than an hour old") {
		t.Fatalf("an old plan: %d %+v", code, env.Error)
	}
	// A body that isn't the form's is refused.
	if code, _ := d.raw("POST", "/api/database/move", []byte(`{"moveId": 5}`)); code != http.StatusBadRequest {
		t.Fatalf("a move id that isn't text: %d", code)
	}
	// MetaRush itself can't be started: the move fails plainly, and says so.
	fifth := plan()
	_ = os.Remove(fake)
	if code, env := d.do("POST", "/api/database/move", DatabaseMoveStartIn{MoveID: fifth.MoveID}, true); code != http.StatusInternalServerError ||
		env.Error.Message != "MetaRush couldn't start moving the database." {
		t.Fatalf("a move that can't start: %d %+v", code, env.Error)
	}
	var st ops.DatabaseMoveStatusOut
	d.op("database.move.status", map[string]any{}, &st)
	if st.Run == nil || st.Run.State != ops.DBMoveFailed {
		t.Fatalf("the status after a move that couldn't start: %+v", st.Run)
	}
	leftNoTrace(t, d, token)
}

// Switching this computer after another one moved the workspace, and
// abandoning a move that stopped: the address typed for the switch goes to
// the keychain and never comes back; nothing switches unless the workspace
// moved, to that very copy; then this computer's settings name the new
// database.
func TestDatabaseSwitchAndAbandonEndpoints(t *testing.T) {
	keyring.MockInit()
	t.Setenv("CLAUDECODE", "")
	d, _, _ := newDash(t, noClaude)
	ctx := context.Background()
	d.signIn()
	address, token := secretAddressFor()
	name := newDatabaseName()
	sw := DatabaseSwitchFormIn{Address: address, Name: name}
	if code, env := d.do("POST", "/api/database/switch", sw, true); code != http.StatusConflict || !strings.Contains(env.Error.Message, "nothing to switch to") {
		t.Fatalf("a switch with no move: %d %+v", code, env.Error)
	}
	if code, _ := d.raw("POST", "/api/database/switch", []byte(`{"address": ["`+token+`"]}`)); code != http.StatusBadRequest {
		t.Fatalf("an address that isn't text: %d", code)
	}
	bad, _ := json.Marshal(DatabaseSwitchFormIn{Address: "postgres://" + token + "@db.example.com/", Name: name})
	if code, body := d.raw("POST", "/api/database/switch", bad); code != http.StatusBadRequest || strings.Contains(body, token) {
		t.Fatalf("not an address: %d %s", code, body)
	}

	// A move that stopped is abandoned from the dashboard: changes go on.
	marker := func(move string) store.MoveMarker {
		return store.MoveMarker{Move: move, Workspace: d.a.WorkspaceID(), To: store.MoveTarget{Kind: "server", Words: "a MongoDB server on this computer", Name: name, Reach: "every-computer"},
			By: "Maya Okafor", From: d.a.Cfg.Installation.ID, FromName: "Studio Mac"}
	}
	if err := d.a.DB.StartMoveMarker(ctx, marker("mvdash1")); err != nil {
		t.Fatal(err)
	}
	if code, env := d.do("POST", "/api/database/switch", sw, true); code != http.StatusConflict || !strings.Contains(env.Error.Message, "still being moved") {
		t.Fatalf("a switch while it moves: %d %+v", code, env.Error)
	}
	var ab ops.DatabaseAbandonOut
	d.op("database.abandon", map[string]any{}, &ab)
	if !ab.Abandoned || !strings.Contains(ab.Summary, "changes go on") {
		t.Fatalf("abandon: %+v", ab)
	}

	// The workspace moved: the switch reaches this move's finished copy.
	if err := d.a.DB.StartMoveMarker(ctx, marker("mvdash2")); err != nil {
		t.Fatal(err)
	}
	if ok, err := d.a.DB.SetMoveState(ctx, "mvdash2", store.MoveMoving, store.MoveMoved, ""); err != nil || !ok {
		t.Fatalf("moved: %v %v", ok, err)
	}
	dst := emptyTestDB(t, name)
	if err := dst.SetMoveLock(ctx, "mvdash-other", d.a.WorkspaceID(), "API test", store.RestoreDone); err != nil {
		t.Fatal(err)
	}
	if code, env := d.do("POST", "/api/database/switch", sw, true); code != http.StatusConflict || env.Error.Details["reason"] != "not_the_copy" {
		t.Fatalf("a switch to another move's copy: %d %+v", code, env.Error)
	}
	if err := dst.SetMoveLock(ctx, "mvdash2", d.a.WorkspaceID(), "API test", store.RestoreDone); err != nil {
		t.Fatal(err)
	}
	if err := config.Save(d.a.Cfg.Home(), d.a.Cfg); err != nil { // this computer's settings, as setup leaves them
		t.Fatal(err)
	}
	b, _ := json.Marshal(sw)
	code, body := d.raw("POST", "/api/database/switch", b)
	if code != 200 || strings.Contains(body, token) || strings.Contains(body, "appName") {
		t.Fatalf("switch: %d %s", code, body)
	}
	var env envelope
	_ = json.Unmarshal([]byte(body), &env)
	var out ops.DatabaseSwitchOut
	_ = json.Unmarshal(env.Data, &out)
	if !out.Switched || out.To.Name != name || !strings.Contains(out.Summary, "with its own sign-in") {
		t.Fatalf("switched: %+v", out)
	}
	settings, _ := os.ReadFile(filepath.Join(d.a.Cfg.Home(), "config.yaml"))
	if !strings.Contains(string(settings), name) || strings.Contains(string(settings), token) {
		t.Fatalf("this computer's settings:\n%s", settings)
	}
	leftNoTrace(t, d, token)
}

// The endpoints that move or switch the database are an owner's, on this
// computer: a member is refused before anything runs, wherever they sign in;
// the owner signed in from another computer is told it starts on this one;
// abandoning is an owner's too.
func TestDatabaseEndpointsAreAnOwnersOnThisComputer(t *testing.T) {
	keyring.MockInit()
	t.Setenv("CLAUDECODE", "")
	d, _, _ := newRemoteDash(t)
	d.signIn()
	d.op("auth.method.set", map[string]any{"method": "password", "email": "owner@example.com", "password": "tomato garden at dusk"}, nil)
	var inv auth.UserLinkOut
	d.op("users.invite", map[string]any{"email": "maya@example.com", "name": "Maya"}, &inv)
	link := inv.Link[strings.LastIndex(inv.Link, "/")+1:]
	away := d.tailnet("", "")
	if res, _, raw := away.do("POST", "/api/auth/link/"+link, map[string]any{"password": "sunflowers by the gate"}, true, nil); res.StatusCode != 200 {
		t.Fatalf("accept: %s", raw)
	}
	here := d.local()
	if res, _, raw := here.do("POST", "/api/auth/login", map[string]any{"email": "maya@example.com", "password": "sunflowers by the gate"}, true, nil); res.StatusCode != 200 {
		t.Fatalf("sign in at the host: %s", raw)
	}
	address, token := secretAddressFor()
	bodies := map[string]any{
		"/api/database/plan":   DatabaseMovePlanIn{Form: ops.DatabaseMoveForm{To: "elsewhere", Address: address, Name: "studio"}},
		"/api/database/move":   DatabaseMoveStartIn{MoveID: strings.Repeat("0", 24)},
		"/api/database/switch": DatabaseSwitchFormIn{Address: address, Name: "studio"},
	}
	for where, b := range map[string]*browser{"another computer": away, "the host's own address": here} {
		for path, body := range bodies {
			res, env, raw := b.do("POST", path, body, true, nil)
			if res.StatusCode != 401 || env.Error.Details["reason"] != "owner_only" || strings.Contains(string(raw), token) {
				t.Fatalf("%s from %s: %d %s", path, where, res.StatusCode, raw)
			}
		}
		res, env, _ := b.do("POST", "/api/ops/database.abandon", map[string]any{}, true, nil)
		if res.StatusCode != 401 || env.Error.Details["reason"] != "owner_only" {
			t.Fatalf("abandon from %s: %d %+v", where, res.StatusCode, env.Error)
		}
	}
	if kept, _ := ops.LoadMoveAddress(d.a.Cfg); kept != "" {
		t.Fatal("a member's address was kept")
	}
	// The owner, signed in from another computer.
	owner := d.tailnet("", "")
	if res, _, raw := owner.do("POST", "/api/auth/login", map[string]any{"email": "owner@example.com", "password": "tomato garden at dusk"}, true, nil); res.StatusCode != 200 {
		t.Fatalf("the owner from another computer: %s", raw)
	}
	for path, body := range bodies {
		res, env, raw := owner.do("POST", path, body, true, nil)
		if res.StatusCode == 200 || env.Error.Details["reason"] != "elsewhere" || !strings.Contains(env.Error.Message, "starts on") || strings.Contains(string(raw), token) {
			t.Fatalf("%s by the owner from another computer: %d %s", path, res.StatusCode, raw)
		}
	}
	if kept, _ := ops.LoadMoveAddress(d.a.Cfg); kept != "" {
		t.Fatal("an address typed on another computer was kept")
	}
}

// heldDash is the dashboard MetaRush serves while a move holds it (NewHeld),
// signed in on this computer.
func heldDash(t *testing.T, d *dash) *dash {
	t.Helper()
	hs := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(hs.Close)
	u, _ := url.Parse(hs.URL)
	d.a.Cfg.Dashboard.Port, _ = strconv.Atoi(u.Port())
	srv := NewHeld(d.a)
	t.Cleanup(srv.Assistant.Close)
	hs.Config.Handler = srv.Handler()
	jar, _ := cookiejar.New(nil)
	h := &dash{t: t, base: hs.URL, client: &http.Client{Jar: jar}, a: d.a, srv: srv}
	h.signIn()
	return h
}

// While a move holds this MetaRush, its dashboard reads and plays; changes
// outside the operations are refused plainly (the operations refuse their
// own), and signing in and out, the move's own endpoints and its operations
// still work. A dashboard that isn't held lets changes through.
func TestHeldDashboardReadsAndRefusesChanges(t *testing.T) {
	keyring.MockInit()
	t.Setenv("CLAUDECODE", "")
	d, projectID, _ := newDash(t, noClaude)
	ctx := context.Background()
	d.signIn()
	// Not held: a change outside the operations goes through to its handler.
	if code, env := d.do("POST", "/api/storage/plan", StorageMovePlanIn{Target: ops.StorageTarget{Provider: "filesystem", Root: filepath.Join(t.TempDir(), "media")}}, true); code != 200 || env.Error.Code == apperr.Moving {
		t.Fatalf("a dashboard that isn't held: %d %+v", code, env.Error)
	}

	h := heldDash(t, d)
	// Held with no move under way yet (it is starting): reads work, other
	// changes wait.
	if code, env := h.do("GET", "/api/session", nil, false); code != 200 {
		t.Fatalf("reading while held: %d %+v", code, env.Error)
	}
	refused := func(what string, code int, env envelope, reason string) {
		t.Helper()
		if code != http.StatusConflict || env.Error.Code != apperr.Moving || env.Error.Details["reason"] != reason {
			t.Fatalf("%s while held: %d %+v", what, code, env.Error)
		}
	}
	plan := StorageMovePlanIn{Target: ops.StorageTarget{Provider: "filesystem", Root: filepath.Join(t.TempDir(), "media")}}
	code, env := h.do("POST", "/api/storage/plan", plan, true)
	refused("a change outside the operations", code, env, "database_moving")

	// A move under way: reads and the move's own status work; operations
	// that change refuse themselves; other changes are refused by the guard.
	m := store.MoveMarker{Move: "mvheld1", Workspace: d.a.WorkspaceID(), To: store.MoveTarget{Kind: "server", Words: "a MongoDB server on your network", Name: "studio", Reach: "every-computer"},
		By: "Maya Okafor", From: "inst_other", FromName: "Studio Mac"}
	if err := d.a.DB.StartMoveMarker(ctx, m); err != nil {
		t.Fatal(err)
	}
	time.Sleep(store.MoveLookTrusted + 200*time.Millisecond)
	var st ops.DatabaseMoveStatusOut
	h.op("database.move.status", map[string]any{}, &st)
	if st.State != store.MoveMoving {
		t.Fatalf("status while held: %+v", st)
	}
	h.op("project.show", map[string]any{"id": projectID}, nil)
	code, env = h.do("POST", "/api/ops/project.create", ops.ProjectCreateIn{Name: "During the move"}, true)
	refused("an operation that changes", code, env, "database_moving")
	code, env = h.do("POST", "/api/storage/plan", plan, true)
	refused("a change outside the operations", code, env, "database_moving")
	if code, env := h.do("POST", "/api/database/switch", DatabaseSwitchFormIn{Name: "studio"}, true); env.Error.Code == apperr.Moving || code == 200 {
		t.Fatalf("the move's own endpoint while held: %d %+v", code, env.Error)
	}
	// Once it moved, the refusal says to switch.
	if ok, err := d.a.DB.SetMoveState(ctx, m.Move, store.MoveMoving, store.MoveMoved, ""); err != nil || !ok {
		t.Fatalf("moved: %v %v", ok, err)
	}
	time.Sleep(store.MoveLookTrusted + 200*time.Millisecond)
	code, env = h.do("POST", "/api/storage/plan", plan, true)
	refused("a change after the move", code, env, "database_moved")
	// Signing out still works.
	if code, env := h.do("POST", "/api/auth/logout", map[string]any{}, true); code != 200 {
		t.Fatalf("signing out while held: %d %+v", code, env.Error)
	}
}

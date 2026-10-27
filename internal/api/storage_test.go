package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
	"github.com/zalando/go-keyring"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/tools"
)

// Moving the media from the dashboard (D112): a signed-in session with the
// CSRF header only; the form's secret goes to the keychain and never comes
// back; typed text is taken as written; the move a person confirmed starts
// as `metarush storage migrate --dashboard-move <id>` in a process of its own
// with the one-time grant on its stdin (never in its arguments); one move at
// a time.
func TestStorageMoveEndpoints(t *testing.T) {
	keyring.MockInit()
	d, _, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	newRoot := filepath.Join(t.TempDir(), "Studio media")
	plan := StorageMovePlanIn{Target: ops.StorageTarget{Provider: "filesystem", Root: newRoot}}
	if code, env := d.do("POST", "/api/storage/plan", plan, true); code != http.StatusUnauthorized || env.Error.Details["reason"] != "no_session" {
		t.Fatalf("without a session: %d %+v", code, env.Error)
	}
	d.signIn()
	if code, env := d.do("POST", "/api/storage/plan", plan, false); code != http.StatusUnauthorized || env.Error.Details["reason"] != "csrf" {
		t.Fatalf("without the CSRF header: %d %+v", code, env.Error)
	}
	code, env := d.do("POST", "/api/storage/plan", plan, true)
	var out StorageMovePlanOut
	_ = json.Unmarshal(env.Data, &out)
	if code != 200 || !regexp.MustCompile(`^[0-9a-f]{24}$`).MatchString(out.MoveID) || out.Plan.To.Name != "the folder Studio media on this computer" || out.Plan.Summary == "" {
		t.Fatalf("plan: %d %+v %+v", code, out, env.Error)
	}
	if _, err := os.Stat(newRoot); !os.IsNotExist(err) {
		t.Fatalf("checking made the new folder: %v", err)
	}
	// Typed text is taken as written; unknown fields are refused.
	if code, env := d.do("POST", "/api/storage/plan", StorageMovePlanIn{Target: ops.StorageTarget{Provider: "filesystem", Root: "${HOME}/media"}}, true); code != 400 || env.Error.Code != apperr.InvalidInput {
		t.Fatalf("a variable: %d %+v", code, env.Error)
	}
	if code, _ := d.do("POST", "/api/storage/plan", map[string]any{"target": plan.Target, "to": "/etc/passwd"}, true); code != 400 {
		t.Fatalf("a description file from the browser: %d", code)
	}

	// Online storage: the secret is kept in the keychain, never sent back.
	backend := s3mem.New()
	s3 := httptest.NewServer(gofakes3.New(backend).Server())
	t.Cleanup(s3.Close)
	_ = backend.CreateBucket("footage")
	const secret = "browser-typed-secret-5678"
	req := map[string]any{"target": map[string]any{"provider": "s3", "preset": "custom", "endpoint": s3.URL, "bucket": "footage", "accessKeyId": "test",
		"secretAccessKey": secret, "forcePathStyle": true}}
	b, _ := json.Marshal(req)
	res, body := d.raw("POST", "/api/storage/plan", b)
	if res != 200 || strings.Contains(body, secret) || !strings.Contains(body, "S3 storage bucket footage") {
		t.Fatalf("online storage: %d %s", res, body)
	}

	// Starting a move: an unknown one is refused.
	if code, env := d.do("POST", "/api/storage/move", StorageMoveStartIn{MoveID: strings.Repeat("0", 24)}, true); code != 404 {
		t.Fatalf("unknown move: %d %+v", code, env.Error)
	}
	// The process it starts: MetaRush itself (a stand-in here) with the move
	// in its arguments and the grant on its stdin.
	seen := t.TempDir()
	t.Setenv("MR_TEST_MOVE_SEEN", seen)
	fake := filepath.Join(t.TempDir(), "metarush")
	_ = os.WriteFile(fake, []byte("#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$MR_TEST_MOVE_SEEN/args.tmp\"\ncat > \"$MR_TEST_MOVE_SEEN/stdin\"\nmv \"$MR_TEST_MOVE_SEEN/args.tmp\" \"$MR_TEST_MOVE_SEEN/args\"\necho moving\n"), 0o755)
	if !d.a.Tools.Use(tools.Self, fake) {
		t.Fatal("stand-in")
	}
	code, env = d.do("POST", "/api/storage/move", StorageMoveStartIn{MoveID: out.MoveID}, true)
	var started StorageMoveStartOut
	_ = json.Unmarshal(env.Data, &started)
	if code != 200 || !started.Started || !strings.HasPrefix(started.Summary, "Moving the media to the folder Studio media") {
		t.Fatalf("start: %d %+v %+v", code, started, env.Error)
	}
	var args []byte
	for i := 0; i < 100 && len(args) == 0; i++ {
		time.Sleep(50 * time.Millisecond)
		args, _ = os.ReadFile(filepath.Join(seen, "args"))
	}
	want := strings.Join([]string{"--home", d.a.Cfg.Home(), "storage", "migrate", "--dashboard-move", out.MoveID}, "\n") + "\n"
	if string(args) != want {
		t.Fatalf("arguments:\n%s\nwant:\n%s", args, want)
	}
	stdin, _ := os.ReadFile(filepath.Join(seen, "stdin"))
	token := strings.TrimSpace(string(stdin))
	if len(token) != 64 || strings.Contains(string(args), token) {
		t.Fatalf("the grant: %q", stdin)
	}
	logPath, _ := ops.DashboardMoveLog(d.a, out.MoveID)
	var logText []byte
	for i := 0; i < 100 && !strings.Contains(string(logText), "moving"); i++ {
		time.Sleep(50 * time.Millisecond)
		logText, _ = os.ReadFile(logPath)
	}
	if st, err := os.Stat(logPath); err != nil || st.Mode().Perm() != 0o600 || !strings.Contains(string(logText), "moving") {
		t.Fatalf("the move's log: %v %v %q", st, err, logText)
	}
	// The grant works once, for that move.
	if _, _, err := ops.ClaimDashboardMove(d.a, out.MoveID, token); err != nil {
		t.Fatalf("claim: %v", err)
	}
	// One move at a time: this process now "runs" it.
	code, env = d.do("POST", "/api/storage/plan", plan, true)
	var second StorageMovePlanOut
	_ = json.Unmarshal(env.Data, &second)
	if code != 200 {
		t.Fatalf("second plan: %d %+v", code, env.Error)
	}
	if code, env := d.do("POST", "/api/storage/move", StorageMoveStartIn{MoveID: second.MoveID}, true); code != http.StatusConflict {
		t.Fatalf("a second move while one runs: %d %+v", code, env.Error)
	}
	// The status says it runs.
	code, env = d.do("POST", "/api/ops/storage.status", map[string]any{}, true)
	var st ops.StorageStatusOut
	_ = json.Unmarshal(env.Data, &st)
	if code != 200 || !st.Moving || st.Move == nil || st.Move.State != ops.MoveRunning {
		t.Fatalf("status: %d %+v", code, st)
	}
}

// raw sends a JSON body as is and returns the status and the body text.
func (d *dash) raw(method, path string, body []byte) (int, string) {
	d.t.Helper()
	req, _ := http.NewRequest(method, d.base+path, strings.NewReader(string(body)))
	req.Header.Set(clientHeader, "dashboard")
	req.Header.Set("Content-Type", "application/json")
	res, err := d.client.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer res.Body.Close()
	var sb strings.Builder
	buf := make([]byte, 32<<10)
	for {
		n, err := res.Body.Read(buf)
		sb.Write(buf[:n])
		if err != nil {
			break
		}
	}
	return res.StatusCode, sb.String()
}

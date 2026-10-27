package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil"
	"github.com/metavert-io/metarush/internal/tools"
)

func TestMain(m *testing.M) {
	fakeclaude.MaybeRun() // this binary doubles as the fake `claude`
	os.Exit(m.Run())
}

type envelope struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error struct {
		Code    string         `json:"code"`
		Message string         `json:"message"`
		Details map[string]any `json:"details"`
	} `json:"error"`
}

type dash struct {
	t      *testing.T
	base   string
	client *http.Client
	a      *app.App
	srv    *Server
}

func (d *dash) do(method, path string, body any, csrf bool) (int, envelope) {
	d.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, d.base+path, rd)
	if csrf {
		req.Header.Set(clientHeader, "dashboard")
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := d.client.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer res.Body.Close()
	var env envelope
	_ = json.NewDecoder(res.Body).Decode(&env)
	return res.StatusCode, env
}

func (d *dash) op(name string, in, out any) {
	d.t.Helper()
	code, env := d.do("POST", "/api/ops/"+name, in, true)
	if code != 200 {
		d.t.Fatalf("%s: %d %+v", name, code, env.Error)
	}
	if out != nil {
		if err := json.Unmarshal(env.Data, out); err != nil {
			d.t.Fatal(err)
		}
	}
}

func newDash(t *testing.T, script func(head, scratch string) *fakeclaude.Script) (*dash, string, string) {
	t.Helper()
	db := testutil.DB(t)
	h := t.TempDir()
	cfg := config.Default(h)
	cfg.SetHome(h)
	cfg.Workspace = config.Ref{ID: ids.New(ids.Workspace), Name: "API test"}
	cfg.Installation = config.Ref{ID: ids.New(ids.Installation), Name: "api-test-machine"}
	cfg.User = config.UserRef{ID: ids.New(ids.User), Name: "Tester"}
	cfg.Database = config.Database{URI: testutil.URI(), Name: db.Name}
	a, err := app.OpenWith(context.Background(), cfg, "dashboard")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(a.Close)
	ctx := context.Background()
	p, err := ops.CallTyped[ops.ProjectOut](ctx, a, "project.create", ops.ProjectCreateIn{Name: "API film"}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	as := models.Asset{ID: ids.New(ids.Asset), WorkspaceID: a.WorkspaceID(), ProjectIDs: []string{p.Project.ID}, Kind: "video", Name: "take.mov",
		SHA256: strings.Repeat("cd", 32), Size: 10, MIME: "video/quicktime", StorageKey: "k", State: models.AssetAvailable,
		Media:    models.MediaInfo{Duration: 30, HasVideo: true, HasAudio: true, Width: 1920, Height: 1080, FPS: 30, Streams: []models.Stream{}},
		Variants: map[string]models.Variant{}, CreatedAt: now, UpdatedAt: now}
	if err := a.DB.Insert(ctx, store.CAssets, as); err != nil {
		t.Fatal(err)
	}
	plan := fmt.Sprintf(`{"projectId":%q,"spec":{"schemaVersion":1,"output":{"preset":"vertical-1080p","fps":30},"clips":[{"id":"a","assetId":%q,"in":0,"out":4},{"id":"b","assetId":%q,"in":8,"out":12}]}}`, p.Project.ID, as.ID, as.ID)
	first, err := ops.CallTyped[ops.EditApplyOut](ctx, a, "edit.apply", ops.EditApplyIn{Plan: json.RawMessage(plan)}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg.Tools.Claude = fakeclaude.Write(t, fakeclaude.Options{Script: script(first.Revision.ID, a.Cfg.Scratch.Dir)})
	a.Tools = tools.New(a.Cfg.Tools)

	hs := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(hs.Close)
	u, _ := url.Parse(hs.URL)
	a.Cfg.Dashboard.Port, _ = strconv.Atoi(u.Port()) // the Host guard allows this port only
	srv := New(a)
	t.Cleanup(srv.Assistant.Close)
	hs.Config.Handler = srv.Handler()
	jar, _ := cookiejar.New(nil)
	return &dash{t: t, base: hs.URL, client: &http.Client{Jar: jar}, a: a, srv: srv}, p.Project.ID, first.Revision.ID
}

func (d *dash) signIn() {
	d.t.Helper()
	nonce, err := MintLaunchNonce(d.a.Cfg.StateDir())
	if err != nil {
		d.t.Fatal(err)
	}
	res, err := d.client.Get(d.base + "/auth?t=" + nonce)
	if err != nil || res.StatusCode != http.StatusOK {
		d.t.Fatalf("sign-in: %v %v", err, res)
	}
	res.Body.Close()
}

func TestAssistantEndpointsNeedSessionAndCSRF(t *testing.T) {
	d, projectID, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	ask := map[string]any{"prompt": "hello", "projectId": projectID}
	id := ids.New(assistant.ConvPrefix)
	for _, c := range []struct{ method, path string }{{"GET", "/api/assistant"}, {"POST", "/api/ops/assistant.ask"}, {"POST", "/api/ops/assistant.list"},
		{"GET", "/api/assistant/conversations/" + id + "/events"}, {"GET", "/api/assistant/image?root=scratch&path=frames/x.jpg"}} {
		code, env := d.do(c.method, c.path, ask, true)
		if code != http.StatusUnauthorized || env.Error.Details["reason"] != "no_session" {
			t.Errorf("%s %s without a session: %d %+v", c.method, c.path, code, env.Error)
		}
	}
	d.signIn()
	if code, env := d.do("POST", "/api/ops/assistant.ask", ask, false); code != http.StatusUnauthorized || env.Error.Details["reason"] != "csrf" {
		t.Fatalf("POST without %s: %d %+v", clientHeader, code, env.Error)
	}
	if code, env := d.do("POST", "/api/ops/assistant.ask", map[string]any{"prompt": "x", "shell": "rm -rf /"}, true); code != http.StatusBadRequest {
		t.Fatalf("unknown fields must be rejected: %d %+v", code, env.Error)
	}
	// People-only and local-only rules hold over HTTP.
	if code, env := d.do("POST", "/api/ops/assistant.apply", map[string]any{"id": id, "proposal": "t1/x.plan.json"}, true); code != http.StatusUnauthorized || env.Error.Details["reason"] != "local_only" {
		t.Fatalf("assistant.apply over HTTP: %d %+v", code, env.Error)
	}
	req, _ := http.NewRequest("GET", d.base+"/api/assistant", nil)
	req.Host = "attacker.example:80"
	if res, err := d.client.Do(req); err != nil || res.StatusCode != http.StatusForbidden {
		t.Fatalf("foreign Host: %v %v", err, res)
	}
}

// sseMsg is one Server-Sent Event.
type sseMsg struct {
	event, id string
	data      []byte
}

// readSSE reads events until stop returns true (or the timeout).
func readSSE(t *testing.T, d *dash, path, lastID string, stop func(sseMsg) bool) []sseMsg {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", d.base+path, nil)
	if lastID != "" {
		req.Header.Set("Last-Event-ID", lastID)
	}
	res, err := d.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	var out []sseMsg
	sc := bufio.NewScanner(res.Body)
	sc.Buffer(make([]byte, 1<<20), 8<<20)
	cur := sseMsg{}
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, "event: "):
			cur.event = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "id: "):
			cur.id = strings.TrimPrefix(line, "id: ")
		case strings.HasPrefix(line, "data: "):
			cur.data = []byte(strings.TrimPrefix(line, "data: "))
		case line == "":
			if cur.data != nil {
				out = append(out, cur)
				if stop(cur) {
					return out
				}
			}
			cur = sseMsg{}
		}
	}
	t.Fatalf("stream closed early after %d messages", len(out))
	return nil
}

func TestAssistantConversationOverHTTP(t *testing.T) {
	d, projectID, head := newDash(t, func(head, scratch string) *fakeclaude.Script {
		plan := fmt.Sprintf(`{"baseRevisionId":%q,"instruction":"shorter b","ops":[{"op":"updateClip","id":"b","set":{"out":10}}]}`, head)
		return &fakeclaude.Script{Turns: []fakeclaude.Turn{
			{Match: "make b shorter", Steps: []fakeclaude.Step{
				{Tool: &fakeclaude.ToolStep{Name: "Read", Input: map[string]any{"file_path": scratch + "/frames/sheet.jpg"}, Result: "[image]"}},
				{Write: &fakeclaude.File{Name: "shorter.plan.json", Content: plan}},
				{Text: "Trimming clip b."},
			}},
			{Match: "and now", Steps: []fakeclaude.Step{{Text: "Second reply."}}},
		}}
	})
	d.signIn()
	var st assistant.AssistantStatus
	_, env := d.do("GET", "/api/assistant?refresh=1", nil, false)
	_ = json.Unmarshal(env.Data, &st)
	if !st.Available || st.Machine != "api-test-machine" || st.Billing != "plan" {
		t.Fatalf("status: %+v", st)
	}
	var sent assistant.ConversationSendOut
	d.op("assistant.ask", map[string]any{"prompt": "make b shorter", "projectId": projectID, "revisionId": head}, &sent)
	cv := sent.Conversation
	if cv.ProjectName != "API film" || sent.Turn != 1 || !cv.Here {
		t.Fatalf("ask: %+v", sent)
	}
	// SSE: events with ids, a meta snapshot, partial text, until the turn ends.
	path := "/api/assistant/conversations/" + cv.ID + "/events"
	var lastEvent int
	msgs := readSSE(t, d, path, "", func(m sseMsg) bool {
		if m.event == "" {
			var e assistant.AssistantEvent
			_ = json.Unmarshal(m.data, &e)
			lastEvent = e.Seq
			return e.Kind == "turn_end"
		}
		return false
	})
	kinds, sawMeta, sawPartial := []string{}, false, false
	for i, m := range msgs {
		switch m.event {
		case "":
			var e assistant.AssistantEvent
			_ = json.Unmarshal(m.data, &e)
			if m.id != strconv.Itoa(e.Seq) {
				t.Fatalf("message %d: SSE id %q for seq %d", i, m.id, e.Seq)
			}
			kinds = append(kinds, e.Kind)
		case "meta":
			var c assistant.Conversation
			_ = json.Unmarshal(m.data, &c)
			sawMeta = sawMeta || c.ID == cv.ID
			if len(c.Turns) > 0 && c.Turns[0].Sent != "" {
				t.Fatal("the stream leaks the raw message")
			}
		case "partial":
			var p assistant.AssistantPartial
			_ = json.Unmarshal(m.data, &p)
			sawPartial = sawPartial || p.Text != ""
		}
	}
	if got := strings.Join(kinds, ","); !strings.HasPrefix(got, "turn,") || !strings.Contains(got, "tool,tool_result") || !strings.HasSuffix(got, "turn_end") || !sawMeta || !sawPartial {
		t.Fatalf("stream: kinds=%s meta=%v partial=%v", got, sawMeta, sawPartial)
	}
	// Reconnect with Last-Event-ID: only what follows is replayed.
	rest := readSSE(t, d, path, strconv.Itoa(lastEvent-2), func(m sseMsg) bool { return m.event == "" && m.id == strconv.Itoa(lastEvent) })
	n := 0
	for _, m := range rest {
		if m.event == "" {
			n++
			if seq, _ := strconv.Atoi(m.id); seq <= lastEvent-2 {
				t.Fatalf("replayed an old event %s", m.id)
			}
		}
	}
	if n != 2 {
		t.Fatalf("replay after Last-Event-ID: %d events", n)
	}

	// The reply left a plan: the person gets one notification that opens this conversation.
	var note models.Notification
	for i := 0; i < 600; i++ { // up to 30 s: the notification is written in the background, slower on a busy machine
		if d.a.DB.C(store.CNotifications).FindOne(context.Background(), bson.M{"workspaceId": d.a.WorkspaceID(), "groupKey": "assistant:" + cv.ID + ":t1"}).Decode(&note) == nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if note.Title != "Claude Code proposed 1 change" || !strings.HasSuffix(note.Link, "?ask="+cv.ID) || note.Kind != models.NotifyAssistantProposal {
		t.Fatalf("notification: %+v", note)
	}

	// The thumbnail of the image Claude read is served; other paths are not.
	var full assistant.Conversation
	d.op("assistant.show", map[string]any{"id": cv.ID}, &full)
	var img *assistant.ImageRef
	for _, e := range full.Events {
		if e.Tool != nil && len(e.Tool.Images) > 0 {
			img = &e.Tool.Images[0]
		}
		if e.Tool != nil && (e.Tool.Input != "" || e.Tool.Output != "") {
			t.Fatal("assistant.show without raw leaks tool I/O")
		}
	}
	if img == nil || img.Root != "scratch" || img.Path != "frames/sheet.jpg" {
		t.Fatalf("image ref: %+v", img)
	}
	jpeg := []byte{0xff, 0xd8, 0xff, 0xe0, 0, 0x10, 'J', 'F', 'I', 'F', 0, 1, 1, 0, 0, 1, 0, 1, 0, 0}
	_ = os.MkdirAll(filepath.Join(d.a.Cfg.Scratch.Dir, "frames"), 0o755)
	_ = os.WriteFile(filepath.Join(d.a.Cfg.Scratch.Dir, "frames", "sheet.jpg"), jpeg, 0o600)
	res, err := d.client.Get(d.base + "/api/assistant/image?root=scratch&path=" + url.QueryEscape(img.Path))
	if err != nil || res.StatusCode != 200 || res.Header.Get("Content-Type") != "image/jpeg" || res.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("thumbnail: %v %+v", err, res)
	}
	res.Body.Close()
	for _, bad := range []string{"root=scratch&path=../config.yaml", "root=scratch&path=%2Fetc%2Fpasswd", "root=home&path=config.yaml", "root=assistant&path=..%2F..%2Fstate%2Fsessions.json"} {
		res, err := d.client.Get(d.base + "/api/assistant/image?" + bad)
		if err != nil || res.StatusCode == 200 {
			t.Errorf("served %s: %v %v", bad, err, res)
		}
		res.Body.Close()
	}

	// Proposals over HTTP carry no confirm token; the plan is applied through the ordinary ops endpoint.
	var props assistant.ConversationProposals
	d.op("assistant.proposals", map[string]any{"id": cv.ID}, &props)
	if len(props.Turns) != 1 || len(props.Turns[0].Plans) != 1 || props.Turns[0].Plans[0].Check == nil || props.Turns[0].Plans[0].Check.ConfirmToken != "" {
		t.Fatalf("proposals: %+v", props)
	}
	var applied ops.EditApplyOut
	d.op("edit.apply", map[string]any{"plan": props.Turns[0].Plans[0].Plan}, &applied)
	if applied.Revision.Origin != assistant.Origin(cv.ID, "t1/shorter.plan.json") {
		t.Fatalf("origin: %q", applied.Revision.Origin)
	}

	// A follow-up, then rename, archive and the list.
	d.op("assistant.continue", map[string]any{"id": cv.ID, "prompt": "and now the ending"}, &sent)
	readSSE(t, d, path, strconv.Itoa(lastEvent), func(m sseMsg) bool {
		var e assistant.AssistantEvent
		return m.event == "" && json.Unmarshal(m.data, &e) == nil && e.Kind == "turn_end"
	})
	d.op("assistant.rename", map[string]any{"id": cv.ID, "title": "Shorter b"}, nil)
	var list assistant.ConversationListOut
	d.op("assistant.list", map[string]any{}, &list)
	if len(list.Conversations) != 1 || list.Conversations[0].Title != "Shorter b" || list.Conversations[0].TurnCount != 2 || list.Conversations[0].Events != nil {
		t.Fatalf("list: %+v", list)
	}
	var sug assistant.AssistantSuggestionsOut
	d.op("assistant.suggestions", map[string]any{"projectId": projectID}, &sug)
	if len(sug.Suggestions) < 4 {
		t.Fatalf("suggestions: %+v", sug)
	}
	if code, _ := d.do("GET", "/api/assistant/conversations/asc_nope/events", nil, false); code != http.StatusNotFound {
		t.Fatalf("unknown conversation events: %d", code)
	}
}

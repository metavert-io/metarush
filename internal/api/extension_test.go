package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/browserext"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
)

const extOrigin = "chrome-extension://abcdefghijklmnopabcdefghijklmnop"

type extResp struct {
	OK    bool            `json:"ok"`
	Data  json.RawMessage `json:"data"`
	Error struct {
		Code    string `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func (d *dash) ext(method, path, origin, token string, body []byte, hdr map[string]string) (int, extResp, http.Header) {
	d.t.Helper()
	req, _ := http.NewRequest(method, d.base+path, bytes.NewReader(body))
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	req.Header.Set("X-MetaRush-Client", "extension")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	// A plain client: the extension never has the dashboard's session cookie.
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer res.Body.Close()
	var out extResp
	_ = json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out, res.Header
}

func pairCode(t *testing.T, d *dash) string {
	t.Helper()
	out, err := ops.CallTyped[browserext.ExtensionPairOut](context.Background(), d.a, "extension.pair", browserext.ExtensionPairIn{}, ops.Caller{Human: true})
	if err != nil {
		t.Fatal(err)
	}
	return out.Code
}

func TestExtensionPairing(t *testing.T) {
	d, _, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	// Pairing is for people: the CLI without a person and the assistant can't make a code.
	if _, err := ops.CallTyped[browserext.ExtensionPairOut](context.Background(), d.a, "extension.pair", browserext.ExtensionPairIn{}, ops.Caller{}); err == nil {
		t.Fatal("a code was made without a person")
	}
	code := pairCode(t, d)
	in, _ := json.Marshal(ExtPairIn{Code: code, Name: "Chrome", Version: "0.1.0"})

	// A web page (any http origin, or none) can't pair, even with the right code.
	for _, o := range []string{"", "https://evil.example", "http://127.0.0.1:4845", "chrome-extension://short"} {
		if st, out, _ := d.ext("POST", "/ext/pair", o, "", in, nil); st != http.StatusUnauthorized || out.OK {
			t.Fatalf("origin %q paired: %d %+v", o, st, out)
		}
	}
	// Without the client header (a cross-site form): refused.
	req, _ := http.NewRequest("POST", d.base+"/ext/pair", bytes.NewReader(in))
	req.Header.Set("Origin", extOrigin)
	if res, _ := http.DefaultClient.Do(req); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("no client header: %d", res.StatusCode)
	}
	// A wrong code doesn't pair; five wrong codes burn the real one.
	bad, _ := json.Marshal(ExtPairIn{Code: "ZZZZ-ZZZZ"})
	if st, _, _ := d.ext("POST", "/ext/pair", extOrigin, "", bad, nil); st != http.StatusUnauthorized {
		t.Fatalf("a wrong code paired: %d", st)
	}
	st, out, hdr := d.ext("POST", "/ext/pair", extOrigin, "", in, nil)
	if st != http.StatusOK || !out.OK {
		t.Fatalf("pairing with the code: %d %+v", st, out)
	}
	if hdr.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("no CORS headers: a web page must never read a reply")
	}
	var p ExtPairOut
	_ = json.Unmarshal(out.Data, &p)
	if !strings.HasPrefix(p.Token, browserext.TokenPrefix) || p.Machine != "api-test-machine" {
		t.Fatalf("token: %+v", p)
	}
	// The code works once.
	if st, _, _ := d.ext("POST", "/ext/pair", extOrigin, "", in, nil); st != http.StatusUnauthorized {
		t.Fatalf("a code paired twice: %d", st)
	}
	// The token is stored hashed, never in the clear.
	b, _ := os.ReadFile(filepath.Join(d.a.Cfg.StateDir(), "browser-extension.json"))
	if bytes.Contains(b, []byte(p.Token)) || bytes.Contains(b, []byte(code)) {
		t.Fatal("the token or the code is stored in the clear")
	}
	fi, _ := os.Stat(filepath.Join(d.a.Cfg.StateDir(), "browser-extension.json"))
	if fi.Mode().Perm() != 0o600 {
		t.Fatalf("pairing file mode %v", fi.Mode().Perm())
	}

	// Burn: five wrong tries end a waiting code.
	code2 := pairCode(t, d)
	for i := 0; i < 5; i++ {
		d.ext("POST", "/ext/pair", "chrome-extension://bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "", bad, nil)
	}
	in2, _ := json.Marshal(ExtPairIn{Code: code2})
	if st, _, _ := d.ext("POST", "/ext/pair", "chrome-extension://bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "", in2, nil); st != http.StatusUnauthorized {
		t.Fatalf("a code survived five wrong guesses: %d", st)
	}

	// Expired codes don't pair.
	s := browserext.Of(d.a)
	c3, _, _ := s.NewCode(time.Now().Add(-time.Hour), browserext.Person{ID: d.a.Cfg.User.ID})
	if _, _, err := s.Pair(c3, extOrigin, "x", "1", time.Now()); err != browserext.ErrCodeExpired {
		t.Fatalf("expired code: %v", err)
	}
}

func TestExtensionTokenScope(t *testing.T) {
	d, _, _ := newDash(t, func(string, string) *fakeclaude.Script { return nil })
	code := pairCode(t, d)
	in, _ := json.Marshal(ExtPairIn{Code: code, Name: "Chrome"})
	_, out, _ := d.ext("POST", "/ext/pair", extOrigin, "", in, nil)
	var p ExtPairOut
	_ = json.Unmarshal(out.Data, &p)
	tok := p.Token

	if st, _, _ := d.ext("POST", "/ext/hello", extOrigin, tok, []byte(`{}`), nil); st != http.StatusOK {
		t.Fatalf("hello: %d", st)
	}
	// Bound to its origin: the same token from another extension (or a page) is refused.
	if st, _, _ := d.ext("POST", "/ext/hello", "chrome-extension://cccccccccccccccccccccccccccccccc", tok, []byte(`{}`), nil); st != http.StatusUnauthorized {
		t.Fatalf("token from another origin: %d", st)
	}
	// It never reads data: the dashboard API refuses it (no session), every op included.
	for _, path := range []string{"/api/ops/asset.list", "/api/ops/project.list", "/api/ops/activity.summary", "/api/uploads"} {
		req, _ := http.NewRequest("POST", d.base+path, strings.NewReader(`{}`))
		req.Header.Set("Origin", extOrigin)
		req.Header.Set("X-MetaRush-Client", "dashboard")
		req.Header.Set("Authorization", "Bearer "+tok)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		res.Body.Close()
		if res.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s with the extension token: %d", path, res.StatusCode)
		}
	}
	if res, _ := http.Get(d.base + "/media/assets/x/proxy"); res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("media without a session: %d", res.StatusCode)
	}

	// A link import: queued on this machine, answered in words only.
	imp, _ := json.Marshal(ExtImportIn{URL: "https://www.youtube.com/watch?v=aqz-KE-bpKQ"})
	st, out, _ := d.ext("POST", "/ext/imports", extOrigin, tok, imp, nil)
	var io ExtImportOut
	_ = json.Unmarshal(out.Data, &io)
	if st != http.StatusOK || io.Status != "queued" || strings.Contains(string(out.Data), "job_") {
		t.Fatalf("import: %d %s", st, out.Data)
	}
	var j models.Job
	if err := d.a.DB.C(store.CJobs).FindOne(context.Background(), bson.M{"workspaceId": d.a.WorkspaceID(), "type": "import"}).Decode(&j); err != nil || j.CreatedBy.Via != models.ViaExtension || j.Installation != d.a.Cfg.Installation.ID {
		t.Fatalf("the import job came from the extension and runs here: %+v %v", j, err)
	}

	// An upload of a video saved in the browser: provenance from the page, no project.
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg is needed for the upload part")
	}
	clip := filepath.Join(t.TempDir(), "clip.mp4")
	if b, err := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-f", "lavfi", "-i", "testsrc2=size=160x90:rate=25:duration=1", "-f", "lavfi", "-i", "sine=frequency=440:duration=1",
		"-c:v", "mpeg4", "-c:a", "aac", "-shortest", clip).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, b)
	}
	data, _ := os.ReadFile(clip)
	up, _ := json.Marshal(ExtUploadIn{Name: "Big Buck Bunny.mp4", Size: int64(len(data)), Method: "recording", Source: ExtSource{URL: "https://www.youtube.com/watch?v=aqz-KE-bpKQ&t=3",
		Title: "Big Buck Bunny", Uploader: "Blender", Handle: "@BlenderOfficial", ChannelURL: "https://www.youtube.com/@BlenderOfficial", UploadDate: "2014-11-10",
		Description: strings.Repeat("x", 5000)}})
	st, out, _ = d.ext("POST", "/ext/uploads", extOrigin, tok, up, map[string]string{"X-MetaRush-Extension-Version": "0.1.0"})
	var u ExtUploadOut
	_ = json.Unmarshal(out.Data, &u)
	if st != http.StatusOK || u.ID == "" || u.Offset != 0 {
		t.Fatalf("create upload: %d %s", st, out.Data)
	}
	// Another paired browser can't send bytes into it, or learn its offset.
	code2 := pairCode(t, d)
	in2, _ := json.Marshal(ExtPairIn{Code: code2})
	_, out2, _ := d.ext("POST", "/ext/pair", "chrome-extension://dddddddddddddddddddddddddddddddd", "", in2, nil)
	var p2 ExtPairOut
	_ = json.Unmarshal(out2.Data, &p2)
	if st, _, _ := d.ext("POST", "/ext/uploads/"+u.ID+"/offset", "chrome-extension://dddddddddddddddddddddddddddddddd", p2.Token, []byte(`{}`), nil); st != http.StatusNotFound {
		t.Fatalf("another browser saw the upload: %d", st)
	}
	st, out, _ = d.ext("PATCH", "/ext/uploads/"+u.ID, extOrigin, tok, data, map[string]string{"Upload-Offset": "0", "Content-Type": "application/offset+octet-stream"})
	var after ExtUploadOut
	_ = json.Unmarshal(out.Data, &after)
	if st != http.StatusOK || after.Offset != int64(len(data)) || strings.Contains(string(out.Data), "result") {
		t.Fatalf("chunk: %d %s", st, out.Data)
	}
	// The ingest runs in the background: wait for the clip.
	var as models.Asset
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := d.a.DB.C(store.CAssets).FindOne(context.Background(), bson.M{"workspaceId": d.a.WorkspaceID(), "source.videoId": "aqz-KE-bpKQ"}).Decode(&as)
		if err == nil && as.State == models.AssetAvailable {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the uploaded clip never arrived: %v", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
	s := as.Source
	if s.Platform != "youtube" || s.URL != "https://www.youtube.com/watch?v=aqz-KE-bpKQ" || s.Handle != "@BlenderOfficial" || s.Method != models.SourceRecording ||
		s.Tool != "metarush-extension" || s.ToolVersion != "0.1.0" || !s.SignedIn || s.UploadDate != "2014-11-10" || len([]rune(s.Description)) > 1000 ||
		s.ChannelURL != "https://www.youtube.com/@BlenderOfficial" {
		t.Fatalf("provenance from the page: %+v", s)
	}
	if as.DisplayTitle() != "Big Buck Bunny" || len(as.ProjectIDs) != 0 || as.Via != models.ViaExtension {
		t.Fatalf("the clip lands in the inbox, titled after the page: %q %v %s", as.DisplayTitle(), as.ProjectIDs, as.Via)
	}
	// A page address MetaRush doesn't recognize is refused.
	bad, _ := json.Marshal(ExtUploadIn{Name: "x.mp4", Size: 10, Method: "browser", Source: ExtSource{URL: "https://evil.example/"}})
	if st, _, _ := d.ext("POST", "/ext/uploads", extOrigin, tok, bad, nil); st != http.StatusBadRequest {
		t.Fatalf("unknown page: %d", st)
	}

	// Revoked: the token stops working at once.
	list, _ := ops.CallTyped[browserext.ExtensionStatusOut](context.Background(), d.a, "extension.status", browserext.ExtensionStatusIn{}, ops.Caller{})
	if len(list.Browsers) != 2 || list.Browsers[0].Sent < 1 {
		t.Fatalf("two paired browsers, the first has sent videos: %+v", list.Browsers)
	}
	if _, err := os.Stat(filepath.Join(list.Folder, "manifest.json")); err != nil {
		t.Fatalf("the extension folder is written: %v", err)
	}
	if _, err := ops.CallTyped[browserext.ExtensionRevokeOut](context.Background(), d.a, "extension.revoke", browserext.ExtensionRevokeIn{ID: list.Browsers[0].ID}, ops.Caller{}); err != nil {
		t.Fatal(err)
	}
	if st, _, _ := d.ext("POST", "/ext/hello", extOrigin, tok, []byte(`{}`), nil); st != http.StatusUnauthorized {
		t.Fatalf("a revoked token still works: %d", st)
	}
	if st, _, _ := d.ext("POST", "/ext/hello", "chrome-extension://dddddddddddddddddddddddddddddddd", p2.Token, []byte(`{}`), nil); st != http.StatusOK {
		t.Fatalf("revoking one browser left the other paired: %d", st)
	}
}

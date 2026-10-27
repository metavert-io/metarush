package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/assistant/fakeclaude"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/uploads"
	"github.com/metavert-io/metarush/web"
)

func noClaude(string, string) *fakeclaude.Script { return nil }

func needFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
}

// clipFile makes a short clip (picture and a tone, or the tone alone).
func clipFile(t *testing.T, name string, seconds int, picture bool) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	args := []string{"-hide_banner", "-loglevel", "error", "-y"}
	if picture {
		args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=320x240:rate=25:duration=%d", seconds))
	}
	args = append(args, "-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=440:duration=%d", seconds), "-shortest")
	if picture {
		args = append(args, "-pix_fmt", "yuv420p")
	}
	args = append(args, "-metadata", "comment="+name, p)
	if out, err := exec.Command("ffmpeg", args...).CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg: %v %s", err, out)
	}
	return p
}

// send is a request with raw bytes and headers (the dashboard's own header
// unless csrf is false), answering the status, the headers and the body.
func (d *dash) send(method, path string, body []byte, hdr map[string]string, csrf bool) (int, http.Header, []byte) {
	d.t.Helper()
	req, _ := http.NewRequest(method, d.base+path, bytes.NewReader(body))
	if csrf {
		req.Header.Set(clientHeader, "dashboard")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	res, err := d.client.Do(req)
	if err != nil {
		d.t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, res.Header, b
}

func envOf(t *testing.T, b []byte) envelope {
	t.Helper()
	var env envelope
	if err := json.Unmarshal(b, &env); err != nil {
		t.Fatalf("not an answer: %s", b)
	}
	return env
}

// Browser uploads (resumable): a start is checked and keeps only the file's
// own name; a chunk at the wrong place is refused with where to carry on; the
// file arrives in parts and is added like any other, for the person who sent
// it; finished uploads take no more bytes and are dismissed from every tray;
// one stopped while sending is canceled with its part.
func TestBrowserUploadsResumeAndAddTheClip(t *testing.T) {
	needFFmpeg(t)
	d, projectID, _ := newDash(t, noClaude)
	d.signIn()
	clip, err := os.ReadFile(clipFile(t, "harbour.mp4", 2, true))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		in   map[string]any
		says string
	}{
		{"no name", map[string]any{"name": "  ", "size": 10}, "needs the file's name and size"},
		{"no size", map[string]any{"name": "a.mp4", "size": 0}, "needs the file's name and size"},
		{"too large", map[string]any{"name": "a.mp4", "size": int64(600) << 30}, "larger than 500 GB"},
		{"a long fingerprint", map[string]any{"name": "a.mp4", "size": 10, "fingerprint": strings.Repeat("f", 81)}, "fingerprint"},
	} {
		code, env := d.do("POST", "/api/uploads", c.in, true)
		if code != http.StatusBadRequest || !strings.Contains(env.Error.Message, c.says) {
			t.Errorf("%s: %d %+v", c.name, code, env.Error)
		}
	}
	if code, env := d.do("POST", "/api/uploads", map[string]any{"name": "a.mp4", "size": 10}, false); code != http.StatusUnauthorized || env.Error.Details["reason"] != "csrf" {
		t.Fatalf("without the dashboard's header: %d %+v", code, env.Error)
	}
	code, env := d.do("POST", "/api/uploads", map[string]any{"name": `../../elsewhere\harbour.mp4`, "size": len(clip), "projectId": projectID, "fingerprint": "fp1:test"}, true)
	if code != 200 {
		t.Fatalf("start: %d %+v", code, env.Error)
	}
	var rec uploads.Record
	_ = json.Unmarshal(env.Data, &rec)
	if rec.Name != "harbour.mp4" || rec.State != uploads.StateReceiving || rec.UserID != d.a.Cfg.User.ID || rec.Offset != 0 {
		t.Fatalf("started %+v", rec)
	}
	path := "/api/uploads/" + rec.ID
	if code, h, _ := d.send("HEAD", path, nil, nil, false); code != http.StatusNoContent || h.Get("Upload-Offset") != "0" || h.Get("Upload-Length") != strconv.Itoa(len(clip)) ||
		h.Get("Upload-State") != uploads.StateReceiving {
		t.Fatalf("head: %d %v", code, h)
	}
	if code, _, _ := d.send("HEAD", "/api/uploads/upl_nothing", nil, nil, false); code != http.StatusNotFound {
		t.Fatalf("head of nothing: %d", code)
	}
	patch := func(off int, b []byte) (int, http.Header, envelope) {
		t.Helper()
		code, h, body := d.send("PATCH", path, b, map[string]string{"Upload-Offset": strconv.Itoa(off), "Content-Type": "application/offset+octet-stream"}, true)
		return code, h, envOf(t, body)
	}
	if code, h, env := patch(5, clip[:10]); code != http.StatusConflict || h.Get("Upload-Offset") != "0" || !strings.Contains(env.Error.Message, "carries on from the part MetaRush already has") {
		t.Fatalf("out of step: %d %v %+v", code, h, env.Error)
	}
	half := len(clip) / 2
	if code, h, _ := patch(0, clip[:half]); code != 200 || h.Get("Upload-Offset") != strconv.Itoa(half) {
		t.Fatalf("the first half: %d %v", code, h)
	}
	if code, env := d.do("GET", path, nil, false); code != 200 || !strings.Contains(string(env.Data), fmt.Sprintf(`"offset":%d`, half)) {
		t.Fatalf("where it is: %d %s", code, env.Data)
	}
	if code, _, env := patch(half, clip[half:]); code != 200 || !strings.Contains(string(env.Data), `"state":"ingesting"`) {
		t.Fatalf("the rest: %d %s", code, env.Data)
	}
	// Added like any other file, then done.
	end := time.Now().Add(60 * time.Second)
	for {
		_, env := d.do("GET", path, nil, false)
		_ = json.Unmarshal(env.Data, &rec)
		if rec.State == uploads.StateDone || rec.State == uploads.StateFailed {
			break
		}
		if time.Now().After(end) {
			t.Fatalf("never added: %+v", rec)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if rec.State != uploads.StateDone || rec.Result == nil || rec.Result.AssetID == "" {
		t.Fatalf("added %+v %+v", rec, rec.Result)
	}
	var as models.Asset
	if err := d.a.DB.GetByID(context.Background(), store.CAssets, d.a.WorkspaceID(), rec.Result.AssetID, &as); err != nil || len(as.ProjectIDs) != 1 || as.ProjectIDs[0] != projectID ||
		as.CreatedBy.UserID != d.a.Cfg.User.ID || as.CreatedBy.Via != "dashboard" {
		t.Fatalf("the clip: %+v %v", as, err)
	}
	if code, _, env := patch(len(clip), []byte("x")); code != http.StatusConflict || !strings.Contains(env.Error.Message, "This upload is finished") {
		t.Fatalf("bytes after it is done: %d %+v", code, env.Error)
	}
	if code, env := d.do("GET", "/api/uploads", nil, false); code != 200 || !strings.Contains(string(env.Data), rec.ID) {
		t.Fatalf("list: %d %s", code, env.Data)
	}
	if code, env := d.do("DELETE", path, nil, true); code != 200 || !strings.Contains(string(env.Data), `"dismissed":true`) {
		t.Fatalf("dismissed: %d %s", code, env.Data)
	}
	// Stopped while sending: canceled, its part gone, no more bytes taken.
	code, env = d.do("POST", "/api/uploads", map[string]any{"name": "spare.mp4", "size": 100}, true)
	if code != 200 {
		t.Fatalf("second start: %d %+v", code, env.Error)
	}
	var spare uploads.Record
	_ = json.Unmarshal(env.Data, &spare)
	if code, env := d.do("DELETE", "/api/uploads/"+spare.ID, nil, true); code != 200 || !strings.Contains(string(env.Data), `"state":"canceled"`) {
		t.Fatalf("canceled: %d %s", code, env.Data)
	}
	if code, _, body := d.send("PATCH", "/api/uploads/"+spare.ID, []byte("abc"), map[string]string{"Upload-Offset": "0"}, true); code != http.StatusConflict ||
		!strings.Contains(envOf(t, body).Error.Message, "This upload is canceled") {
		t.Fatalf("bytes after canceling: %d %s", code, body)
	}
	for _, m := range []string{"GET", "DELETE"} {
		if code, env := d.do(m, "/api/uploads/upl_nothing", nil, true); code != http.StatusNotFound || env.Error.Code != "E_NOT_FOUND" {
			t.Errorf("%s of nothing: %d %+v", m, code, env.Error)
		}
	}
	if code, _, body := d.send("PATCH", "/api/uploads/upl_nothing", []byte("x"), map[string]string{"Upload-Offset": "0"}, true); code != http.StatusNotFound {
		t.Fatalf("bytes for nothing: %d %s", code, body)
	}
	if got := uploadStateWords("weird"); got != "not taking the file now" {
		t.Fatalf("state words %q", got)
	}
}

// Stills and listening clips of a clip (sample tiles, voices): cut once and
// kept, a still's width kept sensible and its time inside the clip; a clip
// without a picture or without sound says so; a listening clip is at most
// MetaRush's limit and inside the clip.
func TestStillsAndListeningClipsOfAClip(t *testing.T) {
	needFFmpeg(t)
	d, projectID, _ := newDash(t, noClaude)
	ctx := context.Background()
	video := ingest.File(ctx, d.a, clipFile(t, "pier.mp4", 3, true), ingest.Options{ProjectID: projectID, Via: "cli"})
	sound := ingest.File(ctx, d.a, clipFile(t, "birds.wav", 3, false), ingest.Options{ProjectID: projectID, Via: "cli"})
	if video.AssetID == "" || sound.AssetID == "" {
		t.Fatalf("ingest %+v %+v", video, sound)
	}
	get := func(path string) (int, http.Header, []byte) {
		t.Helper()
		return d.send("GET", path, nil, nil, false)
	}
	if code, _, _ := get("/media/assets/" + video.AssetID + "/frame?t=1"); code != http.StatusUnauthorized {
		t.Fatalf("a still without signing in: %d", code)
	}
	d.signIn()
	for _, q := range []string{"t=1.23&w=100", "t=2.1&w=10", "t=-4&w=5000", "t=1.23&w=100"} {
		code, h, b := get("/media/assets/" + video.AssetID + "/frame?" + q)
		if code != 200 || h.Get("Content-Type") != "image/jpeg" || len(b) < 100 || !bytes.HasPrefix(b, []byte{0xff, 0xd8}) {
			t.Fatalf("still %s: %d %v %d bytes", q, code, h, len(b))
		}
	}
	frames, _ := os.ReadDir(filepath.Join(d.a.Cfg.Scratch.Dir, "frames"))
	if len(frames) != 3 {
		t.Fatalf("stills kept: %d (the same one is cut once)", len(frames))
	}
	if code, _, b := get("/media/assets/" + sound.AssetID + "/frame?t=1"); code != http.StatusNotFound || !strings.Contains(envOf(t, b).Error.Message, "has no picture") {
		t.Fatalf("a still of a sound: %d %s", code, b)
	}
	for _, id := range []string{video.AssetID, sound.AssetID} {
		code, h, b := get("/media/assets/" + id + "/snippet?from=0.5&to=1.5")
		if code != 200 || h.Get("Content-Type") != "audio/wav" || !bytes.HasPrefix(b, []byte("RIFF")) {
			t.Fatalf("listening clip of %s: %d %v", id, code, h)
		}
	}
	for _, q := range []string{"from=1&to=0.5", "from=0&to=30", "from=abc&to=1", "from=10&to=11"} {
		if code, _, b := get("/media/assets/" + video.AssetID + "/snippet?" + q); code != http.StatusBadRequest || !strings.Contains(envOf(t, b).Error.Message, "plays up to") {
			t.Errorf("listening clip %s: %d %s", q, code, b)
		}
	}
	if code, _, _ := get("/media/assets/ast_00000000000000000000000000/snippet?from=0&to=1"); code != http.StatusNotFound {
		t.Fatalf("a clip that isn't there: %d", code)
	}
	silent := models.Asset{ID: "ast_01silent00000000000000000", WorkspaceID: d.a.WorkspaceID(), Kind: "video", Name: "silent.mp4", State: models.AssetAvailable,
		SHA256: strings.Repeat("ab", 32), StorageKey: "k", Size: 1, Media: models.MediaInfo{Duration: 3, HasVideo: true}, CreatedAt: time.Now(), UpdatedAt: time.Now()}
	if err := d.a.DB.Insert(ctx, store.CAssets, silent); err != nil {
		t.Fatal(err)
	}
	if code, _, b := get("/media/assets/" + silent.ID + "/snippet?from=0&to=1"); code != http.StatusNotFound || !strings.Contains(envOf(t, b).Error.Message, "has no sound to play") {
		t.Fatalf("a clip without sound: %d %s", code, b)
	}
}

func pngFile(t *testing.T, name string) string {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), 90, uint8(y * 5), 255})
		}
	}
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Resource files are served to the signed-in dashboard from this computer,
// never as a redirect, by version, as a download when asked, with a policy
// that keeps an SVG from running anything even when opened on its own.
func TestResourceFilesAreServedSafely(t *testing.T) {
	d, projectID, _ := newDash(t, noClaude)
	ctx := context.Background()
	logo, err := resources.AddFile(ctx, d.a, pngFile(t, "harbour-logo.png"), resources.AddOptions{ProjectID: projectID})
	if err != nil || logo.Resource == nil {
		t.Fatalf("add the logo: %+v %v", logo, err)
	}
	svgPath := filepath.Join(t.TempDir(), "wave.svg")
	if err := os.WriteFile(svgPath, []byte(`<svg xmlns="http://www.w3.org/2000/svg" width="10" height="10"><rect width="10" height="10" fill="#08f"/></svg>`), 0o600); err != nil {
		t.Fatal(err)
	}
	wave, err := resources.AddFile(ctx, d.a, svgPath, resources.AddOptions{ProjectID: projectID})
	if err != nil || wave.Resource == nil {
		t.Fatalf("add the SVG: %+v %v", wave, err)
	}
	lv := logo.Resource.Latest()
	file := "/media/resources/" + logo.Resource.ID + "/file"
	if code, _, _ := d.send("GET", file, nil, nil, false); code != http.StatusUnauthorized {
		t.Fatalf("without signing in: %d", code)
	}
	d.signIn()
	code, h, b := d.send("GET", file, nil, nil, false)
	if code != 200 || h.Get("Content-Type") != "image/png" || !strings.Contains(h.Get("Content-Security-Policy"), "sandbox") || !bytes.HasPrefix(b, []byte("\x89PNG")) {
		t.Fatalf("the logo: %d %v", code, h)
	}
	if code, h, _ := d.send("GET", file+"?v="+lv.SHA256[:8]+"&download=1", nil, nil, false); code != 200 ||
		!strings.HasPrefix(h.Get("Content-Disposition"), `attachment; filename="harbour-logo.`) {
		t.Fatalf("a download of one version: %d %v", code, h)
	}
	if code, _, b := d.send("GET", file+"?v=ffffffff", nil, nil, false); code != http.StatusNotFound || !strings.Contains(envOf(t, b).Error.Message, "has no version like that") {
		t.Fatalf("a version that isn't there: %d %s", code, b)
	}
	if code, h, _ := d.send("GET", "/media/resources/file/"+lv.SHA256, nil, nil, false); code != 200 || h.Get("Content-Type") != "image/png" {
		t.Fatalf("by its SHA-256: %d %v", code, h)
	}
	for _, p := range []string{"/media/resources/file/" + strings.Repeat("0", 64), "/media/resources/file/not-a-hash", "/media/resources/res_00000000000000000000000000/file",
		"/media/resources/" + logo.Resource.ID + "/other"} {
		if code, _, _ := d.send("GET", p, nil, nil, false); code != http.StatusNotFound {
			t.Errorf("%s: %d", p, code)
		}
	}
	// The SVG: its own thumbnail, never able to run anything.
	code, h, b = d.send("GET", "/media/resources/"+wave.Resource.ID+"/thumb", nil, nil, false)
	if code != 200 || h.Get("Content-Type") != "image/svg+xml" || !strings.Contains(h.Get("Content-Security-Policy"), "default-src 'none'") || !bytes.Contains(b, []byte("<svg")) {
		t.Fatalf("the SVG's thumbnail: %d %v %s", code, h, b)
	}
	// The picture's thumbnail, when it has one.
	code, h, _ = d.send("GET", "/media/resources/"+logo.Resource.ID+"/thumb", nil, nil, false)
	if lv.Thumbnail != nil && lv.Thumbnail.StorageKey != "" {
		if code != 200 || h.Get("Content-Type") != "image/png" {
			t.Fatalf("the logo's thumbnail: %d %v", code, h)
		}
	} else if code != http.StatusNotFound {
		t.Fatalf("a picture without a thumbnail: %d", code)
	}
}

// The one-time sign-in link: incomplete, used or expired links are refused
// plainly; a used link never works twice; where it leads is only ever a page
// of this dashboard (never another site).
func TestTheOneTimeSignInLink(t *testing.T) {
	d, _, _ := newDash(t, noClaude)
	plain := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	visit := func(q string) (int, string, string) {
		t.Helper()
		res, err := plain.Get(d.base + "/auth?" + q)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		b, _ := io.ReadAll(res.Body)
		return res.StatusCode, res.Header.Get("Location"), string(b)
	}
	if code, _, body := visit("t=short"); code != http.StatusBadRequest || !strings.Contains(body, "isn't complete") {
		t.Fatalf("an incomplete link: %d %q", code, body)
	}
	if code, _, body := visit("t=" + strings.Repeat("a", 44) + "/../"); code != http.StatusBadRequest || !strings.Contains(body, "isn't complete") {
		t.Fatalf("a link with a path in it: %d %q", code, body)
	}
	n, err := MintLaunchNonce(d.a.Cfg.StateDir())
	if err != nil {
		t.Fatal(err)
	}
	code, loc, _ := visit("t=" + n + "&next=" + "/p/prj_x?tab=sources")
	if code != http.StatusFound || loc != "/p/prj_x?tab=sources" {
		t.Fatalf("signed in: %d %q", code, loc)
	}
	if code, _, body := visit("t=" + n); code != http.StatusUnauthorized || !strings.Contains(body, "already used") {
		t.Fatalf("used twice: %d %q", code, body)
	}
	for _, next := range []string{"//evil.example/x", "https://evil.example/", `/\evil.example`, "relative"} {
		n, _ := MintLaunchNonce(d.a.Cfg.StateDir())
		if code, loc, _ := visit("t=" + n + "&next=" + next); code != http.StatusFound || loc != "/" {
			t.Errorf("next %q led to %q (%d)", next, loc, code)
		}
	}
	// A link older than ten minutes.
	old, _ := MintLaunchNonce(d.a.Cfg.StateDir())
	if err := os.WriteFile(filepath.Join(d.a.Cfg.StateDir(), "nonces", old), []byte(time.Now().Add(-11*time.Minute).UTC().Format(time.RFC3339)), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, _, body := visit("t=" + old); code != http.StatusUnauthorized || !strings.Contains(body, "has expired") {
		t.Fatalf("an expired link: %d %q", code, body)
	}
}

// The status, the list of operations and the page served when this build has
// no dashboard; a request without a session says how to sign in.
func TestStatusOpsAndTheBareDashboard(t *testing.T) {
	d, _, _ := newDash(t, noClaude)
	code, env := d.do("GET", "/api/status", nil, false)
	if code != http.StatusUnauthorized || env.Error.Details["reason"] != "no_session" || env.Error.Message != "This browser is not signed in to MetaRush." || env.Error.Details["method"] != "single" {
		t.Fatalf("without a session: %d %+v", code, env.Error)
	}
	d.signIn()
	d.srv.WorkerFn = func() ([]string, bool, string) { return []string{"proxy"}, true, "fake transcriber" }
	code, env = d.do("GET", "/api/status", nil, false)
	var st Status
	if err := json.Unmarshal(env.Data, &st); code != 200 || err != nil || !st.DBOK || st.Installation != "api-test-machine" || st.Workspace != "API test" || st.User != "Tester" ||
		!st.WorkerOn || st.Transcriber != "fake transcriber" || len(st.WorkerTypes) != 1 || st.MediaStore == "" {
		t.Fatalf("status %d %+v %v", code, st, err)
	}
	code, env = d.do("GET", "/api/ops", nil, false)
	var opsList []OpInfo
	if err := json.Unmarshal(env.Data, &opsList); code != 200 || err != nil || len(opsList) < 50 {
		t.Fatalf("ops %d %d %v", code, len(opsList), err)
	}
	seen := map[string]OpInfo{}
	for _, o := range opsList {
		seen[o.Name] = o
	}
	if o, ok := seen["trash.empty"]; !ok || !o.HumanOnly || !o.Mutating {
		t.Fatalf("an op's flags: %+v", o)
	}
	// A build without its dashboard says how to get one, not a broken app; a
	// build with it (scripts/check.sh builds the dashboard first) serves the
	// app's page. Either way with the security headers.
	want := "built without its dashboard"
	if web.Built() {
		want = `id="root"`
	}
	code, h, b := d.send("GET", "/p/anything", nil, nil, false)
	if code != 200 || !strings.HasPrefix(h.Get("Content-Type"), "text/html") || !strings.Contains(string(b), want) ||
		h.Get("X-Frame-Options") != "DENY" || h.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("the bare page: %d %v %s", code, h, b)
	}
}

// Live events reach every signed-in tab: what changes in the database
// (through change streams), what happens in this process, and — where change
// streams aren't available — what polling finds.
func TestLiveEventsReachTheDashboard(t *testing.T) {
	d, projectID, _ := newDash(t, noClaude)
	d.signIn()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go d.srv.Broker().Run(ctx)
	end := time.Now().Add(20 * time.Second)
	for d.srv.Broker().Mode == "" && time.Now().Before(end) {
		time.Sleep(20 * time.Millisecond)
	}
	if d.srv.Broker().Mode != "changestream" {
		t.Fatalf("mode %q: live updates are change streams, which need MongoDB as a replica set (scripts/dev-mongo.sh; CI starts one)", d.srv.Broker().Mode)
	}
	sawHello := false
	go func() {
		// Changes while the tab listens: a project renamed (database), an upload (this process).
		time.Sleep(300 * time.Millisecond)
		_, _ = d.a.DB.C(store.CProjects).UpdateOne(context.Background(), bson.M{"_id": projectID}, bson.M{"$set": bson.M{"name": "Renamed", "updatedAt": time.Now().UTC()}})
		d.srv.Broker().Local(Event{Type: "upload", ID: "upl_test"})
	}()
	got := map[string]bool{}
	readSSE(t, d, "/api/events", "", func(m sseMsg) bool {
		if m.event == "hello" {
			sawHello = strings.Contains(string(m.data), "changestream")
			return false
		}
		var e Event
		_ = json.Unmarshal(m.data, &e)
		got[e.Type+":"+e.ID] = true
		return got["projects:"+projectID] && got["upload:upl_test"]
	})
	if !sawHello {
		t.Fatal("no hello")
	}
	// Polling finds recent changes too.
	ch := make(chan Event, 256)
	b := d.srv.Broker()
	b.mu.Lock()
	b.subs[ch] = true
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		delete(b.subs, ch)
		b.mu.Unlock()
	}()
	_, _ = d.a.DB.C(store.CProjects).UpdateOne(context.Background(), bson.M{"_id": projectID}, bson.M{"$set": bson.M{"updatedAt": time.Now().UTC()}})
	pctx, pcancel := context.WithTimeout(ctx, 3500*time.Millisecond)
	defer pcancel()
	b.poll(pctx, 3500*time.Millisecond)
	found := false
	for len(ch) > 0 {
		if e := <-ch; e.Type == store.CProjects && e.ID == projectID && e.Op == "update" {
			found = true
		}
	}
	if !found {
		t.Fatal("polling didn't find the project's change")
	}
}

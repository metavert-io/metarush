package videogen_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/testutil/fakevertex"
	"github.com/metavert-io/metarush/internal/videogen"
)

func needFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("needs ffmpeg")
	}
}

func veoRequest(model, task string, seconds int, res string) *videogen.Request {
	return &videogen.Request{Model: model, Task: task, Prompt: "A red kite over a green hill at dawn.", Seconds: seconds, AspectRatio: "16:9", Resolution: res}
}

// A shot goes through Veo's long-running request: started, asked about until
// it is done, the video in the answer itself (no bucket), the token minted
// once from the service account's key and kept while it is good.
func TestVeoLifecycle(t *testing.T) {
	needFFmpeg(t)
	f, key := fakevertex.Start(t)
	v := videogen.Vertex{}
	ctx := context.Background()
	kept, err := providers.NormalizeVertexKey(key)
	if err != nil {
		t.Fatal(err)
	}
	req := veoRequest("veo-3.1-fast-generate-001", videogen.TaskTextToVideo, 8, "720p")
	m, _ := v.Capabilities(req.Model)
	if err := m.Check(req, 0, time.Now()); err != nil {
		t.Fatal(err)
	}
	tk, err := v.Submit(ctx, kept, req)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(tk.ID, "/models/veo-3.1-fast-generate-001/operations/") {
		t.Fatalf("ticket %+v", tk)
	}
	st, err := v.Poll(ctx, kept, tk)
	if err != nil || st.State != videogen.StateRunning {
		t.Fatalf("first check %v %v", st.State, err)
	}
	st, err = v.Poll(ctx, kept, tk)
	if err != nil || st.State != videogen.StateDone || len(st.Outputs) != 1 {
		t.Fatalf("second check %v, %d outputs, %v", st.State, len(st.Outputs), err)
	}
	var b bytes.Buffer
	if n, err := v.Fetch(ctx, kept, tk, &st.Outputs[0], &b); err != nil || n == 0 || !bytes.Contains(b.Bytes()[:16], []byte("ftyp")) {
		t.Fatalf("fetch %d %v", n, err)
	}
	if err := v.Forget(ctx, kept, tk, st.Outputs); err != nil {
		t.Errorf("forget: %v", err)
	}
	r := f.Requests()[0]
	if r.Parameters["durationSeconds"] != 8.0 || r.Parameters["generateAudio"] != true || r.Parameters["resolution"] != "720p" || r.Parameters["aspectRatio"] != "16:9" {
		t.Errorf("parameters %v", r.Parameters)
	}
	if f.Calls("token") != 1 {
		t.Errorf("signed in %d times for one video", f.Calls("token"))
	}
	if ps := f.Problems(); len(ps) > 0 {
		t.Errorf("the fake saw: %v", ps)
	}
}

// Pictures go in the places Veo reads them: a start frame, the first and last
// frames, or references of what should appear.
func TestVeoPictures(t *testing.T) {
	f, key := fakevertex.Start(t)
	dir := t.TempDir()
	pic := filepath.Join(dir, "frame.jpg")
	if err := os.WriteFile(pic, []byte("\xff\xd8\xff\xe0 a picture"), 0o600); err != nil {
		t.Fatal(err)
	}
	img := videogen.Media{Role: "image", MIME: "image/jpeg", Path: pic}
	ctx := context.Background()
	for _, c := range []struct {
		task  string
		imgs  int
		field string
	}{{videogen.TaskImageToVideo, 1, "image"}, {videogen.TaskInterpolate, 2, "lastFrame"}, {videogen.TaskReferenceToVideo, 2, "referenceImages"}} {
		req := veoRequest("veo-3.1-generate-001", c.task, 8, "1080p")
		for i := 0; i < c.imgs; i++ {
			req.Images = append(req.Images, img)
		}
		if _, err := (videogen.Vertex{}).Submit(ctx, key, req); err != nil {
			t.Fatalf("%s: %v", c.task, err)
		}
		rs := f.Requests()
		in := rs[len(rs)-1].Instance
		if in[c.field] == nil {
			t.Errorf("%s: no %s in %v", c.task, c.field, in)
		}
		if c.task == videogen.TaskReferenceToVideo {
			b, _ := json.Marshal(in["referenceImages"])
			if !strings.Contains(string(b), `"referenceType":"asset"`) {
				t.Errorf("references %s", b)
			}
		}
	}
	req := veoRequest("veo-3.1-generate-001", videogen.TaskReferenceToVideo, 6, "1080p")
	req.Images = []videogen.Media{img}
	var ve *videogen.Error
	if _, err := (videogen.Vertex{}).Submit(ctx, key, req); !errors.As(err, &ve) || ve.Kind != videogen.KindInvalid {
		t.Errorf("references for 6 seconds: %v", err)
	}
}

// Google Cloud's refusals say what the project's owner does, and nothing is
// retried that can't succeed by waiting.
func TestVeoRefusals(t *testing.T) {
	needFFmpeg(t)
	f, key := fakevertex.Start(t)
	ctx := context.Background()
	cases := []struct {
		kind, want string
		is         string
	}{
		{fakevertex.Disabled, videogen.KindPermission, "isn't turned on in the Google Cloud project “garden-films-01”"},
		{fakevertex.Denied, videogen.KindPermission, "Vertex AI User role"},
		{fakevertex.Quota, videogen.KindRate, "quota"},
		{fakevertex.Blocked, videogen.KindSafety, "safety filters"},
		{fakevertex.ServerError, videogen.KindServer, "problem on its side"},
	}
	for _, c := range cases {
		f.Script(fakevertex.Behavior{Kind: c.kind})
		_, err := (videogen.Vertex{}).Submit(ctx, key, veoRequest("veo-3.1-fast-generate-001", videogen.TaskTextToVideo, 4, "720p"))
		var ve *videogen.Error
		if !errors.As(err, &ve) || ve.Kind != c.want || !strings.Contains(ve.Message+" "+ve.Hint, c.is) {
			t.Errorf("%s: %v (%+v)", c.kind, err, ve)
		}
	}
	// Veo made it, but the filters held it back: failed, nothing to keep.
	f.Script(fakevertex.Behavior{Kind: fakevertex.Safety})
	tk, err := (videogen.Vertex{}).Submit(ctx, key, veoRequest("veo-3.1-fast-generate-001", videogen.TaskTextToVideo, 4, "720p"))
	if err != nil {
		t.Fatal(err)
	}
	f.ReadyAfter = 0
	st, err := (videogen.Vertex{}).Poll(ctx, key, tk)
	if err != nil || st.State != videogen.StateFailed || st.Err == nil || st.Err.Kind != videogen.KindSafety || st.Safety == nil || st.Safety.FilteredCount != 1 {
		t.Fatalf("held back %v %+v %+v %v", st.State, st.Err, st.Safety, err)
	}
	// A key the token endpoint doesn't know (another fake, another account):
	// Google Cloud refuses the sign-in.
	fakevertex.Start(t)
	_, err = (videogen.Vertex{}).Submit(ctx, key, veoRequest("veo-3.1-fast-generate-001", videogen.TaskTextToVideo, 4, "720p"))
	var ve *videogen.Error
	if !errors.As(err, &ve) || ve.Kind != videogen.KindKey {
		t.Errorf("a key the token endpoint doesn't know: %v", err)
	}
}

// Veo is priced by the second at each resolution, with its dated table; only
// 4, 6 or 8 seconds.
func TestVeoPrices(t *testing.T) {
	at := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		model, res string
		secs       int
		usd        float64
	}{{"veo-3.1-fast-generate-001", "1080p", 8, 0.96}, {"veo-3.1-fast-generate-001", "720p", 4, 0.40}, {"veo-3.1-generate-001", "4k", 8, 4.80}} {
		e, err := (videogen.Vertex{}).Estimate(veoRequest(c.model, videogen.TaskTextToVideo, c.secs, c.res), at)
		if err != nil || e.USD != c.usd || e.PriceTable != "2026-10-25" {
			t.Errorf("%+v: %+v %v", c, e, err)
		}
	}
	m, _ := (videogen.Vertex{}).Capabilities("veo-3.1-fast-generate-001")
	if err := m.Check(veoRequest(m.ID, videogen.TaskTextToVideo, 5, "720p"), 0, at); err == nil || !strings.Contains(err.Error(), "4, 6 or 8 seconds") {
		t.Errorf("5 seconds: %v", err)
	}
	if err := m.Check(veoRequest(m.ID, videogen.TaskTextToVideo, 8, "4k"), 0, at); err == nil {
		t.Error("Veo 3.1 Fast at 4K")
	}
}

// The key MetaRush keeps is the service account's essentials on one line;
// one that sends its sign-in anywhere but Google is refused; settings name
// the account, never the key; tests can never reach Google Cloud.
func TestVertexKeys(t *testing.T) {
	_, key := fakevertex.Start(t)
	kept, err := providers.ShapeKey(providers.VertexName, key)
	if err != nil || strings.ContainsAny(kept, "\r\n") || len(kept) > 3000 {
		t.Fatalf("kept %d characters: %v", len(kept), err)
	}
	if strings.Contains(kept, "client_id") {
		t.Error("kept more than it needs")
	}
	if got := providers.MaskFor(providers.VertexName, kept); !strings.Contains(got, "metarush-veo@garden-films-01.iam.gserviceaccount.com") || strings.Contains(got, "PRIVATE") {
		t.Errorf("masked %q", got)
	}
	var k map[string]any
	_ = json.Unmarshal([]byte(key), &k)
	k["token_uri"] = "https://example.com/token"
	b, _ := json.Marshal(k)
	if _, err := providers.ShapeKey(providers.VertexName, string(b)); err == nil || !strings.Contains(err.Error(), "somewhere other than Google") {
		t.Errorf("another token endpoint: %v", err)
	}
	if _, err := providers.ShapeKey(providers.VertexName, `{"type":"authorized_user"}`); err == nil {
		t.Error("a person's own sign-in taken as a service account")
	}
	t.Setenv(providers.EnvVertexBaseURL, "")
	if _, err := providers.VertexBaseURL("us-central1"); err == nil || !strings.Contains(err.Error(), "Tests never call Google Cloud") {
		t.Errorf("the real host under go test: %v", err)
	}
	t.Setenv(providers.EnvVertexTokenURL, "")
	if _, err := providers.VertexTokenURL(); err == nil {
		t.Error("the real token endpoint under go test")
	}
}

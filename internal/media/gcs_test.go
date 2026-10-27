package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/fsouza/fake-gcs-server/fakestorage"

	"github.com/metavert-io/metarush/internal/testutil/gcsemu"
)

// The GCS driver is tested against an in-process emulator (internal/testutil/
// gcsemu: fake-gcs-server plus a layer that implements the documented
// resumable-session protocol, which fake-gcs-server alone does not). No GCP
// project or account is used, so nothing here proves behavior against the real
// service. TestGCSEmulatorDirect runs the non-interrupted paths against the
// unmodified fake-gcs-server as a cross-check.

func testSAKey(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, _ := x509.MarshalPKCS8PrivateKey(k)
	pk := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	b, _ := json.Marshal(map[string]string{"type": "service_account", "project_id": "test-project", "private_key_id": "k1", "private_key": string(pk),
		"client_email": "metarush-test@test-project.iam.gserviceaccount.com", "client_id": "1", "token_uri": "https://oauth2.googleapis.com/token"})
	p := filepath.Join(t.TempDir(), "sa.json")
	if err := os.WriteFile(p, b, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func newTestGCS(t *testing.T, endpoint string, extra func(*GCSConfig)) *GCS {
	t.Helper()
	cfg := GCSConfig{Bucket: "media", Prefix: "team", Endpoint: endpoint, NoAuth: true, ChunkSize: 8 << 20, RetryBackoff: time.Millisecond}
	if extra != nil {
		extra(&cfg)
	}
	g, err := NewGCS(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

func TestGCSStore(t *testing.T) {
	emu := gcsemu.Start(t, "media")
	st := newTestGCS(t, emu.URL, nil)
	keys := exercise(t, st)
	if emu.Stats().StatusQueries == 0 {
		t.Fatal("resume did not ask GCS for the committed offset")
	}
	// The 20 MiB object was sent as 8 MiB chunks: the interrupted attempt sent one
	// chunk, the resumed one only the remaining two (no restart from zero).
	if want := int64(20<<20 + 12345); emu.Stats().ChunkBytes != want {
		t.Fatalf("sent %d chunk bytes for a %d-byte object (an upload restarted?)", emu.Stats().ChunkBytes, want)
	}
	if st.Locator() != "gs://media/team@"+emu.Host || st.Name() != "gcs" {
		t.Fatalf("locator %s", st.Locator())
	}
	if c := st.Capabilities(); c.PresignedGet || !c.RangeRead || !c.ResumableUpload || !c.Shared {
		t.Fatalf("capabilities without signing credentials: %+v", c)
	}
	if _, err := st.PresignGet(context.Background(), keys[0], time.Minute, "", ""); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("presign without credentials: %v", err)
	}
	if inc, err := st.ListIncomplete(context.Background()); err != nil || len(inc) != 0 {
		t.Fatalf("ListIncomplete must be empty for GCS: %v %v", inc, err)
	}
	if err := st.Delete(context.Background(), keys[0]); err != nil {
		t.Fatal(err)
	}
	if _, err := st.Stat(context.Background(), keys[0]); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted object still visible: %v", err)
	}
	if err := st.Delete(context.Background(), keys[0]); err != nil {
		t.Fatalf("deleting a missing object must be a no-op: %v", err)
	}
	if _, _, err := st.Get(context.Background(), keys[0], 0, -1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get missing: %v", err)
	}
}

// A process restart: a brand-new driver resumes from the persisted Resume
// (session URI + offset) only.
func TestGCSResumeAcrossRestart(t *testing.T) {
	emu := gcsemu.Start(t, "media")
	path, want := writeRandom(t, 3*(1<<20)+777)
	sha, _ := HashFile(path)
	key := ObjectKey("ws_test", sha)
	small := func(c *GCSConfig) { c.ChunkSize = 1 << 20 }
	first := newTestGCS(t, emu.URL, small)
	ctx, cancel := context.WithCancel(context.Background())
	var saved []byte
	_, err := first.PutFile(ctx, key, path, PutOptions{SHA256: sha}, Resume{}, func(done, total int64, r Resume) {
		saved, _ = json.Marshal(r) // what the ingest session stores
		if done >= 2<<20 {
			cancel()
		}
	})
	if err == nil {
		t.Fatal("expected the interrupted upload to fail")
	}
	var r Resume
	_ = json.Unmarshal(saved, &r)
	if r.SessionURI == "" || r.Offset != 2<<20 {
		t.Fatalf("persisted resume state %+v", r)
	}
	second := newTestGCS(t, emu.URL, small) // "after the restart"
	var firstDone int64 = -1
	if _, err := second.PutFile(context.Background(), key, path, PutOptions{SHA256: sha}, r, func(done, total int64, _ Resume) {
		if firstDone < 0 {
			firstDone = done
		}
	}); err != nil {
		t.Fatal(err)
	}
	if firstDone != 2<<20 || emu.Stats().Initiations != 1 {
		t.Fatalf("resumed from %d with %d sessions; want 2 MiB and one session", firstDone, emu.Stats().Initiations)
	}
	rc, _, _ := second.Get(context.Background(), key, 0, -1)
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, want) {
		t.Fatal("content differs after resume")
	}
}

// Sessions expire after a week: the driver notices and starts a new one.
func TestGCSExpiredSessionStartsOver(t *testing.T) {
	emu := gcsemu.Start(t, "media")
	st := newTestGCS(t, emu.URL, func(c *GCSConfig) { c.ChunkSize = 1 << 20 })
	path, _ := writeRandom(t, 3<<20)
	sha, _ := HashFile(path)
	key := ObjectKey("ws_test", sha)
	ctx, cancel := context.WithCancel(context.Background())
	var saved Resume
	_, _ = st.PutFile(ctx, key, path, PutOptions{SHA256: sha}, Resume{}, func(done, total int64, r Resume) {
		saved = r
		if done > 0 {
			cancel()
		}
	})
	emu.Expire(saved.SessionURI)
	var firstDone int64 = -1
	if _, err := st.PutFile(context.Background(), key, path, PutOptions{SHA256: sha}, saved, func(done, total int64, _ Resume) {
		if firstDone < 0 {
			firstDone = done
		}
	}); err != nil {
		t.Fatal(err)
	}
	if firstDone != 0 || emu.Stats().Initiations != 2 {
		t.Fatalf("expired session: restarted from %d with %d sessions", firstDone, emu.Stats().Initiations)
	}
}

// GCS may persist fewer bytes than a chunk carried, and transient 503s happen:
// the driver continues from what GCS reports, never from what it sent.
func TestGCSPartialCommitAndTransientErrors(t *testing.T) {
	emu := gcsemu.Start(t, "media")
	st := newTestGCS(t, emu.URL, func(c *GCSConfig) { c.ChunkSize = 1 << 20 })
	path, want := writeRandom(t, 4<<20+99)
	sha, _ := HashFile(path)
	key := ObjectKey("ws_test", sha)
	emu.ShortCommit(300 << 10)
	calls := 0
	if _, err := st.PutFile(context.Background(), key, path, PutOptions{SHA256: sha}, Resume{}, func(done, total int64, r Resume) {
		calls++
		if calls == 3 {
			emu.FailNext(2)
		}
	}); err != nil {
		t.Fatal(err)
	}
	rc, _, _ := st.Get(context.Background(), key, 0, -1)
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, want) {
		t.Fatal("content differs after a short commit / retries")
	}
	if emu.Stats().StatusQueries == 0 {
		t.Fatal("transient errors must be followed by a status query")
	}
}

func TestGCSIntegrity(t *testing.T) {
	emu := gcsemu.Start(t, "media")
	st := newTestGCS(t, emu.URL, func(c *GCSConfig) { c.ChunkSize = 1 << 20 })
	path, _ := writeRandom(t, 2<<20+5)
	sha, _ := HashFile(path)
	emu.CorruptCRC(true)
	if _, err := st.PutFile(context.Background(), ObjectKey("ws_test", sha), path, PutOptions{SHA256: sha}, Resume{}, nil); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("a stored object whose crc32c differs must be rejected, got %v", err)
	}
	// A session URI pointing at another host is never used (credentials stay home).
	other := "https://evil.example/upload/storage/v1/b/media/o?uploadType=resumable&upload_id=x"
	if st.ownSession(other) || st.AbortIncomplete(context.Background(), Incomplete{UploadID: other}) == nil {
		t.Fatal("foreign session URI accepted")
	}
	g := &GCS{}
	if !g.ownSession("https://storage.googleapis.com/upload/storage/v1/b/b/o?uploadType=resumable&upload_id=abc") || g.ownSession("http://storage.googleapis.com/upload/storage/v1/b/b/o?upload_id=abc") {
		t.Fatal("default endpoint session check")
	}
}

func TestGCSAbortSession(t *testing.T) {
	emu := gcsemu.Start(t, "media")
	st := newTestGCS(t, emu.URL, func(c *GCSConfig) { c.ChunkSize = 1 << 20 })
	path, _ := writeRandom(t, 2<<20+5)
	sha, _ := HashFile(path)
	ctx, cancel := context.WithCancel(context.Background())
	var saved Resume
	_, _ = st.PutFile(ctx, ObjectKey("ws_test", sha), path, PutOptions{}, Resume{}, func(done, total int64, r Resume) {
		saved = r
		if done > 0 {
			cancel()
		}
	})
	if err := st.AbortIncomplete(context.Background(), Incomplete{Key: "k", UploadID: saved.SessionURI}); err != nil {
		t.Fatal(err)
	}
	if emu.Stats().Open != 0 {
		t.Fatal("session not canceled")
	}
}

func TestGCSSignedURL(t *testing.T) {
	emu := gcsemu.Start(t, "media")
	sa := testSAKey(t)
	st := newTestGCS(t, emu.URL, func(c *GCSConfig) { c.CredentialsFile = sa })
	if !st.Capabilities().PresignedGet || !strings.Contains(st.Auth(), "metarush-test@") {
		t.Fatalf("a service-account key must enable signed URLs (%s)", st.Auth())
	}
	path, want := writeRandom(t, 4096)
	sha, _ := HashFile(path)
	key := ObjectKey("ws_test", sha)
	if _, err := st.PutFile(context.Background(), key, path, PutOptions{SHA256: sha, ContentType: "video/mp4"}, Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	u, err := st.PresignGet(context.Background(), key, time.Minute, "video/mp4", "clip.mp4")
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	qv := pu.Query()
	if qv.Get("X-Goog-Algorithm") != "GOOG4-RSA-SHA256" || !strings.HasPrefix(qv.Get("X-Goog-Credential"), "metarush-test@") || qv.Get("X-Goog-Signature") == "" || pu.Host != emu.Host {
		t.Fatalf("not a V4 signed URL for the store: %s", u)
	}
	// The emulator does not verify signatures; this checks the URL addresses the
	// object and supports Range (what the dashboard's video element does).
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Range", "bytes=100-199")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusPartialContent || !bytes.Equal(body, want[100:200]) {
		t.Fatalf("signed range read: status %d, %d bytes", res.StatusCode, len(body))
	}
}

func TestGCSCredentialsFileValidation(t *testing.T) {
	p := filepath.Join(t.TempDir(), "user.json")
	_ = os.WriteFile(p, []byte(`{"type":"authorized_user","client_id":"x","refresh_token":"y"}`), 0o600)
	if _, err := NewGCS(GCSConfig{Bucket: "b", CredentialsFile: p, NoAuth: true, Endpoint: "http://127.0.0.1:1"}); err == nil || !strings.Contains(err.Error(), "service-account key") {
		t.Fatalf("user credentials must be refused as credentialsFile: %v", err)
	}
	if _, err := NewGCS(GCSConfig{Bucket: "b", CredentialsFile: filepath.Join(t.TempDir(), "missing.json")}); err == nil {
		t.Fatal("missing credentials file accepted")
	}
	if _, err := NewGCS(GCSConfig{}); err == nil {
		t.Fatal("bucket required")
	}
}

// TestGCSEmulatorDirect runs the paths that do not need a resumed session
// against the unmodified fake-gcs-server: multi-chunk resumable upload with
// declared checksums (the emulator verifies them), single-request upload,
// ranged reads, no-op re-upload, not-found and traversal.
func TestGCSEmulatorDirect(t *testing.T) {
	fake, err := fakestorage.NewServerWithOptions(fakestorage.Options{NoListener: true, PublicHost: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	fake.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: "media"})
	srv := httptest.NewServer(fake.HTTPHandler())
	t.Cleanup(srv.Close)
	st := newTestGCS(t, srv.URL, func(c *GCSConfig) { c.ChunkSize = 1 << 20 })
	ctx := context.Background()
	for _, size := range []int{3<<20 + 4321, 70000} {
		path, want := writeRandom(t, size)
		sha, _ := HashFile(path)
		key := ObjectKey("ws_test", sha)
		var last int64
		info, err := st.PutFile(ctx, key, path, PutOptions{SHA256: sha, ContentType: "video/mp4"}, Resume{}, func(done, total int64, r Resume) { last = done })
		if err != nil {
			t.Fatalf("%d bytes: %v", size, err)
		}
		if info.Size != int64(size) || last != int64(size) {
			t.Fatalf("size %d / progress %d, want %d", info.Size, last, size)
		}
		obj, err := fake.GetObject("media", "team/"+key)
		if err != nil || obj.Metadata["sha256"] != sha || obj.ContentType != "video/mp4" {
			t.Fatalf("stored object attrs: %+v %v", obj.ObjectAttrs, err)
		}
		rc, _, err := st.Get(ctx, key, 1000, 500)
		if err != nil {
			t.Fatal(err)
		}
		got, _ := io.ReadAll(rc)
		rc.Close()
		if !bytes.Equal(got, want[1000:1500]) {
			t.Fatalf("%d bytes: ranged read mismatch", size)
		}
		if _, err := st.PutFile(ctx, key, path, PutOptions{SHA256: sha}, Resume{}, nil); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.Stat(ctx, "ws/x/objects/aa/"+strings.Repeat("a", 64)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected not found, got %v", err)
	}
	if _, err := st.Stat(ctx, "../etc/passwd"); err == nil {
		t.Fatal("traversal key accepted")
	}
}

// A signed upload on GCS signs its type and the most it may hold
// (x-goog-content-length-range), and copies stay inside the bucket (D313).
// The emulator checks neither signature nor range: this checks the link.
func TestGCSSignedUploadAndCopy(t *testing.T) {
	emu := gcsemu.Start(t, "media")
	st := newTestGCS(t, emu.URL, func(c *GCSConfig) { c.CredentialsFile = testSAKey(t) })
	u, h, err := st.PresignPut(context.Background(), "ws/w/review/k/c0.json", time.Hour, "application/json", 65536)
	if err != nil {
		t.Fatal(err)
	}
	pu, _ := url.Parse(u)
	signed := pu.Query().Get("X-Goog-SignedHeaders")
	if !strings.Contains(signed, "x-goog-content-length-range") || !strings.Contains(signed, "content-type") || h["X-Goog-Content-Length-Range"] != "65536,65536" || h["Content-Type"] != "application/json" {
		t.Fatalf("signed headers %q, browser headers %v", signed, h)
	}
	if _, _, err := st.PresignPut(context.Background(), "ws/w/x", 8*24*time.Hour, "application/json", 10); err == nil {
		t.Fatal("a store signs a week at most")
	}
	path, _ := writeRandom(t, 2048)
	if _, err := st.PutFile(context.Background(), "ws/w/a", path, PutOptions{ContentType: "video/mp4"}, Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	if err := st.Copy(context.Background(), "ws/w/a", "ws/w/review/k/video.mp4"); err != nil {
		t.Fatal(err)
	}
	if info, err := st.Stat(context.Background(), "ws/w/review/k/video.mp4"); err != nil || info.Size != 2048 {
		t.Fatalf("copy: %+v %v", info, err)
	}
	unsigned := newTestGCS(t, emu.URL, nil)
	if _, _, err := unsigned.PresignPut(context.Background(), "ws/w/x", time.Hour, "application/json", 10); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("no key that signs: %v", err)
	}
}

// Signed links name the bucket in the host, so a review page has an address
// of its own bucket rather than one every Google Cloud customer's links share
// (D316). Signed here with a made-up key; nothing is sent anywhere.
func TestGCSLinksNameTheBucketInTheHost(t *testing.T) {
	sa := testSAKey(t)
	st, err := NewGCS(GCSConfig{Bucket: "media", Prefix: "team", CredentialsFile: sa})
	if err != nil {
		t.Fatal(err)
	}
	for _, sign := range []func() (string, error){
		func() (string, error) {
			return st.PresignGet(context.Background(), "review/k/index.html", time.Hour, "text/html", "")
		},
		func() (string, error) {
			u, _, err := st.PresignPut(context.Background(), "review/k/c0.json", time.Hour, "application/json", 10)
			return u, err
		},
	} {
		u, err := sign()
		if err != nil {
			t.Fatal(err)
		}
		pu, _ := url.Parse(u)
		if pu.Host != "media.storage.googleapis.com" || !strings.HasPrefix(pu.Path, "/team/review/k/") {
			t.Fatalf("a link on a shared address: %s%s", pu.Host, pu.Path)
		}
	}
	dotted, err := NewGCS(GCSConfig{Bucket: "media.example.com", CredentialsFile: sa})
	if err != nil {
		t.Fatal(err)
	}
	u, _ := dotted.PresignGet(context.Background(), "review/k/index.html", time.Hour, "", "")
	if pu, _ := url.Parse(u); pu.Host != "storage.googleapis.com" {
		t.Fatalf("a bucket with dots: %s", pu.Host)
	}
}

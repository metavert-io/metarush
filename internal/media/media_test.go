package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

func writeRandom(t *testing.T, size int) (string, []byte) {
	t.Helper()
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "src.bin")
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p, b
}

func newFakeS3(t *testing.T) *S3 {
	t.Helper()
	backend := s3mem.New()
	faker := gofakes3.New(backend)
	srv := httptest.NewServer(faker.Server())
	t.Cleanup(srv.Close)
	if err := backend.CreateBucket("media"); err != nil {
		t.Fatal(err)
	}
	s, err := NewS3(S3Config{Endpoint: srv.URL, Region: "us-east-1", Bucket: "media", Prefix: "team", AccessKeyID: "test", SecretAccessKey: "test", ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func exercise(t *testing.T, st Store) []string {
	ctx := context.Background()
	path, want := writeRandom(t, 20<<20+12345) // multipart for S3
	sha, _ := HashFile(path)
	key := ObjectKey("ws_test", sha)

	// Interrupt the upload after the first progress callback, then resume.
	cctx, cancel := context.WithCancel(ctx)
	var saved Resume
	calls := 0
	_, err := st.PutFile(cctx, key, path, PutOptions{SHA256: sha, ContentType: "application/octet-stream"}, Resume{}, func(done, total int64, r Resume) {
		saved = r
		calls++
		if done > 0 && done < total {
			cancel()
		}
	})
	if err == nil {
		t.Fatalf("%s: expected interrupted upload to fail", st.Name())
	}
	if _, err := st.Stat(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%s: partial upload must not be visible, got %v", st.Name(), err)
	}
	var resumedFrom int64 = -1
	info, err := st.PutFile(ctx, key, path, PutOptions{SHA256: sha}, saved, func(done, total int64, r Resume) {
		if resumedFrom < 0 {
			resumedFrom = done
		}
	})
	if err != nil {
		t.Fatalf("%s: resume failed: %v", st.Name(), err)
	}
	if info.Size != int64(len(want)) {
		t.Fatalf("%s: size %d != %d", st.Name(), info.Size, len(want))
	}
	if resumedFrom <= 0 {
		t.Errorf("%s: upload restarted from zero instead of resuming", st.Name())
	}

	// Ranged read (what video seeking does).
	rc, _, err := st.Get(ctx, key, 5<<20, 1000)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, want[5<<20:5<<20+1000]) {
		t.Fatalf("%s: ranged read mismatch", st.Name())
	}
	// Re-upload of the same content is a no-op.
	if _, err := st.PutFile(ctx, key, path, PutOptions{SHA256: sha}, Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	// Missing object and traversal.
	missing := sha[:63] + map[bool]string{true: "1", false: "0"}[sha[63] == '0'] // always differs from sha
	if _, err := st.Stat(ctx, ObjectKey("ws_test", missing)); !errors.Is(err, ErrNotFound) {
		t.Fatalf("%s: expected not found, got %v", st.Name(), err)
	}
	if _, err := st.Stat(ctx, "../etc/passwd"); err == nil {
		t.Fatalf("%s: traversal key accepted", st.Name())
	}
	return []string{key}
}

// TestS3Live runs the same checks against a real S3-compatible bucket (e.g. Tigris)
// when METARUSH_TEST_S3_BUCKET/ENDPOINT/ACCESS_KEY_ID/SECRET_ACCESS_KEY are set.
// Everything is written under a unique prefix and deleted afterwards.
func TestS3Live(t *testing.T) {
	bucket := os.Getenv("METARUSH_TEST_S3_BUCKET")
	if bucket == "" {
		t.Skip("set METARUSH_TEST_S3_* to run against a real bucket")
	}
	prefix := "ci-test/" + time.Now().UTC().Format("20060102T150405")
	st, err := NewS3(S3Config{Endpoint: os.Getenv("METARUSH_TEST_S3_ENDPOINT"), Region: os.Getenv("METARUSH_TEST_S3_REGION"), Bucket: bucket, Prefix: prefix,
		AccessKeyID: os.Getenv("METARUSH_TEST_S3_ACCESS_KEY_ID"), SecretAccessKey: os.Getenv("METARUSH_TEST_S3_SECRET_ACCESS_KEY")})
	if err != nil {
		t.Fatal(err)
	}
	keys := exercise(t, st)
	ctx := context.Background()
	for _, k := range keys {
		if err := st.Delete(ctx, k); err != nil {
			t.Errorf("cleanup %s: %v", k, err)
		}
	}
	inc, _ := st.ListIncomplete(ctx)
	for _, u := range inc {
		_ = st.AbortIncomplete(ctx, u)
	}
	t.Logf("live bucket %s/%s: resumable multipart, ranged reads, presign and cleanup OK", bucket, prefix)
}

func TestFilesystemStore(t *testing.T) {
	st, err := NewFS(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	_ = exercise(t, st)
}

func TestFilesystemIntegrity(t *testing.T) {
	st, _ := NewFS(t.TempDir(), false)
	path, _ := writeRandom(t, 1000)
	if _, err := st.PutFile(context.Background(), "ws/x/objects/aa/aaaa", path, PutOptions{SHA256: "deadbeef"}, Resume{}, nil); err == nil {
		t.Fatal("hash mismatch must be rejected")
	}
}

func TestS3Store(t *testing.T) {
	st := newFakeS3(t)
	_ = exercise(t, st)
	// Presigned GET works with HTTP Range against the store directly.
	path, want := writeRandom(t, 4096)
	sha, _ := HashFile(path)
	key := ObjectKey("ws_test", sha)
	if _, err := st.PutFile(context.Background(), key, path, PutOptions{SHA256: sha, ContentType: "video/mp4"}, Resume{}, nil); err != nil {
		t.Fatal(err)
	}
	u, err := st.PresignGet(context.Background(), key, time.Minute, "video/mp4", "")
	if err != nil {
		t.Fatal(err)
	}
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("Range", "bytes=100-199")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if res.StatusCode != http.StatusPartialContent || !bytes.Equal(body, want[100:200]) {
		t.Fatalf("presigned range read: status %d, %d bytes", res.StatusCode, len(body))
	}
	if loc := st.Locator(); loc == "" || bytes.Contains([]byte(loc), []byte("test")) && bytes.Contains([]byte(loc), []byte("secret")) {
		t.Fatalf("locator leaks secrets: %s", loc)
	}
}

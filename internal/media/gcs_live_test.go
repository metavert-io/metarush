package media

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/testutil/gcsemu"
)

// TestGCSLive runs the store checks against a real Google Cloud Storage bucket
// (the GCS driver is otherwise tested against emulators only). It runs only
// when METARUSH_TEST_GCS_BUCKET is set, and needs:
//
//	METARUSH_TEST_GCS_BUCKET       the bucket (it must exist)
//	METARUSH_TEST_GCS_CREDENTIALS  a service-account JSON key file (optional:
//	                               empty means Application Default Credentials)
//	METARUSH_TEST_GCS_PREFIX       where it writes (default ci-test)
//
// The account needs roles/storage.objectUser on the bucket, best limited to
// the prefix with an IAM condition (docs/testing.md has the exact commands). Everything goes under <prefix>/<timestamp>-<random>/ and is
// deleted at the end, also when a check fails.
func TestGCSLive(t *testing.T) {
	bucket := os.Getenv("METARUSH_TEST_GCS_BUCKET")
	if bucket == "" {
		t.Skip("set METARUSH_TEST_GCS_BUCKET (and METARUSH_TEST_GCS_CREDENTIALS, or Application Default Credentials) to run against a real bucket")
	}
	if os.Getenv("STORAGE_EMULATOR_HOST") != "" {
		t.Skip("STORAGE_EMULATOR_HOST is set: TestGCSLive is for the real service")
	}
	base := strings.Trim(os.Getenv("METARUSH_TEST_GCS_PREFIX"), "/")
	if base == "" {
		base = "ci-test"
	}
	tag := make([]byte, 4)
	_, _ = rand.Read(tag)
	prefix := base + "/" + time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(tag)
	cfg := GCSConfig{Bucket: bucket, Prefix: prefix, CredentialsFile: os.Getenv("METARUSH_TEST_GCS_CREDENTIALS"), ChunkSize: 8 << 20}
	st := gcsLiveChecks(t, cfg)
	if st.Locator() != "gs://"+bucket+"/"+prefix {
		t.Errorf("locator %s", st.Locator())
	}
}

// The live checks run against the in-process emulator too, so the harness is
// known to work before anyone points it at a real bucket.
func TestGCSLiveChecksOnTheEmulator(t *testing.T) {
	emu := gcsemu.Start(t, "media")
	gcsLiveChecks(t, GCSConfig{Bucket: "media", Prefix: "ci-test/emulated", Endpoint: emu.URL, NoAuth: true, CredentialsFile: testSAKey(t),
		ChunkSize: 8 << 20, RetryBackoff: time.Millisecond})
}

// gcsLiveChecks: an interrupted resumable upload resumed by the same driver
// and by a new one (a restart), ranged reads, a no-op re-upload, missing
// objects, a canceled session, and a V4 signed URL with a Range header when
// the credentials can sign. It deletes what it wrote.
func gcsLiveChecks(t *testing.T, cfg GCSConfig) *GCS {
	t.Helper()
	st, err := NewGCS(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	var keys []string
	t.Cleanup(func() {
		for _, k := range keys {
			if err := st.Delete(context.Background(), k); err != nil {
				t.Errorf("cleanup %s: %v", k, err)
			}
		}
	})

	// The generic store checks: an interrupted upload resumed, ranged reads,
	// a no-op re-upload, missing objects and traversal.
	keys = append(keys, exercise(t, st)...)

	// A restart: a new driver resumes from what the ingest session stores.
	small := cfg
	small.ChunkSize = 1 << 20
	first, err := NewGCS(small)
	if err != nil {
		t.Fatal(err)
	}
	path, want := writeRandom(t, 3*(1<<20)+777)
	sha, _ := HashFile(path)
	key := ObjectKey("ws_test", sha)
	keys = append(keys, key)
	cctx, cancel := context.WithCancel(ctx)
	var saved []byte
	if _, err := first.PutFile(cctx, key, path, PutOptions{SHA256: sha}, Resume{}, func(done, total int64, r Resume) {
		saved, _ = json.Marshal(r)
		if done >= 1<<20 {
			cancel()
		}
	}); err == nil {
		t.Fatal("expected the interrupted upload to fail")
	}
	cancel()
	var r Resume
	_ = json.Unmarshal(saved, &r)
	if r.SessionURI == "" || r.Offset < 1<<20 {
		t.Fatalf("resume state after the interruption: offset %d, session %v", r.Offset, r.SessionURI != "")
	}
	second, err := NewGCS(small)
	if err != nil {
		t.Fatal(err)
	}
	var from int64 = -1
	if _, err := second.PutFile(ctx, key, path, PutOptions{SHA256: sha}, r, func(done, total int64, _ Resume) {
		if from < 0 {
			from = done
		}
	}); err != nil {
		t.Fatalf("resume after a restart: %v", err)
	}
	if from <= 0 {
		t.Errorf("the restarted upload began at %d instead of where GCS had got to", from)
	}
	rc, _, err := second.Get(ctx, key, 0, -1)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, want) {
		t.Fatal("content differs after the resumed upload")
	}

	// A canceled session (what ingest reconcile does with stale ones).
	apath, _ := writeRandom(t, 2<<20+5)
	asha, _ := HashFile(apath)
	akey := ObjectKey("ws_test", asha)
	actx, acancel := context.WithCancel(ctx)
	var aborted Resume
	_, _ = first.PutFile(actx, akey, apath, PutOptions{}, Resume{}, func(done, total int64, r Resume) {
		aborted = r
		if done > 0 {
			acancel()
		}
	})
	acancel()
	if aborted.SessionURI == "" {
		t.Fatal("no session to cancel")
	}
	if err := first.AbortIncomplete(ctx, Incomplete{Key: akey, UploadID: aborted.SessionURI}); err != nil {
		t.Fatalf("cancel a session: %v", err)
	}
	if _, err := st.Stat(ctx, akey); err == nil {
		keys = append(keys, akey)
		t.Fatal("a canceled upload left an object")
	}

	// A V4 signed URL with a Range header (the dashboard's video element),
	// when the credentials can sign.
	signed := "not checked: these credentials can't sign URLs"
	if st.Capabilities().PresignedGet {
		u, err := st.PresignGet(ctx, key, time.Minute, "application/octet-stream", "")
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
			t.Fatalf("signed range read: status %d, %d bytes", res.StatusCode, len(body))
		}
		signed = "OK"
	}
	t.Logf("GCS %s (%s): resumable upload interrupted and resumed, resumed after a restart from %d bytes, ranged reads, canceled session, signed URL %s; cleaned up",
		st.Locator(), st.Auth(), from, signed)
	return st
}

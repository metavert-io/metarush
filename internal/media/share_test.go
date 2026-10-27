package media_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/testutil/fakebucket"
)

func fakeStore(t *testing.T) (*media.S3, *fakebucket.Server) {
	t.Helper()
	b := fakebucket.Start(t)
	s, err := media.NewS3(media.S3Config{Endpoint: b.URL, Region: "us-east-1", Bucket: b.Bucket, Prefix: "team", AccessKeyID: b.KeyID, SecretAccessKey: b.Secret, ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	return s, b
}

func put(t *testing.T, s media.Store, key, body, ct string) {
	t.Helper()
	p := filepath.Join(t.TempDir(), "f")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutFile(context.Background(), key, p, media.PutOptions{ContentType: ct}, media.Resume{}, nil); err != nil {
		t.Fatal(err)
	}
}

func do(t *testing.T, method, u string, body []byte, h map[string]string) (int, string) {
	t.Helper()
	var r io.Reader
	if body != nil {
		r = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, u, r)
	if err != nil {
		t.Fatal(err)
	}
	for k, v := range h {
		req.Header.Set(k, v)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(b)
}

// The fake bucket is private like a cloud bucket (D313): MetaRush's own
// client works, a link signed with its key works until it expires, and
// anything else is refused; a signed upload takes exactly the length and
// type it was signed for.
func TestSignedLinksOnAPrivateBucket(t *testing.T) {
	s, b := fakeStore(t)
	ctx := context.Background()
	put(t, s, "ws/w/review/k/index.html", "<p>hello</p>", "text/html; charset=utf-8")

	if code, _ := do(t, "GET", b.URL+"/media/team/ws/w/review/k/index.html", nil, nil); code != 403 {
		t.Fatalf("an unsigned read must be refused, got %d", code)
	}
	u, err := s.PresignGet(ctx, "ws/w/review/k/index.html", time.Hour, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if code, body := do(t, "GET", u, nil, nil); code != 200 || body != "<p>hello</p>" {
		t.Fatalf("a signed read: %d %q", code, body)
	}
	if code, _ := do(t, "GET", strings.Replace(u, "index.html", "other.html", 1), nil, nil); code != 403 {
		t.Fatalf("a signed link names one object only, got %d", code)
	}

	// Writes: only through a signed upload, of exactly its length and type.
	pu, h, err := s.PresignPut(ctx, "ws/w/review/k/c0.json", time.Hour, "application/json", 16)
	if err != nil {
		t.Fatal(err)
	}
	if h["Content-Type"] != "application/json" || h["Host"] != "" || h["Content-Length"] != "" {
		t.Fatalf("the browser's headers: %v", h)
	}
	if code, body := do(t, "PUT", pu, []byte(`{"a":1}         `), h); code != 200 {
		t.Fatalf("a signed upload of its length: %d %s", code, body)
	}
	if got, _ := b.Object("team/ws/w/review/k/c0.json"); string(got) != `{"a":1}         ` {
		t.Fatalf("stored %q", got)
	}
	if code, _ := do(t, "PUT", pu, []byte(strings.Repeat("x", 400)), h); code != 403 {
		t.Fatalf("a longer upload must be refused, got %d", code)
	}
	if code, _ := do(t, "PUT", pu, []byte(`{"a":1}         `), map[string]string{"Content-Type": "text/html"}); code != 403 {
		t.Fatalf("an upload of another type must be refused, got %d", code)
	}
	if code, _ := do(t, "PUT", strings.Replace(pu, "c0.json", "index.html", 1), []byte(`{"a":1}         `), h); code != 403 {
		t.Fatalf("a signed upload can't write another object, got %d", code)
	}
	if code, _ := do(t, "DELETE", u, nil, nil); code != 403 {
		t.Fatalf("a read link can't delete, got %d", code)
	}

	// Copies stay inside the bucket.
	if err := s.Copy(ctx, "ws/w/review/k/index.html", "ws/w/review/k2/index.html"); err != nil {
		t.Fatal(err)
	}
	if got, ok := b.Object("team/ws/w/review/k2/index.html"); !ok || string(got) != "<p>hello</p>" {
		t.Fatalf("copy: %q", got)
	}
	if err := s.Copy(ctx, "ws/w/review/nothing", "ws/w/review/k3/x"); !errors.Is(err, media.ErrNotFound) {
		t.Fatalf("copying nothing: %v", err)
	}

	// The bucket's clock past the link's time: refused, as expired.
	b.Ahead(2 * time.Hour)
	if code, body := do(t, "GET", u, nil, nil); code != 403 || !strings.Contains(body, "expired") {
		t.Fatalf("an expired link: %d %s", code, body)
	}
	// MetaRush's own client still reads it (its requests carry its key).
	if _, err := s.Stat(ctx, "ws/w/review/k/index.html"); err != nil {
		t.Fatalf("MetaRush's own read after the link expired: %v", err)
	}
	if _, _, err := s.PresignPut(ctx, "ws/w/x", 8*24*time.Hour, "application/json", 10); err == nil {
		t.Fatal("a store signs a week at most")
	}
}

func TestWhatCanShare(t *testing.T) {
	s, _ := fakeStore(t)
	if why := media.CanShare(s); why != "" {
		t.Fatalf("an S3 bucket can share: %s", why)
	}
	fs, err := media.NewFS(t.TempDir(), false)
	if err != nil {
		t.Fatal(err)
	}
	if why := media.CanShare(fs); !strings.Contains(why, "folder") {
		t.Fatalf("a folder can't: %q", why)
	}
}

// The fake bucket is as strict as a cloud bucket with MetaRush's own client
// too: a request is taken only when signed with the bucket's secret — not
// for naming the key's ID, which every signed link on a page shows.
func TestTheFakeBucketChecksEverySignature(t *testing.T) {
	s, b := fakeStore(t)
	ctx := context.Background()
	// MetaRush's own client: a multipart upload, a ranged read, a copy.
	p := filepath.Join(t.TempDir(), "big")
	big := bytes.Repeat([]byte("0123456789abcdef"), (20<<20)/16+77)
	if err := os.WriteFile(p, big, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutFile(ctx, "ws/w/objects/big", p, media.PutOptions{ContentType: "video/mp4"}, media.Resume{}, nil); err != nil {
		t.Fatalf("a multipart upload signed with the secret: %v", err)
	}
	rc, _, err := s.Get(ctx, "ws/w/objects/big", 100, 10)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if !bytes.Equal(got, big[100:110]) {
		t.Fatalf("ranged read: %q", got)
	}
	// Naming the key's ID with any other signature: refused.
	forged := map[string]string{"Authorization": "AWS4-HMAC-SHA256 Credential=" + b.KeyID + "/20260101/us-east-1/s3/aws4_request, SignedHeaders=host, Signature=00",
		"X-Amz-Date": time.Now().UTC().Format("20060102T150405Z"), "X-Amz-Content-Sha256": "UNSIGNED-PAYLOAD"}
	if code, body := do(t, "GET", b.URL+"/media?list-type=2&prefix=team/", nil, forged); code != 403 || strings.Contains(body, "<Key>") {
		t.Fatalf("a forged list: %d %s", code, body)
	}
	if code, _ := do(t, "PUT", b.URL+"/media/team/review/x/index.html", []byte("<script>alert(1)</script>"), forged); code != 403 {
		t.Fatalf("a forged upload: %d", code)
	}
	if _, ok := b.Object("team/review/x/index.html"); ok {
		t.Fatal("a forged upload was stored")
	}
	// Signed with another secret: refused.
	wrong, err := media.NewS3(media.S3Config{Endpoint: b.URL, Region: "us-east-1", Bucket: b.Bucket, Prefix: "team", AccessKeyID: b.KeyID, SecretAccessKey: "not-the-secret", ForcePathStyle: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := wrong.Stat(ctx, "ws/w/objects/big"); err == nil {
		t.Fatal("a request signed with another secret was taken")
	}
	// Unsigned: refused.
	if code, _ := do(t, "GET", b.URL+"/media/team/ws/w/objects/big", nil, nil); code != 403 {
		t.Fatalf("an unsigned read: %d", code)
	}
}

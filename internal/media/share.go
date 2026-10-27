package media

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/storage"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// What outside reviewers need from a store (D313): links a browser reads one
// object through (PresignGet), links it writes one object through, and a
// copy of an object made inside the store. Only stores that are cloud
// buckets can do the first two; a folder on this computer can't be reached
// by anyone else.

// MaxSignedTTL is the longest a store's signed link lasts: SigV4 and GCS V4
// signatures both stop at seven days.
const MaxSignedTTL = 7 * 24 * time.Hour

// PutSigner is a store that signs a link a browser writes one object
// through: exactly size bytes of contentType, until ttl ends. Headers are
// the ones the browser must send with it (the signature covers them).
type PutSigner interface {
	PresignPut(ctx context.Context, key string, ttl time.Duration, contentType string, size int64) (string, map[string]string, error)
}

// Copier copies an object to another key inside the store, without the
// bytes passing through this computer.
type Copier interface {
	Copy(ctx context.Context, srcKey, dstKey string) error
}

// CanShare says whether a store can hold pages people outside the workspace
// open: it signs reads and writes (empty), or why not, in plain words.
func CanShare(st Store) string {
	if st == nil {
		return "MetaRush has no media store here."
	}
	if _, ok := st.(PutSigner); !ok || !st.Capabilities().Shared {
		return "This workspace keeps its media in a folder, which nobody outside can reach."
	}
	if g, ok := st.(*GCS); ok {
		if ok, _ := g.SigningStatus(); !ok {
			return "The Google Cloud Storage key this workspace uses can't sign links."
		}
	}
	return ""
}

// clampTTL keeps a signed link within what a store signs.
func clampTTL(ttl time.Duration) (time.Duration, error) {
	if ttl <= 0 {
		return 0, errors.New("a signed link needs a time to last")
	}
	if ttl > MaxSignedTTL {
		return 0, fmt.Errorf("a signed link lasts at most %s", MaxSignedTTL)
	}
	return ttl, nil
}

// PresignPut signs a PUT of exactly size bytes: the signature covers the
// Content-Length and Content-Type, so the store refuses anything else.
func (s *S3) PresignPut(ctx context.Context, key string, ttl time.Duration, contentType string, size int64) (string, map[string]string, error) {
	k, err := s.full(key)
	if err != nil {
		return "", nil, err
	}
	if ttl, err = clampTTL(ttl); err != nil {
		return "", nil, err
	}
	if size <= 0 {
		return "", nil, errors.New("a signed upload needs its size")
	}
	in := &s3.PutObjectInput{Bucket: &s.cfg.Bucket, Key: &k, ContentLength: aws.Int64(size)}
	if contentType != "" {
		in.ContentType = &contentType
	}
	req, err := s.presign.PresignPutObject(ctx, in, s3.WithPresignExpires(ttl))
	if err != nil {
		return "", nil, err
	}
	return req.URL, browserHeaders(req.SignedHeader), nil
}

// browserHeaders are the signed headers a browser sends itself (it sets
// Host and Content-Length on its own, and may not set them).
func browserHeaders(h http.Header) map[string]string {
	out := map[string]string{}
	for k, v := range h {
		switch strings.ToLower(k) {
		case "host", "content-length":
			continue
		}
		if len(v) > 0 {
			out[http.CanonicalHeaderKey(k)] = v[0]
		}
	}
	return out
}

// Copy copies inside the bucket (objects past 5 GB need a multipart copy:
// the caller falls back to reading and writing them).
func (s *S3) Copy(ctx context.Context, srcKey, dstKey string) error {
	src, err := s.full(srcKey)
	if err != nil {
		return err
	}
	dst, err := s.full(dstKey)
	if err != nil {
		return err
	}
	source := s.cfg.Bucket + "/" + (&url.URL{Path: src}).EscapedPath()
	if _, err := s.client.CopyObject(ctx, &s3.CopyObjectInput{Bucket: &s.cfg.Bucket, Key: &dst, CopySource: &source}); err != nil {
		if isNotFound(err) {
			return ErrNotFound
		}
		return fmt.Errorf("copy %s: %w", srcKey, err)
	}
	return nil
}

// PresignPut signs a V4 PUT whose length GCS checks against the signed
// x-goog-content-length-range (up to size bytes).
func (g *GCS) PresignPut(ctx context.Context, key string, ttl time.Duration, contentType string, size int64) (string, map[string]string, error) {
	name, err := g.full(key)
	if err != nil {
		return "", nil, err
	}
	if ttl, err = clampTTL(ttl); err != nil {
		return "", nil, err
	}
	if ok, _ := g.SigningStatus(); !ok {
		return "", nil, ErrUnsupported
	}
	// Exactly size bytes, like SigV4's signed Content-Length.
	rng := strconv.FormatInt(size, 10) + "," + strconv.FormatInt(size, 10)
	opts := &storage.SignedURLOptions{Scheme: storage.SigningSchemeV4, Method: http.MethodPut, Expires: time.Now().Add(ttl),
		ContentType: contentType, Headers: []string{"x-goog-content-length-range:" + rng}, Style: g.urlStyle()}
	if g.saKey != nil {
		opts.GoogleAccessID, opts.PrivateKey = g.saEmail, g.saKey
	}
	if g.host != "" {
		opts.Hostname, opts.Insecure = g.host, g.baseURL.Scheme == "http"
	}
	u, err := g.bkt.SignedURL(name, opts)
	if err != nil {
		return "", nil, err
	}
	h := map[string]string{"X-Goog-Content-Length-Range": rng}
	if contentType != "" {
		h["Content-Type"] = contentType
	}
	return u, h, nil
}

// Copy copies inside the bucket.
func (g *GCS) Copy(ctx context.Context, srcKey, dstKey string) error {
	src, err := g.full(srcKey)
	if err != nil {
		return err
	}
	dst, err := g.full(dstKey)
	if err != nil {
		return err
	}
	if _, err := g.bkt.Object(dst).CopierFrom(g.bkt.Object(src)).Run(ctx); err != nil {
		if errors.Is(err, storage.ErrObjectNotExist) {
			return ErrNotFound
		}
		return fmt.Errorf("copy %s: %w", srcKey, err)
	}
	return nil
}

// urlStyle is how a signed link names the bucket: in the host
// (<bucket>.storage.googleapis.com), so a page opened from it has an address
// of its own bucket — not storage.googleapis.com, which every Google Cloud
// customer's links share (a browser keeps a site's storage per address).
// An emulator, and a bucket whose name has dots (no certificate covers
// it), keep the path.
func (g *GCS) urlStyle() storage.URLStyle {
	if g.host != "" || strings.Contains(g.cfg.Bucket, ".") {
		return storage.PathStyle()
	}
	return storage.VirtualHostedStyle()
}

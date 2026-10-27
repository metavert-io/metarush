// Package fakebucket is an S3-compatible bucket for tests and browser runs
// that behaves like a private bucket (D313): a request needs MetaRush's key
// (its client's Authorization header names it) or a link signed with that key
// (SigV4 query signing: its signature, the headers it signs — a fixed
// Content-Length, a Content-Type — and its expiry are checked). Anything else
// is refused, as a real private bucket refuses it. Behind the check is
// gofakes3 in memory. No cloud account is involved, and it listens only where
// it is told (127.0.0.1 in browser runs).
package fakebucket

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/johannesboyne/gofakes3"
	"github.com/johannesboyne/gofakes3/backend/s3mem"
)

// Request is one request the bucket answered.
type Request struct {
	Method string
	// Key is the object's key inside the bucket.
	Key string
	// Signed: it came with a signed link (a browser), not MetaRush's client.
	Signed bool
	Status int
	// Why says why it was refused ("" when it wasn't).
	Why string
}

// Server is a running fake bucket.
type Server struct {
	URL    string
	Bucket string
	KeyID  string
	Secret string

	backend *s3mem.Backend
	next    http.Handler
	signer  *v4.Signer

	mu     sync.Mutex
	ahead  time.Duration
	log    []Request
	admin  bool
	closer func()
}

// New makes the bucket's handler (cmd/fakebucket serves it on a port).
// Admin turns on POST /__fakebucket/ahead?by=<duration>, which moves the
// bucket's clock forward so a browser run can watch a link expire.
func New(keyID, secret, bucket string, admin bool) (*Server, error) {
	if keyID == "" || secret == "" || bucket == "" {
		return nil, fmt.Errorf("fakebucket needs a key ID, a secret and a bucket")
	}
	b := s3mem.New()
	if err := b.CreateBucket(bucket); err != nil {
		return nil, err
	}
	s := &Server{Bucket: bucket, KeyID: keyID, Secret: secret, backend: b, signer: v4.NewSigner(), admin: admin}
	s.next = gofakes3.New(b, gofakes3.WithTimeSource(clock{s}), gofakes3.WithTimeSkewLimit(0)).Server()
	return s, nil
}

// Start serves a new bucket on a loopback port until the test ends.
func Start(t testing.TB) *Server {
	t.Helper()
	s, err := New("fake-key-id", "fake-secret-made-for-this-test", "media", false)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(s)
	s.URL = srv.URL
	t.Cleanup(srv.Close)
	return s
}

// Ahead moves the bucket's clock forward (links signed before expire sooner).
func (s *Server) Ahead(d time.Duration) {
	s.mu.Lock()
	s.ahead += d
	s.mu.Unlock()
}

// Now is the bucket's clock.
func (s *Server) Now() time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Add(s.ahead)
}

// Requests are the requests answered so far.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Request(nil), s.log...)
}

// Object reads an object as the bucket holds it (key inside the bucket).
func (s *Server) Object(key string) ([]byte, bool) {
	obj, err := s.backend.GetObject(s.Bucket, key, nil)
	if err != nil {
		return nil, false
	}
	defer obj.Contents.Close()
	b, err := io.ReadAll(obj.Contents)
	return b, err == nil
}

// Keys lists the keys in the bucket under a prefix.
func (s *Server) Keys(prefix string) []string {
	res, err := s.backend.ListBucket(s.Bucket, &gofakes3.Prefix{Prefix: prefix, HasPrefix: prefix != ""}, gofakes3.ListBucketPage{})
	if err != nil {
		return nil
	}
	var out []string
	for _, c := range res.Contents {
		out = append(out, c.Key)
	}
	return out
}

type clock struct{ s *Server }

func (c clock) Now() time.Time                  { return c.s.Now() }
func (c clock) Since(t time.Time) time.Duration { return c.s.Now().Sub(t) }

func (s *Server) record(r Request) {
	s.mu.Lock()
	s.log = append(s.log, r)
	if len(s.log) > 5000 {
		s.log = s.log[len(s.log)-5000:]
	}
	s.mu.Unlock()
}

// key is the object key a request names (path style: /<bucket>/<key>).
func (s *Server) key(r *http.Request) string {
	p := strings.TrimPrefix(r.URL.Path, "/")
	return strings.TrimPrefix(strings.TrimPrefix(p, s.Bucket), "/")
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if s.admin && r.URL.Path == "/__fakebucket/ahead" && r.Method == http.MethodPost {
		d, err := time.ParseDuration(r.URL.Query().Get("by"))
		if err != nil {
			http.Error(w, "by=<duration>", http.StatusBadRequest)
			return
		}
		s.Ahead(d)
		fmt.Fprintln(w, "ok")
		return
	}
	req := Request{Method: r.Method, Key: s.key(r)}
	refuse := func(status int, code, msg string) {
		req.Status, req.Why = status, msg
		s.record(req)
		w.Header().Set("Content-Type", "application/xml")
		w.WriteHeader(status)
		fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>%s</Message></Error>`, code, msg)
	}
	q := r.URL.Query()
	switch {
	case q.Get("X-Amz-Signature") != "":
		req.Signed = true
		if why := s.checkSigned(r); why != "" {
			code := "SignatureDoesNotMatch"
			if strings.Contains(why, "expired") {
				code = "AccessDenied"
			}
			refuse(http.StatusForbidden, code, why)
			return
		}
	case strings.HasPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 "):
		// MetaRush's own client: a request signed with the bucket's secret
		// (the key's ID alone is in every signed link a page shows).
		if why := s.checkHeader(r); why != "" {
			code := "SignatureDoesNotMatch"
			if strings.Contains(why, "skewed") {
				code = "RequestTimeTooSkewed"
			}
			refuse(http.StatusForbidden, code, why)
			return
		}
	default:
		refuse(http.StatusForbidden, "AccessDenied", "Access Denied")
		return
	}
	rec := &recorder{ResponseWriter: w, status: http.StatusOK}
	// A signed link may ask for the answer's type and disposition.
	if v := q.Get("response-content-type"); v != "" && req.Signed {
		rec.contentType = v
	}
	if v := q.Get("response-content-disposition"); v != "" && req.Signed {
		rec.disposition = v
	}
	s.next.ServeHTTP(rec, r)
	req.Status = rec.status
	s.record(req)
}

// checkHeader checks a request signed in its Authorization header: the key,
// the time it was signed (within 15 minutes of now — the real clock, as
// Ahead only moves the clock links are judged by), the body against the hash
// it was signed with, and the signature over the method, path, query and the
// headers it names, computed again with the bucket's secret. "" when it
// holds; else why not.
func (s *Server) checkHeader(r *http.Request) string {
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ")
	fields := map[string]string{}
	for _, part := range strings.Split(auth, ",") {
		k, v, ok := strings.Cut(strings.TrimSpace(part), "=")
		if ok {
			fields[k] = v
		}
	}
	cred := strings.Split(fields["Credential"], "/")
	if len(cred) != 5 || cred[0] != s.KeyID || cred[3] != "s3" || cred[4] != "aws4_request" {
		return "The request isn't signed with this bucket's key."
	}
	at, err := time.Parse("20060102T150405Z", r.Header.Get("X-Amz-Date"))
	if err != nil {
		return "The request has no time it was signed."
	}
	if d := time.Since(at); d > 15*time.Minute || d < -15*time.Minute {
		return "The difference between the request time and the current time is too large (skewed)."
	}
	payload := r.Header.Get("X-Amz-Content-Sha256")
	if payload == "" {
		return "The request doesn't say the hash of its body."
	}
	if payload != "UNSIGNED-PAYLOAD" && !strings.HasPrefix(payload, "STREAMING-") {
		var b []byte
		if r.Body != nil {
			if b, err = io.ReadAll(r.Body); err != nil {
				return "The body can't be read."
			}
		}
		sum := sha256.Sum256(b)
		if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(sum[:])), []byte(payload)) != 1 {
			return "The body isn't the one the request was signed with."
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
	}
	u := *r.URL
	u.Scheme, u.Host = "http", r.Host
	again, err := http.NewRequestWithContext(context.Background(), r.Method, u.String(), nil)
	if err != nil {
		return "The request can't be read."
	}
	again.Host = r.Host
	for _, h := range strings.Split(fields["SignedHeaders"], ";") {
		switch h {
		case "host":
		case "content-length":
			again.ContentLength = r.ContentLength
		default:
			again.Header[http.CanonicalHeaderKey(h)] = r.Header.Values(h)
		}
	}
	creds := aws.Credentials{AccessKeyID: s.KeyID, SecretAccessKey: s.Secret}
	if err := s.signer.SignHTTP(context.Background(), creds, again, payload, "s3", cred[2], at, func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true }); err != nil {
		return "The request can't be checked."
	}
	want := signatureOf(again.Header.Get("Authorization"))
	if want == "" || subtle.ConstantTimeCompare([]byte(want), []byte(fields["Signature"])) != 1 {
		return "The request signature we calculated does not match the signature you provided."
	}
	return ""
}

func signatureOf(auth string) string {
	_, sig, ok := strings.Cut(auth, "Signature=")
	if !ok {
		return ""
	}
	return strings.TrimSpace(sig)
}

// checkSigned checks a link signed with SigV4 query parameters: the
// algorithm, the key, the time it was signed and how long it lasts, then the
// signature over the method, path, query and the headers it signs, computed
// again with the bucket's secret. "" when it holds; else why not.
func (s *Server) checkSigned(r *http.Request) string {
	q := r.URL.Query()
	if q.Get("X-Amz-Algorithm") != "AWS4-HMAC-SHA256" {
		return "The link isn't signed the way this bucket checks."
	}
	cred := strings.Split(q.Get("X-Amz-Credential"), "/")
	if len(cred) != 5 || cred[0] != s.KeyID || cred[3] != "s3" || cred[4] != "aws4_request" {
		return "The link isn't signed with this bucket's key."
	}
	region := cred[2]
	at, err := time.Parse("20060102T150405Z", q.Get("X-Amz-Date"))
	if err != nil {
		return "The link has no time it was signed."
	}
	secs, err := strconv.Atoi(q.Get("X-Amz-Expires"))
	if err != nil || secs <= 0 || secs > 7*24*3600 {
		return "The link says no time to last, or more than a week."
	}
	now := s.Now()
	if now.After(at.Add(time.Duration(secs) * time.Second)) {
		return "Request has expired"
	}
	if at.After(now.Add(15 * time.Minute)) {
		return "The link was signed in the future."
	}
	signed := strings.Split(q.Get("X-Amz-SignedHeaders"), ";")
	// Signed again from scratch: the request as it came, with only the
	// headers the link signs.
	u := *r.URL
	u.Scheme, u.Host = "http", r.Host
	qq := url.Values{}
	for k, v := range q {
		switch k {
		case "X-Amz-Signature", "X-Amz-Algorithm", "X-Amz-Credential", "X-Amz-Date", "X-Amz-SignedHeaders":
			continue
		}
		qq[k] = v
	}
	u.RawQuery = qq.Encode()
	again, err := http.NewRequestWithContext(context.Background(), r.Method, u.String(), nil)
	if err != nil {
		return "The link can't be read."
	}
	again.Host = r.Host
	for _, h := range signed {
		switch h {
		case "host":
		case "content-length":
			if r.ContentLength <= 0 {
				return "The upload doesn't say its length."
			}
			again.ContentLength = r.ContentLength
		default:
			v := r.Header.Values(h)
			if len(v) == 0 {
				return "The request leaves out a header its link signs (" + h + ")."
			}
			again.Header[http.CanonicalHeaderKey(h)] = v
		}
	}
	creds := aws.Credentials{AccessKeyID: s.KeyID, SecretAccessKey: s.Secret}
	signedURL, _, err := s.signer.PresignHTTP(context.Background(), creds, again, "UNSIGNED-PAYLOAD", "s3", region, at,
		func(o *v4.SignerOptions) { o.DisableURIPathEscaping = true })
	if err != nil {
		return "The link can't be checked."
	}
	su, err := url.Parse(signedURL)
	if err != nil {
		return "The link can't be checked."
	}
	if want := su.Query().Get("X-Amz-Signature"); subtle.ConstantTimeCompare([]byte(want), []byte(q.Get("X-Amz-Signature"))) != 1 {
		return "The request signature we calculated does not match the signature you provided."
	}
	if r.ContentLength > 0 && r.Body != nil {
		// gofakes3 reads exactly the length; a longer body is refused by Go's server.
		b, err := io.ReadAll(io.LimitReader(r.Body, r.ContentLength+1))
		if err != nil || int64(len(b)) != r.ContentLength {
			return "The upload isn't the length it says."
		}
		r.Body = io.NopCloser(bytes.NewReader(b))
	}
	return ""
}

type recorder struct {
	http.ResponseWriter
	status      int
	wrote       bool
	contentType string
	disposition string
}

func (r *recorder) WriteHeader(code int) {
	if !r.wrote {
		if r.contentType != "" && code < 300 {
			r.Header().Set("Content-Type", r.contentType)
		}
		if r.disposition != "" && code < 300 {
			r.Header().Set("Content-Disposition", r.disposition)
		}
		r.status, r.wrote = code, true
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if !r.wrote {
		r.WriteHeader(http.StatusOK)
	}
	return r.ResponseWriter.Write(b)
}

// Flush passes streaming through (a video read in ranges).
func (r *recorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

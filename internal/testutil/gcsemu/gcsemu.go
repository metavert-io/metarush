// Package gcsemu is an in-process Google Cloud Storage emulator for tests. No
// GCP project or account is involved.
//
// It is fake-gcs-server with a thin layer in front: fake-gcs-server's resumable
// endpoint appends every chunk without checking its offset and treats a status
// query ("Content-Range: bytes */N") as the final chunk, so on its own it cannot
// exercise resuming a session after a restart. The layer implements the
// documented resumable-upload protocol — 308 + Range, status queries, expired
// sessions (404), declared CRC32C/MD5 validation, ifGenerationMatch=0, DELETE to
// cancel — and commits finished objects into fake-gcs-server, which serves
// everything else (metadata, ranged reads, deletes, signed-URL downloads).
// Neither part is the real service.
package gcsemu

import (
	"crypto/md5"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/crc32"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/fsouza/fake-gcs-server/fakestorage"
)

type session struct {
	bucket, name, ct string
	meta             map[string]string
	crc, md5         string
	total            int64
	data             []byte
	ifGen0           bool
	done             bool
	obj              map[string]any
}

// Server is a running emulator.
type Server struct {
	URL  string // base URL (http://127.0.0.1:port), the driver's Endpoint
	Host string // host:port, for STORAGE_EMULATOR_HOST
	Fake *fakestorage.Server

	next     http.Handler
	mu       sync.Mutex
	sessions map[string]*session
	seq      int
	stats    Stats

	shortCommit int64
	failNext    int
	corruptCRC  bool
}

// Stats counts protocol traffic.
type Stats struct {
	Initiations   int   // resumable sessions started
	StatusQueries int   // "bytes */N" queries
	ChunkBytes    int64 // bytes received in chunk bodies
	Open          int   // sessions not completed or canceled
}

// Start runs an emulator with the given buckets until the test ends.
func Start(t testing.TB, buckets ...string) *Server {
	t.Helper()
	fake, err := fakestorage.NewServerWithOptions(fakestorage.Options{NoListener: true, PublicHost: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	for _, b := range buckets {
		fake.CreateBucketWithOpts(fakestorage.CreateBucketOpts{Name: b})
	}
	s := &Server{Fake: fake, next: fake.HTTPHandler(), sessions: map[string]*session{}}
	srv := httptest.NewServer(s)
	t.Cleanup(srv.Close)
	s.URL = srv.URL
	s.Host = strings.TrimPrefix(srv.URL, "http://")
	return s
}

// Stats returns the traffic counters.
func (s *Server) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := s.stats
	for _, x := range s.sessions {
		if !x.done {
			st.Open++
		}
	}
	return st
}

// Expire forgets a session, as GCS does after a week.
func (s *Server) Expire(sessionURI string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, _ := url.Parse(sessionURI)
	delete(s.sessions, u.Query().Get("upload_id"))
}

// ShortCommit makes the next non-final chunk persist at most n bytes.
func (s *Server) ShortCommit(n int64) { s.mu.Lock(); s.shortCommit = n; s.mu.Unlock() }

// FailNext answers the next n chunk uploads with 503 (nothing committed).
func (s *Server) FailNext(n int) { s.mu.Lock(); s.failNext = n; s.mu.Unlock() }

// CorruptCRC reports a wrong crc32c in completed object resources.
func (s *Server) CorruptCRC(on bool) { s.mu.Lock(); s.corruptCRC = on; s.mu.Unlock() }

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if strings.HasPrefix(r.URL.Path, "/upload/storage/v1/b/") && r.URL.Query().Get("uploadType") == "resumable" {
		s.resumable(w, r)
		return
	}
	s.next.ServeHTTP(w, r)
}

func b64crc(b []byte) string {
	var cb [4]byte
	binary.BigEndian.PutUint32(cb[:], crc32.Checksum(b, crc32.MakeTable(crc32.Castagnoli)))
	return base64.StdEncoding.EncodeToString(cb[:])
}

func b64md5(b []byte) string { m := md5.Sum(b); return base64.StdEncoding.EncodeToString(m[:]) }

func (s *Server) resumable(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	q := r.URL.Query()
	id := q.Get("upload_id")
	if r.Method == http.MethodPost && id == "" {
		var md struct {
			ContentType string            `json:"contentType"`
			Metadata    map[string]string `json:"metadata"`
			CRC32C      string            `json:"crc32c"`
			MD5Hash     string            `json:"md5Hash"`
		}
		_ = json.NewDecoder(r.Body).Decode(&md)
		total, _ := strconv.ParseInt(r.Header.Get("X-Upload-Content-Length"), 10, 64)
		bucket := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/upload/storage/v1/b/"), "/o")
		s.seq++
		s.stats.Initiations++
		id = fmt.Sprintf("sess%d", s.seq)
		s.sessions[id] = &session{bucket: bucket, name: q.Get("name"), ct: md.ContentType, meta: md.Metadata, crc: md.CRC32C, md5: md.MD5Hash, total: total, ifGen0: q.Get("ifGenerationMatch") == "0"}
		w.Header().Set("Location", fmt.Sprintf("http://%s/upload/storage/v1/b/%s/o?uploadType=resumable&name=%s&upload_id=%s", r.Host, bucket, url.QueryEscape(q.Get("name")), id))
		w.WriteHeader(http.StatusOK)
		return
	}
	x := s.sessions[id]
	if x == nil {
		http.Error(w, "no such upload", http.StatusNotFound)
		return
	}
	if r.Method == http.MethodDelete {
		delete(s.sessions, id)
		w.WriteHeader(499)
		return
	}
	writeObj := func() {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(x.obj)
	}
	incomplete := func() {
		if len(x.data) > 0 {
			w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", len(x.data)-1))
		}
		w.WriteHeader(http.StatusPermanentRedirect)
	}
	rng, _, _ := strings.Cut(strings.TrimPrefix(r.Header.Get("Content-Range"), "bytes "), "/")
	if rng == "*" { // status query
		s.stats.StatusQueries++
		if x.done {
			writeObj()
		} else {
			incomplete()
		}
		return
	}
	if s.failNext > 0 {
		s.failNext--
		_, _ = io.Copy(io.Discard, r.Body)
		http.Error(w, "backend unavailable", http.StatusServiceUnavailable)
		return
	}
	a, _, _ := strings.Cut(rng, "-")
	start, _ := strconv.ParseInt(a, 10, 64)
	body, err := io.ReadAll(r.Body)
	s.stats.ChunkBytes += int64(len(body))
	if err != nil {
		return // the client went away mid-chunk: nothing from it is committed
	}
	if start > int64(len(x.data)) {
		http.Error(w, "gap in upload", http.StatusBadRequest)
		return
	}
	body = body[int64(len(x.data))-start:] // bytes already persisted are ignored
	if s.shortCommit > 0 && int64(len(x.data)+len(body)) < x.total && int64(len(body)) > s.shortCommit {
		body = body[:s.shortCommit]
		s.shortCommit = 0
	}
	x.data = append(x.data, body...)
	if int64(len(x.data)) < x.total {
		incomplete()
		return
	}
	if (x.crc != "" && x.crc != b64crc(x.data)) || (x.md5 != "" && x.md5 != b64md5(x.data)) {
		delete(s.sessions, id)
		http.Error(w, `{"error":{"message":"Provided checksum does not match the data"}}`, http.StatusBadRequest)
		return
	}
	if _, err := s.Fake.GetObject(x.bucket, x.name); err == nil && x.ifGen0 {
		http.Error(w, "precondition failed", http.StatusPreconditionFailed)
		return
	}
	s.Fake.CreateObject(fakestorage.Object{ObjectAttrs: fakestorage.ObjectAttrs{BucketName: x.bucket, Name: x.name, ContentType: x.ct, Metadata: x.meta}, Content: x.data})
	x.done = true
	crc := b64crc(x.data)
	if s.corruptCRC {
		crc = b64crc(append([]byte("x"), x.data...))
	}
	x.obj = map[string]any{"name": x.name, "bucket": x.bucket, "size": strconv.Itoa(len(x.data)), "crc32c": crc, "md5Hash": b64md5(x.data), "contentType": x.ct}
	writeObj()
}

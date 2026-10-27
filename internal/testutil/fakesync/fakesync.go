// Package fakesync is an in-process fake of what MetaRush uses of sync.'s
// API for lip sync (D282): the multipart POST /v2/generate (the footage and
// the sound uploaded as assets), GET /v2/generate/{id}, the signed download
// address of the result, DELETE /v2/generations/{id} (terminal ones only, as
// sync. allows), DELETE /v2/assets/{id} and the key check (GET
// /v2/generations). Tests point METARUSH_SYNC_BASE_URL at it; it never talks
// to the network, accepts only its key, and hands back the uploaded footage
// as the "lip-synced" video. Behaviors: delay, fail, rejected, plan (a 402),
// rate (a 429), server (a 500 when sent).
package fakesync

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// Behavior kinds.
const (
	Delay       = "delay"
	Fail        = "fail"
	Rejected    = "rejected"
	Plan        = "plan"
	Rate        = "rate"
	ServerError = "server"
)

// Behavior is one scripted misbehavior for the next generation.
type Behavior struct {
	Kind string
	N    int
}

type gen struct {
	ID      string
	Model   string
	Options string
	Assets  []string
	Polls   int
	B       Behavior
	Deleted bool
	Video   []byte
}

// Server is the running fake.
type Server struct {
	*httptest.Server
	Key string
	// ReadyAfter is how many checks answer PROCESSING before COMPLETED (default 1).
	ReadyAfter int

	mu       sync.Mutex
	gens     map[string]*gen
	order    []*gen
	assets   map[string]bool
	script   []Behavior
	calls    map[string]int
	problems []string
	n        int
}

// Start starts the fake and points MetaRush at it until the test ends.
func Start(t testing.TB, key string) *Server {
	t.Helper()
	s := &Server{Key: key, ReadyAfter: 1, gens: map[string]*gen{}, assets: map[string]bool{}, calls: map[string]int{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/v2/generate", s.create)
	mux.HandleFunc("/v2/generate/", s.get)
	mux.HandleFunc("/v2/generations", s.list)
	mux.HandleFunc("/v2/generations/", s.deleteGen)
	mux.HandleFunc("/v2/assets/", s.deleteAsset)
	mux.HandleFunc("/out/", s.download)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	t.Setenv("METARUSH_SYNC_BASE_URL", s.URL)
	return s
}

// Script queues behaviors for the next generations.
func (s *Server) Script(b ...Behavior) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script = append(s.script, b...)
}

// Calls counts what was asked: create, get, download, delete-generation, delete-asset, list.
func (s *Server) Calls(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[name]
}

// Kept counts generations and uploaded files still at the fake.
func (s *Server) Kept() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, g := range s.order {
		if !g.Deleted {
			n++
		}
	}
	for _, alive := range s.assets {
		if alive {
			n++
		}
	}
	return n
}

// Problems lists what a careful client never does.
func (s *Server) Problems() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.problems...)
}

// Models lists the models asked for, in order.
func (s *Server) Models() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, g := range s.order {
		out = append(out, g.Model+" "+g.Options)
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) authed(w http.ResponseWriter, r *http.Request) bool {
	if r.Header.Get("x-api-key") != s.Key {
		writeJSON(w, 401, map[string]string{"message": "Unauthorized", "errorCode": "unauthorized"})
		return false
	}
	return true
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost || !s.authed(w, r) {
		return
	}
	if err := r.ParseMultipartForm(64 << 20); err != nil {
		writeJSON(w, 400, map[string]string{"message": "not a multipart form"})
		return
	}
	s.mu.Lock()
	s.calls["create"]++
	b := Behavior{}
	if len(s.script) > 0 {
		b, s.script = s.script[0], s.script[1:]
	}
	s.mu.Unlock()
	switch b.Kind {
	case Plan:
		writeJSON(w, 422, map[string]string{"message": "File size exceeds your plan limit", "errorCode": "file_size_exceeds_plan_limit"})
		return
	case Rate:
		writeJSON(w, 429, map[string]string{"message": "Too many requests"})
		return
	case ServerError:
		writeJSON(w, 500, map[string]string{"message": "Internal error"})
		return
	}
	read := func(field string) []byte {
		f, _, err := r.FormFile(field)
		if err != nil {
			return nil
		}
		defer f.Close()
		data, _ := io.ReadAll(f)
		return data
	}
	video, audio := read("video"), read("audio")
	if len(video) == 0 || len(audio) == 0 {
		writeJSON(w, 400, map[string]string{"message": "video and audio are required", "errorCode": "generation_input_validation_failed"})
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.n++
	g := &gen{ID: fmt.Sprintf("gen-%d", s.n), Model: r.FormValue("model"), Options: r.FormValue("options"), B: b, Video: video}
	for _, kind := range []string{"video", "audio"} {
		s.n++
		id := fmt.Sprintf("asset-%s-%d", kind, s.n)
		s.assets[id] = true
		g.Assets = append(g.Assets, id)
	}
	s.gens[g.ID] = g
	s.order = append(s.order, g)
	writeJSON(w, 201, map[string]any{"id": g.ID, "status": "PENDING", "model": g.Model, "createdAt": "2026-10-25T12:00:00Z",
		"input": []map[string]string{{"type": "video", "assetId": g.Assets[0]}, {"type": "audio", "assetId": g.Assets[1]}}})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v2/generate/")
	s.mu.Lock()
	s.calls["get"]++
	g := s.gens[id]
	if g != nil {
		g.Polls++
	}
	ready := s.ReadyAfter
	s.mu.Unlock()
	if g == nil || g.Deleted {
		writeJSON(w, 404, map[string]string{"message": "Generation not found", "errorCode": "generation_not_found"})
		return
	}
	if g.B.Kind == Delay {
		ready += max(1, g.B.N)
	}
	out := map[string]any{"id": g.ID, "model": g.Model, "status": "PROCESSING"}
	switch {
	case g.Polls <= ready:
	case g.B.Kind == Fail:
		out["status"], out["error"], out["errorCode"] = "FAILED", "No face was found in the video", "generation_no_face_detected"
	case g.B.Kind == Rejected:
		out["status"], out["error"], out["errorCode"] = "REJECTED", "The input was rejected by moderation", "generation_rejected"
	default:
		out["status"], out["outputUrl"], out["outputDuration"] = "COMPLETED", s.URL+"/out/"+g.ID+".mp4?sig=fake", 3.0
	}
	writeJSON(w, 200, out)
}

func (s *Server) download(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("x-api-key") != "" {
		s.mu.Lock()
		s.problems = append(s.problems, "the key was sent with the download")
		s.mu.Unlock()
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/out/"), ".mp4")
	s.mu.Lock()
	s.calls["download"]++
	g := s.gens[id]
	s.mu.Unlock()
	if g == nil || g.Deleted {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	_, _ = w.Write(g.Video)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	if !s.authed(w, r) {
		return
	}
	s.mu.Lock()
	s.calls["list"]++
	s.mu.Unlock()
	writeJSON(w, 200, []any{})
}

func (s *Server) deleteGen(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete || !s.authed(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v2/generations/")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls["delete-generation"]++
	g := s.gens[id]
	if g == nil || g.Deleted {
		writeJSON(w, 404, map[string]string{"errorCode": "generation_not_found"})
		return
	}
	g.Deleted = true
	w.WriteHeader(204)
}

func (s *Server) deleteAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodDelete || !s.authed(w, r) {
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/v2/assets/")
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls["delete-asset"]++
	if !s.assets[id] {
		writeJSON(w, 404, map[string]string{"errorCode": "asset_not_found"})
		return
	}
	s.assets[id] = false
	w.WriteHeader(204)
}

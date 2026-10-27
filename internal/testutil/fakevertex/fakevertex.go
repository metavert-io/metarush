// Package fakevertex is an in-process fake of what MetaRush uses of Google
// Cloud for Veo (D282): the token endpoint (a service account's signed
// request for an access token, its RS256 signature checked against the
// account's public key) and Vertex AI's predictLongRunning and
// fetchPredictOperation for Veo models. Tests point
// METARUSH_VERTEX_TOKEN_URL and METARUSH_VERTEX_BASE_URL at it; it never
// talks to the network, accepts only tokens it issued, and answers with tiny
// real MP4s (ffmpeg) inline, as Vertex does when no bucket is named.
//
// Key makes a service account's key at run time (nothing secret is in the
// source). Scripts make it misbehave: Script(Behavior{Kind: …}) for the next
// request, or a marker in the prompt ("[fake:safety]"). Kinds: delay (more
// checks before it finishes), safety (Veo makes it, the filters hold it
// back), blocked (refused when sent), disabled (the API isn't turned on),
// denied (the account lacks the role), quota (a 429), server (a 500 when
// sent).
package fakevertex

import (
	"bytes"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/metavert-io/metarush/internal/providers"
)

// Behavior kinds.
const (
	Delay       = "delay"
	Safety      = "safety"
	Blocked     = "blocked"
	Disabled    = "disabled"
	Denied      = "denied"
	Quota       = "quota"
	ServerError = "server"
)

// Behavior is one scripted misbehavior for the next request.
type Behavior struct {
	Kind string
	N    int
}

// Request is one predictLongRunning the fake received.
type Request struct {
	Model      string
	Instance   map[string]any
	Parameters map[string]any
	Operation  string
	Polls      int
	Behavior   Behavior
}

// Server is the running fake.
type Server struct {
	*httptest.Server
	Project string
	Account string
	// ReadyAfter is how many checks answer "not done" before the video is (default 1).
	ReadyAfter int

	mu       sync.Mutex
	pub      *rsa.PublicKey
	tokens   map[string]bool
	script   []Behavior
	reqs     []*Request
	ops      map[string]*Request
	calls    map[string]int
	problems []string
	dir      string
	n        int
}

// Start starts the fake and points MetaRush at it until the test ends; it
// returns the server and a service account's key file for it.
func Start(t testing.TB) (*Server, string) {
	t.Helper()
	priv, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	s := &Server{Project: "garden-films-01", Account: "metarush-veo@garden-films-01.iam.gserviceaccount.com", ReadyAfter: 1, pub: &priv.PublicKey,
		tokens: map[string]bool{}, ops: map[string]*Request{}, calls: map[string]int{}, dir: t.TempDir()}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", s.token)
	mux.HandleFunc("/v1/", s.api)
	s.Server = httptest.NewServer(mux)
	t.Cleanup(s.Close)
	t.Setenv(providers.EnvVertexTokenURL, s.URL+"/token")
	t.Setenv(providers.EnvVertexBaseURL, s.URL)
	providers.ForgetVertexTokens()
	t.Cleanup(providers.ForgetVertexTokens)
	der, _ := x509.MarshalPKCS8PrivateKey(priv)
	key := map[string]string{"type": "service_account", "project_id": s.Project, "private_key_id": "fake0001",
		"private_key": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "client_email": s.Account,
		"client_id": "1234567890", "token_uri": "https://oauth2.googleapis.com/token"}
	b, _ := json.MarshalIndent(key, "", "  ")
	return s, string(b)
}

// Script queues behaviors for the next requests.
func (s *Server) Script(b ...Behavior) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script = append(s.script, b...)
}

// Requests lists what was asked, oldest first.
func (s *Server) Requests() []Request {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []Request
	for _, r := range s.reqs {
		out = append(out, *r)
	}
	return out
}

// Calls counts token, start and check calls.
func (s *Server) Calls(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[name]
}

// Problems lists what a careful client never does.
func (s *Server) Problems() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.problems...)
}

func (s *Server) problem(f string, a ...any) {
	s.mu.Lock()
	s.problems = append(s.problems, fmt.Sprintf(f, a...))
	s.mu.Unlock()
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func gerr(code int, status, msg string) map[string]any {
	return map[string]any{"error": map[string]any{"code": code, "status": status, "message": msg}}
}

// token checks a service account's signed request and issues an access token.
func (s *Server) token(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	s.calls["token"]++
	s.mu.Unlock()
	if err := r.ParseForm(); err != nil || r.Form.Get("grant_type") != "urn:ietf:params:oauth:grant-type:jwt-bearer" {
		writeJSON(w, 400, map[string]string{"error": "unsupported_grant_type"})
		return
	}
	parts := strings.Split(r.Form.Get("assertion"), ".")
	if len(parts) != 3 {
		writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "not a JWT"})
		return
	}
	sig, _ := base64.RawURLEncoding.DecodeString(parts[2])
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(s.pub, crypto.SHA256, sum[:], sig) != nil {
		writeJSON(w, 400, map[string]string{"error": "invalid_grant", "error_description": "Invalid JWT Signature."})
		return
	}
	var claims map[string]any
	b, _ := base64.RawURLEncoding.DecodeString(parts[1])
	_ = json.Unmarshal(b, &claims)
	if claims["iss"] != s.Account || claims["scope"] != "https://www.googleapis.com/auth/cloud-platform" || claims["aud"] != "https://oauth2.googleapis.com/token" {
		s.problem("a token request with claims %v", claims)
	}
	s.mu.Lock()
	s.n++
	tok := fmt.Sprintf("fake-vertex-token-%d", s.n)
	s.tokens[tok] = true
	s.mu.Unlock()
	writeJSON(w, 200, map[string]any{"access_token": tok, "expires_in": 3599, "token_type": "Bearer"})
}

var pathRe = regexp.MustCompile(`^/v1/projects/([^/]+)/locations/([^/]+)/publishers/google/models/([^/:]+):(predictLongRunning|fetchPredictOperation)$`)

func (s *Server) api(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	s.mu.Lock()
	ok := s.tokens[tok]
	s.mu.Unlock()
	if !ok {
		writeJSON(w, 401, gerr(401, "UNAUTHENTICATED", "Request had invalid authentication credentials."))
		return
	}
	m := pathRe.FindStringSubmatch(r.URL.Path)
	if m == nil || r.Method != http.MethodPost {
		writeJSON(w, 404, gerr(404, "NOT_FOUND", "no such method"))
		return
	}
	if m[1] != s.Project || m[2] != "us-central1" {
		s.problem("a request for project %s in %s", m[1], m[2])
	}
	if m[4] == "predictLongRunning" {
		s.start(w, r, m[3])
		return
	}
	s.fetch(w, r)
}

var markerRe = regexp.MustCompile(`\[fake:([a-z]+)\]`)

func (s *Server) start(w http.ResponseWriter, r *http.Request, model string) {
	var body struct {
		Instances  []map[string]any `json:"instances"`
		Parameters map[string]any   `json:"parameters"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body.Instances) != 1 {
		writeJSON(w, 400, gerr(400, "INVALID_ARGUMENT", "one instance, please"))
		return
	}
	if _, ok := body.Parameters["storageUri"]; ok {
		s.problem("a bucket was named: videos would be left in the project")
	}
	prompt, _ := body.Instances[0]["prompt"].(string)
	s.mu.Lock()
	s.calls["start"]++
	b := Behavior{}
	if mm := markerRe.FindStringSubmatch(prompt); mm != nil {
		b.Kind = mm[1]
	} else if len(s.script) > 0 {
		b, s.script = s.script[0], s.script[1:]
	}
	s.n++
	op := fmt.Sprintf("projects/%s/locations/us-central1/publishers/google/models/%s/operations/op-%d", s.Project, model, s.n)
	req := &Request{Model: model, Instance: body.Instances[0], Parameters: body.Parameters, Operation: op, Behavior: b}
	s.mu.Unlock()
	switch b.Kind {
	case Blocked:
		writeJSON(w, 400, gerr(400, "INVALID_ARGUMENT", "The prompt could not be submitted. This prompt contains words that violate Vertex AI's usage guidelines. Support codes: 15236754"))
		return
	case Disabled:
		writeJSON(w, 403, gerr(403, "PERMISSION_DENIED", "Vertex AI API has not been used in project "+s.Project+" before or it is disabled. SERVICE_DISABLED"))
		return
	case Denied:
		writeJSON(w, 403, gerr(403, "PERMISSION_DENIED", "Permission 'aiplatform.endpoints.predict' denied on resource"))
		return
	case Quota:
		writeJSON(w, 429, gerr(429, "RESOURCE_EXHAUSTED", "Quota exceeded for aiplatform.googleapis.com/online_prediction_requests_per_base_model"))
		return
	case ServerError:
		writeJSON(w, 500, gerr(500, "INTERNAL", "Internal error encountered."))
		return
	}
	s.mu.Lock()
	s.reqs = append(s.reqs, req)
	s.ops[op] = req
	s.mu.Unlock()
	writeJSON(w, 200, map[string]string{"name": op})
}

func (s *Server) fetch(w http.ResponseWriter, r *http.Request) {
	var body struct {
		OperationName string `json:"operationName"`
	}
	_ = json.NewDecoder(r.Body).Decode(&body)
	s.mu.Lock()
	s.calls["check"]++
	req := s.ops[body.OperationName]
	if req != nil {
		req.Polls++
	}
	ready := s.ReadyAfter
	s.mu.Unlock()
	if req == nil {
		writeJSON(w, 404, gerr(404, "NOT_FOUND", "operation not found"))
		return
	}
	if req.Behavior.Kind == Delay {
		ready += max(1, req.Behavior.N)
	}
	if req.Polls <= ready {
		writeJSON(w, 200, map[string]any{"name": req.Operation, "done": false})
		return
	}
	resp := map[string]any{"@type": "type.googleapis.com/cloud.ai.large_models.vision.GenerateVideoResponse", "raiMediaFilteredCount": 0}
	if req.Behavior.Kind == Safety {
		resp["raiMediaFilteredCount"] = 1
		resp["raiMediaFilteredReasons"] = []string{"Veo could not generate videos because the input image violates Vertex AI's usage guidelines. Support codes: 58061214"}
		resp["videos"] = []any{}
		writeJSON(w, 200, map[string]any{"name": req.Operation, "done": true, "response": resp})
		return
	}
	secs, _ := req.Parameters["durationSeconds"].(float64)
	mp4, err := s.video(req.Operation, max(secs, 4))
	if err != nil {
		writeJSON(w, 500, gerr(500, "INTERNAL", err.Error()))
		return
	}
	resp["videos"] = []any{map[string]string{"bytesBase64Encoded": base64.StdEncoding.EncodeToString(mp4), "mimeType": "video/mp4"}}
	writeJSON(w, 200, map[string]any{"name": req.Operation, "done": true, "response": resp})
}

// video is a tiny real MP4 of so many seconds, with sound.
func (s *Server) video(op string, secs float64) ([]byte, error) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("the fake needs ffmpeg on PATH: %w", err)
	}
	out := filepath.Join(s.dir, strings.ReplaceAll(filepath.Base(op), "/", "-")+".mp4")
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=64x36:rate=24:duration=%.3f", secs),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=330:duration=%.3f", secs),
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "48k", "-shortest", "-movflags", "+faststart", out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, stderr.String())
	}
	return os.ReadFile(out)
}

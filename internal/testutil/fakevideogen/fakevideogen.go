// Package fakevideogen is an in-process fake of the parts of the Gemini API
// MetaRush uses for generative video (D123): listing models, creating,
// reading, cancelling and deleting background interactions, and the Files API
// (resumable upload, state, download, delete). Tests, the acceptance run and
// the dashboard's browser checks point METARUSH_GEMINI_BASE_URL at it; it never
// talks to the network, accepts only the key it was created with, and makes
// tiny real MP4s (ffmpeg) keyed by the request's hash and take.
//
// Scripts make it misbehave on purpose: queue them with Script (in process) or
// POST /__script (as its own process), or put a marker in the prompt
// ("[fake:celebrity]"). Kinds: delay (polls before it finishes), celebrity and
// minor (blocked when sent), child (finishes without a video), partial (a
// shorter video, marked incomplete), rate and spend (429s), server (500s when
// sent), poll-error (a 503 while waiting), poll-rate (429s while waiting),
// fail (the interaction fails), expired (the finished file is gone),
// free-tier and free-tier-start (Google's answer for a key whose project is on
// the free tier, word for word, on every status check or when sent), and
// retired models (Retired). SetFreeTier puts every request on the free tier.
package fakevideogen

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Behavior kinds.
const (
	Delay       = "delay"
	Celebrity   = "celebrity"
	Minor       = "minor"
	Child       = "child"
	Partial     = "partial"
	Rate        = "rate"
	Spend       = "spend"
	ServerError = "server"
	PollError   = "poll-error"
	Fail        = "fail"
	Expired     = "expired"
	// PollRate: status checks answer 429 "too many requests" (the first N, or every one when N is 0).
	PollRate = "poll-rate"
	// FreeTier: the request is accepted, then every status check answers the free tier's 429 (FreeTierBody), as
	// Google did on 2026-10-20 for a key whose project had no billing.
	FreeTier = "free-tier"
	// FreeTierStart: the request itself is refused with that 429.
	FreeTierStart = "free-tier-start"
)

// Free-tier modes (SetFreeTier): where every request meets the free tier.
const (
	FreeTierOnStatus = "status" // accepted, then every status check answers the free tier's 429
	FreeTierOnStart  = "start"  // refused when sent
)

// FreeTierBody is Google's answer, word for word, for a model a free-tier
// project can't use (seen 2026-10-20): a 429 whose code is a word, not a
// number, with no status and no details.
func FreeTierBody(model string) map[string]any {
	return map[string]any{"error": map[string]any{"code": "too_many_requests",
		"message": "Rate limit exceeded for model " + model + " (limit: 0 input tokens per minute on Free Tier). Please upgrade your tier at https://ai.dev/rate-limit."}}
}

// Behavior is one scripted misbehavior for the next request.
type Behavior struct {
	Kind string `json:"kind"`
	// N: how many times (rate, spend, server) or polls (delay).
	N int `json:"n,omitempty"`
}

// Interaction is one request the fake received.
type Interaction struct {
	ID          string
	Model       string
	Prompt      string
	Images      int
	Videos      []string // file names used
	Seconds     int
	Resolution  string
	AspectRatio string
	Delivery    string
	Task        string
	Hash        string // the fake's own hash of what was asked
	Take        int    // how many times this exact request came
	Status      string
	Polls       int
	ReadyAfter  int
	Behavior    Behavior
	OutFile     string // files/… for uri delivery
	Video       []byte
	Deleted     bool
	Canceled    bool
	Body        map[string]any
}

type file struct {
	Name     string
	MIME     string
	Data     []byte
	Polls    int
	Deleted  bool
	Output   bool
	Expired  bool
	Uploaded time.Time
}

// Server is the fake API.
type Server struct {
	*httptest.Server
	Key string
	// Retired lists models it answers "not found" for (a model Google retired).
	Retired map[string]bool
	// ReadyAfter is how many polls a request needs before it finishes (default 1).
	ReadyAfter int

	mu       sync.Mutex
	freeTier string // FreeTierOnStatus, FreeTierOnStart or "" (SetFreeTier)
	calls    map[string]int
	script   []Behavior
	inter    map[string]*Interaction
	order    []string
	files    map[string]*file
	uploads  map[string]string // upload id -> mime
	takes    map[string]int
	nextID   int
	cache    map[string][]byte
	problems []string
	dir      string
	media    mediaState // pictures, speech, music, transcription, voice copies (media.go)
}

// New starts the fake on a random loopback port.
func New(key string) *Server {
	s, err := NewAt(key, "")
	if err != nil {
		panic(err)
	}
	return s
}

// NewAt starts the fake on a loopback address (such as 127.0.0.1:4890), or a random port when addr is empty.
func NewAt(key, addr string) (*Server, error) {
	dir, err := os.MkdirTemp("", "fakevideogen-")
	if err != nil {
		return nil, err
	}
	s := &Server{Key: key, Retired: map[string]bool{}, ReadyAfter: 1, calls: map[string]int{}, inter: map[string]*Interaction{}, files: map[string]*file{},
		uploads: map[string]string{}, takes: map[string]int{}, cache: map[string][]byte{}, dir: dir}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1beta/models", s.auth("models", s.models))
	mux.HandleFunc("POST /v1beta/interactions", s.auth("create", s.create))
	mux.HandleFunc("GET /v1beta/interactions/{id}", s.auth("get", s.get))
	mux.HandleFunc("POST /v1beta/interactions/{id}/cancel", s.auth("cancel", s.cancel))
	mux.HandleFunc("DELETE /v1beta/interactions/{id}", s.auth("delete", s.deleteInteraction))
	mux.HandleFunc("POST /upload/v1beta/files", s.auth("upload", s.upload))
	mux.HandleFunc("GET /v1beta/files/{name}", s.auth("file", s.fileGet))
	mux.HandleFunc("DELETE /v1beta/files/{name}", s.auth("file-delete", s.fileDelete))
	s.mediaRoutes(mux)
	// Not part of the real API: what happened, and scripts for a fake running as its own process.
	mux.HandleFunc("GET /__calls", func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		defer s.mu.Unlock()
		out := map[string]any{}
		for k, v := range s.calls {
			out[k] = v
		}
		alive := 0
		for _, it := range s.inter {
			if !it.Deleted {
				alive++
			}
		}
		files := 0
		for _, f := range s.files {
			if !f.Deleted && !f.Expired {
				files++
			}
		}
		out["interactionsKept"], out["filesKept"], out["problems"] = alive, files, s.problems
		writeJSON(w, http.StatusOK, out)
	})
	mux.HandleFunc("POST /__script", func(w http.ResponseWriter, r *http.Request) {
		var b []Behavior
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"error": err.Error()})
			return
		}
		s.Script(b...)
		writeJSON(w, http.StatusOK, map[string]any{"queued": len(b)})
	})
	if addr == "" {
		s.Server = httptest.NewServer(mux)
		return s, nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("the fake Gemini server only listens on loopback, not %s", host)
	}
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, err
	}
	s.Server = httptest.NewUnstartedServer(mux)
	_ = s.Server.Listener.Close()
	s.Server.Listener = l
	s.Server.Start()
	return s, nil
}

// Close stops the fake and removes its files.
func (s *Server) Close() {
	s.Server.Close()
	_ = os.RemoveAll(s.dir)
}

// SetFreeTier puts the key's project on Google's free tier for every request
// that has no other behavior: FreeTierOnStatus (accepted, then every status
// check answers the free tier's 429, as on 2026-10-20) or FreeTierOnStart
// (refused when sent); "" turns it off.
func (s *Server) SetFreeTier(mode string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.freeTier = mode
}

// Script queues behaviors for the next requests (one each, in order).
func (s *Server) Script(b ...Behavior) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.script = append(s.script, b...)
}

// Calls reports how many times an endpoint group was called.
func (s *Server) Calls(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[name]
}

// Interactions lists the requests received, oldest first.
func (s *Server) Interactions() []Interaction {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []Interaction{}
	for _, id := range s.order {
		out = append(out, *s.inter[id])
	}
	return out
}

// Kept counts interactions and files still stored (not deleted, not expired).
func (s *Server) Kept() (interactions, files int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, it := range s.inter {
		if !it.Deleted {
			interactions++
		}
	}
	for _, f := range s.files {
		if !f.Deleted && !f.Expired {
			files++
		}
	}
	return
}

// Problems lists things the client did that the real API would never see
// from a careful client (a key in an address, a key sent to the wrong place).
func (s *Server) Problems() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.problems...)
}

func (s *Server) auth(name string, h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls[name]++
		if strings.Contains(r.URL.RawQuery, "key=") {
			s.problems = append(s.problems, "a key in the address of "+r.URL.Path)
		}
		s.mu.Unlock()
		if r.Header.Get("x-goog-api-key") != s.Key {
			writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "API key not valid. Please pass a valid API key.", map[string]any{
				"@type": "type.googleapis.com/google.rpc.ErrorInfo", "reason": "API_KEY_INVALID", "domain": "googleapis.com"}))
			return
		}
		h(w, r)
	}
}

func gerr(code int, status, msg string, details ...map[string]any) map[string]any {
	e := map[string]any{"code": code, "status": status, "message": msg}
	if len(details) > 0 {
		e["details"] = details
	}
	return map[string]any{"error": e}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func (s *Server) models(w http.ResponseWriter, _ *http.Request) {
	ms := []map[string]any{{"name": "models/gemini-3.8-flash"}}
	for _, m := range []string{"gemini-omni-1.1-flash"} {
		if !s.Retired[m] {
			ms = append(ms, map[string]any{"name": "models/" + m})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": ms})
}

var markerRe = regexp.MustCompile(`\[fake:([a-z-]+)(?::(\d+))?\]`)

// next is the behavior for a new request: a marker in the prompt, or the script.
func (s *Server) next(prompt string) Behavior {
	if m := markerRe.FindStringSubmatch(prompt); m != nil {
		n, _ := strconv.Atoi(m[2])
		return Behavior{Kind: m[1], N: n}
	}
	if len(s.script) > 0 {
		b := s.script[0]
		s.script = s.script[1:]
		return b
	}
	return Behavior{}
}

func randID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type part struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Data     string `json:"data"`
	URI      string `json:"uri"`
	MimeType string `json:"mime_type"`
}

func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	var body struct {
		Model          string          `json:"model"`
		Input          json.RawMessage `json:"input"`
		Background     bool            `json:"background"`
		Store          *bool           `json:"store"`
		ResponseFormat struct {
			Type        string `json:"type"`
			AspectRatio string `json:"aspect_ratio"`
			Resolution  string `json:"resolution"`
			Duration    string `json:"duration"`
			Delivery    string `json:"delivery"`
		} `json:"response_format"`
		GenerationConfig struct {
			VideoConfig struct {
				Task string `json:"task"`
			} `json:"video_config"`
		} `json:"generation_config"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "Invalid JSON payload received."))
		return
	}
	var generic map[string]any
	_ = json.Unmarshal(raw, &generic)
	if body.Store != nil && !*body.Store && body.Background {
		writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "store=false is incompatible with background execution."))
		return
	}
	prompt, images := "", 0
	var videos []string
	h := sha256.New()
	var asText string
	if json.Unmarshal(body.Input, &asText) == nil {
		prompt = asText
	} else {
		var parts []part
		if err := json.Unmarshal(body.Input, &parts); err != nil {
			writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "Invalid value at 'input'."))
			return
		}
		for _, p := range parts {
			switch p.Type {
			case "text":
				prompt += p.Text
			case "image":
				images++
				b, err := base64.StdEncoding.DecodeString(p.Data)
				if err != nil || len(b) == 0 {
					writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "Invalid image data."))
					return
				}
				sum := sha256.Sum256(b)
				h.Write(sum[:])
			case "video":
				name := "files/" + strings.TrimPrefix(fileRe.FindString(p.URI), "/files/")
				s.mu.Lock()
				f := s.files[name]
				s.mu.Unlock()
				if f == nil || f.Deleted {
					writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "File "+name+" not found."))
					return
				}
				videos = append(videos, name)
				sum := sha256.Sum256(f.Data)
				h.Write(sum[:])
			}
		}
	}
	if s.Retired[body.Model] || body.Model == "" {
		writeJSON(w, http.StatusNotFound, gerr(404, "NOT_FOUND", "models/"+body.Model+" is not found for API version v1beta, or is not supported for interactions."))
		return
	}
	secs, _ := strconv.Atoi(strings.TrimSuffix(body.ResponseFormat.Duration, "s"))
	if secs == 0 {
		secs = 8
	}
	if secs < 3 || secs > 10 {
		writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "duration must be between 3s and 10s"))
		return
	}
	res := body.ResponseFormat.Resolution
	if res == "" {
		res = "720p"
	}
	ar := body.ResponseFormat.AspectRatio
	if ar == "" {
		ar = "16:9"
	}
	fmt.Fprintf(h, "|%s|%s|%d|%s|%s|%s", body.Model, prompt, secs, res, ar, body.GenerationConfig.VideoConfig.Task)
	hash := hex.EncodeToString(h.Sum(nil))
	s.mu.Lock()
	b := s.next(prompt)
	if b.Kind == "" {
		switch s.freeTier {
		case FreeTierOnStart:
			b.Kind = FreeTierStart
		case FreeTierOnStatus:
			b.Kind = FreeTier
		}
	}
	s.mu.Unlock()
	switch b.Kind {
	case FreeTierStart:
		writeJSON(w, http.StatusTooManyRequests, FreeTierBody(body.Model))
		return
	case Celebrity, Minor:
		what := map[string]string{Celebrity: "a recognizable person", Minor: "a minor"}[b.Kind]
		writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "Your prompt or input media was blocked by content-safety filters: it may show "+what+". Support codes: 39322892"))
		return
	case Rate, Spend, ServerError:
		if b.N > 1 {
			s.Script(Behavior{Kind: b.Kind, N: b.N - 1})
		}
		switch b.Kind {
		case Rate:
			rateLimited(w)
		case Spend:
			writeJSON(w, http.StatusTooManyRequests, gerr(429, "RESOURCE_EXHAUSTED", "Your project exceeded its spend limit for the current 10-minute window.",
				map[string]any{"@type": "type.googleapis.com/google.rpc.QuotaFailure", "violations": []any{map[string]any{"quotaMetric": "generativelanguage.googleapis.com/spend_per_10_minutes", "quotaId": "SpendPer10MinutesPerProject"}}}))
		default:
			writeJSON(w, http.StatusInternalServerError, gerr(500, "INTERNAL", "An internal error has occurred."))
		}
		return
	}
	s.mu.Lock()
	s.nextID++
	s.takes[hash]++
	ready := s.ReadyAfter
	if b.Kind == Delay && b.N > 0 {
		ready = b.N
	}
	delivery := body.ResponseFormat.Delivery
	if delivery == "" {
		delivery = "inline"
	}
	it := &Interaction{ID: fmt.Sprintf("v1_fake%04d%s", s.nextID, randID()[:6]), Model: body.Model, Prompt: prompt, Images: images, Videos: videos, Seconds: secs,
		Resolution: res, AspectRatio: ar, Delivery: delivery, Task: body.GenerationConfig.VideoConfig.Task, Hash: hash, Take: s.takes[hash],
		Status: "in_progress", ReadyAfter: ready, Behavior: b, Body: generic}
	s.inter[it.ID] = it
	s.order = append(s.order, it.ID)
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"id": it.ID, "status": it.Status, "object": "interaction", "model": it.Model, "created": time.Now().UTC().Format(time.RFC3339)})
}

// rateLimited answers Google's usual 429 for too many requests a minute (retry in a second).
func rateLimited(w http.ResponseWriter) {
	writeJSON(w, http.StatusTooManyRequests, gerr(429, "RESOURCE_EXHAUSTED", "Resource has been exhausted (e.g. check quota).",
		map[string]any{"@type": "type.googleapis.com/google.rpc.QuotaFailure", "violations": []any{map[string]any{"quotaMetric": "generativelanguage.googleapis.com/generate_requests_per_model", "quotaId": "GenerateRequestsPerMinutePerProjectPerModel"}}},
		map[string]any{"@type": "type.googleapis.com/google.rpc.RetryInfo", "retryDelay": "1s"}))
}

var fileRe = regexp.MustCompile(`/files/[A-Za-z0-9_-]+`)

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	it := s.inter[r.PathValue("id")]
	if it == nil || it.Deleted {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, gerr(404, "NOT_FOUND", "Interaction not found."))
		return
	}
	it.Polls++
	switch b := it.Behavior; {
	case b.Kind == PollError && it.Polls == 1:
		s.mu.Unlock()
		writeJSON(w, http.StatusServiceUnavailable, gerr(503, "UNAVAILABLE", "The service is currently unavailable."))
		return
	case b.Kind == FreeTier:
		// The request was accepted, but a free-tier project gets no answer about it, ever.
		model := it.Model
		s.mu.Unlock()
		writeJSON(w, http.StatusTooManyRequests, FreeTierBody(model))
		return
	case b.Kind == PollRate && (b.N <= 0 || it.Polls <= b.N):
		s.mu.Unlock()
		rateLimited(w)
		return
	}
	finishing := it.Status == "in_progress" && it.Polls >= it.ReadyAfter
	s.mu.Unlock()
	if finishing {
		if err := s.finish(it, r); err != nil {
			writeJSON(w, http.StatusInternalServerError, gerr(500, "INTERNAL", err.Error()))
			return
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	writeJSON(w, http.StatusOK, s.view(it, r))
}

// finish decides how a request ends and makes its video.
func (s *Server) finish(it *Interaction, r *http.Request) error {
	switch it.Behavior.Kind {
	case Child:
		s.mu.Lock()
		it.Status = "completed"
		s.mu.Unlock()
		return nil
	case Fail:
		s.mu.Lock()
		it.Status = "failed"
		s.mu.Unlock()
		return nil
	}
	secs := float64(it.Seconds)
	if it.Behavior.Kind == Partial {
		secs = float64(it.Seconds) / 2
	}
	video, err := s.mp4(it.Hash, it.Take, secs, it.Resolution, it.AspectRatio)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	it.Video = video
	it.Status = "completed"
	if it.Behavior.Kind == Partial {
		it.Status = "incomplete"
	}
	if it.Delivery == "uri" {
		s.nextID++
		name := fmt.Sprintf("files/out%04d", s.nextID)
		s.files[name] = &file{Name: name, MIME: "video/mp4", Data: video, Output: true, Expired: it.Behavior.Kind == Expired, Uploaded: time.Now()}
		it.OutFile = name
	}
	return nil
}

func (s *Server) base(r *http.Request) string { return "http://" + r.Host }

func (s *Server) view(it *Interaction, r *http.Request) map[string]any {
	v := map[string]any{"id": it.ID, "status": it.Status, "object": "interaction", "model": it.Model}
	switch it.Status {
	case "completed", "incomplete":
		var content []map[string]any
		if len(it.Video) > 0 {
			c := map[string]any{"type": "video", "mime_type": "video/mp4"}
			if it.Delivery == "uri" {
				c["uri"] = s.base(r) + "/v1beta/" + it.OutFile + ":download?alt=media"
			} else {
				c["data"] = base64.StdEncoding.EncodeToString(it.Video)
			}
			content = append(content, c)
		} else if it.Behavior.Kind == Child {
			content = append(content, map[string]any{"type": "text", "text": "I can't generate that idea: it may show a child. Support codes: 17301594"})
			v["errors"] = []any{map[string]any{"code": "content_filtered", "message": "1 video was filtered by the safety filters."}}
		}
		v["steps"] = []any{
			map[string]any{"type": "user_input", "content": []any{map[string]any{"type": "text", "text": it.Prompt}}},
			map[string]any{"type": "thought", "content": []any{map[string]any{"type": "text", "text": "Planning the shot."}}},
			map[string]any{"type": "model_output", "content": content},
		}
		vt := int64(0)
		if len(it.Video) > 0 {
			per := map[string]int64{"360p": 1931, "720p": 5792, "1080p": 8688, "4k": 17376}[it.Resolution]
			secs := it.Seconds
			if it.Behavior.Kind == Partial {
				secs = it.Seconds / 2
			}
			vt = per * int64(secs)
		}
		in := int64(len(it.Prompt)/4 + 1 + it.Images*1120 + len(it.Videos)*900)
		v["usage"] = map[string]any{"total_input_tokens": in, "total_output_tokens": vt + 12, "total_thought_tokens": 1500,
			"output_tokens_by_modality": []any{map[string]any{"modality": "VIDEO", "tokens": vt}, map[string]any{"modality": "TEXT", "tokens": 12}}}
	case "failed":
		v["errors"] = []any{map[string]any{"code": "internal", "message": "The video could not be generated."}}
	}
	return v
}

// mp4 makes (or reuses) a tiny real video for a request hash and take: its
// colour and tone come from the hash, the take is written into it, so the
// same request gives a different clip each time, like the real service.
func (s *Server) mp4(hash string, take int, secs float64, res, ar string) ([]byte, error) {
	key := fmt.Sprintf("%s-%d-%.2f-%s-%s", hash, take, secs, res, ar)
	s.mu.Lock()
	if b, ok := s.cache[key]; ok {
		s.mu.Unlock()
		return b, nil
	}
	s.mu.Unlock()
	short := map[string]int{"360p": 360, "720p": 720, "1080p": 1080, "4k": 1080}[res]
	if short == 0 {
		short = 360
	}
	long := short * 16 / 9
	w, h := long, short
	if ar == "9:16" {
		w, h = short, long
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("the fake needs ffmpeg on PATH: %w", err)
	}
	hue := int(hash[0])%6*60 - 180
	freq := 220 + int(hash[1])%200
	out := filepath.Join(s.dir, key+".mp4")
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("testsrc2=size=%dx%d:rate=24:duration=%.3f", w, h, secs),
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:duration=%.3f", freq, secs),
		"-vf", fmt.Sprintf("hue=h=%d", hue), "-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p",
		"-c:a", "aac", "-b:a", "48k", "-shortest", "-metadata", fmt.Sprintf("comment=fake %s take %d", hash[:12], take), "-movflags", "+faststart", out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, stderr.String())
	}
	b, err := os.ReadFile(out)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[key] = b
	s.mu.Unlock()
	return b, nil
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.inter[r.PathValue("id")]
	if it == nil || it.Deleted {
		writeJSON(w, http.StatusNotFound, gerr(404, "NOT_FOUND", "Interaction not found."))
		return
	}
	if it.Status == "in_progress" {
		it.Status, it.Canceled = "cancelled", true
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": it.ID, "status": it.Status})
}

func (s *Server) deleteInteraction(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it := s.inter[r.PathValue("id")]
	if it == nil || it.Deleted {
		writeJSON(w, http.StatusNotFound, gerr(404, "NOT_FOUND", "Interaction not found."))
		return
	}
	it.Deleted = true
	writeJSON(w, http.StatusOK, map[string]any{})
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	switch r.Header.Get("X-Goog-Upload-Command") {
	case "start":
		n, _ := strconv.Atoi(r.Header.Get("X-Goog-Upload-Header-Content-Length"))
		if n <= 0 {
			writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "Missing content length."))
			return
		}
		s.mu.Lock()
		s.nextID++
		id := strconv.Itoa(s.nextID)
		s.uploads[id] = r.Header.Get("X-Goog-Upload-Header-Content-Type")
		s.mu.Unlock()
		w.Header().Set("X-Goog-Upload-URL", s.base(r)+"/upload/v1beta/files?upload_id="+id+"&upload_protocol=resumable")
		w.Header().Set("X-Goog-Upload-Status", "active")
		writeJSON(w, http.StatusOK, map[string]any{})
	case "upload, finalize":
		id := r.URL.Query().Get("upload_id")
		s.mu.Lock()
		mime, ok := s.uploads[id]
		s.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusNotFound, gerr(404, "NOT_FOUND", "Upload not found."))
			return
		}
		data, _ := io.ReadAll(io.LimitReader(r.Body, 200<<20))
		sum := sha256.Sum256(data)
		s.mu.Lock()
		name := "files/in" + id
		s.files[name] = &file{Name: name, MIME: mime, Data: data, Uploaded: time.Now()}
		delete(s.uploads, id)
		s.mu.Unlock()
		writeJSON(w, http.StatusOK, map[string]any{"file": map[string]any{"name": name, "uri": s.base(r) + "/v1beta/" + name, "state": "PROCESSING",
			"mimeType": mime, "sizeBytes": strconv.Itoa(len(data)), "sha256Hash": base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(sum[:])))}})
	default:
		writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "Unknown upload command."))
	}
}

func (s *Server) fileGet(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	download := strings.HasSuffix(name, ":download")
	name = "files/" + strings.TrimSuffix(name, ":download")
	s.mu.Lock()
	f := s.files[name]
	if f == nil || f.Deleted || f.Expired {
		s.mu.Unlock()
		writeJSON(w, http.StatusNotFound, gerr(404, "NOT_FOUND", "File "+name+" not found or expired."))
		return
	}
	if download {
		data := f.Data
		s.mu.Unlock()
		if r.URL.Query().Get("alt") != "media" {
			writeJSON(w, http.StatusBadRequest, gerr(400, "INVALID_ARGUMENT", "Use alt=media."))
			return
		}
		w.Header().Set("Content-Type", f.MIME)
		_, _ = w.Write(data)
		return
	}
	f.Polls++
	state := "ACTIVE"
	if f.Polls == 1 && !f.Output {
		state = "PROCESSING"
	}
	sum := sha256.Sum256(f.Data)
	out := map[string]any{"name": f.Name, "uri": s.base(r) + "/v1beta/" + f.Name, "state": state, "mimeType": f.MIME, "sizeBytes": strconv.Itoa(len(f.Data)),
		"sha256Hash": base64.StdEncoding.EncodeToString([]byte(hex.EncodeToString(sum[:])))}
	s.mu.Unlock()
	writeJSON(w, http.StatusOK, out)
}

func (s *Server) fileDelete(w http.ResponseWriter, r *http.Request) {
	name := "files/" + r.PathValue("name")
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.files[name]
	if f == nil || f.Deleted {
		writeJSON(w, http.StatusNotFound, gerr(404, "NOT_FOUND", "File not found."))
		return
	}
	f.Deleted = true
	writeJSON(w, http.StatusOK, map[string]any{})
}

// Requests lists, oldest first, the prompt and settings of each request (for test messages).
func (s *Server) Requests() []string {
	var out []string
	for _, it := range s.Interactions() {
		out = append(out, fmt.Sprintf("%s %ds %s %s %q", it.ID, it.Seconds, it.Resolution, it.AspectRatio, it.Prompt))
	}
	sort.Strings(out)
	return out
}

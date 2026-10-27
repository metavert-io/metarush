// Package fakeheygen is an in-process fake of the parts of HeyGen's v3 API
// MetaRush uses for stock presenters (D273, D275): the account, public
// studio presenters, uploaded sound, videos of a presenter reading it (with
// idempotency keys), and deleting them. It follows HeyGen's reference as read
// on 25 Oct 2026, never talks to the network, accepts only the key it was
// created with, and makes small real MP4 clips with ffmpeg.
package fakeheygen

import (
	"bytes"
	"crypto/sha256"
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
	"sort"
	"strings"
	"sync"
	"time"
)

// Behaviors to queue with Next.
const (
	// NoCredit: the balance doesn't cover the video (402).
	NoCredit = "no-credit"
	// Moderation: the video fails HeyGen's content checks.
	Moderation = "moderation"
	// Lost: the video is made, but the answer never comes back.
	Lost = "lost"
	// ExpiredResult: the finished clip's link has expired.
	ExpiredResult = "expired-result"
	// Garbled: the video is made, but the answer can't be read.
	Garbled = "garbled"
	// GarbledUnmade: an answer that can't be read, and nothing was made.
	GarbledUnmade = "garbled-unmade"
)

// Stock are the fake's public studio presenters.
var Stock = []struct{ ID, Name, Gender string }{{"lk_amber_studio", "Amber", "female"}, {"lk_adam_studio", "Adam", "male"}, {"lk_gone_studio", "Gone", "male"}}

type asset struct {
	ID      string
	Data    []byte
	Deleted bool
}

type video struct {
	ID, Avatar, Asset, Title, Background, Aspect string
	Polls                                        int
	Deleted, Moderated, Expired                  bool
}

// Server is the fake API.
type Server struct {
	*httptest.Server
	Key string
	// Polls is how many checks a video needs before it is done (default 2).
	Polls int

	mu        sync.Mutex
	next      []string
	assets    map[string]*asset
	videos    map[string]*video
	idem      map[string]string // Idempotency-Key → video
	calls     map[string]int
	problems  []string
	lastAsset string
	n         int
	dir       string
}

// New starts the fake on a random loopback port.
func New(key string) *Server {
	s, err := NewAt(key, "")
	if err != nil {
		panic(err)
	}
	return s
}

// NewAt starts the fake on a loopback address ("" any port).
func NewAt(key, addr string) (*Server, error) {
	s := &Server{Key: key, Polls: 2, assets: map[string]*asset{}, videos: map[string]*video{}, idem: map[string]string{}, calls: map[string]int{}}
	d, err := os.MkdirTemp("", "fakeheygen-")
	if err != nil {
		return nil, err
	}
	s.dir = d
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v3/users/me", s.auth(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"data": map[string]any{"billing_type": "wallet"}})
	}))
	mux.HandleFunc("GET /v3/avatars/looks", s.auth(s.looks))
	mux.HandleFunc("POST /v3/assets", s.auth(s.upload))
	mux.HandleFunc("DELETE /v3/assets/{id}", s.auth(s.assetDelete))
	mux.HandleFunc("POST /v3/videos", s.auth(s.videoCreate))
	mux.HandleFunc("GET /v3/videos", s.auth(s.videoList))
	mux.HandleFunc("GET /v3/videos/{id}", s.auth(s.videoGet))
	mux.HandleFunc("DELETE /v3/videos/{id}", s.auth(s.videoDelete))
	mux.HandleFunc("GET /__results/{name}", s.result) // presigned links: no key
	srv := httptest.NewUnstartedServer(mux)
	if addr != "" {
		l, err := net.Listen("tcp", addr)
		if err != nil {
			return nil, err
		}
		srv.Listener.Close()
		srv.Listener = l
	}
	srv.Start()
	s.Server = srv
	return s, nil
}

// Close stops the fake and removes its files.
func (s *Server) Close() {
	s.Server.Close()
	_ = os.RemoveAll(s.dir)
}

// Next queues behaviors for the next requests they apply to.
func (s *Server) Next(b ...string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.next = append(s.next, b...)
}

func (s *Server) take(b string) bool {
	for i, x := range s.next {
		if x == b {
			s.next = append(s.next[:i], s.next[i+1:]...)
			return true
		}
	}
	return false
}

// Calls reports how many times an endpoint was called ("POST /v3/videos").
func (s *Server) Calls(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[name]
}

// Kept counts videos and uploaded sound not deleted.
func (s *Server) Kept() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, v := range s.videos {
		if !v.Deleted {
			n++
		}
	}
	for _, a := range s.assets {
		if !a.Deleted {
			n++
		}
	}
	return n
}

// Videos lists the videos made, oldest first (titles).
func (s *Server) Videos() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for _, v := range s.videos {
		out = append(out, v.ID+" "+v.Title)
	}
	sort.Strings(out)
	return out
}

// Problems lists what a careful client never does.
func (s *Server) Problems() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.problems...)
}

func (s *Server) id(prefix string) string {
	s.n++
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d-%d", prefix, s.n, time.Now().UnixNano())))
	return prefix + "_" + hex.EncodeToString(sum[:])[:14]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, status int, code, msg string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}})
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls[r.Method+" "+r.Pattern[len(r.Method)+1:]]++
		if strings.Contains(r.URL.RawQuery, s.Key) {
			s.problems = append(s.problems, "the key appeared in an address")
		}
		ok := r.Header.Get("X-Api-Key") == s.Key
		s.mu.Unlock()
		if !ok {
			apiErr(w, http.StatusUnauthorized, "unauthorized", "Invalid or expired API key. Verify your x-api-key header.")
			return
		}
		h(w, r)
	}
}

func (s *Server) looks(w http.ResponseWriter, r *http.Request) {
	if r.URL.Query().Get("ownership") != "public" {
		s.mu.Lock()
		s.problems = append(s.problems, "stock presenters asked for without ownership=public")
		s.mu.Unlock()
	}
	var out []any
	for _, p := range Stock {
		out = append(out, map[string]any{"id": p.ID, "name": p.Name, "gender": p.Gender, "avatar_type": "studio_avatar", "preview_image_url": "https://example.com/" + p.ID + ".png",
			"supported_api_engines": []string{"avatar_iv"}})
	}
	writeJSON(w, 200, map[string]any{"data": out, "has_more": false, "next_token": ""})
}

func (s *Server) upload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(40 << 20); err != nil {
		apiErr(w, 400, "invalid_parameter", "file is required")
		return
	}
	f, _, err := r.FormFile("file")
	if err != nil {
		apiErr(w, 400, "invalid_parameter", "file is required")
		return
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	if len(b) == 0 || len(b) > 32<<20 {
		apiErr(w, 400, "invalid_parameter", "file must be 1 byte to 32 MB")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if idem := r.Header.Get("Idempotency-Key"); idem != "" {
		if id, ok := s.idem["asset:"+idem]; ok {
			writeJSON(w, 200, map[string]any{"data": map[string]any{"asset_id": id, "url": s.URL + "/__assets/" + id, "mime_type": "audio/mp4", "size_bytes": len(b)}})
			return
		}
		defer func(idem string) { s.idem["asset:"+idem] = s.lastAsset }(idem)
	}
	id := s.id("as")
	s.lastAsset = id
	s.assets[id] = &asset{ID: id, Data: b}
	writeJSON(w, 200, map[string]any{"data": map[string]any{"asset_id": id, "url": s.URL + "/__assets/" + id, "mime_type": "audio/mp4", "size_bytes": len(b)}})
}

func (s *Server) assetDelete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.assets[r.PathValue("id")]
	if a == nil || a.Deleted {
		apiErr(w, 404, "asset_not_found", "Asset not found.")
		return
	}
	a.Deleted = true
	writeJSON(w, 200, map[string]any{"data": map[string]string{"id": a.ID}})
}

func (s *Server) videoCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Type         string            `json:"type"`
		AvatarID     string            `json:"avatar_id"`
		AudioAssetID string            `json:"audio_asset_id"`
		AudioURL     string            `json:"audio_url"`
		Script       string            `json:"script"`
		VoiceID      string            `json:"voice_id"`
		Title        string            `json:"title"`
		AspectRatio  string            `json:"aspect_ratio"`
		Resolution   string            `json:"resolution"`
		Background   map[string]string `json:"background"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		apiErr(w, 400, "invalid_parameter", "invalid body")
		return
	}
	s.mu.Lock()
	idem := r.Header.Get("Idempotency-Key")
	if id, ok := s.idem[idem]; ok && idem != "" {
		s.mu.Unlock()
		writeJSON(w, 200, map[string]any{"data": map[string]any{"video_id": id, "status": "waiting", "output_format": "mp4"}})
		return
	}
	if s.take(GarbledUnmade) {
		s.mu.Unlock()
		w.WriteHeader(200)
		_, _ = w.Write([]byte("<html>gateway</html>"))
		return
	}
	if s.take(NoCredit) {
		s.mu.Unlock()
		apiErr(w, 402, "insufficient_credit", "Your account has 5 credits but this video requires 10 credits.")
		return
	}
	switch {
	case in.Type != "avatar" || in.Script != "" || in.VoiceID != "":
		s.mu.Unlock()
		apiErr(w, 400, "invalid_parameter", "an avatar video reading supplied audio takes no script or voice")
		return
	case in.AvatarID == "lk_gone_studio":
		s.mu.Unlock()
		apiErr(w, 400, "avatar_not_usable", "This avatar is no longer available for new videos.")
		return
	}
	known := false
	for _, p := range Stock {
		known = known || p.ID == in.AvatarID
	}
	a := s.assets[in.AudioAssetID]
	if !known || a == nil || a.Deleted || in.AudioURL != "" {
		s.mu.Unlock()
		apiErr(w, 400, "invalid_parameter", "avatar_id and an uploaded audio_asset_id are required")
		return
	}
	id := s.id("v")
	v := &video{ID: id, Avatar: in.AvatarID, Asset: in.AudioAssetID, Title: in.Title, Aspect: in.AspectRatio, Moderated: s.take(Moderation), Expired: s.take(ExpiredResult)}
	if in.Background != nil {
		v.Background = in.Background["value"]
	}
	s.videos[id] = v
	if idem != "" {
		s.idem[idem] = id
	}
	lost := s.take(Lost)
	garbled := s.take(Garbled)
	s.mu.Unlock()
	if garbled {
		w.WriteHeader(200)
		_, _ = w.Write([]byte("<html>gateway</html>"))
		return
	}
	if lost {
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
	}
	writeJSON(w, 200, map[string]any{"data": map[string]any{"video_id": id, "status": "waiting", "output_format": "mp4"}})
}

func (s *Server) videoList(w http.ResponseWriter, r *http.Request) {
	title := r.URL.Query().Get("title")
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []any{}
	for _, v := range s.videos {
		if !v.Deleted && (title == "" || strings.Contains(v.Title, title)) {
			out = append(out, map[string]any{"id": v.ID, "title": v.Title, "status": "processing"})
		}
	}
	writeJSON(w, 200, map[string]any{"data": out, "has_more": false, "next_token": nil})
}

// Assets counts uploaded sound the fake keeps.
func (s *Server) Assets() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.assets {
		if !a.Deleted {
			n++
		}
	}
	return n
}

func (s *Server) videoGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.videos[r.PathValue("id")]
	if v == nil || v.Deleted {
		apiErr(w, 404, "not_found", "Video not found.")
		return
	}
	v.Polls++
	d := map[string]any{"id": v.ID, "status": "processing", "title": v.Title}
	if v.Polls >= max(1, s.Polls) {
		d["status"] = "completed"
		if v.Moderated {
			d["status"], d["failure_code"], d["failure_message"] = "failed", "content_policy_violation", "The content violates the policy."
		} else {
			d["video_url"] = s.URL + "/__results/" + v.ID + ".mp4?Expires=9999999999"
			d["duration"] = 2.0
		}
	}
	writeJSON(w, 200, map[string]any{"data": d})
}

func (s *Server) videoDelete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v := s.videos[r.PathValue("id")]
	if v == nil || v.Deleted {
		apiErr(w, 404, "video_not_found", "Video not found.")
		return
	}
	v.Deleted = true
	writeJSON(w, 200, map[string]any{"data": map[string]any{"id": v.ID, "deleted": true}})
}

func (s *Server) result(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("name"), ".mp4")
	s.mu.Lock()
	v := s.videos[id]
	var a *asset
	if v != nil {
		a = s.assets[v.Asset]
	}
	s.mu.Unlock()
	if v == nil || v.Deleted || v.Expired || a == nil {
		http.Error(w, "AccessDenied", http.StatusForbidden)
		return
	}
	out := filepath.Join(s.dir, id+".mp4")
	if _, err := os.Stat(out); err != nil {
		in := filepath.Join(s.dir, id+".sound")
		if err := os.WriteFile(in, a.Data, 0o600); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		color := "0x3b2a6f"
		if strings.HasPrefix(v.Background, "#") && len(v.Background) == 7 {
			color = "0x" + v.Background[1:]
		}
		ff, err := exec.LookPath("ffmpeg")
		if err != nil {
			http.Error(w, "the fake needs ffmpeg", 500)
			return
		}
		cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "color=c="+color+":size=320x180:rate=25:duration=30", "-i", in,
			"-vf", "drawbox=x=130:y=30:w=60:h=110:color=white@0.8:t=fill", "-map", "0:v:0", "-map", "1:a:0", "-c:v", "libx264", "-preset", "ultrafast",
			"-pix_fmt", "yuv420p", "-c:a", "aac", "-shortest", "-movflags", "+faststart", out)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		if err := cmd.Run(); err != nil {
			http.Error(w, "ffmpeg: "+stderr.String(), 500)
			return
		}
	}
	w.Header().Set("Content-Type", "video/mp4")
	http.ServeFile(w, r, out)
}

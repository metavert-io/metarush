// Package fakedid is an in-process fake of the parts of D-ID's API MetaRush
// uses for presenters (D273): credits, consent requests and readings,
// Express avatars made from a training take fetched from a link, uploaded
// sound, scenes of a presenter reading it, and deleting each of them (and
// the voice D-ID copies from a take). It follows D-ID's API reference as read
// on 25 Oct 2026. It never talks to the network beyond the links it is given
// (loopback only), accepts only the key it was created with, and makes small
// real MP4 clips with ffmpeg.
package fakedid

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
	"net/url"
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

// Behaviors to queue with Next.
const (
	// ConsentMismatch: the reading doesn't match the consent words.
	ConsentMismatch = "consent-mismatch"
	// RejectAvatar: the training take is turned down while it is made.
	RejectAvatar = "reject-avatar"
	// NoCredits: the account has no minutes left (402).
	NoCredits = "no-credits"
	// Moderation: a clip is declined by the moderation check (451).
	Moderation = "moderation"
	// LostAvatar: the presenter is made, but the answer never comes back.
	LostAvatar = "lost-avatar"
	// ExpiredResult: the finished clip's link has expired.
	ExpiredResult = "expired-result"
	// Garbled: a presenter or a clip is made, but the answer can't be read.
	Garbled = "garbled"
	// LostScene: a clip is made, but the answer never comes back.
	LostScene = "lost-scene"
)

type consent struct {
	ID, Text, Status, Name, Source string
	Deleted                        bool
	Mismatch                       bool
	Polls                          int
}

type avatar struct {
	ID, Name, Status, ConsentID, Source, VoiceID string
	GreenScreen, Deleted, Rejected               bool
	Polls                                        int
	CreatedAt                                    time.Time
}

type audio struct {
	ID      string
	Data    []byte
	Deleted bool
	Seconds float64
}

type scene struct {
	ID, AvatarID, AudioID, Name, UserData, Background, Status string
	Deleted, Moderated, Expired                               bool
	Polls                                                     int
}

// Server is the fake API.
type Server struct {
	*httptest.Server
	Key string
	// Polls is how many checks an avatar or a scene needs before it is done (default 2).
	Polls int

	mu       sync.Mutex
	next     []string
	consents map[string]*consent
	avatars  map[string]*avatar
	audios   map[string]*audio
	scenes   map[string]*scene
	voices   map[string]bool
	calls    map[string]int
	problems []string
	fetched  []string
	n        int
	dir      string
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
	s := &Server{Key: key, Polls: 2, consents: map[string]*consent{}, avatars: map[string]*avatar{}, audios: map[string]*audio{}, scenes: map[string]*scene{},
		voices: map[string]bool{}, calls: map[string]int{}}
	d, err := os.MkdirTemp("", "fakedid-")
	if err != nil {
		return nil, err
	}
	s.dir = d
	mux := http.NewServeMux()
	mux.HandleFunc("GET /credits", s.auth(s.credits))
	mux.HandleFunc("POST /consents", s.auth(s.consentCreate))
	mux.HandleFunc("GET /consents", s.auth(s.consentList))
	mux.HandleFunc("POST /consents/{id}", s.auth(s.consentUpload))
	mux.HandleFunc("GET /consents/{id}", s.auth(s.consentGet))
	mux.HandleFunc("DELETE /consents/{id}", s.auth(s.consentDelete))
	mux.HandleFunc("POST /scenes/avatars", s.auth(s.avatarCreate))
	mux.HandleFunc("GET /scenes/avatars", s.auth(s.avatarList))
	mux.HandleFunc("GET /scenes/avatars/{id}", s.auth(s.avatarGet))
	mux.HandleFunc("DELETE /scenes/avatars/{id}", s.auth(s.avatarDelete))
	mux.HandleFunc("POST /audios", s.auth(s.audioUpload))
	mux.HandleFunc("DELETE /audios/{id}", s.auth(s.audioDelete))
	mux.HandleFunc("POST /scenes", s.auth(s.sceneCreate))
	mux.HandleFunc("GET /scenes", s.auth(s.sceneList))
	mux.HandleFunc("GET /scenes/{id}", s.auth(s.sceneGet))
	mux.HandleFunc("DELETE /scenes/{id}", s.auth(s.sceneDelete))
	mux.HandleFunc("DELETE /tts/voices/{id}", s.auth(s.voiceDelete))
	// Files D-ID serves without a key: uploaded sound, finished clips.
	mux.HandleFunc("GET /__files/audios/{name}", s.audioFile)
	mux.HandleFunc("GET /__results/{name}", s.result)
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
	if s.dir != "" {
		_ = os.RemoveAll(s.dir)
	}
}

// Next queues behaviors for the next requests they apply to, in order.
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

// Calls reports how many times an endpoint was called ("POST /scenes").
func (s *Server) Calls(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[name]
}

// Kept counts what a careful client deletes after each request: scenes,
// uploaded sound and the voices copied from training takes.
func (s *Server) Kept() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, sc := range s.scenes {
		if !sc.Deleted {
			n++
		}
	}
	for _, a := range s.audios {
		if !a.Deleted {
			n++
		}
	}
	for _, ok := range s.voices {
		if ok {
			n++
		}
	}
	return n
}

// Presenters lists the presenters (Express avatars) the fake keeps.
func (s *Server) Presenters() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for id, a := range s.avatars {
		if !a.Deleted {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Consents lists the consent records the fake keeps.
func (s *Server) Consents() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []string
	for id, c := range s.consents {
		if !c.Deleted {
			out = append(out, id)
		}
	}
	sort.Strings(out)
	return out
}

// Fetched lists the links the fake downloaded (a training take, a reading).
func (s *Server) Fetched() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.fetched...)
}

// Problems lists what a careful client never does.
func (s *Server) Problems() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string{}, s.problems...)
}

// Presenter adds a finished presenter (for clips without making one first).
func (s *Server) Presenter(id, name string) { s.add(id, name, false) }

// GreenPresenter adds a finished presenter made against a green screen (a
// colour can go behind it).
func (s *Server) GreenPresenter(id, name string) { s.add(id, name, true) }

func (s *Server) add(id, name string, green bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.avatars[id] = &avatar{ID: id, Name: name, Status: "done", GreenScreen: green, CreatedAt: time.Now().UTC()}
}

func (s *Server) id(prefix string) string {
	s.n++
	sum := sha256.Sum256([]byte(fmt.Sprintf("%s-%d-%d", prefix, s.n, time.Now().UnixNano())))
	return prefix + "_" + hex.EncodeToString(sum[:])[:16]
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiErr(w http.ResponseWriter, status int, kind, desc string) {
	writeJSON(w, status, map[string]string{"kind": kind, "description": desc})
}

func (s *Server) auth(h http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.calls[r.Method+" "+r.Pattern[len(r.Method)+1:]]++
		if strings.Contains(r.URL.RawQuery, s.Key) || strings.Contains(r.URL.Path, s.Key) {
			s.problems = append(s.problems, "the key appeared in an address")
		}
		ok := r.Header.Get("Authorization") == "Basic "+s.Key
		s.mu.Unlock()
		if !ok {
			apiErr(w, http.StatusUnauthorized, "AuthorizationError", "user unauthenticated")
			return
		}
		h(w, r)
	}
}

func (s *Server) credits(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"credits": []any{}, "remaining": 172, "total": 180})
}

var languages = map[string]bool{"english": true, "spanish": true, "french": true, "german": true, "dutch": true, "italian": true, "portuguese": true, "polish": true,
	"turkish": true, "swedish": true, "indonesian": true, "filipino": true, "czech": true, "romanian": true, "danish": true, "malay": true, "slovak": true, "croatian": true}

func (s *Server) consentCreate(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "invalid body")
		return
	}
	lang, _ := in["language"].(string)
	if len(in) != 1 || !languages[lang] {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "language must be one of the supported languages")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.id("cst")
	code := 1000 + s.n*37%9000
	c := &consent{ID: id, Status: "created", Text: fmt.Sprintf("I, [user name] confirm that I have all the necessary rights or consents to use this footage. Maple river lantern %d.", code)}
	s.consents[id] = c
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "created_at": time.Now().UTC(), "created_by": "fake", "text": c.Text})
}

func (s *Server) consentView(c *consent) map[string]any {
	v := map[string]any{"id": c.ID, "created_at": time.Now().UTC(), "name": c.Name, "status": c.Status, "video_url": c.Source}
	if c.Status == "error" {
		v["error"] = map[string]string{"kind": "ConsentTextSimilarityError", "description": "Consent audio is not correspond to the consent"}
	}
	return v
}

func (s *Server) consentList(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []any
	for _, c := range s.consents {
		if !c.Deleted {
			out = append(out, s.consentView(c))
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"consents": out})
}

// fetch downloads a link the way D-ID does (only links on this computer here).
func (s *Server) fetch(link string) ([]byte, error) {
	u, err := url.Parse(link)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("not a link")
	}
	host := u.Hostname()
	if ip := net.ParseIP(host); host != "localhost" && (ip == nil || !ip.IsLoopback()) {
		return nil, fmt.Errorf("the fake downloads only from this computer")
	}
	res, err := http.Get(link)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("the link answered %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 1<<30))
}

// videoLink is the reference's pattern for a footage link.
var videoLink = regexp.MustCompile(`(https|s3|http):.*\.(mp4|MP4|mov|MOV|mpeg|MPEG)`)

func (s *Server) consentUpload(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "invalid body")
		return
	}
	src, _ := in["source_url"].(string)
	name, _ := in["name"].(string)
	if src == "" || name == "" {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "source_url and name are required")
		return
	}
	b, err := s.fetch(src)
	if err != nil || len(b) == 0 {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "could not download the consent video")
		return
	}
	if len(b) > 50<<20 {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "Video buffer size exceeds 50MB")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.fetched = append(s.fetched, "consent")
	c := s.consents[r.PathValue("id")]
	if c == nil || c.Deleted {
		apiErr(w, http.StatusNotFound, "NotFoundError", "consent not found")
		return
	}
	c.Name, c.Source, c.Status, c.Polls = name, "stored", "validating", 0
	c.Mismatch = s.take(ConsentMismatch)
	writeJSON(w, http.StatusOK, map[string]any{"id": c.ID})
}

func (s *Server) consentGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.consents[r.PathValue("id")]
	if c == nil || c.Deleted {
		apiErr(w, http.StatusNotFound, "NotFoundError", "consent not found")
		return
	}
	if c.Status == "validating" {
		c.Polls++
		if c.Polls >= 1 {
			c.Status = "done"
			if c.Mismatch {
				c.Status = "error"
			}
		}
	}
	writeJSON(w, http.StatusOK, s.consentView(c))
}

func (s *Server) consentDelete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c := s.consents[r.PathValue("id")]
	if c == nil || c.Deleted {
		apiErr(w, http.StatusNotFound, "NotFoundError", "consent not found")
		return
	}
	c.Deleted = true
	w.WriteHeader(http.StatusOK)
}

func (s *Server) avatarCreate(w http.ResponseWriter, r *http.Request) {
	var in map[string]any
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "invalid body")
		return
	}
	src, _ := in["source_url"].(string)
	cid, _ := in["consent_id"].(string)
	name, _ := in["name"].(string)
	green, _ := in["is_greenscreen"].(bool)
	if !videoLink.MatchString(src) {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "source_url must be a link to an mp4, mov or mpeg video")
		return
	}
	s.mu.Lock()
	c := s.consents[cid]
	noCredits := s.take(NoCredits)
	s.mu.Unlock()
	if noCredits {
		apiErr(w, http.StatusPaymentRequired, "InsufficientCreditsError", "not enough credits")
		return
	}
	if c == nil || c.Deleted || c.Status != "done" {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "consent is not valid")
		return
	}
	b, err := s.fetch(src)
	if err != nil || len(b) == 0 {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "could not download the footage")
		return
	}
	s.mu.Lock()
	s.fetched = append(s.fetched, "footage")
	id := s.id("avt")
	a := &avatar{ID: id, Name: name, Status: "created", ConsentID: cid, Source: "stored", GreenScreen: green, CreatedAt: time.Now().UTC(), Rejected: s.take(RejectAvatar)}
	s.avatars[id] = a
	lost := s.take(LostAvatar)
	garbled := s.take(Garbled)
	s.mu.Unlock()
	if garbled {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("<html>upstream busy</html>"))
		return
	}
	if lost {
		// Made, but the answer never comes back.
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "object": "scene_avatar", "status": a.Status, "created_at": a.CreatedAt})
}

func (s *Server) avatarView(a *avatar) map[string]any {
	v := map[string]any{"id": a.ID, "status": a.Status, "object": "scene_avatar", "name": a.Name, "consent_id": a.ConsentID, "created_at": a.CreatedAt}
	if a.VoiceID != "" {
		v["voice_id"] = a.VoiceID
	}
	if a.Status == "rejected" {
		v["error"] = map[string]string{"kind": "InvalidFaceError", "description": "no single clear face was found"}
	}
	return v
}

func (s *Server) avatarList(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id, a := range s.avatars {
		if !a.Deleted {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	start, _ := strconv.Atoi(r.URL.Query().Get("token"))
	out := []any{}
	next := ""
	for i := start; i < len(ids); i++ {
		if len(out) == limit {
			next = strconv.Itoa(i)
			break
		}
		out = append(out, s.avatarView(s.avatars[ids[i]]))
	}
	writeJSON(w, http.StatusOK, map[string]any{"avatars": out, "token": next})
}

func (s *Server) avatarGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.avatars[r.PathValue("id")]
	if a == nil || a.Deleted {
		apiErr(w, http.StatusNotFound, "NotFoundError", "avatar not found")
		return
	}
	if a.Status != "done" && a.Status != "rejected" {
		a.Polls++
		a.Status = "training-started"
		if a.Polls >= max(1, s.Polls) {
			a.Status = "done"
			if a.Rejected {
				a.Status = "rejected"
			} else {
				a.VoiceID = "vc_" + a.ID[4:]
				s.voices[a.VoiceID] = true // D-ID copies the voice from the take on its own
			}
		}
	}
	writeJSON(w, http.StatusOK, s.avatarView(a))
}

func (s *Server) avatarDelete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.avatars[r.PathValue("id")]
	if a == nil || a.Deleted {
		apiErr(w, http.StatusNotFound, "NotFoundError", "avatar not found")
		return
	}
	a.Deleted = true
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) voiceDelete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := r.PathValue("id")
	if !s.voices[id] {
		apiErr(w, http.StatusNotFound, "NotFoundError", "voice not found")
		return
	}
	s.voices[id] = false
	w.WriteHeader(http.StatusOK)
}

func (s *Server) audioUpload(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseMultipartForm(8 << 20); err != nil {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "invalid form")
		return
	}
	f, hdr, err := r.FormFile("audio")
	if err != nil {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "audio is required")
		return
	}
	defer f.Close()
	b, _ := io.ReadAll(f)
	if len(b) > 6<<20 {
		apiErr(w, http.StatusBadRequest, "InvalidFileSizeError", "audio file is larger than 6MB")
		return
	}
	if len(b) == 0 {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "empty file")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	id := s.id("aud")
	ext := filepath.Ext(hdr.Filename)
	if ext == "" {
		ext = ".bin"
	}
	a := &audio{ID: id, Data: b, Seconds: seconds(s.dir, id+ext, b)}
	s.audios[id] = a
	writeJSON(w, http.StatusCreated, map[string]any{"url": s.URL + "/__files/audios/" + id + ext, "id": id, "duration": a.Seconds})
}

// seconds is how long a sound file is (ffprobe), 0 when it can't tell.
func seconds(dir, name string, b []byte) float64 {
	p := filepath.Join(dir, name)
	if os.WriteFile(p, b, 0o600) != nil {
		return 0
	}
	ff, err := exec.LookPath("ffprobe")
	if err != nil {
		return 0
	}
	out, err := exec.Command(ff, "-v", "error", "-show_entries", "format=duration", "-of", "default=nw=1:nk=1", p).Output()
	if err != nil {
		return 0
	}
	v, _ := strconv.ParseFloat(strings.TrimSpace(string(out)), 64)
	return v
}

func (s *Server) audioDelete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	a := s.audios[r.PathValue("id")]
	if a == nil || a.Deleted {
		apiErr(w, http.StatusNotFound, "NotFoundError", "audio not found")
		return
	}
	a.Deleted = true
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) audioFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	id := strings.TrimSuffix(name, filepath.Ext(name))
	s.mu.Lock()
	a := s.audios[id]
	s.mu.Unlock()
	if a == nil || a.Deleted {
		http.NotFound(w, r)
		return
	}
	_, _ = w.Write(a.Data)
}

func (s *Server) sceneCreate(w http.ResponseWriter, r *http.Request) {
	var in struct {
		AvatarID string `json:"avatar_id"`
		Script   struct {
			Type      string `json:"type"`
			AudioURL  string `json:"audio_url"`
			Subtitles bool   `json:"subtitles"`
		} `json:"script"`
		Background map[string]any `json:"background"`
		Name       string         `json:"name"`
		UserData   string         `json:"user_data"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "invalid body")
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.take(NoCredits) {
		apiErr(w, http.StatusPaymentRequired, "InsufficientCreditsError", "not enough credits")
		return
	}
	av := s.avatars[in.AvatarID]
	if av == nil || av.Deleted || av.Status != "done" {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "avatar not found or not ready")
		return
	}
	if in.Script.Type != "audio" || !strings.HasPrefix(in.Script.AudioURL, s.URL+"/__files/audios/") {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "script must be audio with an uploaded audio_url")
		return
	}
	name := strings.TrimPrefix(in.Script.AudioURL, s.URL+"/__files/audios/")
	aid := strings.TrimSuffix(name, filepath.Ext(name))
	if a := s.audios[aid]; a == nil || a.Deleted {
		apiErr(w, http.StatusBadRequest, "BadRequestError", "audio not found")
		return
	}
	bg := ""
	if in.Background != nil {
		if !av.GreenScreen {
			apiErr(w, http.StatusBadRequest, "BadRequestError", "background can only be used for avatars with green screen background")
			return
		}
		bg, _ = in.Background["color"].(string)
	}
	id := s.id("scn")
	s.scenes[id] = &scene{ID: id, AvatarID: in.AvatarID, AudioID: aid, Name: in.Name, UserData: in.UserData, Background: bg, Status: "created",
		Moderated: s.take(Moderation), Expired: s.take(ExpiredResult)}
	switch {
	case s.take(Garbled):
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte("<html>upstream busy</html>"))
		return
	case s.take(LostScene):
		if hj, ok := w.(http.Hijacker); ok {
			if conn, _, err := hj.Hijack(); err == nil {
				_ = conn.Close()
				return
			}
		}
	}
	writeJSON(w, http.StatusCreated, map[string]any{"id": id, "object": "scene", "created_at": time.Now().UTC(), "status": "created"})
}

func (s *Server) sceneList(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []any{}
	for _, sc := range s.scenes {
		if !sc.Deleted {
			out = append(out, map[string]any{"id": sc.ID, "status": sc.Status, "name": sc.Name, "user_data": sc.UserData})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"scenes": out, "token": ""})
}

// Scenes counts the clips the fake keeps (not deleted).
func (s *Server) Scenes() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, sc := range s.scenes {
		if !sc.Deleted {
			n++
		}
	}
	return n
}

// Audios counts the uploaded sound the fake keeps (not deleted).
func (s *Server) Audios() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, a := range s.audios {
		if !a.Deleted {
			n++
		}
	}
	return n
}

func (s *Server) sceneGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.scenes[r.PathValue("id")]
	if sc == nil || sc.Deleted {
		apiErr(w, http.StatusNotFound, "NotFoundError", "scene not found")
		return
	}
	if sc.Status != "done" && sc.Status != "rejected" {
		sc.Polls++
		sc.Status = "started"
		if sc.Polls >= max(1, s.Polls) {
			sc.Status = "done"
			if sc.Moderated {
				sc.Status = "rejected"
			}
		}
	}
	v := map[string]any{"id": sc.ID, "status": sc.Status, "avatar_id": sc.AvatarID, "name": sc.Name, "user_data": sc.UserData}
	if sc.Status == "done" {
		v["result_url"] = s.URL + "/__results/" + sc.ID + ".mp4"
	}
	if sc.Status == "rejected" {
		v["error"] = map[string]string{"kind": "AudioModerationError", "description": "Automatic content moderation - contact support if you would like to submit for manual review"}
	}
	writeJSON(w, http.StatusOK, v)
}

func (s *Server) sceneDelete(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sc := s.scenes[r.PathValue("id")]
	if sc == nil || sc.Deleted {
		apiErr(w, http.StatusNotFound, "NotFoundError", "scene not found")
		return
	}
	sc.Deleted = true
	w.WriteHeader(http.StatusNoContent)
}

// result serves a finished clip: a small real video of the presenter (a
// test pattern) reading the uploaded sound.
func (s *Server) result(w http.ResponseWriter, r *http.Request) {
	id := strings.TrimSuffix(r.PathValue("name"), ".mp4")
	s.mu.Lock()
	sc := s.scenes[id]
	var a *audio
	if sc != nil {
		a = s.audios[sc.AudioID]
	}
	s.mu.Unlock()
	if sc == nil || sc.Deleted || sc.Expired || sc.Status != "done" || a == nil {
		http.Error(w, "AccessDenied", http.StatusForbidden)
		return
	}
	b, err := s.mp4(sc, a)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	_, _ = io.Copy(w, bytes.NewReader(b))
}

func (s *Server) mp4(sc *scene, a *audio) ([]byte, error) {
	out := filepath.Join(s.dir, sc.ID+".mp4")
	if b, err := os.ReadFile(out); err == nil {
		return b, nil
	}
	in := filepath.Join(s.dir, a.ID+".sound")
	if err := os.WriteFile(in, a.Data, 0o600); err != nil {
		return nil, err
	}
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, fmt.Errorf("the fake needs ffmpeg on PATH: %w", err)
	}
	secs := a.Seconds
	if secs <= 0 {
		secs = 2
	}
	color := "0x2a6f4e"
	if strings.HasPrefix(sc.Background, "#") && len(sc.Background) == 7 {
		color = "0x" + sc.Background[1:]
	}
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", fmt.Sprintf("color=c=%s:size=320x180:rate=25:duration=%.3f", color, secs), "-i", in,
		"-vf", "drawbox=x=120:y=40:w=80:h=100:color=white@0.8:t=fill", "-map", "0:v:0", "-map", "1:a:0",
		"-c:v", "libx264", "-preset", "ultrafast", "-pix_fmt", "yuv420p", "-c:a", "aac", "-b:a", "64k", "-shortest", "-movflags", "+faststart", out)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("ffmpeg: %v: %s", err, stderr.String())
	}
	return os.ReadFile(out)
}

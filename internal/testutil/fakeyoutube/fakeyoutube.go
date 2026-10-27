// Package fakeyoutube is a stand-in for Google's sign-in and the YouTube Data
// API on this computer, for tests of publishing (D305): the sign-in page
// answers at once as if the person allowed it, tokens are checked, videos
// arrive through resumable sessions (which can be made to drop), captions
// are kept. Nothing ever reaches YouTube.
package fakeyoutube

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
)

// Video is one video the fake took.
type Video struct {
	ID       string
	Title    string
	Desc     string
	Privacy  string
	Language string
	// Synthetic: the AI disclosure.
	Synthetic bool
	Bytes     []byte
	Captions  []Caption
}

// Caption is one captions file the fake took.
type Caption struct {
	Language, Name, Content string
}

// Server is the fake.
type Server struct {
	*httptest.Server
	mu sync.Mutex
	// ClientID and Secret are the client it accepts.
	ClientID, Secret string
	// Channel is the signed-in channel ("" none).
	ChannelID, Channel string
	// DenyConsent makes the sign-in page answer "access_denied".
	DenyConsent bool
	// DropAt makes the first piece reaching past this many bytes fail once.
	DropAt int64
	// KeepPrivate answers every upload as private (an unreviewed client).
	KeepPrivate bool
	// Quota refuses uploads with quotaExceeded.
	Quota bool
	// HangupFrom drops the connection (no answer at all) for every piece of an
	// upload from this byte on, and for questions about how far it got (0: never).
	HangupFrom int64
	Videos     []*Video
	Revoked    []string
	codes      map[string]string // code → PKCE challenge
	access     map[string]bool
	refresh    map[string]bool
	session    map[string]*upload
	seq        int
	dropped    bool
	// Requests counts requests by path.
	Requests map[string]int
}

type upload struct {
	meta  Video
	total int64
	data  []byte
	done  *Video
}

// New starts the fake; Base() is what METARUSH_YOUTUBE_BASE takes.
func New(t testing.TB) *Server {
	s := &Server{ClientID: "fake-client.apps.example", Secret: "fake-client-secret", ChannelID: "UCfake", Channel: "Garden Club",
		codes: map[string]string{}, access: map[string]bool{}, refresh: map[string]bool{}, session: map[string]*upload{}, Requests: map[string]int{}}
	s.Server = httptest.NewServer(http.HandlerFunc(s.handle))
	t.Cleanup(s.Close)
	return s
}

// Base is the address every endpoint lives under.
func (s *Server) Base() string { return s.URL }

func (s *Server) next(prefix string) string { s.seq++; return fmt.Sprintf("%s%d", prefix, s.seq) }

func jsonOut(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func apiError(w http.ResponseWriter, status int, reason, msg string) {
	jsonOut(w, status, map[string]any{"error": map[string]any{"code": status, "message": msg, "errors": []map[string]string{{"reason": reason}}}})
}

func (s *Server) authorized(r *http.Request) bool {
	tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	return ok && s.access[tok]
}

func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.Requests[r.URL.Path]++
	switch {
	case r.URL.Path == "/o/oauth2/v2/auth":
		q := r.URL.Query()
		redirect := q.Get("redirect_uri")
		u, err := url.Parse(redirect)
		if err != nil || u.Hostname() != "127.0.0.1" || q.Get("client_id") != s.ClientID || q.Get("code_challenge_method") != "S256" || q.Get("access_type") != "offline" {
			http.Error(w, "bad sign-in request", http.StatusBadRequest)
			return
		}
		v := url.Values{}
		v.Set("state", q.Get("state"))
		if s.DenyConsent {
			v.Set("error", "access_denied")
		} else {
			code := s.next("code-")
			s.codes[code] = q.Get("code_challenge")
			v.Set("code", code)
		}
		http.Redirect(w, r, redirect+"?"+v.Encode(), http.StatusFound)
	case r.URL.Path == "/token":
		_ = r.ParseForm()
		if r.Form.Get("client_id") != s.ClientID || r.Form.Get("client_secret") != s.Secret {
			jsonOut(w, http.StatusUnauthorized, map[string]string{"error": "invalid_client"})
			return
		}
		switch r.Form.Get("grant_type") {
		case "authorization_code":
			challenge, ok := s.codes[r.Form.Get("code")]
			sum := sha256.Sum256([]byte(r.Form.Get("code_verifier")))
			if !ok || base64.RawURLEncoding.EncodeToString(sum[:]) != challenge {
				jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
				return
			}
			delete(s.codes, r.Form.Get("code"))
			access, refresh := s.next("access-"), s.next("refresh-")
			s.access[access], s.refresh[refresh] = true, true
			jsonOut(w, http.StatusOK, map[string]any{"access_token": access, "refresh_token": refresh, "expires_in": 3599, "scope": "https://www.googleapis.com/auth/youtube.upload https://www.googleapis.com/auth/youtube.force-ssl"})
		case "refresh_token":
			if !s.refresh[r.Form.Get("refresh_token")] {
				jsonOut(w, http.StatusBadRequest, map[string]string{"error": "invalid_grant"})
				return
			}
			access := s.next("access-")
			s.access[access] = true
			jsonOut(w, http.StatusOK, map[string]any{"access_token": access, "expires_in": 3599})
		default:
			jsonOut(w, http.StatusBadRequest, map[string]string{"error": "unsupported_grant_type"})
		}
	case r.URL.Path == "/revoke":
		_ = r.ParseForm()
		tok := r.Form.Get("token")
		delete(s.refresh, tok)
		s.Revoked = append(s.Revoked, tok)
		w.WriteHeader(http.StatusOK)
	case r.URL.Path == "/youtube/v3/channels":
		if !s.authorized(r) {
			apiError(w, http.StatusUnauthorized, "authError", "Invalid Credentials")
			return
		}
		items := []any{}
		if s.Channel != "" {
			items = append(items, map[string]any{"id": s.ChannelID, "snippet": map[string]any{"title": s.Channel}})
		}
		jsonOut(w, http.StatusOK, map[string]any{"items": items})
	case r.URL.Path == "/upload/youtube/v3/videos" && r.Method == http.MethodPost:
		if !s.authorized(r) {
			apiError(w, http.StatusUnauthorized, "authError", "Invalid Credentials")
			return
		}
		if s.Quota {
			apiError(w, http.StatusForbidden, "quotaExceeded", "The request cannot be completed because you have exceeded your quota.")
			return
		}
		var meta struct {
			Snippet struct {
				Title       string `json:"title"`
				Description string `json:"description"`
				Language    string `json:"defaultLanguage"`
			} `json:"snippet"`
			Status struct {
				Privacy   string `json:"privacyStatus"`
				Synthetic bool   `json:"containsSyntheticMedia"`
			} `json:"status"`
		}
		_ = json.NewDecoder(r.Body).Decode(&meta)
		total, _ := strconv.ParseInt(r.Header.Get("X-Upload-Content-Length"), 10, 64)
		if r.URL.Query().Get("uploadType") != "resumable" || meta.Snippet.Title == "" || total <= 0 {
			apiError(w, http.StatusBadRequest, "invalid", "bad upload request")
			return
		}
		id := s.next("session-")
		s.session[id] = &upload{total: total, meta: Video{Title: meta.Snippet.Title, Desc: meta.Snippet.Description, Privacy: meta.Status.Privacy, Language: meta.Snippet.Language, Synthetic: meta.Status.Synthetic}}
		w.Header().Set("Location", s.URL+"/upload/session/"+id)
		w.WriteHeader(http.StatusOK)
	case strings.HasPrefix(r.URL.Path, "/upload/session/") && r.Method == http.MethodPut:
		if !s.authorized(r) {
			apiError(w, http.StatusUnauthorized, "authError", "Invalid Credentials")
			return
		}
		u := s.session[strings.TrimPrefix(r.URL.Path, "/upload/session/")]
		if u == nil {
			http.NotFound(w, r)
			return
		}
		if u.done != nil {
			jsonOut(w, http.StatusOK, map[string]any{"id": u.done.ID, "status": map[string]any{"privacyStatus": u.done.Privacy}})
			return
		}
		cr := r.Header.Get("Content-Range")
		if s.HangupFrom > 0 {
			var from int64 = -1
			if _, err := fmt.Sscanf(cr, "bytes %d-", &from); err != nil || from >= s.HangupFrom {
				hangup(w)
				return
			}
		}
		if strings.HasPrefix(cr, "bytes */") { // how far did it get?
			s.rangeAnswer(w, u)
			return
		}
		var from, to, total int64
		if _, err := fmt.Sscanf(cr, "bytes %d-%d/%d", &from, &to, &total); err != nil || from != int64(len(u.data)) || total != u.total {
			http.Error(w, "bad range", http.StatusBadRequest)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if s.DropAt > 0 && !s.dropped && to >= s.DropAt {
			s.dropped = true
			// Keep half the piece, then fail as a dropped connection would.
			u.data = append(u.data, body[:len(body)/2]...)
			http.Error(w, "backend error", http.StatusServiceUnavailable)
			return
		}
		u.data = append(u.data, body...)
		if int64(len(u.data)) < u.total {
			s.rangeAnswer(w, u)
			return
		}
		v := u.meta
		v.ID = s.next("vid")
		v.Bytes = u.data
		if s.KeepPrivate {
			v.Privacy = "private"
		}
		u.done = &v
		s.Videos = append(s.Videos, &v)
		jsonOut(w, http.StatusOK, map[string]any{"id": v.ID, "status": map[string]any{"privacyStatus": v.Privacy}})
	case r.URL.Path == "/upload/youtube/v3/captions" && r.Method == http.MethodPost:
		if !s.authorized(r) {
			apiError(w, http.StatusUnauthorized, "authError", "Invalid Credentials")
			return
		}
		_, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil {
			apiError(w, http.StatusBadRequest, "invalid", "not multipart")
			return
		}
		mr := multipart.NewReader(r.Body, params["boundary"])
		var meta struct {
			Snippet struct {
				VideoID  string `json:"videoId"`
				Language string `json:"language"`
				Name     string `json:"name"`
			} `json:"snippet"`
		}
		p1, err := mr.NextPart()
		if err != nil || json.NewDecoder(p1).Decode(&meta) != nil {
			apiError(w, http.StatusBadRequest, "invalid", "no metadata")
			return
		}
		p2, err := mr.NextPart()
		if err != nil {
			apiError(w, http.StatusBadRequest, "invalid", "no captions")
			return
		}
		content, _ := io.ReadAll(p2)
		var v *Video
		for _, x := range s.Videos {
			if x.ID == meta.Snippet.VideoID {
				v = x
			}
		}
		if v == nil || meta.Snippet.Language == "" {
			apiError(w, http.StatusNotFound, "videoNotFound", "video not found")
			return
		}
		v.Captions = append(v.Captions, Caption{Language: meta.Snippet.Language, Name: meta.Snippet.Name, Content: string(content)})
		jsonOut(w, http.StatusOK, map[string]any{"id": s.next("cap")})
	default:
		http.NotFound(w, r)
	}
}

// hangup drops the connection without an answer.
func hangup(w http.ResponseWriter) {
	if hj, ok := w.(http.Hijacker); ok {
		if c, _, err := hj.Hijack(); err == nil {
			_ = c.Close()
			return
		}
	}
	panic(http.ErrAbortHandler)
}

func (s *Server) rangeAnswer(w http.ResponseWriter, u *upload) {
	if n := len(u.data); n > 0 {
		w.Header().Set("Range", fmt.Sprintf("bytes=0-%d", n-1))
	}
	w.WriteHeader(http.StatusPermanentRedirect)
}

// Consent plays the person in the browser: it follows the sign-in page to
// the loopback address MetaRush waits on.
func Consent(rawURL string) error {
	res, err := http.Get(rawURL)
	if err != nil {
		return err
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("sign-in page: %s", res.Status)
	}
	return nil
}

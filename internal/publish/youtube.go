package publish

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/logbook"
)

// YouTube through the person's own sign-in (D305): Google's OAuth for
// installed apps — the person's browser on this computer, a loopback address
// for the answer, PKCE — with the owner's own client; the YouTube Data API's
// resumable upload for the video and captions.insert for each captions file.
// Chapters travel in the description, as YouTube reads them.

// YouTubeEndpoints are the addresses the YouTube client talks to.
type YouTubeEndpoints struct {
	Auth, Token, API, Upload, Revoke string
}

var realYouTube = YouTubeEndpoints{
	Auth:   "https://accounts.google.com/o/oauth2/v2/auth",
	Token:  "https://oauth2.googleapis.com/token",
	API:    "https://www.googleapis.com/youtube/v3",
	Upload: "https://www.googleapis.com/upload/youtube/v3",
	Revoke: "https://oauth2.googleapis.com/revoke",
}

// EnvYouTubeBase points the YouTube client at one server on this computer
// (internal/testutil/fakeyoutube); anything else is refused.
const EnvYouTubeBase = "METARUSH_YOUTUBE_BASE"

// YouTubeScopes are what the sign-in asks for: uploading videos, and
// managing their captions (which also reads the channel's name).
var YouTubeScopes = []string{"https://www.googleapis.com/auth/youtube.upload", "https://www.googleapis.com/auth/youtube.force-ssl"}

func youTubeEndpoints() (YouTubeEndpoints, error) {
	if b := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvYouTubeBase)), "/"); b != "" {
		if !loopback(b) {
			return YouTubeEndpoints{}, &Problem{Msg: EnvYouTubeBase + " may only name a server on this computer (a stand-in for YouTube)."}
		}
		return YouTubeEndpoints{Auth: b + "/o/oauth2/v2/auth", Token: b + "/token", API: b + "/youtube/v3", Upload: b + "/upload/youtube/v3", Revoke: b + "/revoke"}, nil
	}
	if realRefused() {
		return YouTubeEndpoints{}, &Problem{Msg: "The real YouTube is never used in tests: set " + EnvYouTubeBase + " to a stand-in."}
	}
	return realYouTube, nil
}

var ytHTTP = &http.Client{Timeout: 60 * time.Second}

// ytUploadHTTP has no overall timeout (a chunk of a large video takes as long
// as it takes); each request is bounded by its context.
var ytUploadHTTP = &http.Client{}

func randomString(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// ConnectYouTube signs a person in: their browser on this computer opens
// Google's sign-in, they choose the account and allow MetaRush to upload, and
// the answer comes back to a loopback address only this computer can reach.
// Only the lasting permission (the refresh token) is kept, in the keychain.
func ConnectYouTube(ctx context.Context, cfg *config.Config, person string, open Opener) (*Identity, error) {
	client, err := LoadClient(cfg, YouTube)
	if err != nil {
		return nil, err
	}
	ep, err := youTubeEndpoints()
	if err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, &Problem{Msg: "MetaRush couldn't wait for the sign-in on this computer.", Hint: "Try again; if it happens again, restart MetaRush."}
	}
	defer ln.Close()
	redirect := fmt.Sprintf("http://127.0.0.1:%d/callback", ln.Addr().(*net.TCPAddr).Port)
	verifier := randomString(48)
	sum := sha256.Sum256([]byte(verifier))
	state := randomString(24)
	challenge := base64.RawURLEncoding.EncodeToString(sum[:])
	// The activity log never keeps them, whatever records the address.
	logbook.RememberSecret(state)
	logbook.RememberSecret(verifier)
	logbook.RememberSecret(challenge)
	q := url.Values{}
	q.Set("client_id", client.ID)
	q.Set("redirect_uri", redirect)
	q.Set("response_type", "code")
	q.Set("scope", strings.Join(YouTubeScopes, " "))
	q.Set("code_challenge", challenge)
	q.Set("code_challenge_method", "S256")
	q.Set("state", state)
	q.Set("access_type", "offline")
	q.Set("prompt", "consent")
	authURL := ep.Auth + "?" + q.Encode()

	type answer struct{ code, err string }
	got := make(chan answer, 1)
	srv := &http.Server{ReadHeaderTimeout: 10 * time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/callback" {
			http.NotFound(w, r)
			return
		}
		v := r.URL.Query()
		a := answer{code: v.Get("code"), err: v.Get("error")}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if subtle.ConstantTimeCompare([]byte(v.Get("state")), []byte(state)) != 1 {
			// Not this sign-in's answer (another page, a stale tab, someone
			// else): ignored, and the wait goes on for the real one.
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, "<!doctype html><title>MetaRush</title><p style=\"font:16px system-ui;margin:3em\">That wasn't the sign-in MetaRush is waiting for. Finish signing in in the page MetaRush opened.</p>")
			return
		}
		if a.err == "" && a.code != "" {
			_, _ = io.WriteString(w, "<!doctype html><title>MetaRush</title><p style=\"font:16px system-ui;margin:3em\">Signed in. You can close this tab and go back to MetaRush.</p>")
		} else {
			_, _ = io.WriteString(w, "<!doctype html><title>MetaRush</title><p style=\"font:16px system-ui;margin:3em\">The sign-in didn't finish. Go back to MetaRush to try again.</p>")
		}
		select {
		case got <- a:
		default:
		}
	})}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	opened, err := open(ctx, authURL)
	if opened != nil {
		defer opened() // the page that sent the browser on goes with the sign-in
	}
	if err != nil {
		return nil, &Problem{Msg: "MetaRush couldn't open the sign-in page: " + err.Error(), Hint: "Open this address in a browser on this computer: " + authURL}
	}
	wait, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	var a answer
	select {
	case a = <-got:
	case <-wait.Done():
		return nil, &Problem{Msg: "The sign-in didn't finish within 10 minutes.", Hint: "Sign in again when you're ready."}
	}
	switch {
	case a.err == "access_denied":
		return nil, &Problem{Msg: "You didn't allow MetaRush to upload to YouTube, so nothing was saved."}
	case a.err != "" || a.code == "":
		return nil, &Problem{Msg: "Google didn't finish the sign-in (" + firstNonEmpty(a.err, "no answer") + ")."}
	}
	form := url.Values{}
	form.Set("code", a.code)
	form.Set("client_id", client.ID)
	form.Set("client_secret", client.Secret)
	form.Set("redirect_uri", redirect)
	form.Set("grant_type", "authorization_code")
	form.Set("code_verifier", verifier)
	tok, err := tokenRequest(ctx, ep, form, "finish signing in")
	if err != nil {
		return nil, err
	}
	if tok.Refresh == "" {
		return nil, &Problem{Msg: "Google signed you in but gave no lasting permission, so MetaRush couldn't keep it.", Hint: "Sign in again; if it happens again, remove MetaRush under your Google account's third-party access and sign in once more."}
	}
	acct, err := ytChannel(ctx, ep, tok.Access)
	if err != nil {
		return nil, err
	}
	acct.Scopes = strings.Fields(tok.Scope)
	acct.At = time.Now().UTC()
	if err := saveSignIn(cfg, YouTube, person, signIn{Refresh: tok.Refresh, Identity: *acct}); err != nil {
		return nil, &Problem{Msg: "MetaRush couldn't keep the sign-in in this computer's keychain.", Hint: "Check that the keychain is unlocked, then sign in again."}
	}
	return acct, nil
}

type tokenAnswer struct {
	Access  string `json:"access_token"`
	Refresh string `json:"refresh_token"`
	Expires int    `json:"expires_in"`
	Scope   string `json:"scope"`
	Error   string `json:"error"`
	Desc    string `json:"error_description"`
}

func tokenRequest(ctx context.Context, ep YouTubeEndpoints, form url.Values, what string) (*tokenAnswer, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ep.Token, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	start := time.Now()
	res, err := ytHTTP.Do(req)
	call := logbook.ProviderCall{Provider: YouTube, Title: "YouTube", What: what, Method: http.MethodPost, Endpoint: endpointPath(ep.Token)}
	if err != nil {
		logbook.ProviderDone(ctx, call, errors.New(netWords(err))) // never the error's own text (it names the address)
		return nil, &Problem{Msg: "MetaRush couldn't reach Google to " + what + " (" + netWords(err) + ").", Retry: true}
	}
	defer res.Body.Close()
	body := readAll(res.Body, 1<<20)
	call.Status, call.Duration, call.Bytes = res.StatusCode, time.Since(start), len(body)
	var t tokenAnswer
	_ = json.Unmarshal(body, &t)
	if t.Access != "" {
		logbook.RememberSecret(t.Access)
	}
	if t.Refresh != "" {
		logbook.RememberSecret(t.Refresh)
	}
	if res.StatusCode != http.StatusOK || t.Access == "" {
		logbook.ProviderDone(ctx, call, errors.New(firstNonEmpty(t.Error, res.Status)))
		if t.Error == "invalid_grant" {
			return nil, &Problem{Status: res.StatusCode, Msg: "Your YouTube sign-in on this computer has ended (it was taken back or ran out).", Hint: "Sign in again on this computer.", Command: "metarush publish connect youtube"}
		}
		if t.Error == "invalid_client" || t.Error == "unauthorized_client" {
			return nil, &Problem{Status: res.StatusCode, Msg: "Google doesn't accept the YouTube client set up on this computer.", Hint: "An owner sets it up again with the client ID and secret from their Google Cloud project."}
		}
		return nil, &Problem{Status: res.StatusCode, Msg: "Google refused to " + what + " (" + firstNonEmpty(t.Desc, t.Error, res.Status) + ")."}
	}
	logbook.ProviderDone(ctx, call, nil)
	return &t, nil
}

func endpointPath(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return u.Path
}

// ytChannel is the channel a sign-in posts to.
func ytChannel(ctx context.Context, ep YouTubeEndpoints, access string) (*Identity, error) {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, ep.API+"/channels?part=snippet&mine=true", nil)
	req.Header.Set("Authorization", "Bearer "+access)
	var out struct {
		Items []struct {
			ID      string `json:"id"`
			Snippet struct {
				Title string `json:"title"`
			} `json:"snippet"`
		} `json:"items"`
	}
	if err := ytDo(ctx, req, "look up the channel", &out); err != nil {
		return nil, err
	}
	if len(out.Items) == 0 {
		return nil, &Problem{Msg: "This Google account has no YouTube channel yet.", Hint: "Make one on youtube.com, then sign in again."}
	}
	return &Identity{Platform: YouTube, ChannelID: out.Items[0].ID, Channel: out.Items[0].Snippet.Title}, nil
}

// ytDo sends a request and reads a JSON answer, turning YouTube's refusals
// into plain words.
func ytDo(ctx context.Context, req *http.Request, what string, out any) error {
	start := time.Now()
	res, err := ytHTTP.Do(req)
	call := logbook.ProviderCall{Provider: YouTube, Title: "YouTube", What: what, Method: req.Method, Endpoint: req.URL.Path}
	if err != nil {
		logbook.ProviderDone(ctx, call, errors.New(netWords(err))) // never the error's own text (it names the address)
		return &Problem{Msg: "MetaRush couldn't reach YouTube to " + what + " (" + netWords(err) + ").", Retry: true}
	}
	defer res.Body.Close()
	body := readAll(res.Body, 4<<20)
	call.Status, call.Duration, call.Bytes = res.StatusCode, time.Since(start), len(body)
	if res.StatusCode/100 != 2 {
		p := ytProblem(res.StatusCode, body, what)
		logbook.ProviderDone(ctx, call, p)
		return p
	}
	logbook.ProviderDone(ctx, call, nil)
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			return &Problem{Msg: "YouTube's answer to " + what + " couldn't be read."}
		}
	}
	return nil
}

// ytProblem reads YouTube's error answer.
func ytProblem(status int, body []byte, what string) *Problem {
	var e struct {
		Error struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
			Errors  []struct {
				Reason string `json:"reason"`
			} `json:"errors"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &e)
	reason := ""
	if len(e.Error.Errors) > 0 {
		reason = e.Error.Errors[0].Reason
	}
	p := &Problem{Status: status, Msg: "YouTube refused to " + what + " (" + firstNonEmpty(e.Error.Message, http.StatusText(status)) + ")."}
	switch reason {
	case "quotaExceeded", "rateLimitExceeded", "userRateLimitExceeded":
		p.Msg = "YouTube's daily allowance for the client set up on this computer is used up."
		p.Hint = "Try again tomorrow: it starts again at midnight Pacific time."
	case "uploadLimitExceeded":
		p.Msg = "This channel has reached YouTube's upload limit for now."
		p.Hint = "Try again later."
	case "youtubeSignupRequired":
		p.Msg = "This Google account has no YouTube channel yet."
		p.Hint = "Make one on youtube.com, then sign in again."
	case "insufficientPermissions", "forbidden", "authError":
		p.Hint = "Sign in again, and allow MetaRush to upload videos and manage their captions."
	}
	if status == http.StatusUnauthorized {
		p.Hint, p.Command = "Sign in again on this computer.", "metarush publish connect youtube"
	}
	if status >= 500 || status == http.StatusTooManyRequests {
		p.Retry = true
	}
	return p
}

// ForgetYouTube takes a person's sign-in back at Google, then forgets it here.
func ForgetYouTube(ctx context.Context, cfg *config.Config, person string) (*Identity, error) {
	s, err := loadSignIn(cfg, YouTube, person)
	if err != nil {
		return nil, err
	}
	if ep, err := youTubeEndpoints(); err == nil {
		form := url.Values{}
		form.Set("token", s.Refresh)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ep.Revoke, strings.NewReader(form.Encode()))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		if res, err := ytHTTP.Do(req); err == nil {
			res.Body.Close()
			logbook.ProviderDone(ctx, logbook.ProviderCall{Provider: YouTube, Title: "YouTube", What: "take back the sign-in", Method: http.MethodPost, Endpoint: req.URL.Path, Status: res.StatusCode}, nil)
		}
	}
	a := s.Identity
	return &a, Forget(cfg, YouTube, person)
}

// accessFor turns a person's kept sign-in into a short-lived access token.
func accessFor(ctx context.Context, cfg *config.Config, ep YouTubeEndpoints, person string) (string, *Identity, error) {
	client, err := LoadClient(cfg, YouTube)
	if err != nil {
		return "", nil, err
	}
	s, err := loadSignIn(cfg, YouTube, person)
	if err != nil {
		return "", nil, err
	}
	form := url.Values{}
	form.Set("client_id", client.ID)
	form.Set("client_secret", client.Secret)
	form.Set("refresh_token", s.Refresh)
	form.Set("grant_type", "refresh_token")
	tok, err := tokenRequest(ctx, ep, form, "renew the sign-in")
	if err != nil {
		return "", nil, err
	}
	a := s.Identity
	return tok.Access, &a, nil
}

// RetryWait is the first wait before asking how far an upload got after a
// dropped piece (it doubles each time; tests shorten it).
var RetryWait = 250 * time.Millisecond

// UploadChunk is the size of each piece of a video sent (a multiple of 256 KiB).
var UploadChunk int64 = 16 << 20

// UploadYouTube posts a video as the person: the resumable upload, then each
// captions file. A dropped connection resumes where YouTube says it got to;
// v.Session goes on with an upload cut short (YouTube keeps one about a day).
func UploadYouTube(ctx context.Context, cfg *config.Config, person string, v Video) (*Posted, error) {
	ep, err := youTubeEndpoints()
	if err != nil {
		return nil, err
	}
	access, _, err := accessFor(ctx, cfg, ep, person)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(v.Path)
	if err != nil {
		return nil, &Problem{Msg: "MetaRush couldn't open the video to send it."}
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	total := st.Size()
	var video *ytVideo
	if v.Session != "" {
		logbook.RememberSecret(v.Session)
		if !sessionOK(v.Session, ep) {
			return nil, &Problem{Msg: "The unfinished upload's address isn't YouTube's, so nothing was sent to it.", Gone: true}
		}
		video, err = ytResume(ctx, f, v.Session, access, total)
	} else {
		video, err = ytStart(ctx, ep, f, access, total, v)
	}
	if err != nil {
		return nil, err
	}
	posted := &Posted{VideoID: video.ID, URL: "https://www.youtube.com/watch?v=" + url.QueryEscape(video.ID)}
	if v.Mine != nil && !v.Mine() {
		posted.NotMine = true // its owner sends the captions
		return posted, nil
	}
	if video.Status.PrivacyStatus != "" && video.Status.PrivacyStatus != v.Privacy {
		posted.Notes = append(posted.Notes, fmt.Sprintf("YouTube kept the video %s instead of %s: videos sent through a client YouTube hasn't reviewed stay private until you change them in YouTube Studio.", video.Status.PrivacyStatus, v.Privacy))
	}
	for _, c := range v.Captions {
		if err := ytCaption(ctx, ep, access, video.ID, c); err != nil {
			posted.Notes = append(posted.Notes, fmt.Sprintf("The %s captions weren't taken: %s", firstNonEmpty(c.Name, c.Language), err.Error()))
			continue
		}
		posted.Captions = append(posted.Captions, c.Language)
	}
	return posted, nil
}

// ytStart opens a resumable session for the video and sends the file.
func ytStart(ctx context.Context, ep YouTubeEndpoints, f *os.File, access string, total int64, v Video) (*ytVideo, error) {
	meta := map[string]any{
		"snippet": map[string]any{"title": v.Title, "description": v.Description, "tags": nonNilTags(v.Tags), "defaultLanguage": v.Language, "defaultAudioLanguage": v.Language},
		"status":  map[string]any{"privacyStatus": v.Privacy, "selfDeclaredMadeForKids": false, "containsSyntheticMedia": v.Synthetic},
	}
	mb, _ := json.Marshal(meta)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ep.Upload+"/videos?uploadType=resumable&part=snippet,status", bytes.NewReader(mb))
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "application/json; charset=UTF-8")
	req.Header.Set("X-Upload-Content-Length", strconv.FormatInt(total, 10))
	req.Header.Set("X-Upload-Content-Type", "video/mp4")
	start := time.Now()
	res, err := ytHTTP.Do(req)
	call := logbook.ProviderCall{Provider: YouTube, Title: "YouTube", What: "start sending a video", Method: http.MethodPost, Endpoint: req.URL.Path}
	if err != nil {
		logbook.ProviderDone(ctx, call, errors.New(netWords(err))) // never the error's own text (it names the address)
		return nil, &Problem{Msg: "MetaRush couldn't reach YouTube to send the video (" + netWords(err) + ").", Retry: true}
	}
	body := readAll(res.Body, 1<<20)
	res.Body.Close()
	call.Status, call.Duration = res.StatusCode, time.Since(start)
	session := res.Header.Get("Location")
	if res.StatusCode/100 != 2 || session == "" {
		p := ytProblem(res.StatusCode, body, "take the video")
		logbook.ProviderDone(ctx, call, p)
		return nil, p
	}
	// The session's address lets anyone send to it: never in the log, and
	// the sign-in goes only to YouTube's own upload address.
	logbook.RememberSecret(session)
	logbook.ProviderDone(ctx, call, nil)
	if !sessionOK(session, ep) {
		return nil, &Problem{Msg: "YouTube answered with an upload address that isn't its own, so nothing was sent."}
	}
	if v.Started != nil {
		v.Started(session)
	}
	return ytSend(ctx, f, session, access, total, 0)
}

// ytResume asks an unfinished session how far it got and sends the rest (or
// takes the video, when it was all there).
func ytResume(ctx context.Context, f *os.File, session, access string, total int64) (*ytVideo, error) {
	q, err := http.NewRequestWithContext(ctx, http.MethodPut, session, nil)
	if err != nil {
		return nil, &Problem{Msg: "The unfinished upload can't be found again.", Gone: true}
	}
	q.Header.Set("Authorization", "Bearer "+access)
	q.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", total))
	res, err := ytUploadHTTP.Do(q)
	if err != nil {
		return nil, &Problem{Msg: "MetaRush couldn't reach YouTube to go on sending the video (" + netWords(err) + ").", Retry: true}
	}
	b := readAll(res.Body, 1<<20)
	res.Body.Close()
	switch {
	case res.StatusCode/100 == 2:
		var v ytVideo
		if json.Unmarshal(b, &v) != nil || v.ID == "" {
			return nil, &Problem{Msg: "YouTube took the video but its answer couldn't be read."}
		}
		return &v, nil
	case res.StatusCode == http.StatusPermanentRedirect:
		return ytSend(ctx, f, session, access, total, rangeEnd(res.Header.Get("Range")))
	case res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone:
		return nil, &Problem{Status: res.StatusCode, Msg: "YouTube no longer has the unfinished upload (it keeps one about a day), so it can't go on.", Gone: true}
	case res.StatusCode >= 500 || res.StatusCode == http.StatusTooManyRequests:
		return nil, &Problem{Status: res.StatusCode, Msg: "YouTube was busy, so sending the video couldn't go on yet.", Retry: true}
	}
	return nil, ytProblem(res.StatusCode, b, "go on taking the video")
}

func nonNilTags(t []string) []string {
	if t == nil {
		return []string{}
	}
	return t
}

type ytVideo struct {
	ID     string `json:"id"`
	Status struct {
		PrivacyStatus string `json:"privacyStatus"`
	} `json:"status"`
}

// ytSend sends the file in pieces to a resumable session; after a dropped
// connection or a busy server it asks how far YouTube got and goes on.
func ytSend(ctx context.Context, f *os.File, session, access string, total, offset int64) (*ytVideo, error) {
	report := progressOf(ctx)
	tries := 0
	if report != nil && offset > 0 {
		report(offset, total)
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, &Problem{Msg: "Sending the video was stopped.", Retry: true}
		}
		n := min(UploadChunk, total-offset)
		var body io.Reader = io.NewSectionReader(f, offset, n)
		req, _ := http.NewRequestWithContext(ctx, http.MethodPut, session, body)
		req.ContentLength = n
		req.Header.Set("Authorization", "Bearer "+access)
		req.Header.Set("Content-Type", "video/mp4")
		if total == 0 {
			req.Header.Set("Content-Range", "bytes */0")
		} else {
			req.Header.Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", offset, offset+n-1, total))
		}
		res, err := ytUploadHTTP.Do(req)
		if err == nil && res.StatusCode/100 == 2 {
			b := readAll(res.Body, 1<<20)
			res.Body.Close()
			var v ytVideo
			if json.Unmarshal(b, &v) != nil || v.ID == "" {
				return nil, &Problem{Msg: "YouTube took the video but its answer couldn't be read."}
			}
			if report != nil {
				report(total, total)
			}
			logbook.ProviderDone(ctx, logbook.ProviderCall{Provider: YouTube, Title: "YouTube", What: "send a video", Method: http.MethodPut, Endpoint: "/upload/youtube/v3/videos", Status: res.StatusCode, Bytes: int(total)}, nil)
			return &v, nil
		}
		if err == nil && res.StatusCode == http.StatusPermanentRedirect {
			next := rangeEnd(res.Header.Get("Range"))
			res.Body.Close()
			offset = next
			tries = 0
			if report != nil {
				report(offset, total)
			}
			continue
		}
		if err == nil {
			b := readAll(res.Body, 1<<20)
			res.Body.Close()
			if res.StatusCode < 500 && res.StatusCode != http.StatusTooManyRequests {
				return nil, ytProblem(res.StatusCode, b, "take the video")
			}
		}
		// Dropped or busy: ask how far it got, then go on.
		tries++
		if tries > 6 {
			return nil, &Problem{Msg: "Sending the video kept failing, so MetaRush stopped.", Hint: "Check the internet connection and send it again.", Retry: true}
		}
		select {
		case <-ctx.Done():
			return nil, &Problem{Msg: "Sending the video was stopped.", Retry: true}
		case <-time.After(time.Duration(1<<min(tries, 5)) * RetryWait):
		}
		q, _ := http.NewRequestWithContext(ctx, http.MethodPut, session, nil)
		q.Header.Set("Authorization", "Bearer "+access)
		q.Header.Set("Content-Range", fmt.Sprintf("bytes */%d", total))
		if qr, err := ytUploadHTTP.Do(q); err == nil {
			b := readAll(qr.Body, 1<<20)
			qr.Body.Close()
			switch {
			case qr.StatusCode/100 == 2:
				var v ytVideo
				if json.Unmarshal(b, &v) == nil && v.ID != "" {
					return &v, nil
				}
			case qr.StatusCode == http.StatusPermanentRedirect:
				offset = rangeEnd(qr.Header.Get("Range"))
			}
		}
	}
}

// sessionOK reports an upload session at YouTube's own upload address (the
// scheme and host of its upload endpoint): only there does the sign-in go.
func sessionOK(session string, ep YouTubeEndpoints) bool {
	s, err1 := url.Parse(session)
	u, err2 := url.Parse(ep.Upload)
	return err1 == nil && err2 == nil && s.User == nil && s.Scheme == u.Scheme && strings.EqualFold(s.Host, u.Host)
}

// netWords says why a request didn't get through, in plain words (never the
// error's own text, which names the address).
func netWords(err error) string {
	var ne net.Error
	switch {
	case errors.Is(err, context.Canceled):
		return "it was stopped"
	case errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()):
		return "it didn't answer in time"
	}
	return "the connection failed"
}

// rangeEnd reads "bytes=0-1234" as the next byte to send (1235).
func rangeEnd(h string) int64 {
	_, r, ok := strings.Cut(h, "-")
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(strings.TrimSpace(r), 10, 64)
	if err != nil {
		return 0
	}
	return n + 1
}

// ytCaption sends one captions file for a video.
func ytCaption(ctx context.Context, ep YouTubeEndpoints, access, videoID string, c Caption) error {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	h := textproto.MIMEHeader{}
	h.Set("Content-Type", "application/json; charset=UTF-8")
	part, _ := w.CreatePart(h)
	meta, _ := json.Marshal(map[string]any{"snippet": map[string]any{"videoId": videoID, "language": c.Language, "name": c.Name, "isDraft": false}})
	_, _ = part.Write(meta)
	h = textproto.MIMEHeader{}
	ct := "text/vtt"
	if c.Format == "srt" {
		ct = "application/x-subrip"
	}
	h.Set("Content-Type", ct)
	part, _ = w.CreatePart(h)
	_, _ = io.WriteString(part, c.Content)
	_ = w.Close()
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, ep.Upload+"/captions?uploadType=multipart&part=snippet", &buf)
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Content-Type", "multipart/related; boundary="+w.Boundary())
	return ytDo(ctx, req, "add captions", nil)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

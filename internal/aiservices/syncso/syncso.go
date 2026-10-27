// Package syncso is the lip-sync adapter (D279, D282): sync.'s own API, the
// first direct provider of a clip's lips matched to new sound (the groundwork
// for changing a spoken word). The footage and the sound are uploaded in
// one multipart request (no public links), the generation is asked about
// until it is done, the result is downloaded from the address sync. gives
// (without the key), and afterwards the generation and the two uploaded
// files are deleted at sync. The key is the person's own sync. API key, kept
// by internal/providers and sent only in the x-api-key header to sync.'s API
// host (or a server on this computer in tests and browser runs).
package syncso

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Name is the provider's ID in the catalogue; Title how it is named.
const (
	Name  = "sync"
	Title = "sync."
)

// EnvBaseURL points the adapter at a server on this computer (tests and
// browser runs); nothing else is accepted.
const EnvBaseURL = "METARUSH_SYNC_BASE_URL"

const apiHost = "https://api.sync.so"

// BaseURL is sync.'s API host, or the override (on this computer only).
// Under go test and in browser runs only a server on this computer.
func BaseURL() (string, error) {
	base := apiHost
	if o := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvBaseURL)), "/"); o != "" {
		if !loopback(o) {
			return "", apperr.New(apperr.NotConfigured, "%s must be a server on this computer.", EnvBaseURL)
		}
		base = o
	}
	if providers.RealServicesRefused() && !loopback(base) {
		return "", apperr.New(apperr.NotConfigured, "Tests never call sync.: set %s to a fake on this computer.", EnvBaseURL)
	}
	return base, nil
}

func loopback(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	if u.Hostname() == "localhost" {
		return true
	}
	ip := net.ParseIP(u.Hostname())
	return ip != nil && ip.IsLoopback()
}

// Adapter is lip sync on sync.'s API.
type Adapter struct {
	// HTTP overrides the client (tests).
	HTTP *http.Client
}

func init() {
	aiservices.Register(&Adapter{})
	providers.Register(key{})
}

func (a *Adapter) Provider() string { return Name }
func (a *Adapter) Kind() string     { return aiservices.KindLipSync }

func (a *Adapter) Capabilities(model string) (*aiservices.Model, error) {
	return aiservices.ModelFor(Name, aiservices.KindLipSync, model)
}

func (a *Adapter) Estimate(req *aiservices.Request, at time.Time) (*aiservices.Estimate, error) {
	m, err := aiservices.ModelFor(Name, aiservices.KindLipSync, req.Model)
	if err != nil {
		return nil, err
	}
	return aiservices.EstimateFor(Name, m, req, at)
}

func (a *Adapter) client() *http.Client {
	if a.HTTP != nil {
		return a.HTTP
	}
	return &http.Client{Timeout: 30 * time.Minute}
}

// do sends one request with the key in x-api-key, only to sync.'s API host.
func (a *Adapter) do(ctx context.Context, what, method, path, key string, body io.Reader, contentType string) (status int, data []byte, err error) {
	base, err := BaseURL()
	if err != nil {
		return 0, nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: apperr.From(err).Message}
	}
	call := logbook.ProviderCall{Provider: Name, Title: Title, What: what, Method: method, Endpoint: path}
	start := time.Now()
	defer func() {
		call.Duration, call.Status, call.Bytes = time.Since(start), status, len(data)
		logbook.ProviderDone(ctx, call, err)
	}()
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("x-api-key", key)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	res, err := a.client().Do(req)
	if err != nil {
		return 0, nil, &videogen.Error{Kind: videogen.KindNetwork, Provider: Name, Message: "MetaRush couldn't reach sync. Check the internet connection; it tries again by itself.", Detail: err.Error()}
	}
	defer res.Body.Close()
	data, err = io.ReadAll(io.LimitReader(res.Body, 8<<20))
	return res.StatusCode, data, err
}

// generation is sync.'s answer about a generation.
type generation struct {
	ID             string  `json:"id"`
	Status         string  `json:"status"`
	OutputURL      string  `json:"outputUrl"`
	OutputDuration float64 `json:"outputDuration"`
	Error          string  `json:"error"`
	ErrorCode      string  `json:"errorCode"`
	Input          []struct {
		Type    string `json:"type"`
		AssetID string `json:"assetId"`
	} `json:"input"`
}

// Submit uploads the footage and the sound and starts the generation.
func (a *Adapter) Submit(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	media, sound := req.Input(aiservices.RoleMedia), req.Input(aiservices.RoleAudio)
	if media == nil || sound == nil {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "Lip sync needs the footage and the new sound."}
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	_ = mw.WriteField("model", req.Model)
	opts, _ := json.Marshal(map[string]any{"sync_mode": "cut_off"})
	_ = mw.WriteField("options", string(opts))
	for _, f := range []struct {
		field string
		in    *aiservices.Media
		name  string
	}{{"video", media, "footage" + aiservices.ExtFor(media.MIME)}, {"audio", sound, "sound" + aiservices.ExtFor(sound.MIME)}} {
		w, err := mw.CreateFormFile(f.field, f.name)
		if err != nil {
			return nil, err
		}
		in, err := os.Open(f.in.Path)
		if err != nil {
			return nil, err
		}
		_, err = io.Copy(w, in)
		in.Close()
		if err != nil {
			return nil, err
		}
	}
	_ = mw.Close()
	status, data, err := a.do(ctx, "start a lip sync", http.MethodPost, "/v2/generate", key, &body, mw.FormDataContentType())
	if err != nil {
		var ve *videogen.Error
		if errors.As(err, &ve) && ve.Kind == videogen.KindNetwork {
			// It may have left this computer: sync. may have started it.
			return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "The request reached sync., but no answer came back, so MetaRush can't tell whether it started.", Detail: ve.Detail}
		}
		return nil, err
	}
	if status >= 300 {
		return nil, classify(status, data, "submit")
	}
	var g generation
	if err := json.Unmarshal(data, &g); err != nil || g.ID == "" {
		return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "sync. answered in a way MetaRush couldn't read, so it can't tell whether it started.", Detail: firstLine(string(data))}
	}
	t := &aiservices.Ticket{ID: g.ID, SubmittedAt: time.Now().UTC()}
	for _, in := range g.Input {
		if in.AssetID != "" {
			t.Files = append(t.Files, in.AssetID) // uploaded: deleted by Forget
		}
	}
	return t, nil
}

// Poll asks how the generation is going.
func (a *Adapter) Poll(ctx context.Context, key string, t *aiservices.Ticket) (*aiservices.Status, error) {
	status, data, err := a.do(ctx, "ask about a lip sync", http.MethodGet, "/v2/generate/"+url.PathEscape(t.ID), key, nil, "")
	if err != nil {
		return nil, err
	}
	if status >= 300 {
		return nil, classify(status, data, "poll")
	}
	var g generation
	if err := json.Unmarshal(data, &g); err != nil {
		return nil, &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "sync. answered in a way MetaRush couldn't read. It asks again.", Detail: firstLine(string(data))}
	}
	switch g.Status {
	case "COMPLETED":
		if g.OutputURL == "" {
			return &aiservices.Status{State: aiservices.StateFailed, Err: &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "sync. finished without a video."}}, nil
		}
		return &aiservices.Status{State: aiservices.StateDone, Outputs: []aiservices.Output{{MIME: "video/mp4", URI: g.OutputURL}},
			Usage: aiservices.Usage{Known: g.OutputDuration > 0, Seconds: g.OutputDuration}}, nil
	case "FAILED":
		return &aiservices.Status{State: aiservices.StateFailed, Err: &videogen.Error{Kind: videogen.KindServer, Provider: Name,
			Message: "sync. couldn't match the lips: " + firstNonEmpty(g.Error, "it didn't say why") + ".", Detail: g.ErrorCode,
			Hint: "Check that a face is clearly seen speaking, then try again."}}, nil
	case "REJECTED":
		s := &videogen.Safety{Blocked: true, Side: "input", Message: firstNonEmpty(g.Error, "rejected")}
		return &aiservices.Status{State: aiservices.StateFailed, Safety: s, Err: &videogen.Error{Kind: videogen.KindSafety, Provider: Name, Safety: s,
			Message: "sync. declined this request.", Detail: g.ErrorCode}}, nil
	}
	return &aiservices.Status{State: aiservices.StateRunning}, nil
}

// Fetch downloads the result from the address sync. gave, without the key
// (its own instructions: the address is signed).
func (a *Adapter) Fetch(ctx context.Context, _ string, _ *aiservices.Ticket, out *aiservices.Output, dst io.Writer) (int64, error) {
	if len(out.Data) > 0 {
		n, err := dst.Write(out.Data)
		return int64(n), err
	}
	u, err := url.Parse(out.URI)
	if err != nil || u.Scheme != "https" && !(providers.RealServicesRefused() && loopback(out.URI)) {
		return 0, &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "sync. gave an address MetaRush won't download from."}
	}
	if providers.RealServicesRefused() && !loopback(out.URI) {
		return 0, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "Tests never download from sync."}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, out.URI, nil)
	if err != nil {
		return 0, err
	}
	res, err := a.client().Do(req)
	if err != nil {
		return 0, &videogen.Error{Kind: videogen.KindNetwork, Provider: Name, Message: "The download from sync. stopped. MetaRush tries again by itself.", Detail: err.Error()}
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusGone {
		return 0, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "sync.'s download address expired before MetaRush could use it."}
	}
	if res.StatusCode >= 300 {
		return 0, &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "sync. didn't send the video. MetaRush tries again by itself.", HTTPStatus: res.StatusCode}
	}
	return io.Copy(dst, io.LimitReader(res.Body, 4<<30))
}

// Cancel can't stop a generation at sync. (it offers no way); it finishes
// there and is deleted with the rest by Forget.
func (a *Adapter) Cancel(context.Context, string, *aiservices.Ticket) error { return nil }

// Forget deletes the generation and the two uploaded files at sync.
func (a *Adapter) Forget(ctx context.Context, key string, t *aiservices.Ticket, _ []aiservices.Output) error {
	var failed []string
	if t.ID != "" {
		if status, data, err := a.do(ctx, "delete a lip sync", http.MethodDelete, "/v2/generations/"+url.PathEscape(t.ID), key, nil, ""); err != nil || (status >= 300 && status != http.StatusNotFound) {
			failed = append(failed, "the generation ("+strconv.Itoa(status)+" "+firstLine(string(data))+")")
		}
	}
	for _, id := range t.Files {
		if status, _, err := a.do(ctx, "delete an uploaded file", http.MethodDelete, "/v2/assets/"+url.PathEscape(id), key, nil, ""); err != nil || (status >= 300 && status != http.StatusNotFound) {
			failed = append(failed, "an uploaded file")
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("sync. didn't delete %s", strings.Join(failed, " and "))
	}
	return nil
}

// classify says what an error answer from sync. means.
func classify(status int, data []byte, phase string) *videogen.Error {
	var e struct {
		Message   string `json:"message"`
		Error     string `json:"error"`
		ErrorCode string `json:"errorCode"`
	}
	_ = json.Unmarshal(data, &e)
	msg := firstNonEmpty(e.Message, e.Error, firstLine(string(data)))
	out := &videogen.Error{Provider: Name, HTTPStatus: status, Detail: strings.TrimSpace(e.ErrorCode + " " + msg)}
	switch {
	case status == http.StatusUnauthorized:
		out.Kind, out.Message, out.Hint = videogen.KindKey, "sync. didn't accept the key.", "Add your sync. key again in Settings, Integrations."
	case status == http.StatusPaymentRequired || strings.Contains(e.ErrorCode, "plan") || strings.Contains(e.ErrorCode, "credit"):
		out.Kind, out.Message = videogen.KindPermission, "Your sync. plan doesn't allow this (its length, its file size or what's left this month)."
		out.Hint = "Check your plan on sync.'s site. Nothing was made or charged."
	case status == http.StatusForbidden:
		out.Kind, out.Message = videogen.KindPermission, "sync. doesn't allow this key to do that."
	case status == http.StatusTooManyRequests:
		out.Kind, out.Message, out.RetryAfter = videogen.KindRate, "sync. is busy with this key's requests; MetaRush tries again in a minute.", time.Minute
	case status == http.StatusNotFound && phase != "submit":
		out.Kind, out.Message = videogen.KindNotFound, "sync. doesn't have that generation any more."
	case status >= 500:
		out.Kind, out.Message = videogen.KindServer, "sync. had a problem on its side. MetaRush tries again by itself."
	default:
		out.Kind, out.Message = videogen.KindInvalid, "sync. couldn't use this: "+firstLine(msg)
	}
	return out
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > 300 {
		s = s[:300] + "…"
	}
	return s
}

// key is sync. in the integrations registry: the person's own API key.
type key struct{}

func (key) Name() string     { return Name }
func (key) Title() string    { return Title }
func (key) Category() string { return providers.CategoryVideo }
func (key) Summary() string {
	return "Lip sync: the lips of the people in a clip matched to new sound — a word changed, a line said again — with their consent. sync. bills by the second of footage, and MetaRush shows the price before anything is sent."
}
func (key) KeyHelp() string {
	return "Create a key on sync.'s site under API keys. Its Creator plan and above make videos without a watermark."
}
func (key) Capabilities() []string { return []string{"lip-sync"} }

// KeyHosts is where the sync. key may go (an ability that names it): its API host only.
func (key) KeyHosts() []string { return []string{"api.sync.so"} }

// Test lists one generation (free) to check the key.
func (k key) Test(ctx context.Context, apiKey string) (*providers.TestResult, error) {
	status, _, err := (&Adapter{}).do(ctx, "check the key", http.MethodGet, "/v2/generations?limit=1", apiKey, nil, "")
	if err != nil {
		return nil, err
	}
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return &providers.TestResult{OK: false, Message: "sync. didn't accept this key. Check that it was copied completely."}, nil
	case status >= 300:
		return nil, fmt.Errorf("sync. answered %d", status)
	}
	return &providers.TestResult{OK: true, Message: "Connected to sync."}, nil
}

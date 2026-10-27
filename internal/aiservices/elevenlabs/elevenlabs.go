// Package elevenlabs is ElevenLabs' AI services on the ElevenLabs key (D224):
// sound effects, music, speech, the voice changer, dubbing, speech to text
// and instant voice copies, each an aiservices.Adapter.
//
// Most calls answer with the result; a dub runs a while and is asked about by
// its ID. After the download MetaRush deletes what ElevenLabs lets it delete:
// the dubbing project, the transcript, the history item speech and voice
// changes leave (found by the request's ID), a voice copy when it is
// withdrawn. Sound effects and music can't be deleted through the API; the
// record says so. The key goes only in the xi-api-key header, only to an
// ElevenLabs host or a loopback test server (providers.ElevenLabsBaseURL);
// request mappings follow ElevenLabs' API reference as read on 25 Oct 2026.
package elevenlabs

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptrace"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Name is the provider's ID in the catalogue.
const Name = providers.ElevenLabsName

// Title is how people name it.
const Title = providers.ElevenLabsTitle

// OutputFormat is what MetaRush asks for: MP3 at 44.1 kHz, 128 kbps (every plan).
const OutputFormat = providers.DefaultOutputFormat

func init() {
	for _, k := range []string{aiservices.KindSoundEffect, aiservices.KindMusic, aiservices.KindVoice, aiservices.KindVoiceChange,
		aiservices.KindDubbing, aiservices.KindTranscription, aiservices.KindVoiceCopy} {
		aiservices.Register(&Adapter{kind: k})
	}
}

// Adapter is one kind of work on the ElevenLabs API.
type Adapter struct {
	kind string
	// HTTP overrides the client (tests); nil uses a default one.
	HTTP *http.Client
}

// Provider is the catalogue's provider ID.
func (a *Adapter) Provider() string { return Name }

// Kind is the kind of work it does.
func (a *Adapter) Kind() string { return a.kind }

// Capabilities reads the model's catalogue entry.
func (a *Adapter) Capabilities(model string) (*aiservices.Model, error) {
	return aiservices.ModelFor(Name, a.kind, model)
}

// Estimate prices a request from the catalogue.
func (a *Adapter) Estimate(req *aiservices.Request, at time.Time) (*aiservices.Estimate, error) {
	m, err := aiservices.ModelFor(Name, a.kind, req.Model)
	if err != nil {
		return nil, err
	}
	return aiservices.EstimateFor(Name, m, req, at)
}

type client struct {
	base string
	key  string
	http *http.Client
}

func (a *Adapter) client(key, region string) (*client, error) {
	base, err := providers.ElevenLabsBaseURL(region)
	if err != nil {
		return nil, err
	}
	hc := a.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	return &client{base: base, key: key, http: hc}, nil
}

type answer struct {
	status int
	header http.Header
	body   []byte
}

// call sends one request (a JSON body, or a multipart form with its content
// type). what says what it does, in plain words, for the activity log (never
// the key, the words or the files); wrote is set once it has left this computer.
func (c *client) call(ctx context.Context, what, method, path string, body io.Reader, contentType string, timeout time.Duration, wrote *bool) (res *answer, err error) {
	endpoint, _, _ := strings.Cut(path, "?")
	call := logbook.ProviderCall{Provider: Name, Title: Title, What: what, Method: method, Endpoint: endpoint}
	start := time.Now()
	defer func() {
		call.Duration = time.Since(start)
		if res != nil {
			call.Status, call.Bytes, call.RequestID = res.status, len(res.body), res.header.Get("request-id")
		}
		logbook.ProviderDone(ctx, call, err)
	}()
	if timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	if wrote != nil {
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { *wrote = true }})
	}
	req, err := http.NewRequestWithContext(ctx, method, c.base+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("xi-api-key", c.key) // the base is an ElevenLabs host or a loopback test server (ElevenLabsBaseURL)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	r, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &videogen.Error{Kind: videogen.KindNetwork, Provider: Name, Message: "MetaRush couldn't reach ElevenLabs. Check the internet connection; it tries again by itself.", Detail: err.Error()}
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 1<<30))
	res = &answer{status: r.StatusCode, header: r.Header, body: data}
	if err != nil {
		return res, &videogen.Error{Kind: videogen.KindNetwork, Provider: Name, Message: "The connection to ElevenLabs dropped. MetaRush tries again by itself.", Detail: err.Error()}
	}
	return res, nil
}

// jsonBody encodes a JSON request.
func jsonBody(v any) (io.Reader, string) {
	b, _ := json.Marshal(v)
	return bytes.NewReader(b), "application/json"
}

// form builds a multipart form: fields, then files (field name → paths).
func form(fields map[string]string, files map[string][]aiservices.Media) (io.Reader, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if v != "" {
			_ = w.WriteField(k, v)
		}
	}
	for field, ms := range files {
		for _, m := range ms {
			b, err := os.ReadFile(m.Path)
			if err != nil {
				return nil, "", err
			}
			name := firstNonEmpty(m.Name, "file"+aiservices.ExtFor(m.MIME))
			p, err := w.CreateFormFile(field, name)
			if err != nil {
				return nil, "", err
			}
			if _, err := p.Write(b); err != nil {
				return nil, "", err
			}
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

// classify turns an error answer into a plain-words Error.
func classify(res *answer, model string) *videogen.Error {
	var body struct {
		Detail json.RawMessage `json:"detail"`
	}
	_ = json.Unmarshal(res.body, &body)
	var obj struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Status  string `json:"status"`
		Message string `json:"message"`
	}
	var str string
	var list []struct {
		Msg string `json:"msg"`
	}
	msg, code := "", ""
	switch {
	case json.Unmarshal(body.Detail, &obj) == nil && (obj.Message != "" || obj.Code != "" || obj.Status != ""):
		msg, code = obj.Message, firstNonEmpty(obj.Code, obj.Status, obj.Type)
	case json.Unmarshal(body.Detail, &str) == nil:
		msg = str
	case json.Unmarshal(body.Detail, &list) == nil && len(list) > 0:
		msg = list[0].Msg
	default:
		msg = firstLine(string(res.body), 300)
	}
	low := strings.ToLower(msg + " " + code)
	e := &videogen.Error{Provider: Name, HTTPStatus: res.status, Detail: firstLine(strings.TrimSpace(code+" "+msg), 600)}
	if s, err := strconv.Atoi(res.header.Get("Retry-After")); err == nil && s > 0 {
		e.RetryAfter = time.Duration(s) * time.Second
	}
	missing := strings.Contains(low, "permission")
	switch {
	case res.status == http.StatusUnauthorized && !missing:
		e.Kind, e.Message, e.Hint = videogen.KindKey, "ElevenLabs did not accept the key. Check that it was copied completely and hasn't been deleted.", "Add the key again in Settings → Integrations."
	case missing || res.status == http.StatusForbidden:
		e.Kind, e.Message = videogen.KindPermission, "The ElevenLabs key may not do this: allow it where you made the key in your ElevenLabs account."
		if strings.Contains(low, "subscription") || strings.Contains(low, "feature_not_available") || strings.Contains(low, "plan") {
			e.Message = "Your ElevenLabs plan doesn't include this."
		}
	case res.status == http.StatusPaymentRequired || strings.Contains(low, "quota") || strings.Contains(low, "insufficient_credits") || strings.Contains(low, "credits"):
		e.Kind, e.Message, e.Hint = videogen.KindPermission, "Your ElevenLabs plan has no credits left for this.", "Add credits or change the plan in your ElevenLabs account, then try again."
	case res.status == http.StatusTooManyRequests:
		e.Kind, e.Message = videogen.KindRate, "ElevenLabs is busy with this key's requests right now. MetaRush tries again in a minute."
		if e.RetryAfter == 0 {
			e.RetryAfter = time.Minute
		}
	case res.status >= 500:
		e.Kind, e.Message = videogen.KindServer, "ElevenLabs had a problem on its side. MetaRush tries again by itself."
	case res.status == http.StatusNotFound && strings.Contains(low, "model"):
		e.Kind, e.Message, e.Hint = videogen.KindRetired, fmt.Sprintf("ElevenLabs no longer offers %s, or this key can't use it.", model), "Choose another model."
	case res.status == http.StatusNotFound:
		e.Kind, e.Message = videogen.KindNotFound, "ElevenLabs doesn't have that any more."
	case safety(low):
		e.Kind, e.Message, e.Hint = videogen.KindSafety, "ElevenLabs declined this request: it says the words or the recording go against its terms.", "Change the words or the recording and try again."
		e.Safety = &videogen.Safety{Blocked: true, Side: "input", Category: "other", Message: firstLine(msg, 300)}
	default:
		e.Kind, e.Message = videogen.KindInvalid, "ElevenLabs couldn't use these settings."
		if msg != "" {
			e.Message += " It said: " + firstLine(msg, 200)
		}
	}
	return e
}

func safety(low string) bool {
	for _, w := range []string{"terms of service", "terms_of_service", "prohibited", "violat", "copyright", "harmful", "not allowed", "moderation", "blocked"} {
		if strings.Contains(low, w) {
			return true
		}
	}
	return false
}

// post sends a request whose answer is the result (one-shot), telling an
// answer that never came from a refusal.
func (a *Adapter) post(ctx context.Context, key, region, what, path string, body io.Reader, contentType, model string, timeout time.Duration) (*answer, error) {
	c, err := a.client(key, region)
	if err != nil {
		return nil, err
	}
	wrote := false
	res, err := c.call(ctx, what, http.MethodPost, path, body, contentType, timeout, &wrote)
	if err != nil {
		if wrote {
			return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "The request reached ElevenLabs, but no answer came back, so MetaRush can't tell whether it was made.", Detail: err.Error()}
		}
		return nil, err
	}
	if res.status >= 300 {
		return nil, classify(res, model)
	}
	return res, nil
}

// audio is a one-shot answer of sound as a ticket: the bytes, and the
// handles to delete it by afterwards.
func audio(res *answer, usage aiservices.Usage, extra map[string]string) (*aiservices.Ticket, error) {
	if len(res.body) == 0 {
		// Answered (and perhaps charged): never sent again.
		return nil, &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "ElevenLabs answered without any sound."}
	}
	mime := aiservices.Sniff(res.body)
	if mime == "" {
		mime = firstNonEmpty(strings.SplitN(res.header.Get("Content-Type"), ";", 2)[0], "audio/mpeg")
	}
	if rid := res.header.Get("request-id"); rid != "" {
		if extra == nil {
			extra = map[string]string{}
		}
		extra["request"] = rid
	}
	st := &aiservices.Status{State: aiservices.StateDone, Usage: usage, Outputs: []aiservices.Output{{MIME: mime, Data: res.body}}}
	return &aiservices.Ticket{SubmittedAt: time.Now().UTC(), Extra: extra, Ready: st}, nil
}

// Poll asks how a dub is going; other kinds answered with their result.
func (a *Adapter) Poll(ctx context.Context, key string, t *aiservices.Ticket) (*aiservices.Status, error) {
	if t != nil && t.Ready != nil {
		return t.Ready, nil
	}
	if a.kind == aiservices.KindDubbing && t != nil && t.ID != "" {
		return a.pollDub(ctx, key, t)
	}
	return nil, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "ElevenLabs answered with the result straight away and keeps nothing to ask about later."}
}

// Fetch writes an output: bytes ElevenLabs sent, or a finished dub's file.
func (a *Adapter) Fetch(ctx context.Context, key string, t *aiservices.Ticket, out *aiservices.Output, dst io.Writer) (int64, error) {
	if len(out.Data) > 0 {
		n, err := dst.Write(out.Data)
		return int64(n), err
	}
	if a.kind == aiservices.KindDubbing && t != nil && t.ID != "" {
		return a.fetchDub(ctx, key, t, out, dst)
	}
	return 0, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "ElevenLabs' answer had no file to keep."}
}

// Cancel stops a dub (ElevenLabs stops it by deleting it); other calls have ended.
func (a *Adapter) Cancel(ctx context.Context, key string, t *aiservices.Ticket) error {
	if a.kind == aiservices.KindDubbing && t != nil && t.ID != "" {
		return a.deleteDub(ctx, key, t)
	}
	return nil
}

// Forget deletes what ElevenLabs kept for the request, where its API allows.
func (a *Adapter) Forget(ctx context.Context, key string, t *aiservices.Ticket, _ []aiservices.Output) error {
	if t == nil {
		return nil
	}
	switch a.kind {
	case aiservices.KindDubbing:
		if t.ID != "" {
			return a.deleteDub(ctx, key, t)
		}
	case aiservices.KindTranscription:
		if id := t.Extra["transcript"]; id != "" {
			return a.deleteTranscript(ctx, key, t, id)
		}
	case aiservices.KindVoice, aiservices.KindVoiceChange:
		return a.deleteHistory(ctx, key, t)
	case aiservices.KindSoundEffect, aiservices.KindMusic:
		return aiservices.Kept("ElevenLabs offers no way to delete sound effects and music it made; its terms say how long it keeps them")
	}
	return nil
}

func region(t *aiservices.Ticket) string {
	if t == nil {
		return ""
	}
	return t.Extra["region"]
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		s = s[:i]
	}
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

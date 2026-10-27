// Package google is Google's AI services on the Gemini key (D224): pictures
// (Nano Banana), speech and voice replication (Gemini TTS), music (Lyria) and
// transcription in the cloud, each an aiservices.Adapter.
//
// Requests use generateContent: stateless, with store false so Google keeps
// no log of them, answering with the result (one-shot calls, so there is
// nothing to ask about later). Recordings longer than an inline request
// allows go through the Files API and are deleted after use; a voice copy is
// made with the Voices API and deleted when it is withdrawn. The key goes only
// in the x-goog-api-key header, only to Google's API host or a loopback test
// server (videogen.GeminiBaseURL); request mappings follow Google's docs as
// read on 25 Oct 2026 (catalog.json names each page).
package google

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Name is the provider's ID in the catalogue (the Gemini key).
const Name = videogen.GeminiName

// InlineMax is the most a request carries inline; larger recordings go
// through the Files API (Google's pages say 20 MB for the whole request).
var InlineMax int64 = 14 << 20

func init() {
	for _, k := range []string{aiservices.KindPicture, aiservices.KindVoice, aiservices.KindVoiceCopy, aiservices.KindMusic, aiservices.KindTranscription} {
		aiservices.Register(&Adapter{kind: k})
	}
}

// Adapter is one kind of work on the Gemini API.
type Adapter struct {
	kind string
	// HTTP overrides the client (tests); nil uses a default one.
	HTTP *http.Client
}

// New is an adapter for a kind (tests).
func New(kind string, hc *http.Client) *Adapter { return &Adapter{kind: kind, HTTP: hc} }

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

func (a *Adapter) client(key string) (*client, error) {
	base, err := videogen.GeminiBaseURL()
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

// call sends one request. what says what it does, in plain words, for the
// activity log (never the key, the words or the answer); wrote is set once
// the request has left this computer.
func (c *client) call(ctx context.Context, what, method, path string, body any, timeout time.Duration, wrote *bool) (res *answer, err error) {
	u, err := url.Parse(c.base + path)
	if err != nil {
		return nil, err
	}
	call := logbook.ProviderCall{Provider: Name, Title: videogen.GeminiTitle, What: what, Method: method, Endpoint: u.Path}
	start := time.Now()
	defer func() {
		call.Duration = time.Since(start)
		if res != nil {
			call.Status, call.Bytes, call.RequestID = res.status, len(res.body), res.header.Get("X-Goog-Request-Id")
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
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), rd)
	if err != nil {
		return nil, err
	}
	req.Header.Set("x-goog-api-key", c.key) // the base is Google's host or a loopback test server (GeminiBaseURL)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	r, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &videogen.Error{Kind: videogen.KindNetwork, Provider: Name, Message: "MetaRush couldn't reach Google. Check the internet connection; it tries again by itself.", Detail: err.Error()}
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 256<<20))
	res = &answer{status: r.StatusCode, header: r.Header, body: data}
	if err != nil {
		return res, &videogen.Error{Kind: videogen.KindNetwork, Provider: Name, Message: "The connection to Google dropped. MetaRush tries again by itself.", Detail: err.Error()}
	}
	return res, nil
}

// classify reads an error answer in this kind's words.
func classify(kind, model string, res *answer, phase string) *videogen.Error {
	e := videogen.ClassifyGoogle(res.status, res.body, res.header, phase)
	k, _ := aiservices.KindOf(kind)
	thing := strings.ToLower(k.Title)
	switch e.Kind {
	case videogen.KindKey:
		e.Hint = "Add the key again in Settings → Integrations."
	case videogen.KindTier:
		e.Message = fmt.Sprintf("This Google project is on the free tier, which can't use %s. Turn on billing for the project in Google AI Studio, then try again.", model)
	case videogen.KindPermission:
		if !strings.Contains(e.Message, "blocked this key's project") {
			e.Message = fmt.Sprintf("This Google key isn't allowed to use %s. Check the key's project (billing, region) in Google AI Studio.", model)
			e.Hint = "Then check the connection in Settings → Integrations."
		}
	case videogen.KindSafety:
		e.Message = "Google declined this request: its safety filters blocked the words or a file."
		e.Hint = "Change the words or the files and try again."
	case videogen.KindRetired:
		e.Message = fmt.Sprintf("Google no longer offers %s for %s, or this key can't use it.", model, thing)
		e.Hint = "Choose another model."
	}
	return e
}

// part is a generateContent part.
type part struct {
	Text           string              `json:"text,omitempty"`
	Thought        bool                `json:"thought,omitempty"`
	InlineData     *blob               `json:"inlineData,omitempty"`
	FileData       *fileData           `json:"fileData,omitempty"`
	SpeechMetadata *speechMetadata     `json:"speechMetadata,omitempty"`
	Transcription  *audioTranscription `json:"audioTranscription,omitempty"`
}

type blob struct {
	MimeType string `json:"mimeType"`
	Data     string `json:"data"`
}

type fileData struct {
	MimeType string `json:"mimeType"`
	FileURI  string `json:"fileUri"`
}

type speechMetadata struct {
	Style string `json:"style,omitempty"`
}

type audioTranscription struct {
	SpeakerLabel string `json:"speakerLabel"`
	Text         string `json:"text"`
	Words        []struct {
		Word        string `json:"word"`
		StartOffset string `json:"startOffset"`
		EndOffset   string `json:"endOffset"`
	} `json:"words"`
}

type content struct {
	Role  string `json:"role,omitempty"`
	Parts []part `json:"parts"`
}

type genResponse struct {
	Candidates []struct {
		Content       content `json:"content"`
		FinishReason  string  `json:"finishReason"`
		FinishMessage string  `json:"finishMessage"`
	} `json:"candidates"`
	PromptFeedback *struct {
		BlockReason string `json:"blockReason"`
	} `json:"promptFeedback"`
	UsageMetadata *struct {
		PromptTokenCount     int64 `json:"promptTokenCount"`
		CandidatesTokenCount int64 `json:"candidatesTokenCount"`
		ThoughtsTokenCount   int64 `json:"thoughtsTokenCount"`
	} `json:"usageMetadata"`
}

// safetyReasons are the finish reasons that mean a filter declined it.
var safetyReasons = map[string]bool{"SAFETY": true, "IMAGE_SAFETY": true, "PROHIBITED_CONTENT": true, "IMAGE_PROHIBITED_CONTENT": true, "BLOCKLIST": true,
	"SPII": true, "RECITATION": true, "IMAGE_RECITATION": true, "LANGUAGE": true}

// generate sends a generateContent request and reads the first candidate's parts.
func (a *Adapter) generate(ctx context.Context, key, model, what string, body map[string]any) ([]part, aiservices.Usage, error) {
	c, err := a.client(key)
	if err != nil {
		return nil, aiservices.Usage{}, err
	}
	body["store"] = false // Google keeps no log of the request
	wrote := false
	res, err := c.call(ctx, what, http.MethodPost, "/v1beta/models/"+url.PathEscape(model)+":generateContent", body, 10*time.Minute, &wrote)
	if err != nil {
		if wrote {
			// It left this computer but no answer came: Google may have made it, and charged for it.
			return nil, aiservices.Usage{}, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name,
				Message: "The request reached Google, but no answer came back, so MetaRush can't tell whether it was made.", Detail: err.Error()}
		}
		return nil, aiservices.Usage{}, err
	}
	if res.status >= 300 {
		return nil, aiservices.Usage{}, classify(a.kind, model, res, "submit")
	}
	var g genResponse
	if err := json.Unmarshal(res.body, &g); err != nil {
		return nil, aiservices.Usage{}, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "Google answered in a way MetaRush couldn't read.", Detail: firstLine(string(res.body), 300)}
	}
	u := aiservices.Usage{}
	if m := g.UsageMetadata; m != nil {
		u = aiservices.Usage{Known: true, InputTokens: m.PromptTokenCount, OutputTokens: m.CandidatesTokenCount + m.ThoughtsTokenCount}
	}
	if pf := g.PromptFeedback; pf != nil && pf.BlockReason != "" {
		return nil, u, &videogen.Error{Kind: videogen.KindSafety, Provider: Name, Message: "Google declined this request: its safety filters blocked the words or a file.",
			Hint: "Change the words or the files and try again.", Detail: "blockReason " + pf.BlockReason,
			Safety: &videogen.Safety{Blocked: true, Side: "input", Category: "other", Message: pf.BlockReason}}
	}
	if len(g.Candidates) == 0 {
		// Answered (and perhaps charged): never sent again.
		return nil, u, &videogen.Error{Kind: videogen.KindUnusable, Provider: Name, Message: "Google answered without a result."}
	}
	cand := g.Candidates[0]
	if safetyReasons[cand.FinishReason] {
		return nil, u, &videogen.Error{Kind: videogen.KindSafety, Provider: Name, Message: "Google finished without a result: its safety filters (or the model) declined it.",
			Hint: "Change the words or the files and try again.", Detail: strings.TrimSpace(cand.FinishReason + " " + cand.FinishMessage),
			Safety: &videogen.Safety{Blocked: true, Side: "output", Category: "other", Message: firstLine(cand.FinishMessage, 300)}}
	}
	var parts []part
	for _, p := range cand.Content.Parts {
		if !p.Thought {
			parts = append(parts, p)
		}
	}
	return parts, u, nil
}

// ready is a one-shot call's result as a ticket.
func ready(st *aiservices.Status, files []string) *aiservices.Ticket {
	return &aiservices.Ticket{Files: files, SubmittedAt: time.Now().UTC(), Ready: st}
}

// inline reads a file as an inline part.
func inline(m aiservices.Media) (*blob, error) {
	b, err := os.ReadFile(m.Path)
	if err != nil {
		return nil, err
	}
	return &blob{MimeType: m.MIME, Data: base64.StdEncoding.EncodeToString(b)}, nil
}

// media is a recording as a part: inline when small, through the Files API otherwise.
func (a *Adapter) media(ctx context.Context, key string, m aiservices.Media) (part, string, error) {
	if m.Size > 0 && m.Size <= InlineMax {
		b, err := inline(m)
		return part{InlineData: b}, "", err
	}
	name, uri, err := videogen.Gemini{HTTP: a.HTTP}.Upload(ctx, key, videogen.Media{Role: m.Role, MIME: m.MIME, Path: m.Path, SHA256: m.SHA256, Size: m.Size})
	if err != nil {
		return part{}, "", err
	}
	return part{FileData: &fileData{MimeType: m.MIME, FileURI: uri}}, name, nil
}

// Submit sends the request; Google answers with the result.
func (a *Adapter) Submit(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	switch a.kind {
	case aiservices.KindPicture:
		return a.picture(ctx, key, req)
	case aiservices.KindVoice:
		return a.speech(ctx, key, req)
	case aiservices.KindMusic:
		return a.music(ctx, key, req)
	case aiservices.KindTranscription:
		return a.transcribe(ctx, key, req)
	case aiservices.KindVoiceCopy:
		return a.copyVoice(ctx, key, req)
	}
	return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "MetaRush can't do that with Google."}
}

// Poll returns the result Google answered with (it keeps nothing to ask about).
func (a *Adapter) Poll(_ context.Context, _ string, t *aiservices.Ticket) (*aiservices.Status, error) {
	if t != nil && t.Ready != nil {
		return t.Ready, nil
	}
	return nil, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "Google answered with the result straight away and keeps nothing to ask about later."}
}

// Fetch writes an output's bytes (Google sends them in its answer).
func (a *Adapter) Fetch(_ context.Context, _ string, _ *aiservices.Ticket, out *aiservices.Output, dst io.Writer) (int64, error) {
	if len(out.Data) == 0 {
		return 0, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "Google's answer had no file to keep."}
	}
	n, err := dst.Write(out.Data)
	return int64(n), err
}

// Cancel has nothing to stop: the call has ended.
func (a *Adapter) Cancel(context.Context, string, *aiservices.Ticket) error { return nil }

// Forget deletes the recordings uploaded for the request (generateContent
// with store false keeps nothing else).
func (a *Adapter) Forget(ctx context.Context, key string, t *aiservices.Ticket, _ []aiservices.Output) error {
	if t == nil || len(t.Files) == 0 {
		return nil
	}
	if err := (videogen.Gemini{HTTP: a.HTTP}).DeleteFiles(ctx, key, t.Files); err != nil {
		return &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "Google didn't delete the uploaded recording; it deletes files by itself after two days.", Detail: err.Error()}
	}
	return nil
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

// offset reads a duration such as "1.250s".
func offset(s string) float64 {
	v, _ := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(s), "s"), 64)
	return v
}

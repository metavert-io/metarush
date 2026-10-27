package videogen

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/providers"
)

// GeminiName is the provider ID of Google Gemini (the Gemini API with an AI
// Studio key; video by Gemini Omni through the Interactions API).
const GeminiName = "gemini"

// GeminiTitle is how people name it.
const GeminiTitle = "Google Gemini"

// EnvGeminiBaseURL overrides the API endpoint. Only Google's own API host and
// loopback test servers are accepted, so a key can never be sent elsewhere.
const EnvGeminiBaseURL = "METARUSH_GEMINI_BASE_URL"

const geminiAPI = "https://generativelanguage.googleapis.com"

// GeminiBaseURL returns the API base URL (or the override). Under go test and
// in browser runs (providers.RealServicesRefused) only a test server on this
// computer is accepted, so a test that forgets to set up its fake can never
// reach Google (and spend money).
func GeminiBaseURL() (string, error) {
	o := strings.TrimRight(strings.TrimSpace(os.Getenv(EnvGeminiBaseURL)), "/")
	if o != "" && !allowedGeminiBase(o) {
		return "", &Error{Kind: KindInvalid, Provider: GeminiName, Message: EnvGeminiBaseURL + " must be Google's API address or a test server on this computer."}
	}
	if o == "" {
		o = geminiAPI
	}
	if providers.RealServicesRefused() && !localBase(o) {
		return "", &Error{Kind: KindInvalid, Provider: GeminiName, Message: "Tests never call Google: set " + EnvGeminiBaseURL + " to a fake on this computer."}
	}
	return o, nil
}

// localBase reports whether a base URL is a server on this computer.
func localBase(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "http" || u.Scheme == "https") && loopbackHost(u.Hostname())
}

func allowedGeminiBase(raw string) bool {
	if raw == geminiAPI {
		return true
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	return loopbackHost(u.Hostname())
}

func loopbackHost(h string) bool {
	if h == "localhost" {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// Gemini makes video with Gemini Omni through the Interactions API in
// background mode: Submit creates the interaction and returns at once, Poll
// reads it, Fetch downloads the video (inline or from the Files API), Forget
// deletes the interaction and every file (store=false is not allowed with
// background requests, so deleting right after the download keeps likeness
// pictures from staying at Google for weeks).
type Gemini struct {
	// HTTP overrides the client (tests); nil uses a default one.
	HTTP *http.Client
	// PollFiles is how long to wait between checks of an uploaded or produced
	// file that is still being processed (default 2 s).
	PollFiles time.Duration
}

func init() { Register(Gemini{}) }

// Name is the provider ID.
func (Gemini) Name() string { return GeminiName }

// Title is the display name.
func (Gemini) Title() string { return GeminiTitle }

// Capabilities reads the model's entry in the catalog.
func (Gemini) Capabilities(model string) (*Capabilities, error) { return ModelInfo(GeminiName, model) }

// Estimate prices a request with the table in force at t.
func (Gemini) Estimate(req *Request, at time.Time) (*Estimate, error) {
	m, err := ModelInfo(GeminiName, req.Model)
	if err != nil {
		return nil, err
	}
	pt, err := PriceAt(GeminiName, m.ID, at)
	if err != nil {
		return nil, err
	}
	return pt.Estimate(req)
}

type gclient struct {
	base string
	key  string
	http *http.Client
	g    Gemini
}

func (g Gemini) client(key string) (*gclient, error) {
	base, err := GeminiBaseURL()
	if err != nil {
		return nil, err
	}
	hc := g.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	return &gclient{base: base, key: key, http: hc, g: g}, nil
}

// response is one answer from the API.
type response struct {
	status int
	header http.Header
	body   []byte
	n      int64 // bytes of the answer (streamed ones included)
}

// maxDownload caps a downloaded video.
const maxDownload = 2 << 30

// call sends one request; the key goes only in the x-goog-api-key header, and
// only to the configured API host. what says what it does, in plain words, for
// the activity log (never the key, the prompt or the answer).
func (c *gclient) call(ctx context.Context, what, method, rawURL string, body []byte, hdr map[string]string, timeout time.Duration) (*response, error) {
	return c.callTraced(ctx, what, method, rawURL, body, hdr, timeout, nil, nil)
}

// callTraced is call with two extras: wrote is set once the request has left
// this computer, and a successful answer is streamed into sink (a download)
// instead of kept in memory.
func (c *gclient) callTraced(ctx context.Context, what, method, rawURL string, body []byte, hdr map[string]string, timeout time.Duration, wrote *bool, sink io.Writer) (res *response, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, &Error{Kind: KindInvalid, Provider: GeminiName, Message: "Google sent an address MetaRush can't use.", Detail: err.Error()}
	}
	call := logbook.ProviderCall{Provider: GeminiName, Title: GeminiTitle, What: what, Method: method, Endpoint: u.Path}
	start := time.Now()
	defer func() {
		call.Duration = time.Since(start)
		if res != nil {
			call.Status, call.Bytes = res.status, int(res.n)
			call.RequestID = res.header.Get("X-Goog-Request-Id")
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
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rd)
	if err != nil {
		return nil, err
	}
	if c.sameHost(u) {
		req.Header.Set("x-goog-api-key", c.key)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	r, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &Error{Kind: KindNetwork, Provider: GeminiName, Message: "MetaRush couldn't reach Google. Check the internet connection; it tries again by itself.", Detail: err.Error()}
	}
	defer r.Body.Close()
	res = &response{status: r.StatusCode, header: r.Header}
	if sink != nil && r.StatusCode < 300 {
		n, err := io.Copy(sink, io.LimitReader(r.Body, maxDownload+1))
		res.n = n
		if err == nil && n > maxDownload {
			err = errors.New("the video is larger than MetaRush accepts")
		}
		if err != nil {
			return res, &Error{Kind: KindNetwork, Provider: GeminiName, Message: "The download from Google stopped. MetaRush tries again by itself.", Detail: err.Error()}
		}
		return res, nil
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 64<<20))
	res.body, res.n = data, int64(len(data))
	if err != nil {
		return res, &Error{Kind: KindNetwork, Provider: GeminiName, Message: "The connection to Google dropped. MetaRush tries again by itself.", Detail: err.Error()}
	}
	return res, nil
}

// sameHost reports whether an address is on the configured API host (the
// only place the key is sent).
func (c *gclient) sameHost(u *url.URL) bool {
	b, err := url.Parse(c.base)
	return err == nil && strings.EqualFold(u.Scheme, b.Scheme) && strings.EqualFold(u.Host, b.Host)
}

// interaction is the part of an Interactions API resource MetaRush reads.
type interaction struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Steps  []struct {
		Type    string  `json:"type"`
		Content []gpart `json:"content"`
	} `json:"steps"`
	Outputs     []gpart `json:"outputs"`
	OutputVideo *gpart  `json:"output_video"`
	Usage       *struct {
		TotalInputTokens       int64        `json:"total_input_tokens"`
		TotalOutputTokens      int64        `json:"total_output_tokens"`
		TotalThoughtTokens     int64        `json:"total_thought_tokens"`
		OutputTokensByModality []byModality `json:"output_tokens_by_modality"`
	} `json:"usage"`
	// Errors and Error: codes come as words or numbers (looseText), so neither form makes the answer unreadable.
	Errors []struct {
		Code    looseText `json:"code"`
		Message string    `json:"message"`
	} `json:"errors"`
	Error *struct {
		Code    looseText `json:"code"`
		Message string    `json:"message"`
	} `json:"error"`
}

type byModality struct {
	Modality string `json:"modality"`
	Tokens   int64  `json:"tokens"`
}

type gpart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	URI      string `json:"uri,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

// Submit uploads the clips the request uses (Files API), then creates the
// interaction in the background and returns its ID.
func (g Gemini) Submit(ctx context.Context, key string, req *Request) (*Ticket, error) {
	c, err := g.client(key)
	if err != nil {
		return nil, err
	}
	t := &Ticket{}
	var input []gpart
	for _, im := range req.Images {
		b, err := os.ReadFile(im.Path)
		if err != nil {
			return nil, err
		}
		input = append(input, gpart{Type: "image", Data: base64.StdEncoding.EncodeToString(b), MimeType: im.MIME})
	}
	for _, v := range req.Videos {
		f, err := c.upload(ctx, v)
		if err != nil {
			_ = c.deleteFiles(context.WithoutCancel(ctx), t.Files)
			return nil, err
		}
		t.Files = append(t.Files, f.Name)
		input = append(input, gpart{Type: "video", URI: f.URI, MimeType: v.MIME})
	}
	format := map[string]any{"type": "video", "aspect_ratio": req.AspectRatio, "resolution": req.Resolution, "duration": strconv.Itoa(req.Seconds) + "s"}
	if d := geminiDelivery(req); d == "uri" {
		// Inline is the default; it is left out because Google's guide and its API reference name it
		// differently ("base64", "inline"), while both say "uri".
		format["delivery"] = d
	}
	body := map[string]any{"model": req.Model, "background": true, "store": true, "response_format": format}
	if len(input) == 0 {
		body["input"] = req.Prompt
	} else {
		body["input"] = append(input, gpart{Type: "text", Text: req.Prompt})
	}
	// The task is named only where the inputs alone don't say it (the model
	// works it out otherwise, as Google's own examples do).
	switch req.Task {
	case TaskReferenceToVideo, TaskEdit, TaskExtend:
		body["generation_config"] = map[string]any{"video_config": map[string]any{"task": req.Task}}
	}
	raw, _ := json.Marshal(body)
	wrote := false
	res, err := c.callTraced(ctx, "start a video", http.MethodPost, c.base+"/v1beta/interactions", raw, map[string]string{"Content-Type": "application/json"}, 2*time.Minute, &wrote, nil)
	if err != nil {
		_ = c.deleteFiles(context.WithoutCancel(ctx), t.Files)
		if wrote {
			// It left this computer but no answer came (the connection dropped,
			// or MetaRush stopped waiting): Google may have started it.
			return nil, &Error{Kind: KindUncertain, Provider: GeminiName, Message: "The request reached Google, but no answer came back, so MetaRush can't tell whether Google started the video.",
				Detail: err.Error()}
		}
		return nil, err
	}
	if res.status >= 300 {
		_ = c.deleteFiles(context.WithoutCancel(ctx), t.Files)
		return nil, classifyHTTP(res.status, res.body, res.header, "submit")
	}
	var it interaction
	if err := json.Unmarshal(res.body, &it); err != nil || it.ID == "" {
		return nil, &Error{Kind: KindUncertain, Provider: GeminiName, Message: "Google answered in a way MetaRush couldn't read, so it can't tell whether the video was started.", Detail: firstLine(string(res.body), 300)}
	}
	t.ID, t.SubmittedAt = it.ID, time.Now().UTC()
	return t, nil
}

// geminiDelivery asks for the video inline when it will fit in an answer
// (about 4 MB), and as a file to download otherwise.
func geminiDelivery(req *Request) string {
	kbps := map[string]float64{"360p": 900, "720p": 2600, "1080p": 5200, "4k": 16000}[req.Resolution]
	if kbps == 0 || kbps*float64(req.Seconds)/8*1000 > 3_000_000 {
		return "uri"
	}
	return "inline"
}

var interactionIDRe = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,256}$`)

func (c *gclient) interactionURL(id, suffix string) (string, error) {
	if !interactionIDRe.MatchString(id) {
		return "", &Error{Kind: KindInvalid, Provider: GeminiName, Message: "That isn't a request ID Google gave out."}
	}
	return c.base + "/v1beta/interactions/" + url.PathEscape(id) + suffix, nil
}

// Poll reads the interaction.
func (g Gemini) Poll(ctx context.Context, key string, t *Ticket) (*Status, error) {
	c, err := g.client(key)
	if err != nil {
		return nil, err
	}
	u, err := c.interactionURL(t.ID, "")
	if err != nil {
		return nil, err
	}
	res, err := c.call(ctx, "check on a video", http.MethodGet, u, nil, nil, time.Minute)
	if err != nil {
		return nil, err
	}
	if res.status >= 300 {
		e := classifyHTTP(res.status, res.body, res.header, "poll")
		if res.status == http.StatusNotFound {
			e.Kind, e.Message = KindExpired, "Google no longer has this request: it was deleted or kept too long."
		}
		return nil, e
	}
	var it interaction
	if err := json.Unmarshal(res.body, &it); err != nil {
		return nil, &Error{Kind: KindServer, Provider: GeminiName, Message: "Google answered in a way MetaRush couldn't read. It asks again in a moment.", Detail: firstLine(string(res.body), 300)}
	}
	st := statusOf(&it)
	if it.Status == "completed" && len(st.Outputs) == 0 && st.Err != nil {
		// Finished with no video MetaRush could find: keep the answer's shape (never its values) to tell a
		// refusal from an answer in a form this version doesn't know.
		st.Err.Detail = strings.TrimSpace(st.Err.Detail + " (Google's answer had: " + shapeOf(res.body) + ")")
	}
	return st, nil
}

// shapeOf lists an answer's top-level fields, with the kinds of parts in lists ("outputs[video,text]").
func shapeOf(body []byte) string {
	var top map[string]json.RawMessage
	if json.Unmarshal(body, &top) != nil {
		return "no fields"
	}
	var out []string
	for k, v := range top {
		var parts []struct {
			Type string `json:"type"`
		}
		if json.Unmarshal(v, &parts) == nil && len(parts) > 0 {
			var types []string
			for _, p := range parts {
				types = append(types, firstNonEmpty(p.Type, "?"))
			}
			k += "[" + strings.Join(types, ",") + "]"
		}
		out = append(out, k)
	}
	sort.Strings(out)
	return firstLine(strings.Join(out, ", "), 300)
}

func statusOf(it *interaction) *Status {
	st := &Status{}
	var texts []string
	add := func(p gpart) {
		switch p.Type {
		case "video":
			o := Output{MIME: p.MimeType, URI: p.URI}
			if p.Data != "" {
				if b, err := base64.StdEncoding.DecodeString(p.Data); err == nil {
					o.Data = b
				}
			}
			if o.MIME == "" {
				o.MIME = "video/mp4"
			}
			if m := fileRe.FindStringSubmatch(p.URI); m != nil {
				o.FileID = "files/" + m[1]
			}
			if len(o.Data) > 0 || o.URI != "" {
				st.Outputs = append(st.Outputs, o)
			}
		case "text":
			if s := strings.TrimSpace(p.Text); s != "" {
				texts = append(texts, s)
			}
		}
	}
	for _, s := range it.Steps {
		if s.Type != "" && s.Type != "model_output" {
			continue
		}
		for _, p := range s.Content {
			add(p)
		}
	}
	if len(st.Outputs) == 0 {
		for _, p := range it.Outputs {
			add(p)
		}
		if it.OutputVideo != nil {
			v := *it.OutputVideo
			v.Type = "video"
			add(v)
		}
	}
	st.Text = strings.Join(texts, " ")
	if u := it.Usage; u != nil {
		st.Usage = Usage{Known: true, InputTokens: u.TotalInputTokens, OutputTokens: u.TotalOutputTokens, ThoughtTokens: u.TotalThoughtTokens}
		for _, m := range u.OutputTokensByModality {
			switch strings.ToLower(m.Modality) {
			case "video":
				st.Usage.VideoTokens += m.Tokens
			default:
				st.Usage.TextTokens += m.Tokens
			}
		}
	}
	var msgs []string
	for _, e := range it.Errors {
		msgs = append(msgs, strings.TrimSpace(string(e.Code)+" "+e.Message))
	}
	if it.Error != nil && it.Error.Message != "" {
		msgs = append(msgs, strings.TrimSpace(string(it.Error.Code)+" "+it.Error.Message))
	}
	detail := strings.Join(msgs, "; ")
	switch it.Status {
	case "completed":
		if len(st.Outputs) > 0 {
			st.State = StateDone
			return st
		}
		// Finished without a video: the output side of the safety filters, or a refusal in words.
		st.State = StateFailed
		why := strings.TrimSpace(strings.TrimSpace(st.Text + " " + detail))
		st.Safety = safetyFrom(why, "output")
		st.Safety.Blocked = true
		st.Err = &Error{Kind: KindSafety, Provider: GeminiName, Message: "Google Gemini finished without making a video: its safety filters (or the model) declined this shot.", Detail: why, Safety: st.Safety}
	case "failed":
		st.State = StateFailed
		if te := tierFailure(strings.TrimSpace(detail + " " + st.Text)); te != nil {
			// The free tier said in the request's own status rather than in an error answer (D178).
			st.Err = te
		} else if looksLikeSafety(detail + " " + st.Text) {
			st.Safety = safetyFrom(strings.TrimSpace(st.Text+" "+detail), "output")
			st.Safety.Blocked = true
			st.Err = &Error{Kind: KindSafety, Provider: GeminiName, Message: "Google Gemini declined to make this shot.", Detail: detail, Safety: st.Safety}
		} else {
			st.Err = &Error{Kind: KindServer, Provider: GeminiName, Message: "Google Gemini couldn't make this video.", Detail: firstNonEmpty(detail, st.Text)}
		}
	case "cancelled", "canceled":
		st.State = StateCanceled
	case "incomplete":
		st.Partial = true
		if len(st.Outputs) > 0 {
			st.State = StateDone
			return st
		}
		st.State = StateFailed
		st.Err = &Error{Kind: KindServer, Provider: GeminiName, Message: "Google Gemini stopped before the video was finished.", Detail: firstNonEmpty(detail, st.Text)}
	case "requires_action":
		st.State = StateFailed
		st.Err = &Error{Kind: KindInvalid, Provider: GeminiName, Message: "Google Gemini asked for something MetaRush doesn't do here.", Detail: detail}
	default: // queued, in_progress
		st.State = StateRunning
	}
	return st
}

var fileRe = regexp.MustCompile(`/files/([A-Za-z0-9_-]+)`)

// gfile is a Files API resource.
type gfile struct {
	Name       string `json:"name"`
	URI        string `json:"uri"`
	State      string `json:"state"`
	MimeType   string `json:"mimeType"`
	SizeBytes  string `json:"sizeBytes"`
	SHA256Hash string `json:"sha256Hash"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

var fileNameRe = regexp.MustCompile(`^files/[A-Za-z0-9_-]{1,128}$`)

// upload sends one clip through the Files API (resumable upload, one chunk),
// checks Google stored the same bytes, and waits until it can be used.
func (c *gclient) upload(ctx context.Context, m Media) (*gfile, error) {
	b, err := os.ReadFile(m.Path)
	if err != nil {
		return nil, err
	}
	meta, _ := json.Marshal(map[string]any{"file": map[string]any{"display_name": "metarush-" + shortHash(m.SHA256)}})
	res, err := c.call(ctx, "upload a clip", http.MethodPost, c.base+"/upload/v1beta/files", meta, map[string]string{
		"X-Goog-Upload-Protocol": "resumable", "X-Goog-Upload-Command": "start", "X-Goog-Upload-Header-Content-Length": strconv.Itoa(len(b)),
		"X-Goog-Upload-Header-Content-Type": m.MIME, "Content-Type": "application/json"}, time.Minute)
	if err != nil {
		return nil, err
	}
	if res.status >= 300 {
		return nil, classifyHTTP(res.status, res.body, res.header, "upload")
	}
	up := res.header.Get("X-Goog-Upload-URL")
	pu, perr := url.Parse(up)
	if up == "" || perr != nil || !c.sameHost(pu) {
		return nil, &Error{Kind: KindServer, Provider: GeminiName, Message: "Google didn't say where to upload the clip. MetaRush tries again by itself."}
	}
	res, err = c.call(ctx, "upload a clip", http.MethodPost, up, b, map[string]string{"X-Goog-Upload-Offset": "0", "X-Goog-Upload-Command": "upload, finalize",
		"Content-Type": m.MIME}, 10*time.Minute)
	if err != nil {
		return nil, err
	}
	if res.status >= 300 {
		return nil, classifyHTTP(res.status, res.body, res.header, "upload")
	}
	var wrap struct {
		File gfile `json:"file"`
	}
	if err := json.Unmarshal(res.body, &wrap); err != nil || !fileNameRe.MatchString(wrap.File.Name) {
		return nil, &Error{Kind: KindServer, Provider: GeminiName, Message: "Google answered the upload in a way MetaRush couldn't read.", Detail: firstLine(string(res.body), 300)}
	}
	f := &wrap.File
	if h := f.SHA256Hash; h != "" && shaMismatch(h, m.SHA256) {
		_ = c.deleteFiles(context.WithoutCancel(ctx), []string{f.Name})
		return nil, &Error{Kind: KindServer, Provider: GeminiName, Message: "Google's copy of the clip didn't match the original, so it wasn't used. MetaRush tries again by itself."}
	}
	if f, err = c.waitActive(ctx, f); err != nil {
		_ = c.deleteFiles(context.WithoutCancel(ctx), []string{wrap.File.Name})
		return nil, err
	}
	return f, nil
}

// shaMismatch reports whether Google's hash of an uploaded file is a SHA-256
// it can read (hex, base64 of the digest, or base64 of its hex) that differs
// from ours. A form it can't read is not a mismatch.
func shaMismatch(google, hexSum string) bool {
	hexSum = strings.ToLower(hexSum)
	if len(google) == 64 {
		if _, err := hex.DecodeString(google); err == nil {
			return !strings.EqualFold(google, hexSum)
		}
	}
	b, err := base64.StdEncoding.DecodeString(google)
	if err != nil {
		return false
	}
	switch {
	case len(b) == 32:
		return hex.EncodeToString(b) != hexSum
	case len(b) == 64:
		if _, err := hex.DecodeString(string(b)); err == nil {
			return !strings.EqualFold(string(b), hexSum)
		}
	}
	return false
}

// getFile reads a Files API resource.
func (c *gclient) getFile(ctx context.Context, name string) (*gfile, int, error) {
	if !fileNameRe.MatchString(name) {
		return nil, 0, &Error{Kind: KindInvalid, Provider: GeminiName, Message: "That isn't a file Google gave out."}
	}
	res, err := c.call(ctx, "check a file", http.MethodGet, c.base+"/v1beta/"+name, nil, nil, time.Minute)
	if err != nil {
		return nil, 0, err
	}
	if res.status >= 300 {
		return nil, res.status, classifyHTTP(res.status, res.body, res.header, "file")
	}
	var f gfile
	if err := json.Unmarshal(res.body, &f); err != nil || f.Name == "" {
		var wrap struct {
			File gfile `json:"file"`
		}
		if json.Unmarshal(res.body, &wrap) != nil || wrap.File.Name == "" {
			return nil, res.status, &Error{Kind: KindServer, Provider: GeminiName, Message: "Google answered in a way MetaRush couldn't read."}
		}
		f = wrap.File
	}
	return &f, res.status, nil
}

// waitActive waits until a file is ready to use (PROCESSING → ACTIVE).
func (c *gclient) waitActive(ctx context.Context, f *gfile) (*gfile, error) {
	every := c.g.PollFiles
	if every <= 0 {
		every = 2 * time.Second
	}
	deadline := time.Now().Add(5 * time.Minute)
	for f.State == "PROCESSING" || f.State == "STATE_UNSPECIFIED" {
		if time.Now().After(deadline) {
			return nil, &Error{Kind: KindServer, Provider: GeminiName, Message: "Google took too long to get a file ready. MetaRush tries again by itself."}
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(every):
		}
		nf, _, err := c.getFile(ctx, f.Name)
		if err != nil {
			return nil, err
		}
		f = nf
	}
	if f.State == "FAILED" {
		msg := ""
		if f.Error != nil {
			msg = f.Error.Message
		}
		return nil, &Error{Kind: KindServer, Provider: GeminiName, Message: "Google couldn't process the file.", Detail: msg}
	}
	return f, nil
}

// Fetch writes one output: inline bytes, or a download from Google's files
// (waiting until the file is ready). A file Google no longer has is KindExpired.
func (g Gemini) Fetch(ctx context.Context, key string, t *Ticket, out *Output, dst io.Writer) (int64, error) {
	if len(out.Data) > 0 {
		n, err := dst.Write(out.Data)
		return int64(n), err
	}
	c, err := g.client(key)
	if err != nil {
		return 0, err
	}
	u, err := url.Parse(out.URI)
	if err != nil || (u.Scheme != "https" && !(u.Scheme == "http" && loopbackHost(u.Hostname()))) {
		return 0, &Error{Kind: KindInvalid, Provider: GeminiName, Message: "Google sent an address for the video that MetaRush won't use."}
	}
	if out.FileID != "" && c.sameHost(u) {
		f, status, err := c.getFile(ctx, out.FileID)
		if err != nil {
			if status == http.StatusNotFound || status == http.StatusForbidden {
				return 0, expired()
			}
			return 0, err
		}
		if _, err := c.waitActive(ctx, f); err != nil {
			return 0, err
		}
	}
	dl := out.URI
	if c.sameHost(u) && !strings.Contains(u.RawQuery, "alt=media") {
		dl = out.URI + map[bool]string{true: "&", false: "?"}[u.RawQuery != ""] + "alt=media"
	}
	res, err := c.callTraced(ctx, "download a video", http.MethodGet, dl, nil, nil, 15*time.Minute, nil, dst)
	if err != nil {
		return 0, err
	}
	switch {
	case res.status == http.StatusNotFound || res.status == http.StatusGone || res.status == http.StatusForbidden:
		return 0, expired()
	case res.status >= 300:
		return 0, classifyHTTP(res.status, res.body, res.header, "download")
	}
	return res.n, nil
}

func expired() *Error {
	return &Error{Kind: KindExpired, Provider: GeminiName, Message: "Google no longer has the finished video, so MetaRush couldn't download it.",
		Hint: "Generate it again. MetaRush downloads a video as soon as it is ready, so this happens only when the computer that asked was off for a long time."}
}

// Cancel stops the interaction.
func (g Gemini) Cancel(ctx context.Context, key string, t *Ticket) error {
	c, err := g.client(key)
	if err != nil {
		return err
	}
	u, err := c.interactionURL(t.ID, "/cancel")
	if err != nil {
		return err
	}
	res, err := c.call(ctx, "cancel a video", http.MethodPost, u, nil, nil, time.Minute)
	if err != nil {
		return err
	}
	if res.status >= 300 && res.status != http.StatusNotFound {
		return classifyHTTP(res.status, res.body, res.header, "cancel")
	}
	return nil
}

// Forget deletes the interaction, the uploaded clips and the produced files.
func (g Gemini) Forget(ctx context.Context, key string, t *Ticket, outs []Output) error {
	c, err := g.client(key)
	if err != nil {
		return err
	}
	var problems []string
	if t != nil && t.ID != "" {
		u, err := c.interactionURL(t.ID, "")
		if err == nil {
			res, err := c.call(ctx, "delete a finished request", http.MethodDelete, u, nil, nil, time.Minute)
			switch {
			case err != nil:
				problems = append(problems, err.Error())
			case res.status >= 300 && res.status != http.StatusNotFound:
				problems = append(problems, fmt.Sprintf("the request: %d %s", res.status, firstLine(string(res.body), 200)))
			}
		}
	}
	var files []string
	if t != nil {
		files = append(files, t.Files...)
	}
	for _, o := range outs {
		if o.FileID != "" {
			files = append(files, o.FileID)
		}
	}
	if err := c.deleteFiles(ctx, files); err != nil {
		problems = append(problems, err.Error())
	}
	if len(problems) > 0 {
		return &Error{Kind: KindServer, Provider: GeminiName, Message: "Google didn't delete everything it kept for this request; it deletes files by itself after two days.", Detail: strings.Join(problems, "; ")}
	}
	return nil
}

func (c *gclient) deleteFiles(ctx context.Context, names []string) error {
	var problems []string
	for _, n := range names {
		if !fileNameRe.MatchString(n) {
			continue
		}
		res, err := c.call(ctx, "delete a file", http.MethodDelete, c.base+"/v1beta/"+n, nil, nil, time.Minute)
		switch {
		case err != nil:
			problems = append(problems, err.Error())
		case res.status >= 300 && res.status != http.StatusNotFound:
			problems = append(problems, fmt.Sprintf("%s: %d", n, res.status))
		}
	}
	if len(problems) > 0 {
		return errors.New(strings.Join(problems, "; "))
	}
	return nil
}

// ListModels lists the models a key may use (the key check).
func (g Gemini) ListModels(ctx context.Context, key string) ([]string, error) {
	c, err := g.client(key)
	if err != nil {
		return nil, err
	}
	var out []string
	token := ""
	for page := 0; page < 10; page++ {
		u := c.base + "/v1beta/models?pageSize=1000"
		if token != "" {
			u += "&pageToken=" + url.QueryEscape(token)
		}
		res, err := c.call(ctx, "check the key", http.MethodGet, u, nil, nil, 30*time.Second)
		if err != nil {
			return nil, err
		}
		if res.status >= 300 {
			return nil, classifyHTTP(res.status, res.body, res.header, "key")
		}
		var list struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
			NextPageToken string `json:"nextPageToken"`
		}
		if err := json.Unmarshal(res.body, &list); err != nil {
			return nil, &Error{Kind: KindServer, Provider: GeminiName, Message: "Google answered in a way MetaRush couldn't read."}
		}
		for _, m := range list.Models {
			out = append(out, strings.TrimPrefix(m.Name, "models/"))
		}
		if list.NextPageToken == "" {
			break
		}
		token = list.NextPageToken
	}
	return out, nil
}

func shortHash(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	if s == "" {
		sum := sha256.Sum256([]byte(time.Now().String()))
		return hex.EncodeToString(sum[:6])
	}
	return s
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
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

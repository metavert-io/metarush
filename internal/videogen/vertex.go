package videogen

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

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/providers"
)

// Veo through Google Cloud's Vertex AI (D282): Veo left the Gemini API on
// 22 October 2026 and is offered only in a person's own Google Cloud
// project. A request is one predictLongRunning call that returns an
// operation, asked about with fetchPredictOperation until it is done. No
// Cloud Storage bucket is named, so the video comes back in the answer
// itself and nothing is left in a bucket; Google keeps what it caches for at
// most a day and doesn't train on it (its data terms, in the catalogue).
// The service account's key stays on this computer: each request carries an
// access token minted from it (internal/providers), only to the region's
// Vertex AI host.

// Vertex AI's provider names.
const (
	VertexName  = providers.VertexName
	VertexTitle = "Google Cloud Vertex AI"
)

// Vertex is the Veo adapter on Vertex AI.
type Vertex struct {
	// HTTP overrides the client (tests).
	HTTP *http.Client
}

func init() {
	Register(Vertex{})
	providers.Register(vertexKey{})
}

// Name is the provider ID.
func (Vertex) Name() string { return VertexName }

// Title is the display name.
func (Vertex) Title() string { return VertexTitle }

// Capabilities reads the model's catalogue entry.
func (Vertex) Capabilities(model string) (*Capabilities, error) { return ModelInfo(VertexName, model) }

// Estimate prices a request with the table in force at t (by the second).
func (Vertex) Estimate(req *Request, at time.Time) (*Estimate, error) {
	m, err := ModelInfo(VertexName, req.Model)
	if err != nil {
		return nil, err
	}
	pt, err := PriceAt(VertexName, m.ID, at)
	if err != nil {
		return nil, err
	}
	return pt.Estimate(req)
}

type vclient struct {
	base, token, project, region string
	http                         *http.Client
}

// client signs in for one request: the project and region, and an access
// token minted from the service account's key.
func (v Vertex) client(ctx context.Context, key string) (*vclient, error) {
	k, err := providers.ParseVertexKey(key)
	if err != nil {
		return nil, &Error{Kind: KindKey, Provider: VertexName, Message: "The Google Cloud key on this computer isn't a service account's key MetaRush can use.",
			Hint: "Give MetaRush the service account's key again in Settings, Integrations."}
	}
	region := "us-central1"
	if info, err := Info(VertexName); err == nil && info.Region != "" {
		region = info.Region
	}
	base, err := providers.VertexBaseURL(region)
	if err != nil {
		return nil, &Error{Kind: KindInvalid, Provider: VertexName, Message: err.Error()}
	}
	token, err := providers.VertexAccessToken(ctx, key)
	if err != nil {
		var ae *providers.VertexAuthError
		switch {
		case errors.As(err, &ae) && ae.Status == 0:
			return nil, &Error{Kind: KindNetwork, Provider: VertexName, Message: ae.Message}
		case errors.As(err, &ae) && ae.Status >= 500:
			return nil, &Error{Kind: KindServer, Provider: VertexName, Message: ae.Message}
		case errors.As(err, &ae):
			return nil, &Error{Kind: KindKey, Provider: VertexName, HTTPStatus: ae.Status, Message: ae.Message, Detail: ae.Reason,
				Hint: "Make a new key for the service account in the Google Cloud console and give it to MetaRush in Settings, Integrations."}
		}
		return nil, &Error{Kind: KindKey, Provider: VertexName, Message: "MetaRush couldn't sign in to Google Cloud with the key on this computer.", Detail: err.Error()}
	}
	hc := v.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	return &vclient{base: base, token: token, project: k.ProjectID, region: region, http: hc}, nil
}

func (c *vclient) modelURL(model, verb string) string {
	return fmt.Sprintf("%s/v1/projects/%s/locations/%s/publishers/google/models/%s:%s", c.base, url.PathEscape(c.project), c.region, url.PathEscape(model), verb)
}

// post sends one request with the token in the Authorization header, only to
// the region's Vertex AI host (or the test server). what says what it does,
// for the activity log (never the token, the prompt or the answer).
func (c *vclient) post(ctx context.Context, what, rawURL string, body any, timeout time.Duration, wrote *bool) (res *response, err error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, &Error{Kind: KindInvalid, Provider: VertexName, Message: "MetaRush made an address it can't use."}
	}
	raw, _ := json.Marshal(body)
	call := logbook.ProviderCall{Provider: VertexName, Title: VertexTitle, What: what, Method: http.MethodPost, Endpoint: u.Path}
	start := time.Now()
	defer func() {
		call.Duration = time.Since(start)
		if res != nil {
			call.Status, call.Bytes = res.status, int(res.n)
		}
		logbook.ProviderDone(ctx, call, err)
	}()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if wrote != nil {
		ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{WroteRequest: func(httptrace.WroteRequestInfo) { *wrote = true }})
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, rawURL, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	if b, err := url.Parse(c.base); err == nil && strings.EqualFold(b.Host, u.Host) && b.Scheme == u.Scheme {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	r, err := c.http.Do(req)
	if err != nil {
		return nil, &Error{Kind: KindNetwork, Provider: VertexName, Message: "MetaRush couldn't reach Google Cloud. Check the internet connection; it tries again by itself.", Detail: err.Error()}
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 512<<20))
	res = &response{status: r.StatusCode, header: r.Header, body: data, n: int64(len(data))}
	if err != nil {
		return res, &Error{Kind: KindNetwork, Provider: VertexName, Message: "The connection to Google Cloud dropped. MetaRush tries again by itself.", Detail: err.Error()}
	}
	return res, nil
}

type vertexImage struct {
	BytesBase64Encoded string `json:"bytesBase64Encoded"`
	MimeType           string `json:"mimeType"`
}

func vertexInline(m Media) (*vertexImage, error) {
	b, err := os.ReadFile(m.Path)
	if err != nil {
		return nil, err
	}
	return &vertexImage{BytesBase64Encoded: base64.StdEncoding.EncodeToString(b), MimeType: m.MIME}, nil
}

// Submit starts one video: the words, and pictures for a start frame, a
// first and last frame or references; the length, shape and resolution;
// sound on, adults allowed (people's likenesses go through MetaRush's looks
// and consent first, D126).
func (v Vertex) Submit(ctx context.Context, key string, req *Request) (*Ticket, error) {
	c, err := v.client(ctx, key)
	if err != nil {
		return nil, err
	}
	instance := map[string]any{"prompt": req.Prompt}
	imgs := make([]*vertexImage, 0, len(req.Images))
	for _, m := range req.Images {
		im, err := vertexInline(m)
		if err != nil {
			return nil, err
		}
		imgs = append(imgs, im)
	}
	switch req.Task {
	case TaskImageToVideo:
		if len(imgs) > 0 {
			instance["image"] = imgs[0]
		}
	case TaskInterpolate:
		if len(imgs) >= 2 {
			instance["image"], instance["lastFrame"] = imgs[0], imgs[1]
		}
	case TaskReferenceToVideo:
		if req.Model == "veo-3.1-generate-001" && req.Seconds != 8 {
			return nil, &Error{Kind: KindInvalid, Provider: VertexName, Message: "Veo 3.1 makes shots from pictures of what should appear only 8 seconds long."}
		}
		var refs []map[string]any
		for _, im := range imgs {
			refs = append(refs, map[string]any{"image": im, "referenceType": "asset"})
		}
		instance["referenceImages"] = refs
	case TaskEdit, TaskExtend:
		return nil, &Error{Kind: KindInvalid, Provider: VertexName, Message: "MetaRush doesn't change or extend clips with Veo yet; make a new shot instead."}
	}
	params := map[string]any{"sampleCount": 1, "durationSeconds": req.Seconds, "aspectRatio": req.AspectRatio, "resolution": strings.ToLower(req.Resolution),
		"generateAudio": true, "personGeneration": "allow_adult"}
	wrote := false
	res, err := c.post(ctx, "start a Veo video", c.modelURL(req.Model, "predictLongRunning"), map[string]any{"instances": []any{instance}, "parameters": params}, 2*time.Minute, &wrote)
	if err != nil {
		if wrote {
			// It left this computer but no answer came: Google may have started it.
			return nil, &Error{Kind: KindUncertain, Provider: VertexName, Message: "The request reached Google Cloud, but no answer came back, so MetaRush can't tell whether Veo started the video.",
				Detail: err.Error()}
		}
		return nil, err
	}
	if res.status >= 300 {
		return nil, classifyVertex(res.status, res.body, res.header, "submit", c.project)
	}
	var op struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(res.body, &op); err != nil || !strings.Contains(op.Name, "/operations/") {
		return nil, &Error{Kind: KindUncertain, Provider: VertexName, Message: "Google Cloud answered in a way MetaRush couldn't read, so it can't tell whether the video was started.",
			Detail: firstLine(string(res.body), 300)}
	}
	return &Ticket{ID: op.Name, SubmittedAt: time.Now().UTC()}, nil
}

// modelOf is the model in an operation's name (…/models/<model>/operations/<id>).
func modelOf(op string) string {
	parts := strings.Split(op, "/")
	for i := 0; i+1 < len(parts); i++ {
		if parts[i] == "models" {
			return parts[i+1]
		}
	}
	return ""
}

// Poll asks how the video is going.
func (v Vertex) Poll(ctx context.Context, key string, t *Ticket) (*Status, error) {
	c, err := v.client(ctx, key)
	if err != nil {
		return nil, err
	}
	model := modelOf(t.ID)
	if model == "" {
		return nil, &Error{Kind: KindNotFound, Provider: VertexName, Message: "MetaRush doesn't know which Veo request this was."}
	}
	res, err := c.post(ctx, "ask about a Veo video", c.modelURL(model, "fetchPredictOperation"), map[string]string{"operationName": t.ID}, 3*time.Minute, nil)
	if err != nil {
		return nil, err
	}
	if res.status >= 300 {
		return nil, classifyVertex(res.status, res.body, res.header, "poll", c.project)
	}
	var op struct {
		Done     bool `json:"done"`
		Response struct {
			Videos []struct {
				BytesBase64Encoded string `json:"bytesBase64Encoded"`
				GcsURI             string `json:"gcsUri"`
				MimeType           string `json:"mimeType"`
			} `json:"videos"`
			RaiMediaFilteredCount   int      `json:"raiMediaFilteredCount"`
			RaiMediaFilteredReasons []string `json:"raiMediaFilteredReasons"`
		} `json:"response"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(res.body, &op); err != nil {
		return nil, &Error{Kind: KindServer, Provider: VertexName, Message: "Google Cloud answered in a way MetaRush couldn't read. It asks again.", Detail: firstLine(string(res.body), 300)}
	}
	if !op.Done {
		return &Status{State: StateRunning}, nil
	}
	if op.Error != nil {
		e := &Error{Kind: KindServer, Provider: VertexName, Message: "Veo couldn't make this video.", Detail: firstLine(op.Error.Message, 400)}
		switch {
		case looksLikeSafety(op.Error.Message):
			e.Kind, e.Message, e.Safety = KindSafety, "Veo declined this request: Google's safety filters blocked the prompt or a picture.", safetyFrom(op.Error.Message, "input")
			e.Safety.Blocked = true
		case op.Error.Code == 3: // INVALID_ARGUMENT
			e.Kind, e.Message = KindInvalid, "Veo couldn't use these settings: "+firstLine(op.Error.Message, 200)
		case op.Error.Code == 8: // RESOURCE_EXHAUSTED
			e.Kind, e.Message = KindRate, "Google Cloud's quota for Veo in this project ran out while the video was being made."
		}
		return &Status{State: StateFailed, Err: e, Safety: e.Safety}, nil
	}
	st := &Status{State: StateDone}
	for _, vid := range op.Response.Videos {
		if vid.BytesBase64Encoded == "" {
			continue // a bucket's address: MetaRush never names a bucket, so it can't read one
		}
		b, err := base64.StdEncoding.DecodeString(vid.BytesBase64Encoded)
		if err != nil || len(b) == 0 {
			continue
		}
		st.Outputs = append(st.Outputs, Output{MIME: firstNonEmptyText(vid.MimeType, "video/mp4"), Data: b})
	}
	if len(st.Outputs) == 0 {
		reasons := strings.Join(op.Response.RaiMediaFilteredReasons, " ")
		if op.Response.RaiMediaFilteredCount > 0 {
			s := safetyFrom(reasons, "output")
			s.Blocked, s.FilteredCount = true, op.Response.RaiMediaFilteredCount
			return &Status{State: StateFailed, Safety: s, Err: &Error{Kind: KindSafety, Provider: VertexName, Safety: s,
				Message: "Veo made the video, but Google's safety filters held it back.", Detail: firstLine(reasons, 400)}}, nil
		}
		return &Status{State: StateFailed, Err: &Error{Kind: KindUnusable, Provider: VertexName, Message: "Google Cloud finished without sending a video back."}}, nil
	}
	return st, nil
}

// Fetch writes an output's bytes (they came in the answer).
func (Vertex) Fetch(_ context.Context, _ string, _ *Ticket, out *Output, dst io.Writer) (int64, error) {
	if len(out.Data) == 0 {
		return 0, &Error{Kind: KindExpired, Provider: VertexName, Message: "The video Google Cloud sent is gone; make it again."}
	}
	n, err := dst.Write(out.Data)
	return int64(n), err
}

// Cancel has no way to stop Veo: Google Cloud offers none for these
// requests, so the video finishes and is never kept.
func (Vertex) Cancel(context.Context, string, *Ticket) error { return nil }

// Forget has nothing to delete: no bucket was used and the video came in
// the answer; Google's cache keeps it at most a day.
func (Vertex) Forget(context.Context, string, *Ticket, []Output) error { return nil }

// classifyVertex says what an error answer from Vertex AI means, in plain
// words, for the person whose project it is.
func classifyVertex(status int, body []byte, header http.Header, phase, project string) *Error {
	var ge googleError
	_ = json.Unmarshal(body, &ge)
	msg := strings.TrimSpace(ge.Error.Message)
	if msg == "" {
		msg = firstLine(string(body), 300)
	}
	var reasons []string
	for _, d := range ge.Error.Details {
		if d.Reason != "" {
			reasons = append(reasons, d.Reason)
		}
	}
	low := strings.ToLower(msg + " " + ge.Error.Status + " " + strings.Join(reasons, " "))
	e := &Error{Provider: VertexName, HTTPStatus: status, Detail: firstLine(strings.TrimSpace(strconv.Itoa(status)+" "+ge.Error.Status+" "+msg), 600)}
	if s, err := strconv.Atoi(header.Get("Retry-After")); err == nil && s > 0 {
		e.RetryAfter = time.Duration(s) * time.Second
	}
	switch {
	case status == http.StatusUnauthorized:
		e.Kind, e.Message = KindKey, "Google Cloud didn't accept the service account's sign-in."
		e.Hint = "Give MetaRush the service account's key again in Settings, Integrations."
	case strings.Contains(low, "service_disabled") || strings.Contains(low, "has not been used") || strings.Contains(low, "it is disabled"):
		e.Kind = KindPermission
		e.Message = fmt.Sprintf("The Vertex AI API isn't turned on in the Google Cloud project “%s”.", project)
		e.Hint = "Turn it on in the Google Cloud console (APIs & Services → Vertex AI API), then try again. Nothing was made or charged."
	case strings.Contains(low, "billing"):
		e.Kind = KindPermission
		e.Message = fmt.Sprintf("The Google Cloud project “%s” has no billing account, so Veo can't make video there.", project)
		e.Hint = "Link a billing account to the project in the Google Cloud console, then try again. Nothing was made or charged."
	case status == http.StatusForbidden || ge.Error.Status == "PERMISSION_DENIED":
		e.Kind = KindPermission
		e.Message = fmt.Sprintf("The service account isn't allowed to use Veo in the Google Cloud project “%s”.", project)
		e.Hint = "Give it the Vertex AI User role (IAM & Admin → IAM), then try again. Nothing was made or charged."
	case status == http.StatusTooManyRequests || ge.Error.Status == "RESOURCE_EXHAUSTED":
		e.Kind, e.Message = KindRate, "Google Cloud's quota for Veo in this project is used up for now. MetaRush tries again in a minute."
		if e.RetryAfter == 0 {
			e.RetryAfter = time.Minute
		}
	case status == http.StatusNotFound && phase == "submit":
		e.Kind = KindRetired
		e.Message = "Google Cloud doesn't offer this Veo model in the project's region, or the project can't use it."
		e.Hint = "Choose another model."
	case status == http.StatusNotFound:
		e.Kind, e.Message = KindNotFound, "Google Cloud doesn't have that request any more."
	case status < 500 && looksLikeSafety(low):
		e.Kind, e.Message = KindSafety, "Veo declined this request: Google's safety filters blocked the prompt or a picture."
		e.Safety = safetyFrom(msg, "input")
		e.Safety.Blocked = true
		e.Hint = "Change the prompt or the pictures."
	case status >= 500:
		e.Kind, e.Message = KindServer, "Google Cloud had a problem on its side. MetaRush tries again by itself."
	default:
		e.Kind, e.Message = KindInvalid, "Veo couldn't use these settings: "+firstLine(msg, 200)
	}
	return e
}

func firstNonEmptyText(vs ...string) string {
	for _, v := range vs {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// vertexKey is Google Cloud's Vertex AI in the integrations registry: its
// "key" is a service account's key, kept as providers keeps every key, and
// checked by signing in with it (D282).
type vertexKey struct{}

func (vertexKey) Name() string     { return VertexName }
func (vertexKey) Title() string    { return VertexTitle }
func (vertexKey) Category() string { return providers.CategoryVideo }
func (vertexKey) Summary() string {
	return "Veo video from your own Google Cloud project: shots of 4, 6 or 8 seconds, up to 4K, with sound, from words, a frame of your footage, two frames or pictures of what should appear. Google Cloud bills the project by the second, and MetaRush shows the price before anything is made."
}
func (vertexKey) KeyHelp() string {
	return "In the Google Cloud console, choose a project with billing, turn on the Vertex AI API, create a service account with the Vertex AI User role and add a key to it; Google downloads a key file. Give that file to MetaRush (Settings, Integrations, or a terminal), then delete the download."
}
func (vertexKey) Capabilities() []string {
	return []string{"text-to-video", "image-to-video", "references", "between-frames"}
}

// NormalizeKey keeps what MetaRush needs of a service account's key file.
func (vertexKey) NormalizeKey(raw string) (string, error) { return providers.NormalizeVertexKey(raw) }

// DescribeKey names the service account instead of showing the key.
func (vertexKey) DescribeKey(key string) string { return providers.VertexKeyWords(key) }

// ServiceHosts are Vertex AI's hosts, every region's: its key never goes to
// an ability, but an ability calling Vertex AI with a key of its own still
// reaches it (D281).
func (vertexKey) ServiceHosts() []string {
	return []string{"aiplatform.googleapis.com", "*-aiplatform.googleapis.com"}
}

// Test signs in with the key. Whether the project may use Veo (the API
// turned on, billing, the role) shows on the first shot, which says what to
// do and costs nothing when it's refused.
func (vertexKey) Test(ctx context.Context, key string) (*providers.TestResult, error) {
	k, err := providers.ParseVertexKey(key)
	if err != nil {
		return &providers.TestResult{OK: false, Message: apperr.From(err).Message}, nil
	}
	if _, err := providers.VertexAccessToken(ctx, key); err != nil {
		var ae *providers.VertexAuthError
		if errors.As(err, &ae) && ae.Status != 0 && ae.Status < 500 {
			return &providers.TestResult{OK: false, Message: ae.Message}, nil
		}
		return nil, err
	}
	return &providers.TestResult{OK: true, Message: fmt.Sprintf("Signed in to Google Cloud as %s. The first shot says whether its project may use Veo yet.", k.ClientEmail)}, nil
}

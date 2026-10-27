// Package heygen is HeyGen's presenters on the HeyGen key (D273, D275): its
// stock presenters reading sound MetaRush supplies. Request mappings follow
// HeyGen's v3 API reference as read on 25 Oct 2026 (v1 and v2 retire on 31
// Oct 2026); nothing here has been called against the real service yet.
//
// A person's own presenter at HeyGen isn't built: on self-serve accounts the
// person records HeyGen's consent on HeyGen's own page, and HeyGen trains on
// what it is sent unless the account opts out (the catalogue says both).
//
// The sound is uploaded as an asset, the video asked for with an idempotency
// key (MetaRush's tag), polled, downloaded from the link HeyGen gives (without
// the key), then the video and the asset are deleted. The key goes only in the
// X-Api-Key header, only to HeyGen's API host or a loopback test server
// (providers.HeyGenBaseURL).
package heygen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/logbook"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/videogen"
)

// Name is the provider's ID in the catalogue; Title how people name it.
const (
	Name  = providers.HeyGenName
	Title = providers.HeyGenTitle
)

func init() {
	aiservices.Register(&Adapter{})
}

// Adapter is HeyGen's presenter clips.
type Adapter struct {
	// HTTP overrides the client (tests); nil uses a default one.
	HTTP *http.Client
}

// Provider is the catalogue's provider ID.
func (a *Adapter) Provider() string { return Name }

// Kind is the kind of work it does.
func (a *Adapter) Kind() string { return aiservices.KindPresenter }

// Capabilities reads the model's catalogue entry.
func (a *Adapter) Capabilities(model string) (*aiservices.Model, error) {
	return aiservices.ModelFor(Name, aiservices.KindPresenter, model)
}

// Estimate prices a request from the catalogue.
func (a *Adapter) Estimate(req *aiservices.Request, at time.Time) (*aiservices.Estimate, error) {
	m, err := aiservices.ModelFor(Name, aiservices.KindPresenter, req.Model)
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
	base, err := providers.HeyGenBaseURL()
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

func (c *client) call(ctx context.Context, what, method, path string, body io.Reader, contentType, idem string, timeout time.Duration, wrote *bool) (res *answer, err error) {
	endpoint, _, _ := strings.Cut(path, "?")
	call := logbook.ProviderCall{Provider: Name, Title: Title, What: what, Method: method, Endpoint: endpoint}
	start := time.Now()
	defer func() {
		call.Duration = time.Since(start)
		if res != nil {
			call.Status, call.Bytes, call.RequestID = res.status, len(res.body), res.header.Get("X-HeyGen-Request-Id")
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
	req.Header.Set("X-Api-Key", c.key) // the base is HeyGen's host or a loopback test server (HeyGenBaseURL)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	if idem != "" {
		req.Header.Set("Idempotency-Key", idem)
	}
	r, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &videogen.Error{Kind: videogen.KindNetwork, Provider: Name, Message: "MetaRush couldn't reach HeyGen. Check the internet connection; it tries again by itself.", Detail: err.Error()}
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	res = &answer{status: r.StatusCode, header: r.Header, body: data}
	if err != nil {
		return res, &videogen.Error{Kind: videogen.KindNetwork, Provider: Name, Message: "The connection to HeyGen dropped. MetaRush tries again by itself.", Detail: err.Error()}
	}
	return res, nil
}

// do sends a request and reads the answer's data (out may be nil).
func (c *client) do(ctx context.Context, what, method, path string, in, out any, idem string, timeout time.Duration, wrote *bool) error {
	var body io.Reader
	ct := ""
	if in != nil {
		b, _ := json.Marshal(in)
		body, ct = bytes.NewReader(b), "application/json"
	}
	res, err := c.call(ctx, what, method, path, body, ct, idem, timeout, wrote)
	if err != nil {
		if wrote != nil && *wrote && !errors.Is(err, context.Canceled) {
			return &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "The request reached HeyGen, but no answer came back, so MetaRush can't tell whether it was made.", Detail: err.Error()}
		}
		return err
	}
	if res.status >= 300 {
		return classify(res)
	}
	if out != nil && len(bytes.TrimSpace(res.body)) > 0 {
		var env struct {
			Data json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(res.body, &env); err != nil || len(env.Data) == 0 || json.Unmarshal(env.Data, out) != nil {
			if wrote != nil {
				// A call that makes something answered in a way MetaRush can't
				// read: it may have been made, and charged. Never sent again (D277).
				return &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "HeyGen answered, but MetaRush couldn't read the answer, so it can't tell what was made.",
					Detail: firstLine(string(res.body), 300)}
			}
			return &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "HeyGen sent an answer MetaRush couldn't read. It tries again by itself.", Detail: firstLine(string(res.body), 300)}
		}
	}
	return nil
}

// classify turns an error answer into a plain-words Error (never the key).
func classify(res *answer) *videogen.Error {
	var b struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(res.body, &b)
	code, msg := b.Error.Code, b.Error.Message
	if code == "" && msg == "" {
		msg = firstLine(string(res.body), 300)
	}
	e := &videogen.Error{Provider: Name, HTTPStatus: res.status, Detail: firstLine(strings.TrimSpace(code+": "+msg), 600)}
	if s, err := strconv.Atoi(res.header.Get("Retry-After")); err == nil && s > 0 {
		e.RetryAfter = time.Duration(s) * time.Second
	}
	switch {
	case res.status == http.StatusUnauthorized:
		e.Kind, e.Message, e.Hint = videogen.KindKey, "HeyGen did not accept the key. Check that it was copied completely and hasn't been deleted.", "Add the key again in Settings → Integrations."
	case res.status == http.StatusPaymentRequired:
		e.Kind, e.Message, e.Hint = videogen.KindPermission, "Your HeyGen balance doesn't cover this.", "Add to your HeyGen balance, then try again."
	case res.status == http.StatusForbidden:
		e.Kind, e.Message = videogen.KindPermission, "The HeyGen key may not do this."
	case res.status == http.StatusTooManyRequests:
		e.Kind, e.Message = videogen.KindRate, "HeyGen is busy with this key's requests right now. MetaRush tries again in a minute."
		if e.RetryAfter == 0 {
			e.RetryAfter = time.Minute
		}
	case code == "content_policy_violation":
		e.Kind, e.Message, e.Hint = videogen.KindSafety, "HeyGen declined this: its checks found something its policy doesn't allow.", "Change the sound and try again."
		e.Safety = &videogen.Safety{Blocked: true, Side: "input", Category: "other", Message: firstLine(msg, 300)}
	case code == "avatar_not_usable" || code == "avatar_expired" || code == "avatar_not_found":
		e.Kind, e.Message, e.Hint = videogen.KindRetired, "HeyGen no longer offers that stock presenter.", "Choose another one."
	case res.status == http.StatusConflict:
		e.Kind, e.Message = videogen.KindServer, "HeyGen is still working on it. MetaRush tries again by itself."
	case res.status == http.StatusNotFound:
		e.Kind, e.Message = videogen.KindNotFound, "HeyGen doesn't have that any more."
	case res.status >= 500:
		e.Kind, e.Message = videogen.KindServer, "HeyGen had a problem on its side. MetaRush tries again by itself."
	case code == "video_too_long":
		e.Kind, e.Message = videogen.KindInvalid, "HeyGen reads at most 10 minutes of sound at a time. Split the lines into shorter parts."
	default:
		e.Kind, e.Message = videogen.KindInvalid, "HeyGen couldn't use this."
		if msg != "" {
			e.Message += " It said: " + firstLine(msg, 200)
		}
	}
	return e
}

// StockPresenters lists HeyGen's public studio presenters (aiservices.StockLister).
func (a *Adapter) StockPresenters(ctx context.Context, key string) ([]aiservices.StockPresenter, error) {
	c, err := a.client(key)
	if err != nil {
		return nil, err
	}
	var out []aiservices.StockPresenter
	token := ""
	for page := 0; page < 10; page++ {
		q := url.Values{"ownership": {"public"}, "avatar_type": {"studio_avatar"}, "limit": {"50"}}
		if token != "" {
			q.Set("token", token)
		}
		var res struct {
			Data []struct {
				ID      string `json:"id"`
				Name    string `json:"name"`
				Gender  string `json:"gender"`
				Preview string `json:"preview_image_url"`
				Status  string `json:"status"`
			} `json:"data"`
			HasMore   bool   `json:"has_more"`
			NextToken string `json:"next_token"`
		}
		rq, err := c.call(ctx, "listed its stock presenters", http.MethodGet, "/v3/avatars/looks?"+q.Encode(), nil, "", "", 60*time.Second, nil)
		if err != nil {
			return nil, err
		}
		if rq.status >= 300 {
			return nil, classify(rq)
		}
		if err := json.Unmarshal(rq.body, &res); err != nil {
			return nil, &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "HeyGen sent a list MetaRush couldn't read."}
		}
		for _, l := range res.Data {
			if l.Status != "" && l.Status != "completed" {
				continue
			}
			out = append(out, aiservices.StockPresenter{ID: l.ID, Name: l.Name, Gender: l.Gender, Preview: l.Preview})
		}
		if !res.HasMore || res.NextToken == "" {
			break
		}
		token = res.NextToken
	}
	return out, nil
}

type video struct {
	ID             string  `json:"id"`
	VideoID        string  `json:"video_id"`
	Status         string  `json:"status"`
	VideoURL       string  `json:"video_url"`
	Duration       float64 `json:"duration"`
	FailureCode    string  `json:"failure_code"`
	FailureMessage string  `json:"failure_message"`
}

// Submit uploads the sound and asks for a video of the presenter reading it.
func (a *Adapter) Submit(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	c, err := a.client(key)
	if err != nil {
		return nil, err
	}
	in := req.Input(aiservices.RoleAudio)
	if in == nil || in.Path == "" {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "Choose the sound the presenter reads."}
	}
	if !req.Options.Stock {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "MetaRush makes only HeyGen's stock presenters read lines: a person's own presenter there isn't built yet."}
	}
	// 1. The sound, as an asset (deleted after the download).
	b, err := os.ReadFile(in.Path)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("file", firstNonEmpty(in.Name, "sound"+aiservices.ExtFor(in.MIME)))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	// The upload carries its own idempotency key, so a retry gets the same asset.
	soundKey := ""
	if req.Tag != "" {
		soundKey = req.Tag + ".sound"
	}
	uploaded := false
	res, err := c.call(ctx, "uploaded the sound for a clip", http.MethodPost, "/v3/assets", &buf, w.FormDataContentType(), soundKey, 2*time.Minute, &uploaded)
	if err != nil {
		if uploaded && !errors.Is(err, context.Canceled) {
			return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "The sound reached HeyGen, but no answer came back.", Detail: err.Error()}
		}
		return nil, err
	}
	if res.status >= 300 {
		return nil, classify(res)
	}
	var up struct {
		Data struct {
			AssetID string `json:"asset_id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.body, &up); err != nil || up.Data.AssetID == "" {
		return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "HeyGen answered the sound's upload in a way MetaRush couldn't read.", Detail: firstLine(string(res.body), 300)}
	}
	files := []string{"assets/" + up.Data.AssetID}
	// 2. The video, asked for once: the tag is the idempotency key, so a
	// retry after a lost answer gets the same video, never a second one.
	body := map[string]any{"type": "avatar", "avatar_id": req.Options.Presenter, "audio_asset_id": up.Data.AssetID, "resolution": "1080p"}
	if req.Tag != "" {
		body["title"] = req.Tag
	}
	if ar := strings.TrimSpace(req.Options.AspectRatio); ar != "" {
		body["aspect_ratio"] = ar
	}
	if bg := strings.TrimSpace(req.Options.Background); bg != "" {
		body["background"] = map[string]string{"type": "color", "value": bg}
	}
	wrote := false
	var v video
	if err := c.do(ctx, "asked for a presenter clip", http.MethodPost, "/v3/videos", body, &v, req.Tag, 2*time.Minute, &wrote); err != nil {
		var ve *videogen.Error
		switch {
		case errors.As(err, &ve) && ve.Kind == videogen.KindUncertain:
			ve.Files = files // deleted with what is found by the tag
		case errors.As(err, &ve) && !ve.Retryable():
			// Refused for good: no video uses the sound.
			_ = c.do(context.WithoutCancel(ctx), "deleted the sound it was sent", http.MethodDelete, "/v3/assets/"+url.PathEscape(up.Data.AssetID), nil, nil, "", 30*time.Second, nil)
		}
		// Tried again later: the same keys bring back the same asset and video.
		return nil, err
	}
	id := firstNonEmpty(v.VideoID, v.ID)
	if id == "" {
		return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "HeyGen answered without saying which clip it is making.", Files: files}
	}
	return &aiservices.Ticket{ID: id, Files: files, SubmittedAt: time.Now().UTC()}, nil
}

// ForgetTagged deletes the clips whose title is MetaRush's tag: one whose
// answer was lost may have been made all the same (aiservices.TagCleaner).
func (a *Adapter) ForgetTagged(ctx context.Context, key, tag string) (int, error) {
	if strings.TrimSpace(tag) == "" {
		return 0, nil
	}
	c, err := a.client(key)
	if err != nil {
		return 0, err
	}
	var ids []string
	token := ""
	for page := 0; page < 20; page++ {
		q := url.Values{"title": {tag}, "limit": {"100"}}
		if token != "" {
			q.Set("token", token)
		}
		res, err := c.call(ctx, "looked for a clip by its title", http.MethodGet, "/v3/videos?"+q.Encode(), nil, "", "", 60*time.Second, nil)
		if err != nil {
			return 0, err
		}
		if res.status >= 300 {
			return 0, classify(res)
		}
		var l struct {
			Data []struct {
				ID    string `json:"id"`
				Title string `json:"title"`
			} `json:"data"`
			HasMore   bool   `json:"has_more"`
			NextToken string `json:"next_token"`
		}
		if err := json.Unmarshal(res.body, &l); err != nil {
			return 0, &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "HeyGen sent a list MetaRush couldn't read."}
		}
		for _, v := range l.Data {
			if v.Title == tag {
				ids = append(ids, v.ID)
			}
		}
		if !l.HasMore || l.NextToken == "" {
			break
		}
		token = l.NextToken
	}
	n := 0
	for _, id := range ids {
		err := c.do(ctx, "deleted a clip made without an answer", http.MethodDelete, "/v3/videos/"+url.PathEscape(id), nil, nil, "", 60*time.Second, nil)
		var ve *videogen.Error
		if err != nil && !(errors.As(err, &ve) && ve.Kind == videogen.KindNotFound) {
			return n, err
		}
		n++
	}
	return n, nil
}

// Poll asks how a clip is going.
func (a *Adapter) Poll(ctx context.Context, key string, t *aiservices.Ticket) (*aiservices.Status, error) {
	if t == nil || t.ID == "" {
		return nil, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "There is nothing at HeyGen to ask about."}
	}
	c, err := a.client(key)
	if err != nil {
		return nil, err
	}
	var v video
	if err := c.do(ctx, "checked on a presenter clip", http.MethodGet, "/v3/videos/"+url.PathEscape(t.ID), nil, &v, "", 60*time.Second, nil); err != nil {
		var ve *videogen.Error
		if errors.As(err, &ve) && ve.Kind == videogen.KindNotFound {
			ve.Kind, ve.Message = videogen.KindExpired, "HeyGen no longer has the clip it was making."
		}
		return nil, err
	}
	switch strings.ToLower(v.Status) {
	case "completed":
		if v.VideoURL == "" {
			return &aiservices.Status{State: aiservices.StateFailed, Err: &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "HeyGen finished without a clip to download."}}, nil
		}
		return &aiservices.Status{State: aiservices.StateDone, Usage: aiservices.Usage{Known: v.Duration > 0, Seconds: v.Duration},
			Outputs: []aiservices.Output{{MIME: "video/mp4", URI: v.VideoURL}}}, nil
	case "failed":
		e := &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "HeyGen couldn't make the clip.", Detail: firstLine(strings.TrimSpace(v.FailureCode+": "+v.FailureMessage), 300)}
		st := &aiservices.Status{State: aiservices.StateFailed, Err: e}
		if v.FailureCode == "content_policy_violation" || v.FailureCode == "moderation_failed" {
			e.Kind, e.Message = videogen.KindSafety, "HeyGen declined the clip: its checks found something its policy doesn't allow."
			st.Safety = &videogen.Safety{Blocked: true, Side: "input", Category: "other", Message: firstLine(v.FailureMessage, 300)}
		}
		return st, nil
	}
	progress := map[string]float64{"waiting": 0.05, "pending": 0.1, "processing": 0.4}[strings.ToLower(v.Status)]
	return &aiservices.Status{State: aiservices.StateRunning, Progress: progress}, nil
}

// Fetch downloads a finished clip from the link HeyGen gave (without the key).
func (a *Adapter) Fetch(ctx context.Context, _ string, _ *aiservices.Ticket, out *aiservices.Output, dst io.Writer) (int64, error) {
	if len(out.Data) > 0 {
		n, err := dst.Write(out.Data)
		return int64(n), err
	}
	if out.URI == "" || !providers.DownloadAllowed(out.URI) {
		return 0, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "HeyGen's link to the clip isn't one MetaRush downloads from."}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	call := logbook.ProviderCall{Provider: Name, Title: Title, What: "downloaded a presenter clip", Method: http.MethodGet, Endpoint: "(a link HeyGen gave)"}
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, out.URI, nil)
	if err != nil {
		return 0, err
	}
	hc := a.HTTP
	if hc == nil {
		hc = &http.Client{}
	}
	res, err := hc.Do(req)
	if err != nil {
		call.Duration = time.Since(start)
		logbook.ProviderDone(ctx, call, err)
		return 0, &videogen.Error{Kind: videogen.KindNetwork, Provider: Name, Message: "MetaRush couldn't download the clip from HeyGen. It tries again by itself.", Detail: err.Error()}
	}
	defer res.Body.Close()
	call.Status = res.StatusCode
	if res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone {
		call.Duration = time.Since(start)
		logbook.ProviderDone(ctx, call, nil)
		return 0, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, HTTPStatus: res.StatusCode, Message: "HeyGen's link to the clip has expired.", Hint: "Make it again."}
	}
	if res.StatusCode >= 300 {
		call.Duration = time.Since(start)
		logbook.ProviderDone(ctx, call, nil)
		return 0, &videogen.Error{Kind: videogen.KindServer, Provider: Name, HTTPStatus: res.StatusCode, Message: "HeyGen couldn't send the clip just now. MetaRush tries again by itself."}
	}
	n, err := io.Copy(dst, io.LimitReader(res.Body, 2<<30))
	call.Duration, call.Bytes = time.Since(start), int(n)
	logbook.ProviderDone(ctx, call, err)
	return n, err
}

// Cancel stops a clip: HeyGen has no stop, so the video is deleted.
func (a *Adapter) Cancel(ctx context.Context, key string, t *aiservices.Ticket) error {
	if t == nil || t.ID == "" {
		return nil
	}
	c, err := a.client(key)
	if err != nil {
		return err
	}
	err = c.do(ctx, "stopped it", http.MethodDelete, "/v3/videos/"+url.PathEscape(t.ID), nil, nil, "", 60*time.Second, nil)
	var ve *videogen.Error
	if errors.As(err, &ve) && ve.Kind == videogen.KindNotFound {
		return nil
	}
	return err
}

// Forget deletes the video and the sound uploaded for it.
func (a *Adapter) Forget(ctx context.Context, key string, t *aiservices.Ticket, _ []aiservices.Output) error {
	if t == nil {
		return nil
	}
	c, err := a.client(key)
	if err != nil {
		return err
	}
	var paths []string
	if t.ID != "" {
		paths = append(paths, "/v3/videos/"+url.PathEscape(t.ID))
	}
	for _, f := range t.Files {
		if id, ok := strings.CutPrefix(f, "assets/"); ok && id != "" {
			paths = append(paths, "/v3/assets/"+url.PathEscape(id))
		}
	}
	var problems []string
	for _, p := range paths {
		err := c.do(ctx, "deleted what it kept", http.MethodDelete, p, nil, nil, "", 60*time.Second, nil)
		var ve *videogen.Error
		if err != nil && !(errors.As(err, &ve) && ve.Kind == videogen.KindNotFound) {
			problems = append(problems, firstLine(err.Error(), 200))
		}
	}
	if len(problems) > 0 {
		return errors.New("HeyGen didn't delete everything: " + strings.Join(problems, "; "))
	}
	return nil
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

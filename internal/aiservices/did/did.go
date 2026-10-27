// Package did is D-ID's presenters on the D-ID key (D273): a person's own
// presenter (an "Express" avatar) made from a training take after D-ID has
// checked the person reading its consent words, and clips of a presenter
// reading sound MetaRush supplies (D-ID's scenes). Request mappings follow
// D-ID's API reference as read on 25 Oct 2026; nothing here has been called
// against the real service yet.
//
// D-ID downloads the training take and the consent reading itself, from a
// link (it has no upload for video): MetaRush hands it a short-lived link to
// the stored bytes (aiservices.LinkTaker). The sound for a clip is uploaded.
// After the download MetaRush deletes the clip and its sound at D-ID, and the
// voice D-ID copies from a training take on its own (presenters speak only
// MetaRush's sound); a presenter and D-ID's consent record are deleted when
// the presenter is retired. The key goes only in the Authorization header,
// only to D-ID's API host or a loopback test server (providers.DIDBaseURL);
// files D-ID points to are downloaded without it.
package did

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
	Name  = providers.DIDName
	Title = providers.DIDTitle
)

// ConsentWait is how long sending a presenter waits for D-ID to check the
// consent reading before it makes the presenter.
var ConsentWait = 3 * time.Minute

// consentPoll is how often it asks meanwhile.
var consentPoll = 2 * time.Second

func init() {
	aiservices.Register(&Adapter{kind: aiservices.KindPresenter})
	aiservices.Register(&Adapter{kind: aiservices.KindPresenterMake})
}

// Adapter is one kind of presenter work on D-ID's API.
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

// LinkRoles: D-ID downloads a presenter's training take and the consent
// reading from links.
func (a *Adapter) LinkRoles() []string {
	if a.kind == aiservices.KindPresenterMake {
		return []string{aiservices.RoleFootage, aiservices.RoleConsent}
	}
	return nil
}

// LocalProvider: D-ID's address is a test server on this computer.
func (a *Adapter) LocalProvider() bool {
	base, err := providers.DIDBaseURL()
	return err == nil && providers.LocalURL(base)
}

type client struct {
	base string
	key  string
	http *http.Client
}

func (a *Adapter) client(key string) (*client, error) {
	base, err := providers.DIDBaseURL()
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
// activity log (never the key, links or files); wrote is set once it has
// left this computer.
func (c *client) call(ctx context.Context, what, method, path string, body io.Reader, contentType string, timeout time.Duration, wrote *bool) (res *answer, err error) {
	endpoint, _, _ := strings.Cut(path, "?")
	call := logbook.ProviderCall{Provider: Name, Title: Title, What: what, Method: method, Endpoint: endpoint}
	start := time.Now()
	defer func() {
		call.Duration = time.Since(start)
		if res != nil {
			call.Status, call.Bytes = res.status, len(res.body)
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
	req.Header.Set("Authorization", "Basic "+c.key) // the base is D-ID's host or a loopback test server (DIDBaseURL)
	req.Header.Set("Accept", "application/json")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	r, err := c.http.Do(req)
	if err != nil {
		if errors.Is(err, context.Canceled) && ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &videogen.Error{Kind: videogen.KindNetwork, Provider: Name, Message: "MetaRush couldn't reach D-ID. Check the internet connection; it tries again by itself.", Detail: err.Error()}
	}
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 16<<20))
	res = &answer{status: r.StatusCode, header: r.Header, body: data}
	if err != nil {
		return res, &videogen.Error{Kind: videogen.KindNetwork, Provider: Name, Message: "The connection to D-ID dropped. MetaRush tries again by itself.", Detail: err.Error()}
	}
	return res, nil
}

// do sends a request and reads a JSON answer (out may be nil).
func (c *client) do(ctx context.Context, what, method, path string, in, out any, timeout time.Duration, wrote *bool) error {
	var body io.Reader
	ct := ""
	if in != nil {
		b, _ := json.Marshal(in)
		body, ct = bytes.NewReader(b), "application/json"
	}
	res, err := c.call(ctx, what, method, path, body, ct, timeout, wrote)
	if err != nil {
		if wrote != nil && *wrote && !errors.Is(err, context.Canceled) {
			return &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "The request reached D-ID, but no answer came back, so MetaRush can't tell whether it was made.", Detail: err.Error()}
		}
		return err
	}
	if res.status >= 300 {
		return classify(res)
	}
	if out != nil && len(bytes.TrimSpace(res.body)) > 0 {
		if err := json.Unmarshal(res.body, out); err != nil {
			if wrote != nil {
				// A call that makes something (wrote is passed for those) answered
				// in a way MetaRush can't read: it may have been made, and charged.
				// Never sent again: what it left is looked up by its tag (D277).
				return &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "D-ID answered, but MetaRush couldn't read the answer, so it can't tell what was made.",
					Detail: firstLine(string(res.body), 300)}
			}
			return &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "D-ID sent an answer MetaRush couldn't read. It tries again by itself.", Detail: firstLine(string(res.body), 300)}
		}
	}
	return nil
}

// uncertain marks an error as one whose request may have been made, with
// what it uploaded (deleted along with what is found by the tag).
func uncertain(err error, files ...string) error {
	var ve *videogen.Error
	if errors.As(err, &ve) && ve.Kind == videogen.KindUncertain {
		ve.Files = append(ve.Files, files...)
	}
	return err
}

// apiError is D-ID's error body.
type apiError struct {
	Kind        string `json:"kind"`
	Description string `json:"description"`
}

// classify turns an error answer into a plain-words Error (never the key).
func classify(res *answer) *videogen.Error {
	var b apiError
	_ = json.Unmarshal(res.body, &b)
	if b.Kind == "" && b.Description == "" {
		b.Description = firstLine(string(res.body), 300)
	}
	low := strings.ToLower(b.Kind + " " + b.Description)
	e := &videogen.Error{Provider: Name, HTTPStatus: res.status, Detail: firstLine(strings.TrimSpace(b.Kind+": "+b.Description), 600)}
	if s, err := strconv.Atoi(res.header.Get("Retry-After")); err == nil && s > 0 {
		e.RetryAfter = time.Duration(s) * time.Second
	}
	switch {
	case res.status == http.StatusUnauthorized:
		e.Kind, e.Message, e.Hint = videogen.KindKey, "D-ID did not accept the key. Check that it was copied completely and hasn't been deleted.", "Add the key again in Settings → Integrations."
	case res.status == http.StatusPaymentRequired || strings.Contains(low, "insufficientcredits") || strings.Contains(low, "not enough credits"):
		e.Kind, e.Message, e.Hint = videogen.KindPermission, "Your D-ID plan has no minutes left for this.", "Add minutes or change the plan in your D-ID account, then try again."
	case res.status == http.StatusForbidden:
		e.Kind, e.Message = videogen.KindPermission, "The D-ID key may not do this: your D-ID plan may not include it."
	case res.status == http.StatusTooManyRequests:
		e.Kind, e.Message = videogen.KindRate, "D-ID is busy with this key's requests right now. MetaRush tries again in a minute."
		if e.RetryAfter == 0 {
			e.RetryAfter = time.Minute
		}
	case res.status == http.StatusConflict:
		e.Kind, e.Message = videogen.KindServer, "D-ID is still working on it. MetaRush tries again by itself."
	case res.status == http.StatusUnavailableForLegalReasons || strings.Contains(low, "moderation") || strings.Contains(low, "celebrity"):
		e.Kind, e.Message, e.Hint = videogen.KindSafety, "D-ID declined this: its checks found something its terms don't allow.", "Change the recording and try again."
		cat := "other"
		if strings.Contains(low, "celebrity") {
			cat, e.Message = "public-figure", "D-ID declined this: it recognized a public figure."
		}
		e.Safety = &videogen.Safety{Blocked: true, Side: "input", Category: cat, Message: firstLine(b.Description, 300)}
	case res.status == http.StatusNotFound:
		e.Kind, e.Message = videogen.KindNotFound, "D-ID doesn't have that any more."
	case res.status >= 500:
		e.Kind, e.Message = videogen.KindServer, "D-ID had a problem on its side. MetaRush tries again by itself."
	case strings.Contains(low, "consenttextsimilarity") || strings.Contains(low, "not correspond to the consent"):
		e.Kind, e.Message, e.Hint = videogen.KindInvalid, "D-ID didn't hear its consent words in the reading.", "Record the person reading D-ID's words again, all of them, clearly; ask for new words if half an hour has passed."
	case strings.Contains(low, "invalidface"):
		e.Kind, e.Message, e.Hint = videogen.KindInvalid, "D-ID couldn't find one clear face in the training take.", "Use a take of the person alone, facing the camera, well lit."
	case strings.Contains(low, "invalidfilesize") || strings.Contains(low, "exceeds"):
		e.Kind, e.Message = videogen.KindInvalid, "The file is too big for D-ID."
	case strings.Contains(low, "invalidaudioduration"):
		e.Kind, e.Message = videogen.KindInvalid, "D-ID reads at most 5 minutes of sound at a time. Split the lines into shorter parts."
	case strings.Contains(low, "unsupportedmimetype") || res.status == http.StatusUnsupportedMediaType:
		e.Kind, e.Message = videogen.KindInvalid, "D-ID can't read this kind of file."
	default:
		e.Kind, e.Message = videogen.KindInvalid, "D-ID couldn't use this."
		if b.Description != "" {
			e.Message += " It said: " + firstLine(b.Description, 200)
		}
	}
	return e
}

// --- making a presenter (presenter-make) ---

type avatar struct {
	ID         string          `json:"id"`
	Status     string          `json:"status"`
	Name       string          `json:"name"`
	VoiceID    string          `json:"voice_id"`
	Error      json.RawMessage `json:"error"`
	ImageURL   string          `json:"image_url"`
	PreviewURL string          `json:"preview_url"`
}

type consent struct {
	ID     string          `json:"id"`
	Text   string          `json:"text"`
	Status string          `json:"status"`
	Error  json.RawMessage `json:"error"`
}

// AskConsent asks D-ID for its consent words (aiservices.PresenterMaker):
// three random words with a place for the person's name, valid for 30 minutes.
func (a *Adapter) AskConsent(ctx context.Context, key, language string) (*aiservices.ConsentChallenge, error) {
	c, err := a.client(key)
	if err != nil {
		return nil, err
	}
	p, _ := aiservices.ProviderOf(Name)
	lang := "english"
	minutes := 30
	if p != nil && p.Presenters != nil {
		if l := p.Presenters.ConsentLanguage(language); l != "" {
			lang = l
		} else if strings.TrimSpace(language) != "" {
			return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: fmt.Sprintf("D-ID has no consent words in “%s”.", language)}
		}
		if p.Presenters.ConsentMinutes > 0 {
			minutes = p.Presenters.ConsentMinutes
		}
	}
	var out consent
	if err := c.do(ctx, "asked for its consent words", http.MethodPost, "/consents", map[string]string{"language": lang}, &out, 60*time.Second, nil); err != nil {
		return nil, err
	}
	if out.ID == "" || strings.TrimSpace(out.Text) == "" {
		return nil, &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "D-ID answered without its consent words. Try again in a moment."}
	}
	exp := time.Now().UTC().Add(time.Duration(minutes) * time.Minute)
	return &aiservices.ConsentChallenge{ID: out.ID, Text: out.Text, Language: lang, ExpiresAt: &exp}, nil
}

// DropConsent deletes D-ID's consent record (aiservices.PresenterMaker).
func (a *Adapter) DropConsent(ctx context.Context, key, id string) error {
	c, err := a.client(key)
	if err != nil {
		return err
	}
	return c.do(ctx, "deleted its consent record", http.MethodDelete, "/consents/"+url.PathEscape(id), nil, nil, 60*time.Second, nil)
}

// RemovePresenter deletes a presenter at D-ID (aiservices.PresenterMaker).
func (a *Adapter) RemovePresenter(ctx context.Context, key, id string) error {
	c, err := a.client(key)
	if err != nil {
		return err
	}
	return c.do(ctx, "deleted a presenter", http.MethodDelete, "/scenes/avatars/"+url.PathEscape(id), nil, nil, 60*time.Second, nil)
}

// FindPresenters lists the presenters whose names carry MetaRush's tag
// (aiservices.PresenterMaker): D-ID's list can't filter, so it is read page by page.
func (a *Adapter) FindPresenters(ctx context.Context, key, tag string) ([]string, error) {
	c, err := a.client(key)
	if err != nil {
		return nil, err
	}
	var out []string
	token := ""
	for page := 0; page < 50; page++ {
		q := url.Values{"limit": {"200"}}
		if token != "" {
			q.Set("token", token)
		}
		var res struct {
			Avatars []avatar `json:"avatars"`
			Token   string   `json:"token"`
		}
		if err := c.do(ctx, "looked for a presenter by its name", http.MethodGet, "/scenes/avatars?"+q.Encode(), nil, &res, 60*time.Second, nil); err != nil {
			return nil, err
		}
		for _, av := range res.Avatars {
			if tag != "" && strings.Contains(av.Name, "["+tag+"]") {
				out = append(out, av.ID)
			}
		}
		if res.Token == "" || res.Token == token || len(res.Avatars) == 0 {
			break
		}
		token = res.Token
	}
	return out, nil
}

// submitPresenter sends the consent reading, waits for D-ID to check it,
// then asks for the presenter from the training take.
func (a *Adapter) submitPresenter(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	c, err := a.client(key)
	if err != nil {
		return nil, err
	}
	reading, take := req.Input(aiservices.RoleConsent), req.Input(aiservices.RoleFootage)
	o := req.Options
	switch {
	case strings.TrimSpace(o.ConsentID) == "" || reading == nil:
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "D-ID wants a recording of the person reading its consent words before it makes a presenter."}
	case take == nil:
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "D-ID needs a training take of the person."}
	case reading.URL == "" || take.URL == "":
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "D-ID downloads the training take and the consent reading from a link, and MetaRush had none to give."}
	}
	// 1. The consent reading: D-ID compares what is said with its words.
	name := strings.TrimSpace(o.Name)
	sentReading := false
	if err := c.do(ctx, "sent the consent reading", http.MethodPost, "/consents/"+url.PathEscape(o.ConsentID),
		map[string]string{"source_url": reading.URL, "name": firstNonEmpty(name, "MetaRush presenter")}, &struct{}{}, 2*time.Minute, &sentReading); err != nil {
		return nil, err
	}
	deadline := time.Now().Add(ConsentWait)
	for {
		var st consent
		if err := c.do(ctx, "checked the consent reading", http.MethodGet, "/consents/"+url.PathEscape(o.ConsentID), nil, &st, 60*time.Second, nil); err != nil {
			return nil, err
		}
		switch strings.ToLower(st.Status) {
		case "done":
		case "error", "rejected":
			e := classify(&answer{status: http.StatusBadRequest, header: http.Header{}, body: st.Error})
			if e.Kind == videogen.KindInvalid && !strings.Contains(e.Message, "consent") {
				e.Message, e.Hint = "D-ID didn't accept the consent reading.", "Record the person reading D-ID's words again, all of them, clearly."
			}
			return nil, e
		default:
			if time.Now().After(deadline) {
				return nil, &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "D-ID is still checking the consent reading. MetaRush tries again by itself."}
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(consentPoll):
			}
			continue
		}
		break
	}
	// 2. The presenter, from the training take; its name carries the tag.
	wrote := false
	var av avatar
	body := map[string]any{"source_url": take.URL, "consent_id": o.ConsentID, "name": aiservices.TaggedName(firstNonEmpty(name, "MetaRush presenter"), req.Tag),
		"is_greenscreen": o.GreenScreen}
	if err := c.do(ctx, "asked for a presenter", http.MethodPost, "/scenes/avatars", body, &av, 2*time.Minute, &wrote); err != nil {
		return nil, err
	}
	if av.ID == "" {
		return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "D-ID answered without saying which presenter it is making."}
	}
	return &aiservices.Ticket{ID: av.ID, SubmittedAt: time.Now().UTC(), Extra: map[string]string{"consent": o.ConsentID, "what": "presenter"}}, nil
}

func (a *Adapter) pollPresenter(ctx context.Context, key string, t *aiservices.Ticket) (*aiservices.Status, error) {
	c, err := a.client(key)
	if err != nil {
		return nil, err
	}
	var av avatar
	if err := c.do(ctx, "checked on a presenter", http.MethodGet, "/scenes/avatars/"+url.PathEscape(t.ID), nil, &av, 60*time.Second, nil); err != nil {
		var ve *videogen.Error
		if errors.As(err, &ve) && ve.Kind == videogen.KindNotFound {
			ve.Kind, ve.Message = videogen.KindExpired, "D-ID no longer has the presenter it was making."
		}
		return nil, err
	}
	switch strings.ToLower(av.Status) {
	case "done":
		return &aiservices.Status{State: aiservices.StateDone, Usage: aiservices.Usage{Known: true}, Outputs: []aiservices.Output{{PresenterID: av.ID, VoiceID: av.VoiceID}}}, nil
	case "error":
		return &aiservices.Status{State: aiservices.StateFailed, Err: failed(av.Error, "D-ID couldn't make the presenter from this take.")}, nil
	case "rejected":
		e := failed(av.Error, "D-ID turned the training take down.")
		e.Kind = videogen.KindSafety
		return &aiservices.Status{State: aiservices.StateFailed, Err: e, Safety: &videogen.Safety{Blocked: true, Side: "input", Category: "person", Message: e.Detail}}, nil
	}
	progress := map[string]float64{"validating": 0.05, "created": 0.1, "draft": 0.1, "started": 0.2, "training-started": 0.4}[strings.ToLower(av.Status)]
	return &aiservices.Status{State: aiservices.StateRunning, Progress: progress}, nil
}

// failed reads D-ID's error on a finished job.
func failed(raw json.RawMessage, plain string) *videogen.Error {
	e := &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: plain}
	if len(raw) > 0 && string(raw) != "null" {
		var b apiError
		if json.Unmarshal(raw, &b) == nil && (b.Kind != "" || b.Description != "") {
			c := classify(&answer{status: http.StatusBadRequest, header: http.Header{}, body: raw})
			e.Detail = c.Detail
			if c.Kind == videogen.KindSafety || strings.Contains(strings.ToLower(b.Kind), "face") {
				e.Message, e.Hint = c.Message, c.Hint
			}
		} else {
			e.Detail = firstLine(string(raw), 300)
		}
	}
	return e
}

// --- a presenter reading sound (presenter) ---

type scene struct {
	ID        string          `json:"id"`
	Status    string          `json:"status"`
	ResultURL string          `json:"result_url"`
	Error     json.RawMessage `json:"error"`
}

// submitClip uploads the sound and asks for a scene of the presenter reading it.
func (a *Adapter) submitClip(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	c, err := a.client(key)
	if err != nil {
		return nil, err
	}
	in := req.Input(aiservices.RoleAudio)
	if in == nil || in.Path == "" {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "Choose the sound the presenter reads."}
	}
	if req.Options.Stock {
		return nil, &videogen.Error{Kind: videogen.KindInvalid, Provider: Name, Message: "MetaRush doesn't offer D-ID's stock presenters: its terms don't say which plans license them."}
	}
	// 1. The sound, uploaded (D-ID keeps it a day or two; deleted after the download).
	b, err := os.ReadFile(in.Path)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("audio", firstNonEmpty(in.Name, "sound"+aiservices.ExtFor(in.MIME)))
	if err != nil {
		return nil, err
	}
	if _, err := part.Write(b); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	uploaded := false
	res, err := c.call(ctx, "uploaded the sound for a clip", http.MethodPost, "/audios", &buf, w.FormDataContentType(), 2*time.Minute, &uploaded)
	if err != nil {
		if uploaded && !errors.Is(err, context.Canceled) {
			return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "The sound reached D-ID, but no answer came back.", Detail: err.Error()}
		}
		return nil, err
	}
	if res.status >= 300 {
		return nil, classify(res)
	}
	var up struct {
		URL      string  `json:"url"`
		ID       string  `json:"id"`
		Duration float64 `json:"duration"`
	}
	if err := json.Unmarshal(res.body, &up); err != nil || up.URL == "" {
		// D-ID may keep it (it erases uploads within a day): never sent again.
		return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "D-ID answered the sound's upload in a way MetaRush couldn't read.", Detail: firstLine(string(res.body), 300)}
	}
	files := []string{}
	if up.ID != "" {
		files = append(files, "audios/"+up.ID)
	}
	// 2. The scene: the presenter reading it.
	body := map[string]any{"avatar_id": req.Options.Presenter, "script": map[string]any{"type": "audio", "audio_url": up.URL, "subtitles": false}}
	if req.Tag != "" {
		body["name"], body["user_data"] = req.Tag, req.Tag
	}
	if bg := strings.TrimSpace(req.Options.Background); bg != "" {
		body["background"] = map[string]any{"color": bg}
	}
	wrote := false
	var sc scene
	if err := c.do(ctx, "asked for a presenter clip", http.MethodPost, "/scenes", body, &sc, 2*time.Minute, &wrote); err != nil {
		var ve *videogen.Error
		if errors.As(err, &ve) && ve.Kind != videogen.KindUncertain {
			_ = c.do(context.WithoutCancel(ctx), "deleted the sound it was sent", http.MethodDelete, "/audios/"+url.PathEscape(up.ID), nil, nil, 30*time.Second, nil)
		}
		return nil, uncertain(err, files...)
	}
	if sc.ID == "" {
		return nil, &videogen.Error{Kind: videogen.KindUncertain, Provider: Name, Message: "D-ID answered without saying which clip it is making.", Files: files}
	}
	secs := up.Duration
	if secs <= 0 {
		secs = in.Seconds
	}
	return &aiservices.Ticket{ID: sc.ID, Files: files, SubmittedAt: time.Now().UTC(),
		Extra: map[string]string{"what": "scene", "seconds": strconv.FormatFloat(secs, 'f', 2, 64)}}, nil
}

func (a *Adapter) pollClip(ctx context.Context, key string, t *aiservices.Ticket) (*aiservices.Status, error) {
	c, err := a.client(key)
	if err != nil {
		return nil, err
	}
	var sc scene
	if err := c.do(ctx, "checked on a presenter clip", http.MethodGet, "/scenes/"+url.PathEscape(t.ID), nil, &sc, 60*time.Second, nil); err != nil {
		var ve *videogen.Error
		if errors.As(err, &ve) && ve.Kind == videogen.KindNotFound {
			ve.Kind, ve.Message = videogen.KindExpired, "D-ID no longer has the clip it was making."
		}
		return nil, err
	}
	switch strings.ToLower(sc.Status) {
	case "done":
		if sc.ResultURL == "" {
			return &aiservices.Status{State: aiservices.StateFailed, Err: &videogen.Error{Kind: videogen.KindServer, Provider: Name, Message: "D-ID finished without a clip to download."}}, nil
		}
		secs, _ := strconv.ParseFloat(t.Extra["seconds"], 64)
		u := aiservices.Usage{Known: secs > 0, Seconds: secs}
		return &aiservices.Status{State: aiservices.StateDone, Usage: u, Outputs: []aiservices.Output{{MIME: "video/mp4", URI: sc.ResultURL}}}, nil
	case "error":
		return &aiservices.Status{State: aiservices.StateFailed, Err: failed(sc.Error, "D-ID couldn't make the clip.")}, nil
	case "rejected":
		e := failed(sc.Error, "D-ID declined the clip: its checks found something its terms don't allow.")
		e.Kind = videogen.KindSafety
		return &aiservices.Status{State: aiservices.StateFailed, Err: e, Safety: &videogen.Safety{Blocked: true, Side: "input", Category: "other", Message: e.Detail}}, nil
	}
	progress := map[string]float64{"created": 0.1, "started": 0.4}[strings.ToLower(sc.Status)]
	return &aiservices.Status{State: aiservices.StateRunning, Progress: progress}, nil
}

// ForgetTagged deletes the clips (scenes) whose name carries MetaRush's tag:
// one whose answer was lost may have been made all the same
// (aiservices.TagCleaner). A presenter whose answer was lost is found with
// FindPresenters when it is retired.
func (a *Adapter) ForgetTagged(ctx context.Context, key, tag string) (int, error) {
	if a.kind != aiservices.KindPresenter || strings.TrimSpace(tag) == "" {
		return 0, nil
	}
	c, err := a.client(key)
	if err != nil {
		return 0, err
	}
	var ids []string
	token := ""
	for page := 0; page < 50; page++ {
		q := url.Values{"limit": {"100"}}
		if token != "" {
			q.Set("token", token)
		}
		var res struct {
			Scenes []struct {
				ID       string `json:"id"`
				Name     string `json:"name"`
				UserData string `json:"user_data"`
			} `json:"scenes"`
			Token string `json:"token"`
		}
		if err := c.do(ctx, "looked for a clip by its name", http.MethodGet, "/scenes?"+q.Encode(), nil, &res, 60*time.Second, nil); err != nil {
			return 0, err
		}
		for _, sc := range res.Scenes {
			if sc.Name == tag || sc.UserData == tag {
				ids = append(ids, sc.ID)
			}
		}
		if res.Token == "" || res.Token == token || len(res.Scenes) == 0 {
			break
		}
		token = res.Token
	}
	n := 0
	for _, id := range ids {
		err := c.do(ctx, "deleted a clip made without an answer", http.MethodDelete, "/scenes/"+url.PathEscape(id), nil, nil, 60*time.Second, nil)
		if err != nil && !kindIs(err, videogen.KindNotFound) {
			return n, err
		}
		n++
	}
	return n, nil
}

// --- the contract ---

// Submit sends a request: a presenter to make, or a clip to read.
func (a *Adapter) Submit(ctx context.Context, key string, req *aiservices.Request) (*aiservices.Ticket, error) {
	if a.kind == aiservices.KindPresenterMake {
		return a.submitPresenter(ctx, key, req)
	}
	return a.submitClip(ctx, key, req)
}

// Poll asks how a presenter or a clip is going.
func (a *Adapter) Poll(ctx context.Context, key string, t *aiservices.Ticket) (*aiservices.Status, error) {
	if t == nil || t.ID == "" {
		return nil, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "There is nothing at D-ID to ask about."}
	}
	if a.kind == aiservices.KindPresenterMake {
		return a.pollPresenter(ctx, key, t)
	}
	return a.pollClip(ctx, key, t)
}

// Fetch downloads a finished clip from the link D-ID gave (without the key;
// links last a day).
func (a *Adapter) Fetch(ctx context.Context, _ string, _ *aiservices.Ticket, out *aiservices.Output, dst io.Writer) (int64, error) {
	if len(out.Data) > 0 {
		n, err := dst.Write(out.Data)
		return int64(n), err
	}
	if out.URI == "" || !providers.DownloadAllowed(out.URI) {
		return 0, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, Message: "D-ID's link to the clip isn't one MetaRush downloads from."}
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()
	call := logbook.ProviderCall{Provider: Name, Title: Title, What: "downloaded a presenter clip", Method: http.MethodGet, Endpoint: "(a link D-ID gave)"}
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
		return 0, &videogen.Error{Kind: videogen.KindNetwork, Provider: Name, Message: "MetaRush couldn't download the clip from D-ID. It tries again by itself.", Detail: err.Error()}
	}
	defer res.Body.Close()
	call.Status = res.StatusCode
	if res.StatusCode == http.StatusForbidden || res.StatusCode == http.StatusNotFound || res.StatusCode == http.StatusGone {
		call.Duration = time.Since(start)
		logbook.ProviderDone(ctx, call, nil)
		return 0, &videogen.Error{Kind: videogen.KindExpired, Provider: Name, HTTPStatus: res.StatusCode, Message: "D-ID's link to the clip has expired.", Hint: "Make it again."}
	}
	if res.StatusCode >= 300 {
		call.Duration = time.Since(start)
		logbook.ProviderDone(ctx, call, nil)
		return 0, &videogen.Error{Kind: videogen.KindServer, Provider: Name, HTTPStatus: res.StatusCode, Message: "D-ID couldn't send the clip just now. MetaRush tries again by itself."}
	}
	n, err := io.Copy(dst, io.LimitReader(res.Body, 2<<30))
	call.Duration, call.Bytes = time.Since(start), int(n)
	logbook.ProviderDone(ctx, call, err)
	return n, err
}

// Cancel stops a presenter or a clip at D-ID: D-ID stops it by deleting it.
func (a *Adapter) Cancel(ctx context.Context, key string, t *aiservices.Ticket) error {
	if t == nil || t.ID == "" {
		return nil
	}
	c, err := a.client(key)
	if err != nil {
		return err
	}
	path := "/scenes/" + url.PathEscape(t.ID)
	if a.kind == aiservices.KindPresenterMake {
		// Only a presenter still being made is stopped: a finished one is a
		// person's presenter, deleted only when it is retired.
		var av avatar
		if err := c.do(ctx, "checked on a presenter", http.MethodGet, "/scenes/avatars/"+url.PathEscape(t.ID), nil, &av, 60*time.Second, nil); err != nil {
			if kindIs(err, videogen.KindNotFound) {
				return nil
			}
			return err
		}
		if s := strings.ToLower(av.Status); s == "done" || s == "error" || s == "rejected" {
			return nil
		}
		path = "/scenes/avatars/" + url.PathEscape(t.ID)
	}
	err = c.do(ctx, "stopped it", http.MethodDelete, path, nil, nil, 60*time.Second, nil)
	var ve *videogen.Error
	if errors.As(err, &ve) && ve.Kind == videogen.KindNotFound {
		return nil
	}
	return err
}

// Forget deletes what D-ID kept for a request: a clip and its sound; for a
// presenter, the voice D-ID copied from the training take (the presenter and
// D-ID's consent record stay until the presenter is retired).
func (a *Adapter) Forget(ctx context.Context, key string, t *aiservices.Ticket, outs []aiservices.Output) error {
	if t == nil {
		return nil
	}
	c, err := a.client(key)
	if err != nil {
		return err
	}
	var paths []string
	if a.kind == aiservices.KindPresenterMake {
		for _, o := range outs {
			if o.VoiceID != "" {
				paths = append(paths, "/tts/voices/"+url.PathEscape(o.VoiceID))
			}
		}
	} else {
		if t.ID != "" {
			paths = append(paths, "/scenes/"+url.PathEscape(t.ID))
		}
		for _, f := range t.Files {
			if id, ok := strings.CutPrefix(f, "audios/"); ok && id != "" {
				paths = append(paths, "/audios/"+url.PathEscape(id))
			}
		}
	}
	var problems []string
	for _, p := range paths {
		err := c.do(ctx, "deleted what it kept", http.MethodDelete, p, nil, nil, 60*time.Second, nil)
		var ve *videogen.Error
		if err != nil && !(errors.As(err, &ve) && ve.Kind == videogen.KindNotFound) {
			problems = append(problems, firstLine(err.Error(), 200))
		}
	}
	if len(problems) > 0 {
		return errors.New("D-ID didn't delete everything: " + strings.Join(problems, "; "))
	}
	return nil
}

func kindIs(err error, kind string) bool {
	var ve *videogen.Error
	return errors.As(err, &ve) && ve.Kind == kind
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

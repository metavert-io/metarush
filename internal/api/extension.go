package api

import (
	"archive/zip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/mux"

	"github.com/metavert-io/metarush/extension"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/auth"
	"github.com/metavert-io/metarush/internal/browserext"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/resources"
	"github.com/metavert-io/metarush/internal/uploads"
	"github.com/metavert-io/metarush/internal/version"
)

// The "Send to MetaRush" browser extension talks to /ext/*, never to /api/*.
// Every request must come from a browser extension's own origin (web pages
// can't send one), carry X-MetaRush-Client: extension (cross-site forms can't
// set it) and, after pairing, the extension's token (Authorization: Bearer),
// which is bound to the origin that paired. The token can only start uploads,
// send their bytes and ask for link imports: nothing here returns clips,
// projects, jobs or activity. The Host allowlist (DNS rebinding) applies as for
// every request, and no CORS headers are ever sent, so a page can't read a reply.

// ExtPairIn is POST /ext/pair.
type ExtPairIn struct {
	Code    string `json:"code"`
	Name    string `json:"name,omitempty" jsonschema:"description=The browser the extension runs in (Chrome\\, Brave\\, Edge…)."`
	Version string `json:"version,omitempty"`
}

// ExtPairOut answers a pairing: the token is shown once.
type ExtPairOut struct {
	Token   string `json:"token"`
	ID      string `json:"id"`
	Machine string `json:"machine"`
	// For is the person the browser is paired for (D197): what it sends is theirs.
	For string `json:"for,omitempty"`
}

// ExtHelloOut is POST /ext/hello: the pairing still works.
type ExtHelloOut struct {
	Machine string `json:"machine"`
	Version string `json:"version"`
	// For is the person the browser is paired for (D197), as they are named now.
	For string `json:"for,omitempty"`
}

// ExtSource is what the extension read from the video page.
type ExtSource struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Uploader    string `json:"uploader,omitempty"`
	Handle      string `json:"handle,omitempty"`
	ChannelURL  string `json:"channelUrl,omitempty"`
	UploadDate  string `json:"uploadDate,omitempty"`
	Description string `json:"description,omitempty"`
}

// ExtUploadIn is POST /ext/uploads (the video goes to the inbox).
type ExtUploadIn struct {
	Name   string    `json:"name"`
	Size   int64     `json:"size"`
	Method string    `json:"method" jsonschema:"enum=browser,enum=recording,description=browser = the file the page plays\\, fetched with the person's sign-in; recording = recorded while it played."`
	Source ExtSource `json:"source"`
}

// ExtUploadOut is the started upload (only its ID and offset).
type ExtUploadOut struct {
	ID     string `json:"id"`
	Offset int64  `json:"offset"`
}

// ExtImportIn is POST /ext/imports: MetaRush downloads the link itself.
type ExtImportIn struct {
	URL string `json:"url"`
}

// ExtImportOut says what happens, in plain words (no IDs, nothing to read back).
type ExtImportOut struct {
	Status  string `json:"status"`
	Message string `json:"message"`
}

// ExtPageIn is POST /ext/pages: the page on screen in the person's own
// browser ("Send this page"), as a web page snapshot in the resources library.
type ExtPageIn struct {
	URL         string `json:"url"`
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	SiteName    string `json:"siteName,omitempty"`
	ThemeColor  string `json:"themeColor,omitempty"`
	Lang        string `json:"lang,omitempty"`
	// Text is the page's readable text (document.body.innerText).
	Text string `json:"text,omitempty"`
	// Screenshot is what was on screen: a data:image/png;base64 address.
	Screenshot string `json:"screenshot"`
}

// ExtPageOut says what happened, in plain words (nothing to read back).
type ExtPageOut struct {
	Status  string `json:"status" jsonschema:"enum=added,enum=version,enum=same"`
	Message string `json:"message"`
}

const maxExtUpload = 16 << 30

type extKey struct{}

// extAPI holds the pairing rate limit.
type extAPI struct {
	s        *Server
	mu       sync.Mutex
	attempts []time.Time
}

func (s *Server) extRoutes(r *mux.Router) {
	e := &extAPI{s: s}
	ext := r.PathPrefix("/ext").Subrouter()
	ext.HandleFunc("/pair", e.pair).Methods("POST")
	auth := ext.NewRoute().Subrouter()
	auth.Use(e.requireToken)
	auth.HandleFunc("/hello", e.hello).Methods("POST")
	auth.HandleFunc("/unpair", e.unpair).Methods("POST")
	auth.HandleFunc("/uploads", e.createUpload).Methods("POST")
	auth.HandleFunc("/uploads/{id}", e.patchUpload).Methods("PATCH")
	auth.HandleFunc("/uploads/{id}/offset", e.uploadOffset).Methods("POST")
	auth.HandleFunc("/imports", e.importLink).Methods("POST")
	auth.HandleFunc("/pages", e.sendPage).Methods("POST")
}

// maxExtPage bounds a page sent from the browser (a screenshot and its text).
const maxExtPage = 24 << 20

// sendPage adds the page on screen as a web page snapshot (D105). The token
// must have the pages scope; the answer says what happened in words only.
func (e *extAPI) sendPage(w http.ResponseWriter, r *http.Request) {
	b := browserOf(r)
	if !b.Can(browserext.ScopePages) {
		writeErr(w, apperr.New(apperr.ConsentRequired, "This browser was paired before MetaRush could take pages from it.").WithHint("Pair it again with a code from Settings → Browser extension in MetaRush.").WithDetails(map[string]any{"reason": "scope"}))
		return
	}
	var in ExtPageIn
	if err := json.NewDecoder(io.LimitReader(r.Body, maxExtPage)).Decode(&in); err != nil {
		writeErr(w, apperr.New(apperr.InvalidInput, "MetaRush didn't get the page's address, words and picture. Try sending it again."))
		return
	}
	u, err := url.Parse(strings.TrimSpace(in.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		writeErr(w, apperr.New(apperr.InvalidInput, "MetaRush only takes web pages (addresses that start with http or https)."))
		return
	}
	if !resources.AllowLocalPages() && resources.LocalAddress(u.Hostname()) {
		writeErr(w, apperr.New(apperr.InvalidInput, "That page is on this computer or a local network, so MetaRush won't take it."))
		return
	}
	png, err := resources.DecodeDataURL(in.Screenshot, 18<<20)
	if err != nil {
		writeErr(w, apperr.New(apperr.InvalidInput, "%s", err.Error()))
		return
	}
	clip := func(s string, n int) string {
		s = strings.TrimSpace(s)
		if rr := []rune(s); len(rr) > n {
			return string(rr[:n])
		}
		return s
	}
	a := *e.s.appFor(r, models.ViaExtension)
	u.Fragment = ""
	res, status, err := resources.AddPageCapture(r.Context(), &a, resources.PageCapture{URL: u.String(), Title: clip(in.Title, 300), Description: clip(in.Description, 600),
		SiteName: clip(in.SiteName, 120), ThemeColor: clip(in.ThemeColor, 20), Lang: clip(in.Lang, 20), Text: clip(in.Text, 200000), PNG: png})
	if err != nil {
		writeErr(w, err)
		return
	}
	browserext.Of(e.s.App).SentPage(b.ID)
	msg := "Sent to MetaRush: “" + res.Title + "” is in the resources library."
	switch status {
	case resources.StatusVersion:
		msg = "Sent to MetaRush: a new snapshot of “" + res.Title + "” is in the resources library."
	case resources.StatusSame:
		msg = "MetaRush already has this snapshot of “" + res.Title + "”."
	}
	writeOK(w, ExtPageOut{Status: status, Message: msg})
}

// extensionDownload sends the extension's files as a .zip (GET
// /api/extension/download, signed in): a browser on another computer
// (remote access) unzips it for "Load unpacked", since the folder MetaRush
// keeps is on the computer that runs it.
func (s *Server) extensionDownload(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="metarush-browser-extension.zip"`)
	w.Header().Set("Cache-Control", "no-store")
	zw := zip.NewWriter(w)
	src := extension.FS()
	_ = fs.WalkDir(src, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, err := fs.ReadFile(src, p)
		if err != nil {
			return err
		}
		f, err := zw.Create("metarush-browser-extension/" + p)
		if err != nil {
			return err
		}
		_, err = f.Write(b)
		return err
	})
	_ = zw.Close()
}

// fromExtension checks the two things a web page can never fake on its own:
// an extension origin and the custom client header.
func fromExtension(r *http.Request) error {
	if !browserext.ExtensionOrigin(r.Header.Get("Origin")) {
		return apperr.New(apperr.Unauthorized, "Only the MetaRush browser extension can use this.").WithDetails(map[string]any{"reason": "origin"})
	}
	if r.Header.Get(clientHeader) != "extension" {
		return apperr.New(apperr.Unauthorized, "Only the MetaRush browser extension can use this.").WithDetails(map[string]any{"reason": "csrf", "missingHeader": clientHeader})
	}
	return nil
}

func (e *extAPI) requireToken(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := fromExtension(r); err != nil {
			writeErr(w, err)
			return
		}
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			writeErr(w, apperr.New(apperr.Unauthorized, "This browser isn't paired with MetaRush.").WithHint("Pair it in the extension with a code from Settings → Browser extension in MetaRush."))
			return
		}
		e.s.App.Log.Secret(tok) // never in the activity log
		b, err := browserext.Of(e.s.App).Verify(tok, r.Header.Get("Origin"), time.Now())
		if err != nil {
			writeErr(w, apperr.New(apperr.Unauthorized, "%s", err.Error()).WithHint("Pair it again with a code from Settings → Browser extension in MetaRush."))
			return
		}
		// A pairing is its person's (D197): what the browser sends is theirs,
		// and it stops while their account is turned off or removed (checked
		// on every request, the way a sign-in is; unpairing itself still works).
		name, reason, ok := auth.MaySend(r.Context(), e.s.App, b.UserID)
		if !ok && !strings.HasSuffix(r.URL.Path, "/unpair") {
			writeErr(w, apperr.New(apperr.ConsentRequired, "%s", auth.SenderWords(reason, name)).
				WithHint("Pair it again as yourself, with a code from Settings → Browser extension in MetaRush.").WithDetails(map[string]any{"reason": reason}))
			return
		}
		ctx := context.WithValue(r.Context(), extKey{}, b)
		ctx = context.WithValue(ctx, personKey{}, person{UserID: b.UserID, UserName: firstNonEmpty(name, b.UserName)})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func browserOf(r *http.Request) browserext.Browser {
	b, _ := r.Context().Value(extKey{}).(browserext.Browser)
	return b
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if x != "" {
			return x
		}
	}
	return ""
}

// limited allows 10 pairing attempts a minute (a code is also burned after 5 wrong tries).
func (e *extAPI) limited() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := time.Now()
	keep := e.attempts[:0]
	for _, t := range e.attempts {
		if now.Sub(t) < time.Minute {
			keep = append(keep, t)
		}
	}
	e.attempts = keep
	if len(e.attempts) >= 10 {
		return true
	}
	e.attempts = append(e.attempts, now)
	return false
}

func (e *extAPI) pair(w http.ResponseWriter, r *http.Request) {
	if err := fromExtension(r); err != nil {
		writeErr(w, err)
		return
	}
	if e.limited() {
		writeErr(w, apperr.New(apperr.Unauthorized, "Too many pairing tries. Wait a minute, then try again.").WithDetails(map[string]any{"reason": "rate_limit"}))
		return
	}
	var in ExtPairIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 4<<10)).Decode(&in); err != nil {
		writeErr(w, apperr.New(apperr.InvalidInput, "Type the pairing code from Settings → Browser extension in MetaRush."))
		return
	}
	a := e.s.App
	a.Log.Secret(in.Code)
	tok, b, err := browserext.Of(a).Pair(in.Code, r.Header.Get("Origin"), in.Name, in.Version, time.Now())
	if err != nil {
		reason := "code"
		if errors.Is(err, browserext.ErrBadOrigin) {
			reason = "origin"
		}
		writeErr(w, apperr.New(apperr.Unauthorized, "%s", err.Error()).WithDetails(map[string]any{"reason": reason}))
		return
	}
	a.Log.Secret(tok)
	// The pairing is the person's who made the code (D197).
	name, reason, ok := auth.MaySend(r.Context(), a, b.UserID)
	if !ok {
		_, _ = browserext.Of(a).Revoke(b.ID)
		writeErr(w, apperr.New(apperr.ConsentRequired, "%s", auth.SenderWords(reason, name)).WithDetails(map[string]any{"reason": reason}))
		return
	}
	actor := e.s.appAs(person{UserID: b.UserID, UserName: firstNonEmpty(name, b.UserName)}, models.ViaExtension)
	actor.Audit(r.Context(), "extension.paired", "installation", a.Cfg.Installation.ID, "", map[string]any{"browser": b.Name, "id": b.ID, "origin": b.Origin, "version": b.Version,
		"remote": accessOf(r).public, "for": b.UserID})
	writeOK(w, ExtPairOut{Token: tok, ID: b.ID, Machine: a.Cfg.MachineName(), For: firstNonEmpty(name, b.UserName)})
}

func (e *extAPI) hello(w http.ResponseWriter, r *http.Request) {
	p, _ := personOf(r)
	writeOK(w, ExtHelloOut{Machine: e.s.App.Cfg.MachineName(), Version: version.Version, For: p.UserName})
}

func (e *extAPI) unpair(w http.ResponseWriter, r *http.Request) {
	b := browserOf(r)
	_, _ = browserext.Of(e.s.App).Revoke(b.ID)
	e.s.appFor(r, models.ViaExtension).Audit(r.Context(), "extension.revoke", "installation", e.s.App.Cfg.Installation.ID, "", map[string]any{"id": b.ID, "by": "extension"})
	writeOK(w, map[string]any{"unpaired": true})
}

var (
	handleRe = regexp.MustCompile(`^@[A-Za-z0-9._-]{1,100}$`)
	dateRe   = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
)

// sanitizeSource turns what the page said into the clip's provenance. The page
// address must be a video page MetaRush recognizes; everything else is
// shortened, and only plausible values are kept.
func sanitizeSource(in ExtSource, method, extVersion string, now time.Time) (*models.AssetSource, error) {
	l, err := linkimport.Classify(in.URL)
	if err != nil || l.Many() || l.Platform == models.PlatformWeb { // the extension runs on YouTube, TikTok and X only
		return nil, apperr.New(apperr.InvalidInput, "That page isn't a YouTube, TikTok or X video.")
	}
	clip := func(s string, n int) string {
		s = strings.Join(strings.Fields(s), " ")
		if r := []rune(s); len(r) > n {
			return string(r[:n])
		}
		return s
	}
	src := &models.AssetSource{Platform: l.Platform, URL: l.URL, VideoID: l.VideoID, Title: clip(in.Title, 300), Uploader: clip(in.Uploader, 100),
		Description: clip(in.Description, 1000), DownloadedAt: now.UTC(), Tool: "metarush-extension", ToolVersion: clip(extVersion, 20), Method: models.SourceBrowser, SignedIn: true}
	if method == models.SourceRecording {
		src.Method = models.SourceRecording
	}
	if handleRe.MatchString(in.Handle) {
		src.Handle = in.Handle
	}
	if dateRe.MatchString(in.UploadDate) {
		src.UploadDate = in.UploadDate
	}
	if c, err := linkimport.Classify(in.ChannelURL); err == nil && c.Platform == l.Platform && c.Kind == linkimport.KindChannel {
		src.ChannelURL = strings.TrimSuffix(c.URL, "/videos")
	}
	if src.Title == "" {
		src.Title = linkimport.DefaultTitle(l.Platform, src.Who())
	}
	return src, nil
}

func (e *extAPI) createUpload(w http.ResponseWriter, r *http.Request) {
	var in ExtUploadIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 64<<10)).Decode(&in); err != nil || in.Size <= 0 {
		writeErr(w, apperr.New(apperr.InvalidInput, "MetaRush needs the video's name and size to take it."))
		return
	}
	if in.Size > maxExtUpload {
		writeErr(w, apperr.New(apperr.InvalidInput, "That video is too large to send from the browser (16 GB at most).").WithHint("Use “Import the original” instead."))
		return
	}
	src, err := sanitizeSource(in.Source, in.Method, r.Header.Get("X-MetaRush-Extension-Version"), time.Now())
	if err != nil {
		writeErr(w, err)
		return
	}
	name := filepath.Base(strings.ReplaceAll(in.Name, "\\", "/"))
	if name == "" || name == "." || name == "/" {
		name = linkimport.FileName(src.Title, "mp4")
	}
	b := browserOf(r)
	u := e.s.uploads
	rec, err := u.st.Create(name, in.Size, "", "")
	if err != nil {
		writeErr(w, err)
		return
	}
	p, _ := personOf(r)
	rec, _ = u.st.Update(rec.ID, true, func(x *uploads.Record) {
		x.Via, x.Owner, x.Source = models.ViaExtension, b.ID, src
		x.UserID, x.UserName = p.UserID, p.UserName // the person who paired the browser (D197): the clip is theirs
	})
	browserext.Of(e.s.App).Sent(b.ID)
	writeOK(w, ExtUploadOut{ID: rec.ID, Offset: rec.Offset})
}

// own loads an upload the calling browser started (any other is "not found").
func (e *extAPI) own(r *http.Request) (uploads.Record, bool) {
	rec, ok := e.s.uploads.st.Get(mux.Vars(r)["id"])
	if !ok || rec.Owner == "" || rec.Owner != browserOf(r).ID {
		return uploads.Record{}, false
	}
	return rec, true
}

func (e *extAPI) patchUpload(w http.ResponseWriter, r *http.Request) {
	if _, ok := e.own(r); !ok {
		writeErr(w, uploadNotFound())
		return
	}
	x := &extWriter{ResponseWriter: w}
	e.s.uploads.patch(x, r)
	x.flush()
}

// extWriter answers a chunk with the offset only (never the whole record).
type extWriter struct {
	http.ResponseWriter
	status int
	buf    []byte
}

func (x *extWriter) WriteHeader(code int) { x.status = code }
func (x *extWriter) Write(b []byte) (int, error) {
	x.buf = append(x.buf, b...)
	return len(b), nil
}

func (x *extWriter) flush() {
	var env struct {
		OK    bool            `json:"ok"`
		Data  uploads.Record  `json:"data"`
		Error json.RawMessage `json:"error"`
	}
	status := x.status
	if status == 0 {
		status = http.StatusOK
	}
	if json.Unmarshal(x.buf, &env) != nil || !env.OK {
		x.ResponseWriter.WriteHeader(status)
		_, _ = x.ResponseWriter.Write(x.buf)
		return
	}
	writeOK(x.ResponseWriter, ExtUploadOut{ID: env.Data.ID, Offset: env.Data.Offset})
}

func (e *extAPI) uploadOffset(w http.ResponseWriter, r *http.Request) {
	rec, ok := e.own(r)
	if !ok {
		writeErr(w, uploadNotFound())
		return
	}
	writeOK(w, ExtUploadOut{ID: rec.ID, Offset: rec.Offset})
}

func (e *extAPI) importLink(w http.ResponseWriter, r *http.Request) {
	var in ExtImportIn
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<10)).Decode(&in); err != nil || strings.TrimSpace(in.URL) == "" {
		writeErr(w, apperr.New(apperr.InvalidInput, "MetaRush didn't get the video's address. Try sending it again."))
		return
	}
	a := *e.s.appFor(r, models.ViaExtension)
	b, _ := json.Marshal(linkimport.ImportIn{URLs: []string{in.URL}})
	out, err := ops.Call(r.Context(), &a, "ingest.url", b, ops.Caller{Remote: true})
	if err != nil {
		writeErr(w, err)
		return
	}
	res := out.(*linkimport.ImportOut)
	if len(res.Imports) == 0 {
		writeErr(w, apperr.New(apperr.InvalidInput, "There's nothing to import at that address."))
		return
	}
	row := res.Imports[0]
	msg := row.Message
	if row.Next != "" {
		msg += " " + row.Next
	}
	if (row.Status == "queued" || row.Status == "importing") && !res.Running {
		msg += " It starts when MetaRush is running on " + res.Machine + "."
	} else if row.Status == "queued" {
		msg += " Follow it in MetaRush's activity bar."
	}
	browserext.Of(e.s.App).Sent(browserOf(r).ID)
	writeOK(w, ExtImportOut{Status: row.Status, Message: msg})
}

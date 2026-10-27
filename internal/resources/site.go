package resources

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif" // icons
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/media"
	"github.com/metavert-io/metarush/internal/models"
	rnd "github.com/metavert-io/metarush/internal/renderer"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/tools"
)

// Website snapshots (D105). A web page becomes a website resource: a dated
// snapshot whose file is the desktop screenshot, with the phone and
// full-page screenshots as its page pictures (name#2, name#3), its readable
// text (search reads it) and what it says about itself (title, theme colour,
// preview picture, icon). The snapshot is taken by a fetch job on the machine
// that asked, in a fresh headless browser that has network access — outside
// the render jail, signed out, never reaching this computer or the local
// network — with time and page limits. A refresh takes a new snapshot: a new
// version, which cuts use only after `cut update`. The browser extension's
// "Send this page" adds what the person sees in their own browser the same way.

// websiteSource is the URL source for web pages (replaces part 1's placeholder).
type websiteSource struct{}

func (websiteSource) Name() string  { return "website" }
func (websiteSource) Route() string { return LinkPage }
func (websiteSource) Match(u *url.URL) bool {
	return u != nil && (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}

// Add queues the page's snapshot (or the picture's download) on this machine.
func (websiteSource) Add(ctx context.Context, a *app.App, in URLAddIn) (*URLAddOut, error) {
	if a == nil {
		return nil, errors.New("no app") // part 1's readiness probe calls Add without one
	}
	u, err := url.Parse(in.URL)
	if err != nil || u.Host == "" {
		return nil, apperr.New(apperr.InvalidInput, "that isn't a web address")
	}
	kind := FetchPage
	if pictureExt[strings.ToLower(path.Ext(u.Path))] {
		kind = FetchPicture
	}
	q, err := QueueFetch(ctx, a, FetchRequest{Kind: kind, URL: u.String(), ProjectID: in.ProjectID, Workspace: in.ProjectID == "", Depth: in.Depth, MaxPages: in.MaxPages})
	if err != nil {
		return nil, err
	}
	return &URLAddOut{Message: q.Message, JobID: q.JobID, Status: q.Status}, nil
}

func init() { RegisterURLSource(websiteSource{}) }

// PageShot is one page the snapshot script took.
type PageShot struct {
	URL         string            `json:"url"`
	Requested   string            `json:"requested"`
	From        string            `json:"from,omitempty"`
	Title       string            `json:"title"`
	Description string            `json:"description"`
	SiteName    string            `json:"siteName"`
	ThemeColor  string            `json:"themeColor"`
	Lang        string            `json:"lang"`
	TextFile    string            `json:"textFile"`
	Shots       map[string]string `json:"shots"` // desktop | mobile | full → PNG file
	Preview     string            `json:"preview,omitempty"`
	Icon        string            `json:"icon,omitempty"`
	// Brand is the site's logos, colours and fonts (the first page only, D165).
	Brand *PageBrand `json:"brand,omitempty"`
}

type snapEvent struct {
	Type    string   `json:"type"`
	Page    PageShot `json:"page"`
	Message string   `json:"message"`
	Code    string   `json:"code"`
	Done    int      `json:"done"`
	Of      int      `json:"of"`
	Pages   int      `json:"pages"`
	Over    int      `json:"over"`
	Skipped []struct {
		URL string `json:"url"`
		Why string `json:"why"`
	} `json:"skipped"`
}

// SnapshotPages takes a page's snapshot (and, at depth 1, same-site pages it
// links to) in a headless browser and stores each as a website resource.
func SnapshotPages(ctx context.Context, a *app.App, req FetchRequest, progress func(float64, string)) (*FetchResult, error) {
	if progress == nil {
		progress = func(float64, string) {}
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, permanentf("%s isn't a web page's address.", req.URL)
	}
	if !AllowLocalPages() && ResolvesLocal(ctx, u.Hostname()) {
		return nil, permanentf("%s is on this computer or a local network, so MetaRush won't take a snapshot of it.", u.Host)
	}
	if !a.Tools.Has(tools.Node) {
		return nil, permanentf("Taking a snapshot of a web page needs Node.js, which isn't on this computer. Add it in Settings → Components.")
	}
	videoDir, rs, err := rnd.New(a.Cfg, a.Tools).Ready(false)
	if err != nil {
		return nil, err
	}
	release, err := rnd.Use(rs)
	if err != nil {
		return nil, err
	}
	defer release()
	work, err := makeTemp(a)
	if err != nil {
		return nil, err
	}
	defer removeAll(work)
	lim := a.Cfg.Resources.Pages
	maxPages := 1
	if req.Depth > 0 {
		maxPages = lim.Max()
		if req.MaxPages > 0 {
			maxPages = min(req.MaxPages, 20)
		}
	}
	args := []string{"render/snapshot.mjs", "--url", u.String(), "--out", work, "--depth", fmt.Sprint(req.Depth), "--max-pages", fmt.Sprint(maxPages),
		"--timeout-ms", fmt.Sprint(lim.Timeout() * 1000), "--full-height", fmt.Sprint(lim.FullHeight())}
	if AllowLocalPages() {
		args = append(args, "--allow-local")
	}
	var mu sync.Mutex
	var pages []PageShot
	var failMsg string
	var skipped []string
	cctx, cancel := context.WithTimeout(ctx, time.Duration(lim.Timeout()*maxPages+120)*time.Second)
	defer cancel()
	progress(0.05, "opening "+pageWords(u.String()))
	_, runErr := a.Tools.Run(cctx, tools.Spec{Tool: tools.Node, Dir: videoDir, Background: true, Args: args, OnLine: func(line string) {
		var ev snapEvent
		if json.Unmarshal([]byte(line), &ev) != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		switch ev.Type {
		case "page":
			pages = append(pages, ev.Page)
			if ev.Of > 0 {
				progress(0.1+0.7*float64(ev.Done)/float64(ev.Of), fmt.Sprintf("took %d of %d pages", ev.Done, ev.Of))
			}
		case "skipped":
			for _, s := range ev.Skipped {
				skipped = append(skipped, pageWords(s.URL)+" was left out: "+s.Why)
			}
			if ev.Over > 0 {
				what := fmt.Sprintf("%d more linked pages were", ev.Over)
				if ev.Over == 1 {
					what = "1 more linked page was"
				}
				skipped = append(skipped, fmt.Sprintf("%s left out (a snapshot takes at most %d pages unless you ask for more).", what, maxPages))
			}
		case "error":
			failMsg = ev.Message
		}
	}})
	if len(pages) == 0 {
		msg := failMsg
		if msg == "" && runErr != nil {
			msg = runErr.Error()
		}
		if errors.Is(cctx.Err(), context.DeadlineExceeded) {
			msg = "the page took too long"
		}
		if strings.Contains(msg, "local network") {
			return nil, permanentf("%s", msg)
		}
		return nil, fmt.Errorf("MetaRush couldn't take a snapshot of %s: %s", pageWords(u.String()), firstLine(msg))
	}
	progress(0.85, "saving the snapshot")
	out := &FetchResult{}
	takenAt := time.Now().UTC()
	for i, p := range pages {
		pr := req
		if i > 0 {
			pr.Name, pr.Title, pr.ResourceID = "", "", "" // linked pages are their own resources
		}
		r, status, err := storePage(ctx, a, pr, p, takenAt, "fetch", false)
		if err != nil {
			out.Notes = append(out.Notes, pageWords(p.URL)+": "+apperr.From(err).Message)
			continue
		}
		if i == 0 && p.Brand != nil {
			// The site's logos, colours and fonts (D165): kept next to the page.
			_, notes := keepBrand(ctx, a, work, r, p)
			out.Notes = append(out.Notes, notes...)
		}
		out.Resources = append(out.Resources, r)
		out.Statuses = append(out.Statuses, status)
	}
	if len(out.Resources) == 0 {
		return nil, fmt.Errorf("the snapshot couldn't be saved: %s", strings.Join(out.Notes, "; "))
	}
	out.Notes = append(out.Notes, skipped...)
	first := out.Resources[0]
	switch {
	case len(out.Resources) > 1:
		more := "1 linked page"
		if len(out.Resources) > 2 {
			more = fmt.Sprintf("%d linked pages", len(out.Resources)-1)
		}
		out.Message = fmt.Sprintf("Took a snapshot of %s and %s on the same site.", first.Title, more)
	case out.Statuses[0] == StatusVersion:
		out.Message = fmt.Sprintf("Took a new snapshot of %s (%s). Cuts keep the older one until you update them.", first.Title, plainDate(takenAt))
	case out.Statuses[0] == StatusSame:
		out.Message = fmt.Sprintf("%s looks exactly as it did in its last snapshot.", first.Title)
	default:
		out.Message = fmt.Sprintf("Took a snapshot of %s: screenshots for a computer and a phone, the whole page, and its text.", first.Title)
	}
	return out, nil
}

// PageCapture is a page sent from the person's own browser (the extension).
type PageCapture struct {
	URL, Title, Description, SiteName, ThemeColor, Lang string
	Text                                                string
	// PNG is the screenshot of what the person saw.
	PNG []byte
}

// AddPageCapture adds what the browser extension sent as a website snapshot
// in the workspace library (it never picks a project: the extension reads nothing back).
func AddPageCapture(ctx context.Context, a *app.App, c PageCapture) (*models.Resource, string, error) {
	work, err := makeTemp(a)
	if err != nil {
		return nil, "", err
	}
	defer removeAll(work)
	shot := filepath.Join(work, "desktop.png")
	if err := os.WriteFile(shot, c.PNG, 0o600); err != nil {
		return nil, "", err
	}
	textFile := filepath.Join(work, "text.txt")
	if err := os.WriteFile(textFile, []byte(c.Text), 0o600); err != nil {
		return nil, "", err
	}
	p := PageShot{URL: c.URL, Requested: c.URL, Title: c.Title, Description: c.Description, SiteName: c.SiteName, ThemeColor: c.ThemeColor, Lang: c.Lang,
		TextFile: textFile, Shots: map[string]string{models.ShotDesktop: shot}}
	return storePage(ctx, a, FetchRequest{Kind: FetchPage, URL: c.URL, Workspace: true}, p, time.Now().UTC(), "extension", true)
}

// storePage adds one page as a website resource, or a new snapshot of it.
func storePage(ctx context.Context, a *app.App, req FetchRequest, p PageShot, takenAt time.Time, via string, signedIn bool) (*models.Resource, string, error) {
	desktop := p.Shots[models.ShotDesktop]
	if desktop == "" {
		return nil, "", errors.New("the snapshot has no screenshot")
	}
	pi, err := ReadPicture(desktop)
	if err != nil {
		return nil, "", fmt.Errorf("the screenshot can't be read: %w", err)
	}
	sha, err := media.HashFile(desktop)
	if err != nil {
		return nil, "", err
	}
	st, _ := os.Stat(desktop)
	final := canonicalPage(p.URL)
	u, _ := url.Parse(final)
	site := hostOf(final)
	target, err := pageTarget(ctx, a, req, final)
	if err != nil {
		return nil, "", err
	}
	if target != nil {
		if v := target.Version(sha); v != nil {
			return target, StatusSame, nil
		}
	}
	work, err := makeTemp(a)
	if err != nil {
		return nil, "", err
	}
	defer removeAll(work)
	key := media.ObjectKey(a.WorkspaceID(), sha)
	if err := putPrepared(ctx, a, desktop, key, sha, "image/png"); err != nil {
		return nil, "", err
	}
	_, _ = a.Cache.Adopt(desktop, sha)
	text := ""
	if b, err := os.ReadFile(p.TextFile); err == nil {
		text = strings.TrimSpace(string(b))
	}
	ss := &models.SiteSnapshot{URL: final, Site: site, Title: clip(p.Title, 300), Description: clip(p.Description, 600), SiteName: clip(p.SiteName, 120),
		Lang: clip(p.Lang, 20), TakenAt: takenAt, Via: via, SignedIn: signedIn, TextChars: len([]rune(text))}
	if p.Requested != "" && canonicalPage(p.Requested) != final {
		ss.Requested = p.Requested
	}
	if p.From != "" {
		ss.From = canonicalPage(p.From)
	}
	if c := normColor(p.ThemeColor); c != "" {
		ss.ThemeColors = append(ss.ThemeColors, c)
	}
	for _, c := range pi.Colors {
		if len(ss.ThemeColors) >= 6 {
			break
		}
		if !containsStr(ss.ThemeColors, c) {
			ss.ThemeColors = append(ss.ThemeColors, c)
		}
	}
	actor := a.Actor
	v := models.ResourceVersion{SHA256: sha, Size: st.Size(), MIME: "image/png", Ext: "png", StorageKey: key, Stored: true, Width: pi.Width, Height: pi.Height,
		HasAlpha: pi.HasAlpha, Colors: pi.Colors, Site: ss, CreatedAt: takenAt, CreatedBy: actor,
		Source: models.ResourceSource{Kind: models.FromURL, Name: pageWords(final), URL: final, Site: site, Machine: a.Cfg.Installation.Name, At: takenAt}}
	if signedIn {
		v.Source.Tool = "metarush-extension"
	}
	if pi.Thumb != nil {
		if obj, err := putBytes(ctx, a, work, pi.Thumb, "image/png"); err == nil {
			obj.Width, obj.Height = pi.ThumbW, pi.ThumbH
			v.Thumbnail = obj
		}
	}
	var pagePaths []string
	n := 0
	for _, label := range []string{models.ShotDesktop, models.ShotMobile, models.ShotFull} {
		f := p.Shots[label]
		if f == "" {
			continue
		}
		obj, err := putFile(ctx, a, f, "image/png")
		if err != nil {
			return nil, "", err
		}
		w, h := pngSize(f)
		n++
		v.Pages = append(v.Pages, models.ResourcePage{Number: n, SHA256: obj.SHA256, Size: obj.Size, StorageKey: obj.StorageKey, Width: w, Height: h, Label: label})
		pagePaths = append(pagePaths, f)
	}
	v.PageCount = len(v.Pages)
	for _, pic := range []struct {
		file string
		dst  **models.ResourceObject
	}{{p.Preview, &ss.Preview}, {p.Icon, &ss.Icon}} {
		if pic.file == "" {
			continue
		}
		sn, err := SniffFile(pic.file)
		if err != nil || sn.Kind != models.ResourceImage || sn.Convert {
			continue
		}
		if obj, err := putFile(ctx, a, pic.file, sn.MIME); err == nil {
			obj.Width, obj.Height = pngSize(pic.file)
			*pic.dst = obj
		}
	}
	if len(text) > 20000 {
		if obj, err := putBytes(ctx, a, work, []byte(text), "text/plain; charset=utf-8"); err == nil {
			ss.TextObject = obj
		}
		text = string([]rune(text)[:min(len([]rune(text)), 20000)])
	}
	look := TagInput{Name: pageWords(final) + ".png", Kind: models.ResourceWebsite, Version: &v, Path: desktop, Pages: pagePaths, Palette: pi.Palette, Tools: a.Tools}
	tags := append(AutoTags(ctx, look), models.ResourceTag{Tag: "website", Source: "page", Confidence: 0.9, Why: "it is a snapshot of a web page"})
	v.Text = text
	if v.Text == "" {
		v.Text = FindText(ctx, look) // a page drawn on a canvas: read the screenshot
	}
	now := time.Now().UTC()
	if target != nil {
		upd := bson.M{"$push": bson.M{"versions": v}, "$set": bson.M{"current": sha, "updatedAt": now, "autoTags": mergeAuto(target.AutoTags, tags)}}
		if _, err := a.DB.C(store.CResources).UpdateOne(ctx, bson.M{"_id": target.ID, "workspaceId": a.WorkspaceID(), "versions.sha256": bson.M{"$ne": sha}}, upd); err != nil {
			return nil, "", err
		}
		_ = a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), target.ID, target)
		a.Audit(ctx, "resource.version", "resource", target.ID, target.ProjectID, map[string]any{"sha256": sha, "url": final, "via": via})
		return target, StatusVersion, nil
	}
	scope := models.ScopeWorkspace
	if req.ProjectID != "" && !req.Workspace {
		scope = models.ScopeProject
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		name = pageSlug(u)
	} else if !NamePattern.MatchString(name) {
		return nil, "", permanentf("%q can't be a resource name: use lowercase letters, digits, - and _, starting with a letter", name)
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = clip(firstNonEmpty(p.Title, ss.SiteName, pageWords(final)), 200)
	}
	res := models.Resource{ID: ids.New(ids.Resource), WorkspaceID: a.WorkspaceID(), Name: name, Title: title, Kind: models.ResourceWebsite, Scope: scope,
		Versions: []models.ResourceVersion{v}, Current: sha, AutoTags: tags, Description: ss.Description, CreatedAt: now, CreatedBy: actor, UpdatedAt: now}
	if scope == models.ScopeProject {
		res.ProjectID = req.ProjectID
	}
	if err := insertUnique(ctx, a, &res, req.Name != ""); err != nil {
		return nil, "", err
	}
	a.Audit(ctx, "resource.add", "resource", res.ID, res.ProjectID, map[string]any{"name": res.Name, "kind": res.Kind, "sha256": sha, "url": final, "scope": scope, "via": via})
	return &res, StatusAdded, nil
}

// pageTarget finds the website resource a snapshot updates: the one asked
// for (refresh), else a snapshot of the same page in the same place.
func pageTarget(ctx context.Context, a *app.App, req FetchRequest, final string) (*models.Resource, error) {
	if req.ResourceID != "" {
		var x models.Resource
		if err := a.DB.GetByID(ctx, store.CResources, a.WorkspaceID(), req.ResourceID, &x); err != nil {
			return nil, permanentf("the page to refresh is gone")
		}
		return &x, nil
	}
	filter := bson.M{"kind": models.ResourceWebsite, "removed": bson.M{"$ne": true}, "versions.site.url": final}
	if req.ProjectID == "" || req.Workspace {
		filter["scope"] = models.ScopeWorkspace
	} else {
		filter["scope"], filter["projectId"] = models.ScopeProject, req.ProjectID
	}
	var list []models.Resource
	if err := a.DB.Find(ctx, store.CResources, a.WorkspaceID(), filter, &list); err != nil || len(list) == 0 {
		return nil, nil
	}
	return &list[0], nil
}

// canonicalPage drops the fragment and a trailing slash on the path.
func canonicalPage(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return raw
	}
	u.Fragment, u.RawFragment = "", ""
	u.Host = strings.ToLower(u.Host)
	if u.Path == "/" {
		u.Path = ""
	}
	return u.String()
}

// pageSlug names a page's resource: "example-com", "example-com-pricing".
func pageSlug(u *url.URL) string {
	if u == nil {
		return "page"
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	p := strings.Trim(u.Path, "/")
	if p != "" {
		segs := strings.Split(p, "/")
		if len(segs) > 2 {
			segs = segs[len(segs)-2:]
		}
		p = strings.TrimSuffix(strings.Join(segs, " "), path.Ext(p))
	}
	return Slug(strings.TrimSpace(host + " " + p))
}

func normColor(c string) string {
	c = strings.TrimSpace(strings.ToLower(c))
	if len(c) == 7 && c[0] == '#' {
		return strings.ToUpper(c)
	}
	if len(c) == 4 && c[0] == '#' {
		return strings.ToUpper(normHex(c[1:]))
	}
	return ""
}

func containsStr(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func firstNonEmpty(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return strings.TrimSpace(x)
		}
	}
	return ""
}

// ---- a picture on the web ----

// maxPictureBytes is the largest picture or PDF downloaded from a link.
const maxPictureBytes = 64 << 20

// safeClient refuses addresses on this computer or a local network at
// connect time (so a redirect or a rebinding name can't reach them).
func safeClient() *http.Client {
	d := &net.Dialer{Timeout: 15 * time.Second}
	tr := &http.Transport{Proxy: nil, TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second, MaxResponseHeaderBytes: 64 << 10,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			for _, ip := range ips {
				if localIP(ip.IP) && !AllowLocalPages() {
					return nil, fmt.Errorf("%s is on this computer or a local network", host)
				}
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("%s has no address", host)
			}
			return d.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		}}
	return &http.Client{Transport: tr, Timeout: 2 * time.Minute, CheckRedirect: func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if r.URL.Scheme != "http" && r.URL.Scheme != "https" {
			return errors.New("a redirect away from the web")
		}
		return nil
	}}
}

// FetchPictureURL downloads a picture or PDF from a link and adds it as a
// resource whose source is that address.
func FetchPictureURL(ctx context.Context, a *app.App, req FetchRequest, progress func(float64, string)) (*FetchResult, error) {
	if progress == nil {
		progress = func(float64, string) {}
	}
	u, err := url.Parse(req.URL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil {
		return nil, permanentf("%s isn't a web address.", req.URL)
	}
	if !AllowLocalPages() && LocalAddress(u.Hostname()) {
		return nil, permanentf("%s is on this computer or a local network, so MetaRush won't download from it.", u.Host)
	}
	progress(0.1, "downloading "+path.Base(u.Path))
	hreq, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	hreq.Header.Set("User-Agent", "MetaRush (resources; +local)")
	resp, err := safeClient().Do(hreq)
	if err != nil {
		if strings.Contains(err.Error(), "local network") {
			return nil, permanentf("%s points at this computer or a local network, so MetaRush won't download from it.", u.Host)
		}
		return nil, fmt.Errorf("couldn't download %s: %w", pageWords(u.String()), err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, permanentf("%s didn't send the file (the site answered %s).", pageWords(u.String()), resp.Status)
	}
	work, err := makeTemp(a)
	if err != nil {
		return nil, err
	}
	defer removeAll(work)
	name := path.Base(u.Path)
	if name == "" || name == "/" || name == "." {
		name = "picture"
	}
	file := filepath.Join(work, sanitizeFileName(name))
	f, err := os.Create(file)
	if err != nil {
		return nil, err
	}
	n, err := io.Copy(f, io.LimitReader(resp.Body, maxPictureBytes+1))
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	if n > maxPictureBytes {
		return nil, permanentf("That file is larger than %s, the most MetaRush downloads from a link.", humanBytes(maxPictureBytes))
	}
	progress(0.7, "adding it")
	o := AddOptions{ProjectID: req.ProjectID, Name: req.Name, Title: req.Title, FileName: name, ResourceID: req.ResourceID,
		Source: models.ResourceSource{Kind: models.FromURL, Name: name, URL: u.String(), Site: hostOf(u.String())}}
	if req.Workspace {
		o.ProjectID, o.Scope = "", models.ScopeWorkspace
	}
	res, err := AddFile(ctx, a, file, o)
	if errors.Is(err, ErrNotResource) {
		return nil, permanentf("%s isn't a picture, SVG, PDF or font.", pageWords(u.String()))
	}
	if err != nil {
		return nil, err
	}
	return &FetchResult{Resources: []*models.Resource{res.Resource}, Statuses: []string{res.Status}, Message: res.Message, Notes: append(res.Changes, res.Notes...)}, nil
}

func sanitizeFileName(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 32 || strings.ContainsRune(`/\:*?"<>|`, r) {
			return '-'
		}
		return r
	}, s)
	if len(s) > 120 {
		s = s[len(s)-120:]
	}
	if strings.Trim(s, ".-") == "" {
		return "picture"
	}
	return s
}

// DecodeDataURL reads a "data:image/png;base64,…" screenshot (the extension's).
func DecodeDataURL(s string, maxBytes int) ([]byte, error) {
	const prefix = "data:image/png;base64,"
	if !strings.HasPrefix(s, prefix) {
		return nil, errors.New("the screenshot must be a PNG")
	}
	b64 := s[len(prefix):]
	if base64.StdEncoding.DecodedLen(len(b64)) > maxBytes {
		return nil, errors.New("the screenshot is too large")
	}
	b, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, errors.New("the screenshot can't be read")
	}
	cfg, format, err := image.DecodeConfig(strings.NewReader(string(b)))
	if err != nil || format != "png" || cfg.Width < 16 || cfg.Height < 16 || cfg.Width > 12000 || cfg.Height > 12000 {
		return nil, errors.New("the screenshot isn't a picture MetaRush can use")
	}
	return b, nil
}

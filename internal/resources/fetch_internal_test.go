package resources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func permanent(err error) string {
	var p *Permanent
	if errors.As(err, &p) {
		return p.Msg
	}
	return ""
}

// A fetch is a job on this machine: a repository, a page or a picture; the
// same fetch while one is on its way is that one.
func TestQueueFetch(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	film := project(t, a, "Film")
	for _, c := range []struct {
		r    FetchRequest
		code string
	}{
		{FetchRequest{Kind: "video", URL: "https://example.com/v"}, apperr.InvalidInput},
		{FetchRequest{Kind: FetchPage, URL: "https://example.com/", ProjectID: ids.New(ids.Project)}, apperr.NotFound},
		{FetchRequest{Kind: FetchPage, URL: "https://example.com/", Depth: 2}, apperr.InvalidInput},
		{FetchRequest{Kind: FetchPage, URL: "https://example.com/", MaxPages: 21}, apperr.InvalidInput},
	} {
		if _, err := QueueFetch(ctx, a, c.r); apperr.From(err).Code != c.code {
			t.Errorf("%+v: %v", c.r, err)
		}
	}
	q, err := QueueFetch(ctx, a, FetchRequest{Kind: FetchRepo, URL: "https://github.com/acme/widget", ProjectID: film.ID, Ref: "main"})
	if err != nil || q.Status != "fetching" || q.Message != "Getting acme/widget from GitHub." {
		t.Fatalf("queued: %+v %v", q, err)
	}
	j, err := a.Queue.Get(ctx, q.JobID)
	if err != nil || j.Type != jobs.TypeFetch || j.Installation != a.Cfg.Installation.ID || j.ProjectID != film.ID {
		t.Fatalf("job: %+v %v", j, err)
	}
	again, err := QueueFetch(ctx, a, FetchRequest{Kind: FetchRepo, URL: "https://github.com/acme/widget", ProjectID: film.ID, Ref: "main"})
	if err != nil || again.Status != "already" || again.JobID != q.JobID || again.Message != "Already getting acme/widget from GitHub." {
		t.Fatalf("again: %+v %v", again, err)
	}
	ws, err := QueueFetch(ctx, a, FetchRequest{Kind: FetchPicture, URL: "https://www.example.com/brand/logo.png", ProjectID: film.ID, Workspace: true})
	if err != nil || ws.Message != "Getting the picture at example.com." {
		t.Fatalf("workspace picture: %+v %v", ws, err)
	}
	if j, _ := a.Queue.Get(ctx, ws.JobID); j.ProjectID != "" {
		t.Fatalf("the workspace library, not the project: %+v", j)
	}
	page, _ := QueueFetch(ctx, a, FetchRequest{Kind: FetchPage, URL: "https://example.com/pricing/plans", Depth: 1, MaxPages: 5})
	if page.Message != "Getting a snapshot of example.com/pricing/plans." {
		t.Fatalf("page: %+v", page)
	}

	// A job's request reads back whatever number type the database gave.
	got := FetchPayload(&models.Job{Payload: map[string]any{"kind": "page", "url": "https://example.com/", "workspace": true, "depth": int32(1), "maxPages": int64(5)}})
	if got.Depth != 1 || got.MaxPages != 5 || !got.Workspace || got.Kind != FetchPage {
		t.Fatalf("payload: %+v", got)
	}
	if got := FetchPayload(&models.Job{Payload: map[string]any{"depth": 1, "maxPages": float64(3)}}); got.Depth != 1 || got.MaxPages != 3 {
		t.Fatalf("payload numbers: %+v", got)
	}
	fr := &FetchResult{Resources: []*models.Resource{{ID: "res_1", Name: "logo"}}, Statuses: []string{StatusAdded}, Message: "Added logo.", Notes: []string{"n"}}
	if m := fr.Map(); !slices.Equal(m["resourceIds"].([]string), []string{"res_1"}) || !slices.Equal(m["names"].([]string), []string{"logo"}) || m["message"] != "Added logo." {
		t.Fatalf("map: %+v", m)
	}
	for kind, want := range map[string]string{FetchRepo: "the repository", FetchPicture: "the picture at ", FetchPage: "a snapshot of not a url"} {
		if got := fetchWhat(FetchRequest{Kind: kind, URL: "not a url"}); got != want {
			t.Errorf("fetchWhat(%s) = %q", kind, got)
		}
	}
	if pageWords("https://example.com/"+strings.Repeat("a", 60)) != "example.com/"+strings.Repeat("a", 38)+"…" {
		t.Fatal("pageWords")
	}
}

// A picture or PDF on the web is downloaded and added with where it came
// from; a missing file, something that isn't a picture, an address on this
// computer and odd redirects are refused plainly.
func TestFetchPictureURL(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	film := project(t, a, "Film")
	logo := mustRead(t, writePNG(t, t.TempDir(), "logo.png", testLogo(80, 40)))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/brand/logo.png", "/":
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(logo)
		case "/notes.txt":
			_, _ = w.Write([]byte("just words"))
		case "/away":
			http.Redirect(w, r, "ftp://example.com/logo.png", http.StatusFound)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	u, _ := url.Parse(srv.URL)

	t.Setenv("METARUSH_PAGES_ALLOW_LOCAL", "")
	if _, err := FetchPictureURL(ctx, a, FetchRequest{URL: srv.URL + "/brand/logo.png"}, nil); !strings.Contains(permanent(err), "is on this computer or a local network") {
		t.Fatalf("local without the test switch: %v", err)
	}
	t.Setenv("METARUSH_PAGES_ALLOW_LOCAL", "1")
	var steps []string
	res, err := FetchPictureURL(ctx, a, FetchRequest{URL: srv.URL + "/brand/logo.png", ProjectID: film.ID}, func(_ float64, m string) { steps = append(steps, m) })
	if err != nil || len(res.Resources) != 1 || res.Statuses[0] != StatusAdded || len(steps) != 2 {
		t.Fatalf("downloaded: %+v %v (%v)", res, err, steps)
	}
	r := res.Resources[0]
	if v := r.Latest(); v.Source.Kind != models.FromURL || v.Source.URL != srv.URL+"/brand/logo.png" || v.Source.Site != u.Hostname() || r.ProjectID != film.ID || r.Name != "logo" {
		t.Fatalf("resource: %+v / %+v", r, v.Source)
	}
	ws, err := RunFetch(ctx, a, &models.Job{Payload: map[string]any{"kind": FetchPicture, "url": srv.URL + "/", "workspace": true, "name": "site-mark"}}, nil)
	if err != nil || ws.Resources[0].Scope != models.ScopeWorkspace || ws.Resources[0].Name != "site-mark" {
		t.Fatalf("a nameless address into the library: %+v %v", ws, err)
	}
	for _, c := range []struct{ path, says string }{
		{"/missing.png", "didn't send the file (the site answered 404 Not Found)."},
		{"/notes.txt", "isn't a picture, SVG, PDF or font."},
	} {
		if _, err := FetchPictureURL(ctx, a, FetchRequest{URL: srv.URL + c.path}, nil); !strings.HasSuffix(permanent(err), c.says) {
			t.Errorf("%s: %v", c.path, err)
		}
	}
	if _, err := FetchPictureURL(ctx, a, FetchRequest{URL: srv.URL + "/away"}, nil); err == nil || permanent(err) != "" || !strings.Contains(err.Error(), "a redirect away from the web") {
		t.Fatalf("redirect: %v", err)
	}
	if _, err := FetchPictureURL(ctx, a, FetchRequest{URL: "ftp://example.com/logo.png"}, nil); !strings.HasSuffix(permanent(err), "isn't a web address.") {
		t.Fatalf("not the web: %v", err)
	}
	// Jobs of kinds MetaRush doesn't know, and pages without a renderer here, fail plainly.
	if _, err := RunFetch(ctx, a, &models.Job{Payload: map[string]any{"kind": "video"}}, nil); permanent(err) != "MetaRush doesn't know what to bring in for this job." {
		t.Fatalf("unknown kind: %v", err)
	}
	t.Setenv("METARUSH_PAGES_ALLOW_LOCAL", "")
	if _, err := RunFetch(ctx, a, &models.Job{Payload: map[string]any{"kind": FetchPage, "url": srv.URL + "/"}}, nil); !strings.Contains(permanent(err), "won't take a snapshot of it") {
		t.Fatalf("a page on this computer: %v", err)
	}
	if _, err := SnapshotPages(ctx, a, FetchRequest{URL: "file:///etc/passwd"}, nil); !strings.Contains(permanent(err), "isn't a web page's address") {
		t.Fatalf("not a page: %v", err)
	}
	for in, want := range map[string]string{"a/b:c*?.png": "a-b-c--.png", "...": "picture", strings.Repeat("x", 130) + ".png": strings.Repeat("x", 116) + ".png"} {
		if got := sanitizeFileName(in); got != want {
			t.Errorf("sanitizeFileName(%q) = %q", in, got)
		}
	}
	for _, h := range []string{"localhost", "10.0.0.7", "nas", "printer.local"} {
		if !ResolvesLocal(ctx, h) {
			t.Errorf("%s is local", h)
		}
	}
}

// The URL sources the Add box routes to: GitHub repositories and web pages
// queue a fetch; part 1's placeholders said "not yet".
func TestURLSources(t *testing.T) {
	a := testapp.New(t)
	ctx := context.Background()
	gh, _ := url.Parse("https://github.com/acme/widget")
	page, _ := url.Parse("https://example.com/pricing")
	if s := URLSourceFor(LinkRepo, gh); s == nil || s.Name() != "github" || s.Route() != LinkRepo || !s.Match(gh) || s.Match(page) || s.Match(nil) {
		t.Fatalf("github: %+v", s)
	}
	if s := URLSourceFor(LinkPage, page); s == nil || s.Name() != "website" || s.Route() != LinkPage || !s.Match(page) || s.Match(nil) {
		t.Fatalf("website: %+v", s)
	}
	if URLSourceFor(LinkRepo, page) != nil {
		t.Fatal("a page is no repository")
	}
	for _, s := range []URLSource{githubSource{}, websiteSource{}} {
		if _, err := s.Add(ctx, nil, URLAddIn{URL: "https://github.com/acme/widget"}); err == nil {
			t.Errorf("%s without an app", s.Name())
		}
	}
	if _, err := (githubSource{}).Add(ctx, a, URLAddIn{URL: "https://example.com/x"}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("not a repository: %v", err)
	}
	if _, err := (websiteSource{}).Add(ctx, a, URLAddIn{URL: "::"}); apperr.From(err).Code != apperr.InvalidInput {
		t.Fatalf("not an address: %v", err)
	}
	repo, err := (githubSource{}).Add(ctx, a, URLAddIn{URL: "https://github.com/acme/widget/tree/v2"})
	if err != nil || repo.JobID == "" || repo.Status != "fetching" {
		t.Fatalf("repository: %+v %v", repo, err)
	}
	if j, _ := a.Queue.Get(ctx, repo.JobID); j.Payload["ref"] != "v2" || j.Payload["workspace"] != true {
		t.Fatalf("the ref and the library: %+v", j.Payload)
	}
	pic, err := (websiteSource{}).Add(ctx, a, URLAddIn{URL: "https://example.com/brand/logo.PNG"})
	if err != nil || pic.JobID == "" {
		t.Fatalf("picture: %+v %v", pic, err)
	}
	if j, _ := a.Queue.Get(ctx, pic.JobID); j.Payload["kind"] != FetchPicture {
		t.Fatalf("a picture address is downloaded, not snapshotted: %+v", j.Payload)
	}
	var n int64
	if n, _ = a.DB.C(store.CJobs).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "type": jobs.TypeFetch}); n != 2 {
		t.Fatalf("fetch jobs: %d", n)
	}
	// Part 1's placeholders.
	gp, wp := placeholder{name: "github", route: LinkRepo}, placeholder{name: "website", route: LinkPage}
	if gp.Name() != "github" || gp.Route() != LinkRepo || !gp.Match(gh) || gp.Match(page) || !wp.Match(page) {
		t.Fatal("placeholders")
	}
	if _, err := gp.Add(ctx, a, URLAddIn{}); !errors.Is(err, ErrComingSoon) {
		t.Fatalf("coming soon: %v", err)
	}
	if !strings.Contains(ComingSoonMessage(LinkRepo), "GitHub repositories") || !strings.Contains(ComingSoonMessage(LinkPage), "web pages") {
		t.Fatal("ComingSoonMessage")
	}
	if (&Permanent{Msg: "no"}).Error() != "no" {
		t.Fatal("Permanent")
	}
}

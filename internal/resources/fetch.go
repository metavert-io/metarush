package resources

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Fetches (D104, D105): a GitHub repository or a web page is brought in by a
// `fetch` job pinned to the machine that asked — its network, and for a
// repository its own git and gh sign-in. The job runs in the worker (the
// daemon, or the terminal that waits for it) and adds or updates resources.

// Fetch kinds.
const (
	FetchRepo    = "repo"    // a GitHub repository at a commit
	FetchPage    = "page"    // a web page: a dated snapshot
	FetchPicture = "picture" // a picture or PDF on the web: downloaded as a file
)

// FetchRequest asks for one fetch.
type FetchRequest struct {
	Kind string // repo | page | picture
	URL  string
	// ProjectID is where new resources go ("" with Workspace: the library).
	ProjectID string
	Workspace bool
	// Ref is a repository's branch, tag or commit (default: its default branch).
	Ref string
	// Depth 1 also takes same-site pages the page links to (MaxPages in all).
	Depth    int
	MaxPages int
	// Name and Title for a new resource (one page or repository).
	Name, Title string
	// ResourceID makes the result a new version of that resource (refresh).
	ResourceID string
	// Pictures are a repository's folders or files whose pictures are kept
	// too (D216), added to the ones it keeps already.
	Pictures []string
	// PicturesOnly keeps the pictures of the repository (ResourceID) at the
	// commit it shows (Ref), after its picture paths changed: no new version
	// of its files.
	PicturesOnly bool
}

// FetchQueued says what was queued.
type FetchQueued struct {
	JobID   string
	Status  string // fetching | already (the same fetch is queued or running)
	Message string
}

// fetchWhat names what is fetched, for messages ("acme/widget from GitHub").
func fetchWhat(r FetchRequest) string {
	switch r.Kind {
	case FetchRepo:
		if rr, ok := ParseRepoURL(r.URL); ok {
			return rr.Owner + "/" + rr.Name + " from GitHub"
		}
		return "the repository"
	case FetchPicture:
		return "the picture at " + hostOf(r.URL)
	}
	return "a snapshot of " + pageWords(r.URL)
}

// pageWords is a page in a few words: "example.com" or "example.com/pricing".
func pageWords(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return raw
	}
	p := strings.TrimSuffix(u.EscapedPath(), "/")
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if p == "" {
		return host
	}
	if len(p) > 40 {
		p = p[:39] + "…"
	}
	return host + p
}

func hostOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	return strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
}

// QueueFetch queues a fetch on this machine. The same fetch while one is
// queued or running is that one ("already").
func QueueFetch(ctx context.Context, a *app.App, r FetchRequest) (*FetchQueued, error) {
	switch r.Kind {
	case FetchRepo, FetchPage, FetchPicture:
	default:
		return nil, apperr.New(apperr.InvalidInput, "MetaRush can bring in a repository, a web page or a picture.")
	}
	if r.ProjectID != "" && !r.Workspace {
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), r.ProjectID, &p); err != nil {
			return nil, apperr.New(apperr.NotFound, "That project was not found. It may have been deleted.").WithDetails(map[string]any{"project": r.ProjectID})
		}
	}
	if r.Workspace {
		r.ProjectID = ""
	}
	if r.Depth < 0 || r.Depth > 1 {
		return nil, apperr.New(apperr.InvalidInput, "A snapshot takes the page itself (depth 0), or also the pages it links to on the same site (depth 1).")
	}
	if r.MaxPages < 0 || r.MaxPages > 20 {
		return nil, apperr.New(apperr.InvalidInput, "A snapshot takes at most 20 pages.")
	}
	// A repository's pictures (D216).
	if (len(r.Pictures) > 0 || r.PicturesOnly) && r.Kind != FetchRepo {
		return nil, apperr.New(apperr.InvalidInput, "Only a repository keeps the pictures in its folders.")
	}
	if r.PicturesOnly && (r.ResourceID == "" || !fullSHA.MatchString(r.Ref)) {
		return nil, apperr.New(apperr.InvalidInput, "Keeping a repository's pictures needs the repository and the commit it shows.")
	}
	if len(r.Pictures) > 0 {
		ch, err := ChangePicturePaths(nil, r.Pictures, nil)
		if err != nil {
			return nil, err
		}
		r.Pictures = ch.Paths
	}
	// The same fetch already waiting or running: that one.
	var open []models.Job
	same := bson.M{"type": jobs.TypeFetch, "state": bson.M{"$in": bson.A{models.JobQueued, models.JobRunning}},
		"payload.url": r.URL, "payload.kind": r.Kind, "payload.projectId": r.ProjectID, "payload.resourceId": r.ResourceID, "payload.pictures": bson.M{"$exists": false},
		"payload.picturesOnly": bson.M{"$ne": true}}
	if len(r.Pictures) > 0 {
		same["payload.pictures"] = r.Pictures // the same pictures asked for too
	}
	what := fetchWhat(r)
	if r.PicturesOnly {
		same["payload.picturesOnly"], same["payload.ref"] = true, r.Ref
		what = "the pictures of " + what
	}
	_ = a.DB.Find(ctx, store.CJobs, a.WorkspaceID(), same, &open)
	if len(open) > 0 {
		return &FetchQueued{JobID: open[0].ID, Status: "already", Message: "Already getting " + what + "."}, nil
	}
	payload := map[string]any{"kind": r.Kind, "url": r.URL, "projectId": r.ProjectID, "workspace": r.Workspace, "ref": r.Ref,
		"depth": r.Depth, "maxPages": r.MaxPages, "name": r.Name, "title": r.Title, "resourceId": r.ResourceID, "via": a.Actor.Via}
	if len(r.Pictures) > 0 {
		payload["pictures"] = r.Pictures
	}
	if r.PicturesOnly {
		payload["picturesOnly"] = true
	}
	j, _, err := a.Queue.Enqueue(ctx, jobs.EnqueueOptions{Type: jobs.TypeFetch, Payload: payload, IdempotencyKey: ids.New("idk"), Priority: 9, MaxAttempts: 2,
		ProjectID: r.ProjectID, Actor: a.Actor, Installation: a.Cfg.Installation.ID})
	if err != nil {
		return nil, err
	}
	a.Audit(ctx, "resource.fetch", "job", j.ID, r.ProjectID, map[string]any{"kind": r.Kind, "url": r.URL, "ref": r.Ref, "depth": r.Depth, "pictures": r.Pictures, "picturesOnly": r.PicturesOnly})
	return &FetchQueued{JobID: j.ID, Status: "fetching", Message: "Getting " + what + "."}, nil
}

// FetchPayload reads a fetch job's request.
func FetchPayload(j *models.Job) FetchRequest {
	str := func(k string) string { s, _ := j.Payload[k].(string); return s }
	num := func(k string) int {
		switch v := j.Payload[k].(type) {
		case int:
			return v
		case int32:
			return int(v)
		case int64:
			return int(v)
		case float64:
			return int(v)
		}
		return 0
	}
	ws, _ := j.Payload["workspace"].(bool)
	only, _ := j.Payload["picturesOnly"].(bool)
	return FetchRequest{Kind: str("kind"), URL: str("url"), ProjectID: str("projectId"), Workspace: ws, Ref: str("ref"), Depth: num("depth"), MaxPages: num("maxPages"),
		Name: str("name"), Title: str("title"), ResourceID: str("resourceId"), Pictures: payloadStrings(j.Payload["pictures"]), PicturesOnly: only}
}

// payloadStrings reads a list of words from a job's payload (as it was
// queued, or as the database gives it back).
func payloadStrings(v any) []string {
	var items []any
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...)
	case bson.A:
		items = x
	case []any:
		items = x
	}
	var out []string
	for _, it := range items {
		if s, ok := it.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// FetchResult is what a fetch job made.
type FetchResult struct {
	Resources []*models.Resource
	// Statuses per resource: added | version | same.
	Statuses []string
	Message  string
	Notes    []string
	// Pictures counts the pictures a repository kept from its picture paths (D216).
	Pictures int
	// version is the repository version a fetch stored or found.
	version string
}

// Map is the job's result document.
func (r *FetchResult) Map() map[string]any {
	var idsList, names []string
	for _, x := range r.Resources {
		idsList = append(idsList, x.ID)
		names = append(names, x.Name)
	}
	m := map[string]any{"resourceIds": idsList, "names": names, "statuses": r.Statuses, "message": r.Message, "notes": r.Notes}
	if r.Pictures > 0 {
		m["pictures"] = r.Pictures
	}
	return m
}

// Permanent marks a fetch failure retrying cannot fix (the worker fails the
// job at once with the message).
type Permanent struct{ Msg string }

func (p *Permanent) Error() string { return p.Msg }

func permanentf(format string, a ...any) error { return &Permanent{Msg: fmt.Sprintf(format, a...)} }

// RunFetch runs a fetch job: a repository, a page snapshot or a picture.
func RunFetch(ctx context.Context, a *app.App, j *models.Job, progress func(pct float64, msg string)) (*FetchResult, error) {
	r := FetchPayload(j)
	if progress == nil {
		progress = func(float64, string) {}
	}
	switch r.Kind {
	case FetchRepo:
		return FetchRepository(ctx, a, r, progress)
	case FetchPage:
		return SnapshotPages(ctx, a, r, progress)
	case FetchPicture:
		return FetchPictureURL(ctx, a, r, progress)
	}
	return nil, permanentf("MetaRush doesn't know what to bring in for this job.")
}

// AllowLocalPages lets tests take snapshots of a page on this computer
// (METARUSH_PAGES_ALLOW_LOCAL=1). Off for everyone else: a page on this
// computer or the local network is never fetched.
func AllowLocalPages() bool { return os.Getenv("METARUSH_PAGES_ALLOW_LOCAL") == "1" }

// LocalAddress reports an address on this computer or a local network
// (loopback, private, link-local, a bare name or .local).
func LocalAddress(host string) bool {
	host = strings.ToLower(strings.Trim(host, "[]"))
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return localIP(ip)
	}
	return !strings.Contains(host, ".")
}

// localIP: loopback, private, link-local, carrier-grade NAT, benchmark,
// multicast, broadcast and "this network" addresses, also when an IPv6
// address carries one (mapped, compatible, NAT64, 6to4), the rest of ::/8,
// and IPv6 unique-local, link- and site-local addresses. web/proxy.mjs (isLocalIP)
// draws the same line for the snapshot browser.
func localIP(ip net.IP) bool {
	if v4 := embeddedIPv4(ip); v4 != nil {
		ip = v4
	}
	if b := ip.To4(); b != nil {
		return b[0] == 0 || b[0] == 10 || b[0] == 127 || (b[0] == 100 && b[1]&0xc0 == 64) || (b[0] == 169 && b[1] == 254) ||
			(b[0] == 172 && b[1]&0xf0 == 16) || (b[0] == 192 && b[1] == 168) || (b[0] == 198 && b[1]&0xfe == 18) || b[0] >= 224
	}
	if len(ip) != net.IPv6len {
		return true // not an address
	}
	return ip[0] == 0 || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsMulticast() ||
		(ip[0] == 0xfe && ip[1]&0xc0 == 0xc0) // ::/8 (reserved) … site-local
}

// embeddedIPv4 is the IPv4 address inside an IPv4-compatible (::a.b.c.d, ::,
// ::1), NAT64 (64:ff9b::/96) or 6to4 (2002::/16) IPv6 address, else nil.
// (IPv4-mapped addresses are already IPv4 to To4.)
func embeddedIPv4(ip net.IP) net.IP {
	if len(ip) != net.IPv6len || ip.To4() != nil {
		return nil
	}
	zero := func(b []byte) bool {
		for _, x := range b {
			if x != 0 {
				return false
			}
		}
		return true
	}
	switch {
	case zero(ip[:12]), ip[0] == 0 && ip[1] == 0x64 && ip[2] == 0xff && ip[3] == 0x9b && zero(ip[4:12]):
		return net.IPv4(ip[12], ip[13], ip[14], ip[15])
	case ip[0] == 0x20 && ip[1] == 0x02:
		return net.IPv4(ip[2], ip[3], ip[4], ip[5])
	}
	return nil
}

// ResolvesLocal looks a host up and reports whether any address it has is local.
func ResolvesLocal(ctx context.Context, host string) bool {
	if LocalAddress(host) {
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	addrs, err := net.DefaultResolver.LookupIPAddr(cctx, host)
	if err != nil {
		return false // unresolvable: the browser can't reach it either
	}
	for _, ad := range addrs {
		if localIP(ad.IP) {
			return true
		}
	}
	return false
}

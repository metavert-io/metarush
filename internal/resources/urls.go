package resources

import (
	"context"
	"errors"
	"net/url"
	"path"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
)

// Address routes: what the Add box does with a pasted or dropped address.
const (
	LinkVideo     = "video"     // a video page: the link import (yt-dlp)
	LinkRepo      = "repo"      // a GitHub repository (part 2)
	LinkPage      = "page"      // a web page or a picture on the web (part 2: snapshots)
	LinkAmbiguous = "ambiguous" // could be a video or a page: ask
)

// URLSource turns an address into resources: a GitHub repository pinned at a
// commit, a website snapshot (part 2). Part 1 registers the two kinds without
// an implementation, so the Add box recognizes them and says they are coming
// with the next part; part 2 replaces them with RegisterURLSource.
type URLSource interface {
	// Name is "github" or "website".
	Name() string
	// Route is the address route it handles (LinkRepo, LinkPage).
	Route() string
	// Match reports whether it handles this address.
	Match(u *url.URL) bool
	// Add brings the address in (ErrComingSoon when not built yet).
	Add(ctx context.Context, a *app.App, in URLAddIn) (*URLAddOut, error)
}

// URLAddIn is one address to bring in.
type URLAddIn struct {
	URL       string
	ProjectID string // empty: the workspace library
	// Depth 1 also takes same-site pages a page links to (MaxPages in all).
	Depth    int
	MaxPages int
}

// URLAddOut is what an address became: resources at once, or a fetch job
// that makes them (JobID; Status fetching or already).
type URLAddOut struct {
	Resources []*models.Resource
	Message   string
	JobID     string
	Status    string
}

// ErrComingSoon is what a URL source that isn't built yet returns.
var ErrComingSoon = errors.New("not available in this version of MetaRush yet")

var (
	urlMu      sync.RWMutex
	urlSources = []URLSource{placeholder{name: "github", route: LinkRepo}, placeholder{name: "website", route: LinkPage}}
)

// RegisterURLSource adds or replaces (by name) a URL source.
func RegisterURLSource(s URLSource) {
	urlMu.Lock()
	defer urlMu.Unlock()
	for i, x := range urlSources {
		if x.Name() == s.Name() {
			urlSources[i] = s
			return
		}
	}
	urlSources = append(urlSources, s)
}

// URLSourceFor returns the source that handles an address of that route.
func URLSourceFor(route string, u *url.URL) URLSource {
	urlMu.RLock()
	defer urlMu.RUnlock()
	for _, s := range urlSources {
		if s.Route() == route && s.Match(u) {
			return s
		}
	}
	return nil
}

type placeholder struct{ name, route string }

func (p placeholder) Name() string  { return p.name }
func (p placeholder) Route() string { return p.route }
func (p placeholder) Match(u *url.URL) bool {
	if p.name == "github" {
		return isGitHub(u)
	}
	return true
}
func (p placeholder) Add(context.Context, *app.App, URLAddIn) (*URLAddOut, error) {
	return nil, ErrComingSoon
}

// ComingSoonMessage is the plain answer for an address kind not built yet.
func ComingSoonMessage(route string) string {
	if route == LinkRepo {
		return "This version of MetaRush can't bring in GitHub repositories yet, so nothing was added."
	}
	return "This version of MetaRush can't take snapshots of web pages yet, so nothing was added. For a picture on a page, save it and drop the file here."
}

// videoHosts are sites whose links are videos (the link import downloads them).
var videoHosts = []string{"vimeo.com", "player.vimeo.com", "dailymotion.com", "dai.ly", "twitch.tv", "clips.twitch.tv", "loom.com", "streamable.com",
	"bilibili.com", "rumble.com", "odysee.com", "ted.com", "archive.org", "wistia.com", "vidyard.com", "facebook.com", "fb.watch", "instagram.com", "reddit.com", "v.redd.it", "threads.net", "linkedin.com"}

var (
	videoExt   = map[string]bool{".mp4": true, ".mov": true, ".m4v": true, ".webm": true, ".mkv": true, ".m3u8": true, ".mp3": true, ".m4a": true, ".wav": true}
	pictureExt = map[string]bool{".png": true, ".jpg": true, ".jpeg": true, ".webp": true, ".gif": true, ".svg": true, ".pdf": true, ".heic": true, ".avif": true}
)

// RouteWebAddress decides about an address the link import calls "web" (not
// YouTube, TikTok or X): a known video site or a video file is a video; a
// GitHub repository is a repository; a picture or PDF address is a page
// resource; any other page could be either, so the Add box asks.
func RouteWebAddress(raw string) (route string, u *url.URL) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return LinkAmbiguous, u
	}
	if isGitHub(u) {
		return LinkRepo, u
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	for _, h := range videoHosts {
		if host == h || strings.HasSuffix(host, "."+h) {
			return LinkVideo, u
		}
	}
	ext := strings.ToLower(path.Ext(u.Path))
	switch {
	case videoExt[ext]:
		return LinkVideo, u
	case pictureExt[ext]:
		return LinkPage, u
	}
	return LinkAmbiguous, u
}

func isGitHub(u *url.URL) bool {
	if u == nil {
		return false
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host != "github.com" {
		return false
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	return len(parts) >= 2 && parts[0] != "" && parts[1] != ""
}

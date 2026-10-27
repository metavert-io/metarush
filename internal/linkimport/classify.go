// Package linkimport brings videos in from links — YouTube, TikTok and X posts
// (and other sites yt-dlp supports) — and hands the downloaded file to the one
// ingest pipeline, so dedupe, previews, transcripts, speakers, notifications and
// job details work exactly as for dropped files. Each import is a durable job
// pinned to the machine that asked (its network, and its browser sign-in only
// when the person opts in for that import); progress has three stages —
// looking up the video, downloading, adding to MetaRush — and the original
// post is recorded on the clip (models.AssetSource) for credits and reference.
package linkimport

import (
	"errors"
	"net"
	"net/url"
	"regexp"
	"strings"

	"github.com/metavert-io/metarush/internal/models"
)

// Link kinds.
const (
	KindVideo    = "video"    // one video (a YouTube watch/shorts/live link, a TikTok video)
	KindPost     = "post"     // an X post: its video (or videos)
	KindPlaylist = "playlist" // a list of videos: nothing is imported unless asked (--playlist N)
	KindChannel  = "channel"  // a channel or profile page: nothing is imported unless asked
)

// Link is a pasted address after classification (no network involved).
type Link struct {
	Input    string `json:"input"`
	URL      string `json:"url" jsonschema:"description=The canonical address MetaRush downloads from."`
	Platform string `json:"platform" jsonschema:"enum=youtube,enum=tiktok,enum=x,enum=web"`
	Kind     string `json:"kind" jsonschema:"enum=video,enum=post,enum=playlist,enum=channel"`
	// VideoID is the site's video ID when the link says it (YouTube, TikTok, the X post ID).
	VideoID string `json:"videoId,omitempty"`
	// ListID is the playlist of a YouTube link (a playlist page, or a video opened from one).
	ListID string `json:"listId,omitempty"`
	// Note is a plain remark about the link ("This is a video inside a playlist; only the video is imported.").
	Note string `json:"note,omitempty"`
}

// Many reports whether the link points at several videos (a playlist, a channel
// or profile page) rather than one.
func (l Link) Many() bool { return l.Kind == KindPlaylist || l.Kind == KindChannel }

// ErrNotALink and friends are classification refusals, in plain words.
var (
	ErrNotALink  = errors.New("that isn't a web link")
	ErrLocalLink = errors.New("that link points at this computer or a local network, so MetaRush won't download from it")
	ErrNoVideo   = errors.New("there's no video at that address")
)

var (
	ytID       = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)
	digits     = regexp.MustCompile(`^[0-9]{5,25}$`)
	ytHandle   = regexp.MustCompile(`^@[A-Za-z0-9._-]{1,100}$`)
	ttHandle   = regexp.MustCompile(`^@[A-Za-z0-9._-]{1,100}$`)
	xUser      = regexp.MustCompile(`^[A-Za-z0-9_]{1,30}$`)
	shortCode  = regexp.MustCompile(`^[A-Za-z0-9_-]{4,40}$`)
	ytList     = regexp.MustCompile(`^[A-Za-z0-9_-]{10,64}$`)
	mediaIndex = regexp.MustCompile(`^[1-4]$`)
)

// xReserved are first path segments on x.com that are not accounts.
var xReserved = map[string]bool{"home": true, "explore": true, "search": true, "notifications": true, "messages": true, "settings": true,
	"login": true, "signup": true, "tos": true, "privacy": true, "intent": true, "share": true, "hashtag": true, "compose": true}

// Classify reads a pasted address: which site, whether it is one video or
// many, the video ID when the link carries one, and the canonical address to
// download. It never touches the network.
func Classify(raw string) (Link, error) {
	in := strings.TrimSpace(raw)
	in = strings.Trim(in, "<>\"'`")
	l := Link{Input: in}
	if in == "" || strings.ContainsAny(in, " \t\r\n") {
		return l, ErrNotALink
	}
	if !strings.Contains(in, "://") {
		// "youtu.be/abc", "www.tiktok.com/@a/video/1": a link without its scheme.
		if !strings.Contains(strings.SplitN(in, "/", 2)[0], ".") {
			return l, ErrNotALink
		}
		in = "https://" + in
	}
	u, err := url.Parse(in)
	if err != nil || u.Host == "" {
		return l, ErrNotALink
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "http" && scheme != "https" {
		return l, ErrNotALink
	}
	if u.User != nil {
		return l, ErrNotALink // addresses with credentials are never followed
	}
	host := strings.ToLower(u.Hostname())
	if localHost(host) {
		return l, ErrLocalLink
	}
	host = strings.TrimPrefix(host, "www.")
	host = strings.TrimPrefix(host, "m.")
	host = strings.TrimPrefix(host, "mobile.")
	parts := pathParts(u.Path)
	switch host {
	case "youtube.com", "music.youtube.com", "youtube-nocookie.com", "youtu.be":
		return classifyYouTube(l, host, u, parts)
	case "tiktok.com", "vm.tiktok.com", "vt.tiktok.com":
		return classifyTikTok(l, host, u, parts)
	case "x.com", "twitter.com":
		return classifyX(l, u, parts)
	}
	// Another site: yt-dlp may well support it. It is imported the same way.
	l.Platform, l.Kind = models.PlatformWeb, KindVideo
	u.Fragment = ""
	l.URL = u.String()
	return l, nil
}

func pathParts(p string) []string {
	var out []string
	for _, s := range strings.Split(p, "/") {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func localHost(host string) bool {
	if host == "localhost" || strings.HasSuffix(host, ".localhost") || strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip == nil {
		return !strings.Contains(host, ".") // a bare name ("nas") resolves on the local network
	}
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() || ip.IsLinkLocalMulticast()
}

func classifyYouTube(l Link, host string, u *url.URL, parts []string) (Link, error) {
	l.Platform = models.PlatformYouTube
	q := u.Query()
	video := func(id string) (Link, error) {
		if !ytID.MatchString(id) {
			return l, ErrNoVideo
		}
		l.Kind, l.VideoID = KindVideo, id
		l.URL = "https://www.youtube.com/watch?v=" + id
		if list := q.Get("list"); list != "" && ytList.MatchString(list) {
			l.ListID = list
			l.Note = "This is a video inside a playlist; only this video is imported."
		}
		return l, nil
	}
	if host == "youtu.be" {
		if len(parts) == 0 {
			return l, ErrNoVideo
		}
		return video(parts[0])
	}
	if len(parts) == 0 {
		return l, ErrNoVideo
	}
	switch parts[0] {
	case "watch":
		return video(q.Get("v"))
	case "shorts", "live", "embed", "v", "e":
		if len(parts) < 2 {
			return l, ErrNoVideo
		}
		return video(parts[1])
	case "playlist":
		list := q.Get("list")
		if !ytList.MatchString(list) {
			return l, ErrNoVideo
		}
		l.Kind, l.ListID = KindPlaylist, list
		l.URL = "https://www.youtube.com/playlist?list=" + list
		return l, nil
	case "channel", "c", "user":
		if len(parts) < 2 {
			return l, ErrNoVideo
		}
		l.Kind = KindChannel
		l.URL = "https://www.youtube.com/" + parts[0] + "/" + parts[1]
		return l, nil
	}
	if ytHandle.MatchString(parts[0]) {
		l.Kind = KindChannel
		l.URL = "https://www.youtube.com/" + parts[0]
		if len(parts) > 1 && parts[1] == "shorts" {
			l.URL += "/shorts"
		} else {
			l.URL += "/videos"
		}
		return l, nil
	}
	return l, ErrNoVideo
}

func classifyTikTok(l Link, host string, u *url.URL, parts []string) (Link, error) {
	l.Platform = models.PlatformTikTok
	if host == "vm.tiktok.com" || host == "vt.tiktok.com" || (len(parts) >= 2 && parts[0] == "t") {
		// A short share link: the video it points at is known after the lookup.
		code := ""
		if len(parts) == 1 {
			code = parts[0]
		} else if len(parts) >= 2 && parts[0] == "t" {
			code = parts[1]
		}
		if !shortCode.MatchString(code) {
			return l, ErrNoVideo
		}
		l.Kind = KindVideo
		l.URL = "https://" + host + "/" + strings.Join(parts, "/")
		if host == "tiktok.com" {
			l.URL = "https://www.tiktok.com/t/" + code
		}
		return l, nil
	}
	switch {
	case len(parts) >= 3 && ttHandle.MatchString(parts[0]) && parts[1] == "video" && digits.MatchString(parts[2]):
		l.Kind, l.VideoID = KindVideo, parts[2]
		l.URL = "https://www.tiktok.com/" + parts[0] + "/video/" + parts[2]
		return l, nil
	case len(parts) >= 3 && ttHandle.MatchString(parts[0]) && parts[1] == "photo":
		return l, errors.New("that TikTok post is a photo slideshow, not a video")
	case len(parts) >= 2 && parts[0] == "embed":
		id := parts[len(parts)-1]
		if !digits.MatchString(id) {
			return l, ErrNoVideo
		}
		l.Kind, l.VideoID = KindVideo, id
		l.URL = "https://www.tiktok.com/embed/v2/" + id
		return l, nil
	case len(parts) >= 1 && ttHandle.MatchString(parts[0]):
		l.Kind = KindChannel
		l.URL = "https://www.tiktok.com/" + parts[0]
		return l, nil
	}
	return l, ErrNoVideo
}

func classifyX(l Link, u *url.URL, parts []string) (Link, error) {
	l.Platform = models.PlatformX
	switch {
	case len(parts) >= 3 && (parts[1] == "status" || parts[1] == "statuses") && digits.MatchString(parts[2]):
		l.Kind, l.VideoID = KindPost, parts[2]
		l.URL = "https://x.com/" + parts[0] + "/status/" + parts[2]
		if len(parts) >= 5 && parts[3] == "video" && mediaIndex.MatchString(parts[4]) {
			l.URL += "/video/" + parts[4]
		}
		return l, nil
	case len(parts) >= 4 && parts[0] == "i" && parts[1] == "web" && parts[2] == "status" && digits.MatchString(parts[3]):
		l.Kind, l.VideoID = KindPost, parts[3]
		l.URL = "https://x.com/i/status/" + parts[3]
		return l, nil
	case len(parts) >= 2 && parts[0] == "i" && (parts[1] == "spaces" || parts[1] == "broadcasts"):
		return l, errors.New("live broadcasts and Spaces on X can't be imported")
	case len(parts) >= 1 && xUser.MatchString(parts[0]) && !xReserved[strings.ToLower(parts[0])]:
		l.Kind = KindChannel
		l.URL = "https://x.com/" + parts[0]
		return l, nil
	}
	return l, ErrNoVideo
}

// FindLinks pulls web links out of pasted or dropped text (one per line,
// several separated by spaces, or a uri-list).
func FindLinks(text string) []string {
	var out []string
	seen := map[string]bool{}
	for _, f := range strings.Fields(text) {
		f = strings.Trim(f, "<>()[]{}\"',;")
		if strings.HasPrefix(f, "#") || f == "" {
			continue
		}
		if !strings.HasPrefix(f, "http://") && !strings.HasPrefix(f, "https://") {
			continue
		}
		if !seen[f] {
			seen[f] = true
			out = append(out, f)
		}
	}
	return out
}

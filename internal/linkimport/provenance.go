package linkimport

import (
	"fmt"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/models"
)

// Info is the part of yt-dlp's metadata JSON (-J) MetaRush reads.
type Info struct {
	Type             string   `json:"_type"`
	ID               string   `json:"id"`
	Title            string   `json:"title"`
	FullTitle        string   `json:"fulltitle"`
	Description      string   `json:"description"`
	Uploader         string   `json:"uploader"`
	UploaderID       string   `json:"uploader_id"`
	UploaderURL      string   `json:"uploader_url"`
	Channel          string   `json:"channel"`
	ChannelID        string   `json:"channel_id"`
	ChannelURL       string   `json:"channel_url"`
	Creator          string   `json:"creator"`
	Artist           string   `json:"artist"`
	UploadDate       string   `json:"upload_date"` // YYYYMMDD
	Timestamp        *float64 `json:"timestamp"`
	License          string   `json:"license"`
	WebpageURL       string   `json:"webpage_url"`
	OriginalURL      string   `json:"original_url"`
	Extractor        string   `json:"extractor"`
	ExtractorKey     string   `json:"extractor_key"`
	Duration         float64  `json:"duration"`
	Width            int      `json:"width"`
	Height           int      `json:"height"`
	FPS              float64  `json:"fps"`
	VCodec           string   `json:"vcodec"`
	ACodec           string   `json:"acodec"`
	Ext              string   `json:"ext"`
	IsLive           bool     `json:"is_live"`
	LiveStatus       string   `json:"live_status"`
	Availability     string   `json:"availability"`
	HasDRM           any      `json:"has_drm"`
	Filesize         float64  `json:"filesize"`
	FilesizeApprox   float64  `json:"filesize_approx"`
	Format           string   `json:"format"`
	FormatID         string   `json:"format_id"`
	PlaylistCount    int      `json:"playlist_count"`
	PlaylistIndex    int      `json:"playlist_index"`
	URL              string   `json:"url"`    // flat playlist entries: the video's address
	IEKey            string   `json:"ie_key"` // flat playlist entries
	RequestedFormats []Format `json:"requested_formats"`
	Entries          []Info   `json:"entries"`
}

// Format is one downloadable stream in yt-dlp's metadata.
type Format struct {
	FormatID       string  `json:"format_id"`
	Ext            string  `json:"ext"`
	VCodec         string  `json:"vcodec"`
	ACodec         string  `json:"acodec"`
	Width          int     `json:"width"`
	Height         int     `json:"height"`
	FPS            float64 `json:"fps"`
	Filesize       float64 `json:"filesize"`
	FilesizeApprox float64 `json:"filesize_approx"`
	HasDRM         any     `json:"has_drm"`
}

// Size is the format's size in bytes (exact when known, else the estimate).
func (f Format) Size() int64 {
	if f.Filesize > 0 {
		return int64(f.Filesize)
	}
	return int64(f.FilesizeApprox)
}

// DRM reports whether yt-dlp marked the video or a chosen format as
// copy-protected. MetaRush refuses those (it never works around DRM).
func (in *Info) DRM() bool {
	if truthy(in.HasDRM) {
		return true
	}
	for _, f := range in.RequestedFormats {
		if truthy(f.HasDRM) {
			return true
		}
	}
	return false
}

func truthy(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x != "" && x != "false" // yt-dlp uses "maybe" for "possibly DRM"
	}
	return false
}

// Live reports a stream that is still live or has not started (nothing to
// download yet).
func (in *Info) Live() bool {
	return in.IsLive || in.LiveStatus == "is_live" || in.LiveStatus == "is_upcoming"
}

// ExpectedSize is the total of the formats yt-dlp chose (0 when unknown).
func (in *Info) ExpectedSize() int64 {
	var n int64
	for _, f := range in.RequestedFormats {
		n += f.Size()
	}
	if n == 0 {
		if in.Filesize > 0 {
			return int64(in.Filesize)
		}
		return int64(in.FilesizeApprox)
	}
	return n
}

const maxDescription = 1000

var (
	genericTikTokTitle = regexp.MustCompile(`^TikTok video #\d+$`)
	tcoLink            = regexp.MustCompile(`\s*https://t\.co/\S+`)
)

// Provenance maps yt-dlp's metadata to what MetaRush records about the original.
func Provenance(platform string, in *Info, tool, toolVersion string, signedIn bool, now time.Time) models.AssetSource {
	s := models.AssetSource{Platform: platform, VideoID: in.ID, Title: strings.TrimSpace(firstNonEmpty(in.Title, in.FullTitle)), Description: shorten(in.Description, maxDescription),
		License: strings.TrimSpace(in.License), Duration: in.Duration, DownloadedAt: now.UTC(), Tool: tool, ToolVersion: toolVersion, Method: models.SourceDownload, SignedIn: signedIn}
	s.URL = firstNonEmpty(in.WebpageURL, in.OriginalURL)
	if d := in.UploadDate; len(d) == 8 && allDigits(d) {
		s.UploadDate = d[:4] + "-" + d[4:6] + "-" + d[6:]
	} else if in.Timestamp != nil && *in.Timestamp > 0 {
		s.UploadDate = time.Unix(int64(*in.Timestamp), 0).UTC().Format("2006-01-02")
	}
	switch platform {
	case models.PlatformYouTube:
		// uploader is the channel's name; uploader_id its @handle.
		s.Uploader = firstNonEmpty(in.Uploader, in.Channel)
		if strings.HasPrefix(in.UploaderID, "@") {
			s.Handle = in.UploaderID
		}
		s.ChannelURL = firstNonEmpty(in.UploaderURL, in.ChannelURL)
	case models.PlatformTikTok:
		// uploader is the @name (without the @); channel the display name.
		s.Uploader = firstNonEmpty(in.Channel, in.Creator, in.Artist, in.Uploader)
		if h := strings.TrimPrefix(strings.TrimSpace(in.Uploader), "@"); h != "" && !strings.Contains(h, " ") {
			s.Handle = "@" + h
			s.ChannelURL = "https://www.tiktok.com/@" + h
		} else {
			s.ChannelURL = in.UploaderURL
		}
		if s.Title == "" || genericTikTokTitle.MatchString(s.Title) {
			s.Title = firstLine(in.Description, 100)
		}
	case models.PlatformX:
		s.Uploader = firstNonEmpty(in.Uploader, in.Channel)
		if h := strings.TrimPrefix(strings.TrimSpace(in.UploaderID), "@"); h != "" && !allDigits(h) {
			s.Handle = "@" + h
			s.ChannelURL = "https://x.com/" + h
		}
		// yt-dlp titles X posts "<name> - <text…>": the post's own words read
		// better, without the t.co link to the media itself.
		if t := firstLine(tcoLink.ReplaceAllString(in.Description, ""), 100); t != "" {
			s.Title = t
		}
		s.URL = strings.Replace(s.URL, "://twitter.com/", "://x.com/", 1)
	default:
		s.Uploader = firstNonEmpty(in.Uploader, in.Channel, in.Creator)
		s.ChannelURL = firstNonEmpty(in.UploaderURL, in.ChannelURL)
		if h := strings.TrimSpace(in.UploaderID); strings.HasPrefix(h, "@") {
			s.Handle = h
		}
		// IDs are only unique per site: keep the site with them.
		if site := strings.ToLower(firstNonEmpty(in.ExtractorKey, in.Extractor)); site != "" && in.ID != "" {
			s.VideoID = site + ":" + in.ID
		}
	}
	if s.Title == "" {
		s.Title = DefaultTitle(platform, s.Who())
	}
	return s
}

// DefaultTitle names a video that has no title of its own: "TikTok video by @name".
func DefaultTitle(platform, who string) string {
	t := models.PlatformName(platform) + " video"
	if platform == models.PlatformWeb || platform == "" {
		t = "Video"
	}
	if who != "" {
		t += " by " + who
	}
	return t
}

// FileName is the clip's file name: its title, made safe for every file system.
func FileName(title, ext string) string {
	var b strings.Builder
	for _, r := range title {
		switch {
		case r < 32 || strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune(' ')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.Join(strings.Fields(b.String()), " ")
	name = strings.Trim(name, ". ")
	if utf8.RuneCountInString(name) > 120 {
		name = string([]rune(name)[:120])
		name = strings.TrimSpace(name)
	}
	if name == "" {
		name = "video"
	}
	if ext == "" {
		ext = "mp4"
	}
	return name + "." + strings.TrimPrefix(ext, ".")
}

func shorten(s string, n int) string {
	s = strings.TrimSpace(s)
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return strings.TrimSpace(string(r[:n-1])) + "…"
}

func firstLine(s string, n int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return shorten(strings.Join(strings.Fields(s), " "), n)
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if v = strings.TrimSpace(v); v != "" {
			return v
		}
	}
	return ""
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// listedTitles names the videos of a list for a dry run:
// ": “One”, “Two”, “Three”, “Four”, “Five” and 2 more" (empty when none has a title).
func listedTitles(entries []Info, n int) string {
	var names []string
	for _, e := range entries {
		if t := firstLine(firstNonEmpty(e.Title, e.FullTitle), 80); t != "" {
			names = append(names, "“"+t+"”")
		}
	}
	if len(names) == 0 {
		return ""
	}
	more := 0
	if len(names) > n {
		more, names = len(names)-n, names[:n]
	}
	s := ": " + strings.Join(names, ", ")
	if more > 0 {
		s += fmt.Sprintf(" and %d more", more)
	}
	return s
}

// Summary is the one-line description of a looked-up video for people:
// "“Big Buck Bunny” by @BlenderOfficial on YouTube, 10:35".
func Summary(platform string, s models.AssetSource) string {
	out := "“" + s.Title + "”"
	if w := s.Who(); w != "" {
		out += " by " + w
	}
	out += " on " + models.PlatformName(platform)
	if s.Duration > 0 {
		d := int(s.Duration + 0.5)
		if d >= 3600 {
			out += fmt.Sprintf(", %d:%02d:%02d", d/3600, d%3600/60, d%60)
		} else {
			out += fmt.Sprintf(", %d:%02d", d/60, d%60)
		}
	}
	return out
}

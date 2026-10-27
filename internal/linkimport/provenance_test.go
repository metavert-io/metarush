package linkimport

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/models"
)

// Trimmed from real yt-dlp 2026.08.19 output for public videos (a Blender open
// movie on YouTube, CC-BY; a public TikTok; a public X post).
const ytJSON = `{"_type":"video","id":"aqz-KE-bpKQ","title":"Big Buck Bunny 60fps 4K - Official Blender Foundation Short Film",
"description":"UHD High Frame rate version of the iconic short film by Blender, Big Buck Bunny.","uploader":"Blender","uploader_id":"@BlenderOfficial",
"uploader_url":"https://www.youtube.com/@BlenderOfficial","channel":"Blender","channel_id":"UCSMOQeBJ2RAnuFungnQOxLg","channel_url":"https://www.youtube.com/channel/UCSMOQeBJ2RAnuFungnQOxLg",
"upload_date":"20141110","timestamp":1415628355,"license":"Creative Commons Attribution license (reuse allowed)","webpage_url":"https://www.youtube.com/watch?v=aqz-KE-bpKQ",
"extractor":"youtube","extractor_key":"Youtube","duration":635,"width":3840,"height":2160,"fps":60,"live_status":"not_live","availability":"public",
"requested_formats":[{"format_id":"315","vcodec":"vp9","acodec":"none","height":2160,"filesize":1362269481},{"format_id":"140","vcodec":"none","acodec":"mp4a.40.2","filesize":10267000}]}`

const ttJSON = `{"_type":"video","id":"7000000000000000042","title":"TikTok video #7000000000000000042","description":"","uploader":"gardenclub42",
"uploader_id":"100000000000000042","uploader_url":"https://www.tiktok.com/@gardenclub42","channel":"Garden Club","channel_id":"MS4wLjABAAAA",
"channel_url":"https://www.tiktok.com/@MS4wLjABAAAA","upload_date":"20211230","webpage_url":"https://www.tiktok.com/@gardenclub42/video/7000000000000000042",
"extractor":"TikTok","duration":21,"artist":"Garden Club","filesize":5076682}`

const xJSON = `{"_type":"video","id":"1578401165338976258","title":"Prime Video UK & IE - A new power will be forged.   Don’t miss the season finale",
"description":"A new power will be forged.   Don’t miss the season finale of #TheRingsOfPower this Friday, only on @primevideo","uploader":"Prime Video UK & IE",
"uploader_id":"primevideouk","uploader_url":"https://twitter.com/primevideouk","upload_date":"20221007","webpage_url":"https://twitter.com/primevideouk/status/1578401165338976258","duration":6}`

func decode(t *testing.T, s string) *Info {
	t.Helper()
	var in Info
	if err := json.Unmarshal([]byte(s), &in); err != nil {
		t.Fatal(err)
	}
	return &in
}

func TestProvenanceYouTube(t *testing.T) {
	now := time.Date(2026, 10, 18, 12, 0, 0, 0, time.UTC)
	s := Provenance(models.PlatformYouTube, decode(t, ytJSON), "yt-dlp", "2026.08.19", false, now)
	want := models.AssetSource{Platform: "youtube", URL: "https://www.youtube.com/watch?v=aqz-KE-bpKQ", VideoID: "aqz-KE-bpKQ", Uploader: "Blender", Handle: "@BlenderOfficial",
		ChannelURL: "https://www.youtube.com/@BlenderOfficial", UploadDate: "2014-11-10", Title: "Big Buck Bunny 60fps 4K - Official Blender Foundation Short Film",
		Description: "UHD High Frame rate version of the iconic short film by Blender, Big Buck Bunny.", License: "Creative Commons Attribution license (reuse allowed)",
		Duration: 635, DownloadedAt: now, Tool: "yt-dlp", ToolVersion: "2026.08.19", Method: models.SourceDownload}
	if s != want {
		t.Fatalf("provenance:\n got %+v\nwant %+v", s, want)
	}
	if got := s.Line(); got != "From YouTube · @BlenderOfficial · 10 Nov 2014" {
		t.Errorf("line: %q", got)
	}
	if got := s.Credit(); got != "Includes footage from @BlenderOfficial on YouTube: https://www.youtube.com/watch?v=aqz-KE-bpKQ" {
		t.Errorf("credit: %q", got)
	}
	if got := Summary("youtube", s); got != "“Big Buck Bunny 60fps 4K - Official Blender Foundation Short Film” by @BlenderOfficial on YouTube, 10:35" {
		t.Errorf("summary: %q", got)
	}
	if in := decode(t, ytJSON); in.ExpectedSize() != 1362269481+10267000 {
		t.Errorf("expected size %d", in.ExpectedSize())
	}
}

func TestProvenanceTikTok(t *testing.T) {
	s := Provenance(models.PlatformTikTok, decode(t, ttJSON), "yt-dlp", "2026.08.19", true, time.Now())
	if s.Handle != "@gardenclub42" || s.Uploader != "Garden Club" || s.ChannelURL != "https://www.tiktok.com/@gardenclub42" || s.UploadDate != "2021-12-30" || !s.SignedIn {
		t.Fatalf("tiktok account: %+v", s)
	}
	// "TikTok video #…" with no caption: a readable default title.
	if s.Title != "TikTok video by @gardenclub42" {
		t.Errorf("title %q", s.Title)
	}
	if s.License != "" {
		t.Errorf("no license is stated on TikTok: %q", s.License)
	}
}

func TestProvenanceX(t *testing.T) {
	s := Provenance(models.PlatformX, decode(t, xJSON), "yt-dlp", "2026.08.19", false, time.Now())
	if s.Handle != "@primevideouk" || s.Uploader != "Prime Video UK & IE" || s.URL != "https://x.com/primevideouk/status/1578401165338976258" || s.ChannelURL != "https://x.com/primevideouk" {
		t.Fatalf("x account: %+v", s)
	}
	if !strings.HasPrefix(s.Title, "A new power will be forged.") {
		t.Errorf("the post's words make the title: %q", s.Title)
	}
	if s.Line() != "From X · @primevideouk · 7 Oct 2022" {
		t.Errorf("line %q", s.Line())
	}
}

func TestProvenanceOtherSite(t *testing.T) {
	s := Provenance(models.PlatformWeb, &Info{ID: "76979871", Title: "Clip", Extractor: "vimeo", ExtractorKey: "Vimeo", Uploader: "Someone", WebpageURL: "https://vimeo.com/76979871"}, "yt-dlp", "1", false, time.Now())
	if s.VideoID != "vimeo:76979871" || s.Who() != "Someone" || !strings.HasPrefix(s.Credit(), "Includes footage from Someone on the web") {
		t.Fatalf("other site: %+v %q", s, s.Credit())
	}
}

func TestDescriptionIsShortened(t *testing.T) {
	in := &Info{ID: "x", Title: "t", Description: strings.Repeat("word ", 400)}
	s := Provenance(models.PlatformYouTube, in, "yt-dlp", "1", false, time.Now())
	if n := len([]rune(s.Description)); n > maxDescription || !strings.HasSuffix(s.Description, "…") {
		t.Fatalf("description %d runes", n)
	}
}

func TestFileName(t *testing.T) {
	for in, want := range map[string]string{
		"Big Buck Bunny 60fps 4K - Official": "Big Buck Bunny 60fps 4K - Official.mp4",
		`a/b\c:d*e?f"g<h>i|j`:                "a b c d e f g h i j.mp4",
		"  ..hidden.  ":                      "hidden.mp4",
		"":                                   "video.mp4",
		strings.Repeat("é", 200):             strings.Repeat("é", 120) + ".mp4",
		"line\nbreak\ttab":                   "line break tab.mp4",
	} {
		if got := FileName(in, "mp4"); got != want {
			t.Errorf("FileName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestListedTitles(t *testing.T) {
	var entries []Info
	for _, s := range []string{"One", "", "Two", "Three", "Four", "Five", "Six", "Seven"} {
		entries = append(entries, Info{Title: s})
	}
	if got, want := listedTitles(entries, 5), ": “One”, “Two”, “Three”, “Four”, “Five” and 2 more"; got != want {
		t.Fatalf("listedTitles = %q, want %q", got, want)
	}
	if got := listedTitles(entries[:3], 5); got != ": “One”, “Two”" {
		t.Fatalf("short list: %q", got)
	}
	if got := listedTitles([]Info{{URL: "https://example.com/v"}}, 5); got != "" {
		t.Fatalf("untitled list: %q", got)
	}
}

func TestDRMAndLive(t *testing.T) {
	if !(&Info{HasDRM: true}).DRM() || !(&Info{RequestedFormats: []Format{{HasDRM: "maybe"}}}).DRM() || (&Info{HasDRM: false}).DRM() {
		t.Fatal("DRM marks")
	}
	if !(&Info{LiveStatus: "is_live"}).Live() || !(&Info{LiveStatus: "is_upcoming"}).Live() || (&Info{LiveStatus: "was_live"}).Live() {
		t.Fatal("live status: an ended stream can be imported")
	}
}

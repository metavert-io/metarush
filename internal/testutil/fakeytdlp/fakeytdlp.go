// Package fakeytdlp is a stand-in `yt-dlp` for tests, the acceptance run and the
// dashboard's end-to-end check: no network, real media. It answers the calls
// MetaRush makes — the debug header (-v with no link), --version, -U, -J
// lookups of videos, posts and playlists, and downloads from saved metadata or
// a link with MetaRush's progress template and --print-to-file — and makes a
// short, unique test clip with ffmpeg for every video ID.
//
// The link decides what happens (words anywhere in it):
//
//	bot-check   "Sign in to confirm you're not a bot" unless --cookies-from-browser is given
//	drm         "This video is DRM protected"
//	private     "Private video"
//	removed     "Video unavailable. This video has been removed"
//	flaky       the first lookup fails with a network error, later ones work
//	forbidden   the download fails with HTTP Error 403 (the lookup works)
//	livestream  a live stream that hasn't ended
//	multi       an X post with two videos
//	slow        downloads take 15 seconds (watch the progress)
//	cc          YouTube states a Creative Commons license
//
// Playlist lookups (--flat-playlist) list three videos. METARUSH_FAKE_YTDLP_LOG
// names a file that gets every call's arguments (one JSON array per line).
package fakeytdlp

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"
)

// Version is what the fake reports.
const Version = "2026.08.19"

const runEnv = "METARUSH_FAKE_YTDLP"

// MaybeRun makes a test binary act as the fake when started through Write's
// wrapper. Call it first in TestMain.
func MaybeRun() {
	if os.Getenv(runEnv) != "1" {
		return
	}
	os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
}

// Write puts a `yt-dlp` wrapper in a temp dir that runs this test binary as the
// fake (its TestMain must call MaybeRun) and returns the wrapper's path.
func Write(t testing.TB) string {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "yt-dlp")
	script := "#!/bin/sh\nexec env " + runEnv + "=1 '" + strings.ReplaceAll(exe, "'", `'\''`) + "' \"$@\"\n"
	if err := os.WriteFile(p, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

type call struct {
	args      []string
	link      string
	json      bool
	flat      bool
	version   bool
	update    bool
	verbose   bool
	cookies   string
	infoFile  string
	dir       string
	pathFile  string
	ffmpeg    string
	templates []string
	items     string
}

func parse(args []string) call {
	c := call{args: args}
	for i := 0; i < len(args); i++ {
		a := args[i]
		next := func() string {
			if i+1 < len(args) {
				i++
				return args[i]
			}
			return ""
		}
		switch a {
		case "--":
			if i+1 < len(args) {
				c.link = args[i+1]
			}
			i = len(args)
		case "-J", "--dump-single-json":
			c.json = true
		case "--flat-playlist":
			c.flat = true
		case "--version":
			c.version = true
		case "-U", "--update":
			c.update = true
		case "-v", "--verbose":
			c.verbose = true
		case "--cookies-from-browser":
			c.cookies = next()
		case "--load-info-json":
			c.infoFile = next()
		case "-P", "--paths":
			c.dir = next()
		case "--ffmpeg-location":
			c.ffmpeg = next()
		case "--progress-template":
			c.templates = append(c.templates, next())
		case "-I", "--playlist-items":
			c.items = next()
		case "--print-to-file":
			_ = next() // after_move:filepath
			c.pathFile = next()
		case "-o", "-f", "-S", "--merge-output-format", "--cache-dir", "--js-runtimes", "--socket-timeout", "--retries", "--fragment-retries",
			"--color", "--progress-delta", "--remote-components":
			_ = next()
		default:
			if !strings.HasPrefix(a, "-") && c.link == "" && strings.Contains(a, "://") {
				c.link = a
			}
		}
	}
	return c
}

// Main runs the fake with argv (without the program name).
func Main(args []string, stdout, stderr io.Writer) int {
	c := parse(args)
	if lp := os.Getenv("METARUSH_FAKE_YTDLP_LOG"); lp != "" {
		if f, err := os.OpenFile(lp, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			b, _ := json.Marshal(args)
			fmt.Fprintln(f, string(b))
			f.Close()
		}
	}
	switch {
	case c.version:
		fmt.Fprintln(stdout, Version)
		return 0
	case c.update:
		fmt.Fprintf(stdout, "Latest version: stable@%s from yt-dlp/yt-dlp\nyt-dlp is up to date (stable@%s from yt-dlp/yt-dlp)\n", Version, Version)
		return 0
	case c.verbose && c.link == "":
		fmt.Fprintf(stderr, "[debug] Command-line config: %v\n[debug] yt-dlp version stable@%s from yt-dlp/yt-dlp [fake0000] (macos_exe)\n", args, Version)
		fmt.Fprintln(stderr, "[debug] exe versions: ffmpeg 8.0.1")
		fmt.Fprintln(stderr, "[debug] Optional libraries: certifi-2026.07.22, curl_cffi-0.16.3, yt_dlp_ejs-0.8.0")
		fmt.Fprintln(stderr, "[debug] JS runtimes: deno-2.9.7")
		fmt.Fprintln(stderr, "[debug] Plugin directories: none")
		fmt.Fprintln(stderr, "[debug] [youtube] [pot] PO Token Providers: none")
		fmt.Fprintln(stderr, "Usage: yt-dlp [OPTIONS] URL [URL...]\n\nyt-dlp: error: You must provide at least one URL.")
		return 2
	}
	if c.infoFile != "" {
		b, err := os.ReadFile(c.infoFile)
		if err != nil {
			fmt.Fprintf(stderr, "ERROR: Cannot read %s\n", c.infoFile)
			return 1
		}
		var info map[string]any
		_ = json.Unmarshal(b, &info)
		// Like yt-dlp: saved metadata without its formats can't be downloaded.
		if f, _ := info["formats"].([]any); len(f) == 0 {
			fmt.Fprintf(stderr, "ERROR: [youtube] %v: No video formats found!; please report this issue on https://github.com/yt-dlp/yt-dlp/issues?q= , filling out the appropriate issue template. Confirm you are on the latest version using yt-dlp -U\n", info["id"])
			return 1
		}
		link, _ := info["webpage_url"].(string)
		if link == "" {
			link, _ = info["original_url"].(string)
		}
		id, _ := info["id"].(string)
		return download(c, link, id, stdout, stderr)
	}
	if c.link == "" {
		fmt.Fprintln(stderr, "yt-dlp: error: You must provide at least one URL.")
		return 2
	}
	if code := problem(c, stderr); code != 0 {
		return code
	}
	if c.json {
		var v any
		switch {
		case c.flat:
			v = playlist(c.link)
		case strings.Contains(c.link, "multi"):
			v = multiPost(c.link)
		default:
			v = video(c.link, idOf(c.link))
		}
		_ = json.NewEncoder(stdout).Encode(v)
		return 0
	}
	id := idOf(c.link)
	if c.items != "" && strings.Contains(c.link, "multi") {
		id += "-" + c.items
	}
	return download(c, c.link, id, stdout, stderr)
}

func problem(c call, stderr io.Writer) int {
	l := strings.ToLower(c.link)
	id := idOf(c.link)
	switch {
	case strings.Contains(l, "bot-check") && c.cookies == "":
		fmt.Fprintf(stderr, "ERROR: [youtube] %s: Sign in to confirm you’re not a bot. Use --cookies-from-browser or --cookies for the authentication. See  https://github.com/yt-dlp/yt-dlp/wiki/FAQ#how-do-i-pass-cookies-to-yt-dlp  for how to manually pass cookies\n", id)
		return 1
	case strings.Contains(l, "drm"):
		fmt.Fprintf(stderr, "ERROR: [youtube] %s: This video is DRM protected\n", id)
		return 1
	case strings.Contains(l, "private"):
		fmt.Fprintf(stderr, "ERROR: [youtube] %s: Private video. Sign in if you've been granted access to this video\n", id)
		return 1
	case strings.Contains(l, "removed"):
		fmt.Fprintf(stderr, "ERROR: [youtube] %s: Video unavailable. This video has been removed by the uploader\n", id)
		return 1
	case strings.Contains(l, "flaky") && c.json && !seen(c.link):
		fmt.Fprintf(stderr, "ERROR: [youtube] %s: Unable to download webpage: <urlopen error [Errno 8] nodename nor servname provided, or not known> (caused by URLError)\n", id)
		return 1
	}
	return 0
}

// seen remembers a link across runs (the first "flaky" lookup fails).
func seen(link string) bool {
	sum := sha256.Sum256([]byte(link))
	p := filepath.Join(os.TempDir(), "metarush-fake-ytdlp-"+hex.EncodeToString(sum[:8]))
	if _, err := os.Stat(p); err == nil {
		return true
	}
	_ = os.WriteFile(p, []byte("1"), 0o600)
	return false
}

func idOf(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return "fakevideo00"
	}
	if v := u.Query().Get("v"); v != "" {
		return v
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i := len(parts) - 1; i >= 0; i-- {
		if p := parts[i]; p != "" && p != "video" && !strings.HasPrefix(p, "@") {
			return p
		}
	}
	return "fakevideo00"
}

func platformOf(link string) string {
	u, _ := url.Parse(link)
	h := ""
	if u != nil {
		h = strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	}
	switch {
	case strings.Contains(h, "youtu"):
		return "youtube"
	case strings.Contains(h, "tiktok"):
		return "tiktok"
	case h == "x.com" || h == "twitter.com":
		return "x"
	}
	return "web"
}

func video(link, id string) map[string]any {
	p := platformOf(link)
	size := 180000.0
	v := map[string]any{"_type": "video", "id": id, "title": "Fake video " + id, "description": "A test video made by the fake downloader.\nSecond line.",
		"upload_date": "20240312", "timestamp": 1710240000, "duration": 2.0, "width": 320, "height": 180, "fps": 25, "ext": "mp4", "vcodec": "avc1", "acodec": "mp4a.40.2",
		"webpage_url": link, "original_url": link, "extractor": p, "extractor_key": strings.ToUpper(p[:1]) + p[1:], "availability": "public", "live_status": "not_live",
		"format": "137+140", "format_id": "137+140",
		"requested_formats": []map[string]any{{"format_id": "137", "ext": "mp4", "vcodec": "avc1", "acodec": "none", "width": 320, "height": 180, "filesize": size * 0.8},
			{"format_id": "140", "ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2", "filesize": size * 0.2}}}
	switch p {
	case "youtube":
		v["uploader"], v["uploader_id"], v["uploader_url"] = "Fake Channel", "@fakechannel", "https://www.youtube.com/@fakechannel"
		v["channel"], v["channel_id"], v["channel_url"] = "Fake Channel", "UCfake0000000000000000", "https://www.youtube.com/channel/UCfake0000000000000000"
		v["webpage_url"] = "https://www.youtube.com/watch?v=" + id
		if strings.Contains(strings.ToLower(link), "cc") {
			v["license"] = "Creative Commons Attribution license (reuse allowed)"
		}
	case "tiktok":
		v["title"], v["uploader"], v["channel"], v["uploader_url"] = "TikTok video #"+id, "fakecreator", "Fake Creator", "https://www.tiktok.com/@fakecreator"
		delete(v, "requested_formats")
		v["format_id"], v["filesize"] = "h264_540p", size
	case "x":
		v["title"], v["uploader"], v["uploader_id"], v["uploader_url"] = "Fake Account - A post with a video", "Fake Account", "fakeaccount", "https://twitter.com/fakeaccount"
		v["description"] = "A post with a video https://t.co/abc"
		delete(v, "requested_formats")
		v["format_id"], v["filesize"] = "http-832", size
	}
	if strings.Contains(strings.ToLower(link), "livestream") {
		v["is_live"], v["live_status"] = true, "is_live"
	}
	// Every format yt-dlp found, with its address (what --load-info-json downloads from).
	v["formats"] = []map[string]any{
		{"format_id": "18", "ext": "mp4", "vcodec": "avc1", "acodec": "mp4a.40.2", "width": 320, "height": 180, "url": "https://media.example/" + id + "/18.mp4", "filesize": size},
		{"format_id": "137", "ext": "mp4", "vcodec": "avc1", "acodec": "none", "width": 320, "height": 180, "url": "https://media.example/" + id + "/137.mp4", "filesize": size * 0.8},
		{"format_id": "140", "ext": "m4a", "vcodec": "none", "acodec": "mp4a.40.2", "url": "https://media.example/" + id + "/140.m4a", "filesize": size * 0.2},
	}
	return v
}

func multiPost(link string) map[string]any {
	id := idOf(link)
	var entries []map[string]any
	for i := 1; i <= 2; i++ {
		e := video(link, id+"-"+strconv.Itoa(i))
		e["playlist_index"] = i
		entries = append(entries, e)
	}
	return map[string]any{"_type": "playlist", "id": id, "title": "Fake Account - A post with two videos", "uploader": "Fake Account", "uploader_id": "fakeaccount",
		"webpage_url": link, "entries": entries}
}

func playlist(link string) map[string]any {
	var entries []map[string]any
	for i := 1; i <= 3; i++ {
		id := fmt.Sprintf("playlist%03d", i)
		entries = append(entries, map[string]any{"_type": "url", "id": id, "url": "https://www.youtube.com/watch?v=" + id, "title": "Playlist video " + strconv.Itoa(i), "ie_key": "Youtube", "duration": 2})
	}
	return map[string]any{"_type": "playlist", "id": "PLfake", "title": "Fake playlist", "uploader": "Fake Channel", "playlist_count": 3, "webpage_url": link, "entries": entries}
}

func download(c call, link, id string, stdout, stderr io.Writer) int {
	if strings.Contains(strings.ToLower(link), "forbidden") {
		fmt.Fprintln(stderr, "ERROR: unable to download video data: HTTP Error 403: Forbidden")
		return 1
	}
	if c.dir == "" {
		c.dir = "."
	}
	if err := os.MkdirAll(c.dir, 0o755); err != nil {
		fmt.Fprintln(stderr, "ERROR:", err)
		return 1
	}
	out := filepath.Join(c.dir, id+".mp4")
	ff := c.ffmpeg
	if ff == "" {
		ff = "ffmpeg"
	}
	// A short clip whose bytes are unique to the video ID (dedupe sees them as different files).
	sum := sha256.Sum256([]byte(id))
	freq := 300 + int(sum[0])*3
	cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc2=size=320x180:rate=25:duration=2",
		"-f", "lavfi", "-i", fmt.Sprintf("sine=frequency=%d:duration=2", freq), "-metadata", "comment=fake-ytdlp "+id,
		"-c:v", "mpeg4", "-q:v", "6", "-c:a", "aac", "-shortest", out+".tmp.mp4")
	if b, err := cmd.CombinedOutput(); err != nil {
		fmt.Fprintf(stderr, "ERROR: Postprocessing: ffmpeg failed: %v %s\n", err, b)
		return 1
	}
	st, _ := os.Stat(out + ".tmp.mp4")
	total := int64(0)
	if st != nil {
		total = st.Size()
	}
	steps := 4
	delay := 20 * time.Millisecond
	if strings.Contains(strings.ToLower(link), "slow") {
		steps, delay = 15, time.Second // outlasts the dashboard's 4 s activity refresh
	}
	if ms, err := strconv.Atoi(os.Getenv("METARUSH_FAKE_YTDLP_DELAY_MS")); err == nil && ms > 0 {
		delay = time.Duration(ms) * time.Millisecond
	}
	tpl := ""
	post := ""
	for _, t := range c.templates {
		if rest, ok := strings.CutPrefix(t, "download:"); ok {
			tpl = rest
		} else if rest, ok := strings.CutPrefix(t, "postprocess:"); ok {
			post = rest
		}
	}
	fmt.Fprintf(stdout, "[info] %s: Downloading 1 format(s): 18\n[download] Destination: %s\n", id, out)
	for i := 1; i <= steps; i++ {
		done := total * int64(i) / int64(steps)
		status := "downloading"
		if i == steps {
			status = "finished"
		}
		if tpl != "" {
			fmt.Fprintln(stdout, fill(tpl, map[string]string{"progress.status": status, "progress.downloaded_bytes": strconv.FormatInt(done, 10),
				"progress.total_bytes": strconv.FormatInt(total, 10), "progress.total_bytes_estimate": "NA", "progress.speed": "250000.5",
				"progress.eta": strconv.Itoa(steps - i), "info.format_id": "18"}))
		}
		time.Sleep(delay)
	}
	if post != "" {
		fmt.Fprintln(stdout, fill(post, map[string]string{"progress.status": "started", "progress.postprocessor": "MoveFiles"}))
		fmt.Fprintln(stdout, fill(post, map[string]string{"progress.status": "finished", "progress.postprocessor": "MoveFiles"}))
	}
	if err := os.Rename(out+".tmp.mp4", out); err != nil {
		fmt.Fprintln(stderr, "ERROR:", err)
		return 1
	}
	if c.pathFile != "" {
		_ = os.WriteFile(c.pathFile, []byte(out+"\n"), 0o644)
	}
	return 0
}

var fieldRe = regexp.MustCompile(`%\(([a-z_.]+)\)s`)

func fill(tpl string, vals map[string]string) string {
	return fieldRe.ReplaceAllStringFunc(tpl, func(m string) string {
		k := fieldRe.FindStringSubmatch(m)[1]
		if v, ok := vals[k]; ok {
			return v
		}
		return "NA"
	})
}

package linkimport

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/tools"
)

// Format choice: the best picture up to 4K (2160 lines), the higher frame rate,
// SDR before HDR, then H.264 (or HEVC) and AAC where the site offers them at
// that quality — YouTube serves 4K only as VP9/AV1, so 4K stays VP9 there,
// while 1080p and below come as H.264. Separate picture and sound are merged
// into an MP4 with ffmpeg.
const (
	formatSelector = "bv*+ba/b"
	formatSort     = "res:2160,fps,hdr:sdr,+codec:avc:m4a"
	mergeFormat    = "mp4"
)

// maxPostVideos bounds how many videos one post (an X post with several) brings in.
const maxPostVideos = 10

// progressPrefix marks MetaRush's progress lines in yt-dlp's output.
const progressPrefix = "MRPROG"

const progressTemplate = "download:" + progressPrefix + " %(progress.status)s %(progress.downloaded_bytes)s %(progress.total_bytes)s %(progress.total_bytes_estimate)s %(progress.speed)s %(progress.eta)s %(info.format_id)s"

const postTemplate = "postprocess:" + progressPrefix + "-PP %(progress.status)s %(progress.postprocessor)s"

// runner builds yt-dlp command lines from typed options: the link is the only
// free text, and it always follows "--".
type runner struct {
	a       *app.App
	browser string
}

// common are the options every run uses: no user config (MetaRush decides every
// option), no colors, MetaRush's ffmpeg, the JavaScript runtime for YouTube, a
// cache folder inside this installation, and the browser sign-in when the
// person allowed it for this import (yt-dlp reads the cookies itself, in
// memory; nothing is written: there is no --cookies file).
func (r *runner) common() []string {
	args := []string{"--ignore-config", "--color", "never", "--socket-timeout", "30", "--retries", "5", "--fragment-retries", "10",
		"--cache-dir", filepath.Join(r.a.Cfg.Cache.Dir, "yt-dlp")}
	if rt, path := jsRuntime(r.a); rt != "" {
		args = append(args, "--no-js-runtimes", "--js-runtimes", rt+":"+path)
	}
	if p := r.a.Tools.Path(tools.FFmpeg); p != "" {
		args = append(args, "--ffmpeg-location", p)
	}
	if r.browser != "" {
		args = append(args, "--cookies-from-browser", r.browser)
	}
	return args
}

// jsRuntime picks the runtime yt-dlp uses for YouTube's challenges: Deno (its
// default, runs the challenge code with restricted permissions), else the
// Node 22 that MetaRush already needs for Remotion.
func jsRuntime(a *app.App) (string, string) {
	if p := a.Tools.Path(tools.Deno); p != "" {
		return "deno", p
	}
	if p := a.Tools.Path(tools.Node); p != "" {
		if v := a.Tools.Version(context.Background(), tools.Node); majorAtLeast(v, 22) {
			return "node", p
		}
	}
	return "", ""
}

func majorAtLeast(v string, n int) bool {
	maj, _, _ := strings.Cut(strings.TrimPrefix(v, "v"), ".")
	m, err := strconv.Atoi(maj)
	return err == nil && m >= n
}

// lookupArgs reads the metadata of one video (or post) without downloading it.
func (r *runner) lookupArgs(link string) []string {
	args := append(r.common(), "--no-playlist", "--no-progress", "-J", "-f", formatSelector, "-S", formatSort, "--merge-output-format", mergeFormat, "--", link)
	return args
}

// listArgs lists the first n entries of a playlist or channel (flat: no
// per-video lookups).
func (r *runner) listArgs(link string, n int) []string {
	return append(r.common(), "--flat-playlist", "--yes-playlist", "-I", "1:"+strconv.Itoa(n), "--no-progress", "-J", "--", link)
}

// downloadArgs downloads from saved metadata (no second lookup) into dir.
func (r *runner) downloadArgs(infoFile, dir, pathFile string) []string {
	return append(r.common(), "--load-info-json", infoFile, "-f", formatSelector, "-S", formatSort, "--merge-output-format", mergeFormat,
		"-P", dir, "-o", "%(id)s.%(ext)s", "--no-mtime", "--no-playlist", "--newline", "--progress-delta", "0.5",
		"--progress-template", progressTemplate, "--progress-template", postTemplate,
		"--print-to-file", "after_move:filepath", pathFile)
}

// downloadURLArgs downloads by address (when saved metadata went stale).
func (r *runner) downloadURLArgs(link, dir, pathFile string, item int) []string {
	args := append(r.common(), "-f", formatSelector, "-S", formatSort, "--merge-output-format", mergeFormat,
		"-P", dir, "-o", "%(id)s.%(ext)s", "--no-mtime", "--newline", "--progress-delta", "0.5",
		"--progress-template", progressTemplate, "--progress-template", postTemplate,
		"--print-to-file", "after_move:filepath", pathFile)
	if item > 0 {
		args = append(args, "--yes-playlist", "-I", strconv.Itoa(item))
	} else {
		args = append(args, "--no-playlist")
	}
	return append(args, "--", link)
}

// RunError is a failed yt-dlp run: what it said (for Details) and the advice.
type RunError struct {
	Output string
	Advice Advice
}

func (e *RunError) Error() string {
	if e.Output != "" {
		return e.Advice.Message + " (" + firstLine(e.Output, 300) + ")"
	}
	return e.Advice.Message
}

// lookup runs yt-dlp -J. It returns the metadata, or a RunError with advice.
func (r *runner) lookup(ctx context.Context, args []string, platform string) (*Info, error) {
	in, _, err := r.lookupRaw(ctx, args, platform)
	return in, err
}

// lookupRaw also returns yt-dlp's own JSON: a download from saved metadata
// (--load-info-json) needs all of it (formats and their addresses), not the
// fields MetaRush reads.
func (r *runner) lookupRaw(ctx context.Context, args []string, platform string) (*Info, []byte, error) {
	res, err := r.a.Tools.Run(ctx, tools.Spec{Tool: tools.YtDlp, Args: args})
	if err != nil {
		if ctx.Err() != nil {
			return nil, nil, ctx.Err()
		}
		return nil, nil, r.failure(err, res, platform)
	}
	var in Info
	if err := json.Unmarshal(res.Stdout, &in); err != nil {
		return nil, nil, &RunError{Output: firstLine(string(res.Stderr), 300), Advice: AdviceFor(AdviceOutdated, platform, r.browser)}
	}
	return &in, res.Stdout, nil
}

// rawEntries splits a post's JSON into its entries' own JSON (in order).
func rawEntries(raw []byte) []json.RawMessage {
	var p struct {
		Entries []json.RawMessage `json:"entries"`
	}
	_ = json.Unmarshal(raw, &p)
	return p.Entries
}

func (r *runner) failure(err error, res *tools.Result, platform string) error {
	var miss *tools.ErrMissing
	if errors.As(err, &miss) {
		return &RunError{Output: err.Error(), Advice: AdviceFor(AdviceMissing, platform, r.browser)}
	}
	out := ""
	if res != nil {
		out = errorLines(string(res.Stderr))
	}
	if out == "" {
		out = err.Error()
	}
	return &RunError{Output: out, Advice: Explain(out, platform, r.browser)}
}

// Progress is one parsed progress line.
type Progress struct {
	Status   string // downloading | finished | error
	Done     int64
	Total    int64
	Speed    float64
	ETA      float64
	FormatID string
	Post     string // a post-processing step (Merger, FixupM3u8…) while it runs
}

// ParseProgress reads one of MetaRush's progress lines ("" when it isn't one).
func ParseProgress(line string) (Progress, bool) {
	line = strings.TrimSpace(line)
	if rest, ok := strings.CutPrefix(line, progressPrefix+"-PP "); ok {
		f := strings.Fields(rest)
		p := Progress{Status: "postprocessing"}
		if len(f) >= 2 {
			p.Post = f[1]
			if f[0] == "finished" {
				p.Status = "postprocessed"
			}
		}
		return p, true
	}
	rest, ok := strings.CutPrefix(line, progressPrefix+" ")
	if !ok {
		return Progress{}, false
	}
	f := strings.Fields(rest)
	for len(f) < 7 {
		f = append(f, "NA")
	}
	num := func(s string) float64 {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return v
	}
	p := Progress{Status: f[0], Done: int64(num(f[1])), Speed: num(f[4]), ETA: num(f[5]), FormatID: f[6]}
	if t := num(f[2]); t > 0 {
		p.Total = int64(t)
	} else {
		p.Total = int64(num(f[3]))
	}
	if p.FormatID == "NA" {
		p.FormatID = ""
	}
	return p, true
}

// download runs yt-dlp and reports byte progress across the chosen formats
// (picture then sound) as one total. It returns the downloaded file.
func (r *runner) download(ctx context.Context, args []string, platform string, info *Info, pathFile string, onProgress func(done, total int64, rate float64, finishing bool)) (string, error) {
	sizes := map[string]int64{}
	var order []string
	for _, f := range info.RequestedFormats {
		sizes[f.FormatID] = f.Size()
		order = append(order, f.FormatID)
	}
	expected := info.ExpectedSize()
	finished := map[string]int64{}
	current := ""
	onLine := func(line string) {
		p, ok := ParseProgress(line)
		if !ok {
			return
		}
		if p.Status == "postprocessing" || p.Status == "postprocessed" {
			onProgress(expected, expected, 0, true)
			return
		}
		key := p.FormatID
		if key == "" {
			key = "_"
		}
		current = key
		if p.Status == "finished" {
			finished[key] = max(p.Done, p.Total)
		}
		var done int64
		for k, v := range finished {
			if k != current {
				done += v
			}
		}
		if p.Status == "finished" {
			done += finished[key]
		} else {
			done += p.Done
		}
		total := expected
		if total == 0 || done > total {
			// Unknown sizes: count what is known so far.
			var t int64
			for _, id := range order {
				if v, ok := finished[id]; ok {
					t += v
				} else if id == current {
					t += p.Total
				} else {
					t += sizes[id]
				}
			}
			if len(order) == 0 {
				t = p.Total
			}
			total = max(t, done)
		}
		onProgress(done, total, p.Speed, false)
	}
	_ = os.Remove(pathFile)
	res, err := r.a.Tools.Run(ctx, tools.Spec{Tool: tools.YtDlp, Args: args, OnLine: onLine, Background: true})
	if err != nil {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		return "", r.failure(err, res, platform)
	}
	b, err := os.ReadFile(pathFile)
	if err != nil {
		return "", &RunError{Output: "the downloader finished without saying where the file is", Advice: AdviceFor(AdviceFailed, platform, r.browser)}
	}
	var path string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			path = l
		}
	}
	if st, err := os.Stat(path); err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
		return "", &RunError{Output: "the downloaded file is missing: " + filepath.Base(path), Advice: AdviceFor(AdviceFailed, platform, r.browser)}
	}
	return path, nil
}

/* ---------- the downloader on this machine ---------- */

// ImportTools describes the downloader on this machine (doctor, Settings, `metarush import --check-tools`).
type ImportTools struct {
	Installed bool   `json:"installed"`
	Path      string `json:"path,omitempty"`
	Version   string `json:"version,omitempty" jsonschema:"description=yt-dlp's version (a date: 2026.08.19)."`
	// ReleasedOn is the version's date (YYYY-MM-DD); AgeDays how old it is.
	ReleasedOn string `json:"releasedOn,omitempty"`
	AgeDays    int    `json:"ageDays,omitempty"`
	// ManagedBy says how it updates: homebrew (brew upgrade), self (yt-dlp -U),
	// pip (pip install -U), or unknown.
	ManagedBy string `json:"managedBy,omitempty" jsonschema:"enum=homebrew,enum=self,enum=pip,enum=unknown"`
	// JSRuntime is the JavaScript runtime yt-dlp will use for YouTube ("deno 2.9.7").
	JSRuntime string `json:"jsRuntime,omitempty"`
	// ChallengeScripts is the version of yt-dlp's YouTube challenge scripts (yt-dlp-ejs), when bundled.
	ChallengeScripts string `json:"challengeScripts,omitempty"`
	// POTokenProviders are installed PO-token provider plugins (none by default; see D67).
	POTokenProviders []string   `json:"poTokenProviders"`
	FFmpeg           string     `json:"ffmpeg,omitempty"`
	AutoUpdate       bool       `json:"autoUpdate" jsonschema:"description=The daemon updates yt-dlp by itself about once a week (yt-dlp only: never Python\\, Deno or other Homebrew packages)."`
	LastCheck        *time.Time `json:"lastCheck,omitempty" jsonschema:"description=When MetaRush last checked for a newer downloader."`
	LastResult       string     `json:"lastResult,omitempty" jsonschema:"description=What the last update check said\\, in plain words."`
	// UpdateWaiting is a newer yt-dlp the weekly update left for a person,
	// because installing it could also update UpdateOthers from Homebrew.
	UpdateWaiting string   `json:"updateWaiting,omitempty" jsonschema:"description=A newer yt-dlp waiting for a person to run the update (installing it could also update updateOthers)."`
	UpdateOthers  []string `json:"updateOthers,omitempty"`
	// Browser is the browser this machine last used for a sign-in (only a suggestion; every import asks).
	Browser  string   `json:"browser,omitempty"`
	Ready    bool     `json:"ready" jsonschema:"description=Imports can run here."`
	Problems []string `json:"problems" jsonschema:"description=What stops or limits imports here\\, in plain words."`
	Summary  string   `json:"summary"`
}

var (
	debugVersion = regexp.MustCompile(`yt-dlp version (\S+@)?(\d{4}\.\d{2}\.\d{2})(\.\d+)? .*\(([a-z_]+)\)`)
	debugJS      = regexp.MustCompile(`JS runtimes: (.+)`)
	debugEJS     = regexp.MustCompile(`yt_dlp_ejs-([0-9.]+)`)
	debugPOT     = regexp.MustCompile(`PO Token Providers: (.+)`)
)

// Probe inspects the downloader: version, how it is installed, its JavaScript
// runtime and challenge scripts, PO-token plugins. It runs yt-dlp once with no
// link (its debug header, no network) and takes under a second.
func Probe(ctx context.Context, a *app.App) ImportTools {
	st := ImportTools{POTokenProviders: []string{}, Problems: []string{}, AutoUpdate: a.Cfg.Imports.AutoUpdateTools(), Browser: a.Cfg.Imports.Browser}
	if p := a.Tools.Path(tools.FFmpeg); p != "" {
		st.FFmpeg = a.Tools.Version(ctx, tools.FFmpeg)
	}
	if s, ok := loadToolState(a); ok {
		st.LastCheck, st.LastResult = s.LastCheck, s.LastResult
	}
	if !a.Tools.Has(tools.YtDlp) {
		st.Problems = append(st.Problems, "MetaRush's downloader (yt-dlp) isn't on this computer, so links can't be imported here. Add it in Settings → Components; the browser extension works without it.")
		st.Summary = "Not installed"
		return st
	}
	st.Installed, st.Path = true, a.Tools.Path(tools.YtDlp)
	st.Version = a.Tools.Version(ctx, tools.YtDlp)
	args := []string{"-v", "--ignore-config"}
	if rt, path := jsRuntime(a); rt != "" {
		args = append(args, "--no-js-runtimes", "--js-runtimes", rt+":"+path)
	}
	ctx2, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	res, _ := a.Tools.Run(tools.Quiet(ctx2), tools.Spec{Tool: tools.YtDlp, Args: args})
	out := ""
	if res != nil {
		out = string(res.Stderr) + string(res.Stdout)
	}
	if m := debugVersion.FindStringSubmatch(out); m != nil {
		if st.Version == "" || st.Version == "installed" {
			st.Version = m[2] + m[3]
		}
		switch m[4] {
		case "pip":
			st.ManagedBy = "pip"
		case "source":
			st.ManagedBy = "unknown"
		default:
			if strings.HasSuffix(m[4], "_exe") || m[4] == "zip" {
				st.ManagedBy = "self"
			} else {
				st.ManagedBy = "unknown"
			}
		}
	}
	if brewManaged(st.Path) {
		st.ManagedBy = "homebrew"
	}
	if st.ManagedBy == "" {
		st.ManagedBy = "unknown"
	}
	if m := debugJS.FindStringSubmatch(out); m != nil && !strings.Contains(m[1], "none") {
		st.JSRuntime = strings.ReplaceAll(strings.TrimSpace(m[1]), "-", " ")
	}
	if m := debugEJS.FindStringSubmatch(out); m != nil {
		st.ChallengeScripts = m[1]
	}
	if m := debugPOT.FindStringSubmatch(out); m != nil && strings.TrimSpace(m[1]) != "none" {
		for _, p := range strings.Split(m[1], ",") {
			if p = strings.TrimSpace(p); p != "" {
				st.POTokenProviders = append(st.POTokenProviders, p)
			}
		}
	}
	if d, err := time.Parse("2006.01.02", st.Version[:min(10, len(st.Version))]); err == nil {
		st.ReleasedOn = d.Format("2006-01-02")
		st.AgeDays = int(time.Since(d).Hours() / 24)
	}
	if st.JSRuntime == "" {
		st.Problems = append(st.Problems, "YouTube imports need Deno, a small free helper program, and it isn't installed on this computer. Without it they may fail or miss the best quality; the setup check says how to install it.")
	}
	if st.ChallengeScripts == "" && st.JSRuntime != "" {
		st.Problems = append(st.Problems, "The downloader is missing the helper scripts it needs for YouTube. Reinstall the downloader to get them back.")
	}
	if st.FFmpeg == "" {
		st.Problems = append(st.Problems, "The video tool (FFmpeg) isn't on this computer, so downloaded picture and sound can't be put together. Add it in Settings → Components.")
	}
	if st.AgeDays > staleDays {
		st.Problems = append(st.Problems, fmt.Sprintf("The downloader is %d days old, and sites change often. Update it with Update now in Settings > Link imports.", st.AgeDays))
	}
	if s, ok := loadToolState(a); ok && s.Waiting != "" && !versionAtLeast(st.Version, s.Waiting) {
		st.UpdateWaiting, st.UpdateOthers = s.Waiting, s.Others
		st.Problems = append(st.Problems, waitingMessage(s.Waiting, s.Others))
	}
	st.Ready = st.FFmpeg != ""
	parts := []string{"yt-dlp " + st.Version}
	if st.ReleasedOn != "" {
		parts[0] += " (" + ageWords(st.AgeDays) + ")"
	}
	if st.JSRuntime != "" {
		parts = append(parts, "JavaScript: "+st.JSRuntime)
	}
	st.Summary = strings.Join(parts, ", ")
	return st
}

// staleDays is when doctor starts suggesting an update (yt-dlp releases every
// few weeks; a version this old has usually stopped working somewhere).
const staleDays = 120

// versionAtLeast compares dotted versions number by number, ignoring
// Homebrew's revision suffix: yt-dlp says 2026.10.01, Homebrew 2026.10.1_2.
func versionAtLeast(have, want string) bool {
	nums := func(v string) []int {
		v, _, _ = strings.Cut(v, "_")
		var out []int
		for _, p := range strings.Split(v, ".") {
			n, err := strconv.Atoi(p)
			if err != nil {
				return nil
			}
			out = append(out, n)
		}
		return out
	}
	h, w := nums(have), nums(want)
	if h == nil || w == nil {
		return false
	}
	for i := 0; i < max(len(h), len(w)); i++ {
		var a, b int
		if i < len(h) {
			a = h[i]
		}
		if i < len(w) {
			b = w[i]
		}
		if a != b {
			return a > b
		}
	}
	return true
}

func ageWords(days int) string {
	switch {
	case days <= 1:
		return "released today"
	case days < 14:
		return fmt.Sprintf("released %d days ago", days)
	case days < 60:
		return fmt.Sprintf("released %d weeks ago", days/7)
	}
	return fmt.Sprintf("released %d months ago", days/30)
}

// brewManaged reports a yt-dlp that lives in a Homebrew Cellar.
func brewManaged(path string) bool {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		real = path
	}
	return strings.Contains(real, "/Cellar/yt-dlp/")
}

// brewPrefix is the Homebrew prefix a Cellar path belongs to.
func brewPrefix(path string) string {
	real, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ""
	}
	if i := strings.Index(real, "/Cellar/"); i > 0 {
		return real[:i]
	}
	return ""
}

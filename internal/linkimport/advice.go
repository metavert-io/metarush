package linkimport

import (
	"regexp"
	"strings"

	"github.com/metavert-io/metarush/internal/models"
)

// Advice codes (stable: the dashboard, the CLI and skills branch on them).
const (
	AdviceSignIn      = "signin_required" // a bot check, age gate or sign-in wall: the person's browser sign-in may get through
	AdviceMembers     = "members_only"
	AdvicePrivate     = "private"
	AdviceUnavailable = "unavailable"
	AdviceGeo         = "geo_blocked"
	AdviceDRM         = "drm"
	AdviceLive        = "live"
	AdviceRateLimited = "rate_limited"
	AdviceForbidden   = "forbidden"
	AdviceOutdated    = "outdated"
	AdviceJSRuntime   = "js_runtime_missing"
	AdviceEJS         = "challenge_scripts_missing"
	AdviceNetwork     = "network"
	AdviceCookies     = "browser_signin_failed"
	AdviceUnsupported = "unsupported"
	AdviceNoVideo     = "no_video"
	AdviceNoFormats   = "no_formats" // the site offered no downloadable video this time (often passes)
	AdviceFFmpeg      = "ffmpeg_missing"
	AdviceDisk        = "disk_full"
	AdviceMissing     = "downloader_missing"
	AdviceFailed      = "failed"
)

// Advice explains a failed import in plain words, with the next step.
type Advice struct {
	Code    string `json:"code" bson:"code"`
	Message string `json:"message" bson:"message" jsonschema:"description=What happened\\, in plain words."`
	Next    string `json:"next" bson:"next" jsonschema:"description=What to do next\\, in plain words."`
	// Retry: trying again later may work (network trouble, a refused download).
	Retry bool `json:"retry" bson:"retry"`
	// SignIn: importing with the person's browser sign-in may get through
	// ("Use my browser's sign-in"; `metarush import --browser-signin`).
	SignIn bool `json:"signIn" bson:"signIn"`
	// Update: an up-to-date downloader often fixes it (`metarush import --update-tools`).
	Update bool `json:"update" bson:"update"`
	// Extension: the MetaRush browser extension can save it from the person's browser.
	Extension bool `json:"extension" bson:"extension"`
}

type rule struct {
	code string
	re   *regexp.Regexp
}

// Order matters: the first match wins (DRM before "unavailable", the bot check
// before a generic 403, missing pieces before anything about the site).
var rules = []rule{
	{AdviceMissing, regexp.MustCompile(`(?i)yt-dlp is not installed|yt-dlp is not installed or not on PATH`)},
	{AdviceFFmpeg, regexp.MustCompile(`(?i)ffmpeg (is )?not (found|installed)|ffprobe and ffmpeg not found|ffmpeg is not installed or not on PATH`)},
	{AdviceDisk, regexp.MustCompile(`(?i)no space left on device|disk (is )?full|errno 28`)},
	{AdviceDRM, regexp.MustCompile(`(?i)\bDRM\b|copy[- ]protected|widevine|playready|fairplay`)},
	{AdviceLive, regexp.MustCompile(`(?i)this live event will begin|premieres in|\bis (currently )?live\b|live stream (is )?(still )?(in progress|offline)|has not (yet )?started|waiting for (the )?video|is_upcoming`)},
	{AdviceJSRuntime, regexp.MustCompile(`(?i)no supported javascript runtime|javascript runtime (is )?(required|could not be found)`)},
	{AdviceEJS, regexp.MustCompile(`(?i)challenge solver script|yt-dlp-ejs|remote-components ejs|ejs:(npm|github)`)},
	{AdviceCookies, regexp.MustCompile(`(?i)could not find .* cookies database|failed to (decrypt|load) cookies?|cookies? database|unsupported browser specified|keyring|could not copy .*cookie|operation not permitted: .*cookies|cookies from browser`)},
	{AdviceSignIn, regexp.MustCompile(`(?i)not a bot|confirm your age|age[- ]restricted|inappropriate for some users|only available for registered users|login required|log in to|sign in to (view|watch|confirm)|use --cookies|requires authentication|captcha|unable to extract .*cookie`)},
	{AdviceMembers, regexp.MustCompile(`(?i)members[- ]only|join this channel|available to this channel's members|premium (members|subscribers)|requires (a )?(payment|purchase|subscription)|\brental\b`)},
	{AdvicePrivate, regexp.MustCompile(`(?i)private video|video is private|this account is private|protected (tweet|post|account)|has been made private`)},
	{AdviceGeo, regexp.MustCompile(`(?i)not available (in|from) your (country|location)|geo[- ]?restrict|blocked in your country|uploader has not made this video available`)},
	{AdviceRateLimited, regexp.MustCompile(`(?i)HTTP Error 429|too many requests|rate[- ]limit|try again later`)},
	{AdviceUnsupported, regexp.MustCompile(`(?i)unsupported url|is not a valid url|no suitable extractor`)},
	// A challenge the downloader could not solve: an older yt-dlp (checked before "no formats", its usual symptom).
	{AdviceOutdated, regexp.MustCompile(`(?i)signature (extraction|solving) failed|n(sig)? challenge|failed to extract any player response|\bSABR\b`)},
	// YouTube sometimes answers a logged-out request with no formats at all; it often passes.
	{AdviceNoFormats, regexp.MustCompile(`(?i)no video formats found|requested format is not available|only images are available`)},
	{AdviceNoVideo, regexp.MustCompile(`(?i)no video could be found|does not (contain|have) (a )?video|there's no video|no media found`)},
	{AdviceUnavailable, regexp.MustCompile(`(?i)video unavailable|has been removed|no longer available|does not exist|account .*(terminated|suspended)|HTTP Error 404|\bnot found\b|this (tweet|post) is unavailable|video has been deleted|copyright (claim|grounds)`)},
	{AdviceForbidden, regexp.MustCompile(`(?i)HTTP Error 403|403: Forbidden|\bforbidden\b`)},
	{AdviceOutdated, regexp.MustCompile(`(?i)unable to extract|some formats may be missing|please report this issue|yt-dlp -U`)},
	{AdviceNetwork, regexp.MustCompile(`(?i)unable to download webpage|timed out|timeout|connection (refused|reset|aborted)|temporary failure in name resolution|nodename nor servname|name or service not known|network is unreachable|getaddrinfo|\bSSL\b|certificate verify|unable to connect|remote end closed|incomplete read|EOF occurred`)},
}

// Explain turns yt-dlp's error output (or MetaRush's own error) into advice.
// browser is the browser whose sign-in this import used ("" when none).
func Explain(errText, platform, browser string) Advice {
	code := AdviceFailed
	for _, r := range rules {
		if r.re.MatchString(errText) {
			code = r.code
			break
		}
	}
	return adviceFor(code, platform, browser)
}

// AdviceFor is the advice for a code.
func AdviceFor(code, platform, browser string) Advice { return adviceFor(code, platform, browser) }

func adviceFor(code, platform, browser string) Advice {
	site := models.PlatformName(platform)
	if platform == "" || platform == models.PlatformWeb {
		site = "the site"
	}
	signIn := "Choose “Use my browser's sign-in” to download it with your browser's sign-in, or save it with the MetaRush browser extension."
	if browser != "" {
		signIn = "This import already used the sign-in from " + BrowserName(browser) + ". Make sure you're signed in to " + site + " there, or save it with the MetaRush browser extension."
	}
	a := Advice{Code: code}
	switch code {
	case AdviceSignIn:
		a.Message = site + " wants to check that you're not a bot before it lets MetaRush download this video."
		a.Next, a.SignIn, a.Extension = signIn, browser == "", true
	case AdviceMembers:
		a.Message = "Only the channel's members can watch this video."
		a.Next, a.SignIn, a.Extension = "If you're a member, import it with your browser's sign-in, or save it with the MetaRush browser extension while you watch.", browser == "", true
	case AdvicePrivate:
		a.Message = "This video is private."
		a.Next, a.SignIn = "If it was shared with you, import it with your browser's sign-in.", browser == ""
	case AdviceUnavailable:
		a.Message = "This video isn't available any more. It may have been removed or the address may be wrong."
		a.Next = "Check the link in your browser."
	case AdviceGeo:
		a.Message = "The uploader hasn't made this video available in your country."
		a.Next = "It can't be imported from here."
	case AdviceDRM:
		a.Message = "This video is copy-protected (DRM), so MetaRush can't import it. MetaRush never works around copy protection."
		a.Next = "Ask the owner for the file, or use a video that isn't protected."
	case AdviceLive:
		a.Message = "This is a live stream that hasn't finished yet."
		a.Next = "Import it again once the stream has ended."
	case AdviceRateLimited:
		a.Message = site + " is limiting downloads from your network right now."
		a.Next, a.Retry, a.SignIn = "Wait a while (up to an hour) and retry, or import it with your browser's sign-in.", true, browser == ""
	case AdviceForbidden:
		a.Message = site + " refused the download."
		a.Next, a.Retry, a.Update, a.SignIn = "Update the downloader (Update now in Settings > Link imports) and retry. If it keeps happening, import it with your browser's sign-in or the MetaRush browser extension.", true, true, browser == ""
		a.Extension = true
	case AdviceOutdated:
		a.Message = "MetaRush's downloader couldn't read this page. " + site + " changes often, and an older downloader stops working."
		a.Next, a.Retry, a.Update, a.Extension = "Update it (Update now in Settings > Link imports), then retry.", true, true, true
	case AdviceJSRuntime:
		a.Message = "MetaRush's downloader needs Deno, a small free helper program, to read YouTube's pages, and it isn't installed on this computer."
		a.Next, a.Retry = "Add Node.js in Settings → Components (or install Deno), then retry.", true
	case AdviceEJS:
		a.Message = "MetaRush's downloader is missing the helper scripts it needs for YouTube."
		a.Next, a.Retry, a.Update = "Update the downloader (Update now in Settings > Link imports), then retry.", true, true
	case AdviceNetwork:
		a.Message = "MetaRush couldn't reach " + site + "."
		a.Next, a.Retry = "Check the internet connection, then retry.", true
	case AdviceCookies:
		name := BrowserName(browser)
		if name == "" {
			name = "your browser"
		}
		a.Message = "MetaRush couldn't use the sign-in from " + name + "."
		a.Retry = true
		switch browser {
		case "safari":
			a.Next = "macOS keeps Safari's sign-ins private: give the program running MetaRush Full Disk Access (System Settings, Privacy & Security), or choose another browser."
		case "chrome", "brave", "edge", "chromium", "vivaldi", "opera":
			a.Next = "Allow access when macOS asks about “" + name + " Safe Storage” in your keychain, or choose another browser. Quit " + name + " first if it keeps failing."
		default:
			a.Next = "Make sure you're signed in to " + site + " in that browser, or choose another browser."
		}
	case AdviceUnsupported:
		a.Message = "MetaRush can't import from this link."
		a.Next = "Use a link to a single video on YouTube, TikTok or X."
	case AdviceNoVideo:
		a.Message = "There's no video in this post."
		a.Next = "Check that the link points at a post with a video."
	case AdviceNoFormats:
		a.Message = site + " didn't offer the video for download this time."
		a.Next, a.Retry, a.Update, a.SignIn, a.Extension = "This often passes: MetaRush tries again by itself. If it keeps happening, update the downloader (Update now in Settings > Link imports) or import it with your browser's sign-in.", true, true, browser == "", true
	case AdviceFFmpeg:
		a.Message = "The video tool (FFmpeg) isn't on this computer, so the download can't be put together."
		a.Next = "Add FFmpeg in Settings → Components, then retry."
	case AdviceDisk:
		a.Message = "This computer ran out of disk space during the download."
		a.Next, a.Retry = "Free up some space, then retry.", true
	case AdviceMissing:
		a.Message = "Importing videos from links needs the link downloader (yt-dlp), which isn't on this computer yet."
		a.Next = "Add it in Settings → Components (about 40 MB), or save the video with the MetaRush browser extension."
		a.Extension = true
	default:
		a.Code = AdviceFailed
		a.Message = "The download didn't work."
		a.Next, a.Retry, a.Update = "Retry. If it keeps failing, update the downloader (Update now in Settings > Link imports).", true, true
	}
	return a
}

// Browsers whose sign-in yt-dlp can use. Cookies stay in the browser's own
// store: yt-dlp reads them for one run; MetaRush never copies or keeps them.
var Browsers = []string{"chrome", "safari", "firefox", "brave", "edge"}

// BrowserName is a browser as people write it.
func BrowserName(b string) string {
	switch strings.ToLower(b) {
	case "chrome":
		return "Chrome"
	case "safari":
		return "Safari"
	case "firefox":
		return "Firefox"
	case "brave":
		return "Brave"
	case "edge":
		return "Edge"
	}
	return ""
}

// ValidBrowser reports whether b is a browser MetaRush offers for sign-in.
func ValidBrowser(b string) bool { return BrowserName(b) != "" }

// errorLines picks the lines of yt-dlp's output that say what went wrong
// (ERROR lines, else the last lines), for Details.
func errorLines(stderr string) string {
	var errs []string
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "ERROR:") {
			errs = append(errs, l)
		}
	}
	if len(errs) == 0 {
		start := max(0, len(lines)-3)
		errs = lines[start:]
	}
	out := strings.Join(errs, "\n")
	if len(out) > 1200 {
		out = out[len(out)-1200:]
	}
	return out
}

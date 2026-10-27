package linkimport

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/plainwords"
)

// Error lines as yt-dlp 2026.08 prints them (and MetaRush's own).
func TestExplain(t *testing.T) {
	cases := []struct {
		text, code    string
		signIn, retry bool
	}{
		{"ERROR: [youtube] aqz-KE-bpKQ: Sign in to confirm you’re not a bot. Use --cookies-from-browser or --cookies for the authentication. See  https://github.com/yt-dlp/yt-dlp/wiki/FAQ", AdviceSignIn, true, false},
		{"ERROR: [youtube] abc: Sign in to confirm your age. This video may be inappropriate for some users.", AdviceSignIn, true, false},
		{"ERROR: [youtube] abc: This video is available to this channel's members on level: Fan. Join this channel to get access", AdviceMembers, true, false},
		{"ERROR: [youtube] abc: Private video. Sign in if you've been granted access to this video", AdvicePrivate, true, false},
		{"ERROR: [youtube] abc: Video unavailable. This video has been removed by the uploader", AdviceUnavailable, false, false},
		{"ERROR: [youtube] abc: The uploader has not made this video available in your country", AdviceGeo, false, false},
		{"ERROR: [youtube] abc: This video is DRM protected", AdviceDRM, false, false},
		{"ERROR: [youtube] abc: This live event will begin in 3 hours", AdviceLive, false, false},
		{"ERROR: [youtube] abc: This content isn't available, try again later. The current session has been rate-limited by YouTube for up to an hour.", AdviceRateLimited, true, true},
		{"ERROR: unable to download video data: HTTP Error 429: Too Many Requests", AdviceRateLimited, true, true},
		{"ERROR: unable to download video data: HTTP Error 403: Forbidden", AdviceForbidden, true, true},
		{"WARNING: [youtube] abc: n challenge solving failed: Some formats may be missing. Ensure you have a supported JavaScript runtime\nERROR: [youtube] abc: Requested format is not available", AdviceOutdated, false, true},
		{"WARNING: No supported JavaScript runtime could be found. Only deno is enabled by default", AdviceJSRuntime, false, true},
		// Seen for real on 2026-10-18 (a public YouTube short): it passed on the next try.
		{"ERROR: [youtube] tmIYg5NQTrk: No video formats found!; please report this issue on https://github.com/yt-dlp/yt-dlp/issues?q= , filling out the appropriate issue template. Confirm you are on the latest version using yt-dlp -U", AdviceNoFormats, true, true},
		{"ERROR: [youtube] abc: Requested format is not available. Use --list-formats for a list of available formats", AdviceNoFormats, true, true},
		{"ERROR: [youtube] abc: Unable to download webpage: <urlopen error [Errno 8] nodename nor servname provided, or not known>", AdviceNetwork, false, true},
		{"ERROR: [twitter] 12: Unable to download JSON metadata: The read operation timed out", AdviceNetwork, false, true},
		{"ERROR: could not find chrome cookies database in \"/Users/me/Library/Application Support/Google/Chrome\"", AdviceCookies, false, true},
		{"ERROR: Unsupported URL: https://example.com/page", AdviceUnsupported, false, false},
		{"ERROR: [twitter] 1578401165338976258: No video could be found in this tweet", AdviceNoVideo, false, false},
		{"ERROR: ffprobe and ffmpeg not found. Please install or provide the path using --ffmpeg-location", AdviceFFmpeg, false, false},
		{"ERROR: unable to write data: [Errno 28] No space left on device", AdviceDisk, false, true},
		{"yt-dlp is not installed or not on PATH (run ./bootstrap.sh or `metarush doctor`)", AdviceMissing, false, false},
		{"something nobody has seen before", AdviceFailed, false, true},
	}
	for _, c := range cases {
		a := Explain(c.text, "youtube", "")
		if a.Code != c.code || a.SignIn != c.signIn || a.Retry != c.retry {
			t.Errorf("%q\n  got %s signIn=%v retry=%v, want %s signIn=%v retry=%v", c.text, a.Code, a.SignIn, a.Retry, c.code, c.signIn, c.retry)
		}
		if a.Message == "" || a.Next == "" {
			t.Errorf("%s: advice needs a message and a next step: %+v", c.code, a)
		}
		for _, bad := range []string{"ERROR", "yt-dlp:", "Traceback", "--cookies", "HTTP Error"} {
			if strings.Contains(a.Message, bad) {
				t.Errorf("%s: the message is not plain: %q", c.code, a.Message)
			}
		}
	}
}

// Advice is read by people (the activity bar, notifications, the import
// toasts): what happened and what to do, with no commands, flags or paths.
func TestAdviceIsPlain(t *testing.T) {
	codes := []string{AdviceSignIn, AdviceMembers, AdvicePrivate, AdviceUnavailable, AdviceGeo, AdviceDRM, AdviceLive, AdviceRateLimited, AdviceForbidden,
		AdviceOutdated, AdviceJSRuntime, AdviceEJS, AdviceNetwork, AdviceCookies, AdviceUnsupported, AdviceNoVideo, AdviceNoFormats, AdviceFFmpeg, AdviceDisk,
		AdviceMissing, AdviceFailed}
	for _, code := range codes {
		for _, browser := range []string{"", "chrome", "safari", "firefox"} {
			a := AdviceFor(code, "youtube", browser)
			for _, text := range []string{a.Message, a.Next} {
				if fs := plainwords.Check(text); len(fs) > 0 {
					t.Errorf("%s (%s): %q: %v", code, browser, text, fs)
				}
			}
		}
	}
}

func TestAdviceAfterTheBrowserSignIn(t *testing.T) {
	a := Explain("Sign in to confirm you’re not a bot", "youtube", "safari")
	if a.SignIn || !strings.Contains(a.Next, "Safari") || !a.Extension {
		t.Fatalf("after a sign-in attempt the advice points at the browser and the extension: %+v", a)
	}
	c := AdviceFor(AdviceCookies, "youtube", "safari")
	if !strings.Contains(c.Next, "Full Disk Access") {
		t.Fatalf("Safari's cookies need Full Disk Access: %q", c.Next)
	}
	c = AdviceFor(AdviceCookies, "youtube", "chrome")
	if !strings.Contains(c.Next, "Chrome Safe Storage") {
		t.Fatalf("Chrome's cookies need the keychain prompt: %q", c.Next)
	}
}

func TestDRMIsNeverRetried(t *testing.T) {
	a := AdviceFor(AdviceDRM, "youtube", "")
	if a.Retry || a.SignIn || a.Update || a.Extension {
		t.Fatalf("copy-protected video: no retry, no sign-in, no workaround: %+v", a)
	}
	if Retryable(&Failure{Advice: a}) {
		t.Fatal("DRM must not be retried automatically")
	}
	if !Retryable(&Failure{Advice: AdviceFor(AdviceNetwork, "youtube", "")}) || Retryable(&Failure{Advice: AdviceFor(AdviceSignIn, "youtube", "")}) {
		t.Fatal("only network trouble and unknown failures retry by themselves")
	}
}

func TestBrowsers(t *testing.T) {
	for _, b := range Browsers {
		if !ValidBrowser(b) || BrowserName(b) == "" {
			t.Errorf("%s", b)
		}
	}
	if ValidBrowser("opera") || ValidBrowser("") || ValidBrowser("chrome; rm -rf /") {
		t.Fatal("only the offered browsers are valid")
	}
}

func TestParseProgress(t *testing.T) {
	p, ok := ParseProgress("MRPROG downloading 523264 5076682 NA 933014.9586791124 4 h264_540p_1895168-1")
	if !ok || p.Done != 523264 || p.Total != 5076682 || p.Speed < 933014 || p.ETA != 4 || p.FormatID != "h264_540p_1895168-1" || p.Status != "downloading" {
		t.Fatalf("%+v", p)
	}
	p, ok = ParseProgress("MRPROG downloading 1024 NA 9000000 NA NA 315")
	if !ok || p.Total != 9000000 {
		t.Fatalf("estimate as total: %+v", p)
	}
	p, ok = ParseProgress("MRPROG-PP started Merger")
	if !ok || p.Status != "postprocessing" || p.Post != "Merger" {
		t.Fatalf("post-processing: %+v", p)
	}
	if _, ok := ParseProgress("[download]  12.3% of 5.00MiB"); ok {
		t.Fatal("other lines are not progress")
	}
}

func TestErrorLines(t *testing.T) {
	got := errorLines("[youtube] x: Downloading webpage\nWARNING: something\nERROR: [youtube] x: Private video\n")
	if got != "ERROR: [youtube] x: Private video" {
		t.Fatalf("%q", got)
	}
}

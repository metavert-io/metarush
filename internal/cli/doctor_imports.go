package cli

import (
	"context"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/browserext"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/tools"
)

// importChecks reports the optional link-import pieces: yt-dlp (presence, age,
// how it updates), the JavaScript runtime YouTube needs, the challenge scripts,
// PO-token plugins, automatic updates and paired browser extensions.
func importChecks(ctx context.Context, cfg *config.Config, ts *tools.Set, add addCheck) {
	st := linkimport.Probe(ctx, &app.App{Cfg: cfg, Tools: ts})
	if !st.Installed {
		add("link imports", "yt-dlp", "info", "not installed (optional: imports from YouTube, TikTok and X links)", "metarush components add link-import; the browser extension works without it",
			"Importing from YouTube, TikTok and X links isn't set up (optional)", "Add the link downloader in Settings → Components; the browser extension works without it.")
	} else {
		how := map[string]string{"homebrew": "Homebrew", "self": "updates itself", "pip": "pip: update it with pip", "unknown": "unknown install: update it yourself"}[st.ManagedBy]
		if st.AgeDays > 120 {
			add("link imports", "yt-dlp", "warn", fmt.Sprintf("%s, %s", st.Summary, how), "metarush import --update-tools",
				"The link downloader is several months old, so imports from links may fail", "Update the link downloader.")
		} else {
			add("link imports", "yt-dlp", "ok", fmt.Sprintf("%s, %s", st.Summary, how), "", "Ready to import videos from YouTube, TikTok and X links")
		}
		if st.JSRuntime == "" {
			add("link imports", "JavaScript runtime", "warn", "none: YouTube imports may fail or miss the best quality", "metarush components add node (or brew install deno)",
				"YouTube imports may fail or miss the best quality: a helper program (Deno or Node.js) is missing", "Add Node.js in Settings → Components.")
		} else {
			add("link imports", "JavaScript runtime", "ok", st.JSRuntime+" (YouTube's signature challenges)", "")
		}
		if st.ChallengeScripts == "" {
			add("link imports", "challenge scripts", "warn", "yt-dlp has no YouTube challenge scripts (yt-dlp-ejs)", `brew reinstall yt-dlp, or pip install -U "yt-dlp[default]"`,
				"The link downloader is missing the parts it needs for YouTube, so YouTube imports may fail", "Reinstall the link downloader (yt-dlp).")
		} else {
			add("link imports", "challenge scripts", "ok", "yt-dlp-ejs "+st.ChallengeScripts+" (bundled)", "")
		}
		if len(st.POTokenProviders) > 0 {
			add("link imports", "PO tokens", "info", "provider plugins: "+strings.Join(st.POTokenProviders, ", "), "")
		} else {
			add("link imports", "PO tokens", "info", "no provider plugin (not needed for public videos today; see docs/installation.md)", "")
		}
		auto := "on: yt-dlp only, about once a week while no import runs (never Python, Deno or other Homebrew packages)"
		if !st.AutoUpdate {
			auto = "off"
		}
		if st.LastCheck != nil {
			auto += ", last check " + st.LastCheck.Local().Format("2 Jan 15:04") + ": " + st.LastResult
		}
		if st.AutoUpdate {
			add("link imports", "automatic updates", "info", auto, "")
		} else {
			add("link imports", "automatic updates", "info", auto, "", "The link downloader doesn't update itself (automatic updates are off)")
		}
		if st.UpdateWaiting != "" {
			add("link imports", "downloader update", "warn", fmt.Sprintf("yt-dlp %s is ready; installing it could also update %s from Homebrew, so it waits for you",
				st.UpdateWaiting, strings.Join(st.UpdateOthers, ", ")), "metarush import --update-tools",
				"A newer link downloader is ready, but installing it could also update other programs on this computer, so it waits for you", "Update it when you're ready.")
		}
	}
	if n := len(browserext.For(cfg.StateDir()).List()); n > 0 {
		add("link imports", "browser extension", "info", fmt.Sprintf("%d browser(s) paired (metarush extension list)", n), "",
			fmt.Sprintf("The browser extension is paired with %s", plural(n, "browser")))
	} else {
		add("link imports", "browser extension", "info", "no browser paired (optional: metarush extension pair)", "")
	}
}

package api

import (
	"context"
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/media"
)

// The guest page (D502): one page of its own — no part of the dashboard —
// with its style and script inline, allowed by hash, and a policy that lets
// it reach only the guest address and, for video and stills, the workspace's
// bucket. It signs in (a link's button, or the organization's account),
// lists what was shared, plays a version and, for a commenter, keeps their
// comments. Plain words; no browser dialogs.

//go:embed guestpage/page.html guestpage/page.css guestpage/page.js
var guestFiles embed.FS

func guestFile(name string) string {
	b, err := guestFiles.ReadFile("guestpage/" + name)
	if err != nil {
		panic(err) // embedded at build time
	}
	return string(b)
}

var guestPages sync.Map // bucket origin → page

type builtPage struct {
	html []byte
	csp  string
}

// guestPageFor is the page and its policy for a media store's address.
func guestPageFor(bucket string) ([]byte, string) {
	if p, ok := guestPages.Load(bucket); ok {
		bp := p.(builtPage)
		return bp.html, bp.csp
	}
	css, js := guestFile("page.css"), guestFile("page.js")
	media := "'self'"
	if bucket != "" {
		media += " " + bucket
	}
	csp := "default-src 'none'; script-src " + hashOf(js) + "; style-src " + hashOf(css) + "; connect-src 'self'; img-src " + media + " data:; media-src " + media +
		"; base-uri 'none'; form-action 'none'; frame-ancestors 'none'; object-src 'none'"
	page := strings.NewReplacer("{{CSS}}", css, "{{JS}}", js).Replace(guestFile("page.html"))
	bp := builtPage{html: []byte(page), csp: csp}
	guestPages.Store(bucket, bp)
	return bp.html, bp.csp
}

func hashOf(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// originOf is an address's scheme and host.
func originOf(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// isLocalStore reports media kept on this computer (a folder store).
func isLocalStore(a *app.App) bool {
	_, ok := a.Media.(*media.FS)
	return ok
}

// localPathOf is where a stored file is when the media is kept on this
// computer (a folder store).
func localPathOf(a *app.App, key string) (string, bool) {
	fsStore, ok := a.Media.(*media.FS)
	if !ok {
		return "", false
	}
	p, err := fsStore.LocalPath(key)
	if err != nil {
		return "", false
	}
	return p, true
}

// serveFileUntil sends a file with ranges (http.ServeContent) and stops when
// ctx ends.
func serveFileUntil(ctx context.Context, w http.ResponseWriter, r *http.Request, p string) {
	f, err := os.Open(p)
	if err != nil {
		guestError(w, http.StatusNotFound, notSharedErr())
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.IsDir() {
		guestError(w, http.StatusNotFound, notSharedErr())
		return
	}
	http.ServeContent(w, r, "", st.ModTime(), stopper{ctx: ctx, f: f})
}

package reviewlinks

import (
	"crypto/sha256"
	"embed"
	"encoding/base64"
	"encoding/json"
	"html"
	"net/url"
	"strings"
)

//go:embed page/page.html page/page.css page/page.js
var pageFiles embed.FS

// pageData is everything the page knows: signed links to its own objects and
// the words the person chose to show. Nothing else of the workspace.
type pageData struct {
	V        int        `json:"v"`
	Page     string     `json:"page"` // random: names what the page keeps in the reviewer's browser
	Title    string     `json:"title"`
	From     string     `json:"from,omitempty"`
	For      string     `json:"for,omitempty"`
	Message  string     `json:"message,omitempty"`
	Draft    bool       `json:"draft,omitempty"`
	Expires  string     `json:"expires"`
	Video    pageVideo  `json:"video"`
	Poster   string     `json:"poster,omitempty"`
	Download *pageFile  `json:"download,omitempty"`
	Words    []pageTurn `json:"words,omitempty"`
	Seats    []pageSeat `json:"seats"`
	SeatSize int        `json:"seatSize"`
	Limits   pageLimits `json:"limits"`
}

type pageVideo struct {
	URL      string  `json:"url"`
	Type     string  `json:"type"`
	Duration float64 `json:"duration"`
}

type pageFile struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

// pageTurn is one speaker's turn: their lines, each with its times.
type pageTurn struct {
	Speaker string     `json:"speaker,omitempty"`
	Lines   []pageLine `json:"lines"`
}

type pageLine struct {
	From float64 `json:"from"`
	To   float64 `json:"to"`
	Text string  `json:"text"`
}

// pageSeat is one comment slot: a signed read and a signed write of
// exactly SeatSize bytes, with the headers the write must carry.
type pageSeat struct {
	Get     string            `json:"get"`
	Put     string            `json:"put"`
	Headers map[string]string `json:"headers"`
}

type pageLimits struct {
	PerReviewer int `json:"perReviewer"`
	Text        int `json:"text"`
	Name        int `json:"name"`
}

func readPageFile(name string) string {
	b, err := pageFiles.ReadFile("page/" + name)
	if err != nil {
		panic(err) // embedded at build time
	}
	return string(b)
}

// buildPage writes the page: the data as JSON in a script block of its own
// (Go's encoder escapes <, > and &, so nothing in it can end the block), the
// style and script inline, and a Content-Security-Policy that lets it reach
// only its own bucket's address and run only its own script.
func buildPage(d *pageData, origins []string) ([]byte, error) {
	data, err := json.Marshal(d)
	if err != nil {
		return nil, err
	}
	css, js := readPageFile("page.css"), readPageFile("page.js")
	csp := policy(origins, css, js)
	attr := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;") // the policy sits in a double-quoted attribute
	r := strings.NewReplacer("{{CSP}}", attr.Replace(csp), "{{TITLE}}", html.EscapeString(d.Title), "{{CSS}}", css, "{{JS}}", js, "{{DATA}}", string(data))
	return []byte(r.Replace(readPageFile("page.html"))), nil
}

// policy is the page's Content-Security-Policy: nothing by default; its own
// script and style by hash; video, pictures and requests only to the
// bucket's address; no forms, no base, no plugins.
func policy(origins []string, css, js string) string {
	src := strings.Join(origins, " ")
	return "default-src 'none'; script-src " + hash(js) + "; style-src " + hash(css) + "; img-src " + src + " data:; media-src " + src +
		"; connect-src " + src + "; base-uri 'none'; form-action 'none'; object-src 'none'"
}

func hash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}

// originOf is a link's scheme and host (the bucket's address).
func originOf(link string) string {
	u, err := url.Parse(link)
	if err != nil || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

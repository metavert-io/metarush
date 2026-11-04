// Package macapp is the release side of MetaRush.app (D470–D479): the version
// Sparkle compares (BundleVersion) and the appcast, the feed Sparkle reads,
// with a release's archive, its EdDSA signature and what's new in it from
// CHANGELOG.md (Feed, Notes). scripts/mac-app.sh runs it through
// scripts/macapp; the update test builds its feed with it for a real Sparkle
// on a loopback server (update_test.go).
package macapp

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/version"
)

// BundleVersion is CFBundleVersion for a MetaRush version: what Sparkle's
// standard comparator orders updates by. MetaRush's own order (version.Compare)
// has a build N commits past a release newer than the release, which Sparkle
// would read the other way round (to it a word after the numbers is a
// pre-release: "0.2.0-dev.3" < "0.2.0"), so the count becomes a fourth number:
//
//	0.2.0 → 0.2.0      0.2.0-dev.3 → 0.2.0.3      0.2.0-dev (count not known) → 0.2.0.0
//	0.3.0-rc1 → 0.3.0rc1 (older than 0.3.0 to Sparkle too)      0.3.0-rc1-dev.2 → 0.3.0rc1.2
func BundleVersion(v string) (string, error) {
	p, ok := version.Parse(v)
	if !ok {
		return "", fmt.Errorf("%q isn't a MetaRush version", v)
	}
	s := strconv.Itoa(p.Major) + "." + strconv.Itoa(p.Minor) + "." + strconv.Itoa(p.Patch)
	if p.Pre != "" {
		pre := p.Pre
		if pre[0] >= '0' && pre[0] <= '9' {
			pre = "pre" + pre // a word first, so Sparkle sees a pre-release
		}
		s += pre
	}
	if p.Dev {
		s += "." + strconv.Itoa(max(p.DevCount, 0))
	}
	return s, nil
}

// Item is one release of MetaRush.app in the feed.
type Item struct {
	// Title is what Sparkle's window calls it ("MetaRush 0.16.0").
	Title string
	// ShortVersion is CFBundleShortVersionString: what people are shown.
	ShortVersion string
	// Version is CFBundleVersion: what Sparkle compares (BundleVersion).
	Version string
	// MinimumSystem is LSMinimumSystemVersion ("13.0").
	MinimumSystem string
	Published     time.Time
	// URL is the archive's address; Length its size in bytes and
	// EdSignature its EdDSA signature (base64), from Sparkle's sign_update.
	URL         string
	Length      int64
	EdSignature string
	// NotesHTML is shown in Sparkle's update window (Notes).
	NotesHTML string
}

// Feed is an appcast: an RSS feed of releases with Sparkle's elements.
type Feed struct {
	Title string
	Link  string // the project's page
	Items []Item
}

const sparkleNS = "http://www.andymatuschak.org/xml-namespaces/sparkle"

type rss struct {
	XMLName xml.Name `xml:"rss"`
	Version string   `xml:"version,attr"`
	Sparkle string   `xml:"xmlns:sparkle,attr"`
	Channel channel  `xml:"channel"`
}

type channel struct {
	Title string    `xml:"title"`
	Link  string    `xml:"link,omitempty"`
	Items []rssItem `xml:"item"`
}

type rssItem struct {
	Title         string    `xml:"title"`
	PubDate       string    `xml:"pubDate"`
	Version       string    `xml:"sparkle:version"`
	ShortVersion  string    `xml:"sparkle:shortVersionString"`
	MinimumSystem string    `xml:"sparkle:minimumSystemVersion,omitempty"`
	Description   *cdata    `xml:"description,omitempty"`
	Enclosure     enclosure `xml:"enclosure"`
}

type cdata struct {
	Text string `xml:",cdata"`
}

type enclosure struct {
	URL         string `xml:"url,attr"`
	Length      int64  `xml:"length,attr"`
	Type        string `xml:"type,attr"`
	EdSignature string `xml:"sparkle:edSignature,attr"`
}

// XML writes the feed. Every item needs its archive's address, size and
// signature: Sparkle refuses an update it can't check.
func (f Feed) XML() ([]byte, error) {
	doc := rss{Version: "2.0", Sparkle: sparkleNS, Channel: channel{Title: f.Title, Link: f.Link}}
	for _, it := range f.Items {
		switch {
		case it.URL == "" || it.Length <= 0:
			return nil, fmt.Errorf("release %s: the archive's address and size are needed", it.ShortVersion)
		case it.EdSignature == "":
			return nil, fmt.Errorf("release %s: the archive's EdDSA signature is needed (Sparkle refuses an unsigned update)", it.ShortVersion)
		case it.Version == "" || it.ShortVersion == "":
			return nil, fmt.Errorf("a release without its versions")
		}
		ri := rssItem{Title: it.Title, PubDate: it.Published.UTC().Format(time.RFC1123Z), Version: it.Version, ShortVersion: it.ShortVersion,
			MinimumSystem: it.MinimumSystem, Enclosure: enclosure{URL: it.URL, Length: it.Length, Type: "application/octet-stream", EdSignature: it.EdSignature}}
		if it.NotesHTML != "" {
			ri.Description = &cdata{Text: it.NotesHTML}
		}
		doc.Channel.Items = append(doc.Channel.Items, ri)
	}
	b, err := xml.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, err
	}
	return append([]byte(xml.Header), append(b, '\n')...), nil
}

// Notes is what's new in a build as HTML for Sparkle's update window: the
// plain headlines of its CHANGELOG.md sections (a build past a release also
// gets the changes not released yet, as Settings → General shows them).
// Technical entries without a plain headline are counted, not shown.
func Notes(changelog string, v string) string {
	sections := version.ParseChangelog(changelog).WhatsNew(version.Describe(v, "", ""))
	var b strings.Builder
	for _, s := range sections {
		title := "What’s new in " + s.Release
		if s.Release == "" {
			title = "Coming in the next version"
		}
		fmt.Fprintf(&b, "<h3>%s</h3>\n<ul>\n", html.EscapeString(title))
		technical := 0
		for _, e := range s.Entries {
			if e.Headline == "" {
				technical++
				continue
			}
			fmt.Fprintf(&b, "<li>%s</li>\n", html.EscapeString(e.Headline))
		}
		if technical > 0 {
			fmt.Fprintf(&b, "<li>and %d smaller %s</li>\n", technical, map[bool]string{true: "change", false: "changes"}[technical == 1])
		}
		b.WriteString("</ul>\n")
	}
	return b.String()
}

// A signed feed (SURequireSignedFeed, D471) ends with the EdDSA signature of
// the bytes before it, in the comment Sparkle's sign_update writes:
//
//	<!-- sparkle-signatures:
//	edSignature: <base64>
//	length: <how many bytes are signed>
//	-->
//
// scripts/mac-app.sh appcast signs with sign_update (the release Mac's
// keychain); SignFeed is the same for the update test's key made at run time,
// and VerifyFeed and VerifyArchive check a release against the key its app
// carries before anything is published.
const feedSignaturesStart = "<!-- sparkle-signatures:"

var feedSignatures = regexp.MustCompile(`(?s)<!-- sparkle-signatures:\s*\nedSignature: ([A-Za-z0-9+/=]+)\s*\nlength: (\d+)\s*\n-->\s*$`)

// SignFeed returns the feed with its signature at the end (an earlier
// signature is replaced).
func SignFeed(feed []byte, key ed25519.PrivateKey) []byte {
	if i := bytes.LastIndex(feed, []byte(feedSignaturesStart)); i >= 0 {
		feed = feed[:i]
	}
	sig := base64.StdEncoding.EncodeToString(ed25519.Sign(key, feed))
	out := append([]byte(nil), feed...)
	return append(out, fmt.Sprintf("%s\nedSignature: %s\nlength: %d\n-->\n", feedSignaturesStart, sig, len(feed))...)
}

// VerifyFeed checks a signed feed against a public key (base64, as
// Info.plist's SUPublicEDKey holds it).
func VerifyFeed(signed []byte, publicKey string) error {
	pub, err := publicKeyOf(publicKey)
	if err != nil {
		return err
	}
	m := feedSignatures.FindSubmatch(signed)
	if m == nil {
		return errors.New("the feed isn't signed")
	}
	n, err := strconv.Atoi(string(m[2]))
	if err != nil || n <= 0 || n > len(signed) {
		return errors.New("the feed's signature names a length it doesn't have")
	}
	sig, err := base64.StdEncoding.DecodeString(string(m[1]))
	if err != nil || !ed25519.Verify(pub, signed[:n], sig) {
		return errors.New("the feed's signature doesn't match the app's update key")
	}
	return nil
}

// VerifyArchive checks an archive's EdDSA signature (sign_update's) against a
// public key.
func VerifyArchive(archive []byte, signature, publicKey string) error {
	pub, err := publicKeyOf(publicKey)
	if err != nil {
		return err
	}
	sig, err := base64.StdEncoding.DecodeString(strings.TrimSpace(signature))
	if err != nil || !ed25519.Verify(pub, archive, sig) {
		return errors.New("the archive's signature doesn't match the app's update key")
	}
	return nil
}

// CheckPublicKey: an update key as Info.plist's SUPublicEDKey holds it (an
// EdDSA public key: 32 bytes, base64).
func CheckPublicKey(s string) error {
	_, err := publicKeyOf(s)
	return err
}

func publicKeyOf(s string) (ed25519.PublicKey, error) {
	b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(s))
	if err != nil || len(b) != ed25519.PublicKeySize {
		return nil, errors.New("not an EdDSA public key (32 bytes, base64)")
	}
	return ed25519.PublicKey(b), nil
}

package macapp

import (
	"encoding/xml"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/metavert-io/metarush/internal/version"
)

func TestBundleVersion(t *testing.T) {
	for in, want := range map[string]string{
		"0.15.0":           "0.15.0",
		"0.15.0-dev.25":    "0.15.0.25",
		"0.15.0-dev":       "0.15.0.0",
		"v1.2.3":           "1.2.3",
		"0.16.0-rc1":       "0.16.0rc1",
		"0.16.0-rc.2":      "0.16.0rc.2",
		"0.16.0-rc1-dev.4": "0.16.0rc1.4",
		"0.16.0-1":         "0.16.0pre1",
	} {
		got, err := BundleVersion(in)
		if err != nil || got != want {
			t.Errorf("BundleVersion(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "1.2", "next", "1.2.3-"} {
		if _, err := BundleVersion(bad); err == nil {
			t.Errorf("BundleVersion(%q) took it", bad)
		}
	}
}

// Sparkle orders bundle versions the way MetaRush orders its versions, so an
// update is offered exactly when it is newer: a build past a release is newer
// than the release, a release newer than its pre-releases.
// orderingCases are versions in MetaRush's order, oldest first.
var orderingCases = []string{"0.14.2", "0.15.0-rc1", "0.15.0-rc1-dev.3", "0.15.0-rc2", "0.15.0", "0.15.0-dev.1", "0.15.0-dev.25", "0.15.1",
	"0.16.0-beta.1", "0.16.0-rc.1", "0.16.0-rc.1-dev.2", "0.16.0-rc.2", "0.16.0", "0.16.0-dev.3", "1.0.0"}

func TestBundleVersionsOrderLikeMetaRush(t *testing.T) {
	vs := orderingCases
	for i, a := range vs {
		for _, b := range vs[i+1:] {
			want, ok := version.Compare(a, b)
			if !ok {
				continue
			}
			ba, _ := BundleVersion(a)
			bb, _ := BundleVersion(b)
			if got := sparkleCompare(ba, bb); got != want {
				t.Errorf("%s vs %s: MetaRush says %d, Sparkle would say %d (%s vs %s)", a, b, want, got, ba, bb)
			}
		}
	}
	sorted := append([]string(nil), vs...)
	sort.SliceStable(sorted, func(i, j int) bool {
		bi, _ := BundleVersion(sorted[i])
		bj, _ := BundleVersion(sorted[j])
		return sparkleCompare(bi, bj) < 0
	})
	if strings.Join(sorted, " ") != strings.Join(vs, " ") {
		t.Errorf("Sparkle's order:\n %v\nMetaRush's:\n %v", sorted, vs)
	}
}

// sparkleCompare is Sparkle 2's standard version comparator
// (SUStandardVersionComparator), the rule MetaRush.app's updates are compared
// by: versions split into runs of digits, of other characters and single
// periods; numbers compare as numbers, words as text, a number beats a word;
// when one runs out, a word next in the longer one makes it older (1.0b1 <
// 1.0), a number or period newer (1.0.1 > 1.0). The update test checks this
// port against the real Sparkle (`UpdateHarness compare`).
func sparkleCompare(a, b string) int {
	pa, pb := sparkleParts(a), sparkleParts(b)
	n := min(len(pa), len(pb))
	for i := 0; i < n; i++ {
		ta, tb := sparkleType(pa[i]), sparkleType(pb[i])
		switch {
		case ta == tb && ta == partNumber:
			x, y := atoiBig(pa[i]), atoiBig(pb[i])
			if x != y {
				return map[bool]int{true: 1, false: -1}[x > y]
			}
		case ta == tb && ta == partWord:
			if c := strings.Compare(pa[i], pb[i]); c != 0 {
				return c
			}
		case ta == tb:
		case ta != partWord && tb == partWord:
			return 1
		case ta == partWord && tb != partWord:
			return -1
		case ta == partNumber:
			return 1
		default:
			return -1
		}
	}
	if len(pa) == len(pb) {
		return 0
	}
	longer, sign := pa, 1
	if len(pb) > len(pa) {
		longer, sign = pb, -1
	}
	if sparkleType(longer[n]) == partWord {
		return -sign
	}
	return sign
}

const (
	partNumber = iota
	partPeriod
	partWord
)

func sparkleType(s string) int {
	r := rune(s[0])
	switch {
	case unicode.IsDigit(r):
		return partNumber
	case r == '.' || unicode.IsSpace(r):
		return partPeriod
	}
	return partWord
}

func sparkleParts(v string) []string {
	var out []string
	cur := ""
	for _, r := range v {
		s := string(r)
		if cur != "" && (sparkleType(cur) != sparkleType(s) || sparkleType(cur) == partPeriod) {
			out = append(out, cur)
			cur = ""
		}
		cur += s
	}
	if cur != "" {
		out = append(out, cur)
	}
	return out
}

func atoiBig(s string) int64 {
	var n int64
	for _, r := range s {
		n = n*10 + int64(r-'0')
	}
	return n
}

func TestSparkleComparatorPort(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want int
	}{{"1.0", "1.0", 0}, {"1.0.1", "1.0", 1}, {"1.0b1", "1.0", -1}, {"1.0b2", "1.0b10", -1}, {"2", "10", -1}, {"1.0a", "1.0b", -1}, {"1.0.0", "1.0", 1}} {
		if got := sparkleCompare(c.a, c.b); got != c.want {
			t.Errorf("%s vs %s = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

func TestFeedXML(t *testing.T) {
	when := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	f := Feed{Title: "MetaRush", Link: "https://example.com/metarush", Items: []Item{{Title: "MetaRush 0.16.0", ShortVersion: "0.16.0", Version: "0.16.0",
		MinimumSystem: "13.0", Published: when, URL: "https://example.com/MetaRush-0.16.0.zip", Length: 1234, EdSignature: "c2lnbmF0dXJl",
		NotesHTML: "<ul><li>Faster & simpler</li><li>a ]]> inside</li></ul>"}}}
	b, err := f.XML()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Channel struct {
			Items []struct {
				Version      string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle version"`
				ShortVersion string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle shortVersionString"`
				MinSystem    string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle minimumSystemVersion"`
				PubDate      string `xml:"pubDate"`
				Description  string `xml:"description"`
				Enclosure    struct {
					URL    string `xml:"url,attr"`
					Length int64  `xml:"length,attr"`
					Sig    string `xml:"http://www.andymatuschak.org/xml-namespaces/sparkle edSignature,attr"`
				} `xml:"enclosure"`
			} `xml:"item"`
		} `xml:"channel"`
	}
	if err := xml.Unmarshal(b, &doc); err != nil {
		t.Fatalf("%v\n%s", err, b)
	}
	if len(doc.Channel.Items) != 1 {
		t.Fatalf("items: %s", b)
	}
	it := doc.Channel.Items[0]
	if it.Version != "0.16.0" || it.ShortVersion != "0.16.0" || it.MinSystem != "13.0" || it.Enclosure.URL != f.Items[0].URL ||
		it.Enclosure.Length != 1234 || it.Enclosure.Sig != "c2lnbmF0dXJl" || it.PubDate != "Tue, 03 Nov 2026 12:00:00 +0000" {
		t.Errorf("item read back: %+v\n%s", it, b)
	}
	if it.Description != f.Items[0].NotesHTML {
		t.Errorf("notes read back: %q", it.Description)
	}
	f.Items[0].EdSignature = ""
	if _, err := f.XML(); err == nil || !strings.Contains(err.Error(), "signature") {
		t.Errorf("an unsigned release: %v", err)
	}
}

func TestNotes(t *testing.T) {
	changelog := `# Changelog

## Unreleased
- **Updates install themselves**. MetaRush.app updates with Sparkle.
- ` + "`metarush setup questions`" + ` says what setup asks.

## 0.15.0 — 1 Nov 2026
- **Faces & voices are recognized**. Details <b>here</b>.
- **Shapes follow the project**. More.
`
	got := Notes(changelog, "0.15.0")
	if !strings.Contains(got, "<h3>What’s new in 0.15.0</h3>") || !strings.Contains(got, "<li>Faces &amp; voices are recognized</li>") ||
		strings.Contains(got, "Coming in the next version") || strings.Contains(got, "<b>") {
		t.Errorf("a release's notes:\n%s", got)
	}
	got = Notes(changelog, "0.15.0-dev.3")
	if i, j := strings.Index(got, "Coming in the next version"), strings.Index(got, "What’s new in 0.15.0"); i < 0 || j < i {
		t.Errorf("a build past a release:\n%s", got)
	}
	if !strings.Contains(got, "<li>Updates install themselves</li>") || !strings.Contains(got, "<li>and 1 smaller change</li>") {
		t.Errorf("headlines and the technical entry:\n%s", got)
	}
}

package version

import (
	"regexp"
	"strings"
	"sync"
	"time"

	metarush "github.com/metavert-io/metarush"
	"github.com/metavert-io/metarush/internal/plainwords"
)

// ReleaseNotes is one release's part of CHANGELOG.md ("## 0.2.0 — 20 Oct 2026"),
// or the changes not released yet ("## Unreleased").
type ReleaseNotes struct {
	// Release is the version the section is about; "" for the changes not released yet.
	Release string `json:"release,omitempty"`
	// Date is the day it came out (YYYY-MM-DD), when the heading says.
	Date string `json:"date,omitempty"`
	// Heading is the section's heading as written.
	Heading string `json:"heading"`
	// Entries are its changes, in the order written (newest first).
	Entries []ReleaseNote `json:"entries"`
}

// ReleaseNote is one change, as CHANGELOG.md says it.
type ReleaseNote struct {
	// Headline is the change in a few plain words (the entry's bold lead), when
	// it has one that reads plainly; entries without one are technical.
	Headline string `json:"headline,omitempty"`
	// Text is the whole entry as written: Markdown, technical detail included.
	Text string `json:"text"`
}

// Notes is a parsed CHANGELOG.md.
type Notes []ReleaseNotes

// Section is the section about a release ("" for the changes not released yet), or nil.
func (n Notes) Section(release string) *ReleaseNotes {
	for i := range n {
		if n[i].Release == release && (release != "" || isUnreleased(n[i].Heading)) {
			return &n[i]
		}
	}
	return nil
}

var (
	notesOnce sync.Once
	notes     Notes
)

// Changelog is the CHANGELOG.md this binary was built with.
func Changelog() Notes {
	notesOnce.Do(func() { notes = ParseChangelog(metarush.Changelog) })
	return notes
}

// WhatsNew is what Settings → General and `metarush version --details` show for a
// build: a release's own section; for a build past a release, the changes not
// released yet ("coming in the next version") first, then the release's
// section. Empty sections are left out.
func WhatsNew(in Info) []ReleaseNotes { return Changelog().WhatsNew(in) }

// WhatsNew is WhatsNew from these notes.
func (n Notes) WhatsNew(in Info) []ReleaseNotes {
	var out []ReleaseNotes
	if in.Dev {
		if s := n.Section(""); s != nil && len(s.Entries) > 0 {
			out = append(out, *s)
		}
	}
	if s := n.Section(in.Release); s != nil && len(s.Entries) > 0 {
		out = append(out, *s)
	}
	if out == nil {
		out = []ReleaseNotes{}
	}
	return out
}

var (
	headingRe = regexp.MustCompile(`^##\s+(.*?)\s*$`)
	// "0.2.0 — 20 Oct 2026", "0.1.0 — 2026-10-17 (Tier A: local vertical slice)", "v0.3.0-rc1 - 2026-11-02".
	releaseRe = regexp.MustCompile(`^v?(\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?)(?:\s+[—–-]+\s+(.*))?$`)
	leadRe    = regexp.MustCompile(`^\*\*(.+?)\*\*`)
	// A decision reference in a headline: " (D68–D70)", " (D114)".
	decisionRe = regexp.MustCompile(`\s*\((?:[^()]*\bD\d+\b[^()]*)\)`)
)

func isUnreleased(heading string) bool {
	return strings.EqualFold(strings.TrimSpace(heading), "unreleased")
}

// ParseChangelog reads CHANGELOG.md: "## " headings start sections, top-level
// "- " bullets (with their indented lines) are entries.
func ParseChangelog(md string) Notes {
	var out Notes
	var cur *ReleaseNotes
	var entry []string
	flush := func() {
		if cur != nil && len(entry) > 0 {
			if e, ok := makeEntry(entry); ok {
				cur.Entries = append(cur.Entries, e)
			}
		}
		entry = nil
	}
	for _, line := range strings.Split(strings.ReplaceAll(md, "\r\n", "\n"), "\n") {
		if m := headingRe.FindStringSubmatch(line); m != nil && !strings.HasPrefix(line, "###") {
			flush()
			out = append(out, newSection(m[1]))
			cur = &out[len(out)-1]
			continue
		}
		if cur == nil {
			continue // the title and anything before the first section
		}
		switch {
		case strings.HasPrefix(line, "#"):
			flush() // a sub-heading ("### Fixes") groups entries; it is not a change itself
		case strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* "):
			flush()
			entry = []string{line[2:]}
		case strings.TrimSpace(line) == "":
			if len(entry) > 0 {
				entry = append(entry, "")
			}
		case line[0] == ' ' || line[0] == '\t':
			if len(entry) == 0 {
				entry = []string{strings.TrimSpace(line)}
			} else {
				entry = append(entry, dedent(line))
			}
		default:
			// A paragraph between entries is an entry of its own.
			flush()
			entry = []string{line}
		}
	}
	flush()
	for i := range out {
		if out[i].Entries == nil {
			out[i].Entries = []ReleaseNote{}
		}
	}
	return out
}

func newSection(heading string) ReleaseNotes {
	s := ReleaseNotes{Heading: heading}
	if isUnreleased(heading) {
		return s
	}
	m := releaseRe.FindStringSubmatch(heading)
	if m == nil {
		return s
	}
	s.Release = m[1]
	rest := strings.TrimSpace(m[2])
	if i := strings.Index(rest, " ("); i >= 0 {
		rest = rest[:i]
	}
	for _, layout := range []string{"2006-01-02", "2 Jan 2006", "2 January 2006", "Jan 2, 2006", "January 2, 2006"} {
		if t, err := time.Parse(layout, rest); err == nil {
			s.Date = t.Format("2006-01-02")
			break
		}
	}
	return s
}

// dedent removes the two spaces that continue a bullet.
func dedent(line string) string {
	if strings.HasPrefix(line, "  ") {
		return line[2:]
	}
	return strings.TrimLeft(line, " \t")
}

func makeEntry(lines []string) (ReleaseNote, bool) {
	text := strings.TrimSpace(strings.Join(lines, "\n"))
	if text == "" {
		return ReleaseNote{}, false
	}
	return ReleaseNote{Headline: headline(text), Text: text}, true
}

// headline is an entry's bold lead in plain words, or "" when it has none or it
// isn't plain (a command, code, a flag or jargon: such entries are technical).
func headline(text string) string {
	m := leadRe.FindStringSubmatch(text)
	if m == nil || strings.Contains(m[1], "`") {
		return ""
	}
	h := decisionRe.ReplaceAllString(m[1], "")
	h = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(h), ".:;,"))
	if h == "" || len(plainwords.Check(h)) > 0 {
		return ""
	}
	return h
}

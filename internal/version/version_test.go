package version

import (
	"runtime/debug"
	"strings"
	"testing"
	"time"

	metarush "github.com/metavert-io/metarush"
	"github.com/metavert-io/metarush/internal/plainwords"
)

func TestParseAndCompare(t *testing.T) {
	for _, c := range []struct {
		v       string
		release string
		dev     bool
		count   int
	}{
		{"0.2.0", "0.2.0", false, -1},
		{"v0.2.0", "0.2.0", false, -1},
		{"0.2.0-dev.3", "0.2.0", true, 3},
		{"0.2.0-dev.0", "0.2.0", true, 0},
		{"0.2.0-dev", "0.2.0", true, -1},
		{"0.1.0-dev", "0.1.0", true, -1}, // what every build said before D130
		{"0.3.0-rc1", "0.3.0-rc1", false, -1},
		{"0.3.0-rc.2-dev.4", "0.3.0-rc.2", true, 4},
		{"0.1.1-snapshot-abc1234", "0.1.1-snapshot-abc1234", false, -1},
		{"1.2.3+build.7", "1.2.3", false, -1},
	} {
		p, ok := Parse(c.v)
		if !ok || p.Release() != c.release || p.Dev != c.dev || p.DevCount != c.count {
			t.Errorf("Parse(%q) = %+v %v, want release %s dev %v count %d", c.v, p, ok, c.release, c.dev, c.count)
		}
	}
	for _, bad := range []string{"", "0.2", "0.2.0.1", "x.y.z", "0.2.0-", "0.2.0-dev.x", "01.2.0", "0.2.0-dev.3.4", "0.2.0-rc..1", "0.2.0-r c"} {
		if p, ok := Parse(bad); ok {
			t.Errorf("Parse(%q) = %+v, want it refused", bad, p)
		}
	}
	// a older than b, in MetaRush's order: N commits past a release is newer than it.
	for _, c := range [][2]string{
		{"0.1.0-dev", "0.2.0"},
		{"0.1.0", "0.2.0-dev.3"},
		{"0.2.0", "0.2.0-dev.1"},
		{"0.2.0-dev.1", "0.2.0-dev.2"},
		{"0.2.0-dev.9", "0.2.0-dev.10"},
		{"0.2.0-dev.40", "0.2.1"},
		{"0.2.1", "0.10.0"},
		{"0.9.9", "1.0.0"},
		{"0.3.0-rc1", "0.3.0"},
		{"0.3.0-rc1", "0.3.0-rc1-dev.2"},
		{"0.3.0-rc1-dev.2", "0.3.0-rc2"},
		{"0.3.0-rc.2", "0.3.0-rc.10"},
		{"0.3.0-1", "0.3.0-alpha"},
		{"0.3.0-alpha", "0.3.0-alpha.1"},
	} {
		if got, ok := Compare(c[0], c[1]); got != -1 || !ok {
			t.Errorf("Compare(%s, %s) = %d %v, want -1", c[0], c[1], got, ok)
		}
		if got, ok := Compare(c[1], c[0]); got != 1 || !ok {
			t.Errorf("Compare(%s, %s) = %d %v, want 1", c[1], c[0], got, ok)
		}
	}
	for _, c := range [][2]string{{"0.2.0", "v0.2.0"}, {"0.2.0-dev.3", "0.2.0-dev.3"}, {"0.2.0-dev", "0.2.0-dev"}, {"0.2.0", "0.2.0-dev.0"}} {
		if got, ok := Compare(c[0], c[1]); got != 0 || !ok {
			t.Errorf("Compare(%s, %s) = %d %v, want the same", c[0], c[1], got, ok)
		}
	}
	// Can't tell: a version that doesn't parse, or a build from source of the same release.
	for _, c := range [][2]string{{"", "0.2.0"}, {"", ""}, {"garbage", "0.2.0"}, {"0.2.0-dev", "0.2.0"}, {"0.2.0-dev", "0.2.0-dev.4"}} {
		if _, ok := Compare(c[0], c[1]); ok {
			t.Errorf("Compare(%s, %s) claims an order", c[0], c[1])
		}
	}
	// …but releases still order around it.
	if got, ok := Compare("0.2.0-dev", "0.3.0"); got != -1 || !ok {
		t.Errorf("Compare(0.2.0-dev, 0.3.0) = %d %v", got, ok)
	}
}

func TestFillFallsBackToVERSIONAndGit(t *testing.T) {
	bi := &debug.BuildInfo{Settings: []debug.BuildSetting{{Key: "vcs.revision", Value: "abcdef0123456789"}, {Key: "vcs.time", Value: "2026-10-20T10:00:00Z"}, {Key: "vcs.modified", Value: "true"}}}
	if v, c, d := fill("", "", "", "0.2.0\n", bi); v != "0.2.0-dev" || c != "abcdef0-dirty" || d != "2026-10-20T10:00:00Z" {
		t.Errorf("fill from git = %s %s %s", v, c, d)
	}
	if v, c, d := fill("", "", "", "0.2.0", nil); v != "0.2.0-dev" || c != "unknown" || d != "unknown" {
		t.Errorf("fill without git = %s %s %s", v, c, d)
	}
	if v, c, d := fill("0.2.0-dev.3", "1234567", "2026-10-21T14:03:00Z", "0.2.0", bi); v != "0.2.0-dev.3" || c != "1234567" || d != "2026-10-21T14:03:00Z" {
		t.Errorf("stamps were replaced: %s %s %s", v, c, d)
	}
	// This test binary has no stamps: it says VERSION plus -dev.
	if want := strings.TrimSpace(metarush.VersionFile) + "-dev"; Version != want {
		t.Errorf("Version = %q, want %q", Version, want)
	}
}

func TestVERSIONIsARelease(t *testing.T) {
	p, ok := Parse(metarush.VersionFile)
	if !ok || p.Dev || strings.Contains(metarush.VersionFile, "dev") || strings.Count(metarush.VersionFile, "\n") > 1 {
		t.Fatalf("VERSION holds %q: it must be one release such as 0.2.0 (scripts/version.sh adds the rest)", metarush.VersionFile)
	}
}

func TestDescribeAndPlain(t *testing.T) {
	in := Describe("0.2.0-dev.3", "abc1234", "2026-10-21T14:03:00Z")
	if in.Release != "0.2.0" || !in.Dev || in.DevCount == nil || *in.DevCount != 3 {
		t.Errorf("Describe(0.2.0-dev.3) = %+v", in)
	}
	day := time.Date(2026, 10, 21, 14, 3, 0, 0, time.UTC).Local().Format("2 Jan 2006")
	if got, want := in.Plain(), "MetaRush 0.2.0 · "+day+", with changes that aren't in a release yet"; got != want {
		t.Errorf("Plain() = %q, want %q", got, want)
	}
	rel := Info{Version: "0.2.0", Release: "0.2.0", Date: "2026-10-22T09:00:00Z", ReleaseDate: "2026-10-20"}
	if got := rel.Plain(); got != "MetaRush 0.2.0 · 20 Oct 2026" {
		t.Errorf("a release says %q", got)
	}
	if got := (Info{Version: "0.2.0-dev", Release: "0.2.0", Dev: true, Date: "unknown"}).Plain(); got != "MetaRush 0.2.0, with changes that aren't in a release yet" {
		t.Errorf("a build from source says %q", got)
	}
	if in := Describe("0.2.0-dev", "unknown", "unknown"); !in.Dev || in.DevCount != nil {
		t.Errorf("Describe(0.2.0-dev) = %+v", in)
	}
	if in := Describe("garbage", "", ""); in.Release != "garbage" || in.Dev {
		t.Errorf("Describe(garbage) = %+v", in)
	}
	for _, s := range []string{in.Plain(), rel.Plain(), Current().Plain()} {
		if f := plainwords.Check(s); len(f) > 0 {
			t.Errorf("%q is not plain: %v", s, f)
		}
	}
}

const fixture = `# Changelog

## Unreleased
- **Faces are recognized across clips** ([docs](docs/visual-index.md), D114). Name a face once and MetaRush
  recognizes that person.
  - **Nested** detail stays with its entry.

  A second paragraph of the same entry.
- **` + "`metarush machine rekey`" + `** (D122): a command, so technical.
- **QA checks at cuts** (QA version qa-3): jargon, so technical.
- **Project motion graphics (D68–D70).** The reference goes.
- For developers: ` + "`internal/onnx`" + `.
A paragraph between entries.

## 0.2.0 — 20 Oct 2026
- **See which version you have.** Settings → General.

## v0.1.1-rc1 - 2026-10-18
- **A release candidate.**

## 0.1.0 — 2026-10-17 (Tier A: local vertical slice)
- Single Go binary: CLI, local dashboard daemon, job worker, watched inbox.

### Not a section
- belongs to 0.1.0
`

func TestParseChangelog(t *testing.T) {
	n := ParseChangelog(fixture)
	if len(n) != 4 {
		t.Fatalf("%d sections: %+v", len(n), n)
	}
	u := n.Section("")
	if u == nil || u.Heading != "Unreleased" || len(u.Entries) != 6 {
		t.Fatalf("Unreleased = %+v", u)
	}
	heads := []string{}
	for _, e := range u.Entries {
		heads = append(heads, e.Headline)
	}
	if got := strings.Join(heads, "|"); got != "Faces are recognized across clips|||Project motion graphics||" {
		t.Errorf("headlines = %q", got)
	}
	if e := u.Entries[0]; !strings.Contains(e.Text, "recognizes that person.\n- **Nested** detail") || !strings.HasSuffix(e.Text, "A second paragraph of the same entry.") {
		t.Errorf("entry text = %q", e.Text)
	}
	if e := u.Entries[5]; e.Text != "A paragraph between entries." {
		t.Errorf("paragraph entry = %q", e.Text)
	}
	for release, date := range map[string]string{"0.2.0": "2026-10-20", "0.1.1-rc1": "2026-10-18", "0.1.0": "2026-10-17"} {
		s := n.Section(release)
		if s == nil || s.Date != date || len(s.Entries) == 0 {
			t.Errorf("section %s = %+v", release, s)
		}
	}
	if s := n.Section("0.1.0"); len(s.Entries) != 2 {
		t.Errorf("0.1.0 has %d entries (### is not a section)", len(s.Entries))
	}
	if n.Section("9.9.9") != nil {
		t.Error("a section for a release the changelog doesn't have")
	}

	// What's new: a release shows its own section; a build past it the next version's first.
	titles := func(ss []ReleaseNotes) string {
		var out []string
		for _, s := range ss {
			out = append(out, s.Heading)
		}
		return strings.Join(out, " + ")
	}
	if got := titles(n.WhatsNew(Describe("0.2.0", "a", "b"))); got != "0.2.0 — 20 Oct 2026" {
		t.Errorf("release: %s", got)
	}
	if got := titles(n.WhatsNew(Describe("0.2.0-dev.2", "a", "b"))); got != "Unreleased + 0.2.0 — 20 Oct 2026" {
		t.Errorf("dev: %s", got)
	}
	if got := n.WhatsNew(Describe("5.0.0", "a", "b")); got == nil || len(got) != 0 {
		t.Errorf("unknown release: %+v", got)
	}
	empty := ParseChangelog("# Changelog\n\n## Unreleased\n\n## 0.2.0 — 20 Oct 2026\n- **One**\n")
	if got := titles(empty.WhatsNew(Describe("0.2.0-dev.1", "a", "b"))); got != "0.2.0 — 20 Oct 2026" {
		t.Errorf("an empty Unreleased is left out: %s", got)
	}
}

// The changelog this binary carries reads: every section is "Unreleased" or a
// release with its day, and there is an Unreleased section to write in.
func TestEmbeddedChangelog(t *testing.T) {
	n := Changelog()
	if n.Section("") == nil {
		t.Error(`CHANGELOG.md has no "## Unreleased" section`)
	}
	for _, s := range n {
		if s.Release == "" && !isUnreleased(s.Heading) {
			t.Errorf("CHANGELOG.md heading %q is neither Unreleased nor a release (\"## 0.2.0 — 20 Oct 2026\")", s.Heading)
		}
		if s.Release != "" && s.Date == "" {
			t.Errorf("CHANGELOG.md heading %q has no day it came out", s.Heading)
		}
		for _, e := range s.Entries {
			if e.Headline != "" && len(plainwords.Check(e.Headline)) > 0 {
				t.Errorf("headline %q is not plain", e.Headline)
			}
		}
	}
	if len(WhatsNew(Current())) == 0 {
		t.Error("this build has nothing to say about what's new")
	}
}

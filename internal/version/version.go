// Package version is this build of MetaRush (D130). The release number lives in
// one place, the VERSION file at the top of the repository; scripts/version.sh
// stamps a build with how far past that release it is (deploy.sh, release.sh
// through goreleaser, bootstrap.sh and start.sh all use it):
//
//	built at the release tag v0.2.0, nothing uncommitted   0.2.0
//	three commits after it (or uncommitted changes)        0.2.0-dev.3
//	no tag for VERSION yet, or no git                      0.2.0-dev
//
// A build without the stamps (go build, go run, go test) says <VERSION>-dev,
// with the commit and time Go recorded from git when it could.
//
// "0.2.0-dev.3" means three commits after 0.2.0, like git describe: it is newer
// than 0.2.0 (Compare), which is not semver's order for pre-releases.
package version

import (
	"runtime/debug"
	"strings"
	"time"

	metarush "github.com/metavert-io/metarush"
)

// Stamped in at build time (scripts/version.sh --ldflags: -X <this package>.Version=…);
// a build without them fills them in from VERSION and Go's build information.
var (
	// Version is what this build calls itself: "0.2.0", "0.2.0-dev.3" or "0.2.0-dev".
	Version = ""
	// Commit is the short commit it was built from, "-dirty" with uncommitted changes.
	Commit = ""
	// Date is when it was built (RFC 3339, UTC).
	Date = ""
)

func init() {
	bi, _ := debug.ReadBuildInfo()
	Version, Commit, Date = fill(Version, Commit, Date, metarush.VersionFile, bi)
}

// fill completes the stamps: an empty version is VERSION plus "-dev", an empty
// commit and date come from the git information Go records in a binary built
// in a checkout (vcs.revision, vcs.modified, vcs.time), else "unknown".
func fill(v, commit, date, file string, bi *debug.BuildInfo) (string, string, string) {
	if v == "" {
		rel := strings.TrimSpace(file)
		if rel == "" {
			rel = "0.0.0"
		}
		v = rel + "-dev"
	}
	settings := map[string]string{}
	if bi != nil {
		for _, s := range bi.Settings {
			settings[s.Key] = s.Value
		}
	}
	if commit == "" {
		commit = "unknown"
		if rev := settings["vcs.revision"]; rev != "" {
			commit = rev[:min(7, len(rev))]
			if settings["vcs.modified"] == "true" {
				commit += "-dirty"
			}
		}
	}
	if date == "" {
		date = "unknown"
		if t := settings["vcs.time"]; t != "" {
			date = t
		}
	}
	return v, commit, date
}

// String is a one-line banner: "0.2.0-dev.3 (abc1234, 2026-10-21T14:03:00Z)".
func String() string { return Version + " (" + Commit + ", " + Date + ")" }

// Info is a build of MetaRush the way every surface reports it: `metarush
// version --json`, /api/health, Settings → General (`version.show`).
type Info struct {
	// Version is what the build calls itself: 0.2.0 at the release, 0.2.0-dev.3 three commits after it,
	// 0.2.0-dev when the count is not known (a build from source without the stamps).
	Version string `json:"version"`
	// Release is the release it is, or follows: "0.2.0" (what people are shown).
	Release string `json:"release"`
	// Dev: a build with changes that aren't in a release yet.
	Dev bool `json:"dev"`
	// DevCount is how many commits past Release it was built (dev builds whose count is known).
	DevCount *int `json:"devCount,omitempty"`
	// Commit is the short commit, "-dirty" when it was built with uncommitted changes; "unknown" without git.
	Commit string `json:"commit"`
	// Date is when it was built (RFC 3339), or "unknown".
	Date string `json:"date"`
	// ReleaseDate is the day Release came out (YYYY-MM-DD), from its CHANGELOG.md heading.
	ReleaseDate string `json:"releaseDate,omitempty"`
}

// Current is this build.
func Current() Info { return Describe(Version, Commit, Date) }

// Describe is the Info for a version string, a commit and a build date.
func Describe(v, commit, date string) Info {
	in := Info{Version: v, Release: v, Commit: commit, Date: date}
	if p, ok := Parse(v); ok {
		in.Release, in.Dev = p.Release(), p.Dev
		if p.Dev && p.DevCount >= 0 {
			n := p.DevCount
			in.DevCount = &n
		}
	}
	if s := Changelog().Section(in.Release); s != nil {
		in.ReleaseDate = s.Date
	}
	return in
}

// Day is the day in Info's plain line: the release's own day for a release
// build, else the day it was built ("" when neither is known).
func (in Info) Day() string {
	if !in.Dev && in.ReleaseDate != "" {
		if t, err := time.Parse("2006-01-02", in.ReleaseDate); err == nil {
			return t.Format("2 Jan 2006")
		}
	}
	if t, err := time.Parse(time.RFC3339, in.Date); err == nil {
		return t.Local().Format("2 Jan 2006")
	}
	return ""
}

// Plain is the build in plain words, the way Settings → General heads it:
// "MetaRush 0.2.0 · 20 Oct 2026", and for a build past a release
// "MetaRush 0.2.0 · 21 Oct 2026, with changes that aren't in a release yet".
// The exact version, the commit count and the commit are technical (--details).
func (in Info) Plain() string {
	s := "MetaRush " + in.Release
	if d := in.Day(); d != "" {
		s += " · " + d
	}
	if in.Dev {
		s += ", with changes that aren't in a release yet"
	}
	return s
}

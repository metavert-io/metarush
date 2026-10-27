package linkimport

import (
	"reflect"
	"strings"
	"testing"
)

// The weekly (background) update upgrades yt-dlp only when Homebrew has nothing
// else to change for it; otherwise the newer yt-dlp waits for a person.
func TestBackgroundPlanTouchesOnlyYtDlp(t *testing.T) {
	deps := []string{"certifi", "deno", "python@3.15", "sqlite"}
	installed := []string{"certifi", "deno", "ffmpeg", "python@3.15", "sqlite", "yt-dlp"}
	yt := brewOutdated{Name: "yt-dlp", Current: "2026.10.1"}
	for _, c := range []struct {
		name      string
		outdated  []brewOutdated
		installed []string
		want      brewPlan
	}{
		{"nothing newer", []brewOutdated{{Name: "sqlite", Current: "3.54.1"}}, installed, brewPlan{}},
		{"only yt-dlp is outdated", []brewOutdated{yt, {Name: "wget", Current: "1.26"}}, installed,
			brewPlan{Outdated: true, Latest: "2026.10.1", Upgrade: true}},
		{"Python is outdated too", []brewOutdated{yt, {Name: "python@3.15", Current: "3.15.2"}}, installed,
			brewPlan{Outdated: true, Latest: "2026.10.1", Others: []string{"python@3.15"}}},
		{"Deno and a deeper dependency are outdated", []brewOutdated{{Name: "sqlite", Current: "3.54.1"}, yt, {Name: "deno", Current: "2.10.0"}}, installed,
			brewPlan{Outdated: true, Latest: "2026.10.1", Others: []string{"deno", "sqlite"}}},
		{"a dependency is not installed yet", []brewOutdated{yt}, []string{"certifi", "python@3.15", "sqlite", "yt-dlp"},
			brewPlan{Outdated: true, Latest: "2026.10.1", Others: []string{"deno"}}},
		{"yt-dlp is pinned", []brewOutdated{{Name: "yt-dlp", Current: "2026.10.1", Pinned: true}}, installed,
			brewPlan{Outdated: true, Latest: "2026.10.1", Pinned: true}},
	} {
		if got := planBrewUpgrade(c.outdated, deps, c.installed); !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s: %+v, want %+v", c.name, got, c.want)
		}
	}
	msg := waitingMessage("2026.10.1", []string{"python@3.15", "deno", "sqlite", "openssl@4", "xz"})
	for _, w := range []string{"yt-dlp 2026.10.1", "python@3.15, deno, sqlite and 2 more", "never does by itself", "Update now in Settings > Link imports"} {
		if !strings.Contains(msg, w) {
			t.Errorf("waiting message lacks %q: %s", w, msg)
		}
	}
}

func TestVersionAtLeast(t *testing.T) {
	for _, c := range []struct {
		have, want string
		ok         bool
	}{
		{"2026.10.01", "2026.10.1", true},
		{"2026.10.01", "2026.10.1_2", true},
		{"2026.08.19", "2026.10.1", false},
		{"2026.11.02", "2026.10.1", true},
		{"2026.10.01.123456", "2026.10.1", true},
		{"installed", "2026.10.1", false},
	} {
		if got := versionAtLeast(c.have, c.want); got != c.ok {
			t.Errorf("versionAtLeast(%q, %q) = %v", c.have, c.want, got)
		}
	}
}

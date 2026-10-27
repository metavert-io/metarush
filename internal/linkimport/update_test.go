package linkimport_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/linkimport"
	"github.com/metavert-io/metarush/internal/tools"
)

// fakeBrew stands in for Homebrew: it logs every call with the two settings
// that keep an upgrade to yt-dlp alone, and answers from FAKE_BREW_* variables.
const fakeBrew = `#!/bin/sh
printf '%s|dependents-check-off=%s|auto-update-off=%s\n' "$*" "${HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK:-}" "${HOMEBREW_NO_AUTO_UPDATE:-}" >> "$FAKE_BREW_LOG"
case "$1" in
--version) echo "Homebrew 4.6.0" ;;
update) ;;
outdated) printf '%s\n' "$FAKE_BREW_OUTDATED" ;;
deps) printf '%s\n' "$FAKE_BREW_DEPS" ;;
list) printf '%s\n' "$FAKE_BREW_LIST" ;;
upgrade) echo "==> Upgrading $*" ;;
*) echo "unexpected: brew $*" >&2; exit 1 ;;
esac
`

// homebrewApp is an app whose yt-dlp looks installed by Homebrew (it lives in
// a Cellar) next to a fake brew that logs what it is asked to do.
func homebrewApp(t *testing.T) (*app.App, func() []string) {
	t.Helper()
	a, _ := importApp(t)
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cellar := filepath.Join(root, "homebrew", "Cellar", "yt-dlp", "2026.8.19_2", "bin")
	bin := filepath.Join(root, "homebrew", "bin")
	for _, d := range []string{cellar, bin} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	script, err := os.ReadFile(a.Cfg.Tools.YtDlp)
	if err != nil {
		t.Fatal(err)
	}
	a.Cfg.Tools.YtDlp, a.Cfg.Tools.Brew = filepath.Join(cellar, "yt-dlp"), filepath.Join(bin, "brew")
	if err := os.WriteFile(a.Cfg.Tools.YtDlp, script, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(a.Cfg.Tools.Brew, []byte(fakeBrew), 0o755); err != nil {
		t.Fatal(err)
	}
	a.Tools = tools.New(a.Cfg.Tools)
	// The fake logs only what MetaRush sets: a machine's own Homebrew settings
	// (GitHub's runners set HOMEBREW_NO_AUTO_UPDATE=1) are not inherited here.
	for _, k := range []string{"HOMEBREW_NO_AUTO_UPDATE", "HOMEBREW_NO_INSTALLED_DEPENDENTS_CHECK"} {
		t.Setenv(k, "")
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}
	log := filepath.Join(root, "brew-calls.log")
	t.Setenv("FAKE_BREW_LOG", log)
	t.Setenv("FAKE_BREW_DEPS", "certifi\ndeno\npython@3.15\nsqlite")
	t.Setenv("FAKE_BREW_LIST", "certifi\ndeno\nffmpeg\npython@3.15\nsqlite\nyt-dlp")
	calls := func() []string {
		b, _ := os.ReadFile(log)
		_ = os.Remove(log)
		var out []string
		for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
			if l != "" {
				out = append(out, l)
			}
		}
		return out
	}
	return a, calls
}

func outdatedJSON(names ...string) string {
	var parts []string
	for _, n := range names {
		cur := map[string]string{"yt-dlp": "2026.10.1", "python@3.15": "3.15.2", "deno": "2.10.0"}[n]
		parts = append(parts, fmt.Sprintf(`{"name":%q,"installed_versions":["0"],"current_version":%q,"pinned":false,"pinned_version":null}`, n, cur))
	}
	return `{"formulae":[` + strings.Join(parts, ",") + `],"casks":[]}`
}

// upgrades are the calls that change installed software.
func upgrades(calls []string) []string {
	var out []string
	for _, c := range calls {
		if strings.HasPrefix(c, "upgrade") || strings.HasPrefix(c, "install") || strings.HasPrefix(c, "reinstall") {
			out = append(out, c)
		}
	}
	return out
}

// The daemon's weekly update changes yt-dlp and nothing else: Python, Deno and
// the rest of what yt-dlp relies on are never upgraded in the background. When
// a newer yt-dlp would need them, it waits for a person, who sees it in the
// status (doctor, Settings) and runs the update.
func TestBackgroundUpdateOnlyTargetsYtDlp(t *testing.T) {
	a, calls := homebrewApp(t)
	ctx := context.Background()
	if st := linkimport.Probe(ctx, a); st.ManagedBy != "homebrew" {
		t.Fatalf("a yt-dlp in a Cellar is Homebrew's: %+v", st)
	}
	others := []string{"certifi", "deno", "python@3.15", "sqlite"}
	backgroundOnlyYtDlp := func(cs []string) {
		t.Helper()
		for _, c := range cs {
			args := strings.Fields(strings.SplitN(c, "|", 2)[0])
			if !slices.Contains([]string{"update", "outdated", "deps", "list", "upgrade"}, args[0]) {
				t.Errorf("the weekly update ran brew %s", c)
			}
			for _, o := range others {
				if slices.Contains(args, o) {
					t.Errorf("the weekly update named %s: brew %s", o, c)
				}
			}
		}
	}

	// Only yt-dlp is outdated: it alone is upgraded, with the dependents check off.
	t.Setenv("FAKE_BREW_OUTDATED", outdatedJSON("yt-dlp"))
	res, err := linkimport.BackgroundUpdate(ctx, a)
	if err != nil || res.How != "brew upgrade --formula yt-dlp" || res.Waiting != "" {
		t.Fatalf("only yt-dlp outdated: %+v %v", res, err)
	}
	cs := calls()
	backgroundOnlyYtDlp(cs)
	if up := upgrades(cs); len(up) != 1 || up[0] != "upgrade --formula yt-dlp|dependents-check-off=1|auto-update-off=1" {
		t.Fatalf("the one change is yt-dlp's upgrade, and nothing that relies on it: %q (all calls %q)", up, cs)
	}

	// Python (and Deno) are outdated too: nothing is upgraded; the newer yt-dlp waits.
	t.Setenv("FAKE_BREW_OUTDATED", outdatedJSON("yt-dlp", "python@3.15", "deno"))
	res, err = linkimport.BackgroundUpdate(ctx, a)
	if err != nil || res.Waiting != "2026.10.1" || !slices.Equal(res.Others, []string{"deno", "python@3.15"}) || res.Updated {
		t.Fatalf("a newer yt-dlp that needs newer Python and Deno waits: %+v %v", res, err)
	}
	cs = calls()
	backgroundOnlyYtDlp(cs)
	if up := upgrades(cs); len(up) != 0 {
		t.Fatalf("nothing is upgraded in the background when yt-dlp needs other packages: %q", up)
	}
	st, err := call[linkimport.ImportTools](t, a, "ingest.url.tools", linkimport.ImportToolsIn{})
	if err != nil || st.UpdateWaiting != "2026.10.1" || !slices.Contains(st.UpdateOthers, "python@3.15") {
		t.Fatalf("the status says an update waits: %+v %v", st, err)
	}
	if !slices.ContainsFunc(st.Problems, func(p string) bool {
		return strings.Contains(p, "Update now in Settings > Link imports") && strings.Contains(p, "python@3.15")
	}) {
		t.Fatalf("the status says what to do: %q", st.Problems)
	}

	// A dependency that isn't installed yet waits too.
	t.Setenv("FAKE_BREW_OUTDATED", outdatedJSON("yt-dlp"))
	t.Setenv("FAKE_BREW_LIST", "certifi\npython@3.15\nsqlite\nyt-dlp")
	res, err = linkimport.BackgroundUpdate(ctx, a)
	if err != nil || res.Waiting == "" || !slices.Equal(res.Others, []string{"deno"}) || len(upgrades(calls())) != 0 {
		t.Fatalf("a missing dependency waits for a person: %+v %v", res, err)
	}

	// The person runs the update: Homebrew may bring what yt-dlp needs, and the note clears.
	up, err := call[linkimport.ImportToolsUpdate](t, a, "ingest.url.update", linkimport.ImportToolsIn{})
	if err != nil || up.How != "brew upgrade yt-dlp" {
		t.Fatalf("the update a person runs: %+v %v", up, err)
	}
	if cs := upgrades(calls()); len(cs) != 1 || cs[0] != "upgrade yt-dlp|dependents-check-off=|auto-update-off=" {
		t.Fatalf("a person's update is Homebrew's own upgrade of yt-dlp: %q", cs)
	}
	if st, _ := call[linkimport.ImportTools](t, a, "ingest.url.tools", linkimport.ImportToolsIn{}); st.UpdateWaiting != "" {
		t.Fatalf("after the person's update nothing waits: %+v", st)
	}
}

// A yt-dlp MetaRush downloaded (in the home's tools folder) runs only as it
// was put in place; when MetaRush updates it, the new one is written down,
// so it still runs afterwards.
func TestSelfUpdateOfADownloadedYtDlpStillRuns(t *testing.T) {
	a, _ := importApp(t)
	yt := filepath.Join(a.Cfg.Home(), "tools", "yt-dlp", "yt-dlp")
	if err := os.MkdirAll(filepath.Dir(yt), 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nfor x in \"$@\"; do [ \"$x\" = -U ] && printf '# updated\\n' >> \"$0\"; done\nexec '" + a.Cfg.Tools.YtDlp + "' \"$@\"\n"
	if err := os.WriteFile(yt, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tools.Recorded(yt) // as MetaRush put it in place
	a.Cfg.Tools.YtDlp = yt
	a.Tools = tools.New(a.Cfg.Tools)
	if _, err := a.Tools.Run(context.Background(), tools.Spec{Tool: tools.YtDlp, Args: []string{"--version"}}); err != nil {
		t.Fatalf("as put in place: %v", err)
	}
	if _, err := linkimport.Update(context.Background(), a); err != nil {
		t.Fatalf("update: %v", err)
	}
	if b, _ := os.ReadFile(yt); !strings.Contains(string(b), "# updated") {
		t.Fatal("the stand-in didn't update itself")
	}
	if _, err := a.Tools.Run(context.Background(), tools.Spec{Tool: tools.YtDlp, Args: []string{"--version"}}); err != nil {
		t.Errorf("after MetaRush updated it: %v", err)
	}
}

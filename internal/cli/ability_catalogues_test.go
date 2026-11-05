package cli

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/abilities"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

// cliCatalogue serves an index listing the fixture repository's title card
// (scripts/make-fixture-repo.sh … ability) over HTTPS on this computer.
func cliCatalogue(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git isn't installed")
	}
	base := t.TempDir()
	for _, step := range [][]string{{}, {"ability"}} {
		if out, err := exec.Command("bash", append([]string{filepath.Join("..", "..", "scripts", "make-fixture-repo.sh"), base}, step...)...).CombinedOutput(); err != nil {
			t.Fatalf("fixture repo: %v %s", err, out)
		}
	}
	t.Setenv("METARUSH_GIT_BASE", base)
	commit, err := exec.Command("git", "-C", filepath.Join(base, "acme", "widget.git"), "rev-parse", "main").Output()
	if err != nil {
		t.Fatal(err)
	}
	arc, err := abilities.Build(filepath.Join("..", "..", "tests", "fixtures", "ability-repo", "title-card"))
	if err != nil {
		t.Fatal(err)
	}
	index := map[string]any{"format": abilities.IndexFormat, "name": "acme", "title": "Acme abilities", "version": 1, "date": "2026-11-03",
		"abilities": []any{map[string]any{"name": "title-card", "title": "Title card", "does": "Makes a plain title card picture from a few words.",
			"publisher": map[string]any{"github": "acme"},
			"source":    map[string]any{"git": map[string]any{"repository": "https://github.com/acme/widget", "commit": strings.TrimSpace(string(commit)), "path": "abilities/title-card"}},
			"archive":   arc.Hash, "licence": "MIT", "outputs": []string{"picture"}, "network": "off", "cost": "free", "programs": []string{"python3"}}}}
	b, _ := json.Marshal(index)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(b) }))
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(abilities.EnvCatalogueCA, ca)
	return srv.URL + "/index.json"
}

// Trusting a catalogue and bringing an ability in from one take a person at
// their own terminal: Claude Code and AI apps are refused; listing and
// browsing are open to anyone (D486).
func TestCataloguesAtATerminal(t *testing.T) {
	a := testapp.New(t)
	home := a.Cfg.Home()
	u := cliCatalogue(t)

	t.Setenv("CLAUDECODE", "1")
	for _, args := range [][]string{
		{"ability", "catalogues", "add", u, "--yes", "--json"},
		{"abilities", "catalogue", "remove", "acme", "--yes", "--json"},
		{"ability", "import", "--from-catalogue", "acme:title-card", "--yes", "--json"},
	} {
		if code, msg := errorCode(t, runCLI(t, home, args...)); code != apperr.HumanOnly || !strings.Contains(msg, "Only a person") {
			t.Errorf("%v from Claude Code: %s %s", args, code, msg)
		}
	}
	if r := runCLI(t, home, "ability", "browse", "--json"); r.code != 0 || !strings.Contains(r.stdout, "no catalogue") {
		t.Fatalf("anyone browses: %d %s", r.code, r.stdout+r.stderr)
	}

	asPerson(t)
	if r := runCLI(t, home, "ability", "catalogues", "add", u, "--yes"); r.code != 0 || !strings.Contains(r.stdout, "trusts “Acme abilities” now") {
		t.Fatalf("a person adds it: %d %s", r.code, r.stdout+r.stderr)
	}
	if r := runCLI(t, home, "abilities", "catalogues"); r.code != 0 || !strings.Contains(r.stdout, "1 ability listed") {
		t.Fatalf("the list: %d %s", r.code, r.stdout+r.stderr)
	}
	r := runCLI(t, home, "abilities", "browse", "--search", "card", "--details")
	if r.code != 0 || !strings.Contains(r.stdout, "Title card") || !strings.Contains(r.stdout, "Listed, not checked by MetaRush") || !strings.Contains(r.stdout, "--from-catalogue acme:title-card@") {
		t.Fatalf("browse: %d %s", r.code, r.stdout+r.stderr)
	}
	r = runCLI(t, home, "ability", "import", "--from-catalogue", "acme:title-card", "--yes")
	if r.code != 0 || !strings.Contains(r.stdout, "came in from “Acme abilities” as a draft") || !strings.Contains(r.stdout, "metarush ability check --version title-card@") {
		t.Fatalf("a person brings it in: %d %s", r.code, r.stdout+r.stderr)
	}
	r = runCLI(t, home, "ability", "list")
	if r.code != 0 || !strings.Contains(r.stdout, "not checked here yet") {
		t.Fatalf("it is a draft here: %d %s", r.code, r.stdout+r.stderr)
	}
	if r := runCLI(t, home, "ability", "import", "x.ability.zip", "--from-catalogue", "acme:title-card", "--json"); r.code == 0 {
		t.Fatalf("a file and a catalogue's ability at once: %s", r.stdout)
	}
	if r := runCLI(t, home, "ability", "catalogues", "remove", "acme", "--yes"); r.code != 0 || !strings.Contains(r.stdout, "doesn't trust") {
		t.Fatalf("a person removes it: %d %s", r.code, r.stdout+r.stderr)
	}
}

// seedCataloguePlain trusts a catalogue on this computer listing a keyed
// ability (read from an ability file), for the plain-output guard.
func seedCataloguePlain(t *testing.T, a *app.App) {
	t.Helper()
	index := map[string]any{"format": abilities.IndexFormat, "name": "acme", "title": "Acme abilities", "about": "Abilities for video teams.", "version": 3, "date": "2026-11-03",
		"maintainer": map[string]any{"github": "acme", "name": "Acme"},
		"abilities": []any{map[string]any{"name": "stock-photos", "title": "Stock photos", "does": "Finds a stock photo that matches a few words.",
			"publisher": map[string]any{"github": "acme"},
			"source":    map[string]any{"file": map[string]any{"url": "https://127.0.0.1:1/stock-photos.ability.zip", "sha256": strings.Repeat("e", 64)}},
			"archive":   strings.Repeat("f", 64), "licence": "MIT", "outputs": []string{"picture"}, "ai": map[string]any{"picture": "none"},
			"network": "metarush", "hosts": []string{"api.photos.example"}, "sends": []string{"words"}, "keys": []any{map[string]any{"name": "photos"}}, "cost": "keyed",
			"programs": []string{"python3"}, "tags": []string{"stock", "photo"}},
			map[string]any{"name": "broken", "title": "Broken", "does": "x", "archive": "nope"}}}
	b, _ := json.Marshal(index)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(b) }))
	t.Cleanup(srv.Close)
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv(abilities.EnvCatalogueCA, ca)
	if _, err := ops.CallTyped[abilities.AbilityCatalogueAddOut](context.Background(), a, "ability.catalogues.add", abilities.AbilityCatalogueAddIn{URL: srv.URL + "/index.json"}, ops.Caller{Human: true}); err != nil {
		t.Fatal(err)
	}
}

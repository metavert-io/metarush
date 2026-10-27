package secretpath

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// scripts/publish-check.sh (D322, D323) on invented repositories: the
// open-source-prep skill's check-public.sh runs first (deny-listed terms, emails,
// home folders, history), then MetaRush's own checks — work notes, tailnet hosts,
// the repository's secret patterns. Every name, host and key here is made up, and
// the ones the check must catch are put together at run time, so this file itself
// passes both checks.
func TestPublishCheckScript(t *testing.T) {
	for _, tool := range []string{"git", "bash", "awk", "python3"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	script, err := filepath.Abs("../../scripts/publish-check.sh")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(script); err != nil {
		t.Skip("scripts/publish-check.sh not present")
	}
	at, tsnet := "@", ".ts"+".net"
	work := t.TempDir()
	deny := filepath.Join(work, "deny.txt")
	write(t, deny, "# invented\nZanzibar Widgets\n")
	conf := filepath.Join(work, "no-machine-config") // no ~/.config/open-source-prep in tests

	run := func(args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command("bash", append([]string{script}, args...)...)
		cmd.Env = append(os.Environ(), "OPEN_SOURCE_PREP_DIR="+conf)
		out, err := cmd.CombinedOutput()
		code := 0
		if ee, ok := err.(*exec.ExitError); ok {
			code = ee.ExitCode()
		} else if err != nil {
			t.Fatal(err)
		}
		return string(out), code
	}
	repo := func(name string, files map[string]string, force ...string) string {
		t.Helper()
		dir := filepath.Join(work, name)
		for rel, body := range files {
			write(t, filepath.Join(dir, rel), body)
		}
		git := func(args ...string) {
			t.Helper()
			cmd := exec.Command("git", append([]string{"-C", dir, "-c", "maintenance.auto=false", "-c", "gc.auto=0"}, args...)...)
			cmd.Env = append(os.Environ(), "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1")
			if b, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("git %v: %v %s", args, err, b)
			}
		}
		git("init", "-q")
		git("add", "-A")
		for _, f := range force {
			git("add", "-f", f)
		}
		git("-c", "user.name=Maintainer", "-c", "user.email=jon"+at+"metavert.io", "commit", "-qm", "The first version")
		return dir
	}
	wants := func(out string, lines ...string) {
		t.Helper()
		for _, l := range lines {
			if !strings.Contains(out, l) {
				t.Errorf("not reported: %q\n%s", l, out)
			}
		}
	}
	allow := "jon" + at + "metavert.io\n"

	// A clean repository passes both parts, with the hosts that are allowed.
	clean := repo("clean", map[string]string{
		"scripts/check-public.allow": allow,
		"README.md": "Write to sam" + at + "example.com. Open https://studio.example" + tsnet + " or <machine>.<tailnet>" + tsnet +
			", or the placeholder studio.tail1234" + tsnet + ".\n",
		".gitignore": "/local/\nnode_modules/\n",
	})
	out, code := run("-r", clean, deny)
	if code != 0 || !strings.Contains(out, "0 finding(s) from MetaRush's own checks") || !strings.Contains(out, "0 finding(s) in") {
		t.Fatalf("a clean repository (exit %d):\n%s", code, out)
	}

	// One of everything MetaRush checks, and a deny-listed term for the generic part.
	dirty := repo("dirty", map[string]string{
		"scripts/check-public.allow":         allow,
		"handover.md":                        "next\n",
		"pm/plan.md":                         "plan\n",
		"docs/plan-of-record.md":             "later\n",
		"local/publish-denylist.txt":         "Zanzibar Widgets\n",
		"local/publish-worknotes.txt":        "# invented\nhandover.md\nPlan-of-Record.md\nsprint-journal.md\ndesign-log.md\npricing-notes.md\n",
		".gitignore":                         "/pm/\nnode_modules/\nCLAUDE.local.md\n/local/\n",
		"docs/hosts.md":                      "Open https://studio.tail5f3a9c" + tsnet + " for the dashboard.\n",
		"docs/thanks.md":                     "Thanks to Zanzibar widgets for the footage.\n",
		"conf/tigris.sh":                     "export S=tsec_" + strings.Repeat("Zx9", 10) + "\n",
		"conf/fixture.sh":                    "export T=tsec_" + strings.Repeat("Qw8", 10) + " # secret-scan: allow\n",
		"docs/notes/sprint-journal.md":       "plan\n",
		"docs/notes/CLAUDE.local.md":         "notes\n",
		"web/src/strings.ts":                 "export const host = 'name.example" + tsnet + "'\n",
		"tests/fixtures/README.md":           "nothing to see\n",
		"tests/fixtures/address.md":          "studio.tail0000" + tsnet + " and box.tailXXXX" + tsnet + "\n",
		"docs/tailnet.md":                    "https://mini.your-tailnet" + tsnet + "\n",
		"docs/notes/README.md":               "fine\n",
		"docs/design-log.md":                 "the plan for one computer\n",
		"docs/pricing-notes.md":              "a decision\n",
		"internal/app/hosts_test.txt":        "studio.taila1b2c3" + tsnet + "\n",
		"local.example/publish-denylist.txt": "# the shape, invented\nQuillfeather Studios\n",
	}, "local/publish-denylist.txt", "pm/plan.md", "docs/notes/CLAUDE.local.md")
	out, code = run("-r", dirty, deny)
	if code != 1 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	wants(out,
		"handover.md: [work-note]", "pm/plan.md: [work-note]", "docs/plan-of-record.md: [work-note]",
		"docs/notes/sprint-journal.md: [work-note]", "docs/notes/CLAUDE.local.md: [work-note]",
		"docs/design-log.md: [work-note]", "docs/pricing-notes.md: [work-note]",
		"local/publish-denylist.txt: [work-note]",
		".gitignore:1: [work-note] the .gitignore names /pm/", ".gitignore:3: [work-note] the .gitignore names CLAUDE.local.md",
		"docs/hosts.md:1: [tailnet-host: studio.tail5f3a9c"+tsnet+"]",
		"internal/app/hosts_test.txt:1: [tailnet-host: studio.taila1b2c3"+tsnet+"]",
		"conf/tigris.sh:1: [secret] a likely secret (not shown)",
		"docs/thanks.md:1: [deny-list: zanzibar widgets]",
		"13 finding(s) from MetaRush's own checks",
	)
	for _, never := range []string{"tsec_", "conf/fixture.sh", "tail0000", "tailXXXX", "your-tailnet", "name.example", ".gitignore:4", "local.example"} {
		if strings.Contains(out, never) {
			t.Errorf("reported %q, which is allowed:\n%s", never, out)
		}
	}

	// Paths in scripts/check-public.skip are left out of MetaRush's checks too.
	skipped := repo("skipped", map[string]string{
		"scripts/check-public.allow": allow,
		"scripts/check-public.skip":  "# invented fixtures\ndocs/hosts.md\n",
		"docs/hosts.md":              "Open https://studio.tail5f3a9c" + tsnet + "\n",
	})
	if out, code := run("-r", skipped, deny); code != 0 || strings.Contains(out, "tail5f3a9c") {
		t.Fatalf("a skipped file was checked (exit %d):\n%s", code, out)
	}

	// Not a repository: a usage error.
	if out, code := run("-r", work); code != 2 {
		t.Fatalf("not a repository (exit %d):\n%s", code, out)
	}
}

func write(t *testing.T, p, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

package cli

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/plainwords"
	"github.com/metavert-io/metarush/internal/testutil/fakerclone"
	"github.com/metavert-io/metarush/internal/testutil/faketailscale"
	"github.com/metavert-io/metarush/internal/testutil/testapp"
)

func TestMain(m *testing.M) {
	faketailscale.MaybeRun() // this binary doubles as the fake `tailscale`
	fakerclone.MaybeRun()    // … and the fake `rclone` (D220)
	os.Exit(m.Run())
}

func TestRemoteCommands(t *testing.T) {
	a := testapp.New(t)
	home := a.Cfg.Home()
	st := faketailscale.Default()
	st.Certs = false
	tool, _ := faketailscale.Write(t, st)
	t.Setenv("METARUSH_TAILSCALE", tool)

	// Status in plain words: off, and what HTTPS certificates need.
	r := runCLI(t, home, "remote", "status")
	out := r.stdout + r.stderr
	if r.code != 0 || !strings.Contains(out, "Remote access is off") || !strings.Contains(out, "HTTPS Certificates") || !strings.Contains(out, "Sam's MacBook") {
		t.Fatalf("status (exit %d):\n%s", r.code, out)
	}
	if f := plainwords.Check(out); len(f) > 0 {
		t.Fatalf("status isn't plain: %v\n%s", f, out)
	}
	r = runCLI(t, home, "remote", "status", "--json")
	var doc struct {
		OK   bool `json:"ok"`
		Data struct {
			On           bool   `json:"on"`
			ServeCommand string `json:"serveCommand"`
			Tailscale    struct {
				HTTPSReady bool   `json:"httpsReady"`
				DNSName    string `json:"dnsName"`
			} `json:"tailscale"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(r.stdout), &doc); err != nil || !doc.OK || doc.Data.On || doc.Data.Tailscale.HTTPSReady || doc.Data.Tailscale.DNSName != "studio-mac.tail0000.ts.net" ||
		doc.Data.ServeCommand != "tailscale serve --bg 4859" {
		t.Fatalf("--json: %v\n%s", err, r.stdout)
	}
	if r = runCLI(t, home, "remote", "status", "--details"); !strings.Contains(r.stdout, "tailscale serve --bg 4859") {
		t.Fatalf("--details shows the commands:\n%s", r.stdout)
	}

	// Changes need a person at an interactive terminal (tests have none).
	for _, args := range [][]string{{"remote", "enable", "--allow", "maya@example.com"}, {"remote", "disable"}, {"remote", "allow", "maya@example.com"},
		{"remote", "remove", "maya@example.com"}, {"remote", "signout", "--all"}} {
		r = runCLI(t, home, args...)
		if r.code == 0 || !strings.Contains(r.stdout+r.stderr, "needs a person") {
			t.Errorf("metarush %s without a person (exit %d):\n%s", strings.Join(args, " "), r.code, r.stdout+r.stderr)
		}
	}
	// One-time links are for this computer only (D183): no link for another one.
	r = runCLI(t, home, "dashboard", "--remote", "--print-link")
	if r.code == 0 || !strings.Contains(r.stdout+r.stderr, "work only on this computer") {
		t.Fatalf("dashboard --remote (exit %d):\n%s", r.code, r.stdout+r.stderr)
	}
}

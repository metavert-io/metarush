package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNormalizePublicURL(t *testing.T) {
	ok := []struct{ in, url, host string }{
		{"https://Studio-Mac.tail0000.ts.net", "https://studio-mac.tail0000.ts.net", "studio-mac.tail0000.ts.net"},
		{" https://studio-mac.tail0000.ts.net/ ", "https://studio-mac.tail0000.ts.net", "studio-mac.tail0000.ts.net"},
		{"https://studio-mac.tail0000.ts.net.:443", "https://studio-mac.tail0000.ts.net", "studio-mac.tail0000.ts.net"},
		{"https://studio-mac.tail0000.ts.net:8443", "https://studio-mac.tail0000.ts.net:8443", "studio-mac.tail0000.ts.net:8443"},
		{"http://studio-mac.tail0000.ts.net:80", "http://studio-mac.tail0000.ts.net", "studio-mac.tail0000.ts.net"},
	}
	for _, c := range ok {
		u, h, _, err := NormalizePublicURL(c.in)
		if err != nil || u != c.url || h != c.host {
			t.Errorf("%q → %q %q %v, want %q %q", c.in, u, h, err, c.url, c.host)
		}
	}
	for _, bad := range []string{"", "studio-mac.tail0000.ts.net", "ftp://studio-mac.tail0000.ts.net", "https://user:pw@studio-mac.tail0000.ts.net",
		"https://studio-mac.tail0000.ts.net/metarush", "https://studio-mac.tail0000.ts.net/?x=1", "https://studio-mac.tail0000.ts.net/#x",
		"https://100.101.102.103", "https://[fd7a:115c:a1e0::1]", "https://localhost", "https://studio.localhost", "https://studio", "https://studio-mac.tail0000.ts.net:99999",
		"https://bad_name.tail0000.ts.net"} {
		if _, _, _, err := NormalizePublicURL(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		} else if strings.Contains(err.Error(), "%!") {
			t.Errorf("%q: broken message %q", bad, err)
		}
	}
}

func TestNormalizeLogin(t *testing.T) {
	for in, want := range map[string]string{" Maya@Example.com ": "maya@example.com", "octocat@github": "octocat@github", "a.b+c@x.co": "a.b+c@x.co"} {
		if got, err := NormalizeLogin(in); err != nil || got != want {
			t.Errorf("%q → %q %v, want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "maya", "@example.com", "maya@", "ma ya@example.com", "maya@example.com,bob@example.com", "<x>@example.com", "a\x00@b.c"} {
		if _, err := NormalizeLogin(bad); err == nil {
			t.Errorf("%q should be refused", bad)
		}
	}
}

func TestRemoteMatchesHostAndLists(t *testing.T) {
	r, err := RemoteFrom(Dashboard{PublicURL: "https://studio-mac.tail0000.ts.net", TailscaleLogins: []string{"Sam@Example.com", "maya@example.com", "sam@example.com"}, MachineName: " Sam's MacBook "})
	if err != nil {
		t.Fatal(err)
	}
	if !r.On() || !r.IdentitySignIn() || !r.HTTPS || r.MachineName != "Sam's MacBook" {
		t.Fatalf("%+v", r)
	}
	if strings.Join(r.Logins, ",") != "maya@example.com,sam@example.com" {
		t.Fatalf("logins not normalized, sorted and unique: %v", r.Logins)
	}
	for h, want := range map[string]bool{"studio-mac.tail0000.ts.net": true, "STUDIO-MAC.tail0000.ts.net": true, "studio-mac.tail0000.ts.net:443": true,
		"studio-mac.tail0000.ts.net.": true, "studio-mac.tail0000.ts.net:8443": false, "studio-mac.tail0000.ts.net.evil.com": false, "evil.com": false,
		"127.0.0.1:4845": false, "": false, "studio-mac": false} {
		if got := r.MatchesHost(h); got != want {
			t.Errorf("MatchesHost(%q) = %v, want %v", h, got, want)
		}
	}
	if !r.Allowed(" MAYA@example.com") || r.Allowed("bob@example.com") || r.Allowed("") {
		t.Fatal("Allowed")
	}
	p, _ := RemoteFrom(Dashboard{PublicURL: "https://studio-mac.tail0000.ts.net:8443"})
	if !p.MatchesHost("studio-mac.tail0000.ts.net:8443") || p.MatchesHost("studio-mac.tail0000.ts.net") || p.IdentitySignIn() {
		t.Fatalf("port: %+v", p)
	}
	off, _ := RemoteFrom(Dashboard{TailscaleLogins: []string{"maya@example.com"}})
	if off.On() || off.IdentitySignIn() || off.MatchesHost("") {
		t.Fatal("a list without an address does nothing")
	}
	if _, err := RemoteFrom(Dashboard{TailscaleLogins: []string{"nope"}}); err == nil || !strings.Contains(err.Error(), "tailscaleLogins") {
		t.Fatalf("bad login: %v", err)
	}
	if _, err := RemoteFrom(Dashboard{MachineName: strings.Repeat("x", 81)}); err == nil {
		t.Fatal("long machine name")
	}
}

func TestRemoteLiveAndLoaded(t *testing.T) {
	h := t.TempDir()
	cfg := Default(h)
	cfg.Installation = Ref{ID: "ins_x", Name: "studio-mac"}
	if cfg.Remote().On() || cfg.MachineName() != "studio-mac" {
		t.Fatalf("default: %+v %q", cfg.Remote(), cfg.MachineName())
	}
	if err := cfg.SetRemote(Dashboard{PublicURL: "https://studio-mac.tail0000.ts.net", MachineName: "Sam's MacBook"}); err != nil {
		t.Fatal(err)
	}
	if cfg.Remote().PublicURL != "https://studio-mac.tail0000.ts.net" || cfg.MachineName() != "Sam's MacBook" {
		t.Fatalf("live: %+v", cfg.Remote())
	}
	if err := cfg.SetRemote(Dashboard{PublicURL: "https://localhost"}); err == nil || cfg.Remote().PublicURL == "" {
		t.Fatal("a bad address must be refused and leave the settings as they were")
	}

	// config.yaml: read, edited key by key, refused when wrong.
	if err := Save(h, Default(h)); err != nil {
		t.Fatal(err)
	}
	if err := SetField(h, []string{"dashboard", "publicURL"}, "https://studio-mac.tail0000.ts.net"); err != nil {
		t.Fatal(err)
	}
	if err := SetField(h, []string{"dashboard", "tailscaleLogins"}, []string{"maya@example.com"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := Load(h)
	if err != nil {
		t.Fatal(err)
	}
	if rm := loaded.Remote(); !rm.IdentitySignIn() || rm.Logins[0] != "maya@example.com" || rm.Host != "studio-mac.tail0000.ts.net" {
		t.Fatalf("loaded: %+v", rm)
	}
	if err := SetField(h, []string{"dashboard", "tailscaleLogins"}, []string{}); err != nil {
		t.Fatal(err)
	}
	if loaded, err = Load(h); err != nil || loaded.Remote().IdentitySignIn() {
		t.Fatalf("emptied list: %v %+v", err, loaded.Remote())
	}
	if err := SetField(h, []string{"dashboard", "publicURL"}, "http://127.0.0.1:4845"); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(h); err == nil || !strings.Contains(err.Error(), "publicURL") {
		t.Fatalf("a bad publicURL in config.yaml must stop loading with a clear message: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(h, "config.yaml"))
	if !strings.Contains(string(raw), "publicURL") {
		t.Fatalf("config.yaml:\n%s", raw)
	}
}

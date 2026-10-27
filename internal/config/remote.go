package config

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"gopkg.in/yaml.v3"
)

// Remote access over Tailscale (D128, D129): the public address people use
// from their other computers, the Tailscale accounts that are signed in by
// themselves, and the name this computer goes by. They change while MetaRush
// runs (`metarush remote …`, Settings → People → Remote access), so code reads them
// through Config.Remote, never from the Dashboard section directly.

// Remote is the remote-access settings in effect.
type Remote struct {
	// PublicURL is the address as scheme://host[:port] (no path); "" = off.
	PublicURL string
	// Host is the Host header browsers send for PublicURL: the name in lower
	// case, with the port only when it isn't the scheme's default.
	Host string
	// HTTPS reports whether PublicURL is an https address.
	HTTPS bool
	// Logins are the Tailscale accounts that sign in by themselves (lower
	// case, sorted, each once).
	Logins []string
	// MachineName is this computer's name for people using it from elsewhere
	// ("" when not set; see Config.MachineName).
	MachineName string
}

// On reports whether remote access is set up (there is a public address).
func (r Remote) On() bool { return r.PublicURL != "" }

// IdentitySignIn reports whether listed Tailscale accounts are signed in by
// themselves when they come through the public address.
func (r Remote) IdentitySignIn() bool { return r.On() && len(r.Logins) > 0 }

// Allowed reports whether a Tailscale account may sign in by itself.
func (r Remote) Allowed(login string) bool {
	login = strings.ToLower(strings.TrimSpace(login))
	if login == "" {
		return false
	}
	for _, l := range r.Logins {
		if l == login {
			return true
		}
	}
	return false
}

func (r Remote) defaultPort() string {
	if r.HTTPS {
		return "443"
	}
	return "80"
}

// MatchesHost reports whether a request's Host header names the public
// address (any case, a trailing dot, the default port spelled out or not).
func (r Remote) MatchesHost(host string) bool {
	if r.Host == "" || host == "" {
		return false
	}
	name, port := splitNamePort(strings.ToLower(strings.TrimSpace(host)))
	name = strings.TrimSuffix(name, ".")
	wantName, wantPort := splitNamePort(r.Host)
	if name == "" || name != wantName {
		return false
	}
	if port == "" {
		port = r.defaultPort()
	}
	if wantPort == "" {
		wantPort = r.defaultPort()
	}
	return port == wantPort
}

// splitNamePort splits "name:port" for DNS names (never IPv6 literals, which
// public addresses can't be).
func splitNamePort(h string) (string, string) {
	i := strings.LastIndexByte(h, ':')
	if i < 0 {
		return h, ""
	}
	p := h[i+1:]
	if _, err := strconv.Atoi(p); err != nil || p == "" {
		return h, ""
	}
	return h[:i], p
}

// Remote returns the remote-access settings in effect.
func (c *Config) Remote() Remote {
	if c.live != nil {
		if p := c.live.remote.Load(); p != nil {
			return *p
		}
	}
	r, _ := RemoteFrom(c.Dashboard)
	return r
}

// SetRemote changes the remote-access settings for everything that shares
// this config: the dashboard uses them from its next request on. The
// Dashboard section itself is left as it was loaded (read Remote instead).
func (c *Config) SetRemote(d Dashboard) error {
	r, err := RemoteFrom(d)
	if err != nil {
		return err
	}
	c.ensureLive()
	c.live.remote.Store(&r)
	return nil
}

// LoadRemote reads only the dashboard section of <home>/config.yaml (with
// ${VAR:default} expansion, resolving no secrets): the running dashboard uses
// it to pick up `metarush remote …` changes made in a terminal on the very next
// request, so taking someone off the list takes effect at once.
func LoadRemote(home string) (Dashboard, error) {
	raw, err := os.ReadFile(Path(home))
	if err != nil {
		return Dashboard{}, err
	}
	var only struct {
		Dashboard Dashboard `yaml:"dashboard"`
	}
	if err := yaml.Unmarshal([]byte(Expand(string(raw))), &only); err != nil {
		return Dashboard{}, err
	}
	if _, err := RemoteFrom(only.Dashboard); err != nil {
		return Dashboard{}, err
	}
	return only.Dashboard, nil
}

// MachineName is how the dashboard names this computer to people using it
// from another one: dashboard.machineName, else the installation's name.
func (c *Config) MachineName() string {
	if n := c.Remote().MachineName; n != "" {
		return n
	}
	if c.Installation.Name != "" {
		return c.Installation.Name
	}
	return "the computer that runs MetaRush"
}

// RemoteFrom checks and normalizes the dashboard's remote-access settings.
func RemoteFrom(d Dashboard) (Remote, error) {
	r := Remote{MachineName: strings.TrimSpace(d.MachineName)}
	if len([]rune(r.MachineName)) > 80 {
		return Remote{}, errors.New("the computer's name for remote access (dashboard.machineName) is longer than 80 characters")
	}
	if strings.IndexFunc(r.MachineName, unicode.IsControl) >= 0 {
		return Remote{}, errors.New("the computer's name for remote access (dashboard.machineName) has characters that can't be shown")
	}
	if strings.TrimSpace(d.PublicURL) != "" {
		u, host, https, err := NormalizePublicURL(d.PublicURL)
		if err != nil {
			return Remote{}, fmt.Errorf("the address for remote access (dashboard.publicURL): %w", err)
		}
		r.PublicURL, r.Host, r.HTTPS = u, host, https
	}
	seen := map[string]bool{}
	for _, l := range d.TailscaleLogins {
		n, err := NormalizeLogin(l)
		if err != nil {
			return Remote{}, fmt.Errorf("the Tailscale accounts that sign in by themselves (dashboard.tailscaleLogins): %w", err)
		}
		if !seen[n] {
			seen[n] = true
			r.Logins = append(r.Logins, n)
		}
	}
	sort.Strings(r.Logins)
	return r, nil
}

// NormalizePublicURL checks a public address and returns it as
// scheme://name[:port], with the Host header browsers send for it and
// whether it is https. It must be an http(s) address with a name (not a
// number), no sign-in details, path, query or fragment, and not this
// computer's own loopback name.
func NormalizePublicURL(s string) (normalized, host string, https bool, err error) {
	s = strings.TrimSpace(s)
	u, perr := url.Parse(s)
	if perr != nil || u.Opaque != "" {
		return "", "", false, fmt.Errorf("“%s” isn't a web address (it looks like https://studio.tail1234.ts.net)", s)
	}
	scheme := strings.ToLower(u.Scheme)
	if scheme != "https" && scheme != "http" {
		return "", "", false, fmt.Errorf("“%s” must start with https:// (or http://)", s)
	}
	if u.User != nil {
		return "", "", false, errors.New("a web address with a name or password in it can't be used")
	}
	if (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" || u.ForceQuery {
		return "", "", false, fmt.Errorf("use the computer's address alone, without anything after the name (like %s://%s)", scheme, u.Host)
	}
	name := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if name == "" {
		return "", "", false, fmt.Errorf("“%s” has no computer name in it", s)
	}
	if !dnsName(name) {
		return "", "", false, fmt.Errorf("use the computer's Tailscale name (like https://studio.tail1234.ts.net), not “%s”", name)
	}
	if name == "localhost" || strings.HasSuffix(name, ".localhost") {
		return "", "", false, errors.New("that is this computer's own address, which MetaRush already answers on; use its Tailscale name")
	}
	port := u.Port()
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			return "", "", false, fmt.Errorf("“%s” isn't a port number", port)
		}
		port = strconv.Itoa(n)
		if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
			port = ""
		}
	}
	host = name
	if port != "" {
		host = name + ":" + port
	}
	return scheme + "://" + host, host, scheme == "https", nil
}

// dnsName reports whether s is a DNS name with at least one dot and no
// number-only top label (so not an IPv4 address); IPv6 never gets here.
func dnsName(s string) bool {
	if len(s) > 253 || !strings.Contains(s, ".") {
		return false
	}
	labels := strings.Split(s, ".")
	for _, l := range labels {
		if l == "" || len(l) > 63 || l[0] == '-' || l[len(l)-1] == '-' {
			return false
		}
		for _, c := range l {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	top := labels[len(labels)-1]
	_, err := strconv.Atoi(top)
	return err != nil
}

// NormalizeLogin checks a Tailscale account name (as Tailscale shows it:
// name@example.com, or name@github for GitHub sign-ins) and returns it in
// lower case.
func NormalizeLogin(s string) (string, error) {
	l := strings.ToLower(strings.TrimSpace(s))
	at := strings.LastIndexByte(l, '@')
	ok := len(l) >= 3 && len(l) <= 254 && at > 0 && at < len(l)-1
	for _, c := range l {
		if !ok {
			break
		}
		if unicode.IsSpace(c) || unicode.IsControl(c) || strings.ContainsRune(`"'<>(),;:\/[]`, c) {
			ok = false
		}
	}
	if !ok {
		return "", fmt.Errorf("“%s” doesn't look like a Tailscale account (they look like name@example.com)", strings.TrimSpace(s))
	}
	return l, nil
}

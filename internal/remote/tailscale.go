// Package remote is remote access over Tailscale (D128, D129): people on the
// owner's tailnet open the dashboard from their own computers at an HTTPS
// address that `tailscale serve` provides, while MetaRush keeps listening on
// 127.0.0.1 only. This package reads this computer's Tailscale (status and
// serve settings, never changing them unless a person asks at a terminal),
// keeps the public address and the Tailscale accounts that sign in by
// themselves in config.yaml, and maps those accounts to people in the
// workspace. The dashboard's checks (Host, loopback, headers, cookies) live
// in internal/api.
package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/tools"
)

// Tailscale is what MetaRush reads from this computer's Tailscale.
type Tailscale struct {
	Installed bool `json:"installed"`
	// Running: Tailscale is connected to the tailnet (BackendState Running).
	Running bool   `json:"running"`
	State   string `json:"state,omitempty" jsonschema:"description=Tailscale's own word for its state (Running\\, Stopped\\, NeedsLogin…)."`
	// DNSName is this computer's Tailscale name (MagicDNS), without the trailing dot.
	DNSName string `json:"dnsName,omitempty"`
	// HostName is what the tailnet calls this computer ("Sam's MacBook").
	HostName string `json:"hostName,omitempty"`
	// Login is the Tailscale account this computer is signed in with (empty
	// for a tagged computer, which belongs to no one).
	Login       string   `json:"login,omitempty"`
	DisplayName string   `json:"displayName,omitempty"`
	Tailnet     string   `json:"tailnet,omitempty"`
	CertDomains []string `json:"certDomains"`
	// HTTPSReady: HTTPS certificates are on for the tailnet and cover this computer's name.
	HTTPSReady bool   `json:"httpsReady"`
	Version    string `json:"version,omitempty"`
	// Problem says in plain words what stops remote access on Tailscale's side.
	Problem string `json:"problem,omitempty"`
	// Error is what went wrong reading Tailscale (Details).
	Error string `json:"error,omitempty"`
}

// Serve is what `tailscale serve` does for this dashboard.
type Serve struct {
	// Known: the serve settings could be read.
	Known bool `json:"known"`
	// Active: tailscale serve hands an HTTPS address to this dashboard.
	Active bool `json:"active"`
	// Address is that address (https://name[:port]); Target where it hands requests.
	Address string `json:"address,omitempty"`
	Target  string `json:"target,omitempty"`
	// Funnel: Tailscale Funnel puts the address on the public internet (MetaRush refuses those requests).
	Funnel bool `json:"funnel"`
	// Elsewhere lists other addresses tailscale serve offers on this computer (left alone).
	Elsewhere []string `json:"elsewhere"`
	Error     string   `json:"error,omitempty"`
}

// errRealInTest stops a test from touching the machine's real Tailscale.
var errRealInTest = errors.New("refusing to run the real tailscale in a test (set tools.tailscale to a fake)")

// run runs the tailscale tool. Reads are quiet in the activity log (the
// Settings card asks often); a change is recorded like any program run.
func run(ctx context.Context, t *tools.Set, quiet bool, args ...string) ([]byte, error) {
	if testing.Testing() && !t.Explicit(tools.Tailscale) {
		return nil, errRealInTest
	}
	if quiet {
		ctx = tools.Quiet(ctx)
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	res, err := t.Run(ctx, tools.Spec{Tool: tools.Tailscale, Args: args})
	if err != nil {
		msg := err.Error()
		if res != nil && len(res.Stderr) > 0 {
			msg = strings.TrimSpace(string(res.Stderr))
		}
		return nil, errors.New(msg)
	}
	return res.Stdout, nil
}

type statusJSON struct {
	Version        string   `json:"Version"`
	BackendState   string   `json:"BackendState"`
	CertDomains    []string `json:"CertDomains"`
	MagicDNSSuffix string   `json:"MagicDNSSuffix"`
	CurrentTailnet *struct {
		Name            string `json:"Name"`
		MagicDNSEnabled bool   `json:"MagicDNSEnabled"`
	} `json:"CurrentTailnet"`
	Self *struct {
		HostName string   `json:"HostName"`
		DNSName  string   `json:"DNSName"`
		UserID   int64    `json:"UserID"`
		Tags     []string `json:"Tags"`
	} `json:"Self"`
	User map[string]struct {
		LoginName   string `json:"LoginName"`
		DisplayName string `json:"DisplayName"`
	} `json:"User"`
}

// ReadTailscale reads `tailscale status --json` (it changes nothing).
func ReadTailscale(ctx context.Context, t *tools.Set) Tailscale {
	out := Tailscale{CertDomains: []string{}}
	if !t.Has(tools.Tailscale) {
		out.Problem = "Tailscale isn't installed on this computer. Install it from tailscale.com and sign in to your tailnet."
		return out
	}
	out.Installed = true
	b, err := run(ctx, t, true, "status", "--json")
	if err != nil {
		out.Error = err.Error()
		if errors.Is(err, errRealInTest) {
			out.Problem = "MetaRush can't read Tailscale here."
			return out
		}
		out.Problem = "MetaRush couldn't read Tailscale's status on this computer. Open Tailscale and check that it is connected."
		return out
	}
	return parseStatus(b, out)
}

func parseStatus(b []byte, out Tailscale) Tailscale {
	var st statusJSON
	if err := json.Unmarshal(b, &st); err != nil {
		out.Error = err.Error()
		out.Problem = "MetaRush couldn't read Tailscale's status on this computer."
		return out
	}
	out.Version, out.State = st.Version, st.BackendState
	out.Running = st.BackendState == "Running"
	if st.CurrentTailnet != nil {
		out.Tailnet = st.CurrentTailnet.Name
	}
	if st.Self != nil {
		out.DNSName = strings.TrimSuffix(strings.ToLower(st.Self.DNSName), ".")
		out.HostName = strings.TrimSpace(st.Self.HostName)
		if len(st.Self.Tags) == 0 {
			if u, ok := st.User[strconv.FormatInt(st.Self.UserID, 10)]; ok {
				out.Login = strings.ToLower(strings.TrimSpace(u.LoginName))
				out.DisplayName = strings.TrimSpace(u.DisplayName)
			}
		}
	}
	for _, d := range st.CertDomains {
		d = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(d)), ".")
		if d == "" {
			continue
		}
		out.CertDomains = append(out.CertDomains, d)
		if d == out.DNSName {
			out.HTTPSReady = true
		}
	}
	switch {
	case !out.Running:
		state := out.State
		if state == "" {
			state = "not running"
		}
		out.Problem = fmt.Sprintf("Tailscale isn't connected on this computer (it says “%s”). Open Tailscale and connect to your tailnet.", state)
	case out.DNSName == "":
		out.Problem = "This computer has no Tailscale name because MagicDNS is off for your tailnet. Turn it on in the Tailscale admin console, under DNS."
	case !out.HTTPSReady:
		out.Problem = "HTTPS certificates are off for your tailnet, so Tailscale can't give MetaRush a secure address yet. Turn them on in the Tailscale admin console: DNS, then HTTPS Certificates, then Enable."
	}
	return out
}

type serveJSON struct {
	TCP map[string]struct {
		HTTPS bool `json:"HTTPS"`
	} `json:"TCP"`
	Web map[string]struct {
		Handlers map[string]struct {
			Proxy string `json:"Proxy"`
		} `json:"Handlers"`
	} `json:"Web"`
	AllowFunnel map[string]bool      `json:"AllowFunnel"`
	Foreground  map[string]serveJSON `json:"Foreground"`
}

// ReadServe reads `tailscale serve status --json` and finds the address that
// hands requests to this dashboard's port (it changes nothing).
func ReadServe(ctx context.Context, t *tools.Set, port int) Serve {
	out := Serve{Elsewhere: []string{}}
	if !t.Has(tools.Tailscale) {
		return out
	}
	b, err := run(ctx, t, true, "serve", "status", "--json")
	if err != nil {
		out.Error = err.Error()
		return out
	}
	return parseServe(b, port, out)
}

func parseServe(b []byte, port int, out Serve) Serve {
	var sc serveJSON
	if s := strings.TrimSpace(string(b)); s != "" {
		if err := json.Unmarshal([]byte(s), &sc); err != nil {
			out.Error = err.Error()
			return out
		}
	}
	out.Known = true
	all := []serveJSON{sc}
	for _, fg := range sc.Foreground {
		all = append(all, fg)
	}
	for _, c := range all {
		for hostPort, web := range c.Web {
			for mount, h := range web.Handlers {
				addr := webAddress(hostPort)
				if mount == "/" && proxiesTo(h.Proxy, port) {
					if !out.Active {
						out.Active, out.Address, out.Target = true, addr, h.Proxy
					}
					if c.AllowFunnel[hostPort] || sc.AllowFunnel[hostPort] {
						out.Funnel = true
					}
					continue
				}
				out.Elsewhere = append(out.Elsewhere, strings.TrimSuffix(addr, "/")+mount)
			}
		}
	}
	sort.Strings(out.Elsewhere)
	return out
}

// webAddress turns a serve key ("name:443") into the address people open.
func webAddress(hostPort string) string {
	name, port := hostPort, ""
	if i := strings.LastIndexByte(hostPort, ':'); i > 0 {
		name, port = hostPort[:i], hostPort[i+1:]
	}
	name = strings.TrimSuffix(strings.ToLower(name), ".")
	if port == "" || port == "443" {
		return "https://" + name
	}
	return "https://" + name + ":" + port
}

// proxiesTo reports whether a serve handler's target is this computer's port.
func proxiesTo(target string, port int) bool {
	if target == "" {
		return false
	}
	if n, err := strconv.Atoi(target); err == nil {
		return n == port
	}
	if !strings.Contains(target, "://") {
		target = "http://" + target
	}
	u, err := url.Parse(target)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	h := strings.ToLower(u.Hostname())
	if h != "127.0.0.1" && h != "localhost" && h != "::1" {
		return false
	}
	p, err := strconv.Atoi(u.Port())
	return err == nil && p == port
}

// ServeCommand is the command that makes tailscale serve hand the public
// address to this dashboard (HTTPS on 443 by default).
func ServeCommand(rm config.Remote, port int) string {
	if p := publicPort(rm); p != "" && p != "443" {
		return fmt.Sprintf("tailscale serve --bg --https=%s %d", p, port)
	}
	return fmt.Sprintf("tailscale serve --bg %d", port)
}

// StopCommand turns that handler off again (other serve settings stay).
func StopCommand(rm config.Remote) string {
	p := publicPort(rm)
	if p == "" {
		p = "443"
	}
	return "tailscale serve --https=" + p + " off"
}

func publicPort(rm config.Remote) string {
	if rm.Host == "" {
		return ""
	}
	if i := strings.LastIndexByte(rm.Host, ':'); i > 0 {
		return rm.Host[i+1:]
	}
	if rm.HTTPS {
		return "443"
	}
	return "80"
}

// serveArgs are the arguments of ServeCommand (without "tailscale").
func serveArgs(rm config.Remote, port int) []string {
	args := []string{"serve", "--bg"}
	if p := publicPort(rm); p != "" && p != "443" {
		args = append(args, "--https="+p)
	}
	return append(args, strconv.Itoa(port))
}

// stopArgs are the arguments of StopCommand.
func stopArgs(rm config.Remote) []string {
	p := publicPort(rm)
	if p == "" {
		p = "443"
	}
	return []string{"serve", "--https=" + p, "off"}
}

// StartServe runs ServeCommand. Only `metarush remote enable --apply` calls
// it, for a person at an interactive terminal who said yes.
func StartServe(ctx context.Context, t *tools.Set, rm config.Remote, port int) (string, error) {
	b, err := run(ctx, t, false, serveArgs(rm, port)...)
	return strings.TrimSpace(string(b)), err
}

// StopServe runs StopCommand (`metarush remote disable --apply`, a person only).
func StopServe(ctx context.Context, t *tools.Set, rm config.Remote) (string, error) {
	b, err := run(ctx, t, false, stopArgs(rm)...)
	return strings.TrimSpace(string(b)), err
}

// Package faketailscale is a stand-in `tailscale` command for tests, the
// remote-access end-to-end run and anyone trying `metarush remote` without
// touching a real tailnet. It answers what MetaRush asks — `version`,
// `status --json`, `serve status --json` — and keeps what `serve --bg <port>`,
// `serve --https=443 off` and `serve reset` would change in its state file,
// never in Tailscale.
//
// The state file is METARUSH_FAKE_TAILSCALE_STATE (JSON, see State); without
// one it is a running node with HTTPS certificates on. METARUSH_FAKE_TAILSCALE_LOG
// names a file that gets every call's arguments (one JSON array per line).
package faketailscale

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Version is what the fake reports.
const Version = "1.104.1"

const (
	runEnv   = "METARUSH_FAKE_TAILSCALE"
	stateEnv = "METARUSH_FAKE_TAILSCALE_STATE"
	logEnv   = "METARUSH_FAKE_TAILSCALE_LOG"
)

// State is the pretend node and its serve settings.
type State struct {
	// BackendState is Running, Stopped, NeedsLogin…
	BackendState string `json:"backendState"`
	// DNSName is the node's MagicDNS name, with the trailing dot Tailscale prints.
	DNSName     string `json:"dnsName"`
	HostName    string `json:"hostName"`
	Login       string `json:"login"`
	DisplayName string `json:"displayName"`
	// Certs: HTTPS certificates are turned on for the tailnet (CertDomains).
	Certs bool `json:"certs"`
	// Funnel turns on Tailscale Funnel for the serve address.
	Funnel bool `json:"funnel,omitempty"`
	// Serve is the serve configuration (`tailscale serve status --json`).
	Serve map[string]any `json:"serve,omitempty"`
}

// Default is the node the fake pretends to be without a state file.
func Default() State {
	return State{BackendState: "Running", DNSName: "studio-mac.tail0000.ts.net.", HostName: "Sam's MacBook", Login: "sam@example.com",
		DisplayName: "Sam Lee", Certs: true}
}

// MaybeRun makes a test binary act as the fake when started through Write's
// wrapper. Call it first in TestMain.
func MaybeRun() {
	if os.Getenv(runEnv) != "1" {
		return
	}
	os.Exit(Main(os.Args[1:], os.Stdout, os.Stderr))
}

// Write puts a `tailscale` wrapper in a temp dir that runs this test binary as
// the fake (its TestMain must call MaybeRun) with st as its state, and returns
// the wrapper's path and the state file.
func Write(t testing.TB, st State) (tool, stateFile string) {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	stateFile = filepath.Join(dir, "state.json")
	if err := Save(stateFile, st); err != nil {
		t.Fatal(err)
	}
	tool = filepath.Join(dir, "tailscale")
	q := func(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }
	script := "#!/bin/sh\nexec env " + runEnv + "=1 " + stateEnv + "=" + q(stateFile) + " " + q(exe) + " \"$@\"\n"
	if err := os.WriteFile(tool, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return tool, stateFile
}

// Load reads a state file (the default state when there is none).
func Load(p string) State {
	st := Default()
	if p == "" {
		return st
	}
	if b, err := os.ReadFile(p); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

// Save writes a state file.
func Save(p string, st State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(p, b, 0o600)
}

// Main runs the fake with args and returns the exit code.
func Main(args []string, stdout, stderr io.Writer) int {
	if p := os.Getenv(logEnv); p != "" {
		if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600); err == nil {
			b, _ := json.Marshal(args)
			_, _ = f.Write(append(b, '\n'))
			_ = f.Close()
		}
	}
	statePath := os.Getenv(stateEnv)
	st := Load(statePath)
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: tailscale <command>")
		return 2
	}
	switch args[0] {
	case "version":
		fmt.Fprintf(stdout, "%s\n  tailscale commit: fake\n  go version: fake\n", Version)
		return 0
	case "status":
		if !has(args, "--json") {
			fmt.Fprintln(stdout, "100.101.102.103  "+strings.TrimSuffix(st.DNSName, "."))
			return 0
		}
		return writeJSON(stdout, status(st))
	case "serve":
		return serve(args[1:], st, statePath, stdout, stderr)
	}
	fmt.Fprintf(stderr, "the fake tailscale doesn't know %q\n", args[0])
	return 2
}

func has(args []string, flag string) bool {
	for _, a := range args {
		if a == flag {
			return true
		}
	}
	return false
}

func writeJSON(w io.Writer, v any) int {
	b, _ := json.MarshalIndent(v, "", "  ")
	fmt.Fprintln(w, string(b))
	return 0
}

// status mirrors the fields of `tailscale status --json` MetaRush reads.
func status(st State) map[string]any {
	suffix := ""
	if i := strings.Index(strings.TrimSuffix(st.DNSName, "."), "."); i > 0 {
		suffix = strings.TrimSuffix(st.DNSName, ".")[i+1:]
	}
	out := map[string]any{
		"Version":        Version + "-fake",
		"BackendState":   st.BackendState,
		"MagicDNSSuffix": suffix,
		"CurrentTailnet": map[string]any{"Name": st.Login, "MagicDNSSuffix": suffix, "MagicDNSEnabled": st.DNSName != ""},
		"Self": map[string]any{"HostName": st.HostName, "DNSName": st.DNSName, "OS": "macOS", "UserID": 1001, "Online": st.BackendState == "Running",
			"TailscaleIPs": []string{"100.101.102.103", "fd7a:115c:a1e0::1"}},
		"User": map[string]any{"1001": map[string]any{"ID": 1001, "LoginName": st.Login, "DisplayName": st.DisplayName, "ProfilePicURL": ""}},
	}
	if st.Certs && st.DNSName != "" {
		out["CertDomains"] = []string{strings.TrimSuffix(st.DNSName, ".")}
	} else {
		out["CertDomains"] = nil
	}
	return out
}

func serve(args []string, st State, statePath string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "status" {
		cfg := st.Serve
		if cfg == nil {
			cfg = map[string]any{}
		}
		if !has(args, "--json") {
			if len(cfg) == 0 {
				fmt.Fprintln(stdout, "No serve config")
			} else {
				b, _ := json.Marshal(cfg)
				fmt.Fprintln(stdout, string(b))
			}
			return 0
		}
		return writeJSON(stdout, cfg)
	}
	if len(args) > 0 && args[0] == "reset" {
		st.Serve = nil
		return saveOr(statePath, st, stderr)
	}
	port := "443"
	var rest []string
	for _, a := range args {
		switch {
		case a == "--bg" || a == "--yes":
		case strings.HasPrefix(a, "--https="):
			port = strings.TrimPrefix(a, "--https=")
		case strings.HasPrefix(a, "-"):
			fmt.Fprintf(stderr, "the fake tailscale doesn't know %s\n", a)
			return 2
		default:
			rest = append(rest, a)
		}
	}
	host := strings.TrimSuffix(st.DNSName, ".") + ":" + port
	if len(rest) == 1 && rest[0] == "off" {
		if web, _ := st.Serve["Web"].(map[string]any); web != nil {
			delete(web, host)
			if len(web) == 0 {
				delete(st.Serve, "Web")
			}
		}
		if tcp, _ := st.Serve["TCP"].(map[string]any); tcp != nil {
			delete(tcp, port)
			if len(tcp) == 0 {
				delete(st.Serve, "TCP")
			}
		}
		if len(st.Serve) == 0 {
			st.Serve = nil
		}
		return saveOr(statePath, st, stderr)
	}
	if len(rest) != 1 {
		fmt.Fprintln(stderr, "Error: invalid argument format")
		return 2
	}
	if !st.Certs {
		// The real one offers to turn HTTPS on in the admin console and waits.
		fmt.Fprintln(stderr, "error enabling https feature: HTTPS certificates are not enabled for this tailnet")
		return 1
	}
	target := rest[0]
	if _, err := strconv.Atoi(target); err == nil {
		target = "http://127.0.0.1:" + target
	}
	if st.Serve == nil {
		st.Serve = map[string]any{}
	}
	st.Serve["TCP"] = map[string]any{port: map[string]any{"HTTPS": true}}
	st.Serve["Web"] = map[string]any{host: map[string]any{"Handlers": map[string]any{"/": map[string]any{"Proxy": target}}}}
	if st.Funnel {
		st.Serve["AllowFunnel"] = map[string]any{host: true}
	}
	if code := saveOr(statePath, st, stderr); code != 0 {
		return code
	}
	fmt.Fprintf(stdout, "Available within your tailnet:\n\nhttps://%s/\n|-- proxy %s\n\nServe started and running in the background.\nTo disable the proxy, run: tailscale serve --https=%s off\n", strings.TrimSuffix(st.DNSName, "."), target, port)
	return 0
}

func saveOr(p string, st State, stderr io.Writer) int {
	if p == "" {
		return 0
	}
	if err := Save(p, st); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

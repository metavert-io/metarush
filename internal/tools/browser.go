package tools

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/metavert-io/metarush/internal/wsl"
)

// Opening a web address in the person's browser on this computer: the
// dashboard's one-time sign-in link (`metarush dashboard`, setup) and an
// organization's sign-in test. On a Mac `open`; inside WSL Windows' default
// browser (D518) — wslview when it is installed, else Windows' own cmd.exe
// through WSL's interop, `cmd.exe /c start <address>` with the address
// checked and cmd's special characters escaped (never a shell, never a
// browser inside Ubuntu); elsewhere on Linux xdg-open. The address carries a
// one-time sign-in, so the run is kept out of the activity log (Quiet).
// Under go test only stand-ins in METARUSH_TOOL_DIRS ever run.

// ErrNoOpener means nothing here can open a browser (WSL without interop, a
// computer without a desktop): the person opens the printed address.
var ErrNoOpener = errors.New("no program here opens a browser")

// BrowserOpener is the program that opens a web address here ("open",
// "wslview", "cmd.exe", "xdg-open"), or "" when none can.
func (s *Set) BrowserOpener() string {
	w := wsl.Here()
	switch {
	case runtime.GOOS == "darwin" && s.Has(Open):
		return Open
	case w.In() && w.Interop && s.Has(WSLView):
		return WSLView
	case w.In() && w.Interop && s.Has(CmdExe):
		return CmdExe
	case w.In():
		return "" // a browser inside Ubuntu (WSLg) isn't the one people use
	case runtime.GOOS == "linux" && s.Has(XDGOpen):
		return XDGOpen
	}
	return ""
}

// OpenInBrowser opens raw — http on this computer (127.0.0.1, localhost) or
// https — with BrowserOpener, and says which program did.
func (s *Set) OpenInBrowser(ctx context.Context, raw string) (string, error) {
	u, err := BrowserAddress(raw)
	if err != nil {
		return "", err
	}
	tool := s.BrowserOpener()
	if tool == "" {
		return "", ErrNoOpener
	}
	if err := s.openerAllowed(tool); err != nil {
		return "", err
	}
	spec := Spec{Tool: tool, Args: []string{u}, Quiet: true}
	switch tool {
	case CmdExe:
		spec = cmdStart(u)
	case XDGOpen:
		// xdg-open may stay until the browser it started ends: it is left
		// running on its own, as before.
		_, err := s.Start(spec)
		return tool, err
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	_, err = s.Run(ctx, spec)
	return tool, err
}

// openerAllowed refuses, under go test, any opener but a stand-in (one a
// test pointed at with Use, or found in METARUSH_TOOL_DIRS): tests never
// open a real browser.
func (s *Set) openerAllowed(tool string) error {
	if !testing.Testing() {
		return nil
	}
	s.mu.Lock()
	pointed, p := s.pointed[tool], s.paths[tool]
	s.mu.Unlock()
	if pointed {
		return nil
	}
	for _, d := range onlyDirs() {
		if filepath.Dir(p) == filepath.Clean(d) {
			return nil
		}
	}
	return fmt.Errorf("refusing to run the real %s in a test (point it at a stand-in)", tool)
}

// addressChars are the characters an address may have to be handed to an
// opener: letters, digits and the URL characters MetaRush's own addresses
// use. Spaces, quotes, $, `, ;, commas, ^, |, <, >, (, ), ! and # never pass
// (cmd.exe and the shells behind wslview read them).
const addressChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~/?=&%:+@[]"

// BrowserAddress checks an address before it is opened: https, or http on
// this computer; no sign-in in it; only addressChars.
func BrowserAddress(raw string) (string, error) {
	if raw == "" || strings.HasPrefix(raw, "-") {
		return "", fmt.Errorf("refusing to open %q", raw)
	}
	for _, r := range raw {
		if !strings.ContainsRune(addressChars, r) {
			return "", fmt.Errorf("refusing to open an address with %q in it", r)
		}
	}
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Host == "" {
		return "", fmt.Errorf("refusing to open %q", raw)
	}
	switch h := u.Hostname(); {
	case u.Scheme == "https":
	case u.Scheme == "http" && (h == "127.0.0.1" || h == "localhost" || h == "::1"):
	default:
		return "", fmt.Errorf("refusing to open %q: only https, or http on this computer", raw)
	}
	return raw, nil
}

// cmdStart is `cmd.exe /c start <address>`. WSL hands an argument to Windows
// as it is unless it has a space, a tab, a line break or a quote (which no
// checked address has), so cmd.exe reads exactly what is written here: its
// special characters get a caret first ("&" would otherwise end the command
// and start another). It runs from the C: drive, so cmd.exe doesn't complain
// about starting in one of Ubuntu's folders.
func cmdStart(address string) Spec {
	spec := Spec{Tool: CmdExe, Args: []string{"/c", "start", CmdEscape(address)}, Quiet: true}
	if c, err := wsl.Here().LinuxPath(`C:\`); err == nil {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			spec.Dir = c
		}
	}
	return spec
}

// CmdEscape puts a caret before every character cmd.exe treats as special
// on its command line.
func CmdEscape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune(`^&|<>()%!"`, r) {
			b.WriteByte('^')
		}
		b.WriteRune(r)
	}
	return b.String()
}

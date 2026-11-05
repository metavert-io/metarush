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
	"unicode"

	"github.com/metavert-io/metarush/internal/wsl"
)

// Opening a web address in the person's browser on this computer: the
// dashboard's one-time sign-in link (`metarush dashboard`, setup) and an
// organization's sign-in test. On a Mac `open`; inside WSL Windows' default
// browser (D518) — wslview when it is installed, else Windows' own
// rundll32.exe from where Windows keeps it, through WSL's interop:
// `rundll32.exe url.dll,FileProtocolHandler <address>` hands the address to
// Windows as if it were clicked, and no command interpreter ever reads it
// (never a shell, never cmd.exe, never a browser inside Ubuntu); elsewhere on
// Linux xdg-open. The address carries a one-time sign-in, so the run is kept
// out of the activity log (Quiet). Under go test only stand-ins in
// METARUSH_TOOL_DIRS ever run.

// ErrNoOpener means nothing here can open a browser (WSL without interop, a
// computer without a desktop): the person opens the printed address.
var ErrNoOpener = errors.New("no program here opens a browser")

// BrowserOpener is the program that opens a web address here ("open",
// "wslview", "rundll32.exe", "xdg-open"), or "" when none can.
func (s *Set) BrowserOpener() string {
	w := wsl.Here()
	switch {
	case runtime.GOOS == "darwin" && s.Has(Open):
		return Open
	case w.In() && w.Interop && s.Has(WSLView):
		return WSLView
	case w.In() && w.Interop && s.Has(Rundll32):
		return Rundll32
	case w.In():
		return "" // a browser inside Ubuntu (WSLg) isn't the one people use
	case runtime.GOOS == "linux" && s.Has(XDGOpen):
		return XDGOpen
	}
	return ""
}

// OpenInBrowser opens raw — http on this computer (127.0.0.1, localhost) or
// https — with BrowserOpener, and says which program did. Windows' openers
// take only addresses whose every character is plain to them, on this
// computer only at 127.0.0.1 (BrowserAddress); `open` and xdg-open take any
// such address as one argument.
func (s *Set) OpenInBrowser(ctx context.Context, raw string) (string, error) {
	u, err := browserURL(raw)
	if err != nil {
		return "", err
	}
	tool := s.BrowserOpener()
	if tool == "" {
		return "", ErrNoOpener
	}
	if tool == Rundll32 || tool == WSLView {
		if u, err = BrowserAddress(raw); err != nil {
			return "", err
		}
	}
	if err := s.openerAllowed(tool); err != nil {
		return "", err
	}
	spec := Spec{Tool: tool, Args: []string{u}, Quiet: true}
	switch tool {
	case Rundll32:
		spec = rundllOpen(u)
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

// addressChars are the characters an address may have to be handed to a
// Windows opener: letters, digits and the URL characters MetaRush's own
// addresses use. Spaces, quotes, $, `, ;, commas, ^, |, <, >, (, ), ! and #
// never pass (the shell behind wslview reads them, and WSL quotes an argument
// with a space or a quote, which Windows would then read differently).
const addressChars = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-._~/?=&%:+@[]"

// BrowserAddress checks an address before a Windows opener gets it: what
// browserURL checks, only addressChars, and on this computer only 127.0.0.1 —
// never localhost or [::1]: Windows' browser tries [::1] first for
// localhost, where any program on Windows (and, with WSL's mirrored
// networking, in Ubuntu) may listen and would get the one-time sign-in.
func BrowserAddress(raw string) (string, error) {
	for _, r := range raw {
		if !strings.ContainsRune(addressChars, r) {
			return "", fmt.Errorf("refusing to open an address with %q in it", r)
		}
	}
	u, err := browserURL(raw)
	if err != nil {
		return "", err
	}
	if p, err := url.Parse(raw); err != nil || p.Scheme == "http" && p.Hostname() != "127.0.0.1" {
		return "", fmt.Errorf("refusing to open %q in Windows' browser: on this computer only 127.0.0.1", raw)
	}
	return u, nil
}

// browserURL checks an address before it is opened: https, or http on this
// computer; no sign-in in it; nothing an opener could take for an option,
// and no space or control character.
func browserURL(raw string) (string, error) {
	if raw == "" || strings.HasPrefix(raw, "-") || strings.ContainsFunc(raw, func(r rune) bool { return r <= ' ' || r == 0x7f }) {
		return "", fmt.Errorf("refusing to open %q", raw)
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

// rundllOpen is `rundll32.exe url.dll,FileProtocolHandler <target>`: Windows
// opens target as if it were clicked (an address in the default browser, a
// page with what opens web pages), and no command interpreter reads it —
// rundll32 hands the rest of its command line to url.dll as it is, and WSL
// hands an argument on as it is unless it has a space, a tab, a line break or
// a quote, which no checked target has (BrowserAddress, plainPagePath). It
// runs from the C: drive, not one of Ubuntu's folders.
func rundllOpen(target string) Spec {
	spec := Spec{Tool: Rundll32, Args: []string{"url.dll,FileProtocolHandler", target}, Quiet: true}
	if c, err := wsl.Here().LinuxPath(`C:\`); err == nil {
		if st, err := os.Stat(c); err == nil && st.IsDir() {
			spec.Dir = c
		}
	}
	return spec
}

// openPageDir is the private folder for OpenURL's page. Inside WSL it is in
// /tmp whatever TMPDIR says, so the page's path holds only letters, digits,
// "-", "." and "_" (os.MkdirTemp names it with digits), which a Windows
// opener reads as they are.
func openPageDir() (string, error) {
	if wsl.Here().In() {
		return os.MkdirTemp("/tmp", "metarush-open-")
	}
	return os.MkdirTemp("", "metarush-open-")
}

// plainPagePath reports whether a page's path (Linux's or Windows' name for
// it) holds only ASCII letters and digits, "-", ".", "_" and folder
// separators.
func plainPagePath(p string) bool {
	if p == "" {
		return false
	}
	for _, r := range p {
		if r >= 0x80 || !(unicode.IsLetter(r) || unicode.IsDigit(r) || strings.ContainsRune(`-._/\`, r)) {
			return false
		}
	}
	return true
}

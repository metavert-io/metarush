package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/firstrun"
	"github.com/metavert-io/metarush/internal/tools"
	"github.com/metavert-io/metarush/internal/wsl"
)

// MetaRush inside WSL on Windows (D515–D521): what setup, init and doctor
// say and refuse. The words are internal/wsl's, so every surface says the
// same.

// refuseWSL1 stops setup and init on WSL 1, where MetaRush's database and
// many of its tools don't work, saying how to move to WSL 2 (D516).
func refuseWSL1(c *cobra.Command) error {
	w := wsl.Here()
	if w.Version != 1 {
		return nil
	}
	n := w.WSL1()
	return failErr(c, apperr.New(apperr.NotConfigured, "%s", n.Says).WithHint(n.Next).
		WithDetails(map[string]any{"reason": "wsl1", "kernel": w.Kernel, "distro": w.Distro}).WithCommand(n.Command))
}

// wslPlaces are the folders MetaRush uses on this computer that could be on
// the Windows side (config's WindowsPlaces).
func wslPlaces(cfg *config.Config) []wsl.Place { return cfg.WindowsPlaces() }

// printWindows prints the report for people: the headline, then each note
// (✓ ready, · something to know or do, → what to do); --details adds each
// note's command.
func printWindows(r *wsl.Report, details bool) {
	if r == nil {
		return
	}
	fmt.Println("\n" + r.Says)
	for _, n := range r.Notes {
		mark := "·"
		if n.Ready {
			mark = "✓"
		}
		fmt.Printf("  %s %s\n", mark, n.Says)
		if !n.Ready && n.Next != "" {
			fmt.Printf("      → %s\n", n.Next)
		}
		if details && !n.Ready && n.Command != "" {
			for _, line := range strings.Split(n.Command, "\n") {
				fmt.Printf("      %s\n", line)
			}
		}
	}
}

// wslChecks are doctor's checks inside WSL (area "windows"): which WSL, the
// background service, Ubuntu staying up, the Windows browser, the network,
// Claude Code and Ubuntu's tools found only on Windows, and MetaRush's
// folders on the Windows side.
func wslChecks(cfg *config.Config, ts *tools.Set, add addCheck) {
	w := wsl.Here()
	if !w.In() {
		return
	}
	detail := fmt.Sprintf("WSL %d, %s, kernel %s, interop %s, systemd %s", w.Version, orText(w.Distro, "distribution unknown"), w.Kernel,
		onOffWord(w.Interop), map[bool]string{true: "running", false: map[bool]string{true: "on in wsl.conf, not running yet", false: "off"}[w.SystemdAsked]}[w.Systemd])
	if w.Version == 1 {
		n := w.WSL1()
		add("windows", "wsl", "fail", detail, n.Command, strings.TrimSuffix(n.Says, "."), n.Next)
		return
	}
	add("windows", "wsl", "ok", detail, "", strings.TrimSuffix(w.Headline(), "."))
	if cfg == nil || cfg.Dashboard.Host == "127.0.0.1" || cfg.Dashboard.Host == "localhost" {
		// D519: WSL brings Windows' browser to 127.0.0.1 here; nothing listens
		// on WSL's own address or the network, mirrored networking or not.
		add("windows", "loopback", "ok", "dashboard on 127.0.0.1 only; Windows reaches it through WSL's localhost forwarding (NAT) or shared loopback (mirrored)", "",
			"The dashboard opens only on this PC: your Windows browser reaches it through WSL, and other computers can't")
	}
	r := wsl.ReportFor(w, wsl.HereWindowsSettings(), wslPlaces(cfg))
	for _, n := range r.Notes {
		status := "ok"
		switch {
		case n.Ready:
		case n.Key == "browser" || n.Key == "network":
			status = "warn"
		case n.Key == "files":
			status = "warn"
			if strings.HasPrefix(n.Command, wsl.PlaceWatched) || strings.HasPrefix(n.Command, wsl.PlaceLinked) {
				status = "info"
			}
		default:
			status = "info"
		}
		name := n.Key
		if n.Key == "files" {
			name = "on the Windows side"
		}
		add("windows", name, status, firstNonEmpty(n.Command, n.Says), n.Command, strings.TrimSuffix(n.Says, "."), n.Next)
	}
	if ts != nil {
		// Claude Code installed on Windows only (an npm script on Windows'
		// PATH): named, never run (D518).
		if win := ts.WindowsCopy(tools.Claude); win != "" {
			d := w.DistroWord() // what every other line calls it ("Ubuntu", not WSL's "Ubuntu-24.04")
			add("windows", "claude code on Windows", "info", "only a Windows copy on PATH: "+win+" (left alone)", "install Claude Code in "+d+": "+firstrun.InstallCommand,
				"Claude Code is installed on Windows, but MetaRush in "+d+" can't use Windows' copy", "Install Claude Code in "+d+" too, then open it there once and sign in.")
		}
	}
}

func onOffWord(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

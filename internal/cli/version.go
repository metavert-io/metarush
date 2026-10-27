package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/version"
)

func versionString() string { return version.Version }

// versionOut is `metarush version --json`: the build (the same fields as
// /api/health and Settings → General) and the cut format it reads and writes.
type versionOut struct {
	version.Info
	EditSpec int `json:"editSpec"`
}

func versionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the version (--details: what's new in it, and the version each of your computers runs)",
		Long: "Print this MetaRush's version, the way Settings → General heads it.\n\n" +
			"--details also shows the exact build (commits past the release, commit, build time), what's new in this\n" +
			"version (from its changelog) and the version each computer in the workspace runs, older ones flagged.\n" +
			"--json is the build alone and needs no workspace (scripts check it); --details --json is everything (op version.show).",
		RunE: func(c *cobra.Command, _ []string) error {
			b := version.Current()
			if !detailsMode(c) {
				return emit(c, versionOut{Info: b, EditSpec: editspec.SchemaVersion}, func() {
					fmt.Println(b.Plain())
					fmt.Println("(Add --details to see what's new in it and the version each of your computers runs.)")
				})
			}
			out := aboutHere(c)
			return emit(c, out, func() { printAbout(out) })
		},
	}
}

// aboutHere is version.show for this home's workspace, or, when MetaRush isn't
// set up here or the workspace can't be reached, the version and what's new
// with the reason the computers are missing (a version check never fails).
func aboutHere(c *cobra.Command) *ops.VersionShowOut {
	ctx, cancel := context.WithTimeout(c.Context(), 15*time.Second)
	defer cancel()
	out, _ := ops.VersionAbout(ctx, nil)
	h, err := home()
	if err != nil || !config.Exists(h) {
		out.ComputersError = "MetaRush isn't set up on this computer yet, so it has no workspace to list computers from."
		return out
	}
	via := "cli"
	if assistantMode() {
		via = models.ViaAssistant
	}
	a, err := app.Open(ctx, h, via)
	if err != nil {
		out.ComputersError = "MetaRush couldn't reach the workspace to list your computers: " + err.Error()
		return out
	}
	defer a.Close()
	o, err := ops.CallTyped[ops.VersionShowOut](ctx, a, "version.show", ops.VersionShowIn{}, ops.Caller{})
	if err != nil {
		out.ComputersError = "MetaRush couldn't list your computers: " + err.Error()
		return out
	}
	return o
}

// printAbout is `metarush version --details`: Settings → General as text.
func printAbout(o *ops.VersionShowOut) {
	fmt.Println(o.Plain())
	exact := "version " + o.Version
	if o.DevCount != nil {
		exact += fmt.Sprintf(": %s past %s", plural(*o.DevCount, "commit"), o.Release)
	} else if o.Dev {
		exact += ": a build from source, past " + o.Release + " by an unknown number of commits"
	}
	fmt.Printf("%s · commit %s · built %s · cut format %d\n", exact, o.Commit, o.Date, o.EditSpec)

	fmt.Println("\nYour computers")
	if o.ComputersError != "" {
		fmt.Println("  " + o.ComputersError)
	}
	for _, m := range o.Computers {
		line := "  " + theirs(m.Name)
		if m.This {
			line += " (this computer)"
		}
		line += " · " + osWords(m.OS)
		switch {
		case m.State == models.InstallationInvited:
			line += " · invited, not joined yet"
		case m.Version == "":
			line += " · its version isn't known yet"
		default:
			line += " · MetaRush " + m.Release
			switch m.Standing {
			case "older":
				line += ", older than this one: update MetaRush there"
			case "newer":
				line += ", newer than this one"
			}
		}
		if m.LastSeenAt != nil {
			line += " · seen " + plainWhen(*m.LastSeenAt)
		}
		fmt.Println(line)
		fmt.Printf("      %s, commit %s, built %s · %s/%s %s · %s\n", orText(m.Version, "-"), orText(m.Commit, "-"), orText(m.Built, "-"), m.OS, m.Arch, m.Hostname, m.ID)
	}
	if o.Older > 0 {
		fmt.Println("  To update one: on that computer brew upgrade --cask metarush, then metarush service restart;")
		fmt.Println("  or from a checkout, scripts/deploy.sh --host <its name> (docs/deployment.md).")
	}

	for _, s := range o.WhatsNew {
		switch {
		case s.Release == "":
			fmt.Println("\nComing in the next version")
		case s.Release == o.Release && !o.Dev:
			fmt.Printf("\nWhat's new in this version (%s)\n", s.Heading)
		default:
			fmt.Printf("\nWhat's new in %s\n", s.Heading)
		}
		for _, e := range s.Entries {
			lines := strings.Split(e.Text, "\n")
			for i, l := range lines {
				switch {
				case i == 0:
					lines[i] = "- " + l
				case l != "":
					lines[i] = "  " + l
				}
			}
			fmt.Println(strings.Join(lines, "\n"))
		}
	}
}

package cli

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// motionCmd is `metarush motion`: project motion graphics (D68) — components a
// project carries in its folder, checked by MetaRush and approved by a person
// per version before anything renders them.
func motionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "motion",
		Short: "Project motion graphics: start, check, list, show, approve (people only) and preview components in motion/<name>",
		Long: `A project can carry its own motion graphics: Remotion components in motion/<name>/index.tsx
(default export the component; schema = its parameters in zod; meta = summary, region,
minDuration, maxDuration) with sample parameters in preview.json. Vetted templates
(metarush edit templates) stay the default; use a motion graphic when none fits.

  metarush motion new route-map            start one from a working example
  metarush motion check route-map          check it: imports, types, test frames, preview
  metarush motion approve route-map@3fa9c2d1   a person approves that exact version
  metarush motion studio route-map         preview it in Remotion Studio (your browser)

A cut uses an approved version in its cut sheet's ## Graphics:
  - route: motion route-map@3fa9c2d1 at 0:12 for 4 s · with from "Lisbon", to "Porto"
Changing any file makes a new version, which needs approval again.`,
	}
	cmd.AddCommand(motionNewCmd(), motionCheckCmd(), motionListCmd(), motionShowCmd(), motionApproveCmd(), motionRevokeCmd(), motionStudioCmd())
	return cmd
}

func motionNewCmd() *cobra.Command {
	var in ops.MotionNewIn
	var details bool
	c := &cobra.Command{
		Use: "new <name> [--dir <project folder>]", Short: "Start a motion graphic in motion/<name> from a working example", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Name = args[0]
			return runOp(c, "motion.new", in, func(o any) {
				r := o.(*ops.MotionNewOut)
				fmt.Println(r.Summary)
				if details {
					fmt.Println("folder:", r.Dir)
					fmt.Println("next:", r.Next)
				}
			})
		},
	}
	c.Flags().StringVar(&in.Dir, "dir", "", "where to put motion/<name> (default: the current folder)")
	addDetails(c, &details)
	return c
}

func motionCheckCmd() *cobra.Command {
	var in ops.MotionCheckIn
	var details bool
	var libraries []string
	c := &cobra.Command{
		Use:   "check <folder | name> | --version name@version [--library name@version]… [--project <project>] [--shape vertical|landscape|square] [--duration <s>] [--no-preview]",
		Short: "Check a motion graphic: its imports (parsed), types, bundle, test frames and a short preview, drawn with no network; a passing version waits for a person's approval",
		Long: `Check a motion graphic. <folder> is the component's folder (the one with index.tsx); a bare
name means motion/<name> in the project folder you are in. MetaRush reads the files into
one archive whose SHA-256 is the version, refuses imports outside remotion, react, zod,
@remotion/zod-types, @remotion/shapes, @remotion/paths, @remotion/layout-utils,
@remotion/transitions, @metarush/motion and the component's own files, type-checks it,
bundles it and draws test frames (and a short preview video) in a headless browser that
can't reach the network. Problems name the file and line. A passing version is recorded
so a person can review and approve it; nothing renders it before that.

--version name@version checks a version MetaRush keeps but hasn't checked in this workspace:
one brought in with a file from another workspace, with --library name@version for each
shared library it should be drawn with (the versions the file brought).

--json gives the contact sheet and frame paths (images to look at), the parameters as
JSON Schema and every problem with its code.`,
		Args: cobra.RangeArgs(0, 1),
		RunE: func(c *cobra.Command, args []string) error {
			for _, l := range libraries {
				name, hash, ok := strings.Cut(l, "@")
				if !ok || name == "" || hash == "" {
					return fail(c, apperr.InvalidInput, "Give each --library as name@version.", "")
				}
				if in.Libraries == nil {
					in.Libraries = map[string]string{}
				}
				in.Libraries[name] = hash
			}
			if in.Version != "" {
				if len(args) > 0 {
					return fail(c, apperr.InvalidInput, "Give a folder or --version, not both.", "")
				}
				return runOp(c, "motion.check", in, func(o any) { printMotionCheck(o.(*ops.MotionCheckOut), details) })
			}
			if len(args) == 0 {
				return fail(c, apperr.InvalidInput, "Say which motion graphic to check: its folder, its name, or --version name@version.", "")
			}
			if st, err := os.Stat(args[0]); err == nil && st.IsDir() {
				in.Dir = args[0]
			} else if strings.ContainsAny(args[0], "/\\.") {
				in.Dir = args[0]
			} else {
				in.Name = args[0]
			}
			if in.Dir != "" {
				if err := assistantPath(in.Dir); err != nil {
					return fail(c, apperr.Unauthorized, err.Error(), "")
				}
			}
			return runOp(c, "motion.check", in, func(o any) { printMotionCheck(o.(*ops.MotionCheckOut), details) })
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "project whose shape, platform and brand the test frames use (default: the project folder's)")
	c.Flags().StringVar(&in.Shape, "shape", "", "vertical, landscape or square (default: the project's cut, else vertical)")
	c.Flags().Float64Var(&in.Duration, "duration", 0, "seconds to run the graphic for in the test render (kept within its meta)")
	c.Flags().BoolVar(&in.NoPreview, "no-preview", false, "skip the preview video (faster while iterating)")
	c.Flags().StringVar(&in.Version, "version", "", "a version MetaRush keeps but hasn't checked here (brought in from another workspace): name@version")
	c.Flags().StringArrayVar(&libraries, "library", nil, "name@version: a shared library to draw it with (repeatable)")
	addDetails(c, &details)
	return c
}

func printMotionCheck(r *ops.MotionCheckOut, details bool) {
	fmt.Println(r.Summary)
	for _, p := range r.Problems {
		where := ""
		if p.File != "" {
			where = p.File
			if p.Line > 0 {
				where += fmt.Sprintf(" line %d", p.Line)
			}
			where += ": "
		}
		kind := "Problem"
		if p.Severity == "warning" {
			kind = "Note"
		}
		fmt.Printf("  %s: %s%s\n", kind, where, p.Message)
		if p.Hint != "" {
			fmt.Printf("    %s\n", p.Hint)
		}
		if details {
			fmt.Printf("    (%s)\n", p.Code)
		}
	}
	if r.OK && r.State == models.MotionWaiting {
		fmt.Println("Its test frames and preview are in the dashboard under Motion graphics, ready for a person to review.")
	}
	if details {
		fmt.Println("version:", r.Version, "("+r.Hash+")")
		if r.Sheet != "" {
			fmt.Println("contact sheet:", r.Sheet)
		}
		if r.Preview != "" {
			fmt.Println("preview:", r.Preview)
		}
		var imps []string
		for _, i := range r.Imports {
			imps = append(imps, i.Specifier)
		}
		if len(imps) > 0 {
			fmt.Println("imports:", strings.Join(imps, ", "))
		}
		if len(r.Ignored) > 0 {
			fmt.Println("left out (hidden files):", strings.Join(r.Ignored, ", "))
		}
		if r.URL != "" {
			fmt.Println("review:", r.URL)
		}
		if r.Next != "" {
			fmt.Println("next:", r.Next)
		}
	}
}

func motionListCmd() *cobra.Command {
	var in ops.MotionListIn
	var details bool
	c := &cobra.Command{
		Use: "list [--all] [--state waiting|approved|rejected|revoked]", Short: "List the motion graphics checked in this workspace and whether a person approved them",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "motion.list", in, func(o any) {
				r := o.(*ops.MotionListOut)
				if len(r.Components) == 0 {
					fmt.Println("No motion graphics yet.")
					if details {
						fmt.Println("start one: metarush motion new <name>")
					}
					return
				}
				width := 0
				for _, m := range r.Components {
					width = max(width, len(m.Name))
				}
				for _, m := range r.Components {
					fmt.Printf("%-*s  %-21s checked %s", width, m.Name, m.StateLabel, plainWhen(m.CheckedAt))
					if m.Summary != "" {
						fmt.Printf(" · %s", m.Summary)
					}
					fmt.Println()
					if details {
						fmt.Printf("    version %s (%s) · region %s · %.1f–%.1f s · %d note(s) · %s\n", m.Version, m.Hash, m.Region, m.MinDuration, m.MaxDuration, m.Warnings, m.URL)
					}
				}
				if r.Waiting > 0 {
					fmt.Printf("%s waiting for a person.\n", plural(r.Waiting, "version"))
				}
			})
		},
	}
	c.Flags().BoolVar(&in.All, "all", false, "every version, not only the latest of each name")
	c.Flags().StringVar(&in.State, "state", "", "only versions in this state")
	addDetails(c, &details)
	return c
}

func motionShowCmd() *cobra.Command {
	var source, details bool
	c := &cobra.Command{
		Use: "show <name[@version] | version> [--source]", Short: "Show a motion graphic version: what it does, its imports, parameters, notes, decisions and (with --source) its files", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "motion.show", ops.MotionShowIn{Ref: args[0]}, func(o any) { printMotionShow(o.(*ops.MotionShowOut), source, details) })
		},
	}
	c.Flags().BoolVar(&source, "source", false, "print the source files (changes since the last approved version are marked)")
	addDetails(c, &details)
	return c
}

func printMotionShow(r *ops.MotionShowOut, source, details bool) {
	m := r.Row
	fmt.Printf("%s — %s\n", m.Name, m.StateLabel)
	if m.Summary != "" {
		fmt.Println(m.Summary)
	}
	fmt.Printf("Shows in the %s of the frame, for %.1f to %.1f seconds.\n", map[string]string{"lower": "lower band", "center": "middle", "full": "whole"}[m.Region], m.MinDuration, m.MaxDuration)
	if len(r.Fields) > 0 {
		fmt.Println("Fields:")
		for _, f := range r.Fields {
			req := ""
			if f.Required {
				req = ", required"
			}
			line := fmt.Sprintf("  %s (%s%s)", f.Name, f.Type, req)
			if f.Description != "" {
				line += " — " + f.Description
			}
			fmt.Println(line)
		}
	}
	var imps, own []string
	for _, i := range r.Component.Imports {
		switch i.Kind {
		case "relative":
			own = append(own, strings.TrimPrefix(i.Specifier, "./"))
		case "metarush":
			imps = append(imps, "MetaRush's helpers")
		default:
			imps = append(imps, i.Specifier)
		}
	}
	if len(own) > 0 {
		imps = append(imps, "its own files ("+strings.Join(dedupe(own), ", ")+")")
	}
	if len(imps) > 0 {
		fmt.Println("Uses:", strings.Join(dedupe(imps), ", "))
	}
	for _, w := range r.Component.Warnings {
		fmt.Println("  Note:", w.Message)
	}
	fmt.Printf("Checked %s by %s.\n", plainWhen(m.CheckedAt), orSomeone(m.CheckedBy))
	for _, d := range r.Component.Decisions {
		verb := map[string]string{models.MotionApproved: "Approved", models.MotionRejected: "Turned down", models.MotionRevoked: "Approval withdrawn"}[d.State]
		line := fmt.Sprintf("%s %s by %s", verb, plainWhen(d.At), orSomeone(d.By.UserName))
		if d.Note != "" {
			line += ": " + d.Note
		}
		fmt.Println(line + ".")
	}
	if r.Previous != nil {
		changed := 0
		for _, f := range r.Source {
			if f.Change != "same" {
				changed++
			}
		}
		fmt.Printf("Compared with the approved %s: %s changed.\n", r.Previous.Version, plural(changed, "file"))
	}
	if len(r.UsedBy) > 0 {
		var uses []string
		for _, u := range r.UsedBy {
			uses = append(uses, fmt.Sprintf("%s (version %d)", u.ProjectName, u.Number))
		}
		fmt.Println("Used by:", strings.Join(uses, ", "))
	}
	if details {
		fmt.Println("version:", m.Hash)
		fmt.Println("review:", m.URL)
		for _, i := range r.Component.Imports {
			fmt.Printf("  import %s (%s, %s line %d)\n", i.Specifier, i.Kind, i.File, i.Line)
		}
	}
	if source {
		for _, f := range r.Source {
			fmt.Printf("\n── %s (%s)\n", f.Path, f.Change)
			if f.Change == "removed" {
				continue
			}
			fmt.Println(f.Text)
		}
	}
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func orSomeone(n string) string {
	if n == "" {
		return "someone"
	}
	return n
}

// plainWhen is a time in plain words ("today 14:02", "18 Oct").
func plainWhen(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	l := t.Local()
	now := time.Now()
	if l.Year() == now.Year() && l.YearDay() == now.YearDay() {
		return "today " + l.Format("15:04")
	}
	return l.Format("2 Jan 15:04")
}

// motionDecision asks the person at the terminal and runs approve/reject/revoke.
func motionDecision(c *cobra.Command, op, ref, word, note string, reject bool) error {
	if !interactive() {
		return fail(c, apperr.HumanOnly, "Only a person can decide about a motion graphic, in the dashboard or in their own terminal.", "Open Motion graphics in the dashboard, or run this command yourself in a terminal.")
	}
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	// A bare name means the one version of it waiting for a person (the
	// confirmation below names the exact version either way).
	if !strings.Contains(ref, "@") {
		list, err := ops.CallTyped[ops.MotionListOut](c.Context(), a, "motion.list", ops.MotionListIn{All: true}, ops.Caller{})
		if err != nil {
			return failErr(c, err)
		}
		var match []ops.MotionRow
		for _, m := range list.Components {
			if m.Name == ref && (m.State == models.MotionWaiting || (op == "motion.revoke" && m.State == models.MotionApproved)) {
				match = append(match, m)
			}
		}
		if len(match) != 1 {
			return failErr(c, apperr.New(apperr.InvalidInput, "Say which version of %s: %d match.", ref, len(match)).
				WithHint("The list of motion graphics shows every version.").WithCommand("metarush motion list --all --details"))
		}
		ref = match[0].Name + "@" + match[0].Hash
	}
	show, err := ops.CallTyped[ops.MotionShowOut](c.Context(), a, "motion.show", ops.MotionShowIn{Ref: ref}, ops.Caller{})
	if err != nil {
		return failErr(c, err)
	}
	printMotionShow(show, false, false)
	fmt.Println("Look at its test frames and preview (dashboard → Motion graphics) and read its files before you decide.")
	if detailsMode(c) {
		fmt.Println("its files: metarush motion show " + show.Row.Version + " --source")
	}
	fmt.Println("Version:", show.Row.Version)
	if ask(fmt.Sprintf("Type %q to %s this version of %s", word, word, show.Row.Name), "") != word {
		return fail(c, apperr.Canceled, "nothing was decided", "")
	}
	in := ops.MotionDecideIn{Ref: show.Row.Name + "@" + show.Row.Hash, Reject: reject, Note: note}
	return runOpWith(c, a, op, in, ops.Caller{Human: true}, func(o any) {
		out := o.(*ops.MotionDecideOut)
		fmt.Println(out.Summary)
		if reject {
			printToldClaude(out.ToldClaude, note, detailsMode(c)) // the note reaches the conversation that made it (D186)
		}
	})
}

func motionApproveCmd() *cobra.Command {
	var reject bool
	var note string
	c := &cobra.Command{
		Use:   "approve <name[@version]> [--reject] [--note \"…\"]",
		Short: "Approve (or turn down) one exact version of a motion graphic — people only: in an interactive terminal or the dashboard",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			word := "approve"
			if reject {
				word = "reject"
			}
			return motionDecision(c, "motion.approve", args[0], word, note, reject)
		},
	}
	c.Flags().BoolVar(&reject, "reject", false, "turn it down instead")
	c.Flags().StringVar(&note, "note", "", "a note kept with the decision")
	return c
}

func motionStudioCmd() *cobra.Command {
	var in ops.MotionCheckIn
	var port int
	c := &cobra.Command{
		Use:   "studio <folder | name> [--port 4842] [--project <project>] [--shape …]",
		Short: "Preview a motion graphic in Remotion Studio in your browser (runs its code on this machine; for components you wrote or read)",
		Long: `Opens Remotion Studio on this machine with the motion graphic over an empty frame of the
project's shape, using preview.json for its fields. The component is scanned first (the same
import rules as checks and renders). Unlike checks and renders, Studio runs in your own
browser without the network jail, so use it for components you wrote or have read. Edit the
files and reload the page; Ctrl-C stops it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if st, err := os.Stat(args[0]); err == nil && st.IsDir() {
				in.Dir = args[0]
			} else {
				in.Name = args[0]
			}
			if assistantMode() {
				return fail(c, apperr.Unauthorized, "The dashboard assistant can't start Remotion Studio.", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			dir, o, err := ops.MotionStudioSetup(c.Context(), a, &in)
			if err != nil {
				return failErr(c, err)
			}
			return runMotionStudio(c, a, dir, o, port)
		},
	}
	c.Flags().IntVar(&port, "port", 4842, "the Studio's port")
	c.Flags().StringVar(&in.ProjectID, "project", "", "project whose shape, platform and brand the preview uses")
	c.Flags().StringVar(&in.Shape, "shape", "", "vertical, landscape or square")
	return c
}

func motionRevokeCmd() *cobra.Command {
	var note string
	c := &cobra.Command{
		Use:   "revoke <name[@version]> [--note \"…\"]",
		Short: "Withdraw approval of a motion graphic version (new renders of cuts that use it are refused) — people only",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return motionDecision(c, "motion.revoke", args[0], "withdraw", note, false)
		},
	}
	c.Flags().StringVar(&note, "note", "", "why (kept with the decision)")
	return c
}

package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/ops"
)

// The `metarush cut` group works with cut sheets (docs/cut-sheet.md), the
// Markdown form of a cut, and edits the latest cut directly with the editing
// verbs (cut add, move, trim, split, delete, delete-range, remove-words and
// transition; cutedit.go): register new ones in cutCmd.
//
// Plain output speaks to a person (no IDs, file paths or commands); --details
// adds sheet positions, problem codes, links and the exact next command, and
// --json carries everything.

func cutCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "cut", Short: "Read and edit cuts: cut sheets (Markdown you can edit, diff and keep in git) and small edits by command"}
	cmd.AddCommand(cutShowCmd(), cutCheckCmd(), cutDiffCmd(), cutApplyCmd(), cutTransitionCmd(), cutFramingCmd(), cutCaptionsCmd())
	cmd.AddCommand(cutFlashesCmd(), cutNamesCmd()) // call recordings: flashes at clip edges, printed names (calls.go)
	cmd.AddCommand(cutSignoffsCmd())               // the OKs a cut needs before it goes out (keep.go)
	cmd.AddCommand(cutEditCmds()...)
	cmd.AddCommand(cutSayCmd())          // words changed in the speaker's own voice (cutsay.go, D308)
	cmd.AddCommand(cutResourceCmds()...) // update | nest | copy-in | where (resources.go)
	cmd.AddCommand(cutSourceCmds()...)   // rename | use-fixed (sources.go)
	return cmd
}

// assistantPath keeps the dashboard assistant's cut sheet and folder commands
// inside its own conversation folder (where it writes proposals): they read
// files, and the assistant must not read arbitrary ones through them.
func assistantPath(path string) error {
	root := os.Getenv("METARUSH_ASSISTANT_RUN_DIR")
	if !assistantMode() || root == "" {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	real := func(p string) string {
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			return rp
		}
		return p
	}
	if rel, err := filepath.Rel(real(root), real(abs)); err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return fmt.Errorf("the dashboard assistant reads cut sheets only from its own proposals folder")
	}
	return nil
}

// readSheet reads a cut sheet from a file or stdin ("-").
func readSheet(path string) (string, error) {
	var r io.Reader
	if path == "-" || path == "" {
		r = os.Stdin
	} else {
		if err := assistantPath(path); err != nil {
			return "", err
		}
		f, err := os.Open(path)
		if err != nil {
			return "", fmt.Errorf("can't read %s: %w", filepath.Base(path), err)
		}
		defer f.Close()
		r = f
	}
	b, err := io.ReadAll(io.LimitReader(r, 4<<20+1))
	if err != nil {
		return "", err
	}
	if len(b) > 4<<20 {
		return "", fmt.Errorf("%s is larger than 4 MB; that is not a cut sheet", filepath.Base(path))
	}
	return string(b), nil
}

// sheetName is how plain output names the file: its base name.
func sheetName(path string) string {
	if path == "-" || path == "" {
		return "the sheet"
	}
	return filepath.Base(path)
}

func printProblems(file string, ps []cutsheet.Problem, details bool) {
	var errs, notes []cutsheet.Problem
	for _, p := range ps {
		if p.Severity == "error" {
			errs = append(errs, p)
		} else {
			notes = append(notes, p)
		}
	}
	for _, group := range []struct {
		title string
		list  []cutsheet.Problem
	}{{"To fix:", errs}, {"Notes:", notes}} {
		if len(group.list) == 0 {
			continue
		}
		fmt.Println(group.title)
		for _, p := range group.list {
			where := ""
			switch {
			case details && p.Line > 0:
				where = fmt.Sprintf("%s:%d:%d: ", file, p.Line, max(p.Col, 1))
			case p.Line > 0:
				where = fmt.Sprintf("line %d: ", p.Line)
			}
			fmt.Printf("  - %s%s\n", where, p.Message)
			if p.Hint != "" {
				fmt.Printf("    %s\n", p.Hint)
			}
			if details && p.Code != "" {
				fmt.Printf("    (%s)\n", p.Code)
			}
		}
	}
}

func printList(title string, items []string) {
	if len(items) == 0 {
		return
	}
	if title != "" {
		fmt.Println(title)
	}
	for _, c := range items {
		fmt.Println("  - " + c)
	}
}

func addDetailsFlag(c *cobra.Command, v *bool) {
	c.Flags().BoolVar(v, "details", false, "also show sheet positions, problem codes, links and the exact next command")
}

func cutShowCmd() *cobra.Command {
	var in ops.CutShowIn
	var out string
	var details bool
	c := &cobra.Command{
		Use:   "show <project> [--revision <id|rN>] [--format md|json] [--out <file>]",
		Short: "Print a revision as a cut sheet (default: the latest)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			if in.Format != "" && in.Format != "md" && in.Format != "json" {
				return fail(c, apperr.InvalidInput, "--format is md or json", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			b, _ := json.Marshal(in)
			res, err := ops.Call(c.Context(), a, "cut.show", b, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			r := res.(*ops.CutShowOut)
			body := r.Sheet
			if in.Format == "json" {
				sb, _ := json.MarshalIndent(r.Spec, "", "  ")
				body = string(sb) + "\n"
			}
			if out != "" {
				if err := os.WriteFile(out, []byte(body), 0o644); err != nil {
					return fail(c, apperr.InvalidInput, "can't write "+filepath.Base(out)+": "+err.Error(), "")
				}
			}
			return emit(c, r, func() {
				name := filepath.Base(out)
				switch {
				case out == "":
					fmt.Print(body)
					return
				case r.Starter:
					fmt.Printf("This project has no cut yet. Wrote a starting cut sheet with its footage to %s.\n", name)
				case r.ReadOnly:
					fmt.Printf("Wrote version %d to %s. It is an older version; changes start from the latest.\n", r.Revision.Number, name)
				default:
					fmt.Printf("Wrote version %d (the latest) to %s.\n", r.Revision.Number, name)
				}
				if details {
					fmt.Printf("in the dashboard: %s\n", r.SheetURL)
					fmt.Printf("next: metarush cut check %s\n", out)
				}
			})
		},
	}
	c.Flags().StringVar(&in.Revision, "revision", "", "revision ID or rN (default: the latest)")
	c.Flags().StringVar(&in.Branch, "branch", "", "branch (default: main)")
	c.Flags().StringVar(&in.Format, "format", "md", "md (the cut sheet) or json (the edit spec)")
	c.Flags().StringVar(&out, "out", "", "write to this file instead of printing")
	addDetailsFlag(c, &details)
	return c
}

type sheetFlags struct {
	message, why string
	allowUnlock  bool
	details      bool
}

func (f *sheetFlags) add(c *cobra.Command) {
	c.Flags().StringVarP(&f.message, "message", "m", "", "what you changed (recorded with the new revision)")
	c.Flags().StringVar(&f.why, "why", "", "why (recorded with the new revision)")
	c.Flags().BoolVar(&f.allowUnlock, "allow-unlock", false, "permit changes to locked parts (only when the person asked)")
	addDetailsFlag(c, &f.details)
}

// cutApplyCommand is the exact command that saves a checked cut sheet.
func cutApplyCommand(c *cobra.Command, path string, f *sheetFlags, token string) string {
	args := []string{path, "--confirm", token}
	if f.message != "" {
		args = append(args, "-m", f.message)
	}
	if f.why != "" {
		args = append(args, "--why", f.why)
	}
	if f.allowUnlock {
		args = append(args, "--allow-unlock")
	}
	return commandLine(c, "metarush cut apply", args...)
}

// printCutCheck says whether a cut sheet can be saved and what it changes;
// next is the command that saves it (shown with --details; "" when the person
// is asked right away).
func printCutCheck(c *cobra.Command, path string, r *ops.CutCheckOut, next string) {
	details := detailsMode(c)
	switch {
	case r.Valid && r.NoChanges:
		fmt.Printf("No changes compared with version %d.\n", r.BaseNumber)
	case r.Valid && r.BaseNumber == 0:
		fmt.Printf("Looks good: a first cut, %s long.\n", seconds(r.Duration))
	case r.Valid:
		fmt.Printf("Looks good: %s compared with version %d (the cut will be %s long).\n", plural(len(r.Changes), "change"), r.BaseNumber, seconds(r.Duration))
	default:
		n := 0
		for _, p := range r.Problems {
			if p.Severity == "error" {
				n++
			}
		}
		fmt.Printf("%s to fix in %s.\n", capitalize(plural(n, "problem")), sheetName(path))
	}
	if r.Valid && !r.NoChanges {
		printList("", r.Changes)
		for _, an := range r.Anchors {
			verb := "ends"
			if an.Field == "in" {
				verb = "starts"
			}
			fmt.Printf("  - “%s” %s %s at %s.\n", an.Phrase, verb, an.ClipID, cutsheet.Clock(an.Time))
		}
	}
	printProblems(path, r.Problems, details)
	if r.HeadMoved {
		if r.Rebased {
			fmt.Printf("Someone saved version %d since this sheet was written; your changes don't overlap theirs, so saving keeps both.\n", r.HeadNumber)
		} else {
			fmt.Printf("Someone saved version %d since this sheet was written.\n", r.HeadNumber)
		}
	}
	if r.ConfirmToken != "" {
		readyToSave(c, next)
	}
}

func seconds(v float64) string {
	if v < 60 {
		return fmt.Sprintf("%.1f s", v)
	}
	return fmt.Sprintf("%d min %04.1f s", int(v)/60, v-float64(int(v)/60*60))
}

func cutCheckCmd() *cobra.Command {
	var f sheetFlags
	c := &cobra.Command{
		Use:   "check <file>",
		Short: "Check a cut sheet: problems by line, the changes in plain words, and a confirm token",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			text, err := readSheet(args[0])
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			return runOp(c, "cut.check", ops.CutCheckIn{Sheet: text, Instruction: f.message, Rationale: f.why, AllowUnlock: f.allowUnlock}, func(o any) {
				r := o.(*ops.CutCheckOut)
				printCutCheck(c, args[0], r, cutApplyCommand(c, args[0], &f, r.ConfirmToken))
			})
		},
	}
	f.add(c)
	return c
}

func cutApplyCmd() *cobra.Command {
	var f sheetFlags
	var confirm string
	var write bool
	c := &cobra.Command{
		Use:   "apply <file> --confirm <token>",
		Short: "Save a checked cut sheet as a new revision (in a terminal, without --confirm, it asks you first)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			text, err := readSheet(args[0])
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			check := ops.CutCheckIn{Sheet: text, Instruction: f.message, Rationale: f.why, AllowUnlock: f.allowUnlock}
			if dry, _ := c.Flags().GetBool("dry-run"); dry {
				return runOp(c, "cut.check", check, func(o any) {
					r := o.(*ops.CutCheckOut)
					printCutCheck(c, args[0], r, cutApplyCommand(c, args[0], &f, r.ConfirmToken))
				})
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if confirm == "" && interactive() && !jsonMode(c) {
				// A person at a terminal: show the changes and ask (the same gate
				// as the confirm token, which non-interactive callers must pass).
				b, _ := json.Marshal(check)
				res, err := ops.Call(c.Context(), a, "cut.check", b, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				chk := res.(*ops.CutCheckOut)
				printCutCheck(c, args[0], chk, "")
				if chk.ConfirmToken == "" {
					return nil
				}
				if !askYes(fmt.Sprintf("Save these changes as a new version after version %d?", chk.HeadNumber), false) {
					return fail(c, apperr.Canceled, "not saved", "")
				}
				confirm = chk.ConfirmToken
			}
			b, _ := json.Marshal(ops.CutApplyIn{Sheet: text, ConfirmToken: confirm, Instruction: f.message, Rationale: f.why, AllowUnlock: f.allowUnlock})
			res, err := ops.Call(c.Context(), a, "cut.apply", b, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			r := res.(*ops.CutApplyOut)
			wrote := false
			if write && args[0] != "-" {
				wrote = os.WriteFile(args[0], []byte(r.Sheet), 0o644) == nil
			}
			return emit(c, r, func() {
				fmt.Printf("Saved as version %d (%s).", r.Revision.Number, seconds(r.Revision.Duration))
				if r.Rebased {
					fmt.Print(" Someone else saved changes meanwhile; yours were combined with theirs.")
				}
				fmt.Println()
				printList("", r.Changes)
				var notes []cutsheet.Problem
				for _, p := range r.Problems {
					if p.Severity != "error" {
						notes = append(notes, p)
					}
				}
				printProblems(args[0], notes, f.details)
				if wrote {
					fmt.Printf("Updated %s to version %d, with exact times.\n", sheetName(args[0]), r.Revision.Number)
				} else {
					fmt.Printf("Your copy of %s still shows the version before; write out the new one before editing again.\n", sheetName(args[0]))
				}
				if f.details {
					fmt.Printf("revision: %s\n%s\n", r.Revision.ID, r.URL)
					fmt.Printf("cut sheet in the dashboard: %s\n", r.SheetURL)
					if !wrote {
						fmt.Printf("next: metarush cut show %s --out %s\n", r.Revision.ProjectID, args[0])
					}
				}
			})
		},
	}
	f.add(c)
	c.Flags().StringVar(&confirm, "confirm", "", "token from `metarush cut check` for this exact sheet")
	c.Flags().BoolVar(&write, "write", false, "after saving, rewrite the file as the new revision")
	c.Flags().Bool("dry-run", false, "same as `cut check`")
	return c
}

func printComparison(title string, cmp *ops.CutComparison, details bool) {
	fmt.Println(title)
	printList("", cmp.Changes)
	if details && cutsheet.Changed(cmp.Lines) {
		fmt.Println()
		for _, l := range strings.Split(strings.TrimRight(cutsheet.Unified(cmp.Lines, 2), "\n"), "\n") {
			fmt.Println("    " + l)
		}
	}
}

func cutDiffCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{
		Use:   "diff <file>",
		Short: "Compare a cut sheet with the revision it came from and with the latest revision",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			text, err := readSheet(args[0])
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			return runOp(c, "cut.diff", ops.CutDiffIn{Sheet: text}, func(o any) {
				r := o.(*ops.CutDiffOut)
				if !r.Valid {
					fmt.Printf("Can't read %s:\n", sheetName(args[0]))
					printProblems(args[0], r.Problems, details)
					return
				}
				if r.Base == nil {
					fmt.Println("This sheet is a project's first cut, so there is nothing to compare it with.")
					return
				}
				printComparison(fmt.Sprintf("Compared with version %d (the one this sheet came from):", r.Base.Revision.Number), r.Base, details)
				if r.HeadMoved && r.Head != nil {
					fmt.Println()
					by := r.Head.Revision.CreatedBy.UserName
					if by == "" {
						by = "someone"
					}
					printList(fmt.Sprintf("Since then %s saved version %d, which changed:", by, r.Head.Revision.Number), r.TheirEdits)
					fmt.Println()
					printComparison(fmt.Sprintf("Compared with the latest (version %d):", r.Head.Revision.Number), r.Head, details)
				}
				if !details {
					fmt.Println("(Add --details to see the sheet lines that differ.)")
				}
			})
		},
	}
	addDetailsFlag(c, &details)
	return c
}

// projectFolderCmds are `metarush project checkout|status|pull|push`.
func projectFolderCmds() []*cobra.Command {
	var co ops.ProjectCheckoutIn
	var coDetails bool
	checkout := &cobra.Command{
		Use:   "checkout <project> <folder>",
		Short: "Write a project's cut into a folder (cut.md, footage.md) to edit and keep in git",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			co.ProjectID, co.Dir = args[0], args[1]
			return runOp(c, "project.checkout", co, func(o any) { printFolder(o.(*ops.FolderOut), coDetails) })
		},
	}
	checkout.Flags().StringVar(&co.Revision, "revision", "", "revision ID or rN (default: the latest)")
	checkout.Flags().StringVar(&co.Branch, "branch", "", "branch (default: main)")
	checkout.Flags().BoolVar(&co.Force, "force", false, "overwrite cut.md and footage.md if the folder already has them")
	addDetailsFlag(checkout, &coDetails)
	var stDetails bool
	status := &cobra.Command{
		Use:   "status [folder]",
		Short: "Show a project folder's local changes and whether someone saved a newer revision",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.ProjectDirIn{}
			if len(args) == 1 {
				in.Dir = args[0]
			}
			dir := in.Dir
			if dir == "" {
				dir = "."
			}
			if err := assistantPath(dir); err != nil {
				return fail(c, apperr.Unauthorized, err.Error(), "check a project folder inside the proposals folder")
			}
			return runOp(c, "project.status", in, func(o any) { printFolder(o.(*ops.FolderOut), stDetails) })
		},
	}
	addDetailsFlag(status, &stDetails)
	var pullForce, plDetails bool
	pull := &cobra.Command{
		Use:   "pull [folder]",
		Short: "Update a project folder to the latest revision (refuses to overwrite your unsaved changes unless --force)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.ProjectDirIn{Force: pullForce}
			if len(args) == 1 {
				in.Dir = args[0]
			}
			return runOp(c, "project.pull", in, func(o any) { printFolder(o.(*ops.FolderOut), plDetails) })
		},
	}
	pull.Flags().BoolVar(&pullForce, "force", false, "discard local changes to cut.md and footage.md")
	addDetailsFlag(pull, &plDetails)
	var pu ops.ProjectPushIn
	var puDetails bool
	push := &cobra.Command{
		Use:   "push [folder] [-m message] [--dry-run | --confirm <token>]",
		Short: "Save a project folder's cut.md as a new revision (in a terminal, without --confirm, it asks you first)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				pu.Dir = args[0]
			}
			if pu.ConfirmToken == "" && !pu.DryRun && interactive() && !jsonMode(c) {
				a, err := openApp(c, "cli")
				if err != nil {
					return err
				}
				defer a.Close()
				dry := pu
				dry.DryRun = true
				b, _ := json.Marshal(dry)
				res, err := ops.Call(c.Context(), a, "project.push", b, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				f := res.(*ops.FolderOut)
				printFolder(f, puDetails)
				if f.Check == nil || f.Check.ConfirmToken == "" {
					return nil
				}
				if !askYes("Save these changes as a new version?", false) {
					return fail(c, apperr.Canceled, "not saved", "")
				}
				pu.ConfirmToken = f.Check.ConfirmToken
				return runOpWith(c, a, "project.push", pu, ops.Caller{}, func(o any) { printFolder(o.(*ops.FolderOut), puDetails) })
			}
			return runOp(c, "project.push", pu, func(o any) { printFolder(o.(*ops.FolderOut), puDetails) })
		},
	}
	push.Flags().StringVarP(&pu.Message, "message", "m", "", "what you changed (recorded with the new revision)")
	push.Flags().StringVar(&pu.ConfirmToken, "confirm", "", "token from `project push --dry-run`")
	push.Flags().BoolVar(&pu.DryRun, "dry-run", false, "only check: show the changes and a confirm token")
	push.Flags().BoolVar(&pu.AllowUnlock, "allow-unlock", false, "permit changes to locked parts (only when the person asked)")
	addDetailsFlag(push, &puDetails)
	return []*cobra.Command{checkout, status, pull, push}
}

func printFolder(f *ops.FolderOut, details bool) {
	name := f.ProjectName
	if name == "" {
		name = "the project"
	}
	at := "no cut yet"
	if f.Base.Number > 0 {
		at = fmt.Sprintf("version %d", f.Base.Number)
	}
	fmt.Printf("%s: %s, %s\n", filepath.Base(f.Dir), name, at)
	for _, file := range f.Files {
		if file.State != "unchanged" {
			fmt.Printf("  %s %s\n", file.Name, file.State)
		}
	}
	printList("Your changes:", f.Changes)
	printProblems(filepath.Join(f.Dir, "cut.md"), f.Problems, details)
	if f.Message != "" {
		fmt.Println(f.Message)
	}
	if details {
		fmt.Printf("folder: %s\n", f.Dir)
		if f.SheetURL != "" {
			fmt.Printf("in the dashboard: %s\n", f.SheetURL)
		}
		if f.Revision != nil {
			fmt.Printf("revision: %s\n%s\n", f.Revision.ID, f.Revision.URL)
		}
		if f.Next != "" {
			fmt.Printf("next: %s\n", f.Next)
		}
	}
}

// revisionCompareMarkdown prints `revision compare --format md`.
func revisionCompareMarkdown(c *cobra.Command, project, left, right string) error {
	return runOp(c, "cut.compare", ops.CutCompareIn{ProjectID: project, Left: left, Right: right}, func(o any) {
		fmt.Print(o.(*ops.CutCompareOut).Markdown)
	})
}

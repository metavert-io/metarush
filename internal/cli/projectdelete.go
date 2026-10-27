package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// Deleting a project archives it; an owner clears archived projects out for
// good (D196).

func projectDeleteCmds() []*cobra.Command {
	var yes bool
	del := &cobra.Command{
		Use:   "delete <project>",
		Short: "Delete a project: it moves to Archived projects, where anyone can restore it (its footage stays in the Library) — people only, in their own terminal",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return deleteProject(c, args[0], yes)
		},
	}
	del.Flags().BoolVarP(&yes, "yes", "y", false, "don't ask first (still only in a person's terminal)")
	restore := &cobra.Command{
		Use:   "restore <project>",
		Short: "Bring a deleted (archived) project back to your projects, as it was",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "project.restore", ops.ProjectRestoreIn{ID: args[0]}, func(o any) {
				out := o.(*ops.ProjectArchiveOut)
				fmt.Println(out.Message)
				if detailsMode(c) {
					fmt.Println(out.URL)
				}
			})
		},
	}
	var details bool
	archived := &cobra.Command{
		Use:   "archived",
		Short: "List deleted (archived) projects, most recently deleted first",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "project.list", ops.ProjectListIn{ArchivedOnly: true}, func(o any) {
				ps := o.(*ops.ProjectListOut).Projects
				if len(ps) == 0 {
					fmt.Println("No archived projects.")
					return
				}
				for _, p := range ps {
					when := "deleted earlier"
					if p.ArchivedAt != nil {
						when = "deleted " + plainWhen(*p.ArchivedAt)
					}
					if p.ArchivedBy != nil && p.ArchivedBy.UserName != "" {
						when += " by " + p.ArchivedBy.UserName
					}
					line := fmt.Sprintf("%-32s %s · %s · %s", trunc(p.Name, 32), when, plural(p.AssetCount, "clip"), plural(p.RevisionSeq, "version"))
					if p.Purging != nil {
						line += " · clearing out (run purge again to finish)"
					}
					if details {
						line += "  " + p.ID
					}
					fmt.Println(line)
				}
				fmt.Println("Restore one with: metarush project restore <project>. An owner clears them out for good with: metarush project purge.")
			})
		},
	}
	addDetails(archived, &details)
	var purge ops.ProjectPurgeIn
	purgeCmd := &cobra.Command{
		Use:   "purge <project> | --all-archived [--dry-run]",
		Short: "Clear archived projects out for good: their versions, renders, Claude Code conversations and what they kept go; their footage goes back to the Inbox (never deleted) — owners, people only, in their own terminal",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				purge.ID = args[0]
			}
			if purge.ID == "" && !purge.AllArchived {
				return fail(c, apperr.InvalidInput, "Say which archived project to clear out, or --all-archived.", "metarush project archived lists them.")
			}
			if assistantMode() || !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person can clear out projects for good, in the dashboard or in their own terminal.",
					"Run this yourself in a terminal (agents, scripts and the dashboard assistant can't delete for good).")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			check := purge
			check.DryRun, check.Forever = true, false
			res, err := ops.CallTyped[ops.ProjectPurgeOut](c.Context(), a, "project.purge", check, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			ready := len(res.Projects) - res.Refused
			if purge.DryRun || ready == 0 {
				return emit(c, res, func() { printPurge(res) })
			}
			if !jsonMode(c) {
				printPurge(res)
				fmt.Println(res.Question + " This can't be undone.")
			}
			if ask(fmt.Sprintf("Type %q to delete for good", "delete"), "") != "delete" {
				return fail(c, apperr.Canceled, "Nothing was deleted.", "")
			}
			run := purge
			run.DryRun, run.Forever = false, true
			return runOpWith(c, a, "project.purge", run, ops.Caller{Human: true}, func(o any) { fmt.Println(o.(*ops.ProjectPurgeOut).Message) })
		},
	}
	purgeCmd.Flags().BoolVar(&purge.AllArchived, "all-archived", false, "every archived project")
	purgeCmd.Flags().BoolVar(&purge.DryRun, "dry-run", false, "only say what would go, what moves and what stops it")
	return []*cobra.Command{del, restore, archived, purgeCmd}
}

// deleteProject deletes (archives) a project for a person at a terminal.
func deleteProject(c *cobra.Command, ref string, yes bool) error {
	if assistantMode() || !interactive() {
		return fail(c, apperr.HumanOnly, "Only a person can delete a project, in the dashboard or in their own terminal.",
			"Run this yourself in a terminal; it moves to Archived projects, where anyone can restore it.")
	}
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	if !yes {
		show, err := ops.CallTyped[ops.ProjectShowOut](c.Context(), a, "project.show", ops.ProjectShowIn{ID: ref}, ops.Caller{})
		if err != nil {
			return failErr(c, err)
		}
		if !askYes(fmt.Sprintf("Delete “%s”? It moves to Archived projects; you can restore it.", show.Project.Name), false) {
			return fail(c, apperr.Canceled, "Not deleted.", "")
		}
		ref = show.Project.ID
	}
	return runOpWith(c, a, "project.delete", ops.ProjectDeleteIn{ID: ref}, ops.Caller{Human: true}, func(o any) {
		out := o.(*ops.ProjectArchiveOut)
		fmt.Println(out.Message)
		if detailsMode(c) {
			fmt.Println(out.ArchivedURL)
		}
	})
}

// printPurge says, for each project, what goes, what moves and what stops it.
func printPurge(res *ops.ProjectPurgeOut) {
	if len(res.Projects) == 0 {
		fmt.Println(res.Message)
		return
	}
	for _, r := range res.Projects {
		fmt.Printf("“%s”\n", r.Name)
		if r.Status == "refused" {
			for _, b := range r.Blockers {
				fmt.Println("  can't be cleared out yet: " + b)
			}
			continue
		}
		if len(r.Goes) > 0 {
			fmt.Println("  goes: " + strings.Join(r.Goes, ", "))
		} else {
			fmt.Println("  goes: the project itself (nothing else is in it)")
		}
		for _, m := range r.Moves {
			fmt.Println("  " + m)
		}
	}
	fmt.Println("Spending, the audit trail and the activity log stay.")
}

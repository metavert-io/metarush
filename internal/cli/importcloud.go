package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/intake"
)

// importCloudCmd is `metarush import cloud <remote:folder>` (D220, D306):
// files from a Google Drive or Dropbox folder through the person's own rclone.
func importCloudCmd() *cobra.Command {
	var in intake.CloudAddIn
	var list, details bool
	c := &cobra.Command{
		Use:   "cloud <remote:folder> (--list | --id <id>… | --name <file>… | --all) [--project <project>]",
		Short: "Bring files in from a Google Drive or Dropbox folder through your own rclone on this computer, without syncing it",
		Long: `Bring files in from a Google Drive or Dropbox folder that you added to rclone with rclone's own setup (it
signs you in with Google or Dropbox in your browser; MetaRush never sees that sign-in and keeps no token). Only
remotes of rclone's Google Drive and Dropbox types are read. Each file is fetched by its ID, checked against the
folder's listing (its size and the service's checksum) and added like the Add box does: footage to the project,
pictures to its resources, each saying where it came from.

  metarush import cloud dropbox:Shoot/Day 1 --list
  metarush import cloud dropbox:Shoot/Day 1 --name "A001.mov" --name "A002.mov" --project "Garden walk"
  metarush import cloud gdrive:Teaser/Art --all --project "Garden walk"`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Folder = args[0]
			if list {
				return runOp(c, "add.cloud.list", intake.CloudListIn{Folder: in.Folder}, func(o any) {
					out := o.(*intake.CloudListOut)
					fmt.Println(out.Summary)
					for _, f := range out.Files {
						fmt.Printf("  %s  (%s, %s)\n", f.Name, f.What, sizeText(f.Size))
						if details {
							fmt.Printf("      id %s  %s\n", f.ID, f.Checksum)
						}
					}
				})
			}
			return runOp(c, "add.cloud", in, func(o any) {
				out := o.(*intake.AddPathsOut)
				for _, f := range out.Files {
					mark := "✓"
					if f.Status == "failed" || f.Status == "skipped" {
						mark = "✗"
					}
					fmt.Printf("  %s %s\n", mark, firstNonEmpty(f.Message, f.Name))
				}
				if out.Summary != "" {
					fmt.Println(out.Summary)
				}
			})
		},
	}
	f := c.Flags()
	f.BoolVar(&list, "list", false, "only list the folder's files")
	f.StringArrayVar(&in.IDs, "id", nil, "a file's ID (from --list --details; repeat)")
	f.StringArrayVar(&in.Names, "name", nil, "a file's name in the folder (repeat)")
	f.BoolVar(&in.All, "all", false, "every video, sound and picture in the folder")
	f.StringVar(&in.ProjectID, "project", "", "the project they go into (default: footage to the Inbox, pictures to the workspace library)")
	addDetailsFlag(c, &details)
	return c
}

func sizeText(n int64) string {
	switch {
	case n >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(n)/(1<<30))
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	}
	return fmt.Sprintf("%d KB", (n+1023)/1024)
}

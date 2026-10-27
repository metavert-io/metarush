package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// Pictures from a repository (D216) in the terminal: `metarush resources
// pictures <repo>` lists what a repository keeps from its folders; --keep and
// --drop change them (the new folders' pictures come in at the commit it
// shows; --wait waits for them). `metarush resources add <repo> --pictures
// <folder>` keeps them from the start.

func resPicturesCmd() *cobra.Command {
	var in ops.RepoPicturesIn
	var keep, drop []string
	var wait, details bool
	c := &cobra.Command{
		Use:   "pictures <repository> [--project <project>] [--keep <folder>]... [--drop <folder>]... [--version <commit>] [--path <folder>] [--wait]",
		Short: "The pictures a GitHub repository keeps from its folders: each PNG, JPEG, WebP, GIF or SVG file a picture of its own, pinned to the commit and refreshed with it; --keep and --drop change the folders",
		Long: `A repository keeps its text files; it can also keep the pictures in some of its
folders (art kept in git), each one a picture graphics and Remotion picture
settings can show, that says exactly where it came from (repository@commit:file).
Files are told by what they are, not by their names; SVGs are cleaned; files
stored with Git LFS, links and files larger than the limit are left out, and
said why. A newer commit (metarush resources refresh) makes new versions of the
pictures that changed, adds new files and marks the ones that went away; cuts
keep the versions they show until you update them.

  metarush resources pictures widget                          what it keeps
  metarush resources pictures widget --keep art/characters    keep a folder's pictures too
  metarush resources pictures widget --drop art/old           stop keeping one in step`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			print := func(out *ops.RepoPicturesOut) {
				if out.Message != "" {
					fmt.Println(out.Message)
				}
				for _, n := range out.Notes {
					fmt.Println(n)
				}
				if out.Note != "" {
					fmt.Println(out.Note)
				}
				printRepoPictures(&out.RepoPictures, details)
			}
			if len(keep)+len(drop) == 0 {
				return runOp(c, "resources.pictures", in, func(o any) { print(o.(*ops.RepoPicturesOut)) })
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			out, err := ops.CallTyped[ops.RepoPicturesOut](c.Context(), a, "resources.pictures.set", ops.RepoPicturesSetIn{ResourceRefIn: in.ResourceRefIn, Keep: keep, Drop: drop}, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			var jobs []models.Job
			if wait && out.JobID != "" {
				if jobs, err = waitJobs(c, a, []string{out.JobID}, 30*time.Minute, false); err != nil {
					return failErr(c, err)
				}
				// What the repository keeps now that its pictures are in.
				now, err := ops.CallTyped[ops.RepoPicturesOut](c.Context(), a, "resources.pictures", ops.RepoPicturesIn{ResourceRefIn: in.ResourceRefIn}, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				out.RepoPictures, out.Note = now.RepoPictures, ""
			}
			var result any = out
			if len(jobs) > 0 {
				result = struct {
					*ops.RepoPicturesOut
					Jobs []models.Job `json:"jobs"`
				}{out, jobs}
			}
			return emit(c, result, func() {
				// A fetch that didn't work says so before what the repository keeps.
				var failed []models.Job
				for _, j := range jobs {
					if j.State != models.JobSucceeded {
						failed = append(failed, j)
					}
				}
				if out.Message != "" && len(jobs) > 0 {
					fmt.Println(out.Message)
					out.Message = ""
				}
				printFetched(&fetchedResult{Jobs: failed}, details)
				print(out)
			})
		},
	}
	fl := c.Flags()
	fl.StringVar(&in.ProjectID, "project", "", "the project that sees the repository (ID or name)")
	fl.StringSliceVar(&keep, "keep", nil, "keep the pictures in this folder or file too (. is the whole repository; repeat for more)")
	fl.StringSliceVar(&drop, "drop", nil, "stop keeping this folder in step (the pictures kept stay)")
	fl.StringVar(&in.Version, "version", "", "a version of the repository (first characters of a commit; default the one it shows)")
	fl.StringVar(&in.Path, "path", "", "only the pictures in this folder")
	fl.BoolVar(&wait, "wait", false, "with --keep: wait until the folder's pictures are in")
	addDetailsFlag(c, &details)
	return c
}

// printRepoPictures lists what a repository keeps from its folders.
func printRepoPictures(p *ops.RepoPictures, details bool) {
	fmt.Println(p.Summary)
	for _, x := range p.Pictures {
		size := ""
		if x.Width > 0 {
			size = fmt.Sprintf(" · %d×%d", x.Width, x.Height)
		}
		extra := ""
		if x.Newer {
			extra += " · a newer version since"
		}
		if x.Removed {
			extra += " · removed from the library"
		}
		fmt.Printf("  %-26s %s%s%s\n", x.Name, theirs(x.Path), size, extra)
		if details {
			fmt.Printf("  %-26s %s · version %s\n", "", x.ResourceID, short8(x.SHA256))
		}
	}
	if len(p.SkippedWhy) > 0 {
		fmt.Println("Left out:", joinSemi(p.SkippedWhy))
	}
	if details {
		for _, l := range p.LeftOut {
			fmt.Printf("  %s: %s\n", theirs(l.Path), l.Why)
		}
		if p.Commit != "" {
			fmt.Printf("  commit %s\n", p.Commit)
		}
	}
}

func joinSemi(xs []string) string {
	out := ""
	for i, x := range xs {
		if i > 0 {
			out += "; "
		}
		out += x
	}
	return out
}

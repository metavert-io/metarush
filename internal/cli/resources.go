package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// The resources library (D79–D84) in the terminal: `metarush resources …`,
// `metarush add` (the Add box), `metarush brand`, project links (`metarush
// project link|unlink|links|use`), the cut verbs that use them (`metarush cut
// update|nest|copy-in|where`) and footage housekeeping (`metarush asset
// where|move|delete`). Plain output by default; --json for scripts and agents.

func resourcesCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resources",
		Short: "The resources library: pictures, logos, SVGs, PDFs and fonts for a project's graphics (or the whole workspace)",
		Long: `Resources are what a cut's graphics use that isn't footage: logos, photos,
screenshots, PDF pages, fonts. A project has its own; the workspace library is
shared by every project; linked projects bring theirs in.

  metarush resources add logo.svg --project "Spring promo"   add a file (or --workspace)
  metarush resources add ~/Brand --project "Spring promo"    link a folder: kept in step
  metarush resources add https://github.com/acme/widget      a repository, pinned at a commit
  metarush resources pictures widget --keep art/characters   and the pictures in one of its folders
  metarush resources add https://example.com --depth 1        a web page snapshot (and pages it links to)
  metarush resources make code widget --file main.go --lines 10-40   a code slide
  metarush resources path widget                             a read-only copy of the repository
  metarush resources list --project "Spring promo"           what the project can use
  metarush resources show logo --project "Spring promo"      versions, colours, where used
  metarush resources path logo                               the file on this machine

A cut names resources in its graphics (title card: logo "logo") and pins the
exact version; a newer version is used only after "metarush cut update".`,
	}
	cmd.AddCommand(resAddCmd(), resListCmd(), resShowCmd(), resPathCmd(), resUpdateCmd(), resRefreshCmd(), resRemoveCmd(), resPromoteCmd(), resWhereCmd(),
		resLinkCmd(), resUnlinkCmd(), resFoldersCmd(), resFolderCmd(), resMakeCmd(), resPicturesCmd())
	return cmd
}

func printAdded(o *intake.AddPathsOut, details bool) {
	for _, f := range o.Files {
		line := f.Message
		if line == "" {
			line = f.Name + ": " + f.Status
		}
		fmt.Println(line)
		for _, ch := range f.Changes {
			fmt.Println("  " + upperFirst(ch) + ".")
		}
		for _, n := range f.Notes {
			fmt.Println("  " + n)
		}
		if details {
			fmt.Printf("  status %s · route %s · %s%s\n", f.Status, f.Route, f.ResourceID, f.AssetID)
		}
	}
	for _, fo := range o.Folders {
		fmt.Println(fo.Message)
		if details {
			fmt.Printf("  folder %s\n", fo.Folder.ID)
		}
	}
	fmt.Println(o.Summary)
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func resAddCmd() *cobra.Command {
	var in intake.ResourcesAddIn
	var details, copyFolders bool
	var f fetchOptions
	c := &cobra.Command{
		Use:   "add <file | folder | GitHub repository | web page>... [--project <project> | --workspace]",
		Short: "Add pictures (PNG, JPEG, WebP; HEIC becomes JPEG), SVGs (cleaned of scripts and outside links), PDFs (pages become pictures) and fonts; a folder is linked and kept in step; a GitHub repository is pinned at a commit; a web page becomes a dated snapshot",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var paths, links []string
			for _, x := range args {
				if strings.HasPrefix(x, "http://") || strings.HasPrefix(x, "https://") || strings.HasPrefix(x, "github.com/") {
					if !strings.Contains(x, "://") {
						x = "https://" + x
					}
					links = append(links, x)
				} else {
					paths = append(paths, x)
				}
			}
			in.Paths = paths
			if copyFolders {
				in.Folders = "copy"
			}
			if len(f.pictures) > 0 && len(links) == 0 {
				return fail(c, apperr.InvalidInput, "--pictures keeps the pictures in a GitHub repository's folders: give the repository's address too.", "")
			}
			for _, p := range paths {
				if err := assistantPath(p); err != nil {
					return fail(c, apperr.Unauthorized, err.Error(), "")
				}
			}
			if len(links) == 0 {
				return runOp(c, "resources.add", in, func(o any) { printAdded(o.(*intake.AddPathsOut), details) })
			}
			// Links: repositories and web pages come in through a fetch job.
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			out := &fetchedResult{Fetches: []ops.ResourceFetchOut{}}
			if len(paths) > 0 {
				res, err := ops.CallTyped[intake.AddPathsOut](c.Context(), a, "resources.add", in, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				out.Paths = res
			}
			f.project, f.workspace, f.name, f.title = in.ProjectID, in.Workspace, in.Name, in.Title
			if err := fetchLinks(c, a, links, f, out); err != nil {
				return err
			}
			return emit(c, out, func() {
				if out.Paths != nil {
					printAdded(out.Paths, details)
				}
				printFetched(out, details)
			})
		},
	}
	f.addFlags(c)
	c.Flags().StringVar(&f.as, "as", "", "links: repo, page or picture (default: told by the address)")
	fl := c.Flags()
	fl.StringVar(&in.ProjectID, "project", "", "the project (ID or name); default: the workspace library")
	fl.BoolVar(&in.Workspace, "workspace", false, "the workspace library (every project can use it)")
	fl.StringVar(&in.Name, "name", "", "short name (one file)")
	fl.StringVar(&in.Title, "title", "", "display name (one file)")
	fl.StringSliceVar(&in.Tags, "tag", nil, "tags")
	fl.StringVar(&in.Description, "description", "", "what it is")
	fl.StringVar(&in.License, "license", "", "a licence note (where it may be used, who to credit)")
	fl.StringVar(&in.Version, "version-of", "", "add the file as a new version of this resource")
	fl.BoolVar(&copyFolders, "copy", false, "add what a folder holds once instead of linking it")
	fl.StringVar(&in.Upload, "upload", "", "linked folders: small (default), all or on-use")
	addDetailsFlag(c, &details)
	return c
}

func printResourceRow(r ops.ResourceRow, details bool) {
	from := ""
	switch r.Origin {
	case "linked":
		from = " · from " + r.From
	case "workspace":
		from = " · workspace library"
	}
	size := ""
	if r.Width > 0 {
		size = fmt.Sprintf(" · %d×%d", r.Width, r.Height)
	}
	if r.Pages > 0 {
		size += fmt.Sprintf(" · %d pages", r.Pages)
	}
	if r.Family != "" {
		size += " · " + r.Family
	}
	if r.Repo != nil {
		size = fmt.Sprintf(" · %s, %s · %d files", short8(r.Repo.Commit), r.Repo.CommittedAt.Format("2 Jan 2006"), r.Repo.Files)
	}
	if r.Site != nil {
		size = " · snapshot of " + r.Site.TakenAt.Local().Format("2 Jan 2006")
	}
	extra := ""
	switch {
	case r.Missing && r.Source.Kind == models.FromRepo:
		extra += " · no longer in the repository"
	case r.Missing:
		extra += " · missing from its folder"
	}
	if !r.Stored {
		extra += " · only on the machine with its folder"
	}
	fmt.Printf("%-22s %s (%s%s%s)%s\n", r.Name, r.Title, kindName(r.Kind), size, from, extra)
	if tags := append(append([]string{}, r.Tags...), autoTagNames(r.AutoTags)...); len(tags) > 0 {
		fmt.Printf("%-22s tags: %s\n", "", strings.Join(tags, ", "))
	}
	if details {
		fmt.Printf("%-22s %s · version %s · %d versions\n", "", r.ID, short8(r.Current), r.Versions)
	}
}

func autoTagNames(ts []models.ResourceTag) []string {
	var out []string
	for _, t := range ts {
		out = append(out, t.Tag+"?")
	}
	return out
}

func short8(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func kindName(k string) string {
	switch k {
	case "svg":
		return "SVG"
	case "pdf":
		return "PDF"
	case "font":
		return "font"
	case "repo":
		return "repository"
	case "website":
		return "web page"
	}
	return "picture"
}

func resListCmd() *cobra.Command {
	var in ops.ResourceListIn
	var details, workspace bool
	c := &cobra.Command{
		Use:   "list [--project <project> | --workspace] [--kind image|svg|pdf|font] [--tag <tag>] [--query <words>]",
		Short: "List the resources a project can use (its own, the workspace library's, its linked projects'), or the workspace library",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if workspace {
				in.ProjectID = ""
			}
			return runOp(c, "resources.list", in, func(o any) {
				out := o.(*ops.ResourceListOut)
				for _, r := range out.Resources {
					printResourceRow(r, details)
				}
				fmt.Println(out.Summary + ".")
			})
		},
	}
	fl := c.Flags()
	fl.StringVar(&in.ProjectID, "project", "", "the project (ID or name)")
	fl.BoolVar(&workspace, "workspace", false, "only the workspace library")
	fl.StringVar(&in.Kind, "kind", "", "image, svg, pdf or font")
	fl.StringVar(&in.Tag, "tag", "", "a tag (MetaRush's suggestions count)")
	fl.StringVar(&in.Query, "query", "", "words in the name, title, tags or description")
	fl.StringVar(&in.Only, "only", "", "project, workspace or linked")
	fl.StringVar(&in.Folder, "folder", "", "only from this linked folder (ID)")
	fl.BoolVar(&in.Removed, "removed", false, "include removed resources")
	addDetailsFlag(c, &details)
	return c
}

func printUses(uses []ops.UseRow, details bool) {
	for _, u := range uses {
		fmt.Println("  " + u.Text)
		if details && u.Link != "" {
			fmt.Println("    " + u.Link)
		}
	}
}

func resShowCmd() *cobra.Command {
	var in ops.ResourceRefIn
	var details bool
	c := &cobra.Command{
		Use: "show <name | title | id> [--project <project>]", Short: "Show a resource: versions, size, colours, tags, where it came from and where it is used", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			return runOp(c, "resources.show", in, func(o any) {
				out := o.(*ops.ResourceShowOut)
				printResourceRow(out.Row, details)
				if len(out.Row.Colors) > 0 {
					fmt.Println("Colours:", strings.Join(out.Row.Colors, " "))
				}
				if out.Row.Description != "" {
					fmt.Println(out.Row.Description)
				}
				if out.Row.License != "" {
					fmt.Println("Licence:", out.Row.License)
				}
				for i, v := range out.Resource.Versions {
					cur := ""
					if v.SHA256 == out.Resource.Current {
						cur = " (current)"
					}
					src := v.Source.Name
					switch {
					case v.Source.Kind == models.FromFolder:
						src = "folder " + v.Source.RelPath
					case v.Repo != nil:
						src = "commit " + short8(v.Repo.Commit)
					case v.Site != nil:
						src = "a snapshot of " + v.Site.URL
					case v.Source.MadeWith != nil:
						src = v.Source.Provenance()
					}
					fmt.Printf("Version %d%s: %s, added %s from %s\n", i+1, cur, short8(v.SHA256), v.CreatedAt.Local().Format("2 Jan 2006 15:04"), src)
					for _, ch := range v.Changes {
						fmt.Println("  " + upperFirst(ch) + ".")
					}
				}
				if out.Row.Provenance != "" && out.Row.Kind != models.ResourceWebsite {
					fmt.Println("From " + out.Row.Provenance + ".")
				}
				printRepoDetails(out.Repo, out.Made)
				printSite(out.Row.Site, out.Row.PagePictures)
				if len(out.UsedIn) == 0 {
					fmt.Println("Not used in any cut yet.")
				} else {
					fmt.Println("Used in:")
					printUses(out.UsedIn, details)
				}
			})
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project that sees it (ID or name)")
	addDetailsFlag(c, &details)
	return c
}

func resPathCmd() *cobra.Command {
	var in ops.ResourcePathIn
	c := &cobra.Command{
		Use: "path <name | title | id> [--project <project>] [--page N] [--version <sha>]", Short: "Fetch a resource's file (or a PDF page picture) to this machine and print where it is", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			return runOp(c, "resources.path", in, func(o any) { fmt.Println(o.(*ops.ResourcePathOut).Path) })
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project that sees it (ID or name)")
	c.Flags().IntVar(&in.Page, "page", 0, "a PDF's page (1 = the first)")
	c.Flags().StringVar(&in.Version, "version", "", "a version (first characters of its SHA-256)")
	return c
}

func resUpdateCmd() *cobra.Command {
	var in ops.ResourceUpdateIn
	var title, name, desc, license string
	c := &cobra.Command{
		Use: "update <name | title | id> [--title …] [--name …] [--tag …] [--untag …] [--description …] [--license …]", Short: "Rename a resource, change its short name, tags, description or licence note", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			if c.Flags().Changed("title") {
				in.Title = &title
			}
			if c.Flags().Changed("name") {
				in.Name = &name
			}
			if c.Flags().Changed("description") {
				in.Description = &desc
			}
			if c.Flags().Changed("license") {
				in.License = &license
			}
			return runOp(c, "resources.update", in, func(o any) { fmt.Println(o.(*ops.ResourceOut).Message) })
		},
	}
	fl := c.Flags()
	fl.StringVar(&in.ProjectID, "project", "", "the project that sees it (ID or name)")
	fl.StringVar(&title, "title", "", "display name")
	fl.StringVar(&name, "name", "", "short name")
	fl.StringSliceVar(&in.AddTags, "tag", nil, "add tags")
	fl.StringSliceVar(&in.RemoveTags, "untag", nil, "remove tags (MetaRush's suggestions too)")
	fl.StringVar(&desc, "description", "", "what it is")
	fl.StringVar(&license, "license", "", "licence note")
	return c
}

func resRefreshCmd() *cobra.Command {
	var in ops.ResourceRefreshIn
	var wait, details bool
	c := &cobra.Command{
		Use: "refresh <name | title | id> [--project <project>] [--ref <branch>] [--wait]", Short: "Look at a resource again (size, colours, PDF page pictures, suggested tags; the file never changes); a repository moves to its newest commit and a web page gets a new snapshot, as new versions cuts use after cut update", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			out, err := ops.CallTyped[ops.ResourceOut](c.Context(), a, "resources.refresh", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			res := &fetchedResult{Fetches: []ops.ResourceFetchOut{}}
			if wait && out.JobID != "" {
				js, err := waitJobs(c, a, []string{out.JobID}, 30*time.Minute, false)
				if err != nil {
					return failErr(c, err)
				}
				res.Jobs = js
			}
			var result any = out
			if len(res.Jobs) > 0 {
				result = struct {
					*ops.ResourceOut
					Jobs []models.Job `json:"jobs"`
				}{out, res.Jobs}
			}
			return emit(c, result, func() {
				fmt.Println(out.Message)
				for _, n := range out.Notes {
					fmt.Println(n)
				}
				printFetched(res, details)
			})
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project that sees it (ID or name)")
	c.Flags().StringVar(&in.At, "ref", "", "a repository: the branch, tag or commit to pin (default: the one it follows)")
	c.Flags().BoolVar(&wait, "wait", false, "wait for the new commit or snapshot")
	addDetailsFlag(c, &details)
	return c
}

func resRemoveCmd() *cobra.Command {
	var in ops.ResourceRemoveIn
	c := &cobra.Command{
		Use: "remove <name | title | id> [--project <project>] [--restore]", Short: "Remove a resource from the library (nothing is deleted: cuts that use it keep showing it), or bring it back", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			return runOp(c, "resources.remove", in, func(o any) { fmt.Println(o.(*ops.ResourceOut).Message) })
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project that sees it (ID or name)")
	c.Flags().BoolVar(&in.Restore, "restore", false, "bring a removed resource back")
	return c
}

func resPromoteCmd() *cobra.Command {
	var in ops.ResourceRefIn
	c := &cobra.Command{
		Use: "promote <name | title | id> --project <project>", Short: "Move a project's resource into the workspace library, so every project can use it", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			return runOp(c, "resources.promote", in, func(o any) { fmt.Println(o.(*ops.ResourceOut).Message) })
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it belongs to (ID or name)")
	return c
}

func resWhereCmd() *cobra.Command {
	var in ops.ResourceRefIn
	var details bool
	c := &cobra.Command{
		Use: "where <name | title | id> [--project <project>]", Short: "List where a resource is used: cuts (and their older versions) and the brand kit", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			return runOp(c, "resources.where", in, func(o any) {
				out := o.(*ops.ResourceWhereOut)
				fmt.Println(out.Summary)
				printUses(out.Uses, details)
			})
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project that sees it (ID or name)")
	addDetailsFlag(c, &details)
	return c
}

func printLinkedFolder(f intake.LinkedFolderRow, details bool) {
	where := "the workspace library"
	if f.ProjectName != "" {
		where = f.ProjectName
	}
	fmt.Printf("%s → %s: %s\n", f.Name, where, f.Text)
	if f.HerePath != "" {
		fmt.Println("  here:", f.HerePath)
	}
	if f.Here != nil {
		// The files that couldn't be added, each with why (the text says when they are tried again).
		for _, e := range f.Here.Errors {
			fmt.Println("  couldn't add", e)
		}
	}
	for _, m := range f.Machines {
		if details || m.Machine != "" {
			fmt.Printf("  %s: %s, %d files, %d resources\n", m.Machine, m.State, m.Files, m.Resources)
		}
	}
	if details {
		fmt.Printf("  %s · upload %s (%d MB)\n", f.ID, f.Upload, f.SmallMB)
	}
}

func resLinkCmd() *cobra.Command {
	var in intake.LinkedFolderLinkIn
	var details bool
	c := &cobra.Command{
		Use: "link <folder> [--project <project>] [--upload small|all|on-use] [--existing import|skip]", Short: "Link a folder on this machine: its pictures, SVGs, PDFs and fonts become resources (and footage becomes sources), kept in step as files change", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Path = args[0]
			if err := assistantPath(in.Path); err != nil {
				return fail(c, apperr.Unauthorized, err.Error(), "")
			}
			return runOp(c, "resources.link", in, func(o any) {
				out := o.(*intake.LinkedFolderOut)
				fmt.Println(out.Message)
				printLinkedFolder(out.Folder, details)
			})
		},
	}
	fl := c.Flags()
	fl.StringVar(&in.ProjectID, "project", "", "the project (ID or name); default: the workspace library")
	fl.StringVar(&in.Upload, "upload", "", "small (default: up to the size limit at once, larger ones when a cut first uses them), all, or on-use")
	fl.IntVar(&in.SmallMB, "small-mb", 0, "the size limit for small (default resources.uploadSmallMB, else 25)")
	fl.StringVar(&in.Existing, "existing", "", "files already there: import (default) or skip")
	addDetailsFlag(c, &details)
	return c
}

func resUnlinkCmd() *cobra.Command {
	var in intake.LinkedFolderUnlinkIn
	c := &cobra.Command{
		Use: "unlink <folder name | id> [--here]", Short: "Stop keeping a linked folder in step (everywhere, or only on this machine); its resources stay", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Folder = args[0]
			return runOp(c, "resources.unlink", in, func(o any) { fmt.Println(o.(*intake.LinkedFolderOut).Message) })
		},
	}
	c.Flags().BoolVar(&in.Here, "here", false, "only on this machine")
	return c
}

func resFoldersCmd() *cobra.Command {
	var in intake.LinkedFolderListIn
	var details bool
	c := &cobra.Command{
		Use: "folders [--project <project>]", Short: "List linked folders and how they stand on each machine", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "resources.folders", in, func(o any) {
				out := o.(*intake.LinkedFolderListOut)
				if len(out.Folders) == 0 {
					fmt.Println("No linked folders yet.")
				}
				for _, f := range out.Folders {
					printLinkedFolder(f, details)
				}
			})
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "only this project's (ID or name)")
	addDetailsFlag(c, &details)
	return c
}

func resFolderCmd() *cobra.Command {
	var in intake.LinkedFolderSetIn
	var details bool
	c := &cobra.Command{
		Use: "folder <folder name | id> [--upload …] [--small-mb N] [--pause | --resume] [--path <folder here>] [--rescan]", Short: "Change a linked folder: when files upload, pause or resume it here, say where it is on this machine, or look through it again", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Folder = args[0]
			return runOp(c, "resources.folder", in, func(o any) {
				out := o.(*intake.LinkedFolderOut)
				fmt.Println(out.Message)
				printLinkedFolder(out.Folder, details)
			})
		},
	}
	fl := c.Flags()
	fl.StringVar(&in.Upload, "upload", "", "small, all or on-use")
	fl.IntVar(&in.SmallMB, "small-mb", 0, "size limit for small")
	fl.BoolVar(&in.Pause, "pause", false, "stop keeping it in step on this machine")
	fl.BoolVar(&in.Resume, "resume", false, "keep it in step again")
	fl.StringVar(&in.Path, "path", "", "where the folder is on this machine")
	fl.BoolVar(&in.Rescan, "rescan", false, "look through it again now")
	addDetailsFlag(c, &details)
	return c
}

// addCmd is `metarush add`: the Add box in the terminal.
func addCmd() *cobra.Command {
	var project, as string
	var workspace, copyFolders, route, details, wait bool
	var depth, maxPages int
	c := &cobra.Command{
		Use:   "add <file | folder | link>... [--project <project>]",
		Short: "Add anything, the way the Add box does: footage to the project (or the Inbox), pictures, SVGs, PDFs and fonts to its resources, folders as linked folders, and links to the link import",
		Long: `Add files, folders and links in one go. MetaRush looks at what each file is:
video and sound become footage (with previews, transcripts and speakers), pictures,
SVGs, PDFs and fonts become resources, folders are linked and kept in step, and video
links (YouTube, TikTok, X and other video sites) are imported. A GitHub repository is
kept at its current commit and a web page becomes a dated snapshot (both resources).

  metarush add clip.mov logo.svg --project "Spring promo"
  metarush add ~/Brand --project "Spring promo"
  metarush add https://youtu.be/… --project "Spring promo"
  metarush add --route clip.mov deck.pdf https://github.com/acme/widget   where they would go`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var paths, links []string
			for _, x := range args {
				if strings.HasPrefix(x, "http://") || strings.HasPrefix(x, "https://") {
					links = append(links, x)
				} else {
					paths = append(paths, x)
				}
			}
			if route {
				return runOp(c, "add.route", intake.RouteIn{Names: paths, Text: strings.Join(links, " ")}, func(o any) {
					out := o.(*intake.RouteOut)
					for _, f := range out.Files {
						fmt.Printf("%s → %s\n", f.Name, routeWords(f.Route, f.Kind))
					}
					for _, l := range out.Links {
						fmt.Printf("%s → %s\n", l.Input, l.Message)
					}
				})
			}
			for _, p := range paths {
				if err := assistantPath(p); err != nil {
					return fail(c, apperr.Unauthorized, err.Error(), "")
				}
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			// --json: {"paths": files and folders (add.paths), "links": links (add.links)}.
			var result struct {
				Paths *intake.AddPathsOut `json:"paths,omitempty"`
				Links *intake.AddLinksOut `json:"links,omitempty"`
				Jobs  []models.Job        `json:"jobs,omitempty"`
			}
			if len(paths) > 0 {
				in := intake.AddPathsIn{Paths: paths, ProjectID: project, Workspace: workspace}
				if copyFolders {
					in.Folders = "copy"
				}
				out, err := ops.CallTyped[intake.AddPathsOut](c.Context(), a, "add.paths", in, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				result.Paths = out
			}
			if len(links) > 0 {
				out, err := ops.CallTyped[intake.AddLinksOut](c.Context(), a, "add.links", intake.AddLinksIn{URLs: links, ProjectID: project, As: as, Depth: depth, MaxPages: maxPages}, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				result.Links = out
				var jobIDs []string
				for _, l := range out.Links {
					if l.JobID != "" {
						jobIDs = append(jobIDs, l.JobID)
					}
				}
				if out.Imports != nil {
					jobIDs = append(jobIDs, out.Imports.JobIDs...)
				}
				if wait && len(jobIDs) > 0 {
					js, err := waitJobs(c, a, jobIDs, 30*time.Minute, false)
					if err != nil {
						return failErr(c, err)
					}
					result.Jobs = js
				}
			}
			return emit(c, result, func() {
				if result.Paths != nil {
					printAdded(result.Paths, details)
				}
				if result.Links != nil {
					for _, l := range result.Links.Links {
						fmt.Println(l.Message)
						if l.Status == "ask" {
							fmt.Println("  Say which with --as video or --as page.")
						}
					}
				}
				printFetched(&fetchedResult{Jobs: result.Jobs}, details)
			})
		},
	}
	fl := c.Flags()
	fl.StringVar(&project, "project", "", "the project everything goes into (ID or name); default: the Inbox and the workspace library")
	fl.BoolVar(&workspace, "workspace", false, "put resources in the workspace library even with --project")
	fl.BoolVar(&copyFolders, "copy", false, "add what folders hold once instead of linking them")
	fl.StringVar(&as, "as", "", "for links that could be either: video or page")
	fl.BoolVar(&route, "route", false, "only say where each would go")
	fl.BoolVar(&wait, "wait", false, "wait until linked videos, repositories and pages are in")
	fl.IntVar(&depth, "depth", 0, "a web page: 1 also takes the same-site pages it links to")
	fl.IntVar(&maxPages, "max-pages", 0, "a web page at depth 1: how many pages in all (default 5)")
	addDetailsFlag(c, &details)
	return c
}

func routeWords(route, kind string) string {
	switch route {
	case "resource":
		return "resources (" + kindName(kind) + ")"
	case "footage":
		return "footage"
	}
	return "nothing MetaRush uses (left alone)"
}

func brandCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "brand", Short: "The brand — part of a project's Content Settings (its own, another project's or the default's): logos, colours (from the logo or a website, editable) and font; new cuts start with it"}
	var details bool
	var showProject string
	show := &cobra.Command{
		Use: "show [--project <project>]", Short: "Show the brand a project uses (from its Content Settings: its own, another project's or the default's) and the brand new cuts get", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "brand.show", ops.BrandShowIn{ProjectID: showProject}, func(o any) { printBrand(o.(*ops.BrandShowOut), details) })
		},
	}
	show.Flags().StringVar(&showProject, "project", "", "a project: the brand of the settings it uses (default: the default's, what new projects use)")
	addDetailsFlag(show, &details)
	var in ops.BrandSetIn
	var logo, logoDark, logoLight, logoIcon, logoWordmark, fontFile, font, primary, accent, text, background string
	set := &cobra.Command{
		Use:   "set [--project <project>] [--from-site <page> [--apply]] [--logo <resource>] [--logo-dark|--logo-light|--logo-icon|--logo-wordmark <resource>] [--font-file <resource>] [--font Inter|Fraunces|JetBrains Mono] [--primary #RRGGBB] [--accent …] [--text …] [--background …] [--from-logo]",
		Short: "Change the brand of a project's own Content Settings, or without --project the default's (resources are named like everywhere; colours come from the logo unless you set them); --from-site takes what a website snapshot suggests, --apply also puts it on the project's latest cut",
		Long: `Change the brand of a project's own Content Settings (every project using them gets it),
or without --project the default's — what new projects use. A project that uses another
project's settings is refused: change them there, or give it its own (metarush content own).

  metarush brand set --logo logo                                         the default's logo (colours from it)
  metarush brand set --project "Spring promo" --from-site acme-com --apply   a client's brand from its website, on the latest cut

Saved versions never change: the answer lists the latest cuts that could use the new
brand (metarush brand apply <project>).

--apply shows the change to the cut first; in a terminal it asks before saving, otherwise
it saves with --confirm <token> (nothing is saved until then, the kit included).`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			for flag, dst := range map[string]**string{"logo": &in.Logo, "logo-dark": &in.LogoDark, "logo-light": &in.LogoLight, "logo-icon": &in.LogoIcon, "logo-wordmark": &in.LogoWordmark,
				"font-file": &in.FontResource, "font": &in.Font, "primary": &in.Primary, "accent": &in.Accent, "text": &in.Text, "background": &in.Background} {
				if c.Flags().Changed(flag) {
					v, _ := c.Flags().GetString(flag)
					*dst = &v
				}
			}
			if in.Apply && in.ProjectID == "" {
				return fail(c, apperr.InvalidInput, "Say which project's cut to put it on: add --project <project>.", "")
			}
			if !in.Apply {
				return runOp(c, "brand.set", in, func(o any) { printBrand(o.(*ops.BrandShowOut), details) })
			}
			return runBrandSet(c, in, details)
		},
	}
	fl := set.Flags()
	fl.StringVar(&logo, "logo", "", "the main logo; empty clears it")
	fl.StringVar(&logoDark, "logo-dark", "", "the logo for dark backgrounds")
	fl.StringVar(&logoLight, "logo-light", "", "the logo for light backgrounds")
	fl.StringVar(&logoIcon, "logo-icon", "", "a square icon")
	fl.StringVar(&logoWordmark, "logo-wordmark", "", "the wordmark (the name written out)")
	fl.StringVar(&fontFile, "font-file", "", "a font file from the library")
	fl.StringVar(&font, "font", "", "a bundled font: Inter, Fraunces or JetBrains Mono")
	fl.StringVar(&primary, "primary", "", "#RRGGBB")
	fl.StringVar(&accent, "accent", "", "#RRGGBB")
	fl.StringVar(&text, "text", "", "#RRGGBB")
	fl.StringVar(&background, "background", "", "#RRGGBB")
	fl.BoolVar(&in.FromLogo, "from-logo", false, "take the colours from the logo again")
	fl.StringVar(&in.ProjectID, "project", "", "a project with its own settings: change their brand (default: the default's)")
	fl.StringVar(&in.FromSite, "from-site", "", "a web page snapshot: take the logo, colours and font it suggests")
	fl.BoolVar(&in.Apply, "apply", false, "also put the kit on the project's latest cut (shows the change first)")
	fl.StringVar(&in.Branch, "branch", "", "with --apply: the cut's branch (default: main)")
	fl.BoolVar(&in.DryRun, "dry-run", false, "only check: the kit it would be and the change to the cut; nothing is saved")
	fl.StringVar(&in.ConfirmToken, "confirm", "", "with --apply: token from a check of exactly this change")
	fl.StringVarP(&in.Instruction, "message", "m", "", "with --apply: what was asked (recorded with the new version)")
	addDetailsFlag(set, &details)
	var sug ops.BrandSuggestIn
	suggest := &cobra.Command{
		Use:   "suggest [--from <page>] [--project <project>]",
		Short: "Suggest a brand kit from a website snapshot: its logo, colours and font, each with why (nothing is saved; brand set --from-site takes it)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if sug.From == "" && sug.ProjectID == "" {
				return fail(c, apperr.InvalidInput, "Say which web page snapshot: add --from <page>, or --project <project> for its website.", "")
			}
			return runOp(c, "brand.suggest", sug, func(o any) { printBrandSuggestion(o.(*ops.BrandSuggestOut), details) })
		},
	}
	suggest.Flags().StringVar(&sug.From, "from", "", "a web page snapshot (its name, title or ID); default: the project's newest one")
	suggest.Flags().StringVar(&sug.ProjectID, "project", "", "the project the kit would be for")
	addDetailsFlag(suggest, &details)
	var f cutEditFlags
	apply := &cobra.Command{
		Use: "apply <project>", Short: "Use the brand in a project's latest cut — the brand of the settings it uses: its colours, font and logos (shows the change first)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runCutEdit(c, "brand.apply", &f, func(common ops.CutEditCommon) any {
				common.ProjectID = args[0]
				return ops.BrandApplyIn{CutEditCommon: common}
			})
		},
	}
	f.add(apply)
	cmd.AddCommand(show, set, suggest, apply)
	return cmd
}

// runBrandSet is brand set --apply: the kit and the change to the project's
// latest cut are shown first, and a person at a terminal is asked before both
// are saved (the same gate as the confirm token, which every other caller
// passes); nothing is saved until then.
func runBrandSet(c *cobra.Command, in ops.BrandSetIn, details bool) error {
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	call := func(x ops.BrandSetIn) (*ops.BrandShowOut, error) {
		b, err := json.Marshal(x)
		if err != nil {
			return nil, err
		}
		res, err := ops.Call(c.Context(), a, "brand.set", b, ops.Caller{})
		if err != nil {
			return nil, err
		}
		return res.(*ops.BrandShowOut), nil
	}
	if in.ConfirmToken == "" && !in.DryRun && interactive() && !jsonMode(c) {
		dry := in
		dry.DryRun = true
		r, err := call(dry)
		if err != nil {
			return failErr(c, err)
		}
		if r.Cut != nil && !r.Cut.NoChange {
			printBrand(r, details)
			printCutEdit(c, r.Cut, "")
			if r.Cut.Check == nil || r.Cut.Check.ConfirmToken == "" {
				return nil // it can't be made yet: the problems are shown
			}
			if !askYes("Save the brand kit and this change to the cut as a new version?", false) {
				return fail(c, apperr.Canceled, "not saved", "")
			}
			in.ConfirmToken = r.Cut.Check.ConfirmToken
		}
	}
	r, err := call(in)
	if err != nil {
		return failErr(c, err)
	}
	return emit(c, r, func() {
		printBrand(r, details)
		if r.Cut != nil {
			next := ""
			if r.Cut.Check != nil && r.Cut.Check.ConfirmToken != "" {
				next = sameCommandWith(c, "--confirm", r.Cut.Check.ConfirmToken)
			}
			printCutEdit(c, r.Cut, next)
		}
	})
}

func printBrand(o *ops.BrandShowOut, details bool) {
	fmt.Println(o.Message)
	printLogos(o)
	b := o.Brand
	fmt.Printf("Colours: primary %s · accent %s · text %s · background %s\n", b.Primary, b.Accent, b.Text, b.Background)
	switch {
	case o.FontFile != nil:
		fmt.Println("Font:", theirs(o.FontFile.Title))
	case b.Font != "":
		fmt.Println("Font:", b.Font)
	}
	if len(o.Kit.Suggested) > 0 {
		fmt.Println("From the logo:", strings.Join(o.Kit.Suggested, " "))
	}
	if len(o.Sites) > 0 && o.Cut == nil {
		var sites []string
		for _, s := range o.Sites {
			sites = append(sites, s.Site)
		}
		fmt.Println("A brand kit can be suggested from its snapshot of " + strings.Join(sites, ", ") + ".")
	}
	if len(o.LatestCuts) > 0 && o.Cut == nil {
		fmt.Println("\nUse the new brand on the latest cut (saved versions never change):")
		for _, c := range o.LatestCuts {
			fmt.Printf("  %s, version %d — metarush brand apply %s\n", theirs(c.ProjectName), c.Version, c.ProjectID)
		}
	}
	if details {
		if o.From != nil {
			fmt.Println("settings of:", o.From.ID)
		}
		if o.Kit.Logo != "" {
			fmt.Println("logo resource:", o.Kit.Logo)
		}
		for _, s := range o.Sites {
			fmt.Printf("site snapshot: %s (%s)\n", s.Name, s.ID)
		}
	}
}

// printLogos lists the brand's named logos that are set (D198).
func printLogos(o *ops.BrandShowOut) {
	for _, l := range o.Logos {
		if l.Resource == nil {
			continue
		}
		label := l.Label
		if l.Which == "main" {
			label = "Logo"
		} else if l.Which == "dark" || l.Which == "light" {
			label = "Logo " + strings.ToLower(l.Label)
		}
		fmt.Println(label+":", theirs(l.Resource.Title))
	}
}

func printBrandSuggestion(o *ops.BrandSuggestOut, details bool) {
	fmt.Println(o.Message)
	for _, w := range o.Why {
		fmt.Println("  " + w)
	}
	var others []string
	for _, l := range o.Logos {
		if !l.Chosen {
			others = append(others, theirs(l.Resource.Title))
		}
	}
	if len(others) > 0 {
		fmt.Println("Other pictures it kept:", strings.Join(others, ", "))
	}
	if !o.InUse && o.ProjectName != "" {
		fmt.Println("Nothing is saved yet.")
		if !details {
			fmt.Println("(Add --details to see the command that takes it for " + theirs(o.ProjectName) + ".)")
		}
	}
	if details {
		fmt.Println("page:", o.Site.Name, "("+o.Site.ID+")")
		if o.Kit.Logo != "" {
			fmt.Println("logo resource:", o.Kit.Logo)
		}
		if o.Kit.FontResource != "" {
			fmt.Println("font resource:", o.Kit.FontResource)
		}
		if o.ProjectID != "" {
			fmt.Println("next:", "metarush brand set --project "+shellArg(o.ProjectID)+" --from-site "+shellArg(o.Site.ID)+" --apply")
		}
	}
}

// projectLinkCmds are `metarush project link|unlink|links|use`.
func projectLinkCmds() []*cobra.Command {
	var details bool
	var only []string
	var items []string
	link := &cobra.Command{
		Use:   "link <this project> <other project> [--only kind=resources|tag=logo|person=Ada|folder=Brand] [--items a,b]",
		Short: "Let a project use another project's things — by default everything: footage, resources, approved motion graphics and its cut (nothing is copied)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.ProjectLinkIn{ProjectID: args[0], From: args[1], Items: items}
			if len(only) > 0 {
				f := &ops.LinkFilterIn{}
				for _, o := range only {
					k, v, ok := strings.Cut(o, "=")
					if !ok {
						return fail(c, apperr.InvalidInput, fmt.Sprintf("--only %s: write it as kind=…, tag=…, person=… or folder=…", o), "")
					}
					switch strings.TrimSpace(k) {
					case "kind":
						f.Kinds = append(f.Kinds, v)
					case "tag":
						f.Tags = append(f.Tags, v)
					case "person":
						f.People = append(f.People, v)
					case "folder":
						f.Folders = append(f.Folders, v)
					default:
						return fail(c, apperr.InvalidInput, fmt.Sprintf("--only %s: MetaRush narrows links by kind, tag, person or folder", o), "")
					}
				}
				in.Only = f
			}
			return runOp(c, "project.link", in, func(o any) {
				out := o.(*ops.ProjectLinkOut)
				fmt.Println(out.Message)
				if details && out.Link != nil {
					fmt.Println("link:", out.Link.ID)
				}
			})
		},
	}
	link.Flags().StringSliceVar(&only, "only", nil, "narrow it: kind=footage|resources|motion|cuts, tag=…, person=…, folder=… (repeatable)")
	link.Flags().StringSliceVar(&items, "items", nil, "specific things of that project (names, or cut)")
	addDetailsFlag(link, &details)
	unlink := &cobra.Command{
		Use: "unlink <this project> <other project>", Short: "Stop using another project's things; what this project's cuts already use stays (and it says what)", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "project.unlink", ops.ProjectUnlinkIn{ProjectID: args[0], From: args[1]}, func(o any) { fmt.Println(o.(*ops.ProjectLinkOut).Message) })
		},
	}
	links := &cobra.Command{
		Use: "links <project>", Short: "List the projects a project uses, and the projects that use it", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "project.links", ops.ProjectLinksIn{ProjectID: args[0]}, func(o any) {
				out := o.(*ops.ProjectLinksOut)
				if len(out.Links) == 0 {
					fmt.Println("It doesn't use another project's things.")
				}
				for _, l := range out.Links {
					fmt.Println(l.Text + ".")
				}
				for _, l := range out.UsedBy {
					fmt.Printf("%s uses it: %s.\n", l.ProjectName, strings.TrimPrefix(strings.TrimPrefix(l.Text, "Everything from "), "Part of "))
				}
			})
		},
	}
	use := &cobra.Command{
		Use: "use <this project> <other project> <item>...", Short: "Use specific things of another project (footage, resources, motion graphics, or cut for its cut) without linking all of it", Args: cobra.MinimumNArgs(3),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "project.use", ops.ProjectUseIn{ProjectID: args[0], From: args[1], Items: args[2:]}, func(o any) { fmt.Println(o.(*ops.ProjectLinkOut).Message) })
		},
	}
	return []*cobra.Command{link, unlink, links, use}
}

// cutResourceCmds are `metarush cut update|nest|copy-in|where`.
func cutResourceCmds() []*cobra.Command {
	var fu cutEditFlags
	var upd ops.CutUpdateIn
	update := &cobra.Command{
		Use:   "update <project> [--resources a,b] [--cuts clip,…] [--only resources|cuts]",
		Short: "Update the latest cut's resources and nested cuts to their newest versions (an explicit change: shown first, then saved)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runCutEdit(c, "cut.update", &fu, func(common ops.CutEditCommon) any {
				x := upd
				x.CutEditCommon, x.ProjectID = common, args[0]
				return x
			})
		},
	}
	update.Flags().StringSliceVar(&upd.Resources, "resources", nil, "only these resources (short names)")
	update.Flags().StringSliceVar(&upd.Cuts, "cuts", nil, "only these nested clips (names or numbers)")
	update.Flags().StringVar(&upd.Only, "only", "", "resources or cuts")
	fu.add(update)
	var fn cutEditFlags
	var nest ops.CutNestIn
	var after, before, at string
	nestCmd := &cobra.Command{
		Use:   "nest <project> <other project> [--revision rN] [--after <clip> | --before <clip> | --at start|end|N] [--in 0:02] [--out 0:10]",
		Short: "Put another project's cut into this cut as one clip, pinned to one version (picture, sound, narration and graphics as it plays there)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			switch {
			case after != "":
				nest.Position = "after " + after
			case before != "":
				nest.Position = "before " + before
			case at != "":
				nest.Position = at
			}
			return runCutEdit(c, "cut.nest", &fn, func(common ops.CutEditCommon) any {
				x := nest
				x.CutEditCommon, x.ProjectID, x.From = common, args[0], args[1]
				return x
			})
		},
	}
	nf := nestCmd.Flags()
	nf.StringVar(&nest.Revision, "revision", "", "which version of that cut (rN; default its latest)")
	nf.StringVar(&after, "after", "", "after this clip")
	nf.StringVar(&before, "before", "", "before this clip")
	nf.StringVar(&at, "at", "", "start, end or a place number (default the end, just before a locked ending)")
	nf.StringVar(&nest.In, "in", "", "start in that cut (default the beginning)")
	nf.StringVar(&nest.Out, "out", "", "end in that cut (default the end)")
	nf.StringVar(&nest.Name, "name", "", "the clip's name")
	fn.add(nestCmd)
	var fc cutEditFlags
	var cin ops.CutCopyInIn
	var cafter, cbefore, cat string
	copyIn := &cobra.Command{
		Use:   "copy-in <project> <other project> [--revision rN] [--after <clip> | --before <clip> | --at start|end|N]",
		Short: "Copy another project's cut into this cut as a starting point: clips, transitions, graphics, sound and narration (no live link)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			switch {
			case cafter != "":
				cin.Position = "after " + cafter
			case cbefore != "":
				cin.Position = "before " + cbefore
			case cat != "":
				cin.Position = cat
			}
			return runCutEdit(c, "cut.copyIn", &fc, func(common ops.CutEditCommon) any {
				x := cin
				x.CutEditCommon, x.ProjectID, x.From = common, args[0], args[1]
				return x
			})
		},
	}
	cf := copyIn.Flags()
	cf.StringVar(&cin.Revision, "revision", "", "which version of that cut (rN; default its latest)")
	cf.StringVar(&cafter, "after", "", "after this clip")
	cf.StringVar(&cbefore, "before", "", "before this clip")
	cf.StringVar(&cat, "at", "", "start, end or a place number")
	fc.add(copyIn)
	var details bool
	where := &cobra.Command{
		Use: "where <project>", Short: "List the cuts that play this project's cut inside them", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "cut.where", ops.CutWhereIn{ProjectID: args[0]}, func(o any) {
				out := o.(*ops.CutWhereOut)
				fmt.Println(out.Summary)
				printUses(out.Uses, details)
			})
		},
	}
	addDetailsFlag(where, &details)
	return []*cobra.Command{update, nestCmd, copyIn, where}
}

// assetExtraCmds are `metarush asset where|move|delete`.
func assetExtraCmds() []*cobra.Command {
	var details bool
	var project string
	where := &cobra.Command{
		Use: "where <asset> [--project <project>]", Short: "List every place footage is used: its projects, projects that see it through a link, and the cuts (and older versions) that play it", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "asset.where", ops.AssetRefIn{AssetID: args[0], ProjectID: project}, func(o any) {
				out := o.(*ops.AssetWhereOut)
				fmt.Println(out.Asset.Title + ": " + out.Summary)
				printUses(out.Uses, details)
			})
		},
	}
	where.Flags().StringVar(&project, "project", "", "where to look it up by name (ID or name)")
	addDetailsFlag(where, &details)
	var mv ops.AssetMoveIn
	move := &cobra.Command{
		Use: "move <asset> --from <project> --to <project>", Short: "Move footage from one project (or, without --from, the Inbox) to another", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			mv.AssetID = args[0]
			return runOp(c, "asset.move", mv, func(o any) { fmt.Println(o.(*ops.AssetMoveOut).Message) })
		},
	}
	move.Flags().StringVar(&mv.From, "from", "", "the project it leaves (ID or name); empty: the Inbox")
	move.Flags().StringVar(&mv.To, "to", "", "the project it goes to (ID or name)")
	var del ops.AssetDeleteIn
	deleteCmd := &cobra.Command{
		Use:   "delete <asset> --forever [--project <project>] [--dry-run]",
		Short: "Delete footage for good (the file, preview, transcript and speaker data, on every machine) — people only, in their own terminal; refused while any project but this one has it or any cut uses it",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			del.AssetID = args[0]
			if assistantMode() || !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person can delete footage, in the dashboard or in their own terminal.", "Run this yourself in a terminal (agents and the dashboard assistant can't delete).")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			check := del
			check.DryRun = true
			res, err := ops.CallTyped[ops.AssetDeleteOut](c.Context(), a, "asset.delete", check, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			if del.DryRun || !res.Allowed {
				if jsonMode(c) {
					if err := emit(c, res, func() {}); err != nil || res.Allowed {
						return err
					}
					return &exitError{code: 4}
				}
				fmt.Println(res.Message)
				if !res.Allowed {
					printUses(res.Uses, true)
					return &exitError{code: 4}
				}
				return nil
			}
			if !jsonMode(c) {
				fmt.Println(res.Message)
			}
			if !del.Forever {
				return fail(c, apperr.InvalidInput, "Add --forever to delete it for good.", "")
			}
			if ask(fmt.Sprintf("Type %q to delete “%s” for good", "delete", res.Title), "") != "delete" {
				return fail(c, apperr.Canceled, "nothing was deleted", "")
			}
			return runOpWith(c, a, "asset.delete", del, ops.Caller{Human: true}, func(o any) {
				out := o.(*ops.AssetDeleteOut)
				fmt.Println(out.Message)
			})
		},
	}
	deleteCmd.Flags().BoolVar(&del.Forever, "forever", false, "yes, for good (it can't be undone)")
	deleteCmd.Flags().StringVar(&del.ProjectID, "project", "", "the project it is deleted from (it may be in no other)")
	deleteCmd.Flags().BoolVar(&del.DryRun, "dry-run", false, "only say whether it can be deleted")
	return []*cobra.Command{where, move, deleteCmd}
}

var _ = os.Stdout

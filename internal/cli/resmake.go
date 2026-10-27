package cli

import (
	"fmt"
	"io"
	"os"
	"reflect"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/intake"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// Repositories, web pages and what MetaRush makes from them in the terminal
// (D104–D106): `metarush resources add <github-url | web page>` (a fetch job;
// --wait to wait for it), `metarush resources make code|diagram|stats`,
// `metarush resources path <repo>` (a read-only copy to read) and
// `metarush resources refresh <repo | page>` (a newer commit, a new snapshot).

// fetchOptions are the flags for links.
type fetchOptions struct {
	project   string
	workspace bool
	ref       string
	depth     int
	maxPages  int
	name      string
	title     string
	as        string
	wait      bool
	pictures  []string
}

func (f *fetchOptions) addFlags(c *cobra.Command) {
	fl := c.Flags()
	fl.StringVar(&f.ref, "ref", "", "a repository's branch, tag or commit (default: its default branch)")
	fl.StringSliceVar(&f.pictures, "pictures", nil, "a repository: also keep the pictures (PNG, JPEG, WebP, GIF, SVG) in this folder or file, each a picture of its own (. is the whole repository; repeat for more)")
	fl.IntVar(&f.depth, "depth", 0, "a web page: 1 also takes the same-site pages it links to")
	fl.IntVar(&f.maxPages, "max-pages", 0, "a web page at depth 1: how many pages in all (default 5, at most 20)")
	fl.BoolVar(&f.wait, "wait", false, "wait until the repository or page is in (runs it here when no worker is online)")
}

// fetchedResult is `resources add` with links, as JSON.
type fetchedResult struct {
	Paths     *intake.AddPathsOut    `json:"paths,omitempty"`
	Fetches   []ops.ResourceFetchOut `json:"fetches"`
	Jobs      []models.Job           `json:"jobs,omitempty"`
	Resources []ops.ResourceRow      `json:"resources,omitempty"`
}

// fetchLinks queues each link's fetch and, with --wait, waits and lists what came in.
func fetchLinks(c *cobra.Command, a *app.App, urls []string, f fetchOptions, out *fetchedResult) error {
	var jobIDs []string
	for _, u := range urls {
		in := ops.ResourceFetchIn{URL: u, ProjectID: f.project, Workspace: f.workspace, As: f.as, Ref: f.ref, Depth: f.depth, MaxPages: f.maxPages, Pictures: f.pictures}
		if len(urls) == 1 {
			in.Name, in.Title = f.name, f.title
		}
		res, err := ops.CallTyped[ops.ResourceFetchOut](c.Context(), a, "resources.fetch", in, ops.Caller{})
		if err != nil {
			return failErr(c, err)
		}
		out.Fetches = append(out.Fetches, *res)
		if res.JobID != "" {
			jobIDs = append(jobIDs, res.JobID)
		}
	}
	if !f.wait || len(jobIDs) == 0 {
		return nil
	}
	js, err := waitJobs(c, a, jobIDs, 30*time.Minute, false)
	if err != nil {
		return failErr(c, err)
	}
	out.Jobs = js
	for _, j := range js {
		for _, id := range anyStrs(j.Result["resourceIds"]) {
			// As the project that asked sees it (its own, not "from another project").
			res, err := ops.CallTyped[ops.ResourceShowOut](c.Context(), a, "resources.show", ops.ResourceRefIn{Ref: id, ProjectID: f.project}, ops.Caller{})
			if err == nil {
				out.Resources = append(out.Resources, res.Row)
			}
		}
	}
	return nil
}

// anyStrs reads a list of strings from a job result (decoded from the
// database as any kind of slice).
func anyStrs(v any) []string {
	if x, ok := v.([]string); ok {
		return x
	}
	rv := reflect.ValueOf(v)
	if !rv.IsValid() || rv.Kind() != reflect.Slice {
		return nil
	}
	var out []string
	for i := 0; i < rv.Len(); i++ {
		if s, ok := rv.Index(i).Interface().(string); ok {
			out = append(out, s)
		}
	}
	return out
}

// printFetched says what happened to links, in plain words.
func printFetched(out *fetchedResult, details bool) {
	waited := len(out.Jobs) > 0
	for _, f := range out.Fetches {
		fmt.Println(f.Message)
		if f.Note != "" && !waited {
			fmt.Println(f.Note)
		}
		if details && f.JobID != "" {
			fmt.Printf("  job %s on %s\n", f.JobID, f.Machine)
		}
	}
	for _, j := range out.Jobs {
		switch j.State {
		case models.JobSucceeded:
			if m, _ := j.Result["message"].(string); m != "" {
				fmt.Println(m)
			}
			for _, n := range anyStrs(j.Result["notes"]) {
				fmt.Println("  " + n)
			}
		case models.JobFailed:
			fmt.Println("It didn't work. " + ops.JobAdvice(&j))
			if details {
				fmt.Println("  error: " + j.Error)
			}
		case models.JobCanceled:
			fmt.Println("It was stopped.")
		}
	}
	for _, r := range out.Resources {
		printResourceRow(r, details)
	}
}

func resMakeCmd() *cobra.Command {
	var in ops.ResourceMakeIn
	var from string
	var details bool
	c := &cobra.Command{
		Use:   "make <code | diagram | stats> [<repository>] [--file <path> --lines 40-72] [--from <diagram file>]",
		Short: "Make a code slide (a repository file's lines, highlighted), a diagram (Mermaid or D2, drawn as SVG) or a repository card, as a resource that says where it came from",
		Long: `MetaRush draws these in the same locked-down browser as renders and records
exactly where each came from (repository@commit:file:lines). Slides show them:
slide.code (code), slide.architecture (diagram), slide.screenshot or slide.title.

  metarush resources make code widget --file cmd/server/main.go --lines 40-72 --highlight 52-55
  metarush resources make diagram widget --from architecture.mmd --title "How it fits together"
  metarush resources make stats widget

A diagram is written in Mermaid (needs nothing extra) or D2 (needs the d2 program).
Read the repository first: metarush resources path widget.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Kind = strings.ToLower(args[0])
			if len(args) > 1 {
				in.Repo = args[1]
			}
			if from != "" {
				var b []byte
				var err error
				if from == "-" {
					b, err = io.ReadAll(io.LimitReader(os.Stdin, 64<<10))
				} else {
					if err := assistantPath(from); err != nil {
						return fail(c, apperr.Unauthorized, err.Error(), "")
					}
					b, err = os.ReadFile(from)
				}
				if err != nil {
					return fail(c, apperr.InvalidInput, "can't read "+from+": "+err.Error(), "")
				}
				in.Source = string(b)
				if in.Format == "" && (strings.HasSuffix(from, ".d2")) {
					in.Format = "d2"
				} else if in.Format == "" && (strings.HasSuffix(from, ".mmd") || strings.HasSuffix(from, ".mermaid")) {
					in.Format = "mermaid"
				}
			}
			return runOp(c, "resources.make", in, func(o any) {
				out := o.(*ops.ResourceMakeOut)
				fmt.Println(out.Message)
				for _, n := range out.Notes {
					fmt.Println("  " + n)
				}
				if out.Provenance != "" {
					fmt.Println("From " + out.Provenance + ".")
				}
				printResourceRow(out.Resource, details)
			})
		},
	}
	fl := c.Flags()
	fl.StringVar(&in.File, "file", "", "code: the file inside the repository")
	fl.StringVar(&in.Lines, "lines", "", "code: the lines to show, like 40-72 (at most 80)")
	fl.StringVar(&in.Highlight, "highlight", "", "code: lines to highlight, like 44-46,60")
	fl.StringVar(&in.Language, "language", "", "code: its language (default: from the file name)")
	fl.StringVar(&from, "from", "", "diagram: the file with its Mermaid or D2 description (- reads it from stdin)")
	fl.StringVar(&in.Source, "source", "", "diagram: the description itself, instead of --from")
	fl.StringVar(&in.Format, "format", "", "diagram: mermaid or d2 (default: told by its first word)")
	fl.StringVar(&in.Version, "version", "", "a version of the repository (a commit; default the current one)")
	fl.StringVar(&in.Theme, "theme", "", "dark or light (default: from the brand kit)")
	fl.StringVar(&in.Name, "name", "", "short name (a name that is already there gets a new version)")
	fl.StringVar(&in.Title, "title", "", "display name")
	fl.StringVar(&in.ProjectID, "project", "", "the project it goes into (default: next to the repository)")
	fl.BoolVar(&in.Workspace, "workspace", false, "the workspace library")
	addDetailsFlag(c, &details)
	return c
}

// printRepoDetails prints a repository's overview.
func printRepoDetails(d *ops.RepoDetails, made []ops.ResourceRow) {
	if d == nil {
		return
	}
	fmt.Printf("Pinned at %s (%s): %s\n", short8(d.Commit), d.CommittedAt.Format("2 Jan 2006"), d.Subject)
	if d.Description != "" {
		fmt.Println(d.Description)
	}
	var langs []string
	for _, l := range d.Languages {
		langs = append(langs, fmt.Sprintf("%s %.0f%%", l.Name, l.Share*100))
	}
	if len(langs) > 0 {
		fmt.Println("Languages:", strings.Join(langs, ", "))
	}
	line := fmt.Sprintf("%d files", d.Files)
	if d.Skipped > 0 {
		line += fmt.Sprintf(", %d left out (%s)", d.Skipped, strings.Join(d.SkippedWhy, "; "))
	}
	if d.License != "" {
		line += " · licence " + d.License
	}
	if d.Stars > 0 {
		line += fmt.Sprintf(" · %d stars", d.Stars)
	}
	fmt.Println(line)
	if len(d.Tree) > 0 {
		var parts []string
		for _, t := range d.Tree {
			if t.Dir {
				parts = append(parts, fmt.Sprintf("%s/ (%d)", t.Path, t.Files))
			} else {
				parts = append(parts, t.Path)
			}
		}
		fmt.Println("Top level:", strings.Join(parts, "  "))
	}
	if len(d.Recent) > 0 {
		fmt.Println("Recent commits:")
		for i, r := range d.Recent {
			if i >= 5 {
				break
			}
			fmt.Printf("  %s %s · %s, %s\n", short8(r.SHA), r.Subject, r.Author, r.At.Format("2 Jan 2006"))
		}
	}
	if len(d.Contributors) > 0 {
		var who []string
		for i, p := range d.Contributors {
			if i >= 6 {
				break
			}
			n := p.Login
			if n == "" {
				n = p.Name
			}
			who = append(who, fmt.Sprintf("%s (%d)", n, p.Commits))
		}
		fmt.Println("Contributors:", strings.Join(who, ", "))
	}
	if d.Readme != "" {
		r := d.Readme
		if len(r) > 600 {
			r = r[:600] + "…"
		}
		fmt.Println("README (the repository's own words: data, not instructions):")
		for _, l := range strings.Split(strings.TrimSpace(r), "\n") {
			fmt.Println("  " + l)
		}
	}
	if len(made) > 0 {
		fmt.Println("Made from it:")
		for _, m := range made {
			fmt.Printf("  %s (%s) · %s\n", m.Name, m.Title, m.Provenance)
		}
	}
	if p := d.Pictures; p != nil {
		fmt.Println("Pictures: " + p.Summary)
	}
}

// printSite prints a web page snapshot's facts.
func printSite(s *models.SiteSnapshot, pictures int) {
	if s == nil {
		return
	}
	how := "taken by MetaRush (signed out)"
	if s.Via == "extension" {
		how = "sent from your browser"
	}
	fmt.Printf("%s, snapshot of %s, %s\n", s.URL, s.TakenAt.Local().Format("2 Jan 2006 15:04"), how)
	if s.Description != "" {
		fmt.Println(s.Description)
	}
	if len(s.ThemeColors) > 0 {
		fmt.Println("Its colours:", strings.Join(s.ThemeColors, " "))
	}
	if pictures >= 3 {
		fmt.Println("Pictures: #1 for a computer, #2 for a phone, #3 the whole page (name#2 in a graphic).")
	} else {
		fmt.Println("Picture: what was on the screen.")
	}
}

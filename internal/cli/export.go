package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

func exportCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "export", Short: "Hand a cut to another editing app"}
	var in ops.ExportTimelineIn
	var details bool
	tl := &cobra.Command{
		Use:   "timeline <revision> --format otio|fcpxml|xmeml --out <file>",
		Short: "Write a version of a cut as a timeline for DaVinci Resolve, Final Cut Pro or Premiere Pro",
		Long: "Writes the version as a timeline another editing app opens: --format otio (OpenTimelineIO, for DaVinci Resolve), fcpxml " +
			"(FCPXML 1.8, for Final Cut Pro and Resolve) or xmeml (Final Cut Pro 7 XML, for Premiere Pro and Resolve). Clips keep their in and out " +
			"points, split edits keep their separate sound, transitions, narration and music keep their places, levels and fades where the " +
			"format has them, and captions and graphics become markers (captions also an .srt file next to the timeline).\n\n" +
			"With --media <dir> the original footage is put in that folder under readable names (instant copies where the disk allows; --link " +
			"for symbolic links) and the timeline points there; otherwise it points at MetaRush's own copies on this machine. Existing files are " +
			"never overwritten (--replace for the timeline itself). What a format cannot carry exactly is listed after the export.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.RevisionID = args[0]
			in.Format = strings.ToLower(strings.TrimSpace(in.Format))
			if in.Format == "" {
				return fail(c, apperr.InvalidInput, "choose a format: --format otio (Resolve), fcpxml (Final Cut Pro) or xmeml (Premiere Pro)", "")
			}
			return runOp(c, "export.timeline", in, func(o any) {
				r := o.(*ops.ExportTimelineOut)
				fmt.Println(r.Summary)
				fmt.Println(r.Open)
				if r.Captions != "" {
					fmt.Println("The captions are also in an .srt file next to it.")
				}
				if len(r.Notes) > 0 {
					fmt.Println("Not carried exactly:")
					for _, n := range r.Notes {
						fmt.Println("  - " + upperFirst(n))
					}
				}
				if details {
					fmt.Println("Timeline:", r.File)
					if r.Captions != "" {
						fmt.Println("Captions:", r.Captions)
					}
					if r.MediaDir != "" {
						fmt.Println("Footage folder:", r.MediaDir)
					}
					for _, m := range r.Media {
						fmt.Printf("  %-8s %s (%s, sha256 %s…)\n", m.How, m.Path, fmtBytes(m.Size), m.SHA256[:12])
					}
				}
			})
		},
	}
	tl.Flags().StringVar(&in.Format, "format", "", "otio (DaVinci Resolve) | fcpxml (Final Cut Pro, Resolve) | xmeml (Premiere Pro, Resolve)")
	tl.Flags().StringVar(&in.Out, "out", "", "the timeline file to write (the format's extension is added when missing)")
	tl.Flags().StringVar(&in.Media, "media", "", "put the original footage in this folder under readable names and point the timeline there")
	tl.Flags().BoolVar(&in.Link, "link", false, "with --media: symbolic links to MetaRush's local copies instead of copies")
	tl.Flags().BoolVar(&in.Replace, "replace", false, "replace the timeline file (and its .srt) if they exist")
	tl.Flags().BoolVar(&in.NoCaptions, "no-captions", false, "do not write the captions as an .srt file")
	tl.Flags().BoolVar(&details, "details", false, "also show file paths and how each piece of footage was placed")
	_ = tl.MarkFlagRequired("out")
	cmd.AddCommand(tl)
	return cmd
}

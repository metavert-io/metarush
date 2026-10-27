package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/visual"
)

// tileLine is one contact sheet frame in plain words: its label, and why it was
// chosen when the label does not already say so.
func tileLine(t ops.SheetTile) string {
	s := fmt.Sprintf("  #%-2d %s", t.N, t.Caption)
	if t.Why != "" && !strings.HasSuffix(t.Caption, t.Why) {
		s += "  (" + t.Why + ")"
	}
	return s
}

// visualsCmd is `metarush footage visuals <asset>`: what's on screen, in plain lines.
func visualsCmd() *cobra.Command {
	var in ops.VisualsIn
	var details bool
	cmd := &cobra.Command{
		Use:   "visuals <asset-id>",
		Short: "What's on screen in a clip, line by line: shot type, faces and whose, setting, things, on-screen text",
		Long: "Reads the on-screen index MetaRush makes on a Mac with Apple Vision (about one frame a second plus the moments the picture changes;\n" +
			"nothing is uploaded). A face shows a name only where a person confirmed it. Look at frames before claiming anything visual.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.AssetID = args[0]
			return runOp(c, "footage.visuals", in, func(o any) {
				v := o.(*ops.VisualsOut)
				if v.State != "available" {
					fmt.Println(v.Note)
				} else {
					fmt.Printf("%s: %s\n", v.Title, v.Summary)
					for _, l := range v.Lines {
						fmt.Println("  " + l.Line)
					}
				}
				if v.Described != nil && len(v.Described.Scenes) > 0 {
					fmt.Println("Scenes, as Claude Code described them:")
					for _, s := range v.Described.Scenes {
						fmt.Printf("  %s–%s %s\n", visual.Clock(s.From), visual.Clock(s.To), s.Text)
					}
				}
				if v.State != "available" {
					return
				}
				if !details {
					return
				}
				if len(v.Labels) > 0 {
					var ls []string
					for i, l := range v.Labels {
						if i == 12 {
							break
						}
						ls = append(ls, fmt.Sprintf("%s %.0fs", l.Label, l.Seconds))
					}
					fmt.Println("seen: " + strings.Join(ls, ", "))
				}
				for _, t := range v.Text {
					fmt.Printf("text %s–%s “%s”\n", visual.Clock(t.From), visual.Clock(t.To), t.Text)
				}
				for _, f := range v.Faces {
					fmt.Printf("face %s %s–%s %s\n", f.ID, visual.Clock(f.From), visual.Clock(f.To), faceWho(f))
				}
			})
		},
	}
	cmd.Flags().Float64Var(&in.From, "from", 0, "range start (s)")
	cmd.Flags().Float64Var(&in.To, "to", 0, "range end (s)")
	addDetails(cmd, &details)
	cmd.AddCommand(&cobra.Command{
		Use: "status", Short: "Whether clips get looked at (Apple Vision on a Mac) and how many are done", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "footage.visuals.status", ops.VisualStatusIn{}, func(o any) {
				s := o.(*ops.VisualStatusOut)
				fmt.Println(s.Summary)
				fmt.Println("This computer: " + s.Here.Text)
				if s.Here.Fix != "" {
					fmt.Println("  → " + s.Here.Fix)
				}
			})
		},
	})
	return cmd
}

func faceWho(f ops.FaceRow) string {
	switch {
	case f.Name != "":
		return f.Name
	case f.Recognized != nil:
		return fmt.Sprintf("looks like %s (%d%% alike; not confirmed yet)", f.Recognized.Name, int(f.Recognized.Score*100+0.5))
	case f.Maybe != nil:
		return "maybe " + f.Maybe.Name + " (asked)"
	}
	return "no name"
}

// facesCmd is `metarush footage faces …`: faces seen in a clip and whose they are.
func facesCmd() *cobra.Command {
	var details bool
	cmd := &cobra.Command{
		Use:   "faces <asset-id>",
		Short: "Faces seen in a clip: when, and whose (named by a person, or recognized by the face and not confirmed yet)",
		Long: "MetaRush asks \"Is this Maya?\" about a face that is on screen by itself while Maya speaks; answer it with `todos answer`,\n" +
			"or name a face directly with `footage faces name`. Faces a person confirmed teach MetaRush what someone looks like: it names their face in other clips\n" +
			"when it is very sure (\"looks like Maya\", not confirmed yet; `footage faces reject` takes it back) and asks when it is less sure.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "footage.faces", ops.FacesIn{AssetID: args[0]}, func(o any) {
				out := o.(*ops.FacesOut)
				fmt.Println(out.Summary)
				for i, f := range out.Faces {
					line := fmt.Sprintf("  %d. %s–%s %s", i+1, visual.Clock(f.From), visual.Clock(f.To), faceWho(f))
					if details {
						line += "  " + f.ID
						if f.Question != "" {
							line += " (question " + f.Question + ")"
						}
					}
					fmt.Println(line)
				}
			})
		},
	}
	addDetails(cmd, &details)
	list := &cobra.Command{
		Use: "list <asset-id>", Short: "Same as `footage faces <asset-id>` (read-only)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return cmd.RunE(c, args) },
	}
	addDetails(list, &details)
	cmd.AddCommand(list)
	var nm ops.FaceNameIn
	name := &cobra.Command{
		Use: `name <face-id> "<name>"`, Short: "Say whose face it is (in that clip)", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			nm.FaceID, nm.Name = args[0], args[1]
			return runOp(c, "footage.faces.name", nm, func(o any) { fmt.Println(o.(*ops.FaceOut).Done) })
		},
	}
	name.Flags().BoolVar(&nm.NewPerson, "new-person", false, "add the name as someone new even if it is close to an existing name")
	cmd.AddCommand(name)
	var rj ops.FaceRejectIn
	reject := &cobra.Command{
		Use: `reject <face-id> ["<name>"]`, Short: "Say a face is not someone (default: the name it has, or the person it is asked about)", Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			rj.FaceID = args[0]
			if len(args) == 2 {
				rj.Person = args[1]
			}
			return runOp(c, "footage.faces.reject", rj, func(o any) { fmt.Println(o.(*ops.FaceOut).Done) })
		},
	}
	cmd.AddCommand(reject)
	cmd.AddCommand(facePrintsCmd(), facesNotHereCmd())
	return cmd
}

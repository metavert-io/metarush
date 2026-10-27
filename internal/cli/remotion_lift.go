package cli

import (
	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/remotion"
)

// remotion lift (D171): a composition or one of its scenes as a reusable
// motion graphic, the project's theme as a shared library graphics import.

func remotionLiftCmd() *cobra.Command {
	var in remotion.RemotionLiftIn
	var noWait, details bool
	c := &cobra.Command{
		Use:   "lift <name> <Composition> [--scene N|NAME] [--name end-card] [--library brand-theme] [--no-wait]",
		Short: "Turn a composition of a Remotion source, or one of its scenes, into a reusable motion graphic, with the project's theme (colours, fonts, components, words) as a shared library; both are checked with no network and wait for a person's approval",
		Long: `Lifts a scene out of a Remotion project into a project motion graphic that any cut can use: the scene's own code
becomes the graphic (drawn at its own size, fitted to the frame; its props become what can be set), and the files it
shares with the rest of the project become one shared library (its colours, fonts, components and words) that every
graphic lifted from the project uses — so a change of theme is approved once. The Google Fonts its code loads are
added to the workspace's resources. Nothing of the project runs while it is lifted; the graphic and the library are
then checked like every motion graphic (allowed packages, types, no network) and wait for a person under Motion
graphics. --scene picks one scene from the project map (its number or name); without it the whole composition is
lifted.`,
		Example: "  metarush remotion lift teaser End --name end-card\n  metarush remotion lift teaser Teaser --scene 6",
		Args:    cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source, in.Composition = args[0], args[1]
			return runTaskOp(c, "remotion.lift", in, noWait, details)
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	c.Flags().StringVar(&in.Scene, "scene", "", "one scene: its number in the project map or its name")
	c.Flags().StringVar(&in.Name, "name", "", "the motion graphic's name (default: from the scene)")
	c.Flags().StringVar(&in.Library, "library", "", "the shared library's name (default: the source's name and -theme)")
	c.Flags().BoolVar(&noWait, "no-wait", false, "don't wait while it is lifted and checked")
	addDetails(c, &details)
	return c
}

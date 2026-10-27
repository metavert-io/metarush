package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// Answering many questions about faces at once, and how the evidence behind
// the questions about people has done (D189).

// todosNotCmd is `metarush todos not "<name>"`: none of the faces asked about
// as that person are them.
func todosNotCmd() *cobra.Command {
	var in ops.TodoNotPersonIn
	cmd := &cobra.Command{
		Use:   `not "<name>" [--project <id>] [--asset <id>]`,
		Short: "None of the faces asked about as this person are them (every open face question suggesting them, in a project or a clip)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Person = args[0]
			return runOp(c, "todos.notPerson", in, func(o any) { fmt.Println(o.(*ops.FacesAnsweredOut).Summary) })
		},
	}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "only this project")
	cmd.Flags().StringVar(&in.AssetID, "asset", "", "only this video")
	return cmd
}

// todosSkipFacesCmd is `metarush todos skip-faces --asset|--project`.
func todosSkipFacesCmd() *cobra.Command {
	var in ops.TodoSkipFacesIn
	cmd := &cobra.Command{
		Use:   "skip-faces (--asset <id> | --project <id>)",
		Short: "Skip every open question about faces in a clip or a project (those faces aren't asked about again)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			if (in.AssetID == "") == (in.ProjectID == "") {
				return failErr(c, apperr.New(apperr.InvalidInput, "Say which clip (--asset) or which project (--project)."))
			}
			return runOp(c, "todos.skipFaces", in, func(o any) { fmt.Println(o.(*ops.FacesAnsweredOut).Summary) })
		},
	}
	cmd.Flags().StringVar(&in.AssetID, "asset", "", "the video")
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "the project")
	return cmd
}

// facesNotHereCmd is `metarush footage faces not-here <asset> "<name>"`.
func facesNotHereCmd() *cobra.Command {
	return &cobra.Command{
		Use:   `not-here <asset-id> "<name>"`,
		Short: "Say someone appears nowhere on screen in a clip (never suggested for its faces; their voice is no hint there)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "footage.faces.notHere", ops.FacesNotHereIn{AssetID: args[0], Person: args[1]}, func(o any) { fmt.Println(o.(*ops.FacesAnsweredOut).Summary) })
		},
	}
}

// peopleEvidenceCmd is `metarush people evidence`: how often each kind of
// evidence behind the questions about people was right.
func peopleEvidenceCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "evidence",
		Short: "How often each kind of evidence behind the questions about people was right, and which no longer suggest names",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "people.evidence", ops.PeopleEvidenceIn{}, func(o any) {
				out := o.(*ops.PeopleEvidenceOut)
				fmt.Println(out.Summary)
				for _, k := range out.Kinds {
					fmt.Printf("  %s: %s\n", k.Label, k.Words)
				}
			})
		},
	}
}

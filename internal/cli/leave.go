package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/receipt"
)

// contentLeaveCmd is `metarush content leave` (D288, D289): what of a client
// may leave this computer, and to whom.
func contentLeaveCmd() *cobra.Command {
	var in leave.LeaveIn
	cmd := &cobra.Command{
		Use:   "leave [--project <project>]",
		Short: "What of a project may leave this computer, and to whom: footage, voices, faces, words and pictures, to AI services, abilities, Claude, cloud storage, delivery and people outside the workspace",
		Long: `What may leave is a part of Content Settings: for every project using them (a client), which of
their things may leave this computer and to whom. Nothing set keeps everything MetaRush offers.

  metarush content leave --project "Acme"
  metarush content leave set --project "Acme" --nothing                     nothing leaves this computer
  metarush content leave set --project "Acme" --keep ai=footage,voices      no footage or voices to AI services
  metarush content leave set --project "Acme" --only ai=elevenlabs          AI services: only ElevenLabs
  metarush content leave set --project "Acme" --keep claude=words,pictures  Claude reads no transcripts or frames
  metarush content leave set --project "Acme" --only claude=commercial      only a Claude account under commercial terms
  metarush content leave set --project "Acme" --keep storage=all            nothing of theirs in cloud storage
  metarush content leave set --project "Acme" --keep reviewers=words        review links show no words

Places: ai, abilities, claude, storage, delivery, reviewers. Kinds: footage, voices, faces, words, pictures (or all).
Changing them is a person's step; letting more leave takes a person who is an owner.`,
		Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "content.leave", in, func(o any) { printLeave(o.(*leave.LeaveOut)) })
		},
	}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "the project (default: the default settings)")
	cmd.AddCommand(contentLeaveSetCmd())
	return cmd
}

func printLeave(out *leave.LeaveOut) {
	fmt.Println(out.Summary)
	for _, p := range out.Places {
		fmt.Printf("  %s: %s\n", p.Title, p.Summary)
	}
	for _, p := range out.Problems {
		fmt.Println("  ! " + p)
	}
}

func contentLeaveSetCmd() *cobra.Command {
	var in leave.LeaveSetIn
	var nothing, anything bool
	var keep, let, only []string
	var note string
	cmd := &cobra.Command{
		Use:   "set [--project <project>] [--nothing|--anything] [--keep place=kinds] [--let place=kinds] [--only place=who] [--anyone place]",
		Short: "Change what of the projects using a project's own Content Settings may leave this computer (people only; letting more leave takes an owner)",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, args []string) error {
			if nothing && anything {
				return fail(c, apperr.InvalidInput, "Say --nothing or --anything, not both.", "")
			}
			if nothing || anything {
				v := nothing
				in.Nothing = &v
			}
			var err error
			if in.Keep, err = placeLists(keep, "--keep"); err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			if in.Let, err = placeLists(let, "--let"); err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			if in.Only, err = placeLists(only, "--only"); err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			if c.Flags().Changed("note") {
				in.Note = &note
			}
			return peopleOnly(c, "content.leave.set", in, "change what of a client may leave this computer", func(o any) { printLeave(o.(*leave.LeaveOut)) })
		},
	}
	fl := cmd.Flags()
	fl.StringVar(&in.ProjectID, "project", "", "the project whose own Content Settings change (default: the default's)")
	fl.BoolVar(&nothing, "nothing", false, "nothing of these projects leaves this computer")
	fl.BoolVar(&anything, "anything", false, "undo --nothing: what the places say again")
	fl.StringArrayVar(&keep, "keep", nil, "keep kinds on this computer instead of sending them to a place: place=kind,kind (ai=footage,voices)")
	fl.StringArrayVar(&let, "let", nil, "let kinds go to a place again: place=kind,kind")
	fl.StringArrayVar(&only, "only", nil, "only these may receive what goes to a place: place=who,who (ai=elevenlabs; abilities=api.example.com; claude=commercial)")
	fl.StringSliceVar(&in.Anyone, "anyone", nil, "take a place's --only list off")
	fl.BoolVar(&in.Clear, "clear", false, "take every rule off")
	fl.StringVar(&note, "note", "", "why, in your words (the client's contract)")
	return cmd
}

// placeLists reads place=a,b flags.
func placeLists(xs []string, flag string) (map[string][]string, error) {
	if len(xs) == 0 {
		return nil, nil
	}
	out := map[string][]string{}
	for _, x := range xs {
		place, list, ok := strings.Cut(x, "=")
		if !ok || strings.TrimSpace(place) == "" {
			return nil, fmt.Errorf("%s takes place=list, like ai=footage,voices", flag)
		}
		for _, v := range strings.Split(list, ",") {
			if v = strings.TrimSpace(v); v != "" {
				out[strings.TrimSpace(place)] = append(out[strings.TrimSpace(place)], v)
			}
		}
	}
	return out, nil
}

// projectLeavesCmd is `metarush project leaves` (D291).
func projectLeavesCmd() *cobra.Command {
	var in receipt.ProjectLeavesIn
	return &cobra.Command{
		Use:   "leaves <project>",
		Short: "What of a project left this computer, and where: AI services, Claude and AI apps, abilities, delivery and cloud storage, with what its rules kept here",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			return runOp(c, "project.leaves", in, func(o any) {
				out := o.(*receipt.ProjectLeavesOut)
				for _, l := range out.Left.Lines {
					fmt.Println(l)
				}
				for _, r := range out.Left.Refused {
					fmt.Printf("  kept here %s: %s\n", r.At.Local().Format("2 Jan 15:04"), r.Message)
				}
			})
		},
	}
}

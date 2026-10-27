package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// renderFollowCmd is `metarush render follow [on|off]` (D187): whether a review
// copy follows each cut's latest version, made by itself once the cut has been
// left alone for about a minute.
func renderFollowCmd() *cobra.Command {
	return &cobra.Command{
		Use:       "follow [on|off]",
		Short:     "Whether a review copy follows each cut's latest version (made by itself about a minute after the last change); on or off changes it",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"on", "off"},
		RunE: func(c *cobra.Command, args []string) error {
			print := func(o any) {
				f := o.(*ops.FollowOut)
				if f.On {
					fmt.Printf("On: about %s after a cut's last change, MetaRush makes a review copy of its latest version by itself.\n", seconds(float64(f.QuietSeconds)))
				} else {
					fmt.Println("Off: review copies are made only when someone asks for one.")
				}
				if f.UpdatedBy != nil && f.UpdatedBy.UserName != "" && f.UpdatedAt != nil {
					fmt.Printf("Changed by %s, %s.\n", theirs(f.UpdatedBy.UserName), plainWhen(*f.UpdatedAt))
				}
				for _, r := range f.Making {
					fmt.Printf("Making %s: %s.\n", renderWords(r.Quality, r.RevisionNumber), renderStateWords(r.State))
					if detailsMode(c) {
						fmt.Println("  render:", r.ID)
					}
				}
			}
			if len(args) == 0 {
				return runOp(c, "render.follow.show", ops.EmptyIn{}, print)
			}
			switch args[0] {
			case "on", "off":
				return runOp(c, "render.follow.set", ops.FollowSetIn{On: args[0] == "on"}, print)
			}
			return fail(c, apperr.InvalidInput, fmt.Sprintf("“%s” isn't on or off.", args[0]), "Say on or off, or nothing to see how it is set.")
		},
	}
}

// revisionWaitingCmd is `metarush revision waiting` (D188): the versions
// awaiting review, as the dashboard's Studio lists them.
func revisionWaitingCmd() *cobra.Command {
	var in ops.ReviewWaitingIn
	cmd := &cobra.Command{
		Use:   "waiting",
		Short: "Awaiting your review: each cut's latest version an AI app (Claude, Codex, Cursor…) made or someone asked to have reviewed, and the drafts AI apps proposed",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "review.waiting", in, func(o any) {
				w := o.(*ops.ReviewWaitingOut)
				vs := w.Versions
				if len(vs) == 0 && len(w.Changes) == 0 {
					fmt.Println("Nothing is waiting for review.")
					return
				}
				for _, v := range vs {
					name := theirs(v.ProjectName)
					if v.CutTitle != "" {
						name += ", " + theirs(v.CutTitle)
					}
					fmt.Printf("%s: version %d (%s), %s; %s.\n", name, v.Revision.Number, seconds(v.Revision.Duration), plainWhen(v.Revision.CreatedAt), saidWords(v.Said))
					if v.Draft != nil {
						for _, ch := range v.Draft.Changes {
							fmt.Println("  - " + ch)
						}
					}
					if detailsMode(c) {
						fmt.Println("  version:", v.Revision.ID)
						if v.Draft != nil {
							fmt.Println("  draft:", v.Draft.ID)
						}
					}
				}
				for i := range w.Changes {
					printDraft(&w.Changes[i], detailsMode(c))
				}
			})
		},
	}
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "only this project (its ID or name)")
	cmd.Flags().IntVar(&in.Limit, "limit", 0, "at most this many (default 50)")
	return cmd
}

// revisionAskReviewCmd is `metarush revision ask-review <version>` (D188).
func revisionAskReviewCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ask-review <version>",
		Short: "Ask for review: the version waits in “Awaiting your review” until someone approves it or says what should change",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "revision.askReview", ops.AskReviewIn{ID: args[0]}, func(o any) {
				r := o.(*ops.RevisionSummary)
				fmt.Printf("Version %d is waiting for review.\n", r.Number)
				fmt.Println(r.URL)
			})
		},
	}
}

// saidWords is why a version waits, inside a sentence: names keep their
// capitals ("Codex made it", "Maya asked for review"); "Someone" doesn't.
func saidWords(s string) string {
	if rest, ok := strings.CutPrefix(s, "Someone"); ok {
		return "someone" + rest
	}
	return s
}

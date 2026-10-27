package cli

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/remotion"
)

// remotion words (D168, D169): a source's on-screen words as settings — the
// one-time change that makes them settings (a version a person approves), then
// changes of words without new versions (drawn and checked with no network,
// the cuts' clips made again).

func remotionWordsCmd() *cobra.Command {
	var in remotion.RemotionWordsIn
	var details bool
	c := &cobra.Command{
		Use:   "words <name> [--project P]",
		Short: "A Remotion source's on-screen words: its settings (as written in the code and as changed), or where its words are written when they aren't settings yet",
		Long: `The words a Remotion source puts on screen. When its words are settings (after a one-time change a person
approved), each one is listed with the words as the code writes them, the words now, and where the project
map saw it; change them with metarush remotion words set. Otherwise it says which files hold the words, and
metarush remotion words setup makes them settings.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			return runOp(c, "remotion.words", in, func(o any) { printRemotionWords(o.(*remotion.RemotionWordsOut), details) })
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	addDetails(c, &details)
	c.AddCommand(remotionWordsSetupCmd(), remotionWordsSetCmd())
	return c
}

func printRemotionWords(r *remotion.RemotionWordsOut, details bool) {
	fmt.Println(r.Message)
	if r.HasSettings {
		shown := 0
		for _, s := range r.Settings {
			if !s.OnScreen && !s.Changed && !details {
				continue
			}
			if !details && shown >= 40 {
				fmt.Printf("  … and %d more (--details lists them all)\n", len(r.Settings)-shown)
				break
			}
			shown++
			line := "  " + s.Key + ": " + theirs(s.Text)
			if s.Changed {
				line += " (the code says " + theirs(s.Code) + ")"
			}
			if len(s.Seen) > 0 {
				line += " — seen in " + strings.Join(s.Seen, ", ")
			}
			if details {
				line += fmt.Sprintf(" [%s:%d]", s.File, s.Line)
			}
			fmt.Println(line)
		}
	}
	for _, c := range r.Copy {
		if !r.HasSettings && details {
			fmt.Printf("  %s: %d words in %s\n", c.File, c.Words, strings.Join(c.Consts, ", "))
		}
	}
	for i, ch := range r.Changes {
		if i >= 3 {
			break
		}
		fmt.Printf("  change %d (%s, %s): %s\n", ch.Number, actorWords(ch.By), ch.At.Local().Format("2 Jan 15:04"), ch.Summary)
	}
	if r.Next != "" && !strings.HasPrefix(r.Next, "metarush ") {
		fmt.Println(r.Next)
	} else if r.Next != "" && details {
		fmt.Println("  next:", r.Next)
	}
}

func actorWords(a models.Actor) string {
	if a.UserName != "" {
		return a.UserName
	}
	return "someone"
}

func remotionWordsSetupCmd() *cobra.Command {
	var in remotion.RemotionWordsSetupIn
	var noWait, details bool
	c := &cobra.Command{
		Use:   "setup <name> [--file src/story.ts]... [--no-wait]",
		Short: "Make a Remotion source's on-screen words settings: one change to its code (its copy files read their words from settings), checked as its next version for a person to approve",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			return runTaskOp(c, "remotion.words.setup", in, noWait, details)
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	c.Flags().StringSliceVar(&in.Files, "file", nil, "only these files that hold its words")
	c.Flags().BoolVar(&noWait, "no-wait", false, "don't wait while the change is made and checked")
	addDetails(c, &details)
	return c
}

func remotionWordsSetCmd() *cobra.Command {
	var in remotion.RemotionWordsSetIn
	var noWait, details bool
	c := &cobra.Command{
		Use:   "set <name> KEY=WORDS... [--check] [--note \"…\"] [--no-wait]",
		Short: "Change on-screen words of a Remotion source whose words are settings: drawn with no network and checked (length, words that don't fit), then the cuts' clips are made again — no new version of its code",
		Long: `Change words that are settings, like END.tagline="Ship faster". Each change is drawn in MetaRush's sandboxed
browser before and after (the lengths it gives the compositions, words that no longer fit their box), then the
latest cuts' clips that show them are made again with the new words. Empty words go back to the code's own.
--check draws and checks without changing anything.`,
		Example: "  metarush remotion words set teaser END.tagline=\"Ship faster\" --check\n  metarush remotion words set teaser END.tagline=\"Ship faster\" END.cta=\"Book a call\"",
		Args:    cobra.MinimumNArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			for _, kv := range args[1:] {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || strings.TrimSpace(k) == "" {
					return fail(c, apperr.InvalidInput, "Give each change as KEY=WORDS, like END.tagline=\"Ship faster\".", "")
				}
				in.Set = append(in.Set, models.RemotionWordValue{Key: strings.TrimSpace(k), Text: v})
			}
			return runTaskOp(c, "remotion.words.set", in, noWait, details)
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	c.Flags().BoolVar(&in.CheckOnly, "check", false, "only draw and check it; change nothing")
	c.Flags().StringVar(&in.Note, "note", "", "why (kept with the change)")
	c.Flags().BoolVar(&noWait, "no-wait", false, "don't wait for the check")
	addDetails(c, &details)
	return c
}

// runTaskOp calls an op that queues remotion-task work and waits for it
// (running it here when no worker is online), then prints what it did.
func runTaskOp(c *cobra.Command, op string, in any, noWait, details bool) error {
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	out, err := ops.CallTyped[remotion.RemotionTaskOut](ctx, a, op, in, ops.Caller{})
	if err != nil {
		return failErr(c, err)
	}
	if noWait {
		return emit(c, out, func() {
			fmt.Println(out.Message)
			if details {
				fmt.Println("  job:", out.JobID)
			}
		})
	}
	if !jsonMode(c) {
		fmt.Println(out.Message)
	}
	return waitTaskAndSay(c, ctx, a, out, details)
}

func waitTaskAndSay(c *cobra.Command, ctx context.Context, a *app.App, out *remotion.RemotionTaskOut, details bool) error {
	_ = ctx
	return waitAndSay(c, a, out.JobID, details, out)
}

var _ = time.Second

package cli

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/remotion"
)

// remotion refresh and remotion push (D173–D176): a source's own steps run as
// a refresh a person starts, and an approved version's changes sent back to
// its repository. Plans are read-only (Claude may show them); starting either
// is for people only, at an interactive terminal (or the dashboard).

func remotionRefreshCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "refresh",
		Short: "Run a Remotion source's own steps (its npm scripts) as a refresh a person starts: the plan first, then a new version that waits for a person",
		Long: `A refresh runs steps from the project's own package.json (npm run <script>), its own files (node <file>)
or tools its packages have (npx --no <tool>) on this computer, outside the render sandbox with the network on,
in a separate environment that gets only the keys you pick from Settings → Integrations — under the names the
project reads them from — and none of MetaRush's own settings, keys or files. What the steps write in the places
you allow becomes a new version, checked like any other, that waits for a person to approve it.

  metarush remotion refresh plan teaser --step "npm run vo" --key elevenlabs=ELEVENLABS_API_KEY --output public/voiceover/
  metarush remotion refresh start teaser …the same flags…      (people only: it shows the plan and asks)
  metarush remotion refresh runs teaser
  metarush remotion refresh log teaser 2`,
	}
	cmd.AddCommand(remotionRefreshPlanCmd(), remotionRefreshStartCmd(), remotionRefreshRunsCmd(), remotionRefreshLogCmd())
	return cmd
}

// refreshFlags are the refresh-step flags plan and start share.
func refreshFlags(c *cobra.Command, in *remotion.RemotionRefreshIn, keys *[]string) {
	c.Flags().StringArrayVar(&in.Steps, "step", nil, "a step, in order: \"npm run <script>\", \"node <file>\" or \"npx --no <tool>\" with its arguments (repeat; default: the steps last started)")
	c.Flags().StringArrayVar(keys, "key", nil, "a key from Settings → Integrations and the name the project reads it from: elevenlabs=ELEVENLABS_API_KEY (repeat)")
	c.Flags().StringArrayVar(&in.Outputs, "output", nil, "where the steps may write: a folder (public/voice/), a file or a pattern (public/**/*.wav) (repeat; default: public/ and its data files)")
	c.Flags().BoolVar(&in.Fresh, "fresh", false, "start from MetaRush's suggestions, not the steps last started")
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
}

// readKeys reads --key provider=NAME flags.
func readKeys(c *cobra.Command, in *remotion.RemotionRefreshIn, keys []string) error {
	for _, k := range keys {
		p, env, _ := strings.Cut(k, "=")
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			return fail(c, apperr.InvalidInput, "--key is a provider and the variable name it goes under, like elevenlabs=ELEVENLABS_API_KEY.", "")
		}
		in.Keys = append(in.Keys, models.RemotionKey{Provider: p, Env: strings.TrimSpace(env)})
	}
	return nil
}

func remotionRefreshPlanCmd() *cobra.Command {
	var in remotion.RemotionRefreshIn
	var keys []string
	var details bool
	c := &cobra.Command{
		Use:   "plan <name> [--step …] [--key provider=NAME] [--output …]",
		Short: "Show what a refresh would do before anything runs: its steps and commands, the keys passed and their names, where it writes, the hosts it may reach, how it is kept apart",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			if err := readKeys(c, &in, keys); err != nil {
				return err
			}
			return runOp(c, "remotion.refresh.plan", in, func(o any) { printRefreshPlan(o.(*remotion.RemotionRefreshPlanOut), details) })
		},
	}
	refreshFlags(c, &in, &keys)
	addDetails(c, &details)
	return c
}

func printRefreshPlan(p *remotion.RemotionRefreshPlanOut, details bool) {
	fmt.Println(p.Message)
	if len(p.Steps) > 0 {
		fmt.Println("Its steps, in order:")
		for _, st := range p.Steps {
			line := fmt.Sprintf("  %d. %s — %s", st.N, st.Run, st.Words)
			if st.MakesVideos {
				line += " (it renders videos; MetaRush makes clips itself)"
			}
			fmt.Println(line)
		}
	} else if len(p.Scripts) > 0 {
		fmt.Println("Its scripts (pick the ones to run as steps):")
		for _, s := range p.Scripts {
			fmt.Printf("  %s: %s\n", s.Name, s.Command)
		}
	}
	if len(p.Keys) > 0 {
		fmt.Println("Keys it gets (nothing else):")
		for _, k := range p.Keys {
			state := k.Masked
			if !k.Configured {
				state = "not on this computer yet"
			}
			fmt.Printf("  %s key as %s (%s)\n", k.Title, k.Env, state)
		}
	} else {
		fmt.Println("Keys it gets: none.")
	}
	var others []string
	for _, r := range p.Reads {
		if !r.Set {
			others = append(others, r.Name)
		}
	}
	if len(others) > 0 {
		fmt.Println("It also reads " + strings.Join(others, ", ") + ", which MetaRush doesn't set (the code keeps its own defaults).")
	}
	fmt.Println("Where it may write: " + strings.Join(p.Outputs, ", "))
	if len(p.Hosts) > 0 {
		fmt.Println("Places it may reach:")
		for _, h := range p.Hosts {
			fmt.Printf("  %s (%s)\n", h.Host, h.Why)
		}
	}
	if p.Apart != "" {
		fmt.Println("Kept apart: " + p.Apart)
	}
	for _, n := range p.Never {
		fmt.Println("  · " + n)
	}
	for _, pr := range p.Problems {
		fmt.Println("Can't start yet: " + pr)
	}
	if p.Next != "" {
		fmt.Println(p.Next)
	}
	if details {
		fmt.Printf("  version: %s (%d)\n  isolation: %s\n  plan: %s\n", p.Version, p.VersionNumber, p.Isolation, p.Fingerprint)
	}
}

func remotionRefreshStartCmd() *cobra.Command {
	var in remotion.RemotionRefreshStartIn
	var keys []string
	var details, wait bool
	c := &cobra.Command{
		Use:   "start <name> [--step …] [--key provider=NAME] [--output …] [--wait]",
		Short: "Start a refresh — people only, at an interactive terminal: the plan is shown first and you type start",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			if err := readKeys(c, &in.RemotionRefreshIn, keys); err != nil {
				return err
			}
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person starts a refresh, in the dashboard or in their own terminal.", "Open the project's Sources in the dashboard, or run this command yourself in a terminal.")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			p, err := ops.CallTyped[remotion.RemotionRefreshPlanOut](c.Context(), a, "remotion.refresh.plan", in.RemotionRefreshIn, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			printRefreshPlan(p, details)
			if !p.Ready {
				return fail(c, apperr.Validation, "This refresh can't start yet.", strings.Join(p.Problems, " "))
			}
			if ask("Type \"start\" to run these steps now", "") != "start" {
				return fail(c, apperr.Canceled, "nothing was started", "")
			}
			in.Plan = p.Fingerprint
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			out, err := ops.CallTyped[remotion.RemotionRefreshStartOut](ctx, a, "remotion.refresh.start", in, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			if !wait {
				return emit(c, out, func() {
					fmt.Println(out.Message)
					fmt.Println("MetaRush running on this computer does it; follow it in the dashboard's activity, or wait for it with --wait.")
					if details {
						fmt.Println("  job:", out.JobID)
					}
				})
			}
			if !jsonMode(c) {
				fmt.Println(out.Message)
			}
			return waitAndSay(c, a, out.JobID, details, out)
		},
	}
	refreshFlags(c, &in.RemotionRefreshIn, &keys)
	c.Flags().BoolVar(&wait, "wait", false, "run it here and wait until it is done")
	addDetails(c, &details)
	return c
}

func remotionRefreshRunsCmd() *cobra.Command {
	var in remotion.RemotionRefreshRunsIn
	var details bool
	c := &cobra.Command{
		Use:   "runs <name>",
		Short: "List a Remotion source's refreshes: their steps, how each went, what they changed and the version they made",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			return runOp(c, "remotion.refresh.runs", in, func(o any) {
				r := o.(*remotion.RemotionRefreshRunsOut)
				if len(r.Refreshes) == 0 {
					fmt.Printf("%s hasn't been refreshed yet.\n", theirs(r.Title))
					return
				}
				for _, x := range r.Refreshes {
					line := fmt.Sprintf("Refresh %d (%s, %s): %s", x.Number, x.StateLabel, x.CreatedAt.Local().Format("2 Jan 15:04"), strings.Join(x.Steps, ", "))
					fmt.Println(line)
					switch {
					case x.Summary != "":
						fmt.Println("  " + x.Summary)
					case x.Error != "":
						fmt.Println("  " + x.Error)
					}
					if len(x.LeftOut) > 0 {
						fmt.Printf("  left out: %s\n", leftOutWords(x.LeftOut))
					}
					if details {
						fmt.Printf("  id: %s  job: %s  on %s (%s)\n", x.ID, x.JobID, x.Machine, x.Isolation)
					}
				}
			})
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	c.Flags().IntVar(&in.Limit, "limit", 0, "how many (default 20)")
	addDetails(c, &details)
	return c
}

func leftOutWords(ls []models.RemotionFileLeftOut) string {
	var parts []string
	for i, l := range ls {
		if i >= 4 {
			parts = append(parts, fmt.Sprintf("%d more", len(ls)-i))
			break
		}
		parts = append(parts, l.Path+" ("+l.Why+")")
	}
	return strings.Join(parts, "; ")
}

func remotionRefreshLogCmd() *cobra.Command {
	var in remotion.RemotionRefreshLogIn
	c := &cobra.Command{
		Use:   "log <name> [number]",
		Short: "Print what a refresh's steps printed (keys and other secrets hidden; default: the newest refresh)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			if len(args) == 2 {
				n, err := strconv.Atoi(strings.TrimPrefix(args[1], "#"))
				if err != nil || n < 1 {
					return fail(c, apperr.InvalidInput, "Give the refresh's number (like 2).", "")
				}
				in.Number = n
			}
			return runOp(c, "remotion.refresh.log", in, func(o any) {
				r := o.(*remotion.RemotionRefreshLogOut)
				if r.Text == "" {
					fmt.Printf("Refresh %d (%s) has nothing printed yet.\n", r.Number, r.State)
					return
				}
				_, _ = os.Stdout.WriteString(r.Text)
			})
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	return c
}

// ---- push ----

func remotionPushCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "push",
		Short: "Send an approved version's changes back to the Remotion project's repository as a new branch (and a pull request on GitHub) — people only",
		Long: `MetaRush compares what was brought in with an approved version and sends the changes back to where the
project came from — its GitHub repository, its folder's remote, or its folder's own repository — as a new branch,
with a pull request on GitHub (with gh and your sign-in). It never forces anything, never sends to the main branch
and never changes a branch it didn't make. MetaRush's words helper never goes back: constants go back as plain code
(with today's word settings written in when you add --words).

  metarush remotion push plan teaser@4
  metarush remotion push start teaser@4 [--branch metarush/teaser-v4] [--no-pr] [--words]   (people only)
  metarush remotion push list teaser`,
	}
	cmd.AddCommand(remotionPushPlanCmd(), remotionPushStartCmd(), remotionPushListCmd())
	return cmd
}

func pushFlags(c *cobra.Command, in *remotion.RemotionPushIn) {
	c.Flags().StringVar(&in.Branch, "branch", "", "the new branch (default metarush/<name>-v<N>)")
	c.Flags().BoolVar(&in.NoPR, "no-pr", false, "only the branch, no pull request")
	c.Flags().BoolVar(&in.Words, "words", false, "write today's word settings into the code")
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
}

func remotionPushPlanCmd() *cobra.Command {
	var in remotion.RemotionPushIn
	var details bool
	c := &cobra.Command{
		Use:   "plan <name[@version]> [--branch b] [--no-pr] [--words]",
		Short: "Show what sending an approved version back would do: the changes in plain words, where they go, the branch and the pull request",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			return runOp(c, "remotion.push.plan", in, func(o any) { printPushPlan(o.(*remotion.RemotionPushPlanOut), details) })
		},
	}
	pushFlags(c, &in)
	addDetails(c, &details)
	return c
}

func printPushPlan(p *remotion.RemotionPushPlanOut, details bool) {
	fmt.Println(p.Message)
	for i, ch := range p.Changes {
		if i >= 30 && !details {
			fmt.Printf("  … and %d more\n", len(p.Changes)-i)
			break
		}
		line := "  " + ch.Change + " " + ch.Path
		if ch.Lines > 0 {
			line += fmt.Sprintf(" (%d lines)", ch.Lines)
		}
		fmt.Println(line)
	}
	for i, w := range p.WordChanges {
		if i >= 12 {
			break
		}
		fmt.Printf("  words: %s → %s\n", theirs(w.Before), theirs(w.After))
	}
	if p.Helper != "" {
		fmt.Println(p.Helper)
	}
	for _, pr := range p.Problems {
		fmt.Println("Can't send it yet: " + pr)
	}
	if p.Next != "" {
		fmt.Println(p.Next)
	}
	if details {
		fmt.Printf("  to: %s (%s)\n  original commit: %s\n  on top of: %s\n  plan: %s\n", p.Target.URL, p.Target.Kind, p.Base, p.Parent, p.Fingerprint)
	}
}

func remotionPushStartCmd() *cobra.Command {
	var in remotion.RemotionPushStartIn
	var details, wait bool
	c := &cobra.Command{
		Use:   "start <name[@version]> [--branch b] [--no-pr] [--words] [--wait]",
		Short: "Send an approved version's changes back — people only, at an interactive terminal: the changes are shown first and you type send",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person sends changes back to a repository, in the dashboard or in their own terminal.", "Open the project's Sources in the dashboard, or run this command yourself in a terminal.")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			p, err := ops.CallTyped[remotion.RemotionPushPlanOut](c.Context(), a, "remotion.push.plan", in.RemotionPushIn, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			printPushPlan(p, details)
			if !p.Ready {
				return fail(c, apperr.Validation, "These changes can't go back yet.", strings.Join(p.Problems, " "))
			}
			if ask("Type \"send\" to send them now", "") != "send" {
				return fail(c, apperr.Canceled, "nothing was sent", "")
			}
			in.Plan = p.Fingerprint
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			out, err := ops.CallTyped[remotion.RemotionPushStartOut](ctx, a, "remotion.push.start", in, ops.Caller{Human: true})
			if err != nil {
				return failErr(c, err)
			}
			if !wait {
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
			return waitAndSay(c, a, out.JobID, details, out)
		},
	}
	pushFlags(c, &in.RemotionPushIn)
	c.Flags().BoolVar(&wait, "wait", false, "send it from here and wait until it is done")
	addDetails(c, &details)
	return c
}

func remotionPushListCmd() *cobra.Command {
	var in remotion.RemotionPushListIn
	var details bool
	c := &cobra.Command{
		Use:   "list <name>",
		Short: "List the changes sent back to a Remotion project's repository: versions, branches, pull requests",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			return runOp(c, "remotion.push.list", in, func(o any) {
				r := o.(*remotion.RemotionPushListOut)
				if len(r.Pushes) == 0 {
					fmt.Printf("Nothing of %s has been sent back yet.\n", theirs(r.Title))
					return
				}
				for _, x := range r.Pushes {
					line := fmt.Sprintf("Version %d → %s, branch %s (%s)", x.VersionNumber, x.Target.Where, x.Branch, x.StateLabel)
					if x.PRURL != "" {
						line += ": " + x.PRURL
					}
					fmt.Println(line)
					if x.Error != "" {
						fmt.Println("  " + x.Error)
					}
					if details {
						fmt.Printf("  id: %s  commit: %s  job: %s\n", x.ID, x.Commit, x.JobID)
					}
				}
			})
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	addDetails(c, &details)
	return c
}

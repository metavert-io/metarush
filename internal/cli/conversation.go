package cli

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/conversation"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ops"
)

func conversationCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "conversation", Aliases: []string{"conversations", "call"}, Short: "Conversations from online calls: line recordings up, cut between people and layouts, clean up the call",
		Long: `A conversation is several recordings of one talk (one per person from Zoom, Riverside or SquadCast,
or per-person sound plus a gallery view) lined up on one clock. In a cut its picture switches between
people and layouts while everyone's own microphone plays on.

Examples:
  metarush conversation create --project <id> --members maya.mov sam.mov --names "Maya,Sam"
  metarush conversation import "~/Documents/Zoom/2026-10-19 10.00.00 Garden Show" --project <id>
  metarush conversation show "Garden show" --details
  metarush conversation nudge "Garden show" --member sam --frames 1
  metarush conversation cut "Garden show" --project <id> --style podcast --apply
  metarush conversation angle --project <id> --at 0:31.2 --layout side-by-side --apply
  metarush conversation cleanup "Garden show" --project <id>
  metarush conversation moments "Garden show" --count 3
Changes to a cut print for review; add --apply (or --confirm <token>) to make them a new version.`}
	cmd.AddCommand(convCreateCmd(), convListCmd(), convShowCmd(), convSyncCmd(), convNudgeCmd(), convUpdateCmd(), convPinCmd(), convCutCmd(), convAngleCmd(), convStylesCmd())
	cmd.AddCommand(conversationMoreCmds()...)
	return cmd
}

// printCall shows a conversation in plain words.
func printCall(v conversation.CallInfo, details bool) {
	fmt.Println(v.Text)
	for _, m := range v.Members {
		what := map[string]string{"both": "picture and sound", "sound": "sound only", "picture": "shared picture"}[m.Use]
		fmt.Printf("  %-14s %s (%s): %s\n", theirs(m.Name), theirs(m.Title), what, m.Text)
		if details && m.Details != "" {
			fmt.Printf("  %-14s %s · short name %s\n", "", m.Details, m.ID)
		}
	}
	if v.Cleanup.Open > 0 {
		fmt.Printf("%d clean-up suggestions to look at.\n", v.Cleanup.Open)
	}
	for _, cu := range v.Cuts {
		state := "with its current alignment"
		if !cu.Current {
			state = "with an earlier alignment"
		}
		fmt.Printf("Version %d of the cut plays it %s.\n", cu.Number, state)
	}
	if details {
		fmt.Printf("ID %s · %d recordings · %.3f s · %s (%.2f)\n", v.ID, len(v.Members), v.Length, v.Method, v.Confidence)
	}
}

func convCreateCmd() *cobra.Command {
	var project, name, names, uses, align string
	var members []string
	var search float64
	cmd := &cobra.Command{Use: "create", Short: "Make a conversation from several recordings of one talk, lined up by their sound",
		Long: `Examples:
  metarush conversation create --project <id> --members maya.mov sam.mov
  metarush conversation create --project <id> --members audioMaya.m4a audioSam.m4a gallery.mp4 --use sound,sound,picture --names "Maya,Sam,Gallery view"
  metarush conversation create --project <id> --members a.mov b.mov --align together   # files that start together`,
		RunE: func(c *cobra.Command, args []string) error {
			members = append(members, args...)
			if len(members) == 0 {
				return fail(c, apperr.InvalidInput, "name the recordings: --members <clip> <clip>…", "")
			}
			nameList, useList := splitList(names), splitList(uses)
			in := conversation.CallCreateIn{ProjectID: project, Name: name, Align: align, Search: search}
			for i, m := range members {
				mi := conversation.CallMemberIn{AssetID: m}
				if i < len(nameList) {
					mi.Name = nameList[i]
				}
				if i < len(useList) {
					mi.Use = useList[i]
				}
				in.Members = append(in.Members, mi)
			}
			return runOp(c, "conversation.create", in, func(o any) {
				out := o.(*conversation.CallOut)
				fmt.Println(out.Summary)
				printCall(out.Conversation, false)
			})
		}}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "the project (the recordings must be in it)")
	f.StringSliceVar(&members, "members", nil, "the recordings: IDs, titles or file names (or as arguments)")
	f.StringVar(&names, "names", "", "the people's names, in the same order, separated by commas")
	f.StringVar(&uses, "use", "", "what each recording gives, in order: both (picture and sound), sound, picture (a shared gallery view)")
	f.StringVar(&name, "name", "", "the conversation's name (default: the people's names)")
	f.StringVar(&align, "align", "", "audio (by their sound, the default), together (files that start together) or none")
	f.Float64Var(&search, "search", 0, "how far apart the recordings may start, in seconds either way (default 600)")
	return cmd
}

func splitList(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	var out []string
	for _, p := range strings.Split(s, ",") {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

func convListCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{Use: "list", Short: "The conversations, with their people, length and how sure their sync is",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "conversation.list", conversation.CallListIn{ProjectID: project}, func(o any) {
				out := o.(*conversation.CallListOut)
				if len(out.Conversations) == 0 {
					fmt.Println("No conversations yet.")
					return
				}
				for _, v := range out.Conversations {
					fmt.Println("• " + v.Text)
				}
			})
		}}
	cmd.Flags().StringVar(&project, "project", "", "only this project's")
	return cmd
}

func convShowCmd() *cobra.Command {
	var project string
	var details bool
	cmd := &cobra.Command{Use: "show <conversation>", Short: "A conversation's recordings, how each lines up and how sure, and the cuts that play it", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "conversation.show", conversation.CallRefIn{Conversation: args[0], ProjectID: project}, func(o any) {
				printCall(o.(*conversation.CallOut).Conversation, details)
			})
		}}
	cmd.Flags().StringVar(&project, "project", "", "where to look for it by name")
	cmd.Flags().BoolVar(&details, "details", false, "also the measurements: start, drift, windows that agree, the call's delay")
	return cmd
}

func convSyncCmd() *cobra.Command {
	var project, marks string
	var clap, together bool
	var search float64
	cmd := &cobra.Command{Use: "sync <conversation>", Short: "Line the recordings up again: by their sound, on a clap, on moments you mark, or as files that start together", Args: cobra.ExactArgs(1),
		Long: `Examples:
  metarush conversation sync "Garden show"                          # by their sound
  metarush conversation sync "Garden show" --clap                   # on a clap in the first minute
  metarush conversation sync "Garden show" --mark sam=0:05.9,maya=0:03.2   # the same moment in each
  metarush conversation sync "Garden show" --together               # the files start together`,
		RunE: func(c *cobra.Command, args []string) error {
			in := conversation.CallSyncIn{CallRefIn: conversation.CallRefIn{Conversation: args[0], ProjectID: project}, Search: search}
			switch {
			case clap:
				in.Method = conversation.MethodClap
			case together:
				in.Method = conversation.MethodTogether
			case marks != "":
				in.Method = conversation.MethodMark
				in.Marks = map[string]float64{}
				for _, p := range splitList(marks) {
					k, v, ok := strings.Cut(p, "=")
					t, ok2 := editspec.ParseClock(v)
					if !ok || !ok2 {
						return fail(c, apperr.InvalidInput, fmt.Sprintf("%q isn't member=time (like sam=0:05.9)", p), "")
					}
					in.Marks[strings.TrimSpace(k)] = t
				}
			}
			return runOp(c, "conversation.sync", in, func(o any) {
				out := o.(*conversation.CallOut)
				fmt.Println(out.Summary)
				printCall(out.Conversation, false)
			})
		}}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "where to look for it by name")
	f.BoolVar(&clap, "clap", false, "line up on the sharpest loud sound in each recording's first minute (a clap, a slate)")
	f.StringVar(&marks, "mark", "", "the same moment in each recording: member=time,… (times in that recording)")
	f.BoolVar(&together, "together", false, "the files start together (a provider's aligned files)")
	f.Float64Var(&search, "search", 0, "how far apart the recordings may start (seconds either way)")
	return cmd
}

func convNudgeCmd() *cobra.Command {
	var project, member, to string
	var by float64
	var frames int
	cmd := &cobra.Command{Use: "nudge <conversation>", Short: "Move one recording earlier or later on the conversation's clock (when lips and voice don't match)", Args: cobra.ExactArgs(1),
		Long: `Examples:
  metarush conversation nudge "Garden show" --member sam --frames 1     # a frame later
  metarush conversation nudge "Garden show" --member sam --by -0.04     # 40 ms earlier
  metarush conversation nudge "Garden show" --member sam --to 1.25      # exactly there`,
		RunE: func(c *cobra.Command, args []string) error {
			in := conversation.CallNudgeIn{CallRefIn: conversation.CallRefIn{Conversation: args[0], ProjectID: project}, Member: member, By: by, Frames: frames}
			if to != "" {
				t, ok := editspec.ParseClock(strings.TrimPrefix(to, "-"))
				if !ok {
					return fail(c, apperr.InvalidInput, "--to is a time like 1.25 or 0:01.25", "")
				}
				if strings.HasPrefix(to, "-") {
					t = -t
				}
				in.To = &t
			}
			return runOp(c, "conversation.nudge", in, func(o any) { out := o.(*conversation.CallOut); fmt.Println(out.Summary) })
		}}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "where to look for it by name")
	f.StringVar(&member, "member", "", "the recording to move (its short name or the person's name)")
	f.Float64Var(&by, "by", 0, "seconds (positive: later)")
	f.IntVar(&frames, "frames", 0, "frames at the cut's frame rate (positive: later)")
	f.StringVar(&to, "to", "", "where it starts on the conversation's clock")
	return cmd
}

func convUpdateCmd() *cobra.Command {
	var project, name, member, memberName, tag, use, person, style, layout, muted string
	cmd := &cobra.Command{Use: "update <conversation>", Short: "Rename it, or change a member: name, name tag, what their recording gives, who they are in People", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := conversation.CallUpdateIn{CallRefIn: conversation.CallRefIn{Conversation: args[0], ProjectID: project}, Name: name, Member: member, MemberName: memberName, Use: use, Person: person, Style: style, Layout: layout}
			if c.Flags().Changed("tag") {
				in.Tag = &tag
			}
			if muted != "" {
				v, err := strconv.ParseBool(muted)
				if err != nil {
					return fail(c, apperr.InvalidInput, "--muted is true or false", "")
				}
				in.Muted = &v
			}
			return runOp(c, "conversation.update", in, func(o any) { fmt.Println(o.(*conversation.CallOut).Summary) })
		}}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "where to look for it by name")
	f.StringVar(&name, "name", "", "a new name for the conversation")
	f.StringVar(&member, "member", "", "the member to change")
	f.StringVar(&memberName, "rename", "", "the member's new name")
	f.StringVar(&tag, "tag", "", "the name tag's second line (a role)")
	f.StringVar(&use, "use", "", "both (picture and sound), sound or picture")
	f.StringVar(&person, "person", "", "who they are in People (ID or name; - unlinks)")
	f.StringVar(&muted, "muted", "", "true: leave this recording's sound out; false: hear it")
	f.StringVar(&style, "style", "", "the style its automatic cuts use")
	f.StringVar(&layout, "layout", "", "the layout when people talk together")
	return cmd
}

func convPinCmd() *cobra.Command {
	var project, branch, confirm string
	var applyIt bool
	cmd := &cobra.Command{Use: "pin <conversation>", Short: "Use the conversation's current alignment and member settings in the project's cut", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			in := conversation.CallPinIn{CallRefIn: conversation.CallRefIn{Conversation: args[0]}, Project: project, Branch: branch, Apply: applyIt}
			out, err := ops.CallTyped[conversation.CallPlanOut](c.Context(), a, "conversation.pin", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return planOutFlow(c, a, out, confirm)
		}}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "the project whose latest cut gets it (default: the conversation's)")
	f.StringVar(&branch, "branch", "", "the branch (default main)")
	f.StringVar(&confirm, "confirm", "", "apply with the token from the check")
	f.BoolVar(&applyIt, "apply", false, "put it in the cut as a new version")
	return cmd
}

// planOutFlow prints a change to a cut (and applies it with --confirm).
func planOutFlow(c *cobra.Command, a *app.App, out *conversation.CallPlanOut, confirm string) error {
	if confirm != "" && out.Result != nil {
		return planFlow(c, a, out.Result.Plan, confirm, map[string]any{"changes": out.Changes, "why": out.Why}, func() { fmt.Println(out.Summary) })
	}
	return resultFlow(c, a, out, pending(out.Result, out.Applied), func() {
		fmt.Println(out.Summary)
		for _, ch := range out.Changes {
			fmt.Println("  • " + ch)
		}
		if details, _ := c.Flags().GetBool("details"); details {
			for _, w := range out.Why {
				fmt.Println("    " + w)
			}
		}
	})
}

func convCutCmd() *cobra.Command {
	var project, branch, style, shape, from, to, interviewer, questions, layout, confirm string
	var broll []string
	var captions, noTags, applyIt, details bool
	cmd := &cobra.Command{Use: "cut <conversation>", Short: "Cut a conversation automatically in a style: whoever is talking, reactions, split layouts, each choice explained", Args: cobra.ExactArgs(1),
		Long: `Styles:
  podcast     host and guest: opens on both, then whoever talks; reactions; side by side when both talk
  interview   off-camera interviewer: the guest only; questions heard (--questions audio) or as text cards (--questions cards)
  panel       three or more: opens on the grid, then whoever talks
  reactions   short shots, lots of listening faces, the picture going to the listener as the speaker finishes
  broll       talking head with cutaways to --broll footage during long answers
Examples:
  metarush conversation cut "Garden show" --project <id> --style podcast
  metarush conversation cut "Garden show" --project <id> --style interview --interviewer maya --questions cards --apply
  metarush conversation cut "Garden show" --project <id> --shape vertical --from 1:02 --to 1:48 --captions`,
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			in := conversation.CallCutIn{CallRefIn: conversation.CallRefIn{Conversation: args[0]}, Project: project, Branch: branch, Style: style, Shape: shape, Interviewer: interviewer,
				Questions: questions, BRoll: broll, Layout: layout, Apply: applyIt}
			for _, x := range []struct {
				s   string
				dst **float64
			}{{from, &in.From}, {to, &in.To}} {
				if x.s == "" {
					continue
				}
				t, ok := editspec.ParseClock(x.s)
				if !ok {
					return fail(c, apperr.InvalidInput, fmt.Sprintf("%q isn't a time (like 1:02.5)", x.s), "")
				}
				*x.dst = &t
			}
			if c.Flags().Changed("captions") {
				in.Captions = &captions
			}
			if noTags {
				f := false
				in.NameTags = &f
			}
			out, err := ops.CallTyped[conversation.CallPlanOut](c.Context(), a, "conversation.cut", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			_ = details
			return planOutFlow(c, a, out, confirm)
		}}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "the project whose cut it becomes (default: the conversation's)")
	f.StringVar(&branch, "branch", "", "the branch (default main)")
	f.StringVar(&style, "style", "", "podcast | interview | panel | reactions | broll")
	f.StringVar(&shape, "shape", "", "landscape (16:9, the default) | vertical | square")
	f.StringVar(&from, "from", "", "start there on the conversation's clock")
	f.StringVar(&to, "to", "", "end there")
	f.StringVar(&interviewer, "interviewer", "", "interview style: the off-camera interviewer (default: who asks the questions)")
	f.StringVar(&questions, "questions", "", "interview style: audio (heard over the guest) or cards (text cards)")
	f.StringSliceVar(&broll, "broll", nil, "cutaways style: footage to cut away to")
	f.StringVar(&layout, "layout", "", "side-by-side | stacked | pip | grid: the layout when people talk together")
	f.BoolVar(&captions, "captions", false, "captions with each person's name (default on for vertical)")
	f.BoolVar(&noTags, "no-name-tags", false, "no name tags the first time each person is seen")
	f.StringVar(&confirm, "confirm", "", "apply with the token from the check")
	f.BoolVar(&applyIt, "apply", false, "make it a new version of the cut")
	f.BoolVar(&details, "details", false, "also every shot's reason")
	return cmd
}

func convAngleCmd() *cobra.Command {
	var project, branch, clip, at, member, layout, card, broll, brollIn, why, confirm string
	var members []string
	var dissolve float64
	var remove, applyIt bool
	cmd := &cobra.Command{Use: "angle", Short: "Change what a conversation clip shows from a moment on: a person, a layout, a card or a cutaway",
		Long: `Examples:
  metarush conversation angle --project <id> --at 0:31.2 --member sam
  metarush conversation angle --project <id> --at 0:31.2 --layout side-by-side --members maya,sam
  metarush conversation angle --project <id> --at 1:02.5 --card "What did you build first?" --members sam
  metarush conversation angle --project <id> --at 0:31.2 --remove`,
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			t, ok := editspec.ParseClock(at)
			if !ok {
				return fail(c, apperr.InvalidInput, "--at is a moment on the conversation's clock, like 0:31.2", "")
			}
			in := conversation.CallAngleIn{Project: project, Branch: branch, Clip: clip, At: t, Member: member, Layout: layout, Members: members, Card: card, BRoll: broll, Remove: remove, Why: why, Apply: applyIt}
			if brollIn != "" {
				v, ok := editspec.ParseClock(brollIn)
				if !ok {
					return fail(c, apperr.InvalidInput, "--broll-in is a time in that footage", "")
				}
				in.BRollIn = v
			}
			if c.Flags().Changed("dissolve") {
				in.Dissolve = &dissolve
			}
			out, err := ops.CallTyped[conversation.CallPlanOut](c.Context(), a, "conversation.angle", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return planOutFlow(c, a, out, confirm)
		}}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "the project")
	f.StringVar(&branch, "branch", "", "the branch (default main)")
	f.StringVar(&clip, "clip", "", "the conversation clip (default: the one playing that moment)")
	f.StringVar(&at, "at", "", "the moment on the conversation's clock")
	f.StringVar(&member, "member", "", "show this person")
	f.StringVar(&layout, "layout", "", "show a layout: side-by-side | stacked | pip | grid")
	f.StringSliceVar(&members, "members", nil, "who is in the layout (or behind the card), in order")
	f.StringVar(&card, "card", "", "show this text on a card")
	f.StringVar(&broll, "broll", "", "show this footage")
	f.StringVar(&brollIn, "broll-in", "", "where in that footage")
	f.Float64Var(&dissolve, "dissolve", 0, "seconds the switch blends (0 cuts)")
	f.BoolVar(&remove, "remove", false, "take out the shot that starts there")
	f.StringVar(&why, "why", "", "the reason, kept with the shot")
	f.StringVar(&confirm, "confirm", "", "apply with the token from the check")
	f.BoolVar(&applyIt, "apply", false, "make it a new version of the cut")
	return cmd
}

func convStylesCmd() *cobra.Command {
	return &cobra.Command{Use: "styles", Short: "The styles an automatic cut can have, and the layouts",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "conversation.styles", conversation.CallStylesIn{}, func(o any) {
				out := o.(*conversation.CallStylesOut)
				for _, s := range out.Styles {
					fmt.Printf("  %-10s %s: %s\n", s.Name, s.Label, s.About)
				}
				fmt.Println("Layouts:")
				for _, l := range out.Layouts {
					fmt.Printf("  %-13s %s\n", l.ID, l.About)
				}
			})
		}}
}

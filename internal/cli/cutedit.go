package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/ops"
)

// The cut's editing verbs: `metarush cut add|move|trim|split|delete|
// delete-range|remove-words`. Each names clips the way the cut sheet does (or
// by number), shows the change in plain words with its warnings, and saves it
// with --confirm <token> (in a terminal it asks instead), like `cut transition`.

// cutEditFlags are the flags every editing verb shares.
type cutEditFlags struct {
	common  ops.CutEditCommon
	details bool
}

func (f *cutEditFlags) add(c *cobra.Command) {
	fl := c.Flags()
	fl.StringVar(&f.common.Branch, "branch", "", "branch (default: main)")
	fl.BoolVar(&f.common.DryRun, "dry-run", false, "only check: the change, its warnings and a confirm token")
	fl.StringVar(&f.common.ConfirmToken, "confirm", "", "token from a check of exactly this change")
	fl.StringVarP(&f.common.Instruction, "message", "m", "", "what was asked (recorded with the new version)")
	fl.StringVar(&f.common.Rationale, "why", "", "why (recorded with the new version)")
	fl.BoolVar(&f.common.AllowUnlock, "allow-unlock", false, "permit changes to locked clips (only when the person asked)")
	addDetailsFlag(c, &f.details)
}

// runCutEdit calls an editing verb. A person at a terminal without --confirm
// sees the change and is asked before it is saved (the same gate as the
// confirm token, which every other caller must pass).
func runCutEdit(c *cobra.Command, name string, f *cutEditFlags, input func(common ops.CutEditCommon) any) error {
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	var full any
	call := func(common ops.CutEditCommon) (*ops.CutEditOut, error) {
		b, err := json.Marshal(input(common))
		if err != nil {
			return nil, err
		}
		res, err := ops.Call(c.Context(), a, name, b, ops.Caller{})
		if err != nil {
			return nil, err
		}
		full = res
		// An edit that says more (cut flashes, cut names) carries the edit itself.
		return res.(interface{ Edit() *ops.CutEditOut }).Edit(), nil
	}
	common := f.common
	if common.ConfirmToken == "" && !common.DryRun && interactive() && !jsonMode(c) {
		dry := common
		dry.DryRun = true
		r, err := call(dry)
		if err != nil {
			return failErr(c, err)
		}
		printCutEdit(c, r, "")
		if r.NoChange || r.Check == nil || r.Check.ConfirmToken == "" {
			return nil
		}
		if !askYes("Save this change as a new version?", false) {
			return fail(c, apperr.Canceled, "not saved", "")
		}
		common.ConfirmToken = r.Check.ConfirmToken
	}
	r, err := call(common)
	if err != nil {
		return failErr(c, err)
	}
	return emit(c, full, func() {
		next := ""
		if r.Check != nil && r.Check.ConfirmToken != "" {
			next = sameCommandWith(c, "--confirm", r.Check.ConfirmToken)
		}
		printCutEdit(c, r, next)
	})
}

// printCutEdit shows a small edit: what it changes, how long the video gets
// and its warnings, then that it is ready to save (next is the command that
// saves it, for --details; "" when the person is asked right away) — or the
// version it was saved as.
func printCutEdit(c *cobra.Command, r *ops.CutEditOut, next string) {
	details := detailsMode(c)
	length := func() string {
		d := r.Duration - r.DurationBefore
		switch {
		case math.Abs(d) < 0.05:
			return fmt.Sprintf("The video stays %s long.", seconds(r.Duration))
		case d < 0:
			return fmt.Sprintf("The video gets %s shorter: %s → %s.", seconds(-d), seconds(r.DurationBefore), seconds(r.Duration))
		}
		return fmt.Sprintf("The video gets %s longer: %s → %s.", seconds(d), seconds(r.DurationBefore), seconds(r.Duration))
	}
	notes := func() {
		var errs, warns []cutsheet.Problem
		for _, p := range r.Problems {
			if p.Severity == "error" {
				errs = append(errs, p)
			} else {
				warns = append(warns, p)
			}
		}
		for _, p := range errs {
			fmt.Println("  ✗ " + p.Message)
			if p.Hint != "" {
				fmt.Println("    " + p.Hint)
			}
		}
		for _, p := range warns {
			fmt.Println("  ! " + p.Message)
		}
	}
	switch {
	case r.NoChange:
		fmt.Println(r.Summary)
		return
	case r.Applied != nil:
		fmt.Printf("Saved as version %d: %s\n", r.Applied.Revision.Number, r.Summary)
		fmt.Println(length())
		if r.Applied.Rebased {
			fmt.Println("Someone else saved changes meanwhile; yours were combined with theirs.")
		}
		notes()
		if details {
			fmt.Printf("revision: %s\n%s\n", r.Applied.Revision.ID, r.Applied.URL)
		}
		return
	}
	if r.Check != nil && !r.Check.Valid {
		fmt.Printf("This change can't be made yet: %s\n", r.Summary)
		notes()
		return
	}
	printSuggestions(r)
	fmt.Println(r.Summary)
	fmt.Println(length())
	// The changes, without the length again (the cut sheet's list starts with it).
	var changes []string
	for _, ch := range r.Changes {
		if ch != length() && !strings.HasPrefix(ch, "The video gets ") && !strings.HasPrefix(ch, "The video stays ") {
			changes = append(changes, ch)
		}
	}
	if len(changes) > 1 {
		printList("", changes)
	}
	notes()
	if r.Check != nil && r.Check.Rebased {
		fmt.Println("Someone saved a newer version; this change doesn't overlap theirs, so saving keeps both.")
	}
	readyToSave(c, next)
}

// printSuggestions lists cleanup suggestions, numbered as --pick takes them.
func printSuggestions(r *ops.CutEditOut) {
	if len(r.Removals) < 2 && (len(r.Removals) == 0 || r.Removals[0].Kind == cutsheet.RemovePhrase || r.Removals[0].Kind == cutsheet.RemoveSelection) {
		return
	}
	if r.Removals[0].Kind == cutsheet.RemovePause {
		fmt.Printf("Found %d long pauses:\n", len(r.Removals))
		for i, x := range r.Removals {
			fmt.Printf("  %d. %s at %s, clip %d: “…%s ‖ %s…” (%s shorter)\n", i+1, upperFirst(x.Reason), cutsheet.Clock(math.Round(x.At*10)/10), x.Clip, x.Before, x.After, seconds(x.Seconds))
		}
		return
	}
	fmt.Printf("Found %d places:\n", len(r.Removals))
	for i, x := range r.Removals {
		ctx := strings.TrimSpace(fmt.Sprintf("%s [%s] %s", x.Before, x.Words, x.After))
		fmt.Printf("  %d. %s at %s, clip %d (%s): “…%s…”\n", i+1, "“"+x.Words+"”", cutsheet.Clock(math.Round(x.At*10)/10), x.Clip, x.Reason, ctx)
	}
}

func cutEditCmds() []*cobra.Command {
	return []*cobra.Command{cutAddCmd(), cutMoveCmd(), cutTrimCmd(), cutSplitCmd(), cutDeleteCmd(), cutDeleteRangeCmd(), cutRemoveWordsCmd(), cutPausesCmd()}
}

// cutPausesCmd shortens long pauses between words (D236).
func cutPausesCmd() *cobra.Command {
	var f cutEditFlags
	var in ops.CutPausesIn
	var longer, to, pick string
	c := &cobra.Command{
		Use:   "pauses <project> [--longer-than 1s] [--to 0.5s] [--clip <clip>] [--pick 1,3]",
		Short: "Shorten long pauses between words anywhere in the cut, each one heard first",
		Long: `Shorten the long pauses between words in the latest cut: every pause heard inside a clip
that is longer than --longer-than (default 1 s) is shortened to --to (default 0.5 s), the
cut landing in the middle of the quiet so the words on both sides keep their air. It works
on any footage with a transcript, calls included. The pauses at the cuts between clips are
left alone (trim a clip to change one).

The dry run lists the pauses, numbered; saving shortens all of them, or only those in --pick.
In the dashboard, the Transcript tab's Tighten pauses plays each one first.

  metarush cut pauses "Spring promo" --dry-run
  metarush cut pauses "Spring promo" --longer-than 0.6s --to 0.3s --dry-run --json
  metarush cut pauses "Spring promo" --pick 1,3

It shows the change first; in a terminal it asks before saving, otherwise --confirm <token> saves it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			var err error
			if in.LongerThan, err = secondsFlag("--longer-than", longer); err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			if in.To, err = secondsFlag("--to", to); err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			if pick != "" {
				for _, p := range strings.Split(pick, ",") {
					n, err := strconv.Atoi(strings.TrimSpace(p))
					if err != nil {
						return fail(c, apperr.InvalidInput, fmt.Sprintf("--pick takes numbers like 1,3, not %q.", pick), "")
					}
					in.Pick = append(in.Pick, n)
				}
			}
			return runCutEdit(c, "cut.pauses", &f, func(common ops.CutEditCommon) any { x := in; x.CutEditCommon = common; x.ProjectID = args[0]; return x })
		},
	}
	fl := c.Flags()
	fl.StringVar(&longer, "longer-than", "", "pauses longer than this are shortened, like 1s or 0.8 (default 1s)")
	fl.StringVar(&to, "to", "", "what each is shortened to, like 0.5s (default 0.5s)")
	fl.StringVar(&in.Clip, "clip", "", "only in this clip (name or number)")
	fl.StringVar(&pick, "pick", "", "the pauses to shorten, like 1,3 (default: all)")
	f.add(c)
	return c
}

// secondsFlag reads a length like 0.6s, 0.6 or 600ms (empty: 0, the default).
func secondsFlag(name, v string) (float64, error) {
	given := v
	v = strings.TrimSpace(strings.ToLower(v))
	if v == "" {
		return 0, nil
	}
	mult := 1.0
	switch {
	case strings.HasSuffix(v, "ms"):
		v, mult = strings.TrimSuffix(v, "ms"), 0.001
	case strings.HasSuffix(v, "s"):
		v = strings.TrimSuffix(v, "s")
	}
	n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s takes a length in seconds, like 0.6s, not %q", name, given)
	}
	return n * mult, nil
}

func cutAddCmd() *cobra.Command {
	var f cutEditFlags
	var in ops.CutAddIn
	var after, before, at string
	c := &cobra.Command{
		Use:   "add <project> <footage> [<from> [<to>]] [--after <clip> | --before <clip> | --at start|end|<place>]",
		Short: "Put footage into the cut as a new clip: all of it, or from a time (or words) to another",
		Long: `Put footage into the latest cut as a new clip. <footage> is its short name in the cut
sheet's footage list (or its title or file name). From and to are times in the footage
(0:12.4 or 12.4) or quoted words to start or end on; without them the whole footage is used.
It goes at the end unless you say where (just before a locked ending, which stays last).

  metarush cut add "Spring promo" kitchen                        # all of it, at the end
  metarush cut add "Spring promo" interview-a 0:12.4 0:18 --after hook
  metarush cut add "Spring promo" interview-a "when I started" "tiny garage" --at start

It shows the change and its warnings first; in a terminal it asks before saving. Otherwise
run it again with --confirm <token> to save it as a new version.`,
		Args: cobra.RangeArgs(2, 4),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID, in.Footage = args[0], args[1]
			if len(args) > 2 {
				in.From = args[2]
			}
			if len(args) > 3 {
				in.To = args[3]
			}
			n := 0
			for _, v := range []string{after, before, at} {
				if v != "" {
					n++
				}
			}
			if n > 1 {
				return fail(c, apperr.InvalidInput, "Say where in one way: --after, --before or --at.", "")
			}
			switch {
			case after != "":
				in.Position = "after " + after
			case before != "":
				in.Position = "before " + before
			case at != "":
				in.Position = at
			}
			return runCutEdit(c, "cut.add", &f, func(common ops.CutEditCommon) any { x := in; x.CutEditCommon = common; x.ProjectID = args[0]; return x })
		},
	}
	fl := c.Flags()
	fl.StringVar(&in.From, "from", "", "where the clip starts in the footage: a time or quoted words")
	fl.StringVar(&in.To, "to", "", "where it ends: a time or words")
	fl.StringVar(&after, "after", "", "put it after this clip (name or number)")
	fl.StringVar(&before, "before", "", "put it before this clip")
	fl.StringVar(&at, "at", "", "start, end, or a place number (1 = first)")
	fl.StringVar(&in.Name, "name", "", "the new clip's name (default: from the footage's name)")
	fl.BoolVar(&in.Muted, "muted", false, "without its own sound")
	f.add(c)
	return c
}

func cutMoveCmd() *cobra.Command {
	var f cutEditFlags
	c := &cobra.Command{
		Use:   "move <project> <clip> <start | end | after <clip> | before <clip> | <place>>",
		Short: "Move a clip to the start, the end, next to another clip or to a place number",
		Long: `Move one clip of the latest cut. <clip> is its name in the cut sheet or its number.

  metarush cut move "Spring promo" kitchen start
  metarush cut move "Spring promo" 4 after hook
  metarush cut move "Spring promo" broll 2

It shows the change first; in a terminal it asks before saving, otherwise --confirm <token> saves it.`,
		Args: cobra.MinimumNArgs(3),
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.CutMoveIn{Clip: args[1], Position: strings.Join(args[2:], " ")}
			return runCutEdit(c, "cut.move", &f, func(common ops.CutEditCommon) any { x := in; x.CutEditCommon = common; x.ProjectID = args[0]; return x })
		},
	}
	f.add(c)
	return c
}

func cutTrimCmd() *cobra.Command {
	var f cutEditFlags
	var in ops.CutTrimIn
	var slip float64
	c := &cobra.Command{
		Use:   "trim <project> <clip> [--start <time|+s|-s|words>] [--end <time|+s|-s|words>] [--roll] [--slip <s>]",
		Short: "Change where a clip starts or ends in its footage (the rest of the cut moves up), or move a cut",
		Long: `Trim one clip of the latest cut. Times are in the clip's footage (0:12.4), a change
(+0.5 = half a second later, -0.5 = earlier) or quoted words to start or end on.
The rest of the cut moves up or back. With --roll the cut moves instead: the clip next to it
gives or takes the same time, so the video keeps its length. --slip slides the footage under
the clip (same length and place, a different moment).

  metarush cut trim "Spring promo" hook --end -0.5
  metarush cut trim "Spring promo" hook --end "very quickly"
  metarush cut trim "Spring promo" 3 --start +1.2
  metarush cut trim "Spring promo" broll --start +0.4 --roll
  metarush cut trim "Spring promo" broll --slip 2

It shows the change first; in a terminal it asks before saving, otherwise --confirm <token> saves it.`,
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Clip = args[1]
			if c.Flags().Changed("slip") {
				in.Slip = &slip
			}
			return runCutEdit(c, "cut.trim", &f, func(common ops.CutEditCommon) any { x := in; x.CutEditCommon = common; x.ProjectID = args[0]; return x })
		},
	}
	fl := c.Flags()
	fl.StringVar(&in.Start, "start", "", "the new start: a time, +s / -s, or words to start on")
	fl.StringVar(&in.End, "end", "", "the new end: a time, +s / -s, or words to end on")
	fl.BoolVar(&in.Roll, "roll", false, "move the cut instead: the neighbouring clip changes too, the video keeps its length")
	fl.Float64Var(&slip, "slip", 0, "slide the footage under the clip by this many seconds (positive: later)")
	f.add(c)
	return c
}

func cutSplitCmd() *cobra.Command {
	var f cutEditFlags
	var name string
	c := &cobra.Command{
		Use:   "split <project> <time in the video> | split <project> <clip> <time in its footage | words>",
		Short: "Cut a clip in two, at a moment of the video or of a clip's footage",
		Long: `Cut a clip of the latest cut in two (nothing you see or hear changes yet; each part can
then be trimmed, moved or removed on its own). With one time it is a moment of the video
(0:12.4); with a clip first it is a moment of that clip's footage or quoted words to split
just before.

  metarush cut split "Spring promo" 0:12.4
  metarush cut split "Spring promo" hook 0:03.2
  metarush cut split "Spring promo" hook "and then"

It shows the change first; in a terminal it asks before saving, otherwise --confirm <token> saves it.`,
		Args: cobra.RangeArgs(2, 3),
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.CutSplitIn{At: args[1], Name: name}
			if len(args) == 3 {
				in.Clip, in.At = args[1], args[2]
			}
			return runCutEdit(c, "cut.split", &f, func(common ops.CutEditCommon) any { x := in; x.CutEditCommon = common; x.ProjectID = args[0]; return x })
		},
	}
	c.Flags().StringVar(&name, "name", "", "the second part's name (default: the clip's name with -2)")
	f.add(c)
	return c
}

func cutDeleteCmd() *cobra.Command {
	var f cutEditFlags
	c := &cobra.Command{
		Use:   "delete <project> <clip> [<clip>...]",
		Short: "Remove clips from the cut; the rest closes up",
		Long: `Remove clips of the latest cut (by name or number). Nothing is lost: earlier versions
keep them.

  metarush cut delete "Spring promo" kitchen
  metarush cut delete "Spring promo" 2 5

It shows the change first; in a terminal it asks before saving, otherwise --confirm <token> saves it.`,
		Args: cobra.MinimumNArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.CutDeleteIn{Clips: args[1:]}
			return runCutEdit(c, "cut.delete", &f, func(common ops.CutEditCommon) any { x := in; x.CutEditCommon = common; x.ProjectID = args[0]; return x })
		},
	}
	f.add(c)
	return c
}

func cutDeleteRangeCmd() *cobra.Command {
	var f cutEditFlags
	c := &cobra.Command{
		Use:   "delete-range <project> <from> <to>",
		Short: "Cut out a stretch of the video (times in the video, across clips); the rest closes up",
		Long: `Cut out the part of the latest cut between two moments of the video (0:12.4 or 12.4),
whatever clips it covers: clips inside it are removed, clips it reaches into are trimmed,
and a stretch inside one clip cuts it in two.

  metarush cut delete-range "Spring promo" 0:12 0:15.5

It shows the change first; in a terminal it asks before saving, otherwise --confirm <token> saves it.`,
		Args: cobra.ExactArgs(3),
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.CutDeleteRangeIn{From: args[1], To: args[2]}
			return runCutEdit(c, "cut.deleteRange", &f, func(common ops.CutEditCommon) any { x := in; x.CutEditCommon = common; x.ProjectID = args[0]; return x })
		},
	}
	f.add(c)
	return c
}

func cutRemoveWordsCmd() *cobra.Command {
	var f cutEditFlags
	var in ops.CutRemoveWordsIn
	var pick string
	var ums bool
	c := &cobra.Command{
		Use:   "remove-words <project> \"<words>\" [--clip <clip>] [--which N | --all] | remove-words <project> --ums [--pick 1,3]",
		Short: "Take words out of the cut (cutting in the pauses around them), or ums, repeats and false starts",
		Long: `Take words out of the latest cut. The words are found in what its clips say (small
transcription slips are fine) and cut out with the pauses around them, so the join sounds
natural. When they are said more than once, say which (--which 2) or take them all (--all).

--ums suggests the ums and uhs, words said twice in a row ("because… because": the first one
goes) and false starts ("we were, we went": the first try goes). The dry run lists them,
numbered; saving takes all of them, or only those in --pick.

  metarush cut remove-words "Spring promo" "um, anyway"
  metarush cut remove-words "Spring promo" "you know" --all
  metarush cut remove-words "Spring promo" --ums --dry-run
  metarush cut remove-words "Spring promo" --ums --pick 1,3

It shows the change first; in a terminal it asks before saving, otherwise --confirm <token> saves it.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 2 {
				in.Words = args[1]
			}
			in.Cleanup = ums
			if pick != "" {
				for _, p := range strings.Split(pick, ",") {
					n, err := strconv.Atoi(strings.TrimSpace(p))
					if err != nil {
						return fail(c, apperr.InvalidInput, fmt.Sprintf("--pick takes numbers like 1,3, not %q.", pick), "")
					}
					in.Pick = append(in.Pick, n)
				}
			}
			switch {
			case in.Cleanup && in.Words != "":
				return fail(c, apperr.InvalidInput, "Give the words to take out, or --ums for suggestions, not both.", "")
			case !in.Cleanup && in.Words == "":
				return fail(c, apperr.InvalidInput, "Say which words to take out (in quotes), or --ums for suggestions.", "")
			case len(in.Pick) > 0 && !in.Cleanup:
				return fail(c, apperr.InvalidInput, "--pick goes with --ums.", "")
			}
			return runCutEdit(c, "cut.removeWords", &f, func(common ops.CutEditCommon) any { x := in; x.CutEditCommon = common; x.ProjectID = args[0]; return x })
		},
	}
	fl := c.Flags()
	fl.StringVar(&in.Clip, "clip", "", "only in this clip (name or number)")
	fl.IntVar(&in.Which, "which", 0, "when the words are said more than once: which time (1 = the first)")
	fl.BoolVar(&in.All, "all", false, "every time the words are said")
	fl.BoolVar(&ums, "ums", false, "suggest ums, words said twice and false starts")
	fl.BoolVar(&ums, "cleanup", false, "the same as --ums")
	fl.StringVar(&pick, "pick", "", "with --ums: the suggestions to take, like 1,3 (default: all)")
	f.add(c)
	return c
}

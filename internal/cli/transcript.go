package cli

import (
	"fmt"
	"math"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// Fixing transcripts from the terminal (D145–D149, docs/transcripts.md):
// `metarush transcript show|find|replace|fix|speaker|history|undo`,
// `transcript spelling list|add|remove|apply|suggestions`, `transcript pending
// list|apply`, `captions check|file` and `deliver captions`.

// clipAt is a source time for plain output ("0:12.4").
func clipAt(v float64) string { return cutsheet.Clock(math.Round(v*10) / 10) }

// transcriptAliasCmd is `metarush transcript`: reading a transcript (the bare
// form stays `metarush transcript <clip>`) and fixing it.
func transcriptAliasCmd() *cobra.Command {
	var words, original bool
	show := func(c *cobra.Command, ref string) error {
		return runOp(c, "footage.transcript", ops.TranscriptIn{AssetID: ref, Words: words, Original: original}, func(o any) {
			out := o.(*ops.TranscriptOut)
			if out.Original && out.Fixes != nil && out.Fixes.Fixes > 0 {
				fmt.Println("As the computer wrote it, before anyone fixed it:")
			}
			printTranscript(out, detailsMode(c))
		})
	}
	cmd := &cobra.Command{
		Use:   "transcript <clip>",
		Short: "What was said in a clip, with times, and fixing it: retype words, find and replace, who speaks, spelling lists, history and undo",
		Long: "Transcripts can be fixed: retyped words keep their timing, find and replace works across one clip, a project or everything,\n" +
			"and the spelling lists correct new transcripts. The computer's own transcript is kept and every fix can be undone.\n" +
			"A saved version of a cut keeps the words it was made with; fixes made since wait for a new version (transcript pending).",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return show(c, args[0]) },
	}
	cmd.Flags().BoolVar(&words, "words", false, "include word timings")
	cmd.Flags().BoolVar(&original, "original", false, "the transcript as the computer wrote it, before anyone fixed it")
	showCmd := &cobra.Command{
		Use: "show <clip>", Short: "What was said in a clip, with times (--original: as the computer wrote it)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return show(c, args[0]) },
	}
	showCmd.Flags().BoolVar(&words, "words", false, "include word timings")
	showCmd.Flags().BoolVar(&original, "original", false, "the transcript as the computer wrote it, before anyone fixed it")
	cmd.AddCommand(showCmd, transcriptFindCmd(), transcriptReplaceCmd(), transcriptFixCmd(), transcriptSpeakerCmd(), transcriptHistoryCmd(), transcriptUndoCmd(),
		spellingCmd(), pendingCmd())
	return cmd
}

// scopeFlags are where to look: --clip, --project or --everywhere.
type scopeFlags struct {
	clip, project string
	everywhere    bool
}

func (s *scopeFlags) add(c *cobra.Command) {
	c.Flags().StringVar(&s.clip, "clip", "", "one clip (its title or ID)")
	c.Flags().StringVar(&s.project, "project", "", "every clip of a project (its name or ID)")
	c.Flags().BoolVar(&s.everywhere, "everywhere", false, "every clip in the workspace")
}

func printHits(c *cobra.Command, hits []transcripts.Hit) {
	details := detailsMode(c)
	for _, h := range hits {
		line := fmt.Sprintf("  %s %s  …%s %s %s…", theirsCol(h.Clip, 24), clipAt(h.From), h.Before, theirs(h.Text), h.After)
		if h.New != "" && !h.Same {
			line += "  → " + theirs(h.New)
		}
		if h.Same {
			line += "  (already like that)"
		}
		fmt.Println(line)
		if details {
			fmt.Println("      place:", h.ID)
		}
	}
}

func transcriptFindCmd() *cobra.Command {
	var sc scopeFlags
	var in ops.TranscriptFindIn
	var with string
	cmd := &cobra.Command{
		Use:   `find "<words>" (--clip <clip> | --project <project> | --everywhere)`,
		Short: "Find words in transcripts: every place, with the words around it (nothing changes)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Query, in.AssetID, in.ProjectID, in.Everywhere = args[0], sc.clip, sc.project, sc.everywhere
			if c.Flags().Changed("with") {
				in.With = &with
			}
			return runOp(c, "transcript.find", in, func(o any) {
				out := o.(*ops.TranscriptFindOut)
				fmt.Println(out.Summary)
				printHits(c, out.Hits)
				if out.Note != "" {
					fmt.Println(out.Note)
				}
			})
		},
	}
	sc.add(cmd)
	cmd.Flags().BoolVar(&in.PartOfWords, "part-of-words", false, "also find the words inside longer words")
	cmd.Flags().BoolVar(&in.MatchCase, "match-case", false, "capitals must match")
	cmd.Flags().StringVar(&with, "with", "", "show what each place would become")
	cmd.Flags().IntVar(&in.Limit, "limit", 0, "list at most this many places (default 200)")
	return cmd
}

func transcriptReplaceCmd() *cobra.Command {
	var sc scopeFlags
	var in ops.TranscriptReplaceIn
	var dry bool
	cmd := &cobra.Command{
		Use:   `replace "<words>" "<new words>" (--clip <clip> | --project <project> | --everywhere)`,
		Short: "Change words wherever they are said (or only --only places); joined or split words keep their timing, and it can be undone",
		Long: "Whole words by default (punctuation around them stays); --part-of-words changes parts of words too. --dry-run lists the places\n" +
			"first (transcript find shows the same). --only takes the places to change (from --details). --spelling also puts the new\n" +
			"words on the spelling list, so new transcripts get them right. Saved versions of cuts keep their words until a new version.",
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Query, in.With, in.AssetID, in.ProjectID, in.Everywhere = args[0], args[1], sc.clip, sc.project, sc.everywhere
			if dry {
				with := in.With
				return runOp(c, "transcript.find", ops.TranscriptFindIn{Query: in.Query, With: &with, AssetID: in.AssetID, ProjectID: in.ProjectID, Everywhere: in.Everywhere,
					PartOfWords: in.PartOfWords, MatchCase: in.MatchCase}, func(o any) {
					out := o.(*ops.TranscriptFindOut)
					fmt.Println(out.Summary + " Nothing has changed yet.")
					printHits(c, out.Hits)
				})
			}
			return runOp(c, "transcript.replace", in, func(o any) {
				out := o.(*ops.TranscriptReplaceOut)
				fmt.Println(out.Summary)
				if out.Note != "" {
					fmt.Println(out.Note)
				}
				printSpellingSuggestion(out.Suggestion)
				if out.Changed > 0 {
					fmt.Println("Saved versions of cuts keep their words; transcript pending says which cuts have fixes waiting.")
				}
				if detailsMode(c) && out.BatchID != "" {
					fmt.Println("undo: metarush transcript undo --batch " + out.BatchID)
				}
			})
		},
	}
	sc.add(cmd)
	cmd.Flags().BoolVar(&in.PartOfWords, "part-of-words", false, "also change the words inside longer words")
	cmd.Flags().BoolVar(&in.MatchCase, "match-case", false, "capitals must match")
	cmd.Flags().StringSliceVar(&in.Only, "only", nil, "only these places (repeat or separate with commas)")
	cmd.Flags().BoolVar(&in.Spelling, "spelling", false, "also put the new words on the spelling list")
	cmd.Flags().BoolVar(&dry, "dry-run", false, "only list the places")
	return cmd
}

func printSpellingSuggestion(s *transcripts.Suggestion) {
	if s == nil {
		return
	}
	fmt.Printf("You fixed %s %d times: add it to the spelling list with transcript spelling add %s.\n", theirs(s.Word), max(s.Times, 1), theirs(s.Word))
}

// timeRange reads --from/--to (or --at, one word) as source times.
func timeRange(c *cobra.Command, from, to, atWord string) (float64, float64, error) {
	if atWord != "" {
		t, ok := editspec.ParseClock(atWord)
		if !ok {
			return 0, 0, fail(c, apperr.InvalidInput, "say where the word is with --at (like 12.5 or 0:12.5)", "")
		}
		return t, t, nil
	}
	f, ok1 := editspec.ParseClock(from)
	t, ok2 := editspec.ParseClock(to)
	if !ok1 || !ok2 {
		return 0, 0, fail(c, apperr.InvalidInput, "say where the words are with --from and --to (like 12.5 or 0:12.5), or --at for one word", "")
	}
	return f, t, nil
}

func transcriptFixCmd() *cobra.Command {
	var in ops.TranscriptFixIn
	var from, to, atWord string
	cmd := &cobra.Command{
		Use:   `fix <clip> (--at <time> | --from <time> --to <time>) "<what is said>"`,
		Short: "Retype what is said in a stretch: only the words that differ change, each keeping its time; it can be undone",
		Long: "Times are source times like 12.5 or 0:12.5; every word whose middle is inside the stretch is retyped. --expect makes it\n" +
			"refuse when the words there aren't what you saw any more. The computer's own transcript is kept.",
		Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			f, t, err := timeRange(c, from, to, atWord)
			if err != nil {
				return err
			}
			in.AssetID, in.Text, in.From, in.To = args[0], args[1], f, t
			return runOp(c, "transcript.fix", in, func(o any) {
				out := o.(*ops.TranscriptFixOut)
				fmt.Println(out.Summary)
				printSpellingSuggestion(out.Suggestion)
				if detailsMode(c) && out.BatchID != "" {
					fmt.Println("undo: metarush transcript undo --batch " + out.BatchID)
				}
			})
		},
	}
	cmd.Flags().StringVar(&atWord, "at", "", "the word said at this time")
	cmd.Flags().StringVar(&from, "from", "", "where the words start (source time)")
	cmd.Flags().StringVar(&to, "to", "", "where they end")
	cmd.Flags().StringVar(&in.Expect, "expect", "", "what the stretch says now (refuses if it changed)")
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "the project you are working in (for spelling suggestions)")
	return cmd
}

func transcriptSpeakerCmd() *cobra.Command {
	var in ops.TranscriptSpeakerIn
	var from, to, newPerson string
	cmd := &cobra.Command{
		Use:   `speaker <clip> --from <time> --to <time> (--person "<name>" | --new-person "<name>" | --unsure)`,
		Short: "Say who really says a stretch (\"this part is Lena, not Maya\"); it goes in the clip's transcript history and can be undone",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			f, t, err := timeRange(c, from, to, "")
			if err != nil {
				return err
			}
			in.AssetID, in.From, in.To = args[0], f, t
			if newPerson != "" {
				in.Person, in.NewPerson = newPerson, true
			}
			n := 0
			for _, set := range []bool{in.Person != "" && !in.NewPerson, in.NewPerson, in.Unsure} {
				if set {
					n++
				}
			}
			if n != 1 {
				return fail(c, apperr.InvalidInput, "say who it is: --person \"<name>\", --new-person \"<name>\" or --unsure (one of them)", "")
			}
			return runOp(c, "transcript.speaker", in, func(o any) {
				out := o.(*ops.TranscriptSpeakerOut)
				fmt.Println(out.Summary)
				if out.Note != "" {
					fmt.Println(out.Note)
				}
				if detailsMode(c) && out.FixID != "" {
					fmt.Println("undo: metarush transcript undo " + out.FixID)
				}
			})
		},
	}
	cmd.Flags().StringVar(&from, "from", "", "where the words start (source time)")
	cmd.Flags().StringVar(&to, "to", "", "where they end")
	cmd.Flags().StringVar(&in.Person, "person", "", "who says them (someone already named)")
	cmd.Flags().StringVar(&newPerson, "new-person", "", "who says them: someone new")
	cmd.Flags().BoolVar(&in.Unsure, "unsure", false, "not sure who it is (asked about later)")
	cmd.Flags().StringVar(&in.ProjectID, "project", "", "the project you are working in")
	return cmd
}

func transcriptHistoryCmd() *cobra.Command {
	var in ops.TranscriptHistoryIn
	cmd := &cobra.Command{
		Use:   "history <clip>",
		Short: "A clip's transcript fixes, newest first: who changed what and when, and what can still be undone",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.AssetID = args[0]
			return runOp(c, "transcript.history", in, func(o any) {
				out := o.(*ops.TranscriptHistoryOut)
				fmt.Println(out.Summary)
				details := detailsMode(c)
				for _, e := range out.Entries {
					state := ""
					switch {
					case e.Undone != nil:
						state = " (undone)"
					case e.Superseded:
						state = " (didn't carry over to the new transcript)"
					case !e.CanUndo && e.Why != "" && e.Kind != models.FixTranscribed:
						state = " (can't be undone: " + strings.TrimSuffix(e.Why, ".") + ")"
					}
					fmt.Printf("  %s  %s, %s%s\n", e.At.Local().Format("Jan 2 15:04"), e.Says, e.Who, state)
					if details {
						fmt.Printf("      fix: %s  batch: %s (%d)\n", e.ID, e.BatchID, e.BatchSize)
					}
				}
				if c := out.Carry; c != nil && len(c.Missed) > 0 {
					fmt.Printf("When it was transcribed again, %s didn't carry over:\n", plural(len(c.Missed), "fix"))
					for _, m := range c.Missed {
						fmt.Printf("  %s %s — it says %s there now\n", clipAt(m.From), theirs(m.AfterText), theirs(m.Heard))
					}
				}
			})
		},
	}
	cmd.Flags().IntVar(&in.Limit, "limit", 0, "show at most this many changes (default 500)")
	return cmd
}

func transcriptUndoCmd() *cobra.Command {
	var in ops.TranscriptUndoIn
	var last string
	cmd := &cobra.Command{
		Use:   "undo (<fix> | --batch <batch> | --last <clip>)",
		Short: "Undo a transcript fix, a whole find and replace (--batch), or a clip's latest fix (--last)",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				in.FixID = args[0]
			}
			if last != "" {
				a, err := openApp(c, "cli")
				if err != nil {
					return err
				}
				defer a.Close()
				h, err := ops.CallTyped[ops.TranscriptHistoryOut](c.Context(), a, "transcript.history", ops.TranscriptHistoryIn{AssetID: last, Limit: 50}, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				for _, e := range h.Entries {
					if e.CanUndo {
						in.BatchID = e.BatchID
						break
					}
				}
				if in.BatchID == "" {
					return fail(c, apperr.InvalidInput, "nothing in that clip's transcript can be undone", "")
				}
				return runOpWith(c, a, "transcript.undo", in, ops.Caller{}, func(o any) { fmt.Println(o.(*ops.TranscriptUndoOut).Summary) })
			}
			if in.FixID == "" && in.BatchID == "" {
				return fail(c, apperr.InvalidInput, "say which fix to undo (from transcript history --details), --batch, or --last <clip>", "")
			}
			return runOp(c, "transcript.undo", in, func(o any) {
				out := o.(*ops.TranscriptUndoOut)
				fmt.Println(out.Summary)
				if detailsMode(c) {
					for _, s := range out.Skipped {
						fmt.Printf("  stayed: %s — %s\n", s.FixID, s.Why)
					}
				}
			})
		},
	}
	cmd.Flags().StringVar(&in.BatchID, "batch", "", "every fix of one change (a find and replace)")
	cmd.Flags().StringVar(&last, "last", "", "the latest fix that can be undone in this clip")
	return cmd
}

func printSpelling(es []models.SpellingEntry, details bool) {
	for _, e := range es {
		line := "  " + theirs(e.Word)
		if len(e.HeardAs) > 0 {
			hs := make([]string, len(e.HeardAs))
			for i, h := range e.HeardAs {
				hs[i] = theirs(h)
			}
			line += "  (also heard as " + strings.Join(hs, ", ") + ")"
		}
		if e.Note != "" {
			line += " — " + theirs(e.Note)
		}
		fmt.Println(line)
		if details {
			fmt.Println("      entry:", e.ID)
		}
	}
}

func spellingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "spelling",
		Short: "Spelling lists: how names and terms are always written (\"FernBrook\", \"Maple Grove\"), part of a project's Content Settings",
		Long: "A spelling list is part of a project's Content Settings (its own, another project's or the default's). New transcripts\n" +
			"are corrected by it (the same letters written differently, and the ways a word is heard), and the transcriber is told to\n" +
			"expect the words where it can be. A clip in several projects uses all their lists; footage in no project the default's.\n" +
			"MetaRush suggests words you keep fixing the same way.",
	}
	var listIn ops.SpellingListIn
	list := &cobra.Command{
		Use: "list [--project <project>]", Short: "The spelling list a project uses (without --project, the default's)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "transcript.spelling.list", listIn, func(o any) {
				out := o.(*ops.SpellingListOut)
				fmt.Println(out.Summary)
				printSpelling(out.Entries, detailsMode(c))
			})
		},
	}
	list.Flags().StringVar(&listIn.ProjectID, "project", "", "the project (its name or ID); without it, the default's list")
	var addIn ops.SpellingAddIn
	add := &cobra.Command{
		Use: `add "<word>" [--project <project>] [--heard-as "<other spelling>"]... [--fix-existing]`, Short: "Put a word on the spelling list of a project's own settings (without --project, the default's)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			addIn.Word = args[0]
			return runOp(c, "transcript.spelling.add", addIn, func(o any) { fmt.Println(o.(*ops.SpellingAddOut).Summary) })
		},
	}
	add.Flags().StringVar(&addIn.ProjectID, "project", "", "a project with its own settings (its name or ID); without it, the default's list")
	add.Flags().StringSliceVar(&addIn.HeardAs, "heard-as", nil, "another way transcripts write it (repeat for more)")
	add.Flags().StringVar(&addIn.Note, "note", "", "a note about the word")
	add.Flags().BoolVar(&addIn.FixExisting, "fix-existing", false, "also correct the transcripts that write it another way (every clip using the list)")
	var rmIn ops.SpellingRemoveIn
	rm := &cobra.Command{
		Use: `remove "<word>" [--project <project>]`, Short: "Take a word off a spelling list", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			rmIn.Word = args[0]
			return runOp(c, "transcript.spelling.remove", rmIn, func(o any) { fmt.Println(o.(*ops.SpellingRemoveOut).Summary) })
		},
	}
	rm.Flags().StringVar(&rmIn.ProjectID, "project", "", "a project with its own settings (its name or ID); without it, the default's list")
	var sc scopeFlags
	var applyIn ops.SpellingApplyIn
	apply := &cobra.Command{
		Use: "apply (--clip <clip> | --project <project> | --settings-of <project> | --everywhere) [--dry-run]", Short: "Correct existing transcripts by the spelling lists (--settings-of: every clip using that project's settings; --dry-run lists the places)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			applyIn.AssetID, applyIn.ProjectID, applyIn.Everywhere = sc.clip, sc.project, sc.everywhere
			return runOp(c, "transcript.spelling.apply", applyIn, func(o any) {
				out := o.(*ops.SpellingApplyOut)
				fmt.Println(out.Summary)
				printHits(c, out.Hits)
			})
		},
	}
	sc.add(apply)
	apply.Flags().BoolVar(&applyIn.DryRun, "dry-run", false, "only list the places")
	apply.Flags().StringVar(&applyIn.SettingsOf, "settings-of", "", "every clip using the spelling list of this project's settings")
	apply.Flags().StringSliceVar(&applyIn.Only, "only", nil, "only these places")
	var sugIn ops.SpellingSuggestionsIn
	sug := &cobra.Command{
		Use: "suggestions [--project <project>]", Short: "Words you keep fixing the same way that aren't on a spelling list yet", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "transcript.spelling.suggestions", sugIn, func(o any) {
				out := o.(*ops.SpellingSuggestionsOut)
				fmt.Println(out.Summary)
				for _, s := range out.Suggestions {
					heard := make([]string, len(s.Heard))
					for i, h := range s.Heard {
						heard[i] = theirs(h)
					}
					fmt.Printf("  %s — fixed %s (it was %s)\n", theirs(s.Word), plural(max(s.Times, 1), "time"), strings.Join(heard, ", "))
				}
			})
		},
	}
	sug.Flags().StringVar(&sugIn.ProjectID, "project", "", "the project (its name or ID)")
	cmd.AddCommand(list, add, rm, apply, sug)
	return cmd
}

func printPending(c *cobra.Command, out *ops.TranscriptPendingOut) {
	fmt.Println(out.Summary)
	details := detailsMode(c)
	for i, p := range out.Places {
		if i == 20 {
			fmt.Printf("  … and %d more\n", len(out.Places)-20)
			break
		}
		when := ""
		if p.At >= 0 {
			when = clipAt(p.At) + " "
		}
		switch {
		case p.Kind == models.FixTranscribed:
			fmt.Printf("  %s%s was transcribed again\n", when, theirs(p.Clip))
		case p.Kind == models.FixSpeaker:
			fmt.Printf("  %s%s: who speaks, %s → %s\n", when, theirs(p.Clip), theirs(p.BeforeText), theirs(p.AfterText))
		case p.TakenBack:
			fmt.Printf("  %s%s: %s goes back to %s (undone)\n", when, theirs(p.Clip), theirs(p.AfterText), theirs(p.BeforeText))
		default:
			fmt.Printf("  %s%s: %s → %s\n", when, theirs(p.Clip), theirs(p.BeforeText), theirs(p.AfterText))
		}
		if details {
			fmt.Printf("      fix: %s\n", p.ID)
		}
	}
	if out.Note != "" {
		fmt.Println(out.Note)
	}
}

func pendingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "Text fixes waiting for a new version of a cut (list), and making that version with one review copy (apply)",
	}
	var in ops.TranscriptPendingIn
	list := &cobra.Command{
		Use: "list <project> [--branch <branch>] [--version <rN>]", Short: "Text fixes made since a version of the cut that touch its words", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ProjectID = args[0]
			return runOp(c, "transcript.pending", in, func(o any) { printPending(c, o.(*ops.TranscriptPendingOut)) })
		},
	}
	list.Flags().StringVar(&in.Branch, "branch", "", "the cut's branch (default main)")
	list.Flags().StringVar(&in.Revision, "version", "", "a version (rN or its ID); default the latest")
	var ap ops.TranscriptPendingApplyIn
	apply := &cobra.Command{
		Use:   "apply <project> [--branch <branch>] [--no-render]",
		Short: "Make one new version of the cut with every text fix waiting, and one review copy (nothing renders after each fix)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ap.ProjectID = args[0]
			return runOp(c, "transcript.pending.apply", ap, func(o any) {
				out := o.(*ops.TranscriptPendingApplyOut)
				fmt.Println(out.Summary)
				if detailsMode(c) {
					fmt.Println("version:", out.Revision.ID)
					if out.RenderID != "" {
						fmt.Println("review copy:", out.RenderID)
					}
					fmt.Println("open:", out.URL)
				}
			})
		},
	}
	apply.Flags().StringVar(&ap.Branch, "branch", "", "the cut's branch (default main)")
	apply.Flags().BoolVar(&ap.NoRender, "no-render", false, "don't make the review copy")
	apply.Flags().StringVar(&ap.Preset, "preset", "", "the review copy's shape (vertical-1080p, landscape-1080p, square-1080)")
	cmd.AddCommand(list, apply)
	return cmd
}

// captionsCmd is `metarush captions`: the caption check and captions files.
func captionsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "captions",
		Short: "A version's captions: the check before it goes out (unsure words, names to spell-check) and captions files (SubRip, WebVTT)",
	}
	var ck ops.CaptionsCheckIn
	check := &cobra.Command{
		Use: "check <project> [--version <rN>]", Short: "Words in a version's captions worth a look before it goes out", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			ck.ProjectID = args[0]
			return runOp(c, "captions.check", ck, func(o any) {
				out := o.(*ops.CaptionsCheckOut)
				fmt.Println(out.Summary)
				for _, is := range out.Issues {
					mark := "·"
					if is.Kind == transcripts.CaptionFixed {
						mark = "!"
					}
					fmt.Printf("  %s %s\n", mark, is.Says)
				}
				if out.Note != "" {
					fmt.Println(out.Note)
				}
			})
		},
	}
	check.Flags().StringVar(&ck.Revision, "version", "", "a version (rN or its ID); default the latest")
	check.Flags().StringVar(&ck.Branch, "branch", "", "the cut's branch (default main)")
	var fl ops.CaptionsFileIn
	var outPath string
	var versionWords bool
	file := &cobra.Command{
		Use:   "file <project> [--version <rN>] [--format srt|vtt] [--out <file>] [--version-words]",
		Short: "A version's captions as a file, with every text fix made since (no new render); --version-words: the words its video shows",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			fl.ProjectID = args[0]
			if versionWords {
				fl.Words = "version"
			}
			return runOp(c, "captions.file", fl, func(o any) {
				out := o.(*ops.CaptionsFileOut)
				if outPath == "" {
					fmt.Print(out.Content)
					return
				}
				if err := os.WriteFile(outPath, []byte(out.Content), 0o644); err != nil {
					fmt.Fprintln(os.Stderr, sentence("MetaRush couldn't save the captions file there: "+err.Error()))
					return
				}
				fmt.Println(out.Summary + " Saved as " + theirs(baseName(outPath)) + ".")
				if out.Note != "" {
					fmt.Println(out.Note)
				}
			})
		},
	}
	file.Flags().StringVar(&fl.Revision, "version", "", "a version (rN or its ID); default the latest")
	file.Flags().StringVar(&fl.Branch, "branch", "", "the cut's branch (default main)")
	file.Flags().StringVar(&fl.Format, "format", "srt", "srt or vtt")
	file.Flags().StringVar(&outPath, "out", "", "save to this file (default: print it)")
	file.Flags().BoolVar(&versionWords, "version-words", false, "the words the version was made with (as its video shows them), not today's")
	file.Flags().StringVar(&fl.Language, "language", "", "the captions translated into this language (es, pt-BR…)")
	file.Flags().BoolVar(&fl.MarkChanged, "mark-changed", false, "show words changed with AI in the speaker's own voice copy in italics")
	var atIn ops.CaptionsAtIn
	var atTime string
	at := &cobra.Command{
		Use:   "at <version> --at <time>",
		Short: "Which words the caption on screen at a moment of a version shows, where they are said, and what the transcript says there now",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			t, ok := editspec.ParseClock(atTime)
			if !ok {
				return fail(c, apperr.InvalidInput, "say when with --at (seconds into the video, like 12.5 or 0:12.5)", "")
			}
			atIn.RevisionID, atIn.At = args[0], &t
			return runOp(c, "captions.at", atIn, func(o any) {
				out := o.(*ops.CaptionsAtOut)
				if !out.Found {
					fmt.Println(out.Why)
					return
				}
				fmt.Printf("The caption says %s.\n", theirs(out.Text))
				if out.Fixable {
					fmt.Printf("It is said in %s from %s to %s; the transcript says %s there now.\n", theirs(out.Clip), clipAt(out.SourceFrom), clipAt(out.SourceTo), theirs(out.Now))
				} else if out.Why != "" {
					fmt.Println(out.Why)
				}
			})
		},
	}
	at.Flags().StringVar(&atTime, "at", "", "seconds into the video (12.5 or 0:12.5)")
	cmd.AddCommand(check, file, at, captionLooksCmd())
	cmd.AddCommand(captionsTranslationCmds()...)
	return cmd
}

// deliverCaptionsCmd is `metarush deliver captions`.
func deliverCaptionsCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "captions <delivered video, its .json file, or the folder>",
		Short: "Update the captions files of delivered videos with the text fixes made since (no new render)",
		Long:  "Captions drawn into a video keep the version's words; a new version (and a new delivery) changes those.",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "deliver.captions", ops.DeliverCaptionsIn{Path: args[0]}, func(o any) {
				out := o.(*ops.DeliverCaptionsOut)
				fmt.Println(out.Summary)
				if out.Note != "" {
					fmt.Println(out.Note)
				}
				if detailsMode(c) {
					for _, f := range out.Files {
						fmt.Printf("  %s: %s\n", f.Video, strings.Join(f.Files, ", "))
					}
				}
			})
		},
	}
}

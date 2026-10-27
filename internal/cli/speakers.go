package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// speakersCmd is `metarush footage speakers …`: who speaks in a video.
func speakersCmd() *cobra.Command {
	var samples, details bool
	var outDir string
	show := func(c *cobra.Command, assetID string) error {
		if samples {
			return runOp(c, "footage.speakers.samples", ops.SpeakerSamplesIn{AssetID: assetID, OutDir: outDir}, func(o any) {
				out := o.(*ops.SpeakerSamplesOut)
				if len(out.Files) == 0 {
					fmt.Println("No voices to listen to in this video yet.")
					return
				}
				last := ""
				for _, f := range out.Files {
					if f.Name != last {
						fmt.Printf("%s:\n", f.Name)
						last = f.Name
					}
					if details {
						fmt.Printf("  %s to %s  %s\n", fmtDur(f.From), fmtDur(f.To), f.Path)
					} else {
						fmt.Printf("  %s to %s\n", fmtDur(f.From), fmtDur(f.To))
					}
				}
				fmt.Printf("Saved %s to listen to.\n", plural(len(out.Files), "short recording"))
				if !details {
					fmt.Println("(Add --details to see where they are.)")
				}
			})
		}
		return runOp(c, "footage.speakers", ops.SpeakersIn{AssetID: assetID}, func(o any) { printSpeakers(o.(*ops.SpeakersOut), details) })
	}
	cmd := &cobra.Command{
		Use:   "speakers <asset-id>",
		Short: "Who speaks in a video: names, how long each speaks, voices not named yet",
		Long: "Lists the voices heard in a video. Name a voice once (`speakers rename`) and MetaRush recognizes that person in your other videos.\n" +
			"Use --samples to save a few seconds of each voice as audio files you can listen to.",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return show(c, args[0]) },
	}
	cmd.Flags().BoolVar(&samples, "samples", false, "save a few seconds of each voice as WAV files and print where")
	cmd.Flags().StringVar(&outDir, "out", "", "folder for --samples (default: this installation's scratch folder)")
	addDetails(cmd, &details)
	list := &cobra.Command{
		Use: "list <asset-id>", Short: "Same as `footage speakers <asset-id>` (read-only)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error { return show(c, args[0]) },
	}
	list.Flags().BoolVar(&samples, "samples", false, "save a few seconds of each voice as WAV files and print where")
	list.Flags().StringVar(&outDir, "out", "", "folder for --samples")
	addDetails(list, &details)
	cmd.AddCommand(list)

	var rn ops.SpeakerRenameIn
	rename := &cobra.Command{
		Use:   `rename <asset-id> <voice> "<name>"`,
		Short: `Name a voice ("Speaker 2", 2 or speaker_01); the same person is then recognized in other videos`,
		Long: "Giving a second voice of one video a name another voice there already has makes them one speaker (fixes a person split in two):\n" +
			"you are asked first (in a terminal), or pass --same-person. A name very close to someone's (\"Mya\" for \"Maya\") is refused: use their\n" +
			"exact name, or --new-person for someone new.",
		Args: cobra.ExactArgs(3),
		RunE: func(c *cobra.Command, args []string) error {
			rn.AssetID, rn.Speaker, rn.Name = args[0], args[1], args[2]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			out, err := ops.CallTyped[ops.SpeakerRenameOut](c.Context(), a, "footage.speakers.rename", rn, ops.Caller{})
			if apperr.Is(err, apperr.ConfirmRequired) && !rn.SamePerson && interactive() && !jsonMode(c) {
				if !askYes(apperr.From(err).Message, false) {
					fmt.Println("Nothing changed. Pick another name, or say they are one person with --same-person.")
					return nil
				}
				rn.SamePerson = true
				out, err = ops.CallTyped[ops.SpeakerRenameOut](c.Context(), a, "footage.speakers.rename", rn, ops.Caller{})
			}
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, out, func() {
				fmt.Println(out.Summary)
				for _, m := range out.AutoMerged {
					fmt.Printf("Also combined two voices in “%s” that both sound like %s (this can be undone).\n", m.AssetTitle, m.Name)
				}
				if out.Separate != "" && details {
					fmt.Printf("undo: metarush footage speakers unmerge %s %s\n", rn.AssetID, out.Separate)
				}
			})
		},
	}
	rename.Flags().StringVar(&rn.PersonID, "person", "", "link to this person (ID from `metarush people list --json`) instead of matching the name")
	rename.Flags().BoolVar(&rn.NewPerson, "new-person", false, "a different person (even if the name is the same as, or close to, someone's)")
	rename.Flags().BoolVar(&rn.SamePerson, "same-person", false, "confirm it: another voice in this video already has the name (they become one), or the voices sound different")
	addDetails(rename, &details)
	cmd.AddCommand(rename)

	var as ops.SpeakerAssignIn
	var asFrom, asTo string
	assign := &cobra.Command{
		Use:   `assign <asset-id> --from <time> --to <time> (--person "<name>" | --new-person "<name>" | --unsure)`,
		Short: "Fix who said a line: the words in that stretch go to that person (lines split where the speaker changes)",
		Long: "Times are source times like 12.5 or 0:12.5. The voices are learned again only from what you confirm, so a wrong name leaves nothing behind.\n" +
			"--person takes someone already named (case and accents ignored; the start of a name is enough); --new-person adds someone; --unsure\n" +
			"gives the words to a voice nobody named yet (it becomes a question).",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			as.AssetID = args[0]
			f, ok1 := editspec.ParseClock(asFrom)
			t, ok2 := editspec.ParseClock(asTo)
			if !ok1 || !ok2 {
				return fail(c, apperr.InvalidInput, "say where the words are with --from and --to (like 12.5 or 0:12.5)", "")
			}
			as.From, as.To = f, t
			if newName, _ := c.Flags().GetString("new-person"); newName != "" {
				as.Person, as.NewPerson = newName, true
			}
			n := 0
			for _, set := range []bool{as.Person != "" && !as.NewPerson, as.NewPerson, as.Unsure} {
				if set {
					n++
				}
			}
			if n != 1 {
				return fail(c, apperr.InvalidInput, "say who it is: --person \"<name>\", --new-person \"<name>\" or --unsure (one of them)", "")
			}
			return runOp(c, "footage.speakers.assign", as, func(o any) {
				out := o.(*ops.SpeakerAssignOut)
				fmt.Println(out.Summary)
				if out.Note != "" {
					fmt.Println(out.Note)
				}
				for _, d := range out.Dropped {
					fmt.Printf("We'll ask again who speaks in “%s”: it no longer sounds enough like %s.\n", d.AssetTitle, d.Speaker)
				}
				printSpeakers(&out.Speakers, details)
			})
		},
	}
	assign.Flags().StringVar(&asFrom, "from", "", "where the words start (source time)")
	assign.Flags().StringVar(&asTo, "to", "", "where they end")
	assign.Flags().StringVar(&as.Person, "person", "", "who says them (someone already named)")
	assign.Flags().String("new-person", "", "who says them: someone new")
	assign.Flags().BoolVar(&as.Unsure, "unsure", false, "not sure who it is (asked about later)")
	addDetails(assign, &details)
	cmd.AddCommand(assign)

	unmerge := &cobra.Command{
		Use:   `unmerge <asset-id> <voice>`,
		Short: "Separate two voices that were combined into one (undo giving them the same name)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "footage.speakers.unmerge", ops.SpeakerUnmergeIn{AssetID: args[0], Speaker: args[1]}, func(o any) {
				out := o.(*ops.SpeakersOut)
				fmt.Println(out.Summary)
				printSpeakers(out, details)
			})
		},
	}
	addDetails(unmerge, &details)
	cmd.AddCommand(unmerge)

	var all, detectWait bool
	detect := &cobra.Command{
		Use:   "detect [<asset-id>…|--all] [--wait]",
		Short: "Find who speaks in videos: listen for the voices in the given videos (again), or in every video with a transcript but no voices yet",
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.SpeakerDetectIn{AssetIDs: args, All: all}
			if !all && len(args) == 0 {
				return fail(c, apperr.InvalidInput, "say which videos, or --all for every video with a transcript but no voices yet", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			out, err := ops.CallTyped[ops.SpeakerDetectOut](c.Context(), a, "footage.speakers.detect", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if !detectWait || len(out.Jobs) == 0 {
				return emit(c, out, func() { printDetect(out, details) })
			}
			ids := make([]string, 0, len(out.Jobs))
			for _, j := range out.Jobs {
				ids = append(ids, j.JobID)
			}
			if !jsonMode(c) {
				fmt.Println(out.Summary)
			}
			js, err := waitJobs(c, a, ids, time.Hour, true)
			if err != nil {
				return failErr(c, err)
			}
			results := []*ops.SpeakersOut{}
			for _, j := range out.Jobs {
				sp, err := ops.CallTyped[ops.SpeakersOut](c.Context(), a, "footage.speakers", ops.SpeakersIn{AssetID: j.AssetID}, ops.Caller{})
				if err == nil {
					results = append(results, sp)
				}
			}
			return emit(c, map[string]any{"detect": out, "jobs": js, "speakers": results}, func() {
				failed := 0
				for _, j := range js {
					if j.State != models.JobSucceeded {
						failed++
					}
				}
				for _, sp := range results {
					printSpeakers(sp, details)
				}
				if failed > 0 {
					fmt.Printf("%d of %d did not finish. You can retry them from Activity in the dashboard.\n", failed, len(js))
				}
			})
		},
	}
	detect.Flags().BoolVar(&all, "all", false, "every video with a transcript but no voices yet")
	detect.Flags().BoolVar(&detectWait, "wait", false, "wait for the result (listens here if no worker is running)")
	addDetails(detect, &details)
	cmd.AddCommand(detect)

	var rj ops.SpeakerRejectIn
	reject := &cobra.Command{
		Use:   `reject <asset-id> <voice> "<name>"`,
		Short: "Say a voice is not someone (removes that suggestion or automatic label)",
		Args:  cobra.ExactArgs(3),
		RunE: func(c *cobra.Command, args []string) error {
			rj.AssetID, rj.Speaker, rj.Name = args[0], args[1], args[2]
			return runOp(c, "footage.speakers.reject", rj, func(o any) {
				fmt.Printf("Noted: that voice is not %s.\n", rj.Name)
				printSpeakers(o.(*ops.SpeakersOut), details)
			})
		},
	}
	addDetails(reject, &details)
	cmd.AddCommand(reject)

	var count int
	var wait, more bool
	hint := &cobra.Command{
		Use:   "hint <asset-id> (--count N | --more)",
		Short: "Say how many people speak in a video and listen again (keeps the words and the names you gave; 0 = count automatically)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if !c.Flags().Changed("count") && !more {
				return fail(c, apperr.InvalidInput, "say how many people speak with --count N (0 = count automatically), or --more for one more voice than was found", "")
			}
			in := ops.SpeakerHintIn{AssetID: args[0], Count: count, More: more}
			if !wait {
				return runOp(c, "footage.speakers.hint", in, func(o any) { fmt.Println(o.(*ops.SpeakerHintOut).Note) })
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			b, _ := json.Marshal(in)
			out, err := ops.Call(c.Context(), a, "footage.speakers.hint", b, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			res := out.(*ops.SpeakerHintOut)
			js, err := waitJobs(c, a, []string{res.JobID}, 30*time.Minute, jsonMode(c))
			if err != nil {
				return failErr(c, err)
			}
			b2, _ := json.Marshal(ops.SpeakersIn{AssetID: args[0]})
			sp, err := ops.Call(c.Context(), a, "footage.speakers", b2, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return emit(c, map[string]any{"hint": res, "jobs": js, "speakers": sp}, func() {
				if len(js) > 0 && js[0].State != models.JobSucceeded {
					fmt.Println("Listening again did not finish. You can retry it from Activity in the dashboard.")
					if details {
						fmt.Printf("job %s %s: %s\n", js[0].ID, js[0].State, js[0].Error)
					}
					return
				}
				printSpeakers(sp.(*ops.SpeakersOut), details)
			})
		},
	}
	hint.Flags().IntVar(&count, "count", 0, "how many people speak (0 = count automatically)")
	hint.Flags().BoolVar(&more, "more", false, "there's someone else talking: listen for one more voice than was found")
	hint.Flags().BoolVar(&wait, "wait", false, "wait for the result (listens here if no worker is running)")
	addDetails(hint, &details)
	cmd.AddCommand(hint)
	return cmd
}

// printDetect says which videos are being listened to.
func printDetect(out *ops.SpeakerDetectOut, details bool) {
	fmt.Println(out.Summary)
	for _, s := range out.Skipped {
		fmt.Println("  " + s)
	}
	if out.Queued < len(out.Jobs) && out.Queued > 0 {
		fmt.Printf("  (%d were already waiting or running)\n", len(out.Jobs)-out.Queued)
	}
	if len(out.Jobs) > 0 {
		fmt.Println("It runs in the background: the activity bar shows it, and you'll get a notification when the voices are found.")
	}
	if details {
		for _, j := range out.Jobs {
			fmt.Printf("  job %s for %s\n", j.JobID, j.AssetID)
		}
	}
}

// printSpeakers says who speaks in a video; details adds the command that names a voice.
func printSpeakers(s *ops.SpeakersOut, details bool) {
	fmt.Println(s.Summary)
	for _, v := range s.Speakers {
		line := fmt.Sprintf("  %-18s speaks %s", v.DisplayName, plainSeconds(v.Seconds))
		switch {
		case v.Source == models.SpeakerByAuto:
			line += fmt.Sprintf(", recognized by voice (%d%% voice match): check it's really them", int(v.Score*100+0.5))
		case v.Name == "" && len(v.Suggestions) > 0:
			line += fmt.Sprintf(", probably %s (%d%% voice match)?", v.Suggestions[0].Name, int(v.Suggestions[0].Score*100+0.5))
		case v.Name == "" && v.Minor:
			line += ", too brief to tell who it is"
		case v.Name == "":
			line += ", not named yet"
		}
		fmt.Println(line)
	}
	for _, p := range s.People {
		if p.Notes != "" {
			fmt.Printf("  About %s: %s\n", p.Name, oneLine(p.Notes, 160))
		}
	}
	if hasUnnamed(s.Speakers) {
		fmt.Println("Listen and name the voices on the clip's Speakers tab or the To-dos page in the dashboard.")
		if details {
			fmt.Println("Name a voice: metarush footage speakers rename " + s.AssetID + ` "Speaker 2" "Their name"`)
		}
	}
}

// oneLine shortens text to one line of at most n characters.
func oneLine(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

func hasUnnamed(vs []ops.SpeakerRow) bool {
	for _, v := range vs {
		if v.Name == "" && !v.Minor {
			return true
		}
	}
	return false
}

func plainSeconds(s float64) string {
	switch {
	case s < 1.5:
		return "about a second"
	case s < 90:
		return fmt.Sprintf("%.0f seconds", s)
	default:
		return fmt.Sprintf("%.0f minutes", s/60)
	}
}

// peopleCmd is `metarush people …`: everyone named across the library.
func peopleCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "people", Short: "People you named: rename, combine or separate them; see every video they speak in"}
	var details bool
	list := &cobra.Command{
		Use: "list", Short: "Everyone you named and how much they speak",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "people.list", ops.PeopleListIn{}, func(o any) {
				ps := o.(*ops.PeopleListOut).People
				if len(ps) == 0 {
					fmt.Println("Nobody is named yet. Name the voices on a clip's Speakers tab in the dashboard.")
					if details {
						fmt.Println(`Name a voice: metarush footage speakers rename <asset-id> "Speaker 2" "Their name"`)
					}
					return
				}
				for _, p := range ps {
					videos := fmt.Sprintf("%d videos", len(p.Appearances))
					if len(p.Appearances) == 1 {
						videos = "1 video"
					}
					fmt.Printf("%-24s %s, speaks %s\n", p.Name, videos, plainSeconds(p.Seconds))
					if p.Notes != "" {
						fmt.Printf("    %s\n", oneLine(p.Notes, 100))
					}
				}
			})
		},
	}
	addDetails(list, &details)
	cmd.AddCommand(list)
	show := &cobra.Command{
		Use: `show "<name>"`, Short: "Every video a person speaks in, and anyone who sounds alike", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "people.show", ops.PersonIn{ID: args[0]}, func(o any) {
				out := o.(*ops.PersonShowOut)
				fmt.Printf("%s speaks in %s.\n", out.Person.Name, plural(len(out.Person.Appearances), "video"))
				if out.Person.Notes != "" {
					fmt.Println("Notes: " + out.Person.Notes)
				}
				for _, ap := range out.Person.Appearances {
					how := "named by you"
					if ap.Source == models.SpeakerByAuto {
						how = fmt.Sprintf("recognized by voice, %d%% voice match: not confirmed", int(ap.Score*100+0.5))
					}
					fmt.Printf("  %-32s %s (%s)\n", trunc(orWord(ap.AssetTitle, ap.AssetName), 32), plainSeconds(ap.Seconds), how)
				}
				for _, s := range out.Similar {
					extra := ""
					if s.PitchDiffers {
						extra = " Their voices are far apart in pitch, though: probably not the same person."
					}
					fmt.Printf("Sounds like %s (%d%% voice match).%s If they are the same person, combine them on the People page; if not, say so there too.\n", s.Name, int(s.Score*100+0.5), extra)
					if details {
						fmt.Printf("  combine: metarush people merge %q %q\n", s.Name, out.Person.Name)
						fmt.Printf("  different: metarush people different %q %q\n", s.Name, out.Person.Name)
					}
				}
			})
		},
	}
	addDetails(show, &details)
	cmd.AddCommand(show)
	cmd.AddCommand(&cobra.Command{
		Use: `rename "<name>" "<new name>"`, Short: "Rename a person in every video", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "people.rename", ops.PersonRenameIn{ID: args[0], Name: args[1]}, func(o any) { fmt.Println(o.(*ops.PersonOut).Summary) })
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: `merge "<name>" "<into name>"`, Short: "Combine two people who are really the same person", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "people.merge", ops.PersonMergeIn{From: args[0], Into: args[1]}, func(o any) { fmt.Println(o.(*ops.PersonOut).Summary) })
		},
	})
	var sp ops.PersonSplitIn
	split := &cobra.Command{
		Use: `split "<name>" --asset <asset-id> [--voice "Speaker 2"] --as "<other name>"`, Short: "Say a person's voice in one video is really someone else", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			sp.ID = args[0]
			if sp.AssetID == "" || strings.TrimSpace(sp.Name) == "" {
				return fail(c, apperr.InvalidInput, "say which video (--asset) and who it really is (--as)", "")
			}
			return runOp(c, "people.split", sp, func(o any) { fmt.Println(o.(*ops.PersonOut).Summary) })
		},
	}
	split.Flags().StringVar(&sp.AssetID, "asset", "", "the video")
	split.Flags().StringVar(&sp.Speaker, "voice", "", `one voice in it ("Speaker 2"); default: all of this person's voices there`)
	split.Flags().StringVar(&sp.Name, "as", "", "who it really is")
	split.Flags().BoolVar(&sp.NewPerson, "new-person", false, "who it really is is someone new (even if the name is close to someone's)")
	cmd.AddCommand(split)

	var up ops.PersonUpdateIn
	var notes, notesFile string
	update := &cobra.Command{
		Use:   `update "<person>" (--notes "…" | --notes-file <file> | --clear-notes)`,
		Short: "Write notes about a person: who they are, how to name or title them, what to highlight or avoid (Claude uses them)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			up.ID = args[0]
			switch {
			case notesFile != "":
				b, err := os.ReadFile(notesFile)
				if err != nil {
					return fail(c, apperr.InvalidInput, "could not read "+notesFile+": "+err.Error(), "")
				}
				t := string(b)
				up.Notes = &t
			case c.Flags().Changed("notes"):
				up.Notes = &notes
			case !up.ClearNotes:
				return fail(c, apperr.InvalidInput, "nothing to change: give --notes, --notes-file or --clear-notes", "")
			}
			return runOp(c, "people.update", up, func(o any) { fmt.Println(o.(*ops.PersonOut).Summary) })
		},
	}
	update.Flags().StringVar(&notes, "notes", "", "the notes (up to 2,000 characters)")
	update.Flags().StringVar(&notesFile, "notes-file", "", "read the notes from a file")
	update.Flags().BoolVar(&up.ClearNotes, "clear-notes", false, "remove the notes")
	cmd.AddCommand(update)

	cmd.AddCommand(&cobra.Command{
		Use: `different "<name>" "<other name>"`, Short: "Say two people are different people (never asked about as the same person again)", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "people.distinct", ops.PersonPairIn{A: args[0], B: args[1]}, func(o any) { fmt.Println(o.(*ops.PersonOut).Summary) })
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use: `compare "<name>" "<other name>"`, Short: "How alike two people's voices sound (and whether one is much lower)", Args: cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "people.compare", ops.PersonPairIn{A: args[0], B: args[1]}, func(o any) {
				out := o.(*ops.PersonCompareOut)
				fmt.Println(out.Summary)
				if out.Distinct {
					fmt.Println("You already said they are different people.")
				}
				if details && (out.PitchA > 0 || out.PitchB > 0) {
					fmt.Printf("  pitch: %s %.0f Hz, %s %.0f Hz\n", out.A.Name, out.PitchA, out.B.Name, out.PitchB)
				}
			})
		},
	})
	var rebuildAll bool
	rebuild := &cobra.Command{
		Use:   `rebuild ("<name>" | --all)`,
		Short: "Rebuild a person's voice from the clips you confirmed (drops anything a wrong name left behind) and re-check automatic labels",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := ops.PersonRebuildIn{All: rebuildAll}
			if len(args) == 1 {
				in.ID = args[0]
			}
			if (in.ID == "") == !rebuildAll {
				return fail(c, apperr.InvalidInput, "say whose voice to rebuild, or --all", "")
			}
			return runOp(c, "people.rebuild", in, func(o any) {
				out := o.(*ops.PersonRebuildOut)
				fmt.Println(out.Summary)
				for _, d := range out.Dropped {
					fmt.Printf("  We'll ask again who speaks in “%s”.\n", d.AssetTitle)
				}
			})
		},
	}
	rebuild.Flags().BoolVar(&rebuildAll, "all", false, "everyone's")
	cmd.AddCommand(rebuild)
	cmd.AddCommand(peopleLearnedCmd(), peopleUnlearnCmd(), peopleEvidenceCmd())
	return cmd
}

// peopleLearnedCmd is `metarush people learned "<name>"`: what a person's voice
// and face are learned from (D158), compared with a voice or face in question.
func peopleLearnedCmd() *cobra.Command {
	var in ops.PersonLearnedIn
	c := &cobra.Command{
		Use:   `learned "<name>" [--asset <asset-id> --voice "Speaker 2"] [--face <face-id>]`,
		Short: "What a person's voice and face are learned from, how alike each is (to a voice or face in question too), and which seems to be someone else",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ID = args[0]
			if (in.AssetID == "") != (in.Speaker == "") {
				return fail(c, apperr.InvalidInput, "a voice in question needs both --asset and --voice", "")
			}
			return runOp(c, "people.learned", in, func(o any) { printLearned(o.(*ops.PersonLearnedOut), detailsMode(c)) })
		},
	}
	c.Flags().StringVar(&in.AssetID, "asset", "", "the voice in question: its clip")
	c.Flags().StringVar(&in.Speaker, "voice", "", `the voice in question: "Speaker 2", its number or its name`)
	c.Flags().StringVar(&in.FaceID, "face", "", "the face in question")
	return c
}

// peopleUnlearnCmd is `metarush people unlearn "<name>" …`: take back a voice
// or face a person's name was learned from (D158).
func peopleUnlearnCmd() *cobra.Command {
	var in ops.PersonUnlearnIn
	c := &cobra.Command{
		Use:   `unlearn "<name>" (--asset <asset-id> --voice "Speaker 2" | --face <face-id>) (--as "<who it is>" [--new-person] | --unsure)`,
		Short: "Take back a voice or face a person's name was learned from, say who it really is, and learn their voice and face again",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ID = args[0]
			if in.FaceID == "" && (in.AssetID == "" || in.Speaker == "") {
				return fail(c, apperr.InvalidInput, "say what to take back: --asset and --voice, or --face", "")
			}
			if strings.TrimSpace(in.Name) == "" && in.OtherID == "" && !in.Unsure {
				return fail(c, apperr.InvalidInput, "say who it really is (--as), or --unsure", "")
			}
			return runOp(c, "people.unlearn", in, func(o any) {
				out := o.(*ops.PersonUnlearnOut)
				fmt.Println(out.Done)
				printLearned(&out.PersonLearnedOut, detailsMode(c))
			})
		},
	}
	c.Flags().StringVar(&in.AssetID, "asset", "", "the voice to take back: its clip")
	c.Flags().StringVar(&in.Speaker, "voice", "", `the voice to take back: "Speaker 2", its number or its name`)
	c.Flags().StringVar(&in.FaceID, "face", "", "the face to take back (for a group answer: every face it named)")
	c.Flags().StringVar(&in.Name, "as", "", "who it really is")
	c.Flags().StringVar(&in.OtherID, "other-id", "", "who it really is, by ID")
	c.Flags().BoolVar(&in.NewPerson, "new-person", false, "add --as as someone new")
	c.Flags().BoolVar(&in.Unsure, "unsure", false, "not sure who it is (it is asked about again)")
	c.Flags().StringVar(&in.AgainstAssetID, "against-asset", "", "the voice in question: its clip (to say whether it still sounds unlike them)")
	c.Flags().StringVar(&in.AgainstSpeaker, "against-voice", "", "the voice in question")
	c.Flags().StringVar(&in.AgainstFaceID, "against-face", "", "the face in question")
	return c
}

// printLearned lists what a person's voice and face are learned from.
func printLearned(out *ops.PersonLearnedOut, details bool) {
	fmt.Println(out.Summary)
	if out.Voice != nil {
		fmt.Printf("  This voice (%s): %d%% like %s's voice\n", out.Voice.Speaker, int(out.Voice.Score*100+0.5), out.Person.Name)
	}
	for i, v := range out.Voices {
		line := fmt.Sprintf("  %d. %s in %s", i+1, v.Speaker, theirs(v.AssetTitle))
		if v.LikeThis != nil {
			line += fmt.Sprintf(" · %d%% like this voice", int(*v.LikeThis*100+0.5))
		} else if v.Alike != nil {
			line += fmt.Sprintf(" · %d%% like the others", int(*v.Alike*100+0.5))
		}
		if v.Odd {
			line += " · " + v.Why
		}
		fmt.Println(line)
		if details {
			fmt.Printf("     take back: metarush people unlearn %q --asset %s --voice %s --as \"<who it is>\"\n", out.Person.Name, v.AssetID, v.Label)
			if v.Sample != nil {
				fmt.Printf("     listen: %s to %s\n", fmtDur(v.Sample.From), fmtDur(v.Sample.To))
			}
		}
	}
	for i, f := range out.Faces {
		what := "a face"
		if f.Faces > 1 {
			what = fmt.Sprintf("%d faces named in one answer", f.Faces)
		}
		line := fmt.Sprintf("  face %d. %s in %s", i+1, what, theirs(f.AssetTitle))
		if f.LikeThis != nil {
			line += fmt.Sprintf(" · %d%% like this face", int(*f.LikeThis*100+0.5))
		} else if f.Alike != nil {
			line += fmt.Sprintf(" · %d%% like the others", int(*f.Alike*100+0.5))
		}
		if f.Odd {
			line += " · " + f.Why
		}
		fmt.Println(line)
		if details {
			fmt.Printf("     take back: metarush people unlearn %q --face %s --as \"<who it is>\"\n", out.Person.Name, f.FaceID)
		}
	}
}

// assetRightsCmd is `metarush asset rights <asset> --license …`.
func assetRightsCmd() *cobra.Command {
	var in ops.AssetRightsIn
	cmd := &cobra.Command{
		Use: `rights <asset-id> --license "<what allows using it>" [--source <url>] [--attribution "<credit>"]`, Short: "Record where a music or audio file comes from and whether you may use it",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.AssetID = args[0]
			return runOp(c, "asset.rights", in, func(o any) { fmt.Println(o.(*ops.AssetRightsOut).Summary) })
		},
	}
	cmd.Flags().StringVar(&in.License, "license", "", `what allows using it ("original music", "royalty-free library", "CC BY 4.0"…)`)
	cmd.Flags().StringVar(&in.Source, "source", "", "where it came from (URL or vendor)")
	cmd.Flags().StringVar(&in.Attribution, "attribution", "", "credit line, if one is required")
	return cmd
}

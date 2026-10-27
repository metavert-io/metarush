package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/generate"
	"github.com/metavert-io/metarush/internal/ops"
)

// peopleOnly runs a people-only op from a person's own terminal.
func peopleOnly(c *cobra.Command, op string, in any, what string, human func(o any)) error {
	if !isInteractive() {
		return fail(c, apperr.HumanOnly, "Only a person can "+what+", in the dashboard or in their own terminal.", "Open People in the dashboard, or run this command yourself.")
	}
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	return runOpWith(c, a, op, in, ops.Caller{Human: true}, human)
}

func looksCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "looks", Aliases: []string{"look"}, Short: "Looks: how a person (with their consent) or a fictional character appears in generated video",
		Long: `A look is 2 or 3 stills and up to three short clips (3 seconds or less) of someone from your footage,
a fixed description of who they are and how they look, defaults and a test sheet. A person's look
needs their consent first (metarush consent record). Only people create, approve or withdraw a look;
new shots use only approved looks.

  metarush looks create --title Ada --person Ada --description "…" --still <clip>@0:04 --still <clip>@0:19 --clip <clip>@0:07-0:09.5
  metarush looks test ada --max-cost 0.25
  metarush looks approve ada
  metarush generate shot "walks into the garage" --look ada --project <id>`}
	cmd.AddCommand(looksListCmd(), looksShowCmd(), looksCreateCmd(), looksUpdateCmd(), looksDecideCmd(true), looksDecideCmd(false), looksTestCmd())
	return cmd
}

func printLook(v *generate.LookView, details bool) {
	fmt.Println(v.Summary)
	for _, p := range v.Problems {
		fmt.Println("  ✗ " + p)
	}
	if v.Consent != nil {
		fmt.Println("  " + v.Consent.Summary)
	}
	if details {
		fmt.Printf("    %s  %s  version %s\n", v.ID, v.Ref, v.Version)
		for _, s := range v.Stills {
			fmt.Printf("    %s %s@%.2f sha256 %s\n", s.Ref, s.AssetID, s.At, s.SHA256)
		}
		for _, c := range v.Clips {
			fmt.Printf("    %s %s@%.2f-%.2f sha256 %s\n", c.Ref, c.AssetID, c.From, c.To, c.SHA256)
		}
	}
}

func looksListCmd() *cobra.Command {
	var in generate.LookListIn
	var details bool
	c := &cobra.Command{Use: "list", Short: "Looks, and whether each can be used for new shots", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "looks.list", in, func(o any) {
				out := o.(*generate.LookListOut)
				for i := range out.Looks {
					printLook(&out.Looks[i], details)
				}
				fmt.Println(out.Summary)
			})
		}}
	c.Flags().StringVar(&in.Person, "person", "", "one person's looks")
	c.Flags().BoolVar(&in.All, "all", false, "withdrawn looks too")
	addDetails(c, &details)
	return c
}

func looksShowCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{Use: "show <look>", Short: "A look: stills, clips, description, consent and test shots", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "looks.show", generate.LookRefIn{Look: args[0]}, func(o any) {
				v := o.(*generate.LookView)
				printLook(v, details)
				fmt.Println("Description: " + v.Description)
				for _, t := range v.TestShots {
					fmt.Println("Test shot: " + t.Summary)
				}
			})
		}}
	addDetails(c, &details)
	return c
}

// stillFlag reads <clip>@<time>, or a picture's name.
func stillFlag(s string) (generate.LookStillIn, error) {
	if strings.HasPrefix(s, "ast_") {
		f, err := frameFlag(s)
		if err != nil {
			return generate.LookStillIn{}, err
		}
		return generate.LookStillIn{AssetID: f.AssetID, At: f.At}, nil
	}
	return generate.LookStillIn{Resource: s}, nil
}

// clipFlag reads <clip>@<from>-<to>.
func clipFlag(s string) (generate.LookClipIn, error) {
	id, rng, ok := strings.Cut(s, "@")
	a, b, ok2 := strings.Cut(rng, "-")
	from, err1 := clockSeconds(a)
	to, err2 := clockSeconds(b)
	if !ok || !ok2 || err1 != nil || err2 != nil || !strings.HasPrefix(id, "ast_") {
		return generate.LookClipIn{}, apperr.New(apperr.InvalidInput, "A clip of a look is a clip and a range, like ast_…@0:07-0:09.5 (3 seconds or less).")
	}
	return generate.LookClipIn{AssetID: id, From: from, To: to}, nil
}

func lookParts(stills, clips []string) ([]generate.LookStillIn, []generate.LookClipIn, error) {
	var ss []generate.LookStillIn
	for _, s := range stills {
		v, err := stillFlag(s)
		if err != nil {
			return nil, nil, err
		}
		ss = append(ss, v)
	}
	var cs []generate.LookClipIn
	for _, s := range clips {
		v, err := clipFlag(s)
		if err != nil {
			return nil, nil, err
		}
		cs = append(cs, v)
	}
	return ss, cs, nil
}

func looksCreateCmd() *cobra.Command {
	var in generate.LookCreateIn
	var stills, clips []string
	var details bool
	c := &cobra.Command{Use: "create", Short: "Make a look from stills and short clips of your footage (people only; a person's look needs their consent)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			var err error
			if in.Stills, in.Clips, err = lookParts(stills, clips); err != nil {
				return failErr(c, err)
			}
			return peopleOnly(c, "looks.create", in, "make a look", func(o any) {
				out := o.(*generate.LookOut)
				fmt.Println(out.Summary)
				printLook(&out.Look, details)
			})
		}}
	fl := c.Flags()
	fl.StringVar(&in.Title, "title", "", "how you name it")
	fl.StringVar(&in.Name, "name", "", "short name (default: from the title)")
	fl.StringVar(&in.Person, "person", "", "whose look (a person on the People page); leave out for a fictional character")
	fl.StringVar(&in.Style, "style", "", "realistic (default) or stylized")
	fl.StringVar(&in.StyleNote, "style-note", "", "for a stylized look: the style, e.g. a watercolor illustration")
	fl.StringVar(&in.Description, "description", "", "who they are and how they look (sent with every shot)")
	fl.StringSliceVar(&stills, "still", nil, "a still: <clip>@<time>, or a picture's name (2 or 3)")
	fl.StringSliceVar(&clips, "clip", nil, "a short clip: <clip>@<from>-<to> (3 seconds or less; up to 3)")
	fl.StringVar(&in.AspectRatio, "aspect", "", "default shape: 16:9 or 9:16")
	fl.IntVar(&in.Seconds, "seconds", 0, "default length")
	fl.StringVar(&in.Quality, "quality", "", "default quality: draft or final")
	fl.BoolVar(&in.NoRealPeople, "no-real-people", false, "for a character: you say the stills show no real person")
	_ = c.MarkFlagRequired("title")
	_ = c.MarkFlagRequired("description")
	addDetails(c, &details)
	return c
}

func looksUpdateCmd() *cobra.Command {
	var in generate.LookUpdateIn
	var stills, clips []string
	var styleNote string
	var details bool
	c := &cobra.Command{Use: "update <look>", Short: "Change a look; changing what it shows needs approving again (people only)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Look = args[0]
			var err error
			if in.Stills, in.Clips, err = lookParts(stills, clips); err != nil {
				return failErr(c, err)
			}
			if c.Flags().Changed("style-note") {
				in.StyleNote = &styleNote
			}
			return peopleOnly(c, "looks.update", in, "change a look", func(o any) {
				out := o.(*generate.LookOut)
				fmt.Println(out.Summary)
				printLook(&out.Look, details)
			})
		}}
	fl := c.Flags()
	fl.StringVar(&in.Title, "title", "", "a new name")
	fl.StringVar(&in.Style, "style", "", "realistic or stylized")
	fl.StringVar(&styleNote, "style-note", "", "the style of a stylized look")
	fl.StringVar(&in.Description, "description", "", "a new description")
	fl.StringSliceVar(&stills, "still", nil, "replace the stills: <clip>@<time> or a picture's name")
	fl.StringSliceVar(&clips, "clip", nil, "replace the clips: <clip>@<from>-<to>")
	fl.BoolVar(&in.ClearClips, "no-clips", false, "remove the clips")
	fl.StringVar(&in.AspectRatio, "aspect", "", "default shape")
	fl.IntVar(&in.Seconds, "seconds", 0, "default length")
	fl.StringVar(&in.Quality, "quality", "", "default quality")
	fl.BoolVar(&in.NoRealPeople, "no-real-people", false, "for a character: you say the stills show no real person")
	addDetails(c, &details)
	return c
}

func looksDecideCmd(approve bool) *cobra.Command {
	var in generate.LookDecideIn
	use, short, op, word := "approve <look>", "Approve a look so new shots can use it (people only; look at its test shots first)", "looks.approve", "approve"
	if !approve {
		use, short, op, word = "revoke <look>", "Withdraw a look: nothing new is made with it (people only)", "looks.revoke", "withdraw"
	}
	c := &cobra.Command{Use: use, Short: short, Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Look = args[0]
			if isInteractive() && !jsonMode(c) && ask(fmt.Sprintf("Type %q to %s the look %s", word, word, args[0]), "") != word {
				return fail(c, apperr.Canceled, "Nothing was decided.", "")
			}
			return peopleOnly(c, op, in, word+" a look", func(o any) { fmt.Println(o.(*generate.LookOut).Summary) })
		}}
	c.Flags().StringVar(&in.Note, "note", "", "why, in a few words")
	return c
}

func looksTestCmd() *cobra.Command {
	var in generate.LookTestIn
	var maxCost float64
	var details bool
	c := &cobra.Command{Use: "test <look>", Short: "Make a look's test sheet: a few short draft shots, priced first (people only)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Look = args[0]
			if c.Flags().Changed("max-cost") {
				in.MaxUSD = &maxCost
			}
			return peopleOnly(c, "looks.test", in, "try out a look", func(o any) {
				out := o.(*generate.GenRunOut)
				if out.DryRun {
					printEstimate(&out.Estimate, details)
					return
				}
				fmt.Println(out.Summary)
			})
		}}
	c.Flags().StringSliceVar(&in.Prompts, "prompt", nil, "what a test shot shows (up to 3; default two standard shots)")
	c.Flags().Float64Var(&maxCost, "max-cost", 0, "the most you agree to pay, in US dollars")
	c.Flags().BoolVar(&in.DryRun, "dry-run", false, "price it only")
	addDetails(c, &details)
	return c
}

func consentCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "consent", Short: "Consent to make a person's likeness in generated video: yours, or theirs recorded (never a child or a public figure)",
		Long: `Generated video shows a real person only with their consent. Your own likeness needs a statement
in your own words; anyone else's needs a recording of them giving consent (a sound or video clip in
MetaRush), with what it covers and until when. Either can be withdrawn at any time. Only people record
or withdraw consent; MetaRush never records it for a child or a public figure.

  metarush consent record --person Ada --self --adult --statement "I, Ada, agree to …"
  metarush consent record --person Maya --recording <clip> --adult --statement "…" --project <id> --expires 2027-10-20
  metarush consent record --person Ada --self --recording <clip> --adult --statement "…" --covers shots,voice,presenters --service d-id
  metarush consent list
  metarush consent revoke <consent>`}
	cmd.AddCommand(consentListCmd(), consentShowCmd(), consentRecordCmd(), consentRevokeCmd())
	return cmd
}

func consentListCmd() *cobra.Command {
	var in generate.ConsentListIn
	var details bool
	c := &cobra.Command{Use: "list", Short: "Consent records and whether each is in force", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "consent.list", in, func(o any) {
				out := o.(*generate.ConsentListOut)
				for _, v := range out.Consents {
					fmt.Println(v.Summary)
					if details {
						fmt.Printf("    %s  %s  recorded %s by %s\n", v.ID, v.Status, v.RecordedAt.Local().Format("Jan 2 2006"), v.RecordedBy.UserName)
					}
				}
				fmt.Println(out.Summary)
			})
		}}
	c.Flags().StringVar(&in.Person, "person", "", "one person's")
	addDetails(c, &details)
	return c
}

func consentShowCmd() *cobra.Command {
	return &cobra.Command{Use: "show <consent>", Short: "A consent record: who, the statement, what it covers, until when", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "consent.show", generate.ConsentIDIn{ID: args[0]}, func(o any) {
				v := o.(*generate.ConsentView)
				fmt.Println(v.Summary)
				fmt.Printf("Statement: %s\n", v.Statement)
				if len(v.Looks) > 0 {
					fmt.Printf("Looks that rely on it: %s\n", strings.Join(v.Looks, ", "))
				}
				for _, p := range v.Presenters {
					fmt.Println("Presenter " + p)
				}
				for _, vc := range v.VoiceCopies {
					fmt.Println("Voice copy " + vc)
				}
			})
		}}
}

func consentRecordCmd() *cobra.Command {
	var in generate.ConsentRecordIn
	var also []string
	c := &cobra.Command{Use: "record", Short: "Record consent: your own statement (--self), or their recording (--recording) (people only)", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			in.Uses = append(in.Uses, also...)
			return peopleOnly(c, "consent.record", in, "record consent", func(o any) { fmt.Println(o.(*generate.ConsentOut).Summary) })
		}}
	fl := c.Flags()
	fl.StringVar(&in.Person, "person", "", "whose likeness (a name on the People page; a new name adds them)")
	fl.BoolVar(&in.Self, "self", false, "it is your own likeness")
	fl.StringVar(&in.Statement, "statement", "", "the consent in the person's own words")
	fl.StringVar(&in.Recording, "recording", "", "someone else's likeness: a clip of them giving consent")
	fl.StringSliceVar(&in.Projects, "project", nil, "only these projects (default: every project)")
	fl.StringVar(&in.Note, "note", "", "anything they limited it to")
	fl.StringVar(&in.ExpiresOn, "expires", "", "the last day it covers (YYYY-MM-DD); someone else's ends after a year unless you say")
	fl.StringVar(&in.TimeZone, "time-zone", "", "where the last day ends (e.g. America/Los_Angeles; default: this computer's time zone)")
	fl.BoolVar(&in.Adult, "adult", false, "you confirm the person is 18 or older")
	fl.BoolVar(&in.PublicFigure, "public-figure", false, "the person is a public figure (MetaRush then refuses)")
	fl.StringSliceVar(&in.Uses, "covers", nil, "what it covers: shots, voice, presenters, lip-sync, new-words (a word they said changed in their own voice copy) (default: shots and voice); a presenter needs --recording, your own too")
	fl.StringSliceVar(&in.Providers, "service", nil, "an AI service that may keep the recording and footage, match their lips or say new words in their voice copy, as the recording says (d-id, sync, elevenlabs…)")
	// lip sync's first spellings (D279): the same lists.
	fl.StringSliceVar(&also, "uses", nil, "the same as --covers")
	fl.StringSliceVar(&in.Services, "services", nil, "the same as --service")
	_ = c.MarkFlagRequired("person")
	_ = c.MarkFlagRequired("statement")
	return c
}

func consentRevokeCmd() *cobra.Command {
	var in generate.ConsentRevokeIn
	c := &cobra.Command{Use: "revoke <consent>", Short: "Withdraw a consent: nothing new is made with that likeness (people only)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.ID = args[0]
			return peopleOnly(c, "consent.revoke", in, "withdraw consent", func(o any) { fmt.Println(o.(*generate.ConsentOut).Summary) })
		}}
	c.Flags().StringVar(&in.Note, "note", "", "why, in a few words")
	return c
}

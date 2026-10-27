package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/abilities"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// metarush ability … (D230–D235): capabilities added without a release.
// Reading (list, find, show, plan, runs) is open to anyone, Claude included;
// checking a folder records a version; approving and taking back are for
// owners at an interactive terminal (or the dashboard); setting up is for a
// person on that computer, and adding keys for an owner.

func abilityCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ability",
		Short: "Abilities: capabilities added without a new MetaRush, checked in a sandbox, approved by an owner, set up on each computer",
		Long: `An ability is a folder with an ability.yaml (what it does, what it runs, its inputs and outputs, its cost,
the keys and places it uses and what leaves this computer) and the script or program it runs. Its version is a
fingerprint of the folder. A check runs it once in the sandbox with the network off; an owner approves each
version after reading what it may do; a person sets it up on each computer; then members run it, in the
sandbox, its network only through MetaRush, which adds its keys so the code never sees them.

  metarush ability check ./stock-footage          (records a version that waits for an owner)
  metarush ability find "stock footage"
  metarush ability show stock-footage
  metarush ability approve stock-footage          (owners, at an interactive terminal)
  metarush ability setup stock-footage --key stock (people; keys: owners)
  metarush ability plan stock-footage --input words="ocean waves"
  metarush ability run stock-footage --input words="ocean waves"
  metarush ability export stock-footage           (a file another workspace brings in)
  metarush ability import stock-footage.ability.zip (a draft: check it here, an owner approves it)`,
	}
	cmd.AddCommand(abilityListCmd(), abilityFindCmd(), abilityShowCmd(), abilityCheckCmd(), abilityApproveCmd(), abilityRevokeCmd(), abilitySetupCmd(),
		abilityExportCmd(), abilityImportCmd())
	cmd.AddCommand(abilityRunCmds()...)
	return cmd
}

func abilityListCmd() *cobra.Command {
	var in abilities.AbilityListIn
	var details bool
	c := &cobra.Command{
		Use:   "list",
		Short: "List the abilities: what each does, whether an owner approved it, whether it is set up here, its cost and what leaves this computer",
		Args:  cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "ability.list", in, func(o any) {
				out := o.(*abilities.AbilityListOut)
				for _, r := range out.Abilities {
					fmt.Println(r.Line)
					if r.WaitingVersion > 0 {
						fmt.Printf("  Version %d waits for an owner.\n", r.WaitingVersion)
					}
					if details {
						fmt.Printf("  %s@%s · version %d · %d in all\n", r.Name, abilities.Short(r.Version), r.Number, r.VersionsInAll)
					}
				}
				fmt.Println(out.Message)
			})
		},
	}
	c.Flags().StringVar(&in.State, "state", "", "only abilities whose newest version is: waiting, approved, turned_down, revoked or failed")
	addDetails(c, &details)
	return c
}

func abilityFindCmd() *cobra.Command {
	var in abilities.AbilityFindIn
	var details bool
	c := &cobra.Command{
		Use:   "find <words…>",
		Short: "Find abilities for a task: at most eight, one line each (what it does, whether it is set up here, its cost, what leaves)",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Words = strings.Join(args, " ")
			return runOp(c, "ability.find", in, func(o any) {
				out := o.(*abilities.AbilityFindOut)
				for _, r := range out.Matches {
					fmt.Println(r.Line)
					if details {
						fmt.Printf("  %s@%s\n", r.Name, abilities.Short(r.Version))
					}
				}
				fmt.Println(out.Message)
			})
		},
	}
	c.Flags().BoolVar(&in.All, "all", false, "also abilities no owner has approved yet (they can't run)")
	c.Flags().IntVar(&in.Limit, "limit", 0, "at most this many (1 to 8)")
	addDetails(c, &details)
	return c
}

func printAbilityPage(p abilities.AbilityPage) {
	fmt.Println("What it installs:")
	for _, l := range p.Installs {
		fmt.Println("  " + l)
	}
	fmt.Println("What it reaches: " + p.Network)
	if len(p.Keys) == 0 {
		fmt.Println("Keys: none.")
	} else {
		fmt.Println("Keys (MetaRush adds them on the way out; the code only sees a stand-in):")
		for _, k := range p.Keys {
			state := "not on this computer yet"
			if k.Configured {
				state = "on this computer, " + k.Masked
			}
			fmt.Printf("  %s, in %s, only for %s (%s)\n", k.Name, k.Place, strings.Join(k.Hosts, ", "), state)
		}
	}
	fmt.Println("What leaves this computer: " + p.Leaves)
	fmt.Println("What it brings back: " + p.Makes)
	fmt.Println("What it costs: " + p.Cost)
	fmt.Println("Licence: " + theirs(p.Licence))
	if p.Terms != "" {
		fmt.Println("The service's terms: " + theirs(p.Terms))
	}
	if p.Outside != "" {
		fmt.Println(p.Outside)
	}
	if p.Local != "" {
		fmt.Println("This computer: " + p.Local)
	}
	fmt.Println(p.Decides)
	fmt.Println(p.Limits)
}

func printAbilityShow(out *abilities.AbilityShowOut, details bool) {
	v := out.Ability
	fmt.Println(out.Message)
	printAbilityPage(out.Page)
	if len(v.Facts.Inputs) > 0 {
		fmt.Println("Its inputs:")
		for _, in := range v.Facts.Inputs {
			line := "  " + in.Name + " (" + in.Kind
			if in.Optional {
				line += ", optional"
			}
			if in.Default != nil {
				line += fmt.Sprintf(", %v unless you say", in.Default)
			}
			line += ")"
			if in.Says != "" {
				line += ": " + theirs(in.Says)
			}
			fmt.Println(line)
		}
	}
	fmt.Printf("On %s: ", out.Here.Machine)
	if out.Here.Ready {
		fmt.Println("everything a run needs is here.")
	} else {
		fmt.Println(strings.Join(out.Here.Problems, " "))
	}
	if len(out.SetUpOn) > 0 {
		fmt.Println("Set up on: " + strings.Join(out.SetUpOn, ", "))
	}
	if len(v.Check.Problems) > 0 {
		fmt.Println("Its check found:")
		for _, p := range v.Check.Problems {
			fmt.Println("  " + p)
		}
	}
	for _, w := range v.Check.Warnings {
		fmt.Println("Note: " + w)
	}
	if len(out.Versions) > 1 {
		fmt.Println("Versions:")
		for _, r := range out.Versions {
			fmt.Printf("  %d: %s, checked %s\n", r.Number, r.StateLabel, r.CheckedAt.Local().Format("2 Jan 2006 15:04"))
		}
	}
	if len(out.Runs) > 0 {
		fmt.Println("Latest runs:")
		for _, r := range out.Runs {
			fmt.Printf("  %d: %s on %s, %s\n", r.Number, r.StateLabel, r.Machine, r.CreatedAt.Local().Format("2 Jan 15:04"))
		}
	}
	if out.Next != "" {
		fmt.Println(out.Next)
	}
	if details {
		fmt.Printf("  version: %s (%d)\n  link: %s\n", v.Hash, v.Number, out.Link)
		if v.Notes != "" {
			fmt.Println("notes.md:\n" + v.Notes)
		}
		fmt.Println("ability.yaml:\n" + v.Manifest)
	}
}

func abilityShowCmd() *cobra.Command {
	var details bool
	c := &cobra.Command{
		Use:   "show <name[@version]>",
		Short: "Show an ability: what an owner approves it from (what it installs, places, keys, what leaves, cost, licence), its inputs, versions and runs",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "ability.show", abilities.AbilityRefIn{Ref: args[0]}, func(o any) { printAbilityShow(o.(*abilities.AbilityShowOut), details || detailsMode(c)) })
		},
	}
	addDetails(c, &details)
	return c
}

func abilityCheckCmd() *cobra.Command {
	var details bool
	var ver string
	c := &cobra.Command{
		Use:   "check <folder> | --version <name@version>",
		Short: "Check an ability's folder (or, with --version, a version brought in from another workspace): its manifest, no keys or oversized files, its test run once in the sandbox with the network off; a version that passes waits for an owner",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := abilities.AbilityCheckIn{Version: ver}
			if len(args) == 1 {
				in.Path = args[0]
			}
			return runOp(c, "ability.check", in, func(o any) {
				out := o.(*abilities.AbilityCheckOut)
				fmt.Println(out.Message)
				for _, p := range out.Problems {
					fmt.Println("  " + p.String())
				}
				for _, w := range out.Warnings {
					fmt.Println("Note: " + w)
				}
				if t := out.Test; t != nil && (details || !t.OK) && t.Log != "" {
					fmt.Println("What its test run printed:")
					fmt.Println(indent(t.Log, "    "))
				}
				if out.Next != "" {
					fmt.Println(out.Next)
				}
				if details && out.Version != "" {
					fmt.Printf("  version: %s (%d)\n", out.Version, out.Number)
				}
			})
		},
	}
	addDetails(c, &details)
	c.Flags().StringVar(&ver, "version", "", "check a version MetaRush keeps (one brought in from an ability file) instead of a folder")
	return c
}

func abilityApproveCmd() *cobra.Command {
	var in abilities.AbilityApproveIn
	c := &cobra.Command{
		Use:   "approve <name[@version]> [--reject] [--note …] [--open-network] [--local-service]",
		Short: "Approve a version of an ability for the workspace, or turn it down — owners, at an interactive terminal: what it may do is shown first",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person approves an ability, in the dashboard or in their own terminal.", "Open Library → Abilities in the dashboard, or run this command yourself in a terminal.")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			show, err := ops.CallTyped[abilities.AbilityShowOut](c.Context(), a, "ability.show", abilities.AbilityRefIn{Ref: in.Ref}, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			printAbilityShow(show, false)
			in.Ref = show.Ability.Name + "@" + show.Ability.Hash
			if l := show.Local; l != nil && in.LocalService {
				// What the owner agrees to is exactly the program they see here (D284).
				printListening(l)
				in.LocalProgram = l.Now
			}
			word := "approve"
			if in.Reject {
				word = "turn down"
			}
			if ask(fmt.Sprintf("Type %q to %s version %d", word, word, show.Ability.Number), "") != word {
				return fail(c, apperr.Canceled, "nothing was decided", "")
			}
			return runOpWith(c, a, "ability.approve", in, ops.Caller{Human: true}, func(o any) { fmt.Println(o.(*abilities.AbilityDecisionOut).Message) })
		},
	}
	c.Flags().BoolVar(&in.Reject, "reject", false, "turn the version down instead")
	c.Flags().StringVar(&in.Note, "note", "", "why, in a few words (kept with the decision)")
	c.Flags().BoolVar(&in.OpenNetwork, "open-network", false, "also agree to the whole internet, for a version whose manifest asks for it")
	c.Flags().BoolVar(&in.LocalService, "local-service", false, "also agree to the one service on this computer its manifest names (the program listening on its port now), outside MetaRush's sandbox")
	return c
}

// printListening says the program on an ability's local service now, in
// full, and the one an owner agreed to when it differs.
func printListening(l *abilities.HereLocal) {
	if l.NowWords != "" {
		fmt.Printf("Listening on port %d now: %s.\n", l.Port, l.NowWords)
	}
	if l.AgreedWords != "" && !l.Same {
		fmt.Printf("An owner agreed to: %s.\n", l.AgreedWords)
	}
	if l.Problem != "" {
		fmt.Println(l.Problem)
	}
}

func abilityRevokeCmd() *cobra.Command {
	var in abilities.AbilityRevokeIn
	c := &cobra.Command{
		Use:   "revoke <name[@version]> [--note …]",
		Short: "Take an ability's approval back — owners, at an interactive terminal: nothing runs that version until an owner approves it again",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person takes an approval back, in the dashboard or in their own terminal.", "Open Library → Abilities in the dashboard, or run this command yourself in a terminal.")
			}
			if ask("Type \"revoke\" to take the approval back", "") != "revoke" {
				return fail(c, apperr.Canceled, "nothing was changed", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "ability.revoke", in, ops.Caller{Human: true}, func(o any) { fmt.Println(o.(*abilities.AbilityDecisionOut).Message) })
		},
	}
	c.Flags().StringVar(&in.Note, "note", "", "why, in a few words")
	return c
}

func abilitySetupCmd() *cobra.Command {
	var in abilities.AbilitySetupIn
	var programs, keys, files []string
	c := &cobra.Command{
		Use:   "setup <name> [--program name=/full/path] [--file name=/full/path] [--key name] [--agree-local] [--forget]",
		Short: "Set an ability up on this computer — a person, at an interactive terminal: the programs it runs, pinned by fingerprint, and its keys (owners; typed, never on the command line)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			if !interactive() {
				return fail(c, apperr.HumanOnly, "Only a person sets an ability up on a computer, in the dashboard or in their own terminal.", "Run this command yourself in a terminal on that computer.")
			}
			for _, p := range programs {
				name, path, ok := strings.Cut(p, "=")
				if !ok || strings.TrimSpace(name) == "" {
					return fail(c, apperr.InvalidInput, "--program is a program's name and its full path, joined by an equals sign.", "")
				}
				if in.Programs == nil {
					in.Programs = map[string]string{}
				}
				in.Programs[strings.TrimSpace(name)] = strings.TrimSpace(path)
			}
			for _, f := range files {
				name, path, ok := strings.Cut(f, "=")
				if !ok || strings.TrimSpace(name) == "" {
					return fail(c, apperr.InvalidInput, "--file is a file's name and its full path, joined by an equals sign.", "")
				}
				if in.Files == nil {
					in.Files = map[string]string{}
				}
				in.Files[strings.TrimSpace(name)] = strings.TrimSpace(path)
			}
			sort.Strings(keys)
			for _, k := range keys {
				v := askSecret(fmt.Sprintf("The %s key (it is kept in this computer's keychain; nothing shows as you type)", k))
				if strings.TrimSpace(v) == "" {
					return fail(c, apperr.Canceled, "no key was given", "")
				}
				if in.Keys == nil {
					in.Keys = map[string]string{}
				}
				in.Keys[k] = v
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if in.AgreeLocal {
				// The program listening now, as the owner sees it (D284).
				show, err := ops.CallTyped[abilities.AbilityShowOut](c.Context(), a, "ability.show", abilities.AbilityRefIn{Ref: in.Ref}, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				if show.Local == nil || show.Local.Now == nil {
					return fail(c, apperr.InvalidInput, "Nothing to agree to: it reaches no service on this computer, or nothing listens there now.", "")
				}
				printListening(show.Local)
				if ask(fmt.Sprintf("Type \"yes\" to let it reach this program on port %d of this computer", show.Local.Port), "") != "yes" {
					return fail(c, apperr.Canceled, "nothing was set up", "")
				}
				in.LocalProgram = show.Local.Now
			}
			if len(in.Files) > 0 {
				// What each file really is, before it goes into every run (D285).
				look := in
				look.Look, look.Keys = true, nil
				lo, err := ops.CallTyped[abilities.AbilitySetupOut](c.Context(), a, "ability.setup", look, ops.Caller{Human: true})
				if err != nil {
					return failErr(c, err)
				}
				for _, f := range lo.Files {
					fmt.Printf("It asks for %s; you give it %s, %s (%s).\n", theirs(f.Says), f.Path, f.About, byteWords(f.Size))
				}
				if ask("Type \"yes\" to give it these files in every run", "") != "yes" {
					return fail(c, apperr.Canceled, "nothing was set up", "")
				}
			}
			return runOpWith(c, a, "ability.setup", in, ops.Caller{Human: true}, func(o any) {
				out := o.(*abilities.AbilitySetupOut)
				for _, ch := range out.Changed {
					fmt.Println("  " + upperFirst(ch) + ".")
				}
				fmt.Println(out.Message)
			})
		},
	}
	c.Flags().StringArrayVar(&programs, "program", nil, "a program it runs and its full path: python3=/opt/homebrew/bin/python3 (repeat; default: where this computer finds it)")
	c.Flags().StringArrayVar(&files, "file", nil, "a file it reads, set up on this computer: whisper-model=/full/path/ggml-medium.bin (owners; repeat; pinned by fingerprint, copied into each run)")
	c.Flags().StringArrayVar(&keys, "key", nil, "a key it uses, typed at the prompt (owners; repeat)")
	c.Flags().BoolVar(&in.AgreeLocal, "agree-local", false, "agree, on this computer, to the program listening on its local service now (owners; it is shown first)")
	c.Flags().BoolVar(&in.Forget, "forget", false, "take the setup off this computer (its keys stay)")
	return c
}

func abilityExportCmd() *cobra.Command {
	var in abilities.AbilityExportIn
	c := &cobra.Command{
		Use:   "export <name[@version]> [file]",
		Short: "Export a version of an ability to a file another workspace can bring in (its approval here goes along as information only)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			if len(args) == 2 {
				in.Out = args[1]
			}
			return runOp(c, "ability.export", in, func(o any) {
				out := o.(*abilities.AbilityExportOut)
				fmt.Println(out.Message)
				fmt.Println("  " + out.File)
			})
		},
	}
	c.Flags().BoolVar(&in.Force, "force", false, "write over a file that is already there")
	return c
}

func abilityImportCmd() *cobra.Command {
	var in abilities.AbilityImportIn
	c := &cobra.Command{
		Use:   "import <file>",
		Short: "Bring in an ability file from another workspace as a draft: never approved or set up here; check it on this computer, then an owner approves it",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.File = args[0]
			return runOp(c, "ability.import", in, func(o any) {
				out := o.(*abilities.AbilityImportOut)
				fmt.Println(out.Message)
				if out.From != nil && len(out.From.Decisions) > 0 {
					d := out.From.Decisions[len(out.From.Decisions)-1]
					fmt.Printf("  There: %s by %s on %s (information only).\n", strings.ReplaceAll(d.State, "_", " "), d.By, d.At.Local().Format("2 Jan 2006"))
				}
				fmt.Println("  What leaves this computer when it runs: " + out.Leaves)
				if out.Next != "" {
					fmt.Println(out.Next)
				}
			})
		},
	}
	c.Flags().BoolVar(&in.AsVersion, "as-version", false, "bring it in as a new version of the ability of the same name here, though they share no version")
	c.Flags().BoolVar(&in.DryRun, "dry-run", false, "read and check the file and say what would come in; keep nothing")
	return c
}

package cli

import (
	"fmt"
	"io"
	"math"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
)

// promptSecret and isInteractive are variables so tests can drive the hidden-prompt path.
var (
	promptSecret  = askSecret
	isInteractive = interactive
)

func providersCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "providers", Aliases: []string{"provider", "integrations"}, Short: "Set up integrations that need an API key (ElevenLabs narration)",
		Long: `Integrations such as ElevenLabs need an API key. Keys are per machine: each machine
keeps its own in the keychain (or in MetaRush's private file when it was set up with
--secrets env) and never shares it with the others. Keys are read from a hidden prompt,
from standard input (--key-stdin) or from an environment variable (--key-env NAME), never
from the command line.

  metarush providers set elevenlabs              # paste the key when asked
  metarush providers status elevenlabs
  metarush providers settings elevenlabs --voice Ada --project <id>`}
	cmd.AddCommand(&cobra.Command{
		Use: "list", Short: "Integrations and whether this machine has a key",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "provider.list", ops.EmptyIn{}, func(o any) {
				for _, st := range o.(*providers.ListOut).Providers {
					fmt.Println(statusLine(st))
				}
			})
		},
	})
	var project string
	var refresh bool
	status := &cobra.Command{
		Use: "status [name]", Short: "Key status on this machine, plan usage, the chosen voice and model, and a project's budget", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			name := providers.ElevenLabsName
			if len(args) == 1 {
				name = args[0]
			}
			return runOp(c, "provider.status", providers.StatusIn{Name: name, ProjectID: project, Refresh: refresh}, func(o any) { printProviderStatus(o.(*providers.StatusOut)) })
		},
	}
	status.Flags().StringVar(&project, "project", "", "also show this project's voice and budget")
	status.Flags().BoolVar(&refresh, "refresh", false, "ask the provider for the latest plan usage")
	cmd.AddCommand(status)

	var keyStdin, noTest, allowUnsafe bool
	var keyEnv string
	set := &cobra.Command{
		Use: "set <name>", Short: "Save this machine's API key (hidden prompt, --key-stdin or --key-env; never an argument)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := providers.Get(args[0])
			if err != nil {
				return failErr(c, err)
			}
			key, err := readKey(c, p, keyStdin, keyEnv)
			if err != nil {
				return failErr(c, err)
			}
			return runOp(c, "provider.set", providers.SetIn{Name: p.Name(), Key: key, SkipTest: noTest, AllowUnsafeSecretPath: allowUnsafe}, func(o any) {
				st := o.(*providers.Status)
				fmt.Printf("%s is set up on this machine (key %s, kept in %s).\n", st.Title, st.Masked, sourceWords(st.Source))
				if st.LastTest != nil {
					fmt.Println(st.LastTest.Message)
				}
				fmt.Println("Other machines need their own key.")
			})
		},
	}
	set.Flags().BoolVar(&keyStdin, "key-stdin", false, "read the key from standard input")
	set.Flags().StringVar(&keyEnv, "key-env", "", "read the key from this environment variable")
	set.Flags().BoolVar(&noTest, "no-test", false, "save without checking the key with the provider (offline)")
	set.Flags().BoolVar(&allowUnsafe, "allow-unsafe-secret-path", false, "env-mode installations: allow <home>/.env inside a repository that ignores it")
	cmd.AddCommand(set)

	var tKeyStdin bool
	var tKeyEnv string
	test := &cobra.Command{
		Use: "test <name>", Short: "Check the saved key (or a new one from --key-stdin / --key-env, without saving it)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := providers.Get(args[0])
			if err != nil {
				return failErr(c, err)
			}
			key := ""
			if tKeyStdin || tKeyEnv != "" {
				if key, err = readKey(c, p, tKeyStdin, tKeyEnv); err != nil {
					return failErr(c, err)
				}
			}
			return runOp(c, "provider.test", providers.TestIn{Name: p.Name(), Key: key}, func(o any) {
				r := o.(*providers.TestOut)
				if r.Result.OK {
					fmt.Println("✓ " + r.Result.Message)
				} else {
					fmt.Println("✗ " + r.Result.Message)
				}
			})
		},
	}
	test.Flags().BoolVar(&tKeyStdin, "key-stdin", false, "test a key read from standard input (not saved)")
	test.Flags().StringVar(&tKeyEnv, "key-env", "", "test a key from this environment variable (not saved)")
	cmd.AddCommand(test)

	var yes bool
	remove := &cobra.Command{
		Use: "remove <name>", Short: "Remove this machine's API key", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			p, err := providers.Get(args[0])
			if err != nil {
				return failErr(c, err)
			}
			if !yes {
				if !isInteractive() || jsonMode(c) {
					return fail(c, apperr.ConfirmRequired, "Removing the key needs your OK.", "Pass --yes.")
				}
				if !askYes(fmt.Sprintf("Remove the %s key from this machine? Narration that is already made stays", p.Title()), false) {
					return fail(c, apperr.Canceled, "Nothing was removed.", "")
				}
			}
			return runOp(c, "provider.remove", providers.NameIn{Name: p.Name()}, func(o any) {
				fmt.Printf("%s key removed from this machine.\n", p.Title())
			})
		},
	}
	remove.Flags().BoolVar(&yes, "yes", false, "do not ask for confirmation")
	cmd.AddCommand(remove)
	cmd.AddCommand(providerSettingsCmd())
	return cmd
}

func providerSettingsCmd() *cobra.Command {
	var in providers.SettingsIn
	var stability, similarity, style, speed, budget float64
	var boost, region string
	settings := &cobra.Command{
		Use:   "settings <name>",
		Short: "Choose the narration voice, model and voice settings of a project's own settings (or, without --project, the default's) and a project's budget",
		Long: `The voice is part of a project's Content Settings: with --project it changes that
project's own settings (every project using them gets it; a project that uses another's
can change its budget, not its voice); without --project, the default's — what new
projects use. A narration line can still pick its own voice (e.g. two narrators).

  --voice       voice name or ID (see: metarush voiceover voices)
  --model       model ID (see: metarush voiceover models); default: picked from the live list
  --stability   0 = more expressive … 1 = steadier
  --similarity  how closely it sticks to the original voice (0–1)
  --style       exaggerates the speaker's style (0–1; some models ignore it)
  --speed       0.7 slower … 1.2 faster
  --budget      the most this project may spend, in credits (≈ characters); 0 = no limit`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Name = args[0]
			f := c.Flags()
			fp := func(name string, v float64) *float64 {
				if f.Changed(name) {
					x := v
					return &x
				}
				return nil
			}
			in.Stability, in.SimilarityBoost, in.Style, in.Speed, in.BudgetCredits = fp("stability", stability), fp("similarity", similarity), fp("style", style), fp("speed", speed), fp("budget", budget)
			if f.Changed("speaker-boost") {
				b := boost == "true" || boost == "on" || boost == "yes"
				in.SpeakerBoost = &b
			}
			if f.Changed("region") {
				in.Region = &region
			}
			print := func(o any) {
				r := o.(*providers.SettingsOut)
				printProviderStatus(&r.StatusOut)
				if r.Note != "" {
					fmt.Println(r.Note)
				}
				for _, w := range r.Warnings {
					fmt.Println("note: " + w)
				}
			}
			if in.BudgetCredits != nil {
				// A project's budget is a person's to set (D241).
				if !interactive() {
					return fail(c, apperr.HumanOnly, "setting a project's budget is for people", "run this command yourself in a terminal, or set it in Settings → Integrations")
				}
				a, err := openApp(c, "cli")
				if err != nil {
					return err
				}
				defer a.Close()
				return runOpWith(c, a, "provider.settings", in, ops.Caller{Human: true}, print)
			}
			return runOp(c, "provider.settings", in, print)
		},
	}
	f := settings.Flags()
	f.StringVar(&in.ProjectID, "project", "", "a project: the voice of its own settings, and its budget (default: the default's settings)")
	f.StringVar(&in.Voice, "voice", "", "voice name or ID")
	f.StringVar(&in.LibraryOwnerID, "library-owner", "", "for a voice from the public library: its owner (adds it to your account)")
	f.StringVar(&in.ModelID, "model", "", "model ID")
	f.Float64Var(&stability, "stability", 0, "0 more expressive … 1 steadier")
	f.Float64Var(&similarity, "similarity", 0, "closeness to the original voice (0–1)")
	f.Float64Var(&style, "style", 0, "style exaggeration (0–1)")
	f.Float64Var(&speed, "speed", 1, "0.7 slower … 1.2 faster")
	f.StringVar(&boost, "speaker-boost", "", "on | off")
	f.StringVar(&in.OutputFormat, "output-format", "", "audio quality, e.g. mp3_44100_128 (default) or mp3_44100_192")
	f.StringVar(&region, "region", "", "data residency: us | eu | in | sg (empty = default)")
	f.Float64Var(&budget, "budget", 0, "project budget in credits (0 = no limit)")
	f.StringVar(&in.Favorite, "favorite", "", "mark a voice (ID) as a favourite of these settings")
	f.StringVar(&in.Unfavorite, "unfavorite", "", "remove a favorite")
	f.StringSliceVar(&in.Clear, "clear", nil, "reset: voice, model, stability, similarityBoost, style, speed, speakerBoost, outputFormat")
	return settings
}

// readKey gets a key without it ever appearing in argv: stdin, a named
// environment variable, or a hidden prompt.
func readKey(c *cobra.Command, p providers.Provider, fromStdin bool, envName string) (string, error) {
	switch {
	case fromStdin:
		b, err := io.ReadAll(io.LimitReader(c.InOrStdin(), 8<<10))
		if err != nil {
			return "", err
		}
		k := strings.TrimSpace(string(b))
		if k == "" {
			return "", apperr.New(apperr.InvalidInput, "no key on standard input")
		}
		return k, nil
	case envName != "":
		k := strings.TrimSpace(os.Getenv(envName))
		if k == "" {
			return "", apperr.New(apperr.InvalidInput, "the environment variable %s is empty", envName)
		}
		return k, nil
	case isInteractive() && !jsonMode(c):
		fmt.Println(p.KeyHelp())
		if _, file := p.(providers.KeyShaper); file {
			// A key file (a service account's, D282) isn't one line to paste.
			return "", apperr.New(apperr.InvalidInput, "%s takes a key file, given on standard input.", p.Title()).
				WithCommand("metarush providers set " + p.Name() + " --key-stdin < <key file>")
		}
		k := strings.TrimSpace(promptSecret("Paste your " + p.Title() + " API key"))
		if k == "" {
			return "", apperr.New(apperr.Canceled, "no key entered")
		}
		return k, nil
	}
	return "", apperr.New(apperr.InvalidInput, "no key given").
		WithHint("pipe it in with --key-stdin, or name an environment variable with --key-env (keys are never accepted as arguments)")
}

func sourceWords(src string) string {
	switch src {
	case providers.SourceKeychain:
		return "this machine's keychain"
	case providers.SourceFile:
		return "MetaRush's private file on this machine"
	case providers.SourceEnvironment:
		return "an environment variable"
	}
	return "nowhere"
}

func statusLine(st providers.Status) string {
	if !st.Configured {
		return fmt.Sprintf("%-12s not set up on this machine — %s", st.Title, st.Summary)
	}
	test := "not checked yet"
	if st.LastTest != nil {
		test = "checked " + st.LastTest.At.Local().Format("Jan 2 15:04")
		if !st.LastTest.OK {
			test = "last check failed"
		}
	}
	return fmt.Sprintf("%-12s set up (key %s, %s)", st.Title, st.Masked, test)
}

func printProviderStatus(so *providers.StatusOut) {
	st := so.Status
	fmt.Println(statusLine(st))
	if st.Configured {
		fmt.Printf("  key kept in %s\n", sourceWords(st.Source))
	}
	if t := st.LastTest; t != nil {
		fmt.Println("  " + t.Message)
		if u := t.Usage; u != nil && u.ResetsAt != nil {
			fmt.Printf("  usage resets %s\n", u.ResetsAt.Local().Format("Jan 2"))
		}
	}
	// The voice, model, budget and favourites below are the AI voice's
	// (narration); a video service such as Gemini has none of them.
	if st.Name != providers.ElevenLabsName {
		return
	}
	eff := so.Effective
	voice := eff.VoiceName
	if voice == "" {
		voice = "not chosen yet"
	}
	model := eff.ModelName
	if model == "" {
		model = "chosen automatically from your account's models"
	}
	fmt.Printf("  voice: %s (%s)\n  model: %s\n", voice, scopeWords(eff.From["voice"]), model)
	if s := eff.Settings; s.Stability != nil || s.Speed != nil {
		var parts []string
		if s.Stability != nil {
			parts = append(parts, fmt.Sprintf("steadiness %.2f", *s.Stability))
		}
		if s.Speed != nil {
			parts = append(parts, fmt.Sprintf("speed %.2f×", *s.Speed))
		}
		fmt.Println("  settings: " + strings.Join(parts, ", "))
	}
	if b := so.Budget; b != nil {
		if b.LimitCredits != nil {
			fmt.Printf("  project budget: %s of %s credits used\n", providers.Commas(int64(math.Round(b.UsedCredits))), providers.Commas(int64(math.Round(*b.LimitCredits))))
		} else {
			fmt.Printf("  project budget: no limit (%s credits used)\n", providers.Commas(int64(math.Round(b.UsedCredits))))
		}
	}
	if w := so.Settings; w != nil && len(w.Favorites) > 0 {
		names := []string{}
		for _, f := range w.Favorites {
			names = append(names, firstNonEmptyStr(f.Name, f.VoiceID))
		}
		fmt.Println("  favourite voices: " + strings.Join(names, ", "))
	}
	if so.From != nil {
		line := "  the voice comes from " + theirs(so.From.Name) + "'s settings"
		if so.ReadOnly {
			line += " (change it there, or give this project its own settings)"
		}
		if so.UsedBy > 0 {
			line += fmt.Sprintf("; also used by %s", plural(so.UsedBy, "project"))
		}
		fmt.Println(line)
	}
}

func scopeWords(scope string) string {
	switch scope {
	case "project":
		return "this project"
	case "settings", "workspace":
		return "the project's settings"
	case "beat":
		return "this line"
	}
	return "default"
}

func firstNonEmptyStr(vs ...string) string {
	for _, v := range vs {
		if v != "" {
			return v
		}
	}
	return ""
}

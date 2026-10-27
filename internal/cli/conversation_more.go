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

// conversationMoreCmds are the conversation commands for clean-up, webcam
// matching, settings, imports and outputs.
func conversationMoreCmds() []*cobra.Command {
	return []*cobra.Command{convCleanupCmd(), convDecideCmd(), convMatchCmd(), convSettingsCmd(), convImportCmd(), convDetectCmd(), convMomentsCmd(), convShortCmd()}
}

// convPlanFlow prints an answer that carries a change to a cut (and applies
// it with --confirm); --json gets the whole answer.
func convPlanFlow(c *cobra.Command, a *app.App, full any, po *conversation.CallPlanOut, confirm string, human func()) error {
	if confirm != "" && po.Result != nil {
		return planFlow(c, a, po.Result.Plan, confirm, map[string]any{"changes": po.Changes}, func() { fmt.Println(po.Summary) })
	}
	return resultFlow(c, a, full, pending(po.Result, po.Applied), func() {
		fmt.Println(po.Summary)
		for _, ch := range po.Changes {
			fmt.Println("  • " + ch)
		}
		if human != nil {
			human()
		}
	})
}

func printSuggestion(s conversation.CleanupInfo) {
	line := fmt.Sprintf("  %-4s %-13s %s", s.ID, s.When, s.Text)
	if s.Quote != "" && s.Kind == conversation.KindCallTalk {
		line += " “" + s.Quote + "”"
	}
	state := map[string]string{"accepted": " (accepted)", "skipped": " (skipped)"}[s.State]
	fmt.Println(line + state)
	if s.State == "open" {
		fmt.Println("       " + s.Does)
	}
}

func convCleanupCmd() *cobra.Command {
	var project string
	var kinds []string
	var deadAir float64
	cmd := &cobra.Command{Use: "cleanup <conversation>", Short: "Look for what calls leave behind: call talk, dead air, frozen webcams, sound dropouts, the app's labels", Args: cobra.ExactArgs(1),
		Long: `Each finding is a suggestion; nothing changes until you accept it (metarush conversation decide).
Examples:
  metarush conversation cleanup "Garden show"
  metarush conversation cleanup "Garden show" --kinds call-talk,dead-air --dead-air 4`,
		RunE: func(c *cobra.Command, args []string) error {
			in := conversation.CallCleanupIn{CallRefIn: conversation.CallRefIn{Conversation: args[0], ProjectID: project}, Kinds: kinds, DeadAir: deadAir}
			return runOp(c, "conversation.cleanup", in, func(o any) {
				out := o.(*conversation.CallCleanupOut)
				fmt.Println(out.Summary)
				for _, s := range out.Suggestions {
					printSuggestion(s)
				}
				for _, n := range out.Notes {
					fmt.Println("  · " + n)
				}
			})
		}}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "where to look for it by name")
	f.StringSliceVar(&kinds, "kinds", nil, "only these: call-talk, dead-air, freeze, dropout, chrome")
	f.Float64Var(&deadAir, "dead-air", 0, "silences at least this long count as dead air (seconds; default 2.5)")
	return cmd
}

func convDecideCmd() *cobra.Command {
	var project, branch, confirm string
	var accept, skip, reopen, applyIt bool
	cmd := &cobra.Command{Use: "decide <conversation> <suggestion>…", Short: "Accept or skip clean-up suggestions (accepting changes the cut: cut out, someone else shown, a crop)", Args: cobra.MinimumNArgs(2),
		Long: `Suggestions are named c1, c2… (metarush conversation cleanup); "open" means every open one.
Examples:
  metarush conversation decide "Garden show" c1 c3 --accept --apply
  metarush conversation decide "Garden show" c2 --skip
  metarush conversation decide "Garden show" open --accept`,
		RunE: func(c *cobra.Command, args []string) error {
			n := 0
			decision := ""
			for _, x := range []struct {
				on   bool
				name string
			}{{accept, "accept"}, {skip, "skip"}, {reopen, "reopen"}} {
				if x.on {
					n++
					decision = x.name
				}
			}
			if n != 1 {
				return fail(c, apperr.InvalidInput, "say --accept, --skip or --reopen", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			in := conversation.CallDecideIn{CallRefIn: conversation.CallRefIn{Conversation: args[0]}, IDs: args[1:], Decision: decision, Project: project, Branch: branch, Apply: applyIt}
			out, err := ops.CallTyped[conversation.CallDecideOut](c.Context(), a, "conversation.decide", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return convPlanFlow(c, a, out, &out.CallPlanOut, confirm, nil)
		}}
	f := cmd.Flags()
	f.BoolVar(&accept, "accept", false, "accept them")
	f.BoolVar(&skip, "skip", false, "skip them (leave the call as it is there)")
	f.BoolVar(&reopen, "reopen", false, "undecide them")
	f.StringVar(&project, "project", "", "the project whose cut changes (default: the conversation's)")
	f.StringVar(&branch, "branch", "", "the branch (default main)")
	f.StringVar(&confirm, "confirm", "", "apply with the token from the check")
	f.BoolVar(&applyIt, "apply", false, "put accepted changes in the cut as a new version")
	return cmd
}

func convMatchCmd() *cobra.Command {
	var project, branch, reference, confirm string
	var off, applyIt, details bool
	cmd := &cobra.Command{Use: "match <conversation>", Short: "Match the webcams: brightness, contrast and colour fitted to one person's picture, measured before and after", Args: cobra.ExactArgs(1),
		Long: `Examples:
  metarush conversation match "Garden show" --apply
  metarush conversation match "Garden show" --reference sam
  metarush conversation match "Garden show" --off --apply      # as recorded again`,
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			in := conversation.CallMatchIn{CallRefIn: conversation.CallRefIn{Conversation: args[0]}, Reference: reference, Off: off, Project: project, Branch: branch, Apply: applyIt}
			out, err := ops.CallTyped[conversation.CallMatchOut](c.Context(), a, "conversation.match", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return convPlanFlow(c, a, out, &out.CallPlanOut, confirm, func() {
				for _, m := range out.Members {
					fmt.Printf("  %-14s %s\n", m.Name, m.Text)
					if details && m.Look != nil {
						fmt.Printf("  %-14s exposure %.3f · contrast %.3f · saturation %.3f · warmth %+.3f · tint %+.3f · %d frames · face %v\n", "", m.Look.Exposure, m.Look.Contrast, m.Look.Saturation, m.Look.Warmth, m.Look.Tint, m.Frames, m.Faces)
					}
				}
			})
		}}
	f := cmd.Flags()
	f.StringVar(&reference, "reference", "", "the person whose picture the others match (default: the one the recordings were lined up with)")
	f.BoolVar(&off, "off", false, "take the corrections away")
	f.StringVar(&project, "project", "", "the project whose cut gets them (default: the conversation's)")
	f.StringVar(&branch, "branch", "", "the branch (default main)")
	f.StringVar(&confirm, "confirm", "", "apply with the token from the check")
	f.BoolVar(&applyIt, "apply", false, "put them in the cut as a new version")
	f.BoolVar(&details, "details", false, "also each correction's values")
	return cmd
}

func convSettingsCmd() *cobra.Command {
	var project, branch, conv, quieter, nameTags, member, crop, confirm string
	var depth float64
	var noCrop, applyIt bool
	cmd := &cobra.Command{Use: "settings", Short: "How the cut plays a conversation: microphones quieter when their person isn't speaking, name tags, a person's crop",
		Long: `Examples:
  metarush conversation settings --project <id> --quieter false        # every microphone at its level all the time
  metarush conversation settings --project <id> --quieter true --depth 12
  metarush conversation settings --project <id> --name-tags false
  metarush conversation settings --project <id> --member sam --crop 0,0,0,0.1   # left,top,right,bottom
  metarush conversation settings --project <id> --member sam --no-crop --apply`,
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			in := conversation.CallSettingsIn{Project: project, Branch: branch, Conversation: conv, Member: member, NoCrop: noCrop, Apply: applyIt}
			for _, x := range []struct {
				s   string
				dst **bool
				nm  string
			}{{quieter, &in.Quieter, "--quieter"}, {nameTags, &in.NameTags, "--name-tags"}} {
				if x.s == "" {
					continue
				}
				v, err := strconv.ParseBool(x.s)
				if err != nil {
					return fail(c, apperr.InvalidInput, x.nm+" is true or false", "")
				}
				*x.dst = &v
			}
			if c.Flags().Changed("depth") {
				in.Depth = &depth
			}
			if crop != "" {
				parts := splitList(crop)
				if len(parts) != 4 {
					return fail(c, apperr.InvalidInput, "--crop is left,top,right,bottom (fractions, like 0,0,0,0.1)", "")
				}
				var v [4]float64
				for i, p := range parts {
					if v[i], err = strconv.ParseFloat(p, 64); err != nil {
						return fail(c, apperr.InvalidInput, "--crop is left,top,right,bottom (fractions, like 0,0,0,0.1)", "")
					}
				}
				in.Crop = &editspec.Crop{Left: v[0], Top: v[1], Right: v[2], Bottom: v[3]}
			}
			out, err := ops.CallTyped[conversation.CallPlanOut](c.Context(), a, "conversation.settings", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return planOutFlow(c, a, out, confirm)
		}}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "the project")
	f.StringVar(&branch, "branch", "", "the branch (default main)")
	f.StringVar(&conv, "conversation", "", "the conversation in the cut (default: its only one)")
	f.StringVar(&quieter, "quieter", "", "true: each microphone quieter while its person isn't speaking; false: always at its level")
	f.Float64Var(&depth, "depth", 0, "how much quieter, in dB (6–40; default 18)")
	f.StringVar(&nameTags, "name-tags", "", "true or false: names on the tiles of layouts")
	f.StringVar(&member, "member", "", "the person whose picture to crop")
	f.StringVar(&crop, "crop", "", "left,top,right,bottom: how much of each side to cut off (0–0.4)")
	f.BoolVar(&noCrop, "no-crop", false, "show their whole picture again")
	f.StringVar(&confirm, "confirm", "", "apply with the token from the check")
	f.BoolVar(&applyIt, "apply", false, "make it a new version of the cut")
	return cmd
}

func convImportCmd() *cobra.Command {
	var project, name string
	cmd := &cobra.Command{Use: "import <folder>", Short: "Bring in a call app's recording folder (Zoom, Riverside, SquadCast, or one recording per person) as a conversation", Args: cobra.ExactArgs(1),
		Long: `Zoom: record locally with "Record a separate audio file for each participant" on; import the meeting's folder
(Documents/Zoom/<date time topic>). Its files start together, so nothing has to be lined up.
Riverside: the downloaded tracks (riverside_<name>_raw-video-cfr_…). Other apps: a folder with one recording per person.
Examples:
  metarush conversation import "~/Documents/Zoom/2026-10-19 10.00.00 Garden Show" --project <id>
  metarush conversation import ~/Downloads/riverside-garden-show --project <id> --name "Garden show"`,
		RunE: func(c *cobra.Command, args []string) error {
			return runOp(c, "conversation.import", conversation.CallImportIn{ProjectID: project, Folder: expandHome(args[0]), Name: name}, func(o any) {
				out := o.(*conversation.CallOut)
				fmt.Println(out.Summary)
				printCall(out.Conversation, false)
			})
		}}
	cmd.Flags().StringVar(&project, "project", "", "the project the recordings go to")
	cmd.Flags().StringVar(&name, "name", "", "the conversation's name (default: the meeting's topic)")
	return cmd
}

func convDetectCmd() *cobra.Command {
	var files []string
	cmd := &cobra.Command{Use: "detect [folder]", Short: "Say whether a folder (or a list of files) is a call recording, who is in it and which file is whose", Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := conversation.CallDetectIn{}
			if len(args) == 1 {
				in.Folder = expandHome(args[0])
			}
			for _, f := range files {
				in.Files = append(in.Files, conversation.CallDetectFile{Name: f})
			}
			if in.Folder == "" && len(in.Files) == 0 {
				return fail(c, apperr.InvalidInput, "name a folder, or the files with --files", "")
			}
			return runOp(c, "conversation.detect", in, func(o any) {
				out := o.(*conversation.CallDetectOut)
				fmt.Println(out.Text)
				for _, m := range out.Members {
					switch {
					case m.Skip:
						fmt.Printf("  - %s: left out (%s)\n", m.File, m.Why)
					case m.Person != "":
						fmt.Printf("  • %s: %s (%s)\n", m.File, m.Person, map[string]string{"both": "picture and sound", "sound": "sound", "picture": "picture"}[m.Use])
					default:
						fmt.Printf("  • %s: %s\n", m.File, m.Why)
					}
				}
				for _, n := range out.Notes {
					fmt.Println("  · " + n)
				}
			})
		}}
	cmd.Flags().StringSliceVar(&files, "files", nil, "the files' names (with their folders) instead of a folder")
	return cmd
}

func convMomentsCmd() *cobra.Command {
	var project string
	var lo, hi float64
	var count int
	cmd := &cobra.Command{Use: "moments <conversation>", Short: "The best moments for shorts: complete stretches that start cleanly, and why each might work", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in := conversation.CallMomentsIn{CallRefIn: conversation.CallRefIn{Conversation: args[0], ProjectID: project}, Min: lo, Max: hi, Count: count}
			return runOp(c, "conversation.moments", in, func(o any) {
				out := o.(*conversation.CallMomentsOut)
				fmt.Println(out.Summary)
				for i, m := range out.Moments {
					fmt.Printf("%d. %s (%s) — %s\n   “%s”\n", i+1, m.When, secs(m.To-m.From), strings.Join(m.Why, "; "), clip(m.Text, 220))
				}
			})
		}}
	f := cmd.Flags()
	f.StringVar(&project, "project", "", "where to look for it by name")
	f.Float64Var(&lo, "min", 0, "shortest, in seconds (default 15)")
	f.Float64Var(&hi, "max", 0, "longest, in seconds (default 60)")
	f.IntVar(&count, "count", 0, "how many (default 6)")
	return cmd
}

func clip(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func convShortCmd() *cobra.Command {
	var project, from, to, name, shape, style string
	var nest, noCaptions, applyIt bool
	cmd := &cobra.Command{Use: "short <conversation>", Short: "Make a short from a stretch: a new project with a vertical cut of its own (or the episode's stretch nested)", Args: cobra.ExactArgs(1),
		Long: `Examples:
  metarush conversation short "Garden show" --from 0:25.7 --to 0:32.8            # what it would be
  metarush conversation short "Garden show" --from 0:25.7 --to 0:32.8 --apply
  metarush conversation short "Garden show" --from 0:25.7 --to 0:32.8 --nest --apply   # the episode's own stretch`,
		RunE: func(c *cobra.Command, args []string) error {
			f, ok1 := editspec.ParseClock(from)
			t, ok2 := editspec.ParseClock(to)
			if !ok1 || !ok2 {
				return fail(c, apperr.InvalidInput, "--from and --to are moments on the conversation's clock, like 0:25.7", "")
			}
			in := conversation.CallShortIn{CallRefIn: conversation.CallRefIn{Conversation: args[0], ProjectID: project}, From: f, To: t, Name: name, Shape: shape, Style: style, Nest: nest, Apply: applyIt}
			if noCaptions {
				v := false
				in.Captions = &v
			}
			return runOp(c, "conversation.short", in, func(o any) {
				out := o.(*conversation.CallShortOut)
				fmt.Println(out.Summary)
			})
		}}
	fl := cmd.Flags()
	fl.StringVar(&project, "project", "", "where to look for the conversation by name")
	fl.StringVar(&from, "from", "", "the start on the conversation's clock")
	fl.StringVar(&to, "to", "", "the end")
	fl.StringVar(&name, "name", "", "the new project's name")
	fl.StringVar(&shape, "shape", "", "vertical (default) | landscape | square")
	fl.StringVar(&style, "style", "", "podcast | interview | panel | reactions | broll")
	fl.BoolVar(&nest, "nest", false, "play the episode's own stretch as it is (same shape as the episode)")
	fl.BoolVar(&noCaptions, "no-captions", false, "no captions")
	fl.BoolVar(&applyIt, "apply", false, "make the project and its cut")
	return cmd
}

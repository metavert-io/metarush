package cli

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sound"
	"github.com/metavert-io/metarush/internal/voiceover"
)

func soundCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "sound", Short: "Sound: how loud each part and each person is, clearer voices, and music",
		Long: `Examples:
  metarush sound show --project <id>
  metarush sound levels --project <id> --speaker "Maya" --gain 3      # Maya 3 dB louder over her speech
  metarush sound levels --project <id> --part music --gain -4           # music 4 dB quieter
  metarush sound levels --project <id> --duck on --depth 14             # music gets 14 dB quieter when someone talks
  metarush sound levels --project <id> --preview --original --json      # hear every voice as recorded (A/B); saves nothing
  metarush sound enhance Maya --project <id> --mode clean --strength 0.8
  metarush sound enhance narration --project <id> --mode cloud --estimate   # price first; nothing is spent
  metarush sound compare --project <id>                                 # how much clearer, in plain words
  metarush sound music add song.m4a --project <id>                      # fits the video: starts with it, ends on a beat
  metarush sound music add song.m4a --project <id> --from 12 --to 70 --at 3
  metarush sound music set music --project <id> --level -16 --duck off
  metarush sound setup studio                                           # people only: about 1.8 GB
Changes are shown first and nothing is saved; add --apply, or --details for the command that saves them as a new version.`}
	cmd.AddCommand(soundShowCmd(), soundLevelsCmd(), soundEnhanceCmd(), soundCompareCmd(), soundMusicCmd(), soundSetupCmd())
	return cmd
}

func soundShowCmd() *cobra.Command {
	var rf revFlags
	var details bool
	cmd := &cobra.Command{Use: "show", Short: "The sound in plain words: levels, each person, clearer voices, automatic help and music",
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			rev, err := rf.resolve(c, a)
			if err != nil {
				return err
			}
			return runOpWith(c, a, "sound.show", voiceover.RevisionIn{RevisionID: rev}, ops.Caller{}, func(o any) { printSoundShow(o.(*sound.ShowOut), details) })
		}}
	rf.add(cmd)
	cmd.Flags().BoolVar(&details, "details", false, "also show the scores' numbers, track names and where each part comes from")
	return cmd
}

func printSoundShow(o *sound.ShowOut, details bool) {
	fmt.Println(o.Summary)
	fmt.Println()
	fmt.Println("Levels")
	for _, p := range o.Parts {
		if !p.Present && p.GainDB == 0 && !p.Mute && !p.Solo {
			continue
		}
		extra := ""
		if p.Solo {
			extra = " (only this plays)"
		}
		fmt.Printf("  %-18s %s%s\n", p.Label, p.Level, extra)
	}
	if len(o.People) > 0 {
		fmt.Println("People")
		for _, p := range o.People {
			fmt.Printf("  %-18s %s\n", p.Name, p.Level)
			if e := p.Enhance; e != nil {
				fmt.Println("    " + e.Words)
				if e.Score != nil && e.Score.Summary != "" {
					fmt.Println("    " + e.Score.Summary)
				}
				if details && e.Score != nil {
					printScoreNumbers("    ", *e.Score)
				}
			}
		}
	}
	if n := o.Narration; n != nil {
		fmt.Println("Narration")
		switch {
		case n.Enhance != nil:
			fmt.Println("  " + n.Enhance.Words)
			if n.Enhance.Score != nil && n.Enhance.Score.Summary != "" {
				fmt.Println("  " + n.Enhance.Score.Summary)
			}
		case n.Recorded > 0:
			fmt.Printf("  %s with your own recording, as recorded.\n", plural(n.Recorded, "line"))
		}
	}
	fmt.Println("Automatic help")
	fmt.Println("  " + o.Auto.Words)
	if o.Auto.Loudness == "auto" {
		fmt.Println("  Overall loudness: what the platform expects.")
	} else {
		fmt.Printf("  Overall loudness: %s.\n", o.Auto.Loudness)
	}
	if details {
		fmt.Printf("    target %g LUFS\n", o.Auto.Target)
	}
	if len(o.Music) > 0 {
		fmt.Println("Music")
		for _, m := range o.Music {
			fmt.Printf("  “%s”: %s\n", m.Title, m.Placement)
			if m.Music != "" {
				fmt.Println("    " + m.Music)
			}
			if !m.Rights {
				fmt.Println("    Nobody has said where it comes from yet (To-dos asks).")
			}
			if details {
				fmt.Printf("    track %s · %s · from %.2f s to %.2f s of the video · level %+.1f dB · quieter when someone talks: %s · tempo %.0f a minute\n",
					m.ID, m.Fit, m.From, m.To, m.GainDB, m.Duck, m.Tempo)
			}
		}
	}
	fmt.Println("Ways to make a voice clearer here")
	for _, m := range o.Modes {
		state := "Ready."
		if !m.Ready {
			state = m.Problem
		}
		fmt.Printf("  %s: %s\n", m.Label, state)
		fmt.Printf("    %s %s\n", m.Speed, strings.TrimSuffix(m.Cost, ".")+".")
	}
	if details && len(o.Explanations) > 0 {
		fmt.Println("What the mix does")
		for _, e := range o.Explanations {
			fmt.Println("  • " + e)
		}
	}
	for _, w := range o.Warnings {
		fmt.Println("! " + w.Message)
	}
}

func printScoreNumbers(indent string, s sound.PersonScore) {
	if s.Before.Clarity != nil && s.After.Clarity != nil {
		fmt.Printf("%sclarity (1–5): overall %.2f → %.2f, voice %.2f → %.2f, background %.2f → %.2f\n", indent,
			s.Before.Clarity.Overall, s.After.Clarity.Overall, s.Before.Clarity.Speech, s.After.Clarity.Speech, s.Before.Clarity.Background, s.After.Clarity.Background)
	}
	if s.Before.Noise != nil && s.After.Noise != nil {
		fmt.Printf("%svoice above the background: %.0f dB → %.0f dB\n", indent, s.Before.Noise.SNR, s.After.Noise.SNR)
	}
	if s.Likeness != nil {
		fmt.Printf("%sstill sounds like them: %.2f (voice fingerprints, 0–1)\n", indent, *s.Likeness)
	}
	fmt.Printf("%smeasured over %.1f s of speech (%s, %.0f%%)\n", indent, s.Seconds, s.Mode, s.Strength*100)
}

// soundPart reads a part of the mix the way people name it (sound.Part: the
// Claude Code panel's cards read it the same way).
func soundPart(v string) (string, bool) { return sound.Part(v) }

func onOffFlag(c *cobra.Command, name, v string) (*bool, error) {
	if !c.Flags().Changed(name) {
		return nil, nil
	}
	switch strings.ToLower(v) {
	case "on", "yes", "true":
		t := true
		return &t, nil
	case "off", "no", "false":
		f := false
		return &f, nil
	}
	return nil, fail(c, apperr.InvalidInput, fmt.Sprintf("--%s takes on or off, not %q", name, v), "")
}

func soundLevelsCmd() *cobra.Command {
	var rf revFlags
	var part, speaker, track, clip, cameraSound, duck, levelSpeech, smoothCuts, loudness, trackDuck, confirm string
	var gain, depth float64
	var mute, unmute, solo, unsolo, preview, original, applyIt bool
	cmd := &cobra.Command{Use: "levels", Short: "Change how loud things are: a part (camera sound, narration, music, effects), one person over their speech, a music track, and the automatic help",
		Long: `Examples:
  metarush sound levels --revision <id> --speaker "Maya" --gain 3    # louder over her speech only
  metarush sound levels --revision <id> --part narration --gain -2
  metarush sound levels --revision <id> --part music --mute
  metarush sound levels --revision <id> --track music --gain -16 --track-duck off
  metarush sound levels --revision <id> --camera-sound off --clip broll  # one clip without its own sound
  metarush sound levels --revision <id> --duck on --depth 14 --level-speech on --smooth-cuts on --loudness -14
  metarush sound levels --revision <id> --part music --gain -6 --preview --json   # the preview plan; saves nothing
  metarush sound levels --revision <id> --preview --original --json     # every voice as recorded (A/B)
Shows the change and saves nothing; --details shows the command that saves it as a new version (or add --apply).`,
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			rev, err := rf.resolve(c, a)
			if err != nil {
				return err
			}
			in := sound.LevelsIn{RevisionID: rev, Original: original, Apply: applyIt}
			gainSet := c.Flags().Changed("gain")
			targets := 0
			for _, s := range []string{part, speaker, track} {
				if s != "" {
					targets++
				}
			}
			if gainSet && targets != 1 {
				return fail(c, apperr.InvalidInput, "--gain needs exactly one of --part, --speaker or --track", "")
			}
			g := gain
			if part != "" {
				bus, ok := soundPart(part)
				if !ok {
					return fail(c, apperr.InvalidInput, fmt.Sprintf("there is no part called %q (camera, narration, music or effects)", part), "")
				}
				bc := voiceover.BusChange{Bus: bus}
				if gainSet {
					bc.GainDB = &g
				}
				switch {
				case mute:
					bc.Mute = boolFlag(true)
				case unmute:
					bc.Mute = boolFlag(false)
				}
				switch {
				case solo:
					bc.Solo = boolFlag(true)
				case unsolo:
					bc.Solo = boolFlag(false)
				}
				in.Parts = []voiceover.BusChange{bc}
			} else if mute || unmute || solo || unsolo {
				return fail(c, apperr.InvalidInput, "--mute, --unmute, --solo and --unsolo go with --part", "")
			}
			if speaker != "" {
				if !gainSet {
					return fail(c, apperr.InvalidInput, "say how much louder or quieter with --gain (dB; 0 = as recorded)", "")
				}
				in.People = []sound.PersonLevel{{Name: speaker, GainDB: g}}
			}
			td, err := func() (string, error) {
				if !c.Flags().Changed("track-duck") {
					return "", nil
				}
				switch v := strings.ToLower(trackDuck); v {
				case "on", "off", "auto":
					return v, nil
				}
				return "", fail(c, apperr.InvalidInput, "--track-duck takes on, off or auto", "")
			}()
			if err != nil {
				return err
			}
			if track != "" {
				ml := sound.MusicLevel{Track: track, Duck: td}
				if gainSet {
					ml.GainDB = &g
				}
				in.Music = []sound.MusicLevel{ml}
			} else if td != "" {
				return fail(c, apperr.InvalidInput, "--track-duck goes with --track", "")
			}
			cs, err := onOffFlag(c, "camera-sound", cameraSound)
			if err != nil {
				return err
			}
			switch {
			case cs != nil && clip != "":
				in.Clips = []voiceover.ClipSound{{Clip: clip, Muted: !*cs}}
			case cs != nil:
				m := !*cs
				in.MuteSource = &m
			case clip != "":
				return fail(c, apperr.InvalidInput, "--clip goes with --camera-sound on|off", "")
			}
			if in.Ducking, err = onOffFlag(c, "duck", duck); err != nil {
				return err
			}
			if c.Flags().Changed("depth") {
				d := math.Abs(depth)
				in.Depth = &d
			}
			if in.LevelSpeech, err = onOffFlag(c, "level-speech", levelSpeech); err != nil {
				return err
			}
			if in.SmoothCuts, err = onOffFlag(c, "smooth-cuts", smoothCuts); err != nil {
				return err
			}
			in.Loudness = loudness
			if preview || original {
				return runOpWith(c, a, "sound.preview", in, ops.Caller{}, func(o any) {
					p := o.(*sound.PreviewOut)
					fmt.Println(p.Summary)
					for _, ch := range p.Changes {
						fmt.Println("  • " + ch)
					}
				})
			}
			out, err := ops.CallTyped[sound.LevelsOut](c.Context(), a, "sound.levels", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if confirm != "" && out.Result != nil {
				return planFlow(c, a, out.Result.Plan, confirm, map[string]any{"changes": out.Changes}, func() { fmt.Println(out.Summary) })
			}
			return resultFlow(c, a, out, pending(out.Result, out.Applied), func() {
				fmt.Println(out.Summary)
				for _, ch := range out.Changes {
					fmt.Println("  • " + ch)
				}
			})
		}}
	rf.add(cmd)
	f := cmd.Flags()
	f.StringVar(&part, "part", "", "camera (the clips' own sound) | narration | music | effects")
	f.StringVar(&speaker, "speaker", "", "one person, by name as the speakers show it: with --gain, louder or quieter over their speech only")
	f.StringVar(&track, "track", "", "one music track (its name or file title): with --gain and/or --track-duck")
	f.Float64Var(&gain, "gain", 0, "level change in dB for --part, --speaker or --track (negative = quieter, 0 = as recorded)")
	f.BoolVar(&mute, "mute", false, "with --part: silence it")
	f.BoolVar(&unmute, "unmute", false, "with --part: hear it again")
	f.BoolVar(&solo, "solo", false, "with --part: hear only this (and other soloed parts)")
	f.BoolVar(&unsolo, "unsolo", false, "with --part: stop hearing only this")
	f.StringVar(&trackDuck, "track-duck", "", "with --track: on (quieter when someone talks) | off (never) | auto (as the mix decides)")
	f.StringVar(&cameraSound, "camera-sound", "", "on | off: the clips' own sound for the whole video (or with --clip, one clip)")
	f.StringVar(&clip, "clip", "", "with --camera-sound: only this clip (ID or label)")
	f.StringVar(&duck, "duck", "", "on | off: music gets quieter when someone talks")
	f.Float64Var(&depth, "depth", 0, "how much quieter the music gets while someone talks (dB, e.g. 14)")
	f.StringVar(&levelSpeech, "level-speech", "", "on | off: even out voices recorded on different devices")
	f.StringVar(&smoothCuts, "smooth-cuts", "", "on | off: blend the sound for a moment at each cut so it never clicks")
	f.StringVar(&loudness, "loudness", "", "overall loudness: auto (what the platform expects) or a target such as -14 (social), -16 (web), -23 (broadcast)")
	f.BoolVar(&preview, "preview", false, "how the cut would sound with the change (the preview plan with --json); nothing is saved")
	f.BoolVar(&original, "original", false, "preview every voice as recorded, without the clearer versions (A/B); nothing is saved")
	f.StringVar(&confirm, "confirm", "", "apply with the token from the check")
	f.BoolVar(&applyIt, "apply", false, "also put it in the cut as a new version (no separate edit apply)")
	return cmd
}

func soundEnhanceCmd() *cobra.Command {
	var rf revFlags
	var mode, confirm string
	var strength float64
	var estimate, off, on, remove, applyIt bool
	cmd := &cobra.Command{Use: "enhance <person|narration|everyone|recording>", Short: "Make a voice clearer: one person over their speech, the narration, a recording or everyone (the recordings are kept)",
		Long: `Ways to make a voice clearer:
  clean   on this computer, fast and free (the default; set up on first use, about 30 MB)
  studio  on this computer, slower and stronger: it rebuilds the voice (set up once: metarush sound setup studio)
  cloud   ElevenLabs voice isolation, 1,000 credits a minute of speech (priced first, within the project budget)

Examples:
  metarush sound enhance Maya --revision <id>                       # clean, 80%
  metarush sound enhance "Speaker 2" --revision <id> --mode studio
  metarush sound enhance narration --revision <id> --mode cloud --estimate
  metarush sound enhance everyone --revision <id> --strength 0.6
  metarush sound enhance Maya --revision <id> --off                  # A/B: hear the original again (the clearer one is kept)
  metarush sound enhance Maya --revision <id> --remove
Clearer voices are kept: the same recording with the same settings is never processed (or paid for) twice.
Shows the change and saves nothing; --details shows the command that saves it as a new version (or add --apply).`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			rev, err := rf.resolve(c, a)
			if err != nil {
				return err
			}
			in := sound.EnhanceIn{RevisionID: rev, Target: args[0], Mode: strings.ToLower(mode), Remove: remove, Estimate: estimate, Apply: applyIt}
			if c.Flags().Changed("strength") {
				s := strength
				if s > 1 && s <= 100 { // "80" means 80%
					s /= 100
				}
				in.Strength = &s
			}
			switch {
			case off && on:
				return fail(c, apperr.InvalidInput, "--off and --on can't go together", "")
			case off:
				in.Off = boolFlag(true)
			case on:
				in.Off = boolFlag(false)
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			// The cloud's estimate first (D208): a person at a terminal sees what
			// it costs and says yes before anything is sent.
			if !estimate && in.Mode == "cloud" && estimateFirstHere(c, a) {
				probe := in
				probe.Estimate, probe.Apply = true, false
				e, err := ops.CallTyped[sound.EnhanceOut](ctx, a, "sound.enhance", probe, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				if e.Estimate.Allowed && e.Estimate.Cost != nil && e.Estimate.Credits > 0 {
					fmt.Println(e.Summary)
					printCost(*e.Estimate.Cost, detailsMode(c))
					if !askYes("Make it clearer for "+e.Estimate.Cost.About, false) {
						return fail(c, apperr.Canceled, "Nothing was sent or spent.", "")
					}
				}
			}
			if !estimate && !jsonMode(c) {
				fmt.Fprintln(os.Stderr, "Making the voice clearer on this computer…")
			}
			out, err := ops.CallTyped[sound.EnhanceOut](ctx, a, "sound.enhance", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			human := func() { printEnhance(out, detailsMode(c)) }
			if estimate {
				return emit(c, out, human)
			}
			if confirm != "" && out.Result != nil {
				return planFlow(c, a, out.Result.Plan, confirm, map[string]any{"enhanced": out}, human)
			}
			return resultFlow(c, a, out, pending(out.Result, out.Applied), human)
		}}
	rf.add(cmd)
	f := cmd.Flags()
	f.StringVar(&mode, "mode", "", "clean (default) | studio | cloud")
	f.Float64Var(&strength, "strength", 0, "how much of the clearer voice is heard, 0–1 (default 0.8; studio 1)")
	f.BoolVar(&estimate, "estimate", false, "only say what it takes: how much speech, about how long, and for cloud the credits; nothing is made or spent")
	f.BoolVar(&off, "off", false, "A/B: the original voice plays again (the clearer one and the settings are kept)")
	f.BoolVar(&on, "on", false, "A/B: the clearer voice plays again")
	f.BoolVar(&remove, "remove", false, "no clearer voice for them any more (the original plays; a level change stays)")
	f.StringVar(&confirm, "confirm", "", "apply with the token from the check")
	f.BoolVar(&applyIt, "apply", false, "also put it in the cut as a new version (no separate edit apply)")
	return cmd
}

func printEnhance(o *sound.EnhanceOut, details bool) {
	fmt.Println(o.Summary)
	if o.Estimate.Problem != "" && !strings.Contains(o.Summary, o.Estimate.Problem) {
		fmt.Println("  ! " + o.Estimate.Problem)
	}
	for _, w := range o.Warnings {
		fmt.Println("  ! " + w)
	}
	if o.Estimate.Cost != nil && len(o.Copies) == 0 {
		printCost(*o.Estimate.Cost, details) // only the estimate (nothing made yet)
	}
}

func soundCompareCmd() *cobra.Command {
	var rf revFlags
	var details bool
	cmd := &cobra.Command{Use: "compare [person|narration]", Short: "How much clearer the voices are: the voice clarity score and the background, before and after, with warnings when it went too far",
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			rev, err := rf.resolve(c, a)
			if err != nil {
				return err
			}
			in := sound.CompareIn{RevisionID: rev}
			if len(args) == 1 {
				in.Target = args[0]
			}
			return runOpWith(c, a, "sound.compare", in, ops.Caller{}, func(o any) {
				out := o.(*sound.CompareOut)
				if len(out.Scores) == 0 {
					fmt.Println(out.Summary)
					return
				}
				for _, s := range out.Scores {
					name := s.Speaker
					if name == "" {
						name = "The voice"
					}
					fmt.Printf("%s: %s\n", name, s.Summary)
					for _, w := range s.Warnings {
						fmt.Println("  ! " + w)
					}
					if details {
						printScoreNumbers("  ", s)
					}
				}
			})
		}}
	rf.add(cmd)
	cmd.Flags().BoolVar(&details, "details", false, "also show the numbers behind the words")
	return cmd
}

func soundMusicCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "music", Short: "Put music under a cut and change how it plays"}
	cmd.AddCommand(soundMusicAddCmd(), soundMusicSetCmd())
	return cmd
}

// musicTiming holds the timing flags music add and music set share.
type musicTiming struct {
	fit, duck, ending    string
	from, to, at, level  float64
	duckDepth            float64
	loop, noLoop, remove bool
}

func (m *musicTiming) add(c *cobra.Command, set bool) {
	f := c.Flags()
	f.StringVar(&m.fit, "fit", "", "auto: starts with the video and ends with it where the music starts its next phrase, with a smooth fade | manual: --from/--to of the track, starting --at")
	f.StringVar(&m.ending, "ending", "", "with --fit auto: phrase (default: where the music starts its next phrase, else a bar, else a beat) | beat (on a beat)")
	f.Float64Var(&m.from, "from", 0, "where to start in the track (seconds)")
	f.Float64Var(&m.to, "to", 0, "where to stop in the track (seconds; default its end)")
	f.Float64Var(&m.at, "at", 0, "where it starts in the video (seconds)")
	f.Float64Var(&m.level, "level", 0, "its level in dB (default -12: a bed under voices)")
	f.Float64Var(&m.duckDepth, "duck-depth", 0, "how much quieter while someone talks (dB, e.g. 12)")
	f.BoolVar(&m.loop, "loop", false, "repeat a shorter track until the video ends (with --fit auto)")
	if set {
		f.StringVar(&m.duck, "duck", "", "on: quieter while someone talks | off: never | auto: as the mix decides")
		f.BoolVar(&m.noLoop, "no-loop", false, "stop repeating it")
		f.BoolVar(&m.remove, "remove", false, "take the music out of the cut")
	} else {
		f.StringVar(&m.duck, "duck", "", "on (default): quieter while someone talks | off: never")
	}
}

func floatFlag(c *cobra.Command, name string, v float64) *float64 {
	if c.Flags().Changed(name) {
		return &v
	}
	return nil
}

func soundMusicAddCmd() *cobra.Command {
	var rf revFlags
	var mt musicTiming
	var id, confirm string
	var applyIt bool
	cmd := &cobra.Command{Use: "add <file|resource>", Short: "Put music under the cut: a file on this computer or audio already in the workspace (its title or ID)",
		Long: `By default the music fits the video: it starts with it and ends with it where the music starts its next phrase
(else at the end of a bar, else on a beat) near the end, with a smooth fade (--ending beat: on a beat). The whole
track is listened to: its tempo may change, and its bars and phrases are found.
A shorter track fades out early (or repeats with --loop). --from, --to and --at place it by hand.

Examples:
  metarush sound music add ~/Music/bed.m4a --revision <id>
  metarush sound music add "Upbeat bed" --revision <id> --loop
  metarush sound music add bed.m4a --revision <id> --from 12 --to 70 --at 3 --level -16 --duck off
Where the music comes from is asked in To-dos once it is in the cut.
Shows the change and saves nothing; --details shows the command that saves it as a new version (or add --apply).`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			rev, err := rf.resolve(c, a)
			if err != nil {
				return err
			}
			asset := args[0]
			if st, err := os.Stat(asset); err == nil && !st.IsDir() {
				r, err := getRev(c, a, rev)
				if err != nil {
					return err
				}
				if asset, err = importMusic(c, a, r.ProjectID, args[0]); err != nil {
					return err
				}
			}
			in := sound.MusicAddIn{RevisionID: rev, Asset: asset, Fit: strings.ToLower(mt.fit), From: floatFlag(c, "from", mt.from), To: floatFlag(c, "to", mt.to),
				At: floatFlag(c, "at", mt.at), GainDB: floatFlag(c, "level", mt.level), DuckDB: floatFlag(c, "duck-depth", mt.duckDepth), Loop: mt.loop, Ending: strings.ToLower(mt.ending), ID: id, Apply: applyIt}
			if in.Duck, err = onOffFlag(c, "duck", mt.duck); err != nil {
				return err
			}
			if in.DuckDB != nil {
				d := -math.Abs(*in.DuckDB)
				in.DuckDB = &d
			}
			if !jsonMode(c) {
				fmt.Fprintln(os.Stderr, "Listening for the beat…")
			}
			out, err := ops.CallTyped[sound.MusicOut](c.Context(), a, "sound.music.add", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			human := func() { printMusic(out) }
			if confirm != "" && out.Result != nil {
				return planFlow(c, a, out.Result.Plan, confirm, map[string]any{"music": out}, human)
			}
			return resultFlow(c, a, out, pending(out.Result, out.Applied), human)
		}}
	rf.add(cmd)
	mt.add(cmd, false)
	cmd.Flags().StringVar(&id, "id", "", "a name for the track (default music, music-2…)")
	cmd.Flags().StringVar(&confirm, "confirm", "", "apply with the token from the check")
	cmd.Flags().BoolVar(&applyIt, "apply", false, "also put it in the cut as a new version (no separate edit apply)")
	return cmd
}

func printMusic(o *sound.MusicOut) {
	fmt.Println(o.Summary)
	switch {
	case o.Music != "":
		fmt.Println("  " + o.Music)
	case o.Tempo > 0:
		fmt.Printf("  About %.0f beats a minute.\n", o.Tempo)
	}
	if o.Rights != "" {
		fmt.Println("  ! " + o.Rights)
	}
}

// importMusic stores a local music file in the workspace (no transcript) and
// returns its asset ID (the existing one when the same file is there already).
func importMusic(c *cobra.Command, a *app.App, projectID, path string) (string, error) {
	f := false
	b, _ := json.Marshal(ingest.FilesIn{Paths: []string{path}, ProjectID: projectID, Tags: []string{"music"}, Transcribe: &f})
	res, err := ops.Call(c.Context(), a, "ingest.files", b, ops.Caller{})
	if err != nil {
		return "", failErr(c, err)
	}
	fo := res.(*ingest.FilesOut)
	if len(fo.Results) == 0 {
		return "", fail(c, apperr.UnsupportedMedia, "The music file couldn't be added.", "")
	}
	r := fo.Results[0]
	switch {
	case r.AssetID != "":
		return r.AssetID, nil
	case r.Duplicate != nil && r.Duplicate.AssetID != "":
		return r.Duplicate.AssetID, nil
	}
	ae := apperr.New(apperr.UnsupportedMedia, "The music file couldn't be added.")
	if r.Error != "" {
		ae = ae.WithDetails(map[string]any{"error": r.Error})
	}
	return "", failErr(c, ae)
}

func soundMusicSetCmd() *cobra.Command {
	var rf revFlags
	var mt musicTiming
	var confirm string
	var applyIt bool
	cmd := &cobra.Command{Use: "set <track>", Short: "Change a music track: fit it to the video or time it by hand, its level, getting quieter when someone talks, looping, or take it out",
		Long: `Examples:
  metarush sound music set music --revision <id> --fit auto            # starts and ends with the video again
  metarush sound music set music --revision <id> --from 30 --at 0      # by hand from 0:30 of the track
  metarush sound music set music --revision <id> --level -18 --duck on --duck-depth 10
  metarush sound music set music --revision <id> --remove
Shows the change and saves nothing; --details shows the command that saves it as a new version (or add --apply).`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			rev, err := rf.resolve(c, a)
			if err != nil {
				return err
			}
			in := sound.MusicSetIn{RevisionID: rev, Track: args[0], Fit: strings.ToLower(mt.fit), From: floatFlag(c, "from", mt.from), To: floatFlag(c, "to", mt.to),
				At: floatFlag(c, "at", mt.at), GainDB: floatFlag(c, "level", mt.level), DuckDB: floatFlag(c, "duck-depth", mt.duckDepth), Ending: strings.ToLower(mt.ending), Remove: mt.remove, Apply: applyIt}
			if in.DuckDB != nil {
				d := -math.Abs(*in.DuckDB)
				in.DuckDB = &d
			}
			if c.Flags().Changed("duck") {
				switch v := strings.ToLower(mt.duck); v {
				case "on", "off", "auto":
					in.Duck = v
				default:
					return fail(c, apperr.InvalidInput, "--duck takes on, off or auto", "")
				}
			}
			switch {
			case mt.loop && mt.noLoop:
				return fail(c, apperr.InvalidInput, "--loop and --no-loop can't go together", "")
			case mt.loop:
				in.Loop = boolFlag(true)
			case mt.noLoop:
				in.Loop = boolFlag(false)
			}
			out, err := ops.CallTyped[sound.MusicOut](c.Context(), a, "sound.music.set", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			human := func() { printMusic(out) }
			if confirm != "" && out.Result != nil {
				return planFlow(c, a, out.Result.Plan, confirm, map[string]any{"music": out}, human)
			}
			return resultFlow(c, a, out, pending(out.Result, out.Applied), human)
		}}
	rf.add(cmd)
	mt.add(cmd, true)
	cmd.Flags().StringVar(&confirm, "confirm", "", "apply with the token from the check")
	cmd.Flags().BoolVar(&applyIt, "apply", false, "also put it in the cut as a new version (no separate edit apply)")
	return cmd
}

func soundSetupCmd() *cobra.Command {
	var details bool
	cmd := &cobra.Command{Use: "setup [clean] [studio]", Short: "Set up clearer voices on this computer: clean (about 30 MB, also set up on first use) or studio (about 1.8 GB) — people only",
		Long: `clean   the voice cleaner and the voice clarity score (about 30 MB; pinned downloads, checked before use)
studio  studio quality: a Python environment with PyTorch and a voice model (about 1.8 GB). On a Mac it uses
        the graphics chip; it needs Python 3.12–3.14 (Homebrew's is installed when there is none).
Nothing leaves this computer when voices are made clearer in either mode.`,
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			in := sound.SetupIn{Components: args}
			if !jsonMode(c) {
				fmt.Fprintln(os.Stderr, "Setting up clearer voices on this computer (this can take a while)…")
			}
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			b, _ := json.Marshal(in)
			res, err := ops.Call(ctx, a, "sound.setup", b, ops.Caller{Human: interactive()})
			if err != nil {
				return failErr(c, err)
			}
			out := res.(*sound.SetupOut)
			return emit(c, out, func() {
				fmt.Println(out.Summary)
				for _, st := range out.Components {
					state := "ready"
					if !st.Installed {
						state = st.Problem
					}
					line := fmt.Sprintf("  %-34s %s", st.Title, state)
					if st.Installed && st.Speed > 0 {
						line += fmt.Sprintf(" (about %s seconds of work per second of speech)", strconv.FormatFloat(st.Speed, 'f', 1, 64))
					}
					fmt.Println(line)
					if details {
						fmt.Printf("    %s · %s\n", st.Version, st.Path)
					}
				}
			})
		}}
	cmd.Flags().BoolVar(&details, "details", false, "also show versions and where each part lives")
	return cmd
}

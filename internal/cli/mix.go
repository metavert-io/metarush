package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/voiceover"
)

func mixCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "mix", Short: "See and change the sound mix: clip sound, narration, music and effects"}
	var rf revFlags
	show := &cobra.Command{Use: "show", Short: "The mix in plain words: levels, what is muted, when music gets quieter, loudness",
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
			return runOpWith(c, a, "mix.show", voiceover.RevisionIn{RevisionID: rev}, ops.Caller{}, func(o any) {
				m := o.(*voiceover.MixOut)
				for _, b := range m.Buses {
					extra := ""
					if b.Solo {
						extra = " (solo)"
					}
					fmt.Printf("%-22s %s%s\n", b.Label, b.Level, extra)
				}
				for _, e := range m.Explanations {
					fmt.Println("• " + e)
				}
			})
		}}
	rf.add(show)
	cmd.AddCommand(show)
	cmd.AddCommand(mixSetCmd())
	return cmd
}

func mixSetCmd() *cobra.Command {
	var rf revFlags
	var bus, clip, ducking, normalize, levelSpeech, confirm string
	var gain, musicVO, sourceVO, musicDlg, loudness, ceiling, cutBlend float64
	var mute, unmute, solo, unsolo, muteSource, unmuteSource, reset, loudnessAuto, applyIt, preview bool
	cmd := &cobra.Command{Use: "set", Short: "Change levels, mute or solo a part, turn off the clips' own sound, or change how the music dips under voices and how loud it sounds",
		Long: `Examples:
  metarush mix set --revision <id> --bus music --gain -4        # music 4 dB quieter
  metarush mix set --revision <id> --mute-source                # no sound from the clips at all
  metarush mix set --revision <id> --mute-source --clip broll   # silence one clip's own sound
  metarush mix set --revision <id> --bus voiceover --solo       # hear only the narration
  metarush mix set --revision <id> --music-under-voice -18      # music drops further under narration
  metarush mix set --revision <id> --level-speech on            # even out voices recorded on different devices
  metarush mix set --revision <id> --cut-blend 0.1              # blend the sound at straight cuts (no clicks)
  metarush mix set --revision <id> --bus music --gain -6 --preview --json   # how it would sound (the preview plan); saves nothing
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
			num := func(name string, v float64) *float64 {
				if c.Flags().Changed(name) {
					return &v
				}
				return nil
			}
			onOff := func(name, v string) *bool {
				if !c.Flags().Changed(name) {
					return nil
				}
				on := v == "on" || v == "true" || v == "yes"
				return &on
			}
			in := voiceover.MixSetIn{RevisionID: rev, Reset: reset, Ducking: onOff("ducking", ducking), Normalize: onOff("normalize", normalize),
				MusicUnderVoiceover: num("music-under-voice", musicVO), SourceUnderVoiceover: num("source-under-voice", sourceVO), MusicUnderDialogue: num("music-under-dialogue", musicDlg),
				TargetLUFS: num("loudness", loudness), LoudnessAuto: loudnessAuto, TruePeakDBTP: num("peak-ceiling", ceiling),
				LevelSpeech: onOff("level-speech", levelSpeech), CutBlend: num("cut-blend", cutBlend), Apply: applyIt}
			if bus != "" {
				bc := voiceover.BusChange{Bus: bus, GainDB: num("gain", gain)}
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
				in.Buses = []voiceover.BusChange{bc}
			}
			switch {
			case clip != "" && (muteSource || unmuteSource):
				in.Clips = []voiceover.ClipSound{{Clip: clip, Muted: muteSource}}
			case muteSource:
				in.MuteSource = boolFlag(true)
			case unmuteSource:
				in.MuteSource = boolFlag(false)
			}
			if preview {
				return runOpWith(c, a, "mix.preview", in, ops.Caller{}, func(o any) {
					p := o.(*voiceover.MixPreviewOut)
					fmt.Println(p.Summary)
					for _, b := range p.Mix.Buses {
						fmt.Printf("  %-22s %s\n", b.Label, b.Level)
					}
					for _, e := range p.Mix.Explanations {
						fmt.Println("  • " + e)
					}
				})
			}
			out, err := ops.CallTyped[voiceover.MixSetOut](c.Context(), a, "mix.set", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if applyIt || out.Result == nil {
				return emit(c, out, func() { fmt.Println(out.Summary) })
			}
			return planFlow(c, a, out.Result.Plan, confirm, map[string]any{"changes": out.Changes}, nil)
		}}
	rf.add(cmd)
	f := cmd.Flags()
	f.StringVar(&bus, "bus", "", "source (the clips' own sound) | voiceover | music | sfx")
	f.Float64Var(&gain, "gain", 0, "with --bus: level change in dB (negative = quieter)")
	f.BoolVar(&mute, "mute", false, "with --bus: mute it")
	f.BoolVar(&unmute, "unmute", false, "with --bus: unmute it")
	f.BoolVar(&solo, "solo", false, "with --bus: hear only this (and other soloed parts)")
	f.BoolVar(&unsolo, "unsolo", false, "with --bus: stop soloing it")
	f.BoolVar(&muteSource, "mute-source", false, "turn off the clips' own sound (all clips, or --clip)")
	f.BoolVar(&unmuteSource, "unmute-source", false, "turn the clips' own sound back on (all clips, or --clip)")
	f.StringVar(&clip, "clip", "", "with --mute-source/--unmute-source: only this clip (ID or label)")
	f.StringVar(&ducking, "ducking", "", "on | off: music and clip sound get quieter automatically under narration and speech")
	f.Float64Var(&musicVO, "music-under-voice", 0, "how much quieter music gets under narration (dB, e.g. -14)")
	f.Float64Var(&sourceVO, "source-under-voice", 0, "how much quieter the clips' sound gets under narration (dB, e.g. -18)")
	f.Float64Var(&musicDlg, "music-under-dialogue", 0, "how much quieter music gets under on-camera speech (dB, e.g. -12)")
	f.Float64Var(&loudness, "loudness", 0, "how loud it sounds overall, in LUFS (e.g. -14 for social video)")
	f.StringVar(&normalize, "normalize", "", "on | off: set the overall level to the loudness target")
	f.Float64Var(&ceiling, "peak-ceiling", 0, "the loudest the peaks may get (dBTP, e.g. -1; lower leaves more room against distortion)")
	f.StringVar(&levelSpeech, "level-speech", "", "on | off: even out how loud people sound from clip to clip")
	f.Float64Var(&cutBlend, "cut-blend", 0, "seconds the sound blends at straight cuts so they never click (0 = sharp, 0.1 for new cuts)")
	f.BoolVar(&reset, "reset", false, "remove the mix settings (levels, ducking, loudness); speech leveling and cut blends stay")
	f.BoolVar(&loudnessAuto, "loudness-auto", false, "use the usual loudness of the cut's platform again")
	f.StringVar(&confirm, "confirm", "", "apply with the token from the check")
	f.BoolVar(&applyIt, "apply", false, "also put it in the cut as a new version (no separate edit apply)")
	f.BoolVar(&preview, "preview", false, "show how the cut would sound with the change (the preview plan with --json); nothing is saved")
	return cmd
}

func boolFlag(v bool) *bool { return &v }

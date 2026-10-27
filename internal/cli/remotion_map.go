package cli

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/remotion"
)

// remotion map (D167): the project map of a version — scenes, words with where
// they are written, pictures, sounds, fonts, timing data — made in the render
// jail the first time (this command waits for it unless --no-wait).

func remotionMapCmd() *cobra.Command {
	var in remotion.RemotionMapIn
	var noWait, details bool
	c := &cobra.Command{
		Use:   "map <name[@version]> [--composition X] [--again] [--no-wait] [--stills]",
		Short: "The project map of a Remotion source: each composition's scenes with their times and what sets their lengths, the words on screen and where they are written, pictures, sounds and fonts (made with no network the first time)",
		Long: `The project map of a version of a Remotion source (the newest approved one, else the newest). For each
composition: its scenes (where each starts and ends, worked out while its code runs, so lengths computed from
data are exact) and what sets each scene's length, the words on screen with the file and line they are written
on (--details), the pictures from its public/ folder, the sounds it plays (when, how loud, whether they dip
under a voice), the fonts and the data files that set its timing, and a picture of each scene.

The first time, MetaRush makes it in its sandboxed browser with no network (a minute or two); this command
waits for it unless --no-wait. --json gives everything; --stills adds each scene's picture as a file to look at.`,
		Example: "  metarush remotion map teaser --details\n  metarush remotion map teaser --composition Intro --json --stills",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			out, err := ops.CallTyped[remotion.RemotionMapOut](ctx, a, "remotion.map", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if out.State == models.RemotionMapMaking && !noWait && out.JobID != "" {
				if !jsonMode(c) {
					fmt.Println(out.Message)
				}
				if _, err := waitJobs(c, a, []string{out.JobID}, 2*time.Hour, jsonMode(c)); err != nil {
					return failErr(c, err)
				}
				again := in
				again.Again, again.NoMake = false, true
				if out, err = ops.CallTyped[remotion.RemotionMapOut](ctx, a, "remotion.map", again, ops.Caller{}); err != nil {
					return failErr(c, err)
				}
			}
			return emit(c, out, func() { printRemotionMap(out, details) })
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	c.Flags().StringVar(&in.Composition, "composition", "", "only this composition")
	c.Flags().BoolVar(&in.Again, "again", false, "make the map again")
	c.Flags().BoolVar(&noWait, "no-wait", false, "don't wait while it is being made")
	c.Flags().BoolVar(&in.Paths, "stills", false, "give each scene's picture as a file on this computer")
	addDetails(c, &details)
	return c
}

func printRemotionMap(r *remotion.RemotionMapOut, details bool) {
	fmt.Println(r.Message)
	if r.Map == nil {
		if details && r.JobID != "" {
			fmt.Println("  job:", r.JobID)
		}
		return
	}
	at := func(file string, line int) string {
		if !details || file == "" {
			return ""
		}
		if line > 0 {
			return fmt.Sprintf(" (%s:%d)", file, line)
		}
		return " (" + file + ")"
	}
	for _, c := range r.Map.Compositions {
		fmt.Printf("\n%s — %s, %d×%d", theirs(c.ID), fmtDur(c.Duration), c.Width, c.Height)
		if c.Length.Computed {
			fmt.Print(", its length worked out in code")
		}
		fmt.Println(at(c.Length.File, c.Length.Line))
		if c.Problem != "" {
			fmt.Println("  " + c.Problem)
		}
		for _, sc := range c.Scenes {
			line := fmt.Sprintf("  %d. %s  %s–%s", sc.N, theirs(sc.Name), fmtDur(sc.From), fmtDur(sc.To))
			switch {
			case sc.Length.Computed && len(sc.Length.Clues) > 0:
				line += ", its length worked out from " + clueWords(sc.Length.Clues, details)
			case sc.Length.Computed:
				line += ", its length worked out in code"
			case sc.Length.File != "":
				line += ", its length written in the code" + at(sc.Length.File, sc.Length.Line)
			}
			if sc.Length.Overlap > 0 {
				line += fmt.Sprintf(", blending into the next for %s", fmtDur(sc.Length.Overlap))
			}
			fmt.Println(line)
			if details && sc.File != "" {
				fmt.Printf("     code: %s%s\n", sc.File, map[bool]string{true: fmt.Sprintf(" line %d", sc.Line), false: ""}[sc.Line > 0])
			}
			var ws []string
			for i, w := range sc.Words {
				if !details && i >= 4 {
					ws = append(ws, fmt.Sprintf("and %d more", len(sc.Words)-4))
					break
				}
				s := theirs(w.Text)
				switch {
				case w.Setting:
					s += map[bool]string{true: " [setting " + w.Key + "]", false: " (a setting)"}[details]
				case details && w.File != "":
					s += at(w.File, w.Line)
				case details && w.Data != "":
					s += " (from " + w.Data + ")"
				}
				if len(w.Flags) > 0 {
					s += " — it doesn't fit (" + strings.Join(w.Flags, ", ") + ")"
				}
				ws = append(ws, s)
			}
			if len(ws) > 0 {
				fmt.Println("     words: " + strings.Join(ws, " · "))
			}
			if len(sc.Pictures) > 0 {
				var ps []string
				for _, p := range sc.Pictures {
					ps = append(ps, theirs(p.File))
				}
				fmt.Println("     pictures: " + strings.Join(ps, ", "))
			}
			for _, n := range sc.Sounds {
				if n >= 1 && n <= len(c.Sounds) {
					fmt.Println("     sound: " + soundWords(c.Sounds[n-1], details))
				}
			}
			if len(sc.Fonts) > 0 {
				fmt.Println("     fonts: " + strings.Join(sc.Fonts, ", "))
			}
		}
	}
	if len(r.Map.Data) > 0 {
		fmt.Println()
		for _, d := range r.Map.Data {
			what := "data"
			if d.Kind == "timings" {
				what = "timings"
			}
			if d.Lines > 0 {
				what += fmt.Sprintf(" with the words of %s", pluralWords(d.Lines, "sound", "sounds"))
			}
			fmt.Printf("Data file %s: %s\n", theirs(d.File), what)
		}
	}
	if len(r.Map.Copy) > 0 && !r.Map.Settings {
		var fs []string
		for _, c := range r.Map.Copy {
			fs = append(fs, theirs(c.File))
		}
		fmt.Printf("\nIts words are kept in %s; MetaRush can make them settings, once a person approves that change.\n", strings.Join(fs, " and "))
		if details {
			fmt.Println("  next: metarush remotion words setup " + r.Name)
		}
	}
	if details {
		for _, s := range r.Stills {
			if s.Path != "" {
				fmt.Printf("  still of %s scene %d: %s\n", s.Composition, s.N, s.Path)
			}
		}
	}
}

func clueWords(cs []models.RemotionMapClue, details bool) string {
	var parts []string
	for _, c := range cs {
		switch c.Kind {
		case "data":
			p := "its " + c.Text
			if details {
				p += " (" + c.File + ")"
			}
			parts = append(parts, p)
		default:
			p := "its settings"
			if details {
				p += fmt.Sprintf(" (%s:%d: %s)", c.File, c.Line, c.Text)
			}
			parts = append(parts, p)
		}
	}
	return strings.Join(parts, " and ")
}

func soundWords(s models.RemotionMapSound, details bool) string {
	role := map[string]string{"music": "music", "voiceover": "a voice", "effect": "an effect"}[s.Role]
	if role == "" {
		role = "a sound"
	}
	out := fmt.Sprintf("%s, %s at %s–%s", role, theirs(s.File), fmtDur(s.From), fmtDur(s.To))
	if s.Words != "" {
		out += " saying " + theirs(s.Words)
	}
	if s.Volume > 0 && s.Volume < 0.99 {
		out += fmt.Sprintf(", at %d%%", int(s.Volume*100+0.5))
	}
	if s.Dips > 0 {
		out += fmt.Sprintf(", quieter %s", map[bool]string{true: "once", false: fmt.Sprintf("%d times", s.Dips)}[s.Dips == 1])
	}
	if details && s.Code != "" {
		out += " (" + s.Code + ")"
	}
	return out
}

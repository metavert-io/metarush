package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/remotion"
)

// Remotion sources (D131–D138): whole Remotion projects kept in a project,
// read and rendered in the render jail, edited on MetaRush's own history and
// approved by a person per version (docs/remotion-sources.md).

// importRemotionCmd is `metarush import remotion <folder | GitHub address>`.
func importRemotionCmd() *cobra.Command {
	var in remotion.RemotionImportIn
	var wait, details bool
	c := &cobra.Command{
		Use:   "remotion <folder | GitHub address> --project <project>",
		Short: "Bring a Remotion project (a folder on this computer or a GitHub repository) into a project: its code, packages and compositions, and the videos it already made as footage",
		Long: `Bring a whole Remotion project into a MetaRush project as a source. MetaRush copies its files (never
changing the original), leaving out secrets (.env files, keys), installed packages, git's history, build caches
and settings for coding assistants and other tools. It installs the packages its lockfile pins (install scripts
off), downloads the Google Fonts it uses once, and reads its compositions in a browser that can't reach the
network. The videos in its out/ folder become footage right away.

A person approves a version before its compositions go into cuts.`,
		Example: "  metarush import remotion ~/films/teaser --project \"Teaser\" --wait\n  metarush import remotion https://github.com/acme/teaser --project \"Teaser\"",
		Args:    cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.From = args[0]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			out, err := ops.CallTyped[remotion.RemotionImportOut](ctx, a, "remotion.import", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if !wait {
				return emit(c, out, func() {
					fmt.Println(out.Message)
					fmt.Println("MetaRush running on this computer does the rest; follow it in the dashboard's activity, or wait for it with --wait.")
					if details {
						fmt.Println("  job:", out.JobID)
					}
				})
			}
			if !jsonMode(c) {
				fmt.Println(out.Message)
			}
			return waitAndSay(c, a, out.JobID, details, out)
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it goes into (ID or exact name)")
	c.Flags().StringVar(&in.Name, "name", "", "its short name in the project (default: from the folder or repository)")
	c.Flags().StringVar(&in.Title, "title", "", "its title")
	c.Flags().StringVar(&in.Ref, "ref", "", "GitHub: a branch, tag or commit")
	c.Flags().BoolVar(&in.NoVideos, "no-videos", false, "don't bring in the videos in its out/ folder")
	c.Flags().BoolVar(&wait, "wait", false, "bring it in here and wait until it is done")
	addDetails(c, &details)
	_ = c.MarkFlagRequired("project")
	return c
}

// waitAndSay waits for a job (running it here when nothing else does) and
// prints what it did.
func waitAndSay(c *cobra.Command, a *app.App, jobID string, details bool, started any) error {
	js, err := waitJobs(c, a, []string{jobID}, 2*time.Hour, jsonMode(c))
	if err != nil {
		return failErr(c, err)
	}
	if len(js) == 0 {
		return fail(c, apperr.Internal, "the job disappeared", "")
	}
	j := js[0]
	if j.State != models.JobSucceeded {
		msg := j.Error
		if msg == "" {
			msg = "it stopped"
		}
		return failErr(c, apperr.New(apperr.ToolFailed, "%s", strings.TrimSpace(msg)))
	}
	return emit(c, map[string]any{"started": started, "result": j.Result, "jobId": j.ID}, func() {
		if m, _ := j.Result["message"].(string); m != "" {
			fmt.Println(m)
		}
		if details {
			for _, k := range []string{"sourceId", "name", "version", "assetId", "key"} {
				if v, ok := j.Result[k]; ok {
					fmt.Printf("  %s: %v\n", k, v)
				}
			}
		}
	})
}

// remotionCmd is `metarush remotion`.
func remotionCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remotion",
		Short: "Remotion sources: whole Remotion projects in a project — list, show, make clips of compositions, look at frames, edit (checkout, check) and approve (people only)",
		Long: `A Remotion source is a whole Remotion project kept in a MetaRush project (bring one in with
metarush import remotion). Its compositions become clips you can cut with footage, generated shots,
narration and music; Claude can edit its code on MetaRush's own history:

  metarush remotion list --project P               its compositions (size, frame rate, length)
  metarush remotion use teaser Intro --project P   make the clip and put it in the cut
  metarush remotion stills teaser Intro --at 1.5   look at frames (any version)
  metarush remotion checkout teaser ./teaser       write the newest version to edit
  metarush remotion check ./teaser -m "…"          the next version: summary, type check, before/after stills
  metarush remotion approve teaser@3fa9c2d1        a person approves it; the cut's clips move to it
  metarush remotion map teaser                     its scenes, words, pictures, sounds and a still per scene
  metarush remotion words teaser                   its on-screen words (settings once a person approved that)
  metarush remotion pictures list teaser           its pictures (replaced as settings once a person approved that)
  metarush remotion use teaser Intro --sound layers   picture only; its music and voice as the cut's tracks
  metarush remotion lift teaser End --name end-card   a scene as a motion graphic, its theme a shared library
  metarush remotion refresh plan teaser --step "npm run vo"   its own steps, run only when a person starts them
  metarush remotion push plan teaser@4             an approved version's changes back to its repository (people only)

Its code runs in MetaRush's sandboxed browser, with no network; its own scripts run only in a refresh a person
starts, kept away from MetaRush's files and keys.`,
	}
	cmd.AddCommand(remotionListCmd(), remotionShowCmd(), remotionRenderCmd(), remotionUseCmd(), remotionStillsCmd(), remotionCheckoutCmd(),
		remotionCheckCmd(), remotionApproveCmd(), remotionRevokeCmd(), remotionAllowCmd(), remotionUpdateCmd(), remotionMapCmd(), remotionWordsCmd(), remotionLiftCmd(),
		remotionRefreshCmd(), remotionPushCmd(), remotionPicturesCmd())
	return cmd
}

func remotionListCmd() *cobra.Command {
	var in remotion.RemotionListIn
	var details bool
	c := &cobra.Command{
		Use: "list [--project <project>]", Short: "List the Remotion sources: where each came from, its version and approval, its compositions", Args: cobra.NoArgs,
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "remotion.list", in, func(o any) {
				r := o.(*remotion.RemotionListOut)
				if len(r.Sources) == 0 {
					fmt.Println("No Remotion projects yet. Bring one in with: metarush import remotion <folder or GitHub address> --project <project>")
					return
				}
				for _, s := range r.Sources {
					printSourceRow(&s, details)
				}
			})
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "a project (ID or exact name)")
	addDetails(c, &details)
	return c
}

func printSourceRow(s *remotion.RemotionSourceRow, details bool) {
	fmt.Printf("%s (%s) — from %s\n", theirs(s.Title), s.Name, s.Where)
	if s.VersionNumber > 0 {
		fmt.Printf("  version %d: %s", s.VersionNumber, s.StateLabel)
		if s.HeadNumber > s.VersionNumber {
			fmt.Printf("; version %d is newer (%s)", s.HeadNumber, stateWords(s.HeadState))
		}
		fmt.Println()
	}
	if s.Problem != "" {
		fmt.Println("  " + s.Problem)
	}
	for _, c := range s.Compositions {
		line := fmt.Sprintf("  %s  %d×%d, %s fps, %s", c.ID, c.Width, c.Height, fpsWords(c.FPS), fmtDur(c.Duration))
		if c.Folder != "" {
			line += "  (in " + c.Folder + ")"
		}
		if c.Clip != nil {
			line += "  · a clip already"
		}
		fmt.Println(line)
	}
	existing := 0
	for _, cl := range s.Clips {
		if cl.Kind == models.SourceRemotionExisting {
			existing++
		}
	}
	if existing > 0 {
		fmt.Printf("  %s it already had %s footage.\n", pluralWords(existing, "video", "videos"), map[bool]string{true: "is", false: "are"}[existing == 1])
	}
	if len(s.Extras) > 0 {
		var names []string
		for _, p := range s.Extras {
			names = append(names, p.Name)
		}
		fmt.Println("  also uses: " + strings.Join(names, ", "))
	}
	if details {
		fmt.Printf("  id %s · version %s · remotion %s · %s\n", s.ID, s.Version, s.Remotion, s.URL)
	}
}

func stateWords(s string) string {
	switch s {
	case models.RemotionApproved:
		return "approved"
	case models.RemotionRejected:
		return "turned down"
	case models.RemotionRevoked:
		return "approval withdrawn"
	}
	return "waiting for a person"
}

func fpsWords(f float64) string {
	if f == float64(int(f)) {
		return strconv.Itoa(int(f))
	}
	return strconv.FormatFloat(f, 'f', 2, 64)
}

func remotionShowCmd() *cobra.Command {
	var in remotion.RemotionShowIn
	var details bool
	c := &cobra.Command{
		Use: "show <name[@version]> [--project <project>]", Short: "Show a Remotion source and a version: compositions, packages, settings, what was left out, the check and its stills", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Ref = args[0]
			return runOp(c, "remotion.show", in, func(o any) { printRemotionShow(o.(*remotion.RemotionShowOut), details) })
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in (when the name is used in several)")
	addDetails(c, &details)
	return c
}

func printRemotionShow(r *remotion.RemotionShowOut, details bool) {
	printSourceRow(&r.Source, details)
	v := r.Version
	fmt.Printf("Version %d", v.Number)
	if v.Message != "" {
		fmt.Printf(": %s", theirs(v.Message))
	}
	fmt.Printf(" (%s)\n", stateWords(v.State))
	if v.Check != nil && v.Check.Summary != "" {
		fmt.Println("  " + v.Check.Summary)
	}
	for _, p := range v.Config {
		if !p.Applied {
			fmt.Printf("  its settings: %s — %s\n", p.Setting, p.Note)
		}
	}
	for _, l := range r.LeftOut {
		fmt.Printf("  left out: %s — %s\n", pluralWords(l.Count, "file", "files"), l.Why)
	}
	for _, s := range r.Stills {
		fmt.Printf("  still: %s at %s (%.0f%% different)\n", s.Composition, fmtDur(s.At), s.Difference*100)
		if details {
			fmt.Printf("    before %s · after %s\n", s.BeforeURL, s.AfterURL)
		}
	}
	if len(r.Versions) > 1 {
		fmt.Println("Versions:")
		for _, x := range r.Versions {
			line := fmt.Sprintf("  %d  %s", x.Number, x.StateLabel)
			if x.Message != "" {
				line += " — " + theirs(x.Message)
			}
			fmt.Println(line)
		}
	}
	if r.Next != "" {
		fmt.Println(r.Next)
	}
	if details {
		fmt.Println("  version:", v.Hash, "·", r.URL)
	}
}

func renderFlags(c *cobra.Command, in *remotion.RemotionRenderIn, props, frames *string, pictures *[]string) {
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	c.Flags().StringVar(&in.Version, "version", "", "the version (default: the newest approved)")
	c.Flags().StringVar(props, "props", "", "its props as JSON, like '{\"title\":\"Hello\"}'")
	c.Flags().StringVar(frames, "frames", "", "only part of it: first-last frame, like 0-299")
	c.Flags().StringVar(&in.Sound, "sound", "", "mixed (default: its own sound in the clip) or layers (the picture alone; its music, voices and effects become the cut's own sound)")
	c.Flags().StringArrayVar(pictures, "picture", nil, "this clip's own picture in place of the project's, NAME=<file|resource> (its pictures must be settings; NAME= keeps the code's own)")
}

func readRenderFlags(c *cobra.Command, in *remotion.RemotionRenderIn, props, frames string, pictures []string) error {
	for _, kv := range pictures {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || strings.TrimSpace(k) == "" {
			return fail(c, apperr.InvalidInput, "Give each --picture as a picture's name, =, and a file or one of the project's resources, like HERO=new-hero.", "")
		}
		if strings.TrimSpace(v) == "" {
			in.Pictures = append(in.Pictures, remotion.RemotionPictureSet{Name: strings.TrimSpace(k)})
			continue
		}
		in.Pictures = append(in.Pictures, pictureChoice(strings.TrimSpace(k), strings.TrimSpace(v)))
	}
	if props != "" {
		if !json.Valid([]byte(props)) {
			return fail(c, apperr.InvalidInput, "--props couldn't be read: give the props the way the help shows.", "")
		}
		in.Props = json.RawMessage(props)
	}
	if frames != "" {
		f, err := remotion.ParseFrames(frames)
		if err != nil {
			return fail(c, apperr.InvalidInput, "--frames: "+err.Error(), "")
		}
		in.Frames = f
	}
	return nil
}

func remotionRenderCmd() *cobra.Command {
	var in remotion.RemotionRenderIn
	var props, frames string
	var pictures []string
	var wait, details bool
	c := &cobra.Command{
		Use:   "render <name> <composition> [--version …] [--props JSON] [--frames a-b] [--wait]",
		Short: "Make a composition of an approved version into a clip (rendered once, with no network; added to Sources as footage)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source, in.Composition = args[0], args[1]
			if err := readRenderFlags(c, &in, props, frames, pictures); err != nil {
				return err
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			out, err := ops.CallTyped[remotion.RemotionRenderOut](ctx, a, "remotion.render", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if !wait || out.State == models.RemotionRenderDone || out.JobID == "" {
				return emit(c, out, func() {
					fmt.Println(out.Message)
					if details {
						fmt.Printf("  state %s · job %s · clip %s\n", out.State, out.JobID, out.AssetID)
					}
				})
			}
			if !jsonMode(c) {
				fmt.Println(out.Message)
			}
			return waitAndSay(c, a, out.JobID, details, out)
		},
	}
	renderFlags(c, &in, &props, &frames, &pictures)
	c.Flags().BoolVar(&wait, "wait", false, "wait until the clip is made")
	addDetails(c, &details)
	return c
}

func remotionUseCmd() *cobra.Command {
	var in remotion.RemotionUseIn
	var props, frames, from, to string
	var pictures []string
	var wait, details bool
	c := &cobra.Command{
		Use:   "use <name> <composition> --project <project> [--after <clip>] [--from t] [--to t] [--wait]",
		Short: "Put a composition of an approved version into the project's cut (made into a clip first when needed)",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source, in.Composition = args[0], args[1]
			if err := readRenderFlags(c, &in.RemotionRenderIn, props, frames, pictures); err != nil {
				return err
			}
			for _, x := range []struct {
				v   string
				dst **float64
				f   string
			}{{from, &in.From, "--from"}, {to, &in.To, "--to"}} {
				if x.v == "" {
					continue
				}
				t, err := parseClock(x.v)
				if err != nil {
					return fail(c, apperr.InvalidInput, x.f+" is a time like 12.5 or 0:12.5", "")
				}
				*x.dst = &t
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			out, err := ops.CallTyped[remotion.RemotionUseOut](ctx, a, "remotion.use", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if !wait || out.State == models.RemotionRenderDone || out.JobID == "" {
				return emit(c, out, func() {
					fmt.Println(out.Message)
					if details {
						fmt.Printf("  state %s · job %s · clip %s · cut version %s\n", out.State, out.JobID, out.AssetID, out.Revision)
					}
				})
			}
			if !jsonMode(c) {
				fmt.Println(out.Message)
			}
			return waitAndSay(c, a, out.JobID, details, out)
		},
	}
	renderFlags(c, &in.RemotionRenderIn, &props, &frames, &pictures)
	c.Flags().StringVar(&in.After, "after", "", "the clip it goes after (default: at the end)")
	c.Flags().StringVar(&in.Name, "name", "", "the new clip's name")
	c.Flags().StringVar(&from, "from", "", "where the clip starts in the composition (seconds or 0:12.5)")
	c.Flags().StringVar(&to, "to", "", "where it ends")
	c.Flags().BoolVar(&wait, "wait", false, "wait until it is in the cut")
	addDetails(c, &details)
	_ = c.MarkFlagRequired("project")
	return c
}

// parseClock reads 12.5 or 0:12.5 or 1:02:03.
func parseClock(s string) (float64, error) {
	parts := strings.Split(strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "s")), ":")
	total := 0.0
	for _, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v < 0 {
			return 0, fmt.Errorf("not a time")
		}
		total = total*60 + v
	}
	return total, nil
}

func remotionStillsCmd() *cobra.Command {
	var in remotion.RemotionStillsIn
	var at, props string
	var details bool
	c := &cobra.Command{
		Use:   "stills <name> <composition> [--at 1.5,3] [--version …] [--props JSON] [--out <folder>]",
		Short: "Draw frames of a composition (any version) with no network, as pictures on this computer to look at",
		Args:  cobra.ExactArgs(2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source, in.Composition = args[0], args[1]
			for _, x := range strings.FieldsFunc(at, func(r rune) bool { return r == ',' || r == ' ' }) {
				t, err := parseClock(x)
				if err != nil {
					return fail(c, apperr.InvalidInput, "--at takes times like 1.5,3 or 0:02", "")
				}
				in.At = append(in.At, t)
			}
			if props != "" {
				if !json.Valid([]byte(props)) {
					return fail(c, apperr.InvalidInput, "--props couldn't be read: give the props the way the help shows.", "")
				}
				in.Props = json.RawMessage(props)
			}
			if in.Out != "" {
				if abs, err := absPath(in.Out); err == nil {
					in.Out = abs
				}
			}
			return runOp(c, "remotion.stills", in, func(o any) {
				r := o.(*remotion.RemotionStillsOut)
				fmt.Println(r.Message)
				for _, s := range r.Stills {
					fmt.Printf("  %s: %s\n", fmtDur(s.At), s.Path)
				}
			})
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	c.Flags().StringVar(&in.Version, "version", "", "the version (default: the newest)")
	c.Flags().StringVar(&at, "at", "", "moments in seconds, like 1.5,3 (default: start, quarters, end)")
	c.Flags().StringVar(&props, "props", "", "its props as JSON")
	c.Flags().Float64Var(&in.Scale, "scale", 0.5, "size (0.1–1 of the full frame)")
	c.Flags().StringVar(&in.Out, "out", "", "a folder to write them in")
	addDetails(c, &details)
	return c
}

func absPath(p string) (string, error) {
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = h + p[1:]
		}
	}
	if strings.HasPrefix(p, "/") {
		return p, nil
	}
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	return wd + "/" + p, nil
}

func remotionCheckoutCmd() *cobra.Command {
	var in remotion.RemotionCheckoutIn
	var details bool
	c := &cobra.Command{
		Use:   "checkout <name> [folder] [--version …] [--public] [--force]",
		Short: "Write a version of a Remotion source into a folder to edit (MetaRush's branch, with git for diffs)",
		Args:  cobra.RangeArgs(1, 2),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			if len(args) == 2 {
				in.Dir = args[1]
			}
			if in.Dir == "" {
				name, _, _ := strings.Cut(in.Source, "@")
				in.Dir = name
			}
			if err := assistantPath(in.Dir); err != nil {
				return failErr(c, err)
			}
			abs, err := absPath(in.Dir)
			if err != nil {
				return failErr(c, err)
			}
			in.Dir = abs
			return runOp(c, "remotion.checkout", in, func(o any) {
				r := o.(*remotion.RemotionCheckoutOut)
				fmt.Println(r.Message)
				fmt.Println(r.Next)
				if details {
					fmt.Println("  folder:", r.Dir)
					fmt.Println("  check it with: metarush remotion check " + r.Dir + " -m \"what changed\"")
				}
			})
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	c.Flags().StringVar(&in.Version, "version", "", "the version (default: the newest)")
	c.Flags().BoolVar(&in.Public, "public", false, "also write its public/ files (pictures, sound)")
	c.Flags().BoolVar(&in.Force, "force", false, "write over a checkout of the same source (its changes are lost)")
	addDetails(c, &details)
	return c
}

func remotionCheckCmd() *cobra.Command {
	var in remotion.RemotionCheckIn
	var details bool
	c := &cobra.Command{
		Use:   "check <folder> [-m \"what changed\"] [--composition X]... [--no-stills] | --version <name@version>",
		Short: "Check an edited checkout as the next version: the change in words, packages, a type check, and before/after stills drawn with no network; a person approves it afterwards",
		Long: `Check an edited checkout folder (from metarush remotion checkout). Its files become the next version on
MetaRush's branch of the source, and MetaRush checks it: what changed (in words, with on-screen text that
changed), its packages (new ones wait for a person), a type check with MetaRush's own TypeScript (none of the
project's code runs), the compositions the change can affect, and where their picture changed — compared
frame by frame with the version before, in a browser that can't reach the network — with before/after stills
there. --json gives the stills as files to look at.

A passing version waits for a person to approve it (in the dashboard, or metarush remotion approve in their own
terminal). Claude never approves.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 1 {
				if err := assistantPath(args[0]); err != nil {
					return failErr(c, err)
				}
				abs, err := absPath(args[0])
				if err != nil {
					return failErr(c, err)
				}
				in.Dir = abs
			}
			return runOp(c, "remotion.check", in, func(o any) { printRemotionCheck(o.(*remotion.RemotionCheckOut), details) })
		},
	}
	c.Flags().StringVarP(&in.Message, "message", "m", "", "what the change does, in a sentence")
	c.Flags().StringVar(&in.Version, "version", "", "check a recorded version again: name@version")
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	c.Flags().StringSliceVar(&in.Compositions, "composition", nil, "only these compositions")
	c.Flags().BoolVar(&in.NoStills, "no-stills", false, "skip the before/after pass (faster while iterating)")
	addDetails(c, &details)
	return c
}

func printRemotionCheck(r *remotion.RemotionCheckOut, details bool) {
	fmt.Println(r.Summary)
	for _, p := range r.Problems {
		if p.Severity != "error" && !details {
			continue
		}
		where := ""
		if p.File != "" {
			where = p.File
			if p.Line > 0 {
				where += fmt.Sprintf(":%d", p.Line)
			}
			where += ": "
		}
		fmt.Printf("  %s%s\n", where, p.Message)
	}
	for _, s := range r.Stills {
		fmt.Printf("  %s at %s: before %s · after %s\n", s.Composition, fmtDur(s.At), orDash(s.BeforePath), s.AfterPath)
	}
	if r.Next != "" {
		fmt.Println(r.Next)
	}
	if details {
		fmt.Println("  version:", r.Ref, "·", r.URL)
	}
}

// pluralWords is "1 video" or "3 videos".
func pluralWords(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}

func orDash(s string) string {
	if s == "" {
		return "(new)"
	}
	return s
}

// remotionDecision asks the person at the terminal and runs approve, reject,
// revoke or allow.
func remotionDecision(c *cobra.Command, op, ref, word string, in remotion.RemotionDecideIn) error {
	if !interactive() {
		return fail(c, apperr.HumanOnly, "Only a person can decide about a version of a Remotion project, in the dashboard or in their own terminal.", "Open Sources in the dashboard, or run this command yourself in a terminal.")
	}
	a, err := openApp(c, "cli")
	if err != nil {
		return err
	}
	defer a.Close()
	show, err := ops.CallTyped[remotion.RemotionShowOut](c.Context(), a, "remotion.show", remotion.RemotionShowIn{Ref: ref, ProjectID: in.ProjectID}, ops.Caller{})
	if err != nil {
		return failErr(c, err)
	}
	printRemotionShow(show, false)
	fmt.Println("Look at the change and its stills (dashboard → the project's Sources) before you decide.")
	if ask(fmt.Sprintf("Type %q to %s version %d of %s", word, word, show.Version.Number, show.Source.Title), "") != word {
		return fail(c, apperr.Canceled, "nothing was decided", "")
	}
	in.Ref = show.Source.Name + "@" + show.Version.Hash
	in.ProjectID = show.Source.ProjectID
	return runOpWith(c, a, op, in, ops.Caller{Human: true}, func(o any) { fmt.Println(o.(*remotion.RemotionDecideOut).Summary) })
}

func remotionApproveCmd() *cobra.Command {
	var in remotion.RemotionDecideIn
	c := &cobra.Command{
		Use:   "approve <name@version> [--reject] [--note \"…\"] [--keep-cuts]",
		Short: "Approve (or turn down) one version of a Remotion source — people only: the dashboard or an interactive terminal; the latest cuts' clips move to it",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			word := "approve"
			if in.Reject {
				word = "reject"
			}
			return remotionDecision(c, "remotion.approve", args[0], word, in)
		},
	}
	c.Flags().BoolVar(&in.Reject, "reject", false, "turn it down instead")
	c.Flags().StringVar(&in.Note, "note", "", "a note kept with the decision")
	c.Flags().BoolVar(&in.KeepCuts, "keep-cuts", false, "don't move the latest cuts' clips to it")
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	return c
}

func remotionRevokeCmd() *cobra.Command {
	var in remotion.RemotionDecideIn
	c := &cobra.Command{
		Use:   "revoke <name@version> [--note \"…\"]",
		Short: "Withdraw approval of a version of a Remotion source — people only",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return remotionDecision(c, "remotion.revoke", args[0], "withdraw", in)
		},
	}
	c.Flags().StringVar(&in.Note, "note", "", "a note kept with the decision")
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	return c
}

func remotionAllowCmd() *cobra.Command {
	var in remotion.RemotionDecideIn
	c := &cobra.Command{
		Use:   "allow <name@version> [--note \"…\"]",
		Short: "Allow the packages a version of a Remotion source adds — people only",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return remotionDecision(c, "remotion.allow", args[0], "allow", in)
		},
	}
	c.Flags().StringVar(&in.Note, "note", "", "a note kept with the decision")
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	return c
}

func remotionUpdateCmd() *cobra.Command {
	var in remotion.RemotionUpdateIn
	var details bool
	c := &cobra.Command{
		Use: "update <name> --project <project>", Short: "Move a project's latest cut to the newest approved version of a Remotion source (its clips are made again from it)", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			return runOp(c, "remotion.update", in, func(o any) {
				r := o.(*remotion.RemotionUpdateOut)
				fmt.Println(r.Message)
				if details && r.Revision != "" {
					fmt.Println("  cut version:", r.Revision)
				}
			})
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project (ID or exact name)")
	c.Flags().StringVar(&in.Branch, "branch", "", "a branch of the cut (default main)")
	addDetails(c, &details)
	_ = c.MarkFlagRequired("project")
	return c
}

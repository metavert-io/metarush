package cli

import (
	"fmt"
	"os"
	"path"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/remotion"
)

// remotion pictures (D209): a source's pictures as settings — the one-time
// change that makes them settings (a version a person approves), then
// pictures replaced as data (a resource or a file, checked and drawn with no
// network, the cuts' clips made again), with no new version of the code.

func remotionPicturesCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "pictures",
		Short: "A Remotion source's pictures as settings: list them, make them settings once (a person approves), then replace them without new versions of its code",
		Long: `The pictures a Remotion source draws from its own files (its public/ folder, or picture files its code
imports). Once a person approved a one-time change to its code (metarush remotion pictures setup), each one
can be replaced by a picture from the project's resources or a file on this computer — checked and drawn with no
network, then the latest cuts' clips are made again with it — with no new version of the code to approve.`,
		Example: "  metarush remotion pictures list teaser\n  metarush remotion pictures setup teaser\n  metarush remotion pictures set teaser HERO ~/art/new-hero.png --check\n  metarush remotion pictures set teaser HERO=new-hero LOGO=logo-dark",
	}
	c.AddCommand(remotionPicturesListCmd(), remotionPicturesSetupCmd(), remotionPicturesSetCmd())
	return c
}

func remotionPicturesListCmd() *cobra.Command {
	var in remotion.RemotionPicturesIn
	var details, noWait bool
	c := &cobra.Command{
		Use:   "list <name[@version]> [--project P] [--no-wait]",
		Short: "A Remotion source's pictures: each one with the picture that replaces it and where it is seen, or what the one-time change would make settings",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			out, err := ops.CallTyped[remotion.RemotionPicturesOut](ctx, a, "remotion.pictures", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			// A version read by an older build: its pictures are read once (a moment).
			if out.Reading && out.JobID != "" && !noWait {
				if !jsonMode(c) {
					fmt.Println(out.Message)
				}
				if _, err := waitJobs(c, a, []string{out.JobID}, 30*time.Minute, jsonMode(c)); err != nil {
					return failErr(c, err)
				}
				if out, err = ops.CallTyped[remotion.RemotionPicturesOut](ctx, a, "remotion.pictures", in, ops.Caller{}); err != nil {
					return failErr(c, err)
				}
			}
			return emit(c, out, func() { printRemotionPictures(out, details) })
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	c.Flags().BoolVar(&noWait, "no-wait", false, "don't wait while MetaRush reads its pictures (versions read by an older MetaRush)")
	addDetails(c, &details)
	return c
}

func printRemotionPictures(r *remotion.RemotionPicturesOut, details bool) {
	fmt.Println(r.Message)
	for _, p := range r.Pictures {
		line := "  " + p.Name + ": " + theirs(path.Base(p.File))
		if p.File == "" {
			line = "  " + p.Name
		}
		switch {
		case p.Changed && p.Current != nil:
			line += " — replaced by " + theirs(firstOf(p.Current.Title, p.Current.Resource))
		case !p.Setting && p.Why != "":
			line += " — stays in the code: " + p.Why
		case !p.Setting:
			line += " — can become a setting"
		}
		if len(p.Seen) > 0 {
			line += " — seen in " + strings.Join(p.Seen, ", ")
		} else if len(p.Used) > 0 {
			line += " — drawn by " + strings.Join(p.Used, ", ")
		}
		if p.Width > 0 && p.Height > 0 {
			line += fmt.Sprintf(" (%d×%d)", p.Width, p.Height)
		}
		fmt.Println(line)
		if details {
			for _, u := range p.Uses {
				fmt.Printf("      %s:%d (%s)\n", u.File, u.Line, u.How)
			}
			if p.Current != nil {
				fmt.Printf("      now: %s, version %s\n", p.Current.Resource, models.ShortHash(p.Current.SHA256))
			}
		}
	}
	if len(r.Left) > 0 {
		fmt.Printf("  %s stay in the code (worked out as the video plays)\n", pluralWords(len(r.Left), "picture", "pictures"))
		if details {
			for _, l := range r.Left {
				fmt.Printf("      %s:%d %s — %s\n", l.File, l.Line, l.Code, l.Why)
			}
		}
	}
	for i, ch := range r.Changes {
		if i >= 3 {
			break
		}
		fmt.Printf("  change %d (%s, %s): %s\n", ch.Number, actorWords(ch.By), ch.At.Local().Format("2 Jan 15:04"), ch.Summary)
	}
	if r.Next != "" && !strings.HasPrefix(r.Next, "metarush ") {
		fmt.Println(r.Next)
	} else if r.Next != "" && details {
		fmt.Println("  next:", r.Next)
	}
}

func firstOf(xs ...string) string {
	for _, x := range xs {
		if strings.TrimSpace(x) != "" {
			return x
		}
	}
	return ""
}

func remotionPicturesSetupCmd() *cobra.Command {
	var in remotion.RemotionPicturesSetupIn
	var noWait, details bool
	c := &cobra.Command{
		Use:   "setup <name> [--only HERO]... [--no-wait]",
		Short: "Make a Remotion source's pictures settings: one change to its code (each picture it draws wrapped so it can be replaced), checked as its next version for a person to approve",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			return runTaskOp(c, "remotion.pictures.setup", in, noWait, details)
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	c.Flags().StringSliceVar(&in.Names, "only", nil, "only these pictures (their names, as the list shows them)")
	c.Flags().BoolVar(&noWait, "no-wait", false, "don't wait while the change is made and checked")
	addDetails(c, &details)
	return c
}

func remotionPicturesSetCmd() *cobra.Command {
	var in remotion.RemotionPicturesSetIn
	var noWait, details bool
	var original []string
	c := &cobra.Command{
		Use:   "set <name> NAME <file|resource> | NAME=<file|resource>... [--original NAME]... [--check] [--note \"…\"] [--no-wait]",
		Short: "Replace pictures of a Remotion source whose pictures are settings: drawn with no network and checked (does the new picture show, its shape), then the cuts' clips are made again — no new version of its code",
		Long: `Replace a picture that is a setting (metarush remotion pictures list shows their names) with a picture file
on this computer (it is added to the project's resources first) or one of the project's resources (its short
name). Each change is drawn in MetaRush's sandboxed browser before and after (whether the new picture shows,
whether its shape fits the old one's), then the latest cuts' clips that show it are made again with it.
--original NAME puts the project's own picture back. --check draws and checks without changing anything.`,
		Example: "  metarush remotion pictures set teaser HERO ~/art/new-hero.png --check\n  metarush remotion pictures set teaser HERO=new-hero LOGO=~/art/logo.svg\n  metarush remotion pictures set teaser --original HERO",
		Args:    cobra.MinimumNArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			in.Source = args[0]
			rest := args[1:]
			if len(rest) == 2 && !strings.Contains(rest[0], "=") {
				rest = []string{rest[0] + "=" + rest[1]}
			}
			for _, kv := range rest {
				k, v, ok := strings.Cut(kv, "=")
				if !ok || strings.TrimSpace(k) == "" || strings.TrimSpace(v) == "" {
					return fail(c, apperr.InvalidInput, "Give each picture as its name and a file or one of the project's resources, like HERO new-hero or HERO=new-hero.", "")
				}
				in.Set = append(in.Set, pictureChoice(strings.TrimSpace(k), strings.TrimSpace(v)))
			}
			for _, n := range original {
				in.Set = append(in.Set, remotion.RemotionPictureSet{Name: strings.TrimSpace(n)})
			}
			if len(in.Set) == 0 {
				return fail(c, apperr.InvalidInput, "Say which picture to replace, and with what.", "")
			}
			return runTaskOp(c, "remotion.pictures.set", in, noWait, details)
		},
	}
	c.Flags().StringVar(&in.ProjectID, "project", "", "the project it is in")
	c.Flags().StringSliceVar(&original, "original", nil, "put the project's own picture back (its name)")
	c.Flags().BoolVar(&in.CheckOnly, "check", false, "only draw and check it; change nothing")
	c.Flags().StringVar(&in.Note, "note", "", "why (kept with the change)")
	c.Flags().BoolVar(&noWait, "no-wait", false, "don't wait for the check")
	addDetails(c, &details)
	return c
}

// pictureChoice reads what replaces a picture: a file on this computer when
// one is there (~ for the home folder), else a resource's name.
func pictureChoice(name, v string) remotion.RemotionPictureSet {
	p := v
	if strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			p = h + p[1:]
		}
	}
	if st, err := os.Stat(p); err == nil && st.Mode().IsRegular() {
		return remotion.RemotionPictureSet{Name: name, File: p}
	}
	return remotion.RemotionPictureSet{Name: name, Resource: v}
}

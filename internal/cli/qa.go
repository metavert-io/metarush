package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/jobs"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

func qaCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "qa", Short: "Quality checks of finished renders: what was found by watching and listening, and what is only estimated"}
	var wait, force, local, noSpeech bool
	run := &cobra.Command{
		Use:   "run <render-id|revision-id>",
		Short: "Check a render's quality (with --wait, wait for the result; it runs here if no other machine can)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			details := detailsMode(c)
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			b, _ := json.Marshal(ops.QARunIn{ID: args[0], Force: force, NoSpeechCheck: noSpeech})
			o, err := ops.Call(ctx, a, "qa.run", b, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			res := o.(*ops.QARunOut)
			if !wait && res.Report == nil {
				return emit(c, res, func() {
					fmt.Println("The quality check is queued; it isn't done yet.")
					if res.Note != "" {
						fmt.Println(res.Note)
					}
					if details {
						fmt.Printf("render %s · job %s\n", res.RenderID, res.Job.ID)
					}
				})
			}
			if res.Report == nil {
				var here func() jobRunner // this terminal checks it (the wait starts and watches the worker)
				if (local || res.Workers == 0) && res.Job.State == models.JobQueued {
					if !newWorker(a).Can(jobs.TypeQA) {
						return failErr(c, apperr.New(apperr.ToolMissing, "This machine can't check renders: it needs FFmpeg, which isn't on this computer.").
							WithHint("Add FFmpeg in Settings → Components.").WithCommand("metarush components add ffmpeg"))
					}
					switch {
					case jsonMode(c):
					case details:
						fmt.Fprintln(os.Stderr, "checking the render in this terminal…")
					default:
						fmt.Fprintln(os.Stderr, "Checking the render here, in this terminal…")
					}
					here = func() jobRunner {
						w := newWorker(a)
						w.OnlyJobID = res.Job.ID
						// The worker's own log lines (job IDs, timings) are technical detail.
						w.Log = func(f string, x ...any) {
							if !jsonMode(c) && details {
								fmt.Fprintf(os.Stderr, "  "+f+"\n", x...)
							}
						}
						return w
					}
				}
				js, err := waitJobsOpt(c, a, []string{res.Job.ID}, 0, false, here)
				if err != nil {
					return failErr(c, err)
				}
				if len(js) == 1 && js[0].State != models.JobSucceeded {
					var e *apperr.Error
					if js[0].Error != "" {
						e = apperr.Wrap(apperr.ToolFailed, errors.New(js[0].Error), "The quality check didn't finish.")
					} else {
						e = apperr.New(apperr.ToolFailed, "The quality check didn't finish.")
					}
					return failErr(c, e.WithHint(ops.JobAdvice(&js[0])).WithDetails(js[0]))
				}
			}
			return showQA(c, func() (*ops.QAShowOut, error) {
				o, err := ops.Call(ctx, a, "qa.show", mustJSON(ops.QAShowIn{ID: res.RenderID, LocalFiles: true}), ops.Caller{})
				if err != nil {
					return nil, err
				}
				return o.(*ops.QAShowOut), nil
			})
		},
	}
	run.Flags().BoolVar(&wait, "wait", false, "wait for the result (checked here if no other machine can)")
	run.Flags().BoolVar(&force, "force", false, "check again even if this render was checked")
	run.Flags().BoolVar(&noSpeech, "no-speech-check", false, "skip listening to the render's speech word by word (faster)")
	run.Flags().BoolVar(&local, "local", false, "with --wait: always check here, in this terminal")
	cmd.AddCommand(run)
	cmd.AddCommand(&cobra.Command{
		Use:   "show <report-id|render-id|revision-id>",
		Short: "Show a render's latest quality check (--details adds the measurements and where its frames are)",
		Args:  cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			ctx, cancel := signalCtx(c.Context())
			defer cancel()
			return showQA(c, func() (*ops.QAShowOut, error) {
				o, err := ops.Call(ctx, a, "qa.show", mustJSON(ops.QAShowIn{ID: args[0], LocalFiles: true}), ops.Caller{})
				if err != nil {
					return nil, err
				}
				return o.(*ops.QAShowOut), nil
			})
		},
	})
	return cmd
}

func mustJSON(v any) json.RawMessage { b, _ := json.Marshal(v); return b }

func showQA(c *cobra.Command, get func() (*ops.QAShowOut, error)) error {
	out, err := get()
	if err != nil {
		return failErr(c, err)
	}
	return emit(c, out, func() { printQA(out, detailsMode(c)) })
}

// qaMarks start a finding's line: ✗ a problem to fix, ! worth checking (may
// be fine), · a note (the dashboard's Problem, Worth checking and Note).
var qaMarks = map[string]string{models.QAError: "✗", models.QAWarning: "!", models.QAInfo: "·"}

// qaVerdict is a quality check's result in a few words, as the dashboard says it.
func qaVerdict(n models.QACounts) string {
	switch {
	case n.Errors > 0 && n.Warnings > 0:
		return fmt.Sprintf("%s to fix and %s worth checking", plural(n.Errors, "problem"), plural(n.Warnings, "thing"))
	case n.Errors > 0:
		return plural(n.Errors, "problem") + " to fix"
	case n.Warnings > 0:
		return plural(n.Warnings, "thing") + " worth checking"
	}
	return "no problems found"
}

// qaVerdictWords say a quality check's verdict (pass, warnings, fail).
func qaVerdictWords(v string) string {
	switch v {
	case models.QAPass:
		return "quality check passed"
	case models.QAWarnings:
		return "quality check: worth checking"
	case models.QAFail:
		return "quality check found problems"
	}
	return "quality check: " + v
}

// findingWhen is where in the video a finding is: "the whole video" or
// "around 0:21".
func findingWhen(f models.QAFinding, duration float64) string {
	if f.From <= 0.05 && duration > 0 && f.To >= duration-0.1 {
		return "the whole video"
	}
	return "around " + aroundClock(f.From)
}

// printQA says what a quality check found, finding by finding, with the
// suggested fix; details adds the measurements, the checks, the fixes as
// changes to the cut, and where the frames and sheets are.
func printQA(out *ops.QAShowOut, details bool) {
	r := out.Report
	m := r.Measurements
	fmt.Printf("Quality check of %s: %s.\n", renderWords(r.Quality, r.RevisionNumber), qaVerdict(r.Counts))
	when := "Checked " + plainWhen(r.CreatedAt)
	if r.RunBy != "" {
		when += " on " + r.RunBy
	}
	fmt.Println(when + ".")
	frames := map[string]string{}
	for _, f := range r.Frames {
		frames[f.ID] = f.Path
	}
	length := m.Duration
	if length <= 0 {
		length = m.ExpectedDuration
	}
	for _, f := range r.Findings {
		msg := capitalize(strings.TrimSpace(f.Message))
		if !strings.HasPrefix(strings.ToLower(msg), "around ") {
			msg = capitalize(findingWhen(f, length)) + ": " + msg
		}
		if f.Basis == models.QAHeuristic {
			msg += " (estimated)"
		}
		fmt.Printf("  %s %s\n", orWord(qaMarks[f.Severity], "-"), msg)
		if f.Fix != nil && f.Fix.Summary != "" {
			fmt.Printf("    Fix: %s\n", f.Fix.Summary)
		}
		if !details {
			continue
		}
		fmt.Printf("    (%s · %s %s · %s · %s)\n", f.Kind, f.Severity, f.ID, f.Basis, f.Timecode)
		if f.Fix != nil {
			for _, op := range f.Fix.Ops {
				b, _ := json.Marshal(op)
				fmt.Printf("    fix: %s\n", b)
			}
		}
		for _, id := range f.Frames {
			fmt.Printf("    frame: %s\n", orNone(frames[id]))
		}
	}
	fmt.Println(out.URL)
	if !details {
		return
	}
	fmt.Printf("\nreport %s · render %s (%s %s) · %s\n", r.ID, r.RenderID, r.Preset, r.Quality, strings.ToUpper(r.Verdict))
	lufs, tp := "n/a", "n/a"
	if m.IntegratedLUFS != nil {
		lufs = fmt.Sprintf("%.1f LUFS", *m.IntegratedLUFS)
	}
	if m.TruePeakDBTP != nil {
		tp = fmt.Sprintf("%.1f dBTP", *m.TruePeakDBTP)
	}
	fmt.Printf("measured: %dx%d, %.2fs, frames %d/%d · %s/%s · audio %v · loudness %s, true peak %s (target %.0f LUFS, ≤ %.0f dBTP: %s)\n",
		m.Width, m.Height, m.Duration, m.Frames, m.ExpectedFrames, m.VideoCodec, m.PixelFormat, m.HasAudio, lufs, tp, m.LoudnessTarget.IntegratedLUFS, m.LoudnessTarget.MaxTruePeakDBTP, m.LoudnessTarget.Note)
	var cs []string
	for _, ch := range r.Checks {
		cs = append(cs, ch.Name+" "+ch.Status)
	}
	fmt.Println("checks:", strings.Join(cs, " · "))
	if r.ContactSheet != nil {
		fmt.Println("contact sheet:", orNone(r.ContactSheet.Path))
	}
	if r.MomentsSheet != nil {
		fmt.Println("moments sheet:", orNone(r.MomentsSheet.Path))
	}
	if out.Dir != "" {
		fmt.Printf("frames (%d): %s/frames\n", len(r.Frames), out.Dir)
	}
	for _, l := range r.Limitations {
		fmt.Println("limitation:", l)
	}
	if out.Note != "" {
		fmt.Println("note:", out.Note)
	}
}

func deliverCmd() *cobra.Command {
	var in ops.DeliverIn
	cmd := &cobra.Command{
		Use:   "deliver --revision <id>... --out <dir>",
		Short: "Export finished videos to a folder on this machine (checked copies with a notes file each; never overwrites, never uploads)",
		Long: "Copies the newest finished render of each version (every shape it was rendered in, or --preset) into --out with a descriptive file name " +
			"and a notes file beside it (.json: title, version, shape, length, the reasons for the cut, the footage it uses, music rights, approval). " +
			"Versions nobody approved are refused unless --allow-draft; versions that were turned down are always refused. Nothing is uploaded anywhere.",
		RunE: func(c *cobra.Command, args []string) error {
			in.RevisionIDs = append(in.RevisionIDs, args...)
			return runOp(c, "deliver.export", in, func(o any) {
				r := o.(*ops.DeliverOut)
				details := detailsMode(c)
				fmt.Printf("Exported %s to the folder “%s”.\n", plural(len(r.Files), "video"), baseName(strings.TrimRight(r.Dir, "/")))
				for _, f := range r.Files {
					notes := []string{approvalWords(f.Approval)}
					if f.QAVerdict != "" {
						notes = append(notes, qaVerdictWords(f.QAVerdict))
					}
					fmt.Printf("  version %-3d %-10s %8s %9s  %s (%s)\n", f.RevisionNumber, presetWords(f.Preset), seconds(f.Duration), fmtBytes(f.Size), baseName(f.Path), strings.Join(notes, "; "))
					if details {
						fmt.Printf("      %s\n", f.Path)
					}
				}
				for _, w := range r.Warnings {
					fmt.Println("  ! " + w)
				}
				if len(r.Credits) > 0 {
					fmt.Println("Outside footage in these videos (for your description or end credits):")
					for _, c := range r.Credits {
						fmt.Println("  " + c)
					}
				}
				fmt.Println(r.Note)
				if details {
					fmt.Println("folder:", r.Dir)
				}
			})
		},
	}
	cmd.Flags().StringSliceVar(&in.RevisionIDs, "revision", nil, "a version's ID (repeat or separate with commas)")
	cmd.Flags().StringVar(&in.Out, "out", "", "the folder to put them in")
	cmd.Flags().StringSliceVar(&in.Presets, "preset", nil, "only these shapes (vertical-1080p, landscape-1080p, square-1080)")
	cmd.Flags().StringVar(&in.Quality, "quality", "final", "final | review")
	cmd.Flags().BoolVar(&in.AllowDraft, "allow-draft", false, "also export versions nobody approved (only when the person explicitly asks)")
	cmd.AddCommand(deliverCaptionsCmd(), deliverNotesCmd(), deliverReadyCmd(), deliverOverrideCmd(), deliverGateCmd())
	return cmd
}

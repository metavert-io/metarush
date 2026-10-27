package cli

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/assistant"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ingest"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// voiceoverCmd is narration: the script (part of the cut, approved through
// edit check/apply), its audio (scratch, ElevenLabs, imports, recordings) and cleanup.
func voiceoverCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "voiceover", Aliases: []string{"vo", "narration"}, Short: "Write, place, voice and fit narration for a cut",
		Long: `Narration is part of the cut: a script of lines, each starting at a point that moves
with the edit (a shot, a moment in the footage, the previous line, or a clock time).
Commands that change the script show the change first and save nothing; --details shows
the command that saves it as a new version (or add --apply).

  metarush voiceover script import script.txt --revision <id>
  metarush voiceover scratch --revision <id>          # free system voice for timing
  metarush voiceover tts --revision <id> --estimate   # ElevenLabs cost before spending
  metarush voiceover import narration.wav --revision <id>
  metarush voiceover record --revision <id>           # record yourself line by line`}
	cmd.AddCommand(voScriptCmd(), voBeatCmd(), voImportCmd(), voScratchCmd(), voTTSCmd(false), voTTSCmd(true), voVoicesCmd(), voModelsCmd(),
		voTakesCmd(), voCleanupCmd(), voFitCmd(), voCloneCmd(), voRecordCmd())
	return cmd
}

// revisionFlags adds --revision/--project; resolveRevision picks the main head when only a project is given.
type revFlags struct{ revision, project, branch string }

func (r *revFlags) add(c *cobra.Command) {
	c.Flags().StringVar(&r.revision, "revision", "", "the version of the cut (its ID)")
	c.Flags().StringVar(&r.project, "project", "", "use the latest version of this project's cut")
	c.Flags().StringVar(&r.branch, "branch", "main", "with --project: the branch")
}

func (r *revFlags) resolve(c *cobra.Command, a *app.App) (string, error) {
	if r.revision != "" {
		return r.revision, nil
	}
	if r.project == "" {
		return "", fail(c, apperr.InvalidInput, "Say which cut: --revision <id>, or --project <id> for its latest version.", "")
	}
	var p models.Project
	if err := a.DB.GetByID(c.Context(), store.CProjects, a.WorkspaceID(), r.project, &p); err != nil {
		return "", failErr(c, apperr.New(apperr.NotFound, "That project wasn't found.").WithDetails(map[string]any{"project": r.project}))
	}
	h := p.Branches[r.branch].Head
	if h == "" {
		return "", fail(c, apperr.NotFound, "This project has no cut yet.", "Make one first.")
	}
	return h, nil
}

// planFlow saves a generated plan, then either checks it (prints the change and
// a confirm token) or, with --confirm, applies it as a new revision.
func planFlow(c *cobra.Command, a *app.App, plan editspec.Plan, confirm string, extra map[string]any, human func()) error {
	raw, err := json.MarshalIndent(plan, "", "  ")
	if err != nil {
		return err
	}
	file := savePlanFile(a, raw)
	out := map[string]any{"plan": plan, "planFile": file}
	for k, v := range extra {
		out[k] = v
	}
	if confirm != "" {
		res, err := ops.CallTyped[ops.EditApplyOut](c.Context(), a, "edit.apply", ops.EditApplyIn{Plan: raw, ConfirmToken: confirm}, ops.Caller{})
		if err != nil {
			return failErr(c, err)
		}
		out["applied"] = res
		return emit(c, out, func() {
			if human != nil {
				human()
			}
			fmt.Printf("Done — the cut is now version %d.\n", res.Revision.Number)
			for _, s := range res.Diff.Summary {
				fmt.Println("  • " + s)
			}
			for _, w := range res.Warnings {
				fmt.Println("  ! " + w.Message)
			}
			if detailsMode(c) {
				fmt.Printf("version %s (r%d)\n", res.Revision.ID, res.Revision.Number)
			}
		})
	}
	check, err := ops.CallTyped[ops.EditCheckOut](c.Context(), a, "edit.check", ops.EditCheckIn{Plan: raw}, ops.Caller{})
	if err != nil {
		return failErr(c, err)
	}
	out["check"] = check
	return emit(c, out, func() {
		if human != nil {
			human()
		}
		printCheckPlain(c, check, file)
	})
}

// printCheckPlain shows a checked change for people: what would change and
// the warnings; nothing is saved yet (readyToSave), and --details gives the
// exact command that saves it, with its confirm token.
func printCheckPlain(c *cobra.Command, check *ops.EditCheckOut, file string) {
	if check == nil {
		return
	}
	details := detailsMode(c)
	if !check.Valid {
		fmt.Println("This change can't be made yet:")
		for _, e := range check.Errors {
			fmt.Println("  ✗ " + e.Message)
			if details {
				fmt.Printf("    %s %s\n", e.Code, e.Path)
			}
		}
		fmt.Println("Nothing has changed.")
		return
	}
	if check.Diff != nil && len(check.Diff.Summary) > 0 {
		fmt.Println("What would change:")
		for _, s := range check.Diff.Summary {
			fmt.Println("  • " + s)
		}
	}
	for _, w := range check.Warnings {
		fmt.Println("  ! " + w.Message)
		if details {
			fmt.Printf("    %s %s\n", w.Code, w.Path)
		}
	}
	if check.Rebased {
		fmt.Println("Someone saved a newer version; this doesn't overlap theirs, so saving keeps both.")
	}
	readyToSave(c, fmt.Sprintf("metarush edit apply --plan %s --confirm %s", shellQuote(file), check.ConfirmToken))
}

func savePlanFile(a *app.App, raw []byte) string {
	sum := sha256.Sum256(raw)
	dir := filepath.Join(a.Cfg.Scratch.Dir, "plans")
	_ = os.MkdirAll(dir, 0o755)
	p := filepath.Join(dir, "plan-"+hex.EncodeToString(sum[:6])+".json")
	_ = os.WriteFile(p, raw, 0o644)
	return p
}

// pending is the plan still to apply: none once --apply put the work in the cut.
func pending(res *voiceover.PlanResult, ap *voiceover.Applied) *voiceover.PlanResult {
	if ap != nil && ap.Problem == "" {
		return nil
	}
	return res
}

// resultFlow prints a generation result (plan already checked by the op).
func resultFlow(c *cobra.Command, a *app.App, out any, res *voiceover.PlanResult, human func()) error {
	payload := map[string]any{"result": out}
	file := ""
	if res != nil {
		raw, _ := json.MarshalIndent(res.Plan, "", "  ")
		file = savePlanFile(a, raw)
		payload["planFile"] = file
	}
	return emit(c, payload, func() {
		human()
		if res != nil {
			printCheckPlain(c, res.Check, file)
		}
	})
}

func readInput(c *cobra.Command, path string) ([]byte, error) {
	if path == "-" {
		return io.ReadAll(io.LimitReader(c.InOrStdin(), 4<<20))
	}
	return os.ReadFile(path)
}

func voScriptCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "script", Short: "Show, set or import the narration script"}
	var rf revFlags
	show := &cobra.Command{Use: "show", Short: "Each line, where it plays, whether it fits, and its audio",
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
			return runOpWith(c, a, "voiceover.show", voiceover.RevisionIn{RevisionID: rev}, ops.Caller{}, func(o any) { printShow(o.(*voiceover.ShowOut), detailsMode(c)) })
		}}
	rf.add(show)
	cmd.AddCommand(show)

	var rf2 revFlags
	var file, confirm string
	set := &cobra.Command{Use: "set --file <voiceover.json|->", Short: "Replace the narration with a voiceover file (JSON; `metarush schema show edit-spec` says what it may hold)",
		RunE: func(c *cobra.Command, _ []string) error {
			raw, err := readInput(c, file)
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			v, err := voiceover.DecodeScript(raw)
			if err != nil {
				return failErr(c, err)
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			rev, err := rf2.resolve(c, a)
			if err != nil {
				return err
			}
			r, err := getRev(c, a, rev)
			if err != nil {
				return err
			}
			return planFlow(c, a, voiceover.ScriptSetPlan(r, v), confirm, nil, nil)
		}}
	rf2.add(set)
	set.Flags().StringVar(&file, "file", "-", "voiceover JSON (- for stdin)")
	set.Flags().StringVar(&confirm, "confirm", "", "apply with the token from the check")
	cmd.AddCommand(set)

	var rf3 revFlags
	var appendTo, spread bool
	var confirm3, forClip string
	imp := &cobra.Command{Use: "import <script.txt|->", Short: "Read a plain-text script: paragraphs become lines; [@shot+0.5] [0:12.4] [after] (notes) place them",
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			raw, err := readInput(c, args[0])
			if err != nil {
				return fail(c, apperr.InvalidInput, err.Error(), "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			rev, err := rf3.resolve(c, a)
			if err != nil {
				return err
			}
			out, err := ops.CallTyped[voiceover.ParseOut](c.Context(), a, "voiceover.parse", voiceover.ParseIn{RevisionID: rev, Text: string(raw), Append: appendTo, Clip: forClip, Spread: spread}, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if out.Result == nil {
				return emit(c, out, func() {
					fmt.Println(out.Summary)
					for _, is := range out.Issues {
						fmt.Println("  ✗ " + is.Message)
					}
				})
			}
			return planFlow(c, a, out.Result.Plan, confirm3, map[string]any{"parsed": out}, func() { fmt.Println(out.Summary) })
		}}
	rf3.add(imp)
	imp.Flags().BoolVar(&appendTo, "append", false, "add after the existing lines instead of replacing them")
	imp.Flags().StringVar(&forClip, "clip", "", "the narration of this shot only (ID or label): it starts with the shot and replaces only the lines over it")
	imp.Flags().StringVar(&confirm3, "confirm", "", "apply with the token from the check")
	imp.Flags().BoolVar(&spread, "spread", false, "spread lines without a start point over the shots, one per shot (default: as-is, each follows the line before until a recording sets the timing)")
	cmd.AddCommand(imp, voScriptFitCmd(), voScriptUseCmd())
	return cmd
}

// voScriptFitCmd fits the script to the video with Claude (D86): without
// --propose it runs your local Claude Code headless (read-only) and shows its
// version next to yours; with --propose it reads a fitted version Claude wrote
// (a terminal session, or the run above) and checks the change.
func voScriptFitCmd() *cobra.Command {
	var rf revFlags
	var propose, confirm, note, model string
	var keep, use []string
	cmd := &cobra.Command{Use: "fit", Short: "Fit the script to the video with Claude: lines tied to the right moments, trimmed to fit, every change explained; your original is kept",
		Long: `Without --propose, asks your local Claude Code (read-only, headless) to fit the script to
the video: it reads the shots, transcripts, frames and your pace, and proposes a version
with each line tied to the right moment and its words trimmed or extended to fit, keeping
your voice and style and explaining every change. You see both versions side by side, line
by line, and nothing changes until you use it (here with --propose … --confirm, or on the
card in the dashboard's Claude Code panel). Your original script is always kept.

With --propose <file>, reads a fitted version Claude wrote ({"lines":[{"id","text",
"anchor" or "at","reason"}],"note"}) and checks the change; --keep 3,5 keeps your own words
on those lines; --confirm <token> applies it. Without a file, --keep and --use change
which lines of the fitted version already in the cut you keep.

In a terminal Claude Code session, ask "fit my script to the video" instead.`,
		RunE: func(c *cobra.Command, _ []string) error {
			if propose == "" && len(keep) == 0 && len(use) == 0 {
				return fitWithClaude(c, &rf, note, model)
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			rev, err := rf.resolve(c, a)
			if err != nil {
				return err
			}
			in := voiceover.FittedIn{RevisionID: rev, Keep: keep, Use: use}
			if propose != "" {
				raw, err := readInput(c, propose)
				if err != nil {
					return fail(c, apperr.InvalidInput, err.Error(), "")
				}
				var p voiceover.FitProposal
				dec := json.NewDecoder(bytes.NewReader(raw))
				dec.DisallowUnknownFields()
				if err := dec.Decode(&p); err != nil {
					return failErr(c, apperr.New(apperr.InvalidInput, "The fitted script isn't in the expected form.").
						WithHint("It needs a list of lines, each with its line, its words, where it starts (a start point or a time) and why, plus a note.").
						WithDetails(map[string]any{"problem": err.Error(), "expected": `{"lines":[{"id":"line-1","text":"…","anchor":{…} or "at":"0:04.2","reason":"…"}],"note":"…"}`}))
				}
				in.Proposal = &p
			}
			out, err := ops.CallTyped[voiceover.FittedOut](c.Context(), a, "voiceover.fitted", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			if out.Result == nil {
				return emit(c, out, func() { printFitted(out) })
			}
			return planFlow(c, a, out.Result.Plan, confirm, map[string]any{"fitted": out}, func() { printFitted(out) })
		}}
	rf.add(cmd)
	cmd.Flags().StringVar(&propose, "propose", "", "a fitted script Claude wrote (JSON file, - for stdin)")
	cmd.Flags().StringSliceVar(&keep, "keep", nil, "lines (numbers or IDs) where you keep your own words and start")
	cmd.Flags().StringSliceVar(&use, "use", nil, "lines where you take Claude's version")
	cmd.Flags().StringVar(&confirm, "confirm", "", "apply with the token from the check")
	cmd.Flags().StringVar(&note, "note", "", "without --propose: anything Claude should know (\"keep it short\")")
	cmd.Flags().StringVar(&model, "model", "", "without --propose: the Claude model (default: the installation's)")
	return cmd
}

// fitWithClaude runs the headless fit: your local Claude Code, read-only,
// through the same runner as the dashboard's panel. It waits for Claude's
// version and prints it next to yours with the command that uses it.
func fitWithClaude(c *cobra.Command, rf *revFlags, note, model string) error {
	if os.Getenv("CLAUDECODE") != "" || assistantMode() {
		// Already inside Claude Code: no second Claude; the skill does the fitting.
		return fail(c, apperr.InvalidInput, "This is already a Claude Code session, so MetaRush doesn't start another one.",
			assistant.FitInTerminal+". Write the fitted script, then run this command again with --propose <file>.")
	}
	s, err := openAssistant(c, true)
	if err != nil {
		return err
	}
	defer s.close()
	rev, err := rf.resolve(c, s.a)
	if err != nil {
		return err
	}
	var out assistant.ConversationSendOut
	if err := s.call(c.Context(), "voiceover.askFit", assistant.AskFitIn{RevisionID: rev, Note: note, Model: model}, &out); err != nil {
		return failErr(c, err)
	}
	id := out.Conversation.ID
	if !jsonMode(c) {
		fmt.Fprintln(os.Stderr, "Claude Code is reading your cut and fitting the script (read-only; nothing changes yet)…")
	}
	ctx, cancel := signalCtx(c.Context())
	defer cancel()
	var cv assistant.Conversation
	deadline := time.Now().Add(30 * time.Minute)
	for {
		if err := s.call(ctx, "assistant.show", assistant.ConversationShowIn{ID: id, Events: boolp(false)}, &cv); err != nil {
			return failErr(c, err)
		}
		if cv.State != "working" && len(cv.Queue) == 0 {
			break
		}
		if time.Now().After(deadline) {
			return failErr(c, apperr.New(apperr.Conflict, "Claude Code is still fitting the script after 30 minutes.").
				WithHint("Follow it in the dashboard's Claude Code panel.").WithCommand("metarush assistant conversations show "+id))
		}
		select {
		case <-ctx.Done():
			return failErr(c, apperr.New(apperr.Canceled, "Stopped waiting; Claude Code goes on fitting the script (the dashboard's Claude Code panel shows it).").
				WithDetails(map[string]any{"conversation": id}))
		case <-time.After(1500 * time.Millisecond):
		}
	}
	var card *assistant.AssistantAction
	for i := range cv.Actions {
		if cv.Actions[i].Kind == "voiceover script fit" && cv.Actions[i].Fit != nil {
			card = &cv.Actions[i]
		}
	}
	res := map[string]any{"conversationId": id}
	if card == nil {
		res["fitted"] = nil
		return emit(c, res, func() {
			fmt.Println("Claude Code did not propose a fitted script this time. The dashboard's Claude Code panel shows what it said.")
			if detailsMode(c) {
				fmt.Printf("see it: metarush assistant conversations show %s\n", id)
			}
		})
	}
	res["fitted"], res["actionId"] = card.Fit, card.ID
	file := proposalPathOf(card.Command)
	if file != "" {
		res["proposal"] = file
	}
	return emit(c, res, func() {
		printFitted(card.Fit)
		fmt.Println()
		fmt.Println("Nothing has changed yet. Review it line by line in the dashboard's Claude Code panel, and use it from there.")
		if file != "" {
			if detailsMode(c) {
				fmt.Printf("use it from here: metarush voiceover script fit --propose %s --revision %s [--keep 3,5]\n", shellQuote(file), rev)
			} else {
				fmt.Println("(Add --details to see the command that uses it from here.)")
			}
		}
	})
}

// proposalPathOf finds the --propose file in the command Claude ran.
func proposalPathOf(command string) string {
	f := strings.Fields(command)
	for i, w := range f {
		if w == "--propose" && i+1 < len(f) {
			return strings.Trim(f[i+1], `"'`)
		}
		if strings.HasPrefix(w, "--propose=") {
			return strings.Trim(strings.TrimPrefix(w, "--propose="), `"'`)
		}
	}
	return ""
}

func boolp(v bool) *bool { return &v }

// printFitted shows the two versions line by line, in plain words.
func printFitted(o *voiceover.FittedOut) {
	fmt.Println(o.Summary)
	if o.Note != "" {
		fmt.Println("Claude: " + o.Note)
	}
	for _, l := range o.Lines {
		if l.Change == "none" {
			fmt.Printf("\n  %2d. (unchanged) %s\n", l.Line, trunc(l.Original, 90))
			continue
		}
		mark := ""
		if l.Kept {
			mark = "  [keeping yours]"
		}
		fmt.Printf("\n  %2d. yours:  %s\n      Claude: %s%s\n", l.Line, trunc(l.Original, 90), trunc(l.Text, 90), mark)
		if l.Placement != l.OriginalPlacement {
			fmt.Printf("      starts %s (was %s)\n", l.Placement, l.OriginalPlacement)
		}
		if l.Reason != "" {
			fmt.Printf("      why: %s\n", l.Reason)
		}
	}
	for _, w := range o.Warnings {
		fmt.Println("  ! " + w)
	}
}

func voScriptUseCmd() *cobra.Command {
	var rf revFlags
	var confirm string
	cmd := &cobra.Command{Use: "use <original|fitted>", Short: "Use your original script, or Claude's version fitted to the video (the other is kept with its sound and places)",
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
			out, err := ops.CallTyped[voiceover.UseOut](c.Context(), a, "voiceover.use", voiceover.UseIn{RevisionID: rev, Version: strings.ToLower(args[0])}, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return planFlow(c, a, out.Result.Plan, confirm, map[string]any{"active": out.Active}, func() { fmt.Println(strings.TrimSuffix(out.Summary, " Apply the change to put it in the cut.")) })
		}}
	rf.add(cmd)
	cmd.Flags().StringVar(&confirm, "confirm", "", "apply with the token from the check")
	return cmd
}

func getRev(c *cobra.Command, a *app.App, id string) (*models.Revision, error) {
	var r models.Revision
	if err := a.DB.GetByID(c.Context(), store.CRevisions, a.WorkspaceID(), id, &r); err != nil {
		return nil, failErr(c, apperr.New(apperr.NotFound, "That version of the cut wasn't found.").WithDetails(map[string]any{"revision": id}))
	}
	return &r, nil
}

func printShow(s *voiceover.ShowOut, details bool) {
	fmt.Println(s.Summary)
	if !s.HasScript {
		return
	}
	pace := "a typical pace"
	if s.RateSource == "measured" {
		pace = "the narrator's measured pace"
	}
	fmt.Printf("Speaking rate: %.0f words a minute (%s)\n", s.SpeakingRateWPM, pace)
	switch sv := s.Script; {
	case sv.Fitted != nil && sv.Active == editspec.VersionFitted:
		fmt.Println("Script: Claude's version, fitted to the video (yours is kept, and you can switch back)")
		if details {
			fmt.Println("  switch back: metarush voiceover script use original")
		}
	case sv.Fitted != nil:
		fmt.Println("Script: your own words (Claude's fitted version is kept too)")
		if details {
			fmt.Println("  use it: metarush voiceover script use fitted")
		}
	case sv.HasOriginal && !sv.Timed:
		fmt.Println("Script: your own words, no timing yet (record it live and each line lands where you say it)")
	case sv.HasOriginal:
		fmt.Println("Script: your own words")
	}
	if len(s.Reads) > 0 {
		fmt.Println("Live takes, newest first:")
		for i, r := range s.Reads {
			if i == 3 && !details {
				fmt.Printf("  and %d more\n", len(s.Reads)-3)
				break
			}
			use := "not in use"
			if r.InUse > 0 {
				use = "in use for " + plural(r.InUse, "line")
			}
			if details {
				fmt.Printf("  %s  %s · %s\n", r.ReadID, r.Summary, use)
			} else {
				fmt.Printf("  %s · %s\n", r.Summary, use)
			}
		}
	}
	fmt.Println()
	for _, b := range s.Beats {
		mark := map[string]string{"ok": "✓", "tight": "~", "over": "!", "unresolved": "✗"}[b.Fit]
		fmt.Printf("%s %2d. %s–%s  %s\n", mark, b.Line, editspec.Clock(b.Start), editspec.Clock(b.End), trunc(b.Text, 70))
		fmt.Printf("       %s · %s", b.Placement, b.TakeLabel)
		if b.TakeCount > 1 {
			fmt.Printf(" · %d takes", b.TakeCount)
		}
		fmt.Println()
		if b.Problem != "" {
			fmt.Println("       ✗ " + b.Problem)
		}
	}
	for _, f := range s.Fit {
		if f.Status == "over" {
			fmt.Println("! " + f.Message)
		}
	}
}

// anchorFlags reads a start point from flags.
type anchorFlags struct {
	clip, at, moment, quote string
	offset, gap             float64
	afterPrev               bool
}

func (f *anchorFlags) add(c *cobra.Command) {
	c.Flags().StringVar(&f.clip, "at-clip", "", "start with this shot (ID or label; add :end for when it ends)")
	c.Flags().StringVar(&f.at, "at-time", "", "start at a clock time (m:ss.s)")
	c.Flags().StringVar(&f.moment, "at-moment", "", "start at a moment in the footage: <asset-id>@<seconds>")
	c.Flags().StringVar(&f.quote, "quote", "", "with --at-moment: what happens there, in words")
	c.Flags().Float64Var(&f.offset, "offset", 0, "seconds after (or, negative, before) that point")
	c.Flags().BoolVar(&f.afterPrev, "after-previous", false, "start after the previous line")
	c.Flags().Float64Var(&f.gap, "gap", 0, "with --after-previous: pause in seconds")
}

func (f *anchorFlags) anchor(c *cobra.Command, s *editspec.Spec) (*editspec.Anchor, error) {
	spec := voiceover.AnchorSpec{Clip: f.clip, At: f.at, Moment: f.moment, Quote: f.quote, Offset: f.offset, AfterPrevious: f.afterPrev}
	if c.Flags().Changed("gap") {
		g := f.gap
		spec.Gap = &g
	}
	return spec.Anchor(s)
}

func voBeatCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "beat", Aliases: []string{"line"}, Short: "Add, change, move or remove one narration line"}
	type common struct {
		rf      revFlags
		confirm string
	}
	// Every line change is one voiceover.line op (the dashboard's script editor
	// uses the same one): the change is checked, then applied with --confirm.
	withLine := func(c *cobra.Command, cm *common, fn func(r *models.Revision) (voiceover.LineIn, error)) error {
		a, err := openApp(c, "cli")
		if err != nil {
			return err
		}
		defer a.Close()
		rev, err := cm.rf.resolve(c, a)
		if err != nil {
			return err
		}
		r, err := getRev(c, a, rev)
		if err != nil {
			return err
		}
		in, err := fn(r)
		if err != nil {
			return failErr(c, err)
		}
		in.RevisionID = r.ID
		out, err := ops.CallTyped[voiceover.LineOut](c.Context(), a, "voiceover.line", in, ops.Caller{})
		if err != nil {
			return failErr(c, err)
		}
		if out.Result == nil {
			return emit(c, out, func() { fmt.Println(out.Summary) })
		}
		return planFlow(c, a, out.Result.Plan, cm.confirm, map[string]any{"placement": out.Placement}, func() {
			if out.Placement != "" {
				fmt.Println("It starts " + out.Placement + ".")
			}
		})
	}
	// --to <time> places a line at a point of the video, tied to the shot there.
	toFlag := func(c *cobra.Command, to string, in *voiceover.LineIn) error {
		if !c.Flags().Changed("to") {
			return nil
		}
		t, ok := editspec.ParseClock(to)
		if !ok {
			return apperr.New(apperr.InvalidInput, "could not read the time %q (use m:ss.s)", to)
		}
		in.To = &t
		return nil
	}

	var addC common
	var af anchorFlags
	var text, id, delivery, after, before, addTo string
	var addExact bool
	add := &cobra.Command{Use: "add --text <words>", Short: "Add a narration line at a start point (a shot, a moment, after the previous line, or a time)",
		RunE: func(c *cobra.Command, _ []string) error {
			return withLine(c, &addC, func(r *models.Revision) (voiceover.LineIn, error) {
				an, err := af.anchor(c, &r.Spec)
				if err != nil {
					return voiceover.LineIn{}, err
				}
				in := voiceover.LineIn{Action: "add", Text: &text, ID: id, Delivery: &delivery, After: after, Before: before, Anchor: an, Exact: addExact}
				return in, toFlag(c, addTo, &in)
			})
		}}
	addC.rf.add(add)
	af.add(add)
	add.Flags().StringVar(&addTo, "to", "", "start at this point of the video (m:ss.s), tied to the shot playing there")
	add.Flags().BoolVar(&addExact, "exact", false, "with --to: keep it at that exact clock time")
	add.Flags().StringVar(&text, "text", "", "the words to speak")
	add.Flags().StringVar(&id, "id", "", "line ID (default line-N)")
	add.Flags().StringVar(&delivery, "delivery", "", "how to say it (notes for whoever records it)")
	add.Flags().StringVar(&after, "after", "", "put it after this line in the script (ID or number)")
	add.Flags().StringVar(&before, "before", "", "put it before this line in the script")
	add.Flags().StringVar(&addC.confirm, "confirm", "", "apply with the token from the check")
	_ = add.MarkFlagRequired("text")
	cmd.AddCommand(add)

	var updC common
	var uText, uDelivery, uState, uVoice, uVoiceName string
	var uLock bool
	upd := &cobra.Command{Use: "update <line>", Short: "Change a line's words, delivery notes or voice", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return withLine(c, &updC, func(r *models.Revision) (voiceover.LineIn, error) {
				str := func(name, v string) *string {
					if c.Flags().Changed(name) {
						return &v
					}
					return nil
				}
				return voiceover.LineIn{Action: "change", Line: args[0], Text: str("text", uText), Delivery: str("delivery", uDelivery),
					State: str("state", uState), Voice: str("voice", uVoice), VoiceName: uVoiceName, Lock: uLock}, nil
			})
		}}
	updC.rf.add(upd)
	upd.Flags().StringVar(&uText, "text", "", "new words")
	upd.Flags().StringVar(&uDelivery, "delivery", "", "delivery notes")
	upd.Flags().StringVar(&uState, "state", "", "draft | final")
	upd.Flags().StringVar(&uVoice, "voice", "", "ElevenLabs voice ID for this line only (empty = the project's voice)")
	upd.Flags().StringVar(&uVoiceName, "voice-name", "", "with --voice: the voice's name (shown with the line and on price estimates)")
	upd.Flags().BoolVar(&uLock, "lock", false, "lock the line afterwards")
	upd.Flags().StringVar(&updC.confirm, "confirm", "", "apply with the token from the check")
	cmd.AddCommand(upd)

	var mvC common
	var mf anchorFlags
	var mAfter, mBefore, mTo string
	var mExact bool
	mv := &cobra.Command{Use: "move <line>", Short: "Give a line a new start point, or move it in the script order", Args: cobra.ExactArgs(1),
		Long: `Give a line a new start point (--at-clip, --at-moment, --after-previous, --at-time), or
--to a point of the video: the line is tied to what plays there (the shot, or the
moment or previous line it already follows), so it moves with the edit; add --exact
to keep it at that clock time instead.`,
		RunE: func(c *cobra.Command, args []string) error {
			return withLine(c, &mvC, func(r *models.Revision) (voiceover.LineIn, error) {
				an, err := mf.anchor(c, &r.Spec)
				if err != nil {
					return voiceover.LineIn{}, err
				}
				in := voiceover.LineIn{Action: "move", Line: args[0], Anchor: an, After: mAfter, Before: mBefore, Exact: mExact}
				return in, toFlag(c, mTo, &in)
			})
		}}
	mvC.rf.add(mv)
	mf.add(mv)
	mv.Flags().StringVar(&mTo, "to", "", "move it to this point of the video (m:ss.s), tied to what plays there")
	mv.Flags().BoolVar(&mExact, "exact", false, "with --to: keep it at that exact clock time")
	mv.Flags().StringVar(&mAfter, "after", "", "move it after this line in the script")
	mv.Flags().StringVar(&mBefore, "before", "", "move it before this line in the script")
	mv.Flags().StringVar(&mvC.confirm, "confirm", "", "apply with the token from the check")
	cmd.AddCommand(mv)

	var rmC common
	rm := &cobra.Command{Use: "remove <line>", Short: "Remove a narration line", Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			return withLine(c, &rmC, func(r *models.Revision) (voiceover.LineIn, error) {
				return voiceover.LineIn{Action: "remove", Line: args[0]}, nil
			})
		}}
	rmC.rf.add(rm)
	rm.Flags().StringVar(&rmC.confirm, "confirm", "", "apply with the token from the check")
	cmd.AddCommand(rm)
	return cmd
}

func voImportCmd() *cobra.Command {
	var rf revFlags
	var continuous, recorded, split, applyIt bool
	cmd := &cobra.Command{Use: "import <recording>", Short: "Import a narration recording: store it, transcribe it, match it to the script and place it",
		Long: `Stores the recording in the project, transcribes it on this machine, matches its words to
the script lines and places it — split at each line by default (every piece stays at its
line's spot when you re-edit), or as one continuous piece (--continuous). The narrator's
speaking pace is measured. Prints the change to review and apply.`,
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
			r, err := getRev(c, a, rev)
			if err != nil {
				return err
			}
			assetID, err := importRecording(c, a, r.ProjectID, args[0], recorded)
			if err != nil {
				return err
			}
			layout := editspec.LayoutBeats
			if continuous && !split {
				layout = editspec.LayoutContinuous
			}
			source := models.TakeImported
			if recorded {
				source = models.TakeRecorded
			}
			out, err := ops.CallTyped[voiceover.AlignOut](c.Context(), a, "voiceover.align", voiceover.AlignIn{RevisionID: rev, AssetID: assetID, Layout: layout, Source: source, Apply: applyIt}, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return resultFlow(c, a, out, pending(out.Result, out.Applied), func() {
				fmt.Println(out.Summary)
				for _, w := range out.Warnings {
					fmt.Println("  ! " + w)
				}
			})
		}}
	rf.add(cmd)
	cmd.Flags().BoolVar(&split, "split-at-beats", true, "split the recording at each line and place every piece at its line's spot")
	cmd.Flags().BoolVar(&continuous, "continuous", false, "play the recording as one piece from the first line's spot")
	cmd.Flags().BoolVar(&recorded, "recorded", false, "mark it as your own recording (rather than an imported file)")
	cmd.Flags().BoolVar(&applyIt, "apply", false, "also put it in the cut as a new version (no separate edit apply)")
	return cmd
}

// importRecording ingests a local file (transcribing it) and waits for the transcript.
func importRecording(c *cobra.Command, a *app.App, projectID, path string, quiet bool) (string, error) {
	t := true
	b, _ := json.Marshal(ingest.FilesIn{Paths: []string{path}, ProjectID: projectID, Tags: []string{"voiceover"}, Transcribe: &t})
	res, err := ops.Call(c.Context(), a, "ingest.files", b, ops.Caller{})
	if err != nil {
		return "", failErr(c, err)
	}
	fo := res.(*ingest.FilesOut)
	if len(fo.Results) == 0 || fo.Results[0].AssetID == "" {
		ae := apperr.New(apperr.UnsupportedMedia, "The recording couldn't be added.")
		if len(fo.Results) > 0 && fo.Results[0].Error != "" {
			ae = ae.WithDetails(map[string]any{"error": fo.Results[0].Error})
		}
		return "", failErr(c, ae)
	}
	r := fo.Results[0]
	var as models.Asset
	if err := a.DB.GetByID(c.Context(), store.CAssets, a.WorkspaceID(), r.AssetID, &as); err != nil {
		return "", failErr(c, err)
	}
	if as.Transcript != nil && as.Transcript.State == "available" {
		return as.ID, nil
	}
	jobIDs := r.JobIDs
	if len(jobIDs) == 0 { // already imported earlier: make sure it is transcribed
		ab, _ := json.Marshal(ops.AnalyzeIn{AssetID: as.ID})
		if out, err := ops.Call(c.Context(), a, "footage.analyze", ab, ops.Caller{}); err == nil {
			jobIDs = out.(*ops.AnalyzeOut).Jobs
		}
	}
	if len(jobIDs) > 0 {
		if !jsonMode(c) && !quiet {
			fmt.Fprintln(os.Stderr, "Transcribing the recording on this machine…")
		}
		if _, err := waitJobs(c, a, jobIDs, 0, true); err != nil {
			return "", failErr(c, err)
		}
	}
	return as.ID, nil
}

func voScratchCmd() *cobra.Command {
	var rf revFlags
	var in voiceover.ScratchIn
	cmd := &cobra.Command{Use: "scratch", Short: "Make free scratch narration with a system voice, to check timing before paying for a voice or recording",
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if in.RevisionID, err = rf.resolve(c, a); err != nil {
				return err
			}
			out, err := ops.CallTyped[voiceover.GenerateOut](c.Context(), a, "voiceover.scratch", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return resultFlow(c, a, out, pending(out.Result, out.Applied), func() { fmt.Println(out.Summary) })
		}}
	rf.add(cmd)
	cmd.Flags().StringSliceVar(&in.Beats, "beat", nil, "only these lines (IDs or numbers)")
	cmd.Flags().StringVar(&in.Voice, "voice", "", "system voice (see: voiceover voices --provider local)")
	cmd.Flags().Float64Var(&in.WPM, "wpm", 0, "speaking rate in words a minute (default: the script's)")
	cmd.Flags().BoolVar(&in.Replace, "replace", false, "also redo lines that already have scratch audio")
	cmd.Flags().BoolVar(&in.Apply, "apply", false, "also put it in the cut as a new version (no separate edit apply)")
	return cmd
}

func voTTSCmd(estimateOnly bool) *cobra.Command {
	var rf revFlags
	var in voiceover.TTSIn
	var speed, stability float64
	var est bool
	use, short := "tts", "Generate narration with ElevenLabs (unchanged lines are reused for free; --estimate shows the cost first)"
	if estimateOnly {
		use, short = "estimate", "What ElevenLabs narration would cost (characters, credits, budget) — nothing is generated"
	}
	cmd := &cobra.Command{Use: use, Short: short,
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if in.RevisionID, err = rf.resolve(c, a); err != nil {
				return err
			}
			if c.Flags().Changed("speed") {
				in.Speed = &speed
			}
			if c.Flags().Changed("stability") {
				in.Stability = &stability
			}
			if estimateOnly || est {
				return runOpWith(c, a, "voiceover.estimate", in, ops.Caller{}, func(o any) {
					e := o.(*voiceover.EstimateOut)
					fmt.Println(e.Summary)
					if e.Voice.VoiceName != "" || e.Voice.ModelName != "" {
						fmt.Printf("Voice: %s · model: %s\n", firstNonEmptyStr(e.Voice.VoiceName, "not chosen"), firstNonEmptyStr(e.Voice.ModelName, "automatic"))
					}
					if b := e.Budget; b != nil && b.LimitCredits != nil {
						fmt.Printf("Project budget: %.0f of %.0f credits used\n", b.UsedCredits, *b.LimitCredits)
					}
					if e.AccountRemaining != nil {
						fmt.Printf("Your ElevenLabs plan has %d characters left this period.\n", *e.AccountRemaining)
					}
					for _, w := range e.Warnings {
						fmt.Println("! " + w)
					}
					printCost(e.Cost, detailsMode(c))
				})
			}
			// The estimate first (D208): a person at a terminal sees what the AI
			// voice costs and says yes before anything is spent.
			if estimateFirstHere(c, a) {
				e, err := ops.CallTyped[voiceover.EstimateOut](c.Context(), a, "voiceover.estimate", in, ops.Caller{})
				if err != nil {
					return failErr(c, err)
				}
				if e.Characters > 0 && e.Allowed {
					fmt.Println(e.Summary)
					printCost(e.Cost, detailsMode(c))
					if !askYes("Make it for "+e.Cost.About, false) {
						return fail(c, apperr.Canceled, "Nothing was made or spent.", "")
					}
				}
			}
			out, err := ops.CallTyped[voiceover.GenerateOut](c.Context(), a, "voiceover.tts", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return resultFlow(c, a, out, pending(out.Result, out.Applied), func() {
				fmt.Println(out.Summary)
				for _, w := range out.Warnings {
					fmt.Println("! " + w)
				}
			})
		}}
	rf.add(cmd)
	f := cmd.Flags()
	f.StringSliceVar(&in.Beats, "beat", nil, "only these lines (IDs or numbers)")
	f.StringVar(&in.Voice, "voice", "", "voice ID for this run (default: the project's or team's voice)")
	f.StringVar(&in.ModelID, "model", "", "model ID for this run")
	f.Float64Var(&speed, "speed", 1, "0.7 slower … 1.2 faster")
	f.Float64Var(&stability, "stability", 0.5, "0 more expressive … 1 steadier")
	if !estimateOnly {
		f.BoolVar(&est, "estimate", false, "only show what it would cost")
		f.BoolVar(&in.Regenerate, "regenerate", false, "generate again even when an identical take exists (bills again)")
		f.BoolVar(&in.Apply, "apply", false, "also put it in the cut as a new version (no separate edit apply)")
	}
	return cmd
}

func voVoicesCmd() *cobra.Command {
	var in voiceover.VoicesIn
	var previewVoice, out string
	cmd := &cobra.Command{Use: "voices", Short: "List voices to choose from (ElevenLabs, its public library, or free local voices); --preview saves a sample",
		RunE: func(c *cobra.Command, _ []string) error {
			if previewVoice != "" {
				return runOp(c, "voiceover.preview", voiceover.PreviewIn{Provider: in.Provider, Voice: previewVoice, Library: in.Library, Out: out}, func(o any) {
					p := o.(*voiceover.PreviewOut)
					fmt.Printf("Saved a sample of the voice: %s\n", p.Path)
				})
			}
			return runOp(c, "voiceover.voices", in, func(o any) {
				r := o.(*voiceover.VoicesOut)
				for _, v := range r.Voices {
					star := " "
					if v.Favorite {
						star = "★"
					}
					if detailsMode(c) {
						fmt.Printf("%s %-24s %-26s %s\n", star, trunc(v.Name, 24), trunc(v.ID, 26), v.Traits)
					} else {
						fmt.Printf("%s %-24s %s\n", star, trunc(v.Name, 24), v.Traits)
					}
				}
				if len(r.Voices) == 0 {
					fmt.Println("No voices match.")
				}
				if r.Note != "" {
					fmt.Println("\n" + r.Note)
				}
			})
		}}
	f := cmd.Flags()
	f.StringVar(&in.Provider, "provider", "elevenlabs", "elevenlabs | local")
	f.StringVar(&in.Search, "search", "", "search by name, accent, style…")
	f.BoolVar(&in.Library, "library", false, "search ElevenLabs' public voice library")
	f.StringVar(&in.PageToken, "page", "", "next page (from a previous --json result)")
	f.StringVar(&previewVoice, "preview", "", "save a sample of this voice (ID, or name for local voices)")
	f.StringVar(&out, "out", "", "with --preview: where to save it")
	return cmd
}

func voModelsCmd() *cobra.Command {
	return &cobra.Command{Use: "models", Short: "ElevenLabs models this account can use: languages, longest line, cost per character",
		RunE: func(c *cobra.Command, _ []string) error {
			return runOp(c, "voiceover.models", ops.EmptyIn{}, func(o any) {
				r := o.(*voiceover.ModelsOut)
				for _, m := range r.Models {
					d := " "
					if m.ModelID == r.Default {
						d = "*"
					}
					if detailsMode(c) {
						fmt.Printf("%s %-28s %-26s %2d languages  %.2g credits/char\n", d, trunc(m.Name, 28), m.ModelID, len(m.LanguageCodes), m.CostMultiplier)
					} else {
						fmt.Printf("%s %-28s %2d languages  %.2g credits a character\n", d, trunc(m.Name, 28), len(m.LanguageCodes), m.CostMultiplier)
					}
				}
				fmt.Println("\n* = used when none is chosen. " + r.Note)
			})
		}}
}

func voTakesCmd() *cobra.Command {
	cmd := &cobra.Command{Use: "takes", Short: "List the takes for each line, and choose which one plays"}
	var rf revFlags
	var beat string
	list := &cobra.Command{Use: "list", Short: "Takes made or found for the narration lines",
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			in := voiceover.TakesIn{Beat: beat, ProjectID: rf.project}
			if rf.revision != "" || rf.project != "" {
				if in.RevisionID, err = rf.resolve(c, a); err != nil {
					return err
				}
			}
			return runOpWith(c, a, "voiceover.takes", in, ops.Caller{}, func(o any) {
				for _, t := range o.(*voiceover.TakesOut).Takes {
					mark := " "
					if t.Chosen {
						mark = "▶"
					}
					if detailsMode(c) {
						fmt.Printf("%s %-10s %-28s %s  %s\n", mark, t.BeatID, t.ID, t.CreatedAt.Local().Format("Jan 2 15:04"), t.Summary)
					} else {
						fmt.Printf("%s %-10s %s  %s\n", mark, t.BeatID, t.CreatedAt.Local().Format("Jan 2 15:04"), t.Summary)
					}
				}
			})
		}}
	rf.add(list)
	list.Flags().StringVar(&beat, "beat", "", "only this line (ID or number)")
	cmd.AddCommand(list)

	var rf2 revFlags
	var cBeat, take, confirm string
	var original, processed bool
	choose := &cobra.Command{Use: "choose --beat <line> (--take <id> | --original | --processed)", Short: "Play another take on a line, or switch between the original and cleaned-up version",
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			rev, err := rf2.resolve(c, a)
			if err != nil {
				return err
			}
			in := voiceover.ChooseIn{RevisionID: rev, Beat: cBeat, TakeID: take}
			switch {
			case original:
				in.Version = "original"
			case processed:
				in.Version = "processed"
			}
			res, err := ops.CallTyped[voiceover.PlanResult](c.Context(), a, "voiceover.choose", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return planFlow(c, a, res.Plan, confirm, nil, nil)
		}}
	rf2.add(choose)
	choose.Flags().StringVar(&cBeat, "beat", "", "the line (ID or number)")
	choose.Flags().StringVar(&take, "take", "", "take ID (from takes list)")
	choose.Flags().BoolVar(&original, "original", false, "play the original recording")
	choose.Flags().BoolVar(&processed, "processed", false, "play the cleaned-up version")
	choose.Flags().StringVar(&confirm, "confirm", "", "apply with the token from the check")
	_ = choose.MarkFlagRequired("beat")
	cmd.AddCommand(choose, voTakePlaceCmd())
	cmd.Aliases = []string{"take"}
	return cmd
}

// voTakePlaceCmd places a live take (D85): a recording made while the video
// played lands where it was spoken; an earlier take goes back where it was
// spoken (--at-recorded) or each line to its planned spot (--planned).
func voTakePlaceCmd() *cobra.Command {
	var rf revFlags
	var from, until, confirm string
	var lines []string
	var planned, recorded, applyIt bool
	cmd := &cobra.Command{Use: "place <take|recording>", Short: "Put a live take where it was spoken (or each line at its planned spot); a recording needs --from, where on the video it started",
		Long: `A live take is one recording made while the video played. Placing it keeps the whole
recording, finds each script line in it, cuts it only in the pauses between lines and
starts every line where it was said (tied to the shot playing there, to the millisecond),
so the narration plays exactly as recorded and moves with its shots when the cut changes.
Lines not heard lose their old sound and follow the line before (flagged); only the
stretch the take covers is replaced (--until ends it earlier: a re-recorded range).

  metarush voiceover take place narration.wav --from 0:12.4 --revision <id>   # a recording (or ast_…)
  metarush voiceover take place rd_… --planned --revision <id>                # each line at its planned spot
  metarush voiceover take place rd_… --at-recorded --revision <id>            # back where you said them`,
		Args: cobra.ExactArgs(1),
		RunE: func(c *cobra.Command, args []string) error {
			if planned && recorded {
				return fail(c, apperr.InvalidInput, "choose --planned or --at-recorded", "")
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			rev, err := rf.resolve(c, a)
			if err != nil {
				return err
			}
			in := voiceover.PlaceIn{RevisionID: rev, Lines: lines, Apply: applyIt}
			if planned {
				in.Where = "planned"
			}
			ref := args[0]
			switch {
			case ids.Valid(models.ReadPrefix, ref) || ids.Valid(models.VoiceTakePrefix, ref):
				in.Read = ref
			default:
				t, ok := editspec.ParseClock(from)
				if !ok {
					return fail(c, apperr.InvalidInput, "say where on the video the recording starts: --from m:ss.s", "")
				}
				in.From = &t
				if ids.Valid(ids.Asset, ref) {
					in.AssetID = ref
				} else {
					r, err := getRev(c, a, rev)
					if err != nil {
						return err
					}
					if in.AssetID, err = importRecording(c, a, r.ProjectID, ref, false); err != nil {
						return err
					}
				}
				in.Source = models.TakeRecorded
			}
			if until != "" {
				t, ok := editspec.ParseClock(until)
				if !ok {
					return fail(c, apperr.InvalidInput, "could not read --until (use m:ss.s)", "")
				}
				in.Until = &t
			}
			out, err := ops.CallTyped[voiceover.PlaceOut](c.Context(), a, "voiceover.place", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			human := func() { printPlaced(out) }
			if confirm != "" && out.Result != nil {
				return planFlow(c, a, out.Result.Plan, confirm, map[string]any{"placed": out}, human)
			}
			return resultFlow(c, a, out, pending(out.Result, out.Applied), human)
		}}
	rf.add(cmd)
	cmd.Flags().StringVar(&from, "from", "", "a recording: where on the video it starts (m:ss.s), the playhead when recording began")
	cmd.Flags().StringVar(&until, "until", "", "replace only up to this point of the video (a re-recorded range)")
	cmd.Flags().BoolVar(&recorded, "at-recorded", false, "each line where it was spoken (the default)")
	cmd.Flags().BoolVar(&planned, "planned", false, "an earlier take: each line at the spot it was planned for")
	cmd.Flags().StringSliceVar(&lines, "lines", nil, "only these lines (numbers or IDs)")
	cmd.Flags().BoolVar(&applyIt, "apply", false, "also put it in the cut as a new version (no separate edit apply)")
	cmd.Flags().StringVar(&confirm, "confirm", "", "apply with the token from the check")
	return cmd
}

// printPlaced says where each line of a take landed, in plain words.
func printPlaced(o *voiceover.PlaceOut) {
	fmt.Println(o.Summary)
	for _, l := range o.Lines {
		switch l.State {
		case voiceover.PlacedLineIn:
			s := fmt.Sprintf("  ✓ %2d. %s  starts %s", l.Line, editspec.Clock(l.At), l.Placement)
			if l.Moved != nil && (*l.Moved >= 0.05 || *l.Moved <= -0.05) {
				dir := "later"
				if *l.Moved < 0 {
					dir = "earlier"
				}
				s += fmt.Sprintf(" (%.1f s %s than planned)", absf(*l.Moved), dir)
			}
			fmt.Println(s)
		case voiceover.PlacedNotHeard, voiceover.PlacedTrimmed:
			fmt.Printf("  ! %2d. %s\n", l.Line, l.Message)
		}
		if l.State == voiceover.PlacedLineIn && l.Message != "" {
			fmt.Println("       " + l.Message)
		}
	}
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func voCleanupCmd() *cobra.Command {
	var rf revFlags
	var in voiceover.CleanupIn
	var keep, list bool
	cmd := &cobra.Command{Use: "cleanup", Short: "Clean up recorded narration (noise, rumble, harsh s, uneven level) — the original is kept for A/B",
		RunE: func(c *cobra.Command, _ []string) error {
			if list {
				rows := []map[string]string{}
				for _, p := range voiceover.Presets {
					rows = append(rows, map[string]string{"name": p.Name, "label": p.Label, "summary": p.Summary})
				}
				return emit(c, rows, func() {
					for _, p := range voiceover.Presets {
						fmt.Printf("%-16s %s — %s\n", p.Name, p.Label, p.Summary)
					}
				})
			}
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if in.RevisionID, err = rf.resolve(c, a); err != nil {
				return err
			}
			if keep {
				f := false
				in.Use = &f
			}
			out, err := ops.CallTyped[voiceover.CleanupOut](c.Context(), a, "voiceover.cleanup", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			return resultFlow(c, a, out, pending(out.Result, out.Applied), func() { fmt.Println(out.Summary) })
		}}
	rf.add(cmd)
	cmd.Flags().StringSliceVar(&in.Beats, "beat", nil, "only these lines (IDs or numbers)")
	cmd.Flags().StringVar(&in.Preset, "preset", "voice-clean", "which kind of clean-up (see --list)")
	cmd.Flags().BoolVar(&keep, "keep-original", false, "make the cleaned-up version but keep the original playing")
	cmd.Flags().BoolVar(&list, "list", false, "list the kinds of clean-up")
	cmd.Flags().BoolVar(&in.Apply, "apply", false, "also put it in the cut as a new version (no separate edit apply)")
	return cmd
}

func voFitCmd() *cobra.Command {
	var rf revFlags
	var in voiceover.FitIn
	cmd := &cobra.Command{Use: "fit", Short: "Check that every line fits before the next one; --apply builds the fix (it is saved only when you confirm it)",
		Long: `Checks every narration line against the time it has (until the next line). For lines that
run over it suggests fixes: hold the shot longer, play AI speech up to 8% faster, start a
little earlier, move the next line — or shorten the words. --apply preferred uses the fix
MetaRush prefers (recordings: hold the shot; AI voices: speed up); lines that need shorter
words are listed for rewriting.`,
		RunE: func(c *cobra.Command, _ []string) error {
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			if in.RevisionID, err = rf.resolve(c, a); err != nil {
				return err
			}
			out, err := ops.CallTyped[voiceover.FitOut](c.Context(), a, "voiceover.fit", in, ops.Caller{})
			if err != nil {
				return failErr(c, err)
			}
			human := func() {
				fmt.Println(out.Summary)
				for _, l := range out.Lines {
					if l.Status != "over" && l.Status != "unresolved" {
						continue
					}
					fmt.Println("! " + l.Message)
					for _, fx := range l.Fixes {
						pref := " "
						if fx.Strategy == l.Preferred {
							pref = "→"
						}
						fmt.Printf("   %s %s\n", pref, fx.Summary)
					}
				}
				for _, r := range out.Rewrite {
					fmt.Printf("Rewrite line %d to fit (about %d words).\n", r.Line, targetWords(r))
				}
			}
			return resultFlow(c, a, out, out.Result, human)
		}}
	rf.add(cmd)
	cmd.Flags().StringVar(&in.Strategy, "apply", "", "build the fix: preferred | extend-shot | speed-up | start-earlier | move-next")
	cmd.Flags().StringSliceVar(&in.Beats, "beat", nil, "only these lines")
	return cmd
}

func targetWords(f editspec.BeatFit) int {
	for _, fx := range f.Fixes {
		if fx.Strategy == editspec.FixShorten {
			return fx.TargetWords
		}
	}
	return f.Words
}

func voCloneCmd() *cobra.Command {
	var in voiceover.CloneIn
	var notOwn bool
	cmd := &cobra.Command{Use: "clone --name <name> --sample <asset>... --consent \"<statement>\"",
		Short: "Make an ElevenLabs voice from recordings of your own voice (people only; anyone else needs their recorded consent)",
		Long: `Creates an instant voice clone in your ElevenLabs account from clean recordings of your
own voice. You must type a consent statement in your own words; it is kept in the audit log.
MetaRush never clones someone else's voice unless --consent-recording names an audio or video
of that person saying they agree. Everything made with a cloned voice is marked as an AI voice.`,
		RunE: func(c *cobra.Command, _ []string) error {
			if !isInteractive() {
				return fail(c, apperr.HumanOnly, "voice cloning must be done by a person in a terminal", "run this command yourself")
			}
			in.OwnVoice = !notOwn
			a, err := openApp(c, "cli")
			if err != nil {
				return err
			}
			defer a.Close()
			return runOpWith(c, a, "voiceover.clone", in, ops.Caller{Human: true}, func(o any) { fmt.Println(o.(*voiceover.CloneOut).Summary) })
		}}
	f := cmd.Flags()
	f.StringVar(&in.Name, "name", "", "name for the new voice")
	f.StringSliceVar(&in.Samples, "sample", nil, "audio asset IDs with clean speech (import them first)")
	f.StringVar(&in.Consent, "consent", "", "your consent statement, in your words")
	f.StringVar(&in.Speaker, "speaker", "", "whose voice it is (default: you)")
	f.BoolVar(&notOwn, "not-my-voice", false, "the samples are someone else's voice (needs --consent-recording)")
	f.StringVar(&in.ConsentFrom, "consent-recording", "", "audio/video asset of that person stating their consent")
	f.StringVar(&in.Description, "description", "", "description in your ElevenLabs account")
	f.BoolVar(&in.RemoveNoise, "remove-background-noise", false, "ask ElevenLabs to remove background noise from the samples")
	f.StringVar(&in.ProjectID, "project", "", "also use the new voice for this project")
	_ = cmd.MarkFlagRequired("name")
	_ = cmd.MarkFlagRequired("consent")
	return cmd
}

package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/voiceover"
)

// Fitting a script to the video (D86). The person's local Claude Code does it
// through this runner, read-only: it reads the cut (shots, transcripts, frames,
// the narrator's pace), writes a fitted version in its proposals folder and asks
// to use it with `voiceover script fit --propose`, which becomes a card. The
// card shows both versions side by side (and links to the Voiceover tab's
// review); the person takes Claude's lines one by one, and the original script
// is kept whatever they choose.

// AskFitIn asks Claude Code to fit a cut's script to the video.
type AskFitIn struct {
	RevisionID string `json:"revisionId" jsonschema:"pattern=^rev_[0-9a-z]{26}$"`
	Model      string `json:"model,omitempty" jsonschema:"maxLength=80,description=claude --model (default: the installation's)."`
	Effort     string `json:"effort,omitempty" jsonschema:"enum=,enum=default,enum=low,enum=medium,enum=high,enum=xhigh,enum=max"`
	// Note is anything the person wants Claude to know ("keep it short", "the
	// drone shot is the payoff").
	Note string `json:"note,omitempty" jsonschema:"maxLength=1000"`
}

func init() {
	ops.Def("voiceover.askFit", "Ask your local Claude Code (read-only) to fit the script to the video: it proposes a version with lines tied to the right moments and trimmed to fit, every change explained; you review it line by line and the original is kept", ops.Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *AskFitIn) (*ConversationSendOut, error) {
			return For(a).AskFit(ctx, a, *in)
		})
}

// AskFit starts a conversation that fits the script of a cut to the video.
func (m *Manager) AskFit(ctx context.Context, a *app.App, in AskFitIn) (*ConversationSendOut, error) {
	rev := m.revisionFor(ctx, &cliCall{Flags: map[string]string{"revision": in.RevisionID}})
	if rev == nil {
		return nil, apperr.New(apperr.NotFound, "That version of the cut isn't in this workspace.").WithDetails(map[string]any{"revisionId": in.RevisionID})
	}
	if rev.Spec.Voiceover == nil || len(rev.Spec.Voiceover.Beats) == 0 {
		return nil, apperr.New(apperr.InvalidInput, "This cut has no narration script to fit yet.").WithHint("Paste or write a script on the Voiceover tab first.").
			WithCommand(fmt.Sprintf("metarush voiceover script import <file> --revision %s", rev.ID))
	}
	if st := m.Status(ctx, false); !st.Available {
		return nil, apperr.New(apperr.NotConfigured, "Claude Code can't run on this computer (%s).", strings.TrimRight(firstLine(st.Reason), ".")).
			WithHint(FitInTerminal)
	}
	// Fitting reads the cut's transcripts and frames (What may leave, D290).
	if err := leave.Check(ctx, a, leave.Send{Place: models.PlaceClaude, To: m.terms(), ToWords: "Claude", What: "the cut's transcripts and frames, to fit its script",
		Kinds: []string{models.LeaveWords}, Projects: []string{rev.ProjectID}}); err != nil {
		return nil, err
	}
	return m.Ask(ctx, AssistantAskIn{AssistantMessageIn: AssistantMessageIn{Prompt: FitPrompt(rev, in.Note), ProjectID: rev.ProjectID, RevisionID: rev.ID},
		Model: in.Model, Effort: in.Effort, Title: "Fit the script to the video"})
}

// FitInTerminal says how to fit a script in a terminal Claude Code session.
const FitInTerminal = "In a terminal Claude Code session, ask “fit my script to the video”: Claude writes the fitted version and shows it to you before anything changes"

// FitPrompt is the request for a fitted script (the dashboard's button, the
// CLI's headless run and the skill ask for the same thing).
func FitPrompt(rev *models.Revision, note string) string {
	v := rev.Spec.Voiceover
	var b strings.Builder
	fmt.Fprintf(&b, "Fit the narration script of this cut to the video (revision %s). The person wrote the script: keep their voice and style.\n\n", rev.ID)
	fmt.Fprintf(&b, "1. Read the cut and the script: `metarush voiceover script show --revision %s --json` (each line, where it starts, how long it takes, the narrator's pace), `metarush revision show %s --json` (the shots in order, with their timing and labels), the transcripts of the footage (`metarush footage transcript <asset> --json`), and smart sheets or frames (`metarush footage sheet`, `metarush footage frames`) wherever you need to see what is on screen.\n", rev.ID, rev.ID)
	b.WriteString("2. For each line of the person's script, in order, decide:\n")
	b.WriteString("   - where it starts: tied to the shot or moment it talks about, as an anchor ({\"kind\":\"clip\",\"clipId\":\"…\",\"offset\":0.4}, or {\"kind\":\"moment\",\"assetId\":\"…\",\"at\":12.3,\"quote\":\"…\"}), or \"at\": \"m:ss.s\" (tied to the shot playing there);\n")
	b.WriteString("   - its words: trimmed or extended to fit the time before the next line at the narrator's pace, and matched to what is on screen then. Keep the person's own words, voice and style; add nothing their script does not say unless the flow needs it;\n")
	b.WriteString("   - the reason for each change, in one short plain sentence (\"Trimmed so it ends before the cut to the garden\").\n")
	b.WriteString("3. Write the fitted script as JSON in this message's proposals folder, as fitted-script.json: {\"lines\":[{\"id\":\"line-1\",\"text\":\"…\",\"anchor\":{…},\"reason\":\"…\"}, …],\"note\":\"what you changed overall, in a sentence or two\"}. One entry per line (the id from script show), in the same order; keep a line's text as it is when only its start changes.\n")
	fmt.Fprintf(&b, "4. Run `metarush voiceover script fit --propose <that file> --revision %s --json`. The person then sees your version next to theirs, line by line, and keeps what they like; their original script is never changed.\n", rev.ID)
	b.WriteString("Change nothing else and apply nothing yourself.")
	if n := len(v.Beats); n > 0 {
		fmt.Fprintf(&b, " The script has %d lines.", n)
	}
	if note = strings.TrimSpace(note); note != "" {
		b.WriteString("\n\nThe person adds: " + clip(note, 1000))
	}
	return b.String()
}

// describeFit turns `voiceover script fit --propose <file>` into a card that
// shows Claude's version next to the person's, and `voiceover script use` into
// a card that switches versions.
func (m *Manager) describeFit(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	r := m.revisionFor(ctx, call)
	if call.Path == "voiceover script use" {
		const title = "Switch the script version"
		if r == nil {
			incomplete(a, title, "Claude did not name the cut (--revision).")
			return
		}
		ver := strings.ToLower(first(call.Args))
		if ver != "original" && ver != "fitted" {
			incomplete(a, title, "Claude did not say which version: original or fitted.")
			return
		}
		out, err := callOp[voiceover.UseOut](ctx, m, "voiceover.use", voiceover.UseIn{RevisionID: r.ID, Version: ver})
		if err != nil {
			incomplete(a, title, apperr.From(err).Message)
			return
		}
		if ver == "fitted" {
			a.Title = "Use Claude's version of the script, fitted to the video"
		} else {
			a.Title = "Go back to your own script"
		}
		a.Details = []string{"The other version is kept with its sound and places; you can switch back any time"}
		_ = out
		setInput(a, "voiceover.use", voiceover.UseIn{RevisionID: r.ID, Version: ver, Apply: true})
		return
	}
	const title = "Use Claude's fitted script"
	if r == nil {
		incomplete(a, title, "Claude did not name the cut (--revision).")
		return
	}
	file := call.Flags["propose"]
	if file == "" {
		incomplete(a, title, "Write the fitted script in your proposals folder and ask with --propose <file>; this conversation is already the fitting.")
		return
	}
	raw, why := m.proposalFile(c, file, "fitted script")
	if why != "" {
		incomplete(a, title, why)
		return
	}
	var p voiceover.FitProposal
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		incomplete(a, title, "The fitted script is not in the expected form: "+firstLine(err.Error()))
		return
	}
	out, err := callOp[voiceover.FittedOut](ctx, m, "voiceover.fitted", voiceover.FittedIn{RevisionID: r.ID, Proposal: &p})
	if err != nil {
		incomplete(a, title, apperr.From(err).Message)
		return
	}
	n := len(out.Lines)
	a.Title = fmt.Sprintf("Use Claude's fitted script: %s of %d changed", pluralize(out.Changed, "line"), n)
	if out.Changed == 0 {
		a.Title = "Claude's fitted script changes nothing"
	}
	a.Details = []string{"Your original script is kept; you can switch back any time", "Review it line by line and keep your own words where you prefer"}
	for _, w := range out.Warnings {
		a.Details = append(a.Details, w)
	}
	a.Rationale = clip(out.Note, 1000)
	fit := *out
	fit.Result, fit.Applied = nil, nil
	a.Fit = &fit
	a.Review = links.VoiceoverFit(r.ProjectID, c.id, a.ID)
	if out.Result != nil && out.Result.Check != nil && !out.Result.Check.Valid {
		msg := "This version can't be used as it is"
		if len(out.Result.Check.Errors) > 0 {
			msg += ": " + out.Result.Check.Errors[0].Message
		}
		unavailable(a, a.Title, msg)
		return
	}
	setInput(a, "voiceover.fitted", voiceover.FittedIn{RevisionID: r.ID, Proposal: &p, Apply: true})
}

// withKeep applies the person's line choices to a fitted-script card's input.
func withKeep(input json.RawMessage, keep []string) json.RawMessage {
	var fi voiceover.FittedIn
	if json.Unmarshal(input, &fi) != nil {
		return input
	}
	fi.Keep = keep
	b, err := json.Marshal(fi)
	if err != nil {
		return input
	}
	return b
}

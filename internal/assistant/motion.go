package assistant

import (
	"context"
	"path/filepath"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/links"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/motion"
	"github.com/metavert-io/metarush/internal/ops"
)

// Project motion graphics in the Claude Code panel (D68). Claude may write a
// component in its proposals folder; checking it (which runs the component's
// code in the network-jailed headless browser and records the version) is a
// card the person runs, and approving a version is a people-only card that
// links to the review page. Claude never approves: the assistant channel
// refuses human-only operations, whatever the card says.

// describeMotionCheck prepares "check the motion graphic Claude wrote": the
// folder must be in this conversation's proposals.
func (m *Manager) describeMotionCheck(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	if v := strings.TrimSpace(call.Flags["version"]); v != "" && first(call.Args) == "" {
		m.describeMotionRedraw(ctx, c, call, a, v)
		return
	}
	path := first(call.Args)
	if path == "" {
		incomplete(a, "Check a motion graphic", "Claude did not name the component's folder.")
		return
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.workspaceDir(), path)
	}
	real := func(p string) string {
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			return rp
		}
		return p
	}
	root := real(filepath.Join(m.convDir(c.id), "proposals"))
	dir := real(path)
	rel, err := filepath.Rel(root, dir)
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		incomplete(a, "Check a motion graphic", "The component is not in this conversation's proposals folder.")
		return
	}
	name := filepath.Base(dir)
	if !motion.ValidName(name) {
		incomplete(a, "Check a motion graphic", "The component's folder name can't name a motion graphic (use lowercase letters, digits and dashes).")
		return
	}
	_, err = motion.Build(dir) // it must read as a graphic before it is offered
	if err != nil {
		incomplete(a, "Check the motion graphic “"+name+"”", "MetaRush can't read it: "+firstLine(err.Error()))
		return
	}
	a.Title = "Check the graphic " + theirs(name) + " Claude made"
	a.Details = []string{"Draws test frames and a short preview, with no internet access", "Nothing is approved yet: you decide after you have seen it"}
	// The conversation's project: its cuts decide the shapes the test frames
	// are drawn in (D164), even when Claude didn't name it.
	project := call.Flags["project"]
	if project == "" {
		project = c.projectID()
	}
	in := ops.MotionCheckIn{Dir: dir, ProjectID: project, Shape: call.Flags["shape"], NoPreview: hasBool(call, "no-preview")}
	setInput(a, "motion.check", in)
}

// describeMotionRedraw prepares drawing a version checked here in more shapes
// (D377, D381): the same code, drawn again in the jailed browser like any
// check. It only adds test frames and previews, so it starts by itself as a
// safe step and Auto runs it; it never approves anything — a person's
// approval in the new shape is what clears a cut's note.
func (m *Manager) describeMotionRedraw(ctx context.Context, c *conv, call *cliCall, a *AssistantAction, ref string) {
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	comp, err := motion.Resolve(cctx, m.App.DB, m.App.WorkspaceID(), ref)
	if err != nil {
		incomplete(a, "Draw a motion graphic in another shape", firstLine(apperr.From(err).Message))
		return
	}
	if comp.Kind == models.MotionKindLibrary {
		incomplete(a, "Draw the shared library "+theirs(comp.Name), "A shared library draws nothing of its own; the graphics that use it are drawn instead.")
		return
	}
	shape := strings.TrimSpace(call.Flags["shape"])
	words := "in the shapes of the cuts that use it"
	if shape != "" {
		sh, ok := editspec.ParseShape(strings.ToLower(shape))
		if !ok {
			incomplete(a, "Draw the graphic "+theirs(comp.Name)+" in another shape", "Claude named a shape MetaRush doesn't know: say vertical, landscape, square or a size like 1080x1350.")
			return
		}
		words = sh.Word + " (" + sh.Ratio + ")"
	}
	a.Title = "Draw the graphic " + theirs(comp.Name) + " " + words
	a.Details = []string{"The same version, drawn again with no internet access: its code doesn't change, and the frames it has stay as they are",
		"It doesn't approve anything: a person looks at the new frames and approves it in that shape"}
	project := call.Flags["project"]
	if project == "" {
		project = c.projectID()
	}
	setInput(a, "motion.check", ops.MotionCheckIn{Version: comp.Name + "@" + comp.Hash, Shape: shape, ProjectID: project, NoPreview: hasBool(call, "no-preview")})
}

// describeMotionDecision prepares approving, turning down or withdrawing a
// version: people only, after looking at its review page.
func (m *Manager) describeMotionDecision(ctx context.Context, call *cliCall, a *AssistantAction) {
	ref := first(call.Args)
	if ref == "" {
		incomplete(a, "Approve a motion graphic", "Claude did not name the version (name@version).")
		return
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	comp, err := motion.Resolve(cctx, m.App.DB, m.App.WorkspaceID(), ref)
	if err != nil {
		incomplete(a, "Approve a motion graphic", firstLine(err.Error()))
		return
	}
	r, _ := motion.ParseRef(ref)
	if r.Hash == "" {
		incomplete(a, "Approve the motion graphic “"+comp.Name+"”", "Approval is for one exact version: Claude has to name it (name@version).")
		return
	}
	a.HumanOnly = true
	reject := hasBool(call, "reject")
	op := "motion.approve"
	switch {
	case call.Path == "motion revoke":
		a.Title, op = "Withdraw approval of the graphic "+theirs(comp.Name), "motion.revoke"
		a.Details = []string{"New renders of cuts that use it are refused until a person approves it again"}
	case reject:
		a.Title = "Turn down the graphic " + theirs(comp.Name)
	default:
		a.Title = "Approve the graphic " + theirs(comp.Name)
		a.Details = []string{"Only people approve graphics. Claude suggested it; the decision is yours. Look at its frames and preview first."}
		if shapes := shapeFlags(call); len(shapes) > 0 {
			var ws []string
			for _, k := range shapes {
				if sh, ok := editspec.ParseShape(strings.ToLower(k)); ok {
					ws = append(ws, sh.Word+" ("+sh.Ratio+")")
				}
			}
			a.Title += " in " + joinWords(ws)
		} else if comp.State == models.MotionApproved {
			incomplete(a, a.Title, "This exact version is already approved.")
			return
		}
	}
	if comp.Summary != "" {
		a.Details = append(a.Details, "“"+clip(comp.Summary, 200)+"”")
	}
	if n := len(comp.Warnings); n > 0 {
		a.Details = append(a.Details, upperFirst(pluralize(n, "thing"))+" from the check to look at: "+lowerFirst(motionProblemWords(comp.Warnings[0])))
	}
	a.Review = links.Motion(comp.Hash)
	m.motionPictures(ctx, a, comp.Hash) // its test frames, contact sheet and preview, large on click
	a.Rationale = clip(strings.TrimSpace(call.Flags["note"]), 1000)
	in := ops.MotionDecideIn{Ref: comp.Name + "@" + comp.Hash, Reject: reject && op == "motion.approve", Note: clip(call.Flags["note"], 2000)}
	if op == "motion.approve" && !reject {
		// The approval covers exactly the shapes this card shows (D384): its
		// frames are the ones drawn now, so a shape drawn after the card was
		// made (by a safe step or Auto) is never approved unseen.
		if in.Shapes = shapeFlags(call); len(in.Shapes) == 0 {
			in.Shapes = comp.DrawnSizes()
		}
	}
	setInput(a, op, in)
}

// shapeFlags are the --shape values Claude gave (repeatable, or comma-separated).
func shapeFlags(call *cliCall) []string {
	vals := call.Multi["shape"]
	if len(vals) == 0 && call.Flags["shape"] != "" {
		vals = []string{call.Flags["shape"]}
	}
	var out []string
	for _, v := range vals {
		for _, p := range strings.Split(v, ",") {
			if p = strings.TrimSpace(p); p != "" {
				out = append(out, p)
			}
		}
	}
	return out
}

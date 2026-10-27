package assistant

// Cut sheet proposals. Claude can propose a change as a cut sheet (the cut as
// Markdown, docs/cut-sheet.md): it writes <name>.cut.md in the turn's
// proposals folder and checks it with `metarush cut check`. Each sheet
// becomes an action card, checked here with cut.check: the card lists the
// changes in plain sentences and carries the changed sheet lines for the
// dashboard's diff view. Saving is the person's click (cut.apply on the human
// channel, with the proposal's origin); Claude is told the outcome like for
// any card. Asking to run `metarush cut apply <file>` (refused for Claude)
// makes the same card.

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/ops"
)

// maxSheetLines caps the diff lines a card carries (a sheet is a few hundred
// lines; anything bigger is shown as changes only).
const maxSheetLines = 4000

func sheetName(file string) string {
	b := strings.TrimSuffix(filepath.Base(file), ".cut.md")
	return strings.ReplaceAll(strings.ReplaceAll(b, "-", " "), "_", " ")
}

// proposalRef is a path's ref inside the conversation's proposals folder
// (t<turn>/<file>), or "" when it is not in there.
func (m *Manager) proposalRef(c *conv, path string) string {
	if !filepath.IsAbs(path) {
		path = filepath.Join(m.workspaceDir(), path)
	}
	real := func(p string) string {
		if rp, err := filepath.EvalSymlinks(p); err == nil {
			return rp
		}
		return p
	}
	rel, err := filepath.Rel(real(filepath.Join(m.convDir(c.id), "proposals")), real(path))
	if err != nil || strings.HasPrefix(rel, "..") || filepath.IsAbs(rel) {
		return ""
	}
	return filepath.ToSlash(rel)
}

// describeCutApply is the card for `metarush cut apply <file>`.
func (m *Manager) describeCutApply(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	path := first(call.Args)
	raw, why := m.proposalFile(c, path, "cut sheet")
	if why != "" {
		incomplete(a, "Save a cut sheet", why)
		return
	}
	instruction := strings.TrimSpace(call.Flags["message"])
	if _, short := call.Flags["m"]; instruction == "" && short && len(call.Args) > 1 {
		instruction = strings.TrimSpace(call.Args[1]) // -m "…" (a short flag's value arrives as an argument)
	}
	m.sheetCard(ctx, c, a, m.proposalRef(c, path), raw, clip(instruction, 4000), hasBool(call, "allow-unlock"))
}

// sheetCard fills a card that saves a cut sheet proposal.
func (m *Manager) sheetCard(ctx context.Context, c *conv, a *AssistantAction, ref string, raw []byte, instruction string, allowUnlock bool) {
	sheet := string(raw)
	origin := Origin(c.id, ref)
	a.Kind, a.Proposal = "cut apply", ref
	parsed, _ := cutsheet.Parse(sheet, cutsheet.ParseOptions{})
	title := instruction
	if title == "" && parsed != nil {
		title = strings.TrimSpace(parsed.Request)
	}
	if title == "" {
		title = upperFirst(sheetName(ref))
	}
	if parsed != nil && strings.TrimSpace(parsed.Why) != "" {
		a.Rationale = clip(strings.TrimSpace(parsed.Why), 1000)
	}
	// A title, not a sentence: "Save the cut: Start the second line later".
	a.Title = "Save the cut: " + clip(strings.TrimSuffix(strings.TrimSpace(title), "."), 120)
	// The new version records what was asked: -m, else the sheet's change:
	// (cut.apply reads it), else what the person typed for this turn.
	if instruction == "" && (parsed == nil || strings.TrimSpace(parsed.Request) == "") {
		instruction = c.askedFor(a.Turn)
	}
	if allowUnlock {
		a.HumanOnly = true
		a.Details = append(a.Details, "Includes changes to locked parts of the cut")
	}
	chk, err := callOp[ops.CutCheckOut](ctx, m, "cut.check", ops.CutCheckIn{Sheet: sheet, Origin: origin, Instruction: instruction, AllowUnlock: allowUnlock})
	if err != nil {
		a.Details = append(a.Details, "Could not check it: "+firstLine(apperr.From(err).Message))
	} else {
		chk.ConfirmToken, chk.ExpiresAt, chk.Next, chk.Spec, chk.Plan = "", nil, "", nil, nil
		a.SheetCheck = chk
		for i, ch := range chk.Changes {
			if i == 4 {
				a.Details = append(a.Details, fmt.Sprintf("and %s", pluralize(len(chk.Changes)-4, "more change")))
				break
			}
			a.Details = append(a.Details, plainTimes(ch))
		}
		errs := 0
		for _, p := range chk.Problems {
			if p.Severity == "error" {
				errs++
			}
		}
		switch {
		case errs > 0:
			a.Details = append(a.Details, pluralize(errs, "problem")+" to fix first")
		case chk.NoChanges:
			a.Details = append(a.Details, "Nothing changes: it is the same cut as the current version")
		case chk.Rebased:
			a.Details = append(a.Details, "Someone saved a newer version meanwhile; this change doesn't overlap it, so both are kept")
		}
		if d, err := callOp[ops.CutDiffOut](ctx, m, "cut.diff", ops.CutDiffIn{Sheet: sheet}); err == nil && d.Base != nil && len(d.Base.Lines) <= maxSheetLines {
			a.SheetLines = d.Base.Lines
		}
	}
	setInput(a, "cut.apply", ops.CutApplyIn{Sheet: sheet, Origin: origin, Instruction: instruction, AllowUnlock: allowUnlock})
	a.SavesVersion = true
}

// askedFor is what the person typed for a turn, on one line ("" for a
// dashboard note): the instruction recorded with a sheet that names none.
func (c *conv) askedFor(turn int) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	if turn < 1 || turn > len(c.data.Turns) || c.data.Turns[turn-1].Note {
		return ""
	}
	return clip(strings.Join(strings.Fields(c.data.Turns[turn-1].Prompt), " "), 300)
}

// addSheetCards makes a card for every cut sheet a turn wrote that has none
// yet (one card per sheet, also when Claude asked to apply it).
func (m *Manager) addSheetCards(c *conv, turn int) {
	c.mu.Lock()
	if turn < 1 || turn > len(c.data.Turns) {
		c.mu.Unlock()
		return
	}
	t := c.data.Turns[turn-1]
	covered := map[string]bool{}
	for _, a := range c.data.Actions {
		if a.Proposal != "" {
			covered[a.Proposal] = true
		}
	}
	c.mu.Unlock()
	if t.Folder == "" {
		return
	}
	dir := filepath.Join(m.dir(), filepath.FromSlash(t.Folder))
	_, _, sheets, _ := listProposalFiles(dir)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	for _, f := range sheets {
		ref := t.RefPrefix + f
		if covered[ref] {
			continue
		}
		raw, err := readProposal(dir, f)
		if err != nil {
			continue
		}
		a := &AssistantAction{ID: shortID("act"), Turn: turn, Kind: "cut apply", State: ActionPending, CreatedAt: time.Now().UTC()}
		m.sheetCard(ctx, c, a, ref, raw, "", false)
		if a.State == "" {
			continue
		}
		auto := m.startAuto(c, a) // Auto saves it (and renders its review copy) by itself (D186)
		c.mu.Lock()
		c.data.Actions = append(c.data.Actions, *a)
		if len(c.data.Actions) > maxActions {
			c.data.Actions = c.data.Actions[len(c.data.Actions)-maxActions:]
		}
		c.addEvent(AssistantEvent{Kind: "action", Turn: turn, ActionID: a.ID, Text: a.Title})
		c.touch()
		c.mu.Unlock()
		if auto {
			m.addAuto(c, a.ID)
		}
	}
}

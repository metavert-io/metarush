package assistant

import (
	"context"
	"fmt"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// Delivery notes and captions in other languages from the Claude Code panel
// (D237, D238). Claude writes a version's title, description, chapters and
// show notes — or a translation of its captions — into the proposals folder
// with the Claude account the panel answers with (D239) and asks to keep them (`deliver notes
// set`, `captions translate`); the refused command becomes a card that shows
// what it keeps, and the person keeps it with one click. MetaRush makes no AI
// calls of its own.

// deliveryCommands are the commands this file turns into cards.
var deliveryCommands = map[string]bool{"deliver notes set": true, "captions translate": true, "captions remove-language": true}

func (m *Manager) describeDelivery(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	switch call.Path {
	case "deliver notes set":
		m.describeNotes(ctx, c, call, a)
	case "captions translate":
		m.describeTranslation(ctx, c, call, a)
	case "captions remove-language":
		project := first(call.Args)
		if project == "" || call.Flags["to"] == "" {
			incomplete(a, "Take translated captions off", "Claude did not name the project and the language.")
			return
		}
		langs, err := callOp[ops.CaptionsLanguagesOut](ctx, m, "captions.languages", ops.CaptionsLanguagesIn{ProjectID: project})
		if err != nil {
			incomplete(a, "Take translated captions off", apperr.From(err).Message)
			return
		}
		lang, ok := transcripts.Language(call.Flags["to"])
		if !ok {
			incomplete(a, "Take translated captions off", "That isn't a language code MetaRush can read.")
			return
		}
		a.Title = fmt.Sprintf("Take the %s captions off %s", transcripts.LanguageName(lang), quote(m.nameOf(ctx, langs.ProjectID)))
		a.Details = []string{"The captions in " + langs.FromName + " stay; videos delivered before keep their files"}
		setInput(a, "captions.translation.remove", ops.CaptionsLanguageRemoveIn{ProjectID: langs.ProjectID, Language: lang})
	}
}

// describeNotes is keeping the delivery notes Claude wrote for a version.
func (m *Manager) describeNotes(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	const what = "Keep the delivery notes"
	project := first(call.Args)
	if project == "" {
		project = call.Flags["project"]
	}
	if project == "" {
		incomplete(a, what, "Claude did not name the project.")
		return
	}
	cur, err := callOp[ops.DeliveryNotesOut](ctx, m, "delivery.notes", ops.DeliveryNotesIn{ProjectID: project, Revision: call.Flags["version"], Branch: call.Flags["branch"]})
	if err != nil {
		incomplete(a, what, apperr.From(err).Message)
		return
	}
	in := ops.DeliveryNotesSetIn{ProjectID: cur.ProjectID, Revision: cur.RevisionID, WrittenBy: models.WrittenByClaude}
	if hasBool(call, "clear") {
		if cur.Notes == nil {
			incomplete(a, what, fmt.Sprintf("Version %d has no delivery notes to take off.", cur.Number))
			return
		}
		in.Clear = true
		a.Title = fmt.Sprintf("Take the delivery notes off version %d of %s", cur.Number, quote(cur.ProjectName))
		setInput(a, "delivery.notes.set", in)
		return
	}
	if from := call.Flags["from"]; from != "" {
		raw, why := m.proposalFile(c, from, "delivery notes")
		if why != "" {
			incomplete(a, what, why)
			return
		}
		d, err := ops.ParseDeliveryNotes(raw)
		if err != nil {
			incomplete(a, what, apperr.From(err).Message)
			return
		}
		in.Notes = *d
	}
	var title, description *string
	if v, ok := call.Flags["title"]; ok {
		title = &v
	}
	if v, ok := call.Flags["description"]; ok {
		description = &v
	}
	if err := ops.NotesFlags(&in.Notes, title, description, call.Multi["other-title"], call.Multi["chapter"]); err != nil {
		incomplete(a, what, apperr.From(err).Message)
		return
	}
	notes, warnings, err := ops.CheckDeliveryNotes(in.Notes, cur.Duration)
	if err != nil {
		incomplete(a, what, apperr.From(err).Message)
		return
	}
	a.Title = fmt.Sprintf("Keep the delivery notes for version %d of %s: %s", cur.Number, quote(cur.ProjectName), ops.NotesParts(notes))
	if notes.Title != "" {
		a.Details = append(a.Details, "Title: "+theirs(clip(notes.Title, 100)))
	}
	if len(notes.Titles) > 0 {
		alts := make([]string, 0, len(notes.Titles))
		for _, t := range notes.Titles {
			alts = append(alts, theirs(clip(t, 80)))
		}
		a.Details = append(a.Details, "Or: "+strings.Join(alts, " · "))
	}
	if notes.Description != "" {
		a.Details = append(a.Details, "Description: "+theirs(clip(strings.Join(strings.Fields(notes.Description), " "), 160)))
	}
	if lines := ops.ChapterLines(notes.Chapters); len(lines) > 0 {
		shown := lines
		if len(shown) > 6 {
			shown = append(append([]string{}, lines[:6]...), fmt.Sprintf("and %d more", len(lines)-6))
		}
		a.Details = append(a.Details, "Chapters: "+theirs(strings.Join(shown, " · ")))
	}
	if notes.ShowNotes != "" {
		a.Details = append(a.Details, "Show notes: "+theirs(clip(strings.Join(strings.Fields(notes.ShowNotes), " "), 160)))
	}
	if cur.Notes != nil {
		a.Details = append(a.Details, "They replace the notes the version has now")
	}
	a.Details = append(a.Details, warnings...)
	a.Details = append(a.Details, "Claude wrote them; they go out with the video when it's delivered, and the version itself stays as it is")
	setInput(a, "delivery.notes.set", in)
}

// describeTranslation is keeping the captions Claude translated into a language.
func (m *Manager) describeTranslation(ctx context.Context, c *conv, call *cliCall, a *AssistantAction) {
	const what = "Keep translated captions"
	project := first(call.Args)
	if project == "" {
		project = call.Flags["project"]
	}
	if project == "" {
		incomplete(a, what, "Claude did not name the project.")
		return
	}
	raw, why := m.proposalFile(c, call.Flags["from"], "translation")
	if why != "" {
		incomplete(a, what, why)
		return
	}
	fileLang, lines, err := ops.ParseTranslation(raw)
	if err != nil {
		incomplete(a, what, apperr.From(err).Message)
		return
	}
	lang := call.Flags["to"]
	if lang == "" {
		lang = fileLang
	}
	words := ""
	if hasBool(call, "version-words") {
		words = "version"
	}
	cur, err := callOp[ops.CaptionsLinesOut](ctx, m, "captions.lines", ops.CaptionsLinesIn{ProjectID: project, Revision: call.Flags["version"], Branch: call.Flags["branch"], Language: lang, Words: words})
	if err != nil {
		incomplete(a, what, apperr.From(err).Message)
		return
	}
	matched := ops.MatchTranslation(cur.Lines, lines)
	kept, skipped := 0, 0
	var examples, misses []string
	seen := map[int]bool{}
	for i, l := range matched {
		if l == nil {
			skipped++
			continue
		}
		text := strings.Join(strings.Fields(lines[i].Text), " ")
		if text == "" {
			incomplete(a, what, fmt.Sprintf("Line %d has no translation.", l.N))
			return
		}
		if seen[l.N] {
			continue
		}
		seen[l.N] = true
		kept++
		if len(examples) < 3 {
			examples = append(examples, theirs(clip(l.Source, 70))+" → "+theirs(clip(text, 70)))
		}
		for _, w := range ops.GlossaryMisses(cur.Glossary, l.Source, text) {
			if len(misses) < 4 {
				misses = append(misses, fmt.Sprintf("Line %d doesn't keep %s as the spelling list writes it", l.N, theirs(w)))
			}
		}
	}
	if kept == 0 {
		incomplete(a, what, fmt.Sprintf("None of those lines are in the captions of version %d.", cur.Number))
		return
	}
	a.Title = fmt.Sprintf("Keep the %s captions for version %d of %s: %s, machine-translated by Claude", cur.Name, cur.Number, quote(m.nameOf(ctx, cur.ProjectID)), pluralize(kept, "line"))
	a.Details = append(a.Details, examples...)
	a.Details = append(a.Details, misses...)
	if skipped > 0 {
		a.Details = append(a.Details, fmt.Sprintf("%s aren't in the version's captions and are left out", pluralize(skipped, "line")))
	}
	if left := cur.Total - len(seen); left > 0 && cur.Missing+cur.Outdated > 0 {
		a.Details = append(a.Details, fmt.Sprintf("%s of the version's captions still to translate", pluralize(min(left, cur.Missing+cur.Outdated), "line")))
	}
	a.Details = append(a.Details, "Marked as machine-translated; delivery writes them next to the video, and later fixes to the words follow")
	setInput(a, "captions.translate", ops.CaptionsTranslateIn{ProjectID: cur.ProjectID, Revision: cur.RevisionID, Language: cur.Language, Words: cur.Words, Lines: lines})
}

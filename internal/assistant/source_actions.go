package assistant

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/fixes"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/sources"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcribe"
)

// Names and fixes in the panel (D150–D152). Claude reads a source's info card
// (`asset card`), the better names waiting (`asset names`) and its fixes
// (`asset fixes`) itself; renaming a clip or a cut, using MetaRush's names,
// a version's note, ticking fixes and using a fixed copy in a cut are changes,
// so Claude's call is refused and becomes a card the person runs. `cut
// use-fixed` saves a version, so it is one of the cut's editing verbs
// (cutedits.go).

// sourceCommands are the commands this file turns into cards.
var sourceCommands = map[string]bool{"asset update": true, "asset names": true, "asset fixes": true, "cut rename": true, "revision note": true,
	"footage language": true} // a clip's language (D243)

// splitList reads a comma-separated flag (--tick steady,hum).
func splitList(s string) []string {
	var out []string
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			out = append(out, x)
		}
	}
	return out
}

func (m *Manager) describeSources(ctx context.Context, call *cliCall, a *AssistantAction) {
	arg := first(call.Args)
	switch call.Path {
	case "asset update":
		m.describeRename(ctx, call, a)
	case "asset names":
		m.describeNames(ctx, call, a)
	case "asset fixes":
		m.describeFixes(ctx, call, a)
	case "cut rename":
		m.describeCutRename(ctx, call, a)
	case "footage language":
		m.describeLanguage(ctx, call, a)
	case "revision note":
		if !ids.Valid(ids.Revision, arg) || len(call.Args) < 2 {
			incomplete(a, "Change a version's note", "Claude did not name the version and its note.")
			return
		}
		var r models.Revision
		if m.App.DB.GetByID(ctx, store.CRevisions, m.App.WorkspaceID(), arg, &r) != nil {
			incomplete(a, "Change a version's note", "That version isn't in this workspace.")
			return
		}
		note := clip(strings.TrimSpace(strings.Join(call.Args[1:], " ")), 80)
		v := "version " + strconv.Itoa(r.Number)
		if note == "" {
			a.Title = "Take the note off " + v
		} else {
			a.Title = "Note on " + v + ": " + theirs(note)
		}
		if r.Note != "" && r.Note != note {
			a.Details = append(a.Details, "It says "+theirs(r.Note)+" now")
		}
		a.Details = append(a.Details, "Only the note changes: the version itself stays as it is")
		setInput(a, "revision.note", ops.RevisionNoteIn{ID: arg, Note: note})
	}
}

// describeRename: a clip's descriptive name, typed or MetaRush's suggestion.
// Its file name stays on its info card.
func (m *Manager) describeRename(ctx context.Context, call *cliCall, a *AssistantAction) {
	arg := first(call.Args)
	var as models.Asset
	if !ids.Valid(ids.Asset, arg) || m.App.DB.GetByID(ctx, store.CAssets, m.App.WorkspaceID(), arg, &as) != nil {
		incomplete(a, "Rename a clip", "Claude did not name a clip in this workspace.")
		return
	}
	title, suggested := clip(strings.TrimSpace(call.Flags["title"]), 200), hasBool(call, "suggested")
	if suggested {
		if as.NameSuggestion == nil || strings.TrimSpace(as.NameSuggestion.Title) == "" {
			incomplete(a, "Rename a clip", "MetaRush has no better name for "+theirs(clipName(&as))+" right now.")
			return
		}
		title = as.NameSuggestion.Title
	}
	if title == "" {
		incomplete(a, "Rename a clip", "Claude did not give the new name.")
		return
	}
	if title == clipName(&as) {
		incomplete(a, "Rename a clip", "It is called "+theirs(title)+" already.")
		return
	}
	a.Title = "Call " + theirs(clipName(&as)) + " " + theirs(title)
	if suggested && as.NameSuggestion.Why != "" {
		a.Details = append(a.Details, as.NameSuggestion.Why)
	}
	if as.Name != "" {
		a.Details = append(a.Details, "Its file name, "+theirs(as.Name)+", stays on its info card")
	}
	setInput(a, "asset.update", ops.AssetUpdateIn{ID: as.ID, Title: title, Suggested: suggested})
}

// describeNames: MetaRush's better names for many clips at once (Use all).
// The card lists them now and uses exactly those; names people typed are kept.
func (m *Manager) describeNames(ctx context.Context, call *cliCall, a *AssistantAction) {
	_, all := call.Flags["accept-all"]
	pick := splitList(call.Flags["accept"])
	if !all && len(pick) == 0 {
		incomplete(a, "Use better names", "Claude did not say which names to use.")
		return
	}
	list, err := ops.CallTyped[sources.NamesOut](ctx, m.App, "asset.names", sources.NamesIn{ProjectID: call.Flags["project"], Limit: 200}, ops.Caller{})
	if err != nil {
		incomplete(a, "Use better names", firstLine(apperr.From(err).Message))
		return
	}
	want := map[string]bool{}
	for _, id := range pick {
		want[id] = true
	}
	var rows []sources.NameRow
	for _, r := range list.Names {
		if r.Use && (all || want[r.AssetID]) {
			rows = append(rows, r)
		}
	}
	if len(rows) == 0 {
		incomplete(a, "Use better names", "No better names are waiting.")
		return
	}
	in := sources.NamesAcceptIn{ProjectID: call.Flags["project"]}
	for _, r := range rows {
		in.AssetIDs = append(in.AssetIDs, r.AssetID)
	}
	if len(rows) == 1 {
		a.Title = "Call " + theirs(rows[0].Title) + " " + theirs(rows[0].Suggestion)
	} else {
		a.Title = "Use MetaRush's better names for " + pluralize(len(rows), "clip")
	}
	for _, r := range rows[:min(4, len(rows))] {
		a.Details = append(a.Details, theirs(r.Title)+" becomes "+theirs(r.Suggestion))
	}
	if len(rows) > 4 {
		a.Details = append(a.Details, "and "+pluralize(len(rows)-4, "more"))
	}
	a.Details = append(a.Details, "Names you typed stay; file names stay on each clip's info card")
	setInput(a, "asset.names.accept", in)
}

// describeFixes: ticking or unticking a source's fixes, saying what kind of
// source it is, or measuring it again. Ticking makes a fixed copy (the
// original is kept); a version already saved keeps playing what it plays.
func (m *Manager) describeFixes(ctx context.Context, call *cliCall, a *AssistantAction) {
	arg := first(call.Args)
	name := m.displayName(ctx, arg)
	if name == "" || (!ids.Valid(ids.Asset, arg) && !ids.Valid(ids.Resource, arg)) {
		incomplete(a, "Fix a source", "Claude did not name a clip or picture in this workspace.")
		return
	}
	if _, ok := call.Flags["look"]; ok {
		a.Title = "Measure " + name + " again"
		a.Details = []string{"MetaRush looks and listens again and marks the fixes that fit"}
		setInput(a, "asset.fixes.look", fixes.ShowIn{ID: arg})
		return
	}
	in := fixes.SetIn{ID: arg, Tick: splitList(call.Flags["tick"]), Untick: splitList(call.Flags["untick"]), Kind: call.Flags["kind"]}
	_, again := call.Flags["again"]
	in.Again = again
	only, hasOnly := call.Flags["only"]
	_, none := call.Flags["none"]
	if hasOnly || none {
		o := splitList(only)
		if o == nil {
			o = []string{}
		}
		in.Only = &o
	}
	for _, id := range append(append(append([]string(nil), in.Tick...), in.Untick...), derefList(in.Only)...) {
		if _, ok := models.Fix(id); !ok {
			incomplete(a, "Fix "+name, "There is no fix called "+theirs(id)+".")
			return
		}
	}
	if in.Kind != "" && models.SourceKindWords(in.Kind) == "" {
		incomplete(a, "Fix "+name, "Unknown kind of source "+theirs(in.Kind)+".")
		return
	}
	on := in.Tick
	if in.Only != nil {
		on = *in.Only
	}
	switch {
	case in.Only != nil && len(*in.Only) == 0:
		a.Title = "Play the original of " + name + " again"
	case len(on) > 0:
		a.Title = fixWords(on) + " in " + name
	case len(in.Untick) > 0:
		a.Title = "Stop " + lowerFirst(fixes.Doing(in.Untick)) + " in " + name
	case in.Kind != "":
		a.Title = "Treat " + name + " as " + articled(models.SourceKindWords(in.Kind))
	case in.Again:
		a.Title = "Make the fixed copy of " + name + " again"
	default:
		incomplete(a, "Fix "+name, "Claude did not say which fixes to tick.")
		return
	}
	if len(on) > 0 || in.Again {
		a.Details = append(a.Details, "MetaRush makes a fixed copy on a computer that's on; the original is kept")
		if soundFix(on) {
			a.Details = append(a.Details, "Then it listens to the words and voices again (names you gave stay)")
		}
		a.Details = append(a.Details, "Cuts you saved keep playing what they play now; each one offers to use the fixed copy")
	}
	if in.Kind != "" && len(on) == 0 {
		a.Details = append(a.Details, "It changes the fixes offered for it")
	}
	setInput(a, "asset.fixes.set", in)
}

// describeCutRename: a cut's descriptive name, typed or MetaRush's suggestion.
func (m *Manager) describeCutRename(ctx context.Context, call *cliCall, a *AssistantAction) {
	prj := first(call.Args)
	var p models.Project
	if !ids.Valid(ids.Project, prj) || m.App.DB.GetByID(ctx, store.CProjects, m.App.WorkspaceID(), prj, &p) != nil {
		incomplete(a, "Rename the cut", "Claude did not name a project in this workspace.")
		return
	}
	branch := call.Flags["branch"]
	if branch == "" {
		branch = "main"
	}
	cuts := ops.Cuts(ctx, m.App, &p)
	var cur *ops.CutView
	for i := range cuts {
		if cuts[i].Branch == branch {
			cur = &cuts[i]
		}
	}
	if cur == nil {
		incomplete(a, "Rename the cut", "That project has no cut called "+theirs(branch)+".")
		return
	}
	title, suggested := clip(strings.TrimSpace(strings.Join(call.Args[1:], " ")), 120), hasBool(call, "suggested")
	if suggested {
		title = cur.Suggestion
	}
	if title == "" {
		incomplete(a, "Rename the cut", "Claude did not give the cut a name.")
		return
	}
	a.Title = "Call the cut " + theirs(title)
	if cur.Name != "" && cur.Name != title {
		a.Details = append(a.Details, "It is called "+theirs(cur.Name)+" now")
	}
	a.Details = append(a.Details, "No new version is saved and approvals stay")
	setInput(a, "cut.rename", ops.CutRenameIn{ProjectID: p.ID, Branch: call.Flags["branch"], Title: title, Suggested: suggested && title == cur.Suggestion})
}

// useFixedTitle names a `cut use-fixed` card: "Use the steadier picture of
// “Kitchen chat” in “Main open day video”".
func (m *Manager) useFixedTitle(ctx context.Context, in ops.CutUseFixedIn) string {
	name := m.nameOf(ctx, in.Footage)
	if !ids.Valid(ids.Asset, in.Footage) && !ids.Valid(ids.Resource, in.Footage) {
		name = theirs(in.Footage)
	}
	cut := m.cutName(ctx, in.ProjectID, in.Branch)
	where := ""
	if cut != "" {
		where = " in " + theirs(cut)
	}
	if in.Original {
		return "Play the original of " + name + " again" + where
	}
	what := "fixed copy"
	var as models.Asset
	if ids.Valid(ids.Asset, in.Footage) && m.App.DB.GetByID(ctx, store.CAssets, m.App.WorkspaceID(), in.Footage, &as) == nil && as.Fixes != nil && as.Fixes.Copy != nil {
		what = fixes.OfferWords(as.Fixes.Copy.Fixes)
	}
	return "Use the " + what + " of " + name + where
}

// fixWords is a list of fixes as one instruction: "Steady the camera and
// remove hum".
func fixWords(idsList []string) string {
	var parts []string
	for i, id := range idsList {
		f, _ := models.Fix(id)
		l := f.Label
		if i > 0 {
			l = lowerFirst(l)
		}
		parts = append(parts, l)
	}
	switch len(parts) {
	case 0:
		return ""
	case 1:
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " and " + parts[len(parts)-1]
}

// soundFix: one of the fixes changes the sound (so the words are heard again).
func soundFix(idsList []string) bool {
	for _, id := range idsList {
		if f, _ := models.Fix(id); f.Affects == models.AffectsSound {
			return true
		}
	}
	return false
}

func derefList(p *[]string) []string {
	if p == nil {
		return nil
	}
	return *p
}

// articled puts "a"/"an" before a kind of source ("a call recording").
func articled(s string) string {
	if s == "" {
		return s
	}
	if strings.ContainsRune("aeiou", rune(strings.ToLower(s)[0])) {
		return "an " + s
	}
	return "a " + s
}

// sourceResult says what a names or fixes card did: plainly on the card, and
// what Claude needs to carry on.
func sourceResult(op string, res any) (result, link, forClaude string, ok bool) {
	switch o := res.(type) {
	case *ops.AssetLinkOut:
		if op != "asset.update" {
			return "", "", "", false
		}
		return "It is called " + theirs(o.Asset.Title) + " now", "", " (" + o.Asset.ID + ")", true
	case *sources.NamesAcceptOut:
		return plainSummary(o.Summary), "", "", true
	case *fixes.View:
		return plainSummary(o.Summary), "", " (" + o.ID + ")", true
	case *fixes.LookOut:
		return "Measuring it again; the fixes that fit show on its info card once it's done", "", " (job " + o.JobID + ")", true
	case *ops.CutView:
		return "The cut is called " + theirs(o.Name) + " now", "", "", true
	case *ops.RevisionSummary:
		if op != "revision.note" {
			return "", "", "", false
		}
		v := "Version " + strconv.Itoa(o.Number)
		if o.Note == "" {
			return v + " has no note now", "", " (" + o.ID + ")", true
		}
		return v + ": " + theirs(o.Note), "", " (" + o.ID + ")", true
	}
	return "", "", "", false
}

// describeLanguage: `footage language <clip> --set <language>` (D243) — the
// clip's words are written down again in that language, so the card says
// which language it is now and that its words change.
func (m *Manager) describeLanguage(ctx context.Context, call *cliCall, a *AssistantAction) {
	arg := first(call.Args)
	var as models.Asset
	if !ids.Valid(ids.Asset, arg) || m.App.DB.GetByID(ctx, store.CAssets, m.App.WorkspaceID(), arg, &as) != nil {
		incomplete(a, "Set a clip's language", "Claude did not name a clip in this workspace.")
		return
	}
	code, ok := ops.SpokenLanguageCode(call.Flags["set"])
	if !ok {
		incomplete(a, "Set a clip's language", "Claude did not name a language MetaRush knows.")
		return
	}
	name := transcribe.SpokenLanguageName(code)
	a.Title = fmt.Sprintf("%s is spoken in %s: write its words down again", m.nameOf(ctx, as.ID), name)
	if now := ops.LanguageOf(&as); now.Name != "" {
		a.Details = append(a.Details, "Its words are in "+now.Name+" now")
	}
	a.Details = append(a.Details, "Fixes people made carry over where the same words are heard again")
	setInput(a, "footage.language.set", ops.FootageLanguageSetIn{AssetID: as.ID, Language: code})
}

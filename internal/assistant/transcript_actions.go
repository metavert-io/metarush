package assistant

import (
	"context"
	"fmt"
	"math"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/cutsheet"
	"github.com/metavert-io/metarush/internal/editspec"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/transcripts"
)

// Fixing transcripts from the Claude Code panel (D145–D149): when someone says
// how a name is spelled, Claude finds it (transcript find, read-only) and asks
// for the change; the dashboard turns that into one card with the count and
// examples ("Change “Fern Brook” to “FernBrook” in 37 places in 3 videos, and add it to
// the spelling list"), which changes exactly the places it showed. The text
// fixes waiting for a new version of a cut are a card too (transcript pending
// apply): one new version and one review copy, never a render per fix.

// transcriptCommands are the commands describeTranscript turns into cards.
var transcriptCommands = map[string]bool{"transcript replace": true, "transcript fix": true, "transcript speaker": true, "transcript undo": true,
	"transcript spelling add": true, "transcript spelling remove": true, "transcript spelling apply": true, "transcript pending apply": true,
	"captions file": true, "deliver captions": true}

// TranscriptCardKinds are the kinds of the transcript cards (for the panel's
// lists of what waits for the person).
var TranscriptCardKinds = []string{"transcript replace", "transcript fix", "transcript speaker", "transcript undo", "transcript spelling add",
	"transcript spelling remove", "transcript spelling apply", "transcript pending apply"}

// quote marks words from the transcript or the person.
func quote(s string) string {
	s = transcripts.Space(s)
	if len(s) > len("“”") && strings.HasPrefix(s, "“") && strings.HasSuffix(s, "”") {
		return s // already quoted (names in the panel come quoted, D140)
	}
	return "“" + clip(s, 80) + "”"
}

// cardName is how a card names a clip or a project: its name in quotes (people's
// own words), or the plain fallback ("the footage") when it isn't known.
func (m *Manager) cardName(ctx context.Context, id string) string {
	n := m.nameOf(ctx, id)
	switch n {
	case "the cut", "the render", "the project", "the footage", "it":
		return n
	}
	return quote(n)
}

func placesWord(n int) string {
	if n == 1 {
		return "1 place"
	}
	return fmt.Sprintf("%d places", n)
}

func videosWord(n int) string {
	if n == 1 {
		return "1 video"
	}
	return fmt.Sprintf("%d videos", n)
}

// hitLine is one place on a card: where, and the words around it.
func hitLine(h transcripts.Hit) string {
	at := clockAt(h.From)
	return fmt.Sprintf("%s %s: …%s %s %s…", quote(h.Clip), at, clip(h.Before, 60), quote(h.Text), clip(h.After, 60))
}

func (m *Manager) describeTranscript(ctx context.Context, call *cliCall, a *AssistantAction) {
	args := call.Args
	scope := func() (string, string, bool) {
		return call.Flags["clip"], call.Flags["project"], hasBool(call, "everywhere")
	}
	switch call.Path {
	case "transcript replace":
		if len(args) < 2 || transcripts.Space(args[0]) == "" {
			incomplete(a, "Change words in transcripts", "Claude did not say what to replace with what (single-quote both).")
			return
		}
		query, with := transcripts.Space(args[0]), transcripts.Space(args[1])
		clipRef, project, everywhere := scope()
		find := ops.TranscriptFindIn{Query: query, With: &with, AssetID: clipRef, ProjectID: project, Everywhere: everywhere,
			PartOfWords: hasBool(call, "part-of-words"), MatchCase: hasBool(call, "match-case"), Limit: 1000}
		prev, err := callOp[ops.TranscriptFindOut](ctx, m, "transcript.find", find)
		if err != nil {
			incomplete(a, "Change words in transcripts", apperr.From(err).Message)
			return
		}
		var only []string
		clips := map[string]bool{}
		var examples []transcripts.Hit
		for _, h := range prev.Hits {
			if h.Same {
				continue
			}
			only = append(only, h.ID)
			clips[h.AssetID] = true
			if len(examples) < 3 {
				examples = append(examples, h)
			}
		}
		if picked := call.Multi["only"]; len(picked) > 0 {
			only = filterIn(only, picked)
		}
		if len(only) == 0 {
			incomplete(a, "Change "+quote(query)+" to "+quote(with), "There's nothing to change: "+prev.Summary)
			return
		}
		a.Title = fmt.Sprintf("Change %s to %s in %s in %s", quote(query), quote(with), placesWord(len(only)), videosWord(len(clips)))
		if hasBool(call, "spelling") {
			a.Title += ", and add it to the spelling list"
		}
		for _, h := range examples {
			a.Details = append(a.Details, hitLine(h))
		}
		if more := len(only) - len(examples); more > 0 {
			a.Details = append(a.Details, fmt.Sprintf("And %s more", placesWord(more)))
		}
		if prev.More {
			a.Details = append(a.Details, "Only the first 1000 places are changed; ask again for the rest")
		}
		a.Details = append(a.Details, "Every place keeps its timing; it can be undone from a clip's transcript history")
		setInput(a, "transcript.replace", ops.TranscriptReplaceIn{Query: query, With: with, AssetID: clipRef, ProjectID: project, Everywhere: everywhere,
			PartOfWords: find.PartOfWords, MatchCase: find.MatchCase, Only: only, Spelling: hasBool(call, "spelling")})
	case "transcript fix":
		if len(args) < 2 {
			incomplete(a, "Fix words in a transcript", "Claude did not name the clip and the words.")
			return
		}
		from, to, ok := callRange(call)
		if !ok {
			incomplete(a, "Fix words in a transcript", "Claude did not say where the words are (--at, or --from and --to).")
			return
		}
		tx, err := callOp[ops.TranscriptOut](ctx, m, "footage.transcript", ops.TranscriptIn{AssetID: args[0], Words: true})
		if err != nil {
			incomplete(a, "Fix words in a transcript", apperr.From(err).Message)
			return
		}
		now := ""
		if us := transcripts.Units(tx.Segments); len(us) > 0 {
			if i, j, ok := transcripts.Range(us, from, to); ok {
				now = transcripts.Text(us, i, j)
			}
		}
		text := transcripts.Space(args[1])
		if now == "" || now == text {
			incomplete(a, "Fix words in a transcript", "There's nothing to change there.")
			return
		}
		a.Title = fmt.Sprintf("Fix %s → %s in %s", quote(now), quote(text), quote(tx.Title))
		a.Details = []string{"At " + clockAt(from) + "; the words keep their timing, and it can be undone"}
		setInput(a, "transcript.fix", ops.TranscriptFixIn{AssetID: tx.AssetID, From: from, To: to, Text: text, Expect: now, ProjectID: call.Flags["project"]})
	case "transcript speaker":
		if len(args) < 1 {
			incomplete(a, "Fix who speaks", "Claude did not name the clip.")
			return
		}
		from, to, ok := callRange(call)
		if !ok || to <= from {
			incomplete(a, "Fix who speaks", "Claude did not say where (--from and --to).")
			return
		}
		in := ops.TranscriptSpeakerIn{ProjectID: call.Flags["project"]}
		in.AssetID, in.From, in.To = args[0], from, to
		who := ""
		switch {
		case hasBool(call, "unsure"):
			in.Unsure, who = true, "someone not named yet"
		case hasBool(call, "new-person") && len(args) > 1:
			in.Person, in.NewPerson, who = transcripts.Space(args[1]), true, transcripts.Space(args[1])
		case call.Flags["person"] != "":
			in.Person, who = call.Flags["person"], call.Flags["person"]
		default:
			incomplete(a, "Fix who speaks", "Claude did not say who it is.")
			return
		}
		if !ids.Valid(ids.Asset, in.AssetID) {
			incomplete(a, "Fix who speaks", "Claude did not name the clip by its ID.")
			return
		}
		a.Title = fmt.Sprintf("%s–%s of %s is %s", clockAt(from), clockAt(to), m.cardName(ctx, in.AssetID), quote(who))
		a.Details = []string{"Those words move to them, and their voice learns only from what you confirm; it can be undone"}
		setInput(a, "transcript.speaker", in)
	case "transcript undo":
		fixID, batch := first(args), call.Flags["batch"]
		var fixes []models.TranscriptFix
		filter := bson.M{"_id": fixID}
		if batch != "" {
			filter = bson.M{"batchId": batch}
		}
		if fixID == "" && batch == "" {
			incomplete(a, "Undo a transcript fix", "Claude did not say which fix.")
			return
		}
		_ = m.App.DB.Find(ctx, store.CTranscriptFixes, m.App.WorkspaceID(), filter, &fixes)
		if len(fixes) == 0 {
			incomplete(a, "Undo a transcript fix", "That fix isn't in this workspace.")
			return
		}
		f := fixes[0]
		switch {
		case len(fixes) > 1:
			a.Title = fmt.Sprintf("Undo the change of %s to %s (%s)", quote(f.BeforeText), quote(f.AfterText), placesWord(len(fixes)))
		case f.Kind == models.FixSpeaker:
			a.Title = fmt.Sprintf("Undo: give %s–%s of %s back to %s", clockAt(f.From), clockAt(f.To), m.cardName(ctx, f.AssetID), quote(f.BeforeText))
		default:
			a.Title = fmt.Sprintf("Undo %s → %s in %s", quote(f.BeforeText), quote(f.AfterText), m.cardName(ctx, f.AssetID))
		}
		setInput(a, "transcript.undo", ops.TranscriptUndoIn{FixID: fixID, BatchID: batch})
	case "transcript spelling add":
		word := transcripts.Space(first(args))
		if why := transcripts.CheckWord(word); why != "" {
			incomplete(a, "Add a word to the spelling list", why)
			return
		}
		in := ops.SpellingAddIn{ProjectID: call.Flags["project"], Word: word, HeardAs: call.Multi["heard-as"], Note: call.Flags["note"], FixExisting: hasBool(call, "fix-existing")}
		// The list of the Content Settings the project uses (D193): a project
		// that follows another's puts it on that one's list (the card says whose).
		pid := in.ProjectID
		if pid != "" {
			if p, err := ops.ResolveProject(ctx, m.App, pid); err == nil {
				pid = p.ID
			}
		}
		st := ops.KeepSettingsOf(ctx, m.App, pid)
		list := "the spelling list"
		if st.Owner != "" {
			in.ProjectID = st.Owner
			list = "the spelling list of " + quote(st.OwnerName) + "'s settings"
			if st.Others > 0 {
				list += fmt.Sprintf(" (also used by %s)", pluralize(st.Others, "project"))
			}
		}
		a.Title = "Add " + quote(word) + " to " + list
		a.Details = []string{"New transcripts write it that way, and the transcriber is told to expect it"}
		if in.FixExisting {
			a.Details = append(a.Details, "It also corrects the transcripts that write it another way")
		}
		setInput(a, "transcript.spelling.add", in)
	case "transcript spelling remove":
		word := transcripts.Space(first(args))
		if word == "" {
			incomplete(a, "Take a word off the spelling list", "Claude did not say which word.")
			return
		}
		a.Title = "Take " + quote(word) + " off the spelling list"
		a.Details = []string{"Transcripts it corrected stay as they are"}
		setInput(a, "transcript.spelling.remove", ops.SpellingRemoveIn{ProjectID: call.Flags["project"], Word: word})
	case "transcript spelling apply":
		clipRef, project, everywhere := scope()
		prev, err := callOp[ops.SpellingApplyOut](ctx, m, "transcript.spelling.apply", ops.SpellingApplyIn{AssetID: clipRef, ProjectID: project, Everywhere: everywhere, DryRun: true})
		if err != nil {
			incomplete(a, "Correct transcripts by the spelling lists", apperr.From(err).Message)
			return
		}
		if prev.Total == 0 {
			incomplete(a, "Correct transcripts by the spelling lists", prev.Summary)
			return
		}
		var only []string
		for _, h := range prev.Hits {
			only = append(only, h.ID)
		}
		a.Title = fmt.Sprintf("Correct %s in %s by the spelling lists", placesWord(len(only)), videosWord(prev.Clips))
		for i, h := range prev.Hits {
			if i == 3 {
				a.Details = append(a.Details, fmt.Sprintf("And %s more", placesWord(len(prev.Hits)-3)))
				break
			}
			a.Details = append(a.Details, hitLine(h)+" → "+quote(h.New))
		}
		setInput(a, "transcript.spelling.apply", ops.SpellingApplyIn{AssetID: clipRef, ProjectID: project, Everywhere: everywhere, Only: only})
	case "transcript pending apply":
		project := first(args)
		if project == "" {
			project = call.Flags["project"]
		}
		pend, err := callOp[ops.TranscriptPendingOut](ctx, m, "transcript.pending", ops.TranscriptPendingIn{ProjectID: project, Branch: call.Flags["branch"]})
		if err != nil {
			incomplete(a, "Make a new version with the text fixes", apperr.From(err).Message)
			return
		}
		if pend.Count == 0 && len(pend.Transcribed) == 0 {
			incomplete(a, "Make a new version with the text fixes", pend.Summary)
			return
		}
		what := fmt.Sprintf("%d text fixes", pend.Count)
		if pend.Count == 1 {
			what = "1 text fix"
		}
		if pend.Count == 0 {
			what = "the new transcripts"
		}
		a.Title = fmt.Sprintf("Make version %d of %s with %s", pend.Number+1, quote(pend.ProjectName), what)
		norender := hasBool(call, "no-render")
		if !norender {
			a.Title += ", and a review copy"
		}
		n := 0
		for _, p := range pend.Places {
			if p.Kind != models.FixWords || n == 3 {
				continue
			}
			a.Details = append(a.Details, fmt.Sprintf("%s: %s → %s", quote(p.Clip), quote(p.BeforeText), quote(p.AfterText)))
			n++
		}
		if pend.Count > n && n > 0 {
			a.Details = append(a.Details, fmt.Sprintf("And %d more", pend.Count-n))
		}
		a.Details = append(a.Details, "One new version for all of them; version "+fmt.Sprint(pend.Number)+" keeps its words")
		setInput(a, "transcript.pending.apply", ops.TranscriptPendingApplyIn{ProjectID: pend.ProjectID, Branch: call.Flags["branch"], NoRender: norender})
	case "captions file":
		unavailable(a, "Save a captions file", "Saving files into a folder isn't started from here. Download the captions file from the version in the dashboard, or save it from a terminal.")
	case "deliver captions":
		unavailable(a, "Update the captions files of delivered videos", "That rewrites files in a folder on this machine, so it isn't started from here. Run it from a terminal.")
	}
}

// callRange reads --at, or --from and --to, as source times.
func callRange(call *cliCall) (float64, float64, bool) {
	if at := call.Flags["at"]; at != "" {
		t, ok := editspec.ParseClock(at)
		return t, t, ok
	}
	f, ok1 := editspec.ParseClock(call.Flags["from"])
	t, ok2 := editspec.ParseClock(call.Flags["to"])
	return f, t, ok1 && ok2
}

func filterIn(xs, keep []string) []string {
	k := map[string]bool{}
	for _, x := range keep {
		for _, p := range strings.Split(x, ",") {
			k[strings.TrimSpace(p)] = true
		}
	}
	var out []string
	for _, x := range xs {
		if k[x] {
			out = append(out, x)
		}
	}
	return out
}

// clockAt is a source time for a card ("0:12.4").
func clockAt(v float64) string { return cutsheet.Clock(math.Round(v*10) / 10) }

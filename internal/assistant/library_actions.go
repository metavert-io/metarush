package assistant

import (
	"context"
	"strings"

	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/ops"
)

// Filing footage from the Claude Code panel (D96–D98): when Claude asks to
// archive clips, put them on a collection or move them to the Trash, the
// person sees a card and confirms it; the dashboard runs the same op the CLI
// does. Deleting for good is never a card: the person does that in the Library.

// libraryCommands are the commands describeLibrary turns into cards.
var libraryCommands = map[string]bool{"asset archive": true, "asset unarchive": true, "asset trash": true, "asset restore": true,
	"collection create": true, "collection add": true, "collection remove": true, "trash empty": true, "asset delete": true}

// clipsNamed reads clip IDs from the arguments (all must be valid).
func clipsNamed(args []string) []string {
	var out []string
	for _, x := range args {
		x = strings.TrimSpace(x)
		if !ids.Valid(ids.Asset, x) {
			return nil
		}
		out = append(out, x)
	}
	return out
}

// clipsText names one clip or counts several: “Kitchen chat”, 3 clips.
func (m *Manager) clipsText(ctx context.Context, idsList []string) string {
	if len(idsList) == 1 {
		return "“" + m.nameOf(ctx, idsList[0]) + "”"
	}
	return countClips(len(idsList))
}

func (m *Manager) describeLibrary(ctx context.Context, call *cliCall, a *AssistantAction) {
	switch call.Path {
	case "trash empty", "asset delete":
		unavailable(a, "Delete footage for good", "Deleting for good is for you to do: open the Trash in the Library, where it says how much goes first.")
		return
	case "collection create":
		name := clip(strings.TrimSpace(strings.Join(call.Args, " ")), 80)
		if name == "" {
			incomplete(a, "Create a collection", "Claude did not name the collection.")
			return
		}
		a.Title = "Create the collection “" + name + "”"
		setInput(a, "collection.create", ops.CollectionCreateIn{Name: name})
		return
	case "collection add", "collection remove":
		if len(call.Args) < 2 {
			incomplete(a, "Change a collection", "Claude did not name the collection and the clips.")
			return
		}
		name, clips := clip(strings.TrimSpace(call.Args[0]), 80), clipsNamed(call.Args[1:])
		if name == "" || clips == nil {
			incomplete(a, "Change a collection", "Claude did not name the collection and the clips by their IDs.")
			return
		}
		in := ops.CollectionItemsIn{Collection: name, AssetIDs: clips}
		if call.Path == "collection add" {
			in.Create = hasBool(call, "create")
			a.Title = "Put " + m.clipsText(ctx, clips) + " on “" + name + "”"
			a.Details = []string{"A clip can be on several collections and in projects too"}
		} else {
			a.Title = "Take " + m.clipsText(ctx, clips) + " off “" + name + "”"
			a.Details = []string{"Nothing is deleted"}
		}
		setInput(a, strings.Replace(call.Path, " ", ".", 1), in)
		return
	}
	clips := clipsNamed(call.Args)
	if clips == nil {
		incomplete(a, "File clips away", "Claude did not name the clips by their IDs.")
		return
	}
	in := ops.LibraryActionIn{AssetIDs: clips}
	what := m.clipsText(ctx, clips)
	if prj := call.Flags["project"]; prj != "" && (call.Path == "asset archive" || call.Path == "asset trash") {
		if !ids.Valid(ids.Project, prj) {
			incomplete(a, "File clips away", "Claude did not name the project by its ID.")
			return
		}
		in.ProjectID = prj
		a.Details = []string{"Out of " + m.nameOf(ctx, prj)}
	}
	switch call.Path {
	case "asset archive":
		a.Title = "Archive " + what
		a.Details = append(a.Details, "Kept for later, out of the Inbox; nothing is deleted")
	case "asset unarchive":
		a.Title = "Take " + what + " out of the Archive"
	case "asset trash":
		a.Title = "Move " + what + " to the Trash"
		a.Details = append(a.Details, "You can restore it until the Trash is emptied")
	case "asset restore":
		a.Title = "Restore " + what + " from the Trash"
	}
	setInput(a, strings.Replace(call.Path, " ", ".", 1), in)
}

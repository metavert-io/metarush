package ops

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
)

// Questions about groups of faces (D155–D157): one question per person per
// clip, answered for the whole group; the grid on the To-dos page and
// `metarush todos groups|answer|accept-shown|dismiss` use these.

// TodoGroupsIn filters the questions about faces.
type TodoGroupsIn struct {
	ProjectID string `json:"projectId,omitempty"`
	AssetID   string `json:"assetId,omitempty"`
}

// ShownAnswer is one question "Use the names shown in the video" would answer.
type ShownAnswer struct {
	TodoID      string `json:"todoId"`
	Name        string `json:"name"`
	New         bool   `json:"new" jsonschema:"description=Nobody has this name in People yet: accepting adds them."`
	Appearances int    `json:"appearances"`
}

// ShownClip is a clip whose video shows the names of people it asks about.
type ShownClip struct {
	AssetID    string        `json:"assetId"`
	AssetTitle string        `json:"assetTitle"`
	Names      []ShownAnswer `json:"names"`
	// Summary says what one click would do, in plain words.
	Summary string `json:"summary"`
}

// TodoGroupsOut lists the open questions about faces, one per person per clip.
type TodoGroupsOut struct {
	// Groups are the questions (kind face_group): sample faces, how often they
	// are seen, whether a latest cut shows them, the suggestion and its evidence.
	// Faces in a cut first, then the ones seen most.
	Groups []models.Todo `json:"groups"`
	// Shown lists, per clip, the names the video shows that one click accepts.
	Shown   []ShownClip `json:"shown"`
	Summary string      `json:"summary"`
}

// TodoAcceptShownIn accepts the names a clip shows for the people it asks about.
type TodoAcceptShownIn struct {
	AssetID string `json:"assetId"`
}

// ShownAccepted is one question answered with the name the video shows.
type ShownAccepted struct {
	TodoID string `json:"todoId"`
	Name   string `json:"name"`
	// PersonID is who the faces are now (Added: someone new).
	PersonID string `json:"personId"`
	Added    bool   `json:"added"`
	Named    int    `json:"named" jsonschema:"description=Faces named."`
	Aside    int    `json:"aside" jsonschema:"description=Faces left out because they look less like the others (asked about on their own)."`
	Done     string `json:"done"`
}

// TodoAcceptShownOut says what accepting the shown names did.
type TodoAcceptShownOut struct {
	Accepted []ShownAccepted `json:"accepted"`
	// Failed are questions that could not be answered this way, in plain words.
	Failed  []string `json:"failed"`
	Summary string   `json:"summary"`
}

func groupErr(err error) error {
	if errors.Is(err, faces.ErrNoGroup) {
		return apperr.New(apperr.Conflict, "Those faces were named or changed meanwhile.").
			WithHint("The To-dos page shows the questions that are still open.").WithCommand("metarush todos groups")
	}
	return faceErr(err)
}

// answerFaceGroup applies an answer about a group of faces: who they are (a
// suggestion, a typed name or someone picked), not the suggested person, or
// the same person as another question of the clip.
func answerFaceGroup(ctx context.Context, a *app.App, t *models.Todo, value string, sug *models.TodoSuggestion, in *TodoAnswerIn, got *answered) (string, error) {
	asset, group, title := t.AssetID, t.Context.GroupID, t.Context.AssetTitle
	if group == "" {
		return "", invalid("this question is not about a group of faces")
	}
	if in.SameAs != "" {
		other, err := loadTodo(ctx, a, in.SameAs)
		if err != nil {
			return "", err
		}
		if other.ID == t.ID || other.Kind != models.TodoFaceGroup || other.AssetID != asset || other.State != models.TodoOpen || other.Context.GroupID == "" {
			return "", invalid("“Same as” joins two open questions about faces in the same clip.")
		}
		n, err := faces.JoinGroups(ctx, a, asset, other.Context.GroupID, group)
		if err != nil {
			return "", groupErr(err)
		}
		return fmt.Sprintf("One question for both now: %s more joined the other question about “%s”.", count(n, "face", "faces"), title), nil
	}
	var p *models.Person
	var err error
	switch {
	case (sug != nil && sug.Value == "no") || (sug == nil && (no(value) || strings.EqualFold(value, "no, someone else"))):
		if t.Context.PersonID == "" && t.Context.PersonName == "" {
			return "", invalid("Type their name, or skip the question.")
		}
		name := ""
		if t.Context.PersonID == "" {
			name = t.Context.PersonName
		}
		if _, err := faces.RejectGroup(ctx, a, asset, group, t.Context.PersonID, name); err != nil {
			return "", groupErr(err)
		}
		got.notIt()
		return fmt.Sprintf("Noted: they're not %s.", t.Context.PersonName), nil
	case (sug != nil && sug.Value == "yes") || (sug == nil && yes(value)):
		if t.Context.PersonID == "" && t.Context.PersonName == "" {
			return "", invalid("Type their name: nobody is suggested for these faces.")
		}
		p, _, err = speakers.PersonFor(ctx, a, t.Context.PersonName, t.Context.PersonID, false)
	case sug != nil:
		p, _, err = speakers.PersonFor(ctx, a, sug.Value, sug.PersonID, false)
	default:
		p, _, err = speakers.PersonFor(ctx, a, value, in.PersonID, in.NewPerson)
	}
	if err != nil {
		return "", speakerErrFor(err, answerCommand(t.ID))
	}
	res, err := faces.NameGroup(ctx, a, asset, group, *p)
	if err != nil {
		return "", groupErr(err)
	}
	got.as(p)
	afterFacesNamed(ctx, a, asset)
	return groupDone(res, p.Name, title), nil
}

// afterFacesNamed brings a clip up to date once faces were named there, on
// every way of naming them (one face, a group, the names the video shows): its
// voice questions — a voice heard while a named face is alone on screen is
// suggested that person (never named from it, D157) — and what MetaRush says
// about it, since a named face names the clip better (D150).
func afterFacesNamed(ctx context.Context, a *app.App, assetID string) {
	var as models.Asset
	if a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), assetID, &as) == nil && as.Speakers != nil && as.Speakers.State == "available" {
		_ = speakers.Reconcile(ctx, a, assetID)
	}
	SourceFactsChanged(ctx, a, assetID, FactsPeople)
}

// groupDone says what naming a group did, in plain words.
func groupDone(res *faces.GroupNamed, name, title string) string {
	done := fmt.Sprintf("These %d faces are %s now (in “%s”).", res.Named, name, title)
	if res.Named == 1 {
		done = fmt.Sprintf("That face is %s now (in “%s”).", name, title)
	}
	switch {
	case res.Aside == 1:
		done += " One face that looks less like the others is left for a question of its own."
	case res.Aside > 1:
		done += fmt.Sprintf(" %d faces that look less like the others are left for questions of their own.", res.Aside)
	}
	return done
}

func listFaceGroups(ctx context.Context, a *app.App, in *TodoGroupsIn) (*TodoGroupsOut, error) {
	filter := bson.M{"kind": models.TodoFaceGroup, "state": models.TodoOpen}
	if in.ProjectID != "" {
		filter["projectIds"] = in.ProjectID
	}
	if in.AssetID != "" {
		filter["assetId"] = in.AssetID
	}
	out := &TodoGroupsOut{Groups: []models.Todo{}, Shown: []ShownClip{}}
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), filter, &out.Groups, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}}).SetLimit(1000)); err != nil {
		return nil, err
	}
	if out.Groups == nil {
		out.Groups = []models.Todo{}
	}
	sort.SliceStable(out.Groups, func(i, j int) bool {
		x, y := out.Groups[i].Context, out.Groups[j].Context
		if x.InCut != y.InCut {
			return x.InCut
		}
		return x.Appearances > y.Appearances
	})
	byClip := map[string]*ShownClip{}
	var order []string
	clips := map[string]bool{}
	for _, t := range out.Groups {
		clips[t.AssetID] = true
		if t.Context.Shown == "" || len(t.Suggestions) == 0 {
			continue
		}
		c := byClip[t.AssetID]
		if c == nil {
			title := t.Context.AssetTitle
			if title == "" {
				title = t.Context.AssetName
			}
			c = &ShownClip{AssetID: t.AssetID, AssetTitle: title, Names: []ShownAnswer{}}
			byClip[t.AssetID] = c
			order = append(order, t.AssetID)
		}
		c.Names = append(c.Names, ShownAnswer{TodoID: t.ID, Name: t.Context.PersonName, New: t.Suggestions[0].New, Appearances: t.Context.Appearances})
	}
	for _, id := range order {
		c := byClip[id]
		var ns []string
		added := 0
		for _, n := range c.Names {
			ns = append(ns, n.Name)
			if n.New {
				added++
			}
		}
		c.Summary = fmt.Sprintf("Use the names shown in “%s”: %s.", c.AssetTitle, strings.Join(ns, ", "))
		if added > 0 {
			c.Summary = strings.TrimSuffix(c.Summary, ".") + fmt.Sprintf(" (adds %s to your people).", count(added, "new person", "new people"))
		}
		out.Shown = append(out.Shown, *c)
	}
	switch n := len(out.Groups); {
	case n == 0:
		out.Summary = "No faces to name right now."
	default:
		out.Summary = fmt.Sprintf("%s to name in %s.", count(n, "person", "people"), count(len(clips), "clip", "clips"))
	}
	return out, nil
}

func acceptShown(ctx context.Context, a *app.App, in *TodoAcceptShownIn) (*TodoAcceptShownOut, error) {
	var as models.Asset
	if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
		return nil, notFound("asset", in.AssetID)
	}
	var ts []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), bson.M{"kind": models.TodoFaceGroup, "state": models.TodoOpen, "assetId": as.ID, "context.shown": bson.M{"$nin": bson.A{nil, ""}}}, &ts,
		options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}})); err != nil {
		return nil, err
	}
	out := &TodoAcceptShownOut{Accepted: []ShownAccepted{}, Failed: []string{}}
	// Who each question's shown name is, then every group named before the
	// library is re-scored once (faces.NameGroups).
	type pick struct {
		t     *models.Todo
		p     *models.Person
		added bool
	}
	var picks []pick
	var answers []faces.GroupAnswer
	for i := range ts {
		t := &ts[i]
		if len(t.Suggestions) == 0 {
			continue
		}
		sug := t.Suggestions[0]
		var p *models.Person
		var added bool
		var err error
		if sug.PersonID != "" {
			p, added, err = speakers.PersonFor(ctx, a, "", sug.PersonID, false)
		} else {
			p, added, err = speakers.PersonFor(ctx, a, t.Context.PersonName, "", false)
		}
		if err != nil {
			out.Failed = append(out.Failed, fmt.Sprintf("“%s”: %s", t.Context.PersonName, apperr.From(speakerErr(err)).Message))
			continue
		}
		picks = append(picks, pick{t, p, added})
		answers = append(answers, faces.GroupAnswer{GroupID: t.Context.GroupID, Person: *p})
	}
	results, errs, err := faces.NameGroups(ctx, a, as.ID, answers)
	if err != nil {
		return nil, err
	}
	for i, x := range picks {
		t, p := x.t, x.p
		if errs[i] != nil {
			out.Failed = append(out.Failed, fmt.Sprintf("“%s”: %s", t.Context.PersonName, apperr.From(groupErr(errs[i])).Message))
			continue
		}
		res := results[i]
		if _, err := todos.Finish(ctx, a, t.ID, models.TodoAnswered, p.Name); err != nil {
			return nil, err
		}
		todos.Scored(ctx, a, t, p.ID, p.Name, false) // the name shown was right (D189)
		a.Audit(ctx, "todos.answer", "todo", t.ID, t.ProjectID, map[string]any{"kind": t.Kind, "answer": p.Name, "via": "shown"})
		out.Accepted = append(out.Accepted, ShownAccepted{TodoID: t.ID, Name: p.Name, PersonID: p.ID, Added: x.added, Named: res.Named, Aside: res.Aside,
			Done: groupDone(res, p.Name, t.Context.AssetTitle)})
	}
	if len(out.Accepted) > 0 {
		afterFacesNamed(ctx, a, as.ID)
	}
	switch n := len(out.Accepted); {
	case n == 0 && len(out.Failed) == 0:
		out.Summary = fmt.Sprintf("No names shown in “%s” are waiting for an answer.", as.DisplayTitle())
	case n == 0:
		out.Summary = fmt.Sprintf("Couldn't use the names shown in “%s”.", as.DisplayTitle())
	default:
		var ns []string
		added := 0
		for _, x := range out.Accepted {
			ns = append(ns, x.Name)
			if x.Added {
				added++
			}
		}
		out.Summary = fmt.Sprintf("Named %s in “%s” from the names the video shows: %s.", count(n, "person", "people"), as.DisplayTitle(), strings.Join(ns, ", "))
		if added > 0 {
			out.Summary += fmt.Sprintf(" %s added to your people.", capFirst(count(added, "new person", "new people")))
		}
	}
	if len(out.Failed) > 0 {
		out.Summary += fmt.Sprintf(" %s still need an answer.", capFirst(count(len(out.Failed), "question", "questions")))
	}
	return out, nil
}

func init() {
	Def("todos.groups", "List the questions about faces: one per person per clip, with sample faces, how often they are seen, the suggested name and why (names the video shows or says, the voice, the looks)", Flags{}, func(ctx context.Context, a *app.App, in *TodoGroupsIn) (*TodoGroupsOut, error) {
		return listFaceGroups(ctx, a, in)
	})
	Def("todos.acceptShown", "Accept the names a clip shows (a call app's name tiles, lower thirds) for the people it asks about: one answer per question", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *TodoAcceptShownIn) (*TodoAcceptShownOut, error) {
		return acceptShown(ctx, a, in)
	})
}

package ops

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/faces"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/names"
	"github.com/metavert-io/metarush/internal/speakers"
	"github.com/metavert-io/metarush/internal/store"
	"github.com/metavert-io/metarush/internal/todos"
)

// Answering many questions about faces at once (D189), and how the evidence
// behind the questions about people has done: `metarush todos not`,
// `footage faces not-here`, `todos skip-faces` and `people evidence`; the
// To-dos page's "None of these are Lena", "Not Lena anywhere in this
// clip", "Skip all in this clip" and "Skip all in this project".

// TodoNotPersonIn says none of the open questions about faces that suggest a
// person are them (in a project or a clip, or everywhere).
type TodoNotPersonIn struct {
	Person    string `json:"person" jsonschema:"minLength=1,description=The person suggested (a name or per_…)."`
	ProjectID string `json:"projectId,omitempty"`
	AssetID   string `json:"assetId,omitempty"`
}

// FacesAnsweredOut says what an answer about many faces did.
type FacesAnsweredOut struct {
	PersonID string `json:"personId,omitempty"`
	// Answered are the questions answered (or skipped); Clips how many clips they were in.
	Answered int `json:"answered"`
	Clips    int `json:"clips"`
	// Faces changed (not suggested as the person any more, or skipped).
	Faces int `json:"faces"`
	// Kept: faces a person confirmed as them, left as they are.
	Kept    int    `json:"kept,omitempty"`
	Summary string `json:"summary"`
}

// FacesNotHereIn says a person appears nowhere on screen in a clip.
type FacesNotHereIn struct {
	AssetID string `json:"assetId" jsonschema:"minLength=1"`
	Person  string `json:"person" jsonschema:"minLength=1,description=The person (a name or per_…)."`
}

// TodoSkipFacesIn skips every open question about faces in a clip or a project.
type TodoSkipFacesIn struct {
	AssetID   string `json:"assetId,omitempty"`
	ProjectID string `json:"projectId,omitempty"`
}

// PeopleEvidenceIn has no fields.
type PeopleEvidenceIn struct{}

// PeopleEvidenceOut is how each kind of evidence behind the questions about
// people has done in this workspace: how often the names it pointed at were
// right, and which are muted (no longer used to suggest a name).
type PeopleEvidenceOut struct {
	Kinds   []todos.KindScore `json:"kinds"`
	Muted   []string          `json:"muted"`
	Summary string            `json:"summary"`
}

// faceQuestions are the open questions about faces in a scope (a clip, a
// project, or everywhere), the "Is this really …?" ones about confirmed faces
// left out unless rechecks.
func faceQuestions(ctx context.Context, a *app.App, assetID, projectID string, rechecks bool) ([]models.Todo, error) {
	filter := bson.M{"state": models.TodoOpen, "kind": bson.M{"$in": bson.A{models.TodoFaceGroup, models.TodoFacePerson}}}
	if assetID != "" {
		filter["assetId"] = assetID
	}
	if projectID != "" {
		filter["projectIds"] = projectID
	}
	var ts []models.Todo
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), filter, &ts, options.Find().SetSort(bson.D{{Key: "createdAt", Value: 1}})); err != nil {
		return nil, err
	}
	out := ts[:0]
	for _, t := range ts {
		if !rechecks && strings.HasSuffix(t.GroupKey, ":recheck") {
			continue
		}
		out = append(out, t)
	}
	return out, nil
}

// notPerson answers "no" to the open questions about faces that suggest a
// person (personID), or a name nobody has in People yet (name): it is
// rejected for their faces, each answer is kept score of, and each clip's
// questions are worked out again once.
func notPerson(ctx context.Context, a *app.App, qs []models.Todo, personID, name string) (answered, changed int, clips []string, err error) {
	groups := map[string][]string{}
	var order []string
	for i := range qs {
		t := &qs[i]
		switch {
		case personID != "" && t.Context.PersonID == personID:
		case personID == "" && t.Context.PersonID == "" && t.Context.PersonName != "" && names.Fold(t.Context.PersonName) == names.Fold(name):
		default:
			continue
		}
		switch {
		case t.Kind == models.TodoFaceGroup && t.Context.GroupID != "":
			if _, ok := groups[t.AssetID]; !ok {
				order = append(order, t.AssetID)
			}
			groups[t.AssetID] = append(groups[t.AssetID], t.Context.GroupID)
		case t.Kind == models.TodoFacePerson && t.Context.FaceID != "" && personID != "":
			if _, err := a.DB.C(store.CFaces).UpdateOne(ctx, bson.M{"_id": t.Context.FaceID, "workspaceId": a.WorkspaceID()},
				bson.M{"$addToSet": bson.M{"rejected": personID}, "$unset": bson.M{"candidate": ""}}); err != nil {
				return answered, changed, clips, err
			}
			changed++
			if _, ok := groups[t.AssetID]; !ok {
				order = append(order, t.AssetID)
				groups[t.AssetID] = nil
			}
		default:
			continue
		}
		if _, err := todos.Finish(ctx, a, t.ID, models.TodoAnswered, "no"); err != nil {
			return answered, changed, clips, err
		}
		todos.Scored(ctx, a, t, "", "", true)
		a.Audit(ctx, "todos.answer", "todo", t.ID, t.ProjectID, map[string]any{"kind": t.Kind, "answer": "no", "via": "bulk"})
		answered++
	}
	for _, asset := range order {
		n, err := faces.NotIn(ctx, a, asset, groups[asset], personID, name)
		if err != nil {
			return answered, changed, clips, err
		}
		changed += n
		if _, err := faces.Candidates(ctx, a, asset); err != nil {
			return answered, changed, clips, err
		}
	}
	return answered, changed, order, nil
}

// skipFaces skips open questions about faces: their faces are never asked
// about again (they can still be named by hand or by their looks).
func skipFaces(ctx context.Context, a *app.App, qs []models.Todo, note string) (skipped, changed int, clips map[string]bool, err error) {
	clips = map[string]bool{}
	for i := range qs {
		t := &qs[i]
		if t.Kind == models.TodoFaceGroup && t.Context.GroupID != "" {
			n, err := faces.SkipGroup(ctx, a, t.AssetID, t.Context.GroupID)
			if err != nil {
				return skipped, changed, clips, err
			}
			changed += n
		}
		if _, err := todos.Finish(ctx, a, t.ID, models.TodoDismissed, note); err != nil {
			return skipped, changed, clips, err
		}
		a.Audit(ctx, "todos.dismiss", "todo", t.ID, t.ProjectID, map[string]any{"kind": t.Kind, "via": "bulk"})
		clips[t.AssetID] = true
		skipped++
	}
	return skipped, changed, clips, nil
}

func init() {
	Def("todos.notPerson", "Say none of the open questions about faces that suggest a person are them (in a project, a clip, or everywhere): they're never suggested for those faces again", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *TodoNotPersonIn) (*FacesAnsweredOut, error) {
		qs, err := faceQuestions(ctx, a, in.AssetID, in.ProjectID, false)
		if err != nil {
			return nil, err
		}
		// Someone in People, or a name the video shows that nobody has yet.
		pid, pname := "", strings.TrimSpace(in.Person)
		if found, err := resolvePerson(ctx, a, in.Person); err == nil {
			pid, pname = found.ID, found.Name
		} else if !slices.ContainsFunc(qs, func(t models.Todo) bool {
			return t.Context.PersonID == "" && names.Fold(t.Context.PersonName) == names.Fold(in.Person)
		}) {
			return nil, err
		}
		answered, changed, clips, err := notPerson(ctx, a, qs, pid, pname)
		if err != nil {
			return nil, err
		}
		out := &FacesAnsweredOut{PersonID: pid, Answered: answered, Clips: len(clips), Faces: changed}
		switch answered {
		case 0:
			out.Summary = fmt.Sprintf("No open question suggests %s here.", pname)
		case 1:
			out.Summary = fmt.Sprintf("Noted: the person asked about isn't %s. MetaRush won't suggest %s for those faces again.", pname, pname)
		default:
			out.Summary = fmt.Sprintf("Noted: none of the %d people asked about (in %s) are %s. MetaRush won't suggest %s for those faces again.",
				answered, count(len(clips), "clip", "clips"), pname, pname)
		}
		return out, nil
	})
	Def("footage.faces.notHere", "Say a person appears nowhere on screen in a clip: never suggested for its faces (also faces found later), names given by looks go, and their voice is no evidence there", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *FacesNotHereIn) (*FacesAnsweredOut, error) {
		var as models.Asset
		if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
			return nil, notFound("asset", in.AssetID)
		}
		p, err := resolvePerson(ctx, a, in.Person)
		if err != nil {
			return nil, err
		}
		qs, err := faceQuestions(ctx, a, as.ID, "", false)
		if err != nil {
			return nil, err
		}
		answered, _, _, err := notPerson(ctx, a, qs, p.ID, p.Name)
		if err != nil {
			return nil, err
		}
		res, err := faces.NotHere(ctx, a, as.ID, p.ID)
		if err != nil {
			return nil, err
		}
		if as.Speakers != nil && as.Speakers.State == "available" {
			_ = speakers.Reconcile(ctx, a, as.ID) // their face is no hint for the clip's voices either
		}
		out := &FacesAnsweredOut{PersonID: p.ID, Answered: answered, Clips: 1, Faces: res.Faces, Kept: res.Kept,
			Summary: fmt.Sprintf("Noted: %s isn't on screen in “%s”. MetaRush won't suggest %s for its faces, or go by %s's voice there.", p.Name, as.DisplayTitle(), p.Name, p.Name)}
		if res.Kept > 0 {
			out.Summary += fmt.Sprintf(" The %s you named %s there stay%s.", count(res.Kept, "face", "faces"), p.Name, map[bool]string{true: "s", false: ""}[res.Kept == 1])
		}
		return out, nil
	})
	Def("todos.skipFaces", "Skip every open question about faces in a clip or a project: those faces are never asked about again (they can still be named by hand)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *TodoSkipFacesIn) (*FacesAnsweredOut, error) {
		if (in.AssetID == "") == (in.ProjectID == "") {
			return nil, invalid("Say which clip or which project.")
		}
		where := ""
		if in.AssetID != "" {
			var as models.Asset
			if err := a.DB.GetByID(ctx, store.CAssets, a.WorkspaceID(), in.AssetID, &as); err != nil {
				return nil, notFound("asset", in.AssetID)
			}
			where = "“" + as.DisplayTitle() + "”"
		} else {
			var p models.Project
			if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), in.ProjectID, &p); err != nil {
				return nil, notFound("project", in.ProjectID)
			}
			where = "“" + p.Name + "”"
		}
		qs, err := faceQuestions(ctx, a, in.AssetID, in.ProjectID, true)
		if err != nil {
			return nil, err
		}
		skipped, changed, clips, err := skipFaces(ctx, a, qs, "Skipped with the other questions about faces.")
		if err != nil {
			return nil, err
		}
		out := &FacesAnsweredOut{Answered: skipped, Clips: len(clips), Faces: changed}
		if skipped == 0 {
			out.Summary = fmt.Sprintf("No questions about faces are open in %s.", where)
		} else {
			out.Summary = fmt.Sprintf("Skipped %s about faces in %s. We won't ask about those faces again.", count(skipped, "question", "questions"), where)
		}
		return out, nil
	})
	Def("people.evidence", "How often each kind of evidence behind the questions about people was right (who is talking, the looks, names shown or said, the voice), and which no longer suggest names", Flags{}, func(ctx context.Context, a *app.App, _ *PeopleEvidenceIn) (*PeopleEvidenceOut, error) {
		ks, err := todos.Scores(ctx, a)
		if err != nil {
			return nil, err
		}
		out := &PeopleEvidenceOut{Kinds: ks, Muted: []string{}}
		answers := 0
		for _, k := range ks {
			answers += k.Answers
			if k.Muted {
				out.Muted = append(out.Muted, k.Kind)
			}
		}
		sort.Strings(out.Muted)
		switch {
		case answers == 0:
			out.Summary = "No answers about people to learn from yet."
		case len(out.Muted) == 0:
			out.Summary = "Every kind of evidence has been right often enough to suggest names."
		default:
			var ws []string
			for _, k := range ks {
				if k.Muted {
					ws = append(ws, strings.ToLower(k.Label[:1])+k.Label[1:])
				}
			}
			out.Summary = fmt.Sprintf("MetaRush no longer suggests names from %s (often wrong here); they can still be quick picks.", strings.Join(ws, ", or "))
		}
		return out, nil
	})
}

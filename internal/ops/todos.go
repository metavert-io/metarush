package ops

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
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
	"github.com/metavert-io/metarush/internal/transcripts"
)

// TodoListIn filters to-dos.
type TodoListIn struct {
	ProjectID string `json:"projectId,omitempty"`
	AssetID   string `json:"assetId,omitempty"`
	Kind      string `json:"kind,omitempty" jsonschema:"enum=speaker_unnamed,enum=speaker_match,enum=music_rights,enum=brief_missing,enum=duplicate_person,enum=revision_approval,enum=face_person,enum=spelling,enum=face_group,enum=remotion_approval,enum=recipe_run,enum=ability_approval"`
	State     string `json:"state,omitempty" jsonschema:"enum=open,enum=answered,enum=dismissed,enum=all,description=Default open."`
	Limit     int64  `json:"limit,omitempty"`
}

// TodoListOut lists to-dos (most important first).
type TodoListOut struct {
	Todos   []models.Todo `json:"todos"`
	Counts  todos.Counts  `json:"counts"`
	Summary string        `json:"summary"`
}

// TodoIn selects a to-do.
type TodoIn struct {
	ID string `json:"id"`
}

// TodoShowOut is one to-do.
type TodoShowOut struct {
	Todo        models.Todo `json:"todo"`
	HowToAnswer string      `json:"howToAnswer"`
}

// TodoAnswerIn answers a to-do with text or a suggestion.
type TodoAnswerIn struct {
	ID         string `json:"id"`
	Answer     string `json:"answer,omitempty" jsonschema:"maxLength=500"`
	Suggestion int    `json:"suggestion,omitempty" jsonschema:"minimum=1,description=Pick a suggested answer by number (1 = first)."`
	// Speaker questions: PersonID picks a person directly, NewPerson adds the
	// typed name as someone new, SamePerson confirms that two voices of the
	// clip are one person (or that a voice is that person although it sounds different).
	PersonID   string `json:"personId,omitempty"`
	NewPerson  bool   `json:"newPerson,omitempty"`
	SamePerson bool   `json:"samePerson,omitempty"`
	// SameAs answers a question about a group of faces (face_group) with
	// another open face question of the same clip: they are one person, and
	// one question asks about both (D155).
	SameAs string `json:"sameAs,omitempty" jsonschema:"description=Another open question about faces of the same clip: the same person."`
}

// TodoAnswerOut says what answering changed.
type TodoAnswerOut struct {
	Todo models.Todo `json:"todo"`
	// Done says in plain words what changed.
	Done string `json:"done"`
	// Separate is the voice of the question's video that was combined into the
	// named one (undo with footage.speakers.unmerge).
	Separate string `json:"separate,omitempty"`
	// AutoMerged are voices combined in other videos because both were then
	// recognized as one person (each can be separated again).
	AutoMerged []speakers.AutoMerge `json:"autoMerged,omitempty"`
}

// answered is what answering a question changed, beyond the plain sentence.
type answered struct {
	separate   string
	autoMerged []speakers.AutoMerge
	// Who a question about a person was answered as (personID, name), or no
	// (not the suggestion); judged says the answer judged the names at all
	// ("same as another question" doesn't). Kept score of (D189).
	personID, name string
	no, judged     bool
}

// as records who a question about a person was answered as.
func (g *answered) as(p *models.Person) {
	if g != nil && p != nil {
		g.personID, g.name, g.judged = p.ID, p.Name, true
	}
}

// notIt records that a question about a person was answered "no".
func (g *answered) notIt() {
	if g != nil {
		g.no, g.judged = true, true
	}
}

// TodoDismissIn dismisses a to-do (it will not be asked again).
type TodoDismissIn struct {
	ID   string `json:"id"`
	Note string `json:"note,omitempty" jsonschema:"maxLength=300"`
}

// TodoCountsIn has no fields.
type TodoCountsIn struct{}

// TodoSyncIn re-checks what needs asking.
type TodoSyncIn struct {
	ProjectID string `json:"projectId,omitempty"`
	AssetID   string `json:"assetId,omitempty"`
}

// TodoSyncOut reports the open to-dos after the check.
type TodoSyncOut struct {
	Checked int    `json:"checked"`
	Open    int    `json:"open"`
	Summary string `json:"summary"`
}

func howToAnswer(t models.Todo) string {
	var b strings.Builder
	if len(t.Suggestions) > 0 {
		b.WriteString("Pick an answer: ")
		for i, s := range t.Suggestions {
			if i > 0 {
				b.WriteString("; ")
			}
			fmt.Fprintf(&b, "%d) %s", i+1, s.Label)
		}
		b.WriteString(".")
	}
	if t.FreeText {
		if b.Len() > 0 {
			b.WriteString(" Or type your own answer.")
		} else {
			b.WriteString("Type your answer.")
		}
	}
	return b.String()
}

func listTodos(ctx context.Context, a *app.App, in *TodoListIn) (*TodoListOut, error) {
	filter := bson.M{}
	switch in.State {
	case "", models.TodoOpen:
		filter["state"] = models.TodoOpen
	case models.TodoAnswered, models.TodoDismissed:
		filter["state"] = in.State
	case "all":
	default:
		return nil, invalid("state must be open, answered, dismissed or all")
	}
	if in.ProjectID != "" {
		filter["projectIds"] = in.ProjectID
	}
	if in.AssetID != "" {
		filter["assetId"] = in.AssetID
	}
	if in.Kind != "" {
		if !slices.Contains(models.TodoKinds, in.Kind) {
			return nil, invalid("unknown kind %q", in.Kind)
		}
		filter["kind"] = in.Kind
	}
	if in.Limit <= 0 || in.Limit > 1000 {
		in.Limit = 500
	}
	out := &TodoListOut{Todos: []models.Todo{}}
	if err := a.DB.Find(ctx, store.CTodos, a.WorkspaceID(), filter, &out.Todos,
		options.Find().SetSort(bson.D{{Key: "priority", Value: 1}, {Key: "createdAt", Value: 1}}).SetLimit(in.Limit)); err != nil {
		return nil, err
	}
	if out.Todos == nil {
		out.Todos = []models.Todo{}
	}
	c, err := todos.Count(ctx, a)
	if err != nil {
		return nil, err
	}
	out.Counts = *c
	switch n := len(out.Todos); {
	case n == 0 && (in.State == "" || in.State == models.TodoOpen):
		out.Summary = "Nothing to answer right now."
	case n == 1:
		out.Summary = "1 question waiting for you."
	default:
		out.Summary = fmt.Sprintf("%d questions.", n)
	}
	return out, nil
}

func loadTodo(ctx context.Context, a *app.App, id string) (*models.Todo, error) {
	var t models.Todo
	if err := a.DB.GetByID(ctx, store.CTodos, a.WorkspaceID(), id, &t); err != nil {
		return nil, notFound("to-do", id)
	}
	return &t, nil
}

func yes(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "yes", "y", "same", "same person", "merge", "combine", "approve", "approved", "ok":
		return true
	}
	return false
}

func no(s string) bool {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "no", "n", "different", "different people", "reject", "rejected", "not yet", "not this one":
		return true
	}
	return false
}

// answerTodo changes the underlying data through the normal operations.
func answerTodo(ctx context.Context, a *app.App, t *models.Todo, answer string, sug *models.TodoSuggestion, in *TodoAnswerIn, got *answered) (string, error) {
	if in == nil {
		in = &TodoAnswerIn{}
	}
	if got == nil {
		got = &answered{}
	}
	value := strings.TrimSpace(answer)
	if sug != nil {
		value = sug.Value
	}
	if t.Kind == models.TodoFaceGroup && in.SameAs != "" {
		return answerFaceGroup(ctx, a, t, value, sug, in, got)
	}
	if value == "" {
		return "", invalid("Type an answer, or pick one of the suggestions.")
	}
	switch t.Kind {
	case models.TodoSpeakerUnnamed, models.TodoSpeakerMatch:
		personID := in.PersonID
		if sug != nil && sug.PersonID != "" {
			personID = sug.PersonID
		}
		res, err := speakers.Name(ctx, a, speakers.NameInput{AssetID: t.AssetID, Label: t.Context.Label, Name: value, PersonID: personID, NewPerson: in.NewPerson && personID == "", Confirm: in.SamePerson})
		if err != nil {
			return "", speakerErrFor(err, answerCommand(t.ID))
		}
		afterSpeakerChange(ctx, a, res.Library, res.Person.ID)
		got.as(&res.Person)
		got.separate = res.Dropped
		if res.Library != nil {
			got.autoMerged = res.Library.Merged
		}
		if t.Context.PersonID != "" && t.Context.PersonID != res.Person.ID {
			lib, _ := speakers.Reject(ctx, a, t.AssetID, res.Voice.Label, t.Context.PersonID)
			afterSpeakerChange(ctx, a, lib, t.Context.PersonID)
		}
		done := fmt.Sprintf("That voice is %s now.", res.Person.Name)
		if res.MergedWith != "" {
			done += " It was the same person as another voice in this video, so they are combined."
		}
		if n := len(res.Recognized); n > 0 {
			done += fmt.Sprintf(" We also recognized %s in %s.", res.Person.Name, count(n, "other video", "other videos"))
		}
		return done, nil
	case models.TodoMusicRights:
		out, err := setRights(ctx, a, &AssetRightsIn{AssetID: t.AssetID, License: value})
		if err != nil {
			return "", err
		}
		return out.Summary, nil
	case models.TodoBriefMissing:
		var p models.Project
		if err := a.DB.GetByID(ctx, store.CProjects, a.WorkspaceID(), t.ProjectID, &p); err != nil {
			return "", notFound("project", t.ProjectID)
		}
		b := p.Brief
		switch t.Context.Field {
		case "goal":
			b.Goal = value
		case "audience":
			b.Audience = value
		case "tone":
			b.Tone = value
		default:
			return "", invalid("unknown brief field %q", t.Context.Field)
		}
		op, _ := Get("project.update")
		if _, err := Run(ctx, a, op, &ProjectUpdateIn{ID: p.ID, Brief: &b}); err != nil {
			return "", err
		}
		return fmt.Sprintf("Saved to the brief of “%s”.", p.Name), nil
	case models.TodoDuplicatePerson:
		switch {
		case yes(value):
			from, into := t.Context.OtherID, t.Context.PersonID
			var pa, pb models.Person
			if a.DB.GetByID(ctx, store.CPeople, a.WorkspaceID(), into, &pa) == nil && a.DB.GetByID(ctx, store.CPeople, a.WorkspaceID(), from, &pb) == nil && pb.Recordings > pa.Recordings {
				from, into = into, from
			}
			p, err := speakers.MergePeople(ctx, a, from, into)
			if err != nil {
				return "", speakerErr(err)
			}
			return fmt.Sprintf("Combined: everyone in those videos is %s now.", p.Name), nil
		case no(value):
			if err := speakers.MarkDistinct(ctx, a, t.Context.PersonID, t.Context.OtherID); err != nil {
				return "", err
			}
			return fmt.Sprintf("Noted: %s and %s are different people.", t.Context.PersonName, t.Context.OtherName), nil
		}
		return "", invalid("answer “same” or “different”")
	case models.TodoFacePerson:
		f, err := faces.Get(ctx, a, t.Context.FaceID)
		if err != nil {
			return "", faceErr(err)
		}
		switch {
		case yes(value) || (sug != nil && sug.PersonID != "" && sug.PersonID == t.Context.PersonID):
			p, _, err := speakers.PersonFor(ctx, a, "", t.Context.PersonID, false)
			if err != nil {
				return "", speakerErr(err)
			}
			if _, err := faces.Confirm(ctx, a, f.ID, *p); err != nil {
				return "", faceErr(err)
			}
			got.as(p)
			return fmt.Sprintf("That face is %s now (in “%s”).", p.Name, t.Context.AssetTitle), nil
		case no(value) || strings.EqualFold(value, "no, someone else"):
			if _, err := faces.Reject(ctx, a, f.ID, t.Context.PersonID); err != nil {
				return "", faceErr(err)
			}
			got.notIt()
			return fmt.Sprintf("Noted: that's not %s.", t.Context.PersonName), nil
		}
		// A typed name: who it really is.
		p, _, err := speakers.PersonFor(ctx, a, value, in.PersonID, in.NewPerson)
		if err != nil {
			return "", speakerErrFor(err, answerCommand(t.ID))
		}
		if p.ID != t.Context.PersonID {
			if _, err := faces.Reject(ctx, a, f.ID, t.Context.PersonID); err != nil {
				return "", faceErr(err)
			}
		}
		if _, err := faces.Confirm(ctx, a, f.ID, *p); err != nil {
			return "", faceErr(err)
		}
		got.as(p)
		return fmt.Sprintf("That face is %s now (in “%s”).", p.Name, t.Context.AssetTitle), nil
	case models.TodoFaceGroup:
		return answerFaceGroup(ctx, a, t, value, sug, in, got)
	case models.TodoRevisionApproval:
		if !callerOf(ctx).Human {
			return "", apperr.New(apperr.HumanOnly, "Only a person can approve a version.").
				WithHint("Approve it yourself on the To-dos page in the dashboard.").WithCommand("metarush revision approve " + t.RevisionID)
		}
		if !yes(value) && !no(value) {
			return "", invalid("Answer “approve” or “not yet”.")
		}
		op, _ := Get("revision.approve")
		if _, err := Run(ctx, a, op, &RevisionApproveIn{ID: t.RevisionID, Reject: no(value)}); err != nil {
			return "", err
		}
		if no(value) {
			return fmt.Sprintf("Version %d is marked not ready; keep working on it.", t.Context.Revision), nil
		}
		return fmt.Sprintf("Version %d is approved.", t.Context.Revision), nil
	case models.TodoSpelling:
		if !yes(value) && value != "add" {
			return "", invalid("Answer “add”, or dismiss the question.")
		}
		word := t.Context.Field
		heard := []string(nil)
		if sugs, err := transcripts.Suggestions(ctx, a, t.ProjectID); err == nil {
			for _, s := range sugs {
				if s.Word == word {
					heard = s.Heard
				}
			}
		}
		// The list the project uses: its own settings', or the ones it follows (D193).
		op, _ := Get("transcript.spelling.add")
		res, err := Run(ctx, a, op, &SpellingAddIn{ProjectID: SettingsOwner(ctx, a, t.ProjectID), Word: word, HeardAs: heard})
		if err != nil {
			return "", err
		}
		return res.(*SpellingAddOut).Summary, nil
	case models.TodoRemotionApproval:
		// A version of a Remotion source (D172): the same decision as under Sources.
		rm := t.Context.Remotion
		if rm == nil {
			return "", invalid("this question doesn't say which version it is about")
		}
		ref := rm.Name + "@" + rm.Version
		if !callerOf(ctx).Human {
			return "", apperr.New(apperr.HumanOnly, "Only a person can approve a version of a Remotion project.").
				WithHint("Look at it under Sources in the dashboard, then approve it there or on the To-dos page.").WithCommand("metarush remotion approve " + ref)
		}
		if !yes(value) && !no(value) {
			return "", invalid("Answer “approve” or “not this one”.")
		}
		raw, _ := json.Marshal(map[string]any{"ref": ref, "projectId": t.ProjectID, "reject": no(value)})
		out, err := Call(ctx, a, "remotion.approve", raw, callerOf(ctx))
		if err != nil {
			return "", err
		}
		var said struct {
			Summary string `json:"summary"`
		}
		if b, err := json.Marshal(out); err == nil {
			_ = json.Unmarshal(b, &said)
		}
		if said.Summary != "" {
			return said.Summary, nil
		}
		if no(value) {
			return fmt.Sprintf("Version %d of “%s” is turned down.", rm.Number, rm.Title), nil
		}
		return fmt.Sprintf("Version %d of “%s” is approved.", rm.Number, rm.Title), nil
	case models.TodoShipGate:
		// Whether versions wait for the quality check and sign-offs (D298):
		// an owner decides, as a person, through the same op as Settings → General.
		if !callerOf(ctx).Human {
			return "", apperr.New(apperr.HumanOnly, "Only a person decides whether versions wait for the quality check and sign-offs.").
				WithHint("Answer it yourself on the To-dos page, or in Settings → General.").WithCommand("metarush deliver gate on")
		}
		on := value == "on" || yes(value)
		if !on && value != "off" && !no(value) {
			return "", invalid("Answer “hold them back” or “just remind me”.")
		}
		raw, _ := json.Marshal(GateSetIn{On: on})
		if _, err := Call(ctx, a, "delivery.gate.set", raw, callerOf(ctx)); err != nil {
			return "", err
		}
		if on {
			return "Versions now wait for the quality check and their sign-offs before they go out; a person can still send one out anyway with a note.", nil
		}
		return "The quality check and sign-offs only remind; nothing is held back.", nil
	case models.TodoAbilityApproval:
		// A version of an ability (D231): the same decision as on its page,
		// for an owner, by a person.
		ab := t.Context.Ability
		if ab == nil {
			return "", invalid("this question doesn't say which version it is about")
		}
		ref := ab.Name + "@" + ab.Version
		if !callerOf(ctx).Human {
			return "", apperr.New(apperr.HumanOnly, "Only a person can approve an ability.").
				WithHint("Look at it under Library → Abilities, then approve it there or on the To-dos page.").WithCommand("metarush ability approve " + ref)
		}
		if !yes(value) && !no(value) {
			return "", invalid("Answer “approve” or “not this one”.")
		}
		raw, _ := json.Marshal(map[string]any{"ref": ref, "reject": no(value)})
		out, err := Call(ctx, a, "ability.approve", raw, callerOf(ctx))
		if err != nil {
			return "", err
		}
		var said struct {
			Message string `json:"message"`
		}
		if b, err := json.Marshal(out); err == nil {
			_ = json.Unmarshal(b, &said)
		}
		return said.Message, nil
	case models.TodoRecipeRun:
		// A run (Recipes) that waits (D204): go on (a person's OK, people
		// only) or stop it, through the same ops as its page.
		rr := t.Context.Recipe // the run of Recipes it asks about
		if rr == nil {
			return "", invalid("this question doesn't say which run it is about")
		}
		op := ""
		switch v := strings.ToLower(strings.TrimSpace(value)); {
		case v == "continue" || v == "go on" || yes(v):
			op = "recipe.continue"
			if !callerOf(ctx).Human {
				return "", apperr.New(apperr.HumanOnly, "Only a person lets a run go on.").
					WithHint("Open the run on the Library's Recipes tab, or answer this question there.")
			}
		case v == "stop" || no(v):
			op = "recipe.stop"
		default:
			return "", invalid("Answer “go on” or “stop”.")
		}
		in := map[string]any{"run": rr.RunID}
		if op == "recipe.continue" {
			// The question says what the run asks to spend more; going on agrees to it.
			var run models.RecipeRun
			if a.DB.GetByID(ctx, store.CRecipeRuns, a.WorkspaceID(), rr.RunID, &run) == nil && run.WaitingUSD > 0 {
				in["moreUsd"] = run.WaitingUSD
			}
		}
		raw, _ := json.Marshal(in)
		out, err := Call(ctx, a, op, raw, callerOf(ctx))
		if err != nil {
			return "", err
		}
		var said struct {
			Message string `json:"message"`
		}
		if b, err := json.Marshal(out); err == nil {
			_ = json.Unmarshal(b, &said)
		}
		return said.Message, nil
	}
	return "", invalid("this kind of question cannot be answered here")
}

func init() {
	Def("todos.list", "List open questions MetaRush needs a person to answer (filter by project, video or kind)", Flags{}, func(ctx context.Context, a *app.App, in *TodoListIn) (*TodoListOut, error) {
		return listTodos(ctx, a, in)
	})
	Def("todos.show", "Show one question with what to listen to or look at and how to answer", Flags{}, func(ctx context.Context, a *app.App, in *TodoIn) (*TodoShowOut, error) {
		t, err := loadTodo(ctx, a, in.ID)
		if err != nil {
			return nil, err
		}
		return &TodoShowOut{Todo: *t, HowToAnswer: howToAnswer(*t)}, nil
	})
	Def("todos.counts", "Count open questions per video, project and kind (for badges)", Flags{}, func(ctx context.Context, a *app.App, _ *TodoCountsIn) (*todos.Counts, error) {
		return todos.Count(ctx, a)
	})
	Def("todos.answer", "Answer a question; the answer is applied (names a voice, records music rights, fills the brief…)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *TodoAnswerIn) (*TodoAnswerOut, error) {
		t, err := loadTodo(ctx, a, in.ID)
		if err != nil {
			return nil, err
		}
		if t.State != models.TodoOpen {
			return nil, apperr.New(apperr.Conflict, "this question was already %s", t.State).WithDetails(map[string]any{"answer": t.Answer, "resolution": t.Resolution})
		}
		var sug *models.TodoSuggestion
		if in.Suggestion > 0 {
			if in.Suggestion > len(t.Suggestions) {
				return nil, invalid("there are only %d suggested answers", len(t.Suggestions))
			}
			s := t.Suggestions[in.Suggestion-1]
			sug = &s
		} else if strings.TrimSpace(in.Answer) == "" && in.SameAs == "" {
			return nil, invalid("give an answer or pick a suggestion")
		} else if !t.FreeText {
			// Typed answers must match a suggestion when free text is not accepted.
			for i := range t.Suggestions {
				if strings.EqualFold(t.Suggestions[i].Value, in.Answer) || strings.EqualFold(t.Suggestions[i].Label, in.Answer) {
					sug = &t.Suggestions[i]
				}
			}
			if sug == nil && t.Kind != models.TodoDuplicatePerson && t.Kind != models.TodoRevisionApproval && t.Kind != models.TodoRemotionApproval && t.Kind != models.TodoRecipeRun && t.Kind != models.TodoAbilityApproval && t.Kind != models.TodoShipGate {
				return nil, invalid("pick one of the suggested answers")
			}
		}
		var got answered
		done, err := answerTodo(ctx, a, t, in.Answer, sug, in, &got)
		if err != nil {
			return nil, err
		}
		answer := strings.TrimSpace(in.Answer)
		if sug != nil {
			answer = sug.Value
		}
		if in.SameAs != "" && answer == "" {
			answer = "same as another question"
		}
		ft, err := todos.Finish(ctx, a, t.ID, models.TodoAnswered, answer)
		if err != nil {
			return nil, err
		}
		if got.judged {
			// What the names it offered rested on, and whether they were right (D189).
			todos.Scored(ctx, a, t, got.personID, got.name, got.no)
		}
		a.Audit(ctx, "todos.answer", "todo", t.ID, t.ProjectID, map[string]any{"kind": t.Kind, "answer": answer})
		return &TodoAnswerOut{Todo: *ft, Done: done, Separate: got.separate, AutoMerged: got.autoMerged}, nil
	})
	Def("todos.dismiss", "Dismiss a question (it will not be asked again)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *TodoDismissIn) (*TodoAnswerOut, error) {
		t, err := loadTodo(ctx, a, in.ID)
		if err != nil {
			return nil, err
		}
		if t.State != models.TodoOpen {
			return nil, apperr.New(apperr.Conflict, "this question was already %s", t.State)
		}
		done := "Dismissed. We won't ask this again."
		if t.Kind == models.TodoShipGate {
			// Whether versions wait is an owner's decision (D298): dismissing the
			// question is deciding "just remind me", so only an owner, as a person.
			if !callerOf(ctx).Human || !IsOwnerCaller(ctx) {
				return nil, apperr.New(apperr.HumanOnly, "Only an owner, as a person, can set this question aside: it decides whether versions wait for the quality check and sign-offs.").
					WithHint("An owner answers it on the To-dos page or in Settings → General.").WithCommand("metarush deliver gate on")
			}
			if err := setWorkspaceGate(ctx, a, false); err != nil { // closes the question too
				return nil, err
			}
			ft, err := loadTodo(ctx, a, t.ID)
			if err != nil {
				return nil, err
			}
			a.Audit(ctx, "todos.dismiss", "todo", t.ID, t.ProjectID, map[string]any{"kind": t.Kind})
			return &TodoAnswerOut{Todo: *ft, Done: "Set aside: the quality check and sign-offs only remind, as before. Settings → General → Before it goes out changes it."}, nil
		}
		if t.Kind == models.TodoFaceGroup && t.Context.GroupID != "" {
			// Skipping a group of faces skips the faces: never asked about again (D155).
			if _, err := faces.SkipGroup(ctx, a, t.AssetID, t.Context.GroupID); err != nil {
				return nil, err
			}
			done = "Skipped. We won't ask about these faces again."
		}
		ft, err := todos.Finish(ctx, a, t.ID, models.TodoDismissed, strings.TrimSpace(in.Note))
		if err != nil {
			return nil, err
		}
		a.Audit(ctx, "todos.dismiss", "todo", t.ID, t.ProjectID, map[string]any{"kind": t.Kind})
		return &TodoAnswerOut{Todo: *ft, Done: done}, nil
	})
	Def("todos.sync", "Re-check a project or video for questions that need a person (and close ones that no longer apply)", Flags{Mutating: true}, func(ctx context.Context, a *app.App, in *TodoSyncIn) (*TodoSyncOut, error) {
		filter := bson.M{"state": models.AssetAvailable}
		switch {
		case in.AssetID != "":
			filter["_id"] = in.AssetID
		case in.ProjectID != "":
			filter["projectIds"] = in.ProjectID
		}
		var assets []models.Asset
		if err := a.DB.Find(ctx, store.CAssets, a.WorkspaceID(), filter, &assets, options.Find().SetProjection(bson.M{"_id": 1, "speakers": 1})); err != nil {
			return nil, err
		}
		out := &TodoSyncOut{}
		for _, as := range assets {
			if as.Speakers != nil && as.Speakers.State == "available" {
				if err := speakers.Reconcile(ctx, a, as.ID); err != nil {
					return nil, err
				}
				out.Checked++
			}
		}
		pf := bson.M{"archived": false}
		if in.ProjectID != "" {
			pf = bson.M{"_id": in.ProjectID}
		}
		if in.AssetID == "" {
			var ps []models.Project
			if err := a.DB.Find(ctx, store.CProjects, a.WorkspaceID(), pf, &ps); err != nil {
				return nil, err
			}
			for i := range ps {
				if h, ok := ps[i].Branches["main"]; ok && h.Head != "" {
					var r models.Revision
					if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), h.Head, &r) == nil {
						reconcileRevisionTodos(ctx, a, &r, &ps[i], true)
						// A project with a cut was made from a story request: its brief questions apply.
						reconcileBriefTodos(ctx, a, &ps[i])
					}
				}
				closeBriefTodos(ctx, a, &ps[i])
				out.Checked++
			}
		}
		c, err := todos.Count(ctx, a)
		if err != nil {
			return nil, err
		}
		out.Open = c.Total
		out.Summary = fmt.Sprintf("Checked %s. %s waiting.", count(out.Checked, "item", "items"), count(out.Open, "question", "questions"))
		return out, nil
	})
}

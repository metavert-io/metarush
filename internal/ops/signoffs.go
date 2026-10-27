package ops

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// Sign-offs (D166, docs/keeping.md): the OKs a cut needs before it goes out
// ("Lena OKs the final version"). Open ones are a reminder before approving,
// exporting and delivering; they never block. A cut's checklist is the
// project's own sign-offs for it, then the house sign-offs of the Content
// Settings the project uses (D200: "Legal OKs it before export"), which every
// project using them needs; whether a house sign-off was given is kept per
// project and cut, so an OK in one project never ticks it in another.

// SignoffsIn names a project (and its cut).
type SignoffsIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=Project ID or its exact name."`
	Branch    string `json:"branch,omitempty" jsonschema:"description=Default main."`
}

// SignoffsOut is a cut's checklist.
type SignoffsOut struct {
	ProjectID string `json:"projectId"`
	Project   string `json:"project"`
	Branch    string `json:"branch"`
	// Signoffs are the project's own for this cut, then the house sign-offs
	// of the settings it uses (house, houseOf).
	Signoffs []models.Signoff `json:"signoffs"`
	Open     int              `json:"open" jsonschema:"description=Sign-offs still waiting."`
	Summary  string           `json:"summary"`
}

// SignoffAddIn adds a sign-off to a cut's checklist.
type SignoffAddIn struct {
	SignoffsIn
	Text string `json:"text" jsonschema:"minLength=1,maxLength=200,description=Who must OK what (Lena OKs the final version)."`
}

// SignoffDecideIn ticks, unticks or removes a sign-off.
type SignoffDecideIn struct {
	SignoffsIn
	Signoff string `json:"signoff" jsonschema:"description=The sign-off: its number (1 = the first)\\, its ID or its words."`
	Note    string `json:"note,omitempty" jsonschema:"maxLength=300"`
}

func signoffsOf(p *models.Project, branch string) []models.Signoff {
	out := []models.Signoff{}
	for _, s := range p.Signoffs {
		b := s.Branch
		if b == "" {
			b = "main"
		}
		if b == branch {
			out = append(out, s)
		}
	}
	return out
}

// OwnSignoffs are a project cut's own sign-offs (not the house's).
func OwnSignoffs(p *models.Project, branch string) []models.Signoff {
	if branch == "" {
		branch = "main"
	}
	return signoffsOf(p, branch)
}

// SignoffsFor are a cut's sign-offs: the project's own for that cut, then the
// house sign-offs of the Content Settings it uses (D200), each with whether
// it was given for this project's cut.
func SignoffsFor(ctx context.Context, a *app.App, p *models.Project, branch string) []models.Signoff {
	if branch == "" {
		branch = "main"
	}
	out := signoffsOf(p, branch)
	for _, h := range content.HouseSignoffs(ctx, a, p.ID) {
		if st := houseState(p, h.ID, branch); st != nil {
			h.Done, h.DoneBy, h.DoneAt, h.Note, h.Version = st.Done, st.DoneBy, st.DoneAt, st.Note, st.Version
		}
		if branch != "main" {
			h.Branch = branch
		}
		out = append(out, h)
	}
	return out
}

// houseState is whether a house sign-off was given for a project's cut.
func houseState(p *models.Project, id, branch string) *models.SignoffState {
	for i, st := range p.HouseSignoffs {
		b := st.Branch
		if b == "" {
			b = "main"
		}
		if st.ID == id && b == branch {
			return &p.HouseSignoffs[i]
		}
	}
	return nil
}

func signoffsOut(ctx context.Context, a *app.App, p *models.Project, branch string) *SignoffsOut {
	out := &SignoffsOut{ProjectID: p.ID, Project: p.Name, Branch: branch, Signoffs: SignoffsFor(ctx, a, p, branch)}
	for _, s := range out.Signoffs {
		if !s.Done {
			out.Open++
		}
	}
	switch {
	case len(out.Signoffs) == 0:
		out.Summary = p.Name + " has no sign-offs to wait for."
	case out.Open == 0:
		out.Summary = fmt.Sprintf("Everyone has OK'd %s.", p.Name)
	case out.Open == 1:
		out.Summary = fmt.Sprintf("1 sign-off is still waiting for %s.", p.Name)
	default:
		out.Summary = fmt.Sprintf("%d sign-offs are still waiting for %s.", out.Open, p.Name)
	}
	return out
}

// OpenSignoffs are a project cut's sign-offs still waiting (for reminders):
// its own and the house's (D200).
func OpenSignoffs(ctx context.Context, a *app.App, p *models.Project, branch string) []models.Signoff {
	return OpenOf(SignoffsFor(ctx, a, p, branch))
}

// OpenOf are the sign-offs of a list still waiting.
func OpenOf(list []models.Signoff) []models.Signoff {
	var out []models.Signoff
	for _, s := range list {
		if !s.Done {
			out = append(out, s)
		}
	}
	return out
}

// SignoffReminder says which sign-offs are still waiting, in plain words ("" none).
func SignoffReminder(open []models.Signoff) string {
	if len(open) == 0 {
		return ""
	}
	var texts []string
	for _, s := range open {
		texts = append(texts, "“"+s.Text+"”")
	}
	if len(open) == 1 {
		return "Still waiting for a sign-off: " + texts[0] + "."
	}
	return fmt.Sprintf("Still waiting for %d sign-offs: %s.", len(open), strings.Join(texts, "; "))
}

func signoffIndex(list []models.Signoff, ref string) int {
	ref = strings.TrimSpace(ref)
	if n, err := strconv.Atoi(ref); err == nil && n >= 1 && n <= len(list) {
		return n - 1
	}
	for i, s := range list {
		if s.ID == ref || ruleKey(s.Text) == ruleKey(ref) {
			return i
		}
	}
	return -1
}

// AddSignoff adds a sign-off unless the cut already has the same words (its
// own, or a house sign-off of the settings it uses).
func AddSignoff(ctx context.Context, a *app.App, p *models.Project, branch, text string) (*SignoffsOut, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false, invalid("Say who must OK what, in a few words.")
	}
	if branch == "" {
		branch = "main"
	}
	for _, s := range SignoffsFor(ctx, a, p, branch) {
		if ruleKey(s.Text) == ruleKey(text) {
			return signoffsOut(ctx, a, p, branch), false, nil
		}
	}
	if len(p.Signoffs) >= 50 {
		return nil, false, invalid("A project keeps at most 50 sign-offs; take one out first.")
	}
	s := models.Signoff{ID: ids.New(ids.Signoff), Text: text, AddedBy: a.Actor, AddedAt: time.Now().UTC()}
	if branch != "main" {
		s.Branch = branch
	}
	if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": p.ID, "workspaceId": a.WorkspaceID()},
		bson.M{"$push": bson.M{"signoffs": s}, "$set": bson.M{"updatedAt": time.Now().UTC()}}); err != nil {
		return nil, false, err
	}
	a.Audit(ctx, "cut.signoffs.add", "project", p.ID, p.ID, map[string]any{"signoff": s.ID})
	np, err := resolveProject(ctx, a, p.ID)
	if err != nil {
		return nil, false, err
	}
	return signoffsOut(ctx, a, np, branch), true, nil
}

func decideSignoff(ctx context.Context, a *app.App, in *SignoffDecideIn, verb string) (*SignoffsOut, error) {
	p, err := resolveProject(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	branch := in.Branch
	if branch == "" {
		branch = "main"
	}
	list := SignoffsFor(ctx, a, p, branch)
	i := signoffIndex(list, in.Signoff)
	if i < 0 {
		return nil, apperr.New(apperr.NotFound, "%s has no sign-off %q.", p.Name, in.Signoff).WithHint("Give its number from the list (1 = the first) or its words.")
	}
	if verb == "done" || verb == "remove" {
		// With the gate on, a waiting sign-off holds versions back: only a person says it's given or not needed (D299).
		what := map[string]string{"done": "say a sign-off was given", "remove": "take a sign-off off the checklist"}[verb]
		if err := loosening(ctx, a, p.ID, what, fmt.Sprintf("metarush cut signoffs %s %s %q", verb, p.ID, list[i].Text), false); err != nil {
			return nil, err
		}
	}
	if list[i].House {
		return decideHouseSignoff(ctx, a, p, branch, list[i], in, verb)
	}
	id := list[i].ID
	now := time.Now().UTC()
	var upd bson.M
	switch verb {
	case "done":
		upd = bson.M{"$set": bson.M{"signoffs.$.done": true, "signoffs.$.doneBy": a.Actor, "signoffs.$.doneAt": now, "signoffs.$.note": strings.TrimSpace(in.Note), "signoffs.$.version": headNumber(ctx, a, p, branch), "updatedAt": now}}
	case "undo":
		upd = bson.M{"$set": bson.M{"signoffs.$.done": false, "updatedAt": now}, "$unset": bson.M{"signoffs.$.doneBy": "", "signoffs.$.doneAt": "", "signoffs.$.version": ""}}
	case "remove":
		if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": p.ID, "workspaceId": a.WorkspaceID()},
			bson.M{"$pull": bson.M{"signoffs": bson.M{"id": id}}, "$set": bson.M{"updatedAt": now}}); err != nil {
			return nil, err
		}
	}
	if upd != nil {
		if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": p.ID, "workspaceId": a.WorkspaceID(), "signoffs.id": id}, upd); err != nil {
			return nil, err
		}
	}
	a.Audit(ctx, "cut.signoffs."+verb, "project", p.ID, p.ID, map[string]any{"signoff": id})
	np, err := resolveProject(ctx, a, p.ID)
	if err != nil {
		return nil, err
	}
	return signoffsOut(ctx, a, np, branch), nil
}

// headNumber is a cut's latest version number (0 none).
func headNumber(ctx context.Context, a *app.App, p *models.Project, branch string) int {
	if head := p.Branches[branch].Head; head != "" {
		var r models.Revision
		if a.DB.GetByID(ctx, store.CRevisions, a.WorkspaceID(), head, &r) == nil {
			return r.Number
		}
	}
	return 0
}

// decideHouseSignoff ticks or unticks a house sign-off for this project's cut
// (D200); taking it off is refused: it is the settings', so it goes there.
func decideHouseSignoff(ctx context.Context, a *app.App, p *models.Project, branch string, s models.Signoff, in *SignoffDecideIn, verb string) (*SignoffsOut, error) {
	if verb == "remove" {
		owner := SettingsOwner(ctx, a, p.ID)
		return nil, apperr.New(apperr.Conflict, "“%s” is a house sign-off of “%s”'s settings, so every project using them needs it.", s.Text, s.HouseOf).
			WithHint("Take it out of those settings (it goes for every project using them), or tick it when the OK is given.").
			WithCommand(fmt.Sprintf("metarush content signoffs remove %q --project %s", s.Text, owner)).
			WithDetails(map[string]any{"signoff": s.ID, "settingsOf": owner})
	}
	now := time.Now().UTC()
	var keep []models.SignoffState
	for _, st := range p.HouseSignoffs {
		b := st.Branch
		if b == "" {
			b = "main"
		}
		if st.ID == s.ID && b == branch {
			continue
		}
		keep = append(keep, st)
	}
	if verb == "done" {
		actor := a.Actor
		st := models.SignoffState{ID: s.ID, Done: true, DoneBy: &actor, DoneAt: &now, Note: strings.TrimSpace(in.Note), Version: headNumber(ctx, a, p, branch)}
		if branch != "main" {
			st.Branch = branch
		}
		keep = append(keep, st)
	}
	if len(keep) > 200 {
		keep = keep[len(keep)-200:]
	}
	set := bson.M{"updatedAt": now}
	upd := bson.M{"$set": set}
	if len(keep) == 0 {
		upd["$unset"] = bson.M{"houseSignoffs": ""}
	} else {
		set["houseSignoffs"] = keep
	}
	if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": p.ID, "workspaceId": a.WorkspaceID()}, upd); err != nil {
		return nil, err
	}
	a.Audit(ctx, "cut.signoffs."+verb, "project", p.ID, p.ID, map[string]any{"signoff": s.ID, "house": true})
	np, err := resolveProject(ctx, a, p.ID)
	if err != nil {
		return nil, err
	}
	return signoffsOut(ctx, a, np, branch), nil
}

// ---- house sign-offs (D200) ----

// HouseSignoffsIn names whose house sign-offs ("" : the default's).
type HouseSignoffsIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name): the house sign-offs of the settings it uses. Empty: the default's."`
}

// HouseSignoffsOut are the house sign-offs of a project's settings.
type HouseSignoffsOut struct {
	// ProjectID and Project are the project whose own settings hold them.
	ProjectID string           `json:"projectId,omitempty"`
	Project   string           `json:"project,omitempty"`
	Signoffs  []models.Signoff `json:"signoffs"`
	// ReadOnly: asked about a project that uses another's settings.
	ReadOnly bool `json:"readOnly"`
	// UsedBy counts the other projects using these settings.
	UsedBy  int    `json:"usedBy"`
	Summary string `json:"summary"`
}

// HouseSignoffAddIn adds a house sign-off.
type HouseSignoffAddIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own settings. Empty: the default's."`
	Text      string `json:"text" jsonschema:"minLength=1,maxLength=200,description=Who must OK what before a video goes out (Legal OKs it before export)."`
}

// HouseSignoffRemoveIn takes a house sign-off out.
type HouseSignoffRemoveIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own settings. Empty: the default's."`
	Signoff   string `json:"signoff" jsonschema:"description=The house sign-off: its number (1 = the first)\\, its ID or its words."`
}

// houseSignoffs is the house sign-offs a project uses ("" : the default's).
func houseSignoffs(ctx context.Context, a *app.App, projectID string) (*HouseSignoffsOut, error) {
	g, r, err := content.Resolve(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	out := &HouseSignoffsOut{Signoffs: []models.Signoff{}, ProjectID: r.Owner, Project: g.Name(r.Owner), ReadOnly: projectID != "" && r.Owner != projectID}
	if r.Owner != "" {
		rec, err := content.Record(ctx, a, r.Owner)
		if err != nil {
			return nil, err
		}
		if rec != nil && rec.Signoffs != nil {
			out.Signoffs = rec.Signoffs
		}
		out.UsedBy = len(g.UsedBy(r.Owner))
		if projectID != "" && projectID != r.Owner {
			out.UsedBy--
		}
	}
	switch n := len(out.Signoffs); {
	case r.Owner == "":
		out.Summary = "No project's settings are used here yet, so there are no house sign-offs."
	case n == 0:
		out.Summary = fmt.Sprintf("%s's settings have no house sign-offs yet.", q(out.Project))
	default:
		out.Summary = fmt.Sprintf("%s's settings have %s.", q(out.Project), pluralWord(n, "house sign-off"))
	}
	return out, nil
}

// AddHouseSignoff adds a sign-off to the house sign-offs of a project's own
// settings (owner must have them: callers check whose settings a person chose).
func AddHouseSignoff(ctx context.Context, a *app.App, owner, text string) (*models.Signoff, bool, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, false, invalid("Say who must OK what, in a few words.")
	}
	if len([]rune(text)) > 200 {
		return nil, false, invalid("Say it in at most 200 letters.")
	}
	rec, err := content.Record(ctx, a, owner)
	if err != nil {
		return nil, false, err
	}
	if rec != nil {
		for _, s := range rec.Signoffs {
			if ruleKey(s.Text) == ruleKey(text) {
				return &s, false, nil
			}
		}
		if len(rec.Signoffs) >= 50 {
			return nil, false, invalid("Settings keep at most 50 house sign-offs; take one out first.")
		}
	}
	s := models.Signoff{ID: ids.New(ids.Signoff), Text: text, AddedBy: a.Actor, AddedAt: time.Now().UTC()}
	if err := content.Update(ctx, a, owner, bson.M{"$push": bson.M{"signoffs": s}}); err != nil {
		return nil, false, err
	}
	a.Audit(ctx, "content.signoffs.add", "project", owner, owner, map[string]any{"signoff": s.ID})
	return &s, true, nil
}

func init() {
	Def("cut.signoffs.list", "A cut's sign-offs: the OKs it needs before it goes out (the project's own and the house sign-offs of its Content Settings), and who gave them", Flags{},
		func(ctx context.Context, a *app.App, in *SignoffsIn) (*SignoffsOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			branch := in.Branch
			if branch == "" {
				branch = "main"
			}
			return signoffsOut(ctx, a, p, branch), nil
		})
	Def("cut.signoffs.add", "Add a sign-off a cut needs before it goes out (Lena OKs the final version); a reminder before approving, exporting and delivering, and a hold on delivery while the ship gate is on", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *SignoffAddIn) (*SignoffsOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			out, _, err := AddSignoff(ctx, a, p, in.Branch, in.Text)
			return out, err
		})
	Def("cut.signoffs.done", "Tick a sign-off: that OK was given (for the cut's latest version; a house sign-off is ticked for this project only)", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *SignoffDecideIn) (*SignoffsOut, error) {
			return decideSignoff(ctx, a, in, "done")
		})
	Def("cut.signoffs.undo", "Untick a sign-off: the OK is needed again", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *SignoffDecideIn) (*SignoffsOut, error) {
			return decideSignoff(ctx, a, in, "undo")
		})
	Def("cut.signoffs.remove", "Take a sign-off off a cut's checklist (a house sign-off is taken out of its Content Settings instead: content.signoffs.remove)", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *SignoffDecideIn) (*SignoffsOut, error) {
			return decideSignoff(ctx, a, in, "remove")
		})
	Def("content.signoffs.list", "House sign-offs: OKs every project using a project's Content Settings needs before a video goes out (a project's own sign-offs are cut.signoffs)", Flags{},
		func(ctx context.Context, a *app.App, in *HouseSignoffsIn) (*HouseSignoffsOut, error) {
			pid, err := projectRefID(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			return houseSignoffs(ctx, a, pid)
		})
	Def("content.signoffs.add", "Add a house sign-off (Legal OKs it before export) to a project's own Content Settings — every project using them needs it; without a project, the default's", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *HouseSignoffAddIn) (*HouseSignoffsOut, error) {
			pid, err := projectRefID(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			t, g, err := content.EditTarget(ctx, a, pid)
			if err != nil {
				return nil, err
			}
			s, created, err := AddHouseSignoff(ctx, a, t.ProjectID, in.Text)
			if err != nil {
				return nil, err
			}
			out, err := houseSignoffs(ctx, a, t.ProjectID)
			if err != nil {
				return nil, err
			}
			if created {
				out.Summary = fmt.Sprintf("Added the house sign-off %s to %s's settings: every project using them waits for it before a video goes out.", q(s.Text), q(t.ProjectName))
			} else {
				out.Summary = fmt.Sprintf("%s's settings have the house sign-off %s already.", q(t.ProjectName), q(s.Text))
			}
			if note := t.Note(g); note != "" {
				out.Summary += " " + note
			}
			return out, nil
		})
	Def("content.signoffs.remove", "Take a house sign-off out of a project's own Content Settings (without a project, the default's)", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *HouseSignoffRemoveIn) (*HouseSignoffsOut, error) {
			pid, err := projectRefID(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			t, g, err := content.EditTarget(ctx, a, pid)
			if err != nil {
				return nil, err
			}
			rec, err := content.Record(ctx, a, t.ProjectID)
			if err != nil {
				return nil, err
			}
			var list []models.Signoff
			if rec != nil {
				list = rec.Signoffs
			}
			i := signoffIndex(list, in.Signoff)
			if i < 0 {
				return nil, apperr.New(apperr.NotFound, "%s's settings have no house sign-off %q.", t.ProjectName, in.Signoff).WithHint("Give its number from the list (1 = the first) or its words.")
			}
			gone := list[i]
			if err := loosening(ctx, a, t.ProjectID, "take a house sign-off out", fmt.Sprintf("metarush content signoffs remove %q --project %s", gone.Text, t.ProjectID), false); err != nil {
				return nil, err
			}
			if err := content.Update(ctx, a, t.ProjectID, bson.M{"$pull": bson.M{"signoffs": bson.M{"id": gone.ID}}}); err != nil {
				return nil, err
			}
			// The OKs projects gave for it go with it.
			_, _ = a.DB.C(store.CProjects).UpdateMany(ctx, bson.M{"workspaceId": a.WorkspaceID(), "houseSignoffs.id": gone.ID},
				bson.M{"$pull": bson.M{"houseSignoffs": bson.M{"id": gone.ID}}})
			a.Audit(ctx, "content.signoffs.remove", "project", t.ProjectID, t.ProjectID, map[string]any{"signoff": gone.ID})
			out, err := houseSignoffs(ctx, a, t.ProjectID)
			if err != nil {
				return nil, err
			}
			out.Summary = fmt.Sprintf("Took the house sign-off %s out of %s's settings.", q(gone.Text), q(t.ProjectName))
			if note := t.Note(g); note != "" {
				out.Summary += " " + note
			}
			return out, nil
		})
}

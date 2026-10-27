package ops

import (
	"context"
	"fmt"
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

// Content Settings (D192, D193): a project's brand, spelling list, narration
// voice and house rules — its own, another project's (followed live) or the
// default's (the project whose "Use for new projects" is ticked). The parts
// are edited through their own ops (brand.set, transcript.spelling.add,
// provider.settings, content.rules.add); these ops choose whose settings a
// project uses and show them.

// ContentProjectRef names a project.
type ContentProjectRef struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Archived bool   `json:"archived,omitempty"`
}

// ContentPartOut is one part of Content Settings as a project sees it.
type ContentPartOut struct {
	Name    string `json:"name" jsonschema:"enum=brand,enum=spelling,enum=voice,enum=rules,enum=style,enum=signoffs"`
	Label   string `json:"label"`
	Empty   bool   `json:"empty"`
	Summary string `json:"summary"`
}

// ContentShowIn names a project ("" : the default's — what new projects and
// footage in no project use).
type ContentShowIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name). Empty: the default's settings\\, which new projects and footage in no project use."`
}

// ContentShowOut is where a project's Content Settings come from and what they hold.
type ContentShowOut struct {
	// Project is the project asked about (nil: the default's).
	Project *ContentProjectRef `json:"project,omitempty"`
	// Uses is the project's choice: default, project (another project's) or own.
	Uses string `json:"uses" jsonschema:"enum=default,enum=project,enum=own"`
	// Follows is the project it follows (uses project).
	Follows *ContentProjectRef `json:"follows,omitempty"`
	// From is the project whose own settings it gets (nil: none are set up).
	From *ContentProjectRef `json:"from,omitempty"`
	// Through are the projects between it and From, when it follows a chain.
	Through []ContentProjectRef `json:"through,omitempty"`
	// Default is the project whose settings new projects use (its "Use for new projects").
	Default *ContentProjectRef `json:"default,omitempty"`
	// IsDefault: this project's "Use for new projects" is ticked.
	IsDefault bool `json:"isDefault"`
	// ReadOnly: it uses another project's settings; change them there, or give it its own.
	ReadOnly bool `json:"readOnly"`
	// UsedBy are the other projects whose settings are these (directly, through
	// the default or a chain).
	UsedBy []ContentProjectRef `json:"usedBy"`
	// KeptAside: it has settings of its own from before, set aside while it uses another's.
	KeptAside bool `json:"keptAside,omitempty"`
	// Parts are the brand, spelling, narration voice and house rules.
	Parts []ContentPartOut `json:"parts"`
	// Settings are the settings it uses (From's own).
	Settings models.ContentSettings `json:"settings"`
	// Choices are the projects it can follow (none that would go round in a circle), by name.
	Choices []ContentProjectRef `json:"choices"`
	// Changed says what a change did (content.follow, own, setDefault).
	Changed string `json:"changed,omitempty"`
	Summary string `json:"summary"`
}

// ContentFollowIn makes a project use another project's settings, or the default's.
type ContentFollowIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=The project (ID or name)."`
	// Follow is the project whose settings it uses from now on, live.
	Follow string `json:"follow,omitempty" jsonschema:"description=The project (ID or name) whose settings it uses from now on (changes there show up here)."`
	// UseDefault: it uses the default settings (what new projects use).
	UseDefault bool `json:"useDefault,omitempty" jsonschema:"description=Use the default settings (what new projects use) instead."`
}

// ContentOwnIn gives a project its own settings.
type ContentOwnIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=The project (ID or name)."`
	// Copy starts them as a copy of what it uses now (else empty, or the ones it had before).
	Copy bool `json:"copy,omitempty" jsonschema:"description=Start them as a copy of the settings it uses now (else they start empty\\, or as the ones it had before)."`
}

// ContentDefaultIn makes a project's settings the default.
type ContentDefaultIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=The project (ID or name) whose settings new projects use from now on."`
}

// HouseRulesIn names whose house rules ("" : the default's).
type HouseRulesIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name): the house rules of the settings it uses. Empty: the default's."`
}

// HouseRulesOut are the house rules of a project's settings.
type HouseRulesOut struct {
	// ProjectID and Project are the project whose own settings hold them.
	ProjectID string               `json:"projectId,omitempty"`
	Project   string               `json:"project,omitempty"`
	Rules     []models.ProjectRule `json:"rules"`
	// ReadOnly: asked about a project that uses another's settings.
	ReadOnly bool `json:"readOnly"`
	// UsedBy counts the other projects using these settings.
	UsedBy  int    `json:"usedBy"`
	Summary string `json:"summary"`
}

// HouseRuleAddIn adds a house rule.
type HouseRuleAddIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own settings. Empty: the default's."`
	Text      string `json:"text" jsonschema:"minLength=1,maxLength=300,description=The rule in plain words (The name bar never animates)."`
	Check     bool   `json:"check,omitempty" jsonschema:"description=A problem to check for in every quality check of every project using these settings."`
	Measure   string `json:"measure,omitempty" jsonschema:"maxLength=40,description=What the quality check measures (default: recognized from the words)."`
	From      string `json:"from,omitempty" jsonschema:"maxLength=200"`
	// Promise: a promise made to the client (D300): it must be measurable,
	// and a broken one holds every version using these settings back.
	Promise bool `json:"promise,omitempty" jsonschema:"description=A promise made to the client: measured in every quality check\\, and a broken one holds the version back from going out (it must be measurable; otherwise keep it as a sign-off)."`
}

// HouseRuleRemoveIn takes a house rule out.
type HouseRuleRemoveIn struct {
	ProjectID string `json:"projectId,omitempty" jsonschema:"description=A project (ID or name) with its own settings. Empty: the default's."`
	Rule      string `json:"rule" jsonschema:"description=The rule: its number (1 = the first)\\, its ID or its words."`
}

func refOf(g *content.Graph, id string) *ContentProjectRef {
	if id == "" || !g.Has(id) {
		return nil
	}
	return &ContentProjectRef{ID: id, Name: g.Name(id), Archived: g.Archived(id)}
}

// contentShow is content.show for a project (nil: the default's).
func contentShow(ctx context.Context, a *app.App, p *models.Project) (*ContentShowOut, error) {
	g, err := content.Load(ctx, a)
	if err != nil {
		return nil, err
	}
	pid := ""
	if p != nil {
		pid = p.ID
	}
	r := g.Resolve(pid)
	out := &ContentShowOut{Uses: r.Uses, UsedBy: []ContentProjectRef{}, Parts: []ContentPartOut{}, Choices: []ContentProjectRef{}, Default: refOf(g, g.Default)}
	if p != nil {
		out.Project = refOf(g, p.ID)
		out.IsDefault = g.Default == p.ID
		if r.Uses == models.ContentProject {
			out.Follows = refOf(g, r.Next)
		}
	}
	out.From = refOf(g, r.Owner)
	if len(r.Chain) > 1 {
		for _, id := range r.Chain[:len(r.Chain)-1] {
			if x := refOf(g, id); x != nil {
				out.Through = append(out.Through, *x)
			}
		}
	}
	out.ReadOnly = p != nil && r.Owner != p.ID
	if r.Owner != "" {
		for _, id := range g.UsedBy(r.Owner) {
			if id == pid {
				continue
			}
			out.UsedBy = append(out.UsedBy, *refOf(g, id))
		}
	}
	rec := &models.ContentSettings{ProjectID: r.Owner}
	if r.Owner != "" {
		if x, err := content.Record(ctx, a, r.Owner); err != nil {
			return nil, err
		} else if x != nil {
			rec = x
		}
	}
	out.Settings = *rec
	for _, part := range content.Parts() {
		po := ContentPartOut{Name: part.Name, Label: part.Label, Empty: true}
		if r.Owner != "" {
			po.Empty = part.Empty(ctx, a, rec)
			po.Summary = part.Summary(ctx, a, rec)
		} else {
			po.Summary = "Nothing set up"
		}
		out.Parts = append(out.Parts, po)
	}
	if p != nil {
		if own, _ := content.Record(ctx, a, p.ID); own != nil && out.ReadOnly && !content.RecordEmpty(ctx, a, own) {
			out.KeptAside = true
		}
		for _, id := range g.Projects() {
			if id == p.ID {
				continue
			}
			if g.Archived(id) && id != r.Next {
				continue
			}
			if wouldComeBack(g, id, p.ID) {
				continue // its settings come from this project: following it would go round in a circle
			}
			out.Choices = append(out.Choices, *refOf(g, id))
		}
	}
	out.Summary = contentSummary(out)
	return out, nil
}

// wouldComeBack reports whether target's settings come from project (so
// project following target would go round in a circle).
func wouldComeBack(g *content.Graph, target, project string) bool {
	r := g.Resolve(target)
	if r.Owner == project {
		return true
	}
	for _, id := range r.Chain {
		if id == project {
			return true
		}
	}
	return false
}

func contentSummary(o *ContentShowOut) string {
	used := ""
	if n := len(o.UsedBy); n > 0 {
		used = fmt.Sprintf(" Also used by %s.", pluralWord(n, "project"))
	}
	switch {
	case o.Project == nil && o.From == nil:
		return "No project's settings are used for new projects yet."
	case o.Project == nil:
		return fmt.Sprintf("New projects and footage in no project use %s's settings.%s", q(o.From.Name), used)
	case o.From == nil:
		return fmt.Sprintf("%s uses the default settings, and no project's settings are the default yet.", q(o.Project.Name))
	case !o.ReadOnly && o.IsDefault:
		return fmt.Sprintf("%s has its own settings, and new projects use them.%s", q(o.Project.Name), used)
	case !o.ReadOnly:
		return fmt.Sprintf("%s has its own settings.%s", q(o.Project.Name), used)
	case o.Uses == models.ContentDefault:
		return fmt.Sprintf("%s uses the default settings: %s's. Change them in %s, or give it its own.", q(o.Project.Name), q(o.From.Name), q(o.From.Name))
	default:
		return fmt.Sprintf("%s uses %s's settings. Change them there, or give it its own.", q(o.Project.Name), q(o.From.Name))
	}
}

// houseRules is the house rules a project uses (p nil: the default's).
func houseRules(ctx context.Context, a *app.App, projectID string) (*HouseRulesOut, error) {
	g, r, err := content.Resolve(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	out := &HouseRulesOut{Rules: []models.ProjectRule{}, ProjectID: r.Owner, Project: g.Name(r.Owner), ReadOnly: projectID != "" && r.Owner != projectID}
	if r.Owner != "" {
		rec, err := content.Record(ctx, a, r.Owner)
		if err != nil {
			return nil, err
		}
		if rec != nil && rec.HouseRules != nil {
			out.Rules = rec.HouseRules
		}
		out.UsedBy = len(g.UsedBy(r.Owner))
		if projectID != "" && projectID != r.Owner {
			out.UsedBy-- // the one asked about is among them
		}
	}
	switch n := len(out.Rules); {
	case r.Owner == "":
		out.Summary = "No project's settings are used here yet, so there are no house rules."
	case n == 0:
		out.Summary = fmt.Sprintf("%s's settings have no house rules yet.", q(out.Project))
	default:
		out.Summary = fmt.Sprintf("%s's settings have %s.", q(out.Project), pluralWord(n, "house rule"))
	}
	return out, nil
}

// AddHouseRule adds a rule to the house rules of a project's own settings
// (owner must have them: callers check whose settings a person chose).
func AddHouseRule(ctx context.Context, a *app.App, owner string, in HouseRuleAddIn) (*models.ProjectRule, bool, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, false, invalid("Say the rule in a few words.")
	}
	measure := strings.TrimSpace(in.Measure)
	if measure == "" {
		measure = MeasureFor(text)
	}
	if err := checkMeasure(measure, in.Promise, text); err != nil {
		return nil, false, err
	}
	rec, err := content.Record(ctx, a, owner)
	if err != nil {
		return nil, false, err
	}
	if rec != nil {
		for _, r := range rec.HouseRules {
			if ruleKey(r.Text) == ruleKey(text) {
				if in.Promise && !r.Promise {
					// The same words become a promise (D300).
					r.Promise = true
					if r.Measure == "" {
						r.Measure = measure
					}
					rules := append([]models.ProjectRule(nil), rec.HouseRules...)
					for k := range rules {
						if rules[k].ID == r.ID {
							rules[k] = r
						}
					}
					if err := content.Update(ctx, a, owner, bson.M{"$set": bson.M{"houseRules": rules}}); err != nil {
						return nil, false, err
					}
					a.Audit(ctx, "content.rules.add", "project", owner, owner, map[string]any{"rule": r.ID, "promise": true, "measure": r.Measure})
					return &r, true, nil
				}
				return &r, false, nil
			}
		}
		if len(rec.HouseRules) >= 100 {
			return nil, false, invalid("Settings keep at most 100 house rules; take one out first.")
		}
	}
	r := models.ProjectRule{ID: ids.New(ids.Rule), Text: text, Check: in.Check, Measure: measure, Promise: in.Promise, From: strings.TrimSpace(in.From), AddedBy: a.Actor, AddedAt: time.Now().UTC()}
	if err := content.Update(ctx, a, owner, bson.M{"$push": bson.M{"houseRules": r}}); err != nil {
		return nil, false, err
	}
	a.Audit(ctx, "content.rules.add", "project", owner, owner, map[string]any{"rule": r.ID, "check": r.Check, "measure": r.Measure, "promise": r.Promise})
	return &r, true, nil
}

// RulesFor are a project's own rules and the house rules of the settings it uses (D193).
func RulesFor(ctx context.Context, a *app.App, p *models.Project) []models.ProjectRule {
	return content.RulesFor(ctx, a, p)
}

func init() {
	Def("content.show", "Show a project's Content Settings — its brand, spelling list, narration voice and house rules: whether they are its own, another project's (followed live) or the default's, and which projects use them too", Flags{},
		func(ctx context.Context, a *app.App, in *ContentShowIn) (*ContentShowOut, error) {
			var p *models.Project
			if strings.TrimSpace(in.ProjectID) != "" {
				x, err := resolveProject(ctx, a, in.ProjectID)
				if err != nil {
					return nil, err
				}
				p = x
			}
			return contentShow(ctx, a, p)
		})
	Def("content.follow", "Make a project use another project's Content Settings (live: later changes there show up here; chains are followed, circles refused), or the default's", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *ContentFollowIn) (*ContentShowOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			target := ""
			switch {
			case in.UseDefault && strings.TrimSpace(in.Follow) != "":
				return nil, invalid("Use the default settings or another project's, not both.")
			case !in.UseDefault && strings.TrimSpace(in.Follow) == "":
				return nil, invalid("Say whose settings it should use: another project's, or the default.")
			case !in.UseDefault:
				t, err := resolveProject(ctx, a, in.Follow)
				if err != nil {
					return nil, err
				}
				target = t.ID
			}
			if g, err := content.Load(ctx, a); err == nil { // What may leave (D289)
				to := g.Resolve(target).Owner
				if target == "" {
					to = g.Resolve("").Owner
				}
				if err := leaveSwitch(ctx, a, "“"+p.Name+"”", g.Resolve(p.ID).Owner, to); err != nil {
					return nil, err
				}
			}
			ch, err := content.Follow(ctx, a, p.ID, target)
			if err != nil {
				return nil, err
			}
			out, err := contentShow(ctx, a, p)
			if err != nil {
				return nil, err
			}
			out.Changed = ch.Message
			return out, nil
		})
	Def("content.own", "Give a project its own Content Settings: empty (or the ones it had before), or with copy a copy of the settings it uses now", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *ContentOwnIn) (*ContentShowOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			if g, err := content.Load(ctx, a); err == nil && !in.Copy { // What may leave (D289): its own, kept aside or empty
				if err := leaveSwitch(ctx, a, "“"+p.Name+"”", g.Resolve(p.ID).Owner, p.ID); err != nil {
					return nil, err
				}
			}
			ch, err := content.Own(ctx, a, p.ID, in.Copy)
			if err != nil {
				return nil, err
			}
			out, err := contentShow(ctx, a, p)
			if err != nil {
				return nil, err
			}
			out.Changed = ch.Message
			return out, nil
		})
	Def("content.setDefault", "Use a project's Content Settings for new projects (its \"Use for new projects\"; there is one default, so this moves it): projects that use the default and footage in no project use them too", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *ContentDefaultIn) (*ContentShowOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			if g, err := content.Load(ctx, a); err == nil { // What may leave (D289): every project using the default
				if err := leaveSwitch(ctx, a, "the projects that use the default settings", g.Resolve("").Owner, g.Resolve(p.ID).Owner); err != nil {
					return nil, err
				}
			}
			ch, err := content.SetDefault(ctx, a, p.ID)
			if err != nil {
				return nil, err
			}
			out, err := contentShow(ctx, a, p)
			if err != nil {
				return nil, err
			}
			out.Changed = ch.Message
			return out, nil
		})
	Def("content.rules.list", "House rules: rules for every video using a project's Content Settings (a project's own rules stay in project.rules)", Flags{},
		func(ctx context.Context, a *app.App, in *HouseRulesIn) (*HouseRulesOut, error) {
			pid := ""
			if strings.TrimSpace(in.ProjectID) != "" {
				p, err := resolveProject(ctx, a, in.ProjectID)
				if err != nil {
					return nil, err
				}
				pid = p.ID
			}
			return houseRules(ctx, a, pid)
		})
	Def("content.rules.add", "Add a house rule (The name bar never animates) to a project's own Content Settings — every project using them follows it; without a project, the default's", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *HouseRuleAddIn) (*HouseRulesOut, error) {
			pid, err := projectRefID(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			t, g, err := content.EditTarget(ctx, a, pid)
			if err != nil {
				return nil, err
			}
			r, created, err := AddHouseRule(ctx, a, t.ProjectID, *in)
			if err != nil {
				return nil, err
			}
			out, err := houseRules(ctx, a, t.ProjectID)
			if err != nil {
				return nil, err
			}
			if created {
				out.Summary = fmt.Sprintf("Added the house rule %s to %s's settings.", q(r.Text), q(t.ProjectName))
			} else {
				out.Summary = fmt.Sprintf("%s's settings have the house rule %s already.", q(t.ProjectName), q(r.Text))
			}
			if note := t.Note(g); note != "" {
				out.Summary += " " + note
			}
			return out, nil
		})
	Def("content.rules.remove", "Take a house rule out of a project's own Content Settings (without a project, the default's)", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *HouseRuleRemoveIn) (*HouseRulesOut, error) {
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
			var rules []models.ProjectRule
			if rec != nil {
				rules = rec.HouseRules
			}
			i := -1
			ref := strings.TrimSpace(in.Rule)
			for k, r := range rules {
				if r.ID == ref || ruleKey(r.Text) == ruleKey(ref) || fmt.Sprint(k+1) == ref {
					i = k
					break
				}
			}
			if i < 0 {
				return nil, apperr.New(apperr.NotFound, "%s's settings have no house rule %q.", t.ProjectName, in.Rule).WithHint("Give its number from the list (1 = the first) or its words.")
			}
			gone := rules[i]
			if gone.Promise {
				if err := loosening(ctx, a, t.ProjectID, "take back a promise made to the client", fmt.Sprintf("metarush content rules remove %q --project %s", gone.Text, t.ProjectID), true); err != nil {
					return nil, err
				}
			}
			if err := content.Update(ctx, a, t.ProjectID, bson.M{"$pull": bson.M{"houseRules": bson.M{"id": gone.ID}}}); err != nil {
				return nil, err
			}
			a.Audit(ctx, "content.rules.remove", "project", t.ProjectID, t.ProjectID, map[string]any{"rule": gone.ID})
			out, err := houseRules(ctx, a, t.ProjectID)
			if err != nil {
				return nil, err
			}
			out.Summary = fmt.Sprintf("Took the house rule %s out of %s's settings.", q(gone.Text), q(t.ProjectName))
			if note := t.Note(g); note != "" {
				out.Summary += " " + note
			}
			return out, nil
		})
}

// SettingsOwner is the project whose own Content Settings a project uses
// ("" : the default's; "" when none are set up). Things kept for a project's
// settings (a spelling, a logo) go there.
func SettingsOwner(ctx context.Context, a *app.App, projectID string) string {
	_, r, err := content.Resolve(ctx, a, projectID)
	if err != nil || r.Owner == "" {
		return projectID
	}
	return r.Owner
}

// projectRefID resolves a project named by ID or name ("" stays "").
func projectRefID(ctx context.Context, a *app.App, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", nil
	}
	p, err := resolveProject(ctx, a, ref)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

// contentUsers are the projects using a project's own settings, it included,
// and whether footage in no project uses them too (they are the default's).
func contentUsers(ctx context.Context, a *app.App, owner string) ([]string, bool, error) {
	g, err := content.Load(ctx, a)
	if err != nil {
		return nil, false, err
	}
	return append([]string{owner}, g.UsedBy(owner)...), g.DefaultOwner() == owner, nil
}

var _ = store.CContentSettings

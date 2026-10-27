package ops

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ids"
	"github.com/metavert-io/metarush/internal/measures"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// A project's rules (D166, docs/keeping.md): plainly worded style rules and
// checks Claude reads at the start of every turn and the quality check
// measures where it can.

// What the quality check can measure for a rule (internal/measures, D166,
// D300).
const (
	MeasureCaptionsTop    = measures.CaptionsTop
	MeasureCaptionsBottom = measures.CaptionsBottom
	MeasureFirstNames     = measures.FirstNames
	MeasureNoFlashes      = measures.NoFlashes
	MeasureMaxLength      = measures.MaxLength // max-length:<seconds>[@<shape>]
	MeasureShape          = measures.Shape     // shape:<landscape|vertical|square>
)

// MeasureFor recognizes what the quality check can measure from a rule's
// words ("" when it can't).
func MeasureFor(text string) string { return measures.For(text) }

// ValidMeasure reports a measure the quality check knows.
func ValidMeasure(m string) bool { return measures.Valid(m) }

// MeasureWords says what the quality check measures, in plain words.
func MeasureWords(m string) string { return measures.Words(m) }

// checkMeasure refuses a measure the quality check doesn't know, and a
// promise it can't measure: that stays a sign-off (D300).
func checkMeasure(measure string, promise bool, text string) error {
	if !ValidMeasure(measure) {
		return apperr.New(apperr.InvalidInput, "MetaRush can't measure %q.", measure).
			WithHint("Leave it out (MetaRush recognizes what it can from the words), or use " + measures.Usage + ".")
	}
	if promise && measure == "" {
		return apperr.New(apperr.InvalidInput, "MetaRush can't measure “%s”, so it can't hold a version back for it.", text).
			WithHint("Keep it as a sign-off instead (a person ticks it when it's done), or say what to measure: " + measures.Usage + ".").
			WithCommand(fmt.Sprintf("metarush cut signoffs add <project> %q", text))
	}
	return nil
}

// hasMeasure reports a project rule the quality check measures that way.
func hasMeasure(p *models.Project, m string) bool {
	if p == nil {
		return false
	}
	for _, r := range p.Rules {
		if r.Measure == m {
			return true
		}
	}
	return false
}

// ruleKey is a rule's words compared: lower case, letters and digits only.
func ruleKey(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// RulesListIn names a project.
type RulesListIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=Project ID or its exact name."`
}

// RulesOut is a project's rules.
type RulesOut struct {
	ProjectID string               `json:"projectId"`
	Project   string               `json:"project"`
	Rules     []models.ProjectRule `json:"rules"`
	Summary   string               `json:"summary"`
}

// RuleAddIn adds a rule (or a check) to a project.
type RuleAddIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=Project ID or its exact name."`
	Text      string `json:"text" jsonschema:"minLength=1,maxLength=300,description=The rule in plain words (First names only on screen)."`
	// Check: a problem to look for in every quality check of the project.
	Check   bool   `json:"check,omitempty" jsonschema:"description=A problem to check for in every quality check of the project (one the person raised more than once)."`
	Measure string `json:"measure,omitempty" jsonschema:"maxLength=40,description=What the quality check measures: captions-top\\, captions-bottom\\, first-names\\, no-flashes\\, max-length:<seconds>[@<shape>]\\, shape:<landscape|vertical|square>\\, captions\\, logo-end:<seconds>\\, logo-start:<seconds>\\, speech-over-music:<steps>\\, disclosed\\, faces-in-frame\\, min-size:<pixels>\\, min-fps:<frames a second> (default: recognized from the words)."`
	From    string `json:"from,omitempty" jsonschema:"maxLength=200,description=Where it came from (a conversation)."`
	// Promise: a promise made to the client (D300): it must be measurable,
	// and a broken one holds the version back from going out (D298).
	Promise bool `json:"promise,omitempty" jsonschema:"description=A promise made to the client: measured in every quality check\\, and a broken one holds the version back from going out (it must be measurable; otherwise keep it as a sign-off)."`
}

// RuleAddOut is the rule and the project's rules now.
type RuleAddOut struct {
	RulesOut
	Rule    models.ProjectRule `json:"rule"`
	Created bool               `json:"created" jsonschema:"description=False when the project already had this rule."`
}

// RuleRemoveIn takes a rule out.
type RuleRemoveIn struct {
	ProjectID string `json:"projectId" jsonschema:"description=Project ID or its exact name."`
	Rule      string `json:"rule" jsonschema:"description=The rule: its number (1 = the first)\\, its ID or its words."`
}

func rulesOut(p *models.Project) RulesOut {
	out := RulesOut{ProjectID: p.ID, Project: p.Name, Rules: p.Rules}
	if out.Rules == nil {
		out.Rules = []models.ProjectRule{}
	}
	switch n := len(out.Rules); n {
	case 0:
		out.Summary = p.Name + " has no rules yet."
	case 1:
		out.Summary = p.Name + " has 1 rule."
	default:
		out.Summary = fmt.Sprintf("%s has %d rules.", p.Name, n)
	}
	return out
}

// AddRule adds a rule to a project unless it already has the same words.
func AddRule(ctx context.Context, a *app.App, p *models.Project, in RuleAddIn) (*RuleAddOut, error) {
	text := strings.TrimSpace(in.Text)
	if text == "" {
		return nil, invalid("Say the rule in a few words.")
	}
	measure := strings.TrimSpace(in.Measure)
	if measure == "" {
		measure = MeasureFor(text)
	}
	if err := checkMeasure(measure, in.Promise, text); err != nil {
		return nil, err
	}
	for _, r := range p.Rules {
		if ruleKey(r.Text) == ruleKey(text) {
			if in.Promise && !r.Promise {
				// The same words become a promise (D300).
				set := bson.M{"rules.$.promise": true, "updatedAt": time.Now().UTC()}
				if r.Measure == "" {
					set["rules.$.measure"] = measure
				}
				if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": p.ID, "workspaceId": a.WorkspaceID(), "rules.id": r.ID}, bson.M{"$set": set}); err != nil {
					return nil, err
				}
				a.Audit(ctx, "project.rules.add", "project", p.ID, p.ID, map[string]any{"rule": r.ID, "promise": true})
				np, err := resolveProject(ctx, a, p.ID)
				if err != nil {
					return nil, err
				}
				for _, x := range np.Rules {
					if x.ID == r.ID {
						r = x
					}
				}
				return &RuleAddOut{RulesOut: rulesOut(np), Rule: r, Created: true}, nil
			}
			out := &RuleAddOut{RulesOut: rulesOut(p), Rule: r}
			return out, nil
		}
	}
	if len(p.Rules) >= 100 {
		return nil, invalid("A project keeps at most 100 rules; take one out first.")
	}
	r := models.ProjectRule{ID: ids.New(ids.Rule), Text: text, Check: in.Check, Measure: measure, Promise: in.Promise, From: strings.TrimSpace(in.From), AddedBy: a.Actor, AddedAt: time.Now().UTC()}
	res, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": p.ID, "workspaceId": a.WorkspaceID(), "rules.text": bson.M{"$ne": text}},
		bson.M{"$push": bson.M{"rules": r}, "$set": bson.M{"updatedAt": time.Now().UTC()}})
	if err != nil {
		return nil, err
	}
	created := res.ModifiedCount > 0
	np, err := resolveProject(ctx, a, p.ID)
	if err != nil {
		return nil, err
	}
	if created {
		a.Audit(ctx, "project.rules.add", "project", p.ID, p.ID, map[string]any{"rule": r.ID, "check": r.Check, "measure": r.Measure})
	}
	return &RuleAddOut{RulesOut: rulesOut(np), Rule: r, Created: created}, nil
}

func ruleIndex(p *models.Project, ref string) int {
	ref = strings.TrimSpace(ref)
	if n, err := strconv.Atoi(ref); err == nil && n >= 1 && n <= len(p.Rules) {
		return n - 1
	}
	for i, r := range p.Rules {
		if r.ID == ref || ruleKey(r.Text) == ruleKey(ref) {
			return i
		}
	}
	return -1
}

func init() {
	Def("project.rules.list", "A project's rules: plainly worded style rules and checks Claude follows and the quality check measures where it can", Flags{},
		func(ctx context.Context, a *app.App, in *RulesListIn) (*RulesOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			out := rulesOut(p)
			return &out, nil
		})
	Def("project.rules.add", "Add a rule to a project (First names only on screen), or a check a problem should get in every quality check; the same words twice are one rule", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *RuleAddIn) (*RuleAddOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			return AddRule(ctx, a, p, *in)
		})
	Def("project.rules.remove", "Take a rule out of a project", Flags{Mutating: true},
		func(ctx context.Context, a *app.App, in *RuleRemoveIn) (*RulesOut, error) {
			p, err := resolveProject(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			i := ruleIndex(p, in.Rule)
			if i < 0 {
				return nil, apperr.New(apperr.NotFound, "%s has no rule %q.", p.Name, in.Rule).WithHint("Give its number from the list (1 = the first) or its words.")
			}
			id := p.Rules[i].ID
			if p.Rules[i].Promise {
				if err := loosening(ctx, a, p.ID, "take back a promise made to the client", fmt.Sprintf("metarush project rules remove %s %q", p.ID, p.Rules[i].Text), true); err != nil {
					return nil, err
				}
			}
			if _, err := a.DB.C(store.CProjects).UpdateOne(ctx, bson.M{"_id": p.ID, "workspaceId": a.WorkspaceID()},
				bson.M{"$pull": bson.M{"rules": bson.M{"id": id}}, "$set": bson.M{"updatedAt": time.Now().UTC()}}); err != nil {
				return nil, err
			}
			a.Audit(ctx, "project.rules.remove", "project", p.ID, p.ID, map[string]any{"rule": id})
			np, err := resolveProject(ctx, a, p.ID)
			if err != nil {
				return nil, err
			}
			out := rulesOut(np)
			return &out, nil
		})
}

package content

import (
	"context"
	"strings"
	"unicode"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/models"
)

// RulesFor are every rule a project's videos follow: its own rules (D166),
// then the house rules of the Content Settings it uses (D193), marked House
// with whose settings hold them. The same words twice are one rule (the
// project's own).
func RulesFor(ctx context.Context, a *app.App, p *models.Project) []models.ProjectRule {
	if p == nil {
		return nil
	}
	out := append([]models.ProjectRule(nil), p.Rules...)
	g, r, err := Resolve(ctx, a, p.ID)
	if err != nil || r.Owner == "" {
		return out
	}
	rec, err := Record(ctx, a, r.Owner)
	if err != nil || rec == nil {
		return out
	}
	have := map[string]bool{}
	for _, x := range out {
		have[ruleKey(x.Text)] = true
	}
	for _, h := range rec.HouseRules {
		if have[ruleKey(h.Text)] {
			continue
		}
		h.House, h.HouseOf = true, g.Name(r.Owner)
		out = append(out, h)
	}
	return out
}

// HasMeasure reports a rule the quality check measures that way.
func HasMeasure(rules []models.ProjectRule, m string) bool {
	for _, r := range rules {
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

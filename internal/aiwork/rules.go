package aiwork

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/aiservices"
	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/leave"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/providers"
)

// Which AI services a client's work may go to is a Content Settings part
// (D225): it follows like the brand and the spelling list, and every kind of
// AI work — generated video too — asks it before anything is priced.

// PartAIServices is the part's name.
const PartAIServices = "aiServices"

func init() {
	content.Register(content.Part{Name: PartAIServices, Label: "AI services",
		Empty: func(_ context.Context, _ *app.App, r *models.ContentSettings) bool { return r.AIServices.Empty() },
		Summary: func(_ context.Context, _ *app.App, r *models.ContentSettings) string {
			return rulesWords(r.AIServices)
		},
		Copy: func(ctx context.Context, a *app.App, from, to *models.ContentSettings) error {
			if from.AIServices.Empty() {
				return nil
			}
			r := *from.AIServices
			r.UpdatedAt, r.UpdatedBy = time.Now().UTC(), &a.Actor
			to.AIServices = &r
			return content.Save(ctx, a, to.ProjectID, bson.M{"aiServices": r}, nil)
		},
		Check: func(r *models.ContentSettings) string {
			if r.AIServices == nil {
				return ""
			}
			for kind, ref := range r.AIServices.Pin {
				if _, err := aiservices.FindModel(kind, ref); err != nil {
					return fmt.Sprintf("the model pinned for %s isn't one MetaRush knows", kindTitle(kind))
				}
			}
			return ""
		}})
	// What may leave (D288) names AI services by provider.
	leave.ProviderTitle = func(name string) string {
		if p, err := aiservices.ProviderOf(name); err == nil {
			return p.Title
		}
		return ""
	}
	leave.Receivers[models.PlaceAI] = func(context.Context, *app.App) []leave.LeaveReceiver {
		out := []leave.LeaveReceiver{}
		for _, p := range aiservices.Providers() {
			out = append(out, leave.LeaveReceiver{Name: p.Name, Title: p.Title})
		}
		return out
	}
	content.AIAllowed = func(ctx context.Context, a *app.App, projectID, provider string) error {
		rules, owner, err := rulesFor(ctx, a, projectID)
		if err != nil || allows(rules, provider) {
			return err
		}
		return forbidden(ctx, a, rules, owner, provider)
	}
}

// rulesWords says the rules in a few plain words.
func rulesWords(r *models.AIServiceRules) string {
	if r.Empty() {
		return "Any AI service MetaRush offers"
	}
	if r.None {
		return "No AI service outside this computer"
	}
	var bits []string
	if len(r.ForbidKinds) > 0 {
		var kinds []string
		for _, k := range r.ForbidKinds {
			kinds = append(kinds, kindTitle(k))
		}
		bits = append(bits, "no "+joinOr(kinds))
	}
	if len(r.Forbid) > 0 {
		var names []string
		for _, f := range r.Forbid {
			names = append(names, providerTitle(f))
		}
		bits = append(bits, "never "+joinOr(names))
	}
	if n := len(r.Pin); n > 0 {
		var kinds []string
		for k := range r.Pin {
			kinds = append(kinds, kindTitle(k))
		}
		sort.Strings(kinds)
		bits = append(bits, "a chosen model for "+joinAnd(kinds))
	}
	return upperFirst(strings.Join(bits, "; "))
}

// rulesFor is the AI services part of the Content Settings a project uses,
// and whose they are.
func rulesFor(ctx context.Context, a *app.App, projectID string) (*models.AIServiceRules, string, error) {
	rec, owner, err := content.For(ctx, a, projectID)
	if err != nil {
		return nil, "", err
	}
	return rec.AIServices, owner, nil
}

func forbidden(ctx context.Context, a *app.App, rules *models.AIServiceRules, owner, provider string) error {
	whose := "this project's Content Settings"
	if owner != "" {
		if g, err := content.Load(ctx, a); err == nil {
			whose = "the Content Settings of “" + g.Name(owner) + "”"
		}
	}
	msg := endSentence(fmt.Sprintf("%s don't allow %s", upperFirst(whose), providerTitle(provider)))
	switch via := ruledOutVia(rules, provider); via {
	case "":
	case "*":
		msg = fmt.Sprintf("%s rule some AI services out, and %s may reach any service on the internet.", upperFirst(whose), providerTitle(provider))
	default:
		msg = endSentence(fmt.Sprintf("%s don't allow %s, which %s reaches", upperFirst(whose), providerTitle(via), providerTitle(provider)))
	}
	if rules.None {
		msg = fmt.Sprintf("%s say nothing goes to an AI service.", upperFirst(whose))
	}
	e := apperr.New(apperr.Conflict, "%s", msg).WithDetails(map[string]any{"reason": "content_settings", "provider": provider, "settingsOf": owner})
	if rules.Note != "" {
		e = e.WithHint("Their note: “" + rules.Note + "”.")
	}
	return e.WithCommand("metarush ai rules --project <project>")
}

// kindForbidden refuses a kind of work a project's Content Settings forbid
// (presenters for a client, D277).
func kindForbidden(ctx context.Context, a *app.App, rules *models.AIServiceRules, owner, kind string) error {
	whose := "this project's Content Settings"
	if owner != "" {
		if g, err := content.Load(ctx, a); err == nil {
			whose = "the Content Settings of “" + g.Name(owner) + "”"
		}
	}
	e := apperr.New(apperr.Conflict, "%s don't allow %s.", upperFirst(whose), kindTitle(kind)).WithDetails(map[string]any{"reason": "content_settings", "kind": kind, "settingsOf": owner})
	if rules.Note != "" {
		e = e.WithHint("Their note: “" + rules.Note + "”.")
	}
	return e.WithCommand("metarush ai rules show --project <project>")
}

// kindsAllow checks that every project a request touches allows its kind of work.
func kindsAllow(ctx context.Context, a *app.App, kind string, projects []string) error {
	seen := map[string]bool{}
	for _, pid := range projects {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		rules, owner, err := rulesFor(ctx, a, pid)
		if err != nil {
			return err
		}
		if !rules.AllowsKind(kind) {
			return kindForbidden(ctx, a, rules, owner, kind)
		}
	}
	return nil
}

// endSentence ends a sentence with one period, even after a name that has
// its own (“sync.”).
func endSentence(s string) string { return strings.TrimSuffix(s, ".") + "." }

// choice is the model a request uses and why.
type choice struct {
	model *aiservices.Model
	by    string // default | asked | content-settings
	rules *models.AIServiceRules
	owner string
}

// choose picks the model: the one asked for, else the one the project's
// Content Settings pin, else MetaRush's usual one — or, when Content
// Settings forbid its provider, the best ranked model they allow.
func choose(ctx context.Context, a *app.App, projectID, kind, asked string, now time.Time) (*choice, error) {
	aiservices.Refresh(ctx, a) // abilities that provide a kind of work (D281)
	rules, owner, err := rulesFor(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	c := &choice{rules: rules, owner: owner}
	if !rules.AllowsKind(kind) {
		return nil, kindForbidden(ctx, a, rules, owner, kind)
	}
	switch {
	case strings.TrimSpace(asked) != "":
		m, err := aiservices.FindModel(kind, asked)
		if err != nil {
			return nil, err
		}
		if !allows(rules, m.Provider) {
			return nil, forbidden(ctx, a, rules, owner, m.Provider)
		}
		c.model, c.by = m, "asked"
	case rules != nil && rules.Pin[kind] != "":
		m, err := aiservices.FindModel(kind, rules.Pin[kind])
		if err != nil {
			return nil, err
		}
		if !allows(rules, m.Provider) {
			return nil, forbidden(ctx, a, rules, owner, m.Provider)
		}
		c.model, c.by = m, "content-settings"
	default:
		m, err := aiservices.FindModel(kind, "")
		if err != nil {
			return nil, err
		}
		c.model, c.by = m, "default"
		if !allows(rules, m.Provider) || m.Retired(now) {
			var best *aiservices.Model
			for _, x := range aiservices.ModelsOf(kind) {
				x := x
				if !allows(rules, x.Provider) || x.Retired(now) || x.GatewayOnly() || !built(x.Provider, kind) || readyHere(a, x.Provider, kind, x.ID) != nil {
					continue
				}
				if best == nil || x.Rank > best.Rank {
					best = &x
				}
			}
			if best == nil {
				if rules != nil && rules.None {
					return nil, forbidden(ctx, a, rules, owner, "")
				}
				return nil, forbidden(ctx, a, rules, owner, m.Provider)
			}
			c.model = best
		}
	}
	return c, nil
}

// built reports whether MetaRush has an adapter for a provider's kind of work.
func built(provider, kind string) bool {
	_, err := aiservices.Get(provider, kind)
	return err == nil
}

// keyReady reports whether this computer has a provider's key (keys live
// only in internal/providers).
func keyReady(a *app.App, provider string) bool {
	p, err := aiservices.ProviderOf(provider)
	if err != nil {
		return false
	}
	if p.Local() || p.Route == aiservices.RouteAbility {
		return true // no key of MetaRush's own: whether it runs here is readyHere's (D278, D281)
	}
	k, _, err := providers.Key(a.Cfg, firstNonEmpty(p.Key, p.Name))
	return err == nil && k != ""
}

// keyFor is a provider's key on this computer.
func keyFor(a *app.App, provider string) (string, error) {
	p, err := aiservices.ProviderOf(provider)
	if err != nil {
		return "", err
	}
	if p.Local() || p.Route == aiservices.RouteAbility {
		return "", nil // models on this computer need no key; an ability's keys go in through its proxy (D278, D281)
	}
	return providers.MustKey(a.Cfg, firstNonEmpty(p.Key, p.Name))
}

func rulesOut(ctx context.Context, a *app.App, projectID string) (*RulesOut, error) {
	aiservices.Refresh(ctx, a) // abilities that provide a kind of work (D281)
	rules, owner, err := rulesFor(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	out := &RulesOut{Owner: owner}
	if rules != nil {
		out.Rules = *rules
	}
	if owner != "" {
		if g, err := content.Load(ctx, a); err == nil {
			out.OwnerName = g.Name(owner)
		}
	}
	out.Summary = rulesWords(rules) + "."
	if out.OwnerName != "" {
		out.Summary += " From “" + out.OwnerName + "”'s Content Settings."
	}
	return out, nil
}

func rulesSet(ctx context.Context, a *app.App, in *RulesSetIn) (*RulesOut, error) {
	aiservices.Refresh(ctx, a) // so an ability that provides can be pinned or ruled out (D281)
	t, g, err := content.EditTarget(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	cur, _, err := rulesFor(ctx, a, t.ProjectID)
	if err != nil {
		return nil, err
	}
	r := models.AIServiceRules{}
	if cur != nil && !in.Clear {
		r = *cur
		r.Forbid = slices.Clone(cur.Forbid)
		r.Pin = map[string]string{}
		for k, v := range cur.Pin {
			r.Pin[k] = v
		}
	}
	known := func(p string) (string, error) {
		p = strings.ToLower(strings.TrimSpace(p))
		if _, err := aiservices.ProviderOf(p); err != nil {
			if _, err2 := providers.Get(p); err2 != nil {
				return "", apperr.New(apperr.InvalidInput, "MetaRush knows no AI service called “%s”.", p)
			}
		}
		return p, nil
	}
	if in.None != nil {
		r.None = *in.None
	}
	for _, p := range in.Forbid {
		n, err := known(p)
		if err != nil {
			return nil, err
		}
		if !slices.Contains(r.Forbid, n) {
			r.Forbid = append(r.Forbid, n)
		}
	}
	for _, p := range in.Allow {
		n := strings.ToLower(strings.TrimSpace(p))
		r.Forbid = slices.DeleteFunc(r.Forbid, func(x string) bool { return x == n })
	}
	if cur != nil && !in.Clear {
		r.ForbidKinds = slices.Clone(cur.ForbidKinds)
	}
	for _, k := range in.ForbidKinds {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == aiservices.KindPresenterMake || k == "presenters" {
			k = aiservices.KindPresenter
		}
		if _, err := aiservices.KindOf(k); err != nil || k == aiservices.KindVideo {
			return nil, apperr.New(apperr.InvalidInput, "MetaRush has no kind of AI work called “%s” to forbid.", k)
		}
		if !slices.Contains(r.ForbidKinds, k) {
			r.ForbidKinds = append(r.ForbidKinds, k)
		}
	}
	for _, k := range in.AllowKinds {
		k = strings.ToLower(strings.TrimSpace(k))
		if k == aiservices.KindPresenterMake || k == "presenters" {
			k = aiservices.KindPresenter
		}
		r.ForbidKinds = slices.DeleteFunc(r.ForbidKinds, func(x string) bool { return x == k })
	}
	sort.Strings(r.ForbidKinds)
	for kind, ref := range in.Pin {
		if _, err := aiservices.KindOf(kind); err != nil {
			return nil, err
		}
		m, err := aiservices.FindModel(kind, ref)
		if err != nil {
			return nil, err
		}
		if r.Pin == nil {
			r.Pin = map[string]string{}
		}
		r.Pin[kind] = m.Ref()
	}
	for _, kind := range in.Unpin {
		delete(r.Pin, kind)
	}
	for kind, ref := range r.Pin {
		p, _ := aiservices.SplitRef(ref)
		if !allows(&r, p) {
			switch via := ruledOutVia(&r, p); via {
			case "", p:
				return nil, apperr.New(apperr.InvalidInput, "The model chosen for %s is %s's, which these settings forbid.", kindTitle(kind), providerTitle(p))
			case "*":
				return nil, apperr.New(apperr.InvalidInput, "The model chosen for %s is %s's, which may reach any service on the internet, and these settings rule some out.", kindTitle(kind), providerTitle(p))
			default:
				return nil, apperr.New(apperr.InvalidInput, "%s", endSentence(fmt.Sprintf("The model chosen for %s is %s's, which reaches %s, and these settings forbid %s", kindTitle(kind), providerTitle(p), providerTitle(via), providerTitle(via))))
			}
		}
	}
	if in.Note != nil {
		r.Note = strings.TrimSpace(*in.Note)
	}
	sort.Strings(r.Forbid)
	var setErr error
	if r.Empty() && r.Note == "" {
		setErr = content.Save(ctx, a, t.ProjectID, nil, bson.M{"aiServices": ""})
	} else {
		r.UpdatedAt, r.UpdatedBy = time.Now().UTC(), &a.Actor
		setErr = content.Save(ctx, a, t.ProjectID, bson.M{"aiServices": r}, nil)
	}
	if setErr != nil {
		return nil, setErr
	}
	a.Audit(ctx, "ai.rules.set", "project", t.ProjectID, t.ProjectID, map[string]any{"none": r.None, "forbid": r.Forbid, "forbidKinds": r.ForbidKinds, "pin": r.Pin, "note": r.Note})
	out, err := rulesOut(ctx, a, t.ProjectID)
	if err != nil {
		return nil, err
	}
	out.Summary = "Saved: " + strings.ToLower(rulesWords(&r)[:1]) + rulesWords(&r)[1:] + "."
	if note := t.Note(g); note != "" {
		out.Summary += " " + note
	}
	return out, nil
}

func providerTitle(name string) string {
	if p, err := aiservices.ProviderOf(name); err == nil {
		return p.Title
	}
	if p, err := providers.Get(name); err == nil {
		return p.Title()
	}
	return name
}

func kindTitle(kind string) string {
	if k, err := aiservices.KindOf(kind); err == nil {
		return strings.ToLower(k.Title)
	}
	return kind
}

func joinAnd(xs []string) string { return join(xs, "and") }
func joinOr(xs []string) string  { return join(xs, "or") }

func join(xs []string, word string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " " + word + " " + xs[len(xs)-1]
}

func upperFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	return strings.ToUpper(string(r[0])) + string(r[1:])
}

func firstNonEmpty(vs ...string) string {
	for _, v := range vs {
		if s := strings.TrimSpace(v); s != "" {
			return s
		}
	}
	return ""
}

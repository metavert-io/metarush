// Package leave is "What may leave" (D288–D291): a Content Settings part
// that names, per client (every project using a project's Content Settings),
// which of their things may leave this computer — footage, voices, faces,
// words, pictures — and to whom: AI services (by provider), abilities (by
// host), Claude (by the terms of the account that answers), cloud storage and
// delivery. Nothing set keeps today's behaviour.
//
// Every place a client's things leave asks Check (or Problem, for a plan that
// lists what stops it) with what it sends, where, and whose things they are.
// A refusal says which rule, whose Content Settings hold it and who set it,
// and is recorded (audit "leave.refused", the activity log) so the receipt can
// list it next to everything that did leave (D291).
package leave

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"sync/atomic"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/store"
)

// PartName is the part's name in Content Settings.
const PartName = "leave"

// Send is one thing about to leave this computer.
type Send struct {
	// Place is where it goes (models.Place*).
	Place string
	// To is who receives it: a provider (gemini), a host (api.example.com),
	// the terms of a Claude account (commercial); "" when it doesn't matter.
	To string
	// ToWords is the receiver as people see it ("ElevenLabs").
	ToWords string
	// Kinds are what it sends (models.Leave*).
	Kinds []string
	// Projects are whose things they are ("" is footage or a picture in no
	// project: the default's Content Settings apply).
	Projects []string
	// What is what it sends in plain words, for the record ("the voice in
	// “Interview”").
	What string
	// AnyReceiver: who receives it is decided later (an ability's proxy asks
	// host by host), so only what it sends is checked now.
	AnyReceiver bool
}

// Refusal is a rule that keeps something on this computer.
type Refusal struct {
	Place string `json:"place"`
	// Kind is the kind kept here ("" when only who may receive it refused it).
	Kind string `json:"kind,omitempty"`
	To   string `json:"to,omitempty"`
	// Project is the project whose things would have left; Settings the
	// project whose Content Settings hold the rule.
	Project      string `json:"project,omitempty"`
	ProjectName  string `json:"projectName,omitempty"`
	Settings     string `json:"settingsOf,omitempty"`
	SettingsName string `json:"settingsName,omitempty"`
	// SetBy and SetAt say who set the rule and when.
	SetBy   string    `json:"setBy,omitempty"`
	SetAt   time.Time `json:"setAt,omitempty"`
	Note    string    `json:"note,omitempty"`
	Message string    `json:"message"`
	Hint    string    `json:"hint"`
}

// Err is the refusal as MetaRush's error (E_KEPT_HERE).
func (r *Refusal) Err() *apperr.Error {
	return apperr.New(apperr.KeptHere, "%s", r.Message).WithHint(r.Hint).
		WithDetails(map[string]any{"reason": "may_not_leave", "place": r.Place, "kind": r.Kind, "to": r.To, "projectId": r.Project,
			"settingsOf": r.Settings, "settingsName": r.SettingsName, "setBy": r.SetBy, "setAt": r.SetAt}).
		WithCommand(command(r.Settings))
}

func command(project string) string {
	if project == "" {
		return "metarush content leave"
	}
	return "metarush content leave --project " + project
}

// For is the rules of the Content Settings a project uses ("" : the
// default's), and whose they are.
func For(ctx context.Context, a *app.App, projectID string) (*models.LeaveRules, string, error) {
	rec, owner, err := content.For(ctx, a, projectID)
	if err != nil {
		return nil, "", err
	}
	return rec.Leave, owner, nil
}

// Problem says whether a send is allowed: nil, or the first rule that keeps
// it here. Nothing is recorded (plans list it among what stops them).
func Problem(ctx context.Context, a *app.App, s Send) (*Refusal, error) {
	if !anyRules(ctx, a) {
		return nil, nil // nobody set any: today's behaviour, one quick look
	}
	c, err := newChecker(ctx, a)
	if err != nil {
		return nil, err
	}
	return c.problem(s), nil
}

// checker answers many questions with one look: every project's choice and
// every Content Settings record that holds rules, read once.
type checker struct {
	g     *content.Graph
	rules map[string]*models.LeaveRules // owner project → its rules
}

// checkerLoads counts the looks checkers take (tests: one per check).
var checkerLoads atomic.Int64

func newChecker(ctx context.Context, a *app.App) (*checker, error) {
	checkerLoads.Add(1)
	g, err := content.Load(ctx, a)
	if err != nil {
		return nil, err
	}
	var recs []models.ContentSettings
	if err := a.DB.Find(ctx, store.CContentSettings, a.WorkspaceID(), bson.M{"leave": bson.M{"$exists": true}}, &recs,
		options.Find().SetProjection(bson.M{"projectId": 1, "leave": 1}).SetLimit(20000)); err != nil {
		return nil, err
	}
	c := &checker{g: g, rules: map[string]*models.LeaveRules{}}
	for i := range recs {
		if !recs[i].Leave.Empty() {
			c.rules[recs[i].ProjectID] = recs[i].Leave
		}
	}
	return c, nil
}

// problem is the first rule keeping a send here (nil: none).
func (c *checker) problem(s Send) *Refusal {
	projects := s.Projects
	if len(projects) == 0 {
		projects = []string{""}
	}
	seen := map[string]bool{}
	for _, pid := range projects {
		if seen[pid] {
			continue
		}
		seen[pid] = true
		owner := c.g.Resolve(pid).Owner
		rules := c.rules[owner]
		if owner == "" || rules == nil {
			continue
		}
		if r := refuses(rules, s); r != nil {
			r.Project, r.ProjectName, r.Settings, r.SettingsName = pid, c.g.Name(pid), owner, c.g.Name(owner)
			fill(r, rules, s)
			return r
		}
	}
	return nil
}

// anyRules reports whether any Content Settings in the workspace hold What
// may leave rules (unknown: yes, so a failed look never lets things out).
func anyRules(ctx context.Context, a *app.App) bool {
	n, err := a.DB.C(store.CContentSettings).CountDocuments(ctx, bson.M{"workspaceId": a.WorkspaceID(), "leave": bson.M{"$exists": true}}, options.Count().SetLimit(1))
	return err != nil || n > 0
}

// Check refuses a send the rules keep here (E_KEPT_HERE), and records the
// refusal (unless the context is Quiet: an estimate or a plan).
func Check(ctx context.Context, a *app.App, s Send) error {
	r, err := Problem(ctx, a, s)
	if err != nil || r == nil {
		return err
	}
	if !quiet(ctx) {
		Record(ctx, a, r, s)
	}
	return r.Err()
}

// Record keeps a refusal: the change history (audit "leave.refused") and so
// the activity log; the receipt lists them (D291).
func Record(ctx context.Context, a *app.App, r *Refusal, s Send) {
	ctx = context.WithoutCancel(ctx)
	a.Audit(ctx, AuditRefused, "project", r.Project, r.Project, map[string]any{"place": r.Place, "kind": r.Kind, "to": firstNonEmpty(s.ToWords, s.To),
		"what": s.What, "settingsOf": r.Settings, "settingsName": r.SettingsName, "setBy": r.SetBy, "message": r.Message})
}

// AuditRefused is the change history's action for a refusal.
const AuditRefused = "leave.refused"

// refuses is the first rule that keeps a send here (Project and the words
// are filled in by the caller).
func refuses(rules *models.LeaveRules, s Send) *Refusal {
	if rules == nil {
		return nil
	}
	kinds := s.Kinds
	if len(kinds) == 0 {
		kinds = []string{""}
	}
	if rules.Nothing {
		return &Refusal{Place: s.Place, Kind: kinds[0], To: s.To}
	}
	p := rules.Place(s.Place)
	for _, k := range models.LeaveKinds { // in the order people read them
		if slices.Contains(kinds, k) && slices.Contains(p.Keep, k) {
			return &Refusal{Place: s.Place, Kind: k, To: s.To}
		}
	}
	if len(p.Only) > 0 && !s.AnyReceiver && !Receives(p.Only, s.Place, s.To) {
		return &Refusal{Place: s.Place, To: s.To}
	}
	return nil
}

// Receives reports whether a receiver is on a place's Only list: a provider
// or terms by name, a host by name or *.example.com.
func Receives(only []string, place, to string) bool {
	to = strings.ToLower(strings.TrimSpace(to))
	if to == "" {
		return false
	}
	for _, o := range only {
		o = strings.ToLower(strings.TrimSpace(o))
		switch {
		case o == to:
			return true
		case place == models.PlaceAbilities && strings.HasPrefix(o, "*."):
			if strings.HasSuffix(to, o[1:]) && len(to) > len(o)-1 {
				return true
			}
		}
	}
	return false
}

// fill words a refusal.
func fill(r *Refusal, rules *models.LeaveRules, s Send) {
	if rules.UpdatedBy != nil {
		r.SetBy = firstNonEmpty(rules.UpdatedBy.UserName, "a person")
	}
	r.SetAt, r.Note = rules.UpdatedAt, rules.Note
	whose := "“" + r.SettingsName + "”'s Content Settings"
	if r.Project != "" && r.Project != r.Settings {
		whose = "The Content Settings “" + r.ProjectName + "” uses (“" + r.SettingsName + "”'s)"
	} else if r.Project == "" {
		whose = "The Content Settings footage in no project uses (“" + r.SettingsName + "”'s)"
	}
	to := firstNonEmpty(s.ToWords, s.To)
	var msg string
	switch {
	case rules.Nothing && r.Kind == "":
		msg = fmt.Sprintf("%s keep everything on this computer, so nothing goes to %s.", whose, PlaceWords(s.Place, to))
	case rules.Nothing:
		msg = fmt.Sprintf("%s keep everything on this computer, so its %s %s go to %s.", whose, KindWords(r.Kind), dont(r.Kind), PlaceWords(s.Place, to))
	case r.Kind != "":
		msg = fmt.Sprintf("%s keep %s on this computer: %s %s go to %s.", whose, KindWords(r.Kind), pronoun(r.Kind), dont(r.Kind), PlaceWords(s.Place, ""))
	default:
		msg = fmt.Sprintf("%s allow only %s among %s", whose, joinOr(onlyWords(s.Place, rules.Place(s.Place).Only)), PlaceWords(s.Place, ""))
		if to != "" {
			msg += ", so nothing goes to " + to
		}
		msg += "."
	}
	if r.SetBy != "" {
		msg += " " + upperFirst(setWords(r.SetBy, r.SetAt)) + "."
	}
	r.Message = msg
	hint := fmt.Sprintf("To change it: “%s” → Settings → What may leave (letting more leave takes a person who is an owner).", r.SettingsName)
	if r.Note != "" {
		hint = "Their note: “" + r.Note + "”. " + hint
	}
	r.Hint = hint
}

// dont and pronoun agree with a kind: footage is one thing, the rest many.
func dont(kind string) string {
	if kind == models.LeaveFootage {
		return "doesn't"
	}
	return "don't"
}

func pronoun(kind string) string {
	if kind == models.LeaveFootage {
		return "it"
	}
	return "they"
}

func setWords(by string, at time.Time) string {
	s := "set by " + by
	if !at.IsZero() {
		s += " on " + at.Format("2 Jan 2006")
	}
	return s
}

// KindWords names a kind of thing for people.
func KindWords(kind string) string {
	switch kind {
	case models.LeaveFootage:
		return "footage"
	case models.LeaveVoices:
		return "voices"
	case models.LeaveFaces:
		return "faces"
	case models.LeaveWords:
		return "words"
	case models.LeavePictures:
		return "pictures"
	}
	return kind
}

// PlaceWords names a place for people (with its receiver when known).
func PlaceWords(place, to string) string {
	var s string
	switch place {
	case models.PlaceAI:
		s = "AI services"
		if to != "" {
			return to
		}
	case models.PlaceAbilities:
		s = "abilities"
		if to != "" {
			return to
		}
	case models.PlaceClaude:
		s = "Claude"
	case models.PlaceStorage:
		s = "cloud storage"
	case models.PlaceDelivery:
		s = "delivery"
	case models.PlaceReviewers:
		s = "people outside the workspace"
	default:
		s = place
	}
	return s
}

// onlyWords names the receivers a place's Only list allows.
func onlyWords(place string, only []string) []string {
	out := make([]string, 0, len(only))
	for _, o := range only {
		out = append(out, ReceiverWords(place, o))
	}
	return out
}

// ReceiverWords names a receiver for people: a provider's title, a host as
// it is, Claude's terms in words. Providers' titles come from TitleOf.
func ReceiverWords(place, to string) string {
	switch place {
	case models.PlaceAI:
		if ProviderTitle != nil {
			if t := ProviderTitle(to); t != "" {
				return t
			}
		}
	case models.PlaceClaude:
		switch to {
		case "commercial":
			return "an account under commercial terms"
		case "cloud":
			return "Claude through a cloud provider"
		case "consumer":
			return "a personal Claude account"
		}
	}
	return to
}

// ProviderTitle names an AI service by its provider name (set by aiwork).
var ProviderTitle func(name string) string

// Summary says rules in a few plain words.
func Summary(r *models.LeaveRules) string {
	if r.Empty() {
		return "Anything MetaRush offers may leave"
	}
	if r.Nothing {
		return "Nothing leaves this computer"
	}
	var bits []string
	for _, place := range models.LeavePlaces {
		p := r.Place(place)
		if p.Empty() {
			continue
		}
		var s string
		if len(p.Keep) > 0 {
			kinds := []string{}
			for _, k := range models.LeaveKinds {
				if slices.Contains(p.Keep, k) {
					kinds = append(kinds, KindWords(k))
				}
			}
			if len(kinds) == len(models.LeaveKinds) {
				s = "nothing to " + PlaceWords(place, "")
			} else {
				s = "no " + joinOr(kinds) + " to " + PlaceWords(place, "")
			}
		}
		if len(p.Only) > 0 {
			o := PlaceWords(place, "") + " only " + joinOr(onlyWords(place, p.Only))
			if s != "" {
				s += "; " + o
			} else {
				s = o
			}
		}
		bits = append(bits, s)
	}
	return upperFirst(strings.Join(bits, "; "))
}

// Normalize sorts and cleans rules: kinds and places it knows, lower case,
// no repeats, empty places dropped.
func Normalize(r *models.LeaveRules) {
	if r == nil {
		return
	}
	places := map[string]models.LeavePlace{}
	for name, p := range r.Places {
		var keep, only []string
		for _, k := range models.LeaveKinds {
			if slices.Contains(p.Keep, k) {
				keep = append(keep, k)
			}
		}
		for _, o := range p.Only {
			o = strings.ToLower(strings.TrimSpace(o))
			if o != "" && !slices.Contains(only, o) {
				only = append(only, o)
			}
		}
		sort.Strings(only)
		if len(keep) > 0 || len(only) > 0 {
			places[name] = models.LeavePlace{Keep: keep, Only: only}
		}
	}
	if len(places) == 0 {
		places = nil
	}
	r.Places = places
	r.Note = strings.TrimSpace(r.Note)
}

// Looser lists what the new rules let leave that the old ones kept here, in
// plain words (empty: nothing more may leave). Letting more leave takes an
// owner (D289).
func Looser(old, nu *models.LeaveRules) []string {
	var out []string
	if old.Empty() {
		return nil
	}
	if old.Nothing && (nu == nil || !nu.Nothing) {
		return []string{"things that stayed on this computer may leave"}
	}
	if nu != nil && nu.Nothing {
		return nil
	}
	for _, place := range models.LeavePlaces {
		op, np := old.Place(place), nu.Place(place)
		for _, k := range op.Keep {
			if !slices.Contains(np.Keep, k) {
				out = append(out, KindWords(k)+" may go to "+PlaceWords(place, ""))
			}
		}
		if len(op.Only) > 0 {
			if len(np.Only) == 0 {
				out = append(out, "anyone "+PlaceWords(place, "")+" offers may receive them")
				continue
			}
			for _, o := range np.Only {
				if o != noneReceives && !slices.Contains(op.Only, o) {
					out = append(out, ReceiverWords(place, o)+" may receive them")
				}
			}
		}
	}
	return out
}

func joinOr(xs []string) string { return join(xs, "or") }

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

// noneReceives stands for an empty list of who may receive (two lists that
// share nobody): it matches no receiver.
const noneReceives = "\x00nobody"

// Merge is the rules that hold when several apply (a clip in several
// projects): every kind any of them keeps, and only receivers all allow.
func Merge(rs ...*models.LeaveRules) *models.LeaveRules {
	out := &models.LeaveRules{Places: map[string]models.LeavePlace{}}
	onlySet := map[string]bool{}
	for _, r := range rs {
		if r.Empty() {
			continue
		}
		if r.Nothing {
			out.Nothing = true
		}
		for place, p := range r.Places {
			m := out.Places[place]
			for _, k := range p.Keep {
				if !slices.Contains(m.Keep, k) {
					m.Keep = append(m.Keep, k)
				}
			}
			if len(p.Only) > 0 {
				if !onlySet[place] {
					m.Only, onlySet[place] = slices.Clone(p.Only), true
				} else {
					m.Only = slices.DeleteFunc(m.Only, func(x string) bool { return !slices.Contains(p.Only, x) })
					if len(m.Only) == 0 {
						m.Only = []string{noneReceives}
					}
				}
			}
			out.Places[place] = m
		}
	}
	return out
}

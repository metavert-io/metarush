package leave

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/config"
	"github.com/metavert-io/metarush/internal/content"
	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// The part and its two operations (D288, D289): `content.leave` shows a
// project's rules (anyone), `content.leave.set` changes those of a project's
// own Content Settings — a person only, never Claude or an AI app; anyone
// signed in may keep more on this computer, and letting more leave than the
// rules did takes an owner.

func init() {
	content.Register(content.Part{Name: PartName, Label: "What may leave",
		Empty:   func(_ context.Context, _ *app.App, r *models.ContentSettings) bool { return r.Leave.Empty() },
		Summary: func(_ context.Context, _ *app.App, r *models.ContentSettings) string { return Summary(r.Leave) },
		Copy: func(ctx context.Context, a *app.App, from, to *models.ContentSettings) error {
			if from.Leave.Empty() {
				return nil
			}
			r := *from.Leave
			r.UpdatedAt, r.UpdatedBy = time.Now().UTC(), &a.Actor
			to.Leave = &r
			return content.Save(ctx, a, to.ProjectID, bson.M{"leave": r}, nil)
		},
		Check: func(r *models.ContentSettings) string {
			if r.Leave == nil {
				return ""
			}
			return Problems(r.Leave)
		}})
	ops.Def("content.leave", "Show what of a project may leave this computer, and to whom (the What may leave part of its Content Settings): footage, voices, faces, words and pictures, to AI services, abilities, Claude, cloud storage and delivery", ops.Flags{},
		func(ctx context.Context, a *app.App, in *LeaveIn) (*LeaveOut, error) {
			pid, err := projectOf(ctx, a, in.ProjectID)
			if err != nil {
				return nil, err
			}
			return show(ctx, a, pid)
		})
	ops.Def("content.leave.set", "Change what of the projects using a project's own Content Settings may leave this computer, and to whom (people only: anyone may keep more here; letting more leave takes an owner)", ops.Flags{Mutating: true, HumanOnly: true},
		func(ctx context.Context, a *app.App, in *LeaveSetIn) (*LeaveOut, error) {
			return set(ctx, a, in)
		})
	ops.LeaveCheck = func(ctx context.Context, a *app.App, place, to, toWords string, kinds, projects []string, what string) error {
		return Check(ctx, a, Send{Place: place, To: to, ToWords: toWords, Kinds: kinds, Projects: projects, What: what})
	}
	ops.LeaveQuiet = Quiet // a post's plan says a refusal; the post records it
	ops.LeaveStorage = storageCheck
	ops.LeaveDatabase = databaseCheck
	ops.LeaveSwitch = switchCheck
	ops.LeaveCarry = carryCheck
	ops.RevisionTouches = RevisionTouches
}

// mayLoosen lets more of a client's things leave only for a person who is an
// owner (D289): never Claude, an AI app, Claude Code in a terminal or a
// script — even on this computer, where the command line counts as the owner.
func mayLoosen(ctx context.Context, a *app.App, op, what string, looser []string) error {
	if len(looser) == 0 {
		return nil
	}
	msg := fmt.Sprintf("Only a person who is an owner can let more of a client's things leave this computer: %s, %s.", what, joinAnd(looser))
	rd := ReaderOf(ctx, a)
	if !ops.CallerFrom(ctx).Human || rd.Is || a.Actor.Via == models.ViaAssistant || a.Actor.Via == models.ViaAssistantAuto || a.Actor.Via == "mcp" || a.ProposeOnly {
		return TerminalNote(apperr.New(apperr.HumanOnly, "%s", msg).WithHint("An owner does it in the dashboard or in their own terminal.").
			WithDetails(map[string]any{"reason": "may_not_leave", "looser": looser, "op": op}), rd)
	}
	if ops.IsOwnerCaller(ctx) {
		return nil
	}
	e := apperr.From(ops.RequireOwner(ctx, a, op))
	e.Message = msg
	return e
}

// rulesOfSettings are the rules held by a project's own settings record.
func rulesOfSettings(ctx context.Context, a *app.App, owner string) *models.LeaveRules {
	if owner == "" {
		return nil
	}
	rec, err := content.Record(ctx, a, owner)
	if err != nil || rec == nil {
		return nil
	}
	return rec.Leave
}

// switchCheck refuses using other Content Settings that would let more of a
// project's things leave, unless a person who is an owner does it (D289).
func switchCheck(ctx context.Context, a *app.App, projectName, fromOwner, toOwner string) error {
	looser := Looser(rulesOfSettings(ctx, a, fromOwner), rulesOfSettings(ctx, a, toOwner))
	if err := mayLoosen(ctx, a, "content.follow", "with those settings, for "+projectName, looser); err != nil {
		e := apperr.From(err)
		e.Hint = "Copy the settings it uses now instead (they keep their What may leave), or ask an owner. " + e.Hint
		return e
	}
	return nil
}

// rulesOf are the rules a project's things follow ("" the default's).
func rulesOf(ctx context.Context, a *app.App, g *content.Graph, project string) *models.LeaveRules {
	return rulesOfSettings(ctx, a, g.Resolve(project).Owner)
}

// carryCheck refuses putting a client's things where looser rules apply,
// unless a person who is an owner does it (D289): before are the projects
// whose rules they follow now ("" the default's), after those they would
// follow, into the project they go to ("" none).
func carryCheck(ctx context.Context, a *app.App, before, after []string, into, what string) error {
	if !anyRules(ctx, a) {
		return nil
	}
	g, err := content.Load(ctx, a)
	if err != nil {
		return err
	}
	merge := func(ps []string) *models.LeaveRules {
		var rs []*models.LeaveRules
		for _, p := range ps {
			rs = append(rs, rulesOf(ctx, a, g, p))
		}
		return Merge(rs...)
	}
	old := merge(before)
	looser := Looser(old, merge(after))
	if into != "" {
		for _, l := range Looser(old, rulesOf(ctx, a, g, into)) {
			if !contains(looser, l) {
				looser = append(looser, l)
			}
		}
	}
	where := what
	if into != "" {
		where += " into “" + g.Name(into) + "”"
	}
	return mayLoosen(ctx, a, "asset.move", "putting "+where, looser)
}

// LeaveIn names a project (without one, the default's settings).
type LeaveIn struct {
	ProjectID string `json:"projectId,omitempty"`
}

// LeaveSetIn changes the What may leave part of a project's own Content
// Settings (without a project, the default's). Rules replaces them all;
// otherwise the other fields change what they name.
type LeaveSetIn struct {
	ProjectID string `json:"projectId,omitempty"`
	// Rules replaces every rule (the dashboard's form).
	Rules *models.LeaveRules `json:"rules,omitempty"`
	// Nothing: nothing leaves this computer (false: what the places say again).
	Nothing *bool `json:"nothing,omitempty"`
	// Keep adds kinds that stay on this computer instead of going to a place:
	// place → kinds (ai: [footage, voices]).
	Keep map[string][]string `json:"keep,omitempty"`
	// Let takes kinds off a place's list (they may go there again).
	Let map[string][]string `json:"let,omitempty"`
	// Only sets who may receive what goes to a place: place → providers,
	// hosts or terms.
	Only map[string][]string `json:"only,omitempty"`
	// Anyone takes a place's Only list off.
	Anyone []string `json:"anyone,omitempty"`
	// Clear takes every rule off.
	Clear bool    `json:"clear,omitempty"`
	Note  *string `json:"note,omitempty" jsonschema:"maxLength=500"`
}

// LeavePlaceOut is one place as the dashboard and the CLI show it.
type LeavePlaceOut struct {
	Place string `json:"place" jsonschema:"enum=ai,enum=abilities,enum=claude,enum=storage,enum=delivery"`
	Title string `json:"title"`
	// Says what goes there, in plain words.
	Says string `json:"says"`
	// Kinds are the kinds that can go there (Claude never gets footage or
	// voices; storage keeps no words in a bucket).
	Kinds []string `json:"kinds"`
	// Keep and Only are the rule.
	Keep []string `json:"keep"`
	Only []string `json:"only"`
	// Receivers are who the place offers (providers, hosts abilities
	// declared, Claude's terms), for the Only list.
	Receivers []LeaveReceiver `json:"receivers"`
	// Summary says the rule in words.
	Summary string `json:"summary"`
}

// LeaveReceiver is one who a place can send to.
type LeaveReceiver struct {
	Name  string `json:"name"`
	Title string `json:"title"`
}

// LeaveOut is a project's What may leave.
type LeaveOut struct {
	Rules models.LeaveRules `json:"rules"`
	// Owner is the project whose settings they are, and its name.
	Owner     string          `json:"owner,omitempty"`
	OwnerName string          `json:"ownerName,omitempty"`
	Places    []LeavePlaceOut `json:"places"`
	// Kept is where this workspace keeps its media and words now.
	Kept []Kept `json:"kept"`
	// Problems say what these rules can't keep as they stand (cloud storage
	// already holding the client's things), and what to do.
	Problems []string `json:"problems"`
	// SetBy and SetAt say who last changed the rules.
	SetBy   string    `json:"setBy,omitempty"`
	SetAt   time.Time `json:"setAt,omitempty"`
	Summary string    `json:"summary"`
	// Looser lists what a change let leave that was kept here (set only).
	Looser []string `json:"looser,omitempty"`
}

func projectOf(ctx context.Context, a *app.App, ref string) (string, error) {
	if strings.TrimSpace(ref) == "" {
		return "", nil
	}
	p, err := ops.ResolveProject(ctx, a, ref)
	if err != nil {
		return "", err
	}
	return p.ID, nil
}

func show(ctx context.Context, a *app.App, projectID string) (*LeaveOut, error) {
	rules, owner, err := For(ctx, a, projectID)
	if err != nil {
		return nil, err
	}
	out := &LeaveOut{Owner: owner, Kept: StoragePlaces(a), Problems: StorageProblems(a, rules)}
	if out.Problems == nil {
		out.Problems = []string{}
	}
	if rules != nil {
		out.Rules = *rules
		if rules.UpdatedBy != nil && !rules.Empty() {
			out.SetBy, out.SetAt = firstNonEmpty(rules.UpdatedBy.UserName, "a person"), rules.UpdatedAt
		}
	}
	if owner != "" {
		if g, err := content.Load(ctx, a); err == nil {
			out.OwnerName = g.Name(owner)
		}
	}
	out.Places = placesOut(ctx, a, rules)
	out.Summary = Summary(rules) + "."
	if out.OwnerName != "" {
		out.Summary += " From “" + out.OwnerName + "”'s Content Settings."
	}
	if out.SetBy != "" {
		out.Summary += " " + upperFirst(setWords(out.SetBy, out.SetAt)) + "."
	}
	return out, nil
}

// placeKinds are the kinds each place can receive.
var placeKinds = map[string][]string{
	models.PlaceAI:        models.LeaveKinds,
	models.PlaceAbilities: models.LeaveKinds,
	models.PlaceClaude:    {models.LeaveFaces, models.LeaveWords, models.LeavePictures},
	models.PlaceStorage:   models.LeaveKinds,
	models.PlaceDelivery:  models.LeaveKinds,
	models.PlaceReviewers: models.LeaveKinds,
}

var placeSays = map[string]string{
	models.PlaceAI:        "Pictures, speech, voice copies, dubs, music, cloud transcripts and generated video are made by services elsewhere from what MetaRush sends them.",
	models.PlaceAbilities: "An ability's run sends what it is given to the places it reaches, through MetaRush. A program on this computer an owner let an ability use isn't one of those places: what the run gives it stays here, though the program may send things on by itself.",
	models.PlaceClaude:    "Claude in the panel, Claude Code in a terminal and AI apps through MCP read transcripts, frames and contact sheets through MetaRush; the panel's Claude uses the account of the computer that answers. Footage and sound never go to them. Claude Code in a terminal, or an AI app, can also open files on this computer directly: these rules hold only for what MetaRush hands it.",
	models.PlaceStorage:   "Where the workspace keeps its media (a cloud bucket, or a folder a sync service uploads) and its words (the database).",
	models.PlaceDelivery:  "Videos delivered to a folder a sync service uploads, timeline exports and the files Recipes makes for another workspace.",
	models.PlaceReviewers: "Review links a person sends to someone without an account: the version's review copy (its picture, sound, faces and graphics) and, when the link shows them, its words and who speaks. The page is kept in the workspace's cloud storage until the link ends.",
}

func placesOut(ctx context.Context, a *app.App, rules *models.LeaveRules) []LeavePlaceOut {
	out := []LeavePlaceOut{}
	for _, place := range models.LeavePlaces {
		p := rules.Place(place)
		title := upperFirst(PlaceWords(place, ""))
		if place == models.PlaceClaude {
			title = "Claude and AI apps"
		}
		po := LeavePlaceOut{Place: place, Title: title, Says: placeSays[place], Kinds: placeKinds[place],
			Keep: nonNil(p.Keep), Only: nonNil(p.Only), Receivers: receivers(ctx, a, place)}
		switch {
		case rules != nil && rules.Nothing:
			po.Summary = "Nothing goes there"
		case p.Empty():
			po.Summary = "Anything it offers"
		default:
			one := &models.LeaveRules{Places: map[string]models.LeavePlace{place: p}}
			po.Summary = Summary(one)
		}
		out = append(out, po)
	}
	return out
}

// Receivers lists who a place offers (set by the packages that know: AI
// services' providers, abilities' hosts).
var Receivers = map[string]func(ctx context.Context, a *app.App) []LeaveReceiver{}

func receivers(ctx context.Context, a *app.App, place string) []LeaveReceiver {
	if place == models.PlaceClaude {
		return []LeaveReceiver{{Name: "commercial", Title: "An account under commercial terms"}, {Name: "cloud", Title: "Claude through a cloud provider"}}
	}
	if f := Receivers[place]; f != nil {
		if r := f(ctx, a); r != nil {
			return r
		}
	}
	return []LeaveReceiver{}
}

func nonNil(xs []string) []string {
	if xs == nil {
		return []string{}
	}
	return xs
}

// Problems says what is wrong with rules ("" nothing).
func Problems(r *models.LeaveRules) string {
	for place, p := range r.Places {
		if !slices.Contains(models.LeavePlaces, place) {
			return fmt.Sprintf("“%s” isn't a place things leave to (ai, abilities, claude, storage, delivery, reviewers)", place)
		}
		for _, k := range p.Keep {
			if !slices.Contains(models.LeaveKinds, k) {
				return fmt.Sprintf("“%s” isn't a kind of thing (footage, voices, faces, words, pictures)", k)
			}
		}
		if len(p.Only) > 32 {
			return "at most 32 receivers per place"
		}
		if place == models.PlaceClaude {
			for _, o := range p.Only {
				if o != "commercial" && o != "cloud" && o != "consumer" {
					return fmt.Sprintf("Claude's terms are commercial, cloud or consumer, not “%s”", o)
				}
			}
		}
		if (place == models.PlaceStorage || place == models.PlaceDelivery || place == models.PlaceReviewers) && len(p.Only) > 0 {
			return "cloud storage, delivery and people outside the workspace take no list of receivers"
		}
	}
	if len([]rune(r.Note)) > 500 {
		return "the note is longer than 500 characters"
	}
	return ""
}

func set(ctx context.Context, a *app.App, in *LeaveSetIn) (*LeaveOut, error) {
	t, g, err := content.EditTarget(ctx, a, in.ProjectID)
	if err != nil {
		return nil, err
	}
	cur, _, err := For(ctx, a, t.ProjectID)
	if err != nil {
		return nil, err
	}
	r := models.LeaveRules{}
	switch {
	case in.Rules != nil:
		r = *in.Rules
		if in.Note == nil && cur != nil && r.Note == "" {
			r.Note = cur.Note
		}
	case cur != nil && !in.Clear:
		r = *cur
		r.Places = map[string]models.LeavePlace{}
		for k, v := range cur.Places {
			r.Places[k] = models.LeavePlace{Keep: slices.Clone(v.Keep), Only: slices.Clone(v.Only)}
		}
	}
	if r.Places == nil {
		r.Places = map[string]models.LeavePlace{}
	}
	if in.Nothing != nil {
		r.Nothing = *in.Nothing
	}
	place := func(name string) (models.LeavePlace, error) {
		name = strings.ToLower(strings.TrimSpace(name))
		if !slices.Contains(models.LeavePlaces, name) {
			return models.LeavePlace{}, apperr.New(apperr.InvalidInput, "“%s” isn't a place things leave to: say ai, abilities, claude, storage, delivery or reviewers.", name)
		}
		return r.Places[name], nil
	}
	kinds := func(xs []string) ([]string, error) {
		var out []string
		for _, k := range xs {
			k = strings.ToLower(strings.TrimSpace(k))
			if k == "all" || k == "everything" {
				out = append(out, models.LeaveKinds...)
				continue
			}
			if !slices.Contains(models.LeaveKinds, k) {
				return nil, apperr.New(apperr.InvalidInput, "“%s” isn't a kind of thing: say footage, voices, faces, words or pictures.", k)
			}
			out = append(out, k)
		}
		return out, nil
	}
	for name, ks := range in.Keep {
		p, err := place(name)
		if err != nil {
			return nil, err
		}
		add, err := kinds(ks)
		if err != nil {
			return nil, err
		}
		for _, k := range add {
			if !slices.Contains(p.Keep, k) {
				p.Keep = append(p.Keep, k)
			}
		}
		r.Places[strings.ToLower(strings.TrimSpace(name))] = p
	}
	for name, ks := range in.Let {
		p, err := place(name)
		if err != nil {
			return nil, err
		}
		take, err := kinds(ks)
		if err != nil {
			return nil, err
		}
		p.Keep = slices.DeleteFunc(p.Keep, func(k string) bool { return slices.Contains(take, k) })
		r.Places[strings.ToLower(strings.TrimSpace(name))] = p
	}
	for name, who := range in.Only {
		p, err := place(name)
		if err != nil {
			return nil, err
		}
		p.Only = who
		r.Places[strings.ToLower(strings.TrimSpace(name))] = p
	}
	for _, name := range in.Anyone {
		p, err := place(name)
		if err != nil {
			return nil, err
		}
		p.Only = nil
		r.Places[strings.ToLower(strings.TrimSpace(name))] = p
	}
	if in.Note != nil {
		r.Note = *in.Note
	}
	Normalize(&r)
	if why := Problems(&r); why != "" {
		return nil, apperr.New(apperr.InvalidInput, "%s.", upperFirst(why))
	}
	looser := Looser(cur, &r)
	if err := mayLoosen(ctx, a, "content.leave.set", "here", looser); err != nil {
		return nil, err
	}
	var saveErr error
	if r.Empty() && r.Note == "" {
		saveErr = content.Save(ctx, a, t.ProjectID, nil, bson.M{"leave": ""})
	} else {
		r.UpdatedAt, r.UpdatedBy = time.Now().UTC(), &a.Actor
		saveErr = content.Save(ctx, a, t.ProjectID, bson.M{"leave": r}, nil)
	}
	if saveErr != nil {
		return nil, saveErr
	}
	a.Audit(ctx, "content.leave.set", "project", t.ProjectID, t.ProjectID, map[string]any{"nothing": r.Nothing, "places": r.Places, "note": r.Note, "looser": looser})
	out, err := show(ctx, a, t.ProjectID)
	if err != nil {
		return nil, err
	}
	out.Looser = looser
	out.Summary = "Saved: " + lowerFirst(Summary(&r)) + "."
	if OnRulesChanged != nil { // review links these rules no longer allow end (D318)
		for _, line := range OnRulesChanged(ctx, a) {
			out.Summary += " " + line
		}
	}
	if note := t.Note(g); note != "" {
		out.Summary += " " + note
	}
	if len(out.Problems) > 0 {
		out.Summary += " " + out.Problems[0]
	}
	return out, nil
}

func lowerFirst(s string) string {
	if s == "" {
		return s
	}
	r := []rune(s)
	if len(r) > 1 && strings.ToUpper(string(r[1])) == string(r[1]) && strings.ToLower(string(r[1])) != string(r[1]) {
		return s // an acronym ("AI services")
	}
	return strings.ToLower(string(r[0])) + string(r[1:])
}

// storageCheck refuses a move of the media to cloud storage while a
// client's rules keep their things from it (D290), saying which and what to
// do.
func storageCheck(ctx context.Context, a *app.App, to config.Media) error {
	k := MediaKept(to)
	if !k.Cloud {
		return nil
	}
	if !anyRules(ctx, a) {
		return nil
	}
	c, err := newChecker(ctx, a)
	if err != nil {
		return err
	}
	// Archived projects count (their footage stays), and so does footage in
	// no project (the default's rules; a clip in the Trash counts under the
	// projects it was in).
	for _, pid := range append(c.g.Projects(), "") {
		s := Send{Place: models.PlaceStorage, To: "cloud", ToWords: k.Words, Kinds: MediaKinds, Projects: []string{pid}, What: "the workspace's media"}
		if r := c.problem(s); r != nil {
			Record(ctx, a, r, s)
			e := r.Err()
			e.Hint = "Keep the media in a folder on this computer, or keep that client in a workspace of its own. " + e.Hint
			return e
		}
	}
	return nil
}

// databaseCheck refuses moving the workspace's database to a database that
// is cloud storage (DatabaseKept: elsewhere on the internet) while any
// client's rules keep their words from cloud storage (D337). Every project
// counts — archived ones too — and the words of footage in no project (the
// default's rules). quiet (a plan) only says it.
func databaseCheck(ctx context.Context, a *app.App, to config.Database, quiet bool) error {
	k := DatabaseKept(to)
	if !k.Cloud || !anyRules(ctx, a) {
		return nil
	}
	c, err := newChecker(ctx, a)
	if err != nil {
		return err
	}
	for _, pid := range append(c.g.Projects(), "") {
		s := Send{Place: models.PlaceStorage, To: "cloud", ToWords: k.Words, Kinds: k.Kinds, Projects: []string{pid}, What: "the workspace's database"}
		if r := c.problem(s); r != nil {
			if !quiet {
				Record(ctx, a, r, s)
			}
			e := r.Err()
			e.Hint = "Keep the workspace's database on this computer or your network, or keep that client in a workspace of its own. " + e.Hint
			return e
		}
	}
	return nil
}

// OnRulesChanged acts on what rules just saved no longer allow (review
// links to people outside the workspace end, D318) and says what it did.
// Set by internal/reviewlinks.
var OnRulesChanged func(ctx context.Context, a *app.App) []string

package catalog

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// The menu (D272): what MetaRush can do as abilities — the first-party ones
// and those a workspace added, side by side — each saying whether it works on
// this computer now or what it needs first. `metarush catalogue list|show`
// and Library → Abilities read it.

// MenuItem is one ability in the menu.
type MenuItem struct {
	// Name is what find, describe and run take ("metarush/steady-footage",
	// or an added ability's name).
	Name string `json:"name"`
	// ID is a first-party ability's stable ID ("steady-footage").
	ID   string `json:"id,omitempty"`
	Kind string `json:"kind" jsonschema:"enum=first-party,enum=ability"`
	// Group and GroupTitle are the part of the menu it is in.
	Group      string `json:"group"`
	GroupTitle string `json:"groupTitle"`
	Title      string `json:"title"`
	Does       string `json:"does"`
	// Runs is the operation that does it; Fixed the fields it always runs
	// with; Command the command line.
	Runs    string         `json:"runs"`
	Fixed   map[string]any `json:"fixed,omitempty"`
	Command string         `json:"command,omitempty"`
	// Cost is free, priced or plan; Leaves what leaves this computer (added
	// abilities); Requires what it needs on a computer.
	Cost     string   `json:"cost,omitempty" jsonschema:"enum=,enum=free,enum=priced,enum=keyed,enum=plan"`
	Leaves   string   `json:"leaves,omitempty"`
	Requires []string `json:"requires"`
	// WorksHere: it works on this computer now; Missing what it needs here
	// first; Blocked: it can't run until that is done.
	WorksHere bool   `json:"worksHere"`
	Missing   string `json:"missing,omitempty"`
	Blocked   bool   `json:"blocked,omitempty"`
	// Partly says what it still does here when it works here only in part.
	Partly string `json:"partly,omitempty"`
	// Changes: running it changes something; PeopleOnly: a person starts it.
	Changes    bool `json:"changes"`
	PeopleOnly bool `json:"peopleOnly"`
	// NeedsKey: it needs a key to a paid service.
	NeedsKey bool `json:"needsKey"`
	// MadeWith is what a receipt or a run's step records for it.
	MadeWith string `json:"madeWith"`
	// Line is the item in one plain line.
	Line string `json:"line"`
}

// MenuGroup counts a group's abilities.
type MenuGroup struct {
	Key      string `json:"key"`
	Title    string `json:"title"`
	Count    int    `json:"count"`
	WorksNow int    `json:"worksNow"`
}

// CatalogueListIn lists the menu.
type CatalogueListIn struct {
	Group    string `json:"group,omitempty" jsonschema:"enum=,enum=understand,enum=fix,enum=make,enum=bring,enum=edit,enum=deliver,enum=added,description=Only this part of the menu."`
	WorksNow bool   `json:"worksNow,omitempty" jsonschema:"description=Only what works on this computer now."`
	// Kind: built-in (first-party) or added (abilities a workspace added).
	Kind string `json:"kind,omitempty" jsonschema:"enum=,enum=built-in,enum=added"`
}

// CatalogueListOut is the menu, group by group.
type CatalogueListOut struct {
	Items  []MenuItem  `json:"items"`
	Groups []MenuGroup `json:"groups"`
	// BuiltIn and Added count what MetaRush has and what the workspace added;
	// WorksNow what works here now, NoKeys what of that needs no key.
	BuiltIn  int    `json:"builtIn"`
	Added    int    `json:"added"`
	WorksNow int    `json:"worksNow"`
	NoKeys   int    `json:"noKeys"`
	Machine  string `json:"machine"`
	Message  string `json:"message"`
}

// CatalogueShowIn names one ability of the menu.
type CatalogueShowIn struct {
	Name string `json:"name" jsonschema:"minLength=1,description=A first-party ability's ID or name (steady-footage\\, metarush/steady-footage)\\, an added ability's name\\, or an operation a first-party ability is."`
}

// CatalogueShowOut is one ability of the menu with how to use it.
type CatalogueShowOut struct {
	Item        MenuItem     `json:"item"`
	Description *Description `json:"description"`
	Message     string       `json:"message"`
}

func init() {
	ops.Def("catalogue.list", "List what MetaRush can do as abilities, built in and added to this workspace, group by group: each says whether it works on this computer now or what it needs first (a key, a Mac, a program, an owner's approval)", ops.Flags{},
		func(ctx context.Context, a *app.App, in *CatalogueListIn) (*CatalogueListOut, error) {
			return Menu(ctx, a, in)
		})
	ops.Def("catalogue.show", "Show one ability of the menu, built in or added: what it does, the operation that runs it and the fields it fixes, what it needs, whether it works here, and the name a receipt records", ops.Flags{},
		func(ctx context.Context, a *app.App, in *CatalogueShowIn) (*CatalogueShowOut, error) {
			return ShowMenuItem(ctx, a, in.Name)
		})
}

// menuKinds are the catalogues the menu shows.
var menuKinds = []string{KindFirstParty, kindAbility}

// kindAbility is internal/abilities' kind (it imports this package).
const kindAbility = "ability"

// Menu lists the menu.
func Menu(ctx context.Context, a *app.App, in *CatalogueListIn) (*CatalogueListOut, error) {
	if in.Group != "" && GroupTitle(in.Group) == "" {
		return nil, apperr.New(apperr.InvalidInput, "The menu has no part called %q.", in.Group).WithDetails(map[string]any{"groups": groupKeys()})
	}
	kinds := menuKinds
	switch in.Kind {
	case "built-in":
		kinds = []string{KindFirstParty}
	case "added":
		kinds = []string{kindAbility}
	case "":
	default:
		return nil, apperr.New(apperr.InvalidInput, "Say built-in or added.")
	}
	es, err := All(ctx, a, kinds...)
	if err != nil {
		return nil, err
	}
	out := &CatalogueListOut{Items: []MenuItem{}, Groups: []MenuGroup{}}
	if a != nil {
		out.Machine = a.Cfg.Installation.Name
	}
	counts := map[string]*MenuGroup{}
	for _, g := range Groups {
		counts[g.Key] = &MenuGroup{Key: g.Key, Title: g.Title}
	}
	for _, e := range es {
		it := menuItem(Strict(e))
		if in.Group != "" && it.Group != in.Group {
			continue
		}
		if in.WorksNow && !it.WorksHere {
			continue
		}
		out.Items = append(out.Items, it)
		if g := counts[it.Group]; g != nil {
			g.Count++
			if it.WorksHere {
				g.WorksNow++
			}
		}
		if it.Kind == KindFirstParty {
			out.BuiltIn++
		} else {
			out.Added++
		}
		if it.WorksHere {
			out.WorksNow++
			if !it.NeedsKey {
				out.NoKeys++
			}
		}
	}
	order := map[string]int{}
	for i, g := range Groups {
		order[g.Key] = i
	}
	sort.SliceStable(out.Items, func(i, j int) bool {
		x, y := out.Items[i], out.Items[j]
		if order[x.Group] != order[y.Group] {
			return order[x.Group] < order[y.Group]
		}
		return strings.ToLower(x.Title) < strings.ToLower(y.Title)
	})
	for _, g := range Groups {
		if c := counts[g.Key]; c.Count > 0 {
			out.Groups = append(out.Groups, *c)
		}
	}
	out.Message = menuMessage(out, a != nil, in)
	return out, nil
}

func menuMessage(o *CatalogueListOut, here bool, in *CatalogueListIn) string {
	n := len(o.Items)
	if n == 0 {
		if in.WorksNow {
			return "Nothing there works on this computer yet; leave out --works-now to see what each needs."
		}
		return "Nothing there yet."
	}
	s := fmt.Sprintf("%d built in", o.BuiltIn)
	if o.Added > 0 {
		s += fmt.Sprintf(" and %d added to this workspace", o.Added)
	}
	if !here {
		return s + "."
	}
	if in.WorksNow {
		return fmt.Sprintf("%s work on %s now, %d of them with no keys.", s, o.Machine, o.NoKeys)
	}
	return fmt.Sprintf("%s: %d work on %s now, %d of them with no keys.", s, o.WorksNow, o.Machine, o.NoKeys)
}

func groupKeys() []string {
	var out []string
	for _, g := range Groups {
		out = append(out, g.Key)
	}
	return out
}

// menuItem is an entry as the menu shows it.
func menuItem(e Entry) MenuItem {
	it := MenuItem{Name: e.Name, Kind: e.Kind, Group: e.Group, GroupTitle: GroupTitle(e.Group), Title: e.Title, Does: e.About,
		Runs: e.RunWith, Command: e.Command, Cost: e.Cost, Leaves: e.Leaves, Requires: e.Requires, Missing: e.Missing, Blocked: e.Blocked, Partly: e.Partly,
		Changes: e.Changes, PeopleOnly: e.PeopleOnly, MadeWith: e.MadeWith}
	if it.Requires == nil {
		it.Requires = []string{}
	}
	if e.Kind == KindFirstParty {
		it.ID = strings.TrimPrefix(e.Name, FirstPartyPrefix)
		it.Fixed = e.RunInput
		if fp, ok := firstPartyNamed(e.Name); ok {
			it.NeedsKey = len(fp.Needs.Keys) > 0
		}
	} else {
		it.NeedsKey = hasKeyWord(e.Requires)
	}
	if !strings.HasSuffix(it.Does, ".") && it.Does != "" {
		it.Does += "."
	}
	it.WorksHere = e.WorksHere != nil && *e.WorksHere
	switch {
	case e.WorksHere == nil:
		it.Line = it.Title + " — " + strings.TrimSuffix(it.Does, ".")
	case it.WorksHere && it.Partly != "":
		it.Line = it.Title + " — works here, " + it.Partly
	case it.WorksHere:
		it.Line = it.Title + " — works here"
	default:
		it.Line = it.Title + " — " + it.Missing
	}
	switch it.Cost {
	case CostPriced:
		it.Line += " (priced first)"
	case CostKeyed:
		it.Line += " (the service may charge)"
	}
	return it
}

func hasKeyWord(rs []string) bool {
	for _, r := range rs {
		if strings.HasSuffix(r, " key") {
			return true
		}
	}
	return false
}

// ShowMenuItem shows one ability of the menu: an added ability by its name
// first; a first-party one by its name (metarush/<id>), by its bare ID when
// no added ability has that name, or by the operation it is.
func ShowMenuItem(ctx context.Context, a *app.App, name string) (*CatalogueShowOut, error) {
	name = strings.TrimSpace(name)
	var d *Description
	if added, err := describeAdded(ctx, a, name); err != nil {
		return nil, err
	} else if added != nil {
		d = added
	} else if fp, ok := firstPartyNamed(name); ok {
		d = DescribeFirstParty(ctx, a, fp)
	} else if fp, ok := firstPartyFor(name); ok {
		d = DescribeFirstParty(ctx, a, fp)
	} else {
		return nil, apperr.New(apperr.NotFound, "The menu has nothing called %s.", name).
			WithHint("The menu of abilities says what it has.").WithCommand("metarush catalogue list").WithDetails(map[string]any{"name": name})
	}
	e := Entry{Name: d.Name, Kind: d.Kind, About: d.About, Title: d.Title, Group: d.Group, Command: d.Command, Cost: d.Cost, Leaves: d.Leaves,
		Requires: d.Requires, WorksHere: d.WorksHere, Missing: d.Missing, Partly: d.Partly, Blocked: d.Blocked, Changes: d.Changes, PeopleOnly: d.PeopleOnly,
		RunWith: d.RunWith, RunInput: d.RunInput, MadeWith: d.MadeWith}
	it := menuItem(e)
	out := &CatalogueShowOut{Item: it, Description: d}
	switch {
	case d.WorksHere == nil:
		out.Message = fmt.Sprintf("%s: %s", it.Title, it.Does)
	case it.WorksHere:
		out.Message = fmt.Sprintf("%s works on this computer now. %s", it.Title, it.Does)
	case strings.HasPrefix(it.Missing, "needs "):
		out.Message = fmt.Sprintf("%s %s first. %s", it.Title, it.Missing, it.Does)
	default:
		out.Message = fmt.Sprintf("%s can't run here: it %s. %s", it.Title, it.Missing, it.Does)
	}
	return out, nil
}

// describeAdded describes the workspace's ability of that name, or nil when
// it has none.
func describeAdded(ctx context.Context, a *app.App, name string) (*Description, error) {
	for _, s := range Sources() {
		if s.Kind() != kindAbility {
			continue
		}
		d, err := s.Describe(ctx, a, name)
		switch {
		case err == nil:
			strictDescription(d)
			return d, nil
		case apperr.From(err).Code != apperr.NotFound:
			return nil, err
		}
	}
	return nil, nil
}

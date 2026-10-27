package abilities

import (
	"context"
	"embed"
	"io/fs"
	"path"
	"reflect"
	"sort"
	"strings"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/catalog"
	"github.com/metavert-io/metarush/internal/models"
)

// Abilities join find (D268): the workspace's abilities are a catalogue of
// their own, found and described like operations and run only through
// ability.run, with a plan first. Each entry carries the strictest flags its
// manifest and approval allow — a run is always a change; a person's to do
// unless it is free, uses no key and sends nothing but words (the run's
// decider, D234) — and says what it costs, what leaves this computer and
// whether it works here. One no owner approved, or not set up on this
// computer, is found with a plain line saying so and can't be run.

// KindAbility is the kind of the abilities' catalogue entries.
const KindAbility = "ability"

type catalogueSource struct{}

func init() { _ = catalog.Register(catalogueSource{}) }

// Kind is "ability".
func (catalogueSource) Kind() string { return KindAbility }

// Entries lists the workspace's abilities (none without one).
func (catalogueSource) Entries(ctx context.Context, a *app.App) ([]catalog.Entry, error) {
	if a == nil {
		return nil, nil
	}
	vs, err := allVersions(ctx, a)
	if err != nil {
		return nil, err
	}
	newest, approved, order := latest(vs)
	out := make([]catalog.Entry, 0, len(order))
	for _, name := range order {
		out = append(out, entryHere(ctx, a, newest[name], approved[name]))
	}
	return out, nil
}

// Describe says how to run one: through ability.run, its inputs under
// "inputs" with the plan's fingerprint, after ability.plan.
func (catalogueSource) Describe(ctx context.Context, a *app.App, name string) (*catalog.Description, error) {
	name = strings.TrimSpace(name)
	if a == nil || !ValidName(name) {
		return nil, apperr.New(apperr.NotFound, "No ability by that name.")
	}
	vs, err := versionsOf(ctx, a, name)
	if err != nil {
		return nil, err
	}
	if len(vs) == 0 {
		return nil, apperr.New(apperr.NotFound, "No ability by that name.")
	}
	newest, approved, _ := latest(vs)
	e := entryHere(ctx, a, newest[name], approved[name])
	use := newest[name]
	if approved[name] != nil {
		use = approved[name]
	}
	d := &catalog.Description{Name: e.Name, Kind: KindAbility, About: e.About, Command: e.Command, Usage: e.Command + " --input name=value…",
		Changes: e.Changes, PeopleOnly: e.PeopleOnly, OwnerOnly: e.OwnerOnly, ThisComputerOnly: e.ThisComputerOnly,
		RunWith: e.RunWith, RunInput: e.RunInput, Title: e.Title, Group: e.Group, Cost: e.Cost, Leaves: e.Leaves, Requires: e.Requires,
		WorksHere: e.WorksHere, Missing: e.Missing, Blocked: e.Blocked, MadeWith: e.MadeWith,
		Input: runInputSchema(use.Facts), Returns: catalog.Returns(typeOfRunOut)}
	d.Example = catalog.Example(d.Input)
	d.Note = "Plan it first: ability.plan with ref " + e.Name + " and the same inputs says what the run uses, sends, costs and who decides; give its fingerprint as plan. " +
		deciderWords(decider(use.Facts))
	if use.Notes != "" {
		d.Note += " Its notes: " + clipWords(use.Notes, 600)
	}
	return d, nil
}

// Samples are typical abilities for the selection eval (catalog.Sampler):
// the manifests in samples/, read and turned into entries the way a
// workspace's are, as approved versions set up here.
func (catalogueSource) Samples() []catalog.Entry {
	var out []catalog.Entry
	for _, v := range SampleVersions() {
		e := entryOf(&v, &v)
		out = append(out, e)
	}
	return out
}

//go:embed samples
var samplesFS embed.FS

// SampleVersions are the sample manifests as approved versions.
func SampleVersions() []models.AbilityVersion {
	var out []models.AbilityVersion
	dirs, _ := fs.ReadDir(samplesFS, "samples")
	for _, d := range dirs {
		b, err := samplesFS.ReadFile(path.Join("samples", d.Name(), "ability.yaml"))
		if err != nil {
			continue
		}
		m, err := ParseManifest(b)
		if err != nil {
			panic("abilities: sample " + d.Name() + ": " + err.Error())
		}
		out = append(out, models.AbilityVersion{Name: m.Name, Title: m.Title, Does: m.Does, Search: m.Search, Facts: factsOf(m),
			Hash: strings.Repeat("0", 64), Number: 1, State: models.AbilityApproved, Manifest: string(b)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// entryHere is an ability as a catalogue entry, saying whether it works on
// this computer.
func entryHere(ctx context.Context, a *app.App, newest, approved *models.AbilityVersion) catalog.Entry {
	e := entryOf(newest, approved)
	if e.Blocked {
		return e
	}
	h, _ := hereFor(ctx, a, approved.Facts, approved.Name, true)
	if h.Ready {
		e.WorksHere, e.Missing = catalog.Yes(), ""
		return e
	}
	e.WorksHere, e.Blocked, e.Missing = catalog.No(), true, hereMissing(approved.Facts, h)
	return e
}

// entryOf is an ability as a catalogue entry from its versions alone: its
// approved version's manifest (the newest's while none is approved).
func entryOf(newest, approved *models.AbilityVersion) catalog.Entry {
	use := newest
	if approved != nil {
		use = approved
	}
	f := use.Facts
	dec := decider(f)
	e := catalog.Entry{Name: use.Name, Kind: KindAbility, About: strings.TrimSuffix(strings.TrimSpace(use.Does), "."), Title: use.Title,
		Words: append([]string{use.Title}, use.Search...), Group: catalog.GroupAdded, Command: "metarush ability run " + use.Name,
		// A run is a change; anything but a safe one is a person's to do
		// (D234), so run never starts it for an agent.
		Changes: true, PeopleOnly: dec != DecideSafe,
		RunWith: "ability.run", RunInput: map[string]any{"ref": use.Name + "@" + use.Hash},
		Cost: costClass(f), Leaves: leavesShort(f), Requires: requiresOf(f), MadeWith: use.Name + "@" + Short(use.Hash)}
	switch {
	case approved == nil && newest.State == models.AbilityFailed:
		e.WorksHere, e.Blocked, e.Missing = catalog.No(), true, "needs a version that passes its check, then an owner's approval"
	case approved == nil:
		e.WorksHere, e.Blocked, e.Missing = catalog.No(), true, "needs an owner's approval"
	case dec == DecideLikeness:
		e.WorksHere, e.Blocked, e.Missing = catalog.No(), true, "makes someone's likeness, which MetaRush doesn't run yet"
	case f.Network == NetworkOpen && !approved.OpenNetwork:
		e.WorksHere, e.Blocked, e.Missing = catalog.No(), true, "needs an owner to agree to its open network"
	}
	return e
}

// hereMissing says in a few words what this computer lacks for a run.
func hereMissing(f models.AbilityFacts, h Here) string {
	var ws []string
	if !h.OS {
		ws = append(ws, "needs "+strings.Join(osWords(f.OS), " or "))
	}
	if h.Isolation == "" {
		ws = append(ws, "needs a sandbox this computer can make")
	}
	setUpLine := false
	for _, p := range h.Problems {
		if strings.HasPrefix(p, "A person hasn't set it up") {
			setUpLine = true
		}
	}
	programs := false
	for _, p := range h.Programs {
		if !p.OK {
			programs = true
		}
	}
	switch {
	case setUpLine:
		ws = append(ws, "needs setting up on this computer")
	case programs && h.SetUp:
		ws = append(ws, "needs setting up on this computer again")
	case programs:
		ws = append(ws, "needs setting up on this computer")
	}
	var keys []string
	for _, k := range h.Keys {
		if !k.Configured {
			keys = append(keys, k.Name)
		}
	}
	if len(keys) > 0 {
		ws = append(ws, "needs the "+joinAnd(keys)+" "+map[bool]string{true: "key", false: "keys"}[len(keys) == 1]+" on this computer")
	}
	if len(ws) == 0 {
		return "needs setting up on this computer"
	}
	return strings.Join(ws, ", ")
}

// leavesShort says in a few words what leaves this computer.
func leavesShort(f models.AbilityFacts) string {
	if f.Network == NetworkOpen {
		return "it may reach any public place on the internet"
	}
	what := sendsList(f.Sends)
	if len(what) == 0 || f.Network == NetworkOff {
		return "nothing leaves this computer"
	}
	return joinAnd(what) + " leave this computer"
}

// requiresOf says what it needs on a computer, a few words each.
func requiresOf(f models.AbilityFacts) []string {
	var out []string
	if len(f.OS) > 0 {
		out = append(out, strings.Join(osWords(f.OS), " or "))
	}
	out = append(out, f.Programs...)
	for _, k := range f.Keys {
		out = append(out, "the "+k.Name+" key")
	}
	return out
}

// runInputSchema is ability.run's input as find's run gives it: the
// ability's inputs under "inputs", the plan's fingerprint, and where what it
// brings back goes.
func runInputSchema(f models.AbilityFacts) map[string]any {
	props := map[string]any{}
	var req []any
	for _, in := range f.Inputs {
		p := map[string]any{}
		if in.Says != "" {
			p["description"] = in.Says
		}
		switch in.Kind {
		case InWords:
			p["type"] = "string"
			if in.Max != nil {
				p["maxLength"] = int(*in.Max)
			}
		case InNumber:
			p["type"] = "number"
			if in.Min != nil {
				p["minimum"] = *in.Min
			}
			if in.Max != nil {
				p["maximum"] = *in.Max
			}
		case InChoice:
			p["type"] = "string"
			var opts []any
			for _, o := range in.Options {
				opts = append(opts, o)
			}
			p["enum"] = opts
		case InYesNo:
			p["type"] = "boolean"
		default: // footage, picture, sound
			p["type"] = "string"
			if p["description"] == nil {
				p["description"] = "The " + kindWords(in.Kind) + " to use: its ID or name."
			}
		}
		if in.Default != nil {
			p["default"] = in.Default
		} else if !in.Optional {
			req = append(req, in.Name)
		}
		props[in.Name] = p
	}
	inputs := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(req) > 0 {
		inputs["required"] = req
	}
	return map[string]any{"type": "object", "additionalProperties": false, "required": []any{"plan"},
		"properties": map[string]any{
			"inputs":    inputs,
			"plan":      map[string]any{"type": "string", "description": "The fingerprint ability.plan gave for these inputs: what starts is what was planned."},
			"projectId": map[string]any{"type": "string", "description": "The project what it brings back goes to (default: footage to the Inbox, pictures to the workspace library)."},
			"maxUsd":    map[string]any{"type": "number", "minimum": 0, "description": "A priced run: what a person agreed it may cost (at least the estimate)."},
		}}
}

func clipWords(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	if r := []rune(s); len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

var typeOfRunOut = reflect.TypeOf(AbilityRunOut{})

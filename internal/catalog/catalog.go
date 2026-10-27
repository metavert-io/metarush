// Package catalog is what MetaRush can do, as entries an agent finds by
// describing a task in plain words (D221): MetaRush's operations — the ops
// registry, with the command-line command of each and the words people use for
// it — and any other catalogue that registers a Source (abilities join this
// way). find ranks entries deterministically, without a model call, so the
// same words always give the same answer, and a larger catalogue costs an
// agent nothing until find returns its entries: MCP's core (internal/mcp)
// stays at about ten tools whatever the catalogue holds.
//
// The ranking is BM25F over each entry's fields (its name, its command, the
// words people use for it, its summary and its command's help line), with
// stemming, a few synonyms and phrase matches. Its accuracy is measured by the
// tool-selection eval (selection.json, D223), which go test gates.
package catalog

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// KindOperation is an entry of the operation registry.
const KindOperation = "operation"

// Entry is one thing find can return.
type Entry struct {
	// Name is unique within its kind: an operation's name ("footage.moments").
	Name string
	// Kind says which catalogue it comes from ("operation").
	Kind string
	// About is what it does, in a sentence.
	About string
	// Command is the command line that does it ("metarush footage moments").
	Command string
	// CommandAbout is that command's help line (empty when it has none).
	CommandAbout string
	// Words are other words people use for it ("review copy", "watch it").
	Words []string
	// Changes: it changes something. PeopleOnly: only a person may do it
	// (never an agent). OwnerOnly: only an owner of the workspace.
	// ThisComputerOnly: it works only where the files are.
	Changes, PeopleOnly, OwnerOnly, ThisComputerOnly bool
	// RunWith is the operation that runs this entry, with RunInput as its
	// input; empty for an operation, which runs itself. Another catalogue's
	// entries are run through an operation of their own (abilities through
	// theirs), so `run` and the CLI need nothing new to reach them: the
	// caller's input goes under RunInput's "input" key (RunInputKey), and
	// RunWith's own flags (changes, people only) decide what may run it.
	RunWith  string
	RunInput map[string]any
	// RunOwnInput: the caller's input is RunWith's own input, with
	// RunInput's fields fixed, instead of going under RunInputKey — a
	// first-party ability runs as the operation it is (D269).
	RunOwnInput bool

	// What it needs, and whether it works here (D270). Operations say
	// nothing of their own; a first-party ability fills these in for the
	// operation it is, and an ability from its manifest and approval.

	// Title is its name in a menu ("Steady shaky footage"), when it has one.
	Title string
	// Group is the part of the menu it is in (GroupUnderstand…; GroupAdded
	// for abilities a workspace added).
	Group string
	// Cost is CostFree, CostPriced or CostPlan; empty when nothing says.
	Cost string
	// Leaves says what leaves this computer when it runs, in a few words
	// ("nothing leaves this computer"); empty when nothing says.
	Leaves string
	// Requires says what it needs on a computer, a few words each ("a Mac",
	// "rclone", "an ElevenLabs key").
	Requires []string
	// WorksHere says whether it works on this computer now; nil when nobody
	// can tell (no workspace open).
	WorksHere *bool
	// Missing says what it needs here first, in plain words ("needs a Mac",
	// "needs an owner's approval"); empty when it works here.
	Missing string
	// Partly says what it still does on this computer when it works here
	// only in part ("only plain-colour backgrounds").
	Partly string
	// Blocked: run refuses it until what Missing says is done (an ability no
	// owner approved, or one not set up on this computer).
	Blocked bool
	// FirstParty names the first-party ability this entry is
	// ("metarush/best-moments") when it is an operation find shows in its
	// place (D270).
	FirstParty string
	// MadeWith is what a receipt or a run's step records for it: a
	// first-party ability's name, an ability's name@version.
	MadeWith string
}

// The groups of the menu (Library → Abilities, `metarush catalogue list`).
const (
	GroupUnderstand = "understand" // understand footage
	GroupFix        = "fix"        // fix sources
	GroupMake       = "make"       // make media
	GroupBring      = "bring"      // bring things in
	GroupEdit       = "edit"       // shape the cut
	GroupDeliver    = "deliver"    // check and deliver
	GroupAdded      = "added"      // abilities a workspace added
)

// Groups lists the groups in menu order, each with its plain name.
var Groups = []struct{ Key, Title string }{
	{GroupUnderstand, "Understand footage"}, {GroupFix, "Fix sources"}, {GroupMake, "Make media"},
	{GroupBring, "Bring things in"}, {GroupEdit, "Shape the cut"}, {GroupDeliver, "Check and deliver"},
	{GroupAdded, "Added to this workspace"},
}

// GroupTitle is a group's plain name ("" for an unknown group).
func GroupTitle(key string) string {
	for _, g := range Groups {
		if g.Key == key {
			return g.Title
		}
	}
	return ""
}

// Cost classes. Only CostFree says it costs nothing: anything that uses a
// key to a service is CostKeyed at least, whatever it says of itself.
const (
	CostFree   = "free"   // nothing to pay: no price, no key
	CostPriced = "priced" // each run priced first and paid to the service that does it
	CostKeyed  = "keyed"  // it uses a key to a service, which may charge for what it does
	CostPlan   = "plan"   // it uses the person's Claude Code, counted toward their plan
)

// Yes and No are WorksHere's values.
func Yes() *bool { t := true; return &t }
func No() *bool  { f := false; return &f }

// Source is a catalogue find searches.
type Source interface {
	// Kind names its entries' kind ("operation", "ability").
	Kind() string
	// Entries lists what it holds. a may be nil (the eval, a program
	// without a workspace): a source that needs the workspace then returns
	// none.
	Entries(ctx context.Context, a *app.App) ([]Entry, error)
	// Describe says how to use one entry, or returns apperr.NotFound.
	Describe(ctx context.Context, a *app.App, name string) (*Description, error)
}

var (
	mu      sync.RWMutex
	sources []Source
)

// RunInputKey is where an entry's RunWith operation takes the caller's input.
const RunInputKey = "input"

// RunInputFor is the input of an entry's RunWith operation: its RunInput,
// with the caller's input under RunInputKey.
func RunInputFor(runInput map[string]any, input map[string]any) map[string]any {
	out := make(map[string]any, len(runInput)+1)
	for k, v := range runInput {
		out[k] = v
	}
	if len(input) > 0 {
		out[RunInputKey] = input
	}
	return out
}

// Register adds a catalogue to find and describe, and returns what takes it
// out again (for tests). Another catalogue calls it from an init function;
// registering a kind twice panics.
func Register(s Source) (unregister func()) {
	mu.Lock()
	defer mu.Unlock()
	for _, have := range sources {
		if have.Kind() == s.Kind() {
			panic("catalog: two sources of kind " + s.Kind())
		}
	}
	sources = append(sources, s)
	return func() {
		mu.Lock()
		defer mu.Unlock()
		for i, have := range sources {
			if have.Kind() == s.Kind() {
				sources = append(sources[:i:i], sources[i+1:]...)
				return
			}
		}
	}
}

// Sources lists the registered catalogues, operations first.
func Sources() []Source {
	mu.RLock()
	defer mu.RUnlock()
	return append([]Source(nil), sources...)
}

// Query is what find is asked.
type Query struct {
	// Text is the task in plain words.
	Text string
	// Limit is how many to return (default 8, at most 20).
	Limit int
	// ReadOnly leaves out what changes anything.
	ReadOnly bool
	// Kinds limits the catalogues searched (default: all).
	Kinds []string
}

// DefaultLimit is how many entries find returns unless asked otherwise.
const DefaultLimit = 8

// MaxLimit is the most find returns.
const MaxLimit = 20

// Hit is one entry find returns, with its score.
type Hit struct {
	Entry
	Score float64
}

// All lists every entry of every catalogue (a nil app: the ones that need no
// workspace).
func All(ctx context.Context, a *app.App, kinds ...string) ([]Entry, error) {
	var out []Entry
	for _, s := range Sources() {
		if len(kinds) > 0 && !contains(kinds, s.Kind()) {
			continue
		}
		es, err := s.Entries(ctx, a)
		if err != nil {
			return nil, err
		}
		out = append(out, es...)
	}
	return out, nil
}

// Find ranks the catalogue's entries for a task and returns the best.
func Find(ctx context.Context, a *app.App, q Query) ([]Hit, error) {
	if strings.TrimSpace(q.Text) == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say in a few words what you want to do, and find lists what does it.")
	}
	es, err := All(ctx, a, q.Kinds...)
	if err != nil {
		return nil, err
	}
	es = Searchable(es)
	if q.ReadOnly {
		kept := es[:0]
		for _, e := range es {
			if !e.Changes {
				kept = append(kept, e)
			}
		}
		es = kept
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	return Rank(es, q.Text, DefaultRanking, limit), nil
}

// Describe says how to use one entry, found by its exact name in every
// catalogue (an operation's first: "footage.moments"); only when none has it
// is the name taken as an operation's MCP tool name ("footage_moments"), so
// another catalogue's entry of that name is never an operation by mistake.
// Another catalogue's entry is described with the stricter of its own flags
// and its RunWith operation's.
func Describe(ctx context.Context, a *app.App, name string) (*Description, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, apperr.New(apperr.InvalidInput, "Say which one to describe: a name find returned.")
	}
	for _, s := range Sources() {
		d, err := s.Describe(ctx, a, name)
		if err == nil {
			if d.Kind != KindOperation {
				strictDescription(d)
			} else {
				annotateOp(ctx, a, d)
			}
			return d, nil
		}
		if apperr.From(err).Code != apperr.NotFound {
			return nil, err
		}
	}
	if op, ok := OpName(name); ok {
		d := DescribeOp(op)
		annotateOp(ctx, a, d)
		return d, nil
	}
	return nil, notFound(name)
}

// annotateOp adds what the first-party ability that is an operation as it
// is says about it (its title, group and what it needs here).
func annotateOp(ctx context.Context, a *app.App, d *Description) {
	fp, ok := firstPartyFor(d.Name)
	if !ok {
		return
	}
	e := fp.entry()
	if a != nil {
		here(ctx, a).fill(&e, fp)
	}
	d.Title, d.Group, d.Cost, d.Leaves, d.Requires, d.WorksHere, d.Missing, d.Partly, d.FirstParty, d.MadeWith = e.Title, e.Group, e.Cost, e.Leaves, e.Requires, e.WorksHere, e.Missing, e.Partly, e.Name, e.MadeWith
}

// Searchable is what find ranks: each entry as running it really does
// (Strict), and each operation once — a first-party ability that is an
// operation as it is (no fields fixed) is shown as that operation, which
// takes its title, group, words and what it needs (D270). With no
// operation among the entries (find limited to other kinds), the ability
// stays itself.
func Searchable(es []Entry) []Entry {
	isOp := map[string]int{}
	for i, e := range es {
		if e.Kind == KindOperation {
			isOp[e.Name] = i
		}
	}
	out := make([]Entry, 0, len(es))
	stands := map[string]Entry{}
	for _, e := range es {
		if StandsFor(e) != "" {
			if _, ok := isOp[e.RunWith]; ok {
				stands[e.RunWith] = e
				continue
			}
		}
		out = append(out, e)
	}
	for i, e := range out {
		if fp, ok := stands[e.Name]; ok {
			out[i] = Annotated(e, fp)
		}
		out[i] = Strict(out[i]) // what running each really does
	}
	return out
}

// StandsFor is the operation an entry is as it is — a first-party ability
// that runs it with nothing fixed — or "".
func StandsFor(e Entry) string {
	if e.RunOwnInput && len(e.RunInput) == 0 {
		return e.RunWith
	}
	return ""
}

// Annotated is an operation's entry with what a first-party ability that
// is that operation says about it.
func Annotated(op, fp Entry) Entry {
	op.Title, op.Group, op.Cost, op.Leaves, op.Requires = fp.Title, fp.Group, fp.Cost, fp.Leaves, fp.Requires
	op.WorksHere, op.Missing, op.Partly, op.FirstParty, op.MadeWith = fp.WorksHere, fp.Missing, fp.Partly, fp.Name, fp.MadeWith
	op.Words = append(append(append([]string(nil), op.Words...), fp.Title), fp.Words...)
	return op
}

// Strict is an entry with the stricter of its own flags and its RunWith
// operation's (an entry whose operation is missing counts as a change): what
// running it really does, whatever it says.
func Strict(e Entry) Entry {
	if e.RunWith == "" {
		return e
	}
	op, ok := ops.Get(e.RunWith)
	if !ok {
		e.Changes = true
		return e
	}
	e.Changes = e.Changes || op.Mutating
	e.PeopleOnly = e.PeopleOnly || op.HumanOnly
	e.OwnerOnly = e.OwnerOnly || op.OwnerOnly
	e.ThisComputerOnly = e.ThisComputerOnly || op.LocalOnly
	return e
}

func strictDescription(d *Description) {
	e := Strict(Entry{RunWith: d.RunWith, Changes: d.Changes, PeopleOnly: d.PeopleOnly, OwnerOnly: d.OwnerOnly, ThisComputerOnly: d.ThisComputerOnly})
	d.Changes, d.PeopleOnly, d.OwnerOnly, d.ThisComputerOnly = e.Changes, e.PeopleOnly, e.OwnerOnly, e.ThisComputerOnly
}

// RunTarget is what runs another catalogue's entry: its RunWith operation and
// that operation's input (the entry's RunInput with the caller's input under
// "input"). It is refused when the entry asks for more than its operation
// holds callers to — a person only, an owner only, this computer only, or a
// change, when the operation is none of these — since running it would then
// skip a rule the entry has.
func RunTarget(name string, raw *Description, input map[string]any) (*ops.Op, map[string]any, error) {
	if raw.RunWith == "" {
		return nil, nil, apperr.New(apperr.InvalidInput, "%s can't be run from here.", name).WithDetails(map[string]any{"name": name})
	}
	if raw.Blocked {
		// Not yet: an owner's approval, a person setting it up here (D268).
		missing := raw.Missing
		if missing == "" {
			missing = "isn't ready on this computer"
		}
		return nil, nil, apperr.New(apperr.NotConfigured, "%s can't run yet: it %s.", name, missing).
			WithDetails(map[string]any{"name": name, "reason": "entry_not_ready"})
	}
	op, ok := ops.Get(raw.RunWith)
	if !ok {
		return nil, nil, apperr.New(apperr.NotFound, "What runs %s isn't in this version of MetaRush.", name).
			WithDetails(map[string]any{"name": name, "runWith": raw.RunWith})
	}
	if raw.PeopleOnly && !op.HumanOnly {
		// A person starts it (an ability that uses a key, costs money or
		// sends media away): said so, with the command to hand to them.
		e := apperr.New(apperr.HumanOnly, "Only a person starts %s: in the dashboard or their own terminal.", name).
			WithDetails(map[string]any{"name": name, "runWith": op.Name, "reason": "entry_people_only"})
		if raw.Command != "" {
			e = e.WithCommand(raw.Command)
		}
		return nil, nil, e
	}
	var beyond []string
	if raw.OwnerOnly && !op.OwnerOnly {
		beyond = append(beyond, "for owners only")
	}
	if raw.ThisComputerOnly && !op.LocalOnly {
		beyond = append(beyond, "for this computer only")
	}
	if raw.Changes && !op.Mutating {
		beyond = append(beyond, "a change")
	}
	if len(beyond) > 0 {
		return nil, nil, apperr.New(apperr.Unauthorized, "MetaRush won't run %s: it is %s, and what would run it doesn't hold callers to that.", name, strings.Join(beyond, ", ")).
			WithDetails(map[string]any{"name": name, "runWith": op.Name, "reason": "entry_flags_beyond_operation"})
	}
	if raw.RunOwnInput {
		in, err := OwnInput(name, raw.RunInput, input)
		if err != nil {
			return nil, nil, err
		}
		return op, in, nil
	}
	return op, RunInputFor(raw.RunInput, input), nil
}

// OwnInput is a first-party ability's input for its operation: the caller's
// input with the ability's fixed fields set. A fixed field given another
// value is refused: that is another ability, or the operation itself.
func OwnInput(name string, fixed, input map[string]any) (map[string]any, error) {
	out := make(map[string]any, len(input)+len(fixed))
	for k, v := range input {
		// The operation's input is read without minding case (Go's JSON
		// matches "TICK" and "ticK", with the Kelvin sign, to tick): a key that
		// is a fixed one in another case would override it.
		for f := range fixed {
			if k != f && strings.EqualFold(k, f) {
				return nil, apperr.New(apperr.InvalidInput, "%s always runs with %s set to %s; leave %s out.", name, f, compact(fixed[f]), k).
					WithDetails(map[string]any{"name": name, "field": f, "given": k})
			}
		}
		out[k] = v
	}
	for k, v := range fixed {
		if have, ok := out[k]; ok && !sameJSON(have, v) {
			return nil, apperr.New(apperr.InvalidInput, "%s always runs with %s set to %s.", name, k, compact(v)).
				WithHint("Leave it out, or use the operation itself for another value.").WithDetails(map[string]any{"name": name, "field": k, "fixed": v})
		}
		out[k] = v
	}
	return out, nil
}

func sameJSON(a, b any) bool {
	return compact(a) == compact(b)
}

func compact(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func notFound(name string) error {
	return apperr.New(apperr.NotFound, "MetaRush has nothing by that name.").
		WithHint("Describe the task to find, and use a name it returns.").
		WithDetails(map[string]any{"name": name})
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// byScore orders hits best first; equal scores by name, so find is stable.
func byScore(hs []Hit) {
	sort.SliceStable(hs, func(i, j int) bool {
		if hs[i].Score != hs[j].Score {
			return hs[i].Score > hs[j].Score
		}
		return hs[i].Name < hs[j].Name
	})
}

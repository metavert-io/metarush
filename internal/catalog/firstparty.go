package catalog

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/firstrun"
	"github.com/metavert-io/metarush/internal/ops"
	"github.com/metavert-io/metarush/internal/providers"
	"github.com/metavert-io/metarush/internal/tools"
)

// The first-party catalogue (D269): what MetaRush already does, as about
// fifty abilities in the menu and in find. Each is a small annotation next to
// its operation (ops.Offer); firstparty.json is generated from them by
// `METARUSH_UPDATE_CATALOGUE=1 go test ./internal/catalog -run
// TestFirstPartyCatalogue` (the test fails while it is out of date), so the
// list is never written by hand and every ID it ever had stays stable.

// KindFirstParty is the first-party catalogue's kind.
const KindFirstParty = "first-party"

// FirstPartyPrefix starts a first-party ability's name ("metarush/best-moments"):
// no operation (dotted) or added ability (lowercase and dashes) is named so.
const FirstPartyPrefix = "metarush/"

//go:embed firstparty.json
var firstPartyJSON []byte

// FirstPartyAbility is one first-party ability as the generated file holds it.
type FirstPartyAbility struct {
	// ID is stable ("steady-footage"); Name is what find, describe and run
	// take ("metarush/steady-footage").
	ID   string `json:"id"`
	Name string `json:"name"`
	// Group is the part of the menu it is in.
	Group string `json:"group"`
	Title string `json:"title"`
	Does  string `json:"does"`
	// Op is the operation it runs, with Input's fields fixed.
	Op    string          `json:"op"`
	Input map[string]any  `json:"input,omitempty"`
	Words []string        `json:"words,omitempty"`
	Needs FirstPartyNeeds `json:"needs"`
	// Cost is free, priced or plan.
	Cost string `json:"cost"`
	// MadeWith is what a receipt or a run's step records for it: its name
	// (with MetaRush's version beside it when they record one).
	MadeWith string `json:"madeWith"`
}

// FirstPartyNeeds is what a first-party ability needs on a computer.
type FirstPartyNeeds struct {
	Works    []string `json:"works,omitempty"`
	OS       []string `json:"os,omitempty"`
	Partly   string   `json:"partly,omitempty"`
	Leaves   string   `json:"leaves,omitempty"`
	Programs []string `json:"programs,omitempty"`
	Keys     []string `json:"keys,omitempty"`
	Claude   bool     `json:"claude,omitempty"`
	Setup    string   `json:"setup,omitempty"`
}

// FirstPartyFile is firstparty.json's form.
type FirstPartyFile struct {
	Comment   string              `json:"_comment"`
	Abilities []FirstPartyAbility `json:"abilities"`
	// Retired are IDs taken away, each with why: never used again.
	Retired map[string]string `json:"retired,omitempty"`
}

var firstParty = sync.OnceValue(func() *FirstPartyFile {
	var f FirstPartyFile
	if err := json.Unmarshal(firstPartyJSON, &f); err != nil {
		panic("catalog: firstparty.json: " + err.Error())
	}
	return &f
})

// FirstParty lists the first-party abilities, by ID.
func FirstParty() []FirstPartyAbility { return firstParty().Abilities }

// firstPartyNamed is the ability with that name ("metarush/x") or ID ("x").
func firstPartyNamed(name string) (FirstPartyAbility, bool) {
	id := strings.TrimPrefix(strings.TrimSpace(name), FirstPartyPrefix)
	for _, fp := range FirstParty() {
		if fp.ID == id {
			return fp, true
		}
	}
	return FirstPartyAbility{}, false
}

// firstPartyFor is the first-party ability that is an operation as it is.
func firstPartyFor(op string) (FirstPartyAbility, bool) {
	for _, fp := range FirstParty() {
		if fp.Op == op && len(fp.Input) == 0 {
			return fp, true
		}
	}
	return FirstPartyAbility{}, false
}

// entry is the ability as a catalogue entry (what it needs, not yet whether
// it works here).
func (fp FirstPartyAbility) entry() Entry {
	e := Entry{Name: fp.Name, Kind: KindFirstParty, About: fp.Does, Title: fp.Title, Group: fp.Group, Cost: fp.Cost, Leaves: fp.Needs.Leaves,
		Words: append([]string{fp.Title}, fp.Words...), RunWith: fp.Op, RunInput: fp.Input, RunOwnInput: true, MadeWith: fp.MadeWith,
		Requires: fp.requires()}
	if c, ok := CommandFor(fp.Op); ok {
		e.Command = c.Command
	}
	if op, ok := ops.Get(fp.Op); ok {
		e.Changes, e.PeopleOnly, e.OwnerOnly, e.ThisComputerOnly = op.Mutating, op.HumanOnly, op.OwnerOnly, op.LocalOnly
	} else {
		e.Changes = true
	}
	return e
}

// requires says what it needs, a few words each.
func (fp FirstPartyAbility) requires() []string {
	var out []string
	n := fp.Needs
	if len(n.OS) > 0 {
		if n.Partly != "" {
			out = append(out, osWords(n.OS)+" (elsewhere, "+n.Partly+")")
		} else {
			out = append(out, osWords(n.OS))
		}
	}
	for _, k := range n.Works {
		if w, ok := worksWords[k]; ok {
			out = append(out, w)
		}
	}
	for _, p := range n.Programs {
		out = append(out, programWords(p))
	}
	if len(n.Keys) > 0 {
		out = append(out, keyWords(n.Keys))
	}
	if n.Claude {
		out = append(out, "Claude Code")
	}
	if n.Setup != "" {
		out = append(out, setupWords(n.Setup))
	}
	return out
}

// worksWords says what each of the first run's items needs (the words of
// firstrun's Needs when it is missing).
var worksWords = map[string]string{
	"transcripts": "the transcriber", "editing": "FFmpeg", "renders": "Node.js and FFmpeg", "checks": "FFmpeg",
	"scratch-voice": "a system voice",
}

func osWords(os []string) string {
	var ws []string
	for _, o := range os {
		switch o {
		case "mac":
			ws = append(ws, "a Mac")
		case "linux":
			ws = append(ws, "Linux")
		default:
			ws = append(ws, o)
		}
	}
	return strings.Join(ws, " or ")
}

func programWords(p string) string {
	switch p {
	case tools.GH:
		return "GitHub's gh"
	case tools.YtDlp:
		return "yt-dlp"
	}
	return p
}

func keyWords(keys []string) string {
	var ts []string
	for _, k := range keys {
		ts = append(ts, keyTitle(k))
	}
	if len(ts) == 0 {
		return ""
	}
	return article(ts[0]) + " " + strings.Join(ts, " or ") + " key"
}

func keyTitle(name string) string {
	if p, err := providers.Get(name); err == nil {
		return p.Title()
	}
	return name
}

func setupWords(name string) string {
	if w, ok := setupNames[name]; ok {
		return w
	}
	return name
}

// setupNames say what each setup check is about.
var setupNames = map[string]string{"studio-voices": "the studio voice cleaner, set up once by a person",
	"on-screen-helper": "the helper that looks at the picture, which MetaRush builds"}

func article(s string) string {
	if s != "" && strings.ContainsRune("AEIOUaeiou", rune(s[0])) {
		return "an"
	}
	return "a"
}

// hereFacts is what this computer has, read once per listing.
type hereFacts struct {
	a      *app.App
	ctx    context.Context
	os     string
	works  map[string]firstrun.WorksItem
	keys   map[string]bool
	claude bool
}

// here reads this computer's state: the first run's items (D256), its
// system, programs and keys. It changes nothing.
func here(ctx context.Context, a *app.App) *hereFacts {
	h := &hereFacts{a: a, ctx: ctx, os: thisOS(), works: firstrun.Works(a.Cfg, a.Tools), keys: map[string]bool{}}
	h.claude = a.Tools.Has(tools.Claude)
	return h
}

func thisOS() string {
	switch runtime.GOOS {
	case "darwin":
		return "mac"
	}
	return runtime.GOOS
}

func (h *hereFacts) hasKey(name string) bool {
	if v, ok := h.keys[name]; ok {
		return v
	}
	k, _, err := providers.Key(h.a.Cfg, name)
	h.keys[name] = err == nil && k != ""
	return h.keys[name]
}

// fill says whether the ability works here and, if not, what it needs first.
func (h *hereFacts) fill(e *Entry, fp FirstPartyAbility) {
	n := fp.Needs
	var missing []string
	e.Partly = ""
	otherOS := len(n.OS) > 0 && !contains(n.OS, h.os)
	if otherOS {
		if n.Partly != "" {
			e.Partly = n.Partly // it works here, in part
		} else {
			missing = append(missing, osWords(n.OS))
		}
	}
	for _, k := range n.Works {
		if w, ok := h.works[k]; ok && !w.Ready {
			needs := w.Needs
			if needs == "" {
				needs = worksWords[k]
			}
			missing = append(missing, needs)
		}
	}
	for _, p := range n.Programs {
		if !h.a.Tools.Has(p) {
			missing = append(missing, programWords(p))
		}
	}
	if len(n.Keys) > 0 {
		any := false
		for _, k := range n.Keys {
			if h.hasKey(k) {
				any = true
				break
			}
		}
		if !any {
			missing = append(missing, keyWords(n.Keys))
		}
	}
	if n.Claude && !h.claude {
		missing = append(missing, "Claude Code")
	}
	if n.Setup != "" && !otherOS && h.a != nil {
		// A setup is for the systems it runs on: elsewhere the system says it.
		if fn, ok := ops.ReadyCheckFor(n.Setup); ok {
			if ok, needs := fn(h.ctx, h.a); !ok {
				if needs == "" {
					needs = setupWords(n.Setup)
				}
				missing = append(missing, needs)
			}
		}
	}
	if len(missing) == 0 {
		e.WorksHere, e.Missing = Yes(), ""
		return
	}
	e.WorksHere, e.Missing, e.Partly = No(), "needs "+joinAnd(dedupe(missing)), ""
}

func dedupe(xs []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, x := range xs {
		if !seen[x] {
			seen[x] = true
			out = append(out, x)
		}
	}
	return out
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

// FirstPartySource is the first-party catalogue.
type FirstPartySource struct{}

func init() { _ = Register(FirstPartySource{}) }

// Kind is "first-party".
func (FirstPartySource) Kind() string { return KindFirstParty }

// Entries lists every first-party ability; with a workspace open, each says
// whether it works on this computer now.
func (FirstPartySource) Entries(ctx context.Context, a *app.App) ([]Entry, error) {
	var h *hereFacts
	if a != nil {
		h = here(ctx, a)
	}
	out := make([]Entry, 0, len(FirstParty()))
	for _, fp := range FirstParty() {
		e := fp.entry()
		if h != nil {
			h.fill(&e, fp)
		}
		out = append(out, e)
	}
	return out, nil
}

// Describe says how to use one: its operation's input without the fields it
// fixes, what it returns, its command, what it needs and whether it works
// here.
func (FirstPartySource) Describe(ctx context.Context, a *app.App, name string) (*Description, error) {
	if !strings.HasPrefix(strings.TrimSpace(name), FirstPartyPrefix) {
		return nil, apperr.New(apperr.NotFound, "No first-party ability by that name.")
	}
	fp, ok := firstPartyNamed(name)
	if !ok {
		return nil, apperr.New(apperr.NotFound, "MetaRush has no ability called %s.", name).
			WithHint("The menu of abilities lists them.").WithCommand("metarush catalogue list").WithDetails(map[string]any{"name": name})
	}
	return DescribeFirstParty(ctx, a, fp), nil
}

// DescribeFirstParty describes a first-party ability (a nil app: without
// whether it works here).
func DescribeFirstParty(ctx context.Context, a *app.App, fp FirstPartyAbility) *Description {
	e := fp.entry()
	if a != nil {
		here(ctx, a).fill(&e, fp)
	}
	d := &Description{Name: fp.Name, Kind: KindFirstParty, About: fp.Does, Changes: e.Changes, PeopleOnly: e.PeopleOnly, OwnerOnly: e.OwnerOnly,
		ThisComputerOnly: e.ThisComputerOnly, RunWith: fp.Op, RunInput: fp.Input, RunOwnInput: true, Title: fp.Title, Group: fp.Group,
		Cost: fp.Cost, Leaves: e.Leaves, Requires: e.Requires, WorksHere: e.WorksHere, Missing: e.Missing, Partly: e.Partly, FirstParty: fp.Name, MadeWith: fp.MadeWith,
		Input: map[string]any{"type": "object", "properties": map[string]any{}}, Returns: []ReturnField{}}
	if op, ok := ops.Get(fp.Op); ok {
		d.OwnCheck = ChecksFirst(op)
		d.Input = withoutFields(InputSchema(op.InType), fp.Input)
		d.Returns = Returns(op.OutType)
	}
	if c, ok := CommandFor(fp.Op); ok {
		d.Command, d.Usage = c.Command, c.Usage
	}
	d.Example = Example(d.Input)
	if len(fp.Input) > 0 {
		d.Note = "It runs " + fp.Op + " with " + fixedWords(fp.Input) + "."
	}
	return d
}

// withoutFields is a schema without the fields an ability fixes.
func withoutFields(schema map[string]any, fixed map[string]any) map[string]any {
	if len(fixed) == 0 {
		return schema
	}
	if props, ok := schema["properties"].(map[string]any); ok {
		kept := map[string]any{}
		for k, v := range props {
			if _, f := fixed[k]; !f {
				kept[k] = v
			}
		}
		schema["properties"] = kept
	}
	if req, ok := schema["required"].([]any); ok {
		var kept []any
		for _, r := range req {
			if s, _ := r.(string); s != "" {
				if _, f := fixed[s]; f {
					continue
				}
			}
			kept = append(kept, r)
		}
		if len(kept) == 0 {
			delete(schema, "required")
		} else {
			schema["required"] = kept
		}
	}
	return schema
}

func fixedWords(fixed map[string]any) string {
	keys := make([]string, 0, len(fixed))
	for k := range fixed {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var ws []string
	for _, k := range keys {
		ws = append(ws, k+" "+compact(fixed[k]))
	}
	return strings.Join(ws, ", ")
}

// ---- generating firstparty.json ----

// FirstPartyFromOfferings builds firstparty.json from the operations'
// annotations, refusing what can't be right: an unknown group, operation,
// field, program, key or check, a missing title or sentence, or an ID the
// file had that is neither offered nor retired (IDs are stable: receipts and
// saved runs name them).
func FirstPartyFromOfferings(offered []ops.OpOffering, retired map[string]string, before *FirstPartyFile) ([]byte, error) {
	var problems []string
	f := FirstPartyFile{Comment: "The first-party catalogue: what MetaRush already does, as abilities in the menu and in find. Generated from each operation's ops.Offer annotation by METARUSH_UPDATE_CATALOGUE=1 go test ./internal/catalog -run TestFirstPartyCatalogue; never edit by hand. IDs are stable: one taken away moves to retired and is never used again.",
		Abilities: []FirstPartyAbility{}, Retired: map[string]string{}}
	have := map[string]bool{}
	for _, o := range offered {
		have[o.ID] = true
		op, ok := ops.Get(o.Op)
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s: no operation %s", o.ID, o.Op))
			continue
		case GroupTitle(o.Group) == "" || o.Group == GroupAdded:
			problems = append(problems, fmt.Sprintf("%s: no group %q", o.ID, o.Group))
		case strings.TrimSpace(o.Title) == "" || strings.TrimSpace(o.Does) == "":
			problems = append(problems, o.ID+": a title and a sentence saying what it does, please")
		}
		if why, gone := retired[o.ID]; gone {
			problems = append(problems, fmt.Sprintf("%s was retired (%s); give it a new ID", o.ID, why))
		}
		for k := range o.Input {
			if !HasField(op.InType, k) {
				problems = append(problems, fmt.Sprintf("%s: %s has no field %s", o.ID, o.Op, k))
			}
		}
		n := o.Needs
		for _, w := range n.Works {
			if worksWords[w] == "" {
				problems = append(problems, fmt.Sprintf("%s: the first run says nothing called %q", o.ID, w))
			}
		}
		if contains(n.Works, "transcripts") && !contains(n.Works, "editing") {
			problems = append(problems, o.ID+": transcripts need FFmpeg too (works: transcripts and editing)")
		}
		if n.Partly != "" && len(n.OS) == 0 {
			problems = append(problems, o.ID+": partly says what it does where its systems leave it out, so name its systems")
		}
		if (len(n.Keys) > 0 || n.Claude) && strings.TrimSpace(n.Leaves) == "" {
			problems = append(problems, o.ID+": it uses a key or Claude Code, so say what leaves this computer (needs.Leaves)")
		}
		for _, s := range n.OS {
			if s != "mac" && s != "linux" {
				problems = append(problems, fmt.Sprintf("%s: no system %q (mac, linux)", o.ID, s))
			}
		}
		for _, p := range n.Programs {
			if !tools.Allowed(p) {
				problems = append(problems, fmt.Sprintf("%s: %s isn't a program MetaRush runs", o.ID, p))
			}
		}
		for _, k := range n.Keys {
			if _, err := providers.Get(k); err != nil {
				problems = append(problems, fmt.Sprintf("%s: no integration %q", o.ID, k))
			}
		}
		if n.Setup != "" {
			if _, ok := ops.ReadyCheckFor(n.Setup); !ok {
				problems = append(problems, fmt.Sprintf("%s: no check %q is registered", o.ID, n.Setup))
			}
		}
		cost := CostFree
		switch {
		case n.Priced:
			cost = CostPriced
		case len(n.Keys) > 0:
			cost = CostKeyed // a key to a service: it may charge, whatever else is said
		case n.Claude:
			cost = CostPlan
		}
		name := FirstPartyPrefix + o.ID
		f.Abilities = append(f.Abilities, FirstPartyAbility{ID: o.ID, Name: name, Group: o.Group, Title: strings.TrimSpace(o.Title), Does: strings.TrimSpace(o.Does),
			Op: o.Op, Input: o.Input, Words: o.Words, Cost: cost, MadeWith: name,
			Needs: FirstPartyNeeds{Works: n.Works, OS: n.OS, Partly: n.Partly, Leaves: strings.TrimSpace(n.Leaves), Programs: n.Programs, Keys: n.Keys, Claude: n.Claude, Setup: n.Setup}})
	}
	for id, why := range retired {
		f.Retired[id] = why
	}
	if before != nil {
		for _, a := range before.Abilities {
			if !have[a.ID] && retired[a.ID] == "" {
				problems = append(problems, fmt.Sprintf("%s was in the catalogue and isn't offered any more: list it in catalog.RetiredFirstParty with why (IDs are never reused)", a.ID))
			}
		}
		for id := range before.Retired {
			if retired[id] == "" {
				problems = append(problems, fmt.Sprintf("%s was retired; keep it in catalog.RetiredFirstParty", id))
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		return nil, fmt.Errorf("the first-party catalogue has problems:\n  %s", strings.Join(problems, "\n  "))
	}
	if len(f.Retired) == 0 {
		f.Retired = nil
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// RetiredFirstParty are first-party IDs taken away, each with why. Never
// remove one: receipts and saved runs may still name it.
var RetiredFirstParty = map[string]string{}

// IsFirstPartyID says whether an ID is, or was, a first-party ability's: an
// ability a workspace adds can't take one (D268), so a name always means one
// thing in find, describe, run and the menu.
func IsFirstPartyID(id string) bool {
	if _, ok := firstPartyNamed(id); ok && !strings.Contains(id, "/") {
		return true
	}
	if _, ok := firstParty().Retired[id]; ok {
		return true
	}
	_, ok := RetiredFirstParty[id]
	return ok
}

// FirstPartyFileNow is the embedded file (for the generator's test).
func FirstPartyFileNow() *FirstPartyFile { return firstParty() }

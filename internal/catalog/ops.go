package catalog

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/ops"
)

// FindIn asks find for what does a task.
type FindIn struct {
	Task string `json:"task" jsonschema:"description=The task in plain words: what the person wants done (\"make a review copy\"\\, \"who is speaking in this clip\")."`
	// Limit is how many to return (8 unless asked, at most 20).
	Limit int `json:"limit,omitempty" jsonschema:"description=How many to return: 8 unless asked\\, at most 20."`
	// ReadOnly leaves out what changes anything.
	ReadOnly bool `json:"readOnly,omitempty" jsonschema:"description=Only what changes nothing."`
	// Kinds limits the catalogues searched (default: all).
	Kinds []string `json:"kinds,omitempty" jsonschema:"description=Which catalogues to search (operation\\, first-party\\, ability); all when empty."`
}

// FindOut is what find found, best first.
type FindOut struct {
	Task string    `json:"task"`
	Hits []FindHit `json:"hits"`
}

// FindHit is one thing find found.
type FindHit struct {
	// Name is what describe and run take ("footage.moments").
	Name string `json:"name"`
	Kind string `json:"kind"`
	// About is what it does.
	About string `json:"about"`
	// Command is the command line that does it.
	Command          string  `json:"command,omitempty"`
	Changes          bool    `json:"changes"`
	PeopleOnly       bool    `json:"peopleOnly"`
	OwnerOnly        bool    `json:"ownerOnly"`
	ThisComputerOnly bool    `json:"thisComputerOnly"`
	Score            float64 `json:"score"`
	// Line is the hit in one line: its name, what it does and whether it
	// changes anything or is a person's to do, and what it needs here.
	Line string `json:"line"`
	// What it needs and whether it works here (D270): see Description.
	Group     string `json:"group,omitempty"`
	Cost      string `json:"cost,omitempty" jsonschema:"enum=,enum=free,enum=priced,enum=keyed,enum=plan"`
	Leaves    string `json:"leaves,omitempty"`
	WorksHere *bool  `json:"worksHere,omitempty"`
	Missing   string `json:"missing,omitempty"`
	Partly    string `json:"partly,omitempty"`
	Blocked   bool   `json:"blocked,omitempty"`
	// FirstParty names the first-party ability an operation is.
	FirstParty string `json:"firstParty,omitempty"`
}

// DescribeEntryIn asks how to use one entry.
type DescribeEntryIn struct {
	Name string `json:"name" jsonschema:"description=A name find returned (\"footage.moments\"; an MCP tool name such as \"footage_moments\" works too)."`
}

func init() {
	ops.Def("ops.find", "Find what does a task, from words describing it: MetaRush's operations (and any catalogue that joins find), the best few first, one line each; nothing changes", ops.Flags{},
		func(ctx context.Context, a *app.App, in *FindIn) (*FindOut, error) {
			hs, err := Find(ctx, a, Query{Text: in.Task, Limit: in.Limit, ReadOnly: in.ReadOnly, Kinds: in.Kinds})
			if err != nil {
				return nil, err
			}
			out := &FindOut{Task: strings.TrimSpace(in.Task), Hits: make([]FindHit, 0, len(hs))}
			for _, h := range hs {
				out.Hits = append(out.Hits, FindHit{Name: h.Name, Kind: h.Kind, About: h.About, Command: h.Command, Changes: h.Changes,
					PeopleOnly: h.PeopleOnly, OwnerOnly: h.OwnerOnly, ThisComputerOnly: h.ThisComputerOnly, Score: h.Score, Line: Line(h.Entry),
					Group: h.Group, Cost: h.Cost, Leaves: h.Leaves, WorksHere: h.WorksHere, Missing: h.Missing, Partly: h.Partly, Blocked: h.Blocked, FirstParty: h.FirstParty})
			}
			return out, nil
		})
	ops.Def("ops.describe", "Say how to use one operation (or another catalogue's entry): what it does, its input with an example, what it returns, its command and whether it changes anything or is people only", ops.Flags{},
		func(ctx context.Context, a *app.App, in *DescribeEntryIn) (*Description, error) {
			return Describe(ctx, a, in.Name)
		})
}

// LineAbout is how long a hit's line says what it does, at most.
const LineAbout = 120

// Line is an entry in one line: its name, what it does (Short) and what it
// needs.
func Line(e Entry) string {
	return fmt.Sprintf("%s — %s%s", e.Name, Short(e.About), Needs(e))
}

// Short keeps what something does to one line: at most LineAbout letters,
// cut at a word.
func Short(about string) string {
	if utf8.RuneCountInString(about) <= LineAbout {
		return about
	}
	cut := string([]rune(about)[:LineAbout])
	if i := strings.LastIndexAny(cut, " ,;:("); i > LineAbout/2 {
		cut = cut[:i]
	}
	return strings.TrimRight(cut, " ,;:(—-") + "…"
}

// Needs says in a few words what running an entry needs (empty when it
// only reads and needs nothing here): who may run it, whether it is priced
// or uses the person's Claude plan, what leaves this computer, and what it
// needs on this computer first.
func Needs(e Entry) string {
	var ws []string
	switch {
	case e.PeopleOnly:
		ws = append(ws, "a person's to do")
	case e.Changes:
		ws = append(ws, "changes something")
	}
	if e.OwnerOnly {
		ws = append(ws, "owners")
	}
	if e.ThisComputerOnly {
		ws = append(ws, "this computer only")
	}
	switch e.Cost {
	case CostPriced:
		ws = append(ws, "priced first")
	case CostKeyed:
		ws = append(ws, "the service may charge")
	case CostPlan:
		ws = append(ws, "uses your Claude plan")
	}
	if e.Leaves != "" && !strings.HasPrefix(strings.ToLower(e.Leaves), "nothing") {
		ws = append(ws, strings.ToLower(e.Leaves[:1])+strings.TrimSuffix(e.Leaves[1:], "."))
	}
	if e.Missing != "" {
		ws = append(ws, e.Missing)
	} else if e.Partly != "" {
		ws = append(ws, "here "+e.Partly)
	}
	if len(ws) == 0 {
		return ""
	}
	return " (" + strings.Join(ws, ", ") + ")"
}

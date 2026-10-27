package catalog

import (
	"context"
	_ "embed"
	"encoding/json"
	"strings"
	"sync"

	"github.com/metavert-io/metarush/internal/app"
	"github.com/metavert-io/metarush/internal/apperr"
	"github.com/metavert-io/metarush/internal/ops"
)

// commandsJSON is each operation's command-line command, generated from the
// CLI by `go test ./internal/cli -run TestCommandIndex` (with
// METARUSH_UPDATE_COMMANDS=1 it rewrites the file; the test fails while it is
// out of date).
//
//go:embed commands.json
var commandsJSON []byte

// CLICommand is an operation's command-line command.
type CLICommand struct {
	// Command is the command ("metarush footage moments").
	Command string `json:"command"`
	// Usage is the command with its arguments ("metarush footage moments <project>").
	Usage string `json:"usage,omitempty"`
	// Help is the command's help line.
	Help string `json:"help,omitempty"`
}

// CommandIndex is the generated file's form.
type CommandIndex struct {
	Comment  string                `json:"_comment"`
	Commands map[string]CLICommand `json:"commands"`
}

var commandIndex = sync.OnceValue(func() map[string]CLICommand {
	var ix CommandIndex
	if err := json.Unmarshal(commandsJSON, &ix); err != nil {
		panic("catalog: commands.json: " + err.Error())
	}
	return ix.Commands
})

// CommandFor is the command line that runs an operation, when it has one.
func CommandFor(op string) (CLICommand, bool) {
	c, ok := commandIndex()[op]
	return c, ok
}

// Operations is the catalogue of the ops registry.
type Operations struct{}

func init() { _ = Register(Operations{}) }

// Kind is "operation".
func (Operations) Kind() string { return KindOperation }

// Entries lists every registered operation, people-only ones included (find
// says they are a person's to do).
func (Operations) Entries(context.Context, *app.App) ([]Entry, error) {
	all := ops.All()
	out := make([]Entry, 0, len(all))
	for _, op := range all {
		out = append(out, OperationEntry(op))
	}
	return out, nil
}

// OperationEntry is an operation as a catalogue entry.
func OperationEntry(op *ops.Op) Entry {
	e := Entry{Name: op.Name, Kind: KindOperation, About: strings.TrimSuffix(strings.TrimSpace(op.Summary), "."),
		Changes: op.Mutating, PeopleOnly: op.HumanOnly, OwnerOnly: op.OwnerOnly, ThisComputerOnly: op.LocalOnly,
		Words: opWords[op.Name]}
	if c, ok := CommandFor(op.Name); ok {
		e.Command, e.CommandAbout = c.Command, c.Help
	}
	return e
}

// OpName is the operation a name means: an operation's name, or its MCP
// tool name (dots as underscores: "footage_moments").
func OpName(name string) (*ops.Op, bool) {
	name = strings.TrimSpace(name)
	if op, ok := ops.Get(name); ok {
		return op, true
	}
	return ops.Get(strings.ReplaceAll(name, "_", "."))
}

// Describe says how to use an operation, by its exact name (catalog.Describe
// tries an operation's MCP tool name only after every catalogue's own names).
func (Operations) Describe(_ context.Context, _ *app.App, name string) (*Description, error) {
	op, ok := ops.Get(strings.TrimSpace(name))
	if !ok {
		return nil, apperr.New(apperr.NotFound, "MetaRush has no operation by that name.").WithDetails(map[string]any{"name": name})
	}
	return DescribeOp(op), nil
}

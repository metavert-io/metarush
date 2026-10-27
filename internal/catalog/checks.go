package catalog

import "github.com/metavert-io/metarush/internal/ops"

// checksFirst are the changing operations that check themselves first: called
// without the confirm token their own check or dry run gave, they only check
// and save nothing (every change goes through the edit plan's token check).
// The list is explicit, not read from the input's fields: brand.set has a
// confirmToken for the cut it may change, yet saves the kit at once, and
// project.push stores a folder's graphics before it checks. Every other
// changing operation is confirmed by the caller's surface (MCP's two steps).
var checksFirst = map[string]bool{
	"edit.apply": true, "cut.apply": true, "assistant.apply": true,
	"cut.add": true, "cut.move": true, "cut.trim": true, "cut.split": true, "cut.delete": true, "cut.deleteRange": true,
	"cut.removeWords": true, "cut.transition": true, "cut.framing": true, "cut.flashes": true, "cut.names": true,
	"cut.nest": true, "cut.copyIn": true, "cut.update": true, "cut.useFixed": true,
	"brand.apply": true, "content.style.apply": true,
}

// ChecksFirst says whether a changing operation checks itself first (see
// checksFirst); one that only reads needs no check.
func ChecksFirst(op *ops.Op) bool {
	return op.Mutating && checksFirst[op.Name] && HasField(op.InType, "confirmToken")
}

// ChecksFirstNames lists them (for tests and documentation).
func ChecksFirstNames() []string {
	out := make([]string, 0, len(checksFirst))
	for n := range checksFirst {
		out = append(out, n)
	}
	return out
}

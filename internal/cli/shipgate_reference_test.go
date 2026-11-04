package cli

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/metavert-io/metarush/internal/models"
	"github.com/metavert-io/metarush/internal/ops"
)

// With the ship gate off a reference still holds a version (D441, D459):
// `deliver ready` says what holds it, how to say the video is yours and how
// to send it out anyway — never that nothing is held — and with --details the
// command stays with the hold.
func TestDeliverReadySaysWhatAReferenceHoldsWithTheGateOff(t *testing.T) {
	g := &ops.ShipGateOut{ProjectID: "prj_teaser", Project: "Teaser", Number: 3, Headline: "Not ready yet",
		Why: "The quality check and sign-offs only remind: an owner turned holding versions off. Even so, a reference holds this version: someone else's video goes out only when a person says it's theirs, or sends it out anyway with a note.",
		Holds: []ops.GateHold{{ShipHold: models.ShipHold{Key: "reference:ast_theirs@rev_3", Kind: models.HoldReference,
			Says: "Version 3 plays “Their hook”, a reference: someone else's video, kept to learn from. If it's your own video, say so; otherwise take it out and make a new version."},
			Overridable: true, Holding: true,
			Paths: []ops.GatePath{{Label: "It's mine", Op: "footage.reference.mine", Command: "metarush footage reference ast_theirs --mine", People: true}}}}}
	cmd := func(details bool) *cobra.Command {
		c := &cobra.Command{}
		c.Flags().Bool("details", details, "")
		return c
	}
	out := stdout(t, func() { printGate(cmd(false), g) })
	for _, want := range []string{
		"Version 3 of “Teaser”: Not ready yet.",
		"Version 3 plays “Their hook”, a reference",
		"Even so, a reference holds this version",
		"If it's your own video, say so (a person): metarush footage reference ast_theirs --mine",
		"Take out what holds it and make a new version, or send it out anyway with a note (a person): metarush deliver override prj_teaser --version r3 --note",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("deliver ready lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "nothing is held") {
		t.Errorf("says nothing is held:\n%s", out)
	}
	// --details: the command under the hold, once.
	detailed := stdout(t, func() { printGate(cmd(true), g) })
	if n := strings.Count(detailed, "metarush footage reference ast_theirs --mine"); n != 1 || !strings.Contains(detailed, "It's mine (a person): metarush footage reference ast_theirs --mine") {
		t.Errorf("--details shows the command %d times:\n%s", n, detailed)
	}
	// Sent out anyway with a note: nothing holds it, no lines about it.
	g.Holds[0].Covered, g.Holds[0].Holding = true, false
	g.Why, g.Headline = "The quality check and sign-offs only remind: an owner turned holding versions off.", "Ready to deliver"
	if out := stdout(t, func() { printGate(cmd(false), g) }); strings.Contains(out, "send it out anyway") || strings.Contains(out, "say so (a person)") {
		t.Errorf("covered: %s", out)
	}
}

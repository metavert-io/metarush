package apperr

import (
	"encoding/json"
	"strings"
	"testing"
)

// A command that fixes a problem stays behind Details, whatever order the
// details and the command are added in; the JSON shape is unchanged.
func TestWithCommandKeepsTheCommandInDetails(t *testing.T) {
	e := New(NotFound, "There's no quality check for that render yet.").WithHint("Check it first.").WithCommand("metarush qa run <render> --wait")
	if e.Command() != "metarush qa run <render> --wait" {
		t.Fatalf("command %q", e.Command())
	}
	e.WithDetails(map[string]any{"render": "rnd_x"})
	if e.Command() == "" {
		t.Fatal("WithDetails after WithCommand dropped the command")
	}
	b, _ := json.Marshal(e)
	var back struct {
		Code, Message, Hint string
		Details             map[string]string
	}
	if err := json.Unmarshal(b, &back); err != nil || back.Code != NotFound || back.Details["command"] != "metarush qa run <render> --wait" || back.Details["render"] != "rnd_x" ||
		strings.Contains(back.Message+back.Hint, "metarush") {
		t.Fatalf("json %s (%v)", b, err)
	}
	other := New(Validation, "x").WithDetails([]string{"a"}).WithCommand("metarush y")
	if other.Command() != "" {
		t.Fatalf("non-map details got a command: %v", other.Details)
	}
}

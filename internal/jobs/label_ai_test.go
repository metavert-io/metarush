package jobs

import "testing"

// A generate job is any kind of AI work (D226); its label reads after "a".
func TestGenerateJobLabel(t *testing.T) {
	if got := Label(TypeGenerate); got != "piece of AI work" {
		t.Errorf("label %q", got)
	}
}

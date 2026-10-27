package cli

import (
	"strings"
	"testing"

	"github.com/metavert-io/metarush/internal/aiservices"
)

// `metarush ai models --kind` names every kind of AI work.
func TestAIModelsKindHelpNamesEveryKind(t *testing.T) {
	help := aiModelsCmd().Flags().Lookup("kind").Usage
	for _, k := range aiservices.Kinds() {
		if !strings.Contains(help, k.Kind) {
			t.Errorf("--kind doesn't name %s: %q", k.Kind, help)
		}
	}
}

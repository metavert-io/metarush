package plainwords

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// The article before a number follows how the number is said ("an 8-second
// shot", "a 6-second shot"), with the same cases as the dashboard's rule
// (web/scripts/article-test.mjs).
func TestAOrAnFollowsHowTheNumberIsSaid(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "articles.json"))
	if err != nil {
		t.Fatal(err)
	}
	var cases struct {
		An []int `json:"an"`
		A  []int `json:"a"`
	}
	if err := json.Unmarshal(b, &cases); err != nil {
		t.Fatal(err)
	}
	if len(cases.An) == 0 || len(cases.A) == 0 {
		t.Fatalf("no cases: %+v", cases)
	}
	for want, ns := range map[string][]int{"an": cases.An, "a": cases.A} {
		for _, n := range ns {
			if got := AOrAn(n); got != want {
				t.Errorf("AOrAn(%d) = %q, want %q", n, got, want)
			}
		}
	}
}

package plainwords

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Hidden characters come out by one rule on every surface: the cases are
// shared with the dashboard's (web/scripts/hidden-test.mjs).
func TestCleanTakesHiddenCharactersOut(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "hidden.json"))
	if err != nil {
		t.Fatal(err)
	}
	var data struct {
		Cases []struct {
			In      string `json:"in"`
			Out     string `json:"out"`
			Removed int    `json:"removed"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(b, &data); err != nil {
		t.Fatal(err)
	}
	if len(data.Cases) < 20 {
		t.Fatalf("only %d cases", len(data.Cases))
	}
	for _, c := range data.Cases {
		got, n := CleanCount(c.In)
		if got != c.Out || n != c.Removed {
			t.Errorf("CleanCount(%+q) = %+q, %d; want %+q, %d", c.In, got, n, c.Out, c.Removed)
		}
		if Clean(c.In) != c.Out {
			t.Errorf("Clean(%+q) differs from CleanCount", c.In)
		}
		if HasHidden(c.In) != (c.Removed > 0) {
			t.Errorf("HasHidden(%+q) = %v", c.In, HasHidden(c.In))
		}
		if again, n := CleanCount(got); again != got || n != 0 {
			t.Errorf("cleaning %+q twice changed it again: %+q, %d", c.In, again, n)
		}
	}
}

// Bytes that aren't text (JSON can't carry them, so they are here only): a
// lone 0x9B is a terminal's one-byte escape on some terminals.
func TestCleanTakesBytesThatArentTextOut(t *testing.T) {
	for in, want := range map[string]string{
		"a\x9b31mb":            "a31mb",
		"\xff\xfe\xfdtext":     "text",
		"ok\xe2\x80":           "ok",
		"\xc0\xafslash":        "slash",
		"keep \xef\xbf\xbd it": "keep � it",
	} {
		got, n := CleanCount(in)
		if got != want {
			t.Errorf("CleanCount(%+q) = %+q, want %+q", in, got, want)
		}
		if (n > 0) != (got != in) {
			t.Errorf("CleanCount(%+q) counted %d", in, n)
		}
	}
	// Plain text is handed back as it is, without a copy.
	s := strings.Repeat("plain ", 100)
	if Clean(s) != s || HasHidden(s) {
		t.Error("plain text changed")
	}
}

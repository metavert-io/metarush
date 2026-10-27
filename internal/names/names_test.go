package names

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

type table struct {
	People []string `json:"people"`
	Rank   []struct {
		Query string   `json:"query"`
		Names []string `json:"names"`
		Kinds []string `json:"kinds"`
	} `json:"rank"`
	Resolve []struct {
		Query     string   `json:"query"`
		Found     string   `json:"found"`
		Ambiguous []string `json:"ambiguous"`
		Suggest   []string `json:"suggest"`
	} `json:"resolve"`
	Close []struct {
		Query string   `json:"query"`
		Names []string `json:"names"`
	} `json:"close"`
}

func load(t *testing.T) (table, []Candidate) {
	t.Helper()
	b, err := os.ReadFile("testdata/cases.json")
	if err != nil {
		t.Fatal(err)
	}
	var tb table
	if err := json.Unmarshal(b, &tb); err != nil {
		t.Fatal(err)
	}
	var cs []Candidate
	for _, p := range tb.People {
		cs = append(cs, Candidate{ID: p, Name: p})
	}
	return tb, cs
}

func namesOf(cs []Candidate) []string {
	out := []string{}
	for _, c := range cs {
		out = append(out, c.Name)
	}
	return out
}

// The same table drives web/src/lib/names.ts (web/scripts/names-test.mjs).
func TestSharedNameMatchingTable(t *testing.T) {
	tb, cs := load(t)
	for _, c := range tb.Rank {
		hits := Rank(c.Query, cs)
		got, kinds := []string{}, []string{}
		for _, h := range hits {
			got = append(got, h.Name)
			kinds = append(kinds, h.Kind.String())
		}
		if !reflect.DeepEqual(got, append([]string{}, c.Names...)) || !reflect.DeepEqual(kinds, append([]string{}, c.Kinds...)) {
			t.Errorf("rank %q: got %v %v, want %v %v", c.Query, got, kinds, c.Names, c.Kinds)
		}
	}
	for _, c := range tb.Resolve {
		r := Resolve(c.Query, cs)
		switch {
		case c.Found != "":
			if r.Found == nil || r.Found.Name != c.Found {
				t.Errorf("resolve %q: got %+v, want %q", c.Query, r, c.Found)
			}
		case c.Ambiguous != nil:
			if r.Found != nil || !reflect.DeepEqual(namesOf(r.Ambiguous), c.Ambiguous) {
				t.Errorf("resolve %q: got %+v, want ambiguous %v", c.Query, r, c.Ambiguous)
			}
		default:
			if r.Found != nil || len(r.Ambiguous) > 0 || !reflect.DeepEqual(namesOf(r.Suggest), append([]string{}, c.Suggest...)) {
				t.Errorf("resolve %q: got %+v, want suggestions %v", c.Query, r, c.Suggest)
			}
		}
	}
	for _, c := range tb.Close {
		if got := namesOf(Close(c.Query, cs)); !reflect.DeepEqual(got, append([]string{}, c.Names...)) {
			t.Errorf("close %q: got %v, want %v", c.Query, got, c.Names)
		}
	}
}

func TestFoldAndDistance(t *testing.T) {
	for in, want := range map[string]string{"  Zoë   Ibáñez ": "zoe ibanez", "Jean-Luc": "jean luc", "O'Brien": "obrien", "ÅSA": "asa"} {
		if got := Fold(in); got != want {
			t.Errorf("Fold(%q) = %q, want %q", in, got, want)
		}
	}
	for _, c := range []struct {
		a, b string
		d    int
	}{{"mya", "maya", 1}, {"teho", "theo", 1}, {"kitten", "sitting", 3}, {"", "abc", 3}} {
		if got := Distance(c.a, c.b); got != c.d {
			t.Errorf("Distance(%q, %q) = %d, want %d", c.a, c.b, got, c.d)
		}
	}
	if Quote([]Candidate{{Name: "A"}, {Name: "B"}, {Name: "C"}}) != "“A”, “B” or “C”" {
		t.Error("Quote")
	}
}
